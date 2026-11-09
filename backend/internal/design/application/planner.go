package designapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// Design authoring work-item priorities: P0 requirements are claimed before P1,
// P2 and P3 so a 67-requirement authoring wave cannot starve the critical
// scope behind FIFO requirement-import order (Task 79). The work queue claims
// by priority DESC then id ASC.
const (
	designPriorityP3 = 0
	designPriorityP2 = 1
	designPriorityP1 = 2
	designPriorityP0 = 3
)

// designRequirementPriority maps a requirement priority to the claim priority
// of its authoring tasks. Unknown/empty priorities keep the default P3 level.
func designRequirementPriority(priority string) int {
	switch strings.ToUpper(strings.TrimSpace(priority)) {
	case "P0":
		return designPriorityP0
	case "P1":
		return designPriorityP1
	case "P2":
		return designPriorityP2
	default:
		return designPriorityP3
	}
}

// Planner deterministically activates design scopes, ensures design tasks and
// serves the version design metrics. It never writes case content.
type Planner struct {
	repo          design.Repository
	ids           platformid.Generator
	clock         clock.Clock
	events        eventingapp.Publisher
	enqueuer      TaskEnqueuer
	teamRoom      TeamRoom
	conversations ConversationCreator
	versions      VersionStarter
	staler        Staler
	converger     DesignConverger
}

// NewPlanner wires the design planner. Sinks and collaborators may be nil in
// unit tests; production assembly injects every dependency.
func NewPlanner(
	repo design.Repository,
	ids platformid.Generator,
	clk clock.Clock,
	events eventingapp.Publisher,
	enqueuer TaskEnqueuer,
	teamRoom TeamRoom,
	conversations ConversationCreator,
	versions VersionStarter,
	staler Staler,
) *Planner {
	if clk == nil {
		clk = clock.System{}
	}
	return &Planner{
		repo: repo, ids: ids, clock: clk, events: events, enqueuer: enqueuer,
		teamRoom: teamRoom, conversations: conversations, versions: versions, staler: staler,
	}
}

// SetDesignConverger wires the designing -> case_review convergence port. It is
// a setter because the composition root builds the version stage machine after
// the design planner; nil disables the edge for degraded assemblies.
func (p *Planner) SetDesignConverger(converger DesignConverger) *Planner {
	if p != nil {
		p.converger = converger
	}
	return p
}

// EnsureDesignConversation resolves (creating when absent) the version's design
// conversation. The dispatcher calls it before creating the design.cases
// stage-entry job, so the persisted work-item payload carries the conversation
// association the job card and cancel/retry write-back need. It is idempotent.
func (p *Planner) EnsureDesignConversation(ctx context.Context, versionID int64) (int64, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return 0, err
	}
	conversationID, err := p.ensureDesignConversation(ctx, versionID)
	if err != nil {
		return 0, mapDesignError(err, "version")
	}
	return conversationID, nil
}

