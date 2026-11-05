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
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
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

// ResetCalls drops the recorded call history (used when a test exercises a
// retry that must only process the reset segment).
func (p *recordingProcessor) ResetCalls() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = nil
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
	// ConfidenceSet distinguishes the SQL NUMERIC value 0 from NULL: emitted
	// requirements always carry a score, while requirement rows from other
	// writers may have confidence IS NULL.
	ConfidenceSet bool
	Constraints   json.RawMessage
	RunID         int64
}

type fakeExtractionRepo struct {
	mu               sync.Mutex
	nextID           int64
	runs             map[int64]*versionapp.ExtractionRun
	segments         map[int64][]versionapp.ExtractionSegment
	runByKey         map[string]int64
	requirements     map[string]*fakeRequirement
	requirementOrder []string
	renewals         int
	lastRenewalAt    time.Time
	milestoneLocksMu sync.Mutex
	milestoneLocks   map[string]*sync.Mutex
}

func newFakeExtractionRepo() *fakeExtractionRepo {
	return &fakeExtractionRepo{
		runs:           map[int64]*versionapp.ExtractionRun{},
		segments:       map[int64][]versionapp.ExtractionSegment{},
		runByKey:       map[string]int64{},
		requirements:   map[string]*fakeRequirement{},
		milestoneLocks: map[string]*sync.Mutex{},
	}
}

var _ versionapp.ExtractionRepository = (*fakeExtractionRepo)(nil)
var _ versionapp.ExtractionMilestoneLocker = (*fakeExtractionRepo)(nil)

// LockMilestone is the in-memory equivalent of the PostgreSQL session advisory
// lock: it serializes one attachment's milestone projection per process.
func (r *fakeExtractionRepo) LockMilestone(_ context.Context, versionID, attachmentID int64) (func(), error) {
	key := fmt.Sprintf("%d:%d", versionID, attachmentID)
	r.milestoneLocksMu.Lock()
	lock, ok := r.milestoneLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		r.milestoneLocks[key] = lock
	}
	r.milestoneLocksMu.Unlock()
	lock.Lock()
	return lock.Unlock, nil
}

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
		existing := r.runs[runID]
		// The requested sha exists but was already superseded and a different
		// run is active: return the active run instead of resurrecting the old
		// cursor (Start never leaves the attachment without an active run).
		if existing.Status == versionapp.ExtractionRunDone || existing.Status == versionapp.ExtractionRunFailed {
			if active := r.activeRunLocked(run.VersionID, run.AttachmentID); active != nil && active.ID != existing.ID {
				return *active, nil
			}
		}
		r.archiveActiveRunsLocked(run.VersionID, run.AttachmentID, existing.ID, run.UpdatedAt)
		r.upsertSegmentsLocked(existing.ID, segments, run.UpdatedAt)
		if run.ExpectedCount != nil {
			existing.ExpectedCount = run.ExpectedCount
		}
		r.recomputeRunLocked(existing.ID, run.UpdatedAt)
		r.reopenForWorkLocked(existing)
		return *existing, nil
	}
	// New-run path: one attachment has at most one active run.
	r.archiveActiveRunsLocked(run.VersionID, run.AttachmentID, 0, run.UpdatedAt)
	run.ID = r.next()
	run.TotalSegments = len(segments)
	run.DoneSegments = 0
	run.FailedSegments = 0
	run.ExtractedCount = 0
	if run.Status == "" {
		run.Status = versionapp.ExtractionRunPending
	}
	r.runs[run.ID] = &run
	r.segments[run.ID] = nil
	r.upsertSegmentsLocked(run.ID, segments, run.UpdatedAt)
	r.runByKey[key] = run.ID
	return run, nil
}

// activeRunLocked returns the (single, by invariant) active run of one
// attachment, if any.
func (r *fakeExtractionRepo) activeRunLocked(versionID, attachmentID int64) *versionapp.ExtractionRun {
	for _, run := range r.runs {
		if run.VersionID != versionID || run.AttachmentID != attachmentID {
			continue
		}
		switch run.Status {
		case versionapp.ExtractionRunPending, versionapp.ExtractionRunRunning, versionapp.ExtractionRunPaused:
			return run
		}
	}
	return nil
}

// archiveActiveRunsLocked archives every pending/running/paused run of one
// attachment except keepID.
func (r *fakeExtractionRepo) archiveActiveRunsLocked(versionID, attachmentID, keepID int64, now time.Time) {
	for _, other := range r.runs {
		if other.VersionID != versionID || other.AttachmentID != attachmentID || other.ID == keepID {
			continue
		}
		switch other.Status {
		case versionapp.ExtractionRunPending, versionapp.ExtractionRunRunning, versionapp.ExtractionRunPaused:
			other.Status = versionapp.ExtractionRunFailed
			other.Error = "archived: input_sha256 changed"
			other.UpdatedAt = now
		}
	}
}

// upsertSegmentsLocked inserts missing (run_id, seq) segments and refreshes the
// stored text identity. A changed text_sha256 reopens the segment (pending,
// attempts=0) so the new text is re-extracted instead of silently skipped.
func (r *fakeExtractionRepo) upsertSegmentsLocked(runID int64, segments []versionapp.ExtractionSegment, now time.Time) {
	stored := r.segments[runID]
	for _, incoming := range segments {
		matched := false
		for index := range stored {
			segment := &stored[index]
			if segment.Seq != incoming.Seq {
				continue
			}
			matched = true
			segment.HeadingPath = incoming.HeadingPath
			segment.CharStart = incoming.CharStart
			segment.CharEnd = incoming.CharEnd
			if segment.TextSHA256 != incoming.TextSHA256 {
				segment.TextSHA256 = incoming.TextSHA256
				segment.Status = versionapp.ExtractionSegmentPending
				segment.Attempts = 0
				segment.ExtractedCount = 0
				segment.Error = ""
				segment.RenewedAt = nil
			}
			break
		}
		if matched {
			continue
		}
		incoming.ID = r.next()
		incoming.RunID = runID
		incoming.Status = versionapp.ExtractionSegmentPending
		incoming.Attempts = 0
		incoming.ExtractedCount = 0
		incoming.Error = ""
		incoming.RenewedAt = nil
		stored = append(stored, incoming)
	}
	sort.Slice(stored, func(i, j int) bool { return stored[i].Seq < stored[j].Seq })
	r.segments[runID] = stored
}

