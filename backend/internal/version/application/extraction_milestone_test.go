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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	inputs []versionapp.ExtractionStartInput
	// doc is the attachment's current document (used when no per-sha document
	// is registered). docs registers the document of one input_sha256, so a
	// test can prove the terminal projection reconciles the run's own document
	// instead of the attachment's current one.
	doc       versionapp.ExtractionDocument
	docs      map[string]versionapp.ExtractionDocument
	inputsErr error
	docErr    error
	// docCalls and docSHAs record which documents were requested.
	docCalls int
	docSHAs  []string
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

func (s *fakeExtractionSource) ExtractionDocument(_ context.Context, _ int64, _ int64, inputSHA256 string) (versionapp.ExtractionDocument, error) {
	s.docCalls++
	s.docSHAs = append(s.docSHAs, inputSHA256)
	if s.docErr != nil {
		return versionapp.ExtractionDocument{}, s.docErr
	}
	if s.docs != nil {
		if document, ok := s.docs[inputSHA256]; ok {
			return document, nil
		}
		// No document registered for this run sha: fall back to the
		// attachment's current document, like the production adapter would
		// when the run identity is not propagated.
		if s.doc.Name != "" || s.doc.Text != "" {
			return s.doc, nil
		}
		return versionapp.ExtractionDocument{}, platform.NotFound("requirement attachment document")
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

// TestResumeAfterPauseFinalizesMilestone mirrors the Task 17 resume contract
// (TestResumeAfterPauseFinalizesCompletedRun) on the projection side: the
// worker finishes the already-claimed batch while the run is paused, so Resume
// is the operation that observes the terminal state. It must finalize the plan
// milestone, write both facts and persist the reconciliation report.
func TestResumeAfterPauseFinalizesMilestone(t *testing.T) {
	env, _, planRepo, _, factRepo := newMilestoneEnv(t)
	ctx := context.Background()
	input := versionapp.ExtractionStartInput{
		VersionID:      7,
		AttachmentID:   3,
		InputSHA256:    testSHA("resume-milestone"),
		Segments:       testExtractionSegments("段一", "段二"),
		DocumentText:   milestoneDocText(),
		AttachmentName: "需求文档.docx",
	}
	run, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var pauseOnce sync.Once
	env.processor.process = func(versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		pauseOnce.Do(func() {
			if _, pauseErr := env.svc.Pause(ctx, run.ID); pauseErr != nil {
				t.Errorf("Pause during processing: %v", pauseErr)
			}
		})
		return versionapp.ExtractionSegmentResult{}, nil
	}
	processed, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	if processed.Status != versionapp.ExtractionRunPaused || processed.DoneSegments != 2 {
		t.Fatalf("paused-after-batch run=%+v", processed)
	}

	// The paused run is not terminal yet: the milestone must still be open and
	// no completion fact may exist before Resume observes the settled cursor.
	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "in_progress" {
		t.Fatalf("paused plan items = %#v, want one in_progress", items)
	}
	if got := factRepo.count(); got != 0 {
		t.Fatalf("paused run facts = %d, want 0", got)
	}

	resumed, err := env.svc.Resume(ctx, run.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Status != versionapp.ExtractionRunDone || resumed.DoneSegments != 2 {
		t.Fatalf("resumed completed run=%+v", resumed)
	}

	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "done" {
		t.Fatalf("resumed plan items = %#v, want one done", items)
	}
	if !strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, run.ID)) {
		t.Fatalf("resumed plan decision = %q, want run %d", items[0].Decision, run.ID)
	}

	facts := currentFactsByKind(t, factRepo, 7)
	if len(facts) != 2 {
		t.Fatalf("resumed facts = %#v, want attachment_parse and requirement_source", facts)
	}
	for _, kind := range []string{"attachment_parse", "requirement_source"} {
		fact, ok := facts[kind]
		if !ok {
			t.Fatalf("missing %s fact after resume: %#v", kind, facts)
		}
		if fact.Key != kind+":run:"+idString(run.ID) || fact.Payload["status"] != "done" {
			t.Fatalf("%s fact after resume = %#v", kind, fact)
		}
	}

	stored, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Report == nil || stored.Report.Expected != 58 || stored.Report.PlannedCount != 32 {
		t.Fatalf("resumed run report = %#v, want persisted 58 expected / 32 planned", stored.Report)
	}
}

// TestConcurrentStartWritesSinglePlanItem pins the first-write race: two
// concurrent Start calls for the same attachment must serialize the milestone
// projection (version+attachment advisory lock / find-or-create) instead of
// both listing an empty plan and inserting a duplicate milestone item.
func TestConcurrentStartWritesSinglePlanItem(t *testing.T) {
	env := newExtractionEnv(t)
	planRepo := newConcurrentPlanRepo()
	planSvc := versionapp.NewPlanService(planRepo, &concurrentIDs{}, nil)
	env.svc.SetMilestoneStores(planSvc, nil)

	input := versionapp.ExtractionStartInput{
		VersionID:    7,
		AttachmentID: 3,
		InputSHA256:  testSHA("concurrent-plan"),
		Segments:     testExtractionSegments("段一", "段二"),
	}
	const workers = 2
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	runIDs := make(chan int64, workers)
	begin := make(chan struct{})
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-begin
			run, startErr := env.svc.Start(context.Background(), input)
			if startErr != nil {
				errs <- startErr
				return
			}
			errs <- nil
			runIDs <- run.ID
		}()
	}
	close(begin)
	wait.Wait()
	close(errs)
	close(runIDs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Start: %v", err)
		}
	}
	firstRun := <-runIDs
	for runID := range runIDs {
		if runID != firstRun {
			t.Fatalf("concurrent Start created runs %d and %d, want one", firstRun, runID)
		}
	}

	items, err := planRepo.List(context.Background(), 7, "")
	if err != nil {
		t.Fatalf("list plan items: %v", err)
	}
	milestones := 0
	for _, item := range items {
		if strings.HasPrefix(item.Decision, versionapp.ExtractionPlanDecisionMarkerPrefix) {
			milestones++
		}
	}
	if milestones != 1 {
		t.Fatalf("concurrent first writes created %d milestone items, want 1: %#v", milestones, items)
	}
}

