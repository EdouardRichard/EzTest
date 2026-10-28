package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Analysis run kinds, statuses and telemetry names of spec §4.2.3/§5.6/§8.1.
const (
	// AnalysisKindFailure is the failure-analysis agent run.
	AnalysisKindFailure = "failure"
	// AnalysisKindIssueTriage is the issue-triage agent run.
	AnalysisKindIssueTriage = "issue_triage"

	// AnalysisQueued is a run row created but not yet started with an agent.
	AnalysisQueued = "queued"
	// AnalysisRunning is a run whose agent session is being polled.
	AnalysisRunning = "running"
	// AnalysisDone is a run whose structured submission was materialized.
	AnalysisDone = "done"
	// AnalysisFailed is a run whose agent failed or could not be started.
	AnalysisFailed = "failed"
	// AnalysisDegraded is a run that produced no usable result but must not
	// block the autonomous batch chain (spec §9.7).
	AnalysisDegraded = "degraded"

	// EventAnalysisStarted is published when a run moves to running.
	EventAnalysisStarted = "analysis.started"
	// EventAnalysisFinished is published when a run reaches a terminal status.
	EventAnalysisFinished = "analysis.finished"

	// ReasonAnalysisActive is the 409 reason of the unique active run.
	ReasonAnalysisActive = "analysis_active"
	// ReasonAnalysisNotApplicable is the 409 reason of a run whose batch state
	// does not allow it (no failed application case, no failure candidates).
	ReasonAnalysisNotApplicable = "analysis_not_applicable"
	// ReasonAnalysisNotFound is the 404 reason of an unknown analysis run.
	ReasonAnalysisNotFound = "analysis_not_found"
	// ReasonStaleRunRecovered is the failure reason written by boot recovery.
	ReasonStaleRunRecovered = "stale_run_recovered"
	// ReasonAIUnavailable is the failure reason of an unavailable LLM.
	ReasonAIUnavailable = "ai_unavailable"
	// ReasonNoCandidates is the degradation reason of an empty submission.
	ReasonNoCandidates = "no_candidates"
	// ReasonMaterializeFailed is the failure reason of a failed materialization.
	ReasonMaterializeFailed = "materialize_failed"

	// AnalysisPromptVersionFailure is the failure-analysis prompt version of
	// spec §9.1; the profile seed (T13) must keep it in sync.
	AnalysisPromptVersionFailure = "p7-failure-v1"
	// AnalysisPromptVersionTriage is the issue-triage prompt version of §9.1.
	AnalysisPromptVersionTriage = "p7-triage-v1"

	// AnalysisMaxFailureCandidates is the §5.6 case-run/candidate cap.
	AnalysisMaxFailureCandidates = 20
	// AnalysisMaxIssueDrafts is the §9.4 draft cap.
	AnalysisMaxIssueDrafts = 20
)