// reopenForWorkLocked moves a terminal run back to pending when a replay added a
// pending segment, so a done run can never keep orphan pending segments. This
// mirrors the SQL reopen predicate (agg.pending > 0): a running segment left by
// an in-flight worker does not make a terminal run claimable again.
func (r *fakeExtractionRepo) reopenForWorkLocked(run *versionapp.ExtractionRun) {
	if run == nil || run.Status == versionapp.ExtractionRunPaused {
		return
	}
	if run.Status != versionapp.ExtractionRunDone && run.Status != versionapp.ExtractionRunFailed {
		return
	}
	for _, segment := range r.segments[run.ID] {
		if segment.Status == versionapp.ExtractionSegmentPending {
			run.Status = versionapp.ExtractionRunPending
			run.Error = ""
			return
		}
	}
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
	// Pause/archive gate: a paused or archived run is not claimable.
	if run.Status != versionapp.ExtractionRunPending && run.Status != versionapp.ExtractionRunRunning {
		return nil, nil
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
	if run.Status == versionapp.ExtractionRunPending {
		run.Status = versionapp.ExtractionRunRunning
	}
	return claimed, nil
}

// RenewSegmentLeases extends the running lease of the given claimed segments.
func (r *fakeExtractionRepo) RenewSegmentLeases(_ context.Context, runID int64, seqs []int, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[runID]; !ok {
		return 0, versionapp.ErrExtractionRunNotFound
	}
	renewed := 0
	for index := range r.segments[runID] {
		segment := &r.segments[runID][index]
		if segment.Status != versionapp.ExtractionSegmentRunning {
			continue
		}
		for _, seq := range seqs {
			if segment.Seq != seq {
				continue
			}
			segment.RenewedAt = &now
			renewed++
			break
		}
	}
	r.renewals++
	r.lastRenewalAt = now
	return renewed, nil
}

// Renewals reports how many heartbeat renewal calls the repository saw.
func (r *fakeExtractionRepo) Renewals() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.renewals
}

// LastRenewalAt reports the clock time of the last renewal call, so a test can
// prove the heartbeat renewed after advancing (not just the synchronous renew
// that happens before the slow processor call).
func (r *fakeExtractionRepo) LastRenewalAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRenewalAt
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

// ensureRunWritableLocked fences segment transitions and emits: only pending/
// running/paused runs that were not superseded by a newer *active* run of the
// same attachment may write (a paused run still finishes its claimed batch). A
// newer terminal run does not fence an older run revived by a replay.
func (r *fakeExtractionRepo) ensureRunWritableLocked(runID int64) error {
	run, ok := r.runs[runID]
	if !ok {
		return versionapp.ErrExtractionRunNotFound
	}
	switch run.Status {
	case versionapp.ExtractionRunPending, versionapp.ExtractionRunRunning, versionapp.ExtractionRunPaused:
	default:
		return versionapp.ErrExtractionRunState
	}
	for _, other := range r.runs {
		if other.VersionID != run.VersionID || other.AttachmentID != run.AttachmentID || other.ID <= run.ID {
			continue
		}
		switch other.Status {
		case versionapp.ExtractionRunPending, versionapp.ExtractionRunRunning, versionapp.ExtractionRunPaused:
			return versionapp.ErrExtractionRunState
		}
	}
	return nil
}

// ensureRunNotSupersededLocked rejects retries of an archived run.
func (r *fakeExtractionRepo) ensureRunNotSupersededLocked(runID int64) error {
	run, ok := r.runs[runID]
	if !ok {
		return versionapp.ErrExtractionRunNotFound
	}
	for _, other := range r.runs {
		if other.VersionID == run.VersionID && other.AttachmentID == run.AttachmentID && other.ID > run.ID {
			return versionapp.ErrExtractionRunState
		}
	}
	return nil
}

