package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmaphttp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/interfaces/httpapi"
)

// supplementGraphFake implements the P7 graph slice on top of graphFake (all
// legacy GraphService methods are inherited).
type supplementGraphFake struct {
	*graphFake
	planFn       func(context.Context, int64) (application.IncrementalPlanView, error)
	transitionFn func(context.Context, int64, int64, int64, string) (domain.CaseNode, error)
}

// IncrementalPlan implements the GET /incremental-plan read.
func (f *supplementGraphFake) IncrementalPlan(ctx context.Context, versionID int64) (application.IncrementalPlanView, error) {
	if f.planFn == nil {
		return application.IncrementalPlanView{}, errUnexpected
	}
	return f.planFn(ctx, versionID)
}

// TransitionNodeStatus implements PATCH /nodes/{nodeId}/supplement.
func (f *supplementGraphFake) TransitionNodeStatus(ctx context.Context, versionID, nodeID, actor int64, to string) (domain.CaseNode, error) {
	if f.transitionFn == nil {
		return domain.CaseNode{}, errUnexpected
	}
	return f.transitionFn(ctx, versionID, nodeID, actor, to)
}

// decodeSupplementPlan decodes the incremental plan envelope.
func decodeSupplementPlan(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode plan envelope: %v (body=%s)", err, body)
	}
	return envelope.Data
}

