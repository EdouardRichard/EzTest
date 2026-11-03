package main

// Task 37 fix round 1 (R93): a fresh deployment wrote no page/API facts, so the
// gap probes and the coverage matrix had no discovery data to consume. This
// wiring test proves the analysis refresh now logs in first, runs the runtime
// page crawl and the static API scan through the existing executor/fact
// recorder/source provider, records page/API facts, and that gaps/coverage then
// consume them. It also pins the discovery boundaries: page-scoped close and no
// browser network capture.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// errFakeOpen simulates an executor failure during the crawl.
var errFakeOpen = errors.New("executor explore action failed: page_limit")

// p9EventLog is a shared, ordered log across the fake login runtime, the fake
// explore executor and the fake page closer. The analysis flows are serial, so
// the order is deterministic.
type p9EventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *p9EventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *p9EventLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// fakeP9DiscoveryExecutor serves the page crawl. It records every explore action
// it receives; the test asserts discovery only uses open/snapshot (no network
// capture action exists in the protocol at all).
type fakeP9DiscoveryExecutor struct {
	mu      sync.Mutex
	log     *p9EventLog
	actions []string
	opens   []string
	counter int
	openErr error
}

func (f *fakeP9DiscoveryExecutor) Action(_ context.Context, action string, args map[string]any) (map[string]any, error) {
	f.mu.Lock()
	f.actions = append(f.actions, action)
	if f.log != nil {
		f.log.add("executor:" + action)
	}
	if f.openErr != nil && action == "explore.open" {
		f.mu.Unlock()
		return nil, f.openErr
	}
	url, _ := args["url"].(string)
	pageID, _ := args["page_id"].(string)
	f.mu.Unlock()

	if pageID == "" {
		f.mu.Lock()
		f.counter++
		pageID = fmt.Sprintf("page_%d", f.counter)
		f.opens = append(f.opens, url)
		f.mu.Unlock()
	}
	elements := []any{}
	if action == "explore.open" && url != "http://sut.test/dashboard" {
		elements = []any{map[string]any{
			"index": 0, "tag": "a", "selector": "#dashboard", "role": "link",
			"name": "Dashboard", "href": "/dashboard", "visible": true,
		}}
	}
	return map[string]any{
		"page_id": pageID,
		"url":     url,
		"dom_snapshot": map[string]any{
			"url":      url,
			"title":    "page " + pageID,
			"route":    "/",
			"elements": elements,
		},
	}, nil
}

func (f *fakeP9DiscoveryExecutor) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func (f *fakeP9DiscoveryExecutor) openedURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opens...)
}

// fakeP9Sources is the static repository source provider (the production
// adapter reads bound repositories; this fake returns one Gin registration).
type fakeP9Sources struct {
	files     []codeintelapp.SourceFile
	versionID int64
}

func (f *fakeP9Sources) SourceFiles(_ context.Context, versionID int64) ([]codeintelapp.SourceFile, error) {
	f.versionID = versionID
	return f.files, nil
}

func p9DiscoverySources() *fakeP9Sources {
	return &fakeP9Sources{files: []codeintelapp.SourceFile{{
		Path: "backend/internal/server/routes.go",
		Content: `package server

func Register(r *gin.Engine) {
	r.POST("/orders", createOrder)
}
`,
	}}}
}

func p9DiscoveryRequirement() exploreapp.RequirementSnapshot {
	return exploreapp.RequirementSnapshot{
		ID: 101, VersionID: 10010, Title: "下单", Status: "clarified",
		Description:        "管理员下单，异常超时重试，边界上限 100，使用预置测试账号数据。",
		AcceptanceCriteria: "下单成功后订单状态为已创建",
		TargetPages:        []string{"/dashboard"},
		TargetAPIs:         []string{"POST /orders"},
	}
}

// indexOfEvent returns the first index of event in the log, or -1.
func indexOfEvent(events []string, event string) int {
	for index, value := range events {
		if value == event {
			return index
		}
	}
	return -1
}