// TestRetryRunReopensPlanMilestone pins "the plan follows the latest run":
// RetryRun resets the failed cursor to pending, so the milestone must leave the
// failed state and reopen; a later completion finalizes it again with both
// facts.
func TestRetryRunReopensPlanMilestone(t *testing.T) {
	env, _, planRepo, _, factRepo := newMilestoneEnv(t)
	ctx := context.Background()
	run := startFailedMilestoneRun(t, env)

	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "failed" {
		t.Fatalf("failed plan items = %#v", items)
	}

	env.processor.failuresLeft = 0
	retried, err := env.svc.RetryRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("RetryRun: %v", err)
	}
	if retried.Status != versionapp.ExtractionRunPending {
		t.Fatalf("retried run status = %q, want pending", retried.Status)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "in_progress" {
		t.Fatalf("retried plan items = %#v, want one in_progress", items)
	}
	if !strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, run.ID)) {
		t.Fatalf("retried plan decision = %q, want run %d", items[0].Decision, run.ID)
	}

	done, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun after retry: %v", err)
	}
	if done.Status != versionapp.ExtractionRunDone {
		t.Fatalf("run after retry = %q, want done", done.Status)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "done" {
		t.Fatalf("completed plan items after retry = %#v", items)
	}
	facts := currentFactsByKind(t, factRepo, 7)
	if len(facts) != 2 || facts["attachment_parse"].Payload["status"] != "done" {
		t.Fatalf("facts after retry completion = %#v", facts)
	}
}

// TestRetrySegmentReopensPlanMilestone pins the segment-level retry variant:
// retrying the failed segment reopens the failed plan item as in_progress.
func TestRetrySegmentReopensPlanMilestone(t *testing.T) {
	env, _, planRepo, _, _ := newMilestoneEnv(t)
	ctx := context.Background()
	run := startFailedMilestoneRun(t, env)
	env.processor.failuresLeft = 0

	retried, err := env.svc.RetrySegment(ctx, run.ID, 1)
	if err != nil {
		t.Fatalf("RetrySegment: %v", err)
	}
	if retried.Status != versionapp.ExtractionRunPending {
		t.Fatalf("retried segment run status = %q, want pending", retried.Status)
	}
	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "in_progress" {
		t.Fatalf("segment-retried plan items = %#v, want one in_progress", items)
	}
}

