package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Execution settings keys (contracts/settings.md §5). The executor token is a
// secret setting; it is only hashed into executor_instance.token_hash and never
// logged or persisted in clear text.
const (
	SettingExecutorURL               = "execution.executor_url"
	SettingExecutorURLs              = "execution.executor_urls"
	SettingExecutorToken             = "execution.executor_token"
	SettingExecutorHeartbeatSeconds  = "execution.executor_heartbeat_seconds"
	SettingExecutorOfflineSeconds    = "execution.executor_offline_seconds"
	SettingExecutorContextTTLSeconds = "execution.executor_context_ttl_seconds"
)

// maxExecutorEndpointLength mirrors the internal register contract (§12.1).
const maxExecutorEndpointLength = 500

// RegistryPollInterval is the fixed pull interval used by cmd/eztest for
// Registry.PollOnce (spec §6.2: pull healthz every heartbeat, 1s scheduler tick).
const RegistryPollInterval = time.Second

// maxExecutorErrorLength bounds the recorded last_error string.
const maxExecutorErrorLength = 500

// ExecutorHealth is the neutral /healthz projection the registry consumes. It is
// implemented by the executor HTTP client wrapper so the application layer does
// not import the executor infrastructure.
type ExecutorHealth struct {
	Status         string
	Version        string
	Contexts       int
	MaxContexts    int
	BrowserVersion string
	Error          string
}

// ExecutorContextRef is one executor-side browser context as listed by
// GET /v1/contexts (orphan reconciliation / TTL cleanup).
type ExecutorContextRef struct {
	ID     string
	Key    string
	Status string
}

// ExecutorProbe is the executor health/inventory surface. The infra adapter
// binds the endpoint and token from settings.
type ExecutorProbe interface {
	Health(ctx context.Context) (ExecutorHealth, error)
	ListContexts(ctx context.Context) ([]ExecutorContextRef, error)
}

// ExecutorRegistration is the push register payload (executor-protocol §12.1).
type ExecutorRegistration struct {
	Endpoint       string
	Name           string
	Capacity       int
	ActiveContexts int
	Version        string
	// Status is accepted for contract symmetry; a register always results in
	// active (or keeps an existing draining row) and never degrades a row.
	Status string
}

// ExecutorHeartbeat is the push heartbeat payload (executor-protocol §12.1).
// Capacity/Version/Status are optional; zero/empty means "keep the stored
// value". Status accepts only active|degraded (empty means no health signal).
type ExecutorHeartbeat struct {
	Endpoint       string
	ActiveContexts int
	Capacity       int
	Version        string
	Status         string
}

// RegistryDeps wires the executor registry.
type RegistryDeps struct {
	Repo     ExecutorRepository
	Probe    ExecutorProbe
	Settings SettingsReader
	Clock    Clock
	IDs      IDGenerator
	// ProbeFactory builds a probe for one additional endpoint from
	// execution.executor_urls. When nil the additional endpoints are skipped
	// (P3 single-endpoint wiring stays valid).
	ProbeFactory func(endpoint string) ExecutorProbe
}

// Registry implements the P3 pull registration model (P3-D-01), extended in P7
// with the execution.executor_urls multi-endpoint poll and the push
// register/heartbeat endpoints (executor-protocol §12). It satisfies
// ExecutorRegistry for the scheduler and owns drain/activate.
type Registry struct {
	repo         ExecutorRepository
	probe        ExecutorProbe
	probeFactory func(endpoint string) ExecutorProbe
	settings     SettingsReader
	clock        Clock
	ids          IDGenerator
}

var _ ExecutorRegistry = (*Registry)(nil)

// NewRegistry wires the executor registry.
func NewRegistry(deps RegistryDeps) *Registry {
	return &Registry{
		repo:         deps.Repo,
		probe:        deps.Probe,
		probeFactory: deps.ProbeFactory,
		settings:     deps.Settings,
		clock:        deps.Clock,
		ids:          deps.IDs,
	}
}

