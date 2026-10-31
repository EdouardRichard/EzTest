package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	SettingExecutorToken             = "execution.executor_token"
	SettingExecutorHeartbeatSeconds  = "execution.executor_heartbeat_seconds"
	SettingExecutorOfflineSeconds    = "execution.executor_offline_seconds"
	SettingExecutorContextTTLSeconds = "execution.executor_context_ttl_seconds"
)

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

// RegistryDeps wires the executor registry.
type RegistryDeps struct {
	Repo     ExecutorRepository
	Probe    ExecutorProbe
	Settings SettingsReader
	Clock    Clock
	IDs      IDGenerator
}

// Registry implements the P3 pull registration model (P3-D-01): every poll
// probes the configured executor healthz and upserts its executor_instance row.
// It satisfies ExecutorRegistry for the scheduler and owns drain/activate.
type Registry struct {
	repo     ExecutorRepository
	probe    ExecutorProbe
	settings SettingsReader
	clock    Clock
	ids      IDGenerator
}

var _ ExecutorRegistry = (*Registry)(nil)

// NewRegistry wires the executor registry.
func NewRegistry(deps RegistryDeps) *Registry {
	return &Registry{
		repo:     deps.Repo,
		probe:    deps.Probe,
		settings: deps.Settings,
		clock:    deps.Clock,
		ids:      deps.IDs,
	}
}

// PollOnce probes the executor and refreshes its registry row.
//
//   - healthy (200 or degraded 503) → upsert endpoint row: token_hash,
//     capacity/active contexts/version/last_heartbeat; a draining row stays
//     draining, any other row becomes active.
//   - unreachable → an existing row records last_error and becomes offline once
//     now-last_heartbeat > execution.executor_offline_seconds; an unknown
//     endpoint is never registered (P3-D-01 pull registration only).
func (r *Registry) PollOnce(ctx context.Context) error {
	if r.repo == nil || r.probe == nil {
		return errors.New("execution registry is not configured")
	}
	endpoint, token, err := r.executorConfig(ctx)
	if err != nil {
		return err
	}
	if endpoint == "" {
		// No executor configured: nothing to poll. The scheduler then has no
		// dispatchable executor and leaves case_runs pending.
		return nil
	}
	now := r.now()
	health, probeErr := r.probe.Health(ctx)
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

// Select returns the active executor whose endpoint matches the configured
// executor URL, or nil when none is dispatchable.
func (r *Registry) Select(ctx context.Context) (*execution.ExecutorInstance, error) {
	if r.repo == nil {
		return nil, errors.New("execution registry is not configured")
	}
	endpoint, _, err := r.executorConfig(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.repo.List(ctx)
	if err != nil {
		return nil, err
	}
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

func (r *Registry) setStatus(ctx context.Context, id int64, status execution.ExecutorStatus) (bool, error) {
	if r.repo == nil {
		return false, errors.New("execution registry is not configured")
	}
	if id <= 0 {
		return false, platform.Invalid("executor_id", "must be a positive decimal ID")
	}
	return r.repo.SetStatus(ctx, id, status)
}

// executorConfig reads the executor URL/token settings, normalizing the endpoint.
// Missing settings are treated as "not configured" (no error), so a fresh
// install without an executor does not log a poll failure every second.
func (r *Registry) executorConfig(ctx context.Context) (string, string, error) {
	if r.settings == nil {
		return "", "", errors.New("execution registry has no settings reader")
	}
	endpoint, err := r.settings.String(ctx, SettingExecutorURL)
	if err != nil {
		var apiErr *platform.Error
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return "", "", nil
		}
		return "", "", err
	}
	token := ""
	if value, tokenErr := r.settings.String(ctx, SettingExecutorToken); tokenErr == nil {
		token = value
	}
	return normalizeExecutorEndpoint(endpoint), token, nil
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
