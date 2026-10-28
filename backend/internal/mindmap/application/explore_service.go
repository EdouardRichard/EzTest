package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Exploration bounds and limits of spec §6.9/D36.
const (
	// exploreMaxRequests bounds one exploration run (spec §6.9 point 3).
	exploreMaxRequests = 40
	// exploreMaxDepth is the maximum link depth below the start page.
	exploreMaxDepth = 2
	// exploreMaxStartPaths bounds the operator-provided extra start paths.
	exploreMaxStartPaths = 10
	// exploreTotalTimeout is the wall-clock budget of one exploration run.
	exploreTotalTimeout = 60 * time.Second
)

// ErrProbeTargetNotAllowed reports a probe target rejected by the read-only URL
// rules (scheme, embedded credentials, host allowlist or private/reserved
// address). The httpclient adapter translates its own sentinels to this error so
// the service keeps its environment_not_allowed mapping without importing the
// adapter package (Agent.md §6.2).
var ErrProbeTargetNotAllowed = errors.New("mindmap: probe target is not allowed")

// exploreSkipMarkers are the link/action fragments of state-changing endpoints
// the read-only explorer must never call (spec §6.9 point 1).
var exploreSkipMarkers = []string{"logout", "signout", "delete", "remove", "unsubscribe"}

// ExploreDeps are the ports of the HTTP-only application exploration service.
type ExploreDeps struct {
	Versions VersionLifecycle
	Catalog  CatalogGateway
	Sessions SessionGateway
	// Prober performs the read-only HTTP probes. It is required: Start fails
	// with a configuration error when it is nil, so the service never falls
	// back to constructing an adapter client itself (Agent.md §6.2).
	Prober Prober
	Runs   RunRepository
	Events EventPublisher
	IDs    IDGenerator
	Clock  Clock
}

// ExploreCommand is the exploration part of the POST /runs body (spec §7.2
// endpoint 17). The system/environment ids are optional overrides that must
// match the version binding; StartPaths are extra same-allowlist start pages.
type ExploreCommand struct {
	SystemID      *int64   `json:"systemId,omitempty"`
	EnvironmentID *int64   `json:"environmentId,omitempty"`
	SessionID     *int64   `json:"sessionId,omitempty"`
	StartPaths    []string `json:"startPaths,omitempty"`
}

// exploreInput is the persisted run input document.
type exploreInput struct {
	SystemID      int64    `json:"systemId"`
	EnvironmentID int64    `json:"environmentId"`
	SessionID     *int64   `json:"sessionId,omitempty"`
	StartPaths    []string `json:"startPaths,omitempty"`
}

// ExploreStats is the run stats document of spec §6.9 point 6.
type ExploreStats struct {
	Discovered int  `json:"discovered"`
	Imported   int  `json:"imported"`
	Skipped    int  `json:"skipped"`
	Requests   int  `json:"requests"`
	Truncated  bool `json:"truncated"`
}

// ExploreResult is the projection the HTTP layer exposes for an exploration run
// (T16). The app model inputs themselves live in the resource catalog.
type ExploreResult struct {
	RunID      int64  `json:"runId"`
	VersionID  int64  `json:"versionId"`
	Status     string `json:"status"`
	Discovered int    `json:"discovered"`
	Imported   int    `json:"imported"`
	Skipped    int    `json:"skipped"`
	Requests   int    `json:"requests"`
	Truncated  bool   `json:"truncated"`
}

// ExploreService implements P5 application exploration with a deterministic,
// read-only BFS (spec §6.9/D36). It starts an explore run and, when run, probes
// the environment over GET only, imports the discovered app model nodes with
// source='explored' and records discovered/imported/skipped/truncated stats. No
// LLM is involved: the BFS path always finishes as done.
type ExploreService struct {
	deps ExploreDeps
}

// NewExploreService wires the exploration service to its ports.
func NewExploreService(deps ExploreDeps) *ExploreService {
	return &ExploreService{deps: deps}
}