// PollOnce probes every configured executor endpoint and refreshes its registry
// row (executor-protocol §12.2): execution.executor_url plus the comma-separated
// execution.executor_urls (trimmed, de-duplicated, invalid entries skipped).
//
//   - healthy (200 or degraded 503) → upsert endpoint row: token_hash,
//     capacity/active contexts/version/last_heartbeat; a draining row stays
//     draining, any other row becomes active.
//   - unreachable → an existing row records last_error and becomes offline once
//     now-last_heartbeat > execution.executor_offline_seconds; an unknown
//     endpoint is never registered (pull registration only).
//
// One endpoint failing never prevents the other endpoints from being polled.
func (r *Registry) PollOnce(ctx context.Context) error {
	if r.repo == nil || (r.probe == nil && r.probeFactory == nil) {
		return errors.New("execution registry is not configured")
	}
	primary, token, extras, err := r.executorConfig(ctx)
	if err != nil {
		return err
	}
	endpoints := make([]string, 0, 1+len(extras))
	if primary != "" {
		endpoints = append(endpoints, primary)
	}
	endpoints = append(endpoints, extras...)
	if len(endpoints) == 0 {
		// No executor configured: nothing to poll. The scheduler then has no
		// dispatchable executor and leaves case_runs pending.
		return nil
	}
	var pollErr error
	for index, endpoint := range endpoints {
		probe := r.probeForEndpoint(endpoint, index == 0 && primary != "")
		if probe == nil {
			continue
		}
		if err := r.pollEndpoint(ctx, endpoint, token, probe); err != nil {
			pollErr = errors.Join(pollErr, err)
		}
	}
	return pollErr
}

// probeForEndpoint returns the legacy probe for the primary endpoint and a
// factory-built probe for every additional endpoint.
func (r *Registry) probeForEndpoint(endpoint string, primary bool) ExecutorProbe {
	if primary && r.probe != nil {
		return r.probe
	}
	if r.probeFactory != nil {
		return r.probeFactory(endpoint)
	}
	return nil
}

// pollEndpoint probes one endpoint and upserts its row.
func (r *Registry) pollEndpoint(ctx context.Context, endpoint, token string, probe ExecutorProbe) error {
	now := r.now()
	health, probeErr := probe.Health(ctx)
	if probeErr != nil {
		return r.recordProbeFailure(ctx, endpoint, probeErr, now)
	}
	existing, getErr := r.repo.GetByEndpoint(ctx, endpoint)
	instance := execution.ExecutorInstance{}
	switch {
	case getErr == nil:
		instance = existing
	case errors.Is(getErr, execution.ErrNotFound):
		if r.ids == nil {
			return errors.New("execution registry has no ID generator")
		}
		instance.ID = r.ids.Next()
		instance.Status = execution.ExecutorActive
		instance.CreatedAt = now
		instance.Name = endpoint
	default:
		return getErr
	}

	instance.Endpoint = endpoint
	instance.TokenHash = hashExecutorToken(token)
	if health.MaxContexts > 0 {
		instance.Capacity = health.MaxContexts
	}
	instance.ActiveContexts = health.Contexts
	instance.Version = health.Version
	instance.LastHeartbeat = &now
	instance.UpdatedAt = now
	if health.Status != "" && health.Status != "ok" {
		instance.LastError = truncateExecutorError(health.Error)
	} else {
		instance.LastError = ""
	}
	// A degraded executor still heartbeats and stays active (degraded is an
	// alerting concern, not a P3 dispatch blocker, spec §6.2). Only an explicit
	// admin drain survives a successful poll.
	if instance.Status != execution.ExecutorDraining {
		instance.Status = execution.ExecutorActive
	}
	return r.repo.UpsertHeartbeat(ctx, instance)
}

// recordProbeFailure keeps the last known row, records the error and marks the
// executor offline after the offline window. An unknown endpoint is ignored.
func (r *Registry) recordProbeFailure(ctx context.Context, endpoint string, probeErr error, now time.Time) error {
	existing, getErr := r.repo.GetByEndpoint(ctx, endpoint)
	if errors.Is(getErr, execution.ErrNotFound) {
		return nil
	}
	if getErr != nil {
		return getErr
	}
	existing.LastError = truncateExecutorError(probeErr.Error())
	existing.UpdatedAt = now
	offlineAfter := r.offlineWindow(ctx)
	if existing.LastHeartbeat == nil || now.Sub(*existing.LastHeartbeat) > offlineAfter {
		existing.Status = execution.ExecutorOffline
	}
	return r.repo.UpsertHeartbeat(ctx, existing)
}