func (r *fakeExtractionRepo) MarkSegmentDone(_ context.Context, runID int64, seq, extractedCount int, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureRunWritableLocked(runID); err != nil {
		return err
	}
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
	if err := r.ensureRunWritableLocked(runID); err != nil {
		return err
	}
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
	if err := r.ensureRunWritableLocked(runID); err != nil {
		return err
	}
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
	if run.TotalSegments > 0 && pending == 0 && running == 0 && retryable == 0 {
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
	if err := r.ensureRunNotSupersededLocked(runID); err != nil {
		return 0, err
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
	if err := r.ensureRunNotSupersededLocked(runID); err != nil {
		return err
	}
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

func (r *fakeExtractionRepo) SaveReconciliation(_ context.Context, runID int64, report versionapp.ReconciliationReport, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return versionapp.ErrExtractionRunNotFound
	}
	expected := report.Expected
	run.ExpectedCount = &expected
	run.ExtractedCount = report.Extracted
	stored := report
	stored.MissingIDs = append([]string(nil), report.MissingIDs...)
	stored.PlannedIDs = append([]string(nil), report.PlannedIDs...)
	stored.CurrentIDs = append([]string(nil), report.CurrentIDs...)
	run.Report = &stored
	run.UpdatedAt = now
	return nil
}

// CountLowConfidence derives the low-confidence count from the scored
// requirement rows attributed to the run (fake equivalent of
// requirement.confidence IS NOT NULL AND confidence < threshold).
func (r *fakeExtractionRepo) CountLowConfidence(_ context.Context, runID int64, threshold float64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[runID]; !ok {
		return 0, versionapp.ErrExtractionRunNotFound
	}
	count := 0
	for _, requirement := range r.requirements {
		if requirement.RunID == runID && requirement.ConfidenceSet && requirement.Confidence < threshold {
			count++
		}
	}
	return count, nil
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

func (r *fakeExtractionRepo) UpsertExtractedRequirements(_ context.Context, source versionapp.ExtractionSource, requirements []versionapp.ExtractionRequirement, _ time.Time) (versionapp.ExtractionEmitCounts, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureRunWritableLocked(source.RunID); err != nil {
		return versionapp.ExtractionEmitCounts{}, err
	}
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
			existing.ConfidenceSet = true
			existing.Constraints = requirement.ConstraintsJSON
			existing.RunID = source.RunID
			counts.Updated++
			continue
		}
		stored := &fakeRequirement{
			VersionID:     source.VersionID,
			ExternalID:    requirement.ExternalID,
			Title:         requirement.Title,
			Description:   requirement.Description,
			Criteria:      append([]string(nil), requirement.AcceptanceCriteria...),
			Priority:      requirement.Priority,
			Category:      requirement.Category,
			SourceQuote:   requirement.SourceQuote,
			Confidence:    requirement.Confidence,
			ConfidenceSet: true,
			Constraints:   requirement.ConstraintsJSON,
			RunID:         source.RunID,
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

// debugSetRunStatus forces a run status to model a state the service cannot
// produce directly (a done run that still has a running segment), so the fake
// reopen predicate can be compared with the SQL one.
func (r *fakeExtractionRepo) debugSetRunStatus(runID int64, status versionapp.ExtractionRunStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[runID]; ok {
		run.Status = status
	}
}

// debugAddUnscoredRequirement stores a requirement with NULL confidence (the
// fake models SQL NULL with ConfidenceSet=false).
func (r *fakeExtractionRepo) debugAddUnscoredRequirement(versionID, runID int64, externalID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fakeRequirementKey(versionID, externalID)
	r.requirements[key] = &fakeRequirement{
		VersionID:  versionID,
		ExternalID: externalID,
		RunID:      runID,
	}
	r.requirementOrder = append(r.requirementOrder, key)
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
	// R-A7-8: expected/missing cover the V1 current requirements only. The 18
	// V2 planned ids are reported separately and are never "missing".
	report := versionapp.ReconcileDocument(reconciliationDocText(), versionapp.ReconciliationInput{
		ExtractedIDs:  allFRIDs(),
		LowConfidence: 2,
		SkippedImages: 1,
	})
	if report.Expected != 40 || report.Extracted != 40 || report.Missing != 0 {
		t.Fatalf("report=%+v (expected/extracted must count V1 current ids only)", report)
	}
	if len(report.MissingIDs) != 0 {
		t.Fatalf("missing_ids=%v", report.MissingIDs)
	}
	if report.LowConfidence != 2 || report.SkippedImages != 1 {
		t.Fatalf("low/skipped=%d/%d", report.LowConfidence, report.SkippedImages)
	}
	if len(report.CurrentIDs) != 40 || len(report.PlannedIDs) != 18 || report.PlannedCount != 18 {
		t.Fatalf("current=%d planned=%d planned_count=%d",
			len(report.CurrentIDs), len(report.PlannedIDs), report.PlannedCount)
	}
	for _, id := range report.PlannedIDs {
		if id == "FR-001" {
			t.Fatalf("duplicate FR-001 moved to planned")
		}
	}

	// Missing current ids are reported when the extraction omitted one of them.
	partial := versionapp.ReconcileDocument(reconciliationDocText(), versionapp.ReconciliationInput{
		ExtractedIDs: allFRIDs()[1:],
	})
	if partial.Missing != 1 || len(partial.MissingIDs) != 1 || partial.MissingIDs[0] != "FR-001" {
		t.Fatalf("partial report=%+v", partial)
	}

	// Extracting only planned requirements never produces false missing ids.
	plannedOnly := versionapp.ReconcileDocument(reconciliationDocText(), versionapp.ReconciliationInput{
		ExtractedIDs: allFRIDs()[40:],
	})
	if plannedOnly.Expected != 40 || plannedOnly.Extracted != 0 || plannedOnly.Missing != 40 {
		t.Fatalf("planned-only report=%+v", plannedOnly)
	}
	for _, missing := range plannedOnly.MissingIDs {
		for _, planned := range plannedOnly.PlannedIDs {
			if missing == planned {
				t.Fatalf("planned id %s reported missing", missing)
			}
		}
	}
}

func currentFRIDs() []string {
	ids := make([]string, 0, 58)
	for index := 1; index <= 58; index++ {
		ids = append(ids, fmt.Sprintf("FR-%03d", index))
	}
	return ids
}

func plannedFRIDs() []string {
	ids := make([]string, 0, 32)
	for index := 59; index <= 90; index++ {
		ids = append(ids, fmt.Sprintf("FR-%03d", index))
	}
	return ids
}

// reconciliation58Plus32Doc mimics the live 95.3k requirements document: 58
// V1 current FR ids followed by 32 V2 planned ones.
func reconciliation58Plus32Doc() string {
	var builder strings.Builder
	builder.WriteString("# V1 当前需求（功能需求清单）\n\n")
	for index := 1; index <= 58; index++ {
		fmt.Fprintf(&builder, "FR-%03d 当前需求 %d：验收通过。\n", index, index)
	}
	builder.WriteString("\n## V2 规划需求（后续版本）\n\n")
	for index := 59; index <= 90; index++ {
		fmt.Fprintf(&builder, "FR-%03d 规划需求 %d：后续版本能力。\n", index, index)
	}
	return builder.String()
}

// TestReconciliationCurrent58Planned32 is the A-9 acceptance shape: the live
// document declares 58 current + 32 planned ids. Extracting only the 58 current
// ones must report 58/58 with no missing ids and planned_count=32.
func TestReconciliationCurrent58Planned32(t *testing.T) {
	document := reconciliation58Plus32Doc()
	report := versionapp.ReconcileDocument(document, versionapp.ReconciliationInput{
		ExtractedIDs:  currentFRIDs(),
		LowConfidence: 3,
		SkippedImages: 2,
	})
	if report.Expected != 58 || report.Extracted != 58 || report.Missing != 0 {
		t.Fatalf("58+32 report=%+v", report)
	}
	if len(report.MissingIDs) != 0 {
		t.Fatalf("missing_ids=%v", report.MissingIDs)
	}
	if report.PlannedCount != 32 || len(report.PlannedIDs) != 32 {
		t.Fatalf("planned_count=%d planned_ids=%d", report.PlannedCount, len(report.PlannedIDs))
	}
	if report.LowConfidence != 3 || report.SkippedImages != 2 {
		t.Fatalf("low/skipped=%d/%d", report.LowConfidence, report.SkippedImages)
	}
	for _, id := range report.PlannedIDs {
		for _, missing := range report.MissingIDs {
			if id == missing {
				t.Fatalf("planned id %s reported missing", id)
			}
		}
	}

	// Planned-only extraction: current ids are still missing, planned ids are
	// never reported missing even though none of them were extracted.
	plannedOnly := versionapp.ReconcileDocument(document, versionapp.ReconciliationInput{
		ExtractedIDs: plannedFRIDs(),
	})
	if plannedOnly.Expected != 58 || plannedOnly.Extracted != 0 || plannedOnly.Missing != 58 {
		t.Fatalf("planned-only 58+32 report=%+v", plannedOnly)
	}
	for _, missing := range plannedOnly.MissingIDs {
		for _, planned := range plannedOnly.PlannedIDs {
			if missing == planned {
				t.Fatalf("planned id %s reported missing", missing)
			}
		}
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

	report, err := env.svc.Reconcile(ctx, run.ID, document, 0)
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
	if stored.Report == nil || stored.Report.Expected != 2 || stored.Report.Extracted != 1 ||
		stored.Report.Missing != 1 || len(stored.Report.MissingIDs) != 1 {
		t.Fatalf("persisted report=%+v", stored.Report)
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

// TestPauseStopsProcessRunBetweenBatches pins R-A7-9: Pause is not advisory. A
// worker with more than one claim batch must stop at the next batch boundary
// instead of draining every remaining segment.
func TestPauseStopsProcessRunBetweenBatches(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	run := env.start(t, 7, 3, testSHA("pause-batches"), "", "段一", "段二", "段三", "段四", "段五")
	env.svc.SetClaimBatch(2)

	var once sync.Once
	env.processor.process = func(versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		once.Do(func() {
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
		t.Fatalf("paused run=%+v", processed)
	}
	if calls := env.processor.CallCount(); calls != 2 {
		t.Fatalf("worker processed %d segments after pause, want 2 (one claim batch)", calls)
	}
	segments, err := env.repo.ListSegments(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	pending := 0
	for _, segment := range segments {
		if segment.Status == versionapp.ExtractionSegmentPending {
			pending++
		}
	}
	if pending != 3 {
		t.Fatalf("pending segments=%d, want 3", pending)
	}

	// Resume drains the remaining segments on the next worker pass.
	if _, err := env.svc.Resume(ctx, run.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	final, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("resumed ProcessRun: %v", err)
	}
	if final.Status != versionapp.ExtractionRunDone || final.DoneSegments != 5 {
		t.Fatalf("resumed run=%+v", final)
	}
}

// TestArchiveFencingRejectsInFlightWorker pins the changed-sha fence: a worker
// that already claimed segments of an archived run must not mark them terminal
// and must not overwrite the new run's requirement provenance.
func TestArchiveFencingRejectsInFlightWorker(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	document := "# V1 当前需求\n\nFR-001 登录\n"
	oldRun := env.start(t, 7, 3, testSHA("sha-old"), document, "旧段落一", "旧段落二")
	env.svc.SetClaimBatch(1)
	oldSource := versionapp.ExtractionSource{VersionID: 7, AttachmentID: 3, RunID: oldRun.ID, SegmentSeq: 1}

	var newRun versionapp.ExtractionRun
	var once sync.Once
	env.processor.process = func(versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		once.Do(func() {
			// The document changed while this worker was in flight.
			started, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
				VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("sha-new"),
				Segments: testExtractionSegments("新段落一"), DocumentText: document,
			})
			if err != nil {
				t.Errorf("Start changed sha: %v", err)
				return
			}
			newRun = started
			if _, err := env.svc.EmitExtractedRequirements(ctx, oldSource,
				[]versionapp.ExtractionRequirement{{ExternalID: "FR-001", Title: "旧", Confidence: 0.4}}); !errors.Is(err, versionapp.ErrExtractionRunState) {
				t.Errorf("archived run emit err=%v, want ErrExtractionRunState", err)
			}
		})
		return versionapp.ExtractionSegmentResult{ExtractedCount: 1}, nil
	}

	processed, err := env.svc.ProcessRun(ctx, oldRun.ID)
	if err != nil {
		t.Fatalf("in-flight worker must stop cleanly after archive: %v", err)
	}
	if processed.ID != oldRun.ID || processed.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("archived run after in-flight batch=%+v", processed)
	}
	segments, err := env.repo.ListSegments(ctx, oldRun.ID)
	if err != nil {
		t.Fatalf("ListSegments old: %v", err)
	}
	for _, segment := range segments {
		if segment.Status == versionapp.ExtractionSegmentDone || segment.Status == versionapp.ExtractionSegmentSkipped {
			t.Fatalf("archived run segment %d was marked %s", segment.Seq, segment.Status)
		}
	}

	// The new run writes its own provenance; the archived worker cannot update it.
	if _, err := env.svc.EmitExtractedRequirements(ctx, versionapp.ExtractionSource{
		VersionID: 7, AttachmentID: 3, RunID: newRun.ID, SegmentSeq: 1,
	}, []versionapp.ExtractionRequirement{{ExternalID: "FR-001", Title: "新", Confidence: 0.9}}); err != nil {
		t.Fatalf("new run emit: %v", err)
	}
	if _, err := env.svc.EmitExtractedRequirements(ctx, oldSource,
		[]versionapp.ExtractionRequirement{{ExternalID: "FR-001", Title: "旧覆盖", Confidence: 0.2}}); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("late archived emit err=%v, want ErrExtractionRunState", err)
	}
	stored, ok := env.repo.Requirement(7, "FR-001")
	if !ok {
		t.Fatal("FR-001 not stored")
	}
	if stored.RunID != newRun.ID || stored.Title != "新" {
		t.Fatalf("requirement overwritten by archived worker: %+v", stored)
	}

	if _, err := env.svc.RetryRun(ctx, oldRun.ID); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("RetryRun archived err=%v, want ErrExtractionRunState", err)
	}
	if _, err := env.svc.RetrySegment(ctx, oldRun.ID, 1); !errors.Is(err, versionapp.ErrExtractionRunState) {
		t.Fatalf("RetrySegment archived err=%v, want ErrExtractionRunState", err)
	}

	// The new run is unaffected and can be processed to completion.
	newProcessed, err := env.svc.ProcessRun(ctx, newRun.ID)
	if err != nil {
		t.Fatalf("ProcessRun new: %v", err)
	}
	if newProcessed.Status != versionapp.ExtractionRunDone {
		t.Fatalf("new run=%+v", newProcessed)
	}
}

// TestStartArchivesSupersededRunsOnEveryPath pins the archive invariant on both
// the new-run and the replay path: one attachment has at most one active run,
// and a replay never resurrects a superseded run over the active one.
func TestStartArchivesSupersededRunsOnEveryPath(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	first := env.start(t, 7, 3, testSHA("sha-1"), "", "一")
	second := env.start(t, 7, 3, testSHA("sha-2"), "", "二")

	storedFirst, err := env.repo.GetRun(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetRun first: %v", err)
	}
	if storedFirst.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("superseded run status=%s, want failed", storedFirst.Status)
	}

	// Replaying the superseded sha must not archive the active run or return a
	// dead cursor; the active run is the current input.
	replayed, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("sha-1"),
		Segments: testExtractionSegments("一"),
	})
	if err != nil {
		t.Fatalf("replay superseded sha: %v", err)
	}
	latest, err := env.repo.GetLatestRun(ctx, 7, 3)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if replayed.ID != latest.ID || replayed.ID != second.ID {
		t.Fatalf("replay returned run %d, active run is %d (latest %d)", replayed.ID, second.ID, latest.ID)
	}
	active := 0
	for _, run := range mustListRuns(t, env.repo, 7) {
		switch run.Status {
		case versionapp.ExtractionRunPending, versionapp.ExtractionRunRunning, versionapp.ExtractionRunPaused:
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active runs=%d, want 1", active)
	}

	// Replaying the active sha keeps the same active run.
	same, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("sha-2"),
		Segments: testExtractionSegments("二"),
	})
	if err != nil {
		t.Fatalf("replay active sha: %v", err)
	}
	if same.ID != second.ID {
		t.Fatalf("active replay returned %d, want %d", same.ID, second.ID)
	}
}

// TestReplaySupersededShaAfterNewRunSettles pins the recoverable replay path: A
// is archived when B starts; once B is terminal there is no active run, so
// replaying A must revive A in a writable state (not a pending zombie fenced by
// B's higher id) and ProcessRun must settle it without retry loops.
func TestReplaySupersededShaAfterNewRunSettles(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	a := env.start(t, 7, 3, testSHA("sha-a"), "", "A段一", "A段二")
	b := env.start(t, 7, 3, testSHA("sha-b"), "", "B段一")

	archived, err := env.repo.GetRun(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetRun archived A: %v", err)
	}
	if archived.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("A after B Start status=%s, want failed", archived.Status)
	}
	settledB, err := env.svc.ProcessRun(ctx, b.ID)
	if err != nil {
		t.Fatalf("ProcessRun B: %v", err)
	}
	if settledB.Status != versionapp.ExtractionRunDone {
		t.Fatalf("B did not settle: %+v", settledB)
	}

	replayed, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("sha-a"),
		Segments: testExtractionSegments("A段一", "A段二"),
	})
	if err != nil {
		t.Fatalf("replay A after B settled: %v", err)
	}
	if replayed.ID != a.ID {
		t.Fatalf("replay A returned run %d, want %d", replayed.ID, a.ID)
	}
	if replayed.Status != versionapp.ExtractionRunPending || replayed.TotalSegments != 2 {
		t.Fatalf("replayed A=%+v, want pending with 2 segments", replayed)
	}

	// A real processor emits while the run is active; the emit and the segment
	// marks must both be accepted because there is no newer *active* run.
	env.processor.process = func(segment versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		_, emitErr := env.svc.EmitExtractedRequirements(ctx, versionapp.ExtractionSource{
			VersionID: 7, AttachmentID: 3, RunID: segment.RunID, SegmentSeq: segment.Seq,
		}, []versionapp.ExtractionRequirement{{
			ExternalID: fmt.Sprintf("FR-%03d", segment.Seq), Title: "A需求", Confidence: 0.9,
		}})
		if emitErr != nil {
			return versionapp.ExtractionSegmentResult{}, emitErr
		}
		return versionapp.ExtractionSegmentResult{ExtractedCount: 1}, nil
	}
	processed, err := env.svc.ProcessRun(ctx, replayed.ID)
	if err != nil {
		t.Fatalf("ProcessRun replayed A: %v", err)
	}
	if processed.Status != versionapp.ExtractionRunDone || processed.DoneSegments != 2 {
		t.Fatalf("replayed A after ProcessRun=%+v, want done with 2 segments", processed)
	}
	if got := env.repo.RequirementCount(7); got != 2 {
		t.Fatalf("replayed A emitted %d requirements, want 2", got)
	}
	segments, err := env.repo.ListSegments(ctx, replayed.ID)
	if err != nil {
		t.Fatalf("ListSegments replayed A: %v", err)
	}
	for _, segment := range segments {
		if segment.Status == versionapp.ExtractionSegmentRunning ||
			segment.Status == versionapp.ExtractionSegmentPending {
			t.Fatalf("replayed A left segment %d %s", segment.Seq, segment.Status)
		}
	}

	// The settled replay is idempotent: a second pass processes nothing and
	// ProcessNext does not pick the run up forever.
	calls := env.processor.CallCount()
	again, err := env.svc.ProcessRun(ctx, replayed.ID)
	if err != nil {
		t.Fatalf("second ProcessRun replayed A: %v", err)
	}
	if again.Status != versionapp.ExtractionRunDone || env.processor.CallCount() != calls {
		t.Fatalf("second ProcessRun=%+v calls=%d, want done without new calls", again, env.processor.CallCount())
	}
	next, err := env.svc.ProcessNext(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessNext after replay settled: %v", err)
	}
	if next != 0 {
		t.Fatalf("ProcessNext processed %d settled runs, want 0", next)
	}
}

