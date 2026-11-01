package chainapp

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var errProviderBoom = errors.New("provider boom")

type fakeClock struct {
	now time.Time
}

func (c fakeClock) Now() time.Time { return c.now }

// fakeProviders implements every chain provider port, so one value can be
// handed to every constructor argument and records which ports were called.
type fakeProviders struct {
	requirements     []chain.Node
	versionSUTs      []chain.Node
	scenarios        []chain.Node
	cases            []chain.Node
	historical       map[int64]chain.Node
	waves            []chain.Node
	caseRuns         []chain.Node
	issues           []chain.Node
	reports          []chain.Node
	closures         []chain.Node
	sutNames         map[int64]string
	pageNames        map[int64]string
	cursor           int64
	eventTimes       map[int64]time.Time
	eventTimeSeqs    []int64
	eventTimeChannel string
	errs             map[string]error
	calls            []string
	versionIDs       map[string]int64
	requirementLimit int
	// requirementOverflow makes Requirements ignore limit, mimicking a provider
	// that returns a larger page than the chain service asked for.
	requirementOverflow bool
	historicalIDs       []int64
	cursorChannel       string
}

func newFakeProviders() *fakeProviders {
	return &fakeProviders{
		historical: map[int64]chain.Node{},
		sutNames:   map[int64]string{},
		pageNames:  map[int64]string{},
		eventTimes: map[int64]time.Time{},
		errs:       map[string]error{},
		versionIDs: map[string]int64{},
	}
}

func (f *fakeProviders) begin(operation string, versionID int64) error {
	f.calls = append(f.calls, operation)
	f.versionIDs[operation] = versionID
	return f.errs[operation]
}

func (f *fakeProviders) Requirements(ctx context.Context, versionID int64, limit int) ([]chain.Node, error) {
	if err := f.begin("requirements", versionID); err != nil {
		return nil, err
	}
	f.requirementLimit = limit
	// Mirror the real VersionProvider pagination: return at most limit roots.
	if !f.requirementOverflow && limit > 0 && len(f.requirements) > limit {
		return f.requirements[:limit], nil
	}
	return f.requirements, nil
}

func (f *fakeProviders) VersionSUTs(ctx context.Context, versionID int64) ([]chain.Node, error) {
	if err := f.begin("version_suts", versionID); err != nil {
		return nil, err
	}
	return f.versionSUTs, nil
}

func (f *fakeProviders) Scenarios(ctx context.Context, versionID int64) ([]chain.Node, error) {
	if err := f.begin("scenarios", versionID); err != nil {
		return nil, err
	}
	return f.scenarios, nil
}

func (f *fakeProviders) Cases(ctx context.Context, versionID int64) ([]chain.Node, error) {
	if err := f.begin("cases", versionID); err != nil {
		return nil, err
	}
	return f.cases, nil
}

func (f *fakeProviders) CaseVersionsByIDs(ctx context.Context, versionID int64, ids []int64) ([]chain.Node, error) {
	if err := f.begin("historical", versionID); err != nil {
		return nil, err
	}
	f.historicalIDs = append([]int64(nil), ids...)
	out := make([]chain.Node, 0, len(ids))
	for _, id := range ids {
		if node, ok := f.historical[id]; ok {
			out = append(out, node)
		}
	}
	return out, nil
}

func (f *fakeProviders) Waves(ctx context.Context, versionID int64) ([]chain.Node, error) {
	if err := f.begin("waves", versionID); err != nil {
		return nil, err
	}
	return f.waves, nil
}

func (f *fakeProviders) CaseRuns(ctx context.Context, versionID int64) ([]chain.Node, error) {
	if err := f.begin("case_runs", versionID); err != nil {
		return nil, err
	}
	return f.caseRuns, nil
}

func (f *fakeProviders) Issues(ctx context.Context, versionID int64) ([]chain.Node, error) {
	if err := f.begin("issues", versionID); err != nil {
		return nil, err
	}
	return f.issues, nil
}

func (f *fakeProviders) Reports(ctx context.Context, versionID int64) ([]chain.Node, error) {
	if err := f.begin("reports", versionID); err != nil {
		return nil, err
	}
	return f.reports, nil
}

func (f *fakeProviders) Closures(ctx context.Context, versionID int64) ([]chain.Node, error) {
	if err := f.begin("closures", versionID); err != nil {
		return nil, err
	}
	return f.closures, nil
}