// Select returns the best dispatchable executor, or nil when none is
// available. With execution.executor_urls empty it keeps the P3 single-endpoint
// behavior (only the configured endpoint is eligible). In multi-endpoint mode it
// considers every active row with free context capacity, ordered by
// capacity DESC, last_heartbeat DESC, id ASC (executor-protocol §12.3).
func (r *Registry) Select(ctx context.Context) (*execution.ExecutorInstance, error) {
	if r.repo == nil {
		return nil, errors.New("execution registry is not configured")
	}
	endpoint, _, extras, err := r.executorConfig(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(extras) == 0 {
		// P3 single-endpoint selection is intentionally unchanged.
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		for i := range rows {
			row := rows[i]
			if !row.Status.Dispatchable() {
				continue
			}
			if endpoint != "" && normalizeExecutorEndpoint(row.Endpoint) != endpoint {
				continue
			}
			return &row, nil
		}
		return nil, nil
	}
	candidates := make([]execution.ExecutorInstance, 0, len(rows))
	for _, row := range rows {
		// capacity=0 can never be selected; a full executor is not dispatchable.
		if !row.Status.Dispatchable() || row.ActiveContexts >= row.Capacity {
			continue
		}
		candidates = append(candidates, row)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Capacity != right.Capacity {
			return left.Capacity > right.Capacity
		}
		switch {
		case left.LastHeartbeat == nil && right.LastHeartbeat != nil:
			return false
		case left.LastHeartbeat != nil && right.LastHeartbeat == nil:
			return true
		case left.LastHeartbeat != nil && right.LastHeartbeat != nil:
			if !left.LastHeartbeat.Equal(*right.LastHeartbeat) {
				return left.LastHeartbeat.After(*right.LastHeartbeat)
			}
		}
		return left.ID < right.ID
	})
	selected := candidates[0]
	return &selected, nil
}

// Drain stops new dispatches to an executor without touching in-flight runs.
func (r *Registry) Drain(ctx context.Context, id int64) (bool, error) {
	return r.setStatus(ctx, id, execution.ExecutorDraining)
}

// Activate returns a draining/offline executor to the dispatchable set.
func (r *Registry) Activate(ctx context.Context, id int64) (bool, error) {
	return r.setStatus(ctx, id, execution.ExecutorActive)
}

// List returns every registered executor row.
func (r *Registry) List(ctx context.Context) ([]execution.ExecutorInstance, error) {
	if r.repo == nil {
		return nil, errors.New("execution registry is not configured")
	}
	return r.repo.List(ctx)
}

// Get returns one registered executor by id (P8 per-executor routing). It
// returns execution.ErrNotFound for an unknown id.
func (r *Registry) Get(ctx context.Context, id int64) (execution.ExecutorInstance, error) {
	if r.repo == nil {
		return execution.ExecutorInstance{}, errors.New("execution registry is not configured")
	}
	return r.repo.Get(ctx, id)
}

func (r *Registry) setStatus(ctx context.Context, id int64, status execution.ExecutorStatus) (bool, error) {
	if r.repo == nil {
		return false, errors.New("execution registry is not configured")
	}
	if id <= 0 {
		return false, platform.Invalid("executor_id", "must be a positive decimal ID")
	}
	return r.repo.SetStatus(ctx, id, status)
}

// executorConfig reads the executor URL/token settings, normalizing the primary
// endpoint and parsing the additional endpoint list. Missing settings are
// treated as "not configured" (no error), so a fresh install without an executor
// does not log a poll failure every second.
func (r *Registry) executorConfig(ctx context.Context) (string, string, []string, error) {
	if r.settings == nil {
		return "", "", nil, errors.New("execution registry has no settings reader")
	}
	endpoint := ""
	endpointValue, err := r.settings.String(ctx, SettingExecutorURL)
	if err != nil {
		var apiErr *platform.Error
		if !(errors.As(err, &apiErr) && apiErr.Status == 404) {
			return "", "", nil, err
		}
	} else {
		endpoint = normalizeExecutorEndpoint(endpointValue)
	}
	token := ""
	if value, tokenErr := r.settings.String(ctx, SettingExecutorToken); tokenErr == nil {
		token = value
	}
	// A missing optional setting (older database, test fake) means single
	// endpoint mode; the P3 behavior is preserved.
	additional := []string{}
	if raw, urlsErr := r.settings.String(ctx, SettingExecutorURLs); urlsErr == nil {
		additional = parseExecutorEndpoints(raw, endpoint)
	}
	return endpoint, token, additional, nil
}

