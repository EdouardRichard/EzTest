//go:build integration

package deliveryapp_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

/* ------------------------------ issue fixture ----------------------------- */

// integrationIssueExecution serves the case_run snapshots of IngestCandidate.
type integrationIssueExecution struct {
	snapshots map[int64]deliveryapp.CaseRunSnapshot
}

func (e *integrationIssueExecution) ListFailedApplicationCaseRuns(context.Context, int64) ([]deliveryapp.CaseRunSnapshot, error) {
	return nil, nil
}

func (e *integrationIssueExecution) CaseRunSnapshot(_ context.Context, caseRunID int64) (deliveryapp.CaseRunSnapshot, error) {
	snapshot, ok := e.snapshots[caseRunID]
	if !ok {
		return deliveryapp.CaseRunSnapshot{}, fmt.Errorf("unknown case run %d", caseRunID)
	}
	return snapshot, nil
}

func (e *integrationIssueExecution) WaveSnapshot(context.Context, int64, int64) (deliveryapp.WaveSnapshot, error) {
	return deliveryapp.WaveSnapshot{}, nil
}

func (e *integrationIssueExecution) VersionSnapshot(context.Context, int64) (deliveryapp.VersionSnapshot, error) {
	return deliveryapp.VersionSnapshot{}, nil
}

type integrationIssueEvent struct {
	eventType string
	versionID int64
	payload   map[string]any
}

type integrationIssueEvents struct {
	records []integrationIssueEvent
}

func (e *integrationIssueEvents) Publish(_ context.Context, eventType string, versionID int64, payload map[string]any) error {
	copied := make(map[string]any, len(payload))
	for key, value := range payload {
		copied[key] = value
	}
	e.records = append(e.records, integrationIssueEvent{eventType: eventType, versionID: versionID, payload: copied})
	return nil
}

type integrationIssueAuditRecord struct {
	actorID    int64
	action     string
	targetKind string
	targetID   string
	metadata   map[string]any
}

type integrationIssueAudit struct {
	records []integrationIssueAuditRecord
}

func (a *integrationIssueAudit) Write(_ context.Context, actorID int64, action, targetKind, targetID string, metadata map[string]any) error {
	copied := make(map[string]any, len(metadata))
	for key, value := range metadata {
		copied[key] = value
	}
	a.records = append(a.records, integrationIssueAuditRecord{
		actorID: actorID, action: action, targetKind: targetKind, targetID: targetID, metadata: copied,
	})
	return nil
}

type integrationIssueTasks struct{}

func (integrationIssueTasks) EnsureIssueTriage(context.Context, int64, int64) error { return nil }
func (integrationIssueTasks) EnsureReportTask(context.Context, int64, int32) error  { return nil }
func (integrationIssueTasks) EnsureRequirementClose(context.Context, int64, int64, int32) error {
	return nil
}
func (integrationIssueTasks) EnsureVersionClose(context.Context, int64, int64, string) error {
	return nil
}

type integrationIssueClock struct{ now time.Time }

func (c integrationIssueClock) Now() time.Time { return c.now }

type integrationIssueFixture struct {
	pool   *pgxpool.Pool
	repo   *deliverypg.IssueRepository
	svc    *deliveryapp.IssueService
	exec   *integrationIssueExecution
	events *integrationIssueEvents
	audit  *integrationIssueAudit
}

func newIntegrationIssueFixture(t *testing.T) *integrationIssueFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(0)
	if err != nil {
		t.Fatalf("new snowflake: %v", err)
	}
	repo := deliverypg.NewIssueRepository(pool, ids)
	exec := &integrationIssueExecution{snapshots: map[int64]deliveryapp.CaseRunSnapshot{}}
	events := &integrationIssueEvents{}
	audit := &integrationIssueAudit{}
	svc := deliveryapp.NewIssueService(repo, exec, events, audit,
		integrationIssueClock{now: time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)},
		integrationIssueTasks{},
	)
	return &integrationIssueFixture{pool: pool, repo: repo, svc: svc, exec: exec, events: events, audit: audit}
}

