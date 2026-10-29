package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Visual assertion evidence kinds and assertion verdicts (design spec §9.3). The
// evidence kinds are part of the frozen DDL allow-list (execution_0005).
const (
	visualActualEvidenceKind = "visual_actual"
	visualDiffEvidenceKind   = "visual_diff"
	visualEvidenceMIME       = "image/png"

	// visualAssertNotRun is the assertion-level verdict of an unexecuted or
	// environment-mismatched visual assertion.
	visualAssertNotRun = "not_run"
	// visualAssertPassed and visualAssertFailed are the executed verdicts.
	visualAssertPassed = "passed"
	visualAssertFailed = "failed"
)

// Visual baseline vocabulary of design spec §9.3.
const (
	visualBaselineKindScreenshot = "screenshot"
	visualBaselineKindDedicated  = "visual_baseline"

	// visualReasonMatch is the passing audit reason of ClassifyVisual.
	visualReasonMatch = "visual_match"
	// visualReasonMissingBaseline is the §9.3 case reason of an unapproved
	// visual baseline.
	visualReasonMissingBaseline = "missing_visual_baseline"
	// visualReasonEnvironmentMismatch is the §9.3 environment reason.
	visualReasonEnvironmentMismatch = "visual_environment_mismatch"
	// visualReasonUnsupported is the case reason of a browser adapter without
	// the visual comparison capability.
	visualReasonUnsupported = "unsupported_assertion_kind"
	// visualReasonInvalidConfig marks a snapshot visual configuration that cannot
	// be compared (corrupt or out-of-range values).
	visualReasonInvalidConfig = "invalid_visual_configuration"
	// visualReasonBaselineInvalid marks an approved baseline artifact that is
	// not a usable screenshot/visual_baseline PNG.
	visualReasonBaselineInvalid = "visual_baseline_invalid"
	// visualReasonReadFailed marks an unreadable baseline artifact.
	visualReasonReadFailed = "visual_baseline_read_failed"
	// visualReasonCaptureFailed marks a comparison whose evidence could not be
	// committed.
	visualReasonCaptureFailed = "visual_evidence_capture_failed"
	// visualReasonCompareFailed marks a transport/executor comparison failure.
	visualReasonCompareFailed = "visual_compare_failed"
)

// Visual comparison defaults mirroring the mindmap domain defaults. A snapshot
// written through the mindmap API always carries explicit values; these only
// keep a historical snapshot comparable instead of failing the executor's
// argument validation.
const (
	visualDefaultViewportWidth  = 1280
	visualDefaultViewportHeight = 720
	visualDefaultDeviceScale    = 1.0
)

// VisualBaseline is one approved baseline artifact read for execution. Data
// holds the already sha256/size-verified PNG bytes.
type VisualBaseline struct {
	EvidenceID int64
	Kind       string
	Mime       string
	SHA256     string
	Data       []byte
}

// VisualBaselineReader reads the approved visual baseline of one assertion. The
// implementation must prove the evidence belongs to versionID and return a
// not_found domain error otherwise; the bytes are re-verified by the reader
// (EvidenceService.Open owns the sha256/size proof).
type VisualBaselineReader interface {
	ReadVisualBaseline(ctx context.Context, versionID, evidenceID int64) (VisualBaseline, error)
}

// VisualAssertOptions are the collaborators of one execution.visual_assert call.
// Comparator and Context are only required when a baseline exists; Capture is
// required to persist the actual/diff evidence and Baselines to read the
// approved baseline.
type VisualAssertOptions struct {
	Comparator VisualComparator
	Context    ContextRef
	// ContextError is a browser-context creation failure the tool observed
	// before the comparison. It is recorded as a not_run result so the finish
	// gate still has the assertion submission, and returned unchanged so the
	// tool maps the executor error code.
	ContextError error
	Baselines    VisualBaselineReader
	Capture      EvidenceCapturePort
	// MaxBaselineBytes caps the request-side baseline copy (0 = the 5 MiB
	// design spec default, enforced by the browser client).
	MaxBaselineBytes int64
	// MaxArtifactBytes caps one captured PNG (0 = no extra cap; the evidence
	// service still enforces EZTEST_EXEC_EVIDENCE_MAX_BYTES).
	MaxArtifactBytes int64
}