func (f *fakeProviders) SUTNames(ctx context.Context, versionID int64) (map[int64]string, error) {
	if err := f.begin("sut_names", versionID); err != nil {
		return nil, err
	}
	return f.sutNames, nil
}

func (f *fakeProviders) PageNames(ctx context.Context, versionID int64) (map[int64]string, error) {
	if err := f.begin("page_names", versionID); err != nil {
		return nil, err
	}
	return f.pageNames, nil
}

func (f *fakeProviders) LatestCursor(ctx context.Context, channel string) (int64, error) {
	f.calls = append(f.calls, "cursor")
	f.cursorChannel = channel
	if err := f.errs["cursor"]; err != nil {
		return 0, err
	}
	return f.cursor, nil
}

func (f *fakeProviders) EventTime(ctx context.Context, channel string, seq int64) (time.Time, bool, error) {
	f.calls = append(f.calls, "event_time")
	f.eventTimeChannel = channel
	f.eventTimeSeqs = append(f.eventTimeSeqs, seq)
	if err := f.errs["event_time"]; err != nil {
		return time.Time{}, false, err
	}
	at, ok := f.eventTimes[seq]
	return at, ok, nil
}

func chainNode(kind chain.NodeKind, refID int64, label string) chain.Node {
	return chain.Node{
		ID:    chain.NodeID(kind, refID),
		Kind:  kind,
		RefID: strconv.FormatInt(refID, 10),
		Label: label,
	}
}

func ptr(value int64) *int64 { return &value }

func completeFakes() *fakeProviders {
	fakes := newFakeProviders()

	requirement := chainNode(chain.NodeRequirement, 1, "Checkout")
	requirement.Status = "clarified"
	requirement.Meta = map[string]any{"target_version_sut_ids": []int64{5}}

	scenario := chainNode(chain.NodeScenario, 10, "Happy path")
	scenario.RequirementID = ptr(1)

	caseVersion := chainNode(chain.NodeCaseVersion, 100, "Pays with card")
	caseVersion.RequirementID = ptr(1)
	caseVersion.ScenarioID = ptr(10)
	caseVersion.VersionSUTID = ptr(5)
	caseVersion.VersionSUTIDs = []int64{5}
	caseVersion.PageID = ptr(50)
	caseVersion.Status = "confirmed"
	caseVersion.Meta = map[string]any{
		"page_ids": []int64{50},
	}

	wave := chainNode(chain.NodeWave, 7, "Wave 1")
	wave.Status = "completed"

	caseRun := chainNode(chain.NodeCaseRun, 200, "Pays with card")
	caseRun.CaseVersionID = ptr(100)
	caseRun.WaveID = ptr(7)
	caseRun.VersionSUTIDs = []int64{5}
	caseRun.Status = "done"
	caseRun.Meta = map[string]any{"evidence_count": 2}

	issue := chainNode(chain.NodeIssue, 300, "Amount mismatch")
	issue.RequirementID = ptr(1)
	issue.CaseRunID = ptr(200)
	issue.WaveID = ptr(7)
	issue.ReportID = ptr(900)
	issue.Status = "open"

	report := chainNode(chain.NodeReport, 900, "Wave 1 report")
	report.WaveID = ptr(7)
	report.Status = "published"

	closure := chainNode(chain.NodeClosure, 400, "Closed")
	closure.RequirementID = ptr(1)

	fakes.requirements = []chain.Node{requirement}
	fakes.versionSUTs = []chain.Node{{
		ID:    "version_sut:5",
		RefID: "5",
		Label: "Pay (version binding)",
	}}
	fakes.scenarios = []chain.Node{scenario}
	fakes.cases = []chain.Node{caseVersion}
	fakes.waves = []chain.Node{wave}
	fakes.caseRuns = []chain.Node{caseRun}
	fakes.issues = []chain.Node{issue}
	fakes.reports = []chain.Node{report}
	fakes.closures = []chain.Node{closure}
	fakes.sutNames = map[int64]string{5: "Pay"}
	fakes.pageNames = map[int64]string{50: "Checkout"}
	fakes.cursor = 123
	return fakes
}

func newService(fakes *fakeProviders, now time.Time) *ChainService {
	return NewChainService(fakes, fakes, fakes, fakes, fakes, fakes, fakes, fakeClock{now: now})
}