// integrationIssueSnapshot builds one failed(application) case_run projection.
// The same case version/step/assertion pair makes two case runs share the
// dedupe key, exactly like a repeated failure across waves.
func integrationIssueSnapshot(versionID, caseRunID, caseVersionID int64) deliveryapp.CaseRunSnapshot {
	return deliveryapp.CaseRunSnapshot{
		ID:                 caseRunID,
		VersionID:          versionID,
		CaseVersionID:      caseVersionID,
		RequirementID:      1200,
		CaseTitle:          "Checkout submission",
		Status:             "failed",
		FailureClass:       "application",
		FailedStepSeq:      2,
		FailedAssertionSeq: 1,
		FailedExpected:     "order confirmation visible",
		FailedActual:       "order confirmation missing",
		Category:           "ui",
		Severity:           "major",
	}
}

// integrationIssueInput maps the snapshot onto the domain candidate input.
func integrationIssueInput(snapshot deliveryapp.CaseRunSnapshot) delivery.CandidateInput {
	return delivery.CandidateInput{
		VersionID:      snapshot.VersionID,
		CaseRunID:      snapshot.ID,
		CaseVersionID:  snapshot.CaseVersionID,
		RequirementID:  snapshot.RequirementID,
		CaseTitle:      snapshot.CaseTitle,
		StepSeq:        snapshot.FailedStepSeq,
		AssertionSeq:   snapshot.FailedAssertionSeq,
		FailureClass:   snapshot.FailureClass,
		Category:       snapshot.Category,
		Severity:       snapshot.Severity,
		FailedExpected: snapshot.FailedExpected,
		FailedActual:   snapshot.FailedActual,
		DedupeHint:     snapshot.DedupeHint,
	}
}

// integrationIssueCreate inserts one candidate through the real repository and
// returns the persisted issue and its refs.
func integrationIssueCreate(t *testing.T, repo *deliverypg.IssueRepository, snapshot deliveryapp.CaseRunSnapshot) (delivery.Issue, []delivery.IssueRef) {
	t.Helper()
	issue, refs, err := delivery.NewIssueCandidate(integrationIssueInput(snapshot))
	if err != nil {
		t.Fatalf("build candidate %d: %v", snapshot.ID, err)
	}
	created, isNew, err := repo.CreateCandidate(context.Background(), issue, refs)
	if err != nil {
		t.Fatalf("create candidate %d: %v", snapshot.ID, err)
	}
	if !isNew {
		t.Fatalf("create candidate %d reported an existing issue %d", snapshot.ID, created.ID)
	}
	return created, refs
}

/* ---------------------------------- tests --------------------------------- */

