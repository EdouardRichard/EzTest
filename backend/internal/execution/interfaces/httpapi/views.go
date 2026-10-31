package httpapi

import (
	"encoding/json"
	"strconv"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ExecutionOverviewView is the GET /execution/overview projection (spec §9.3).
type ExecutionOverviewView struct {
	VersionID                  string               `json:"version_id"`
	ActiveWave                 *RunWaveView         `json:"active_wave"`
	Counts                     execution.WaveCounts `json:"counts"`
	CaseRunCounts              CaseRunCountsView    `json:"case_run_counts"`
	OpenHumanRequests          int                  `json:"open_human_requests"`
	Executor                   ExecutorStatusView   `json:"executor"`
	Quotas                     ExecutionQuotas      `json:"quotas"`
	HumanRequestTimeoutSeconds int64                `json:"human_request_timeout_seconds"`
}

// CaseRunCountsView is the version-wide case_run projection of the overview.
type CaseRunCountsView struct {
	Total        int `json:"total"`
	Pending      int `json:"pending"`
	Running      int `json:"running"`
	Paused       int `json:"paused"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Blocked      int `json:"blocked"`
	Inconclusive int `json:"inconclusive"`
	Skipped      int `json:"skipped"`
	Interrupted  int `json:"interrupted"`
	Superseded   int `json:"superseded"`
}

// ExecutionQuotas is the frozen 3/2/1 quota projection.
type ExecutionQuotas struct {
	Global      int64 `json:"global"`
	PerSUT      int64 `json:"per_sut"`
	CrossSystem int64 `json:"cross_system"`
}

// ExecutorStatusView is the overview executor block (registered row or offline).
type ExecutorStatusView struct {
	ID             *string `json:"id"`
	Status         string  `json:"status"`
	Endpoint       string  `json:"endpoint"`
	Capacity       int     `json:"capacity"`
	ActiveContexts int     `json:"active_contexts"`
	LastHeartbeat  *string `json:"last_heartbeat"`
	Version        string  `json:"version"`
}

// ExecutorView is the admin-only executor list projection (no token_hash).
type ExecutorView struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Endpoint       string         `json:"endpoint"`
	Status         string         `json:"status"`
	Capacity       int            `json:"capacity"`
	ActiveContexts int            `json:"active_contexts"`
	Labels         map[string]any `json:"labels"`
	LastHeartbeat  *string        `json:"last_heartbeat"`
	Version        string         `json:"version"`
	LastError      string         `json:"last_error"`
}

// RunWaveView is the run_wave projection (spec §9.3).
type RunWaveView struct {
	ID          string               `json:"id"`
	VersionID   string               `json:"version_id"`
	Status      string               `json:"status"`
	Trigger     string               `json:"trigger"`
	Counts      execution.WaveCounts `json:"counts"`
	StartedAt   *string              `json:"started_at"`
	FinishedAt  *string              `json:"finished_at"`
	PausedAt    *string              `json:"paused_at"`
	PauseMode   string               `json:"pause_mode"`
	PauseReason string               `json:"pause_reason"`
	CreatedAt   string               `json:"created_at"`
}

// IntentView is the execution_intent projection (spec §9.3).
type IntentView struct {
	ID            string   `json:"id"`
	VersionID     string   `json:"version_id"`
	CaseVersionID string   `json:"case_version_id"`
	CaseID        *string  `json:"case_id"`
	CaseTitle     string   `json:"case_title"`
	VersionSUTIDs []string `json:"version_sut_ids"`
	Purpose       string   `json:"purpose"`
	Status        string   `json:"status"`
	AttemptNo     int      `json:"attempt_no"`
	CreatedAt     string   `json:"created_at"`
}

// CaseRunSummaryView is the stable subset of case_run.summary.
type CaseRunSummaryView struct {
	VerdictReason   string  `json:"verdict_reason"`
	AssertionTotal  int     `json:"assertion_total"`
	AssertionFailed int     `json:"assertion_failed"`
	StepTotal       int     `json:"step_total"`
	StepFailed      int     `json:"step_failed"`
	EvidenceCount   int     `json:"evidence_count"`
	VisualSkipped   int     `json:"visual_skipped"`
	HumanRequestID  *string `json:"human_request_id"`
}

// CaseRunView is the case_run projection (spec §9.3).
type CaseRunView struct {
	ID            string             `json:"id"`
	WaveID        string             `json:"wave_id"`
	IntentID      string             `json:"intent_id"`
	CaseID        string             `json:"case_id"`
	CaseVersionID string             `json:"case_version_id"`
	Title         string             `json:"title"`
	Status        string             `json:"status"`
	FailureClass  string             `json:"failure_class"`
	Attempts      int                `json:"attempts"`
	Summary       CaseRunSummaryView `json:"summary"`
	StartedAt     *string            `json:"started_at"`
	FinishedAt    *string            `json:"finished_at"`
	VersionSUTIDs []string           `json:"version_sut_ids"`
	EvidenceCount int                `json:"evidence_count"`
}

// CaseRunStepView is one step_result row of a case-run detail.
type CaseRunStepView struct {
	Seq                int            `json:"seq"`
	TargetVersionSUTID *string        `json:"target_version_sut_id"`
	Status             string         `json:"status"`
	Observed           map[string]any `json:"observed"`
	Error              string         `json:"error"`
	StartedAt          *string        `json:"started_at"`
	FinishedAt         *string        `json:"finished_at"`
}

// CaseRunAssertionView is one assertion_result row of a case-run detail.
type CaseRunAssertionView struct {
	StepSeq            int     `json:"step_seq"`
	AssertionSeq       int     `json:"assertion_seq"`
	Kind               string  `json:"kind"`
	Verdict            string  `json:"verdict"`
	Expected           string  `json:"expected"`
	Actual             string  `json:"actual"`
	Reason             string  `json:"reason"`
	TargetVersionSUTID *string `json:"target_version_sut_id"`
}

// CaseRunDetailView is the case-run detail envelope (steps + assertions + first
// 50 evidence rows).
type CaseRunDetailView struct {
	CaseRun    CaseRunView            `json:"case_run"`
	Steps      []CaseRunStepView      `json:"steps"`
	Assertions []CaseRunAssertionView `json:"assertions"`
	Evidence   []EvidenceView         `json:"evidence"`
}

// EvidenceView is immutable evidence metadata; content_ref_id and paths are
// never exposed (P3-D-20).
type EvidenceView struct {
	ID           string  `json:"id"`
	CaseRunID    string  `json:"case_run_id"`
	VersionSUTID *string `json:"version_sut_id"`
	StepSeq      *int    `json:"step_seq"`
	AssertionSeq *int    `json:"assertion_seq"`
	Kind         string  `json:"kind"`
	SHA256       string  `json:"sha256"`
	SizeBytes    int64   `json:"size_bytes"`
	MIME         string  `json:"mime"`
	Filename     string  `json:"filename"`
	CreatedAt    string  `json:"created_at"`
}

// HumanRequestPayloadView is the frozen human_request payload projection.
type HumanRequestPayloadView struct {
	Reason        string  `json:"reason"`
	VersionSUTID  *string `json:"version_sut_id"`
	AccountRole   string  `json:"account_role"`
	PageURL       string  `json:"page_url"`
	ChallengeKind string  `json:"challenge_kind"`
	EvidenceID    *string `json:"evidence_id"`
}

// HumanRequestResponseView never echoes the one-time code (spec §9.3).
type HumanRequestResponseView struct {
	Note string `json:"note"`
}

// HumanRequestView is the human_request projection (spec §9.3).
type HumanRequestView struct {
	ID         string                    `json:"id"`
	VersionID  string                    `json:"version_id"`
	CaseRunID  *string                   `json:"case_run_id"`
	Kind       string                    `json:"kind"`
	Status     string                    `json:"status"`
	Payload    HumanRequestPayloadView   `json:"payload"`
	ExpiresAt  string                    `json:"expires_at"`
	Response   *HumanRequestResponseView `json:"response"`
	ResolvedBy *string                   `json:"resolved_by"`
	ResolvedAt *string                   `json:"resolved_at"`
	CreatedAt  string                    `json:"created_at"`
}

func toRunWaveView(wave execution.RunWave) RunWaveView {
	trigger := wave.Trigger
	if trigger == "" {
		trigger = execution.WaveTriggerAuto
	}
	return RunWaveView{
		ID:          platformid.Format(wave.ID),
		VersionID:   platformid.Format(wave.VersionID),
		Status:      string(wave.Status),
		Trigger:     trigger,
		Counts:      wave.Counts.Normalize(),
		StartedAt:   formatOptionalTime(wave.StartedAt),
		FinishedAt:  formatOptionalTime(wave.FinishedAt),
		PausedAt:    formatOptionalTime(wave.PausedAt),
		PauseMode:   wave.PauseMode,
		PauseReason: wave.PauseReason,
		CreatedAt:   formatTime(wave.CreatedAt),
	}
}

func toIntentView(intent execution.ExecutionIntent, caseID, title string) IntentView {
	view := IntentView{
		ID:            platformid.Format(intent.ID),
		VersionID:     platformid.Format(intent.VersionID),
		CaseVersionID: platformid.Format(intent.CaseVersionID),
		CaseTitle:     title,
		VersionSUTIDs: idsToStrings(intent.VersionSUTIDs),
		Purpose:       string(intent.Purpose),
		Status:        string(intent.Status),
		AttemptNo:     intent.AttemptNo,
		CreatedAt:     formatTime(intent.CreatedAt),
	}
	if caseID != "" {
		view.CaseID = &caseID
	}
	return view
}

func toCaseRunView(run execution.CaseRun, meta caseMeta, versionSUTIDs []int64, evidenceCount int) CaseRunView {
	return CaseRunView{
		ID:            platformid.Format(run.ID),
		WaveID:        platformid.Format(run.WaveID),
		IntentID:      platformid.Format(run.IntentID),
		CaseID:        meta.CaseID,
		CaseVersionID: platformid.Format(run.CaseVersionID),
		Title:         meta.Title,
		Status:        string(run.Status),
		FailureClass:  string(run.FailureClass),
		Attempts:      run.Attempts,
		Summary: CaseRunSummaryView{
			VerdictReason:   run.Summary.VerdictReason,
			AssertionTotal:  run.Summary.AssertionTotal,
			AssertionFailed: run.Summary.AssertionFailed,
			StepTotal:       run.Summary.StepTotal,
			StepFailed:      run.Summary.StepFailed,
			EvidenceCount:   run.Summary.EvidenceCount,
			VisualSkipped:   run.Summary.VisualSkipped,
			HumanRequestID:  optionalIDString(run.Summary.HumanRequestID),
		},
		StartedAt:     formatOptionalTime(run.StartedAt),
		FinishedAt:    formatOptionalTime(run.FinishedAt),
		VersionSUTIDs: idsToStrings(versionSUTIDs),
		EvidenceCount: evidenceCount,
	}
}

func toCaseRunStepView(step execution.StepResult) CaseRunStepView {
	observed := step.Observed
	if observed == nil {
		observed = map[string]any{}
	}
	return CaseRunStepView{
		Seq:                step.Seq,
		TargetVersionSUTID: optionalIDStringPtr(step.TargetVersionSUTID),
		Status:             string(step.Status),
		Observed:           observed,
		Error:              step.Error,
		StartedAt:          formatOptionalTime(step.StartedAt),
		FinishedAt:         formatOptionalTime(step.FinishedAt),
	}
}

func toCaseRunAssertionView(assertion execution.AssertionResult) CaseRunAssertionView {
	return CaseRunAssertionView{
		StepSeq:            assertion.StepSeq,
		AssertionSeq:       assertion.AssertionSeq,
		Kind:               assertion.Kind,
		Verdict:            string(assertion.Verdict),
		Expected:           assertion.Expected,
		Actual:             assertion.Actual,
		Reason:             assertion.Reason,
		TargetVersionSUTID: optionalIDStringPtr(assertion.TargetVersionSUTID),
	}
}

func toEvidenceView(evidence execution.Evidence) EvidenceView {
	return EvidenceView{
		ID:           platformid.Format(evidence.ID),
		CaseRunID:    platformid.Format(evidence.CaseRunID),
		VersionSUTID: optionalIDStringPtr(evidence.VersionSUTID),
		StepSeq:      evidence.StepSeq,
		AssertionSeq: evidence.AssertionSeq,
		Kind:         string(evidence.Kind),
		SHA256:       evidence.SHA256,
		SizeBytes:    evidence.SizeBytes,
		MIME:         evidence.MIME,
		Filename:     evidence.Filename,
		CreatedAt:    formatTime(evidence.CreatedAt),
	}
}

func toHumanRequestView(request execution.HumanRequest) HumanRequestView {
	payload := request.Payload
	view := HumanRequestView{
		ID:        platformid.Format(request.ID),
		VersionID: platformid.Format(request.VersionID),
		CaseRunID: optionalIDStringPtr(request.CaseRunID),
		Kind:      string(request.Kind),
		Status:    string(request.Status),
		Payload: HumanRequestPayloadView{
			Reason:        payloadString(payload, execution.HumanPayloadReason),
			VersionSUTID:  payloadIDString(payload, execution.HumanPayloadVersionSUTID),
			AccountRole:   payloadString(payload, execution.HumanPayloadAccountRole),
			PageURL:       payloadString(payload, execution.HumanPayloadPageURL),
			ChallengeKind: payloadString(payload, execution.HumanPayloadChallengeKind),
			EvidenceID:    payloadIDString(payload, execution.HumanPayloadEvidenceID),
		},
		ExpiresAt:  formatTime(request.ExpiresAt),
		ResolvedBy: optionalIDStringPtr(request.ResolvedBy),
		ResolvedAt: formatOptionalTime(request.ResolvedAt),
		CreatedAt:  formatTime(request.CreatedAt),
	}
	if note := payloadString(request.Response, execution.HumanResponseNote); note != "" {
		view.Response = &HumanRequestResponseView{Note: note}
	}
	return view
}

func toExecutorView(instance execution.ExecutorInstance) ExecutorView {
	labels := instance.Labels
	if labels == nil {
		labels = map[string]any{}
	}
	return ExecutorView{
		ID:             platformid.Format(instance.ID),
		Name:           instance.Name,
		Endpoint:       instance.Endpoint,
		Status:         string(instance.Status),
		Capacity:       instance.Capacity,
		ActiveContexts: instance.ActiveContexts,
		Labels:         labels,
		LastHeartbeat:  formatOptionalTime(instance.LastHeartbeat),
		Version:        instance.Version,
		LastError:      instance.LastError,
	}
}

func toExecutorStatusView(instance *execution.ExecutorInstance, endpoint string) ExecutorStatusView {
	if instance == nil {
		return ExecutorStatusView{
			Status:   string(execution.ExecutorOffline),
			Endpoint: endpoint,
		}
	}
	return ExecutorStatusView{
		ID:             optionalIDString(instance.ID),
		Status:         string(instance.Status),
		Endpoint:       instance.Endpoint,
		Capacity:       instance.Capacity,
		ActiveContexts: instance.ActiveContexts,
		LastHeartbeat:  formatOptionalTime(instance.LastHeartbeat),
		Version:        instance.Version,
	}
}

func toCaseRunCounts(counts execution.WaveCounts) CaseRunCountsView {
	return CaseRunCountsView{
		Total:        counts.Total,
		Pending:      counts.Pending,
		Running:      counts.Running,
		Paused:       counts.Paused,
		Passed:       counts.Passed,
		Failed:       counts.Failed,
		Blocked:      counts.Blocked,
		Inconclusive: counts.Inconclusive,
		Skipped:      counts.Skipped,
		Interrupted:  counts.Interrupted,
		Superseded:   counts.Superseded,
	}
}

func emptyWaveCounts() execution.WaveCounts {
	return execution.WaveCounts{ByVersionSUT: map[string]execution.SUTCounts{}}
}

func addWaveCounts(total *execution.WaveCounts, item execution.WaveCounts) {
	total.Total += item.Total
	total.Pending += item.Pending
	total.Running += item.Running
	total.Paused += item.Paused
	total.Passed += item.Passed
	total.Failed += item.Failed
	total.Blocked += item.Blocked
	total.Inconclusive += item.Inconclusive
	total.Skipped += item.Skipped
	total.Interrupted += item.Interrupted
	total.Superseded += item.Superseded
	total.Evidence += item.Evidence
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func optionalIDString(value int64) *string {
	if value <= 0 {
		return nil
	}
	formatted := platformid.Format(value)
	return &formatted
}

func optionalIDStringPtr(value *int64) *string {
	if value == nil || *value <= 0 {
		return nil
	}
	formatted := platformid.Format(*value)
	return &formatted
}

func idsToStrings(ids []int64) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			result = append(result, platformid.Format(id))
		}
	}
	return result
}

// payloadString reads a string payload field; JSON-decoded numbers are also
// accepted so a legacy row cannot break the view.
func payloadString(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

// payloadIDString reads a decimal-string ID payload field.
func payloadIDString(payload map[string]any, key string) *string {
	text := payloadString(payload, key)
	if text == "" {
		return nil
	}
	return &text
}
