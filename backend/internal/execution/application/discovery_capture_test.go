package executionapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// fakeDiscoveryWriter records the observation batches the terminal capture hands
// to the discovery repository port (W1-11 wires the postgres adapter; W1-10
// allows a nil writer).
type fakeDiscoveryWriter struct {
	mu    sync.Mutex
	calls [][]executionapp.DiscoveryObservation
	err   error
}

func (f *fakeDiscoveryWriter) WriteObservations(_ context.Context, observations []executionapp.DiscoveryObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]executionapp.DiscoveryObservation(nil), observations...))
	return f.err
}

func (f *fakeDiscoveryWriter) all() []executionapp.DiscoveryObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]executionapp.DiscoveryObservation, 0)
	for _, call := range f.calls {
		rows = append(rows, call...)
	}
	return rows
}

func (f *fakeDiscoveryWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// discoveryNetworkResult is the verbatim recorded-traffic payload the executor's
// passive recorder returns (T16): one navigated document, one API fetch (with a
// query secret the observation normalizes away but the evidence keeps verbatim,
// GD-01) and one static script (which the observation capture filters). The
// {items,recording,degraded} envelope mirrors the GET /network read surface.
const discoveryNetworkResult = `{"recording":true,"degraded":false,"items":[
  {"method":"GET","url":"https://shop.example.com/login","status":200,"duration_ms":12,"mime":"text/html; charset=utf-8","resource_type":"document"},
  {"method":"POST","url":"https://shop.example.com/api/orders?token=secret","status":201,"duration_ms":8,"mime":"application/json","resource_type":"fetch"},
  {"method":"GET","url":"https://shop.example.com/static/app.js","status":200,"duration_ms":3,"mime":"application/javascript","resource_type":"script"}
]}`

// seedDiscoverySUTContexts installs two active browser contexts plus the two
// contexts the capture must ignore (one closed, one active without a browser
// context id).
func seedDiscoverySUTContexts(fixture *caseRunnerFixture) {
	executorID := int64(7)
	for _, row := range []execution.CaseRunSUTContext{
		{
			ID: 4001, CaseRunID: 3001, SUTID: 7, AccountRole: "admin",
			SessionID: "ctx_a", ExecutorID: &executorID,
			Status: execution.SUTContextActive,
		},
		{
			ID: 4002, CaseRunID: 3001, SUTID: 7, AccountRole: "viewer",
			SessionID: "ctx_b", ExecutorID: &executorID,
			Status: execution.SUTContextActive,
		},
		{
			ID: 4003, CaseRunID: 3001, SUTID: 7, AccountRole: "closed",
			SessionID: "ctx_closed", ExecutorID: &executorID,
			Status: execution.SUTContextClosed,
		},
		{
			ID: 4004, CaseRunID: 3001, SUTID: 7, AccountRole: "empty",
			Status: execution.SUTContextActive,
		},
	} {
		fixture.sutContext.rows[row.ID] = row
	}
}

// newDiscoveryFixture returns a runner wired to a recording discovery writer.
func newDiscoveryFixture(t *testing.T) *caseRunnerFixture {
	t.Helper()
	fixture := newCaseRunnerFixtureWithDiscovery(t)
	fixture.execution.networkLog = json.RawMessage(discoveryNetworkResult)
	return fixture
}

// finishDiscoveryRun drives one case_run to the requested terminal status
// through the production Finish path.
func finishDiscoveryRun(t *testing.T, fixture *caseRunnerFixture, candidate execution.CaseRunStatus) execution.CaseRun {
	t.Helper()
	prepareCaseRun(t, fixture)
	seedDiscoverySUTContexts(fixture)
	switch candidate {
	case execution.CaseRunPassed:
		if _, err := fixture.runner.StepRecord(context.Background(), executionScope(), tools.StepRecordInput{Seq: 1, Status: "passed"}); err != nil {
			t.Fatalf("StepRecord: %v", err)
		}
		if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
			StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "Login 按钮可见",
		}); err != nil {
			t.Fatalf("AssertionRecord: %v", err)
		}
	case execution.CaseRunFailed:
		if _, err := fixture.runner.StepRecord(context.Background(), executionScope(), tools.StepRecordInput{Seq: 1, Status: "passed"}); err != nil {
			t.Fatalf("StepRecord: %v", err)
		}
		if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
			StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "Login 按钮不存在",
		}); err != nil {
			t.Fatalf("AssertionRecord: %v", err)
		}
	case execution.CaseRunBlocked, execution.CaseRunInconclusive:
		// blocked: explicit candidate with no executed result. inconclusive:
		// candidate passed but no required result was recorded.
	}
	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{Verdict: candidate})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return finished
}

