package httpapi

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// The HTTP DTOs of spec §7.2/§7.3. They are deliberately separate from the
// domain entities: every mindmap id is serialized as a decimal string (D29), the
// free-form testData document is passed through untouched and nullable values are
// typed pointers so the JSON keeps its null keys.

// idString renders one mindmap id as the decimal string of spec §7.1/D29.
func idString(id int64) string { return strconv.FormatInt(id, 10) }

// optIDString renders one optional id, keeping JSON null for an absent value.
func optIDString(id *int64) *string {
	if id == nil {
		return nil
	}
	rendered := idString(*id)
	return &rendered
}

// idStrings renders a non-nil array of decimal string ids.
func idStrings(ids []int64) []string {
	rendered := make([]string, 0, len(ids))
	for _, id := range ids {
		rendered = append(rendered, idString(id))
	}
	return rendered
}

// parseIDString parses one decimal-string request id. A malformed or non-positive
// value is a 400 invalid_argument (the malformed-body class of spec §7.1).
func parseIDString(field, raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, newBadRequest(field, "invalid_id", field+" must be a positive decimal id")
	}
	return id, nil
}

// parseIDStrings parses one optional array of decimal-string ids.
func parseIDStrings(field string, raw []string) ([]int64, error) {
	if raw == nil {
		return nil, nil
	}
	ids := make([]int64, 0, len(raw))
	for _, value := range raw {
		id, err := parseIDString(field, value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// testDataJSON renders one stored testData document. An empty document is JSON
// null, exactly like the §7.3 contract expects for an unset value.
func testDataJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return json.RawMessage(raw)
}

// caseNodeDTO is CaseNodeView (spec §7.3).
type caseNodeDTO struct {
	ID               string             `json:"id"`
	ParentID         *string            `json:"parentId"`
	Kind             string             `json:"kind"`
	Title            string             `json:"title"`
	Description      string             `json:"description"`
	Priority         string             `json:"priority"`
	Preconditions    string             `json:"preconditions"`
	TestData         json.RawMessage    `json:"testData"`
	Source           string             `json:"source"`
	Status           string             `json:"status"`
	SortOrder        int                `json:"sortOrder"`
	Revision         int                `json:"revision"`
	Steps            []caseStepDTO      `json:"steps"`
	Assertions       []caseAssertionDTO `json:"assertions"`
	Sources          []caseSourceDTO    `json:"sources"`
	Anchors          []codeAnchorDTO    `json:"anchors"`
	OpenBlockerCount int                `json:"openBlockerCount"`
}

// newNodeDTO renders one node with its child collections. Every collection is a
// non-nil array so the JSON never carries null where the contract wants [].
func newNodeDTO(
	node domain.CaseNode,
	steps []domain.CaseStep,
	assertions []domain.CaseAssertion,
	sources []domain.CaseSource,
	anchors []application.Anchor,
	openBlockers int,
) caseNodeDTO {
	return caseNodeDTO{
		ID:               idString(node.ID),
		ParentID:         optIDString(node.ParentID),
		Kind:             node.Kind,
		Title:            node.Title,
		Description:      node.Description,
		Priority:         node.Priority,
		Preconditions:    node.Preconditions,
		TestData:         testDataJSON(node.TestData),
		Source:           node.Source,
		Status:           node.Status,
		SortOrder:        node.SortOrder,
		Revision:         node.Revision,
		Steps:            newStepDTOs(steps),
		Assertions:       newAssertionDTOs(assertions),
		Sources:          newSourceDTOs(sources),
		Anchors:          newAnchorDTOs(anchors),
		OpenBlockerCount: openBlockers,
	}
}

// caseStepDTO is CaseStepView (spec §7.3).
type caseStepDTO struct {
	ID       string `json:"id"`
	Seq      int    `json:"seq"`
	Action   string `json:"action"`
	Expected string `json:"expected"`
	Data     string `json:"data"`
	Source   string `json:"source"`
}

// newStepDTO renders one step.
func newStepDTO(step domain.CaseStep) caseStepDTO {
	return caseStepDTO{
		ID:       idString(step.ID),
		Seq:      step.Seq,
		Action:   step.Action,
		Expected: step.Expected,
		Data:     step.Data,
		Source:   step.Source,
	}
}

// newStepDTOs renders a non-nil step array.
func newStepDTOs(steps []domain.CaseStep) []caseStepDTO {
	DTOs := make([]caseStepDTO, 0, len(steps))
	for _, step := range steps {
		DTOs = append(DTOs, newStepDTO(step))
	}
	return DTOs
}

// caseAssertionDTO is CaseAssertionView (spec §7.3).
type caseAssertionDTO struct {
	ID         string  `json:"id"`
	StepID     *string `json:"stepId"`
	Kind       string  `json:"kind"`
	Target     string  `json:"target"`
	Expression string  `json:"expression"`
	Expected   string  `json:"expected"`
	Source     string  `json:"source"`
}

// newAssertionDTO renders one assertion.
func newAssertionDTO(assertion domain.CaseAssertion) caseAssertionDTO {
	return caseAssertionDTO{
		ID:         idString(assertion.ID),
		StepID:     optIDString(assertion.StepID),
		Kind:       assertion.Kind,
		Target:     assertion.Target,
		Expression: assertion.Expression,
		Expected:   assertion.Expected,
		Source:     assertion.Source,
	}
}

// newAssertionDTOs renders a non-nil assertion array.
func newAssertionDTOs(assertions []domain.CaseAssertion) []caseAssertionDTO {
	DTOs := make([]caseAssertionDTO, 0, len(assertions))
	for _, assertion := range assertions {
		DTOs = append(DTOs, newAssertionDTO(assertion))
	}
	return DTOs
}

// caseSourceDTO is CaseSourceView (spec §7.3).
type caseSourceDTO struct {
	ID         string  `json:"id"`
	RefKind    string  `json:"refKind"`
	RefID      *string `json:"refId"`
	RefKey     string  `json:"refKey"`
	Source     string  `json:"source"`
	Confidence string  `json:"confidence"`
}

// newSourceDTO renders one source.
func newSourceDTO(source domain.CaseSource) caseSourceDTO {
	return caseSourceDTO{
		ID:         idString(source.ID),
		RefKind:    source.RefKind,
		RefID:      optIDString(source.RefID),
		RefKey:     source.RefKey,
		Source:     source.Source,
		Confidence: source.Confidence,
	}
}

// newSourceDTOs renders a non-nil source array.
func newSourceDTOs(sources []domain.CaseSource) []caseSourceDTO {
	DTOs := make([]caseSourceDTO, 0, len(sources))
	for _, source := range sources {
		DTOs = append(DTOs, newSourceDTO(source))
	}
	return DTOs
}

// codeAnchorDTO is CodeAnchorView (spec §7.3).
type codeAnchorDTO struct {
	ID         string  `json:"id"`
	FilePath   string  `json:"filePath"`
	StartLine  *int    `json:"startLine"`
	EndLine    *int    `json:"endLine"`
	Confidence string  `json:"confidence"`
	CommitSHA  *string `json:"commitSha"`
}

// newAnchorDTO renders one case anchor.
func newAnchorDTO(anchor application.Anchor) codeAnchorDTO {
	return codeAnchorDTO{
		ID:         idString(anchor.ID),
		FilePath:   anchor.FilePath,
		StartLine:  anchor.StartLine,
		EndLine:    anchor.EndLine,
		Confidence: anchor.Confidence,
		CommitSHA:  anchor.CommitSHA,
	}
}

// newAnchorDTOs renders a non-nil anchor array.
func newAnchorDTOs(anchors []application.Anchor) []codeAnchorDTO {
	DTOs := make([]codeAnchorDTO, 0, len(anchors))
	for _, anchor := range anchors {
		DTOs = append(DTOs, newAnchorDTO(anchor))
	}
	return DTOs
}

// coverageCellDTO is CoverageCell (spec §7.3).
type coverageCellDTO struct {
	RequirementID string   `json:"requirementId"`
	CaseIDs       []string `json:"caseIds"`
}

// coverageDTO is CoverageView (spec §7.3).
type coverageDTO struct {
	RequirementIDs          []string          `json:"requirementIds"`
	UncoveredRequirementIDs []string          `json:"uncoveredRequirementIds"`
	StaleRequirementIDs     []string          `json:"staleRequirementIds"`
	Matrix                  []coverageCellDTO `json:"matrix"`
}

// newCoverageDTO renders the coverage projection with decimal string ids.
func newCoverageDTO(coverage domain.Coverage) coverageDTO {
	matrix := make([]coverageCellDTO, 0, len(coverage.Matrix))
	for _, cell := range coverage.Matrix {
		matrix = append(matrix, coverageCellDTO{
			RequirementID: idString(cell.RequirementID),
			CaseIDs:       idStrings(cell.CaseIDs),
		})
	}
	return coverageDTO{
		RequirementIDs:          idStrings(coverage.RequirementIDs),
		UncoveredRequirementIDs: idStrings(coverage.UncoveredRequirementIDs),
		StaleRequirementIDs:     idStrings(coverage.StaleRequirementIDs),
		Matrix:                  matrix,
	}
}

// baselineDTO is BaselineView (spec §7.3).
type baselineDTO struct {
	ID                 string    `json:"id"`
	GraphHash          string    `json:"graphHash"`
	CaseCount          int       `json:"caseCount"`
	NodeCount          int       `json:"nodeCount"`
	FrozenAt           time.Time `json:"frozenAt"`
	CodeBaselineCommit *string   `json:"codeBaselineCommit"`
}

// newBaselineDTO renders the frozen baseline, or JSON null before approval.
func newBaselineDTO(baseline *domain.CaseBaseline) *baselineDTO {
	if baseline == nil {
		return nil
	}
	return &baselineDTO{
		ID:                 idString(baseline.ID),
		GraphHash:          baseline.GraphHash,
		CaseCount:          baseline.CaseCount,
		NodeCount:          baseline.NodeCount,
		FrozenAt:           baseline.FrozenAt,
		CodeBaselineCommit: baseline.CodeBaselineCommit,
	}
}

// runSummaryDTO is RunSummaryView inside the graph response (spec §7.3).
type runSummaryDTO struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind,omitempty"`
	Status     string     `json:"status"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// newRunSummaryDTO renders the latest-run summary of one kind.
func newRunSummaryDTO(run domain.CaseDesignRun) *runSummaryDTO {
	if run.ID == 0 {
		return nil
	}
	return &runSummaryDTO{
		ID:         idString(run.ID),
		Kind:       run.Kind,
		Status:     run.Status,
		FinishedAt: run.FinishedAt,
	}
}

// impactCaseDTO is ImpactCaseView (spec §7.3).
type impactCaseDTO struct {
	CaseNodeID string `json:"caseNodeId"`
	Reason     string `json:"reason"`
	Confidence string `json:"confidence"`
	Source     string `json:"source"`
}

// mindmapGraphDTO is the GET /mindmap response (spec §7.3).
type mindmapGraphDTO struct {
	VersionID     string                    `json:"versionId"`
	VersionStatus string                    `json:"versionStatus"`
	Editable      bool                      `json:"editable"`
	Nodes         []caseNodeDTO             `json:"nodes"`
	Coverage      coverageDTO               `json:"coverage"`
	Baseline      *baselineDTO              `json:"baseline"`
	LatestRuns    map[string]*runSummaryDTO `json:"latestRuns"`
	ImpactCases   []impactCaseDTO           `json:"impactCases"`
	Findings      []caseReviewFindingDTO    `json:"findings,omitempty"`
}

// caseRunErrorDTO is CaseRunError (spec §7.3): a non-fatal run error, never any
// LLM or mindmap body text.
type caseRunErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// caseRunDTO is CaseRunView (spec §7.2 endpoints 17-19).
type caseRunDTO struct {
	ID             string            `json:"id"`
	VersionID      string            `json:"versionId"`
	Kind           string            `json:"kind"`
	Status         string            `json:"status"`
	Reason         string            `json:"reason"`
	Stats          json.RawMessage   `json:"stats"`
	Errors         []caseRunErrorDTO `json:"errors"`
	AgentSessionID *int64            `json:"agentSessionId"`
	AgentRunID     *int64            `json:"agentRunId"`
	Model          string            `json:"model"`
	PromptVersion  string            `json:"promptVersion"`
	CreatedBy      int64             `json:"createdBy"`
	CreatedAt      time.Time         `json:"createdAt"`
	StartedAt      *time.Time        `json:"startedAt"`
	FinishedAt     *time.Time        `json:"finishedAt"`
}

// runErrorsDocument is the stored errors JSONB of one run (spec §8.4): the stable
// reason plus the capped validation messages. No other field is read, so a run
// detail can never leak a proposal body.
type runErrorsDocument struct {
	Reason           string   `json:"reason"`
	ValidationErrors []string `json:"validationErrors"`
}

// decodeRunErrors decodes the stored run error document, tolerating a missing or
// malformed document (a run that never failed has none).
func decodeRunErrors(raw []byte) runErrorsDocument {
	var document runErrorsDocument
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &document)
	}
	return document
}

// newRunDTO renders one run detail/listing row.
func newRunDTO(run domain.CaseDesignRun) caseRunDTO {
	document := decodeRunErrors(run.Errors)
	errorsDTO := make([]caseRunErrorDTO, 0, len(document.ValidationErrors)+1)
	for _, message := range document.ValidationErrors {
		errorsDTO = append(errorsDTO, caseRunErrorDTO{Code: "validation_error", Message: message})
	}
	if len(errorsDTO) == 0 && document.Reason != "" {
		errorsDTO = append(errorsDTO, caseRunErrorDTO{Code: document.Reason, Message: document.Reason})
	}
	var stats json.RawMessage
	if len(run.Stats) > 0 {
		stats = json.RawMessage(run.Stats)
	}
	return caseRunDTO{
		ID:             idString(run.ID),
		VersionID:      idString(run.VersionID),
		Kind:           run.Kind,
		Status:         run.Status,
		Reason:         document.Reason,
		Stats:          stats,
		Errors:         errorsDTO,
		AgentSessionID: run.AgentSessionID,
		AgentRunID:     run.AgentRunID,
		Model:          run.Model,
		PromptVersion:  run.PromptVersion,
		CreatedBy:      run.CreatedBy,
		CreatedAt:      run.CreatedAt,
		StartedAt:      run.StartedAt,
		FinishedAt:     run.FinishedAt,
	}
}

// caseReviewDTO is CaseReviewView (spec §7.3).
type caseReviewDTO struct {
	ID         string     `json:"id"`
	VersionID  string     `json:"versionId"`
	RunID      *string    `json:"runId"`
	Status     string     `json:"status"`
	Comment    string     `json:"comment"`
	ReviewerID int64      `json:"reviewerId"`
	DecidedBy  *int64     `json:"decidedBy"`
	DecidedAt  *time.Time `json:"decidedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// newReviewDTO renders one review round.
func newReviewDTO(review domain.CaseReview) caseReviewDTO {
	return caseReviewDTO{
		ID:         idString(review.ID),
		VersionID:  idString(review.VersionID),
		RunID:      optIDString(review.RunID),
		Status:     review.Status,
		Comment:    review.Comment,
		ReviewerID: review.ReviewerID,
		DecidedBy:  review.DecidedBy,
		DecidedAt:  review.DecidedAt,
		CreatedAt:  review.CreatedAt,
		UpdatedAt:  review.UpdatedAt,
	}
}

// caseReviewFindingDTO is CaseReviewFindingView (spec §7.3).
type caseReviewFindingDTO struct {
	ID        string    `json:"id"`
	ReviewID  string    `json:"reviewId"`
	NodeID    *string   `json:"nodeId"`
	Category  string    `json:"category"`
	Severity  string    `json:"severity"`
	Detail    string    `json:"detail"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	CreatedBy int64     `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// newFindingDTO renders one review finding.
func newFindingDTO(finding domain.CaseReviewFinding) caseReviewFindingDTO {
	return caseReviewFindingDTO{
		ID:        idString(finding.ID),
		ReviewID:  idString(finding.ReviewID),
		NodeID:    optIDString(finding.NodeID),
		Category:  finding.Category,
		Severity:  finding.Severity,
		Detail:    finding.Detail,
		Status:    finding.Status,
		Source:    finding.Source,
		CreatedBy: finding.CreatedBy,
		CreatedAt: finding.CreatedAt,
		UpdatedAt: finding.UpdatedAt,
	}
}

// newFindingDTOs renders a non-nil finding array.
func newFindingDTOs(findings []domain.CaseReviewFinding) []caseReviewFindingDTO {
	DTOs := make([]caseReviewFindingDTO, 0, len(findings))
	for _, finding := range findings {
		DTOs = append(DTOs, newFindingDTO(finding))
	}
	return DTOs
}

// caseExportDTO is CaseExportView (D30/D38). The internal file path is never
// exposed; fileName is the suggested attachment name of the download.
type caseExportDTO struct {
	ID         string    `json:"id"`
	VersionID  string    `json:"versionId"`
	Format     string    `json:"format"`
	NodeCount  int       `json:"nodeCount"`
	SizeBytes  int64     `json:"sizeBytes"`
	BaselineID *string   `json:"baselineId"`
	CreatedAt  time.Time `json:"createdAt"`
	SHA256     string    `json:"sha256"`
	FileName   string    `json:"fileName"`
}

// newExportDTO renders one export metadata row.
func newExportDTO(export domain.CaseExport) caseExportDTO {
	return caseExportDTO{
		ID:         idString(export.ID),
		VersionID:  idString(export.VersionID),
		Format:     export.Format,
		NodeCount:  export.NodeCount,
		SizeBytes:  export.SizeBytes,
		BaselineID: optIDString(export.BaselineID),
		CreatedAt:  export.CreatedAt,
		SHA256:     export.FileSHA256,
		FileName:   exportFileName(export),
	}
}

// versionView is the P3 version projection returned by submit-review and review.
// The mindmap context only knows the VersionLifecycle port (spec §4.3), whose
// VersionSnapshot carries exactly these fields; rendering the fuller P3
// VersionDTO would require importing the version interfaces layer, which the
// dependency rule (§4.2) forbids and the port does not expose. Documented
// hand-off: a full VersionDTO needs a port addition (Lead-owned).
type versionView struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Status             string  `json:"status"`
	SystemID           *int64  `json:"systemId"`
	EnvironmentID      *int64  `json:"environmentId"`
	CodeRepositoryID   *int64  `json:"codeRepositoryId"`
	CodeBaselineCommit *string `json:"codeBaselineCommit"`
}

// newVersionView renders the version subset available through the port.
func newVersionView(version application.VersionSnapshot) versionView {
	return versionView{
		ID:                 idString(version.ID),
		Name:               version.Name,
		Status:             version.Status,
		SystemID:           version.SystemID,
		EnvironmentID:      version.EnvironmentID,
		CodeRepositoryID:   version.CodeRepositoryID,
		CodeBaselineCommit: version.CodeBaselineCommit,
	}
}

// submitReviewResultDTO is the POST /submit-review response.
type submitReviewResultDTO struct {
	Version versionView `json:"version"`
}

// reviewResultDTO is the POST /review response: baseline is present on approval
// and null on rejection.
type reviewResultDTO struct {
	Version  versionView   `json:"version"`
	Baseline *baselineDTO  `json:"baseline"`
	Review   caseReviewDTO `json:"review"`
}

// startRunResultDTO is the POST /runs response.
type startRunResultDTO struct {
	RunID string `json:"runId"`
}
