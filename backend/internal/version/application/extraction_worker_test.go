package versionapp_test

// TDD contract for v0.22 A-7 extraction worker (R-A7-6..R-A7-9, A §7.6/§7.7):
//   - run identity (version_id, attachment_id, input_sha256) is idempotent and a
//     changed sha archives the previous run; segments are inserted once;
//   - failed segments retry with exponential backoff up to 3 attempts, a stale
//     running lease returns to pending, pause/resume are typed operations;
//   - the requirement sink upserts by (version_id, external_id) and updates
//     instead of inserting a duplicate;
//   - reconciliation counts declared FR ids (regex + heading second pass),
//     splits V1 current from V2 planned ids and reports expected/extracted/
//     missing_ids/low_confidence/skipped_images;
//   - two workers never process the same segment twice.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// ---------- clock / processor / publisher doubles ----------

type stepClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *stepClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(delta)
}

type recordingProcessor struct {
	mu           sync.Mutex
	calls        []int
	failuresLeft int
	process      func(versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error)
}

func (p *recordingProcessor) ProcessSegment(_ context.Context, _ versionapp.ExtractionRun, segment versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, segment.Seq)
	failures := p.failuresLeft > 0
	if failures {
		p.failuresLeft--
	}
	process := p.process
	p.mu.Unlock()
	if failures {
		return versionapp.ExtractionSegmentResult{}, errors.New("model extraction failed")
	}
	if process != nil {
		return process(segment)
	}
	return versionapp.ExtractionSegmentResult{ExtractedCount: 1}, nil
}

func (p *recordingProcessor) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *recordingProcessor) Calls() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.calls...)
}

type recordingPublisher struct {
	mu     sync.Mutex
	events []string
}

func (p *recordingPublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event.Type)
	return nil
}

func (p *recordingPublisher) Types() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.events...)
}

// ---------- in-memory extraction repository ----------

type fakeRequirement struct {
	VersionID   int64
	ExternalID  string
	Title       string
	Description string
	Criteria    []string
	Priority    string
	Category    string
	SourceQuote string
	Confidence  float64
	Constraints json.RawMessage
	RunID       int64
}

type fakeExtractionRepo struct {
	mu               sync.Mutex
	nextID           int64
	runs             map[int64]*versionapp.ExtractionRun
	segments         map[int64][]versionapp.ExtractionSegment
	runByKey         map[string]int64
	requirements     map[string]*fakeRequirement
	requirementOrder []string
}

func newFakeExtractionRepo() *fakeExtractionRepo {
	return &fakeExtractionRepo{
		runs:         map[int64]*versionapp.ExtractionRun{},
		segments:     map[int64][]versionapp.ExtractionSegment{},
		runByKey:     map[string]int64{},
		requirements: map[string]*fakeRequirement{},
	}
}

var _ versionapp.ExtractionRepository = (*fakeExtractionRepo)(nil)

func fakeRunKey(versionID, attachmentID int64, sha string) string {
	return fmt.Sprintf("%d:%d:%s", versionID, attachmentID, sha)
}

func fakeRequirementKey(versionID int64, externalID string) string {
	return fmt.Sprintf("%d:%s", versionID, externalID)
}

func (r *fakeExtractionRepo) next() int64 {
	r.nextID++
	return r.nextID
}

func (r *fakeExtractionRepo) CreateRun(_ context.Context, run versionapp.ExtractionRun, segments []versionapp.ExtractionSegment) (versionapp.ExtractionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fakeRunKey(run.VersionID, run.AttachmentID, run.InputSHA256)
	if runID, ok := r.runByKey[key]; ok {
		return *r.runs[runID], nil
	}
	for _, existing := range r.runs {
		if existing.VersionID != run.VersionID || existing.AttachmentID != run.AttachmentID {
			continue
		}
		if existing.InputSHA256 == run.InputSHA256 {
			continue
		}
		switch existing.Status {
		case versionapp.ExtractionRunPending, versionapp.ExtractionRunRunning, versionapp.ExtractionRunPaused:
			existing.Status = versionapp.ExtractionRunFailed
			existing.Error = "archived: input_sha256 changed"
			existing.UpdatedAt = run.UpdatedAt
		}
	}
	run.ID = r.next()
	run.TotalSegments = len(segments)
	run.DoneSegments = 0
	run.FailedSegments = 0
	run.ExtractedCount = 0
	if run.Status == "" {
		run.Status = versionapp.ExtractionRunPending
	}
	stored := make([]versionapp.ExtractionSegment, len(segments))
	for index, segment := range segments {
		segment.ID = r.next()
		segment.RunID = run.ID
		segment.Status = versionapp.ExtractionSegmentPending
		segment.Attempts = 0
		segment.ExtractedCount = 0
		segment.Error = ""
		segment.RenewedAt = nil
		stored[index] = segment
	}
	r.runs[run.ID] = &run
	r.segments[run.ID] = stored
	r.runByKey[key] = run.ID
	return run, nil
}