// TestCaptureTerminalObservationsAllVerdicts is R-EX-1: every terminal state
// (including passed) captures all active browser contexts before they are
// closed, and the deterministic verdict is untouched.
func TestCaptureTerminalObservationsAllVerdicts(t *testing.T) {
	cases := []struct {
		candidate   execution.CaseRunStatus
		wantVerdict execution.CaseRunStatus
	}{
		{execution.CaseRunPassed, execution.CaseRunPassed},
		{execution.CaseRunFailed, execution.CaseRunFailed},
		{execution.CaseRunBlocked, execution.CaseRunBlocked},
		{execution.CaseRunInconclusive, execution.CaseRunInconclusive},
	}
	for _, tc := range cases {
		t.Run(string(tc.candidate), func(t *testing.T) {
			fixture := newDiscoveryFixture(t)
			finished := finishDiscoveryRun(t, fixture, tc.candidate)
			if finished.Status != tc.wantVerdict {
				t.Fatalf("%s: verdict changed to %q", tc.candidate, finished.Status)
			}
			if got := fixture.execution.networkLogCalls; got != 2 {
				t.Fatalf("%s: network calls = %d, want the 2 active contexts", tc.candidate, got)
			}
			observations := fixture.discovery.all()
			if len(observations) == 0 {
				t.Fatalf("%s: no discovery observation was written", tc.candidate)
			}
			for _, observation := range observations {
				if observation.Kind != "page" && observation.Kind != "api" {
					t.Fatalf("%s: kind = %q", tc.candidate, observation.Kind)
				}
				if observation.Host == "" || observation.Path == "" {
					t.Fatalf("%s: observation is not normalized: %+v", tc.candidate, observation)
				}
				if strings.Contains(observation.Path, "?") || strings.Contains(observation.Path, "secret") {
					t.Fatalf("%s: observation leaked query: %+v", tc.candidate, observation)
				}
			}
			t.Logf("%s: contexts=2 network_calls=2 observations=%d sample=%+v",
				tc.candidate, len(observations), observations[0])
		})
	}
}

// TestCaptureTerminalObservationsRequestsFullNetworkWindow pins the W1-15 fix:
// the recorder's read tail defaults to the last 50 items, while a normal SPA
// page load puts the document request first (the W1 SUT home page makes 52
// requests), so the default window drops the page observation. The capture must
// request the 200-item maximum the read surface supports.
func TestCaptureTerminalObservationsRequestsFullNetworkWindow(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	finishDiscoveryRun(t, fixture, execution.CaseRunPassed)
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if len(fixture.execution.networkLogTails) == 0 {
		t.Fatal("the terminal capture never read the network log")
	}
	for _, tail := range fixture.execution.networkLogTails {
		if tail != 200 {
			t.Fatalf("network read tail = %d, want 200 so the first document request survives", tail)
		}
	}
}

// TestEvidenceCapDoesNotDropObservations: a configured evidence cap (here 100
// via execution.evidence_max_per_case_run) may drop the evidence row, but the
// DB-29 observation write is independent of it. The dropped evidence still
// degrades the verdict to inconclusive (constitution art.4.5, ruling T6-A) — the
// observation surviving is not a licence to keep a passed verdict.
func TestEvidenceCapDoesNotDropObservations(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	fixture.settings.ints["execution.evidence_max_per_case_run"] = 100
	for i := 0; i < 100; i++ {
		fixture.evidence.rows = append(fixture.evidence.rows, execution.Evidence{
			ID: int64(9000 + i), CaseRunID: 3001, Kind: execution.EvidenceText,
		})
	}
	finished := finishDiscoveryRun(t, fixture, execution.CaseRunPassed)
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("verdict = %q, want inconclusive after the cap dropped evidence", finished.Status)
	}
	if finished.Summary.VerdictReason != execution.VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q", finished.Summary.VerdictReason, execution.VerdictReasonEvidenceDropped)
	}
	if len(fixture.discovery.all()) == 0 {
		t.Fatal("observation must not depend on evidence rows")
	}
	if got := fixture.execution.networkLogCalls; got != 2 {
		t.Fatalf("network calls = %d, want 2", got)
	}
	if got := len(fixture.evidence.rows); got != 100 {
		t.Fatalf("evidence rows = %d, want the configured cap to hold at 100", got)
	}
}