func mustListRuns(t *testing.T, repo *fakeExtractionRepo, versionID int64) []versionapp.ExtractionRun {
	t.Helper()
	runs, err := repo.ListRuns(context.Background(), versionID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	return runs
}

// TestReplayRefreshesSegmentsAndExpected pins re-extract semantics on the
// idempotent replay path: changed segment text reopens the segment, totals and
// expected_count refresh, and a done run never keeps orphan pending segments.
func TestReplayRefreshesSegmentsAndExpected(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	base := versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("replay-input"),
		Segments: testExtractionSegments("第一段", "第二段"),
	}
	run, err := env.svc.Start(ctx, base)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if done, err := env.svc.ProcessRun(ctx, run.ID); err != nil || done.Status != versionapp.ExtractionRunDone {
		t.Fatalf("initial ProcessRun=%+v err=%v", done, err)
	}

	// Unchanged replay stays done and does not reopen completed segments.
	unchanged, err := env.svc.Start(ctx, base)
	if err != nil {
		t.Fatalf("unchanged replay: %v", err)
	}
	if unchanged.ID != run.ID || unchanged.Status != versionapp.ExtractionRunDone || unchanged.DoneSegments != 2 {
		t.Fatalf("unchanged replay=%+v", unchanged)
	}

	// Same sha, changed segment text + document: the changed segment reopens and
	// expected_count is refreshed from the replayed document.
	changed := base
	changed.Segments = testExtractionSegments("第一段", "第二段（修订）")
	changed.DocumentText = "# V1 当前需求\n\nFR-001 登录\nFR-002 退出\nFR-003 下单\n"
	replayed, err := env.svc.Start(ctx, changed)
	if err != nil {
		t.Fatalf("changed replay: %v", err)
	}
	if replayed.ID != run.ID {
		t.Fatalf("changed replay created run %d, want %d", replayed.ID, run.ID)
	}
	if replayed.Status != versionapp.ExtractionRunPending {
		t.Fatalf("replayed run status=%s, want pending (changed segment must reopen)", replayed.Status)
	}
	if replayed.TotalSegments != 2 {
		t.Fatalf("replayed total=%d, want 2", replayed.TotalSegments)
	}
	if replayed.ExpectedCount == nil || *replayed.ExpectedCount != 3 {
		t.Fatalf("replayed expected_count=%v, want 3", replayed.ExpectedCount)
	}
	segments, err := env.repo.ListSegments(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if segments[0].Status != versionapp.ExtractionSegmentDone {
		t.Fatalf("unchanged segment 1 status=%s", segments[0].Status)
	}
	if segments[1].Status != versionapp.ExtractionSegmentPending || segments[1].Attempts != 0 {
		t.Fatalf("changed segment 2=%+v, want pending attempts=0", segments[1])
	}
	if segments[1].TextSHA256 != testSHA("第二段（修订）") {
		t.Fatalf("changed segment text sha not refreshed: %s", segments[1].TextSHA256)
	}

	before := env.processor.CallCount()
	final, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("replay ProcessRun: %v", err)
	}
	if final.Status != versionapp.ExtractionRunDone || final.DoneSegments != 2 {
		t.Fatalf("replayed run=%+v", final)
	}
	if calls := env.processor.CallCount(); calls != before+1 {
		t.Fatalf("replay processed %d segments, want only the changed one", calls-before)
	}
}