// VisualAssertOutcome is the model-facing result of one visual assertion.
// Verdict/Classification/FailureClass are the three lenses the agent needs:
// the recorded assertion_result verdict (passed/failed/not_run), the §9.3 case
// classification (from ClassifyVisual) and its failure class.
type VisualAssertOutcome struct {
	AssertionID      int64
	Verdict          string
	Classification   string
	FailureClass     string
	Reason           string
	FailureReason    string
	DiffRatio        float64
	Threshold        float64
	HasBaseline      bool
	EnvironmentMatch bool
	Width            int
	Height           int
	ActualEvidenceID int64
	DiffEvidenceID   int64
	Repeated         bool
}

// VisualAssertor is the optional visual-assertion use case of the recorder. It
// is a separate interface so a partially wired Recorder stays valid; the
// execution.visual_assert tool type-asserts it and fails closed otherwise.
type VisualAssertor interface {
	VisualAssert(ctx context.Context, caseRunID, assertionID int64, opts VisualAssertOptions) (VisualAssertOutcome, error)
}

// visualAssertActual is the compact JSON projection stored on assertion_result
// .actual and replayed on a repeated call.
type visualAssertActual struct {
	AssertionID      string  `json:"assertionId"`
	DiffRatio        float64 `json:"diffRatio"`
	Threshold        float64 `json:"threshold"`
	EnvironmentMatch bool    `json:"environmentMatch"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	BaselineEvidence string  `json:"baselineEvidenceId,omitempty"`
	ActualEvidenceID string  `json:"actualEvidenceId,omitempty"`
	DiffEvidenceID   string  `json:"diffEvidenceId,omitempty"`
	Error            string  `json:"error,omitempty"`
}

var _ VisualAssertor = (*CaseRecorder)(nil)

// VisualAssert executes one kind=visual assertion of a case run (design spec
// §9.3). The agent supplies only the assertion id: the comparison configuration
// is read from the pinned snapshot, the approved baseline from the evidence
// store and the actual screenshot from the executor. The four-state outcome is
// computed by domain.ClassifyVisual; every executed/failed attempt records the
// assertion_result so the finish gate always has the assertion submission, and a
// repeated call replays the recorded result instead of comparing again.
func (r *CaseRecorder) VisualAssert(ctx context.Context, caseRunID, assertionID int64, opts VisualAssertOptions) (VisualAssertOutcome, error) {
	if err := r.ready(); err != nil {
		return VisualAssertOutcome{}, err
	}
	if caseRunID <= 0 || assertionID <= 0 {
		return VisualAssertOutcome{}, recorderToolError(toolCodeInvalidArguments,
			"case run and assertion ids must be positive")
	}

	run, err := r.loadCaseRun(ctx, caseRunID)
	if err != nil {
		return VisualAssertOutcome{}, err
	}
	if err := requireRecordableRun(run); err != nil {
		return VisualAssertOutcome{}, err
	}
	assertion, ok := findSnapshotAssertion(run.Snapshot, assertionID)
	if !ok {
		return VisualAssertOutcome{}, recorderToolError(toolCodeInvalidArguments,
			"assertion %d is not part of case run %d snapshot", assertionID, caseRunID)
	}
	if !strings.EqualFold(strings.TrimSpace(assertion.Kind), "visual") {
		return VisualAssertOutcome{}, recorderToolError(toolCodeInvalidArguments,
			"assertion %d is kind %q; the execution.visual_assert tool only executes visual assertions",
			assertionID, assertion.Kind)
	}

	// Idempotency: the (case_run, assertion) pair may be recorded once. A
	// repeated call replays the recorded result instead of comparing again.
	existing, err := r.deps.Assertions.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return VisualAssertOutcome{}, recorderToolError(toolCodeInternal,
			"read recorded assertions: %v", err)
	}
	for _, recorded := range existing {
		if recorded.AssertionID == assertionID {
			return r.replayVisualAssertOutcome(ctx, run, assertion, recorded)
		}
	}

	visual := assertion.Visual
	if visual == nil {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Error: visualReasonInvalidConfig,
		}, 0, visualReasonInvalidConfig, "invalid_arguments",
			"the visual assertion has no comparison configuration; finish inconclusive(case)")
	}
	threshold := visual.Threshold
	if threshold < 0 || threshold > 0.1 {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold, Error: visualReasonInvalidConfig,
		}, threshold, visualReasonInvalidConfig, "invalid_arguments",
			"the visual threshold is outside 0..0.1; finish inconclusive(case)")
	}
	if visual.BaselineEvidenceID == nil || *visual.BaselineEvidenceID <= 0 {
		// No approved baseline: the assertion cannot run. The browser is never
		// opened and the agent finishes inconclusive(case).
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold, EnvironmentMatch: true,
			Error: visualReasonMissingBaseline,
		}, threshold, visualReasonMissingBaseline, "missing_visual_baseline",
			"no approved visual baseline; finish inconclusive(case)")
	}
	baselineEvidenceID := *visual.BaselineEvidenceID
	if opts.Comparator == nil {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold, EnvironmentMatch: true,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: visualReasonUnsupported,
		}, threshold, visualReasonUnsupported, "unsupported_assertion_kind",
			"the browser executor does not support visual comparison; finish inconclusive(case)")
	}
	if opts.ContextError != nil {
		reason := visualReasonCompareFailed
		var browserErr *BrowserError
		if errors.As(opts.ContextError, &browserErr) && strings.TrimSpace(browserErr.Code) != "" {
			reason = browserErr.Code
		}
		outcome, recordErr := r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: reason,
		}, threshold, reason, "", "")
		if recordErr != nil {
			return VisualAssertOutcome{}, recordErr
		}
		return outcome, opts.ContextError
	}
	if opts.Baselines == nil {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: visualReasonReadFailed,
		}, threshold, visualReasonReadFailed, toolCodeInternal,
			"the visual baseline reader is not configured; finish inconclusive(model)")
	}
	if opts.Capture == nil {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: visualReasonCaptureFailed,
		}, threshold, visualReasonCaptureFailed, toolCodeInternal,
			"the evidence capture is not configured; finish inconclusive(model)")
	}

	baseline, err := opts.Baselines.ReadVisualBaseline(ctx, run.VersionID, baselineEvidenceID)
	if err != nil {
		code, reason, hint := visualBaselineReadFailure(err)
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold, EnvironmentMatch: true,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: reason,
		}, threshold, reason, code, hint)
	}

	request := VisualCompareRequest{
		Baseline:          baseline.Data,
		Selector:          strings.TrimSpace(visual.Selector),
		Threshold:         threshold,
		Viewport:          visualViewport(visual),
		DeviceScaleFactor: visualDeviceScaleFactor(visual),
		MaxBaselineBytes:  opts.MaxBaselineBytes,
	}
	if visual.Region != nil {
		request.Region = &VisualRegion{
			X: visual.Region.X, Y: visual.Region.Y,
			Width: visual.Region.Width, Height: visual.Region.Height,
		}
	}
	result, err := opts.Comparator.CompareVisual(ctx, opts.Context, request)
	if err != nil {
		reason := visualReasonCompareFailed
		var browserErr *BrowserError
		if errors.As(err, &browserErr) && strings.TrimSpace(browserErr.Code) != "" {
			reason = browserErr.Code
		}
		outcome, recordErr := r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: reason,
		}, threshold, reason, "", "")
		if recordErr != nil {
			return VisualAssertOutcome{}, recordErr
		}
		// The tool maps the typed browser error; the assertion result is already
		// durable so the finish gate accepts the prescribed class.
		return outcome, err
	}

	if len(result.ActualPNG) == 0 {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold, EnvironmentMatch: result.EnvironmentMatch,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: "visual_actual_missing",
		}, threshold, "visual_actual_missing", "internal",
			"the executor returned no actual screenshot; finish inconclusive(model)")
	}
	if opts.MaxArtifactBytes > 0 && int64(len(result.ActualPNG)) > opts.MaxArtifactBytes {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold, EnvironmentMatch: result.EnvironmentMatch,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: "visual_artifact_too_large",
		}, threshold, "visual_artifact_too_large", "internal",
			"the actual screenshot exceeds the evidence limit; finish inconclusive(model)")
	}
	if opts.MaxArtifactBytes > 0 && int64(len(result.DiffPNG)) > opts.MaxArtifactBytes {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold, EnvironmentMatch: result.EnvironmentMatch,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: "visual_artifact_too_large",
		}, threshold, "visual_artifact_too_large", "internal",
			"the diff screenshot exceeds the evidence limit; finish inconclusive(model)")
	}

	verdict, class, reason := domain.ClassifyVisual(true, result.EnvironmentMatch, result.DiffRatio, threshold)
	assertionVerdict := visualAssertNotRun
	switch verdict {
	case domain.VerdictPassed:
		assertionVerdict = visualAssertPassed
	case domain.VerdictFailed:
		assertionVerdict = visualAssertFailed
	}

	// Reserve the assertion_result id before capturing evidence so both
	// artifacts can carry their assertion_result_id metadata.
	resultID, idErr := nextID(r.deps.IDs)
	if idErr != nil {
		return VisualAssertOutcome{}, recorderToolError(toolCodeInternal, "mint visual assertion result id: %v", idErr)
	}
	width, height := result.Width, result.Height
	if width <= 0 || height <= 0 {
		width, height = result.ActualWidth, result.ActualHeight
	}
	metadata := map[string]any{
		"assertion_id":         decimalString(assertionID),
		"assertion_result_id":  decimalString(resultID),
		"threshold":            threshold,
		"diff_ratio":           result.DiffRatio,
		"environment_match":    result.EnvironmentMatch,
		"width":                width,
		"height":               height,
		"baseline_evidence_id": decimalString(baselineEvidenceID),
	}
	actualEvidence, err := opts.Capture.Capture(ctx, CaptureInput{
		BatchID:   run.BatchID,
		CaseRunID: run.ID,
		Kind:      visualActualEvidenceKind,
		Mime:      visualEvidenceMIME,
		Data:      result.ActualPNG,
		Metadata:  metadata,
	})
	if err != nil {
		return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
			AssertionID: decimalString(assertionID), Threshold: threshold, EnvironmentMatch: result.EnvironmentMatch,
			BaselineEvidence: decimalString(baselineEvidenceID), Error: visualReasonCaptureFailed,
		}, threshold, visualReasonCaptureFailed, "internal",
			"the visual_actual evidence could not be captured; finish inconclusive(model)")
	}

	actualIDs := []int64{actualEvidence.ID}
	var diffEvidenceID int64
	if len(result.DiffPNG) > 0 {
		diffEvidence, diffErr := opts.Capture.Capture(ctx, CaptureInput{
			BatchID:   run.BatchID,
			CaseRunID: run.ID,
			Kind:      visualDiffEvidenceKind,
			Mime:      visualEvidenceMIME,
			Data:      result.DiffPNG,
			Metadata:  metadata,
		})
		if diffErr != nil {
			return r.recordVisualNotRun(ctx, run, assertion, &visualAssertActual{
				AssertionID: decimalString(assertionID), Threshold: threshold, EnvironmentMatch: result.EnvironmentMatch,
				BaselineEvidence: decimalString(baselineEvidenceID), Error: visualReasonCaptureFailed,
			}, threshold, visualReasonCaptureFailed, "internal",
				"the visual_diff evidence could not be captured; finish inconclusive(model)")
		}
		diffEvidenceID = diffEvidence.ID
		actualIDs = append(actualIDs, diffEvidenceID)
	}

	actual := &visualAssertActual{
		AssertionID:      decimalString(assertionID),
		DiffRatio:        result.DiffRatio,
		Threshold:        threshold,
		EnvironmentMatch: result.EnvironmentMatch,
		Width:            width,
		Height:           height,
		BaselineEvidence: decimalString(baselineEvidenceID),
		ActualEvidenceID: decimalString(actualEvidence.ID),
	}
	if diffEvidenceID > 0 {
		actual.DiffEvidenceID = decimalString(diffEvidenceID)
	}
	failureReason := ""
	if verdict != domain.VerdictPassed {
		failureReason = reason
	}
	stored, persistErr := r.persistVisualAssertion(ctx, run, assertion, resultID,
		assertionVerdict, actual, failureReason)
	if persistErr != nil {
		return VisualAssertOutcome{}, persistErr
	}
	r.createRefs(ctx, stored.ID, actualIDs, "assertion_result")

	outcome := VisualAssertOutcome{
		AssertionID:      assertionID,
		Verdict:          assertionVerdict,
		Classification:   string(verdict),
		FailureClass:     string(class),
		Reason:           reason,
		FailureReason:    failureReason,
		DiffRatio:        result.DiffRatio,
		Threshold:        threshold,
		HasBaseline:      true,
		EnvironmentMatch: result.EnvironmentMatch,
		Width:            width,
		Height:           height,
		ActualEvidenceID: actualEvidence.ID,
		DiffEvidenceID:   diffEvidenceID,
	}
	if verdict == domain.VerdictInconclusive {
		// The mismatch is recorded, but the agent must follow the prescribed
		// inconclusive(environment) finish instead of treating it as a failure.
		return outcome, &agentdomain.ToolError{Code: visualReasonEnvironmentMismatch,
			Message: "visual_environment_mismatch: the live viewport/device scale factor differs from the approved baseline; finish inconclusive(environment)"}
	}
	return outcome, nil
}

// recordVisualNotRun persists a not_run assertion result and returns the
// prescribed tool error. code/hint are empty when the caller returns a typed
// browser error instead (the tool maps it). The write must succeed: without the
// assertion submission the finish gate rejects every non-passed finish.
func (r *CaseRecorder) recordVisualNotRun(ctx context.Context, run domain.CaseRun, assertion domain.CaseSnapshotAssertion, actual *visualAssertActual, threshold float64, reason, code, hint string) (VisualAssertOutcome, error) {
	if _, err := r.persistVisualAssertion(ctx, run, assertion, 0, visualAssertNotRun, actual, reason); err != nil {
		return VisualAssertOutcome{}, err
	}
	class := domain.FailureClassCase
	if code == "internal" {
		class = domain.FailureClassModel
	}
	if reason == visualReasonEnvironmentMismatch {
		class = domain.FailureClassEnvironment
	}
	outcome := VisualAssertOutcome{
		AssertionID:      assertion.ID,
		Verdict:          visualAssertNotRun,
		Classification:   string(domain.VerdictInconclusive),
		FailureClass:     string(class),
		Reason:           reason,
		FailureReason:    reason,
		Threshold:        threshold,
		HasBaseline:      reason != visualReasonMissingBaseline,
		EnvironmentMatch: reason != visualReasonEnvironmentMismatch && reason != visualReasonMissingBaseline,
		Width:            actual.Width,
		Height:           actual.Height,
	}
	if code == "" {
		return outcome, nil
	}
	if hint == "" {
		hint = "finish inconclusive(case)"
	}
	return outcome, &agentdomain.ToolError{Code: code, Message: reason + ": " + hint}
}

// replayVisualAssertOutcome projects an already recorded visual assertion
// without comparing again. A recorded not_run failure replays the same stable
// tool code so the agent keeps following the prescribed finish.
func (r *CaseRecorder) replayVisualAssertOutcome(ctx context.Context, run domain.CaseRun, assertion domain.CaseSnapshotAssertion, recorded domain.AssertionResult) (VisualAssertOutcome, error) {
	outcome := VisualAssertOutcome{
		AssertionID:   recorded.AssertionID,
		Verdict:       recorded.Verdict,
		Reason:        recorded.FailureReason,
		FailureReason: recorded.FailureReason,
		Repeated:      true,
		HasBaseline:   assertion.Visual != nil && assertion.Visual.BaselineEvidenceID != nil,
	}
	if assertion.Visual != nil {
		outcome.Threshold = assertion.Visual.Threshold
	}
	var actual visualAssertActual
	if strings.TrimSpace(recorded.Actual) != "" {
		_ = json.Unmarshal([]byte(recorded.Actual), &actual)
		outcome.DiffRatio = actual.DiffRatio
		outcome.EnvironmentMatch = actual.EnvironmentMatch
		outcome.Width = actual.Width
		outcome.Height = actual.Height
	}
	if r.deps.Evidence != nil {
		rows, err := r.deps.Evidence.ListByCaseRun(ctx, run.ID)
		if err == nil {
			want := decimalString(recorded.AssertionID)
			for _, row := range rows {
				if !strings.EqualFold(metadataString(row.Metadata["assertion_id"]), want) {
					continue
				}
				switch row.Kind {
				case visualActualEvidenceKind:
					outcome.ActualEvidenceID = row.ID
				case visualDiffEvidenceKind:
					outcome.DiffEvidenceID = row.ID
				}
			}
		}
	}

	switch strings.ToLower(strings.TrimSpace(recorded.Verdict)) {
	case visualAssertPassed:
		outcome.Classification = string(domain.VerdictPassed)
		outcome.Reason = visualReasonMatch
		outcome.FailureReason = ""
		return outcome, nil
	case visualAssertFailed:
		outcome.Classification = string(domain.VerdictFailed)
		outcome.FailureClass = string(domain.FailureClassApplication)
		if outcome.Reason == "" {
			outcome.Reason = "visual_mismatch"
		}
		return outcome, nil
	}

	reason := strings.TrimSpace(recorded.FailureReason)
	if reason == "" {
		reason = visualReasonCompareFailed
	}
	outcome.Reason = reason
	switch reason {
	case visualReasonEnvironmentMismatch:
		outcome.Classification = string(domain.VerdictInconclusive)
		outcome.FailureClass = string(domain.FailureClassEnvironment)
		return outcome, &agentdomain.ToolError{Code: visualReasonEnvironmentMismatch,
			Message: reason + ": finish inconclusive(environment)"}
	case visualReasonMissingBaseline:
		outcome.Classification = string(domain.VerdictInconclusive)
		outcome.FailureClass = string(domain.FailureClassCase)
		return outcome, &agentdomain.ToolError{Code: visualReasonMissingBaseline,
			Message: reason + ": finish inconclusive(case)"}
	case visualReasonUnsupported:
		outcome.Classification = string(domain.VerdictInconclusive)
		outcome.FailureClass = string(domain.FailureClassCase)
		return outcome, &agentdomain.ToolError{Code: visualReasonUnsupported,
			Message: reason + ": finish inconclusive(case)"}
	case visualReasonInvalidConfig, visualReasonBaselineInvalid:
		outcome.Classification = string(domain.VerdictInconclusive)
		outcome.FailureClass = string(domain.FailureClassCase)
		return outcome, &agentdomain.ToolError{Code: toolCodeInvalidArguments,
			Message: reason + ": finish inconclusive(case)"}
	}
	outcome.Classification = string(domain.VerdictInconclusive)
	outcome.FailureClass = string(domain.FailureClassModel)
	return outcome, &agentdomain.ToolError{Code: toolCodeInternal,
		Message: reason + ": finish inconclusive(model)"}
}

// persistVisualAssertion writes the server-owned assertion_result of one visual
// assertion. id 0 mints a fresh Snowflake id; a caller that already reserved one
// (to stamp evidence metadata) passes it here.
func (r *CaseRecorder) persistVisualAssertion(ctx context.Context, run domain.CaseRun, assertion domain.CaseSnapshotAssertion, id int64, verdict string, actual *visualAssertActual, failureReason string) (domain.AssertionResult, error) {
	if id <= 0 {
		minted, err := nextID(r.deps.IDs)
		if err != nil {
			return domain.AssertionResult{}, recorderToolError(toolCodeInternal,
				"mint visual assertion result id: %v", err)
		}
		id = minted
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		return domain.AssertionResult{}, recorderToolError(toolCodeInternal,
			"encode visual assertion actual: %v", err)
	}
	now := r.now()
	stored, err := r.deps.Assertions.Upsert(ctx, domain.AssertionResult{
		ID:            id,
		BatchID:       run.BatchID,
		CaseRunID:     run.ID,
		StepResultID:  r.stepResultIDForSeq(ctx, run.ID, assertion.StepSeq),
		AssertionID:   assertion.ID,
		Kind:          assertion.Kind,
		Target:        assertion.Target,
		Expected:      assertion.Expected,
		Actual:        string(encoded),
		Verdict:       verdict,
		FailureReason: failureReason,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		return domain.AssertionResult{}, recorderToolError(toolCodeInternal,
			"persist visual assertion %d: %v", assertion.ID, err)
	}
	return stored, nil
}

// visualBaselineReadFailure maps a baseline read failure onto the model-facing
// tool code, the recorded §9.3 reason and the finish hint.
func visualBaselineReadFailure(err error) (code, reason, hint string) {
	var reasonErr *domain.ReasonError
	if errors.As(err, &reasonErr) {
		switch reasonErr.Code {
		case "not_found":
			return visualReasonMissingBaseline, visualReasonMissingBaseline, "finish inconclusive(case)"
		case "invalid_argument":
			return toolCodeInvalidArguments, visualReasonBaselineInvalid, "finish inconclusive(case)"
		}
	}
	if errors.Is(err, ErrEvidenceNotFound) {
		return visualReasonMissingBaseline, visualReasonMissingBaseline, "finish inconclusive(case)"
	}
	return toolCodeInternal, visualReasonReadFailed, "finish inconclusive(model)"
}

// visualViewport projects a snapshot viewport, applying the mindmap defaults
// when a historical snapshot left it zero.
func visualViewport(visual *domain.CaseSnapshotVisual) Viewport {
	width, height := visual.ViewportWidth, visual.ViewportHeight
	if width <= 0 {
		width = visualDefaultViewportWidth
	}
	if height <= 0 {
		height = visualDefaultViewportHeight
	}
	return Viewport{Width: width, Height: height}
}

// visualDeviceScaleFactor projects a snapshot device scale factor with the
// mindmap default.
func visualDeviceScaleFactor(visual *domain.CaseSnapshotVisual) float64 {
	if visual.DeviceScaleFactor <= 0 {
		return visualDefaultDeviceScale
	}
	return visual.DeviceScaleFactor
}

// decimalString formats a positive id as the decimal string wire form.
func decimalString(id int64) string { return strconv.FormatInt(id, 10) }