// newServiceWithBudget wires the same fakes with an explicit chain budget, the
// way P4 settings wiring (task 23) constructs the service.
func newServiceWithBudget(fakes *fakeProviders, now time.Time, budget chain.Budget) *ChainService {
	return NewChainService(fakes, fakes, fakes, fakes, fakes, fakes, fakes, fakeClock{now: now}, budget)
}

// requirementRoots returns count bare requirement roots. Their zero timestamps
// make the builder's tie-break select the highest ref IDs.
func requirementRoots(count int) []chain.Node {
	nodes := make([]chain.Node, 0, count)
	for id := int64(1); id <= int64(count); id++ {
		nodes = append(nodes, chainNode(chain.NodeRequirement, id, "Requirement "+strconv.FormatInt(id, 10)))
	}
	return nodes
}

func TestChainServiceReadAssemblesView(t *testing.T) {
	fakes := completeFakes()
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	service := newService(fakes, now)

	view, err := service.Read(context.Background(), 42, chain.ChainFilter{
		Status:    " Open ",
		FocusNode: " issue:300 ",
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if view.VersionID != "42" {
		t.Errorf("VersionID = %q, want 42", view.VersionID)
	}
	if view.SnapshotCursor != 123 {
		t.Errorf("SnapshotCursor = %d, want 123", view.SnapshotCursor)
	}
	if !view.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v, want %v", view.GeneratedAt, now)
	}
	if view.Filters.Status != "open" || view.Filters.FocusNode != "issue:300" {
		t.Errorf("Filters = %+v, want the normalized filter", view.Filters)
	}
	if view.Replay.Mode != chain.ReplayModeLive || !view.Replay.Approximate {
		t.Errorf("Replay = %+v, want live/approximate", view.Replay)
	}

	nodes := make(map[string]chain.Node, len(view.Nodes))
	for _, node := range view.Nodes {
		nodes[node.ID] = node
	}
	for _, id := range []string{
		"requirement:1", "scenario:10", "case_version:100",
		"wave:7", "case_run:200", "issue:300",
	} {
		if _, ok := nodes[id]; !ok {
			t.Errorf("node %q missing after filter+focus: %v", id, view.Nodes)
		}
	}
	for _, id := range []string{"report:900", "closure:400"} {
		if _, ok := nodes[id]; ok {
			t.Errorf("filtered node %q unexpectedly kept", id)
		}
	}

	edges := make(map[string]bool, len(view.Edges))
	for _, edge := range view.Edges {
		edges[string(edge.Kind)+"|"+edge.From+"|"+edge.To] = true
	}
	for _, want := range []string{
		"has_scenario|requirement:1|scenario:10",
		"has_case|scenario:10|case_version:100",
		"executed_by|case_version:100|case_run:200",
		"contains|wave:7|case_run:200",
		"raised|case_run:200|issue:300",
	} {
		if !edges[want] {
			t.Errorf("edge %q missing from %v", want, view.Edges)
		}
	}

	caseNode := nodes["case_version:100"]
	if got := caseNode.Meta["sut_labels"]; !slices.Equal(got.([]string), []string{"Pay"}) {
		t.Errorf("case sut_labels = %v, want [Pay] from ResourceChainProvider", got)
	}
	if got := caseNode.Meta["page_labels"]; !slices.Equal(got.([]string), []string{"Checkout"}) {
		t.Errorf("case page_labels = %v, want [Checkout]", got)
	}

	for _, operation := range []string{
		"requirements", "version_suts", "scenarios", "cases", "waves",
		"case_runs", "issues", "reports", "closures", "sut_names", "page_names", "cursor",
	} {
		if !slices.Contains(fakes.calls, operation) {
			t.Errorf("provider operation %q was not called (calls=%v)", operation, fakes.calls)
		}
		if operation != "cursor" && fakes.versionIDs[operation] != 42 {
			t.Errorf("%s got versionID %d, want 42", operation, fakes.versionIDs[operation])
		}
	}
	if slices.Contains(fakes.calls, "historical") {
		t.Errorf("historical reader called without a missing case version: %v", fakes.calls)
	}
	if fakes.requirementLimit != chain.DefaultMaxRequirements+1 {
		t.Errorf("Requirements limit = %d, want MaxRequirements+1 = %d", fakes.requirementLimit, chain.DefaultMaxRequirements+1)
	}
	if fakes.cursorChannel != "version:42" {
		t.Errorf("cursor channel = %q, want version:42", fakes.cursorChannel)
	}
}

// TestChainServiceRequirementBudgetTruncation covers the end-to-end path of the
// requirement root budget: the service asks the provider for one root more than
// chain.max_requirements so BuildChain can truncate and report
// requirement_budget instead of silently serving exactly the budget page.
func TestChainServiceRequirementBudgetTruncation(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	t.Run("requests one root beyond the budget and reports truncation", func(t *testing.T) {
		fakes := newFakeProviders()
		fakes.cursor = 7
		fakes.requirements = requirementRoots(chain.DefaultMaxRequirements + 1)

		view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{})
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}

		if got := fakes.requirementLimit; got != chain.DefaultMaxRequirements+1 {
			t.Errorf("Requirements limit = %d, want MaxRequirements+1 = %d", got, chain.DefaultMaxRequirements+1)
		}
		if !view.Truncated || view.Truncation.Reason != chain.TruncationReasonRequirementBudget {
			t.Fatalf("truncation = %+v, want requirement_budget", view.Truncation)
		}
		if got := view.Truncation.MaxRequirements; got != chain.DefaultMaxRequirements {
			t.Errorf("Truncation.MaxRequirements = %d, want %d", got, chain.DefaultMaxRequirements)
		}
		if got := len(view.Nodes); got != chain.DefaultMaxRequirements {
			t.Errorf("nodes = %d, want the %d selected requirements", got, chain.DefaultMaxRequirements)
		}
		if got := view.Counts["requirements"]; got != chain.DefaultMaxRequirements+1 {
			t.Errorf("Counts[requirements] = %d, want the provider page %d before truncation", got, chain.DefaultMaxRequirements+1)
		}
		nodes := make(map[string]bool, len(view.Nodes))
		for _, node := range view.Nodes {
			nodes[node.ID] = true
		}
		if nodes[chain.NodeID(chain.NodeRequirement, 1)] {
			t.Errorf("oldest requirement survived the root budget: %v", view.Nodes)
		}
		if !nodes[chain.NodeID(chain.NodeRequirement, int64(chain.DefaultMaxRequirements+1))] {
			t.Errorf("newest requirement missing after the root budget: %v", view.Nodes)
		}
	})

	t.Run("a provider returning more than the requested page is still truncated", func(t *testing.T) {
		fakes := newFakeProviders()
		fakes.cursor = 7
		fakes.requirementOverflow = true
		fakes.requirements = requirementRoots(chain.DefaultMaxRequirements + 5)

		view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{})
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if got := fakes.requirementLimit; got != chain.DefaultMaxRequirements+1 {
			t.Errorf("Requirements limit = %d, want MaxRequirements+1 = %d", got, chain.DefaultMaxRequirements+1)
		}
		if !view.Truncated || view.Truncation.Reason != chain.TruncationReasonRequirementBudget {
			t.Fatalf("truncation = %+v, want requirement_budget", view.Truncation)
		}
		if got := len(view.Nodes); got != chain.DefaultMaxRequirements {
			t.Errorf("nodes = %d, want %d", got, chain.DefaultMaxRequirements)
		}
		if got := view.Counts["requirements"]; got != chain.DefaultMaxRequirements+5 {
			t.Errorf("Counts[requirements] = %d, want the full provider page %d", got, chain.DefaultMaxRequirements+5)
		}
	})
}

