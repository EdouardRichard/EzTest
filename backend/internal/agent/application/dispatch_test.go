package agentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- dispatch fakes ----------

// fakeWorkItemContractRepo is an in-memory WorkItemContractRepository. Only
// CreateWorkItem is used by the dispatcher tests; the rest of the 0.2.1
// contract is stubbed so the type satisfies the port.
type fakeWorkItemContractRepo struct {
	mu      sync.Mutex
	nextID  int64
	items   []agent.WorkItem
	byKey   map[string]agent.WorkItem
	creates int
}

func newFakeWorkItemContractRepo() *fakeWorkItemContractRepo {
	return &fakeWorkItemContractRepo{byKey: map[string]agent.WorkItem{}}
}

func (r *fakeWorkItemContractRepo) CreateWorkItem(_ context.Context, in agent.CreateWorkItemParams) (agent.WorkItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates++
	key := ""
	if in.IdempotencyKey != "" {
		key = fmt.Sprintf("%d/%s/%s", in.VersionID, in.Kind, in.IdempotencyKey)
		if existing, ok := r.byKey[key]; ok {
			return existing, nil
		}
	}
	r.nextID++
	item := agent.WorkItem{
		ID:        r.nextID,
		VersionID: in.VersionID,
		Kind:      in.Kind,
		Goal:      in.Goal,
		Target:    in.Target,
		Inputs:    in.Inputs,
		Expected:  in.Expected,
		Budget:    in.Budget,
		Status:    agent.WorkItemOpen,
		DependsOn: in.DependsOn,
		CreatedBy: in.CreatedBy,
		CreatedAt: time.Unix(r.nextID, 0).UTC(),
		UpdatedAt: time.Unix(r.nextID, 0).UTC(),
	}
	if in.IdempotencyKey != "" {
		idempotencyKey := in.IdempotencyKey
		item.IdempotencyKey = &idempotencyKey
		r.byKey[key] = item
	}
	r.items = append(r.items, item)
	return item, nil
}

func (r *fakeWorkItemContractRepo) ClaimWorkItem(context.Context, string, time.Time) (*agent.WorkItem, error) {
	return nil, nil
}

func (r *fakeWorkItemContractRepo) RenewLease(context.Context, int64, int64, time.Time) error {
	return nil
}

func (r *fakeWorkItemContractRepo) CompleteWorkItem(context.Context, int64, int64, json.RawMessage) error {
	return nil
}

func (r *fakeWorkItemContractRepo) FailWorkItem(context.Context, int64, int64, string) error {
	return nil
}

var _ agent.WorkItemContractRepository = (*fakeWorkItemContractRepo)(nil)

func (r *fakeWorkItemContractRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

func (r *fakeWorkItemContractRepo) itemsOfKind(kind string) []agent.WorkItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]agent.WorkItem, 0, len(r.items))
	for _, item := range r.items {
		if item.Kind == kind {
			items = append(items, item)
		}
	}
	return items
}

// dispatchStageRepo is both the stage machine's StageRepository and the
// dispatcher's VersionStageReader, so the integration test can assert the stage
// the enqueuer observed.
type dispatchStageRepo struct {
	mu    sync.Mutex
	stage version.Stage
	found bool
}

func newDispatchStageRepo(stage version.Stage) *dispatchStageRepo {
	return &dispatchStageRepo{stage: stage, found: true}
}

func (r *dispatchStageRepo) set(stage version.Stage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stage = stage
	r.found = true
}

func (r *dispatchStageRepo) GetStage(_ context.Context, versionID int64) (version.StageState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.found || r.stage == "" {
		return version.StageState{}, version.ErrNotFound
	}
	return version.StageState{
		VersionID:      versionID,
		Stage:          r.stage,
		StageEnteredAt: time.Unix(1, 0).UTC(),
	}, nil
}

func (r *dispatchStageRepo) ApplyStage(_ context.Context, expected version.Stage, next version.StageState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.found || r.stage != expected {
		return version.ErrStageConflict
	}
	r.stage = next.Stage
	return nil
}

var _ version.StageRepository = (*dispatchStageRepo)(nil)

// fakeVersionReportEnsurer records report-gate calls and returns a configured
// error.
type fakeVersionReportEnsurer struct {
	calls  []int64
	report delivery.Report
	err    error
}

func (f *fakeVersionReportEnsurer) EnsureVersionReport(_ context.Context, versionID int64) (delivery.Report, error) {
	f.calls = append(f.calls, versionID)
	return f.report, f.err
}

