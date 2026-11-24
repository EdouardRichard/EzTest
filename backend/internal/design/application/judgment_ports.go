package designapp

import (
	"context"
	"errors"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// Override conditions. Compile-time aliases of the DOMAIN authority
// (design.OverrideCondition*, where the Override value object lives): the
// application layer keeps its existing names without holding a second copy of
// the value set (H-F10 T6, constitution V.1/V.4).
const (
	OverrideConditionFailExhausted  = design.OverrideConditionFailExhausted
	OverrideConditionJudgeExhausted = design.OverrideConditionJudgeExhausted
)

// ValidOverrideCondition delegates to the domain authority.
func ValidOverrideCondition(value string) bool {
	return design.ValidOverrideCondition(value)
}

// ErrJudgeExhaustedStoreUnavailable reports that the judge-exhausted fact
// store is not wired. It is an explicit fail-closed signal (constitution
// VII.6): an unwired cross-context fact store must never answer "no
// escalation" silently.
var ErrJudgeExhaustedStoreUnavailable = errors.New("design: judge-exhausted fact store is not wired")

// JudgeExhaustedStore is the neutral cross-context port for the
// judge_exhausted escalation fact. The fact lives in the version context's
// append-only version_fact mechanism (kind "judge_exhausted", bound to the
// case version); the composition root wires an adapter over the version
// FactService (T13 wiring). The design context never imports the version
// context and never creates a second fact table.
type JudgeExhaustedStore interface {
	// InsertJudgeExhausted records that the judge generation budget of one
	// case version is exhausted and the case is escalated to a human. It
	// reports whether the escalation fact was NEWLY created: the fact append is
	// idempotent, and the caller emits the design.judge_exhausted degradation
	// event only on the creation transition. Re-publishing it on every
	// exhausted reconcile pass would sustain a permanent publish -> wake ->
	// reconcile -> publish loop (every persisted event wakes a reconcile).
	InsertJudgeExhausted(ctx context.Context, versionID, requirementID, caseID, caseVersionID int64) (bool, error)
	// HasJudgeExhausted reports whether an escalation fact exists for exactly
	// this case version. An older version's escalation is not this version's.
	HasJudgeExhausted(ctx context.Context, caseID, caseVersionID int64) (bool, error)
}

// JudgmentRepository persists the append-only judge facts and override audit
// rows (spec SC-E5). Facts are never updated or deleted; a duplicate
// (case_id, case_version_id) insert is design.ErrDuplicate.
type JudgmentRepository interface {
	InsertJudgment(ctx context.Context, judgment design.Judgment) error
	// LatestJudgment returns the newest fact of one logical case, or nil when
	// the case has never been judged.
	LatestJudgment(ctx context.Context, caseID int64) (*design.Judgment, error)
	ListJudgmentsByRequirement(ctx context.Context, versionID, requirementID int64) ([]design.Judgment, error)
	InsertOverride(ctx context.Context, override design.Override) error
	// LatestOverride returns the newest override bound to exactly this case
	// version, or nil. Validity against the latest fact is the caller's check.
	LatestOverride(ctx context.Context, caseID, caseVersionID int64) (*design.Override, error)
	// CountReworks derives the ROUND-PROGRESSION rework count of one logical
	// case from judgment facts: max(round) - 1, or 0 without facts. Round
	// increments ONLY on rework; a host edit produces a new case version judged
	// at the same round, so it never advances this counter. It is consumed by
	// resolveRound and the judge adapter's round derivation, which must accept
	// the re-judge of a SUCCEEDED rework (that rework is already terminal when
	// the revised draft is submitted, so a terminal count would reject the
	// legitimate next round). Agent work items are never counted HERE; the
	// escalation budget is a separate authority (ReworkBudgetReader).
	CountReworks(ctx context.Context, caseID int64) (int, error)
	JudgeExhaustedStore
}

// ReworkBudgetReader is the composition-root port for the ESCALATION rework
// budget: the count of terminal (budget-consuming) design.rework work items of
// one logical case, plus whether the rework responding to ONE failed version is
// terminal. Spec SC-E4 derives the rework budget from terminal rework facts, not
// round-1; a rework that ended without advancing the case consumes budget even
// though it produces no new judgment round. The design context never reads
// work_item directly.
//
// The effective escalation count is max(CountReworks, TerminalReworkCount):
// every round increment implies a succeeded (terminal) rework, so the terminal
// count is never behind in a reachable state and the max is defensive against
// either source lagging. An unwired (nil) reader falls back to CountReworks
// alone — a degraded assembly with no agent work-item store has no terminal
// reworks, which is the same answer.
type ReworkBudgetReader interface {
	TerminalReworkCount(ctx context.Context, versionID, caseID int64) (int, error)
	// HasTerminalReworkForVersion reports whether the design.rework responding
	// to this FAILED case version is terminal: done, dead, or failed at its
	// attempt cap. The rework request key is rework:<case>:<failedVersion> with
	// no generation and the work-item store never revives a terminal row, so a
	// terminal rework means no further rework can ever respond to this fail —
	// the case must escalate to a human even while the round budget still has
	// room. This covers BOTH endings: a rework that died, and a rework that
	// completed WITHOUT proposing a revision (the run may end without a verified
	// completion), which leaves the fail bound to the current version.
	// A rework that DID produce a revision advances the current version, so the
	// fail is no longer bound to it and this fact is never consulted for the
	// normal loop. In-flight and retryable-failed reworks still owe work and are
	// not terminal. Derived from the same work-item facts as the count (no
	// second authority).
	HasTerminalReworkForVersion(ctx context.Context, versionID, caseID, caseVersionID int64) (bool, error)
}

// JudgmentCaseReader reads one logical case with its CURRENT version and
// owning requirement (nil selector). design.Repository satisfies this port;
// the judgment service uses it to bind facts to the server-read content hash.
type JudgmentCaseReader interface {
	ReadCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (design.CaseData, error)
}

// SettingsReader is the design context's view of the settings-center service
// (settingsapp.Service satisfies it). The judgment service reads
// design.max_rework_rounds through this port; an unwired reader fails closed.
type SettingsReader interface {
	Int(ctx context.Context, key string) (int64, error)
}
