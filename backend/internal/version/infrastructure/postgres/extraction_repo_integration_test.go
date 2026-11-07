//go:build integration

package versionpg_test

// PostgreSQL half of the v0.22 A-7 extraction cursor. Runs only when
// EZTEST_TEST_DATABASE_URL is set (testutil.NewDB skips otherwise); the unit
// fake in version/application covers the worker policy and this test pins the
// SQL: advisory-locked run idempotency, segment uniqueness, stale lease
// recovery, FOR UPDATE SKIP LOCKED claim and the (version_id, external_id)
// requirement upsert.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func createExtractionAttachment(t *testing.T, pool *pgxpool.Pool, ids platformid.Generator, versionID int64) int64 {
	t.Helper()
	attachmentID := ids.Next()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO requirement_attachment (id, version_id, content_ref_id, name, mime, size, extraction_status)
		VALUES ($1, $2, $3, 'requirements.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', 2048, 'done')
	`, attachmentID, versionID, ids.Next()); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	return attachmentID
}

func extractionSegments() []versionapp.ExtractionSegment {
	return []versionapp.ExtractionSegment{
		{Seq: 1, HeadingPath: "3.1 登录", CharStart: 0, CharEnd: 120, TextSHA256: strings.Repeat("a", 64)},
		{Seq: 2, HeadingPath: "3.2 下单", CharStart: 121, CharEnd: 260, TextSHA256: strings.Repeat("b", 64)},
	}
}

func TestExtractionRepositoryIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	attachmentID := createExtractionAttachment(t, pool, ids, versionID)
	repo := versionpg.NewExtractionRepository(pool, ids)
	ctx := context.Background()
	now := time.Now().UTC()

	shaV1 := strings.Repeat("1", 64)
	run := versionapp.ExtractionRun{
		VersionID: versionID, AttachmentID: attachmentID, InputSHA256: shaV1,
		Status: versionapp.ExtractionRunPending, CreatedAt: now, UpdatedAt: now,
	}
	first, err := repo.CreateRun(ctx, run, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun first: %v", err)
	}
	if first.ID == 0 || first.TotalSegments != 2 {
		t.Fatalf("first run=%+v", first)
	}
	second, err := repo.CreateRun(ctx, run, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun idempotent: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("run duplicated: %d/%d", first.ID, second.ID)
	}
	segments, err := repo.ListSegments(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments=%d, want 2", len(segments))
	}

	// A changed sha opens a new run and archives the pending one.
	changed := run
	changed.InputSHA256 = strings.Repeat("2", 64)
	third, err := repo.CreateRun(ctx, changed, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun changed: %v", err)
	}
	if third.ID == first.ID {
		t.Fatalf("changed sha reused run %d", third.ID)
	}
	archived, err := repo.GetRun(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetRun archived: %v", err)
	}
	if archived.Status != versionapp.ExtractionRunFailed || archived.Error == "" {
		t.Fatalf("archived run=%+v", archived)
	}

	// Claim is exclusive and increments attempts exactly once per segment.
	claimed, err := repo.ClaimSegments(ctx, third.ID, 10, now)
	if err != nil {
		t.Fatalf("ClaimSegments: %v", err)
	}
	if len(claimed) != 2 || claimed[0].Seq != 1 || claimed[1].Seq != 2 {
		t.Fatalf("claimed=%+v", claimed)
	}
	if claimed[0].Attempts != 1 || claimed[0].Status != versionapp.ExtractionSegmentRunning {
		t.Fatalf("claimed[0]=%+v", claimed[0])
	}
	none, err := repo.ClaimSegments(ctx, third.ID, 10, now)
	if err != nil {
		t.Fatalf("second ClaimSegments: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("running segments reclaimed: %+v", none)
	}

	// Duplicate external_id updates the existing requirement, never inserts.
	// Emits happen while the run is active: the sink is fenced against
	// archived/done runs, so this block runs before the final segment mark.
	source := versionapp.ExtractionSource{
		VersionID: versionID, AttachmentID: attachmentID, RunID: third.ID, SegmentSeq: 1,
	}
	requirement := versionapp.ExtractionRequirement{
		ExternalID: "FR-001", Title: "登录", Description: "手机号登录",
		AcceptanceCriteria: []string{"验证码正确"}, Priority: "P0",
		Category: "functional", SourceQuote: "FR-001 登录", Confidence: 0.9,
	}
	counts, err := repo.UpsertExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{requirement}, now)
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	if counts.Created != 1 || counts.Updated != 0 {
		t.Fatalf("first upsert counts=%+v", counts)
	}
	requirement.Title = "手机号登录"
	requirement.Confidence = 0.7
	counts, err = repo.UpsertExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{requirement}, now)
	if err != nil {
		t.Fatalf("Upsert duplicate: %v", err)
	}
	if counts.Created != 0 || counts.Updated != 1 {
		t.Fatalf("duplicate upsert counts=%+v", counts)
	}
	var requirementCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM requirement WHERE version_id = $1 AND external_id = 'FR-001'
	`, versionID).Scan(&requirementCount); err != nil {
		t.Fatalf("count requirements: %v", err)
	}
	if requirementCount != 1 {
		t.Fatalf("requirements=%d, want 1", requirementCount)
	}
	var title string
	var runID int64
	var sourceQuote, externalID string
	var confidence float64
	if err := pool.QueryRow(ctx, `
		SELECT v.title, r.extraction_run_id, r.source_quote, r.external_id, r.confidence
		FROM requirement r
		JOIN requirement_version v ON v.id = r.current_version_id
		WHERE r.version_id = $1 AND r.external_id = 'FR-001'
	`, versionID).Scan(&title, &runID, &sourceQuote, &externalID, &confidence); err != nil {
		t.Fatalf("read requirement: %v", err)
	}
	if title != "手机号登录" || runID != third.ID || sourceQuote != "FR-001 登录" ||
		externalID != "FR-001" || confidence != 0.7 {
		t.Fatalf("requirement title=%q run=%d quote=%q external=%q confidence=%v",
			title, runID, sourceQuote, externalID, confidence)
	}
	// A second, low-confidence requirement for the derived count assertion.
	if _, err := repo.UpsertExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{
		{ExternalID: "FR-002", Title: "低置信", Confidence: 0.4},
	}, now); err != nil {
		t.Fatalf("Upsert low confidence: %v", err)
	}

	// Stale running lease returns to pending (error cleared) and is reclaimed.
	if _, err := pool.Exec(ctx, `
		UPDATE requirement_extraction_segment
		SET renewed_at = $2, error = 'worker crashed'
		WHERE run_id = $1 AND seq = 1
	`, third.ID, now.Add(-6*time.Minute)); err != nil {
		t.Fatalf("age lease: %v", err)
	}
	if err := repo.MarkSegmentDone(ctx, third.ID, 2, 0, now); err != nil {
		t.Fatalf("MarkSegmentDone 2: %v", err)
	}
	reclaimed, err := repo.ClaimSegments(ctx, third.ID, 10, now)
	if err != nil {
		t.Fatalf("ClaimSegments after stale lease: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].Seq != 1 || reclaimed[0].Attempts != 2 || reclaimed[0].Error != "" {
		t.Fatalf("reclaimed=%+v", reclaimed)
	}
	if err := repo.MarkSegmentDone(ctx, third.ID, 1, 1, now); err != nil {
		t.Fatalf("MarkSegmentDone 1: %v", err)
	}
	stored, err := repo.GetRun(ctx, third.ID)
	if err != nil {
		t.Fatalf("GetRun done: %v", err)
	}
	if stored.Status != versionapp.ExtractionRunDone || stored.DoneSegments != 2 || stored.TotalSegments != 2 {
		t.Fatalf("run after done=%+v", stored)
	}

	// The full reconciliation report plus the UI counts persist on the run.
	report := versionapp.ReconciliationReport{
		Expected: 58, Extracted: 1, Missing: 57,
		MissingIDs:    []string{"FR-002"},
		LowConfidence: 1, SkippedImages: 3,
		PlannedCount: 32, PlannedIDs: []string{"FR-059", "FR-060"},
	}
	if err := repo.SaveReconciliation(ctx, third.ID, report, now); err != nil {
		t.Fatalf("SaveReconciliation: %v", err)
	}
	reconciled, err := repo.GetRun(ctx, third.ID)
	if err != nil {
		t.Fatalf("GetRun reconciled: %v", err)
	}
	if reconciled.ExpectedCount == nil || *reconciled.ExpectedCount != 58 || reconciled.ExtractedCount != 1 {
		t.Fatalf("reconciled run=%+v", reconciled)
	}
	if reconciled.Report == nil || reconciled.Report.Expected != 58 ||
		reconciled.Report.SkippedImages != 3 || reconciled.Report.PlannedCount != 32 ||
		len(reconciled.Report.PlannedIDs) != 2 || len(reconciled.Report.MissingIDs) != 1 {
		t.Fatalf("persisted report=%+v", reconciled.Report)
	}
	var storedPlanned string
	if err := pool.QueryRow(ctx, `
		SELECT report->>'planned_count' FROM requirement_extraction_run WHERE id = $1
	`, third.ID).Scan(&storedPlanned); err != nil {
		t.Fatalf("read report jsonb: %v", err)
	}
	if storedPlanned != "32" {
		t.Fatalf("report->>'planned_count' = %q, want 32", storedPlanned)
	}

	// low_confidence is derived from requirement.confidence, not supplied.
	low, err := repo.CountLowConfidence(ctx, third.ID, versionapp.DefaultLowConfidenceThreshold)
	if err != nil {
		t.Fatalf("CountLowConfidence: %v", err)
	}
	if low != 1 {
		t.Fatalf("low confidence count = %d, want 1", low)
	}

	// Missing run is a typed not-found.
	if _, err := repo.GetRun(ctx, ids.Next()); !errors.Is(err, versionapp.ErrExtractionRunNotFound) {
		t.Fatalf("GetRun missing err=%v, want ErrExtractionRunNotFound", err)
	}
}

