//go:build integration

package versionpg_test

// Task 18 (R-A7-10 / A-15) live half: the extraction service projects its
// terminal run into one version_plan_item clarifying milestone and the two
// append-only version_fact kinds through the real PlanRepository/FactRepository.
// Replay is idempotent (no second plan row, no second fact) and a changed input
// sha keeps the attachment's single milestone.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type milestoneProcessor struct{}

func (milestoneProcessor) ProcessSegment(_ context.Context, _ versionapp.ExtractionRun, _ versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
	return versionapp.ExtractionSegmentResult{ExtractedCount: 1}, nil
}

type milestoneSource struct {
	document versionapp.ExtractionDocument
}

// pausingMilestoneProcessor pauses the run while the already-claimed batch is
// still finishing, the pause-during-processing edge of Task 17: both segments
// settle while the run is paused, so Resume observes the terminal state.
type pausingMilestoneProcessor struct {
	svc  *versionapp.ExtractionService
	run  int64
	once sync.Once
	err  error
}

func (p *pausingMilestoneProcessor) ProcessSegment(ctx context.Context, _ versionapp.ExtractionRun, _ versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
	p.once.Do(func() {
		if _, err := p.svc.Pause(ctx, p.run); err != nil {
			p.err = err
		}
	})
	if p.err != nil {
		return versionapp.ExtractionSegmentResult{}, p.err
	}
	return versionapp.ExtractionSegmentResult{ExtractedCount: 1}, nil
}

func (milestoneSource) ExtractionInputs(_ context.Context, _ int64, _ int64) ([]versionapp.ExtractionStartInput, error) {
	return nil, nil
}

func (s milestoneSource) ExtractionDocument(_ context.Context, _ int64, _ int64, _ string) (versionapp.ExtractionDocument, error) {
	return s.document, nil
}

func milestoneDocumentText() string {
	return "# V1 当前需求（功能需求清单）\n\nFR-001 登录与下单验收。\nFR-002 退款 T+1 到账。\n"
}

func milestoneSegments() []versionapp.Segment {
	return []versionapp.Segment{
		{Seq: 1, HeadingPath: "登录", CharStart: 0, CharEnd: 10, Text: "FR-001 登录与下单验收。", TextSHA256: strings.Repeat("a", 64)},
		{Seq: 2, HeadingPath: "退款", CharStart: 11, CharEnd: 30, Text: "FR-002 退款 T+1 到账。", TextSHA256: strings.Repeat("b", 64)},
	}
}

type milestonePlanRow struct {
	id       int64
	status   string
	decision string
	revision int
}