// AnalysisRun is one failure-analysis or issue-triage run (spec §4.2.3).
// Input/Result/Stats/Errors are JSONB documents; every id inside them is a
// decimal string (D29) and no prompt, evidence body or agent raw text is ever
// recorded.
type AnalysisRun struct {
	ID             int64
	VersionID      int64
	BatchID        int64
	Kind           string
	Status         string
	Model          string
	PromptVersion  string
	AgentSessionID *int64
	AgentRunID     *int64
	Input          json.RawMessage
	Result         json.RawMessage
	Stats          json.RawMessage
	Errors         json.RawMessage
	CreatedBy      int64
	StartedAt      *time.Time
	FinishedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IsActive reports whether the run still occupies the (batch_id, kind)
// single-flight slot of uk_analysis_run_active.
func (r AnalysisRun) IsActive() bool {
	return r.Status == AnalysisQueued || r.Status == AnalysisRunning
}

// AnalysisView is the read projection returned by the analysis endpoints
// (spec §7.5). Ids are decimal strings because the view crosses the JSON
// boundary; Result/Stats/Errors are passed through verbatim.
type AnalysisView struct {
	ID            string          `json:"id"`
	VersionID     string          `json:"versionId"`
	BatchID       string          `json:"batchId"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	Model         string          `json:"model"`
	PromptVersion string          `json:"promptVersion"`
	Result        json.RawMessage `json:"result,omitempty"`
	Stats         json.RawMessage `json:"stats,omitempty"`
	Errors        json.RawMessage `json:"errors,omitempty"`
	StartedAt     *time.Time      `json:"startedAt"`
	FinishedAt    *time.Time      `json:"finishedAt"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// NewAnalysisView builds the JSON-safe projection of run.
func NewAnalysisView(run AnalysisRun) AnalysisView {
	return AnalysisView{
		ID:            strconv.FormatInt(run.ID, 10),
		VersionID:     strconv.FormatInt(run.VersionID, 10),
		BatchID:       strconv.FormatInt(run.BatchID, 10),
		Kind:          run.Kind,
		Status:        run.Status,
		Model:         run.Model,
		PromptVersion: run.PromptVersion,
		Result:        run.Result,
		Stats:         run.Stats,
		Errors:        run.Errors,
		StartedAt:     run.StartedAt,
		FinishedAt:    run.FinishedAt,
		CreatedAt:     run.CreatedAt,
	}
}

// FailureCandidate is one validated failure-analysis candidate (spec §5.6).
// The JSON form uses decimal string ids and never carries prompt or evidence
// bytes.
type FailureCandidate struct {
	CaseNodeID      int64     `json:"-"`
	CaseRunID       int64     `json:"-"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	Severity        string    `json:"severity"`
	Confidence      string    `json:"confidence"`
	FailureClass    string    `json:"failureClass"`
	RerunSuggestion string    `json:"rerunSuggestion,omitempty"`
	CodeRefs        []CodeRef `json:"-"`
	EvidenceIDs     []int64   `json:"-"`
}

// analysisJSONID is an int64 that marshals as a decimal JSON string and accepts
// either a string or a number when unmarshalling (D29).
type analysisJSONID int64

// MarshalJSON implements json.Marshaler.
func (id analysisJSONID) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(id), 10))
}

// UnmarshalJSON implements json.Unmarshaler and accepts both forms so an older
// numeric document still decodes.
func (id *analysisJSONID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*id = 0
		return nil
	}
	raw = strings.Trim(raw, `"`)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("delivery: invalid id %q: %w", raw, err)
	}
	*id = analysisJSONID(value)
	return nil
}

// analysisCodeRefDocument is the JSON form of CodeRef: file path, 1-based line
// and a short reason.
type analysisCodeRefDocument struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// analysisCodeRefDocuments converts code refs to their JSON documents.
func analysisCodeRefDocuments(refs []CodeRef) []analysisCodeRefDocument {
	if len(refs) == 0 {
		return nil
	}
	documents := make([]analysisCodeRefDocument, 0, len(refs))
	for _, ref := range refs {
		documents = append(documents, analysisCodeRefDocument{Path: ref.Path, Line: ref.Line, Reason: ref.Reason})
	}
	return documents
}

// analysisCodeRefs converts JSON documents back to code refs.
func analysisCodeRefs(documents []analysisCodeRefDocument) []CodeRef {
	if len(documents) == 0 {
		return nil
	}
	refs := make([]CodeRef, 0, len(documents))
	for _, document := range documents {
		refs = append(refs, CodeRef{Path: document.Path, Line: document.Line, Reason: document.Reason})
	}
	return refs
}

// failureCandidateDocument is the persisted JSON form of one candidate.
type failureCandidateDocument struct {
	CaseNodeID      analysisJSONID            `json:"caseNodeId"`
	CaseRunID       analysisJSONID            `json:"caseRunId"`
	Title           string                    `json:"title"`
	Description     string                    `json:"description"`
	Severity        string                    `json:"severity"`
	Confidence      string                    `json:"confidence"`
	FailureClass    string                    `json:"failureClass"`
	RerunSuggestion string                    `json:"rerunSuggestion,omitempty"`
	CodeRefs        []analysisCodeRefDocument `json:"codeRefs,omitempty"`
	EvidenceIDs     []analysisJSONID          `json:"evidenceIds,omitempty"`
}

// MarshalJSON implements json.Marshaler with decimal string ids.
func (c FailureCandidate) MarshalJSON() ([]byte, error) {
	return json.Marshal(failureCandidateDocument{
		CaseNodeID:      analysisJSONID(c.CaseNodeID),
		CaseRunID:       analysisJSONID(c.CaseRunID),
		Title:           c.Title,
		Description:     c.Description,
		Severity:        c.Severity,
		Confidence:      c.Confidence,
		FailureClass:    c.FailureClass,
		RerunSuggestion: c.RerunSuggestion,
		CodeRefs:        analysisCodeRefDocuments(c.CodeRefs),
		EvidenceIDs:     analysisEvidenceIDs(c.EvidenceIDs),
	})
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *FailureCandidate) UnmarshalJSON(data []byte) error {
	var document failureCandidateDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	*c = FailureCandidate{
		CaseNodeID:      int64(document.CaseNodeID),
		CaseRunID:       int64(document.CaseRunID),
		Title:           document.Title,
		Description:     document.Description,
		Severity:        document.Severity,
		Confidence:      document.Confidence,
		FailureClass:    document.FailureClass,
		RerunSuggestion: document.RerunSuggestion,
		CodeRefs:        analysisCodeRefs(document.CodeRefs),
		EvidenceIDs:     analysisInt64IDs(document.EvidenceIDs),
	}
	return nil
}

// analysisEvidenceIDs converts evidence ids to their JSON form.
func analysisEvidenceIDs(ids []int64) []analysisJSONID {
	if len(ids) == 0 {
		return nil
	}
	converted := make([]analysisJSONID, 0, len(ids))
	for _, id := range ids {
		converted = append(converted, analysisJSONID(id))
	}
	return converted
}

// analysisInt64IDs converts JSON ids back to int64.
func analysisInt64IDs(ids []analysisJSONID) []int64 {
	if len(ids) == 0 {
		return nil
	}
	converted := make([]int64, 0, len(ids))
	for _, id := range ids {
		converted = append(converted, int64(id))
	}
	return converted
}

// FailureInput is the failure-analysis input document of spec §5.6: only the
// failed(application) case run ids of the batch (≤20).
type FailureInput struct {
	VersionID  int64
	BatchID    int64
	CaseRunIDs []int64
}

// failureInputDocument is the persisted form of FailureInput.
type failureInputDocument struct {
	VersionID  analysisJSONID   `json:"versionId"`
	BatchID    analysisJSONID   `json:"batchId"`
	CaseRunIDs []analysisJSONID `json:"caseRunIds"`
}

// EncodeFailureInput builds the failure-analysis input document.
func EncodeFailureInput(versionID, batchID int64, caseRunIDs []int64) json.RawMessage {
	document := failureInputDocument{
		VersionID:  analysisJSONID(versionID),
		BatchID:    analysisJSONID(batchID),
		CaseRunIDs: analysisEvidenceIDs(caseRunIDs),
	}
	return analysisMarshal(document)
}

// DecodeFailureInput parses a failure-analysis input document.
func DecodeFailureInput(raw json.RawMessage) (FailureInput, error) {
	var document failureInputDocument
	if len(raw) == 0 {
		return FailureInput{}, nil
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return FailureInput{}, fmt.Errorf("delivery: decode failure input: %w", err)
	}
	return FailureInput{
		VersionID:  int64(document.VersionID),
		BatchID:    int64(document.BatchID),
		CaseRunIDs: analysisInt64IDs(document.CaseRunIDs),
	}, nil
}

// FailureResult is the failure-analysis result document: the validated
// candidates plus short non-sensitive warnings.
type FailureResult struct {
	Candidates []FailureCandidate
	Warnings   []string
}

// failureResultDocument is the persisted form of FailureResult.
type failureResultDocument struct {
	Candidates []FailureCandidate `json:"candidates"`
	Warnings   []string           `json:"warnings,omitempty"`
}

// EncodeFailureResult builds the failure-analysis result document.
func EncodeFailureResult(candidates []FailureCandidate, warnings []string) json.RawMessage {
	return analysisMarshal(failureResultDocument{Candidates: candidates, Warnings: warnings})
}

// DecodeFailureResult parses a failure-analysis result document.
func DecodeFailureResult(raw json.RawMessage) (FailureResult, error) {
	if len(raw) == 0 {
		return FailureResult{}, nil
	}
	var document failureResultDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return FailureResult{}, fmt.Errorf("delivery: decode failure result: %w", err)
	}
	return FailureResult{Candidates: document.Candidates, Warnings: document.Warnings}, nil
}

