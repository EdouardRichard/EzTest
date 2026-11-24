package designapp

import (
	"context"
	"errors"
)

// ErrAuthorExhaustedStoreUnavailable reports that the author-exhaustion fact
// store is not wired. It is an explicit fail-closed signal (constitution VII.6),
// the mirror of ErrJudgeExhaustedStoreUnavailable: an unwired cross-context fact
// store must never answer "no escalation" silently and leave a requirement
// stranded with incomplete acceptance-criterion coverage.
var ErrAuthorExhaustedStoreUnavailable = errors.New("design: author-exhausted fact store is not wired")

// AuthorExhaustedStore is the neutral cross-context port for the author_exhausted
// escalation fact: the CASE-AUTHORING generation budget of one requirement
// version is spent while its coverage union is still incomplete, so no further
// design.requirement generation can ever be created for that revision and a human
// must take over (spec SC-E4 "无永久 pending", H-F5b — the authoring-side mirror of
// the judge generation budget's JudgeExhaustedStore).
//
// The fact lives in the version context's append-only version_fact mechanism
// (kind "author_exhausted", bound to the requirement revision); the composition
// root wires an adapter over the version FactService. The design context never
// imports the version context and never creates a second fact table.
//
// WHY a requirement-level fact and not JudgeExhaustedStore: judge_exhausted binds
// a CASE VERSION, and an authoring budget can be spent with ZERO usable drafts
// (the model produced nothing) — there would be no case version to bind, and the
// requirement would stay silent. The requirement revision is the entity that is
// actually stuck.
//
// The escalation is NOT a dead end: a new requirement revision starts a fresh
// authoring budget, and the human can also author/confirm cases by hand or waive
// the requirement. The fact makes the stall visible and attributable through the
// version facts read model instead of leaving it invisible.
type AuthorExhaustedStore interface {
	// InsertAuthorExhausted records that the authoring generation budget of one
	// requirement revision is exhausted and the requirement is escalated to a
	// human. generations is the spent budget, carried in the payload so the
	// escalation is attributable to its reason. It reports whether the fact was
	// NEWLY created: the append is idempotent, and a caller that signals the
	// escalation (log/event) must do so only on the creation transition, because
	// the exhausted condition persists across every later reconcile pass.
	InsertAuthorExhausted(ctx context.Context, versionID, requirementID, requirementVersionID int64, generations int) (bool, error)
}