func loadMilestonePlan(t *testing.T, pool *pgxpool.Pool, versionID int64) []milestonePlanRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, status, decision, revision FROM version_plan_item
		WHERE version_id = $1 ORDER BY id
	`, versionID)
	if err != nil {
		t.Fatalf("query plan items: %v", err)
	}
	defer rows.Close()
	items := make([]milestonePlanRow, 0, 1)
	for rows.Next() {
		var item milestonePlanRow
		if err := rows.Scan(&item.id, &item.status, &item.decision, &item.revision); err != nil {
			t.Fatalf("scan plan item: %v", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return items
}

func loadMilestoneFacts(t *testing.T, pool *pgxpool.Pool, versionID int64) []map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT kind, key, payload->>'run_id', payload->>'skipped_images', payload->>'status'
		FROM version_fact WHERE version_id = $1 ORDER BY id
	`, versionID)
	if err != nil {
		t.Fatalf("query facts: %v", err)
	}
	defer rows.Close()
	facts := make([]map[string]string, 0, 2)
	for rows.Next() {
		var kind, key, runID, skipped, status string
		if err := rows.Scan(&kind, &key, &runID, &skipped, &status); err != nil {
			t.Fatalf("scan fact: %v", err)
		}
		facts = append(facts, map[string]string{
			"kind": kind, "key": key, "run_id": runID, "skipped_images": skipped, "status": status,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("fact rows: %v", err)
	}
	return facts
}

func TestExtractionMilestoneIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	attachmentID := createExtractionAttachment(t, pool, ids, versionID)
	ctx := context.Background()

	extractionRepo := versionpg.NewExtractionRepository(pool, ids)
	svc := versionapp.NewExtractionService(extractionRepo, milestoneProcessor{}, nil, clock.System{})
	plan := versionapp.NewPlanService(versionpg.NewPlanRepository(pool), ids, nil)
	facts := versionapp.NewFactService(versionpg.NewFactRepository(pool, ids), ids, nil)
	svc.SetMilestoneStores(plan, facts)
	svc.SetSource(milestoneSource{document: versionapp.ExtractionDocument{
		Name: "需求文档.docx", Text: milestoneDocumentText(), SkippedImages: 3,
	}})
	svc.SetLeaseHeartbeat(0)

	input := versionapp.ExtractionStartInput{
		VersionID: versionID, AttachmentID: attachmentID,
		InputSHA256:  strings.Repeat("1", 64),
		Segments:     milestoneSegments(),
		DocumentText: milestoneDocumentText(),
	}
	run, err := svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	items := loadMilestonePlan(t, pool, versionID)
	if len(items) != 1 || items[0].status != versionapp.ExtractionPlanStatusRunning {
		t.Fatalf("plan after start = %#v", items)
	}
	if !strings.HasPrefix(items[0].decision, versionapp.ExtractionPlanDecisionMarker(attachmentID, run.ID)) {
		t.Fatalf("start decision = %q", items[0].decision)
	}

	done, err := svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	if done.Status != versionapp.ExtractionRunDone {
		t.Fatalf("run status = %q", done.Status)
	}
	items = loadMilestonePlan(t, pool, versionID)
	if len(items) != 1 || items[0].status != versionapp.ExtractionPlanStatusDone {
		t.Fatalf("plan after completion = %#v", items)
	}
	completedRevision := items[0].revision
	t.Logf("plan item: id=%d status=%s revision=%d decision=%q",
		items[0].id, items[0].status, items[0].revision, items[0].decision)
	if !strings.Contains(items[0].decision, "0/2") {
		t.Fatalf("completion decision = %q, want 0/2 counts", items[0].decision)
	}
	factRows := loadMilestoneFacts(t, pool, versionID)
	if len(factRows) != 2 {
		t.Fatalf("facts = %#v, want 2", factRows)
	}
	byKind := map[string]map[string]string{}
	for _, fact := range factRows {
		t.Logf("fact: kind=%s key=%s run_id=%s skipped_images=%s status=%s",
			fact["kind"], fact["key"], fact["run_id"], fact["skipped_images"], fact["status"])
		byKind[fact["kind"]] = fact
	}
	for _, kind := range []string{versionapp.ExtractionFactKindAttachmentParse, versionapp.ExtractionFactKindRequirementSource} {
		fact, ok := byKind[kind]
		if !ok {
			t.Fatalf("missing %s fact: %#v", kind, factRows)
		}
		if fact["run_id"] != platformid.Format(run.ID) {
			t.Fatalf("%s run_id = %q", kind, fact["run_id"])
		}
		if fact["skipped_images"] != "3" {
			t.Fatalf("%s skipped_images = %q, want 3", kind, fact["skipped_images"])
		}
	}

	// Replay: same run, same plan row/revision, same two facts.
	replayed, err := svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("replay Start: %v", err)
	}
	if replayed.ID != run.ID {
		t.Fatalf("replay run = %d, want %d", replayed.ID, run.ID)
	}
	if _, err := svc.ProcessRun(ctx, replayed.ID); err != nil {
		t.Fatalf("replay ProcessRun: %v", err)
	}
	items = loadMilestonePlan(t, pool, versionID)
	if len(items) != 1 || items[0].status != versionapp.ExtractionPlanStatusDone || items[0].revision != completedRevision {
		t.Fatalf("replay plan = %#v, want one done row at revision %d", items, completedRevision)
	}
	if got := len(loadMilestoneFacts(t, pool, versionID)); got != 2 {
		t.Fatalf("replay facts = %d, want 2", got)
	}

	// A changed document sha opens a new run but keeps the attachment's single
	// milestone, refreshed to the new run.
	changed := input
	changed.InputSHA256 = strings.Repeat("2", 64)
	newRun, err := svc.Start(ctx, changed)
	if err != nil {
		t.Fatalf("changed Start: %v", err)
	}
	items = loadMilestonePlan(t, pool, versionID)
	if len(items) != 1 || items[0].status != versionapp.ExtractionPlanStatusRunning {
		t.Fatalf("changed plan = %#v, want one in_progress row", items)
	}
	if !strings.HasPrefix(items[0].decision, versionapp.ExtractionPlanDecisionMarker(attachmentID, newRun.ID)) {
		t.Fatalf("changed decision = %q, want run %d", items[0].decision, newRun.ID)
	}
}