// TestReplayDoesNotReviveDoneRunForRunningSegment pins fake/SQL parity for the
// terminal-run reopen predicate: a replay reopens a done/failed run only when it
// has a *pending* segment (SQL: agg.pending > 0). A running segment left by an
// in-flight worker must not be converted into a claimable run by a replay.
func TestReplayDoesNotReviveDoneRunForRunningSegment(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	input := versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("running-segment-parity"),
		Segments: testExtractionSegments("唯一段落"),
	}
	run, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if claimed, err := env.repo.ClaimSegments(ctx, run.ID, 1, testTime); err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimSegments=%d err=%v", len(claimed), err)
	}
	// Model the SQL state directly: a done run whose only segment is running
	// (the service itself never produces this state).
	env.repo.debugSetRunStatus(run.ID, versionapp.ExtractionRunDone)

	replayed, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.Status != versionapp.ExtractionRunDone {
		t.Fatalf("replay revived a done run with only a running segment: %+v", replayed)
	}
}

// TestRetrySegmentResetsOneFailedSegment covers the single-segment retry path
// (the run-level RetryRun path is covered by TestExtractionRetryBackoffAndCap).
func TestRetrySegmentResetsOneFailedSegment(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	run := env.start(t, 7, 3, testSHA("retry-segment"), "", "正常段", "失败段")
	env.processor.process = func(segment versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		if segment.Seq == 2 {
			return versionapp.ExtractionSegmentResult{}, errors.New("segment 2 failed")
		}
		return versionapp.ExtractionSegmentResult{}, nil
	}
	for attempt := 1; attempt <= versionapp.DefaultExtractionMaxAttempts; attempt++ {
		if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
			t.Fatalf("ProcessRun attempt %d: %v", attempt, err)
		}
		env.clock.Advance(24 * time.Hour)
	}
	stored, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != versionapp.ExtractionRunFailed {
		t.Fatalf("run after exhausted attempts=%+v", stored)
	}
	retried, err := env.svc.RetrySegment(ctx, run.ID, 2)
	if err != nil {
		t.Fatalf("RetrySegment: %v", err)
	}
	if retried.Status != versionapp.ExtractionRunPending {
		t.Fatalf("retried run status=%s, want pending", retried.Status)
	}
	segments, _ := env.repo.ListSegments(ctx, run.ID)
	if segments[1].Status != versionapp.ExtractionSegmentPending || segments[1].Attempts != 0 {
		t.Fatalf("retried segment=%+v", segments[1])
	}

	env.processor.process = nil
	env.processor.ResetCalls()
	final, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun after RetrySegment: %v", err)
	}
	if final.Status != versionapp.ExtractionRunDone {
		t.Fatalf("final run=%+v", final)
	}
	if calls := env.processor.Calls(); len(calls) != 1 || calls[0] != 2 {
		t.Fatalf("retry processed seqs=%v, want only segment 2", calls)
	}

	// Typed errors for the impossible retries.
	if _, err := env.svc.RetrySegment(ctx, run.ID, 1); !errors.Is(err, versionapp.ErrExtractionSegmentState) {
		t.Fatalf("RetrySegment done err=%v, want ErrExtractionSegmentState", err)
	}
	if _, err := env.svc.RetrySegment(ctx, run.ID, 99); !errors.Is(err, versionapp.ErrExtractionSegmentNotFound) {
		t.Fatalf("RetrySegment missing err=%v, want ErrExtractionSegmentNotFound", err)
	}
	if _, err := env.svc.RetrySegment(ctx, run.ID, 0); !errors.Is(err, versionapp.ErrExtractionSegmentNotFound) {
		t.Fatalf("RetrySegment zero seq err=%v, want ErrExtractionSegmentNotFound", err)
	}
}