// TestChainServiceUsesConfiguredBudget pins the constructor budget: the service
// must apply the configured chain.max_nodes/chain.max_edges/
// chain.max_requirements instead of ClampBudget(Budget{}), and ChainFilter.Limit
// stays a lower-only override of the configured node budget. Task 23 passes the
// settings through the same constructor.
func TestChainServiceUsesConfiguredBudget(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	fakes := completeFakes()
	service := newServiceWithBudget(fakes, now, chain.Budget{MaxNodes: 3, MaxEdges: 2, MaxRequirements: 2})
	view, err := service.Read(context.Background(), 42, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if view.Truncation.MaxNodes != 3 || view.Truncation.MaxEdges != 2 || view.Truncation.MaxRequirements != 2 {
		t.Errorf("Truncation = %+v, want the configured maxima nodes=3 edges=2 requirements=2", view.Truncation)
	}
	if !view.Truncated || view.Truncation.Reason != chain.TruncationReasonNodeBudget {
		t.Errorf("truncation = %+v, want node_budget", view.Truncation)
	}
	if got := len(view.Nodes); got != 3 {
		t.Errorf("nodes = %d, want the configured node budget 3: %v", got, view.Nodes)
	}
	if got := fakes.requirementLimit; got != 3 {
		t.Errorf("Requirements limit = %d, want MaxRequirements+1 = 3", got)
	}

	lower := completeFakes()
	lowered, err := newServiceWithBudget(lower, now, chain.Budget{MaxNodes: 5}).
		Read(context.Background(), 42, chain.ChainFilter{Limit: 50})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := lowered.Truncation.MaxNodes; got != 5 {
		t.Errorf("Truncation.MaxNodes = %d, want the configured budget 5 (limit is lower-only)", got)
	}
	if got := lower.requirementLimit; got != chain.DefaultMaxRequirements+1 {
		t.Errorf("Requirements limit = %d, want the default MaxRequirements+1 = %d", got, chain.DefaultMaxRequirements+1)
	}
}

func TestChainServiceLoadsHistoricalCaseVersions(t *testing.T) {
	fakes := completeFakes()
	historical := chainNode(chain.NodeCaseVersion, 101, "Superseded title")
	historical.Status = "superseded"
	historical.ScenarioID = ptr(10)
	historical.RequirementID = ptr(1)
	fakes.historical[101] = historical

	supersededRun := chainNode(chain.NodeCaseRun, 201, "Superseded title")
	supersededRun.CaseVersionID = ptr(101)
	supersededRun.WaveID = ptr(7)
	supersededRun.Status = "done"
	fakes.caseRuns = append(fakes.caseRuns, supersededRun)

	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if !slices.Equal(fakes.historicalIDs, []int64{101}) {
		t.Errorf("historical ids = %v, want [101]", fakes.historicalIDs)
	}
	nodes := make(map[string]chain.Node, len(view.Nodes))
	for _, node := range view.Nodes {
		nodes[node.ID] = node
	}
	if node, ok := nodes["case_version:101"]; !ok {
		t.Fatalf("superseded case version missing: %v", view.Nodes)
	} else if node.Label != "Superseded title" {
		t.Errorf("historical case label = %q, want Superseded title", node.Label)
	}
	found := false
	for _, edge := range view.Edges {
		if edge.Kind == chain.EdgeExecutedBy && edge.From == "case_version:101" && edge.To == "case_run:201" {
			found = true
		}
	}
	if !found {
		t.Errorf("executed_by edge for the historical case version missing: %v", view.Edges)
	}
}

func TestChainServiceErrors(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	t.Run("rejects a non-positive version id before calling providers", func(t *testing.T) {
		fakes := completeFakes()
		if _, err := newService(fakes, now).Read(context.Background(), 0, chain.ChainFilter{}); err == nil {
			t.Fatal("Read(versionID=0) error = nil, want an error")
		}
		if len(fakes.calls) != 0 {
			t.Errorf("providers were called for an invalid version id: %v", fakes.calls)
		}
	})

	tests := []struct {
		name      string
		operation string
		mutate    func(*fakeProviders)
	}{
		{name: "requirements", operation: "requirements", mutate: func(f *fakeProviders) {
			f.errs["requirements"] = errProviderBoom
		}},
		{name: "version suts", operation: "version_suts", mutate: func(f *fakeProviders) {
			f.errs["version_suts"] = errProviderBoom
		}},
		{name: "scenarios", operation: "scenarios", mutate: func(f *fakeProviders) {
			f.errs["scenarios"] = errProviderBoom
		}},
		{name: "cases", operation: "cases", mutate: func(f *fakeProviders) {
			f.errs["cases"] = errProviderBoom
		}},
		{name: "waves", operation: "waves", mutate: func(f *fakeProviders) {
			f.errs["waves"] = errProviderBoom
		}},
		{name: "case runs", operation: "case_runs", mutate: func(f *fakeProviders) {
			f.errs["case_runs"] = errProviderBoom
		}},
		{name: "issues", operation: "issues", mutate: func(f *fakeProviders) {
			f.errs["issues"] = errProviderBoom
		}},
		{name: "reports", operation: "reports", mutate: func(f *fakeProviders) {
			f.errs["reports"] = errProviderBoom
		}},
		{name: "closures", operation: "closures", mutate: func(f *fakeProviders) {
			f.errs["closures"] = errProviderBoom
		}},
		{name: "SUT names", operation: "sut_names", mutate: func(f *fakeProviders) {
			f.errs["sut_names"] = errProviderBoom
		}},
		{name: "page names", operation: "page_names", mutate: func(f *fakeProviders) {
			f.errs["page_names"] = errProviderBoom
		}},
		{name: "cursor", operation: "cursor", mutate: func(f *fakeProviders) {
			f.errs["cursor"] = errProviderBoom
		}},
		{name: "historical case versions", operation: "historical", mutate: func(f *fakeProviders) {
			f.caseRuns = append(f.caseRuns, chain.Node{
				ID:            "case_run:202",
				Kind:          chain.NodeCaseRun,
				RefID:         "202",
				CaseVersionID: ptr(102),
				WaveID:        ptr(7),
			})
			f.errs["historical"] = errProviderBoom
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakes := completeFakes()
			tc.mutate(fakes)
			_, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{})
			if err == nil {
				t.Fatalf("Read() error = nil, want the %s provider error", tc.operation)
			}
			if !errors.Is(err, errProviderBoom) {
				t.Fatalf("Read() error = %v, want errors.Is(errProviderBoom)", err)
			}
			if !slices.Contains(fakes.calls, tc.operation) {
				t.Errorf("provider operation %q was not attempted: %v", tc.operation, fakes.calls)
			}
		})
	}
}

