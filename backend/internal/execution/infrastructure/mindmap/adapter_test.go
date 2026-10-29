package mindmap

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

func ptr[T any](value T) *T { return &value }

// fakeExecutionService is the mindmap execution projection the adapter consumes.
type fakeExecutionService struct {
	baseline        mindmapdomain.ExecutionBaseline
	baselineErr     error
	cases           []mindmapdomain.ExecutionCase
	snapshotErr     error
	markErr         error
	markCalls       [][]int64
	markMergedErr   error
	markMergedCalls [][]int64
}

func (f *fakeExecutionService) Baseline(context.Context, int64) (mindmapdomain.ExecutionBaseline, error) {
	return f.baseline, f.baselineErr
}

func (f *fakeExecutionService) SnapshotCases(context.Context, int64, []int64) ([]mindmapdomain.ExecutionCase, error) {
	return f.cases, f.snapshotErr
}

func (f *fakeExecutionService) MarkExecuted(_ context.Context, _ int64, caseIDs []int64) error {
	f.markCalls = append(f.markCalls, append([]int64(nil), caseIDs...))
	return f.markErr
}

func (f *fakeExecutionService) MarkMerged(_ context.Context, _ int64, caseIDs []int64) error {
	f.markMergedCalls = append(f.markMergedCalls, append([]int64(nil), caseIDs...))
	return f.markMergedErr
}

func seededExecutionCase() mindmapdomain.ExecutionCase {
	return mindmapdomain.ExecutionCase{
		NodeID:        101,
		Revision:      4,
		Title:         "Checkout",
		Priority:      "P1",
		Preconditions: "signed in",
		TestData:      json.RawMessage(`{"sku":"A-1"}`),
		Steps: []mindmapdomain.ExecutionStep{
			{Seq: 2, Action: "pay", Expected: "paid", Data: `{"method":"card"}`},
			{Seq: 1, Action: "open", Expected: "shown", Data: ""},
		},
		Assertions: []mindmapdomain.ExecutionAssertion{
			{ID: 30, StepSeq: ptr(2), Kind: "ui", Target: "body", Expression: "paid", Expected: "paid"},
			{ID: 10, Kind: "ui", Target: "#total", Expression: "total > 0", Expected: "total"},
			{ID: 20, StepSeq: ptr(1), Kind: "ui", Target: "#list", Expression: "rows", Expected: "rows"},
		},
		RequirementIDs: []int64{9, 3, 9},
		GraphHash:      "graph-hash",
		Origin:         mindmapdomain.ExecutionOriginAISupplement,
		Status:         mindmapdomain.NodeStatusSupplemented,
	}
}

func TestBaselineMapsFrozenProjection(t *testing.T) {
	service := &fakeExecutionService{baseline: mindmapdomain.ExecutionBaseline{
		GraphHash:          "baseline-hash",
		CodeBaselineCommit: ptr("0123456789abcdef0123456789abcdef01234567"),
		CaseIDs:            []int64{3, 5},
	}}
	ref, err := New(service).Baseline(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Hash != "baseline-hash" || ref.CodeCommit == nil ||
		*ref.CodeCommit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("ref = %+v", ref)
	}
	if !reflect.DeepEqual(ref.CaseIDs, []int64{3, 5}) {
		t.Fatalf("case ids = %v", ref.CaseIDs)
	}

	*service.baseline.CodeBaselineCommit = "changed"
	service.baseline.CaseIDs[0] = 99
	if *ref.CodeCommit != "0123456789abcdef0123456789abcdef01234567" || ref.CaseIDs[0] != 3 {
		t.Fatalf("ref aliases the mindmap baseline: %+v", ref)
	}
}

func TestBaselineMapsBaselineRequired(t *testing.T) {
	service := &fakeExecutionService{
		baselineErr: mindmapdomain.NewStateConflict("baseline_required", "no baseline"),
	}
	_, err := New(service).Baseline(context.Background(), 7)
	if !errors.Is(err, executionapp.ErrBaselineRequired) {
		t.Fatalf("error = %v, want ErrBaselineRequired", err)
	}
}

func TestSnapshotCasesMapsExecutionCase(t *testing.T) {
	service := &fakeExecutionService{cases: []mindmapdomain.ExecutionCase{seededExecutionCase()}}
	views, err := New(service).SnapshotCases(context.Background(), 7, []int64{101})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	view := views[0]
	if view.CaseNodeID != 101 || view.Revision != 4 || view.GraphHash != "graph-hash" {
		t.Fatalf("view identity = %+v", view)
	}
	// The P7 gates read the provenance and the live node status from the view.
	if view.Origin != executiondomain.CaseSourceAISupplement {
		t.Fatalf("view origin = %q, want %q", view.Origin, executiondomain.CaseSourceAISupplement)
	}
	if view.Status != mindmapdomain.NodeStatusSupplemented {
		t.Fatalf("view status = %q, want %q", view.Status, mindmapdomain.NodeStatusSupplemented)
	}
	if view.Node.ID != 101 || view.Node.Title != "Checkout" || view.Node.Priority != "P1" ||
		view.Node.Preconditions != "signed in" {
		t.Fatalf("node = %+v", view.Node)
	}
	if !reflect.DeepEqual(view.Node.TestData, map[string]any{"sku": "A-1"}) {
		t.Fatalf("test data = %#v", view.Node.TestData)
	}
	if !reflect.DeepEqual(view.RequirementIDs, []int64{9, 3, 9}) {
		t.Fatalf("requirement ids = %v", view.RequirementIDs)
	}

	// Steps are ordered by seq, assertions by id; the step_seq of an assertion
	// is the 1-based case_step.seq and unattached assertions use 0.
	if len(view.Steps) != 2 || view.Steps[0].Seq != 1 || view.Steps[1].Seq != 2 {
		t.Fatalf("steps = %+v", view.Steps)
	}
	if view.Steps[0].Action != "open" || view.Steps[0].Data != nil {
		t.Fatalf("step 1 = %+v", view.Steps[0])
	}
	if !reflect.DeepEqual(view.Steps[1].Data, map[string]any{"method": "card"}) {
		t.Fatalf("step 2 data = %#v", view.Steps[1].Data)
	}
	if len(view.Assertions) != 3 ||
		view.Assertions[0].ID != 10 || view.Assertions[1].ID != 20 || view.Assertions[2].ID != 30 {
		t.Fatalf("assertions = %+v", view.Assertions)
	}
	if view.Assertions[0].StepSeq != 0 || view.Assertions[1].StepSeq != 1 || view.Assertions[2].StepSeq != 2 {
		t.Fatalf("assertion step seqs = %+v", view.Assertions)
	}
	if view.Assertions[2].Kind != "ui" || view.Assertions[2].Expression != "paid" {
		t.Fatalf("assertion projection = %+v", view.Assertions[2])
	}
}