// OpenIssue is the short existing-issue summary injected into the triage input
// (spec §5.6: open issues only; no description, dedupe key or evidence body).
type OpenIssue struct {
	IssueID  int64
	Title    string
	Severity string
}

// openIssueDocument is the persisted form of OpenIssue.
type openIssueDocument struct {
	IssueID  analysisJSONID `json:"issueId"`
	Title    string         `json:"title"`
	Severity string         `json:"severity"`
}

// MarshalJSON implements json.Marshaler.
func (i OpenIssue) MarshalJSON() ([]byte, error) {
	return json.Marshal(openIssueDocument{
		IssueID:  analysisJSONID(i.IssueID),
		Title:    i.Title,
		Severity: i.Severity,
	})
}

// UnmarshalJSON implements json.Unmarshaler.
func (i *OpenIssue) UnmarshalJSON(data []byte) error {
	var document openIssueDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	*i = OpenIssue{IssueID: int64(document.IssueID), Title: document.Title, Severity: document.Severity}
	return nil
}

// TriageInput is the issue-triage input document of spec §5.6: the failure
// candidates plus open issue summaries.
type TriageInput struct {
	VersionID      int64
	BatchID        int64
	Candidates     []FailureCandidate
	ExistingIssues []OpenIssue
}

// triageInputDocument is the persisted form of TriageInput.
type triageInputDocument struct {
	VersionID      analysisJSONID     `json:"versionId"`
	BatchID        analysisJSONID     `json:"batchId"`
	Candidates     []FailureCandidate `json:"candidates"`
	ExistingIssues []OpenIssue        `json:"existingIssues"`
}