// TestTerminalMilestoneRefreshesPlanTitle pins the title refresh: the start
// name may be stale (renamed attachment), so the terminal projection refreshes
// the plan title from the run's document while keeping the attachment identity.
func TestTerminalMilestoneRefreshesPlanTitle(t *testing.T) {
	env, _, planRepo, _, _ := newMilestoneEnv(t)
	ctx := context.Background()
	env.svc.SetSource(&fakeExtractionSource{
		doc: versionapp.ExtractionDocument{
			Name: "重命名后.docx", Text: milestoneDocText(), SkippedImages: 1,
		},
	})
	run, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID:      7,
		AttachmentID:   3,
		InputSHA256:    testSHA("title-refresh"),
		Segments:       testExtractionSegments("段一"),
		DocumentText:   milestoneDocText(),
		AttachmentName: "旧名称.docx",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Title != "解析需求文档 旧名称.docx" {
		t.Fatalf("start plan title = %#v", items)
	}
	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Title != "解析需求文档 重命名后.docx" {
		t.Fatalf("terminal plan title = %#v, want the run document name", items)
	}
	if !strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, run.ID)) {
		t.Fatalf("terminal plan decision = %q, want run %d", items[0].Decision, run.ID)
	}
}

// TestTerminalReconcilePersistsReportWithoutPlanOrFactStores pins the
// source-only assembly: reconciliation is independent of the plan/fact
// projection, so wiring just the extraction source still persists the report
// read by the progress API.
func TestTerminalReconcilePersistsReportWithoutPlanOrFactStores(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	env.svc.SetSource(&fakeExtractionSource{
		doc: versionapp.ExtractionDocument{
			Name: "需求文档.docx", Text: milestoneDocText(), SkippedImages: 1,
		},
	})
	run, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("source-only"),
		Segments: testExtractionSegments("段一"),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	if done.Status != versionapp.ExtractionRunDone {
		t.Fatalf("run status = %q, want done", done.Status)
	}
	stored, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Report == nil || stored.Report.Expected != 58 || stored.Report.SkippedImages != 1 {
		t.Fatalf("source-only persisted report = %#v, want 58 expected / 1 skipped", stored.Report)
	}
}

// TestMilestoneSupersedeFenceBoundary pins the archive fence boundary: an
// archived older run must never overwrite the live milestone, even after the
// newer run settled, unless Start deliberately revived that older run (replay
// of its sha), in which case the revived run owns the milestone again.
func TestMilestoneSupersedeFenceBoundary(t *testing.T) {
	env, _, planRepo, _, factRepo := newMilestoneEnv(t)
	ctx := context.Background()
	inputA := versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("fence-a"),
		Segments: testExtractionSegments("A段一", "A段二"), DocumentText: milestoneDocText(),
		AttachmentName: "需求文档.docx",
	}
	inputB := versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("fence-b"),
		Segments: testExtractionSegments("B段一"), DocumentText: milestoneDocText(),
		AttachmentName: "需求文档.docx",
	}
	a, err := env.svc.Start(ctx, inputA)
	if err != nil {
		t.Fatalf("Start A: %v", err)
	}
	b, err := env.svc.Start(ctx, inputB)
	if err != nil {
		t.Fatalf("Start B: %v", err)
	}
	archivedA, err := env.svc.GetRun(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetRun archived A: %v", err)
	}
	if archivedA.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("A after B Start status = %q, want failed", archivedA.Status)
	}
	if _, err := env.svc.ProcessRun(ctx, b.ID); err != nil {
		t.Fatalf("ProcessRun B: %v", err)
	}
	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "done" ||
		!strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, b.ID)) {
		t.Fatalf("settled B plan = %#v", items)
	}
	settledFacts := factRepo.count()
	if settledFacts != 2 {
		t.Fatalf("settled B facts = %d, want 2", settledFacts)
	}

	// Processing the archived run (B already settled, so the old "newer active
	// run" fence alone would allow it) must not touch the live milestone.
	processedA, err := env.svc.ProcessRun(ctx, a.ID)
	if err != nil {
		t.Fatalf("ProcessRun archived A: %v", err)
	}
	if processedA.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("archived A run status = %q, want failed", processedA.Status)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "done" ||
		!strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, b.ID)) {
		t.Fatalf("archived A overwrote the milestone: %#v", items)
	}
	if got := factRepo.count(); got != settledFacts {
		t.Fatalf("archived A appended facts: %d, want %d", got, settledFacts)
	}

	// Start intentionally revives A (its sha replay wins over the settled B):
	// the revived run now owns the milestone and can finalize it.
	revived, err := env.svc.Start(ctx, inputA)
	if err != nil {
		t.Fatalf("revive A: %v", err)
	}
	if revived.ID != a.ID || revived.Status != versionapp.ExtractionRunPending {
		t.Fatalf("revived A = %#v (run %d), want pending run %d", revived, revived.ID, a.ID)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "in_progress" ||
		!strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, a.ID)) {
		t.Fatalf("revived A plan = %#v", items)
	}
	// Re-running the settled B after the revival must not steal the milestone
	// back: Start deliberately nominated A as the live generation.
	if _, err := env.svc.ProcessRun(ctx, b.ID); err != nil {
		t.Fatalf("ProcessRun settled B after revival: %v", err)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "in_progress" ||
		!strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, a.ID)) {
		t.Fatalf("settled B stole the revived milestone: %#v", items)
	}
	if got := factRepo.count(); got != settledFacts {
		t.Fatalf("settled B appended facts after revival: %d, want %d", got, settledFacts)
	}
	if _, err := env.svc.ProcessRun(ctx, a.ID); err != nil {
		t.Fatalf("ProcessRun revived A: %v", err)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "done" ||
		!strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, a.ID)) {
		t.Fatalf("revived A did not own the milestone: %#v", items)
	}
	facts := currentFactsByKind(t, factRepo, 7)
	if len(facts) != 2 {
		t.Fatalf("revived A facts = %#v", facts)
	}
	for _, kind := range []string{"attachment_parse", "requirement_source"} {
		if facts[kind].Key != kind+":run:"+idString(a.ID) {
			t.Fatalf("%s fact key after revival = %q, want run %d", kind, facts[kind].Key, a.ID)
		}
	}
}

