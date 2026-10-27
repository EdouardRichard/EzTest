package server

// These tests prove the P4 codeintel wiring in the HTTP assembly: the server
// mounts the repository- and version-scoped routes behind the global bearer
// auth, the one administrator action keeps its admin guard, the agent tool
// catalog exposes the nine P4 tools, and the existing P3 surface is unchanged.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/tools"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelhttp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// codeIntelTestCommit is a valid lowercase 40-hex commit for the query probes.
const codeIntelTestCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// stubCodeIntelIndex is a minimal codeintel IndexService. It also satisfies the
// narrower agent tool CodeIntelIndexPort.
type stubCodeIntelIndex struct {
	startRun  codeinteldomain.IndexRun
	latestRun codeinteldomain.IndexRun
}

func (s stubCodeIntelIndex) Start(context.Context, int64, int64) (codeinteldomain.IndexRun, error) {
	return s.startRun, nil
}

func (s stubCodeIntelIndex) Get(context.Context, int64, int64) (codeinteldomain.IndexRun, error) {
	return s.latestRun, nil
}

func (s stubCodeIntelIndex) GetLatest(context.Context, int64) (codeinteldomain.IndexRun, error) {
	return s.latestRun, nil
}

// stubCodeIntelQuery is a minimal codeintel QueryService that returns empty
// successful reads; nothing here reaches Git or SQL.
type stubCodeIntelQuery struct{}

func (stubCodeIntelQuery) Tree(context.Context, int64, string, string, string, int) (codeinteldomain.TreePage, error) {
	return codeinteldomain.TreePage{}, nil
}

func (stubCodeIntelQuery) File(context.Context, int64, int64, string, string, int, int) (codeinteldomain.FileWindow, error) {
	return codeinteldomain.FileWindow{}, nil
}

func (stubCodeIntelQuery) Diff(context.Context, int64, string, string, int, string) (codeinteldomain.ChangeSetData, error) {
	return codeinteldomain.ChangeSetData{}, nil
}

func (stubCodeIntelQuery) FileDiff(context.Context, int64, string, string, string) (codeinteldomain.FileDiff, error) {
	return codeinteldomain.FileDiff{}, nil
}

func (stubCodeIntelQuery) Facts(context.Context, int64, codeinteldomain.EntityFilter) ([]codeinteldomain.CodeEntity, string, bool, error) {
	return nil, "", false, nil
}

func (stubCodeIntelQuery) Search(context.Context, int64, int64, string, string, string, int, string) ([]codeinteldomain.CodeEntity, string, bool, error) {
	return nil, "", false, nil
}

func (stubCodeIntelQuery) Commit(context.Context, int64, string) (codeinteldomain.CommitInfo, error) {
	return codeinteldomain.CommitInfo{}, nil
}

func (stubCodeIntelQuery) VersionChanges(context.Context, int64, string) (codeinteldomain.ChangeSetData, string, string, error) {
	return codeinteldomain.ChangeSetData{}, codeIntelTestCommit, codeIntelTestCommit, nil
}

// stubCodeIntelAnalysis is a minimal codeintel AnalysisService. It also
// satisfies the narrower agent tool CodeIntelAnalysisPort.
type stubCodeIntelAnalysis struct {
	analysis codeinteldomain.CodeAnalysis
}

func (s stubCodeIntelAnalysis) Start(context.Context, int64, int64, string, bool) (codeinteldomain.CodeAnalysis, bool, error) {
	return s.analysis, false, nil
}

func (s stubCodeIntelAnalysis) Get(context.Context, int64, int64) (codeinteldomain.CodeAnalysis, error) {
	return s.analysis, nil
}

func (s stubCodeIntelAnalysis) GetLatest(context.Context, int64) (codeinteldomain.CodeAnalysis, error) {
	return s.analysis, nil
}

func (s stubCodeIntelAnalysis) ListCandidates(context.Context, int64) ([]codeinteldomain.ImpactCandidate, error) {
	return nil, nil
}

func (s stubCodeIntelAnalysis) PatchCandidate(context.Context, int64, int64, int64, int64, bool) (codeinteldomain.ImpactCandidate, error) {
	return codeinteldomain.ImpactCandidate{}, nil
}

