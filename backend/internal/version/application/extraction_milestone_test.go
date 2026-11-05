package versionapp_test

// Task 18 (v0.22 R-A7-10 / A-15): TDD contract for the plan/fact milestone and
// the ingest auto-enqueue hook.
//
//   - Start writes one clarifying plan item ("解析需求文档 <name>") and is
//     idempotent on replay;
//   - a terminal run writes status done/failed and the attachment_parse /
//     requirement_source facts through PlanService/FactService, revision-safe
//     (the plan item may have been rewritten between Start and completion);
//   - replaying a completed run never duplicates the plan item or the facts;
//   - EnqueueAfterIngest starts a pending run for the attachment.ingest
//     completion path and never processes it (the worker does);
//   - StartVersion resolves one attachment or every ready attachment through
//     the extraction input provider and returns typed errors without one.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// milestoneDocText declares the live acceptance shape: 58 current FR ids plus
// 32 V2 planned ids, which must never be reported missing.
func milestoneDocText() string {
	var builder strings.Builder
	builder.WriteString("# V1 当前需求（功能需求清单）\n\n")
	for index := 1; index <= 58; index++ {
		fmt.Fprintf(&builder, "FR-%03d 当前需求 %d：登录与下单验收。\n", index, index)
	}
	builder.WriteString("\n## V2 规划需求（后续版本）\n\n")
	for index := 59; index <= 90; index++ {
		fmt.Fprintf(&builder, "FR-%03d 规划需求 %d：后续版本能力。\n", index, index)
	}
	return builder.String()
}

// fakeExtractionSource is the attachment-side port Task 36/37 wires from the
// attachment service: ready inputs plus the canonical document for reconcile.
type fakeExtractionSource struct {
	inputs    []versionapp.ExtractionStartInput
	doc       versionapp.ExtractionDocument
	inputsErr error
	docErr    error
	docCalls  int
}