// TestZeroSegmentRun covers an attachment whose parse produced no text chunks:
// the run is created done, never claimable, and reconciliation still reports the
// declared ids.
func TestZeroSegmentRun(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	document := "# V1 当前需求\n\nFR-001 登录\nFR-002 退出\n"
	run, err := env.svc.Start(ctx, versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("empty"), DocumentText: document,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != versionapp.ExtractionRunDone || run.TotalSegments != 0 {
		t.Fatalf("zero-segment run=%+v", run)
	}
	if run.ExpectedCount == nil || *run.ExpectedCount != 2 {
		t.Fatalf("zero-segment expected_count=%v, want 2", run.ExpectedCount)
	}
	processed, err := env.svc.ProcessRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ProcessRun: %v", err)
	}
	if processed.Status != versionapp.ExtractionRunDone || env.processor.CallCount() != 0 {
		t.Fatalf("zero-segment run processed: %+v calls=%d", processed, env.processor.CallCount())
	}

	// Reconciliation still persists the full report for an empty run.
	report, err := env.svc.Reconcile(ctx, run.ID, document, 5)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.Expected != 2 || report.Extracted != 0 || report.Missing != 2 || report.SkippedImages != 5 {
		t.Fatalf("zero-segment report=%+v", report)
	}
	if report.PlannedCount != 0 || len(report.PlannedIDs) != 0 {
		t.Fatalf("zero-segment planned=%+v", report)
	}
	storedRun, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if storedRun.Report == nil || storedRun.Report.Expected != 2 || storedRun.Report.SkippedImages != 5 {
		t.Fatalf("zero-segment persisted report=%+v", storedRun.Report)
	}
}