// stubCodeIntelAnchor is a minimal codeintel AnchorService. It also satisfies
// the narrower agent tool CodeIntelAnchorPort.
type stubCodeIntelAnchor struct{}

func (stubCodeIntelAnchor) Create(context.Context, int64, int64, codeinteldomain.CreateAnchorParams) (codeinteldomain.CodeAnchor, error) {
	return codeinteldomain.CodeAnchor{}, nil
}

func (stubCodeIntelAnchor) List(context.Context, int64, codeinteldomain.AnchorFilter) ([]codeinteldomain.CodeAnchor, error) {
	return []codeinteldomain.CodeAnchor{}, nil
}

func (stubCodeIntelAnchor) Delete(context.Context, int64, int64) error { return nil }

// stubCodeIntelRepoCatalog is the P2 repository projection mounted on
// GET /api/code/repos.
type stubCodeIntelRepoCatalog struct {
	repos []codeintelapp.RepoInfo
}

func (s stubCodeIntelRepoCatalog) Get(_ context.Context, repoID int64) (codeintelapp.RepoInfo, error) {
	for _, repo := range s.repos {
		if repo.ID == repoID {
			return repo, nil
		}
	}
	return codeintelapp.RepoInfo{}, codeinteldomain.NewNotFound("code_repository_not_found", "repository not found")
}

func (s stubCodeIntelRepoCatalog) List(context.Context) ([]codeintelapp.RepoInfo, error) {
	return s.repos, nil
}

// stubToolRepoCatalog satisfies the agent tools' local ListRepos port. The
// production adapter lives in cmd/eztest and is tested there; here the tools
// only need a non-nil catalog.
type stubToolRepoCatalog struct{}

func (stubToolRepoCatalog) ListRepos(context.Context) ([]agenttools.CodeIntelRepo, error) {
	return nil, nil
}

// newStubCodeIntelHandler builds the handler group over the stubs.
func newStubCodeIntelHandler() *codeintelhttp.Handler {
	return codeintelhttp.NewHandler(codeintelhttp.HandlerDeps{
		Index: stubCodeIntelIndex{
			startRun: codeinteldomain.IndexRun{
				ID: 9, RepoID: 7, CommitSHA: codeIntelTestCommit, Status: codeinteldomain.RunQueued,
			},
			latestRun: codeinteldomain.IndexRun{
				ID: 9, RepoID: 7, CommitSHA: codeIntelTestCommit, Status: codeinteldomain.RunDone,
			},
		},
		Query: stubCodeIntelQuery{},
		Analyses: stubCodeIntelAnalysis{analysis: codeinteldomain.CodeAnalysis{
			ID: 55, VersionID: 101, RepoID: 7, Status: codeinteldomain.RunDone,
			AnalysisVersion: 1, PromptVersion: "p4.1", CreatedBy: 2,
		}},
		Anchors: stubCodeIntelAnchor{},
		Repos: stubCodeIntelRepoCatalog{repos: []codeintelapp.RepoInfo{{
			ID: 7, Key: "web", Name: "Web", LocalPath: "/repo", FixedBranch: "main", Status: "active",
		}}},
	})
}

// codeIntelTestDeps wires the bearer tokens and the stub codeintel handlers.
func codeIntelTestDeps() (Dependencies, identity.User, identity.User) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	return Dependencies{
		Auth:      tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		CodeIntel: newStubCodeIntelHandler(),
	}, admin, regular
}