// Start validates the version state and the system/environment binding, claims
// the per-kind single-flight slot and persists a queued explore run. It does not
// create an LLM session: the P5 default is the deterministic BFS.
func (s *ExploreService) Start(ctx context.Context, versionID, actor int64, cmd ExploreCommand) (domain.CaseDesignRun, error) {
	if s.deps.Versions == nil || s.deps.Runs == nil || s.deps.IDs == nil || s.deps.Catalog == nil {
		return domain.CaseDesignRun{}, errors.New("mindmap: exploration ports are not configured")
	}
	if s.deps.Prober == nil {
		return domain.CaseDesignRun{}, errors.New("mindmap: exploration prober is not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return domain.CaseDesignRun{}, err
	}
	if version.Status != versionStatusDesigning {
		return domain.CaseDesignRun{}, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s; an exploration run needs designing", versionID, version.Status))
	}
	if version.SystemID == nil || version.EnvironmentID == nil {
		return domain.CaseDesignRun{}, domain.NewConflict("exploration_requires_environment",
			fmt.Sprintf("version %d must be bound to a system and an environment", versionID))
	}
	if cmd.SystemID != nil && *cmd.SystemID != *version.SystemID {
		return domain.CaseDesignRun{}, domain.NewConflict("exploration_requires_environment",
			fmt.Sprintf("systemId %d does not match version %d system %d", *cmd.SystemID, versionID, *version.SystemID))
	}
	if cmd.EnvironmentID != nil && *cmd.EnvironmentID != *version.EnvironmentID {
		return domain.CaseDesignRun{}, domain.NewConflict("exploration_requires_environment",
			fmt.Sprintf("environmentId %d does not match version %d environment %d",
				*cmd.EnvironmentID, versionID, *version.EnvironmentID))
	}
	startPaths, err := normalizeExploreStartPaths(cmd.StartPaths)
	if err != nil {
		return domain.CaseDesignRun{}, err
	}
	if _, err := s.deps.Runs.ActiveByKind(ctx, versionID, domain.RunKindExplore); err == nil {
		return domain.CaseDesignRun{}, runInProgressError(versionID, domain.RunKindExplore)
	} else if !runIsNotFound(err) {
		return domain.CaseDesignRun{}, err
	}

	runID, err := s.nextID()
	if err != nil {
		return domain.CaseDesignRun{}, err
	}
	input, err := json.Marshal(exploreInput{
		SystemID:      *version.SystemID,
		EnvironmentID: *version.EnvironmentID,
		SessionID:     cmd.SessionID,
		StartPaths:    startPaths,
	})
	if err != nil {
		return domain.CaseDesignRun{}, fmt.Errorf("mindmap: encode explore input: %w", err)
	}
	now := s.now()
	run := domain.CaseDesignRun{
		ID:        runID,
		VersionID: versionID,
		Kind:      domain.RunKindExplore,
		Status:    domain.RunQueued,
		Input:     input,
		CreatedBy: actor,
		CreatedAt: now,
		UpdatedAt: now,
	}
	created, err := s.deps.Runs.Create(ctx, run)
	if err != nil {
		return created, err
	}
	s.publishRunEvent(ctx, created, domain.EventCaseRunStarted, "")
	return created, nil
}

// Run executes one queued explore run: it moves the run to running, performs the
// deterministic read-only BFS, imports the explored app model nodes and finishes
// the run as done (or failed with a stable reason). It never starts an agent run.
func (s *ExploreService) Run(ctx context.Context, run domain.CaseDesignRun) (ExploreResult, error) {
	if run.Kind != domain.RunKindExplore {
		return ExploreResult{}, domain.NewInvalid("kind", "invalid_run_kind",
			fmt.Sprintf("run %d kind %q is not explore", run.ID, run.Kind))
	}
	if s.deps.Runs == nil || s.deps.Catalog == nil || s.deps.Prober == nil {
		return ExploreResult{}, errors.New("mindmap: exploration ports are not configured")
	}
	switch run.Status {
	case domain.RunQueued:
		startedAt := s.now()
		updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, domain.RunQueued, domain.RunRunning, RunPatch{StartedAt: &startedAt})
		if err != nil {
			return ExploreResult{}, err
		}
		run = updated
	case domain.RunRunning:
	default:
		return ExploreResult{}, domain.NewStateConflict("run_state_conflict",
			fmt.Sprintf("run %d is %s and cannot be explored", run.ID, run.Status))
	}

	input, err := decodeExploreInput(run)
	if err != nil {
		return s.failRun(ctx, run, "invalid_explore_input", err)
	}
	result, stats, err := s.explore(ctx, input)
	if err != nil {
		return s.failRun(ctx, run, exploreFailureReason(err), err)
	}
	result.RunID = run.ID
	result.VersionID = run.VersionID
	result.Status = domain.RunDone
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return s.failRun(ctx, run, "internal_error", err)
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return s.failRun(ctx, run, "internal_error", err)
	}
	finished := s.now()
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, domain.RunRunning, domain.RunDone, RunPatch{
		Result:     resultJSON,
		Stats:      statsJSON,
		FinishedAt: &finished,
	})
	if err != nil {
		return ExploreResult{}, err
	}
	result.Status = updated.Status
	s.publishRunEvent(ctx, updated, domain.EventCaseRunFinished, "")
	return result, nil
}

