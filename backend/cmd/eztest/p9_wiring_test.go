package main

// Task 37 (P9-T4) composition-root test, R93: the P9 analysis consumer built in
// p9_wiring.go must consume the shared explore runtime (Login before opening
// pages, serialized actions, page-scoped close), and the design case-author
// projection must read the same version facts.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

/* -------------------------------- fakes ---------------------------------- */

type fakeP9Runtime struct {
	mu        sync.Mutex
	events    []string
	opens     []string
	blocked   bool
	openErr   error
	active    int
	maxActive int
	// log is the optional shared log the discovery test uses to prove the
	// cross-component ordering (login before discovery/probes).
	log *p9EventLog
}

func (f *fakeP9Runtime) Login(_ context.Context, cfg exploreapp.LoginFlowConfig) (exploreapp.SessionFact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "login")
	if f.log != nil {
		f.log.add("login")
	}
	if f.blocked {
		return exploreapp.SessionFact{Status: exploreapp.SessionBlocked, Reason: "invalid_credentials", AccountRole: cfg.Role}, nil
	}
	return exploreapp.SessionFact{Status: exploreapp.SessionActive, AccountRole: cfg.Role}, nil
}

func (f *fakeP9Runtime) OpenWithSession(_ context.Context, _ exploreapp.LoginFlowConfig, url string) (exploreapp.Page, exploreapp.SessionFact, error) {
	f.mu.Lock()
	f.events = append(f.events, "open:"+url)
	if f.log != nil {
		f.log.add("open:" + url)
	}
	f.opens = append(f.opens, url)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.openErr != nil {
		return exploreapp.Page{}, exploreapp.SessionFact{}, f.openErr
	}
	return exploreapp.Page{ID: "page_1", URL: url}, exploreapp.SessionFact{}, nil
}

// loginCount reports how many times the runtime was asked to log in. The
// analysis pre-pass logs in once and hands the same session to the probes.
func (f *fakeP9Runtime) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, event := range f.events {
		if event == "login" {
			count++
		}
	}
	return count
}

type fakeP9Closer struct {
	mu  sync.Mutex
	ids []string
}

func (f *fakeP9Closer) ClosePage(_ context.Context, pageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, pageID)
	return nil
}

type fakeP9Facts struct {
	mu      sync.Mutex
	byKind  map[string][]versiondomain.Fact
	written []versiondomain.Fact
	err     error
}

func (f *fakeP9Facts) Current(_ context.Context, _ int64, kind string) ([]versiondomain.Fact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]versiondomain.Fact(nil), f.byKind[kind]...), nil
}

func (f *fakeP9Facts) Record(_ context.Context, fact versiondomain.Fact) (versiondomain.Fact, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fact.ID = int64(500 + len(f.written))
	f.written = append(f.written, fact)
	if f.byKind == nil {
		f.byKind = map[string][]versiondomain.Fact{}
	}
	f.byKind[fact.Kind] = append(f.byKind[fact.Kind], fact)
	return fact, false, nil
}

type fakeP9Requirements struct {
	snapshots []exploreapp.RequirementSnapshot
	calls     int
}

func (f *fakeP9Requirements) Requirements(_ context.Context, _ int64) ([]exploreapp.RequirementSnapshot, error) {
	f.calls++
	return append([]exploreapp.RequirementSnapshot(nil), f.snapshots...), nil
}

type fakeP9Cases struct {
	cases []exploreapp.CaseSnapshot
}

func (f *fakeP9Cases) Cases(_ context.Context, _ int64) ([]exploreapp.CaseSnapshot, error) {
	return append([]exploreapp.CaseSnapshot(nil), f.cases...), nil
}

type fakeP9Roles struct {
	roles []string
}

func (f *fakeP9Roles) Roles(_ context.Context, _ int64) ([]string, error) {
	return append([]string(nil), f.roles...), nil
}

type fakeP9Versions struct {
	view versionapp.VersionView
	err  error
}

func (f *fakeP9Versions) Get(_ context.Context, _ int64) (versionapp.VersionView, error) {
	return f.view, f.err
}

type fakeP9RequirementsReader struct {
	views   []versionapp.RequirementView
	details map[int64]versionapp.RequirementDetail
	lists   int
	gets    int
}