// codeIntelRequest builds a request with an optional bearer token and body.
func codeIntelRequest(method, path, token, body string) *http.Request {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// TestCodeIntelRoutesMounted proves every codeintel route group is mounted when
// the composition root provides Dependencies.CodeIntel: anonymous callers get
// 401 for all of them, a regular user reaches the read handlers, and the one
// administrator action (triggering a T0 index) keeps its 403 admin guard.
func TestCodeIntelRoutesMounted(t *testing.T) {
	deps, _, _ := codeIntelTestDeps()
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/code/repos"},
		{http.MethodPost, "/api/code/repos/7/index"},
		{http.MethodGet, "/api/code/repos/7/index-runs/latest"},
		{http.MethodGet, "/api/code/repos/7/index-runs/9"},
		{http.MethodGet, "/api/code/repos/7/tree"},
		{http.MethodGet, "/api/code/repos/7/file?path=main.go"},
		{http.MethodGet, "/api/code/repos/7/diff?base=" + codeIntelTestCommit + "&head=" + codeIntelTestCommit},
		{http.MethodGet, "/api/code/repos/7/diff/file?base=" + codeIntelTestCommit + "&head=" + codeIntelTestCommit + "&path=main.go"},
		{http.MethodGet, "/api/code/repos/7/facts?commit=" + codeIntelTestCommit},
		{http.MethodGet, "/api/code/repos/7/search?q=main&commit=" + codeIntelTestCommit},
		{http.MethodGet, "/api/code/repos/7/commits/" + codeIntelTestCommit},
		{http.MethodGet, "/api/versions/101/code/changes"},
		{http.MethodPost, "/api/versions/101/code/analyses"},
		{http.MethodGet, "/api/versions/101/code/analyses/latest"},
		{http.MethodGet, "/api/versions/101/code/analyses/55"},
		{http.MethodPatch, "/api/versions/101/code/analyses/55/candidates/66"},
		{http.MethodGet, "/api/versions/101/code/anchors"},
		{http.MethodPost, "/api/versions/101/code/anchors"},
		{http.MethodDelete, "/api/versions/101/code/anchors/77"},
	}
	for _, route := range routes {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, codeIntelRequest(route.method, route.path, "", `{}`))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s anonymous=%d body=%s want 401", route.method, route.path, rec.Code, rec.Body)
		}
	}

	// Repository list: reachable by a regular user and carrying the P2
	// projection plus the latest index run.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, codeIntelRequest(http.MethodGet, "/api/code/repos", "ezt_user", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("user GET /api/code/repos=%d body=%s want 200", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"key":"web"`) || !strings.Contains(body, `"runId":"9"`) {
		t.Fatalf("user GET /api/code/repos body=%s want the repository and its latest run", body)
	}

	// The index trigger is the only administrator action (design spec §6.10).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, codeIntelRequest(http.MethodPost, "/api/code/repos/7/index", "ezt_user", ""))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("user POST index=%d body=%s want 403", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, codeIntelRequest(http.MethodPost, "/api/code/repos/7/index", "ezt_admin", ""))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"runId":"9"`) {
		t.Fatalf("admin POST index=%d body=%s want 202 with the queued run", rec.Code, rec.Body)
	}

	// One authenticated probe per read route group proves the route is mounted
	// (a missing route is the mux's plain-text 404, not a handler payload).
	probes := []struct {
		method, path, token string
		body                string
		want                int
	}{
		{http.MethodGet, "/api/code/repos/7/index-runs/latest", "ezt_user", "", http.StatusOK},
		{http.MethodGet, "/api/code/repos/7/tree?limit=10", "ezt_user", "", http.StatusOK},
		{http.MethodGet, "/api/code/repos/7/facts?commit=" + codeIntelTestCommit, "ezt_user", "", http.StatusOK},
		{http.MethodGet, "/api/versions/101/code/changes", "ezt_user", "", http.StatusOK},
		{http.MethodPost, "/api/versions/101/code/analyses", "ezt_user", `{}`, http.StatusAccepted},
		{http.MethodGet, "/api/versions/101/code/analyses/latest", "ezt_user", "", http.StatusOK},
		{http.MethodGet, "/api/versions/101/code/anchors", "ezt_user", "", http.StatusOK},
	}
	for _, probe := range probes {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, codeIntelRequest(probe.method, probe.path, probe.token, probe.body))
		if rec.Code != probe.want {
			t.Fatalf("%s %s user=%d body=%s want %d", probe.method, probe.path, rec.Code, rec.Body, probe.want)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s %s content-type=%q want a JSON handler payload", probe.method, probe.path, ct)
		}
	}
}