// TestExtractionMilestoneResumeIntegration is the live Resume-projection case:
// the worker finishes the claimed batch while the run is paused, and only
// Resume observes the done status. It must finalize the plan item, write both
// facts and persist the report - exactly like a worker completion.
func TestExtractionMilestoneResumeIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	attachmentID := createExtractionAttachment(t, pool, ids, versionID)
	ctx := context.Background()

	extractionRepo := versionpg.NewExtractionRepository(pool, ids)
	processor := &pausingMilestoneProcessor{}
	svc := versionapp.NewExtractionService(extractionRepo, processor, nil, clock.System{})
	svc.SetLeaseHeartbeat(0)
	plan := versionapp.NewPlanService(versionpg.NewPlanRepository(pool), ids, nil)
	facts := versionapp.NewFactService(versionpg.NewFactRepository(pool, ids), ids, nil)
	svc.SetMilestoneStores(plan, facts)
	svc.SetSource(milestoneSource{document: versionapp.ExtractionDocument{
		Name: "需求文档.docx", Text: milestoneDocumentText(), SkippedImages: 3,
	}})

	run, err := svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: versionID, AttachmentID: attachmentID,
		InputSHA256:  strings.Repeat("3", 64),
		Segments:     milestoneSegments(),
		DocumentText: milestoneDocumentText(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	processor.svc = svc
	processor.run = run.ID

	paused, err := svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	if paused.Status != versionapp.ExtractionRunPaused || paused.DoneSegments != 2 {
		t.Fatalf("paused-after-batch run = %+v, want paused with 2 done segments", paused)
	}
	items := loadMilestonePlan(t, pool, versionID)
	if len(items) != 1 || items[0].status != versionapp.ExtractionPlanStatusRunning {
		t.Fatalf("paused plan = %#v, want one in_progress row", items)
	}
	if got := len(loadMilestoneFacts(t, pool, versionID)); got != 0 {
		t.Fatalf("paused facts = %d, want 0", got)
	}

	resumed, err := svc.Resume(ctx, run.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Status != versionapp.ExtractionRunDone {
		t.Fatalf("resumed run = %+v, want done", resumed)
	}
	items = loadMilestonePlan(t, pool, versionID)
	if len(items) != 1 || items[0].status != versionapp.ExtractionPlanStatusDone {
		t.Fatalf("resumed plan = %#v, want one done row", items)
	}
	factRows := loadMilestoneFacts(t, pool, versionID)
	if len(factRows) != 2 {
		t.Fatalf("resumed facts = %#v, want 2", factRows)
	}
	for _, fact := range factRows {
		if fact["run_id"] != platformid.Format(run.ID) || fact["status"] != "done" {
			t.Fatalf("resumed fact = %#v, want run %s done", fact, platformid.Format(run.ID))
		}
	}
	stored, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Report == nil || stored.Report.Expected != 2 || stored.Report.Extracted != 0 {
		t.Fatalf("resumed run report = %+v, want persisted expected=2", stored.Report)
	}
}