// failRun records a terminal failure with the stable reason document and returns
// the original cause to the caller.
func (s *ExploreService) failRun(ctx context.Context, run domain.CaseDesignRun, reason string, cause error) (ExploreResult, error) {
	finished := s.now()
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, domain.RunFailed, RunPatch{
		Errors:     runFailureErrors(reason, nil),
		FinishedAt: &finished,
	})
	if err != nil {
		return ExploreResult{}, fmt.Errorf("mindmap: fail explore run %d (%s): %w", run.ID, reason, err)
	}
	s.publishRunEvent(ctx, updated, domain.EventCaseRunFinished, reason)
	return ExploreResult{RunID: run.ID, VersionID: run.VersionID, Status: updated.Status}, cause
}

// explore performs one BFS and the app model import.
func (s *ExploreService) explore(ctx context.Context, input exploreInput) (ExploreResult, ExploreStats, error) {
	environment, err := s.deps.Catalog.GetEnvironment(ctx, input.EnvironmentID)
	if err != nil {
		return ExploreResult{}, ExploreStats{}, err
	}
	if input.SystemID != 0 && environment.SystemID != 0 && input.SystemID != environment.SystemID {
		return ExploreResult{}, ExploreStats{}, domain.NewConflict("exploration_requires_environment",
			fmt.Sprintf("run system %d does not match environment %d system %d",
				input.SystemID, environment.ID, environment.SystemID))
	}
	systemID := environment.SystemID
	if systemID == 0 {
		systemID = input.SystemID
	}
	base, err := parseExploreBaseURL(environment.BaseURL)
	if err != nil {
		return ExploreResult{}, ExploreStats{}, err
	}
	allowedHosts := exploreAllowedHosts(base, environment.AllowedHosts)
	cookieHeader, err := s.exploreCookieHeader(ctx, input)
	if err != nil {
		return ExploreResult{}, ExploreStats{}, err
	}

	crawler := newExploreCrawl(s.deps.Prober, base, allowedHosts, cookieHeader)
	if err := crawler.seed(input.StartPaths); err != nil {
		return ExploreResult{}, ExploreStats{}, err
	}
	exploreCtx, cancel := context.WithTimeout(ctx, exploreTotalTimeout)
	defer cancel()
	if err := crawler.run(exploreCtx); err != nil {
		return ExploreResult{}, ExploreStats{}, err
	}

	stats := ExploreStats{
		Discovered: len(crawler.models),
		Requests:   crawler.requests,
		Truncated:  crawler.truncated,
	}
	existing, err := s.deps.Catalog.ListAppModels(ctx, systemID)
	if err != nil {
		return ExploreResult{}, ExploreStats{}, fmt.Errorf("mindmap: list app models: %w", err)
	}
	existingKeys := make(map[string]struct{}, len(existing))
	for _, node := range existing {
		existingKeys[node.Key] = struct{}{}
	}
	pending := make([]AppModelInput, 0, len(crawler.models))
	for _, model := range crawler.models {
		if _, ok := existingKeys[model.Key]; ok {
			stats.Skipped++
			continue
		}
		pending = append(pending, model)
	}
	if len(pending) > 0 {
		created, updated, err := s.deps.Catalog.ImportAppModels(ctx, systemID, pending)
		if err != nil {
			return ExploreResult{}, ExploreStats{}, fmt.Errorf("mindmap: import explored app models: %w", err)
		}
		stats.Imported = created + updated
	}
	return ExploreResult{
		Discovered: stats.Discovered,
		Imported:   stats.Imported,
		Skipped:    stats.Skipped,
		Requests:   stats.Requests,
		Truncated:  stats.Truncated,
	}, stats, nil
}