func (s *fakeExtractionSource) ExtractionInputs(_ context.Context, versionID, attachmentID int64) ([]versionapp.ExtractionStartInput, error) {
	if s.inputsErr != nil {
		return nil, s.inputsErr
	}
	inputs := make([]versionapp.ExtractionStartInput, 0, len(s.inputs))
	for _, input := range s.inputs {
		if input.VersionID != versionID {
			continue
		}
		if attachmentID > 0 && input.AttachmentID != attachmentID {
			continue
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func (s *fakeExtractionSource) ExtractionDocument(_ context.Context, _ int64, _ int64) (versionapp.ExtractionDocument, error) {
	s.docCalls++
	if s.docErr != nil {
		return versionapp.ExtractionDocument{}, s.docErr
	}
	return s.doc, nil
}

func newMilestoneEnv(t *testing.T) (*extractionEnv, *versionapp.PlanService, *fakePlanRepo, *versionapp.FactService, *fakeFactRepo) {
	t.Helper()
	env := newExtractionEnv(t)
	planRepo := newFakePlanRepo()
	planSvc := versionapp.NewPlanService(planRepo, &seqIDs{}, nil)
	factRepo := newFakeFactRepo()
	factSvc := versionapp.NewFactService(factRepo, &seqIDs{}, nil)
	env.svc.SetMilestoneStores(planSvc, factSvc)
	env.svc.SetSource(&fakeExtractionSource{
		doc: versionapp.ExtractionDocument{
			Name:          "需求文档.docx",
			Text:          milestoneDocText(),
			SkippedImages: 1,
		},
	})
	return env, planSvc, planRepo, factSvc, factRepo
}

func milestonePlanItems(t *testing.T, repo *fakePlanRepo, versionID int64) []version.PlanItem {
	t.Helper()
	items, err := repo.List(context.Background(), versionID, "")
	if err != nil {
		t.Fatalf("list plan items: %v", err)
	}
	return items
}

// TestExtractionWritesPlanFact pins R-A7-10: start writes the clarifying plan
// item, completion writes done plus both facts, a concurrent plan revision is
// respected, and a replay duplicates nothing.
func TestExtractionWritesPlanFact(t *testing.T) {
	env, planSvc, planRepo, _, factRepo := newMilestoneEnv(t)
	ctx := context.Background()
	input := versionapp.ExtractionStartInput{
		VersionID:      7,
		AttachmentID:   3,
		InputSHA256:    testSHA("doc"),
		Segments:       testExtractionSegments("第一段需求", "第二段需求"),
		DocumentText:   milestoneDocText(),
		AttachmentName: "需求文档.docx",
	}

	run, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 {
		t.Fatalf("plan items after start = %d, want 1: %#v", len(items), items)
	}
	item := items[0]
	if item.Phase != "clarifying" || item.Status != "in_progress" {
		t.Fatalf("start plan item phase/status = %q/%q", item.Phase, item.Status)
	}
	if item.Title != "解析需求文档 需求文档.docx" {
		t.Fatalf("start plan title = %q", item.Title)
	}
	marker := versionapp.ExtractionPlanDecisionMarker(run.AttachmentID, run.ID)
	if !strings.HasPrefix(item.Decision, marker) {
		t.Fatalf("start plan decision = %q, want marker %q", item.Decision, marker)
	}

	// A concurrent writer (the host/plan.update path) rewrites the same item
	// between start and completion: the terminal write must use the fresh
	// revision instead of failing or overwriting it.
	order := 9
	if _, err := planSvc.Update(ctx, 7, item.ID, versionapp.PlanPatch{Order: &order}, item.Revision); err != nil {
		t.Fatalf("concurrent plan update: %v", err)
	}

	done, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	if done.Status != versionapp.ExtractionRunDone {
		t.Fatalf("run status = %q, want done", done.Status)
	}
	if done.Report == nil || done.Report.Expected != 58 || done.Report.PlannedCount != 32 {
		t.Fatalf("run report = %#v, want expected=58 planned=32", done.Report)
	}

	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 {
		t.Fatalf("plan items after completion = %d, want 1", len(items))
	}
	item = items[0]
	if item.Status != "done" {
		t.Fatalf("completed plan status = %q, want done", item.Status)
	}
	if item.Revision <= 1 || item.Order != 9 {
		t.Fatalf("completed plan revision/order = %d/%d, want revision>1 order=9", item.Revision, item.Order)
	}
	if !strings.HasPrefix(item.Decision, marker) {
		t.Fatalf("completed plan decision = %q, want marker %q", item.Decision, marker)
	}

	facts := currentFactsByKind(t, factRepo, 7)
	if len(facts) != 2 {
		t.Fatalf("facts after completion = %d, want 2: %#v", len(facts), facts)
	}
	for _, kind := range []string{"attachment_parse", "requirement_source"} {
		fact, ok := facts[kind]
		if !ok {
			t.Fatalf("missing fact kind %q in %#v", kind, facts)
		}
		if fact.Key != kind+":run:"+idString(run.ID) {
			t.Fatalf("%s fact key = %q", kind, fact.Key)
		}
		if fact.Payload["run_id"] != idString(run.ID) {
			t.Fatalf("%s fact run_id = %#v", kind, fact.Payload["run_id"])
		}
		if fact.Payload["input_sha256"] != input.InputSHA256 {
			t.Fatalf("%s fact sha = %#v", kind, fact.Payload["input_sha256"])
		}
		if fact.Payload["skipped_images"] != 1 {
			t.Fatalf("%s fact skipped_images = %#v, want 1", kind, fact.Payload["skipped_images"])
		}
	}
	if facts["attachment_parse"].Payload["status"] != "done" {
		t.Fatalf("attachment_parse status = %#v", facts["attachment_parse"].Payload["status"])
	}

	// Replay: same run, same plan item, same two fact rows (content-hash dedupe),
	// and no extra plan revision.
	replayed, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("replay Start: %v", err)
	}
	if replayed.ID != run.ID {
		t.Fatalf("replay run = %d, want %d", replayed.ID, run.ID)
	}
	replayed, err = env.svc.ProcessRun(ctx, replayed.ID)
	if err != nil {
		t.Fatalf("replay ProcessRun: %v", err)
	}
	if len(milestonePlanItems(t, planRepo, 7)) != 1 {
		t.Fatalf("replay duplicated the plan item")
	}
	if got := factRepo.count(); got != 2 {
		t.Fatalf("replay fact rows = %d, want 2 (no duplicates)", got)
	}
	if replayed.Status != versionapp.ExtractionRunDone {
		t.Fatalf("replay run status = %q", replayed.Status)
	}
}

// TestExtractionFailedRunWritesFailedPlanAndParseFact pins the failure side of
// R-A7-10: the plan milestone is failed and the parse fact records the failure
// (requirement_source is only written for a completed extraction).
func TestExtractionFailedRunWritesFailedPlanAndParseFact(t *testing.T) {
	env, _, planRepo, _, factRepo := newMilestoneEnv(t)
	env.processor.failuresLeft = 3 // max attempts for the single segment
	ctx := context.Background()

	run, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID:      7,
		AttachmentID:   3,
		InputSHA256:    testSHA("doc"),
		Segments:       testExtractionSegments("唯一一段需求"),
		DocumentText:   "FR-001 当前需求",
		AttachmentName: "需求文档.docx",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		current, processErr := env.svc.ProcessRun(ctx, run.ID)
		if processErr != nil {
			t.Fatalf("ProcessRun attempt %d: %v", attempt+1, processErr)
		}
		if current.Status == versionapp.ExtractionRunFailed {
			break
		}
		env.clock.Advance(2 * time.Minute)
	}
	failed, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if failed.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("run status = %q, want failed after 3 attempts", failed.Status)
	}

	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "failed" {
		t.Fatalf("failed plan items = %#v", items)
	}

	facts := currentFactsByKind(t, factRepo, 7)
	if len(facts) != 1 {
		t.Fatalf("failed facts = %#v, want only attachment_parse", facts)
	}
	parse, ok := facts["attachment_parse"]
	if !ok {
		t.Fatalf("missing attachment_parse fact: %#v", facts)
	}
	if parse.Payload["status"] != "failed" {
		t.Fatalf("attachment_parse status = %#v", parse.Payload["status"])
	}
}

