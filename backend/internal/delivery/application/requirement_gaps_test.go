package application

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// TestApplyRequirementGapsCreatesRequirementGapIssue pins the P7 escalation
// materialization of spec §6.4.4: a supplement escalation becomes an AI
// requirement_gap issue (severity major, medium confidence, requirement refs and
// no case), the deterministic §5.4 dedupe key is used, and a replay is
// idempotent.
func TestApplyRequirementGapsCreatesRequirementGapIssue(t *testing.T) {
	env := newIssueTestEnv(t)
	env.allow(domain.RefKindRequirement, 55, issueTestVersionID)
	env.allow(domain.RefKindRequirement, 56, issueTestVersionID)

	issues, err := env.svc.ApplyRequirementGaps(context.Background(), issueTestVersionID, 600, issueTestActor,
		[]RequirementGapEscalation{{
			Title:          "  Partial refund uncovered  ",
			Detail:         "no requirement covers partial refunds",
			RequirementIDs: []int64{56, 55, 56},
		}})
	if err != nil {
		t.Fatalf("ApplyRequirementGaps: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues=%+v want one materialized issue", issues)
	}
	issue := issues[0]
	if issue.Category != domain.CategoryRequirementGap || issue.Source != domain.SourceAI {
		t.Fatalf("category/source=(%q,%q) want requirement_gap/ai", issue.Category, issue.Source)
	}
	if issue.Title != "Partial refund uncovered" || issue.Description != "no requirement covers partial refunds" {
		t.Fatalf("title/description=(%q,%q)", issue.Title, issue.Description)
	}
	if issue.Severity != domain.SeverityMajor || issue.Confidence != domain.ConfidenceMedium {
		t.Fatalf("severity/confidence=(%q,%q) want major/medium", issue.Severity, issue.Confidence)
	}
	if issue.CaseNodeID != nil || issue.CaseRunID != nil || issue.FailureClass != "" {
		t.Fatalf("requirement-gap issue unexpectedly carries case fields: %+v", issue)
	}
	if issue.BatchID == nil || *issue.BatchID != 600 {
		t.Fatalf("batch=%v want 600", issue.BatchID)
	}
	wantKey := domain.RequirementGapDedupeKey(issueTestVersionID, []int64{55, 56})
	if issue.DedupeKey != wantKey {
		t.Fatalf("dedupe key=%q want %q", issue.DedupeKey, wantKey)
	}
	refs := env.store.refs[issue.ID]
	if len(refs) != 2 || refs[0].RefKind != domain.RefKindRequirement || refs[0].RefID != "55" ||
		refs[1].RefKind != domain.RefKindRequirement || refs[1].RefID != "56" {
		t.Fatalf("refs=%+v want the two requirement refs", refs)
	}
	if len(env.events.published) != 1 || env.events.published[0].Type != "issue.created" {
		t.Fatalf("events=%+v want one issue.created", env.events.published)
	}

	// Replay: the same escalation must not create a second issue or bump the
	// recurrence (the incoming ref set is already linked).
	again, err := env.svc.ApplyRequirementGaps(context.Background(), issueTestVersionID, 600, issueTestActor,
		[]RequirementGapEscalation{{
			Title:          "Partial refund uncovered",
			Detail:         "no requirement covers partial refunds",
			RequirementIDs: []int64{55, 56},
		}})
	if err != nil {
		t.Fatalf("replay ApplyRequirementGaps: %v", err)
	}
	if len(again) != 1 || again[0].ID != issue.ID {
		t.Fatalf("replay issues=%+v want the existing issue %d", again, issue.ID)
	}
	if stored := env.store.issues[issue.ID]; stored.Revision != issue.Revision || stored.RecurrenceCount != 0 {
		t.Fatalf("replay mutated the issue: revision=%d recurrence=%d", stored.Revision, stored.RecurrenceCount)
	}
}

// TestApplyRequirementGapsRejectsInvalidEscalation pins that a malformed
// escalation fails before anything is written, so the worker logs and degrades
// instead of creating a half-valid issue.
func TestApplyRequirementGapsRejectsInvalidEscalation(t *testing.T) {
	env := newIssueTestEnv(t)

	if _, err := env.svc.ApplyRequirementGaps(context.Background(), issueTestVersionID, 600, issueTestActor,
		[]RequirementGapEscalation{{Title: "gap", RequirementIDs: nil}}); err == nil {
		t.Fatal("want an error for an escalation without requirements")
	}
	if len(env.store.issues) != 0 || len(env.events.published) != 0 {
		t.Fatalf("invalid escalation wrote state: issues=%d events=%d",
			len(env.store.issues), len(env.events.published))
	}
	if env.store.inTx != 0 || env.store.creates != 0 {
		t.Fatalf("invalid escalation reached the store: inTx=%d creates=%d", env.store.inTx, env.store.creates)
	}
}
