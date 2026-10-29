package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Graph request bodies (spec §7.2 endpoints 2-16). Optional fields are pointers
// so an omitted value keeps its stored value in a partial update.

// createNodeRequest is POST /nodes.
type createNodeRequest struct {
	Kind           string           `json:"kind"`
	ParentID       *string          `json:"parentId"`
	Title          string           `json:"title"`
	Description    string           `json:"description"`
	Priority       string           `json:"priority"`
	Preconditions  string           `json:"preconditions"`
	TestData       *json.RawMessage `json:"testData"`
	RequirementIDs []string         `json:"requirementIds"`
	Source         string           `json:"source"`
}

// updateNodeRequest is PATCH /nodes/{nodeId}.
type updateNodeRequest struct {
	Title          *string         `json:"title"`
	Description    *string         `json:"description"`
	Priority       *string         `json:"priority"`
	Preconditions  *string         `json:"preconditions"`
	TestData       json.RawMessage `json:"testData"`
	RequirementIDs *[]string       `json:"requirementIds"`
	Revision       int             `json:"revision"`
}

// reorderRequest is PUT /order.
type reorderRequest struct {
	ParentID *string  `json:"parentId"`
	NodeIDs  []string `json:"nodeIds"`
}

// createStepRequest is POST /nodes/{nodeId}/steps. nodeRevision is deliberately
// absent: the handler derives it from the node of the path (spec §7.2 note).
type createStepRequest struct {
	Action   string `json:"action"`
	Expected string `json:"expected"`
	Data     string `json:"data"`
	AfterSeq *int   `json:"afterSeq"`
}

// updateStepRequest is PATCH /steps/{stepId}.
type updateStepRequest struct {
	Action       *string `json:"action"`
	Expected     *string `json:"expected"`
	Data         *string `json:"data"`
	NodeRevision int     `json:"nodeRevision"`
}

// reorderStepsRequest is PUT /nodes/{nodeId}/steps/order.
type reorderStepsRequest struct {
	StepIDs      []string `json:"stepIds"`
	NodeRevision int      `json:"nodeRevision"`
}

// createAssertionRequest is POST /nodes/{nodeId}/assertions.
type createAssertionRequest struct {
	StepID     *string `json:"stepId"`
	Kind       string  `json:"kind"`
	Target     string  `json:"target"`
	Expression string  `json:"expression"`
	Expected   string  `json:"expected"`
}

// updateAssertionRequest is PATCH /assertions/{assertionId}. The assertion's step
// link is immutable: spec §7.2 endpoint 11 has no stepId field.
type updateAssertionRequest struct {
	Kind         *string `json:"kind"`
	Target       *string `json:"target"`
	Expression   *string `json:"expression"`
	Expected     *string `json:"expected"`
	NodeRevision int     `json:"nodeRevision"`
}

// createSourceRequest is POST /nodes/{nodeId}/sources.
type createSourceRequest struct {
	RefKind string  `json:"refKind"`
	RefID   *string `json:"refId"`
	RefKey  string  `json:"refKey"`
}

// createAnchorRequest is POST /nodes/{nodeId}/anchors.
type createAnchorRequest struct {
	FilePath   string `json:"filePath"`
	StartLine  *int   `json:"startLine"`
	EndLine    *int   `json:"endLine"`
	Confidence string `json:"confidence"`
}

// graphIncludes is the parsed ?include= subset of the graph read (spec §7.3).
// Omitting the parameter means every section.
type graphIncludes struct {
	anchors  bool
	findings bool
	impact   bool
}

// parseGraphIncludes parses the comma-separated include list. Unknown sections
// are ignored rather than rejected: the parameter is an optimization hint and a
// stricter contract would break forward compatibility.
func parseGraphIncludes(raw string) graphIncludes {
	if strings.TrimSpace(raw) == "" {
		return graphIncludes{anchors: true, findings: true, impact: true}
	}
	includes := graphIncludes{}
	for _, part := range strings.Split(raw, ",") {
		switch strings.TrimSpace(part) {
		case "anchors":
			includes.anchors = true
		case "findings":
			includes.findings = true
		case "impact":
			includes.impact = true
		}
	}
	return includes
}

