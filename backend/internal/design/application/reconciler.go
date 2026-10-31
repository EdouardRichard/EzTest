package designapp

import (
	"context"
	"errors"
	"fmt"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Reconcile scans candidate versions and repairs missed design rounds, scopes,
// tasks and stale markers. It is called by the agent reconciler on its P1 3s
// cycle; every operation is idempotent.
func (p *Planner) Reconcile(ctx context.Context) error {
	versionIDs, err := p.repo.ListCandidateVersionIDs(ctx, 100)
	if err != nil {
		return mapDesignError(err, "version")
	}
	var failures []error
	for _, versionID := range versionIDs {
		if err := p.ScanVersion(ctx, versionID); err != nil {
			failures = append(failures, fmt.Errorf("scan design version %d: %w", versionID, err))
		}
	}
	return errors.Join(failures...)
}

// ScanVersion applies the deterministic design reconciler rules of spec §9.1:
// stale safety net, D25 auto conversation / auto join, redesign tasks, and
// critic/data-planner task derivation.
func (p *Planner) ScanVersion(ctx context.Context, versionID int64) error {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return err
	}
	if err := p.applyStaleSafetyNet(ctx, versionID); err != nil {
		return err
	}

	latestConversation, hasConversation, err := p.repo.LatestDesignConversation(ctx, versionID)
	if err != nil {
		return mapDesignError(err, "version")
	}
	if !hasConversation {
		return p.autoStartIfEnabled(ctx, versionID)
	}

	return p.repo.WithVersionLock(ctx, versionID, func(ctx context.Context) error {
		round, err := p.repo.GetRoundByConversation(ctx, latestConversation)
		if err != nil {
			return err
		}
		pending, err := p.pendingRequirements(ctx, versionID)
		if err != nil {
			return err
		}
		if round == nil {
			// Repair a conversation whose round creation failed.
			redesign, err := p.redesignRequirements(ctx, versionID)
			if err != nil {
				return err
			}
			scope := requirementRefsUnion(pending, redesign)
			repaired, err := p.createRound(ctx, versionID, latestConversation, 0, design.ActivationManual, scope)
			if err != nil {
				return err
			}
			round = &repaired
			if _, err := p.activateScopes(ctx, versionID, round.ID, pending); err != nil {
				return err
			}
			if err := p.ensureRequirementTasks(ctx, versionID, pending, "pending"); err != nil {
				return err
			}
			if err := p.ensureRequirementTasks(ctx, versionID, redesign, "requirement_changed"); err != nil {
				return err
			}
			if err := p.ensureRoundTask(ctx, versionID, round.ID, latestConversation); err != nil {
				return err
			}
			p.markRunning(ctx, versionID)
			p.publish(ctx, versionID, "design.round_started", map[string]any{
				"round_id":        platformid.Format(round.ID),
				"conversation_id": platformid.Format(latestConversation),
				"activation":      round.Activation,
				"requirement_ids": formatIDList(requirementIDs(scope)),
			})
			return nil
		}

		activated, err := p.activateScopes(ctx, versionID, round.ID, pending)
		if err != nil {
			return err
		}
		if len(activated) > 0 {
			mergedIDs := mergeIDs(round.RequirementIDs, requirementIDs(activated))
			mergedVersions := mergeIDs(round.RequirementVersionIDs, requirementVersionIDs(activated))
			if err := p.repo.UpdateRoundRequirements(ctx, round.ID, mergedIDs, mergedVersions); err != nil {
				return err
			}
			if err := p.ensureRequirementTasks(ctx, versionID, activated, "auto_new"); err != nil {
				return err
			}
			p.publish(ctx, versionID, "design.scope_activated", map[string]any{
				"round_id":        platformid.Format(round.ID),
				"activation":      design.ActivationAutoNew,
				"requirement_ids": formatIDList(requirementIDs(activated)),
			})
		}
		redesign, err := p.redesignRequirements(ctx, versionID)
		if err != nil {
			return err
		}
		if err := p.ensureRequirementTasks(ctx, versionID, redesign, "requirement_changed"); err != nil {
			return err
		}
		if err := p.ensureRoundTask(ctx, versionID, round.ID, latestConversation); err != nil {
			return err
		}
		return p.ensureCriticAndDataPlanTasks(ctx, versionID)
	})
}

// applyStaleSafetyNet marks confirmed current cases stale when the requirement
// revision moved without the version hook running.
func (p *Planner) applyStaleSafetyNet(ctx context.Context, versionID int64) error {
	outdated, err := p.repo.ListOutdatedConfirmedRequirements(ctx, 100)
	if err != nil {
		return mapDesignError(err, "version")
	}
	if p.staler == nil {
		return nil
	}
	for _, item := range outdated {
		if item.VersionID != versionID {
			continue
		}
		if _, err := p.staler.MarkRequirementChanged(ctx, versionID, item.RequirementID, "requirement_changed", 0); err != nil {
			return err
		}
	}
	return nil
}

// autoStartIfEnabled implements D25: when a version has no design conversation
// at all and the auto flag is on, create one conversation covering every
// pending clarified requirement. Runs at most once per version.
func (p *Planner) autoStartIfEnabled(ctx context.Context, versionID int64) error {
	pending, err := p.pendingRequirements(ctx, versionID)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	autoEnabled, err := p.repo.AutoDesignEnabled(ctx, versionID)
	if err != nil {
		return mapDesignError(err, "version")
	}
	if !autoEnabled || p.conversations == nil {
		return nil
	}
	title := "AI 设计 " + p.clock.Now().Format("2006-01-02 15:04")
	conversationID, err := p.conversations.CreateDesign(ctx, versionID, title)
	if err != nil {
		return fmt.Errorf("auto-create design conversation: %w", err)
	}
	var round design.Round
	err = p.repo.WithVersionLock(ctx, versionID, func(ctx context.Context) error {
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
		round, err = p.createRound(ctx, versionID, conversationID, 0, design.ActivationAutoNew, pending)
		if err != nil {
			return err
		}
		if _, err := p.activateScopes(ctx, versionID, round.ID, pending); err != nil {
			return err
		}
		if err := p.ensureRequirementTasks(ctx, versionID, pending, "auto_new"); err != nil {
			return err
		}
		if err := p.ensureRoundTask(ctx, versionID, round.ID, conversationID); err != nil {
			return err
		}
		if err := p.ensureCriticAndDataPlanTasks(ctx, versionID); err != nil {
			return err
		}
		p.markRunning(ctx, versionID)
		p.publish(ctx, versionID, "design.round_started", map[string]any{
			"round_id":        platformid.Format(round.ID),
			"conversation_id": platformid.Format(conversationID),
			"activation":      design.ActivationAutoNew,
			"requirement_ids": formatIDList(requirementIDs(pending)),
		})
		if len(pending) > 0 {
			p.publish(ctx, versionID, "design.scope_activated", map[string]any{
				"round_id":        platformid.Format(round.ID),
				"activation":      design.ActivationAutoNew,
				"requirement_ids": formatIDList(requirementIDs(pending)),
			})
		}
		p.postRoundStatus(ctx, versionID, round, len(pending))
		return nil
	})
	return err
}

// mergeIDs appends values not already present, preserving order.
func mergeIDs(existing, additions []int64) []int64 {
	seen := make(map[int64]struct{}, len(existing)+len(additions))
	merged := make([]int64, 0, len(existing)+len(additions))
	for _, group := range [][]int64{existing, additions} {
		for _, id := range group {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
	}
	return merged
}