// TestIssueServiceIntegrationRecurrenceRedirect merges A into B and ingests a
// new failure with A's dedupe key through the real PostgreSQL repository: the
// refs land on B with role='recurrence', B.recurrence_count increments,
// last_recurred_at is stamped, A stays untouched, and a second recurrence
// increments the counter again. Event/audit projections fire once per redirect.
func TestIssueServiceIntegrationRecurrenceRedirect(t *testing.T) {
	fixture := newIntegrationIssueFixture(t)
	ctx := context.Background()
	const versionID = int64(100)

	sourceSnapshot := integrationIssueSnapshot(versionID, 3001, 4001)
	targetSnapshot := integrationIssueSnapshot(versionID, 3002, 4002)
	source, sourceRefs := integrationIssueCreate(t, fixture.repo, sourceSnapshot)
	target, targetRefs := integrationIssueCreate(t, fixture.repo, targetSnapshot)

	if _, err := fixture.svc.Merge(ctx, versionID, source.ID, target.ID, 77, "duplicate target", nil); err != nil {
		t.Fatalf("merge source into target: %v", err)
	}

	// Capture the source/target state after the merge, before any recurrence.
	sourceRefsAfterMerge, err := fixture.repo.ListRefs(ctx, source.ID)
	if err != nil {
		t.Fatalf("source refs after merge: %v", err)
	}
	sourceHistoryAfterMerge, err := fixture.repo.ListHistory(ctx, source.ID)
	if err != nil {
		t.Fatalf("source history after merge: %v", err)
	}
	targetHistoryAfterMerge, err := fixture.repo.ListHistory(ctx, target.ID)
	if err != nil {
		t.Fatalf("target history after merge: %v", err)
	}
	targetRefsAfterMerge, err := fixture.repo.ListRefs(ctx, target.ID)
	if err != nil {
		t.Fatalf("target refs after merge: %v", err)
	}
	if len(sourceRefsAfterMerge) != len(sourceRefs) || len(targetHistoryAfterMerge) != 1 ||
		len(targetRefsAfterMerge) != len(targetRefs) {
		t.Fatalf("merge changed refs/history: source refs=%d target refs=%d target history=%d",
			len(sourceRefsAfterMerge), len(targetRefsAfterMerge), len(targetHistoryAfterMerge))
	}
	fixture.events.records = nil
	fixture.audit.records = nil

	// Same dedupe key as A from a new case_run: the redirect targets B.
	firstRecurrence := integrationIssueSnapshot(versionID, 3003, 4001)
	fixture.exec.snapshots[firstRecurrence.ID] = firstRecurrence
	firstIssue, created, err := fixture.svc.IngestCandidate(ctx, firstRecurrence.ID)
	if err != nil {
		t.Fatalf("IngestCandidate(first recurrence): %v", err)
	}
	if created {
		t.Fatal("recurrence created a new issue, want a redirect to the target")
	}
	if firstIssue.ID != target.ID {
		t.Fatalf("recurrence issue = %d, want the merge target %d", firstIssue.ID, target.ID)
	}
	if firstIssue.RecurrenceCount != 1 || firstIssue.LastRecurredAt == nil {
		t.Fatalf("recurrence target = %+v, want count 1 with last_recurred_at", firstIssue)
	}

	targetRefsAfter, err := fixture.repo.ListRefs(ctx, target.ID)
	if err != nil {
		t.Fatalf("target refs after recurrence: %v", err)
	}
	wantTargetRefs := len(targetRefsAfter) - len(targetRefsAfterMerge)
	if wantTargetRefs <= 0 {
		t.Fatalf("target refs = %d, want the recurrence refs appended", len(targetRefsAfter))
	}
	recurrenceRefs := 0
	recurrenceCaseRun := false
	for _, ref := range targetRefsAfter {
		if ref.Role != delivery.IssueRefRoleRecurrence {
			continue
		}
		recurrenceRefs++
		if ref.Kind == delivery.IssueRefKindCaseRun && ref.RefID == firstRecurrence.ID {
			recurrenceCaseRun = true
		}
	}
	if recurrenceRefs == 0 || !recurrenceCaseRun {
		t.Fatalf("target recurrence refs = %d case_run=%v, want role recurrence incl. case_run %d",
			recurrenceRefs, recurrenceCaseRun, firstRecurrence.ID)
	}

	// A is untouched: same refs, same history, zero recurrence counter.
	sourceAfter, err := fixture.repo.Get(ctx, versionID, source.ID)
	if err != nil {
		t.Fatalf("source after recurrence: %v", err)
	}
	if sourceAfter.RecurrenceCount != 0 || sourceAfter.LastRecurredAt != nil || sourceAfter.Status != delivery.IssueStatusMerged {
		t.Fatalf("source changed by recurrence: %+v", sourceAfter)
	}
	sourceRefsAfter, err := fixture.repo.ListRefs(ctx, source.ID)
	if err != nil || len(sourceRefsAfter) != len(sourceRefsAfterMerge) {
		t.Fatalf("source refs after recurrence = %d err=%v, want unchanged %d",
			len(sourceRefsAfter), err, len(sourceRefsAfterMerge))
	}
	sourceHistoryAfter, err := fixture.repo.ListHistory(ctx, source.ID)
	if err != nil || len(sourceHistoryAfter) != len(sourceHistoryAfterMerge) {
		t.Fatalf("source history after recurrence = %d err=%v, want unchanged %d",
			len(sourceHistoryAfter), err, len(sourceHistoryAfterMerge))
	}

	targetHistory, err := fixture.repo.ListHistory(ctx, target.ID)
	if err != nil {
		t.Fatalf("target history after recurrence: %v", err)
	}
	if len(targetHistory) != len(targetHistoryAfterMerge)+1 {
		t.Fatalf("target history = %d, want one recurrence row", len(targetHistory))
	}
	recurrenceRow := targetHistory[len(targetHistory)-1]
	if recurrenceRow.From == nil || *recurrenceRow.From != delivery.IssueStatusOpen ||
		recurrenceRow.To != delivery.IssueStatusOpen || recurrenceRow.Reason != "recurrence" ||
		recurrenceRow.ActorKind != "system" || recurrenceRow.ActorID != nil {
		t.Fatalf("recurrence history = %+v", recurrenceRow)
	}

	if len(fixture.events.records) != 1 || fixture.events.records[0].eventType != "issue.recurred" {
		t.Fatalf("events = %+v, want one issue.recurred", fixture.events.records)
	}
	event := fixture.events.records[0]
	if event.versionID != versionID ||
		event.payload["issue_id"] != fmt.Sprintf("%d", target.ID) ||
		event.payload["source_issue_id"] != fmt.Sprintf("%d", source.ID) ||
		event.payload["recurrence_count"] != 1 {
		t.Fatalf("issue.recurred payload = %v", event.payload)
	}
	if event.payload["dedupe_key"] != source.DedupeKey {
		t.Fatalf("issue.recurred dedupe_key = %v, want %q", event.payload["dedupe_key"], source.DedupeKey)
	}
	if len(fixture.audit.records) != 1 || fixture.audit.records[0].action != "delivery.issue.recurred" ||
		fixture.audit.records[0].targetID != fmt.Sprintf("%d", target.ID) {
		t.Fatalf("audit = %+v, want delivery.issue.recurred on the target", fixture.audit.records)
	}

	// A second failure with the same dedupe key increments the counter again
	// while the recurrence refs stay one set per candidate.
	secondRecurrence := integrationIssueSnapshot(versionID, 3004, 4001)
	fixture.exec.snapshots[secondRecurrence.ID] = secondRecurrence
	secondIssue, created, err := fixture.svc.IngestCandidate(ctx, secondRecurrence.ID)
	if err != nil || created {
		t.Fatalf("IngestCandidate(second recurrence) = %+v/%v err=%v", secondIssue, created, err)
	}
	if secondIssue.ID != target.ID || secondIssue.RecurrenceCount != 2 {
		t.Fatalf("second recurrence = %+v, want target %d with count 2", secondIssue, target.ID)
	}
	targetRefsSecond, err := fixture.repo.ListRefs(ctx, target.ID)
	if err != nil {
		t.Fatalf("target refs after second recurrence: %v", err)
	}
	if len(targetRefsSecond) <= len(targetRefsAfter) {
		t.Fatalf("second recurrence appends no refs: %d -> %d", len(targetRefsAfter), len(targetRefsSecond))
	}
	sourceRefsSecond, err := fixture.repo.ListRefs(ctx, source.ID)
	if err != nil || len(sourceRefsSecond) != len(sourceRefsAfterMerge) {
		t.Fatalf("source refs after second recurrence = %d err=%v, want unchanged %d",
			len(sourceRefsSecond), err, len(sourceRefsAfterMerge))
	}
	if len(fixture.events.records) != 2 || fixture.events.records[1].payload["recurrence_count"] != 2 {
		t.Fatalf("events after second recurrence = %+v", fixture.events.records)
	}
	if len(fixture.audit.records) != 2 {
		t.Fatalf("audits after second recurrence = %d, want 2", len(fixture.audit.records))
	}
}

