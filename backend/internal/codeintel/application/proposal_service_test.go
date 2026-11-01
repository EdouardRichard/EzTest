package codeintelapp

import (
	"context"
	"strings"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// --- proposal fakes ---------------------------------------------------------------

type fakeProposalTargets struct {
	targets map[int64]ProposalTarget
	err     error
	calls   []int64
}

func (f *fakeProposalTargets) ProposalTarget(_ context.Context, versionSUTID int64) (ProposalTarget, error) {
	f.calls = append(f.calls, versionSUTID)
	if f.err != nil {
		return ProposalTarget{}, f.err
	}
	target, ok := f.targets[versionSUTID]
	if !ok {
		return ProposalTarget{}, codeintel.ErrNotFound
	}
	return target, nil
}

type fakeProposalWriter struct {
	pageCalls   []ProposalPageInput
	apiCalls    []ProposalAPIInput
	pageCreated map[string]bool
	apiCreated  map[string]bool
	createErr   error
	items       []ProposalItem
	listCalls   [][]int64
	listErr     error
	nextID      int64
}

func newFakeProposalWriter() *fakeProposalWriter {
	return &fakeProposalWriter{pageCreated: map[string]bool{}, apiCreated: map[string]bool{}}
}

func (f *fakeProposalWriter) ProposePage(_ context.Context, in ProposalPageInput) (ProposalItem, bool, error) {
	f.pageCalls = append(f.pageCalls, in)
	if f.createErr != nil {
		return ProposalItem{}, false, f.createErr
	}
	created := !f.pageCreated[in.RouteTemplate]
	f.pageCreated[in.RouteTemplate] = true
	f.nextID++
	return ProposalItem{
		Kind: "page", ID: f.nextID, Name: in.Name, URL: in.URL,
		RouteTemplate: in.RouteTemplate, Source: in.Source, Status: "proposed",
	}, created, nil
}

func (f *fakeProposalWriter) ProposeAPI(_ context.Context, in ProposalAPIInput) (ProposalItem, bool, error) {
	f.apiCalls = append(f.apiCalls, in)
	if f.createErr != nil {
		return ProposalItem{}, false, f.createErr
	}
	key := in.Method + " " + in.PathTemplate
	created := !f.apiCreated[key]
	f.apiCreated[key] = true
	f.nextID++
	return ProposalItem{
		Kind: "api", ID: f.nextID, Name: in.Name, Method: in.Method,
		PathTemplate: in.PathTemplate, Source: in.Source, Status: "proposed",
	}, created, nil
}

func (f *fakeProposalWriter) ListProposals(_ context.Context, environmentIDs []int64) ([]ProposalItem, error) {
	f.listCalls = append(f.listCalls, environmentIDs)
	return f.items, f.listErr
}

type fakeExecutionReader struct {
	network []NetworkRequest
	err     error
}

func (f *fakeExecutionReader) NetworkEvidence(context.Context, int64) ([]NetworkRequest, error) {
	return f.network, f.err
}

func (f *fakeExecutionReader) EvidenceMeta(context.Context, int64, int64) (EvidenceRef, error) {
	return EvidenceRef{}, codeintel.ErrNotFound
}

// --- harness ----------------------------------------------------------------------

type proposalHarness struct {
	service  *ProposalService
	analyses *fakeAnalysisRepo
	facts    *fakeFactRepo
	versions *fakeVersionReader
	targets  *fakeProposalTargets
	writer   *fakeProposalWriter
	exec     *fakeExecutionReader
	events   *fakeEvents
	audit    *fakeAudit
	ctx      context.Context
}

func newProposalHarness(t *testing.T) *proposalHarness {
	t.Helper()
	ctx := platform.WithActor(context.Background(), platform.Actor{ID: 42, Role: "user"})
	now := testProposalTime
	versions := &fakeVersionReader{bindings: []VersionSUTBinding{
		{VersionSUTID: 11, VersionID: 1, SUTID: 1001, RepoID: 10, CodeBaselineCommit: impactBaseCommit},
	}}
	targets := &fakeProposalTargets{targets: map[int64]ProposalTarget{
		11: {VersionSUTID: 11, SUTID: 1001, EnvironmentID: 1101, BaseURL: "https://staging.example.test"},
	}}
	analyses := newFakeAnalysisRepo()
	if _, err := analyses.Create(ctx, codeintel.Analysis{
		ID: 5, VersionID: 1, RepoID: 10, BaseCommitSHA: impactBaseCommit,
		HeadCommitSHA: anchorCommitB, Status: codeintel.AnalysisDone, AnalysisVersion: 1,
		ChangedFiles: []byte(`[]`), Stats: []byte(`{}`), Unmapped: []byte(`[]`),
		Errors: []byte(`[]`), FactVersions: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed analysis: %v", err)
	}
	facts := &fakeFactRepo{}
	for _, fact := range []codeintel.Fact{
		{RepoID: 10, CommitSHA: anchorCommitB, FactKey: "route:/orders", Version: 1,
			Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "/orders",
			Source: "ai", Confidence: codeintel.ConfidenceHigh},
		{RepoID: 10, CommitSHA: anchorCommitB, FactKey: "page:/checkout", Version: 1,
			Kind: codeintel.EntityPage, Path: "src/pages/Checkout.tsx", Name: "/checkout",
			Source: "ai", Confidence: codeintel.ConfidenceHigh},
		{RepoID: 10, CommitSHA: anchorCommitB, FactKey: "api:POST /api/refund", Version: 1,
			Kind: codeintel.EntityAPI, Path: "server/refund.go", Name: "POST /api/refund",
			Source: "ai", Confidence: codeintel.ConfidenceHigh},
		{RepoID: 10, CommitSHA: anchorCommitB, FactKey: "client:POST /api/refund", Version: 1,
			Kind: codeintel.EntityClientCall, Path: "src/api/refund.ts", Name: "POST /api/refund",
			Source: "ai", Confidence: codeintel.ConfidenceHigh},
	} {
		fact.Body = []byte(`{}`)
		fact.Evidence = []byte(`[]`)
		fact.CreatedAt = now
		fact.UpdatedAt = now
		if _, _, err := facts.InsertVersion(ctx, fact); err != nil {
			t.Fatalf("seed fact: %v", err)
		}
	}
	writer := newFakeProposalWriter()
	exec := &fakeExecutionReader{}
	events := &fakeEvents{}
	audit := &fakeAudit{}
	service := NewProposalService(ProposalDeps{
		Analyses:  analyses,
		Facts:     facts,
		Versions:  versions,
		Targets:   targets,
		Resources: writer,
		Execution: exec,
		Events:    events,
		Audit:     audit,
		Settings:  &fakeSettings{},
		Clock:     fakeClock{now: now},
	})
	return &proposalHarness{
		service: service, analyses: analyses, facts: facts, versions: versions,
		targets: targets, writer: writer, exec: exec, events: events, audit: audit, ctx: ctx,
	}
}

var testProposalTime = time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)

// --- tests -----------------------------------------------------------------------

func TestProposalGenerateFromCodeFacts(t *testing.T) {
	h := newProposalHarness(t)
	result, err := h.service.Generate(h.ctx, 1, 0)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.PagesCreated != 2 || result.PagesSkipped != 0 || result.APIsCreated != 1 || result.APISkipped != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(h.writer.pageCalls) != 2 || len(h.writer.apiCalls) != 1 {
		t.Fatalf("writer calls = %d pages / %d apis", len(h.writer.pageCalls), len(h.writer.apiCalls))
	}
	var ordersPage *ProposalPageInput
	for index := range h.writer.pageCalls {
		if h.writer.pageCalls[index].RouteTemplate == "/orders" {
			ordersPage = &h.writer.pageCalls[index]
		}
	}
	if ordersPage == nil {
		t.Fatalf("orders page missing: %+v", h.writer.pageCalls)
	}
	if ordersPage.EnvironmentID != 1101 || ordersPage.SUTID != 1001 || ordersPage.Source != "ai" ||
		ordersPage.URL != "https://staging.example.test/orders" {
		t.Fatalf("page input = %+v", ordersPage)
	}
	called := false
	for _, candidate := range h.writer.pageCalls {
		if candidate.RouteTemplate == "/checkout" && candidate.URL == "https://staging.example.test/checkout" {
			called = true
		}
	}
	if !called {
		t.Fatalf("page fact missing: %+v", h.writer.pageCalls)
	}
	api := h.writer.apiCalls[0]
	if api.EnvironmentID != 1101 || api.Method != "POST" || api.PathTemplate != "/api/refund" ||
		api.Source != "ai" || api.Name != "POST /api/refund" {
		t.Fatalf("api input = %+v", api)
	}

	if len(h.events.events) != 1 || h.events.events[0].Type != "code.page_api_proposed" {
		t.Fatalf("events = %v", h.events.types())
	}
	payload := h.events.events[0].Payload
	if payload["pages_created"] != 2 || payload["apis_created"] != 1 || payload["pages_skipped"] != 0 {
		t.Fatalf("event payload = %+v", payload)
	}
	foundAudit := false
	for _, entry := range h.audit.entries {
		if entry.Action == "code.page_api_proposed" {
			foundAudit = true
			if entry.ActorID == nil || *entry.ActorID != 42 {
				t.Fatalf("audit actor = %+v", entry)
			}
		}
	}
	if !foundAudit {
		t.Fatalf("audit = %+v", h.audit.entries)
	}
}

func TestProposalGenerateNetworkEvidenceAndDedupe(t *testing.T) {
	h := newProposalHarness(t)
	h.exec.network = []NetworkRequest{
		{ID: 1, CaseRunID: 9, VersionSUTID: int64Ptr(11), Method: "GET", URL: "https://staging.example.test/api/orders/123", Status: 200},
		{ID: 2, CaseRunID: 9, VersionSUTID: int64Ptr(11), Method: "GET", URL: "https://evil.example.test/api/orders/123", Status: 200},
		{ID: 3, CaseRunID: 9, VersionSUTID: nil, Method: "GET", URL: "https://staging.example.test/api/orders/456", Status: 200},
	}
	h.writer.apiCreated["POST /api/refund"] = true // an approved/proposed API already exists
	result, err := h.service.Generate(h.ctx, 1, 10)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.APIsCreated != 1 || result.APISkipped != 1 {
		t.Fatalf("result = %+v", result)
	}
	networkAPI := false
	for _, call := range h.writer.apiCalls {
		if call.PathTemplate == "/api/orders/{id}" {
			if call.Source != "code" || call.Method != "GET" {
				t.Fatalf("network api = %+v", call)
			}
			networkAPI = true
		}
	}
	if !networkAPI {
		t.Fatalf("network-derived API missing: %+v", h.writer.apiCalls)
	}
	for _, call := range h.writer.apiCalls {
		if strings.Contains(call.PathTemplate, "evil") {
			t.Fatal("foreign host must not be proposed")
		}
	}
	if result.PagesCreated != 2 {
		t.Fatalf("facts pages = %+v", result)
	}
}

func TestProposalGenerateRequiresBindingsAndAnalysis(t *testing.T) {
	h := newProposalHarness(t)
	_, err := h.service.Generate(h.ctx, 1, 99)
	assertPlatformError(t, err, 422, "repo_not_bound")

	h.versions.bindings = nil
	_, err = h.service.Generate(h.ctx, 1, 0)
	assertPlatformError(t, err, 422, "repo_not_bound")

	h.versions.bindings = []VersionSUTBinding{{VersionSUTID: 11, VersionID: 1, RepoID: 10}}
	h.analyses.runs = map[int64]codeintel.Analysis{}
	h.analyses.order = nil
	_, err = h.service.Generate(h.ctx, 1, 0)
	assertPlatformError(t, err, 422, "analysis_not_found")
}

func TestProposalList(t *testing.T) {
	h := newProposalHarness(t)
	h.writer.items = []ProposalItem{
		{Kind: "page", ID: 501, Name: "Orders", URL: "https://staging.example.test/orders",
			RouteTemplate: "/orders", Source: "ai", Status: "proposed"},
		{Kind: "api", ID: 601, Name: "Refund", Method: "POST", PathTemplate: "/api/refund",
			Source: "code", Status: "proposed"},
	}
	views, err := h.service.List(h.ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 2 || views[0].Kind != "api" || views[1].RouteTemplate != "/orders" {
		t.Fatalf("views = %+v", views)
	}
	if len(h.writer.listCalls) != 1 || len(h.writer.listCalls[0]) != 1 || h.writer.listCalls[0][0] != 1101 {
		t.Fatalf("list calls = %+v", h.writer.listCalls)
	}
}

// int64Ptr is a tiny test helper.
func int64Ptr(value int64) *int64 { return &value }