// exploreCookieHeader builds the Cookie header from the P2 session storage
// state. Only cookies are used (no localStorage and no token injection, spec
// §6.9 point 4); the session is never written back.
func (s *ExploreService) exploreCookieHeader(ctx context.Context, input exploreInput) (string, error) {
	if input.SessionID == nil {
		return "", nil
	}
	if s.deps.Sessions == nil {
		return "", errors.New("mindmap: session gateway is not configured")
	}
	session, err := s.deps.Sessions.GetForExplore(ctx, *input.SessionID)
	if err != nil {
		return "", err
	}
	if session.EnvironmentID != 0 && session.EnvironmentID != input.EnvironmentID {
		return "", domain.NewStateConflict("session_not_valid",
			fmt.Sprintf("session %d belongs to environment %d, not %d", session.ID, session.EnvironmentID, input.EnvironmentID))
	}
	if len(session.Cookies) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(session.Cookies))
	for name := range session.Cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+session.Cookies[name])
	}
	return strings.Join(parts, "; "), nil
}

// publishRunEvent emits one case.run_started/finished summary. A publish failure
// is logged and never returned (spec §6.11).
func (s *ExploreService) publishRunEvent(ctx context.Context, run domain.CaseDesignRun, eventType, reason string) {
	if s.deps.Events == nil {
		return
	}
	detail := map[string]any{
		"runId":  run.ID,
		"kind":   run.Kind,
		"status": run.Status,
	}
	summary := fmt.Sprintf("%s run %s", run.Kind, run.Status)
	if reason != "" {
		detail["reason"] = reason
		summary += ": " + reason
	}
	err := s.deps.Events.Publish(ctx, Event{
		VersionID: run.VersionID,
		Type:      eventType,
		Summary:   truncateRunes(summary, runMaxSummaryRunes),
		ActorID:   run.CreatedBy,
		Source:    domain.NodeSourceAIGenerated,
		Detail:    detail,
	})
	if err != nil {
		slog.WarnContext(ctx, "mindmap event publish failed", "type", eventType, "version_id", run.VersionID, "error", err)
	}
}

// nextID mints one application-generated Snowflake id (D29).
func (s *ExploreService) nextID() (int64, error) {
	if s.deps.IDs == nil {
		return 0, errors.New("mindmap: id generator is not configured")
	}
	id := s.deps.IDs.Next()
	if id <= 0 {
		return 0, errors.New("mindmap: id generator returned a non-positive id")
	}
	return id, nil
}

// now returns the injected clock time (falling back to the wall clock).
func (s *ExploreService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}