// TestExtractionArchiveFencingIntegration pins the changed-sha fence in SQL: a
// worker that claimed segments of the archived run cannot mark them terminal or
// overwrite the new run's requirement provenance, so reconciliation never has
// false missing ids because of a late old-worker emit.
func TestExtractionArchiveFencingIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	attachmentID := createExtractionAttachment(t, pool, ids, versionID)
	repo := versionpg.NewExtractionRepository(pool, ids)
	ctx := context.Background()
	now := time.Now().UTC()

	oldRun := versionapp.ExtractionRun{
		VersionID: versionID, AttachmentID: attachmentID,
		InputSHA256: strings.Repeat("a", 64), Status: versionapp.ExtractionRunPending,
		CreatedAt: now, UpdatedAt: now,
	}
	archived, err := repo.CreateRun(ctx, oldRun, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun old: %v", err)
	}
	claimed, err := repo.ClaimSegments(ctx, archived.ID, 10, now)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim old segments=%d err=%v", len(claimed), err)
	}
	oldSource := versionapp.ExtractionSource{
		VersionID: versionID, AttachmentID: attachmentID, RunID: archived.ID, SegmentSeq: 1,
	}
	if _, err := repo.UpsertExtractedRequirements(ctx, oldSource, []versionapp.ExtractionRequirement{
		{ExternalID: "FR-001", Title: "旧", Confidence: 0.9},
	}, now); err != nil {
		t.Fatalf("old emit before archive: %v", err)
	}

	// The document changed while the worker was in flight.
	newRun := oldRun
	newRun.InputSHA256 = strings.Repeat("b", 64)
	newRun.UpdatedAt = now.Add(time.Second)
	active, err := repo.CreateRun(ctx, newRun, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun new: %v", err)
	}
	storedOld, err := repo.GetRun(ctx, archived.ID)
	if err != nil {
		t.Fatalf("GetRun old: %v", err)
	}
	if storedOld.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("archived run status=%s, want failed", storedOld.Status)
	}

	// Fenced terminal marks and emits.
	if err := repo.MarkSegmentDone(ctx, archived.ID, 1, 1, now); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("MarkSegmentDone archived err=%v, want ErrExtractionRunState", err)
	}
	if err := repo.MarkSegmentFailed(ctx, archived.ID, 2, "late", now); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("MarkSegmentFailed archived err=%v, want ErrExtractionRunState", err)
	}
	if _, err := repo.UpsertExtractedRequirements(ctx, oldSource, []versionapp.ExtractionRequirement{
		{ExternalID: "FR-001", Title: "旧覆盖", Confidence: 0.1},
	}, now); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("late archived emit err=%v, want ErrExtractionRunState", err)
	}
	if _, err := repo.RetryFailedSegments(ctx, archived.ID, now); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("RetryFailedSegments archived err=%v, want ErrExtractionRunState", err)
	}

	// The new run owns the requirement provenance and reports no false missing id.
	newSource := versionapp.ExtractionSource{
		VersionID: versionID, AttachmentID: attachmentID, RunID: active.ID, SegmentSeq: 1,
	}
	if _, err := repo.UpsertExtractedRequirements(ctx, newSource, []versionapp.ExtractionRequirement{
		{ExternalID: "FR-001", Title: "新", Confidence: 0.9},
	}, now); err != nil {
		t.Fatalf("new emit: %v", err)
	}
	var requirementRunID int64
	if err := pool.QueryRow(ctx, `
		SELECT extraction_run_id FROM requirement
		WHERE version_id = $1 AND external_id = 'FR-001'
	`, versionID).Scan(&requirementRunID); err != nil {
		t.Fatalf("read requirement provenance: %v", err)
	}
	if requirementRunID != active.ID {
		t.Fatalf("requirement extraction_run_id=%d, want new run %d", requirementRunID, active.ID)
	}
	extracted, err := repo.ListExtractedExternalIDs(ctx, versionID, active.ID)
	if err != nil {
		t.Fatalf("ListExtractedExternalIDs: %v", err)
	}
	if len(extracted) != 1 || extracted[0] != "FR-001" {
		t.Fatalf("new run extracted ids=%v, want [FR-001]", extracted)
	}
	oldExtracted, err := repo.ListExtractedExternalIDs(ctx, versionID, archived.ID)
	if err != nil {
		t.Fatalf("ListExtractedExternalIDs old: %v", err)
	}
	if len(oldExtracted) != 0 {
		t.Fatalf("archived run still owns ids=%v", oldExtracted)
	}
}

