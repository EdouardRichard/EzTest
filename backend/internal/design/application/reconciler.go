package designapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Reconcile scans candidate versions and repairs missed design rounds, scopes,
// tasks and stale markers. It is called by the agent reconciler, which is
// event-driven (every persisted event wakes a coalescing pass) with a periodic
// scan as the safety net; every operation is idempotent.
func (p *Planner) Reconcile(ctx context.Context) error {
	versionIDs, err := p.repo.ListCandidateVersionIDs(ctx, 100)
	if err != nil {
		return mapDesignError(err, "version")
	}
	var failures []error
	for _, versionID := range versionIDs {
		if err := p.ScanVersion(ctx, versionID); err != nil {
			failures = append(failures, fmt.Errorf("scan design version %d: %w", versionID, err))
			continue
		}
		// Design completion: once the scan observed usable coverage for every
		// activated requirement, the version leaves designing for the case hard
		// gate. The stage machine only acts at designing, so a repeat pass is a
		// no-op.
		if err := p.convergeCompleteDesign(ctx, versionID); err != nil {
			failures = append(failures, fmt.Errorf("converge design version %d: %w", versionID, err))
		}
	}
	return errors.Join(failures...)
}

// convergeCompleteDesign applies the designing -> case_review edge when every
// activated, still-clarified requirement has usable case coverage (a scenario
// and a current draft/confirmed case, no stale current case) AND the shared
// review gate passes (AC coverage complete + every current draft review-complete,
// T9). Missing coverage, an uncovered requirement or a closed review gate leaves
// the version designing; the stage machine is the final guard and only
// transitions a version that is still designing.
func (p *Planner) convergeCompleteDesign(ctx context.Context, versionID int64) error {
	if p == nil || p.converger == nil {
		return nil
	}
	coverage, err := p.repo.ListRequirementCoverage(ctx, versionID)
	if err != nil {
		return mapDesignError(err, "version")
	}
	considered := 0
	for _, item := range coverage {
		if !strings.EqualFold(strings.TrimSpace(item.Requirement.Status), string(version.RequirementClarified)) {
			continue
		}
		considered++
		if NeedsRedesign(item) {
			return nil
		}
	}
	if considered == 0 {
		return nil
	}
	// T9 shared review gate: the SAME predicate the manual stage advance calls
	// (versionapp.DesignReviewGate). NeedsRedesign above keeps the legacy
	// zero-coverage/stale redesign trigger; the gate adds the AC-coverage and
	// judge-review completeness checks. A closed gate (coverage gap / unreviewed
	// drafts) leaves the version designing quietly: converge is best-effort
	// idempotent and the next reconciler pass retries. An UNREADABLE gate also
	// leaves it designing but is logged at WARN (an infra fault is not a verdict).
	// An unwired gate fails closed the same way the manual edge does (T13 wires
	// the real gate).
	if p.reviewGate == nil {
		return nil
	}
	if err := p.reviewGate.ReviewGate(ctx, versionID); err != nil {
		// A typed 409 is the gate's VERDICT (coverage gap / unreviewed drafts):
		// the designed outcome of every pass while the author still works, so it
		// stays quiet. Anything else is an infrastructure fault — a persistent
		// judgment-store outage would otherwise pin the version in designing with
		// no signal at all (constitution VII.6 no silent degradation, VII.7
		// observability is a feature). Log it with the version and the cause;
		// still do not converge, and still return nil so one unreadable version
		// cannot abort the whole reconcile pass.
		if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != http.StatusConflict {
			p.log().WarnContext(ctx, "design converge: review gate unreadable; version stays designing",
				slog.Int64("version_id", versionID),
				slog.String("error", err.Error()),
			)
		}
		return nil
	}
	return p.converger.ConvergeDesign(ctx, versionID)
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
			if err := p.ensureRequirementTasks(ctx, versionID, pending, "pending", latestConversation, round.ID); err != nil {
				return err
			}
			if err := p.ensureCoverageRepairTasks(ctx, versionID, redesign, latestConversation, round.ID); err != nil {
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
			if err := p.ensureRequirementTasks(ctx, versionID, activated, "auto_new", latestConversation, round.ID); err != nil {
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
		if err := p.ensureCoverageRepairTasks(ctx, versionID, redesign, latestConversation, round.ID); err != nil {
			return err
		}
		return p.ensureCriticTasks(ctx, versionID)
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
		if err := p.ensureRequirementTasks(ctx, versionID, pending, "auto_new", conversationID, round.ID); err != nil {
			return err
		}
		if err := p.ensureCriticTasks(ctx, versionID); err != nil {
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