func (f *fakeP9RequirementsReader) List(_ context.Context, _ int64, _ string, _ int, _ *int64) ([]versionapp.RequirementView, bool, error) {
	f.lists++
	return f.views, false, nil
}

func (f *fakeP9RequirementsReader) Get(_ context.Context, _ int64, requirementID int64) (versionapp.RequirementDetail, error) {
	f.gets++
	return f.details[requirementID], nil
}

type fakeP9DesignGraph struct {
	view designapp.GraphView
}

func (f *fakeP9DesignGraph) ReadGraph(_ context.Context, _ int64) (designapp.GraphView, error) {
	return f.view, nil
}

/* ---------------------------- fixtures ----------------------------------- */

func p9TestVersionView() versionapp.VersionView {
	return versionapp.VersionView{
		ID: 1001,
		Bindings: []versionapp.BindingView{{
			ID:          77,
			Environment: resourceapp.EnvironmentView{ID: "8", Name: "staging"},
			Pages: []resourceapp.PageView{
				{ID: "501", URL: "http://sut.test/login", RouteTemplate: "/login"},
				{ID: "502", URL: "http://sut.test/checkout", RouteTemplate: "/checkout"},
			},
			APIs:     []resourceapp.APIView{{ID: "601", Method: "post", PathTemplate: "/orders"}},
			Accounts: []resourceapp.AccountView{{ID: "9", Roles: []string{"admin"}}},
		}},
	}
}

func p9PageFact(id int64, url string) versiondomain.Fact {
	return versiondomain.Fact{
		ID:   id,
		Kind: exploreapp.KindPage,
		Key:  "page:" + url,
		Payload: map[string]any{
			"page_key":       url,
			"route_template": "/",
			"url":            url,
		},
	}
}

func p9GapFact(id int64) versiondomain.Fact {
	return versiondomain.Fact{
		ID:   id,
		Kind: exploreapp.KindGap,
		Key:  "gap:req:101:acceptance",
		Payload: map[string]any{
			"dimension":         "acceptance",
			"requirement_id":    int64(101),
			"requirement_title": "下单",
			"reason":            "缺少验收标准",
			"question":          "请补充验收标准",
		},
	}
}

/* ------------------------------- tests ----------------------------------- */