func TestChainServiceReplayModes(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		filter     chain.ChainFilter
		wantMode   string
		wantWave   *int64
		wantFrom   *int64
		wantTo     *int64
		wantFilter chain.ChainFilter
	}{
		{
			name:       "live",
			filter:     chain.ChainFilter{},
			wantMode:   chain.ReplayModeLive,
			wantFilter: chain.ChainFilter{},
		},
		{
			name:       "wave",
			filter:     chain.ChainFilter{WaveID: ptr(7)},
			wantMode:   chain.ReplayModeWave,
			wantWave:   ptr(7),
			wantFilter: chain.ChainFilter{WaveID: ptr(7)},
		},
		{
			name:       "range",
			filter:     chain.ChainFilter{EventFrom: ptr(9), EventTo: ptr(5)},
			wantMode:   chain.ReplayModeRange,
			wantFrom:   ptr(5),
			wantTo:     ptr(9),
			wantFilter: chain.ChainFilter{EventFrom: ptr(5), EventTo: ptr(9)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakes := newFakeProviders()
			fakes.cursor = 7
			fakes.eventTimes[5] = now.Add(-time.Hour)
			fakes.eventTimes[9] = now.Add(time.Hour)
			view, err := newService(fakes, now).Read(context.Background(), 42, tc.filter)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if view.Replay.Mode != tc.wantMode {
				t.Errorf("Replay.Mode = %q, want %q", view.Replay.Mode, tc.wantMode)
			}
			if !view.Replay.Approximate {
				t.Errorf("Replay.Approximate = false, want true")
			}
			if !samePtr(view.Replay.WaveID, tc.wantWave) ||
				!samePtr(view.Replay.EventFrom, tc.wantFrom) ||
				!samePtr(view.Replay.EventTo, tc.wantTo) {
				t.Errorf("Replay = %+v, want wave=%v from=%v to=%v", view.Replay, tc.wantWave, tc.wantFrom, tc.wantTo)
			}
			if !sameFilter(view.Filters, tc.wantFilter) {
				t.Errorf("Filters = %+v, want %+v", view.Filters, tc.wantFilter)
			}
		})
	}
}