type fakeDispatchRequirementGate struct {
	calls int
}

func (f *fakeDispatchRequirementGate) BatchConfirm(_ context.Context, _ int64, _ []int64) error {
	f.calls++
	return nil
}

type fakeDispatchCaseGate struct {
	calls int
}

func (f *fakeDispatchCaseGate) ConfirmCases(_ context.Context, _ int64, _ []int64) error {
	f.calls++
	return nil
}

func newTestDispatcher(stage version.Stage) (*agentapp.Dispatcher, *fakeWorkItemContractRepo, *dispatchStageRepo, *fakeVersionReportEnsurer) {
	repo := newFakeWorkItemContractRepo()
	stages := newDispatchStageRepo(stage)
	reports := &fakeVersionReportEnsurer{}
	dispatcher := agentapp.NewDispatcher(agentapp.DispatcherDeps{
		WorkItems: repo,
		Stages:    stages,
		Reports:   reports,
	})
	return dispatcher, repo, stages, reports
}

func validDispatchRequest() agentapp.DispatchRequest {
	return agentapp.DispatchRequest{
		VersionID:      7,
		Kind:           agent.WorkItemKindDesignCases,
		Goal:           "generate cases for the confirmed requirements",
		Target:         json.RawMessage(`{"sut_id":"1"}`),
		Inputs:         json.RawMessage(`{"requirement_ids":["1"]}`),
		Expected:       json.RawMessage(`{"artifact":"case_draft"}`),
		Budget:         json.RawMessage(`{"max_tokens":20000}`),
		Actor:          agentapp.DispatchActorStateMachine,
		IdempotencyKey: "7:design.cases:target",
	}
}

func requirePlatformStatus(t *testing.T, err error, status int) {
	t.Helper()
	var platformErr *platform.Error
	if !errors.As(err, &platformErr) {
		t.Fatalf("error = %v, want *platform.Error", err)
	}
	if platformErr.Status != status {
		t.Fatalf("error status = %d (%s), want %d", platformErr.Status, platformErr.Code, status)
	}
}

// ---------- tests ----------

// TestDispatchIdempotent proves a repeated dispatch of the same request yields
// exactly one work item and that the dispatch-level field validation rejects
// an empty idempotency key and a non-positive depends_on.
func TestDispatchIdempotent(t *testing.T) {
	ctx := context.Background()
	dispatcher, repo, _, _ := newTestDispatcher(version.StageDesigning)
	request := validDispatchRequest()

	first, err := dispatcher.Dispatch(ctx, request)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		repeated, err := dispatcher.Dispatch(ctx, request)
		if err != nil {
			t.Fatalf("repeat dispatch %d: %v", attempt+2, err)
		}
		if repeated.ID != first.ID {
			t.Fatalf("repeat dispatch %d id = %d, want existing %d", attempt+2, repeated.ID, first.ID)
		}
	}
	if got := repo.count(); got != 1 {
		t.Fatalf("stored work items = %d, want 1 (duplicate dispatch)", got)
	}
	stored := repo.itemsOfKind(agent.WorkItemKindDesignCases)
	if len(stored) != 1 {
		t.Fatalf("design.cases items = %d, want 1", len(stored))
	}
	if stored[0].CreatedBy != agent.WorkItemCreatedByStateMachine {
		t.Fatalf("created_by = %q, want %q", stored[0].CreatedBy, agent.WorkItemCreatedByStateMachine)
	}

	emptyKey := request
	emptyKey.IdempotencyKey = "  "
	if _, err := dispatcher.Dispatch(ctx, emptyKey); err == nil {
		t.Fatal("dispatch with empty idempotency key succeeded")
	} else {
		requirePlatformStatus(t, err, http.StatusBadRequest)
	}

	badDependency := request
	badDependency.IdempotencyKey = "7:design.cases:bad-dependency"
	badDependency.DependsOn = []int64{0}
	if _, err := dispatcher.Dispatch(ctx, badDependency); err == nil {
		t.Fatal("dispatch with depends_on 0 succeeded")
	} else {
		requirePlatformStatus(t, err, http.StatusBadRequest)
	}
	if got := repo.count(); got != 1 {
		t.Fatalf("rejected dispatches stored work items; count = %d, want 1", got)
	}
}

