package executionapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// coverage reasons of the P3 deterministic fallback (P3 §8.1 step 3).
const (
	GapReasonExecutionFailedCoverage = "execution_failed_coverage"
	GapReasonExecutionNotRun         = "execution_not_run"
)

// autoGapDesign reads feature.auto_gap_design, defaulting to true (P3验收默认
// true). A missing setting (older deployment) keeps the fallback on.
func (s *AnalysisService) autoGapDesign(ctx context.Context) bool {
	if s.settings == nil {
		return true
	}
	enabled, err := s.settings.Bool(ctx, "feature.auto_gap_design")
	if err != nil {
		return true
	}
	return enabled
}

// analyzeCoverage computes the execution coverage gaps of one wave: for every
// current clarified/closed requirement, a current+confirmed case version with
// no passing latest case_run is a gap (P3 §8.1 step 3).
func (s *AnalysisService) analyzeCoverage(ctx context.Context, wave execution.RunWave, _ []execution.CaseRun) error {
	if s.requirements == nil || s.cases == nil {
		return nil
	}
	requirements, err := s.requirements.ListRequirements(ctx, wave.VersionID)
	if err != nil {
		return mapExecutionError(err, "requirement")
	}
	if len(requirements) == 0 {
		return nil
	}
	cases, err := s.cases.ListExecutable(ctx, wave.VersionID)
	if err != nil {
		return mapExecutionError(err, "case version")
	}
	caseVersionIDs := make([]int64, 0, len(cases))
	for _, item := range cases {
		caseVersionIDs = append(caseVersionIDs, item.CaseVersionID)
	}
	latest := map[int64]execution.CaseRun{}
	if s.caseRuns != nil && len(caseVersionIDs) > 0 {
		latest, err = s.caseRuns.LatestByCaseVersions(ctx, wave.VersionID, caseVersionIDs)
		if err != nil {
			return mapExecutionError(err, "case run")
		}
	}
	enabled := s.autoGapDesign(ctx)
	for _, requirement := range requirements {
		matched := make([]execution.CaseSnapshot, 0)
		for _, item := range cases {
			if item.RequirementID == requirement.RequirementID &&
				item.RequirementVersionID == requirement.RequirementVersionID {
				matched = append(matched, item)
			}
		}
		if len(matched) == 0 {
			// No current+confirmed case version: P2 design coverage owns it.
			continue
		}
		covered := false
		type coverageGap struct {
			caseVersionID int64
			reason        string
		}
		gaps := make([]coverageGap, 0, len(matched))
		for _, item := range matched {
			run, ok := latest[item.CaseVersionID]
			switch {
			case !ok:
				gaps = append(gaps, coverageGap{caseVersionID: item.CaseVersionID, reason: GapReasonExecutionNotRun})
			case run.Status == execution.CaseRunPassed:
				covered = true
			case run.Status == execution.CaseRunSuperseded:
				gaps = append(gaps, coverageGap{caseVersionID: item.CaseVersionID, reason: GapReasonExecutionNotRun})
			default:
				gaps = append(gaps, coverageGap{caseVersionID: item.CaseVersionID, reason: GapReasonExecutionFailedCoverage})
			}
		}
		if covered {
			continue
		}
		for _, gap := range gaps {
			s.emitCoverageGap(ctx, wave.VersionID, wave.ID, requirement.RequirementID, gap.caseVersionID, gap.reason, enabled)
		}
	}
	return nil
}

// emitCoverageGap publishes execution.coverage_gap, writes a team-room blocker
// and ensures the P2 design.gap work item when the feature flag is on. The
// in-process dedupe plus the gap adapter dedupe keep repeated analysis
// idempotent.
func (s *AnalysisService) emitCoverageGap(ctx context.Context, versionID, waveID, requirementID, caseVersionID int64, reason string, autoGap bool) {
	key := fmt.Sprintf("%d|%d|%d|%d|%s", versionID, waveID, requirementID, caseVersionID, reason)
	s.mu.Lock()
	if _, seen := s.coverageKeys[key]; seen {
		s.mu.Unlock()
		return
	}
	s.coverageKeys[key] = struct{}{}
	s.mu.Unlock()

	scope := map[string]any{
		"wave_id":        platformid.Format(waveID),
		"requirement_id": platformid.Format(requirementID),
	}
	payload := map[string]any{
		"wave_id":        platformid.Format(waveID),
		"requirement_id": platformid.Format(requirementID),
		"reason":         reason,
	}
	if caseVersionID > 0 {
		payload["case_version_id"] = platformid.Format(caseVersionID)
	}
	publishEvent(ctx, s.events, EventCoverageGap, versionID, scope, payload, nil)
	s.post(ctx, TeamPost{
		VersionID: versionID,
		Team:      TeamExecution,
		Kind:      "blocker",
		Content: fmt.Sprintf("execution coverage gap: requirement %s has no passing case (%s)",
			platformid.Format(requirementID), reason),
		Refs: []TeamPostRef{
			{Kind: "requirement", ID: platformid.Format(requirementID)},
			{Kind: "wave", ID: platformid.Format(waveID)},
		},
	})
	if autoGap && s.gaps != nil && caseVersionID > 0 {
		_ = s.gaps.EnsureGap(ctx, versionID, requirementID, caseVersionID, reason)
	}
}

// CoverageSubmit is the analysis.coverage_submit entry point (ExecutionPort).
func (s *AnalysisService) CoverageSubmit(ctx context.Context, scope tools.Scope, in tools.CoverageSubmitInput) (any, error) {
	return s.SubmitCoverage(ctx, scope, in)
}

// SubmitCoverage validates and records one analyst coverage gap (P3 §8.3). The
// requirement must belong to the task version and every evidence case_run must
// belong to the task wave.
func (s *AnalysisService) SubmitCoverage(ctx context.Context, scope tools.Scope, in tools.CoverageSubmitInput) (any, error) {
	if err := requirePositiveID("requirement_id", in.RequirementID); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, platform.Invalid("reason", "must not be empty")
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	if scope.VersionID <= 0 {
		return nil, platform.Invalid("version_id", "execution scope is missing the version")
	}
	requirements, err := s.requirements.ListRequirements(ctx, scope.VersionID)
	if err != nil {
		return nil, mapExecutionError(err, "requirement")
	}
	found := false
	for _, requirement := range requirements {
		if requirement.RequirementID == in.RequirementID {
			found = true
			break
		}
	}
	if !found {
		return nil, platform.NotFound("requirement")
	}
	caseVersionID := int64(0)
	for _, caseRunID := range in.EvidenceCaseRunIDs {
		run, err := s.caseRuns.Get(ctx, caseRunID)
		if err != nil {
			return nil, mapExecutionError(err, "case run")
		}
		if scope.WaveID > 0 && run.WaveID != scope.WaveID {
			return nil, platform.NotFound("case run")
		}
		if caseVersionID == 0 {
			caseVersionID = run.CaseVersionID
		}
	}
	s.emitCoverageGap(ctx, scope.VersionID, scope.WaveID, in.RequirementID, caseVersionID, reason, s.autoGapDesign(ctx))
	return map[string]any{
		"requirement_id": platformid.Format(in.RequirementID),
		"reason":         reason,
		"status":         "recorded",
	}, nil
}
