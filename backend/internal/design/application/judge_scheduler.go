package designapp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// SetJudgeScheduler wires the independent-judge scheduling ports: the
// append-only judgment facts (T6 JudgmentRepository) and the settings reader
// that bounds rework rounds (T1 design.max_rework_rounds). It is a setter
// because the composition root builds the judgment service after the design
// planner (T13); until both are wired ensureJudgeTasks is a no-op, so degraded
// assemblies and the legacy planner paths keep working.
func (p *Planner) SetJudgeScheduler(judgments JudgmentRepository, settings SettingsReader) *Planner {
	if p != nil {
		p.judgments = judgments
		p.judgeSettings = settings
	}
	return p
}

// SetReworkBudgetReader wires the optional escalation-budget read (terminal
// design.rework work items of one logical case, spec SC-E4). It is a separate
// setter so every existing SetJudgeScheduler caller keeps compiling unchanged;
// nil keeps the round-derived CountReworks as the only budget source.
func (p *Planner) SetReworkBudgetReader(r ReworkBudgetReader) *Planner {
	if p != nil {
		p.reworkBudget = r
	}
	return p
}

// NeedsACRepair reports whether an activated requirement version needs another
// authoring pass to close an acceptance-criterion coverage gap (spec SC-E4). It
// extends NeedsRedesign: the legacy zero-coverage/stale trigger is preserved,
// and a requirement that already has usable drafts but whose confirmed-union-
// draft coverage does not cover every AC ALSO needs repair. Without this second
// clause a partially covered requirement (draft>0) livelocked: NeedsRedesign
// returned false so no repair was ever requested and the union never completed.
//
// union must be the normalized CoverageUnion output; reqACs is the requirement
// version's current AC id set.
func NeedsACRepair(coverage design.RequirementCoverage, union, reqACs []string) bool {
	if NeedsRedesign(coverage) {
		return true
	}
	return !design.CoverageComplete(union, reqACs)
}