// TestDispatchRoleMatrix pins the D21 creator-kind matrix (task-types.md §9.3).
func TestDispatchRoleMatrix(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		actor  agentapp.DispatchActor
		kind   string
		stage  version.Stage
		reject bool
	}{
		{"assistant execute.batch", agentapp.DispatchActorAssistant, agent.WorkItemKindExecuteBatch, version.StageExecuting, true},
		{"assistant report.version", agentapp.DispatchActorAssistant, agent.WorkItemKindReportVersion, version.StageReporting, true},
		{"assistant explore.page", agentapp.DispatchActorAssistant, agent.WorkItemKindExplorePage, version.StageClarifying, true},
		{"assistant design.cases", agentapp.DispatchActorAssistant, agent.WorkItemKindDesignCases, version.StageDesigning, false},
		{"assistant review.cases", agentapp.DispatchActorAssistant, agent.WorkItemKindReviewCases, version.StageDesigning, false},
		{"planner explore.login", agentapp.DispatchActorPlanner, agent.WorkItemKindExploreLogin, version.StageClarifying, false},
		{"planner explore.page", agentapp.DispatchActorPlanner, agent.WorkItemKindExplorePage, version.StageClarifying, false},
		{"planner explore.api", agentapp.DispatchActorPlanner, agent.WorkItemKindExploreAPI, version.StageClarifying, false},
		{"planner explore.coverage", agentapp.DispatchActorPlanner, agent.WorkItemKindExploreCoverage, version.StageClarifying, false},
		{"planner explore.gap", agentapp.DispatchActorPlanner, agent.WorkItemKindExploreGap, version.StageClarifying, false},
		{"planner design.cases", agentapp.DispatchActorPlanner, agent.WorkItemKindDesignCases, version.StageDesigning, true},
		{"trigger report.version", agentapp.DispatchActorTrigger, agent.WorkItemKindReportVersion, version.StageReporting, false},
		{"trigger analyze.failure", agentapp.DispatchActorTrigger, agent.WorkItemKindAnalyzeFailure, version.StageExecuting, false},
		{"trigger knowledge.propose", agentapp.DispatchActorTrigger, agent.WorkItemKindKnowledgePropose, version.StageReporting, false},
		{"trigger design.cases", agentapp.DispatchActorTrigger, agent.WorkItemKindDesignCases, version.StageDesigning, true},
		{"state_machine design.cases", agentapp.DispatchActorStateMachine, agent.WorkItemKindDesignCases, version.StageDesigning, false},
		{"state_machine review.cases", agentapp.DispatchActorStateMachine, agent.WorkItemKindReviewCases, version.StageDesigning, false},
		{"state_machine execute.batch", agentapp.DispatchActorStateMachine, agent.WorkItemKindExecuteBatch, version.StageExecuting, false},
		{"state_machine report.version", agentapp.DispatchActorStateMachine, agent.WorkItemKindReportVersion, version.StageReporting, false},
		{"state_machine analyze.failure", agentapp.DispatchActorStateMachine, agent.WorkItemKindAnalyzeFailure, version.StageReporting, true},
		{"unknown actor", agentapp.DispatchActor("executor"), agent.WorkItemKindDesignCases, version.StageDesigning, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dispatcher, repo, _, _ := newTestDispatcher(testCase.stage)
			request := validDispatchRequest()
			request.Kind = testCase.kind
			request.Actor = testCase.actor
			request.IdempotencyKey = fmt.Sprintf("7:%s:matrix", testCase.kind)
			item, err := dispatcher.Dispatch(ctx, request)
			if testCase.reject {
				if err == nil {
					t.Fatalf("actor %s created forbidden kind %s", testCase.actor, testCase.kind)
				}
				wantStatus := http.StatusForbidden
				if !testCase.actor.Valid() {
					wantStatus = http.StatusBadRequest
				}
				requirePlatformStatus(t, err, wantStatus)
				if got := repo.count(); got != 0 {
					t.Fatalf("rejected dispatch stored %d work item(s)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("dispatch %s/%s: %v", testCase.actor, testCase.kind, err)
			}
			if item.CreatedBy != string(testCase.actor) {
				t.Fatalf("created_by = %q, want %q", item.CreatedBy, testCase.actor)
			}
		})
	}
}