// decodeExploreInput decodes the persisted run input document.
func decodeExploreInput(run domain.CaseDesignRun) (exploreInput, error) {
	var input exploreInput
	if len(run.Input) == 0 {
		return input, domain.NewInvalid("input", "invalid_explore_input", "explore run has no input")
	}
	if err := json.Unmarshal(run.Input, &input); err != nil {
		return input, domain.NewInvalid("input", "invalid_explore_input",
			fmt.Sprintf("explore run %d input is not valid JSON", run.ID))
	}
	if input.EnvironmentID <= 0 || input.SystemID <= 0 {
		return input, domain.NewInvalid("input", "invalid_explore_input",
			"explore run input must carry systemId and environmentId")
	}
	return input, nil
}

// normalizeExploreStartPaths trims the extra start paths and enforces the ≤10
// bound of spec §6.9 point 5.
func normalizeExploreStartPaths(paths []string) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		value := strings.TrimSpace(path)
		if value == "" {
			continue
		}
		normalized = append(normalized, value)
	}
	if len(normalized) > exploreMaxStartPaths {
		return nil, domain.NewInvalid("startPaths", "invalid_start_paths",
			fmt.Sprintf("an exploration accepts at most %d start paths", exploreMaxStartPaths))
	}
	return normalized, nil
}

// parseExploreBaseURL validates and normalizes the environment base URL.
func parseExploreBaseURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, domain.NewConflict("exploration_requires_environment", "environment base URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, domain.NewConflict("environment_not_allowed", fmt.Sprintf("environment base URL %q is invalid", raw))
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, domain.NewConflict("environment_not_allowed",
			fmt.Sprintf("environment base URL %q must be http or https", raw))
	}
	if parsed.User != nil {
		return nil, domain.NewConflict("environment_not_allowed", "environment base URL must not embed credentials")
	}
	if parsed.Hostname() == "" {
		return nil, domain.NewConflict("environment_not_allowed", "environment base URL has no host")
	}
	parsed.Fragment = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed, nil
}

// exploreAllowedHosts builds the host allowlist: the environment base host plus
// the configured allowed_hosts.
func exploreAllowedHosts(base *url.URL, configured []string) []string {
	allowed := make([]string, 0, len(configured)+1)
	seen := make(map[string]struct{}, len(configured)+1)
	add := func(host string) {
		if host == "" {
			return
		}
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		allowed = append(allowed, host)
	}
	add(strings.ToLower(base.Hostname()))
	for _, raw := range configured {
		add(exploreNormalizeHost(raw))
	}
	return allowed
}

// exploreNormalizeHost reduces an allowed_hosts entry to a bare lowercase host.
func exploreNormalizeHost(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil {
			return strings.Trim(parsed.Hostname(), "[]")
		}
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(value, "[]")
}

// exploreHostAllowed reports whether target's host is in the allowlist.
func exploreHostAllowed(target *url.URL, allowed []string) bool {
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return false
	}
	for _, entry := range allowed {
		if entry == host {
			return true
		}
	}
	return false
}

// exploreTarget is one BFS frontier entry.
type exploreTarget struct {
	url   *url.URL
	depth int
}

// exploreCrawl is the deterministic BFS state of one exploration run.
type exploreCrawl struct {
	probe   Prober
	base    *url.URL
	allowed []string
	cookie  string

	queue     []exploreTarget
	seenURLs  map[string]struct{}
	seenKeys  map[string]struct{}
	models    []AppModelInput
	requests  int
	truncated bool
}

// newExploreCrawl builds one deterministic BFS walker.
func newExploreCrawl(probe Prober, base *url.URL, allowed []string, cookie string) *exploreCrawl {
	return &exploreCrawl{
		probe:    probe,
		base:     base,
		allowed:  allowed,
		cookie:   cookie,
		seenURLs: make(map[string]struct{}),
		seenKeys: make(map[string]struct{}),
	}
}