// TestCaptureTerminalObservationsNormalizesObservationKeepsEvidencePlaintext
// separates the two carriers: the DB-29 observation is functionally normalized
// (query-free host/path/route template for fact-key dedup — kept), while the
// network EVIDENCE row carries the raw executor payload verbatim. GD-01 (Q05=B):
// credentials may enter evidence plaintext, so the legacy query/userinfo erasure
// on the evidence payload is removed.
func TestCaptureTerminalObservationsNormalizesObservationKeepsEvidencePlaintext(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	fixture.execution.networkLog = json.RawMessage(`[
	  {"method":"GET","url":"https://alice:pw@shop.example.com/orders/42?token=secret#/receipt?token=secret","status":200,"duration_ms":9,"mime":"text/html","resource_type":"document"}
	]`)
	finishDiscoveryRun(t, fixture, execution.CaseRunPassed)

	observations := fixture.discovery.all()
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want one per active context", len(observations))
	}
	observation := observations[0]
	// Functional normalization (route template / fact key) is preserved.
	if observation.Host != "shop.example.com" {
		t.Fatalf("host = %q", observation.Host)
	}
	if observation.Path != "/orders/42#/receipt" {
		t.Fatalf("path = %q, want the query-free hash route", observation.Path)
	}
	if observation.ResourceTemplate != "/orders/{id}#/receipt" {
		t.Fatalf("resource_template = %q, want /orders/{id}#/receipt", observation.ResourceTemplate)
	}
	for _, leaked := range []string{"secret", "token", "alice", "pw", "?"} {
		if strings.Contains(observation.ResourceTemplate, leaked) {
			t.Fatalf("resource_template leaked %q: %q", leaked, observation.ResourceTemplate)
		}
	}
	// GD-01: the network evidence keeps the raw URL/query/userinfo verbatim.
	if len(fixture.content.puts) == 0 {
		t.Fatal("no network evidence was stored")
	}
	for _, put := range fixture.content.puts {
		payload := string(put)
		for _, want := range []string{"alice:pw", "token=secret", "https://alice:pw@shop.example.com/orders/42?token=secret"} {
			if !strings.Contains(payload, want) {
				t.Fatalf("evidence must keep %q verbatim (GD-01 plaintext): %s", want, payload)
			}
		}
	}
	t.Logf("normalized observation: host=%s path=%s template=%s; plaintext evidence=%s",
		observation.Host, observation.Path, observation.ResourceTemplate, string(fixture.content.puts[0]))
}

