package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

const (
	exploreVersionID = int64(77)
	// exploreBaseURL is the fake application origin of the exploration tests.
	exploreBaseURL = "http://app.test"
)

// exploreHits counts fake probe calls per path and keeps the last cookie and
// host allowlist each call was made with.
type exploreHits struct {
	mu         sync.Mutex
	counts     map[string]int
	cookies    map[string]string
	allowlists map[string][]string
}

func newExploreHits() *exploreHits {
	return &exploreHits{
		counts:     make(map[string]int),
		cookies:    make(map[string]string),
		allowlists: make(map[string][]string),
	}
}

func (h *exploreHits) add(path, cookie string, allowedHosts []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counts[path]++
	h.cookies[path] = cookie
	h.allowlists[path] = append([]string(nil), allowedHosts...)
}

func (h *exploreHits) allowlist(path string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.allowlists[path]...)
}

func (h *exploreHits) get(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counts[path]
}

func (h *exploreHits) cookie(path string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cookies[path]
}

func (h *exploreHits) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, count := range h.counts {
		total += count
	}
	return total
}

// fakeProber is an in-memory application.Prober: handler returns the canned
// result of one path, so the application tests never open a connection.
type fakeProber struct {
	hits    *exploreHits
	handler func(path, rawURL, cookieHeader string) (application.ProbeResult, error)
}

func (p *fakeProber) Probe(_ context.Context, rawURL, cookieHeader string, allowedHosts []string) (application.ProbeResult, error) {
	path := "/"
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Path != "" {
		path = parsed.Path
	}
	if p.hits != nil {
		p.hits.add(path, cookieHeader, allowedHosts)
	}
	result, err := p.handler(path, rawURL, cookieHeader)
	if err != nil {
		return application.ProbeResult{}, err
	}
	if result.URL == "" {
		result.URL = rawURL
	}
	if result.Status == 0 {
		result.Status = 200
	}
	return result, nil
}

// failingProber is the default fixture prober: it fails the run if reached.
type failingProber struct{}

func (failingProber) Probe(context.Context, string, string, []string) (application.ProbeResult, error) {
	return application.ProbeResult{}, errors.New("unexpected probe")
}

// exploreFixture is an in-memory implementation of every ExploreService port.
type exploreFixture struct {
	version     application.VersionSnapshot
	environment application.EnvironmentSnapshot

	environmentErr error
	session        *application.SessionSnapshot
	sessionErr     error
	prober         application.Prober

	existing      []application.AppModelSnapshot
	imported      []application.AppModelInput
	importCreated int
	importUpdated int

	runs   map[int64]domain.CaseDesignRun
	events []application.Event

	now    time.Time
	nextID int64
}

// newExploreFixture builds a designing version bound to system 5 and environment
// 9, whose base URL is baseURL.
func newExploreFixture(baseURL string) *exploreFixture {
	systemID, environmentID := int64(5), int64(9)
	return &exploreFixture{
		version: application.VersionSnapshot{
			ID:            exploreVersionID,
			Name:          "P5 exploration",
			Status:        "designing",
			SystemID:      &systemID,
			EnvironmentID: &environmentID,
		},
		environment: application.EnvironmentSnapshot{
			ID:           environmentID,
			SystemID:     systemID,
			Name:         "fixture",
			BaseURL:      baseURL,
			Kind:         "test",
			AllowedHosts: []string{"127.0.0.1"},
		},
		prober: failingProber{},
		runs:   make(map[int64]domain.CaseDesignRun),
		now:    time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC),
		nextID: 9000,
	}
}

// service wires the fixture into the production constructor.
func (f *exploreFixture) service() *application.ExploreService {
	return application.NewExploreService(application.ExploreDeps{
		Versions: f,
		Catalog:  f,
		Sessions: f,
		Prober:   f.prober,
		Runs:     f,
		Events:   f,
		IDs:      f,
		Clock:    f,
	})
}