// TestMilestoneUsesReportExtractedCount pins the count source: once the
// reconciliation report exists, the plan/fact projection reads
// report.Extracted (the acceptance count over declared requirements), not the
// denormalized run.extracted_count (which counts requirements outside the
// document too).
func TestMilestoneUsesReportExtractedCount(t *testing.T) {
	base := newFakeExtractionRepo()
	repo := &reportOnlyReconciliationRepo{fakeExtractionRepo: base}
	clock := &stepClock{at: testTime}
	processor := &recordingProcessor{}
	svc := versionapp.NewExtractionService(repo, processor, nil, clock)
	planRepo := newFakePlanRepo()
	planSvc := versionapp.NewPlanService(planRepo, &seqIDs{}, nil)
	factRepo := newFakeFactRepo()
	factSvc := versionapp.NewFactService(factRepo, &seqIDs{}, nil)
	svc.SetMilestoneStores(planSvc, factSvc)
	svc.SetSource(&fakeExtractionSource{doc: versionapp.ExtractionDocument{
		Name: "需求文档.docx", Text: "# V1 当前需求\n\nFR-001 登录\n", SkippedImages: 1,
	}})
	env := &extractionEnv{svc: svc, repo: base, clock: clock, processor: processor}
	ctx := context.Background()

	run, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("report-count"),
		Segments:     testExtractionSegments("需求段落"),
		DocumentText: "# V1 当前需求\n\nFR-001 登录\n",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env.processor.process = func(segment versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		_, emitErr := env.svc.EmitExtractedRequirements(ctx, versionapp.ExtractionSource{
			VersionID: 7, AttachmentID: 3, RunID: segment.RunID, SegmentSeq: segment.Seq,
		}, []versionapp.ExtractionRequirement{
			{ExternalID: "FR-001", Title: "登录", Confidence: 0.9},
			{ExternalID: "FR-099", Title: "文档外需求", Confidence: 0.9},
		})
		if emitErr != nil {
			return versionapp.ExtractionSegmentResult{}, emitErr
		}
		return versionapp.ExtractionSegmentResult{ExtractedCount: 2}, nil
	}
	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}

	stored, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Report == nil || stored.Report.Extracted != 1 || stored.ExtractedCount != 2 {
		t.Fatalf("run report/extracted = %#v/%d, want report extracted=1 run count=2", stored.Report, stored.ExtractedCount)
	}
	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || !strings.Contains(items[0].Decision, "已抽取 1/1 条") {
		t.Fatalf("plan decision = %#v, want report extracted count 1/1", items)
	}
	facts := currentFactsByKind(t, factRepo, 7)
	if facts["attachment_parse"].Payload["extracted"] != 1 {
		t.Fatalf("parse fact extracted = %#v, want 1 from the report", facts["attachment_parse"].Payload["extracted"])
	}
}

