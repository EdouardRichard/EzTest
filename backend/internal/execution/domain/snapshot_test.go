package domain_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func assertJSONKeys(t *testing.T, scope string, raw json.RawMessage, want ...string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("%s is not a JSON object: %v", scope, err)
	}
	got := make([]string, 0, len(fields))
	for key := range fields {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s keys = %v, want %v", scope, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s keys = %v, want %v", scope, got, want)
		}
	}
}

func TestCaseSnapshotJSONShapeAndRoundTrip(t *testing.T) {
	captured := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := domain.CaseSnapshot{
		Node: domain.CaseSnapshotNode{
			ID: 7, Title: "下单", Priority: "P1", Preconditions: "已登录",
			TestData: map[string]any{"sku": "A1"},
		},
		Steps: []domain.CaseSnapshotStep{
			{Seq: 1, Action: "打开列表", Expected: "列表可见", Data: map[string]any{"row": "2"}},
		},
		Assertions: []domain.CaseSnapshotAssertion{
			{ID: 9, StepSeq: 1, Kind: "ui", Target: "button", Expression: "text", Expected: "存在"},
		},
		Sources:    []domain.CaseSnapshotSource{{RequirementID: 42}},
		GraphHash:  strings.Repeat("a", 64),
		CapturedAt: captured,
	}

	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	assertJSONKeys(t, "snapshot", raw,
		"node", "steps", "assertions", "sources", "graph_hash", "captured_at")

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}

	assertJSONKeys(t, "node", top["node"],
		"id", "origin", "title", "priority", "preconditions", "test_data")
	var node map[string]json.RawMessage
	if err := json.Unmarshal(top["node"], &node); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if got, want := string(node["id"]), `"7"`; got != want {
		t.Fatalf("node.id = %s, want quoted decimal string %s (D29)", got, want)
	}

	var steps []json.RawMessage
	if err := json.Unmarshal(top["steps"], &steps); err != nil || len(steps) != 1 {
		t.Fatalf("decode steps: %v (len=%d)", err, len(steps))
	}
	assertJSONKeys(t, "step", steps[0], "seq", "action", "expected", "data")

	var assertions []json.RawMessage
	if err := json.Unmarshal(top["assertions"], &assertions); err != nil || len(assertions) != 1 {
		t.Fatalf("decode assertions: %v (len=%d)", err, len(assertions))
	}
	assertJSONKeys(t, "assertion", assertions[0],
		"id", "step_seq", "kind", "target", "expression", "expected")
	var assertion map[string]json.RawMessage
	if err := json.Unmarshal(assertions[0], &assertion); err != nil {
		t.Fatalf("decode assertion: %v", err)
	}
	if got, want := string(assertion["id"]), `"9"`; got != want {
		t.Fatalf("assertion.id = %s, want quoted decimal string %s (D29)", got, want)
	}

	var sources []json.RawMessage
	if err := json.Unmarshal(top["sources"], &sources); err != nil || len(sources) != 1 {
		t.Fatalf("decode sources: %v (len=%d)", err, len(sources))
	}
	assertJSONKeys(t, "source", sources[0], "requirement_id")
	var source map[string]json.RawMessage
	if err := json.Unmarshal(sources[0], &source); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	if got, want := string(source["requirement_id"]), `"42"`; got != want {
		t.Fatalf("source.requirement_id = %s, want quoted decimal string %s (D29)", got, want)
	}

	var back domain.CaseSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	roundTripped, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("re-marshal snapshot: %v", err)
	}
	if string(roundTripped) != string(raw) {
		t.Fatalf("round-trip mismatch:\n got %s\nwant %s", roundTripped, raw)
	}
	if !back.CapturedAt.Equal(captured) {
		t.Fatalf("captured_at round-trip = %v, want %v", back.CapturedAt, captured)
	}
}

// graphHashSnapshot is one case of a working-graph fixture.
func graphHashSnapshot(caseNodeID int64, title string, origin domain.CaseSource) domain.CaseSnapshot {
	return domain.CaseSnapshot{
		Node:       domain.CaseSnapshotNode{ID: caseNodeID, Title: title, Priority: "P1", Origin: origin},
		Steps:      []domain.CaseSnapshotStep{{Seq: 1, Action: "打开列表", Expected: "列表可见"}},
		Assertions: []domain.CaseSnapshotAssertion{{ID: caseNodeID * 10, StepSeq: 1, Kind: "ui", Expected: "存在"}},
		GraphHash:  strings.Repeat("b", 64),
		CapturedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
	}
}