func (r *fakeExtractionRepo) GetRun(_ context.Context, runID int64) (versionapp.ExtractionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return versionapp.ExtractionRun{}, versionapp.ErrExtractionRunNotFound
	}
	return *run, nil
}

func (r *fakeExtractionRepo) GetLatestRun(_ context.Context, versionID, attachmentID int64) (versionapp.ExtractionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest *versionapp.ExtractionRun
	for _, run := range r.runs {
		if run.VersionID != versionID || run.AttachmentID != attachmentID {
			continue
		}
		if latest == nil || run.ID > latest.ID {
			latest = run
		}
	}
	if latest == nil {
		return versionapp.ExtractionRun{}, versionapp.ErrExtractionRunNotFound
	}
	return *latest, nil
}

func (r *fakeExtractionRepo) ListRuns(_ context.Context, versionID int64) ([]versionapp.ExtractionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := make([]versionapp.ExtractionRun, 0)
	for _, run := range r.runs {
		if run.VersionID == versionID {
			runs = append(runs, *run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, nil
}

func (r *fakeExtractionRepo) ListRunsByStatus(_ context.Context, statuses []versionapp.ExtractionRunStatus, limit int) ([]versionapp.ExtractionRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	allowed := map[versionapp.ExtractionRunStatus]bool{}
	for _, status := range statuses {
		allowed[status] = true
	}
	runs := make([]versionapp.ExtractionRun, 0)
	for _, run := range r.runs {
		if allowed[run.Status] {
			runs = append(runs, *run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (r *fakeExtractionRepo) ListSegments(_ context.Context, runID int64) ([]versionapp.ExtractionSegment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[runID]; !ok {
		return nil, versionapp.ErrExtractionRunNotFound
	}
	return append([]versionapp.ExtractionSegment(nil), r.segments[runID]...), nil
}

func (r *fakeExtractionRepo) ClaimSegments(_ context.Context, runID int64, limit int, now time.Time) ([]versionapp.ExtractionSegment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return nil, versionapp.ErrExtractionRunNotFound
	}
	if limit <= 0 {
		limit = versionapp.DefaultExtractionClaimBatch
	}
	leaseCutoff := now.Add(-versionapp.DefaultExtractionLeaseTimeout)
	segments := r.segments[runID]
	for index := range segments {
		segment := &segments[index]
		if segment.Status != versionapp.ExtractionSegmentRunning || segment.RenewedAt == nil {
			continue
		}
		if !segment.RenewedAt.Before(leaseCutoff) {
			continue
		}
		if segment.Attempts < versionapp.DefaultExtractionMaxAttempts {
			segment.Status = versionapp.ExtractionSegmentPending
			segment.Error = ""
			segment.RenewedAt = nil
			continue
		}
		segment.Status = versionapp.ExtractionSegmentFailed
		segment.Error = "segment lease expired after max attempts"
		segment.RenewedAt = nil
	}
	claimed := make([]versionapp.ExtractionSegment, 0, limit)
	for index := range segments {
		segment := &segments[index]
		eligible := segment.Status == versionapp.ExtractionSegmentPending
		if !eligible && segment.Status == versionapp.ExtractionSegmentFailed && segment.Attempts < versionapp.DefaultExtractionMaxAttempts {
			eligible = segment.RenewedAt == nil || !segment.RenewedAt.After(now)
		}
		if !eligible {
			continue
		}
		segment.Status = versionapp.ExtractionSegmentRunning
		segment.Attempts++
		segment.RenewedAt = &now
		segment.Error = ""
		claimed = append(claimed, *segment)
		if len(claimed) >= limit {
			break
		}
	}
	_ = run
	return claimed, nil
}

func (r *fakeExtractionRepo) findSegment(runID int64, seq int) (*versionapp.ExtractionSegment, error) {
	run, ok := r.runs[runID]
	if !ok {
		return nil, versionapp.ErrExtractionRunNotFound
	}
	_ = run
	segments := r.segments[runID]
	for index := range segments {
		if segments[index].Seq == seq {
			return &segments[index], nil
		}
	}
	return nil, versionapp.ErrExtractionSegmentNotFound
}

func (r *fakeExtractionRepo) MarkSegmentDone(_ context.Context, runID int64, seq, extractedCount int, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	segment, err := r.findSegment(runID, seq)
	if err != nil {
		return err
	}
	if segment.Status != versionapp.ExtractionSegmentRunning {
		return versionapp.ErrExtractionSegmentState
	}
	segment.Status = versionapp.ExtractionSegmentDone
	segment.ExtractedCount = extractedCount
	segment.Error = ""
	segment.RenewedAt = &now
	r.recomputeRunLocked(runID, now)
	return nil
}

func (r *fakeExtractionRepo) MarkSegmentSkipped(_ context.Context, runID int64, seq int, reason string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	segment, err := r.findSegment(runID, seq)
	if err != nil {
		return err
	}
	if segment.Status != versionapp.ExtractionSegmentRunning {
		return versionapp.ErrExtractionSegmentState
	}
	segment.Status = versionapp.ExtractionSegmentSkipped
	segment.Error = reason
	segment.RenewedAt = &now
	r.recomputeRunLocked(runID, now)
	return nil
}

func (r *fakeExtractionRepo) MarkSegmentFailed(_ context.Context, runID int64, seq int, message string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	segment, err := r.findSegment(runID, seq)
	if err != nil {
		return err
	}
	if segment.Status != versionapp.ExtractionSegmentRunning {
		return versionapp.ErrExtractionSegmentState
	}
	segment.Status = versionapp.ExtractionSegmentFailed
	segment.Error = message
	retryAt := now.Add(versionapp.SegmentRetryDelay(segment.Attempts))
	segment.RenewedAt = &retryAt
	r.recomputeRunLocked(runID, now)
	return nil
}

func (r *fakeExtractionRepo) recomputeRunLocked(runID int64, now time.Time) {
	run := r.runs[runID]
	if run == nil {
		return
	}
	segments := r.segments[runID]
	var done, failed, skipped, pending, running, retryable int
	run.ExtractedCount = 0
	for _, segment := range segments {
		switch segment.Status {
		case versionapp.ExtractionSegmentDone:
			done++
		case versionapp.ExtractionSegmentFailed:
			failed++
			if segment.Attempts < versionapp.DefaultExtractionMaxAttempts {
				retryable++
			}
		case versionapp.ExtractionSegmentSkipped:
			skipped++
		case versionapp.ExtractionSegmentRunning:
			running++
		default:
			pending++
		}
	}
	for _, requirement := range r.requirements {
		if requirement.RunID == runID {
			run.ExtractedCount++
		}
	}
	run.TotalSegments = len(segments)
	run.DoneSegments = done
	run.FailedSegments = failed
	run.UpdatedAt = now
	if run.Status == versionapp.ExtractionRunPaused {
		return
	}
	if run.TotalSegments > 0 && done+skipped == run.TotalSegments {
		run.Status = versionapp.ExtractionRunDone
		run.Error = ""
		return
	}
	if pending == 0 && running == 0 && retryable == 0 {
		run.Status = versionapp.ExtractionRunFailed
		return
	}
}

func (r *fakeExtractionRepo) TransitionRun(_ context.Context, runID int64, from []versionapp.ExtractionRunStatus, to versionapp.ExtractionRunStatus, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return false, versionapp.ErrExtractionRunNotFound
	}
	allowed := false
	resumingPaused := false
	for _, status := range from {
		if run.Status == status {
			allowed = true
		}
		if status == versionapp.ExtractionRunPaused {
			resumingPaused = true
		}
	}
	if !allowed {
		return false, nil
	}
	run.Status = to
	run.UpdatedAt = now
	if to == versionapp.ExtractionRunPending || to == versionapp.ExtractionRunRunning {
		run.Error = ""
	}
	// Starting (pending -> running) or resuming a paused run must re-evaluate
	// terminality: a run paused mid-batch can already have every segment done.
	// A failed -> pending retry is intentionally not re-evaluated here because
	// its segments are still terminal until RetryFailedSegments resets them.
	if to == versionapp.ExtractionRunRunning || (to == versionapp.ExtractionRunPending && resumingPaused) {
		r.recomputeRunLocked(runID, now)
	}
	return true, nil
}

func (r *fakeExtractionRepo) RetryFailedSegments(_ context.Context, runID int64, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[runID]; !ok {
		return 0, versionapp.ErrExtractionRunNotFound
	}
	reset := 0
	segments := r.segments[runID]
	for index := range segments {
		if segments[index].Status != versionapp.ExtractionSegmentFailed {
			continue
		}
		segments[index].Status = versionapp.ExtractionSegmentPending
		segments[index].Attempts = 0
		segments[index].Error = ""
		segments[index].RenewedAt = nil
		reset++
	}
	r.recomputeRunLocked(runID, now)
	return reset, nil
}

func (r *fakeExtractionRepo) RetrySegment(_ context.Context, runID int64, seq int, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	segment, err := r.findSegment(runID, seq)
	if err != nil {
		return err
	}
	if segment.Status != versionapp.ExtractionSegmentFailed {
		return versionapp.ErrExtractionSegmentState
	}
	segment.Status = versionapp.ExtractionSegmentPending
	segment.Attempts = 0
	segment.Error = ""
	segment.RenewedAt = nil
	run := r.runs[runID]
	if run.Status == versionapp.ExtractionRunFailed {
		run.Status = versionapp.ExtractionRunPending
		run.Error = ""
	}
	r.recomputeRunLocked(runID, now)
	return nil
}

func (r *fakeExtractionRepo) SaveReconciliation(_ context.Context, runID int64, expected, extracted int, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return versionapp.ErrExtractionRunNotFound
	}
	run.ExpectedCount = &expected
	run.ExtractedCount = extracted
	run.UpdatedAt = now
	return nil
}

func (r *fakeExtractionRepo) ListExtractedExternalIDs(_ context.Context, versionID, runID int64) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0)
	for _, requirement := range r.requirements {
		if requirement.VersionID == versionID && requirement.RunID == runID {
			ids = append(ids, requirement.ExternalID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (r *fakeExtractionRepo) UpsertExtractedRequirements(_ context.Context, source versionapp.ExtractionSource, requirements []versionapp.ExtractionRequirement) (versionapp.ExtractionEmitCounts, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := versionapp.ExtractionEmitCounts{}
	for _, requirement := range requirements {
		key := fakeRequirementKey(source.VersionID, requirement.ExternalID)
		existing, ok := r.requirements[key]
		if ok {
			existing.VersionID = source.VersionID
			existing.ExternalID = requirement.ExternalID
			existing.Title = requirement.Title
			existing.Description = requirement.Description
			existing.Criteria = append([]string(nil), requirement.AcceptanceCriteria...)
			existing.Priority = requirement.Priority
			existing.Category = requirement.Category
			existing.SourceQuote = requirement.SourceQuote
			existing.Confidence = requirement.Confidence
			existing.Constraints = requirement.ConstraintsJSON
			existing.RunID = source.RunID
			counts.Updated++
			continue
		}
		stored := &fakeRequirement{
			VersionID:   source.VersionID,
			ExternalID:  requirement.ExternalID,
			Title:       requirement.Title,
			Description: requirement.Description,
			Criteria:    append([]string(nil), requirement.AcceptanceCriteria...),
			Priority:    requirement.Priority,
			Category:    requirement.Category,
			SourceQuote: requirement.SourceQuote,
			Confidence:  requirement.Confidence,
			Constraints: requirement.ConstraintsJSON,
			RunID:       source.RunID,
		}
		r.requirements[key] = stored
		r.requirementOrder = append(r.requirementOrder, key)
		counts.Created++
	}
	return counts, nil
}

// test-only inspection helpers.

func (r *fakeExtractionRepo) SegmentCount(runID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.segments[runID])
}

func (r *fakeExtractionRepo) RunCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

func (r *fakeExtractionRepo) RequirementCount(versionID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, requirement := range r.requirements {
		if requirement.VersionID == versionID {
			count++
		}
	}
	return count
}

func (r *fakeExtractionRepo) Requirement(versionID int64, externalID string) (fakeRequirement, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	requirement, ok := r.requirements[fakeRequirementKey(versionID, externalID)]
	if !ok {
		return fakeRequirement{}, false
	}
	return *requirement, true
}

// debugSetSegmentError simulates a worker that crashed while a lease was held.
func (r *fakeExtractionRepo) debugSetSegmentError(runID int64, seq int, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	segment, err := r.findSegment(runID, seq)
	if err != nil {
		return
	}
	segment.Error = message
}

// ---------- fixtures ----------

func testSHA(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func testExtractionSegments(texts ...string) []versionapp.Segment {
	segments := make([]versionapp.Segment, 0, len(texts))
	offset := 0
	for index, text := range texts {
		length := len([]rune(text))
		segments = append(segments, versionapp.Segment{
			Seq:         index + 1,
			HeadingPath: fmt.Sprintf("章节 %d", index+1),
			Kind:        versionapp.SegmentKindText,
			CharStart:   offset,
			CharEnd:     offset + length,
			Text:        text,
			TextSHA256:  testSHA(text),
		})
		offset += length + 1
	}
	return segments
}

type extractionEnv struct {
	svc       *versionapp.ExtractionService
	repo      *fakeExtractionRepo
	clock     *stepClock
	processor *recordingProcessor
	events    *recordingPublisher
}

func newExtractionEnv(t *testing.T) *extractionEnv {
	t.Helper()
	clock := &stepClock{at: testTime}
	repo := newFakeExtractionRepo()
	processor := &recordingProcessor{}
	events := &recordingPublisher{}
	svc := versionapp.NewExtractionService(repo, processor, events, clock)
	return &extractionEnv{svc: svc, repo: repo, clock: clock, processor: processor, events: events}
}

func (e *extractionEnv) start(t *testing.T, versionID, attachmentID int64, sha string, documentText string, texts ...string) versionapp.ExtractionRun {
	t.Helper()
	run, err := e.svc.Start(context.Background(), versionapp.ExtractionStartInput{
		VersionID:    versionID,
		AttachmentID: attachmentID,
		InputSHA256:  sha,
		Segments:     testExtractionSegments(texts...),
		DocumentText: documentText,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return run
}

func allFRIDs() []string {
	ids := make([]string, 0, 58)
	for index := 1; index <= 58; index++ {
		ids = append(ids, fmt.Sprintf("FR-%03d", index))
	}
	return ids
}

// reconciliationDocText declares 58 unique FR ids: 40 under the V1 current
// requirements heading and 18 under the V2 planned heading, plus a duplicate
// reference after the planned section (it must not move FR-001 to planned).
func reconciliationDocText() string {
	var builder strings.Builder
	builder.WriteString("# V1 当前需求（功能需求清单）\n\n")
	for index := 1; index <= 40; index++ {
		fmt.Fprintf(&builder, "FR-%03d 当前需求 %d：登录与下单验收。\n", index, index)
	}
	builder.WriteString("\n## V2 规划需求（后续版本）\n\n")
	for index := 41; index <= 58; index++ {
		fmt.Fprintf(&builder, "FR-%03d 规划需求 %d：后续版本能力。\n", index, index)
	}
	builder.WriteString("\n参考重复引用：FR-001（已在当前需求章节声明）。\n")
	return builder.String()
}

// ---------- tests ----------

func TestExtractionIdempotent(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	input := versionapp.ExtractionStartInput{
		VersionID:    7,
		AttachmentID: 3,
		InputSHA256:  testSHA("doc"),
		Segments:     testExtractionSegments("第一段需求", "第二段需求"),
	}
	first, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	second, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("run duplicated: %d/%d", first.ID, second.ID)
	}
	if got := env.repo.SegmentCount(first.ID); got != 2 {
		t.Fatalf("segments=%d, want 2", got)
	}
	if got := env.repo.RunCount(); got != 1 {
		t.Fatalf("runs=%d, want 1", got)
	}

	// A changed document sha creates a new run and archives the old one.
	changed := input
	changed.InputSHA256 = testSHA("doc-v2")
	third, err := env.svc.Start(ctx, changed)
	if err != nil {
		t.Fatalf("changed Start: %v", err)
	}
	if third.ID == first.ID {
		t.Fatalf("changed sha reused run %d", third.ID)
	}
	archived, err := env.repo.GetRun(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetRun archived: %v", err)
	}
	if archived.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("archived status=%s, want failed", archived.Status)
	}
	if got := env.repo.SegmentCount(third.ID); got != 2 {
		t.Fatalf("new run segments=%d, want 2", got)
	}

	// Completed segments are skipped when the same run is processed again.
	env.processor.process = nil
	processed, err := env.svc.ProcessRun(ctx, third.ID)
	if err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	if processed.Status != versionapp.ExtractionRunDone || processed.DoneSegments != 2 {
		t.Fatalf("processed run=%+v", processed)
	}
	calls := env.processor.CallCount()
	if _, err := env.svc.ProcessRun(ctx, third.ID); err != nil {
		t.Fatalf("re-ProcessRun: %v", err)
	}
	if env.processor.CallCount() != calls {
		t.Fatalf("completed segments re-processed: calls %d -> %d", calls, env.processor.CallCount())
	}
}

func TestReconciliationCounts(t *testing.T) {
	report := versionapp.ReconcileDocument(reconciliationDocText(), versionapp.ReconciliationInput{
		ExtractedIDs:  allFRIDs(),
		LowConfidence: 2,
		SkippedImages: 1,
	})
	if report.Expected != 58 || report.Extracted != 58 || report.Missing != 0 {
		t.Fatalf("report=%+v", report)
	}
	if len(report.MissingIDs) != 0 {
		t.Fatalf("missing_ids=%v", report.MissingIDs)
	}
	if report.LowConfidence != 2 || report.SkippedImages != 1 {
		t.Fatalf("low/skipped=%d/%d", report.LowConfidence, report.SkippedImages)
	}
	if len(report.CurrentIDs) != 40 || len(report.PlannedIDs) != 18 {
		t.Fatalf("current=%d planned=%d", len(report.CurrentIDs), len(report.PlannedIDs))
	}
	for _, id := range report.PlannedIDs {
		if id == "FR-001" {
			t.Fatalf("duplicate FR-001 moved to planned")
		}
	}

	// Missing ids are reported when the extraction omitted declared ids.
	partial := versionapp.ReconcileDocument(reconciliationDocText(), versionapp.ReconciliationInput{
		ExtractedIDs: allFRIDs()[:57],
	})
	if partial.Missing != 1 || len(partial.MissingIDs) != 1 || partial.MissingIDs[0] != "FR-058" {
		t.Fatalf("partial report=%+v", partial)
	}
}

func TestReconciliationPersistsRunCounts(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	document := "# V1 当前需求\n\nFR-001 登录\nFR-002 退出\n"
	run := env.start(t, 7, 3, testSHA(document), document, document)
	if run.ExpectedCount == nil || *run.ExpectedCount != 2 {
		t.Fatalf("start expected_count=%v, want 2", run.ExpectedCount)
	}

	if _, err := env.svc.EmitExtractedRequirements(ctx, versionapp.ExtractionSource{
		VersionID: 7, AttachmentID: 3, RunID: run.ID, SegmentSeq: 1,
	}, []versionapp.ExtractionRequirement{{
		ExternalID: "FR-001", Title: "登录", Description: "手机号登录",
		AcceptanceCriteria: []string{"验证码正确"}, Priority: "P0",
		Category: "functional", SourceQuote: "FR-001 登录", Confidence: 0.9,
	}}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	report, err := env.svc.Reconcile(ctx, run.ID, document, 1, 0)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.Expected != 2 || report.Extracted != 1 || report.Missing != 1 {
		t.Fatalf("report=%+v", report)
	}
	if len(report.MissingIDs) != 1 || report.MissingIDs[0] != "FR-002" {
		t.Fatalf("missing_ids=%v", report.MissingIDs)
	}
	stored, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.ExpectedCount == nil || *stored.ExpectedCount != 2 || stored.ExtractedCount != 1 {
		t.Fatalf("persisted expected/extracted=%v/%d", stored.ExpectedCount, stored.ExtractedCount)
	}
}

func TestExtractionRetryBackoffAndCap(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	run := env.start(t, 7, 3, testSHA("retry"), "", "需要重试的段落")
	env.processor.failuresLeft = 10

	failedRun, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("first ProcessRun: %v", err)
	}
	if failedRun.Status != versionapp.ExtractionRunRunning || failedRun.FailedSegments != 1 {
		t.Fatalf("after first failure run=%+v", failedRun)
	}
	segments, err := env.repo.ListSegments(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	segment := segments[0]
	if segment.Status != versionapp.ExtractionSegmentFailed || segment.Attempts != 1 {
		t.Fatalf("segment=%+v", segment)
	}
	if segment.RenewedAt == nil {
		t.Fatal("failed segment has no renewed_at backoff")
	}
	firstDelay := segment.RenewedAt.Sub(env.clock.Now())
	if firstDelay != versionapp.DefaultExtractionRetryBaseDelay {
		t.Fatalf("first backoff=%v, want %v", firstDelay, versionapp.DefaultExtractionRetryBaseDelay)
	}

	// Backoff not elapsed: the segment must not be claimed again.
	env.clock.Advance(firstDelay - time.Second)
	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("backoff ProcessRun: %v", err)
	}
	if calls := env.processor.CallCount(); calls != 1 {
		t.Fatalf("claimed before backoff: calls=%d", calls)
	}

	// Second attempt after the first backoff.
	env.clock.Advance(2 * time.Second)
	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("second ProcessRun: %v", err)
	}
	segments, _ = env.repo.ListSegments(ctx, run.ID)
	segment = segments[0]
	if segment.Attempts != 2 || segment.Status != versionapp.ExtractionSegmentFailed {
		t.Fatalf("second failure segment=%+v", segment)
	}
	secondDelay := segment.RenewedAt.Sub(env.clock.Now())
	if secondDelay != 2*versionapp.DefaultExtractionRetryBaseDelay {
		t.Fatalf("second backoff=%v, want %v", secondDelay, 2*versionapp.DefaultExtractionRetryBaseDelay)
	}
	env.clock.Advance(secondDelay + time.Second)

	// Third attempt fails permanently (3-attempt cap).
	terminal, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("third ProcessRun: %v", err)
	}
	if terminal.Status != versionapp.ExtractionRunFailed || terminal.FailedSegments != 1 {
		t.Fatalf("terminal run=%+v", terminal)
	}
	segments, _ = env.repo.ListSegments(ctx, run.ID)
	if segments[0].Attempts != versionapp.DefaultExtractionMaxAttempts {
		t.Fatalf("attempts=%d, want cap %d", segments[0].Attempts, versionapp.DefaultExtractionMaxAttempts)
	}
	if calls := env.processor.CallCount(); calls != 3 {
		t.Fatalf("processor calls=%d, want 3", calls)
	}
	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("capped ProcessRun: %v", err)
	}
	if calls := env.processor.CallCount(); calls != 3 {
		t.Fatalf("capped segment reclaimed: calls=%d", calls)
	}

	// Retry-run resets the terminal failure and clears the attempt budget.
	env.processor.failuresLeft = 0
	retried, err := env.svc.RetryRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("RetryRun: %v", err)
	}
	if retried.Status != versionapp.ExtractionRunPending || retried.Error != "" {
		t.Fatalf("retried run=%+v", retried)
	}
	segments, _ = env.repo.ListSegments(ctx, run.ID)
	if segments[0].Status != versionapp.ExtractionSegmentPending || segments[0].Attempts != 0 {
		t.Fatalf("retried segment=%+v", segments[0])
	}
	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("retried ProcessRun: %v", err)
	}
	segments, _ = env.repo.ListSegments(ctx, run.ID)
	if segments[0].Status != versionapp.ExtractionSegmentDone {
		t.Fatalf("retried segment not done: %+v", segments[0])
	}
}

func TestPauseResumeRun(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	run := env.start(t, 7, 3, testSHA("pause"), "", "暂停恢复段落")

	paused, err := env.svc.Pause(ctx, run.ID)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused.Status != versionapp.ExtractionRunPaused {
		t.Fatalf("paused status=%s", paused.Status)
	}
	unchanged, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("paused ProcessRun: %v", err)
	}
	if unchanged.Status != versionapp.ExtractionRunPaused || env.processor.CallCount() != 0 {
		t.Fatalf("paused run processed: %+v calls=%d", unchanged, env.processor.CallCount())
	}
	resumed, err := env.svc.Resume(ctx, run.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Status != versionapp.ExtractionRunPending {
		t.Fatalf("resumed status=%s", resumed.Status)
	}
	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("resumed ProcessRun: %v", err)
	}
	segments, _ := env.repo.ListSegments(ctx, run.ID)
	if segments[0].Status != versionapp.ExtractionSegmentDone || env.processor.CallCount() != 1 {
		t.Fatalf("resumed run not processed: %+v calls=%d", segments[0], env.processor.CallCount())
	}

	if _, err := env.svc.Pause(ctx, 9999); !errors.Is(err, versionapp.ErrExtractionRunNotFound) {
		t.Fatalf("Pause missing err=%v, want ErrExtractionRunNotFound", err)
	}
	if _, err := env.svc.Pause(ctx, run.ID); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("Pause done err=%v, want ErrExtractionRunState", err)
	}
	if _, err := env.svc.Resume(ctx, run.ID); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("Resume done err=%v, want ErrExtractionRunState", err)
	}
	if _, err := env.svc.RetryRun(ctx, run.ID); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("RetryRun done err=%v, want ErrExtractionRunState", err)
	}
}