func TestChainServiceEventRangeFiltersNodes(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 10, 0, 0, time.UTC)
	base := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	fakes := completeFakes()
	// seq 5 -> base+1m (lower bound), seq 7 -> base+4m (upper bound).
	fakes.eventTimes[5] = base.Add(time.Minute)
	fakes.eventTimes[7] = base.Add(4 * time.Minute)

	fakes.requirements[0].CreatedAt = base
	fakes.requirements[0].UpdatedAt = base
	fakes.scenarios[0].CreatedAt = base.Add(time.Minute)
	fakes.scenarios[0].UpdatedAt = base.Add(time.Minute)
	fakes.cases[0].CreatedAt = base.Add(2 * time.Minute)
	fakes.cases[0].UpdatedAt = base.Add(2 * time.Minute)
	fakes.caseRuns[0].CreatedAt = base.Add(3 * time.Minute)
	fakes.caseRuns[0].UpdatedAt = base.Add(3 * time.Minute)
	fakes.waves[0].CreatedAt = base.Add(4 * time.Minute)
	fakes.waves[0].UpdatedAt = base.Add(4 * time.Minute)
	fakes.issues[0].CreatedAt = base.Add(5 * time.Minute)
	fakes.issues[0].UpdatedAt = base.Add(5 * time.Minute)
	fakes.reports[0].CreatedAt = time.Time{} // zero timestamps are always kept
	fakes.reports[0].UpdatedAt = time.Time{}
	fakes.closures[0].CreatedAt = base.Add(5 * time.Minute)
	fakes.closures[0].UpdatedAt = base.Add(5 * time.Minute)

	view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{
		EventFrom: ptr(5),
		EventTo:   ptr(7),
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if view.Replay.Mode != chain.ReplayModeRange || !view.Replay.Approximate {
		t.Errorf("Replay = %+v, want range/approximate", view.Replay)
	}
	if !samePtr(view.Replay.EventFrom, ptr(5)) || !samePtr(view.Replay.EventTo, ptr(7)) {
		t.Errorf("Replay bounds = %v/%v, want 5/7", view.Replay.EventFrom, view.Replay.EventTo)
	}
	if fakes.eventTimeChannel != "version:42" {
		t.Errorf("event time channel = %q, want version:42", fakes.eventTimeChannel)
	}
	if !slices.Equal(fakes.eventTimeSeqs, []int64{5, 7}) {
		t.Errorf("event time seqs = %v, want [5 7]", fakes.eventTimeSeqs)
	}

	nodes := make(map[string]chain.Node, len(view.Nodes))
	for _, node := range view.Nodes {
		nodes[node.ID] = node
	}
	for _, id := range []string{
		"scenario:10", "case_version:100", "wave:7", "case_run:200", "report:900",
	} {
		if _, ok := nodes[id]; !ok {
			t.Errorf("node %q missing from the event range: %v", id, view.Nodes)
		}
	}
	for _, id := range []string{"requirement:1", "issue:300", "closure:400"} {
		if _, ok := nodes[id]; ok {
			t.Errorf("node %q outside the event range was not filtered", id)
		}
	}
	if got := view.Counts["requirements"]; got != 0 {
		t.Errorf("Counts[requirements] = %d, want 0", got)
	}
	if got := view.Counts["reports"]; got != 1 {
		t.Errorf("Counts[reports] = %d, want the zero-timestamp report", got)
	}

	edges := make(map[string]bool, len(view.Edges))
	for _, edge := range view.Edges {
		edges[string(edge.Kind)+"|"+edge.From+"|"+edge.To] = true
	}
	for _, want := range []string{
		"has_case|scenario:10|case_version:100",
		"executed_by|case_version:100|case_run:200",
		"contains|wave:7|case_run:200",
		"reports|wave:7|report:900",
	} {
		if !edges[want] {
			t.Errorf("edge %q missing from the induced range graph: %v", want, view.Edges)
		}
	}
	for _, unwanted := range []string{
		"has_scenario|requirement:1|scenario:10",
		"raised|case_run:200|issue:300",
		"closed_by|requirement:1|closure:400",
	} {
		if edges[unwanted] {
			t.Errorf("edge %q survived with a filtered endpoint", unwanted)
		}
	}
}