// TestExploreRuntimeConsumed is the R93 wiring proof: the analysis bundle built
// from the process-wide explore wiring opens pages only after Login, serializes
// the probes and releases every opened page by id. Without this the Task 49
// composition would be dead wiring.
func TestExploreRuntimeConsumed(t *testing.T) {
	runtime := &fakeP9Runtime{}
	closer := &fakeP9Closer{}
	facts := &fakeP9Facts{
		byKind: map[string][]versiondomain.Fact{
			exploreapp.KindPage: {p9PageFact(1, "http://sut.test/checkout")},
			exploreapp.KindGap:  {p9GapFact(2)},
		},
	}
	requirements := &fakeP9Requirements{snapshots: []exploreapp.RequirementSnapshot{{
		ID: 101, VersionID: 10010, Title: "下单", Status: "clarified",
		Description: "用户下单", TargetPages: []string{"/checkout"},
	}}}
	cases := &fakeP9Cases{cases: []exploreapp.CaseSnapshot{{
		CaseID: 301, CaseVersionID: 3001, RequirementID: 101, Title: "下单成功", Status: "confirmed",
		PageKeys: []string{"/checkout"},
	}}}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      runtime,
		Pages:        closer,
		Facts:        facts,
		Recorder:     facts,
		Requirements: requirements,
		Cases:        cases,
		Roles:        &fakeP9Roles{roles: []string{"admin"}},
		Versions:     &fakeP9Versions{view: p9TestVersionView()},
	})
	if err != nil {
		t.Fatalf("newP9Analysis: %v", err)
	}
	result, err := analysis.Refresh(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.Requirements != 1 || result.Cases != 1 || result.Pages != 1 || result.Roles != 1 {
		t.Fatalf("analysis result = %+v", result)
	}
	if result.LoginStatus != exploreapp.SessionActive {
		t.Fatalf("login status = %q, want active", result.LoginStatus)
	}
	if len(runtime.events) == 0 || runtime.events[0] != "login" {
		t.Fatalf("runtime must log in before opening pages: %v", runtime.events)
	}
	if len(runtime.opens) != 1 || runtime.opens[0] != "http://sut.test/checkout" {
		t.Fatalf("runtime opens = %v", runtime.opens)
	}
	if runtime.maxActive > 1 {
		t.Fatalf("runtime actions were concurrent: %d", runtime.maxActive)
	}
	if len(closer.ids) != 1 || closer.ids[0] != "page_1" {
		t.Fatalf("page-scoped close ids = %v", closer.ids)
	}
	if result.Gaps == 0 || result.Drafts == 0 {
		t.Fatalf("analysis did not produce gaps/drafts: %+v", result)
	}
	if len(facts.written) != result.Gaps {
		t.Fatalf("written gap facts = %d, want %d", len(facts.written), result.Gaps)
	}

	// A blocked login must stop the live probes (no page is opened).
	blockedRuntime := &fakeP9Runtime{blocked: true}
	blockedCloser := &fakeP9Closer{}
	blocked, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      blockedRuntime,
		Pages:        blockedCloser,
		Facts:        facts,
		Recorder:     &fakeP9Facts{byKind: facts.byKind},
		Requirements: requirements,
		Cases:        cases,
		Versions:     &fakeP9Versions{view: p9TestVersionView()},
	})
	if err != nil {
		t.Fatalf("newP9Analysis blocked: %v", err)
	}
	blockedResult, err := blocked.Refresh(context.Background(), 1001)
	if err != nil {
		t.Fatalf("blocked Refresh: %v", err)
	}
	if blockedResult.LoginStatus != exploreapp.SessionBlocked {
		t.Fatalf("blocked login status = %q", blockedResult.LoginStatus)
	}
	if len(blockedRuntime.opens) != 0 || len(blockedCloser.ids) != 0 {
		t.Fatalf("blocked login opened/closed pages: %v %v", blockedRuntime.opens, blockedCloser.ids)
	}

	// Fail closed on a missing runtime: the consumer is never built without the
	// process-wide explore wiring.
	if _, err := newP9Analysis(p9AnalysisDeps{Facts: facts, Recorder: facts, Requirements: requirements}); err == nil {
		t.Fatal("newP9Analysis must reject a nil runtime")
	}
}

// TestP9AnalysisDebounce keeps an event storm to one refresh per interval and
// one refresh per version.
func TestP9AnalysisDebounce(t *testing.T) {
	now := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	debounce := newP9AnalysisDebounce(10*time.Minute, func() time.Time { return now })
	if !debounce.due(1001) {
		t.Fatal("first refresh must be due")
	}
	if debounce.due(1001) {
		t.Fatal("second refresh within the interval must be dropped")
	}
	if !debounce.due(1002) {
		t.Fatal("another version has its own interval")
	}
	now = now.Add(10 * time.Minute)
	if !debounce.due(1001) {
		t.Fatal("refresh must be due after the interval")
	}
}