// EncodeTriageInput builds the issue-triage input document.
func EncodeTriageInput(versionID, batchID int64, candidates []FailureCandidate, existing []OpenIssue) json.RawMessage {
	return analysisMarshal(triageInputDocument{
		VersionID:      analysisJSONID(versionID),
		BatchID:        analysisJSONID(batchID),
		Candidates:     candidates,
		ExistingIssues: existing,
	})
}

// DecodeTriageInput parses an issue-triage input document.
func DecodeTriageInput(raw json.RawMessage) (TriageInput, error) {
	if len(raw) == 0 {
		return TriageInput{}, nil
	}
	var document triageInputDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return TriageInput{}, fmt.Errorf("delivery: decode triage input: %w", err)
	}
	return TriageInput{
		VersionID:      int64(document.VersionID),
		BatchID:        int64(document.BatchID),
		Candidates:     document.Candidates,
		ExistingIssues: document.ExistingIssues,
	}, nil
}

// TriageResult is the issue-triage result document: the validated drafts plus
// short non-sensitive warnings. The JSON key is "issues" per spec §5.6.
type TriageResult struct {
	Drafts   []IssueDraft
	Warnings []string
}

// triageDraftDocument is the persisted JSON form of one issue draft.
type triageDraftDocument struct {
	DedupeKey      string                    `json:"dedupeKey"`
	Title          string                    `json:"title"`
	Description    string                    `json:"description,omitempty"`
	ReproSteps     string                    `json:"reproSteps,omitempty"`
	Expected       string                    `json:"expected,omitempty"`
	Actual         string                    `json:"actual,omitempty"`
	Severity       string                    `json:"severity"`
	Confidence     string                    `json:"confidence"`
	Category       string                    `json:"category"`
	FailureClass   string                    `json:"failureClass,omitempty"`
	CaseRunID      *analysisJSONID           `json:"caseRunId,omitempty"`
	CaseNodeID     *analysisJSONID           `json:"caseNodeId,omitempty"`
	EvidenceIDs    []analysisJSONID          `json:"evidenceIds,omitempty"`
	CodeRefs       []analysisCodeRefDocument `json:"codeRefs,omitempty"`
	RequirementIDs []analysisJSONID          `json:"requirementIds,omitempty"`
	MergeIntoIssue *analysisJSONID           `json:"mergeIntoIssueId,omitempty"`
}

// triageResultDocument is the persisted form of TriageResult.
type triageResultDocument struct {
	Issues   []triageDraftDocument `json:"issues"`
	Warnings []string              `json:"warnings,omitempty"`
}

// EncodeTriageResult builds the issue-triage result document.
func EncodeTriageResult(drafts []IssueDraft, warnings []string) (json.RawMessage, error) {
	documents := make([]triageDraftDocument, 0, len(drafts))
	for _, draft := range drafts {
		documents = append(documents, newTriageDraftDocument(draft))
	}
	encoded, err := json.Marshal(triageResultDocument{Issues: documents, Warnings: warnings})
	if err != nil {
		return nil, fmt.Errorf("delivery: encode triage result: %w", err)
	}
	return encoded, nil
}