// TestCaptureTerminalObservationResourceTypeMapping pins the deterministic
// R-EX-2 page/api discrimination and the static-resource filter.
func TestCaptureTerminalObservationResourceTypeMapping(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	fixture.execution.networkLog = json.RawMessage(`[
	  {"method":"GET","url":"https://shop.example.com/page","status":200,"mime":"text/html","resource_type":"document"},
	  {"method":"POST","url":"https://shop.example.com/api/x","status":201,"mime":"application/json","resource_type":"xhr"},
	  {"method":"GET","url":"https://shop.example.com/api/y","status":200,"mime":"application/json","resource_type":"fetch"},
	  {"method":"GET","url":"https://shop.example.com/app.js","status":200,"mime":"application/javascript","resource_type":"script"},
	  {"method":"GET","url":"https://shop.example.com/logo.png","status":200,"mime":"image/png","resource_type":"image"},
	  {"method":"GET","url":"https://shop.example.com/style.css","status":200,"mime":"text/css","resource_type":"stylesheet"},
	  {"method":"GET","url":"https://shop.example.com/font.woff2","status":200,"mime":"font/woff2","resource_type":"font"},
	  {"method":"GET","url":"https://shop.example.com/old","status":200,"mime":"text/html; charset=utf-8","resource_type":""},
	  {"method":"GET","url":"https://shop.example.com/legacy.html","status":200,"mime":"text/html","resource_type":""},
	  {"method":"GET","url":"https://shop.example.com/data","status":200,"mime":"application/json","resource_type":""},
	  {"method":"GET","url":"https://shop.example.com/api/stream","status":200,"mime":"text/event-stream","resource_type":"eventsource"},
	  {"method":"GET","url":"https://shop.example.com/api/ws","status":200,"mime":"application/json","resource_type":"websocket"},
	  {"method":"OPTIONS","url":"https://shop.example.com/api/preflight","status":204,"mime":"","resource_type":"preflight"},
	  {"method":"GET","url":"https://shop.example.com/manifest.webmanifest","status":200,"mime":"application/manifest+json","resource_type":"manifest"},
	  {"method":"GET","url":"https://shop.example.com/assets/logo","status":200,"mime":"image/png","resource_type":""},
	  {"method":"GET","url":"https://shop.example.com/assets/thumb","status":200,"mime":"image/jpeg","resource_type":"other"}
	]`)
	finishDiscoveryRun(t, fixture, execution.CaseRunPassed)

	calls := fixture.discovery.calls
	if len(calls) != 2 {
		t.Fatalf("writer calls = %d, want one per active context", len(calls))
	}
	got := make(map[string]string, len(calls[0]))
	resourceTypes := make(map[string]string, len(calls[0]))
	for _, observation := range calls[0] {
		got[observation.Path] = observation.Kind
		resourceTypes[observation.Path] = observation.ResourceType
	}
	want := map[string]string{
		"/page":                 "page",
		"/api/x":                "api",
		"/api/y":                "api",
		"/old":                  "page",
		"/legacy.html":          "page", // text/html + extension is a page (M10)
		"/data":                 "api",
		"/api/stream":           "api", // eventsource is an api, never dropped (M8)
		"/api/ws":               "api", // websocket is an api (M8)
		"/api/preflight":        "api",
		"/manifest.webmanifest": "api",
	}
	for path, kind := range want {
		if got[path] != kind {
			t.Fatalf("%s: kind = %q, want %q (all: %v)", path, got[path], kind, got)
		}
	}
	for _, filtered := range []string{"/app.js", "/logo.png", "/style.css", "/font.woff2", "/assets/logo", "/assets/thumb"} {
		if _, ok := got[filtered]; ok {
			t.Fatalf("static resource %s was not filtered", filtered)
		}
	}
	// A missing resource_type keeps the empty string on the observation.
	if resourceTypes["/old"] != "" {
		t.Fatalf("fallback resource_type = %q, want empty", resourceTypes["/old"])
	}
	t.Logf("resource_type mapping: %v", got)
}

// TestCaptureTerminalObservationsSurvivesOneFailingContext: one dead context
// never drops the others and never changes the verdict.
func TestCaptureTerminalObservationsSurvivesOneFailingContext(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	fixture.execution.networkLogErrBySession = map[string]error{"ctx_a": errors.New("executor network read failed")}
	finished := finishDiscoveryRun(t, fixture, execution.CaseRunPassed)
	if finished.Status != execution.CaseRunPassed {
		t.Fatalf("verdict = %q, want passed", finished.Status)
	}
	if got := fixture.execution.networkLogCalls; got != 2 {
		t.Fatalf("network attempts = %d, want one per active context", got)
	}
	observations := fixture.discovery.all()
	if len(observations) == 0 {
		t.Fatal("the healthy context must still produce observations")
	}
	if got := len(fixture.discovery.calls); got != 1 {
		t.Fatalf("writer calls = %d, want only the healthy context batch", got)
	}
}

// TestCaptureTerminalObservationsOnFinalize covers the second terminal write
// site (the agent-loop Finalize path) so the capture is not only wired into the
// execution.case_finish path.
func TestCaptureTerminalObservationsOnFinalize(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	prepareCaseRun(t, fixture)
	seedDiscoverySUTContexts(fixture)
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, nil, 1, 1); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive", stored.Status)
	}
	if got := fixture.execution.networkLogCalls; got != 2 {
		t.Fatalf("network calls = %d, want the 2 active contexts", got)
	}
	if len(fixture.discovery.all()) == 0 {
		t.Fatal("Finalize did not capture discovery observations")
	}
}