func TestWorkingGraphHashStableAndContentSensitive(t *testing.T) {
	first := []domain.CaseSnapshot{
		graphHashSnapshot(11, "case 11", domain.CaseSourceBaseline),
		graphHashSnapshot(12, "case 12", domain.CaseSourceAISupplement),
	}
	second := []domain.CaseSnapshot{first[1], first[0]}
	second[0].CapturedAt = first[0].CapturedAt.Add(time.Hour)
	second[1].CapturedAt = first[1].CapturedAt.Add(2 * time.Hour)
	second[0].GraphHash = strings.Repeat("c", 64)
	second[1].GraphHash = strings.Repeat("d", 64)

	a := domain.WorkingGraphHash(7, first)
	b := domain.WorkingGraphHash(7, second)
	if a == "" || len(a) != 64 || a != strings.ToLower(a) {
		t.Fatalf("working graph hash = %q, want a lowercase 64-hex digest", a)
	}
	if a != b {
		t.Fatalf("working graph hash must ignore input order and snapshot metadata: %s != %s", a, b)
	}
	if got := domain.WorkingGraphHash(8, first); got == a {
		t.Fatal("working graph hash must depend on the version id")
	}

	changed := append([]domain.CaseSnapshot(nil), first...)
	changed[0].Node.Title = "renamed"
	if domain.WorkingGraphHash(7, changed) == a {
		t.Fatal("working graph hash must change when case content changes")
	}

	supplemented := append([]domain.CaseSnapshot(nil), first...)
	supplemented[0].Node.Origin = domain.CaseSourceAISupplement
	if domain.WorkingGraphHash(7, supplemented) == a {
		t.Fatal("working graph hash must change when a case origin changes")
	}

	if got := domain.WorkingGraphHash(7, nil); got != "" {
		t.Fatalf("empty graph hash = %q, want empty", got)
	}
}

func TestCaseSnapshotOriginDefaultsToBaseline(t *testing.T) {
	node := domain.CaseSnapshotNode{ID: 7}
	if got := node.EffectiveOrigin(); got != domain.CaseSourceBaseline {
		t.Fatalf("missing origin = %q, want baseline", got)
	}
	node.Origin = domain.CaseSourceAISupplement
	if got := node.EffectiveOrigin(); got != domain.CaseSourceAISupplement {
		t.Fatalf("supplement origin = %q", got)
	}
}

func TestCanonicalCaseHashDeterministicAndOrderSensitive(t *testing.T) {
	a := domain.CaseSnapshot{GraphHash: strings.Repeat("a", 64),
		Node:       domain.CaseSnapshotNode{ID: 7, Title: "下单"},
		Steps:      []domain.CaseSnapshotStep{{Seq: 1, Action: "打开列表"}},
		Assertions: []domain.CaseSnapshotAssertion{{ID: 9, Kind: "ui", Expected: "存在"}}}
	b := a
	// Copy the slice: a plain struct copy aliases the backing array, which
	// would mutate a as well and hide the change this test asserts.
	b.Steps = append([]domain.CaseSnapshotStep(nil), a.Steps...)
	if got, want := domain.CanonicalCaseHash(b), domain.CanonicalCaseHash(a); got != want {
		t.Fatalf("hash not deterministic: %s != %s", got, want)
	}
	b.Steps[0].Action = "打开详情"
	if domain.CanonicalCaseHash(b) == domain.CanonicalCaseHash(a) {
		t.Fatal("hash must change when a step changes")
	}
	if len(domain.CanonicalCaseHash(a)) != 64 {
		t.Fatal("hash must be 64 hex chars")
	}
	if got := domain.CanonicalCaseHash(a); got != strings.ToLower(got) {
		t.Fatalf("hash must be lowercase hex: %s", got)
	}
}

func TestCanonicalCaseHashStepOrderMatters(t *testing.T) {
	first := domain.CaseSnapshot{Steps: []domain.CaseSnapshotStep{
		{Seq: 1, Action: "打开列表"},
		{Seq: 2, Action: "点击新增"},
	}}
	second := domain.CaseSnapshot{Steps: []domain.CaseSnapshotStep{
		{Seq: 2, Action: "点击新增"},
		{Seq: 1, Action: "打开列表"},
	}}
	if domain.CanonicalCaseHash(first) == domain.CanonicalCaseHash(second) {
		t.Fatal("hash must change when step order changes")
	}
}