func TestChainServiceEventRangeOneSidedAndZeroUnset(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 10, 0, 0, time.UTC)
	base := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	t.Run("zero event bounds are unset", func(t *testing.T) {
		fakes := completeFakes()
		view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{
			EventFrom: ptr(0),
			EventTo:   ptr(0),
		})
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if view.Replay.Mode != chain.ReplayModeLive {
			t.Errorf("Replay.Mode = %q, want live for zero bounds", view.Replay.Mode)
		}
		if view.Filters.EventFrom != nil || view.Filters.EventTo != nil {
			t.Errorf("Filters event bounds = %v/%v, want nil/nil", view.Filters.EventFrom, view.Filters.EventTo)
		}
		if slices.Contains(fakes.calls, "event_time") {
			t.Errorf("event time reader was called for unset bounds: %v", fakes.calls)
		}
	})

	t.Run("event_from alone filters only the lower bound", func(t *testing.T) {
		fakes := completeFakes()
		fakes.eventTimes[5] = base.Add(2 * time.Minute)
		fakes.requirements[0].CreatedAt = base.Add(time.Minute)
		fakes.issues[0].CreatedAt = base.Add(3 * time.Minute)
		fakes.reports[0].CreatedAt = base.Add(3 * time.Minute)
		fakes.scenarios[0].CreatedAt = base.Add(3 * time.Minute)
		fakes.cases[0].CreatedAt = base.Add(3 * time.Minute)
		fakes.waves[0].CreatedAt = base.Add(3 * time.Minute)
		fakes.caseRuns[0].CreatedAt = base.Add(3 * time.Minute)
		fakes.closures[0].CreatedAt = base.Add(3 * time.Minute)

		view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{EventFrom: ptr(5)})
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if view.Replay.Mode != chain.ReplayModeRange || !view.Replay.Approximate {
			t.Errorf("Replay = %+v, want range/approximate", view.Replay)
		}
		if !samePtr(view.Replay.EventFrom, ptr(5)) || view.Replay.EventTo != nil {
			t.Errorf("Replay bounds = %v/%v, want 5/nil", view.Replay.EventFrom, view.Replay.EventTo)
		}
		nodes := make(map[string]bool, len(view.Nodes))
		for _, node := range view.Nodes {
			nodes[node.ID] = true
		}
		if nodes["requirement:1"] {
			t.Errorf("requirement before the lower bound survived")
		}
		if !nodes["issue:300"] || !nodes["report:900"] {
			t.Errorf("upper-unbounded nodes missing: %v", view.Nodes)
		}
		if !slices.Equal(fakes.eventTimeSeqs, []int64{5}) {
			t.Errorf("event time seqs = %v, want [5]", fakes.eventTimeSeqs)
		}
	})
}