func (f *exploreFixture) GetVersion(_ context.Context, versionID int64) (application.VersionSnapshot, error) {
	if versionID != f.version.ID {
		return application.VersionSnapshot{}, domain.NewNotFound("version", versionID)
	}
	return f.version, nil
}

func (f *exploreFixture) EnterReview(_ context.Context, versionID, _ int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, fmt.Errorf("EnterReview(%d) is not used", versionID)
}

func (f *exploreFixture) RejectReview(_ context.Context, versionID, _ int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, fmt.Errorf("RejectReview(%d) is not used", versionID)
}

func (f *exploreFixture) ApproveBaseline(_ context.Context, versionID, _ int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, fmt.Errorf("ApproveBaseline(%d) is not used", versionID)
}

func (f *exploreFixture) GetEnvironment(_ context.Context, envID int64) (application.EnvironmentSnapshot, error) {
	if f.environmentErr != nil {
		return application.EnvironmentSnapshot{}, f.environmentErr
	}
	if envID != f.environment.ID {
		return application.EnvironmentSnapshot{}, domain.NewNotFound("environment", envID)
	}
	return f.environment, nil
}

func (f *exploreFixture) ListAppModels(_ context.Context, _ int64) ([]application.AppModelSnapshot, error) {
	return append([]application.AppModelSnapshot(nil), f.existing...), nil
}

func (f *exploreFixture) ImportAppModels(_ context.Context, _ int64, inputs []application.AppModelInput) (int, int, error) {
	for _, input := range inputs {
		if input.Source != "explored" {
			return 0, 0, fmt.Errorf("imported %q with source %q, want explored", input.Key, input.Source)
		}
	}
	f.imported = append(f.imported, inputs...)
	f.importCreated += len(inputs)
	f.importUpdated += 0
	return len(inputs), 0, nil
}

func (f *exploreFixture) GetForExplore(_ context.Context, sessionID int64) (application.SessionSnapshot, error) {
	if f.sessionErr != nil {
		return application.SessionSnapshot{}, f.sessionErr
	}
	if f.session == nil {
		return application.SessionSnapshot{}, domain.NewNotFound("session", sessionID)
	}
	return *f.session, nil
}

func (f *exploreFixture) Create(_ context.Context, run domain.CaseDesignRun) (domain.CaseDesignRun, error) {
	if _, exists := f.runs[run.ID]; exists {
		return domain.CaseDesignRun{}, domain.NewConflict("duplicate_key", "run already exists")
	}
	f.runs[run.ID] = run
	return run, nil
}

func (f *exploreFixture) Get(_ context.Context, versionID, runID int64) (domain.CaseDesignRun, error) {
	run, ok := f.runs[runID]
	if !ok || run.VersionID != versionID {
		return domain.CaseDesignRun{}, domain.NewNotFound("run", runID)
	}
	return run, nil
}

