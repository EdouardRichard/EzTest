package designapp

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// AuthorBatchState is the settle state of the CASE-AUTHORING batch
// (design.requirement work items) of ONE requirement version. It is the fact the
// judge scheduling predicate consumes (spec SC-E4, H-F6): the judge reviews the
// requirement's current draft set, so it must not run while the authoring batch
// that produces that set still owes work.
//
// The three values are exhaustive and mutually exclusive:
//   - NoBatch: the requirement version has NO authoring work item at all. This
//     is reachable — coverage can be completed by confirmed cases plus drafts
//     authored by another path (host proposal, discovery revision), and an
//     authoring item of an OLDER requirement revision is not this version's.
//     It must release the judge, never block it.
//   - AllTerminal: authoring work items exist and every one is terminal (done,
//     dead, or failed at its attempt cap). The batch is settled.
//   - InFlight: at least one authoring work item still owes work
//     (open/claimed/running/waiting_human, or a failure below its attempt cap
//     that the queue will retry).
type AuthorBatchState string

// Author batch settle states (single authority; the composition-root adapter
// answers with these values and the planner predicate switches on them).
const (
	AuthorBatchNoBatch     AuthorBatchState = "no_batch"
	AuthorBatchAllTerminal AuthorBatchState = "all_terminal"
	AuthorBatchInFlight    AuthorBatchState = "in_flight"
)

// AuthorBatchStallWindowPerAttempt is the no-progress allowance ONE attempt of
// the authoring batch gets before a lease-holding row counts as stalled. The
// scheduler multiplies it by the row's OWN max_attempts (which the work-item
// fact carries: the queue default is 3, the domain bound is 10 and the DB CHECK
// allows up to 20), so a row configured with more attempts gets a proportionally
// longer allowance instead of being judged against the default.
//
// Derivation: one attempt may legitimately run for the design_author hard run
// budget (900s) and then wait out the queue's capped retry backoff (30s) — 930s.
// 20 minutes covers that with margin. The composition-root pin
// TestAuthorBatchStallWindowCoversAuthorAttemptEnvelope re-derives the per-attempt
// envelope from those authorities and fails if it ever outgrows this allowance.
//
// IMPORTANT — what this window does NOT measure (review I1): it is compared
// against the last-progress clock of a row that HOLDS A LEASE (claimed/running).
// work_item.updated_at only advances while a lease is held (the Renew statement
// is guarded by the leased-status set), so a queued row (open, including one
// waiting out its retry backoff) and a row parked for a human (waiting_human)
// have a FROZEN clock and an UNBOUNDED, perfectly normal wait. Those rows block
// the judge but never start this clock — see AuthorBatchReader.
const AuthorBatchStallWindowPerAttempt = 20 * time.Minute

// AuthorBatchReader is the design context's neutral port over the agent work
// item facts of one requirement version's authoring batch. The design context
// never reads work_item itself; the composition root wires the targeted SQL
// adapter (authorBatchSQLSource), which queries by version + type +
// payload requirement_version_id with NO LIMIT window — the same shape and the
// same hazard as judgeGenerationSQLSource / judgeReworkSQLSource: a capped
// newest-500 page loses the older rows of a busy version and silently changes
// the answer.
//
// An unwired reader keeps the pre-H-F6 predicate (coverage + pending drafts),
// exactly like the optional ReworkBudgetReader; an UNREADABLE one fails closed.
type AuthorBatchReader interface {
	// AuthorBatchState reports the settle state of the requirement version's
	// design.requirement batch.
	AuthorBatchState(ctx context.Context, versionID, requirementVersionID int64) (AuthorBatchState, error)
	// AuthorBatchStallDeadline returns the EARLIEST moment at which one of the
	// batch's lease-holding in-flight rows would count as stalled: for every
	// claimed/running row, its last-progress timestamp plus
	// AuthorBatchStallWindowPerAttempt × that row's max_attempts.
	//
	// The zero time means "no stall clock is running": either nothing is in
	// flight, or every in-flight row is queued (open) or parked for a human
	// (waiting_human). Those waits are unbounded and normal, so they defer the
	// judge WITHOUT ever escalating it. A lease-holding row whose progress
	// timestamp is missing is a data-integrity error, not a zero deadline.
	AuthorBatchStallDeadline(ctx context.Context, versionID, requirementVersionID int64) (time.Time, error)
}

// SetAuthorBatchReader wires the authoring-batch settle read the judge
// scheduling predicate gates on (H-F6). It is a setter for the same reason as
// SetJudgeScheduler: the composition root builds the work-item adapter after the
// planner. Nil keeps the legacy predicate so degraded assemblies and the
// pre-existing planner tests behave exactly as before.
func (p *Planner) SetAuthorBatchReader(r AuthorBatchReader) *Planner {
	if p != nil {
		p.authorBatch = r
	}
	return p
}

