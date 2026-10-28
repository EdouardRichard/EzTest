package httpapi

import (
	"encoding/json"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// batchCountsDTO is the per-status tally of one batch (spec §6.3).
type batchCountsDTO struct {
	Total        int `json:"total"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Blocked      int `json:"blocked"`
	Inconclusive int `json:"inconclusive"`
	Skipped      int `json:"skipped"`
	Interrupted  int `json:"interrupted"`
}

// batchDTO is the BatchDTO of spec §6.3. Every execution-owned id is a decimal
// string; user ids stay numbers elsewhere but created_by is an execution-scoped
// string for consistency with the P6 DTO.
type batchDTO struct {
	ID                 int64          `json:"id,string"`
	VersionID          int64          `json:"version_id,string"`
	BatchNo            int            `json:"batch_no"`
	Status             string         `json:"status"`
	Paused             bool           `json:"paused"`
	PauseMode          string         `json:"pause_mode"`
	PauseReason        string         `json:"pause_reason"`
	BaselineHash       string         `json:"baseline_hash"`
	CodeBaselineCommit *string        `json:"code_baseline_commit"`
	HeadCommit         *string        `json:"head_commit"`
	Counts             batchCountsDTO `json:"counts"`
	CreatedBy          int64          `json:"created_by,string"`
	StartedAt          *time.Time     `json:"started_at"`
	FinishedAt         *time.Time     `json:"finished_at"`
	FailureReason      string         `json:"failure_reason"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// newBatchDTO maps one batch aggregate to the API shape.
func newBatchDTO(batch *domain.RunBatch) batchDTO {
	if batch == nil {
		return batchDTO{}
	}
	return batchDTO{
		ID:                 batch.ID,
		VersionID:          batch.VersionID,
		BatchNo:            batch.BatchNo,
		Status:             string(batch.Status),
		Paused:             batch.Paused,
		PauseMode:          string(batch.PauseMode),
		PauseReason:        batch.PauseReason,
		BaselineHash:       batch.BaselineHash,
		CodeBaselineCommit: copyString(batch.CodeBaselineCommit),
		HeadCommit:         copyString(batch.HeadCommit),
		Counts: batchCountsDTO{
			Total:        batch.CaseCount,
			Passed:       batch.PassedCount,
			Failed:       batch.FailedCount,
			Blocked:      batch.BlockedCount,
			Inconclusive: batch.InconclusiveCount,
			Skipped:      batch.SkippedCount,
			Interrupted:  batch.InterruptedCount,
		},
		CreatedBy:     batch.CreatedBy,
		StartedAt:     batch.StartedAt,
		FinishedAt:    batch.FinishedAt,
		FailureReason: batch.FailureReason,
		CreatedAt:     batch.CreatedAt,
		UpdatedAt:     batch.UpdatedAt,
	}
}

// caseRunSessionDTO is the credential-free session projection of a case run.
type caseRunSessionDTO struct {
	ID      int64  `json:"id,string"`
	Account string `json:"account"`
	State   string `json:"state"`
}

// caseCheckpointDTO is the resume checkpoint of a case run (spec §5.4).
type caseCheckpointDTO struct {
	CompletedSeqs      []int `json:"completed_seqs"`
	CleanupRequired    bool  `json:"cleanup_required"`
	InterruptedStepSeq *int  `json:"interrupted_step_seq"`
}

// stepDTO is one recorded step of a case run (spec §6.3).
type stepDTO struct {
	ID          int64      `json:"id,string"`
	Seq         int        `json:"seq"`
	Action      string     `json:"action"`
	Expected    string     `json:"expected"`
	Actual      string     `json:"actual"`
	Status      string     `json:"status"`
	EvidenceIDs []string   `json:"evidence_ids"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// assertionDTO is one recorded assertion outcome (spec §6.3).
type assertionDTO struct {
	ID            int64    `json:"id,string"`
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	Expected      string   `json:"expected"`
	Actual        string   `json:"actual"`
	Verdict       string   `json:"verdict"`
	EvidenceIDs   []string `json:"evidence_ids"`
	StepSeq       int      `json:"step_seq,omitempty"`
	FailureReason string   `json:"failure_reason,omitempty"`
}

// evidenceDTO is the artifact summary embedded in a case run / report.
type evidenceDTO struct {
	ID        int64  `json:"id,string"`
	Kind      string `json:"kind"`
	Mime      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// newEvidenceDTO maps one evidence row to its summary.
func newEvidenceDTO(evidence *domain.Evidence) evidenceDTO {
	return evidenceDTO{
		ID:        evidence.ID,
		Kind:      evidence.Kind,
		Mime:      evidence.Mime,
		SizeBytes: evidence.SizeBytes,
		SHA256:    evidence.SHA256,
	}
}

// evidenceMetaDTO is the metadata read of GET .../evidence/{evidenceId}/meta.
// The filesystem path is deliberately absent (spec §5.7).
type evidenceMetaDTO struct {
	ID           int64          `json:"id,string"`
	Kind         string         `json:"kind"`
	Mime         string         `json:"mime"`
	SizeBytes    int64          `json:"size_bytes"`
	SHA256       string         `json:"sha256"`
	BatchID      int64          `json:"batch_id,string"`
	CaseRunID    *int64         `json:"case_run_id,string"`
	StepResultID *int64         `json:"step_result_id,string"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    time.Time      `json:"created_at"`
}

// pendingTakeoverDTO is the waiting human takeover attached to a case run.
type pendingTakeoverDTO struct {
	ID           int64           `json:"id,string"`
	Reason       string          `json:"reason"`
	Instructions json.RawMessage `json:"instructions"`
	ExpiresAt    string          `json:"expires_at"`
}

// caseRunDTO is the CaseRun detail of spec §6.3. List responses carry empty
// detail arrays; the detail endpoint fills them in.
type caseRunDTO struct {
	ID               int64               `json:"id,string"`
	BatchID          int64               `json:"batch_id,string"`
	CaseNodeID       int64               `json:"case_node_id,string"`
	CaseRevision     int                 `json:"case_revision"`
	Attempt          int                 `json:"attempt"`
	Status           string              `json:"status"`
	FailureClass     *string             `json:"failure_class"`
	FailureReason    string              `json:"failure_reason"`
	Summary          string              `json:"summary"`
	DurationMS       int64               `json:"duration_ms"`
	Title            string              `json:"title"`
	Session          *caseRunSessionDTO  `json:"session"`
	Checkpoint       caseCheckpointDTO   `json:"checkpoint"`
	Steps            []stepDTO           `json:"steps"`
	Assertions       []assertionDTO      `json:"assertions"`
	Evidence         []evidenceDTO       `json:"evidence"`
	PendingTakeover  *pendingTakeoverDTO `json:"pending_takeover"`
	AgentRunID       *int64              `json:"agent_run_id,string"`
	AgentSessionID   *int64              `json:"agent_session_id,string"`
	RetryOfCaseRunID *int64              `json:"retry_of_case_run_id,string"`
	StartedAt        *time.Time          `json:"started_at"`
	FinishedAt       *time.Time          `json:"finished_at"`
}

// caseRunDetail is the loaded detail set of one case run. A nil detail yields
// the list projection (empty arrays and a run-derived checkpoint).
type caseRunDetail struct {
	Steps             []domain.StepResult
	Assertions        []domain.AssertionResult
	Evidence          []domain.Evidence
	StepEvidence      map[int64][]int64
	AssertionEvidence map[int64][]int64
	Session           *CaseRunSession
	Takeover          *executionapp.TakeoverRef
}

// newCaseRunDTO maps one case run aggregate to the API shape.
func newCaseRunDTO(run *domain.CaseRun, detail *caseRunDetail) caseRunDTO {
	if run == nil {
		return caseRunDTO{}
	}
	dto := caseRunDTO{
		ID:               run.ID,
		BatchID:          run.BatchID,
		CaseNodeID:       run.CaseNodeID,
		CaseRevision:     run.CaseRevision,
		Attempt:          run.Attempt,
		Status:           string(run.Status),
		FailureReason:    run.FailureReason,
		Summary:          run.Summary,
		DurationMS:       run.DurationMS,
		Title:            run.Snapshot.Node.Title,
		Checkpoint:       caseCheckpointDTO{CompletedSeqs: []int{}, CleanupRequired: run.CleanupRequired, InterruptedStepSeq: run.InterruptedStepSeq},
		Steps:            []stepDTO{},
		Assertions:       []assertionDTO{},
		Evidence:         []evidenceDTO{},
		AgentRunID:       run.AgentRunID,
		AgentSessionID:   run.AgentSessionID,
		RetryOfCaseRunID: run.RetryOfCaseRunID,
		StartedAt:        run.StartedAt,
		FinishedAt:       run.FinishedAt,
	}
	if run.FailureClass != nil {
		class := string(*run.FailureClass)
		dto.FailureClass = &class
	}
	if detail == nil {
		return dto
	}

	completed, _ := domain.Checkpoint(detail.Steps)
	dto.Checkpoint.CompletedSeqs = completed
	dto.Steps = newStepDTOs(detail.Steps, detail.Evidence, detail.StepEvidence)
	dto.Assertions = newAssertionDTOs(run, detail.Assertions, detail.AssertionEvidence)
	dto.Evidence = newEvidenceDTOs(detail.Evidence)
	if detail.Session != nil {
		dto.Session = &caseRunSessionDTO{ID: detail.Session.ID, Account: detail.Session.Account, State: detail.Session.State}
	}
	if detail.Takeover != nil {
		expiresAt := ""
		if detail.Takeover.ExpiresAt != nil {
			expiresAt = detail.Takeover.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		dto.PendingTakeover = &pendingTakeoverDTO{
			ID:           detail.Takeover.ID,
			Reason:       detail.Takeover.Reason,
			Instructions: ensureJSONObject(detail.Takeover.Instructions),
			ExpiresAt:    expiresAt,
		}
	}
	return dto
}

// newStepDTOs maps the recorded steps and attaches the evidence owned by each.
// Evidence is linked either through the capture-time step_result_id or through
// the evidence_ref row the recorder writes when the step is submitted.
func newStepDTOs(steps []domain.StepResult, evidence []domain.Evidence, refs map[int64][]int64) []stepDTO {
	byStep := evidenceByStep(evidence)
	out := make([]stepDTO, 0, len(steps))
	for _, step := range steps {
		evidenceIDs := mergeIDs(byStep[step.ID], refs[step.ID])
		out = append(out, stepDTO{
			ID:          step.ID,
			Seq:         step.Seq,
			Action:      step.Action,
			Expected:    step.Expected,
			Actual:      step.Actual,
			Status:      string(step.Status),
			EvidenceIDs: formatOptionalIDs(evidenceIDs),
			StartedAt:   step.StartedAt,
			FinishedAt:  step.FinishedAt,
		})
	}
	return out
}

// evidenceByStep groups the evidence ids by their capture-time step_result id.
func evidenceByStep(evidence []domain.Evidence) map[int64][]int64 {
	byStep := make(map[int64][]int64)
	for _, artifact := range evidence {
		if artifact.StepResultID == nil {
			continue
		}
		byStep[*artifact.StepResultID] = append(byStep[*artifact.StepResultID], artifact.ID)
	}
	return byStep
}

// mergeIDs merges two id slices preserving order and dropping duplicates.
func mergeIDs(primary, extra []int64) []int64 {
	if len(extra) == 0 {
		return primary
	}
	seen := make(map[int64]struct{}, len(primary)+len(extra))
	merged := make([]int64, 0, len(primary)+len(extra))
	for _, list := range [][]int64{primary, extra} {
		for _, id := range list {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
	}
	return merged
}

// newAssertionDTOs maps the recorded assertions and attaches their evidence ids.
func newAssertionDTOs(run *domain.CaseRun, assertions []domain.AssertionResult, evidenceByAssertion map[int64][]int64) []assertionDTO {
	stepSeq := assertionStepSeqs(run)
	out := make([]assertionDTO, 0, len(assertions))
	for _, assertion := range assertions {
		out = append(out, assertionDTO{
			ID:            assertion.ID,
			Kind:          assertion.Kind,
			Target:        assertion.Target,
			Expected:      assertion.Expected,
			Actual:        assertion.Actual,
			Verdict:       assertion.Verdict,
			EvidenceIDs:   formatOptionalIDs(evidenceByAssertion[assertion.ID]),
			StepSeq:       stepSeq[assertion.AssertionID],
			FailureReason: assertion.FailureReason,
		})
	}
	return out
}

// assertionStepSeqs maps a snapshot assertion id to its step sequence.
func assertionStepSeqs(run *domain.CaseRun) map[int64]int {
	seqs := make(map[int64]int, len(run.Snapshot.Assertions))
	for _, assertion := range run.Snapshot.Assertions {
		seqs[assertion.ID] = assertion.StepSeq
	}
	return seqs
}

// newEvidenceDTOs maps an evidence slice to a non-nil DTO slice.
func newEvidenceDTOs(evidence []domain.Evidence) []evidenceDTO {
	out := make([]evidenceDTO, 0, len(evidence))
	for i := range evidence {
		out = append(out, newEvidenceDTO(&evidence[i]))
	}
	return out
}

// reportBatchDTO is the batch identity embedded in a report.
type reportBatchDTO struct {
	ID                 int64   `json:"id,string"`
	BatchNo            int     `json:"batch_no"`
	Status             string  `json:"status"`
	BaselineHash       string  `json:"baseline_hash"`
	CodeBaselineCommit *string `json:"code_baseline_commit"`
}

// reportSummaryDTO is the aggregated report tally.
type reportSummaryDTO struct {
	CaseCount      int            `json:"case_count"`
	ByStatus       map[string]int `json:"by_status"`
	ByFailureClass map[string]int `json:"by_failure_class"`
	FlakyCaseIDs   []string       `json:"flaky_case_ids"`
	DurationMS     int64          `json:"duration_ms"`
	EvidenceCount  int            `json:"evidence_count"`
}

// reportCaseDTO is one case row of the report.
type reportCaseDTO struct {
	CaseRunID    int64   `json:"case_run_id,string"`
	CaseNodeID   int64   `json:"case_node_id,string"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	FailureClass *string `json:"failure_class"`
	Steps        int     `json:"steps"`
	Assertions   int     `json:"assertions"`
	Evidence     int     `json:"evidence"`
	DurationMS   int64   `json:"duration_ms"`
}

// reportDTO is the Batch 1 report of spec §6.3.
type reportDTO struct {
	Batch       reportBatchDTO    `json:"batch"`
	Summary     reportSummaryDTO  `json:"summary"`
	Cases       []reportCaseDTO   `json:"cases"`
	Environment map[string]string `json:"environment"`
	GeneratedAt time.Time         `json:"generated_at"`
}

// newReportDTO maps the derived report view to the API shape.
func newReportDTO(view *executionapp.ReportView) reportDTO {
	if view == nil {
		return reportDTO{}
	}
	batch := reportBatchDTO{
		ID:           view.Batch.ID,
		BatchNo:      view.Batch.BatchNo,
		Status:       string(view.Batch.Status),
		BaselineHash: view.Batch.BaselineHash,
	}
	if view.Batch.CodeBaselineCommit != "" {
		commit := view.Batch.CodeBaselineCommit
		batch.CodeBaselineCommit = &commit
	}
	flaky := make([]string, 0, len(view.Summary.FlakyCaseIDs))
	for _, id := range view.Summary.FlakyCaseIDs {
		flaky = append(flaky, formatID(id))
	}
	summary := reportSummaryDTO{
		CaseCount:      view.Summary.CaseCount,
		ByStatus:       nonNilIntMap(view.Summary.ByStatus),
		ByFailureClass: nonNilIntMap(view.Summary.ByFailureClass),
		FlakyCaseIDs:   flaky,
		DurationMS:     view.Summary.DurationMS,
		EvidenceCount:  view.Summary.EvidenceCount,
	}
	cases := make([]reportCaseDTO, 0, len(view.Cases))
	for _, item := range view.Cases {
		dto := reportCaseDTO{
			CaseRunID:  item.CaseRunID,
			CaseNodeID: item.CaseNodeID,
			Title:      item.Title,
			Status:     string(item.Status),
			Steps:      item.Steps,
			Assertions: item.Assertions,
			Evidence:   item.Evidence,
			DurationMS: item.DurationMS,
		}
		if item.FailureClass != "" {
			class := item.FailureClass
			dto.FailureClass = &class
		}
		cases = append(cases, dto)
	}
	environment := map[string]string{"system": "", "environment": "", "base_url": ""}
	for key, value := range view.Environment {
		environment[key] = value
	}
	return reportDTO{
		Batch:       batch,
		Summary:     summary,
		Cases:       cases,
		Environment: environment,
		GeneratedAt: view.GeneratedAt,
	}
}

// executionSessionDTO is one execution agent session of the version channel.
type executionSessionDTO struct {
	ID         int64     `json:"id,string"`
	Channel    string    `json:"channel"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Running    bool      `json:"running"`
	ProfileKey string    `json:"profile_key"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// newExecutionSessionDTO maps one agent session projection. The execution-agent
// profile key is fixed by spec §7.1.
func newExecutionSessionDTO(session *executionapp.AgentSessionRef, versionID int64) executionSessionDTO {
	status := session.Status
	if status == "" {
		status = "idle"
	}
	return executionSessionDTO{
		ID:         session.SessionID,
		Channel:    "version:" + formatID(versionID),
		Title:      session.Title,
		Status:     status,
		Running:    status == "running",
		ProfileKey: "execution-agent",
		CreatedAt:  session.CreatedAt,
		UpdatedAt:  session.UpdatedAt,
	}
}

// nonNilIntMap returns a non-nil map so the JSON shape is stable.
func nonNilIntMap(value map[string]int) map[string]int {
	if value == nil {
		return map[string]int{}
	}
	return value
}

// copyString returns an owned copy of an optional string.
func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