// TestAnalysisWiresDiscovery is the R93 wiring proof for the discovery pre-pass:
// Login runs first, page/API discovery records facts through the shared
// executor/recorder/source provider, the crawl page is closed page-scoped, and
// the gap analysis/coverage matrix consume the freshly recorded facts (the
// requirement target is verified, so it produces no dependency gap).
func TestAnalysisWiresDiscovery(t *testing.T) {
	log := &p9EventLog{}
	executor := &fakeP9DiscoveryExecutor{log: log}
	runtime := &fakeP9Runtime{log: log}
	closer := &fakeP9Closer{}
	facts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{}}
	versions := &fakeP9Versions{view: p9TestVersionView()}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      runtime,
		Executor:     executor,
		Sources:      p9DiscoverySources(),
		Pages:        closer,
		Facts:        facts,
		Recorder:     facts,
		Requirements: &fakeP9Requirements{snapshots: []exploreapp.RequirementSnapshot{p9DiscoveryRequirement()}},
		Cases:        &fakeP9Cases{},
		Roles:        &fakeP9Roles{roles: []string{"admin"}},
		Versions:     versions,
	})
	if err != nil {
		t.Fatalf("newP9Analysis: %v", err)
	}

	result, err := analysis.Refresh(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.LoginStatus != exploreapp.SessionActive {
		t.Fatalf("login status = %q, want active", result.LoginStatus)
	}
	if result.DiscoveredPages == 0 || result.DiscoveredAPIs == 0 {
		t.Fatalf("discovery result = pages %d apis %d", result.DiscoveredPages, result.DiscoveredAPIs)
	}

	// Facts recorded by discovery are page/API facts and the analysis reads them
	// back (coverage counts pages and APIs).
	kinds := map[string]int{}
	for _, fact := range facts.written {
		kinds[fact.Kind]++
	}
	if kinds[exploreapp.KindPage] == 0 || kinds[exploreapp.KindAPI] == 0 {
		t.Fatalf("recorded fact kinds = %v, want page and api facts", kinds)
	}
	if result.Pages == 0 || result.APIs == 0 {
		t.Fatalf("coverage did not consume discovery facts: %+v", result)
	}
	// The requirement depends on the crawled page and the statically discovered
	// API; because both facts now exist, the dependency gap is gone.
	if result.Gaps != 0 {
		t.Fatalf("gaps = %d, want 0 after discovery verified both dependencies: %+v", result.Gaps, facts.written)
	}

	// Login must precede every discovery/probe action.
	events := log.list()
	if len(events) == 0 || events[0] != "login" {
		t.Fatalf("login must run first, got %v", events)
	}
	if runtime.loginCount() != 1 {
		t.Fatalf("runtime login calls = %d, want exactly 1 (the analysis pre-pass logs in once)", runtime.loginCount())
	}
	firstExecutor := -1
	for index, event := range events {
		if len(event) > len("executor:") && event[:len("executor:")] == "executor:" {
			firstExecutor = index
			break
		}
	}
	if firstExecutor < 0 || firstExecutor < indexOfEvent(events, "login") {
		t.Fatalf("discovery actions must follow login: %v", events)
	}
	firstProbe := -1
	for index, event := range events {
		if len(event) > len("open:") && event[:len("open:")] == "open:" {
			firstProbe = index
			break
		}
	}
	if firstProbe >= 0 && firstProbe < firstExecutor {
		t.Fatalf("gap probes ran before discovery: %v", events)
	}
	if len(runtime.opens) == 0 {
		t.Fatalf("the gap analysis must still probe discovered pages: %v", events)
	}

	// Only the explore open/snapshot actions drive discovery: there is no
	// browser network-capture action in the flow.
	for _, action := range executor.recorded() {
		if action != "explore.open" && action != "explore.snapshot" {
			t.Fatalf("discovery used a non-DOM action %q (network capture is not allowed)", action)
		}
	}
	if len(executor.openedURLs()) == 0 {
		t.Fatal("page discovery never opened a page")
	}

	// Page-scoped close: the crawl page and every probe page are released by
	// exact id, never by a close-all.
	closed := closer.ids
	if len(closed) == 0 {
		t.Fatal("discovery/probe pages were not closed")
	}
	for _, id := range closed {
		if id == "" {
			t.Fatalf("empty page id closed: %v", closed)
		}
	}
}