// TestExtractionPlanItemPerAttachment pins the milestone identity: re-extraction
// after a document change (new run, archived old one) updates the same plan item
// instead of leaving a stale in-progress item behind.
func TestExtractionPlanItemPerAttachment(t *testing.T) {
	env, _, planRepo, _, _ := newMilestoneEnv(t)
	ctx := context.Background()
	base := versionapp.ExtractionStartInput{
		VersionID:      7,
		AttachmentID:   3,
		Segments:       testExtractionSegments("第一段"),
		AttachmentName: "需求文档.docx",
	}
	base.InputSHA256 = testSHA("doc-v1")
	first, err := env.svc.Start(ctx, base)
	if err != nil {
		t.Fatalf("Start v1: %v", err)
	}
	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 {
		t.Fatalf("plan items after v1 = %d, want 1", len(items))
	}
	firstItemID := items[0].ID

	base.InputSHA256 = testSHA("doc-v2")
	second, err := env.svc.Start(ctx, base)
	if err != nil {
		t.Fatalf("Start v2: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("changed sha reused the run %d", first.ID)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 {
		t.Fatalf("re-extraction duplicated the plan item: %#v", items)
	}
	if items[0].ID != firstItemID || items[0].Status != "in_progress" {
		t.Fatalf("re-extraction plan item = %#v, want id=%d in_progress", items[0], firstItemID)
	}
	if !strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, second.ID)) {
		t.Fatalf("re-extraction decision = %q, want run %d", items[0].Decision, second.ID)
	}
}