// seed queues the environment base URL and the operator-provided start paths.
// A start path outside the allowlist is environment_not_allowed.
func (c *exploreCrawl) seed(startPaths []string) error {
	c.enqueue(c.base, 0)
	for _, raw := range startPaths {
		target, err := resolveExploreURL(c.base, raw, c.allowed)
		if err != nil {
			return err
		}
		c.enqueue(target, 0)
	}
	return nil
}

// enqueue adds one target once.
func (c *exploreCrawl) enqueue(target *url.URL, depth int) {
	canonical := exploreCanonicalURL(target)
	if _, seen := c.seenURLs[canonical]; seen {
		return
	}
	c.seenURLs[canonical] = struct{}{}
	c.queue = append(c.queue, exploreTarget{url: target, depth: depth})
}

// run performs the BFS until the frontier is empty or the request/depth/time
// limits are hit.
func (c *exploreCrawl) run(ctx context.Context) error {
	for len(c.queue) > 0 {
		if ctx.Err() != nil {
			if err := ctx.Err(); errors.Is(err, context.Canceled) {
				return err
			}
			c.truncated = true
			break
		}
		if c.requests >= exploreMaxRequests {
			c.truncated = true
			break
		}
		target := c.queue[0]
		c.queue = c.queue[1:]
		c.requests++

		probe, err := c.probe.Probe(ctx, target.url.String(), c.cookie, c.allowed)
		if err != nil {
			if target.depth != 0 {
				continue
			}
			if errors.Is(err, ErrProbeTargetNotAllowed) {
				return domain.NewConflict("environment_not_allowed",
					fmt.Sprintf("environment URL %s is not allowed: %v", target.url, err))
			}
			return domain.NewConflict("environment_unreachable",
				fmt.Sprintf("environment URL %s is unreachable: %v", target.url, err))
		}
		if probe.Truncated {
			c.truncated = true
		}
		if probe.Status < 200 || probe.Status >= 300 {
			continue
		}
		final, err := url.Parse(probe.URL)
		if err != nil || final.Hostname() == "" {
			final = target.url
		}
		path := explorePath(final)
		if exploreIsAPIPath(path) && exploreLooksJSON(probe) {
			c.addModel("api", "api:GET "+path, path, path, "GET", nil)
			continue
		}
		c.addPage(path, probe.Title)
		c.addForms(final, probe.Forms)
		c.addLinks(final, target.depth, probe.Links)
	}
	return nil
}

// addPage records one fetched HTML page.
func (c *exploreCrawl) addPage(path, title string) {
	name := strings.TrimSpace(title)
	if name == "" {
		name = path
	}
	var meta map[string]string
	if title = strings.TrimSpace(title); title != "" {
		meta = map[string]string{"title": title}
	}
	c.addModel("page", "page:"+path, name, path, "", meta)
}

// addForms records one route per same-allowlist form action. The Prober port
// carries each form as "<METHOD> <action>" (see the Prober adapter's Probe).
func (c *exploreCrawl) addForms(page *url.URL, forms []string) {
	for _, form := range forms {
		method, action := splitExploreForm(form)
		if isSkippedExploreLink(action) {
			continue
		}
		actionURL := page
		if action != "" {
			resolved, err := resolveExploreURL(page, action, c.allowed)
			if err != nil {
				continue
			}
			actionURL = resolved
		}
		path := explorePath(actionURL)
		if method == "" {
			method = "GET"
		}
		c.addModel("route", "route:"+path, method+" "+path, path, "", nil)
	}
}

// splitExploreForm decodes one "<METHOD> <action>" Prober form entry. The method
// is uppercased; a missing method falls back to GET in addForms.
func splitExploreForm(raw string) (method, action string) {
	fields := strings.SplitN(strings.TrimSpace(raw), " ", 2)
	method = strings.ToUpper(fields[0])
	if len(fields) == 2 {
		action = strings.TrimSpace(fields[1])
	}
	return method, action
}

