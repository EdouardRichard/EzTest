// Package visualadapter adapts the execution context to the P5 visual context
// and vice versa: VisualPort implements executionapp.VisualAssertPort over the
// visual application services, and EvidenceReader implements
// visualapp.EvidenceReader over the execution repositories/content ledger.
package visualadapter

import (
	"context"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// VisualPort maps the execution visual-assert port onto the visual services.
type VisualPort struct {
	Configs   *visualapp.ConfigService
	Baselines *visualapp.BaselineService
	Diffs     *visualapp.DiffService
}

var _ executionapp.VisualAssertPort = (*VisualPort)(nil)

// Config returns the visual assertion configuration of one case_run scope.
func (p *VisualPort) Config(ctx context.Context, versionID, assertionID int64) (executionapp.VisualAssertConfig, error) {
	if p.Configs == nil {
		return executionapp.VisualAssertConfig{}, platform.Unavailable("visual configuration service is not configured")
	}
	assertion, err := p.Configs.Get(ctx, assertionID)
	if err != nil {
		return executionapp.VisualAssertConfig{}, err
	}
	if assertion.VersionID != versionID {
		return executionapp.VisualAssertConfig{}, platform.NotFound("visual_assertion")
	}
	return executionapp.VisualAssertConfig{
		ID: assertion.ID, VersionID: assertion.VersionID, CaseVersionID: assertion.CaseVersionID,
		CaseAssertionID: assertion.CaseAssertionID, StepSeq: assertion.StepSeq,
		AssertionSeq: assertion.AssertionSeq, VersionSUTID: assertion.VersionSUTID,
		Selector: assertion.Selector, Region: assertion.Region, Threshold: assertion.Threshold,
		ViewportWidth: assertion.ViewportWidth, ViewportHeight: assertion.ViewportHeight,
		DeviceScaleFactor: assertion.DeviceScaleFactor, WaitPolicy: assertion.WaitPolicy,
		ConfigRevision: assertion.ConfigRevision,
	}, nil
}

// ActiveBaseline returns the approved baseline of one assertion.
func (p *VisualPort) ActiveBaseline(ctx context.Context, assertionID int64) (executionapp.VisualBaselineRef, bool, error) {
	if p.Baselines == nil {
		return executionapp.VisualBaselineRef{}, false, platform.Unavailable("visual baseline service is not configured")
	}
	baseline, ok, err := p.Baselines.Active(ctx, assertionID)
	if err != nil || !ok {
		return executionapp.VisualBaselineRef{}, ok, err
	}
	return executionapp.VisualBaselineRef{
		ID: baseline.ID, EvidenceID: baseline.EvidenceID, SHA256: baseline.SHA256,
		SizeBytes: baseline.SizeBytes, Width: baseline.Width, Height: baseline.Height,
		ConfigRevision: baseline.ConfigRevision, EnvironmentFingerprint: baseline.EnvironmentFingerprint,
	}, true, nil
}

// RecordDiff persists one comparison with the visual four-state classification.
func (p *VisualPort) RecordDiff(ctx context.Context, in executionapp.VisualDiffInput) (executionapp.VisualDiffRecord, error) {
	if p.Diffs == nil {
		return executionapp.VisualDiffRecord{}, platform.Unavailable("visual diff service is not configured")
	}
	diff, err := p.Diffs.Record(ctx, visual.RecordDiffInput{
		VersionID: in.VersionID, VisualAssertionID: in.VisualAssertionID, CaseRunID: in.CaseRunID,
		AssertionResultID: in.AssertionResultID, BaselineID: in.BaselineID,
		ActualEvidenceID: in.ActualEvidenceID, DiffEvidenceID: in.DiffEvidenceID,
		DiffRatio: in.DiffRatio, Threshold: in.Threshold,
		Classification: visual.Classification(in.Classification), Reason: in.Reason,
		EnvironmentMatch: in.EnvironmentMatch, Width: in.Width, Height: in.Height,
		MismatchBoxes: in.MismatchBoxes,
	})
	if err != nil {
		return executionapp.VisualDiffRecord{}, err
	}
	return executionapp.VisualDiffRecord{
		ID: diff.ID, AssertionResultID: diff.AssertionResultID,
		VisualAssertionID: diff.VisualAssertionID, BaselineID: diff.BaselineID,
		ActualEvidenceID: diff.ActualEvidenceID, DiffEvidenceID: diff.DiffEvidenceID,
		DiffRatio: diff.DiffRatio, Threshold: diff.Threshold,
		Classification: string(diff.Classification), Reason: diff.Reason,
		EnvironmentMatch: diff.EnvironmentMatch, Width: diff.Width, Height: diff.Height,
		MismatchBoxes: diff.MismatchBoxes,
	}, nil
}

// DiffForAssertionResult returns the recorded comparison of one assertion
// result (idempotent replay).
func (p *VisualPort) DiffForAssertionResult(ctx context.Context, assertionResultID int64) (executionapp.VisualDiffRecord, bool, error) {
	if p.Diffs == nil {
		return executionapp.VisualDiffRecord{}, false, nil
	}
	diff, ok, err := p.Diffs.ByAssertionResult(ctx, assertionResultID)
	if err != nil || !ok {
		return executionapp.VisualDiffRecord{}, ok, err
	}
	return executionapp.VisualDiffRecord{
		ID: diff.ID, AssertionResultID: diff.AssertionResultID,
		VisualAssertionID: diff.VisualAssertionID, BaselineID: diff.BaselineID,
		ActualEvidenceID: diff.ActualEvidenceID, DiffEvidenceID: diff.DiffEvidenceID,
		DiffRatio: diff.DiffRatio, Threshold: diff.Threshold,
		Classification: string(diff.Classification), Reason: diff.Reason,
		EnvironmentMatch: diff.EnvironmentMatch, Width: diff.Width, Height: diff.Height,
		MismatchBoxes: diff.MismatchBoxes,
	}, true, nil
}
