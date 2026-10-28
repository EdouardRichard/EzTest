package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// The P7 mindmap handlers of spec §7.6: the two new run kinds of POST /runs, the
// incremental plan read and the ai_supplement node decision. They stay thin: the
// request is parsed, the mindmap application service is called and the shared
// envelope/error mapping is reused. No business rule lives here.
//
// POST /runs dispatches between the P7 payload (which carries the parent batch
// id) and the legacy P5 handler so the frozen P5 endpoint keeps its behavior.

// startRunDispatch handles POST /runs: the supplement/incremental_design kinds
// are validated here (parent batch id mandatory) and started through the run
// coordinator; every other kind is handed to the frozen P5 startRun handler.
func (h *Handler) startRunDispatch(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	kind, _ := peekRunKind(body)
	if kind == application.RunKindSupplement || kind == application.RunKindIncrementalDesign {
		h.startSupplementRun(w, r, body, kind)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	h.startRun(w, r)
}

// startSupplementRun validates and starts one P7 run kind (spec §7.6).
func (h *Handler) startSupplementRun(w http.ResponseWriter, r *http.Request, body []byte, kind string) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var request supplementStartRunRequest
	if err := decodeBody(r, &request); err != nil {
		writeServiceError(w, r, err)
		return
	}
	if strings.TrimSpace(request.BatchID) == "" {
		writeServiceError(w, r, domain.NewInvalid("batchId", "invalid_batch_id",
			"a supplement or incremental_design run requires the parent batch id"))
		return
	}
	batchID, err := parseIDString("batchId", strings.TrimSpace(request.BatchID))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if h.deps.Runs == nil {
		writeServiceError(w, r, errors.New("mindmap: run coordinator is not configured"))
		return
	}
	run, err := h.deps.Runs.Start(ctx, versionID, actorID(ctx), application.StartRunCommand{
		Kind:    kind,
		Mode:    request.Mode,
		BatchID: batchID,
	})
	if err != nil {
		h.writeStartRunError(w, r, run, err)
		return
	}
	writeData(w, http.StatusAccepted, startRunResultDTO{RunID: idString(run.ID)})
}

// getIncrementalPlan handles GET /incremental-plan (200): the incremental design
// plan of a continuing version. A version without an incremental_design run
// returns the empty plan, not a 404.
func (h *Handler) getIncrementalPlan(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	reader, ok := h.deps.Graph.(incrementalPlanReader)
	if !ok || reader == nil {
		writeServiceError(w, r, errors.New("mindmap: incremental plan service is not configured"))
		return
	}
	plan, err := reader.IncrementalPlan(r.Context(), versionID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newIncrementalPlanDTO(plan))
}

// supplementDecisionRequest is the PATCH /nodes/{nodeId}/supplement body.
type supplementDecisionRequest struct {
	Decision string `json:"decision"`
}

// patchSupplementNode handles PATCH /nodes/{nodeId}/supplement (200): accept
// moves an ai_supplement case to merged, reject to rejected. Only the status is
// written; frozen node content stays untouched (spec §5.2/§7.6).
func (h *Handler) patchSupplementNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	nodeID, err := pathID(r, "nodeId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var request supplementDecisionRequest
	if err := decodeBody(r, &request); err != nil {
		writeServiceError(w, r, err)
		return
	}
	target, ok := supplementDecisionTarget(request.Decision)
	if !ok {
		writeServiceError(w, r, domain.NewInvalid("decision", "invalid_decision",
			"decision must be accept or reject"))
		return
	}
	decision, ok := h.deps.Graph.(supplementNodeDecision)
	if !ok || decision == nil {
		writeServiceError(w, r, errors.New("mindmap: supplement node service is not configured"))
		return
	}
	if h.deps.Nodes == nil {
		writeServiceError(w, r, errors.New("mindmap: mindmap node reader is not configured"))
		return
	}
	node, err := h.deps.Nodes.Get(ctx, versionID, nodeID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if node.Source != domain.NodeSourceAISupplement {
		writeServiceError(w, r, domain.NewStateConflict("case_status_conflict",
			"only ai_supplement cases can be accepted or rejected"))
		return
	}
	updated, err := decision.TransitionNodeStatus(ctx, versionID, nodeID, actorID(ctx), target)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, supplementDecisionDTO{
		NodeID: idString(updated.ID),
		Status: updated.Status,
	})
}

// supplementDecisionTarget maps one decision to the P7 node status.
func supplementDecisionTarget(decision string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "accept":
		return domain.NodeStatusMerged, true
	case "reject":
		return domain.NodeStatusRejected, true
	default:
		return "", false
	}
}

// readRequestBody reads one bounded JSON request body. The limit mirrors the
// shared decodeBody bound so the dispatch never buffers an unbounded payload.
func readRequestBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		return nil, newBadRequest("body", "invalid_json", "request body must be a single valid JSON object")
	}
	if int64(len(body)) > maxRequestBytes {
		return nil, newBadRequest("body", "body_too_large", "request body is too large")
	}
	return body, nil
}

// peekRunKind extracts the kind field of a request body without enforcing the
// full schema; a malformed document yields an empty kind.
func peekRunKind(body []byte) (string, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", false
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &document); err != nil {
		return "", false
	}
	raw, ok := document["kind"]
	if !ok {
		return "", false
	}
	var kind string
	if err := json.Unmarshal(raw, &kind); err != nil {
		return "", false
	}
	return kind, true
}