// addLinks resolves, filters and enqueues the same-allowlist page links.
func (c *exploreCrawl) addLinks(page *url.URL, depth int, links []string) {
	for _, raw := range links {
		if isSkippedExploreLink(raw) {
			continue
		}
		reference, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		scheme := strings.ToLower(reference.Scheme)
		if scheme != "" && scheme != "http" && scheme != "https" {
			continue
		}
		target := page.ResolveReference(reference)
		target.Fragment = ""
		if target.User != nil || !exploreHostAllowed(target, c.allowed) {
			continue
		}
		canonical := exploreCanonicalURL(target)
		if _, seen := c.seenURLs[canonical]; seen {
			continue
		}
		if depth >= exploreMaxDepth {
			c.truncated = true
			continue
		}
		c.enqueue(target, depth+1)
	}
}

// addModel appends one deduplicated app model candidate in discovery order.
func (c *exploreCrawl) addModel(kind, key, name, path, method string, meta map[string]string) {
	if path == "" || key == "" {
		return
	}
	if _, seen := c.seenKeys[key]; seen {
		return
	}
	c.seenKeys[key] = struct{}{}
	var raw json.RawMessage
	if len(meta) > 0 {
		if encoded, err := json.Marshal(meta); err == nil {
			raw = encoded
		}
	}
	c.models = append(c.models, AppModelInput{
		Key:    key,
		Kind:   kind,
		Name:   name,
		Path:   path,
		Method: method,
		Meta:   raw,
		Source: "explored",
	})
}

// resolveExploreURL resolves raw against base and applies the read-only URL
// rules. A disallowed start path is environment_not_allowed.
func resolveExploreURL(base *url.URL, raw string, allowed []string) (*url.URL, error) {
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, domain.NewConflict("environment_not_allowed", fmt.Sprintf("start path %q is invalid", raw))
	}
	target := base.ResolveReference(reference)
	target.Fragment = ""
	scheme := strings.ToLower(target.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, domain.NewConflict("environment_not_allowed",
			fmt.Sprintf("start path %q must resolve to http or https", raw))
	}
	if target.User != nil {
		return nil, domain.NewConflict("environment_not_allowed",
			fmt.Sprintf("start path %q must not embed credentials", raw))
	}
	if !exploreHostAllowed(target, allowed) {
		return nil, domain.NewConflict("environment_not_allowed",
			fmt.Sprintf("start path %q host %q is not in the environment allowlist", raw, target.Hostname()))
	}
	return target, nil
}

// explorePath returns the URL path used as the app model identity; a bare origin
// maps to "/".
func explorePath(target *url.URL) string {
	path := target.Path
	if path == "" {
		return "/"
	}
	return path
}

// exploreIsAPIPath reports whether path is under the JSON API namespace.
func exploreIsAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/")
}

// exploreLooksJSON reports whether a probe response is JSON.
func exploreLooksJSON(probe ProbeResult) bool {
	if strings.Contains(strings.ToLower(probe.ContentType), "json") {
		return true
	}
	return json.Valid([]byte(probe.Body))
}

// exploreCanonicalURL normalizes one URL for BFS deduplication.
func exploreCanonicalURL(target *url.URL) string {
	clone := *target
	clone.Scheme = strings.ToLower(clone.Scheme)
	clone.Host = strings.ToLower(clone.Host)
	clone.Fragment = ""
	clone.RawFragment = ""
	return clone.String()
}

// isSkippedExploreLink reports whether a link or form action contains one of the
// state-changing markers of spec §6.9 point 1.
func isSkippedExploreLink(raw string) bool {
	value := strings.ToLower(raw)
	for _, marker := range exploreSkipMarkers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// exploreFailureReason maps an exploration failure to its stable run reason.
func exploreFailureReason(err error) string {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) && domainErr.Reason != "" {
		return domainErr.Reason
	}
	return "internal_error"
}