// ensureJudgeTasks schedules the independent judge for every activated
// requirement whose coverage union is complete, triggers an AC-gap repair for
// the rest, and observes judge fail verdicts to drive the rework/escalation
// loop (spec SC-E2/SC-E4). It is a no-op until the judge ports are wired (T13).
//
// Coverage is computed from the CURRENT requirement revision's cases only:
// confirmed cases and current draft cases contribute their covered_acs to the
// union (a single partial proposal batch is incomplete, but the union can be
// complete). The judge is enqueued per requirement version (dedupe
// judge:<req>:<reqver>) with a review scope covering EVERY pending draft; the
// generation budget is bounded per current-draft case-version set by the
// adapter (EnsureJudgeGeneration).
//
// The enqueue is additionally gated on the AUTHORING batch of the requirement
// version (H-F6): no batch or a settled batch releases the judge, an in-flight
// batch defers it, and a LEASE-HOLDING run that stalls past its own attempt
// allowance escalates the pending drafts to a human instead of deferring
// forever. A batch that is merely queued or parked for a human defers without
// ever escalating (see authorBatchAllowsJudge).
func (p *Planner) ensureJudgeTasks(ctx context.Context, versionID int64) error {
	if p.enqueuer == nil || p.judgments == nil || p.judgeSettings == nil {
		return nil
	}
	graph, err := p.repo.ReadGraph(ctx, versionID)
	if err != nil {
		return mapDesignError(err, "version")
	}
	for _, requirement := range graph.Requirements {
		if !strings.EqualFold(strings.TrimSpace(requirement.Requirement.Status), string(version.RequirementClarified)) {
			continue
		}
		if requirement.Requirement.CurrentVersionID <= 0 {
			continue
		}
		coverage, confirmedACs, draftACs, draftCases := judgeCoverage(requirement)
		union := design.CoverageUnion(confirmedACs, draftACs)
		if NeedsACRepair(coverage, union, requirement.Requirement.ACIDs) {
			if err := p.ensureACGapRepairTasks(ctx, versionID, requirement.Requirement); err != nil {
				return err
			}
			continue
		}
		// Observe EVERY current draft case (review fix round 1: no stranding).
		// A draft with no judgment matching its current version content (never
		// judged, or content changed) is pending review; a fail bound to the
		// current version drives the rework/escalation predicate. A draft
		// already judged pass/fail for its current version is NOT re-judged --
		// re-judging it would burn the generation budget into a spurious
		// judge_exhausted escalation (spec SC-E4 content binding).
		drafts := make([]JudgeCaseRef, 0, len(draftCases))
		pending := make([]JudgeCaseRef, 0, len(draftCases))
		for _, item := range draftCases {
			ref := JudgeCaseRef{
				CaseID:        item.Case.ID,
				CaseVersionID: item.Current.ID,
				ContentHash:   item.Current.ContentHash,
			}
			drafts = append(drafts, ref)
			latest, err := p.judgments.LatestJudgment(ctx, item.Case.ID)
			if err != nil {
				return mapDesignError(err, "judgment")
			}
			if !design.JudgmentBoundTo(latest, item.Current.ID) || latest.ContentHash != item.Current.ContentHash {
				pending = append(pending, ref)
				continue
			}
			if latest.Verdict == design.VerdictFail {
				if err := p.handleJudgeFail(ctx, versionID, item.Case.ID); err != nil {
					return err
				}
			}
		}
		// Coverage complete and at least one current draft awaits review:
		// schedule ONE judge run for the requirement version whose review scope
		// covers EVERY pending draft (per-case verdicts via design.judgment_submit).
		// The generation budget is anchored on the FULL current-draft case-version
		// set, so any new revision or draft-set change resets it (spec SC-E4 v1.2).
		if len(pending) > 0 {
			// H-F6 author-batch gate: the judge reviews the requirement's current
			// draft SET, so it must not run while the authoring batch that
			// produces that set still owes work. No batch / settled batch releases
			// the judge; a stalled batch escalates the pending drafts instead of
			// deferring forever (spec SC-E4, see authorBatchAllowsJudge).
			scope := RequirementJudgeScope{
				RequirementID:        requirement.Requirement.ID,
				RequirementVersionID: requirement.Requirement.CurrentVersionID,
			}
			allowed, err := p.authorBatchAllowsJudge(ctx, versionID, scope, pending)
			if err != nil {
				return err
			}
			if !allowed {
				continue
			}
			sortJudgeRefs(drafts)
			sortJudgeRefs(pending)
			cases := make([]map[string]any, 0, len(pending))
			for _, ref := range pending {
				cases = append(cases, map[string]any{
					"case_id":         platformid.Format(ref.CaseID),
					"case_version_id": platformid.Format(ref.CaseVersionID),
					"content_hash":    ref.ContentHash,
				})
			}
			payload := map[string]any{
				"requirement_id":         platformid.Format(requirement.Requirement.ID),
				"requirement_version_id": platformid.Format(requirement.Requirement.CurrentVersionID),
				"draft_case_version_ids": JudgeDraftSetKey(drafts),
				"cases":                  cases,
			}
			if _, err := p.enqueuer.EnsureJudgeGeneration(ctx, TaskInput{
				VersionID: versionID,
				Team:      TeamDesign,
				Type:      TaskTypeDesignJudge,
				DedupeKey: fmt.Sprintf("judge:%d:%d", requirement.Requirement.ID, requirement.Requirement.CurrentVersionID),
				Payload:   payload,
				Priority:  designRequirementPriority(requirement.Requirement.Priority),
			}, drafts, pending); err != nil {
				return err
			}
		}
	}
	return nil
}

// JudgeDraftSetKey is THE single authority for the judge generation anchor: the
// draft set's case_version_ids sorted numerically ascending, deduplicated and
// comma-joined. The planner writes it into the judge payload
// (draft_case_version_ids) and the composition-root adapter derives the same
// key from the current drafts to match stored generations, so the writer and
// the matcher cannot drift apart.
func JudgeDraftSetKey(refs []JudgeCaseRef) string {
	ids := make([]int64, 0, len(refs))
	seen := make(map[int64]struct{}, len(refs))
	for _, ref := range refs {
		if ref.CaseVersionID <= 0 {
			continue
		}
		if _, duplicate := seen[ref.CaseVersionID]; duplicate {
			continue
		}
		seen[ref.CaseVersionID] = struct{}{}
		ids = append(ids, ref.CaseVersionID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, platformid.Format(id))
	}
	return strings.Join(parts, ",")
}