func (f *exploreFixture) List(_ context.Context, _ int64, _, _ string, _ int, _ string) ([]domain.CaseDesignRun, string, bool, error) {
	runs := make([]domain.CaseDesignRun, 0, len(f.runs))
	for _, run := range f.runs {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, "", false, nil
}

func (f *exploreFixture) UpdateStatus(_ context.Context, runID int64, from, to string, patch application.RunPatch) (domain.CaseDesignRun, error) {
	run, ok := f.runs[runID]
	if !ok {
		return domain.CaseDesignRun{}, domain.NewNotFound("run", runID)
	}
	if run.Status != from {
		return domain.CaseDesignRun{}, domain.NewStateConflict("run_state_conflict",
			fmt.Sprintf("run %d is %s, not %s", runID, run.Status, from))
	}
	if patch.Result != nil {
		run.Result = patch.Result
	}
	if patch.Stats != nil {
		run.Stats = patch.Stats
	}
	if patch.Errors != nil {
		run.Errors = patch.Errors
	}
	if patch.StartedAt != nil {
		run.StartedAt = patch.StartedAt
	}
	if patch.FinishedAt != nil {
		run.FinishedAt = patch.FinishedAt
	}
	run.Status = to
	run.UpdatedAt = f.now
	f.runs[runID] = run
	return run, nil
}

func (f *exploreFixture) LatestByKind(_ context.Context, versionID int64, kind string) (domain.CaseDesignRun, error) {
	var latest domain.CaseDesignRun
	for _, run := range f.runs {
		if run.VersionID == versionID && run.Kind == kind && run.ID > latest.ID {
			latest = run
		}
	}
	if latest.ID == 0 {
		return domain.CaseDesignRun{}, domain.NewNotFound("run", versionID)
	}
	return latest, nil
}

func (f *exploreFixture) ActiveByKind(_ context.Context, versionID int64, kind string) (domain.CaseDesignRun, error) {
	for _, run := range f.runs {
		if run.VersionID == versionID && run.Kind == kind && run.IsActive() {
			return run, nil
		}
	}
	return domain.CaseDesignRun{}, domain.NewNotFound("run", versionID)
}

func (f *exploreFixture) ListActive(_ context.Context, limit int) ([]domain.CaseDesignRun, error) {
	runs := make([]domain.CaseDesignRun, 0)
	for _, run := range f.runs {
		if run.IsActive() {
			runs = append(runs, run)
		}
	}
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (f *exploreFixture) RecoverStale(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func (f *exploreFixture) Publish(_ context.Context, event application.Event) error {
	f.events = append(f.events, event)
	return nil
}

func (f *exploreFixture) Next() int64 {
	f.nextID++
	return f.nextID
}

func (f *exploreFixture) Now() time.Time {
	return f.now
}

// exploreErrorReason extracts the stable domain reason of err.
func exploreErrorReason(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("error is nil, want a domain error")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %v is not a domain error", err)
	}
	return domainErr.Reason
}

// exploreImportedKeys indexes the imported app model inputs by key.
func exploreImportedKeys(inputs []application.AppModelInput) map[string]application.AppModelInput {
	byKey := make(map[string]application.AppModelInput, len(inputs))
	for _, input := range inputs {
		byKey[input.Key] = input
	}
	return byKey
}

// exploreRequireKey asserts that key was imported with the expected kind.
func exploreRequireKey(t *testing.T, inputs []application.AppModelInput, key, kind string) application.AppModelInput {
	t.Helper()
	input, ok := exploreImportedKeys(inputs)[key]
	if !ok {
		t.Fatalf("key %q was not imported; imported keys = %v", key, exploreSortedKeys(inputs))
	}
	if input.Kind != kind {
		t.Fatalf("key %q kind = %q, want %q", key, input.Kind, kind)
	}
	if input.Source != "explored" {
		t.Fatalf("key %q source = %q, want explored", key, input.Source)
	}
	return input
}

// exploreSortedKeys returns the imported keys sorted for readable failures.
func exploreSortedKeys(inputs []application.AppModelInput) []string {
	keys := make([]string, 0, len(inputs))
	for _, input := range inputs {
		keys = append(keys, input.Key)
	}
	sort.Strings(keys)
	return keys
}

// exploreHasEvent reports whether the fixture published eventType.
func exploreHasEvent(fixture *exploreFixture, eventType string) bool {
	for _, event := range fixture.events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

// newExploreSite is the deterministic fake application: a home page with
// same-origin links, a form and a script, an orders section linking a JSON API,
// an admin page guarded by the session cookie and a login redirect.
func newExploreSite() (*fakeProber, *exploreHits) {
	hits := newExploreHits()
	loginPage := application.ProbeResult{
		ContentType: "text/html",
		Title:       "Fixture Login",
		Forms:       []string{"POST /login"},
	}
	pages := map[string]application.ProbeResult{
		"/": {
			URL:         exploreBaseURL + "/p5/home",
			ContentType: "text/html; charset=utf-8",
			Title:       "P5 Home",
			Links:       []string{"/p5/orders", "/p5/orders/1", "/p5/admin", "/logout", "/p5/delete/1"},
			Forms:       []string{"GET /p5/orders"},
			Scripts:     []string{"/static/app.js"},
		},
		"/p5/orders": {
			ContentType: "text/html",
			Title:       "P5 Orders",
			Links:       []string{"/p5/home", "/p5/orders/1", "/api/orders"},
		},
		"/p5/orders/1": {
			ContentType: "text/html",
			Title:       "P5 Order 1",
			Links:       []string{"/p5/orders"},
		},
		// The fake folds the /p5/admin -> /login same-host redirect into the
		// final login response, exactly like the real probe client does.
		"/p5/admin": {
			URL:         exploreBaseURL + "/login",
			ContentType: loginPage.ContentType,
			Title:       loginPage.Title,
			Forms:       loginPage.Forms,
		},
		"/login": loginPage,
		"/api/orders": {
			ContentType: "application/json",
			Body:        `{"orders":[]}`,
		},
	}
	prober := &fakeProber{hits: hits, handler: func(path, _, _ string) (application.ProbeResult, error) {
		page, ok := pages[path]
		if !ok {
			return application.ProbeResult{Status: 404}, nil
		}
		return page, nil
	}}
	return prober, hits
}

func TestExploreStartCreatesQueuedRun(t *testing.T) {
	fixture := newExploreFixture("http://127.0.0.1:1")
	service := fixture.service()

	run, err := service.Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Kind != domain.RunKindExplore || run.Status != domain.RunQueued {
		t.Fatalf("run = kind %q status %q, want explore/queued", run.Kind, run.Status)
	}
	if run.CreatedBy != 7 || run.VersionID != exploreVersionID {
		t.Fatalf("run owner/version = %d/%d", run.CreatedBy, run.VersionID)
	}
	if run.AgentSessionID != nil {
		t.Fatal("explore Start must not create an LLM session")
	}
	var input struct {
		SystemID      int64  `json:"systemId"`
		EnvironmentID int64  `json:"environmentId"`
		SessionID     *int64 `json:"sessionId"`
	}
	if err := json.Unmarshal(run.Input, &input); err != nil {
		t.Fatalf("decode run input: %v", err)
	}
	if input.SystemID != 5 || input.EnvironmentID != 9 {
		t.Fatalf("run input = %+v, want system 5 environment 9", input)
	}
	if !exploreHasEvent(fixture, domain.EventCaseRunStarted) {
		t.Fatalf("events = %#v, want case.run_started", fixture.events)
	}
}

func TestExploreStartRequiresProber(t *testing.T) {
	fixture := newExploreFixture("http://127.0.0.1:1")
	fixture.prober = nil

	_, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err == nil {
		t.Fatal("Start succeeded without a prober, want a configuration error")
	}
	if len(fixture.runs) != 0 {
		t.Fatal("a run was created without a prober")
	}
}

func TestExploreStartRequiresDesigningVersion(t *testing.T) {
	fixture := newExploreFixture("http://127.0.0.1:1")
	fixture.version.Status = "reviewing"

	_, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if reason := exploreErrorReason(t, err); reason != "version_state_conflict" {
		t.Fatalf("reason = %q, want version_state_conflict", reason)
	}
	if len(fixture.runs) != 0 {
		t.Fatal("a run was created for a non-designing version")
	}
}

func TestExploreStartRequiresEnvironmentBinding(t *testing.T) {
	fixture := newExploreFixture("http://127.0.0.1:1")
	fixture.version.EnvironmentID = nil

	_, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if reason := exploreErrorReason(t, err); reason != "exploration_requires_environment" {
		t.Fatalf("reason = %q, want exploration_requires_environment", reason)
	}
}

func TestExploreStartRejectsConcurrentRun(t *testing.T) {
	fixture := newExploreFixture("http://127.0.0.1:1")
	fixture.runs[1] = domain.CaseDesignRun{
		ID: 1, VersionID: exploreVersionID, Kind: domain.RunKindExplore, Status: domain.RunQueued,
	}

	_, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if reason := exploreErrorReason(t, err); reason != "run_in_progress" {
		t.Fatalf("reason = %q, want run_in_progress", reason)
	}
}

func TestExploreStartRejectsTooManyStartPaths(t *testing.T) {
	fixture := newExploreFixture("http://127.0.0.1:1")
	paths := make([]string, 11)
	for index := range paths {
		paths[index] = fmt.Sprintf("/p%d", index)
	}

	_, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{StartPaths: paths})
	if err == nil {
		t.Fatal("Start accepted more than 10 start paths")
	}
}

func TestExploreRunImportsExploredAppModels(t *testing.T) {
	prober, hits := newExploreSite()
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober
	service := fixture.service()

	run, err := service.Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := service.Run(context.Background(), run)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != domain.RunDone {
		t.Fatalf("result status = %q, want done (the deterministic BFS is not degraded)", result.Status)
	}
	if result.Truncated {
		t.Fatal("unexpected truncation for the small fixture site")
	}
	if result.Requests == 0 || result.Requests > 40 {
		t.Fatalf("requests = %d, want 1..40", result.Requests)
	}
	if result.Discovered != len(fixture.imported) {
		t.Fatalf("discovered = %d, imported = %d", result.Discovered, len(fixture.imported))
	}
	if result.Imported != len(fixture.imported) || result.Skipped != 0 {
		t.Fatalf("result = %+v, imported inputs = %d", result, len(fixture.imported))
	}
	// The fake site reproduces the pre-port fixture graph exactly: 6 probes
	// (/ -> /p5/home counts once, and /p5/home is probed again at depth 2),
	// 7 discovered/imported keys.
	if result.Discovered != 7 || result.Imported != 7 || result.Requests != 6 ||
		result.Skipped != 0 || result.Truncated {
		t.Fatalf("stats = discovered %d imported %d requests %d skipped %d truncated %t, want 7/7/6/0/false",
			result.Discovered, result.Imported, result.Requests, result.Skipped, result.Truncated)
	}

	exploreRequireKey(t, fixture.imported, "page:/p5/home", "page")
	exploreRequireKey(t, fixture.imported, "page:/p5/orders", "page")
	exploreRequireKey(t, fixture.imported, "page:/p5/orders/1", "page")
	exploreRequireKey(t, fixture.imported, "page:/login", "page")
	exploreRequireKey(t, fixture.imported, "route:/p5/orders", "route")
	exploreRequireKey(t, fixture.imported, "api:GET /api/orders", "api")
	for _, forbidden := range []string{"page:/p5/admin", "page:/logout", "page:/p5/delete/1"} {
		if _, ok := exploreImportedKeys(fixture.imported)[forbidden]; ok {
			t.Fatalf("forbidden key %q was imported", forbidden)
		}
	}

	stored := fixture.runs[run.ID]
	if stored.Status != domain.RunDone {
		t.Fatalf("stored run status = %q, want done", stored.Status)
	}
	if stored.AgentSessionID != nil || stored.AgentRunID != nil {
		t.Fatal("explore run must not reference an agent session")
	}
	var stats application.ExploreStats
	if err := json.Unmarshal(stored.Stats, &stats); err != nil {
		t.Fatalf("decode stored stats: %v", err)
	}
	if stats.Discovered != result.Discovered || stats.Imported != result.Imported {
		t.Fatalf("stored stats = %+v, result = %+v", stats, result)
	}
	if !exploreHasEvent(fixture, domain.EventCaseRunFinished) {
		t.Fatalf("events = %#v, want case.run_finished", fixture.events)
	}
	if hits.get("/logout") != 0 || hits.get("/p5/delete/1") != 0 {
		t.Fatal("logout/delete links were fetched")
	}
}

// TestExplorePassesEnvironmentAllowlistToProbes pins that every probe (the
// start page and the links discovered from it) carries the per-environment
// allowlist: the base URL host plus the configured allowed_hosts, normalized to
// bare lowercase hosts and deduplicated.
func TestExplorePassesEnvironmentAllowlistToProbes(t *testing.T) {
	prober, hits := newExploreSite()
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober
	fixture.environment.AllowedHosts = []string{"API.Test:8443", "127.0.0.1", "App.TEST"}

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := fixture.service().Run(context.Background(), run); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"app.test", "api.test", "127.0.0.1"}
	for _, path := range []string{"/", "/p5/orders"} {
		if got := hits.allowlist(path); !slices.Equal(got, want) {
			t.Fatalf("probe %s allowlist = %v, want %v", path, got, want)
		}
	}
}

func TestExploreSkipsExistingAppModelKeys(t *testing.T) {
	prober, _ := newExploreSite()
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober
	fixture.existing = []application.AppModelSnapshot{
		{Key: "page:/p5/home", Kind: "page"},
		{Key: "route:/p5/orders", Kind: "route"},
	}

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := fixture.service().Run(context.Background(), run)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2", result.Skipped)
	}
	if result.Imported != result.Discovered-2 {
		t.Fatalf("result = %+v, want imported = discovered-2", result)
	}
	byKey := exploreImportedKeys(fixture.imported)
	if _, ok := byKey["page:/p5/home"]; ok {
		t.Fatal("existing page:/p5/home was imported again")
	}
	if _, ok := byKey["route:/p5/orders"]; ok {
		t.Fatal("existing route:/p5/orders was imported again")
	}
	exploreRequireKey(t, fixture.imported, "page:/p5/orders", "page")
}

