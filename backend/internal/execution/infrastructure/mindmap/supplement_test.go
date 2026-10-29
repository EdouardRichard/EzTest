package mindmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// fakeRunStarter records supplement run starts.
type fakeRunStarter struct {
	run   mindmapdomain.CaseDesignRun
	err   error
	calls []mindmapapp.StartRunCommand
}

func (f *fakeRunStarter) Start(_ context.Context, _ int64, _ int64, cmd mindmapapp.StartRunCommand) (mindmapdomain.CaseDesignRun, error) {
	f.calls = append(f.calls, cmd)
	return f.run, f.err
}

// fakeBatchCreator records the child supplement batch creation.
type fakeBatchCreator struct {
	batch  *executiondomain.RunBatch
	err    error
	inputs []executionapp.CreateBatchCommand
}

func (f *fakeBatchCreator) Create(_ context.Context, _ int64, _ int64, cmd executionapp.CreateBatchCommand) (*executiondomain.RunBatch, error) {
	f.inputs = append(f.inputs, cmd)
	if f.err != nil {
		return nil, f.err
	}
	return f.batch, nil
}

// fakeMerger records the post-create merge call.
type fakeMerger struct{ caseIDs []int64 }

func (f *fakeMerger) MarkMerged(_ context.Context, _ int64, caseIDs []int64) error {
	f.caseIDs = append([]int64(nil), caseIDs...)
	return nil
}

// fakeRunByBatch returns one stored P7 run of a parent batch.
type fakeRunByBatch struct {
	run   mindmapdomain.CaseDesignRun
	found bool
	err   error
}

func (f fakeRunByBatch) LatestByBatch(context.Context, int64) (mindmapdomain.CaseDesignRun, bool, error) {
	return f.run, f.found, f.err
}

// TestPermanentSupplementCreateError pins the worker's degrade-vs-retry decision:
// only the two case-set validation reasons are permanent (retrying the same set
// can never succeed); every other failure must retry on the next tick.
func TestPermanentSupplementCreateError(t *testing.T) {
	cases := map[string]bool{
		"invalid_case_ids":  true,
		"too_many_cases":    true,
		"version_not_found": false,
		"batch_active":      false,
		"":                  false,
	}
	for reason, want := range cases {
		err := executiondomain.NewReasonError("invalid", reason, "message", false, nil)
		if got := permanentSupplementCreateError(err); got != want {
			t.Fatalf("reason %q: permanent=%v want %v", reason, got, want)
		}
	}
	if permanentSupplementCreateError(errors.New("mindmap unavailable")) {
		t.Fatal("a plain adapter error must not be treated as permanent")
	}
	if permanentSupplementCreateError(nil) {
		t.Fatal("nil must not be treated as permanent")
	}
	if !permanentSupplementCreateError(
		fmt.Errorf("wrapped: %w", executiondomain.NewReasonError("invalid", "invalid_case_ids", "", false, nil))) {
		t.Fatal("a wrapped permanent reason must still be detected")
	}
}

// TestSupplementPortStartMapsRunInProgress pins the 409 mapping: a concurrent
// run of the same kind/version becomes the worker's wait sentinel instead of an
// error that would end the chain.
func TestSupplementPortStartMapsRunInProgress(t *testing.T) {
	starter := &fakeRunStarter{err: mindmapdomain.NewConflict("run_in_progress", "busy")}
	port := NewSupplementPort(starter, nil, nil, nil)
	if _, err := port.StartSupplement(context.Background(), 7, 41, 0); !errors.Is(err, executionapp.ErrAutonomyRunInProgress) {
		t.Fatalf("err=%v want ErrAutonomyRunInProgress", err)
	}
	if len(starter.calls) != 1 || starter.calls[0].Kind != mindmapapp.RunKindSupplement || starter.calls[0].BatchID != 41 {
		t.Fatalf("start calls=%+v want one supplement run of batch 41", starter.calls)
	}
}