// sortJudgeRefs orders refs by case_version_id ascending so the judge payload
// and generation anchor are deterministic regardless of graph read order.
func sortJudgeRefs(refs []JudgeCaseRef) {
	sort.Slice(refs, func(i, j int) bool { return refs[i].CaseVersionID < refs[j].CaseVersionID })
}

// handleJudgeFail applies the rework predicate to one logical case whose latest
// judge fact is a fail bound to its current version (spec SC-E4). It escalates
// (enqueues nothing) when the escalation budget is spent —
// max(round-derived CountReworks, terminal design.rework work items) against
// design.max_rework_rounds — OR when the rework responding to this failed version
// is TERMINAL (done/dead/failed-at-cap): the request key
// rework:<case>:<failedVersion> has no generation and a terminal row is never
// revived, so no further rework could ever be created for it. A rework that
// actually revised the case advanced the current version, so the binding guard
// above already returned and the normal loop is unaffected. Round increments
// ONLY on rework, so a harmless host edit judged at the same round never consumes
// budget. While another rework is possible it enqueues one design.rework carrying
// the next judgment round and a findings summary; once escalated the case's
// review_state derives escalated_fail (T6 ReviewStateFor), the human decision
// point in case_review. No extra fact is written for the escalation: it is a
// projection of fail + reworkExhausted.
func (p *Planner) handleJudgeFail(ctx context.Context, versionID, caseID int64) error {
	if p.enqueuer == nil || p.judgments == nil || p.judgeSettings == nil {
		return nil
	}
	data, err := p.repo.ReadCase(ctx, versionID, caseID, nil)
	if err != nil {
		return mapDesignError(err, "case")
	}
	current := data.Selected
	if current.ID == 0 {
		return nil
	}
	latest, err := p.judgments.LatestJudgment(ctx, caseID)
	if err != nil {
		return mapDesignError(err, "judgment")
	}
	if !design.JudgmentBoundTo(latest, current.ID) ||
		latest.ContentHash != current.ContentHash || latest.Verdict != design.VerdictFail {
		return nil
	}
	count, err := p.judgments.CountReworks(ctx, caseID)
	if err != nil {
		return mapDesignError(err, "judgment")
	}
	// Escalation budget (spec SC-E4): a rework that died at its attempt cap is
	// terminal but produced no new judgment round, so the round-derived count
	// alone keeps asking for the same dedupe key — Ensure never revives a dead
	// row, so nothing is created and the case strands in reworking forever.
	var terminalRework bool
	if p.reworkBudget != nil {
		terminal, err := p.reworkBudget.TerminalReworkCount(ctx, versionID, caseID)
		if err != nil {
			return mapDesignError(err, "rework budget")
		}
		if terminal > count {
			count = terminal
		}
		// The rework request key below has NO generation, so a TERMINAL rework
		// bound to this failed version blocks every future rework for it — even
		// with budget left. Terminal covers a rework that died AND one that
		// completed without proposing a revision; a rework that DID revise
		// advances the current version, so the guard above already returned.
		terminalRework, err = p.reworkBudget.HasTerminalReworkForVersion(ctx, versionID, caseID, current.ID)
		if err != nil {
			return mapDesignError(err, "rework budget")
		}
	}
	maxRounds, err := p.maxReworkRounds(ctx)
	if err != nil {
		return err
	}
	if !design.ReworkAllowed(count, maxRounds) || terminalRework {
		// Budget exhausted, or the rework responding to this fail can never run
		// again: escalate to a human. review_state derives escalated_fail from
		// the fail fact + the same predicate; nothing to write.
		return nil
	}
	nextRound := latest.Round + 1
	payload := map[string]any{
		"requirement_id":         platformid.Format(data.Requirement.ID),
		"requirement_version_id": platformid.Format(data.Requirement.CurrentVersionID),
		"case_id":                platformid.Format(caseID),
		"case_version_id":        platformid.Format(current.ID),
		"round":                  nextRound,
		"findings":               judgmentFindingSummary(latest.Findings),
	}
	created, err := p.enqueuer.Ensure(ctx, TaskInput{
		VersionID: versionID,
		Team:      TeamDesign,
		Type:      TaskTypeDesignRework,
		DedupeKey: fmt.Sprintf("rework:%d:%d", caseID, current.ID),
		Payload:   payload,
		Priority:  designRequirementPriority(data.Requirement.Priority),
	})
	if err != nil {
		return err
	}
	// Announce ONLY the creation transition. The fail-within-budget condition
	// persists across every reconcile pass until the author actually revises the
	// case, and every persisted event wakes a full reconcile pass; re-publishing
	// per pass would be a self-sustaining event/SSE loop. The work item is the
	// durable request either way.
	if !created {
		return nil
	}
	p.publish(ctx, versionID, "design.rework_requested", map[string]any{
		"requirement_id":  platformid.Format(data.Requirement.ID),
		"case_id":         platformid.Format(caseID),
		"case_version_id": platformid.Format(current.ID),
		"round":           nextRound,
	})
	return nil
}