// TestP9RequirementSourceMapsBindingKeys covers the version adapters: a
// requirement target resource ID is projected onto the knowledge key the page/API
// facts use, and a case target resource ID the same way.
func TestP9RequirementSourceMapsBindingKeys(t *testing.T) {
	versions := &fakeP9Versions{view: p9TestVersionView()}
	reader := &fakeP9RequirementsReader{
		views: []versionapp.RequirementView{
			{ID: 101, CurrentVersionID: 10010, Title: "下单", Status: "clarified"},
			{ID: 102, CurrentVersionID: 10020, Title: "旧需求", Status: "obsolete"},
		},
		details: map[int64]versionapp.RequirementDetail{
			101: {
				RequirementView: versionapp.RequirementView{ID: 101, Title: "下单", Status: "clarified"},
				CurrentVersion: versionapp.RequirementVersionView{
					ID:                 10010,
					Title:              "下单",
					Description:        "用户下单",
					AcceptanceCriteria: "下单成功",
					TargetPages:        []string{"502", "999"},
					TargetAPIs:         []string{"601"},
				},
			},
		},
	}
	source := &p9RequirementSource{requirements: reader, versions: versions}
	snapshots, err := source.Requirements(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Requirements: %v", err)
	}
	if reader.lists != 1 || reader.gets != 1 {
		t.Fatalf("reader calls = lists %d gets %d", reader.lists, reader.gets)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v, want the non-obsolete requirement", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.ID != 101 || snapshot.VersionID != 10010 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(snapshot.TargetPages) != 1 || snapshot.TargetPages[0] != "/checkout" {
		t.Fatalf("target pages = %v, want the mapped route template only", snapshot.TargetPages)
	}
	if len(snapshot.TargetAPIs) != 1 || snapshot.TargetAPIs[0] != "POST /orders" {
		t.Fatalf("target apis = %v, want the mapped api key", snapshot.TargetAPIs)
	}

	caseSource := &p9CaseSource{
		graph: &fakeP9DesignGraph{view: designapp.GraphView{
			VersionID: 1001,
			Requirements: []designapp.RequirementGraphView{{
				RequirementID: 101,
				Scenarios: []designapp.ScenarioGraphView{{
					ID: 501,
					Cases: []designapp.CaseCardView{
						{CaseID: 301, CurrentVersionID: 3001, Title: "下单成功", Status: "confirmed", PageIDs: []int64{502}, APIIDs: []int64{601}},
						{CaseID: 302, CurrentVersionID: 3002, Title: "旧用例", Status: "stale"},
					},
				}},
			}},
		}},
		versions: versions,
	}
	caseSnapshots, err := caseSource.Cases(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Cases: %v", err)
	}
	if len(caseSnapshots) != 1 || caseSnapshots[0].CaseID != 301 {
		t.Fatalf("cases = %+v, want only the confirmed case", caseSnapshots)
	}
	if len(caseSnapshots[0].PageKeys) != 1 || caseSnapshots[0].PageKeys[0] != "/checkout" {
		t.Fatalf("case page keys = %v", caseSnapshots[0].PageKeys)
	}
	if len(caseSnapshots[0].APIKeys) != 1 || caseSnapshots[0].APIKeys[0] != "POST /orders" {
		t.Fatalf("case api keys = %v", caseSnapshots[0].APIKeys)
	}
}

// TestP9LoginPageURL prefers a bound login route so a fresh login can find the
// form, while the reuse-verification open still gets a URL.
func TestP9LoginPageURL(t *testing.T) {
	pages := []resourceapp.PageView{
		{ID: "1", URL: "http://sut.test/home", RouteTemplate: "/home"},
		{ID: "2", URL: "http://sut.test/auth/signin", RouteTemplate: "/auth/signin"},
	}
	if got := p9LoginPageURL(pages); got != "http://sut.test/auth/signin" {
		t.Fatalf("login page = %q", got)
	}
	if got := p9LoginPageURL(pages[:1]); got != "http://sut.test/home" {
		t.Fatalf("fallback page = %q", got)
	}
	if got := p9LoginPageURL(nil); got != "" {
		t.Fatalf("empty pages = %q", got)
	}
}

// TestP9FactRecordsProjection proves the design case author reads the same
// version facts through the neutral record projection (no payload leak beyond
// what the design projection renders).
func TestP9FactRecordsProjection(t *testing.T) {
	facts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{
		exploreapp.KindPage: {{
			ID: 7, Kind: exploreapp.KindPage, Key: "page:http://sut/app",
			Payload: map[string]any{"route_template": "/app", "title": "工作台", "elements": []any{"x"}},
		}},
	}}
	graph := designapp.NewGraphService(nil, nil, nil, nil, nil, nil)
	graph.SetDiscoveryFacts(p9FactRecords{facts: facts})
	listed, err := graph.CaseAuthorFacts(context.Background(), 1001)
	if err != nil {
		t.Fatalf("CaseAuthorFacts: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != 7 || listed[0].Summary != "/app · 工作台" {
		t.Fatalf("case-author facts = %+v", listed)
	}
	if _, err := (p9FactRecords{}).Current(context.Background(), 1001, exploreapp.KindPage); err == nil {
		t.Fatal("p9FactRecords must fail closed without a fact service")
	}
	if _, err := (p9FactRecords{facts: &fakeP9Facts{err: errors.New("db down")}}).Current(context.Background(), 1001, exploreapp.KindPage); err == nil {
		t.Fatal("p9FactRecords must propagate a read error")
	}
}