// TestDispatchStageValidation proves a work item kind is only created at the
// version stage that owns it (spec §5.3/§5.4).
func TestDispatchStageValidation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		kind   string
		stage  version.Stage
		reject bool
	}{
		{"design.cases in designing", agent.WorkItemKindDesignCases, version.StageDesigning, false},
		{"design.cases in clarifying", agent.WorkItemKindDesignCases, version.StageClarifying, true},
		{"design.cases in case_review", agent.WorkItemKindDesignCases, version.StageCaseReview, true},
		{"review.cases in designing", agent.WorkItemKindReviewCases, version.StageDesigning, false},
		{"execute.batch in executing", agent.WorkItemKindExecuteBatch, version.StageExecuting, false},
		{"execute.batch in case_review", agent.WorkItemKindExecuteBatch, version.StageCaseReview, true},
		{"report.version in reporting", agent.WorkItemKindReportVersion, version.StageReporting, false},
		{"report.version in executing", agent.WorkItemKindReportVersion, version.StageExecuting, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dispatcher, repo, _, _ := newTestDispatcher(testCase.stage)
			request := validDispatchRequest()
			request.Kind = testCase.kind
			request.IdempotencyKey = fmt.Sprintf("7:%s:stage", testCase.kind)
			_, err := dispatcher.Dispatch(ctx, request)
			if testCase.reject {
				if err == nil {
					t.Fatalf("%s at stage %s was accepted", testCase.kind, testCase.stage)
				}
				requirePlatformStatus(t, err, http.StatusConflict)
				if got := repo.count(); got != 0 {
					t.Fatalf("stage-rejected dispatch stored %d work item(s)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s at stage %s: %v", testCase.kind, testCase.stage, err)
			}
		})
	}
}

// TestDispatchRequestJobWhitelist proves the controlled assistant bypass only
// accepts the job kinds the contract lets the assistant request and that a
// repeated job.start is idempotent.
func TestDispatchRequestJobWhitelist(t *testing.T) {
	ctx := context.Background()
	dispatcher, repo, _, _ := newTestDispatcher(version.StageDesigning)
	payload := json.RawMessage(`{"requirement_ids":[11]}`)

	first, err := dispatcher.RequestJob(ctx, 7, agent.WorkItemKindDesignCases, payload)
	if err != nil {
		t.Fatalf("assistant design.cases request: %v", err)
	}
	if first.CreatedBy != agent.WorkItemCreatedByAssistant {
		t.Fatalf("created_by = %q, want assistant", first.CreatedBy)
	}
	if string(first.Inputs) != string(payload) {
		t.Fatalf("inputs = %s, want %s", first.Inputs, payload)
	}
	repeated, err := dispatcher.RequestJob(ctx, 7, agent.WorkItemKindDesignCases, payload)
	if err != nil {
		t.Fatalf("repeated assistant request: %v", err)
	}
	if repeated.ID != first.ID {
		t.Fatalf("repeated request id = %d, want %d", repeated.ID, first.ID)
	}
	if got := repo.count(); got != 1 {
		t.Fatalf("stored work items = %d, want 1", got)
	}

	for _, kind := range []string{agent.WorkItemKindExecuteBatch, agent.WorkItemKindReportVersion, agent.WorkItemKindExplorePage} {
		if _, err := dispatcher.RequestJob(ctx, 7, kind, nil); err == nil {
			t.Fatalf("assistant request for %s was accepted", kind)
		} else {
			requirePlatformStatus(t, err, http.StatusForbidden)
		}
	}
}