// ensureACGapRepairTasks re-triggers the case-author work for a requirement
// whose coverage union is incomplete (spec SC-E4 AC-level gap repair). It rides
// the same bounded generation mechanism as ensureCoverageRepairTasks but tags
// the payload reason "ac_gap" so the repair is attributable to a coverage gap
// rather than a zero-coverage stall.
//
// H-F5b: the created flag is deliberately not consumed here. The port owns the
// whole outcome, including the STRANDED case — when the authoring generation
// budget of this requirement revision is spent it appends the author_exhausted
// escalation fact (designapp.AuthorExhaustedStore) instead of silently reporting
// "nothing created", so the requirement is escalated to a human whether or not a
// work item was enqueued. Duplicating that decision here would be a second
// authority for one invariant.
func (p *Planner) ensureACGapRepairTasks(ctx context.Context, versionID int64, requirement design.RequirementRef) error {
	if p.enqueuer == nil || requirement.CurrentVersionID <= 0 {
		return nil
	}
	payload := map[string]any{
		"requirement_id":         platformid.Format(requirement.ID),
		"requirement_version_id": platformid.Format(requirement.CurrentVersionID),
		"reason":                 "ac_gap",
	}
	_, err := p.enqueuer.EnsureCoverageRepair(ctx, TaskInput{
		VersionID: versionID,
		Team:      TeamDesign,
		Type:      TaskTypeDesignRequirement,
		DedupeKey: fmt.Sprintf("design:%d:%d", requirement.ID, requirement.CurrentVersionID),
		Payload:   payload,
		Priority:  designRequirementPriority(requirement.Priority),
	}, requirement.CurrentVersionID)
	return err
}

// maxReworkRounds reads the configured rework bound through the settings port.
// An unwired or negative value fails closed (constitution VII.6); it never
// silently assumes a budget. It mirrors JudgmentService.maxReworkRounds so the
// scheduler and the judgment service agree on the bound.
func (p *Planner) maxReworkRounds(ctx context.Context) (int, error) {
	if p.judgeSettings == nil {
		return 0, platform.Unavailable("the settings reader is not wired for judge scheduling")
	}
	value, err := p.judgeSettings.Int(ctx, maxReworkRoundsKey)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, platform.Unavailable(maxReworkRoundsKey + " must not be negative")
	}
	return int(value), nil
}

// judgeCoverage projects one requirement graph node into the planner coverage
// counts plus the per-case covered_acs sets (split by lifecycle status) and the
// current draft cases. Only scenarios bound to the requirement's CURRENT
// revision contribute, matching the confirmation gate's selection universe.
func judgeCoverage(requirement design.RequirementGraph) (coverage design.RequirementCoverage, confirmedACs, draftACs [][]string, draftCases []design.CaseGraph) {
	coverage = design.RequirementCoverage{Requirement: requirement.Requirement}
	for _, scenario := range requirement.Scenarios {
		if scenario.Scenario.RequirementVersionID != requirement.Requirement.CurrentVersionID {
			continue
		}
		coverage.ScenarioCount++
		for _, item := range scenario.Cases {
			switch item.Current.Status {
			case design.CaseStatusConfirmed:
				coverage.ConfirmedCount++
				confirmedACs = append(confirmedACs, item.Current.CoveredACs)
			case design.CaseStatusDraft:
				coverage.DraftCount++
				draftACs = append(draftACs, item.Current.CoveredACs)
				draftCases = append(draftCases, item)
			case design.CaseStatusStale:
				coverage.StaleCount++
			}
		}
	}
	return coverage, confirmedACs, draftACs, draftCases
}