// StartRound creates (or repairs) the manual design round of one conversation.
// It activates every clarified requirement that is not yet in scope and ensures
// the requirement/round tasks. The operation is idempotent per conversation.
func (p *Planner) StartRound(ctx context.Context, versionID, conversationID, actorID int64) (RoundView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return RoundView{}, err
	}
	if err := requirePositiveID("conversation_id", conversationID); err != nil {
		return RoundView{}, err
	}
	var round design.Round
	err := p.repo.WithVersionLock(ctx, versionID, func(ctx context.Context) error {
		existing, err := p.repo.GetRoundByConversation(ctx, conversationID)
		if err != nil {
			return err
		}
		if existing != nil {
			round = *existing
			return nil
		}
		pending, err := p.pendingRequirements(ctx, versionID)
		if err != nil {
			return err
		}
		redesign, err := p.redesignRequirements(ctx, versionID)
		if err != nil {
			return err
		}
		scope := requirementRefsUnion(pending, redesign)
		round, err = p.createRound(ctx, versionID, conversationID, actorID, design.ActivationManual, scope)
		if err != nil {
			return err
		}
		if _, err := p.activateScopes(ctx, versionID, round.ID, pending); err != nil {
			return err
		}
		if err := p.ensureRequirementTasks(ctx, versionID, pending, "pending", conversationID, round.ID); err != nil {
			return err
		}
		if err := p.ensureCoverageRepairTasks(ctx, versionID, redesign, conversationID, round.ID); err != nil {
			return err
		}
		if err := p.ensureCriticAndDataPlanTasks(ctx, versionID); err != nil {
			return err
		}
		p.markRunning(ctx, versionID)
		p.publish(ctx, versionID, "design.round_started", map[string]any{
			"round_id":        platformid.Format(round.ID),
			"conversation_id": platformid.Format(conversationID),
			"activation":      round.Activation,
			"requirement_ids": formatIDList(requirementIDs(scope)),
		})
		if len(pending) > 0 {
			p.publish(ctx, versionID, "design.scope_activated", map[string]any{
				"round_id":        platformid.Format(round.ID),
				"activation":      design.ActivationManual,
				"requirement_ids": formatIDList(requirementIDs(pending)),
			})
		}
		p.postRoundStatus(ctx, versionID, round, len(scope))
		return nil
	})
	if err != nil {
		return RoundView{}, mapDesignError(err, "version")
	}
	roundNo := p.roundNumber(ctx, versionID, round.ID)
	return roundView(round, roundNo), nil
}

// StartStageRound ensures the design round for the requirements confirmed by
// the requirement hard gate (Task 65). It is the deterministic consumer of the
// design.cases stage-entry work item and deliberately does not read the legacy
// auto_design_new_requirements toggle: the stage transition already selected the
// scope. It creates the design conversation when the version has none, creates
// or reuses the conversation's round, activates the confirmed requirements,
// enqueues one case-author task per requirement version (with the conversation
// association) plus the round task, and returns the design conversation id.
//
// The operation is idempotent: a repeated stage entry (event replay or R49
// repair) reuses the same conversation/round and adds no duplicate tasks.
func (p *Planner) StartStageRound(ctx context.Context, versionID int64, requestedIDs []int64) (int64, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return 0, err
	}
	requested := make(map[int64]struct{}, len(requestedIDs))
	for _, requirementID := range requestedIDs {
		if requirementID <= 0 {
			return 0, platform.Invalid("requirement_ids", "must contain only positive decimal IDs")
		}
		requested[requirementID] = struct{}{}
	}

	conversationID, err := p.ensureDesignConversation(ctx, versionID)
	if err != nil {
		return 0, mapDesignError(err, "version")
	}

	err = p.repo.WithVersionLock(ctx, versionID, func(ctx context.Context) error {
		existing, err := p.repo.GetRoundByConversation(ctx, conversationID)
		if err != nil {
			return err
		}
		pending, err := p.pendingRequirements(ctx, versionID)
		if err != nil {
			return err
		}
		if len(requested) > 0 {
			pending = filterRequirementRefs(pending, requested)
		}
		redesign, err := p.redesignRequirements(ctx, versionID)
		if err != nil {
			return err
		}
		if len(requested) > 0 {
			redesign = filterRequirementRefs(redesign, requested)
		}
		scope := requirementRefsUnion(pending, redesign)

		createdRound := false
		var round design.Round
		if existing != nil {
			round = *existing
			mergedIDs := mergeIDs(round.RequirementIDs, requirementIDs(scope))
			mergedVersions := mergeIDs(round.RequirementVersionIDs, requirementVersionIDs(scope))
			if len(mergedIDs) != len(round.RequirementIDs) {
				if err := p.repo.UpdateRoundRequirements(ctx, round.ID, mergedIDs, mergedVersions); err != nil {
					return err
				}
				round.RequirementIDs = mergedIDs
				round.RequirementVersionIDs = mergedVersions
			}
		} else {
			round, err = p.createRound(ctx, versionID, conversationID, 0, design.ActivationAutoNew, scope)
			if err != nil {
				return err
			}
			createdRound = true
		}
		activated, err := p.activateScopes(ctx, versionID, round.ID, scope)
		if err != nil {
			return err
		}
		if err := p.ensureRequirementTasks(ctx, versionID, pending, "requirement_confirmed", conversationID, round.ID); err != nil {
			return err
		}
		if err := p.ensureCoverageRepairTasks(ctx, versionID, redesign, conversationID, round.ID); err != nil {
			return err
		}
		if err := p.ensureCriticAndDataPlanTasks(ctx, versionID); err != nil {
			return err
		}
		p.markRunning(ctx, versionID)
		if createdRound {
			p.publish(ctx, versionID, "design.round_started", map[string]any{
				"round_id":        platformid.Format(round.ID),
				"conversation_id": platformid.Format(conversationID),
				"activation":      round.Activation,
				"requirement_ids": formatIDList(requirementIDs(scope)),
			})
		}
		if len(activated) > 0 {
			p.publish(ctx, versionID, "design.scope_activated", map[string]any{
				"round_id":        platformid.Format(round.ID),
				"activation":      design.ActivationAutoNew,
				"requirement_ids": formatIDList(requirementIDs(activated)),
			})
		}
		if createdRound || len(activated) > 0 {
			p.postRoundStatus(ctx, versionID, round, len(scope))
		}
		return nil
	})
	if err != nil {
		return 0, mapDesignError(err, "version")
	}
	return conversationID, nil
}