// TestExtractionReplayLeaseAndPauseIntegration pins the SQL half of replay and
// lease safety: a changed text_sha256 reopens the segment and refreshes
// expected_count/totals; heartbeat renewal keeps a claim alive past the lease;
// a paused run is no longer claimable.
func TestExtractionReplayLeaseAndPauseIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	attachmentID := createExtractionAttachment(t, pool, ids, versionID)
	repo := versionpg.NewExtractionRepository(pool, ids)
	ctx := context.Background()
	now := time.Now().UTC()

	sha := strings.Repeat("3", 64)
	run := versionapp.ExtractionRun{
		VersionID: versionID, AttachmentID: attachmentID, InputSHA256: sha,
		Status: versionapp.ExtractionRunPending, CreatedAt: now, UpdatedAt: now,
	}
	created, err := repo.CreateRun(ctx, run, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	claimed, err := repo.ClaimSegments(ctx, created.ID, 10, now)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim=%d err=%v", len(claimed), err)
	}
	if err := repo.MarkSegmentDone(ctx, created.ID, 1, 1, now); err != nil {
		t.Fatalf("MarkSegmentDone 1: %v", err)
	}
	if err := repo.MarkSegmentDone(ctx, created.ID, 2, 1, now); err != nil {
		t.Fatalf("MarkSegmentDone 2: %v", err)
	}

	// Replay the same sha with changed seq2 text and a fresh expected count.
	expected := 58
	replay := run
	replay.ExpectedCount = &expected
	replay.UpdatedAt = now.Add(time.Minute)
	changedSegments := extractionSegments()
	changedSegments[1].TextSHA256 = strings.Repeat("c", 64)
	replayed, err := repo.CreateRun(ctx, replay, changedSegments)
	if err != nil {
		t.Fatalf("CreateRun replay: %v", err)
	}
	if replayed.ID != created.ID {
		t.Fatalf("replay created run %d, want %d", replayed.ID, created.ID)
	}
	if replayed.Status != versionapp.ExtractionRunPending || replayed.TotalSegments != 2 {
		t.Fatalf("replayed run=%+v", replayed)
	}
	if replayed.ExpectedCount == nil || *replayed.ExpectedCount != 58 {
		t.Fatalf("replayed expected_count=%v, want 58", replayed.ExpectedCount)
	}
	segments, err := repo.ListSegments(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if segments[0].Status != versionapp.ExtractionSegmentDone {
		t.Fatalf("unchanged segment status=%s, want done", segments[0].Status)
	}
	if segments[1].Status != versionapp.ExtractionSegmentPending || segments[1].Attempts != 0 {
		t.Fatalf("changed segment=%+v, want pending attempts=0", segments[1])
	}

	reclaimed, err := repo.ClaimSegments(ctx, created.ID, 10, replay.UpdatedAt)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Seq != 2 {
		t.Fatalf("replay claim=%+v err=%v", reclaimed, err)
	}

	// Heartbeat renewal keeps the claim alive past the five minute lease.
	renewed, err := repo.RenewSegmentLeases(ctx, created.ID, []int{2}, replay.UpdatedAt.Add(4*time.Minute))
	if err != nil || renewed != 1 {
		t.Fatalf("RenewSegmentLeases=%d err=%v", renewed, err)
	}
	none, err := repo.ClaimSegments(ctx, created.ID, 10, replay.UpdatedAt.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("ClaimSegments renewed: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("renewed claim was stolen: %+v", none)
	}
	stolen, err := repo.ClaimSegments(ctx, created.ID, 10, replay.UpdatedAt.Add(10*time.Minute))
	if err != nil || len(stolen) != 1 || stolen[0].Attempts != 2 {
		t.Fatalf("stale claim after lease=%+v err=%v", stolen, err)
	}

	// Pause gates further claims.
	transitioned, err := repo.TransitionRun(ctx, created.ID,
		[]versionapp.ExtractionRunStatus{versionapp.ExtractionRunRunning},
		versionapp.ExtractionRunPaused, replay.UpdatedAt.Add(11*time.Minute))
	if err != nil || !transitioned {
		t.Fatalf("pause transition=%v err=%v", transitioned, err)
	}
	pausedClaims, err := repo.ClaimSegments(ctx, created.ID, 10, replay.UpdatedAt.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("ClaimSegments paused: %v", err)
	}
	if len(pausedClaims) != 0 {
		t.Fatalf("paused run still claimable: %+v", pausedClaims)
	}
}

// TestExtractionConcurrentDifferentSHAIntegration pins the attachment-level
// Start lock: concurrent CreateRun calls with different shas must leave exactly
// one active run.
func TestExtractionConcurrentDifferentSHAIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	attachmentID := createExtractionAttachment(t, pool, ids, versionID)
	repo := versionpg.NewExtractionRepository(pool, ids)
	ctx := context.Background()
	now := time.Now().UTC()

	type result struct {
		run versionapp.ExtractionRun
		sha string
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, sha := range []string{strings.Repeat("d", 64), strings.Repeat("e", 64)} {
		wait.Add(1)
		go func(sha string) {
			defer wait.Done()
			<-start
			run, err := repo.CreateRun(ctx, versionapp.ExtractionRun{
				VersionID: versionID, AttachmentID: attachmentID, InputSHA256: sha,
				Status: versionapp.ExtractionRunPending, CreatedAt: now, UpdatedAt: now,
			}, extractionSegments())
			results <- result{run: run, sha: sha, err: err}
		}(sha)
	}
	close(start)
	wait.Wait()
	close(results)

	returned := make(map[int64]struct{}, 2)
	var failedSHA string
	var activeID int64
	for item := range results {
		if item.err != nil {
			t.Fatalf("concurrent CreateRun: %v", item.err)
		}
		returned[item.run.ID] = struct{}{}
		stored, err := repo.GetRun(ctx, item.run.ID)
		if err != nil {
			t.Fatalf("GetRun concurrent result: %v", err)
		}
		if stored.Status == versionapp.ExtractionRunFailed {
			failedSHA = item.sha
		} else {
			activeID = stored.ID
		}
	}
	if len(returned) != 2 {
		t.Fatalf("returned run ids=%v, want two distinct runs", returned)
	}
	if failedSHA == "" || activeID == 0 {
		t.Fatalf("concurrent runs did not archive one input: failed=%q active=%d", failedSHA, activeID)
	}
	var active int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM requirement_extraction_run
		WHERE version_id = $1 AND attachment_id = $2 AND status IN ('pending', 'running', 'paused')
	`, versionID, attachmentID).Scan(&active); err != nil {
		t.Fatalf("count active runs: %v", err)
	}
	if active != 1 {
		t.Fatalf("active runs=%d, want 1", active)
	}
	var total int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM requirement_extraction_run
		WHERE version_id = $1 AND attachment_id = $2
	`, versionID, attachmentID).Scan(&total); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if total != 2 {
		t.Fatalf("runs=%d, want 2", total)
	}

	// Replaying the superseded sha returns the active run and does not archive
	// it or create a third run.
	replayed, err := repo.CreateRun(ctx, versionapp.ExtractionRun{
		VersionID: versionID, AttachmentID: attachmentID, InputSHA256: failedSHA,
		Status: versionapp.ExtractionRunPending, CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun superseded replay: %v", err)
	}
	if replayed.ID != activeID || replayed.Status == versionapp.ExtractionRunFailed {
		t.Fatalf("superseded replay returned run=%+v, want active run %d", replayed, activeID)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM requirement_extraction_run
		WHERE version_id = $1 AND attachment_id = $2
	`, versionID, attachmentID).Scan(&total); err != nil {
		t.Fatalf("count runs after replay: %v", err)
	}
	if total != 2 {
		t.Fatalf("replay created extra runs: total=%d", total)
	}
}

// TestExtractionZeroSegmentReplayIntegration pins the empty-parse replay: a run
// created done with zero segments must stay done when the same sha is replayed,
// not fall through the "no retryable work" branch to failed.
func TestExtractionZeroSegmentReplayIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	attachmentID := createExtractionAttachment(t, pool, ids, versionID)
	repo := versionpg.NewExtractionRepository(pool, ids)
	ctx := context.Background()
	now := time.Now().UTC()

	expected := 2
	run := versionapp.ExtractionRun{
		VersionID: versionID, AttachmentID: attachmentID,
		InputSHA256: strings.Repeat("8", 64), Status: versionapp.ExtractionRunDone,
		ExpectedCount: &expected, CreatedAt: now, UpdatedAt: now,
	}
	created, err := repo.CreateRun(ctx, run, nil)
	if err != nil {
		t.Fatalf("CreateRun zero segments: %v", err)
	}
	if created.Status != versionapp.ExtractionRunDone || created.TotalSegments != 0 {
		t.Fatalf("zero-segment run=%+v, want done with 0 segments", created)
	}

	replay := run
	replay.UpdatedAt = now.Add(time.Minute)
	replayed, err := repo.CreateRun(ctx, replay, nil)
	if err != nil {
		t.Fatalf("CreateRun zero-segment replay: %v", err)
	}
	if replayed.ID != created.ID {
		t.Fatalf("zero-segment replay created run %d, want %d", replayed.ID, created.ID)
	}
	if replayed.Status != versionapp.ExtractionRunDone || replayed.TotalSegments != 0 {
		t.Fatalf("zero-segment replay=%+v, want the same run still done", replayed)
	}
	if replayed.ExpectedCount == nil || *replayed.ExpectedCount != 2 {
		t.Fatalf("zero-segment replay expected_count=%v, want 2", replayed.ExpectedCount)
	}
	segments, err := repo.ListSegments(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListSegments zero run: %v", err)
	}
	if len(segments) != 0 {
		t.Fatalf("zero-segment run has %d segments", len(segments))
	}
}

// TestExtractionSupersededReplayAfterNewRunSettlesIntegration pins the replay
// path once the changed-sha run is terminal: with no active newer run, replaying
// the archived sha must revive it in a writable state. The revived run must be
// able to claim, emit and mark its segments instead of being permanently fenced
// by the newer (already settled) run's id.
func TestExtractionSupersededReplayAfterNewRunSettlesIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	attachmentID := createExtractionAttachment(t, pool, ids, versionID)
	repo := versionpg.NewExtractionRepository(pool, ids)
	ctx := context.Background()
	now := time.Now().UTC()

	oldRun := versionapp.ExtractionRun{
		VersionID: versionID, AttachmentID: attachmentID,
		InputSHA256: strings.Repeat("6", 64), Status: versionapp.ExtractionRunPending,
		CreatedAt: now, UpdatedAt: now,
	}
	archived, err := repo.CreateRun(ctx, oldRun, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun A: %v", err)
	}
	newRun := oldRun
	newRun.InputSHA256 = strings.Repeat("7", 64)
	newRun.UpdatedAt = now.Add(time.Second)
	settled, err := repo.CreateRun(ctx, newRun, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun B: %v", err)
	}
	storedOld, err := repo.GetRun(ctx, archived.ID)
	if err != nil {
		t.Fatalf("GetRun archived A: %v", err)
	}
	if storedOld.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("A after B Start status=%s, want failed", storedOld.Status)
	}

	// B terminates normally: claim both segments and mark them done.
	if claimed, err := repo.ClaimSegments(ctx, settled.ID, 10, now); err != nil || len(claimed) != 2 {
		t.Fatalf("claim B=%d err=%v", len(claimed), err)
	}
	if err := repo.MarkSegmentDone(ctx, settled.ID, 1, 1, now); err != nil {
		t.Fatalf("MarkSegmentDone B1: %v", err)
	}
	if err := repo.MarkSegmentDone(ctx, settled.ID, 2, 1, now); err != nil {
		t.Fatalf("MarkSegmentDone B2: %v", err)
	}
	storedB, err := repo.GetRun(ctx, settled.ID)
	if err != nil {
		t.Fatalf("GetRun B: %v", err)
	}
	if storedB.Status != versionapp.ExtractionRunDone {
		t.Fatalf("B did not settle: %+v", storedB)
	}

	// Replay A now that B is terminal but still newer by id.
	replay := oldRun
	replay.UpdatedAt = now.Add(2 * time.Minute)
	replayed, err := repo.CreateRun(ctx, replay, extractionSegments())
	if err != nil {
		t.Fatalf("CreateRun replay A: %v", err)
	}
	if replayed.ID != archived.ID {
		t.Fatalf("replay A returned run %d, want %d", replayed.ID, archived.ID)
	}
	if replayed.Status != versionapp.ExtractionRunPending || replayed.TotalSegments != 2 {
		t.Fatalf("replayed A=%+v, want pending with 2 segments", replayed)
	}

	// The revived run must be writable even though B has a higher id: claim,
	// emit and mark must all succeed (B is terminal, not active).
	claimed, err := repo.ClaimSegments(ctx, archived.ID, 10, replay.UpdatedAt)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim replayed A=%d err=%v", len(claimed), err)
	}
	source := versionapp.ExtractionSource{
		VersionID: versionID, AttachmentID: attachmentID, RunID: archived.ID, SegmentSeq: 1,
	}
	if err := repo.MarkSegmentDone(ctx, archived.ID, 1, 1, replay.UpdatedAt); err != nil {
		t.Fatalf("MarkSegmentDone replayed A1: %v", err)
	}
	if _, err := repo.UpsertExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{
		{ExternalID: "FR-009", Title: "重放需求", Confidence: 0.8},
	}, replay.UpdatedAt); err != nil {
		t.Fatalf("emit from revived run: %v", err)
	}
	if err := repo.MarkSegmentDone(ctx, archived.ID, 2, 1, replay.UpdatedAt); err != nil {
		t.Fatalf("MarkSegmentDone replayed A2: %v", err)
	}
	final, err := repo.GetRun(ctx, archived.ID)
	if err != nil {
		t.Fatalf("GetRun replayed A: %v", err)
	}
	if final.Status != versionapp.ExtractionRunDone || final.DoneSegments != 2 || final.TotalSegments != 2 {
		t.Fatalf("replayed A after marking=%+v, want done with 2 segments", final)
	}
	segments, err := repo.ListSegments(ctx, archived.ID)
	if err != nil {
		t.Fatalf("ListSegments replayed A: %v", err)
	}
	for _, segment := range segments {
		if segment.Status != versionapp.ExtractionSegmentDone {
			t.Fatalf("replayed A left segment %d %s", segment.Seq, segment.Status)
		}
	}
	var active int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM requirement_extraction_run
		WHERE version_id = $1 AND attachment_id = $2
		  AND status IN ('pending', 'running', 'paused')
	`, versionID, attachmentID).Scan(&active); err != nil {
		t.Fatalf("count active runs: %v", err)
	}
	if active != 0 {
		t.Fatalf("active runs=%d after the replay settled, want 0 (no zombie)", active)
	}
}