// TestStartSupplementRunKindsAndBatchGate pins the extended POST /runs kind enum
// and the mandatory parent batch id of the P7 runs (spec §7.6).
func TestStartSupplementRunKindsAndBatchGate(t *testing.T) {
	var captured []application.StartRunCommand
	deps := mindmaphttp.Deps{Runs: &runStarterFake{startFn: func(_ context.Context, versionID, actor int64, cmd application.StartRunCommand) (domain.CaseDesignRun, error) {
		if versionID != 101 || actor != regularUser.ID {
			t.Fatalf("start args version=%d actor=%d", versionID, actor)
		}
		captured = append(captured, cmd)
		return domain.CaseDesignRun{ID: 77, VersionID: versionID, Kind: cmd.Kind, Status: domain.RunQueued}, nil
	}}}
	h := newServer(t, deps)

	rec := call(t, h, http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"supplement","batchId":"41"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("supplement run=%d body=%s, want 202", rec.Code, rec.Body)
	}
	if data := decodeData(t, rec); data["runId"] != "77" {
		t.Fatalf("runId=%v want the decimal string 77", data["runId"])
	}
	rec = call(t, h, http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"incremental_design","batchId":"42"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("incremental_design run=%d body=%s, want 202", rec.Code, rec.Body)
	}
	if len(captured) != 2 {
		t.Fatalf("run starts=%d want 2", len(captured))
	}
	if captured[0].Kind != application.RunKindSupplement || captured[0].BatchID != 41 {
		t.Fatalf("supplement command=%+v", captured[0])
	}
	if captured[1].Kind != application.RunKindIncrementalDesign || captured[1].BatchID != 42 {
		t.Fatalf("incremental command=%+v", captured[1])
	}

	// The parent batch id is mandatory for both P7 kinds.
	for _, kind := range []string{"supplement", "incremental_design"} {
		rec = call(t, h, http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"`+kind+`"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s without batchId=%d body=%s, want 422", kind, rec.Code, rec.Body)
		}
		if reason := reasonOf(t, rec); reason != "invalid_batch_id" {
			t.Fatalf("%s missing-batch reason=%q want invalid_batch_id", kind, reason)
		}
	}
	rec = call(t, h, http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"supplement","batchId":"abc"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed batchId=%d body=%s, want 400", rec.Code, rec.Body)
	}
	if rec = call(t, h, http.MethodPost, "/api/versions/101/mindmap/runs", "", `{"kind":"supplement","batchId":"41"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated run=%d want 401", rec.Code)
	}
}

// TestStartSupplementRunGateErrors pins the version-state and single-flight
// conflicts of the P7 run start.
func TestStartSupplementRunGateErrors(t *testing.T) {
	deps := mindmaphttp.Deps{Runs: &runStarterFake{startFn: func(context.Context, int64, int64, application.StartRunCommand) (domain.CaseDesignRun, error) {
		return domain.CaseDesignRun{}, domain.NewStateConflict("version_state_conflict", "version is not executing")
	}}}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"supplement","batchId":"41"}`)
	if rec.Code != http.StatusConflict || reasonOf(t, rec) != "version_state_conflict" {
		t.Fatalf("state conflict=%d body=%s, want 409 version_state_conflict", rec.Code, rec.Body)
	}

	deps = mindmaphttp.Deps{Runs: &runStarterFake{startFn: func(context.Context, int64, int64, application.StartRunCommand) (domain.CaseDesignRun, error) {
		return domain.CaseDesignRun{}, domain.NewConflict("run_in_progress", "already running")
	}}}
	rec = call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"incremental_design","batchId":"42"}`)
	if rec.Code != http.StatusConflict || reasonOf(t, rec) != "run_in_progress" {
		t.Fatalf("active run=%d body=%s, want 409 run_in_progress", rec.Code, rec.Body)
	}
}

// TestIncrementalPlanDTO pins the GET /incremental-plan payload: the plan
// fields, string ids, the deterministic default selection and the warnings.
func TestIncrementalPlanDTO(t *testing.T) {
	parentBatchID := int64(41)
	plan := application.IncrementalPlanView{
		VersionID:     101,
		Phase:         "designing",
		ParentBatchID: &parentBatchID,
		Run:           &application.IncrementalPlanRun{ID: 77, Kind: "incremental_design", Status: "done", Result: json.RawMessage(`{"summary":"ok"}`)},
		NewCases: []application.IncrementalPlanNewCase{
			{NodeID: 21, Title: "new case", Priority: "P1", Confidence: "high", RequirementIDs: []int64{11}, Status: "supplemented"},
		},
		AffectedCases: []application.IncrementalPlanAffectedCase{
			{NodeID: 22, Title: "old case", Confidence: "medium", Reason: "changed", Source: "code", Selected: true},
			{NodeID: 23, Title: "low case", Confidence: "low", Reason: "maybe", Source: "requirement", Selected: false},
		},
		UnmappedChanges: []application.IncrementalPlanChange{{Path: "src/x.ts", Summary: "changed"}},
		DefaultCaseIDs:  []int64{21, 22},
		Warnings:        []application.IncrementalPlanWarning{{Code: "history_rewritten", Message: "rewritten"}},
		Degraded:        false,
	}
	deps := mindmaphttp.Deps{Graph: &supplementGraphFake{
		graphFake: &graphFake{},
		planFn: func(_ context.Context, versionID int64) (application.IncrementalPlanView, error) {
			if versionID != 101 {
				t.Fatalf("plan version=%d", versionID)
			}
			return plan, nil
		},
	}}
	h := newServer(t, deps)

	rec := call(t, h, http.MethodGet, "/api/versions/101/mindmap/incremental-plan", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("plan=%d body=%s, want 200", rec.Code, rec.Body)
	}
	data := decodeSupplementPlan(t, rec.Body.Bytes())
	if data["versionId"] != "101" || data["phase"] != "designing" || data["parentBatchId"] != "41" {
		t.Fatalf("plan=%v", data)
	}
	run, _ := data["run"].(map[string]any)
	if run["id"] != "77" || run["kind"] != "incremental_design" || run["status"] != "done" {
		t.Fatalf("run=%v", run)
	}
	newCases, _ := data["newCases"].([]any)
	if len(newCases) != 1 || newCases[0].(map[string]any)["nodeId"] != "21" {
		t.Fatalf("newCases=%v", newCases)
	}
	requirementIDs, _ := newCases[0].(map[string]any)["requirementIds"].([]any)
	if len(requirementIDs) != 1 || requirementIDs[0] != "11" {
		t.Fatalf("requirementIds=%v want decimal strings", requirementIDs)
	}
	affected, _ := data["affectedCases"].([]any)
	if len(affected) != 2 || affected[0].(map[string]any)["nodeId"] != "22" || affected[1].(map[string]any)["selected"] != false {
		t.Fatalf("affectedCases=%v", affected)
	}
	defaultIDs, _ := data["defaultCaseIds"].([]any)
	if len(defaultIDs) != 2 || defaultIDs[0] != "21" || defaultIDs[1] != "22" {
		t.Fatalf("defaultCaseIds=%v want decimal strings", defaultIDs)
	}
	unmapped, _ := data["unmappedChanges"].([]any)
	if len(unmapped) != 1 || unmapped[0].(map[string]any)["path"] != "src/x.ts" {
		t.Fatalf("unmappedChanges=%v", unmapped)
	}
	warnings, _ := data["warnings"].([]any)
	if len(warnings) != 1 || warnings[0].(map[string]any)["code"] != "history_rewritten" {
		t.Fatalf("warnings=%v", warnings)
	}

	if rec = call(t, h, http.MethodGet, "/api/versions/101/mindmap/incremental-plan", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated plan=%d want 401", rec.Code)
	}
}

// TestIncrementalPlanEmpty pins the empty plan of a version without an
// incremental_design run (200, null run, [] arrays).
func TestIncrementalPlanEmpty(t *testing.T) {
	deps := mindmaphttp.Deps{Graph: &supplementGraphFake{
		graphFake: &graphFake{},
		planFn: func(context.Context, int64) (application.IncrementalPlanView, error) {
			return application.IncrementalPlanView{VersionID: 101, Phase: "executing"}, nil
		},
	}}
	rec := call(t, newServer(t, deps), http.MethodGet, "/api/versions/101/mindmap/incremental-plan", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty plan=%d body=%s", rec.Code, rec.Body)
	}
	data := decodeSupplementPlan(t, rec.Body.Bytes())
	if data["run"] != nil {
		t.Fatalf("run=%v want null", data["run"])
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"newCases", "affectedCases", "unmappedChanges", "defaultCaseIds", "warnings"} {
		if string(document[key]) == "null" {
			t.Fatalf("%s is null, want []", key)
		}
	}
}

// TestSupplementNodeDecision pins the accept/reject decision endpoint and its
// P7 status matrix mapping.
func TestSupplementNodeDecision(t *testing.T) {
	node := domain.CaseNode{
		ID: 21, VersionID: 101, Kind: domain.NodeKindCase, Title: "new case",
		Source: domain.NodeSourceAISupplement, Status: domain.NodeStatusSupplemented, Revision: 1,
	}
	var transitions []string
	graph := &supplementGraphFake{
		graphFake: &graphFake{},
		transitionFn: func(_ context.Context, versionID, nodeID, actor int64, to string) (domain.CaseNode, error) {
			if versionID != 101 || nodeID != 21 || actor != regularUser.ID {
				t.Fatalf("transition args version=%d node=%d actor=%d", versionID, nodeID, actor)
			}
			transitions = append(transitions, to)
			updated := node
			updated.Status = to
			return updated, nil
		},
	}
	deps := mindmaphttp.Deps{
		Graph: graph,
		Nodes: &nodeReaderFake{getFn: func(_ context.Context, versionID, nodeID int64) (domain.CaseNode, error) {
			if versionID != 101 || nodeID != 21 {
				t.Fatalf("node read version=%d node=%d", versionID, nodeID)
			}
			return node, nil
		}},
	}
	h := newServer(t, deps)

	rec := call(t, h, http.MethodPatch, "/api/versions/101/mindmap/nodes/21/supplement", tokenUser, `{"decision":"accept"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept=%d body=%s, want 200", rec.Code, rec.Body)
	}
	if data := decodeData(t, rec); data["nodeId"] != "21" || data["status"] != "merged" {
		t.Fatalf("accept data=%v", data)
	}
	rec = call(t, h, http.MethodPatch, "/api/versions/101/mindmap/nodes/21/supplement", tokenUser, `{"decision":"reject"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject=%d body=%s, want 200", rec.Code, rec.Body)
	}
	if data := decodeData(t, rec); data["status"] != "rejected" {
		t.Fatalf("reject data=%v", data)
	}
	if len(transitions) != 2 || transitions[0] != domain.NodeStatusMerged || transitions[1] != domain.NodeStatusRejected {
		t.Fatalf("transitions=%v want merged then rejected", transitions)
	}
	// The decision endpoint never edits node content.
	if graph.updateNodeFn != nil || graph.deleteNodeFn != nil {
		t.Fatalf("the decision endpoint must not call content writes")
	}

	if rec = call(t, h, http.MethodPatch, "/api/versions/101/mindmap/nodes/21/supplement", "", `{"decision":"accept"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated decision=%d want 401", rec.Code)
	}
}