// newTriageDraftDocument converts an IssueDraft to its persisted form.
func newTriageDraftDocument(draft IssueDraft) triageDraftDocument {
	document := triageDraftDocument{
		DedupeKey:      draft.DedupeKey,
		Title:          draft.Title,
		Description:    draft.Description,
		ReproSteps:     draft.ReproSteps,
		Expected:       draft.Expected,
		Actual:         draft.Actual,
		Severity:       draft.Severity,
		Confidence:     draft.Confidence,
		Category:       draft.Category,
		FailureClass:   draft.FailureClass,
		EvidenceIDs:    analysisEvidenceIDs(draft.EvidenceIDs),
		CodeRefs:       analysisCodeRefDocuments(draft.CodeRefs),
		RequirementIDs: analysisEvidenceIDs(draft.RequirementIDs),
	}
	if draft.CaseRunID != nil {
		id := analysisJSONID(*draft.CaseRunID)
		document.CaseRunID = &id
	}
	if draft.CaseNodeID != nil {
		id := analysisJSONID(*draft.CaseNodeID)
		document.CaseNodeID = &id
	}
	if draft.MergeIntoIssueID != nil {
		id := analysisJSONID(*draft.MergeIntoIssueID)
		document.MergeIntoIssue = &id
	}
	return document
}

// DecodeTriageResult parses an issue-triage result document.
func DecodeTriageResult(raw json.RawMessage) (TriageResult, error) {
	if len(raw) == 0 {
		return TriageResult{}, nil
	}
	var document triageResultDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return TriageResult{}, fmt.Errorf("delivery: decode triage result: %w", err)
	}
	drafts := make([]IssueDraft, 0, len(document.Issues))
	for _, item := range document.Issues {
		draft := IssueDraft{
			DedupeKey:      item.DedupeKey,
			Title:          item.Title,
			Description:    item.Description,
			ReproSteps:     item.ReproSteps,
			Expected:       item.Expected,
			Actual:         item.Actual,
			Severity:       item.Severity,
			Confidence:     item.Confidence,
			Category:       item.Category,
			FailureClass:   item.FailureClass,
			EvidenceIDs:    analysisInt64IDs(item.EvidenceIDs),
			CodeRefs:       analysisCodeRefs(item.CodeRefs),
			RequirementIDs: analysisInt64IDs(item.RequirementIDs),
		}
		if item.CaseRunID != nil {
			value := int64(*item.CaseRunID)
			draft.CaseRunID = &value
		}
		if item.CaseNodeID != nil {
			value := int64(*item.CaseNodeID)
			draft.CaseNodeID = &value
		}
		if item.MergeIntoIssue != nil {
			value := int64(*item.MergeIntoIssue)
			draft.MergeIntoIssueID = &value
		}
		drafts = append(drafts, draft)
	}
	return TriageResult{Drafts: drafts, Warnings: document.Warnings}, nil
}

// analysisMarshal encodes value and never panics: the documents are built from
// plain structs, so an error would be a programming defect. It degrades to an
// empty object instead of failing the whole run.
func analysisMarshal(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// NewAnalysisConflict builds a 409 analysis error (analysis_active and other
// uniqueness conflicts).
func NewAnalysisConflict(reason, message string) *Error {
	return &Error{Kind: KindConflict, Reason: reason, Message: message}
}

// NewAnalysisNotApplicable builds the 409 state-conflict error of a run whose
// batch cannot be analyzed yet.
func NewAnalysisNotApplicable(message string) *Error {
	return &Error{Kind: KindStateConflict, Reason: ReasonAnalysisNotApplicable, Message: message}
}

// NewAnalysisNotFound builds the 404 analysis error.
func NewAnalysisNotFound(analysisID int64) *Error {
	return &Error{
		Kind:    KindNotFound,
		Reason:  ReasonAnalysisNotFound,
		Message: fmt.Sprintf("analysis run %d not found", analysisID),
	}
}

// NewAnalysisNotFoundForBatch builds the 404 analysis error of a missing
// (batch, kind) lookup used by the execution autonomy adapter.
func NewAnalysisNotFoundForBatch(batchID int64, kind string) *Error {
	return &Error{
		Kind:    KindNotFound,
		Reason:  ReasonAnalysisNotFound,
		Message: fmt.Sprintf("batch %d has no %s analysis", batchID, kind),
	}
}

// AnalysisReason returns the stable details.reason carried by err, or "" when
// err is not a delivery domain error.
func AnalysisReason(err error) string {
	if err == nil {
		return ""
	}
	var domainErr *Error
	if errors.As(err, &domainErr) {
		return domainErr.Reason
	}
	return ""
}