// TestSupplementPortStartsIncrementalDesignAndCreatesChildBatch covers the
// designing segment start and the child supplement batch (kind/parent/case ids).
func TestSupplementPortStartsIncrementalDesignAndCreatesChildBatch(t *testing.T) {
	starter := &fakeRunStarter{run: mindmapdomain.CaseDesignRun{ID: 91}}
	creator := &fakeBatchCreator{batch: &executiondomain.RunBatch{ID: 55}}
	merger := &fakeMerger{}
	port := NewSupplementPort(starter, creator, merger, nil)

	if _, err := port.StartIncrementalDesign(context.Background(), 7, 41, 0); err != nil {
		t.Fatalf("StartIncrementalDesign: %v", err)
	}
	if len(starter.calls) != 1 || starter.calls[0].Kind != mindmapapp.RunKindIncrementalDesign {
		t.Fatalf("start calls=%+v want one incremental_design run", starter.calls)
	}

	created, err := port.CreateSupplementBatch(context.Background(), 7, 41, []int64{11, 12}, 0)
	if err != nil {
		t.Fatalf("CreateSupplementBatch: %v", err)
	}
	if created == nil || created.ID != 55 {
		t.Fatalf("created=%+v want batch 55", created)
	}
	if len(creator.inputs) != 1 || creator.inputs[0].Kind != string(executiondomain.BatchKindSupplement) ||
		creator.inputs[0].ParentBatchID == nil || *creator.inputs[0].ParentBatchID != 41 ||
		len(creator.inputs[0].CaseIDs) != 2 {
		t.Fatalf("create inputs=%+v want supplement child of batch 41 with 2 cases", creator.inputs)
	}
	if err := port.MarkNodesMerged(context.Background(), 7, []int64{11, 12}, 0); err != nil {
		t.Fatalf("MarkNodesMerged: %v", err)
	}
	if len(merger.caseIDs) != 2 || merger.caseIDs[0] != 11 || merger.caseIDs[1] != 12 {
		t.Fatalf("merged case ids=%v want [11 12]", merger.caseIDs)
	}
}

// TestSupplementPortGetSupplementRunDecodesResult pins the by-batch lookup: both
// string and numeric newCaseIds are accepted and the degraded flag is surfaced.
func TestSupplementPortGetSupplementRunDecodesResult(t *testing.T) {
	result, err := json.Marshal(map[string]any{
		"newCaseIds": []any{"11", 12, 0},
		"degraded":   true,
		"escalations": []any{map[string]any{
			"title":          "Missing refund requirement",
			"detail":         "no requirement covers partial refunds",
			"requirementIds": []any{"21", 22},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	port := NewSupplementPort(nil, nil, nil, fakeRunByBatch{
		run:   mindmapdomain.CaseDesignRun{ID: 91, Kind: mindmapapp.RunKindSupplement, Status: "done", Result: result},
		found: true,
	})
	view, err := port.GetSupplementRun(context.Background(), 41)
	if err != nil {
		t.Fatalf("GetSupplementRun: %v", err)
	}
	if view == nil || view.ID != 91 || view.Kind != mindmapapp.RunKindSupplement || view.Status != "done" {
		t.Fatalf("view=%+v want run 91 supplement done", view)
	}
	if len(view.NewCaseIDs) != 2 || view.NewCaseIDs[0] != 11 || view.NewCaseIDs[1] != 12 {
		t.Fatalf("new case ids=%v want [11 12]", view.NewCaseIDs)
	}
	if !view.Degraded {
		t.Fatal("degraded flag was not surfaced")
	}
	if len(view.Escalations) != 1 || view.Escalations[0].Title != "Missing refund requirement" ||
		view.Escalations[0].Detail != "no requirement covers partial refunds" ||
		len(view.Escalations[0].RequirementIDs) != 2 ||
		view.Escalations[0].RequirementIDs[0] != 21 || view.Escalations[0].RequirementIDs[1] != 22 {
		t.Fatalf("escalations=%+v want the stored requirement gap", view.Escalations)
	}
}

// TestSupplementPortGetSupplementRunMissingIsNil covers the no-run case.
func TestSupplementPortGetSupplementRunMissingIsNil(t *testing.T) {
	port := NewSupplementPort(nil, nil, nil, fakeRunByBatch{found: false})
	view, err := port.GetSupplementRun(context.Background(), 41)
	if err != nil {
		t.Fatalf("GetSupplementRun: %v", err)
	}
	if view != nil {
		t.Fatalf("view=%+v want nil", view)
	}
}