func TestSnapshotCasesMapsInvalidIDs(t *testing.T) {
	service := &fakeExecutionService{
		snapshotErr: mindmapdomain.NewInvalid("caseIds", "invalid_case_ids", "foreign case"),
	}
	_, err := New(service).SnapshotCases(context.Background(), 7, []int64{99})
	if !errors.Is(err, executiondomain.ErrInvalidCaseIDs) {
		t.Fatalf("error = %v, want ErrInvalidCaseIDs", err)
	}
}

func TestMarkExecutedForwardsAndIsIdempotent(t *testing.T) {
	service := &fakeExecutionService{}
	adapter := New(service)
	ctx := context.Background()

	if err := adapter.MarkExecuted(ctx, 7, []int64{101, 102}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.MarkExecuted(ctx, 7, []int64{101, 102}); err != nil {
		t.Fatal(err)
	}
	if len(service.markCalls) != 2 ||
		!reflect.DeepEqual(service.markCalls[0], []int64{101, 102}) ||
		!reflect.DeepEqual(service.markCalls[1], []int64{101, 102}) {
		t.Fatalf("mark calls = %v", service.markCalls)
	}

	service.markCalls = nil
	if err := adapter.MarkExecuted(ctx, 7, nil); err != nil {
		t.Fatal(err)
	}
	if len(service.markCalls) != 0 {
		t.Fatalf("empty mark executed must not call the service: %v", service.markCalls)
	}
}

func TestMarkExecutedMapsStatusConflict(t *testing.T) {
	service := &fakeExecutionService{
		markErr: mindmapdomain.NewStateConflict("case_status_conflict", "case is draft"),
	}
	err := New(service).MarkExecuted(context.Background(), 7, []int64{101})

	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr.Code != "case_status_conflict" {
		t.Fatalf("error = %v", err)
	}
}

// TestMarkMergedForwardsAndSkipsEmpty covers the optional P7 SupplementGraphPort
// (spec §5.3): an adopted supplement batch marks its cases merged, and an empty
// selection never calls the mindmap service.
func TestMarkMergedForwardsAndSkipsEmpty(t *testing.T) {
	service := &fakeExecutionService{}
	adapter := New(service)
	ctx := context.Background()

	if err := adapter.MarkMerged(ctx, 7, []int64{101, 102}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.MarkMerged(ctx, 7, []int64{101, 102}); err != nil {
		t.Fatal(err)
	}
	if len(service.markMergedCalls) != 2 ||
		!reflect.DeepEqual(service.markMergedCalls[0], []int64{101, 102}) ||
		!reflect.DeepEqual(service.markMergedCalls[1], []int64{101, 102}) {
		t.Fatalf("mark merged calls = %v", service.markMergedCalls)
	}

	service.markMergedCalls = nil
	if err := adapter.MarkMerged(ctx, 7, nil); err != nil {
		t.Fatal(err)
	}
	if len(service.markMergedCalls) != 0 {
		t.Fatalf("empty mark merged must not call the service: %v", service.markMergedCalls)
	}
}

func TestMarkMergedMapsStatusConflict(t *testing.T) {
	service := &fakeExecutionService{
		markMergedErr: mindmapdomain.NewStateConflict("case_status_conflict", "case is reviewed"),
	}
	err := New(service).MarkMerged(context.Background(), 7, []int64{101})

	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr.Code != "case_status_conflict" {
		t.Fatalf("error = %v", err)
	}
}

func TestAdapterRejectsNilService(t *testing.T) {
	adapter := New(nil)
	if _, err := adapter.Baseline(context.Background(), 7); err == nil {
		t.Fatal("Baseline with a nil service = nil error")
	}
	if _, err := adapter.SnapshotCases(context.Background(), 7, nil); err == nil {
		t.Fatal("SnapshotCases with a nil service = nil error")
	}
	if err := adapter.MarkExecuted(context.Background(), 7, []int64{1}); err == nil {
		t.Fatal("MarkExecuted with a nil service = nil error")
	}
	if err := adapter.MarkMerged(context.Background(), 7, []int64{1}); err == nil {
		t.Fatal("MarkMerged with a nil service = nil error")
	}
}

var _ executionapp.MindmapPort = (*Adapter)(nil)
var _ executionapp.SupplementGraphPort = (*Adapter)(nil)