func TestChainServiceRejectsInvalidEventBounds(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	t.Run("negative bound", func(t *testing.T) {
		fakes := completeFakes()
		_, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{EventFrom: ptr(-1)})
		if err == nil {
			t.Fatal("Read() error = nil, want an invalid event bound error")
		}
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 400 {
			t.Fatalf("error = %v, want a 400 platform error", err)
		}
		if len(fakes.calls) != 0 {
			t.Errorf("providers were called for an invalid event bound: %v", fakes.calls)
		}
	})

	t.Run("unknown event seq", func(t *testing.T) {
		fakes := completeFakes()
		_, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{
			EventFrom: ptr(5),
			EventTo:   ptr(7),
		})
		if err == nil {
			t.Fatal("Read() error = nil, want an unknown event seq error")
		}
		if !slices.Equal(fakes.eventTimeSeqs, []int64{5}) {
			t.Errorf("event time seqs = %v, want [5] before rejecting the missing bound", fakes.eventTimeSeqs)
		}
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 400 {
			t.Fatalf("error = %v, want a 400 platform error", err)
		}
	})

	t.Run("event reader failure", func(t *testing.T) {
		fakes := completeFakes()
		fakes.eventTimes[5] = now
		fakes.errs["event_time"] = errProviderBoom
		_, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{EventFrom: ptr(5)})
		if !errors.Is(err, errProviderBoom) {
			t.Fatalf("Read() error = %v, want the event reader error", err)
		}
	})
}

func TestChainServiceLimitOverride(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	fakes := newFakeProviders()
	fakes.cursor = 7

	view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{Limit: 50})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := view.Truncation.MaxNodes; got != 50 {
		t.Errorf("Truncation.MaxNodes = %d, want the filter limit 50", got)
	}
	if view.Filters.Limit != 50 {
		t.Errorf("Filters.Limit = %d, want 50", view.Filters.Limit)
	}

	capped, err := newService(newFakeProviders(), now).Read(context.Background(), 42, chain.ChainFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := capped.Truncation.MaxNodes; got != chain.DefaultMaxNodes {
		t.Errorf("Truncation.MaxNodes = %d, want the configured budget %d for limit 1000", got, chain.DefaultMaxNodes)
	}

	invalid, err := newService(newFakeProviders(), now).Read(context.Background(), 42, chain.ChainFilter{Limit: 5000})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := invalid.Truncation.MaxNodes; got != chain.DefaultMaxNodes {
		t.Errorf("Truncation.MaxNodes = %d, want the default %d", got, chain.DefaultMaxNodes)
	}
}

func sameFilter(left, right chain.ChainFilter) bool {
	return samePtr(left.VersionSUTID, right.VersionSUTID) &&
		samePtr(left.PageID, right.PageID) &&
		samePtr(left.WaveID, right.WaveID) &&
		left.Status == right.Status &&
		left.Result == right.Result &&
		left.FocusNode == right.FocusNode &&
		samePtr(left.EventFrom, right.EventFrom) &&
		samePtr(left.EventTo, right.EventTo) &&
		left.Limit == right.Limit
}

func samePtr(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