// TestTerminalMilestoneReconcilesRunDocument pins the superseded-sha revival
// path: the run's own parsed document (identified by its input_sha256) must be
// reconciled, not the attachment's current document. Reviving run A after run B
// settled and completing A must report A's expected count, not B's.
func TestTerminalMilestoneReconcilesRunDocument(t *testing.T) {
	env, _, planRepo, _, factRepo := newMilestoneEnv(t)
	ctx := context.Background()
	docA := "# V1 当前需求\n\nFR-001 登录\n"
	docB := "# V1 当前需求\n\nFR-001 登录\nFR-002 退出\n"
	env.svc.SetSource(&fakeExtractionSource{
		// The attachment's current document is B's, while run A's input sha was
		// parsed from docA. The source returns per-run documents when the
		// service passes the run sha, so the wrong lookup is observable.
		doc: versionapp.ExtractionDocument{Name: "需求文档-v2.docx", Text: docB, SkippedImages: 2},
		docs: map[string]versionapp.ExtractionDocument{
			testSHA("doc-v1"): {Name: "需求文档-v1.docx", Text: docA, SkippedImages: 1},
			testSHA("doc-v2"): {Name: "需求文档-v2.docx", Text: docB, SkippedImages: 2},
		},
	})
	inputA := versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("doc-v1"),
		Segments: testExtractionSegments("A段一", "A段二"), DocumentText: docA,
	}
	inputB := versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("doc-v2"),
		Segments: testExtractionSegments("B段一"), DocumentText: docB,
	}
	a, err := env.svc.Start(ctx, inputA)
	if err != nil {
		t.Fatalf("Start A: %v", err)
	}
	b, err := env.svc.Start(ctx, inputB)
	if err != nil {
		t.Fatalf("Start B: %v", err)
	}
	if _, err := env.svc.ProcessRun(ctx, b.ID); err != nil {
		t.Fatalf("ProcessRun B: %v", err)
	}

	// A's sha replay revives A once B has settled.
	revived, err := env.svc.Start(ctx, inputA)
	if err != nil {
		t.Fatalf("revive A: %v", err)
	}
	if revived.ID != a.ID || revived.Status != versionapp.ExtractionRunPending {
		t.Fatalf("revived A = %#v (run %d), want pending run %d", revived, revived.ID, a.ID)
	}
	done, err := env.svc.ProcessRun(ctx, a.ID)
	if err != nil {
		t.Fatalf("ProcessRun revived A: %v", err)
	}
	if done.Status != versionapp.ExtractionRunDone {
		t.Fatalf("revived A status = %q, want done", done.Status)
	}

	stored, err := env.svc.GetRun(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetRun A: %v", err)
	}
	if stored.Report == nil || stored.Report.Expected != 1 || stored.Report.SkippedImages != 1 {
		t.Fatalf("revived A report = %#v, want A's document (1 expected, 1 skipped)", stored.Report)
	}
	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || !strings.Contains(items[0].Decision, "已抽取 0/1 条") {
		t.Fatalf("revived A plan = %#v, want A's document count 0/1", items)
	}
	if !strings.Contains(items[0].Title, "需求文档-v1.docx") {
		t.Fatalf("revived A plan title = %q, want A's document name", items[0].Title)
	}
	facts := currentFactsByKind(t, factRepo, 7)
	parse, ok := facts["attachment_parse"]
	if !ok || parse.Payload["expected"] != 1 || parse.Payload["skipped_images"] != 1 {
		t.Fatalf("revived A parse fact = %#v, want A's document counts", parse)
	}
}