// ensureDesignConversation returns the version's latest design conversation,
// creating one when the version has none. It is the conversation the
// design.cases job card and the round's case-author traces are projected into.
func (p *Planner) ensureDesignConversation(ctx context.Context, versionID int64) (int64, error) {
	conversationID, has, err := p.repo.LatestDesignConversation(ctx, versionID)
	if err != nil {
		return 0, err
	}
	if has && conversationID > 0 {
		return conversationID, nil
	}
	if p.conversations == nil {
		return 0, fmt.Errorf("design planner has no conversation creator")
	}
	title := "AI 设计 " + p.clock.Now().Format("2006-01-02 15:04")
	return p.conversations.CreateDesign(ctx, versionID, title)
}

// filterRequirementRefs keeps only the requested requirement IDs and preserves
// the repository order.
func filterRequirementRefs(requirements []design.RequirementRef, wanted map[int64]struct{}) []design.RequirementRef {
	filtered := make([]design.RequirementRef, 0, len(requirements))
	for _, requirement := range requirements {
		if _, ok := wanted[requirement.ID]; !ok {
			continue
		}
		filtered = append(filtered, requirement)
	}
	return filtered
}

// CountPendingDesign returns clarified requirements that are not yet activated.
func (p *Planner) CountPendingDesign(ctx context.Context, versionID int64) (int, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return 0, err
	}
	count, err := p.repo.CountPendingDesign(ctx, versionID)
	if err != nil {
		return 0, mapDesignError(err, "version")
	}
	return count, nil
}

// CaseCounts returns the current (non-superseded) case counts of a version.
func (p *Planner) CaseCounts(ctx context.Context, versionID int64) (CaseCounts, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return CaseCounts{}, err
	}
	counts, err := p.repo.CaseCounts(ctx, versionID)
	if err != nil {
		return CaseCounts{}, mapDesignError(err, "version")
	}
	return CaseCounts{Draft: counts.Draft, Confirmed: counts.Confirmed, Stale: counts.Stale}, nil
}