func TestExplorePrefersAllowedSameOriginLinks(t *testing.T) {
	hits := newExploreHits()
	prober := &fakeProber{hits: hits, handler: func(path, _, _ string) (application.ProbeResult, error) {
		switch path {
		case "/":
			return application.ProbeResult{
				ContentType: "text/html",
				Title:       "Home",
				Links:       []string{"http://example.com/secret", "/ok"},
			}, nil
		case "/ok":
			return application.ProbeResult{ContentType: "text/html", Title: "OK"}, nil
		default:
			return application.ProbeResult{Status: 404}, nil
		}
	}}
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := fixture.service().Run(context.Background(), run); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := exploreImportedKeys(fixture.imported)["page:/secret"]; ok {
		t.Fatal("a cross-host link was imported")
	}
	exploreRequireKey(t, fixture.imported, "page:/ok", "page")
	if hits.total() != 2 {
		t.Fatalf("probe calls = %d, want 2 (home + /ok)", hits.total())
	}
}

func TestExploreSkipsLogoutAndDeleteLinks(t *testing.T) {
	hits := newExploreHits()
	prober := &fakeProber{hits: hits, handler: func(path, _, _ string) (application.ProbeResult, error) {
		if path != "/" {
			return application.ProbeResult{Status: 404}, nil
		}
		return application.ProbeResult{
			ContentType: "text/html",
			Title:       "Home",
			Links: []string{"/logout", "/signout", "/p5/delete/1",
				"/p5/remove/1", "/unsubscribe", "/keep"},
		}, nil
	}}
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := fixture.service().Run(context.Background(), run)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, path := range []string{"/logout", "/signout", "/p5/delete/1", "/p5/remove/1", "/unsubscribe"} {
		if hits.get(path) != 0 {
			t.Fatalf("%s was fetched", path)
		}
	}
	if hits.get("/keep") != 1 {
		t.Fatalf("/keep hits = %d, want 1", hits.get("/keep"))
	}
	if result.Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", result.Status)
	}
}