// TestResumeAfterPauseFinalizesCompletedRun pins the pause-during-processing
// edge: the worker finishes the already-claimed batch while the run is paused,
// so Resume must re-evaluate the terminal state instead of leaving the run
// pending forever.
func TestResumeAfterPauseFinalizesCompletedRun(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	run := env.start(t, 7, 3, testSHA("pause-mid-batch"), "", "段一", "段二")
	var pauseOnce sync.Once
	env.processor.process = func(versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		pauseOnce.Do(func() {
			if _, err := env.svc.Pause(ctx, run.ID); err != nil {
				t.Errorf("Pause during processing: %v", err)
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
	resumed, err := env.svc.Resume(ctx, run.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Status != versionapp.ExtractionRunDone || resumed.DoneSegments != 2 {
		t.Fatalf("resumed completed run=%+v", resumed)
	}
}

func TestExpiredRunningSegmentReset(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	run := env.start(t, 7, 3, testSHA("expired"), "", "崩溃恢复段落")

	// Simulate a crashed worker: claim the segment and never finish it.
	claimed, err := env.repo.ClaimSegments(ctx, run.ID, 1, env.clock.Now())
	if err != nil {
		t.Fatalf("ClaimSegments: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Status != versionapp.ExtractionSegmentRunning {
		t.Fatalf("claimed=%+v", claimed)
	}
	env.repo.debugSetSegmentError(run.ID, claimed[0].Seq, "worker crashed")

	env.clock.Advance(versionapp.DefaultExtractionLeaseTimeout + time.Second)
	processed, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun after lease expiry: %v", err)
	}
	if processed.Status != versionapp.ExtractionRunDone {
		t.Fatalf("run=%+v", processed)
	}
	segments, _ := env.repo.ListSegments(ctx, run.ID)
	segment := segments[0]
	if segment.Status != versionapp.ExtractionSegmentDone || segment.Attempts != 2 || segment.Error != "" {
		t.Fatalf("recovered segment=%+v", segment)
	}
	if calls := env.processor.CallCount(); calls != 1 {
		t.Fatalf("processor calls=%d, want 1", calls)
	}
}

func TestDuplicateExternalIDUpsert(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	run := env.start(t, 7, 3, testSHA("sink"), "", "需求段落")
	source := versionapp.ExtractionSource{VersionID: 7, AttachmentID: 3, RunID: run.ID, SegmentSeq: 1}

	base := versionapp.ExtractionRequirement{
		ExternalID: "FR-001", Title: "登录", Description: "手机号登录",
		AcceptanceCriteria: []string{"验证码正确"}, Priority: "P0",
		Category: "functional", SourceQuote: "FR-001 登录", Confidence: 0.9,
	}
	first, err := env.svc.EmitExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{base})
	if err != nil {
		t.Fatalf("first Emit: %v", err)
	}
	if first.Created != 1 || first.Updated != 0 {
		t.Fatalf("first counts=%+v", first)
	}

	updated := base
	updated.Title = "手机号登录"
	updated.SourceQuote = "FR-001 登录（手机号）"
	updated.Confidence = 0.7
	second, err := env.svc.EmitExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{updated})
	if err != nil {
		t.Fatalf("second Emit: %v", err)
	}
	if second.Created != 0 || second.Updated != 1 {
		t.Fatalf("second counts=%+v", second)
	}
	if got := env.repo.RequirementCount(7); got != 1 {
		t.Fatalf("requirements=%d, want 1 (duplicate external_id inserted)", got)
	}
	stored, ok := env.repo.Requirement(7, "FR-001")
	if !ok {
		t.Fatal("FR-001 not stored")
	}
	if stored.Title != "手机号登录" || stored.SourceQuote != "FR-001 登录（手机号）" || stored.Confidence != 0.7 || stored.RunID != run.ID {
		t.Fatalf("stored requirement=%+v", stored)
	}
}

func TestConcurrentExtractionWorkers(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	texts := make([]string, 20)
	for index := range texts {
		texts[index] = fmt.Sprintf("并发段落 %d", index+1)
	}
	run := env.start(t, 7, 3, testSHA("concurrent"), "", texts...)

	var mu sync.Mutex
	processed := map[int]int{}
	env.processor.process = func(segment versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		mu.Lock()
		processed[segment.Seq]++
		mu.Unlock()
		time.Sleep(time.Millisecond)
		return versionapp.ExtractionSegmentResult{}, nil
	}

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for worker := 0; worker < 2; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := env.svc.ProcessRun(ctx, run.ID)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ProcessRun: %v", err)
		}
	}
	for seq := 1; seq <= 20; seq++ {
		if processed[seq] != 1 {
			t.Fatalf("segment %d processed %d times", seq, processed[seq])
		}
	}
	stored, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != versionapp.ExtractionRunDone || stored.DoneSegments != 20 {
		t.Fatalf("run=%+v", stored)
	}
	segments, _ := env.repo.ListSegments(ctx, run.ID)
	for _, segment := range segments {
		if segment.Status != versionapp.ExtractionSegmentDone {
			t.Fatalf("segment %d status=%s", segment.Seq, segment.Status)
		}
	}

	// ProcessNext only picks pending/running runs and is a no-op on done runs.
	next, err := env.svc.ProcessNext(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}
	if next != 0 {
		t.Fatalf("ProcessNext processed %d done runs", next)
	}
}