// TestCaptureTerminalObservationsWithoutWriter: the port is optional in W1-10;
// a nil writer skips the observation write while the network evidence still
// flows through the existing evidence store.
func TestCaptureTerminalObservationsWithoutWriter(t *testing.T) {
	fixture := newCaseRunnerFixture(t) // nil discovery writer
	fixture.execution.networkLog = json.RawMessage(discoveryNetworkResult)
	finishDiscoveryRun(t, fixture, execution.CaseRunPassed)
	if got := fixture.execution.networkLogCalls; got != 2 {
		t.Fatalf("network calls = %d, want 2", got)
	}
	if len(fixture.content.puts) != 2 {
		t.Fatalf("network evidence puts = %d, want one per active context", len(fixture.content.puts))
	}
}

// TestCaptureTerminalObservationsAttachIdentity: the capture fills the
// SUT, wave and case_run identity it has; version_id and environment_id
// stay zero for the ingest/repository layer to resolve.
func TestCaptureTerminalObservationsAttachIdentity(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	finishDiscoveryRun(t, fixture, execution.CaseRunPassed)

	observations := fixture.discovery.all()
	if len(observations) == 0 {
		t.Fatal("no observation was written")
	}
	for _, observation := range observations {
		if observation.SUTID != 7 {
			t.Fatalf("sut_id = %d, want 7", observation.SUTID)
		}
		if observation.WaveID != 2001 || observation.CaseRunID != 3001 {
			t.Fatalf("wave/case_run = %d/%d, want 2001/3001", observation.WaveID, observation.CaseRunID)
		}
		if observation.VersionID != 0 || observation.EnvironmentID != 0 {
			t.Fatalf("version/environment = %d/%d, want the repository to resolve them",
				observation.VersionID, observation.EnvironmentID)
		}
		if observation.ResourceTemplate == "" || strings.Contains(observation.ResourceTemplate, "?") {
			t.Fatalf("resource_template = %q, want a query-free normalized template", observation.ResourceTemplate)
		}
	}
}

// TestCaptureTerminalObservationsSkipsMalformedItem: per-item parsing keeps the
// good network entries when one entry is malformed (M6).
func TestCaptureTerminalObservationsSkipsMalformedItem(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	fixture.execution.networkLog = json.RawMessage(`[
	  {"method":"GET","url":"https://shop.example.com/home","status":200,"mime":"text/html","resource_type":"document"},
	  "not-an-item",
	  {"method":"GET","url":"https://shop.example.com/api/x","status":200,"mime":"application/json","resource_type":"fetch"}
	]`)
	finishDiscoveryRun(t, fixture, execution.CaseRunPassed)

	calls := fixture.discovery.calls
	if len(calls) != 2 {
		t.Fatalf("writer calls = %d, want one per active context", len(calls))
	}
	for _, call := range calls {
		if len(call) != 2 {
			t.Fatalf("observations = %d, want the 2 good items (bad item skipped): %+v", len(call), call)
		}
	}
}

// TestCaptureTerminalObservationsSkipsBlockedWebSocket: the executor's
// synthetic blocked-socket network entry (method WS) is not a real API
// observation, so the capture skips it (M1). Real websocket/eventsource
// resource types are still kept as api (M8).
func TestCaptureTerminalObservationsSkipsBlockedWebSocket(t *testing.T) {
	fixture := newDiscoveryFixture(t)
	fixture.execution.networkLog = json.RawMessage(`[
	  {"method":"WS","url":"wss://payments.example.com/socket","status":null,"duration_ms":0,"mime":null,"resource_type":"websocket","error":"host_not_allowed"},
	  {"method":"GET","url":"https://shop.example.com/home","status":200,"mime":"text/html","resource_type":"document"}
	]`)
	finishDiscoveryRun(t, fixture, execution.CaseRunPassed)

	for _, observation := range fixture.discovery.all() {
		if observation.Path != "/home" {
			t.Fatalf("blocked websocket leaked into observations: %+v", observation)
		}
	}
	if got := len(fixture.discovery.all()); got != 2 {
		t.Fatalf("observations = %d, want one page per active context", got)
	}
}