// RequestGap activates the requirement (if needed) and ensures one design.gap
// task for an execution gap. It is idempotent on the gap dedupe key.
func (p *Planner) RequestGap(ctx context.Context, in GapInput) error {
	if err := requirePositiveID("version_id", in.VersionID); err != nil {
		return err
	}
	if err := requirePositiveID("requirement_id", in.RequirementID); err != nil {
		return err
	}
	if in.CaseID != nil && *in.CaseID <= 0 {
		return platform.Invalid("case_id", "must be a positive decimal ID")
	}
	if in.CaseVersionID != nil && *in.CaseVersionID <= 0 {
		return platform.Invalid("case_version_id", "must be a positive decimal ID")
	}
	if in.RequirementVersionID != nil && *in.RequirementVersionID <= 0 {
		return platform.Invalid("requirement_version_id", "must be a positive decimal ID")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "gap_requested"
	}
	requirement, err := p.repo.RequirementContext(ctx, in.VersionID, in.RequirementID)
	if err != nil {
		return mapDesignError(err, "requirement")
	}

	dedupeSuffix := requirement.CurrentVersionID
	if in.CaseVersionID != nil {
		dedupeSuffix = *in.CaseVersionID
	} else if in.RequirementVersionID != nil {
		dedupeSuffix = *in.RequirementVersionID
	}
	payload := map[string]any{
		"requirement_id":         platformid.Format(in.RequirementID),
		"requirement_version_id": platformid.Format(requirement.CurrentVersionID),
		"reason":                 reason,
	}
	if in.CaseID != nil {
		payload["case_id"] = platformid.Format(*in.CaseID)
	}
	if in.CaseVersionID != nil {
		payload["case_version_id"] = platformid.Format(*in.CaseVersionID)
	}

	err = p.repo.WithVersionLock(ctx, in.VersionID, func(ctx context.Context) error {
		round, err := p.repo.GetLatestRound(ctx, in.VersionID)
		if err != nil {
			return err
		}
		scope := design.Scope{
			ID:            p.ids.Next(),
			VersionID:     in.VersionID,
			RequirementID: in.RequirementID,
			ActivatedAt:   p.clock.Now().UTC(),
			Status:        design.ScopeStatusActive,
			CreatedAt:     p.clock.Now().UTC(),
			UpdatedAt:     p.clock.Now().UTC(),
		}
		if round != nil {
			roundID := round.ID
			scope.ActivatedByRoundID = &roundID
		}
		_, created, err := p.repo.UpsertScope(ctx, scope)
		if err != nil {
			return err
		}
		if p.enqueuer != nil {
			if _, err := p.enqueuer.Ensure(ctx, TaskInput{
				VersionID: in.VersionID,
				Team:      TeamDesign,
				Type:      TaskTypeDesignGap,
				DedupeKey: fmt.Sprintf("gap:%d:%d", in.RequirementID, dedupeSuffix),
				Payload:   payload,
				Priority:  designRequirementPriority(requirement.Priority),
			}); err != nil {
				return err
			}
		}
		if created {
			var roundID int64
			if round != nil {
				roundID = round.ID
			}
			p.publish(ctx, in.VersionID, "design.scope_activated", map[string]any{
				"round_id":        platformid.Format(roundID),
				"activation":      design.ActivationAutoGap,
				"requirement_ids": []string{platformid.Format(in.RequirementID)},
			})
		}
		return nil
	})
	if err != nil {
		return mapDesignError(err, "version")
	}
	return nil
}

// pendingRequirements returns clarified requirements without an active scope.
func (p *Planner) pendingRequirements(ctx context.Context, versionID int64) ([]design.RequirementRef, error) {
	clarified, err := p.repo.ListClarifiedRequirements(ctx, versionID)
	if err != nil {
		return nil, err
	}
	scopes, err := p.repo.ListActiveScopes(ctx, versionID)
	if err != nil {
		return nil, err
	}
	active := make(map[int64]struct{}, len(scopes))
	for _, scope := range scopes {
		active[scope.RequirementID] = struct{}{}
	}
	pending := make([]design.RequirementRef, 0, len(clarified))
	for _, requirement := range clarified {
		if _, ok := active[requirement.ID]; ok {
			continue
		}
		pending = append(pending, requirement)
	}
	return pending, nil
}

// redesignRequirements returns activated requirements whose current requirement
// version has no scenarios, no draft/confirmed case, or a stale current case.
func (p *Planner) redesignRequirements(ctx context.Context, versionID int64) ([]design.RequirementRef, error) {
	coverage, err := p.repo.ListRequirementCoverage(ctx, versionID)
	if err != nil {
		return nil, err
	}
	redesign := make([]design.RequirementRef, 0)
	for _, item := range coverage {
		if NeedsRedesign(item) {
			redesign = append(redesign, item.Requirement)
		}
	}
	return redesign, nil
}