// TestIssueServiceIntegrationRecurrenceBrokenChainFallsBack keeps the pre-P7
// behavior when the merged source's target is missing: the new refs land on the
// source and the event/audit carry redirect_failed=true.
func TestIssueServiceIntegrationRecurrenceBrokenChainFallsBack(t *testing.T) {
	fixture := newIntegrationIssueFixture(t)
	ctx := context.Background()
	const versionID = int64(100)

	sourceSnapshot := integrationIssueSnapshot(versionID, 3101, 4101)
	source, sourceRefs := integrationIssueCreate(t, fixture.repo, sourceSnapshot)
	// A dangling merged_into_id cannot be produced through Merge (the target
	// must exist), so corrupt the pointer directly; the FK still requires a
	// row, so point it at a target that is then removed is unsafe. Instead the
	// broken chain is a cross-version pointer, rejected by the redirect guard.
	otherVersion := integrationIssueSnapshot(200, 3102, 4201)
	foreign, _ := integrationIssueCreate(t, fixture.repo, otherVersion)
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE delivery_issue SET status = 'merged', merged_into_id = $2, merged_at = now()
		WHERE id = $1
	`, source.ID, foreign.ID); err != nil {
		t.Fatalf("corrupt source chain: %v", err)
	}
	fixture.events.records = nil
	fixture.audit.records = nil

	recurrence := integrationIssueSnapshot(versionID, 3103, 4101)
	fixture.exec.snapshots[recurrence.ID] = recurrence
	issue, created, err := fixture.svc.IngestCandidate(ctx, recurrence.ID)
	if err != nil {
		t.Fatalf("IngestCandidate: %v", err)
	}
	if created || issue.ID != source.ID {
		t.Fatalf("broken-chain fallback = %+v/%v, want the source %d", issue, created, source.ID)
	}
	refsAfter, err := fixture.repo.ListRefs(ctx, source.ID)
	if err != nil {
		t.Fatalf("source refs: %v", err)
	}
	if len(refsAfter) <= len(sourceRefs) {
		t.Fatalf("fallback did not append refs: %d -> %d", len(sourceRefs), len(refsAfter))
	}
	if len(fixture.events.records) != 1 || fixture.events.records[0].eventType != "issue.recurred" ||
		fixture.events.records[0].payload["redirect_failed"] != true {
		t.Fatalf("fallback events = %+v", fixture.events.records)
	}
	if len(fixture.audit.records) != 1 || fixture.audit.records[0].action != "delivery.issue.recurred" ||
		fixture.audit.records[0].metadata["redirect_failed"] != true {
		t.Fatalf("fallback audit = %+v", fixture.audit.records)
	}
}

// TestIssueServiceIntegrationReportRevisionStableAfterRecurrence publishes a
// revision, performs a recurrence redirect, and proves the old revision's as_of
// and HTML bytes are unchanged while a regenerated revision reflects the new
// state (spec §4.6 "old report as-of").
func TestIssueServiceIntegrationReportRevisionStableAfterRecurrence(t *testing.T) {
	fixture := newIntegrationReportFixture(t)
	ctx := context.Background()
	const versionID = int64(7)

	// The report fixture's version is 7; seed the issue rows in the same schema.
	ids, err := platformid.NewSnowflake(0)
	if err != nil {
		t.Fatalf("new snowflake: %v", err)
	}
	issueRepo := deliverypg.NewIssueRepository(fixture.pool, ids)
	sourceSnapshot := integrationIssueSnapshot(versionID, 3301, 4301)
	targetSnapshot := integrationIssueSnapshot(versionID, 3302, 4302)
	source, _ := integrationIssueCreate(t, issueRepo, sourceSnapshot)
	target, _ := integrationIssueCreate(t, issueRepo, targetSnapshot)

	fixture.data.wave.Issues = []delivery.IssueSnapshot{{
		ID: target.ID, VersionID: versionID, Title: "结算按钮（原始）",
		Status: delivery.IssueStatusOpen, Category: "ui", Severity: "major", Source: "analysis",
	}}
	report, err := fixture.svc.EnsureWaveReport(ctx, versionID, 12)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	first, err := fixture.svc.Publish(ctx, report.ID, 1, "第一版叙述")
	if err != nil {
		t.Fatalf("Publish r1: %v", err)
	}
	firstHTML, firstRevision, err := fixture.svc.OpenHTML(ctx, versionID, report.ID, 1)
	if err != nil {
		t.Fatalf("OpenHTML r1: %v", err)
	}
	if !bytes.Contains(firstHTML, []byte("结算按钮（原始）")) {
		t.Fatalf("r1 HTML does not contain the issue title (%d bytes)", len(firstHTML))
	}

	// Merge A into B and trigger a recurrence while r1 is already published.
	if _, err := issueRepo.Merge(ctx, deliveryapp.IssueMerge{
		VersionID: versionID, SourceID: source.ID, TargetID: target.ID, ActorID: 77,
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	exec := &integrationIssueExecution{snapshots: map[int64]deliveryapp.CaseRunSnapshot{}}
	recurrence := integrationIssueSnapshot(versionID, 3303, 4301)
	exec.snapshots[recurrence.ID] = recurrence
	issueSvc := deliveryapp.NewIssueService(issueRepo, exec, nil, nil,
		integrationIssueClock{now: time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC)},
		integrationIssueTasks{},
	)
	recurred, created, err := issueSvc.IngestCandidate(ctx, recurrence.ID)
	if err != nil {
		t.Fatalf("IngestCandidate during report stability test: %v", err)
	}
	if created || recurred.ID != target.ID || recurred.RecurrenceCount != 1 {
		t.Fatalf("recurrence = %+v/%v, want target %d count 1", recurred, created, target.ID)
	}

	// The old revision's stored as_of and HTML bytes must be identical.
	oldHTML, oldRevision, err := fixture.svc.OpenHTML(ctx, versionID, report.ID, 1)
	if err != nil {
		t.Fatalf("OpenHTML r1 after recurrence: %v", err)
	}
	if !oldRevision.AsOf.Equal(firstRevision.AsOf) {
		t.Fatalf("r1 as_of changed after recurrence: %v != %v", oldRevision.AsOf, firstRevision.AsOf)
	}
	if !bytes.Equal(oldHTML, firstHTML) {
		t.Fatalf("r1 HTML changed after recurrence: %d bytes != %d bytes", len(oldHTML), len(firstHTML))
	}
	if oldRevision.ID != first.ID || oldRevision.InputFingerprint != first.InputFingerprint {
		t.Fatalf("r1 revision identity changed after recurrence: %+v", oldRevision)
	}

	// A new revision reflects the current (recurred/merged) state, while r1 is
	// still byte-identical.
	fixture.data.wave.Issues = []delivery.IssueSnapshot{{
		ID: target.ID, VersionID: versionID, Title: "结算按钮（复发）",
		Status: delivery.IssueStatusOpen, Category: "ui", Severity: "major", Source: "analysis",
	}}
	next, err := fixture.svc.Regenerate(ctx, versionID, report.ID)
	if err != nil || next != 2 {
		t.Fatalf("Regenerate = %d err=%v, want revision 2", next, err)
	}
	if _, err := fixture.svc.Publish(ctx, report.ID, next, "第二版叙述"); err != nil {
		t.Fatalf("Publish r2: %v", err)
	}
	newHTML, _, err := fixture.svc.OpenHTML(ctx, versionID, report.ID, 2)
	if err != nil {
		t.Fatalf("OpenHTML r2: %v", err)
	}
	if !bytes.Contains(newHTML, []byte("结算按钮（复发）")) {
		t.Fatalf("r2 HTML does not reflect the new state (%d bytes)", len(newHTML))
	}
	oldHTMLAgain, _, err := fixture.svc.OpenHTML(ctx, versionID, report.ID, 1)
	if err != nil || !bytes.Equal(oldHTMLAgain, firstHTML) {
		t.Fatalf("r1 HTML changed after r2 publish: err=%v equal=%v", err, bytes.Equal(oldHTMLAgain, firstHTML))
	}
}
