package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Visual assertion verdict reasons (execution domain subset of spec §7.3).
const (
	VisualReasonMatch                = "visual_match"
	VisualReasonMismatch             = "visual_mismatch"
	VisualReasonMissingBase          = "missing_visual_baseline"
	VisualReasonEnvironment          = "visual_environment_mismatch"
	VisualReasonConfigChanged        = "visual_config_changed"
	VisualReasonBaseInvalid          = "visual_baseline_invalid"
	VisualReasonActualInvalid        = "visual_actual_invalid"
	VisualClassificationPassed       = "passed"
	VisualClassificationFailed       = "failed"
	VisualClassificationInconclusive = "inconclusive"
	VisualClassificationBlocked      = "blocked"
)

const (
	settingVisualCompareTimeout = "visual.compare_timeout_seconds"
	settingVisualBaselineMax    = "visual.baseline_max_bytes"
	defaultCompareTimeoutSec    = int64(60)
	defaultVisualBaselineBytes  = int64(5 << 20)
)

// VisualAssertInput is one execution.visual_assert call. AssertionID is the
// visual_assertion configuration ID (the case assertion is resolved by the
// configuration).
type VisualAssertInput struct {
	VersionID     int64
	CaseRunID     int64
	CaseVersionID int64
	AssertionID   int64
	VersionSUTID  int64
	StepSeq       int
	AssertionSeq  int
	AccountRole   string
}

// VisualAssertResult is the persistent outcome of one visual assertion.
type VisualAssertResult struct {
	AssertionResultID int64
	Verdict           string
	Reason            string
	Classification    string
	VisualDiffID      *int64
	ActualEvidenceID  *int64
	DiffEvidenceID    *int64
	EnvironmentMatch  bool
	DiffRatio         float64
}

// VisualAssertConfig is the execution-side read projection of visual_assertion.
type VisualAssertConfig struct {
	ID                int64
	VersionID         int64
	CaseVersionID     int64
	CaseAssertionID   int64
	StepSeq           int
	AssertionSeq      int
	VersionSUTID      int64
	Selector          string
	Region            json.RawMessage
	Threshold         float64
	ViewportWidth     int
	ViewportHeight    int
	DeviceScaleFactor float64
	WaitPolicy        json.RawMessage
	ConfigRevision    int
}

// VisualBaselineRef is the execution-side read projection of an approved
// baseline (no bytes; the caller reads them from the content ledger).
type VisualBaselineRef struct {
	ID                     int64
	EvidenceID             int64
	SHA256                 string
	SizeBytes              int64
	Width                  int
	Height                 int
	ConfigRevision         int
	EnvironmentFingerprint string
}

// VisualDiffInput is one comparison record written by the execution service.
type VisualDiffInput struct {
	VersionID         int64
	VisualAssertionID int64
	CaseRunID         int64
	AssertionResultID int64
	BaselineID        *int64
	ActualEvidenceID  int64
	DiffEvidenceID    *int64
	DiffRatio         float64
	Threshold         float64
	Classification    string
	Reason            string
	EnvironmentMatch  bool
	Width             int
	Height            int
	MismatchBoxes     json.RawMessage
}

// VisualDiffRecord is one stored visual_diff row.
type VisualDiffRecord struct {
	ID                int64
	AssertionResultID int64
	VisualAssertionID int64
	BaselineID        *int64
	ActualEvidenceID  int64
	DiffEvidenceID    *int64
	DiffRatio         float64
	Threshold         float64
	Classification    string
	Reason            string
	EnvironmentMatch  bool
	Width             int
	Height            int
	MismatchBoxes     json.RawMessage
}

// VisualAssertPort is the execution context's view of the visual context. The
// adapter (execution/infrastructure/visualadapter) maps it onto the visual
// application services; the execution service owns evidence writes.
type VisualAssertPort interface {
	Config(ctx context.Context, versionID, assertionID int64) (VisualAssertConfig, error)
	ActiveBaseline(ctx context.Context, assertionID int64) (VisualBaselineRef, bool, error)
	RecordDiff(ctx context.Context, in VisualDiffInput) (VisualDiffRecord, error)
	DiffForAssertionResult(ctx context.Context, assertionResultID int64) (VisualDiffRecord, bool, error)
}