func TestExploreStopsAtDepthTwo(t *testing.T) {
	hits := newExploreHits()
	prober := &fakeProber{hits: hits, handler: func(path, _, _ string) (application.ProbeResult, error) {
		links := map[string][]string{
			"/a": {"/b"},
			"/b": {"/c"},
			"/c": {"/d"},
			"/d": nil,
		}
		pageLinks, ok := links[path]
		if !ok {
			return application.ProbeResult{Status: 404}, nil
		}
		return application.ProbeResult{
			ContentType: "text/html",
			Title:       strings.ToUpper(strings.TrimPrefix(path, "/")),
			Links:       pageLinks,
		}, nil
	}}
	fixture := newExploreFixture(exploreBaseURL + "/a")
	fixture.prober = prober

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := fixture.service().Run(context.Background(), run)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hits.get("/d") != 0 {
		t.Fatal("depth-3 page /d was fetched")
	}
	if _, ok := exploreImportedKeys(fixture.imported)["page:/d"]; ok {
		t.Fatal("depth-3 page /d was imported")
	}
	exploreRequireKey(t, fixture.imported, "page:/c", "page")
	if !result.Truncated {
		t.Fatal("depth limit did not set stats.truncated")
	}
}

func TestExploreCapsRequestsAtForty(t *testing.T) {
	hits := newExploreHits()
	links := make([]string, 60)
	for index := range links {
		links[index] = fmt.Sprintf("/p/%d", index)
	}
	prober := &fakeProber{hits: hits, handler: func(path, _, _ string) (application.ProbeResult, error) {
		if path == "/" {
			return application.ProbeResult{
				ContentType: "text/html",
				Title:       "Home",
				Links:       links,
			}, nil
		}
		return application.ProbeResult{
			ContentType: "text/html",
			Title:       "Page " + path,
			Links:       []string{"/"},
		}, nil
	}}
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := fixture.service().Run(context.Background(), run)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Requests != 40 {
		t.Fatalf("requests = %d, want 40", result.Requests)
	}
	if !result.Truncated {
		t.Fatal("request cap did not set stats.truncated")
	}
	if total := hits.total(); total != 40 {
		t.Fatalf("probe calls = %d, want 40", total)
	}
}