// parseExecutorEndpoints trims, validates and de-duplicates the comma-separated
// execution.executor_urls value. Empty and invalid entries are skipped; the
// primary endpoint is never repeated (executor-protocol §12.2).
func parseExecutorEndpoints(raw, primary string) []string {
	seen := map[string]struct{}{}
	if primary != "" {
		seen[primary] = struct{}{}
	}
	additional := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		endpoint := normalizeExecutorEndpoint(item)
		if endpoint == "" || len(endpoint) > maxExecutorEndpointLength {
			continue
		}
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		if !validExecutorEndpoint(endpoint) {
			continue
		}
		seen[endpoint] = struct{}{}
		additional = append(additional, endpoint)
	}
	return additional
}

// validExecutorEndpoint reports whether endpoint is an absolute http(s) URL.
func validExecutorEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return parsed.Host != ""
}

// RegisterPush upserts an executor registration delivered over the internal
// push endpoint (executor-protocol §12.1). A draining row stays draining; every
// other status becomes active. Repeated registers are idempotent.
func (r *Registry) RegisterPush(ctx context.Context, token string, in ExecutorRegistration) (execution.ExecutorInstance, error) {
	if r.repo == nil {
		return execution.ExecutorInstance{}, errors.New("execution registry is not configured")
	}
	endpoint, err := validateExecutorRegistration(in)
	if err != nil {
		return execution.ExecutorInstance{}, err
	}
	now := r.now()
	existing, getErr := r.repo.GetByEndpoint(ctx, endpoint)
	instance := execution.ExecutorInstance{}
	switch {
	case getErr == nil:
		instance = existing
	case errors.Is(getErr, execution.ErrNotFound):
		if r.ids == nil {
			return execution.ExecutorInstance{}, errors.New("execution registry has no ID generator")
		}
		instance.ID = r.ids.Next()
		instance.Status = execution.ExecutorActive
		instance.CreatedAt = now
	default:
		return execution.ExecutorInstance{}, getErr
	}
	instance.Endpoint = endpoint
	instance.Name = strings.TrimSpace(in.Name)
	instance.TokenHash = hashExecutorToken(token)
	instance.Capacity = in.Capacity
	instance.ActiveContexts = in.ActiveContexts
	instance.Version = strings.TrimSpace(in.Version)
	instance.LastHeartbeat = &now
	instance.UpdatedAt = now
	instance.LastError = ""
	if instance.Status != execution.ExecutorDraining {
		instance.Status = execution.ExecutorActive
	}
	if err := r.repo.UpsertHeartbeat(ctx, instance); err != nil {
		return execution.ExecutorInstance{}, err
	}
	if stored, err := r.repo.GetByEndpoint(ctx, endpoint); err == nil {
		return stored, nil
	}
	return instance, nil
}

// HeartbeatPush applies an executor heartbeat delivered over the internal push
// endpoint (executor-protocol §12.1). The endpoint must already be registered:
// an unknown endpoint returns execution.ErrNotFound and never creates a row.
func (r *Registry) HeartbeatPush(ctx context.Context, in ExecutorHeartbeat) (execution.ExecutorInstance, error) {
	if r.repo == nil {
		return execution.ExecutorInstance{}, errors.New("execution registry is not configured")
	}
	endpoint, err := validateExecutorHeartbeat(in)
	if err != nil {
		return execution.ExecutorInstance{}, err
	}
	instance, getErr := r.repo.GetByEndpoint(ctx, endpoint)
	if errors.Is(getErr, execution.ErrNotFound) {
		return execution.ExecutorInstance{}, execution.ErrNotFound
	}
	if getErr != nil {
		return execution.ExecutorInstance{}, getErr
	}
	now := r.now()
	instance.ActiveContexts = in.ActiveContexts
	if in.Capacity > 0 {
		instance.Capacity = in.Capacity
	}
	if version := strings.TrimSpace(in.Version); version != "" {
		instance.Version = version
	}
	// degraded is an alerting marker only; it never removes a row from the
	// dispatchable set. An active (or silent) heartbeat clears it.
	if in.Status == "degraded" {
		instance.LastError = "degraded"
	} else {
		instance.LastError = ""
	}
	instance.LastHeartbeat = &now
	instance.UpdatedAt = now
	if instance.Status != execution.ExecutorDraining {
		// A heartbeat recovers an offline executor (P3 pull semantics).
		instance.Status = execution.ExecutorActive
	}
	if err := r.repo.UpsertHeartbeat(ctx, instance); err != nil {
		return execution.ExecutorInstance{}, err
	}
	if stored, err := r.repo.GetByEndpoint(ctx, endpoint); err == nil {
		return stored, nil
	}
	return instance, nil
}

