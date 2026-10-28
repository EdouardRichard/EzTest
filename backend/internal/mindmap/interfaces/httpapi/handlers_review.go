package httpapi

import (
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// reviewRequest is the POST /review body (spec §7.2 endpoint 21).
type reviewRequest struct {
	Decision            string `json:"decision"`
	Comment             string `json:"comment"`
	AcknowledgeFindings bool   `json:"acknowledgeFindings"`
}

// updateFindingRequest is the PATCH /findings/{findingId} body.
type updateFindingRequest struct {
	Status string `json:"status"`
}

// submitReview handles POST /submit-review (200).
//
// The application layer exposes no SubmitReview use case (T10/T11 own the review
// and baseline services, T16 is scoped to the HTTP layer), so the handler
// composes the two documented steps from existing services and the pure domain
// validator: GraphService.Get for the graph, domain.ValidateGraph plus the
// "at least one case / one requirement source per case" gate of endpoint 20, and
// VersionLifecycle.EnterReview for the designing→reviewing transition (which is
// idempotent for an already-reviewing version).
func (h *Handler) submitReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	// The body is ignored; anything object-shaped ({} included) is accepted.
	var ignored map[string]any
	if err := decodeOptionalBody(r, &ignored); err != nil {
		writeServiceError(w, r, err)
		return
	}
	view, err := h.deps.Graph.Get(ctx, versionID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	fromDesigning := view.VersionStatus == "designing"
	if fromDesigning {
		if err := validateSubmittable(view); err != nil {
			writeServiceError(w, r, err)
			return
		}
	}
	actor := actorID(ctx)
	version, err := h.deps.Versions.EnterReview(ctx, versionID, actor)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	// gate.pending belongs to the designing→reviewing transition (spec §6.11):
	// an idempotent submit of an already-reviewing version publishes nothing.
	if fromDesigning && version.Status == "reviewing" && h.deps.Events != nil {
		if err := h.deps.Events.Publish(ctx, application.Event{
			VersionID: versionID,
			Type:      domain.EventGatePending,
			Summary:   "case review pending",
			ActorID:   actor,
			Source:    domain.NodeSourceUserInput,
			Detail:    map[string]any{"status": version.Status},
		}); err != nil {
			h.logger.WarnContext(ctx, "mindmap: publishing gate.pending failed",
				"version_id", versionID, "error", err)
		}
	}
	writeData(w, http.StatusOK, submitReviewResultDTO{Version: newVersionView(version)})
}

// validateSubmittable enforces the submit-review gate of spec §7.2 endpoint 20
// that the per-write validation does not cover: the graph must hold at least one
// case, every case must carry a requirement source, and the tree rules of
// domain.ValidateGraph must hold.
func validateSubmittable(view application.GraphView) error {
	nodes := make([]domain.CaseNode, 0, len(view.Nodes))
	steps := make(map[int64][]domain.CaseStep, len(view.Nodes))
	assertions := make(map[int64][]domain.CaseAssertion, len(view.Nodes))
	cases := 0
	for _, node := range view.Nodes {
		nodes = append(nodes, node.Node)
		steps[node.Node.ID] = node.Steps
		assertions[node.Node.ID] = node.Assertions
		if node.Node.Kind != domain.NodeKindCase {
			continue
		}
		cases++
		if requirementSources(node.Sources) == 0 {
			return domain.NewInvalid("graph", "invalid_case_tree",
				"case "+idString(node.Node.ID)+" has no requirement source")
		}
	}
	if cases == 0 {
		return domain.NewInvalid("graph", "no_cases", "the graph has no case to review")
	}
	return domain.ValidateGraph(nodes, steps, assertions)
}

// requirementSources counts the requirement sources of one node.
func requirementSources(sources []domain.CaseSource) int {
	count := 0
	for _, source := range sources {
		if source.RefKind == domain.RefRequirement {
			count++
		}
	}
	return count
}

// decideReview handles POST /review (200): approve (freeze) or reject.
func (h *Handler) decideReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body reviewRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	actor := actorID(ctx)
	switch body.Decision {
	case application.ReviewDecisionApprove:
		result, err := h.deps.Reviews.Approve(ctx, versionID, actor, application.ReviewCommand{
			Decision:            body.Decision,
			Comment:             body.Comment,
			AcknowledgeFindings: body.AcknowledgeFindings,
		})
		if err != nil {
			writeServiceError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, reviewResultDTO{
			Version:  newVersionView(result.Version),
			Baseline: newBaselineDTO(&result.Baseline),
			Review:   newReviewDTO(result.Review),
		})
	case application.ReviewDecisionReject:
		result, err := h.deps.Reviews.Reject(ctx, versionID, actor, body.Comment)
		if err != nil {
			writeServiceError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, reviewResultDTO{
			Version:  newVersionView(result.Version),
			Baseline: nil,
			Review:   newReviewDTO(result.Review),
		})
	default:
		writeServiceError(w, r, domain.NewInvalid("decision", "invalid_decision",
			"decision must be approve or reject"))
	}
}

// listReviews handles GET /reviews (200).
func (h *Handler) listReviews(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	limit, cursor, err := queryPage(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	reviews, next, hasMore, err := h.deps.Reviews.List(r.Context(), versionID, limit, cursor)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	rows := make([]caseReviewDTO, 0, len(reviews))
	for _, review := range reviews {
		rows = append(rows, newReviewDTO(review))
	}
	writePage(w, rows, next, hasMore)
}

// listFindings handles GET /findings (200): the authoritative flat, unpaginated
// finding list of the version (spec §7.3).
func (h *Handler) listFindings(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	nodeID, err := queryOptionalID(r, "nodeId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	severity := strings.TrimSpace(r.URL.Query().Get("severity"))
	findings, err := h.deps.Reviews.ListFindings(r.Context(), versionID, status, severity, nodeID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newFindingDTOs(findings))
}

// updateFinding handles PATCH /findings/{findingId} (200): the human disposition
// (fixed/dismissed only).
func (h *Handler) updateFinding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	findingID, err := pathID(r, "findingId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body updateFindingRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	finding, err := h.deps.Reviews.UpdateFinding(ctx, versionID, findingID, actorID(ctx), body.Status)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newFindingDTO(finding))
}
