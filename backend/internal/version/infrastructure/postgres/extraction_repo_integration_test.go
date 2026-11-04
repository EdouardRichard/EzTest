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

	// Duplicate external_id updates the existing requirement, never inserts.
	source := versionapp.ExtractionSource{
		VersionID: versionID, AttachmentID: attachmentID, RunID: third.ID, SegmentSeq: 1,
	}
	requirement := versionapp.ExtractionRequirement{
		ExternalID: "FR-001", Title: "登录", Description: "手机号登录",
		AcceptanceCriteria: []string{"验证码正确"}, Priority: "P0",
		Category: "functional", SourceQuote: "FR-001 登录", Confidence: 0.9,
	}
	counts, err := repo.UpsertExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{requirement})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	if counts.Created != 1 || counts.Updated != 0 {
		t.Fatalf("first upsert counts=%+v", counts)
	}
	requirement.Title = "手机号登录"
	requirement.Confidence = 0.7
	counts, err = repo.UpsertExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{requirement})
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

	// Reconciliation counts persist on the run.
	if err := repo.SaveReconciliation(ctx, third.ID, 58, 1, now); err != nil {
		t.Fatalf("SaveReconciliation: %v", err)
	}
	reconciled, err := repo.GetRun(ctx, third.ID)
	if err != nil {
		t.Fatalf("GetRun reconciled: %v", err)
	}
	if reconciled.ExpectedCount == nil || *reconciled.ExpectedCount != 58 || reconciled.ExtractedCount != 1 {
		t.Fatalf("reconciled run=%+v", reconciled)
	}

	// Missing run is a typed not-found.
	if _, err := repo.GetRun(ctx, ids.Next()); !errors.Is(err, versionapp.ErrExtractionRunNotFound) {
		t.Fatalf("GetRun missing err=%v, want ErrExtractionRunNotFound", err)
	}
}