// getGraph renders GET /mindmap (spec §7.3): the whole graph snapshot with
// anchors, coverage, baseline, latest runs and the read-only P4 impact hints.
func (h *Handler) getGraph(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	view, err := h.deps.Graph.Get(ctx, versionID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	// Findings are loaded even when include omits them: the per-node
	// openBlockerCount of the §7.3 contract is required on every node.
	findings, err := h.loadFindings(ctx, versionID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	blockers := blockerCountByNode(findings)

	includes := parseGraphIncludes(r.URL.Query().Get("include"))
	anchorIndex := map[int64][]application.Anchor{}
	if includes.anchors && h.deps.Anchors != nil {
		anchorIndex = h.loadAnchorIndex(ctx, versionID, view)
	}

	nodes := make([]caseNodeDTO, 0, len(view.Nodes))
	for _, node := range view.Nodes {
		nodes = append(nodes, newNodeDTO(
			node.Node, node.Steps, node.Assertions, node.Sources,
			anchorIndex[node.Node.ID], blockers[node.Node.ID],
		))
	}

	latestRuns := map[string]*runSummaryDTO{}
	if h.deps.RunStore != nil {
		latestRuns = h.loadLatestRuns(ctx, versionID)
	}

	impact := make([]impactCaseDTO, 0)
	if includes.impact && h.deps.CodeFacts != nil {
		impact = h.loadImpactCases(ctx, versionID, view)
	}

	dto := mindmapGraphDTO{
		VersionID:     idString(versionID),
		VersionStatus: view.VersionStatus,
		Editable:      view.Editable,
		Nodes:         nodes,
		Coverage:      newCoverageDTO(view.Coverage),
		Baseline:      newBaselineDTO(view.Baseline),
		LatestRuns:    latestRuns,
		ImpactCases:   impact,
	}
	if includes.findings {
		dto.Findings = newFindingDTOs(findings)
	}
	// The P5 graph is capped at 300 nodes (a bigger graph is 409 graph_too_large),
	// so a served graph always carries every node.
	w.Header().Set("X-Graph-Truncated", "false")
	writeData(w, http.StatusOK, dto)
}

// loadFindings returns every finding of the version. A process without the
// finding store simply has none.
func (h *Handler) loadFindings(ctx context.Context, versionID int64) ([]domain.CaseReviewFinding, error) {
	if h.deps.Findings == nil {
		return nil, nil
	}
	return h.deps.Findings.List(ctx, versionID, "", "", nil)
}

// blockerCountByNode counts the still-open blocker findings per node.
func blockerCountByNode(findings []domain.CaseReviewFinding) map[int64]int {
	counts := map[int64]int{}
	for _, finding := range findings {
		if finding.Status != domain.FindingOpen || finding.Severity != domain.SeverityBlocker {
			continue
		}
		if finding.NodeID == nil {
			continue
		}
		counts[*finding.NodeID]++
	}
	return counts
}

// loadAnchorIndex lists the case anchors of every case node. Anchor listing is
// best-effort: a code-intel outage must not blank the human graph read
// (spec §6.13 rule 11); the affected node just reports no anchors.
func (h *Handler) loadAnchorIndex(ctx context.Context, versionID int64, view application.GraphView) map[int64][]application.Anchor {
	index := make(map[int64][]application.Anchor, len(view.Nodes))
	for _, node := range view.Nodes {
		if node.Node.Kind != domain.NodeKindCase {
			continue
		}
		anchors, err := h.deps.Anchors.List(ctx, versionID, node.Node.ID)
		if err != nil {
			h.logger.WarnContext(ctx, "mindmap: listing case anchors failed",
				"version_id", versionID, "node_id", node.Node.ID, "error", err)
			continue
		}
		index[node.Node.ID] = anchors
	}
	return index
}

// loadLatestRuns resolves the latest run of each kind for the graph summary. A
// kind without any run is simply absent from the map (spec §7.3).
func (h *Handler) loadLatestRuns(ctx context.Context, versionID int64) map[string]*runSummaryDTO {
	runs := map[string]*runSummaryDTO{}
	for _, kind := range []string{
		domain.RunKindDesign, domain.RunKindReview, domain.RunKindDataPlan, domain.RunKindExplore,
	} {
		run, err := h.deps.RunStore.LatestByKind(ctx, versionID, kind)
		if err != nil || run.ID == 0 {
			continue
		}
		if summary := newRunSummaryDTO(run); summary != nil {
			runs[kind] = summary
		}
	}
	return runs
}

// loadImpactCases derives the read-only "possibly affected" hints from the latest
// P4 analysis candidates: each candidate subject is mapped back to the case nodes
// that cite it as a source (spec §7.3).
func (h *Handler) loadImpactCases(ctx context.Context, versionID int64, view application.GraphView) []impactCaseDTO {
	impact := make([]impactCaseDTO, 0)
	snapshot, err := h.deps.CodeFacts.GetLatestAnalysis(ctx, versionID)
	if err != nil || snapshot.AnalysisID == 0 {
		return impact
	}
	candidates, err := h.deps.CodeFacts.ListCandidates(ctx, snapshot.AnalysisID)
	if err != nil {
		h.logger.WarnContext(ctx, "mindmap: listing impact candidates failed",
			"version_id", versionID, "error", err)
		return impact
	}
	type subject struct {
		kind string
		id   int64
	}
	casesBySubject := map[subject][]int64{}
	for _, node := range view.Nodes {
		if node.Node.Kind != domain.NodeKindCase {
			continue
		}
		for _, source := range node.Sources {
			if source.RefID == nil {
				continue
			}
			key := subject{kind: source.RefKind, id: *source.RefID}
			casesBySubject[key] = append(casesBySubject[key], node.Node.ID)
		}
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		for _, caseID := range casesBySubject[subject{kind: candidate.SubjectKind, id: candidate.SubjectID}] {
			dedupe := idString(caseID) + "|" + candidate.Reason
			if seen[dedupe] {
				continue
			}
			seen[dedupe] = true
			impact = append(impact, impactCaseDTO{
				CaseNodeID: idString(caseID),
				Reason:     candidate.Reason,
				Confidence: candidate.Confidence,
				Source:     candidate.Source,
			})
		}
	}
	sort.SliceStable(impact, func(i, j int) bool { return impact[i].CaseNodeID < impact[j].CaseNodeID })
	return impact
}

// createNode handles POST /nodes (201).
func (h *Handler) createNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body createNodeRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	parentID, err := optionalBodyID("parentId", body.ParentID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	requirementIDs, err := parseIDStrings("requirementIds", body.RequirementIDs)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	cmd := application.CreateNodeCommand{
		Kind:           body.Kind,
		ParentID:       parentID,
		Title:          body.Title,
		Description:    body.Description,
		Priority:       body.Priority,
		Preconditions:  body.Preconditions,
		TestData:       rawBody(body.TestData),
		RequirementIDs: requirementIDs,
		Source:         body.Source,
	}
	node, err := h.deps.Graph.CreateNode(ctx, versionID, actorID(ctx), cmd)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	// A create returns the node alone; the child collections are fetched by the
	// next graph read. They are still rendered as empty arrays, never null.
	writeData(w, http.StatusCreated, newNodeDTO(node, nil, nil, nil, nil, 0))
}

// updateNode handles PATCH /nodes/{nodeId} (200).
func (h *Handler) updateNode(w http.ResponseWriter, r *http.Request) {
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
	var body updateNodeRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	var requirementIDs *[]int64
	if body.RequirementIDs != nil {
		parsed, err := parseIDStrings("requirementIds", *body.RequirementIDs)
		if err != nil {
			writeServiceError(w, r, err)
			return
		}
		requirementIDs = &parsed
	}
	// An absent testData keeps the stored document; an explicit null clears it
	// (spec §7.2 endpoint 3).
	var testData *json.RawMessage
	if len(body.TestData) > 0 {
		raw := json.RawMessage(body.TestData)
		testData = &raw
	}
	node, err := h.deps.Graph.UpdateNode(ctx, versionID, nodeID, actorID(ctx), application.UpdateNodeCommand{
		Revision:       body.Revision,
		Title:          body.Title,
		Description:    body.Description,
		Priority:       body.Priority,
		Preconditions:  body.Preconditions,
		TestData:       testData,
		RequirementIDs: requirementIDs,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newNodeDTO(node, nil, nil, nil, nil, 0))
}

// deleteNode handles DELETE /nodes/{nodeId} (204).
func (h *Handler) deleteNode(w http.ResponseWriter, r *http.Request) {
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
	revision, err := queryRevision(r, "revision")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if err := h.deps.Graph.DeleteNode(ctx, versionID, nodeID, actorID(ctx), revision); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeNoContent(w)
}

// reorderNodes handles PUT /order (204).
func (h *Handler) reorderNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body reorderRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	parentID, err := optionalBodyID("parentId", body.ParentID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	nodeIDs, err := parseIDStrings("nodeIds", body.NodeIDs)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if err := h.deps.Graph.Reorder(ctx, versionID, actorID(ctx), application.ReorderCommand{
		ParentID: parentID,
		NodeIDs:  nodeIDs,
	}); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeNoContent(w)
}

// createStep handles POST /nodes/{nodeId}/steps (201).
//
// The spec body carries no nodeRevision (the note of §7.2 derives it from the
// node of the path), while the application command requires the OCC token. The
// handler therefore loads the node and passes its current revision: a concurrent
// writer that moved the revision between this read and the service CAS still
// makes the write fail with case_revision_conflict inside the transaction.
func (h *Handler) createStep(w http.ResponseWriter, r *http.Request) {
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
	var body createStepRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	node, err := h.deps.Nodes.Get(ctx, versionID, nodeID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	step, err := h.deps.Graph.AddStep(ctx, versionID, nodeID, actorID(ctx), application.AddStepCommand{
		NodeRevision: node.Revision,
		Action:       body.Action,
		Expected:     body.Expected,
		Data:         body.Data,
		AfterSeq:     body.AfterSeq,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, newStepDTO(step))
}

// updateStep handles PATCH /steps/{stepId} (200).
func (h *Handler) updateStep(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	stepID, err := pathID(r, "stepId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body updateStepRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	step, err := h.deps.Graph.UpdateStep(ctx, versionID, stepID, actorID(ctx), application.UpdateStepCommand{
		NodeRevision: body.NodeRevision,
		Action:       body.Action,
		Expected:     body.Expected,
		Data:         body.Data,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newStepDTO(step))
}

// deleteStep handles DELETE /steps/{stepId} (204).
func (h *Handler) deleteStep(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	stepID, err := pathID(r, "stepId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	nodeRevision, err := queryRevision(r, "nodeRevision")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if err := h.deps.Graph.DeleteStep(ctx, versionID, stepID, actorID(ctx), nodeRevision); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeNoContent(w)
}

// reorderSteps handles PUT /nodes/{nodeId}/steps/order (204).
func (h *Handler) reorderSteps(w http.ResponseWriter, r *http.Request) {
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
	var body reorderStepsRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	stepIDs, err := parseIDStrings("stepIds", body.StepIDs)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if err := h.deps.Graph.ReorderSteps(ctx, versionID, nodeID, actorID(ctx), application.ReorderStepsCommand{
		NodeRevision: body.NodeRevision,
		StepIDs:      stepIDs,
	}); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeNoContent(w)
}

// createAssertion handles POST /nodes/{nodeId}/assertions (201). As with the
// step create, the node revision is derived from the path node.
func (h *Handler) createAssertion(w http.ResponseWriter, r *http.Request) {
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
	var body createAssertionRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	stepID, err := optionalBodyID("stepId", body.StepID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	node, err := h.deps.Nodes.Get(ctx, versionID, nodeID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	assertion, err := h.deps.Graph.AddAssertion(ctx, versionID, nodeID, actorID(ctx), application.AddAssertionCommand{
		NodeRevision: node.Revision,
		StepID:       stepID,
		Kind:         body.Kind,
		Target:       body.Target,
		Expression:   body.Expression,
		Expected:     body.Expected,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, newAssertionDTO(assertion))
}

// updateAssertion handles PATCH /assertions/{assertionId} (200).
func (h *Handler) updateAssertion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	assertionID, err := pathID(r, "assertionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body updateAssertionRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	assertion, err := h.deps.Graph.UpdateAssertion(ctx, versionID, assertionID, actorID(ctx), application.UpdateAssertionCommand{
		NodeRevision: body.NodeRevision,
		Kind:         body.Kind,
		Target:       body.Target,
		Expression:   body.Expression,
		Expected:     body.Expected,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newAssertionDTO(assertion))
}

// deleteAssertion handles DELETE /assertions/{assertionId} (204).
func (h *Handler) deleteAssertion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	assertionID, err := pathID(r, "assertionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	nodeRevision, err := queryRevision(r, "nodeRevision")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if err := h.deps.Graph.DeleteAssertion(ctx, versionID, assertionID, actorID(ctx), nodeRevision); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeNoContent(w)
}

// createSource handles POST /nodes/{nodeId}/sources (201).
func (h *Handler) createSource(w http.ResponseWriter, r *http.Request) {
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
	var body createSourceRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	refID, err := optionalBodyID("refId", body.RefID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	source, err := h.deps.Graph.AddSource(ctx, versionID, nodeID, actorID(ctx), application.AddSourceCommand{
		RefKind: body.RefKind,
		RefID:   refID,
		RefKey:  body.RefKey,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, newSourceDTO(source))
}

// deleteSource handles DELETE /sources/{sourceId} (204).
func (h *Handler) deleteSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	sourceID, err := pathID(r, "sourceId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if err := h.deps.Graph.DeleteSource(ctx, versionID, sourceID, actorID(ctx)); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeNoContent(w)
}

// createAnchor handles POST /nodes/{nodeId}/anchors (201). The case ownership is
// validated here (the codeintel context only stores subject_kind='case', D37) and
// the anchor is created through the codeintel gateway.
func (h *Handler) createAnchor(w http.ResponseWriter, r *http.Request) {
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
	var body createAnchorRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	node, err := h.deps.Nodes.Get(ctx, versionID, nodeID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if node.Kind != domain.NodeKindCase {
		writeServiceError(w, r, domain.NewInvalid("nodeId", "invalid_case_tree",
			"code anchors attach to case nodes only"))
		return
	}
	anchor, err := h.deps.Anchors.Create(ctx, versionID, actorID(ctx), application.AnchorParams{
		SubjectID:  node.ID,
		FilePath:   body.FilePath,
		StartLine:  body.StartLine,
		EndLine:    body.EndLine,
		Confidence: body.Confidence,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, newAnchorDTO(anchor))
}

// deleteAnchor handles DELETE /anchors/{anchorId} (204). Spec §7.2 endpoint 16
// allows an administrator or the anchor creator. The port has no
// get-anchor-by-id, so the creator path scans the case anchors of the version
// (the same per-node listing the graph read uses); an administrator deletes
// directly and lets the codeintel not-found surface as 404.
func (h *Handler) deleteAnchor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	anchorID, err := pathID(r, "anchorId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	baseline, err := h.deps.Baselines.GetByVersion(ctx, versionID)
	if err != nil {
		if !notFound(err) {
			writeServiceError(w, r, err)
			return
		}
	} else if baseline.ID != 0 {
		writeServiceError(w, r, domain.NewConflict("baseline_frozen",
			"the case graph is frozen and its anchors are read-only"))
		return
	}

	actor := actorID(ctx)
	if !isAdmin(ctx) {
		anchor, found, err := h.findAnchor(ctx, versionID, anchorID)
		if err != nil {
			writeServiceError(w, r, err)
			return
		}
		if found && anchor.CreatedBy != actor {
			writeServiceError(w, r, newForbidden("forbidden",
				"only an administrator or the anchor creator may delete this anchor"))
			return
		}
	}
	if err := h.deps.Anchors.Delete(ctx, versionID, anchorID); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeNoContent(w)
}

// findAnchor resolves one anchor of the version by scanning the case anchors of
// every node. It is only used by the non-admin delete path.
func (h *Handler) findAnchor(ctx context.Context, versionID, anchorID int64) (application.Anchor, bool, error) {
	nodes, err := h.deps.Nodes.ListByVersion(ctx, versionID)
	if err != nil {
		return application.Anchor{}, false, err
	}
	for _, node := range nodes {
		if node.Kind != domain.NodeKindCase {
			continue
		}
		anchors, err := h.deps.Anchors.List(ctx, versionID, node.ID)
		if err != nil {
			if notFound(err) {
				continue
			}
			return application.Anchor{}, false, err
		}
		for _, anchor := range anchors {
			if anchor.ID == anchorID {
				return anchor, true, nil
			}
		}
	}
	return application.Anchor{}, false, nil
}

// optionalBodyID parses one optional decimal-string body id.
func optionalBodyID(field string, raw *string) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := parseIDString(field, *raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// rawBody returns the raw JSON document of one optional body field.
func rawBody(raw *json.RawMessage) []byte {
	if raw == nil {
		return nil
	}
	return append([]byte(nil), (*raw)...)
}