// NeedsRedesign reports whether an activated requirement version lacks usable
// case coverage and needs another design pass.
func NeedsRedesign(coverage design.RequirementCoverage) bool {
	if coverage.ScenarioCount == 0 {
		return true
	}
	if coverage.StaleCount > 0 {
		return true
	}
	return coverage.DraftCount == 0 && coverage.ConfirmedCount == 0
}

// createRound persists one design round and returns it.
func (p *Planner) createRound(ctx context.Context, versionID, conversationID, actorID int64, activation string, scope []design.RequirementRef) (design.Round, error) {
	now := p.clock.Now().UTC()
	round := design.Round{
		ID:                    p.ids.Next(),
		VersionID:             versionID,
		ConversationID:        conversationID,
		Activation:            activation,
		RequirementIDs:        requirementIDs(scope),
		RequirementVersionIDs: requirementVersionIDs(scope),
		Scope:                 json.RawMessage(`{}`),
		Status:                design.RoundStatusActive,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if actorID > 0 {
		round.CreatedBy = &actorID
	}
	return p.repo.CreateRound(ctx, round)
}

// activateScopes upserts one active scope per requirement.
func (p *Planner) activateScopes(ctx context.Context, versionID, roundID int64, requirements []design.RequirementRef) ([]design.RequirementRef, error) {
	activated := make([]design.RequirementRef, 0, len(requirements))
	now := p.clock.Now().UTC()
	for _, requirement := range requirements {
		scope := design.Scope{
			ID:                 p.ids.Next(),
			VersionID:          versionID,
			RequirementID:      requirement.ID,
			ActivatedByRoundID: &roundID,
			ActivatedAt:        now,
			Status:             design.ScopeStatusActive,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if _, created, err := p.repo.UpsertScope(ctx, scope); err != nil {
			return nil, err
		} else if created {
			activated = append(activated, requirement)
		}
	}
	return activated, nil
}

// ensureRequirementTasks enqueues one design.requirement task per requirement
// version with the contract dedupe key. When conversationID/roundID are positive
// the task payload carries the design round association so the case author's
// proposal trace and summary land in the design thread (Task 65).
func (p *Planner) ensureRequirementTasks(ctx context.Context, versionID int64, requirements []design.RequirementRef, reason string, conversationID, roundID int64) error {
	if p.enqueuer == nil {
		return nil
	}
	for _, requirement := range requirements {
		if requirement.CurrentVersionID <= 0 {
			continue
		}
		payload := map[string]any{
			"requirement_id":         platformid.Format(requirement.ID),
			"requirement_version_id": platformid.Format(requirement.CurrentVersionID),
			"reason":                 reason,
		}
		if conversationID > 0 {
			payload["conversation_id"] = platformid.Format(conversationID)
		}
		if roundID > 0 {
			payload["round_id"] = platformid.Format(roundID)
		}
		if _, err := p.enqueuer.Ensure(ctx, TaskInput{
			VersionID: versionID,
			Team:      TeamDesign,
			Type:      TaskTypeDesignRequirement,
			DedupeKey: fmt.Sprintf("design:%d:%d", requirement.ID, requirement.CurrentVersionID),
			Payload:   payload,
			Priority:  designRequirementPriority(requirement.Priority),
		}); err != nil {
			return err
		}
	}
	return nil
}

// ensureCoverageRepairTasks re-triggers the case-author work for requirements
// that are still uncovered (or stale) after their previous design.requirement
// task finished. The port derives a fresh generation key from the base key, so a
// terminal "done but produced no cases" item no longer blocks the next attempt;
// the adapter still refuses to duplicate an in-flight generation and bounds the
// number of repair generations (Task 70).
func (p *Planner) ensureCoverageRepairTasks(ctx context.Context, versionID int64, requirements []design.RequirementRef, conversationID, roundID int64) error {
	if p.enqueuer == nil {
		return nil
	}
	for _, requirement := range requirements {
		if requirement.CurrentVersionID <= 0 {
			continue
		}
		payload := map[string]any{
			"requirement_id":         platformid.Format(requirement.ID),
			"requirement_version_id": platformid.Format(requirement.CurrentVersionID),
			"reason":                 "coverage_repair",
		}
		if conversationID > 0 {
			payload["conversation_id"] = platformid.Format(conversationID)
		}
		if roundID > 0 {
			payload["round_id"] = platformid.Format(roundID)
		}
		if _, err := p.enqueuer.EnsureCoverageRepair(ctx, TaskInput{
			VersionID: versionID,
			Team:      TeamDesign,
			Type:      TaskTypeDesignRequirement,
			DedupeKey: fmt.Sprintf("design:%d:%d", requirement.ID, requirement.CurrentVersionID),
			Payload:   payload,
			Priority:  designRequirementPriority(requirement.Priority),
		}, requirement.CurrentVersionID); err != nil {
			return err
		}
	}
	return nil
}

// ensureCriticAndDataPlanTasks enqueues the per-requirement-version critic and
// data-planner tasks derived from the current coverage.
func (p *Planner) ensureCriticAndDataPlanTasks(ctx context.Context, versionID int64) error {
	if p.enqueuer == nil {
		return nil
	}
	coverage, err := p.repo.ListRequirementCoverage(ctx, versionID)
	if err != nil {
		return err
	}
	for _, item := range coverage {
		if item.Requirement.CurrentVersionID <= 0 {
			continue
		}
		payload := map[string]any{
			"requirement_id":         platformid.Format(item.Requirement.ID),
			"requirement_version_id": platformid.Format(item.Requirement.CurrentVersionID),
		}
		priority := designRequirementPriority(item.Requirement.Priority)
		if item.DraftCount > 0 || item.ConfirmedCount > 0 {
			if _, err := p.enqueuer.Ensure(ctx, TaskInput{
				VersionID: versionID,
				Team:      TeamDesign,
				Type:      TaskTypeDesignCritic,
				DedupeKey: fmt.Sprintf("critic:%d:%d", item.Requirement.ID, item.Requirement.CurrentVersionID),
				Payload:   payload,
				Priority:  priority,
			}); err != nil {
				return err
			}
		}
		if item.DraftCount > 0 {
			if _, err := p.enqueuer.Ensure(ctx, TaskInput{
				VersionID: versionID,
				Team:      TeamDesign,
				Type:      TaskTypeDesignDataPlan,
				DedupeKey: fmt.Sprintf("dataplan:%d:%d", item.Requirement.ID, item.Requirement.CurrentVersionID),
				Payload:   payload,
				Priority:  priority,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// markRunning projects the version to running. Best effort: the reconciler is
// the fallback.
func (p *Planner) markRunning(ctx context.Context, versionID int64) {
	if p.versions != nil {
		_ = p.versions.MarkRunning(ctx, versionID)
	}
}

// postRoundStatus posts a deterministic planner status to the design team room.
func (p *Planner) postRoundStatus(ctx context.Context, versionID int64, round design.Round, count int) {
	if p.teamRoom == nil {
		return
	}
	_ = p.teamRoom.Post(ctx, TeamPost{
		VersionID: versionID,
		Team:      TeamDesign,
		Kind:      "status",
		Content:   fmt.Sprintf("Planner 已开启设计轮次 #%s，激活 %d 条需求", platformid.Format(round.ID), count),
		Refs: []TeamPostRef{
			{Kind: "round", ID: platformid.Format(round.ID)},
		},
	})
}

// roundNumber returns the 1-based ordinal of a round inside its version.
func (p *Planner) roundNumber(ctx context.Context, versionID, roundID int64) int {
	rounds, err := p.repo.ListRounds(ctx, versionID)
	if err != nil {
		return 0
	}
	for index, round := range rounds {
		if round.ID == roundID {
			return index + 1
		}
	}
	return 0
}

// publish writes one version-channel planner event.
func (p *Planner) publish(ctx context.Context, versionID int64, eventType string, payload map[string]any) {
	if p.events == nil {
		return
	}
	payload["version_id"] = platformid.Format(versionID)
	_ = p.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      eventType,
		Scope:     map[string]any{"version_id": platformid.Format(versionID)},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}