// authorBatchAllowsJudge applies the pinned H-F6 predicate to one requirement
// version whose coverage union is complete and which has pending drafts:
//
//	NoBatch | AllTerminal -> true  (enqueue the judge generation)
//	InFlight              -> false (defer; escalate once a LEASED row stalls)
//
// Deferral is bounded only where a bound is meaningful (review I1): a row that
// holds a lease and stops making progress past its own attempt allowance
// escalates every pending draft through the same judge_exhausted fact the judge
// generation budget uses, so the drafts derive judge_unavailable and the human
// decision point (override) stays reachable. A row that is merely QUEUED or
// parked for a HUMAN has no progress clock and an unbounded normal wait: it
// defers the judge and is never escalated — escalating those would stamp
// judge_exhausted over drafts whose author simply has not been scheduled yet,
// and the escalation is a one-way door (it outranks a later pass).
//
// An unwired reader keeps the legacy behavior; an unreadable one fails closed
// (constitution VII.6) rather than guessing either way.
func (p *Planner) authorBatchAllowsJudge(ctx context.Context, versionID int64, requirement RequirementJudgeScope, pending []JudgeCaseRef) (bool, error) {
	if p.authorBatch == nil {
		return true, nil
	}
	state, err := p.authorBatch.AuthorBatchState(ctx, versionID, requirement.RequirementVersionID)
	if err != nil {
		return false, mapDesignError(err, "author batch")
	}
	switch state {
	case AuthorBatchNoBatch, AuthorBatchAllTerminal:
		return true, nil
	case AuthorBatchInFlight:
		deadline, err := p.authorBatch.AuthorBatchStallDeadline(ctx, versionID, requirement.RequirementVersionID)
		if err != nil {
			return false, mapDesignError(err, "author batch")
		}
		// No stall clock: every in-flight row is queued or awaiting a human.
		// Normal, unbounded, and attributable (VII.7) — logged at debug because
		// it is the expected state on every pass while the author is queued,
		// not a degradation.
		if deadline.IsZero() {
			p.log().Debug("design: judge deferred, authoring batch queued or awaiting a human (no stall clock)",
				"version_id", versionID,
				"requirement_id", requirement.RequirementID,
				"requirement_version_id", requirement.RequirementVersionID,
				"pending_drafts", len(pending))
			return false, nil
		}
		if p.clock.Now().Before(deadline) {
			p.log().Debug("design: judge deferred, authoring batch still holds a lease within its stall allowance",
				"version_id", versionID,
				"requirement_id", requirement.RequirementID,
				"requirement_version_id", requirement.RequirementVersionID,
				"stall_deadline", deadline)
			return false, nil
		}
		return false, p.escalateStalledAuthorBatch(ctx, versionID, requirement, pending)
	default:
		// An unknown state is an unreadable fact, not a silent "no batch".
		return false, platform.Unavailable("the author batch reader returned an unknown settle state: " + string(state))
	}
}

// escalateStalledAuthorBatch records the judge_exhausted escalation fact for
// EVERY pending draft of a requirement version whose authoring batch never
// settled, and emits the degradation event once per NEWLY created fact. It rides
// the SAME fact store and the SAME event as the judge generation budget
// exhaustion (one authority, one review-state projection): the draft has no
// judgment and cannot get one while the batch is stalled, which is exactly what
// judge_unavailable means. Already-judged drafts are not in pending, so a real
// verdict is never demoted by a false escalation.
func (p *Planner) escalateStalledAuthorBatch(ctx context.Context, versionID int64, requirement RequirementJudgeScope, pending []JudgeCaseRef) error {
	if p.judgments == nil {
		return ErrJudgeExhaustedStoreUnavailable
	}
	createdAny := false
	for _, ref := range pending {
		created, err := p.judgments.InsertJudgeExhausted(ctx, versionID, requirement.RequirementID, ref.CaseID, ref.CaseVersionID)
		if err != nil {
			return mapDesignError(err, "judge escalation fact")
		}
		// Creation transition only: every persisted event wakes a reconcile pass,
		// so re-publishing per pass would sustain a publish -> wake -> reconcile
		// loop. The fact is the durable truth either way.
		if created {
			createdAny = true
			if p.events != nil {
				_ = p.events.Publish(ctx, JudgeExhaustedEvent(ctx, versionID, requirement.RequirementID, ref.CaseID, ref.CaseVersionID))
			}
		}
	}
	// Attribute the escalation ONCE, on the pass that actually wrote facts. The
	// stalled condition persists across every later reconcile pass; warning per
	// pass would bury the signal in its own repetition (review M3).
	if createdAny {
		p.log().Warn("design: lease-holding authoring run stalled; pending drafts escalated to a human",
			"version_id", versionID,
			"requirement_id", requirement.RequirementID,
			"requirement_version_id", requirement.RequirementVersionID,
			"pending_drafts", len(pending),
			"stall_window_per_attempt", AuthorBatchStallWindowPerAttempt.String())
	}
	return nil
}

// RequirementJudgeScope identifies the requirement version the judge scheduling
// predicate is deciding for. It carries only IDs: the scope is a scheduling
// address, never a second copy of requirement state.
type RequirementJudgeScope struct {
	RequirementID        int64
	RequirementVersionID int64
}