type visualComparePayload struct {
	CaptureOnly      bool            `json:"capture_only"`
	DiffRatio        float64         `json:"diff_ratio"`
	Width            int             `json:"width"`
	Height           int             `json:"height"`
	EnvironmentMatch bool            `json:"environment_match"`
	MismatchBoxes    json.RawMessage `json:"mismatch_boxes"`
}

// VisualAssert executes one visual assertion under the case_run dispatch fence:
// it captures the actual screenshot, compares against the approved baseline
// (capture-only when none exists), stores the visual_actual/visual_diff
// evidence, writes the assertion_result/visual_diff rows and returns the
// classification. Replays return the recorded result without new evidence.
func (s *CaseRunner) VisualAssert(ctx context.Context, scope tools.Scope, assertionID int64) (any, error) {
	if assertionID <= 0 {
		return nil, platform.Invalid("assertionId", "must be a positive decimal ID")
	}
	if s.visual == nil {
		return nil, errors.New("case runner has no visual assertion service")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	cfg, err := s.visual.Config(ctx, scope.VersionID, assertionID)
	if err != nil {
		return nil, mapExecutionError(err, "visual_assertion")
	}
	if cfg.VersionID != scope.VersionID || cfg.CaseVersionID != caseRun.CaseVersionID {
		return nil, platform.NotFound("visual_assertion")
	}
	caseSnapshot, err := s.caseSnapshot(ctx, scope, caseRun)
	if err != nil {
		return nil, err
	}
	assertion, ok := findAssertion(caseSnapshot, cfg.StepSeq, cfg.AssertionSeq)
	if !ok || assertion.Kind != execution.AssertionKindVisual {
		return nil, platform.StateConflict("the case version has no visual assertion at that position")
	}
	if cfg.VersionSUTID <= 0 {
		cfg.VersionSUTID = assertion.TargetVersionSUTID
	}
	if assertion.TargetVersionSUTID > 0 && cfg.VersionSUTID != assertion.TargetVersionSUTID {
		return nil, platform.StateConflict("the visual configuration targets another version_sut")
	}

	if replay, found, replayErr := s.visualAssertReplay(ctx, caseRun, cfg); replayErr != nil {
		return nil, replayErr
	} else if found {
		return replay, nil
	}

	step, found := findStep(caseSnapshot, cfg.StepSeq)
	if !found {
		return nil, platform.StateConflict("the case version has no step for the visual assertion")
	}
	binding, err := s.bindingFor(ctx, scope.VersionID, cfg.VersionSUTID)
	if err != nil {
		return nil, err
	}
	contextID, err := s.ensureBrowserContext(ctx, caseRun, binding, step.AccountRole, bindingHosts(binding))
	if err != nil {
		return nil, err
	}

	baseline, hasBaseline, baselineErr := s.visual.ActiveBaseline(ctx, cfg.ID)
	if baselineErr != nil {
		return nil, mapExecutionError(baselineErr, "visual_baseline")
	}
	baselineInvalid := false
	baselineBytes, readErr := s.readBaselineBytes(ctx, baseline)
	if hasBaseline && readErr != nil {
		// The baseline cannot be used, but the actual capture still runs so the
		// outcome is a reproducible inconclusive(visual_baseline_invalid).
		hasBaseline = false
		baselineInvalid = true
	}

	args, err := s.visualCompareArgs(ctx, cfg, baselineBytes, hasBaseline)
	if err != nil {
		return nil, err
	}
	actionResult, err := s.browser.Action(ctx, contextID, "visual.compare", args)
	if err != nil {
		return s.writeVisualFailure(ctx, caseRun, scope, assertion, cfg, reasonForVisualError(err), true)
	}
	if actionResult.Error != nil {
		if actionResult.Error.Code == "context_not_found" {
			s.markSUTContext(ctx, caseRun.ID, cfg.VersionSUTID, step.AccountRole, execution.SUTContextFailed)
		}
		return s.writeVisualFailure(ctx, caseRun, scope, assertion, cfg,
			reasonForVisualError(&tools.Error{Code: actionResult.Error.Code, Message: actionResult.Error.Message}), true)
	}
	var payload visualComparePayload
	if len(actionResult.Result) > 0 {
		if decodeErr := json.Unmarshal(actionResult.Result, &payload); decodeErr != nil {
			return s.writeVisualFailure(ctx, caseRun, scope, assertion, cfg, VisualReasonActualInvalid, false)
		}
	}

	actualArtifact, ok := visualArtifact(actionResult.Artifacts, "actual_png", "visual_actual")
	if !ok {
		return s.writeVisualFailure(ctx, caseRun, scope, assertion, cfg, VisualReasonActualInvalid, false)
	}
	actualBytes, err := decodeVisualArtifact(actualArtifact)
	if err != nil {
		return s.writeVisualFailure(ctx, caseRun, scope, assertion, cfg, VisualReasonActualInvalid, false)
	}
	maxBaselineBytes := s.intSetting(settingVisualBaselineMax, defaultVisualBaselineBytes)
	if int64(len(actualBytes)) > maxBaselineBytes {
		return s.writeVisualFailure(ctx, caseRun, scope, assertion, cfg, VisualReasonActualInvalid, false)
	}
	actualEvidence, err := s.AddEvidence(ctx, scope, EvidenceInput{
		Kind: execution.EvidenceVisualActual, Data: actualBytes,
		Filename: "visual-actual.png", StepSeq: &cfg.StepSeq, AssertionSeq: &cfg.AssertionSeq,
		VersionSUTID: &cfg.VersionSUTID, Auto: true,
	})
	if err != nil {
		if isEvidenceTooLarge(err) {
			return s.writeVisualFailure(ctx, caseRun, scope, assertion, cfg, VisualReasonActualInvalid, false)
		}
		return nil, err
	}
	if actualEvidence.ID == 0 {
		// The evidence cap dropped the capture; the assertion result is still
		// recorded as inconclusive and no diff row is fabricated.
		return s.writeVisualFailure(ctx, caseRun, scope, assertion, cfg, VisualReasonActualInvalid, false)
	}

	classification, reason := classifyVisual(hasBaseline, baselineInvalid, payload.EnvironmentMatch, payload.DiffRatio, cfg.Threshold)
	var diffEvidenceID *int64
	if classification == VisualClassificationPassed || classification == VisualClassificationFailed {
		if diffArtifact, ok := visualArtifact(actionResult.Artifacts, "diff_png", "visual_diff"); ok {
			diffBytes, decodeErr := decodeVisualArtifact(diffArtifact)
			if decodeErr != nil {
				classification, reason = VisualClassificationInconclusive, VisualReasonActualInvalid
			} else {
				stored, addErr := s.AddEvidence(ctx, scope, EvidenceInput{
					Kind: execution.EvidenceVisualDiff, Data: diffBytes,
					Filename: "visual-diff.png", StepSeq: &cfg.StepSeq, AssertionSeq: &cfg.AssertionSeq,
					VersionSUTID: &cfg.VersionSUTID, Auto: true,
				})
				if addErr != nil {
					return nil, addErr
				}
				if stored.ID > 0 {
					id := stored.ID
					diffEvidenceID = &id
				}
			}
		}
	}

	verdict := execution.AssertionInconclusive
	switch classification {
	case VisualClassificationPassed:
		verdict = execution.AssertionPassed
	case VisualClassificationFailed:
		verdict = execution.AssertionFailed
	}
	actualText := visualActualText(payload, cfg.Threshold)
	stored, err := s.writeVisualAssertionResult(ctx, caseRun, assertion, cfg, verdict, reason, actualText)
	if err != nil {
		return nil, err
	}
	var baselineID *int64
	if hasBaseline {
		id := baseline.ID
		baselineID = &id
	}
	boxes := payload.MismatchBoxes
	if len(boxes) == 0 {
		boxes = json.RawMessage(`[]`)
	}
	record, err := s.visual.RecordDiff(ctx, VisualDiffInput{
		VersionID: scope.VersionID, VisualAssertionID: cfg.ID, CaseRunID: caseRun.ID,
		AssertionResultID: stored.ID, BaselineID: baselineID, ActualEvidenceID: actualEvidence.ID,
		DiffEvidenceID: diffEvidenceID, DiffRatio: payload.DiffRatio, Threshold: cfg.Threshold,
		Classification: classification, Reason: reason, EnvironmentMatch: payload.EnvironmentMatch,
		Width: payload.Width, Height: payload.Height, MismatchBoxes: boxes,
	})
	if err != nil {
		return nil, mapExecutionError(err, "visual_diff")
	}
	diffID := record.ID
	s.recordVisualAssertAudit(ctx, caseRun, cfg, stored, &diffID)
	actualID := actualEvidence.ID
	return map[string]any{
		"assertion_result_id": platformid.Format(stored.ID),
		"verdict":             string(verdict),
		"classification":      classification,
		"reason":              reason,
		"diff_ratio":          payload.DiffRatio,
		"environment_match":   payload.EnvironmentMatch,
		"visual_diff_id":      platformid.Format(diffID),
		"actual_evidence_id":  platformid.Format(actualID),
		"diff_evidence_id":    optionalIDString(diffEvidenceID),
	}, nil
}

// writeVisualFailure records an inconclusive assertion result when the
// comparison could not run (executor/page/artifact failure). The assertion
// verdict stays inconclusive; the case_run failure class is decided by the
// caller/finalizer.
func (s *CaseRunner) writeVisualFailure(ctx context.Context, caseRun execution.CaseRun, scope tools.Scope, assertion execution.CaseAssertion, cfg VisualAssertConfig, reason string, blocked bool) (any, error) {
	stored, err := s.writeVisualAssertionResult(ctx, caseRun, assertion, cfg, execution.AssertionInconclusive, reason, "visual comparison could not run")
	if err != nil {
		return nil, err
	}
	classification := VisualClassificationInconclusive
	if blocked {
		classification = VisualClassificationBlocked
	}
	s.recordVisualAssertAudit(ctx, caseRun, cfg, stored, nil)
	return map[string]any{
		"assertion_result_id": platformid.Format(stored.ID),
		"verdict":             string(execution.AssertionInconclusive),
		"classification":      classification,
		"reason":              reason,
		"environment_match":   false,
		"diff_ratio":          0,
	}, nil
}

func (s *CaseRunner) writeVisualAssertionResult(ctx context.Context, caseRun execution.CaseRun, assertion execution.CaseAssertion, cfg VisualAssertConfig, verdict execution.AssertionVerdict, reason, actual string) (execution.AssertionResult, error) {
	now := s.now()
	target := cfg.VersionSUTID
	result := execution.AssertionResult{
		ID: s.nextID(), CaseRunID: caseRun.ID, StepSeq: cfg.StepSeq, AssertionSeq: cfg.AssertionSeq,
		Kind: execution.AssertionKindVisual, Verdict: verdict,
		Reason:             truncateRunes(reason, execution.MaxAssertionReasonLength),
		Expected:           truncateRunes(assertion.Expected, execution.MaxExpectedLength),
		Actual:             truncateRunes(actual, execution.MaxActualLength),
		TargetVersionSUTID: &target, CreatedAt: now, UpdatedAt: now,
	}
	ok, err := s.results.UpsertAssertion(ctx, result, caseRun.DispatchEpoch, caseRun.DispatchToken)
	if err != nil {
		return execution.AssertionResult{}, mapExecutionError(err, "assertion_result")
	}
	if !ok {
		return execution.AssertionResult{}, platform.LeaseLost()
	}
	stored, found, err := s.findVisualResult(ctx, caseRun.ID, cfg.StepSeq, cfg.AssertionSeq)
	if err != nil {
		return execution.AssertionResult{}, err
	}
	if !found {
		return execution.AssertionResult{}, errors.New("visual assertion result was not persisted")
	}
	s.renewLeaseQuietly(ctx, caseRun)
	s.publishCaseRunEvent(EventAssertionFinished, caseRun, map[string]any{
		"step_seq": cfg.StepSeq, "assertion_seq": cfg.AssertionSeq, "verdict": string(verdict),
	})
	return stored, nil
}

func (s *CaseRunner) findVisualResult(ctx context.Context, caseRunID int64, stepSeq, assertionSeq int) (execution.AssertionResult, bool, error) {
	assertions, err := s.results.ListAssertions(ctx, caseRunID)
	if err != nil {
		return execution.AssertionResult{}, false, mapExecutionError(err, "assertion_result")
	}
	for _, row := range assertions {
		if row.StepSeq == stepSeq && row.AssertionSeq == assertionSeq {
			return row, true, nil
		}
	}
	return execution.AssertionResult{}, false, nil
}

// visualAssertReplay returns the stored result of a previous call. Any recorded
// non-skipped visual result is a replay; the diff row supplies the
// classification when present.
func (s *CaseRunner) visualAssertReplay(ctx context.Context, caseRun execution.CaseRun, cfg VisualAssertConfig) (any, bool, error) {
	existing, found, err := s.findVisualResult(ctx, caseRun.ID, cfg.StepSeq, cfg.AssertionSeq)
	if err != nil {
		return nil, false, err
	}
	if !found || existing.Kind != execution.AssertionKindVisual || existing.Verdict == execution.AssertionSkipped {
		return nil, false, nil
	}
	diff, hasDiff, err := s.visual.DiffForAssertionResult(ctx, existing.ID)
	if err != nil {
		return nil, false, mapExecutionError(err, "visual_diff")
	}
	classification := visualClassificationForVerdict(existing.Verdict)
	reason := existing.Reason
	ratio := 0.0
	envMatch := false
	diffID := ""
	actualID := ""
	diffEvidenceID := ""
	if hasDiff {
		classification = diff.Classification
		reason = diff.Reason
		ratio = diff.DiffRatio
		envMatch = diff.EnvironmentMatch
		diffID = platformid.Format(diff.ID)
		actualID = platformid.Format(diff.ActualEvidenceID)
		diffEvidenceID = optionalIDString(diff.DiffEvidenceID)
	}
	return map[string]any{
		"assertion_result_id": platformid.Format(existing.ID),
		"verdict":             string(existing.Verdict),
		"classification":      classification,
		"reason":              reason,
		"diff_ratio":          ratio,
		"environment_match":   envMatch,
		"visual_diff_id":      diffID,
		"actual_evidence_id":  actualID,
		"diff_evidence_id":    diffEvidenceID,
		"replayed":            true,
	}, true, nil
}

func (s *CaseRunner) visualCompareArgs(ctx context.Context, cfg VisualAssertConfig, baseline []byte, hasBaseline bool) (json.RawMessage, error) {
	timeoutSeconds := s.intSetting(settingVisualCompareTimeout, defaultCompareTimeoutSec)
	args := map[string]any{
		"selector":            cfg.Selector,
		"threshold":           cfg.Threshold,
		"viewport":            map[string]any{"width": cfg.ViewportWidth, "height": cfg.ViewportHeight},
		"device_scale_factor": cfg.DeviceScaleFactor,
		"timeout_ms":          timeoutSeconds * 1000,
	}
	if region := strings.TrimSpace(string(cfg.Region)); region != "" && region != "{}" && region != "null" {
		var decoded any
		if err := json.Unmarshal(cfg.Region, &decoded); err != nil {
			return nil, platform.StateConflict("the visual region configuration is invalid")
		}
		args["region"] = decoded
	}
	if wait := strings.TrimSpace(string(cfg.WaitPolicy)); wait != "" && wait != "{}" && wait != "null" {
		var decoded any
		if err := json.Unmarshal(cfg.WaitPolicy, &decoded); err != nil {
			return nil, platform.StateConflict("the visual wait policy configuration is invalid")
		}
		args["wait_policy"] = decoded
	}
	if hasBaseline {
		sum := sha256.Sum256(baseline)
		args["baseline"] = map[string]any{
			"base64": base64.StdEncoding.EncodeToString(baseline),
			"sha256": hex.EncodeToString(sum[:]),
			"bytes":  len(baseline),
		}
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode visual.compare arguments: %w", err)
	}
	return encoded, nil
}

func (s *CaseRunner) readBaselineBytes(ctx context.Context, baseline VisualBaselineRef) ([]byte, error) {
	if s.content == nil || baseline.SHA256 == "" {
		return nil, errors.New("the baseline bytes are not readable")
	}
	data, err := s.content.Open(ctx, baseline.SHA256)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != baseline.SHA256 {
		return nil, errors.New("the baseline bytes do not match the approved sha256")
	}
	if baseline.SizeBytes > 0 && int64(len(data)) != baseline.SizeBytes {
		return nil, errors.New("the baseline bytes do not match the approved size")
	}
	return data, nil
}

func (s *CaseRunner) recordVisualAssertAudit(ctx context.Context, caseRun execution.CaseRun, cfg VisualAssertConfig, result execution.AssertionResult, diffID *int64) {
	if s.audit == nil {
		return
	}
	metadata := map[string]any{
		"case_run_id":         platformid.Format(caseRun.ID),
		"visual_assertion_id": platformid.Format(cfg.ID),
		"config_revision":     cfg.ConfigRevision,
		"verdict":             string(result.Verdict),
		"reason":              result.Reason,
	}
	if diffID != nil {
		metadata["visual_diff_id"] = platformid.Format(*diffID)
	}
	_ = s.audit.Record(ctx, auditEvent("execution.visual_assert", "visual_assertion", cfg.ID, metadata))
}

func visualArtifact(artifacts []BrowserArtifact, kinds ...string) (BrowserArtifact, bool) {
	for _, artifact := range artifacts {
		for _, kind := range kinds {
			if artifact.Kind == kind {
				return artifact, true
			}
		}
	}
	return BrowserArtifact{}, false
}

// decodeVisualArtifact decodes one executor artifact and re-verifies its size
// and sha256: the executor-reported values are never trusted (protocol §4).
func decodeVisualArtifact(artifact BrowserArtifact) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(artifact.Base64)
	if err != nil || len(data) == 0 {
		return nil, errors.New("executor visual artifact is not valid base64")
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if artifact.SHA256 != "" && artifact.SHA256 != sha {
		return nil, errors.New("executor visual artifact sha256 mismatch")
	}
	if artifact.Bytes > 0 && artifact.Bytes != len(data) {
		return nil, errors.New("executor visual artifact size mismatch")
	}
	return data, nil
}

func classifyVisual(hasBaseline, baselineInvalid, environmentMatch bool, diffRatio, threshold float64) (classification, reason string) {
	switch {
	case baselineInvalid:
		return VisualClassificationInconclusive, VisualReasonBaseInvalid
	case !hasBaseline:
		return VisualClassificationInconclusive, VisualReasonMissingBase
	case !environmentMatch:
		return VisualClassificationInconclusive, VisualReasonEnvironment
	case diffRatio > threshold:
		return VisualClassificationFailed, VisualReasonMismatch
	default:
		return VisualClassificationPassed, VisualReasonMatch
	}
}

func visualClassificationForVerdict(verdict execution.AssertionVerdict) string {
	switch verdict {
	case execution.AssertionPassed:
		return VisualClassificationPassed
	case execution.AssertionFailed:
		return VisualClassificationFailed
	default:
		return VisualClassificationInconclusive
	}
}

func visualActualText(payload visualComparePayload, threshold float64) string {
	return fmt.Sprintf("diff_ratio=%.6f threshold=%.5f environment_match=%t width=%d height=%d",
		payload.DiffRatio, threshold, payload.EnvironmentMatch, payload.Width, payload.Height)
}

func reasonForVisualError(err error) string {
	var toolErr *tools.Error
	if errors.As(err, &toolErr) {
		switch toolErr.Code {
		case "timeout", "executor_timeout":
			return "visual_actual_invalid"
		case "context_not_found", "executor_unavailable", "unavailable":
			return "visual_actual_invalid"
		}
	}
	return "visual_actual_invalid"
}

func isEvidenceTooLarge(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Code == "evidence_too_large"
}

func optionalIDString(value *int64) string {
	if value == nil || *value <= 0 {
		return ""
	}
	return platformid.Format(*value)
}