// TestFactsOnlyMilestoneFenceRejectsArchivedRun pins the degraded assembly with
// facts but no plan store: without a Decision marker the process-local Start
// selection still fences an archived run, so an old worker cannot append facts
// for a settled newer generation.
func TestFactsOnlyMilestoneFenceRejectsArchivedRun(t *testing.T) {
	env, _, _, factSvc, factRepo := newMilestoneEnv(t)
	env.svc.SetMilestoneStores(nil, factSvc)
	ctx := context.Background()
	a, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("facts-only-a"),
		Segments: testExtractionSegments("A段一"), DocumentText: milestoneDocText(),
	})
	if err != nil {
		t.Fatalf("Start A: %v", err)
	}
	b, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("facts-only-b"),
		Segments: testExtractionSegments("B段一"), DocumentText: milestoneDocText(),
	})
	if err != nil {
		t.Fatalf("Start B: %v", err)
	}
	if _, err := env.svc.ProcessRun(ctx, b.ID); err != nil {
		t.Fatalf("ProcessRun B: %v", err)
	}
	settled := factRepo.count()
	if settled != 2 {
		t.Fatalf("settled B facts = %d, want 2", settled)
	}
	if _, err := env.svc.ProcessRun(ctx, a.ID); err != nil {
		t.Fatalf("ProcessRun archived A: %v", err)
	}
	if got := factRepo.count(); got != settled {
		t.Fatalf("archived A appended facts without a plan marker: %d, want %d", got, settled)
	}
	facts := currentFactsByKind(t, factRepo, 7)
	for _, kind := range []string{"attachment_parse", "requirement_source"} {
		if facts[kind].Key != kind+":run:"+idString(b.ID) {
			t.Fatalf("%s fact key = %q, want run %d", kind, facts[kind].Key, b.ID)
		}
	}
}

// TestMilestonePlanRevisionConflictRetries pins the stale-revision path: a
// concurrent plan writer between the milestone List and Update must not drop
// the projection; the writer re-lists and applies the terminal status.
func TestMilestonePlanRevisionConflictRetries(t *testing.T) {
	env, planSvc, planRepo, factSvc, factRepo := newMilestoneEnv(t)
	ctx := context.Background()
	run, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("conflict"),
		Segments: testExtractionSegments("段一"), DocumentText: milestoneDocText(),
		AttachmentName: "需求文档.docx",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 {
		t.Fatalf("plan items after start = %d", len(items))
	}
	// A concurrent writer (the host plan.update path) bumps the revision.
	order := 7
	if _, err := planSvc.Update(ctx, 7, items[0].ID, versionapp.PlanPatch{Order: &order}, items[0].Revision); err != nil {
		t.Fatalf("concurrent plan update: %v", err)
	}
	stale := &stalePlanRevisionStore{inner: planSvc, staleRemaining: 1}
	env.svc.SetMilestoneStores(stale, factSvc)

	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	items = milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "done" || items[0].Order != 7 {
		t.Fatalf("plan after conflict retry = %#v, want done with order 7", items)
	}
	if items[0].Revision <= 2 {
		t.Fatalf("plan revision = %d, want the conflict retry to write a new revision", items[0].Revision)
	}
	if got := len(currentFactsByKind(t, factRepo, 7)); got != 2 {
		t.Fatalf("facts after conflict retry = %d, want 2", got)
	}
}

// stalePlanRevisionStore wraps the plan store and returns a stale revision on
// its first List, simulating a writer that changed the row between List and
// Update.
type stalePlanRevisionStore struct {
	inner versionapp.ExtractionPlanStore
	mu    sync.Mutex
	// staleRemaining is how many List calls still report a stale revision.
	staleRemaining int
}

func (s *stalePlanRevisionStore) List(ctx context.Context, versionID int64, status string) ([]version.PlanItem, error) {
	items, err := s.inner.List(ctx, versionID, status)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.staleRemaining > 0 && len(items) > 0 {
		s.staleRemaining--
		items[0].Revision--
	}
	return items, nil
}

func (s *stalePlanRevisionStore) Upsert(ctx context.Context, item version.PlanItem) (version.PlanItem, error) {
	return s.inner.Upsert(ctx, item)
}

func (s *stalePlanRevisionStore) Update(ctx context.Context, versionID, itemID int64, patch versionapp.PlanPatch, revision int) (version.PlanItem, error) {
	return s.inner.Update(ctx, versionID, itemID, patch, revision)
}

var _ versionapp.ExtractionPlanStore = (*stalePlanRevisionStore)(nil)