// TestAnalysisDiscoveryUsesEnvironmentBaseURLWithoutBoundPages is the S1
// production path: a version binds only the SUT/environment/accounts/repository,
// so the analysis must seed login and the page crawl from the environment base
// URL instead of writing no facts at all.
func TestAnalysisDiscoveryUsesEnvironmentBaseURLWithoutBoundPages(t *testing.T) {
	log := &p9EventLog{}
	executor := &fakeP9DiscoveryExecutor{log: log}
	runtime := &fakeP9Runtime{log: log}
	facts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{}}
	view := versionapp.VersionView{
		ID: 1001,
		Bindings: []versionapp.BindingView{{
			ID:          77,
			Environment: resourceapp.EnvironmentView{ID: "8", Name: "staging", BaseURL: "http://sut.test/app"},
			Accounts:    []resourceapp.AccountView{{ID: "9", Roles: []string{"admin"}}},
		}},
	}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      runtime,
		Executor:     executor,
		Sources:      p9DiscoverySources(),
		Pages:        &fakeP9Closer{},
		Facts:        facts,
		Recorder:     facts,
		Requirements: &fakeP9Requirements{},
		Cases:        &fakeP9Cases{},
		Roles:        &fakeP9Roles{roles: []string{"admin"}},
		Versions:     &fakeP9Versions{view: view},
	})
	if err != nil {
		t.Fatalf("newP9Analysis: %v", err)
	}
	result, err := analysis.Refresh(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.LoginStatus != exploreapp.SessionActive {
		t.Fatalf("login status = %q, want active", result.LoginStatus)
	}
	opened := executor.openedURLs()
	if len(opened) == 0 || opened[0] != "http://sut.test/app" {
		t.Fatalf("crawl seeds = %v, want the environment base URL first", opened)
	}
	if result.DiscoveredPages == 0 || result.DiscoveredAPIs == 0 {
		t.Fatalf("discovery result = pages %d apis %d", result.DiscoveredPages, result.DiscoveredAPIs)
	}
	kinds := map[string]int{}
	for _, fact := range facts.written {
		kinds[fact.Kind]++
	}
	if kinds[exploreapp.KindPage] == 0 || kinds[exploreapp.KindAPI] == 0 {
		t.Fatalf("recorded fact kinds = %v, want page and api facts", kinds)
	}
}

// TestAnalysisRetriesTransientLogin covers a transient executor/page-cap
// failure during discovery login: the analysis retries the retry_login outcome
// instead of leaving the version without facts until the 10-minute debounce.
func TestAnalysisRetriesTransientLogin(t *testing.T) {
	executor := &fakeP9DiscoveryExecutor{}
	blocked := exploreapp.SessionFact{
		Status: exploreapp.SessionBlocked, Reason: "login_page_unavailable",
		Action: exploreapp.ActionRetryLogin,
	}
	runtime := &fakeP9Runtime{loginQueue: []exploreapp.SessionFact{blocked, blocked}}
	facts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{}}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      runtime,
		Executor:     executor,
		Sources:      p9DiscoverySources(),
		Pages:        &fakeP9Closer{},
		Facts:        facts,
		Recorder:     facts,
		Requirements: &fakeP9Requirements{},
		Cases:        &fakeP9Cases{},
		Roles:        &fakeP9Roles{roles: []string{"admin"}},
		Versions:     &fakeP9Versions{view: p9TestVersionView()},
	})
	if err != nil {
		t.Fatalf("newP9Analysis: %v", err)
	}
	analysis.loginRetryDelay = 0
	result, err := analysis.Refresh(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if runtime.loginCount() != 3 {
		t.Fatalf("login calls = %d, want 2 retries + the active attempt", runtime.loginCount())
	}
	if result.LoginStatus != exploreapp.SessionActive || result.DiscoveredPages == 0 || result.DiscoveredAPIs == 0 {
		t.Fatalf("result after retry = %+v", result)
	}
}

// TestAnalysisDiscoveryIsBestEffort keeps a crawler/static-scan failure from
// failing the whole analysis refresh: static gaps/coverage still run.
func TestAnalysisDiscoveryIsBestEffort(t *testing.T) {
	executor := &fakeP9DiscoveryExecutor{openErr: errFakeOpen}
	facts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{}}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      &fakeP9Runtime{},
		Executor:     executor,
		Sources:      p9DiscoverySources(),
		Pages:        &fakeP9Closer{},
		Facts:        facts,
		Recorder:     facts,
		Requirements: &fakeP9Requirements{snapshots: []exploreapp.RequirementSnapshot{p9DiscoveryRequirement()}},
		Cases:        &fakeP9Cases{},
		Versions:     &fakeP9Versions{view: p9TestVersionView()},
	})
	if err != nil {
		t.Fatalf("newP9Analysis: %v", err)
	}
	if result, err := analysis.Refresh(context.Background(), 1001); err != nil {
		t.Fatalf("a discovery failure must not fail the refresh: %v", err)
	} else if result.DiscoveredPages != 0 {
		t.Fatalf("discovered pages = %d after a failed crawl", result.DiscoveredPages)
	}
}