// TestSupplementNodeDecisionValidation pins the invalid-decision, non-supplement
// and state-conflict failures.
func TestSupplementNodeDecisionValidation(t *testing.T) {
	aiNode := domain.CaseNode{ID: 21, VersionID: 101, Kind: domain.NodeKindCase, Source: domain.NodeSourceAISupplement, Status: domain.NodeStatusSupplemented}
	userNode := domain.CaseNode{ID: 22, VersionID: 101, Kind: domain.NodeKindCase, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusReviewed}

	graph := &supplementGraphFake{
		graphFake: &graphFake{},
		transitionFn: func(context.Context, int64, int64, int64, string) (domain.CaseNode, error) {
			return domain.CaseNode{}, domain.NewStateConflict("case_status_conflict", "illegal transition")
		},
	}
	nodes := &nodeReaderFake{getFn: func(_ context.Context, _, nodeID int64) (domain.CaseNode, error) {
		switch nodeID {
		case 21:
			return aiNode, nil
		case 22:
			return userNode, nil
		default:
			return domain.CaseNode{}, domain.NewNotFound("node", nodeID)
		}
	}}
	h := newServer(t, mindmaphttp.Deps{Graph: graph, Nodes: nodes})

	rec := call(t, h, http.MethodPatch, "/api/versions/101/mindmap/nodes/21/supplement", tokenUser, `{"decision":"maybe"}`)
	if rec.Code != http.StatusUnprocessableEntity || reasonOf(t, rec) != "invalid_decision" {
		t.Fatalf("invalid decision=%d body=%s, want 422 invalid_decision", rec.Code, rec.Body)
	}
	rec = call(t, h, http.MethodPatch, "/api/versions/101/mindmap/nodes/22/supplement", tokenUser, `{"decision":"accept"}`)
	if rec.Code != http.StatusConflict || reasonOf(t, rec) != "case_status_conflict" {
		t.Fatalf("non-supplement node=%d body=%s, want 409 case_status_conflict", rec.Code, rec.Body)
	}
	rec = call(t, h, http.MethodPatch, "/api/versions/101/mindmap/nodes/21/supplement", tokenUser, `{"decision":"accept"}`)
	if rec.Code != http.StatusConflict || reasonOf(t, rec) != "case_status_conflict" {
		t.Fatalf("illegal transition=%d body=%s, want 409 case_status_conflict", rec.Code, rec.Body)
	}
	rec = call(t, h, http.MethodPatch, "/api/versions/101/mindmap/nodes/99/supplement", tokenUser, `{"decision":"accept"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown node=%d body=%s, want 404", rec.Code, rec.Body)
	}
}