// startFailedMilestoneRun drives one single-segment run to the terminal failed
// state (three failed attempts) so the retry tests start from a real failure.
func startFailedMilestoneRun(t *testing.T, env *extractionEnv) versionapp.ExtractionRun {
	t.Helper()
	ctx := context.Background()
	run, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("retry-milestone"),
		Segments: testExtractionSegments("失败段落"), AttachmentName: "需求文档.docx",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env.processor.failuresLeft = versionapp.DefaultExtractionMaxAttempts
	for attempt := 0; attempt < versionapp.DefaultExtractionMaxAttempts; attempt++ {
		if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
			t.Fatalf("ProcessRun attempt %d: %v", attempt+1, err)
		}
		env.clock.Advance(2 * time.Minute)
	}
	stored, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("run status = %q, want failed", stored.Status)
	}
	return stored
}

// reportOnlyReconciliationRepo persists the reconciliation report but leaves
// the denormalized run.extracted_count untouched, so a test can prove the
// milestone projection reads the audit report instead of the run column.
type reportOnlyReconciliationRepo struct {
	*fakeExtractionRepo
}

func (r *reportOnlyReconciliationRepo) SaveReconciliation(_ context.Context, runID int64, report versionapp.ReconciliationReport, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return versionapp.ErrExtractionRunNotFound
	}
	stored := report
	stored.MissingIDs = append([]string(nil), report.MissingIDs...)
	stored.PlannedIDs = append([]string(nil), report.PlannedIDs...)
	stored.CurrentIDs = append([]string(nil), report.CurrentIDs...)
	run.Report = &stored
	run.UpdatedAt = now
	return nil
}

// concurrentPlanRepo is a thread-safe version.PlanRepository whose first List
// waits briefly for a second concurrent List: without a version+attachment
// milestone lock both first writers observe an empty plan and insert
// duplicate items.
type concurrentPlanRepo struct {
	mu    sync.Mutex
	items map[int64]version.PlanItem
	lists int
}

func newConcurrentPlanRepo() *concurrentPlanRepo {
	return &concurrentPlanRepo{items: map[int64]version.PlanItem{}}
}

func (r *concurrentPlanRepo) List(_ context.Context, versionID int64, status string) ([]version.PlanItem, error) {
	r.mu.Lock()
	r.lists++
	waitForPeer := r.lists == 1
	// Snapshot while holding the lock: the first writer observes the empty plan
	// before it parks, so an un-serialized second writer also observes it empty.
	items := make([]version.PlanItem, 0, len(r.items))
	for _, item := range r.items {
		if item.VersionID != versionID {
			continue
		}
		if status != "" && item.Status != status {
			continue
		}
		items = append(items, item)
	}
	r.mu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Order != items[j].Order {
			return items[i].Order < items[j].Order
		}
		return items[i].ID < items[j].ID
	})
	if !waitForPeer {
		return items, nil
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		arrived := r.lists
		r.mu.Unlock()
		if arrived >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return items, nil
}

func (r *concurrentPlanRepo) Upsert(_ context.Context, item version.PlanItem) (version.PlanItem, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.items[item.ID]
	if ok && samePlanItem(existing, item) {
		return existing, false, nil
	}
	if ok {
		item.Revision = existing.Revision + 1
	} else {
		item.Revision = 1
	}
	r.items[item.ID] = item
	return item, true, nil
}

func (r *concurrentPlanRepo) Update(_ context.Context, versionID, itemID int64, patch version.PlanPatch, expectedRevision int) (version.PlanItem, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.items[itemID]
	if !ok || existing.VersionID != versionID {
		return version.PlanItem{}, false, version.ErrPlanNotFound
	}
	if existing.Revision != expectedRevision {
		return version.PlanItem{}, false, &version.PlanRevisionConflictError{
			ItemID: itemID, Expected: expectedRevision, Actual: existing.Revision,
		}
	}
	updated := patch.Apply(existing)
	if samePlanItem(existing, updated) {
		return existing, false, nil
	}
	updated.Revision = existing.Revision + 1
	r.items[itemID] = updated
	return updated, true, nil
}

var _ version.PlanRepository = (*concurrentPlanRepo)(nil)

// concurrentIDs is a concurrency-safe platformid.Generator for the plan repo
// used by the concurrent first-write test.
type concurrentIDs struct{ next atomic.Int64 }

func (g *concurrentIDs) Next() int64 { return g.next.Add(1) }

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