// validateExecutorRegistration validates and normalizes a push register payload.
func validateExecutorRegistration(in ExecutorRegistration) (string, error) {
	endpoint, err := validatePushEndpoint(in.Endpoint)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", platform.Invalid("name", "name is required")
	}
	if len(name) > 200 {
		return "", platform.Invalid("name", "name must be at most 200 characters")
	}
	if in.Capacity < 1 {
		return "", platform.Invalid("capacity", "capacity must be at least 1")
	}
	if in.ActiveContexts < 0 {
		return "", platform.Invalid("active_contexts", "active_contexts must be at least 0")
	}
	version := strings.TrimSpace(in.Version)
	if version == "" {
		return "", platform.Invalid("version", "version is required")
	}
	if len(version) > 64 {
		return "", platform.Invalid("version", "version must be at most 64 characters")
	}
	if in.Status != "" && in.Status != string(execution.ExecutorActive) && in.Status != "degraded" {
		return "", platform.Invalid("status", "status must be active or degraded")
	}
	return endpoint, nil
}

// validateExecutorHeartbeat validates and normalizes a push heartbeat payload.
func validateExecutorHeartbeat(in ExecutorHeartbeat) (string, error) {
	endpoint, err := validatePushEndpoint(in.Endpoint)
	if err != nil {
		return "", err
	}
	if in.ActiveContexts < 0 {
		return "", platform.Invalid("active_contexts", "active_contexts must be at least 0")
	}
	if in.Capacity < 0 {
		return "", platform.Invalid("capacity", "capacity must be at least 0")
	}
	if len(strings.TrimSpace(in.Version)) > 64 {
		return "", platform.Invalid("version", "version must be at most 64 characters")
	}
	if in.Status != "" && in.Status != string(execution.ExecutorActive) && in.Status != "degraded" {
		return "", platform.Invalid("status", "status must be active or degraded")
	}
	return endpoint, nil
}

// validatePushEndpoint normalizes and validates an internal push endpoint.
func validatePushEndpoint(raw string) (string, error) {
	endpoint := normalizeExecutorEndpoint(raw)
	if endpoint == "" {
		return "", platform.Invalid("endpoint", "endpoint is required")
	}
	if len(endpoint) > maxExecutorEndpointLength {
		return "", platform.Invalid("endpoint", "endpoint must be at most 500 characters")
	}
	if !validExecutorEndpoint(endpoint) {
		return "", platform.Invalid("endpoint", "endpoint must be an absolute http(s) URL")
	}
	return endpoint, nil
}

func (r *Registry) offlineWindow(ctx context.Context) time.Duration {
	seconds := int64(execution.DefaultExecutorOfflineSeconds)
	if r.settings != nil {
		if value, err := r.settings.Int(ctx, SettingExecutorOfflineSeconds); err == nil && value > 0 {
			seconds = value
		}
	}
	return time.Duration(seconds) * time.Second
}

func (r *Registry) now() time.Time {
	if r.clock != nil {
		return r.clock.Now()
	}
	return time.Now()
}

// normalizeExecutorEndpoint trims trailing slashes and whitespace so the
// registered row and the configured URL compare consistently.
func normalizeExecutorEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}

// hashExecutorToken returns the sha256 hex digest stored in token_hash. The clear
// token is never persisted or logged.
func hashExecutorToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func truncateExecutorError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxExecutorErrorLength {
		return message
	}
	return message[:maxExecutorErrorLength]
}