func TestExploreDeduplicatesLoopingLinks(t *testing.T) {
	hits := newExploreHits()
	prober := &fakeProber{hits: hits, handler: func(path, _, _ string) (application.ProbeResult, error) {
		switch path {
		case "/a":
			return application.ProbeResult{
				ContentType: "text/html",
				Title:       "A",
				Links:       []string{"/b", "/b", "/a"},
			}, nil
		case "/b":
			return application.ProbeResult{
				ContentType: "text/html",
				Title:       "B",
				Links:       []string{"/a"},
			}, nil
		default:
			return application.ProbeResult{Status: 404}, nil
		}
	}}
	fixture := newExploreFixture(exploreBaseURL + "/a")
	fixture.prober = prober

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := fixture.service().Run(context.Background(), run); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hits.get("/a") != 1 || hits.get("/b") != 1 {
		t.Fatalf("hits = %#v, want one fetch per page", hits.counts)
	}
	if len(fixture.imported) != 2 {
		t.Fatalf("imported keys = %v, want page:/a and page:/b", exploreSortedKeys(fixture.imported))
	}
}

func TestExploreSendsSessionCookies(t *testing.T) {
	const wantCookie = "fixture_session=ok; theme=dark"
	hits := newExploreHits()
	prober := &fakeProber{hits: hits, handler: func(_, rawURL, cookieHeader string) (application.ProbeResult, error) {
		if cookieHeader != wantCookie {
			return application.ProbeResult{URL: rawURL, Status: 401}, nil
		}
		return application.ProbeResult{URL: rawURL, ContentType: "text/html", Title: "Authorized"}, nil
	}}
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober
	sessionID := int64(42)
	fixture.session = &application.SessionSnapshot{
		ID:            sessionID,
		State:         "valid",
		EnvironmentID: 9,
		Cookies:       map[string]string{"fixture_session": "ok", "theme": "dark"},
	}

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{SessionID: &sessionID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := fixture.service().Run(context.Background(), run); err != nil {
		t.Fatalf("Run: %v", err)
	}
	exploreRequireKey(t, fixture.imported, "page:/", "page")
	if got := hits.cookie("/"); got != wantCookie {
		t.Fatalf("probe cookie = %q, want %q", got, wantCookie)
	}
}

func TestExploreRejectsSessionOfAnotherEnvironment(t *testing.T) {
	fixture := newExploreFixture(exploreBaseURL)
	sessionID := int64(42)
	fixture.session = &application.SessionSnapshot{
		ID:            sessionID,
		State:         "valid",
		EnvironmentID: 99,
		Cookies:       map[string]string{"fixture_session": "ok"},
	}

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{SessionID: &sessionID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = fixture.service().Run(context.Background(), run)
	if reason := exploreErrorReason(t, err); reason != "session_not_valid" {
		t.Fatalf("reason = %q, want session_not_valid", reason)
	}
	if status := fixture.runs[run.ID].Status; status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", status)
	}
}

func TestExploreFailsUnreachableEnvironment(t *testing.T) {
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = &fakeProber{handler: func(_, _, _ string) (application.ProbeResult, error) {
		return application.ProbeResult{}, errors.New("dial tcp: connection refused")
	}}

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = fixture.service().Run(context.Background(), run)
	if reason := exploreErrorReason(t, err); reason != "environment_unreachable" {
		t.Fatalf("reason = %q, want environment_unreachable", reason)
	}
	stored := fixture.runs[run.ID]
	if stored.Status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", stored.Status)
	}
	if !strings.Contains(string(stored.Errors), "environment_unreachable") {
		t.Fatalf("run errors = %s, want environment_unreachable", stored.Errors)
	}
}