// TestCodeIntelRoutesNilSafe proves a process without the P4 wiring leaves the
// route surface unmounted instead of panicking: a nil dependency and an empty
// handler group both yield the mux 404.
func TestCodeIntelRoutesNilSafe(t *testing.T) {
	user := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	for name, deps := range map[string]Dependencies{
		"nil dependency": {Auth: tokenMapAuth{"ezt_user": user}},
		"empty handlers": {Auth: tokenMapAuth{"ezt_user": user}, CodeIntel: codeintelhttp.NewHandler(codeintelhttp.HandlerDeps{})},
		"empty-nil-safety": {
			Auth: tokenMapAuth{"ezt_user": user}, CodeIntel: &codeintelhttp.Handler{},
		},
	} {
		h := New(config.Config{Env: "test"}, testLogger(), deps)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, codeIntelRequest(http.MethodGet, "/api/code/repos", "ezt_user", ""))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: GET /api/code/repos=%d body=%s want 404", name, rec.Code, rec.Body)
		}
	}
}

// TestAgentToolCatalogIncludesCodeIntel proves the registry built by the
// composition root exposes the nine P4 tools (design spec §8.1) alongside the
// unchanged P1 tools. The API lists the dotted registry names; the model-facing
// underscore wire names are produced by the existing LLM gateway mapping
// (code.analyze -> code_analyze) and therefore never appear here. The registry
// fixture is built locally because package main cannot be imported; the
// composition root's own append helper (codeIntelTools) is covered by
// TestCodeIntelToolsRegistersNine in cmd/eztest.
func TestAgentToolCatalogIncludesCodeIntel(t *testing.T) {
	registry := agentapp.NewToolRegistry(append(
		[]agentdomain.Tool{agenttools.NewTestEcho()},
		agenttools.NewCodeIntelTools(agenttools.CodeIntelToolDeps{
			Repos:    stubToolRepoCatalog{},
			Indexes:  stubCodeIntelIndex{},
			Queries:  stubCodeIntelQuery{},
			Analyses: stubCodeIntelAnalysis{},
			Anchors:  stubCodeIntelAnchor{},
		})...,
	)...)
	deps, _, _ := codeIntelTestDeps()
	deps.Agent = &AgentDependencies{
		Profiles: stubAgentProfiles{},
		Sessions: stubAgentSessions{},
		Runs:     stubAgentRuns{},
		Tools:    agentapp.NewToolCatalog(registry),
	}
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, codeIntelRequest(http.MethodGet, "/api/agents/tools", "ezt_admin", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/agents/tools=%d body=%s want 200", rec.Code, rec.Body)
	}
	var body struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode tool catalog: %v body=%s", err, rec.Body)
	}
	names := make(map[string]bool, len(body.Data))
	for _, tool := range body.Data {
		names[tool.Name] = true
	}
	if !names["test.echo"] {
		t.Fatalf("tool catalog=%v want the unchanged P1 test.echo tool", names)
	}
	for _, name := range []string{
		"code.repos", "code.search", "code.diff", "code.facts", "code.routes",
		"code.apis", "code.analyze", "code.impact", "code.anchors",
	} {
		if !names[name] {
			t.Fatalf("tool catalog=%v missing P4 tool %q", names, name)
		}
	}
}

// TestP3RoutesUnchangedWithCodeIntel guards the P3 surface against the P4
// assembly: the version routes stay registered and admin-free while the
// codeintel routes are mounted, and the health endpoint still answers.
func TestP3RoutesUnchangedWithCodeIntel(t *testing.T) {
	deps, _, _ := codeIntelTestDeps()
	deps.Pinger = fakePinger{}
	deps.Version = versionRoutesDeps().Version
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/versions"},
		{http.MethodGet, "/api/versions/101"},
		{http.MethodPost, "/api/versions"},
	} {
		rec := httptest.NewRecorder()
		body := ""
		if route.method == http.MethodPost {
			body = `{"name":"wiring version"}`
		}
		h.ServeHTTP(rec, codeIntelRequest(route.method, route.path, "ezt_user", body))
		if route.method == http.MethodPost {
			if rec.Code != http.StatusCreated {
				t.Fatalf("%s %s=%d body=%s want 201 (P3 has no admin guard)", route.method, route.path, rec.Code, rec.Body)
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s=%d body=%s want 200", route.method, route.path, rec.Code, rec.Body)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, codeIntelRequest(http.MethodGet, "/api/health", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/health=%d body=%s want 200", rec.Code, rec.Body)
	}
}