// TestStageMachineEnqueuesJobsThroughDispatcher wires the stage machine to the
// real dispatcher (R35): the requirement and case hard gates create the
// design.cases/execute.batch work items, case_review creates no review.cases,
// and the reporting entry goes through the report gate instead of a duplicate
// work item.
func TestStageMachineEnqueuesJobsThroughDispatcher(t *testing.T) {
	ctx := context.Background()
	repo := newFakeWorkItemContractRepo()
	stages := newDispatchStageRepo(version.StageRequirementReview)
	reports := &fakeVersionReportEnsurer{}
	dispatcher := agentapp.NewDispatcher(agentapp.DispatcherDeps{
		WorkItems: repo,
		Stages:    stages,
		Reports:   reports,
	})
	machine := versionapp.NewStageMachine(stages, dispatcher, nil, nil, nil,
		versionapp.WithRequirementGateConfirmer(&fakeDispatchRequirementGate{}),
		versionapp.WithCaseGateConfirmer(&fakeDispatchCaseGate{}),
	)

	if _, err := machine.OnRequirementConfirmed(ctx, 7, []int64{3}); err != nil {
		t.Fatalf("requirement gate: %v", err)
	}
	designItems := repo.itemsOfKind(agent.WorkItemKindDesignCases)
	if len(designItems) != 1 {
		t.Fatalf("design.cases items = %d, want 1", len(designItems))
	}
	if designItems[0].CreatedBy != agent.WorkItemCreatedByStateMachine {
		t.Fatalf("design.cases created_by = %q, want state_machine", designItems[0].CreatedBy)
	}
	if !strings.Contains(string(designItems[0].Inputs), "3") {
		t.Fatalf("design.cases inputs = %s, want the confirmed requirement id", designItems[0].Inputs)
	}

	// A stage re-entry re-ensures the same stage job instead of duplicating it.
	reentry := versionapp.StageJob{VersionID: 7, Kind: versionapp.StageJobDesignCases, RequirementIDs: []int64{3}}
	if err := dispatcher.EnqueueStageJob(ctx, reentry); err != nil {
		t.Fatalf("re-ensure design.cases: %v", err)
	}
	if got := len(repo.itemsOfKind(agent.WorkItemKindDesignCases)); got != 1 {
		t.Fatalf("design.cases items after re-entry = %d, want 1", got)
	}

	// Entering case_review must not enqueue the removed review.cases job.
	if err := machine.Transition(ctx, 7, version.StageCaseReview, "cases_generated"); err != nil {
		t.Fatalf("transition to case_review: %v", err)
	}
	if got := len(repo.itemsOfKind(agent.WorkItemKindReviewCases)); got != 0 {
		t.Fatalf("case_review enqueued %d review.cases item(s), want 0", got)
	}

	if _, err := machine.OnCasesConfirmed(ctx, 7, []int64{21, 22}); err != nil {
		t.Fatalf("case gate: %v", err)
	}
	executeItems := repo.itemsOfKind(agent.WorkItemKindExecuteBatch)
	if len(executeItems) != 1 {
		t.Fatalf("execute.batch items = %d, want 1", len(executeItems))
	}
	if !strings.Contains(string(executeItems[0].Inputs), "21") {
		t.Fatalf("execute.batch inputs = %s, want the confirmed case ids", executeItems[0].Inputs)
	}

	// The reporting entry is the report gate: no duplicate stage work item.
	if _, err := machine.OnWaveTerminal(ctx, 7); err != nil {
		t.Fatalf("wave terminal: %v", err)
	}
	if len(reports.calls) != 1 || reports.calls[0] != 7 {
		t.Fatalf("report gate calls = %v, want [7]", reports.calls)
	}
	if items := repo.itemsOfKind(agent.WorkItemKindReportVersion); len(items) != 0 {
		t.Fatalf("reporting entry created %d report.version work item(s), want 0", len(items))
	}
}

// TestReportVersionNotDueNoOp proves the report gate rejection is a silent
// no-op (R43): no work item, no error, while other failures propagate.
func TestReportVersionNotDueNoOp(t *testing.T) {
	ctx := context.Background()
	repo := newFakeWorkItemContractRepo()
	reports := &fakeVersionReportEnsurer{
		err: &deliveryapp.VersionReportNotDueError{Reason: "no_execution_data"},
	}
	dispatcher := agentapp.NewDispatcher(agentapp.DispatcherDeps{
		WorkItems: repo,
		Stages:    newDispatchStageRepo(version.StageReporting),
		Reports:   reports,
	})

	if err := dispatcher.HandleReportVersion(ctx, 7); err != nil {
		t.Fatalf("not-due report gate returned %v, want nil", err)
	}
	if len(reports.calls) != 1 {
		t.Fatalf("report gate calls = %d, want 1", len(reports.calls))
	}
	if got := repo.count(); got != 0 {
		t.Fatalf("not-due report gate created %d work item(s)", got)
	}

	// The report.version stage entry uses the same gated handler.
	if err := dispatcher.EnqueueStageJob(ctx, versionapp.StageJob{VersionID: 7, Kind: versionapp.StageJobReportVersion}); err != nil {
		t.Fatalf("not-due report stage entry returned %v, want nil", err)
	}
	if got := repo.count(); got != 0 {
		t.Fatalf("not-due report stage entry created %d work item(s)", got)
	}

	// A wrapped sentinel is still a no-op.
	reports.err = fmt.Errorf("ensure version report: %w", deliveryapp.ErrVersionReportNotDue)
	if err := dispatcher.HandleReportVersion(ctx, 7); err != nil {
		t.Fatalf("wrapped not-due report gate returned %v, want nil", err)
	}

	// Other errors propagate untouched.
	boom := errors.New("report repo unavailable")
	reports.err = boom
	if err := dispatcher.HandleReportVersion(ctx, 7); !errors.Is(err, boom) {
		t.Fatalf("report gate error = %v, want %v", err, boom)
	}

	reports.err = nil
	if err := dispatcher.HandleReportVersion(ctx, 7); err != nil {
		t.Fatalf("due report gate returned %v, want nil", err)
	}
}