func TestExploreFailsDisallowedEnvironment(t *testing.T) {
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = &fakeProber{handler: func(_, _, _ string) (application.ProbeResult, error) {
		return application.ProbeResult{}, fmt.Errorf("%w: host not allowlisted", application.ErrProbeTargetNotAllowed)
	}}

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = fixture.service().Run(context.Background(), run)
	if reason := exploreErrorReason(t, err); reason != "environment_not_allowed" {
		t.Fatalf("reason = %q, want environment_not_allowed", reason)
	}
	if status := fixture.runs[run.ID].Status; status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", status)
	}
}

func TestExploreRejectsStartPathOutsideAllowlist(t *testing.T) {
	prober, hits := newExploreSite()
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{
		StartPaths: []string{"http://example.com/secret"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = fixture.service().Run(context.Background(), run)
	if reason := exploreErrorReason(t, err); reason != "environment_not_allowed" {
		t.Fatalf("reason = %q, want environment_not_allowed", reason)
	}
	if hits.total() != 0 {
		t.Fatalf("probe calls = %d, want 0 before URL validation", hits.total())
	}
	if status := fixture.runs[run.ID].Status; status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", status)
	}
}

func TestExploreMarksOversizedPageAsTruncated(t *testing.T) {
	hits := newExploreHits()
	prober := &fakeProber{hits: hits, handler: func(_, rawURL, _ string) (application.ProbeResult, error) {
		return application.ProbeResult{
			URL:         rawURL,
			ContentType: "text/html",
			Title:       "Huge",
			Truncated:   true,
		}, nil
	}}
	fixture := newExploreFixture(exploreBaseURL)
	fixture.prober = prober

	run, err := fixture.service().Start(context.Background(), exploreVersionID, 7, application.ExploreCommand{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := fixture.service().Run(context.Background(), run)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Truncated {
		t.Fatal("oversized body did not set stats.truncated")
	}
	exploreRequireKey(t, fixture.imported, "page:/", "page")
}