// TestZeroSegmentReplayStaysDone pins the replay safety of an empty parse: a
// zero-segment run is created done; replaying the same sha must keep it done
// instead of falling through the "no retryable work" branch to failed.
func TestZeroSegmentReplayStaysDone(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	input := versionapp.ExtractionStartInput{
		VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("empty-replay"),
		DocumentText: "# V1 当前需求\n\nFR-001 登录\nFR-002 退出\n",
	}
	first, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if first.Status != versionapp.ExtractionRunDone || first.TotalSegments != 0 {
		t.Fatalf("zero-segment run=%+v, want done with 0 segments", first)
	}

	replayed, err := env.svc.Start(ctx, input)
	if err != nil {
		t.Fatalf("zero-segment replay: %v", err)
	}
	if replayed.ID != first.ID {
		t.Fatalf("zero-segment replay created run %d, want %d", replayed.ID, first.ID)
	}
	if replayed.Status != versionapp.ExtractionRunDone || replayed.TotalSegments != 0 {
		t.Fatalf("zero-segment replay=%+v, want the same run still done", replayed)
	}
	if replayed.ExpectedCount == nil || *replayed.ExpectedCount != 2 {
		t.Fatalf("zero-segment replay expected_count=%v, want 2", replayed.ExpectedCount)
	}
	stored, err := env.svc.GetRun(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != versionapp.ExtractionRunDone || stored.Error != "" {
		t.Fatalf("stored zero-segment run=%+v, want done without error", stored)
	}
}

// TestLatestRunValidatesFields pins the API error field names (the attachment
// parameter used to be reported for an invalid version id).
func TestLatestRunValidatesFields(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	var apiErr *platform.Error
	if _, err := env.svc.LatestRun(ctx, 0, 3); !errors.As(err, &apiErr) || apiErr.Details["field"] != "version_id" {
		t.Fatalf("invalid version err=%v, want invalid_argument field=version_id", err)
	}
	if _, err := env.svc.LatestRun(ctx, 7, 0); !errors.As(err, &apiErr) || apiErr.Details["field"] != "attachment_id" {
		t.Fatalf("invalid attachment err=%v, want invalid_argument field=attachment_id", err)
	}
}

// TestReconcileComputesLowConfidenceAndPersistsReport pins two review
// requirements: low_confidence is derived from the run's requirement rows (the
// caller can no longer supply it) and the full report - including skipped
// images and the V2 planned partition - is persisted on the run.
func TestReconcileComputesLowConfidenceAndPersistsReport(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	document := "# V1 当前需求\n\nFR-001 登录\nFR-002 退出\n\n## V2 规划需求\n\nFR-003 后续能力\nFR-004 后续能力\n"
	run := env.start(t, 7, 3, testSHA(document), document, "需求段落")
	source := versionapp.ExtractionSource{VersionID: 7, AttachmentID: 3, RunID: run.ID, SegmentSeq: 1}
	if _, err := env.svc.EmitExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{
		{ExternalID: "FR-001", Title: "登录", Confidence: 0.9},
		{ExternalID: "FR-002", Title: "退出", Confidence: 0.5},
		{ExternalID: "FR-003", Title: "后续一", Confidence: 0.4},
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	report, err := env.svc.Reconcile(ctx, run.ID, document, 4)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.Expected != 2 || report.Extracted != 2 || report.Missing != 0 {
		t.Fatalf("report=%+v", report)
	}
	if report.LowConfidence != 2 {
		t.Fatalf("low_confidence=%d, want 2 derived from requirement rows", report.LowConfidence)
	}
	if report.SkippedImages != 4 {
		t.Fatalf("skipped_images=%d, want 4", report.SkippedImages)
	}
	if report.PlannedCount != 2 || len(report.PlannedIDs) != 2 {
		t.Fatalf("planned_count=%d planned_ids=%v", report.PlannedCount, report.PlannedIDs)
	}

	stored, err := env.svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Report == nil {
		t.Fatal("full reconciliation report was not persisted on the run")
	}
	if stored.Report.Expected != 2 || stored.Report.Extracted != 2 ||
		stored.Report.Missing != 0 || stored.Report.LowConfidence != 2 ||
		stored.Report.SkippedImages != 4 || stored.Report.PlannedCount != 2 {
		t.Fatalf("persisted report=%+v", stored.Report)
	}
	if len(stored.Report.PlannedIDs) != 2 || len(stored.Report.MissingIDs) != 0 {
		t.Fatalf("persisted report ids=%+v", stored.Report)
	}
}

// TestCountLowConfidenceIgnoresUnscored pins fake/SQL parity for NULL
// confidence: SQL counts `confidence IS NOT NULL AND confidence < threshold`,
// so an unscored requirement must not be reported as low confidence.
func TestCountLowConfidenceIgnoresUnscored(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	run := env.start(t, 7, 3, testSHA("confidence-null"), "", "需求段落")
	source := versionapp.ExtractionSource{VersionID: 7, AttachmentID: 3, RunID: run.ID, SegmentSeq: 1}
	if _, err := env.svc.EmitExtractedRequirements(ctx, source, []versionapp.ExtractionRequirement{
		{ExternalID: "FR-001", Title: "低置信", Confidence: 0.4},
		{ExternalID: "FR-002", Title: "高置信", Confidence: 0.9},
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	env.repo.debugAddUnscoredRequirement(7, run.ID, "FR-003")

	low, err := env.repo.CountLowConfidence(ctx, run.ID, versionapp.DefaultLowConfidenceThreshold)
	if err != nil {
		t.Fatalf("CountLowConfidence: %v", err)
	}
	if low != 1 {
		t.Fatalf("low confidence count=%d, want 1 (the unscored requirement is NULL, not low)", low)
	}

	document := "# V1 当前需求\n\nFR-001 低\nFR-002 高\nFR-003 未评分\n"
	report, err := env.svc.Reconcile(ctx, run.ID, document, 0)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.LowConfidence != 1 {
		t.Fatalf("reconciliation low_confidence=%d, want 1", report.LowConfidence)
	}
}

// TestLeaseHeartbeatPreventsDuplicateClaims pins the lease safety requirement:
// a worker holding a full claim batch renews the outstanding leases while it
// processes, so a second worker cannot reclaim segments whose lease would
// otherwise have expired (5 minute lease, slow processor).
func TestLeaseHeartbeatPreventsDuplicateClaims(t *testing.T) {
	env := newExtractionEnv(t)
	ctx := context.Background()
	texts := make([]string, 8)
	for index := range texts {
		texts[index] = fmt.Sprintf("慢段落 %d", index+1)
	}
	run := env.start(t, 7, 3, testSHA("lease-heartbeat"), "", texts...)
	env.svc.SetClaimBatch(8)
	env.svc.SetLeaseHeartbeat(5 * time.Millisecond)

	entered := make(chan struct{})
	release := make(chan struct{})
	var gateMu sync.Mutex
	gateStarted := false
	var mu sync.Mutex
	processed := map[int]int{}
	env.processor.process = func(segment versionapp.ExtractionSegment) (versionapp.ExtractionSegmentResult, error) {
		gateMu.Lock()
		first := !gateStarted
		if first {
			gateStarted = true
		}
		gateMu.Unlock()
		if first {
			close(entered)
			<-release
		}
		mu.Lock()
		processed[segment.Seq]++
		mu.Unlock()
		return versionapp.ExtractionSegmentResult{}, nil
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := env.svc.ProcessRun(ctx, run.ID)
		firstDone <- err
	}()
	<-entered

	// Simulate a processor that takes longer than the 5 minute lease: the fake
	// clock moves past the lease while the worker is still processing.
	advanced := env.clock.Now().Add(versionapp.DefaultExtractionLeaseTimeout + time.Minute)
	env.clock.Advance(versionapp.DefaultExtractionLeaseTimeout + time.Minute)
	deadline := time.Now().Add(2 * time.Second)
	for env.repo.LastRenewalAt().Before(advanced) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if env.repo.LastRenewalAt().Before(advanced) {
		t.Fatal("worker did not renew its outstanding claim leases after the slow processor began")
	}

	// A second worker must not reclaim the still-running (renewed) segments.
	if _, err := env.svc.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("second worker: %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first worker: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(processed) != 8 {
		t.Fatalf("processed %d segments, want 8", len(processed))
	}
	for seq, count := range processed {
		if count != 1 {
			t.Fatalf("segment %d processed %d times (duplicate claim)", seq, count)
		}
	}
}