// TestEnqueueAfterIngestIsIdempotent pins the auto-enqueue hook: it starts the
// pending run (plan item created) and never processes segments itself.
func TestEnqueueAfterIngestIsIdempotent(t *testing.T) {
	env, _, planRepo, _, _ := newMilestoneEnv(t)
	ctx := context.Background()
	input := versionapp.ExtractionStartInput{
		VersionID:      7,
		AttachmentID:   3,
		InputSHA256:    testSHA("doc"),
		Segments:       testExtractionSegments("第一段", "第二段"),
		AttachmentName: "需求文档.docx",
	}

	first, err := env.svc.EnqueueAfterIngest(ctx, input)
	if err != nil {
		t.Fatalf("EnqueueAfterIngest: %v", err)
	}
	if first.Status != versionapp.ExtractionRunPending {
		t.Fatalf("queued run status = %q, want pending", first.Status)
	}
	if env.processor.CallCount() != 0 {
		t.Fatalf("EnqueueAfterIngest processed %d segments, want 0", env.processor.CallCount())
	}
	second, err := env.svc.EnqueueAfterIngest(ctx, input)
	if err != nil {
		t.Fatalf("replay EnqueueAfterIngest: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("enqueue duplicated the run: %d/%d", first.ID, second.ID)
	}
	if items := milestonePlanItems(t, planRepo, 7); len(items) != 1 {
		t.Fatalf("enqueue plan items = %d, want 1", len(items))
	}

	segments, err := env.svc.ListSegments(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(segments) != 2 || segments[0].Seq != 1 || segments[1].Seq != 2 {
		t.Fatalf("segments = %#v", segments)
	}
}

// TestStartVersionResolvesReadyAttachments pins the "attachment_id or all
// attachments" start contract and its typed failures.
func TestStartVersionResolvesReadyAttachments(t *testing.T) {
	env, _, planRepo, _, _ := newMilestoneEnv(t)
	ctx := context.Background()
	env.svc.SetSource(&fakeExtractionSource{
		inputs: []versionapp.ExtractionStartInput{
			{
				VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("a"),
				Segments: testExtractionSegments("A"), AttachmentName: "a.docx",
			},
			{
				VersionID: 7, AttachmentID: 4, InputSHA256: testSHA("b"),
				Segments: testExtractionSegments("B"), AttachmentName: "b.docx",
			},
			{
				VersionID: 8, AttachmentID: 5, InputSHA256: testSHA("c"),
				Segments: testExtractionSegments("C"), AttachmentName: "c.docx",
			},
		},
	})

	runs, err := env.svc.StartVersion(ctx, 7, 0)
	if err != nil {
		t.Fatalf("StartVersion all: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("all runs = %d, want 2", len(runs))
	}
	if items := milestonePlanItems(t, planRepo, 7); len(items) != 2 {
		t.Fatalf("all plan items = %d, want 2", len(items))
	}

	runs, err = env.svc.StartVersion(ctx, 7, 4)
	if err != nil {
		t.Fatalf("StartVersion one: %v", err)
	}
	if len(runs) != 1 || runs[0].AttachmentID != 4 {
		t.Fatalf("one runs = %#v", runs)
	}

	// No ready attachment: typed 404 for a specific attachment, 409 for all.
	if _, err := env.svc.StartVersion(ctx, 7, 99); err == nil || platform.AsError(err).Status != 404 {
		t.Fatalf("unknown attachment error = %v", err)
	}
	empty := &fakeExtractionSource{}
	env.svc.SetSource(empty)
	if _, err := env.svc.StartVersion(ctx, 7, 0); err == nil || platform.AsError(err).Status != 409 {
		t.Fatalf("no-ready error = %v", err)
	}

	// A missing provider is a typed 503, never a silent empty success.
	env.svc.SetSource(nil)
	if _, err := env.svc.StartVersion(ctx, 7, 3); err == nil || platform.AsError(err).Status != 503 {
		t.Fatalf("no-provider error = %v", err)
	}
}

func currentFactsByKind(t *testing.T, repo *fakeFactRepo, versionID int64) map[string]version.Fact {
	t.Helper()
	facts, err := repo.Current(context.Background(), versionID, "")
	if err != nil {
		t.Fatalf("current facts: %v", err)
	}
	byKind := make(map[string]version.Fact, len(facts))
	for _, fact := range facts {
		byKind[fact.Kind] = fact
	}
	return byKind
}
