package versionapp

// v0.22 A-7 deterministic requirement extraction worker (spec R-A7-6..R-A7-9,
// docs/version-V0.22/A §7.6/§7.7).
//
// The worker owns the run/segment cursor, not a model loop: attachment
// extraction (Task 15/16) produces the deterministic segment plan, the worker
// claims segments with FOR UPDATE SKIP LOCKED through its repository port and
// hands each claimed chunk to the injected segment processor (the LLM +
// requirement.extraction_emit path wired by Task 18/36). Model/plan/fact work
// never leaks into this package.
//
// Idempotency and recovery:
//   - run key = (version_id, attachment_id, input_sha256); Start twice returns
//     the same run and segments are inserted with INSERT ... ON CONFLICT
//     (run_id, seq) DO NOTHING. Start locks (version_id, attachment_id), so one
//     attachment always has a single active run: the new-run path archives every
//     other active run and the replay path returns the active run when the
//     requested sha was already superseded;
//   - a changed document sha opens a new run and archives the previous
//     pending/running/paused run as failed. Archived runs are fenced: segment
//     transitions and requirement emits from a worker that was in flight when
//     the archive happened are rejected;
//   - segment claims increment attempts, failed segments are retried with
//     attempts-based exponential backoff (SegmentRetryDelay) up to
//     DefaultExtractionMaxAttempts, and a running lease older than
//     DefaultExtractionLeaseTimeout returns to pending (error cleared). A worker
//     renews the leases of its outstanding claim batch (heartbeat) so a slow
//     processor cannot let a second worker reclaim the same segments;
//   - done/skipped segments are never claimed again, so replay is a no-op.
//
// The requirement sink upserts by (version_id, external_id) (source_key
// "extraction:<external_id>") and updates instead of inserting a duplicate.
// Counts (total/done/failed/extracted) are recomputed in the same transaction
// as the segment transition; the full reconciliation report is persisted in
// requirement_extraction_run.report.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// ExtractionRunStatus is the lifecycle state of one extraction run.
type ExtractionRunStatus string

// Extraction run statuses (requirement_extraction_run.status CHECK).
const (
	ExtractionRunPending ExtractionRunStatus = "pending"
	ExtractionRunRunning ExtractionRunStatus = "running"
	ExtractionRunPaused  ExtractionRunStatus = "paused"
	ExtractionRunDone    ExtractionRunStatus = "done"
	ExtractionRunFailed  ExtractionRunStatus = "failed"
)

// ExtractionSegmentStatus is the cursor state of one segment
// (requirement_extraction_segment.status CHECK).
type ExtractionSegmentStatus string

// Extraction segment statuses.
const (
	ExtractionSegmentPending ExtractionSegmentStatus = "pending"
	ExtractionSegmentRunning ExtractionSegmentStatus = "running"
	ExtractionSegmentDone    ExtractionSegmentStatus = "done"
	ExtractionSegmentFailed  ExtractionSegmentStatus = "failed"
	ExtractionSegmentSkipped ExtractionSegmentStatus = "skipped"
)

// Worker defaults (R-A7-7).
const (
	// DefaultExtractionClaimBatch is the number of segments one claim takes.
	DefaultExtractionClaimBatch = 8
	// DefaultExtractionMaxAttempts caps how often one segment is attempted.
	DefaultExtractionMaxAttempts = 3
	// DefaultExtractionLeaseTimeout is the stale running lease threshold: a
	// running segment renewed earlier than this returns to pending.
	DefaultExtractionLeaseTimeout = 5 * time.Minute
	// DefaultExtractionRetryBaseDelay is the first retry backoff. Retry n
	// waits base * 2^(n-1) (30s, 60s, ...), persisted in segment.renewed_at.
	DefaultExtractionRetryBaseDelay = 30 * time.Second
	// DefaultExtractionLeaseHeartbeat is how often a worker renews the leases
	// of its outstanding claimed segments while processing a batch.
	DefaultExtractionLeaseHeartbeat = DefaultExtractionLeaseTimeout / 3
)

// Extraction worker sentinel errors. Operations on paused/done/failed runs and
// on non-claimable segments report these directly so callers can map them to
// their API contract with errors.Is.
var (
	ErrExtractionRunNotFound     = errors.New("requirement extraction run not found")
	ErrExtractionRunState        = errors.New("requirement extraction run state conflict")
	ErrExtractionSegmentNotFound = errors.New("requirement extraction segment not found")
	ErrExtractionSegmentState    = errors.New("requirement extraction segment state conflict")
)

// ExtractionRun is one requirement_extraction_run row plus its aggregate counts.
type ExtractionRun struct {
	ID             int64               `json:"id"`
	VersionID      int64               `json:"version_id"`
	AttachmentID   int64               `json:"attachment_id"`
	InputSHA256    string              `json:"input_sha256"`
	Status         ExtractionRunStatus `json:"status"`
	TotalSegments  int                 `json:"total_segments"`
	DoneSegments   int                 `json:"done_segments"`
	FailedSegments int                 `json:"failed_segments"`
	ExpectedCount  *int                `json:"expected_count,omitempty"`
	ExtractedCount int                 `json:"extracted_count"`
	Error          string              `json:"error,omitempty"`
	// Report is the full R-A7-8 reconciliation report persisted in
	// requirement_extraction_run.report. Nil until Reconcile has run once.
	Report    *ReconciliationReport `json:"report,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

// ExtractionSegment is one requirement_extraction_segment row. It never carries
// the segment text: text is re-derived from the attachment and checked against
// TextSHA256, so the DB only stores positions and identities.
type ExtractionSegment struct {
	ID             int64                   `json:"id"`
	RunID          int64                   `json:"run_id"`
	Seq            int                     `json:"seq"`
	HeadingPath    string                  `json:"heading_path"`
	CharStart      int                     `json:"char_start"`
	CharEnd        int                     `json:"char_end"`
	TextSHA256     string                  `json:"text_sha256"`
	Status         ExtractionSegmentStatus `json:"status"`
	Attempts       int                     `json:"attempts"`
	ExtractedCount int                     `json:"extracted_count"`
	Error          string                  `json:"error,omitempty"`
	RenewedAt      *time.Time              `json:"renewed_at,omitempty"`
}

// ExtractionStartInput is one idempotent Start call. Segments come from
// SegmentExtraction (Task 16); DocumentText is optional and only feeds the
// declared-FR reconciliation count (it is never persisted by this package).
type ExtractionStartInput struct {
	VersionID    int64
	AttachmentID int64
	InputSHA256  string
	Segments     []Segment
	DocumentText string
}

// ExtractionSegmentResult is one segment processor outcome. A skipped segment
// (for example a segment that only contains images, A §7.9) is terminal and
// never retried.
type ExtractionSegmentResult struct {
	ExtractedCount int
	Skipped        bool
	SkipReason     string
}

// ExtractionSegmentProcessor performs the model extraction for one claimed
// segment and writes requirements through the ExtractionSink. Task 18/36
// implements it; this package only owns the cursor and retry policy.
type ExtractionSegmentProcessor interface {
	ProcessSegment(ctx context.Context, run ExtractionRun, segment ExtractionSegment) (ExtractionSegmentResult, error)
}

// ExtractionSource identifies one emitted requirement batch (the Task 16 sink
// contract, reused so ExtractionService implements tools.ExtractionSink
// directly and Task 36 can wire tools.NewExtractionEmitTool).
type ExtractionSource = tools.ExtractionSource

// ExtractionRequirement is one validated fixed-core-schema requirement.
type ExtractionRequirement = tools.ExtractionRequirement

// ExtractionEmitCounts is the created/updated result of one sink call.
type ExtractionEmitCounts = tools.ExtractionEmitCounts

// ExtractionRequirementSourceKeyPrefix namespaces the deterministic
// requirement.source_key of extracted requirements: the requirement identity is
// (version_id, external_id), stored as source_key "extraction:<external_id>"
// because requirement has a partial unique index on (version_id, source_key).
const ExtractionRequirementSourceKeyPrefix = "extraction:"

// ExtractionRequirementSourceKey returns the deterministic source_key used to
// upsert one extracted requirement without inserting a duplicate.
func ExtractionRequirementSourceKey(externalID string) string {
	return ExtractionRequirementSourceKeyPrefix + strings.TrimSpace(externalID)
}

// ExtractionRepository persists runs, segments and extracted requirements. The
// PostgreSQL implementation is versionpg.ExtractionRepository; tests use an
// in-memory fake. SQL implementations must claim with FOR UPDATE SKIP LOCKED and
// update the run counts in the same transaction as a segment transition.
type ExtractionRepository interface {
	// CreateRun is idempotent on (version_id, attachment_id, input_sha256). It
	// archives superseded pending/running/paused runs of the same attachment
	// and inserts missing segments without duplicating (run_id, seq).
	CreateRun(ctx context.Context, run ExtractionRun, segments []ExtractionSegment) (ExtractionRun, error)
	GetRun(ctx context.Context, runID int64) (ExtractionRun, error)
	GetLatestRun(ctx context.Context, versionID, attachmentID int64) (ExtractionRun, error)
	ListRuns(ctx context.Context, versionID int64) ([]ExtractionRun, error)
	ListRunsByStatus(ctx context.Context, statuses []ExtractionRunStatus, limit int) ([]ExtractionRun, error)
	ListSegments(ctx context.Context, runID int64) ([]ExtractionSegment, error)
	// ClaimSegments resets stale running leases, then claims pending (and due
	// failed, attempts < DefaultExtractionMaxAttempts) segments
	// FOR UPDATE SKIP LOCKED. Claim sets running, attempts+1, renewed_at=now
	// and clears the error. It must return no segments when the run is no
	// longer pending/running (pause/archive gate).
	ClaimSegments(ctx context.Context, runID int64, limit int, now time.Time) ([]ExtractionSegment, error)
	// RenewSegmentLeases extends the running lease of the given claimed
	// segments (worker heartbeat). Terminal segments are not touched.
	RenewSegmentLeases(ctx context.Context, runID int64, seqs []int, now time.Time) (int, error)
	MarkSegmentDone(ctx context.Context, runID int64, seq, extractedCount int, now time.Time) error
	MarkSegmentSkipped(ctx context.Context, runID int64, seq int, reason string, now time.Time) error
	// MarkSegmentFailed records the failure and sets renewed_at to the
	// attempts-based exponential backoff retry time.
	MarkSegmentFailed(ctx context.Context, runID int64, seq int, message string, now time.Time) error
	// TransitionRun applies a guarded status transition (from one of `from`).
	// It reports false when the current status does not match.
	TransitionRun(ctx context.Context, runID int64, from []ExtractionRunStatus, to ExtractionRunStatus, now time.Time) (bool, error)
	// RetryFailedSegments resets failed segments to a fresh attempt budget.
	// Superseded (archived) runs are rejected with ErrExtractionRunState.
	RetryFailedSegments(ctx context.Context, runID int64, now time.Time) (int, error)
	// RetrySegment resets one failed segment (and a failed run back to pending).
	RetrySegment(ctx context.Context, runID int64, seq int, now time.Time) error
	// SaveReconciliation persists the full report plus the expected/extracted
	// counts read by the UI.
	SaveReconciliation(ctx context.Context, runID int64, report ReconciliationReport, now time.Time) error
	// CountLowConfidence counts the requirements attributed to the run whose
	// confidence is below threshold (NULL confidence is not counted).
	CountLowConfidence(ctx context.Context, runID int64, threshold float64) (int, error)
	// ListExtractedExternalIDs returns the external ids currently attributed to
	// this run (requirement.extraction_run_id), used for missing_ids.
	ListExtractedExternalIDs(ctx context.Context, versionID, runID int64) ([]string, error)
	// UpsertExtractedRequirements updates by (version_id, external_id) and
	// never inserts a second requirement for the same external id. Superseded
	// (archived) runs are rejected with ErrExtractionRunState.
	UpsertExtractedRequirements(ctx context.Context, source ExtractionSource, requirements []ExtractionRequirement, now time.Time) (ExtractionEmitCounts, error)
}

// ExtractionService runs the deterministic extraction cursor and owns the
// requirement sink.
type ExtractionService struct {
	repo       ExtractionRepository
	processor  ExtractionSegmentProcessor
	events     eventingapp.Publisher
	clock      clock.Clock
	claimBatch int
	heartbeat  time.Duration
}

// NewExtractionService wires the worker. events may be nil (best-effort
// progress events); clk defaults to the system clock.
func NewExtractionService(repo ExtractionRepository, processor ExtractionSegmentProcessor, events eventingapp.Publisher, clk clock.Clock) *ExtractionService {
	if clk == nil {
		clk = clock.System{}
	}
	return &ExtractionService{
		repo: repo, processor: processor, events: events, clock: clk,
		claimBatch: DefaultExtractionClaimBatch,
		heartbeat:  DefaultExtractionLeaseHeartbeat,
	}
}

// SetClaimBatch overrides how many segments one claim takes (tests/tuning).
func (s *ExtractionService) SetClaimBatch(batch int) {
	if batch > 0 {
		s.claimBatch = batch
	}
}

// SetLeaseHeartbeat overrides the outstanding-claim renewal interval. Zero
// disables the background heartbeat (tests that do not exercise lease safety).
func (s *ExtractionService) SetLeaseHeartbeat(interval time.Duration) {
	s.heartbeat = interval
}

// Start opens (or replays) the extraction run of one attachment input. The same
// (version_id, attachment_id, input_sha256) always returns the same run; a
// changed sha creates a new run and archives the old one. On replay, changed
// (run_id, seq) segment text reopens that segment and totals/expected_count are
// refreshed so a done run never keeps orphan pending segments.
func (s *ExtractionService) Start(ctx context.Context, input ExtractionStartInput) (ExtractionRun, error) {
	if s == nil || s.repo == nil {
		return ExtractionRun{}, errors.New("extraction service has no repository")
	}
	if err := validateExtractionStart(input); err != nil {
		return ExtractionRun{}, err
	}
	now := s.clock.Now()
	segments := make([]ExtractionSegment, 0, len(input.Segments))
	for _, segment := range input.Segments {
		textSHA := strings.TrimSpace(segment.TextSHA256)
		if textSHA == "" {
			sum := sha256.Sum256([]byte(segment.Text))
			textSHA = hex.EncodeToString(sum[:])
		}
		segments = append(segments, ExtractionSegment{
			Seq:         segment.Seq,
			HeadingPath: segment.HeadingPath,
			CharStart:   segment.CharStart,
			CharEnd:     segment.CharEnd,
			TextSHA256:  textSHA,
		})
	}
	run := ExtractionRun{
		VersionID:     input.VersionID,
		AttachmentID:  input.AttachmentID,
		InputSHA256:   strings.ToLower(strings.TrimSpace(input.InputSHA256)),
		Status:        ExtractionRunPending,
		TotalSegments: len(segments),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if len(segments) == 0 {
		// An empty document has no work segments; the run is immediately done
		// instead of staying claimable forever.
		run.Status = ExtractionRunDone
	}
	if strings.TrimSpace(input.DocumentText) != "" {
		report := ReconcileDocument(input.DocumentText, ReconciliationInput{})
		expected := report.Expected
		run.ExpectedCount = &expected
	}
	return s.repo.CreateRun(ctx, run, segments)
}

// ProcessRun drains one run deterministically: it claims batches of segments,
// processes each exactly once, records done/skipped/failed and returns the
// final run snapshot. Concurrent workers may call it for the same run; the
// repository claim guarantees a segment is processed by one worker only.
func (s *ExtractionService) ProcessRun(ctx context.Context, runID int64) (ExtractionRun, error) {
	if s == nil || s.repo == nil {
		return ExtractionRun{}, errors.New("extraction service has no repository")
	}
	if runID <= 0 {
		return ExtractionRun{}, ErrExtractionRunNotFound
	}
	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return ExtractionRun{}, err
	}
	switch run.Status {
	case ExtractionRunPaused, ExtractionRunDone, ExtractionRunFailed:
		return run, nil
	case ExtractionRunPending:
		transitioned, err := s.repo.TransitionRun(ctx, runID,
			[]ExtractionRunStatus{ExtractionRunPending}, ExtractionRunRunning, s.clock.Now())
		if err != nil {
			return ExtractionRun{}, err
		}
		if transitioned {
			run.Status = ExtractionRunRunning
			s.publish(ctx, run, "extraction.started", map[string]any{
				"status":         string(run.Status),
				"total_segments": run.TotalSegments,
			})
		} else {
			// Another worker won the start transition.
			run, err = s.repo.GetRun(ctx, runID)
			if err != nil {
				return ExtractionRun{}, err
			}
			if run.Status != ExtractionRunRunning {
				return run, nil
			}
		}
	}
	if s.processor == nil {
		return run, errors.New("extraction service has no segment processor")
	}

	worked := false
	for {
		if err := ctx.Err(); err != nil {
			return run, err
		}
		// Re-read the run before every claim: Pause, archive (changed sha) or a
		// terminal transition made while this worker was processing a previous
		// batch must stop the loop instead of draining more segments.
		current, err := s.repo.GetRun(ctx, runID)
		if err != nil {
			return run, err
		}
		run = current
		if run.Status != ExtractionRunPending && run.Status != ExtractionRunRunning {
			break
		}
		segments, err := s.repo.ClaimSegments(ctx, runID, s.claimBatch, s.clock.Now())
		if err != nil {
			return run, err
		}
		if len(segments) == 0 {
			break
		}
		worked = true
		if err := s.processClaimedBatch(ctx, &run, segments); err != nil {
			if ctx.Err() != nil {
				// The in-flight segment lease stays running and is reset by the
				// next worker after DefaultExtractionLeaseTimeout.
				return run, ctx.Err()
			}
			if errors.Is(err, ErrExtractionRunState) {
				// The run was archived (or otherwise changed) while the batch
				// was in flight. Stop cleanly and report the current cursor.
				current, getErr := s.repo.GetRun(ctx, runID)
				if getErr != nil {
					return run, getErr
				}
				return current, nil
			}
			return run, err
		}
	}

	final, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return run, err
	}
	if worked && (final.Status == ExtractionRunDone || final.Status == ExtractionRunFailed) {
		s.publish(ctx, final, "extraction.done", map[string]any{
			"status":          string(final.Status),
			"total_segments":  final.TotalSegments,
			"done_segments":   final.DoneSegments,
			"failed_segments": final.FailedSegments,
			"extracted_count": final.ExtractedCount,
		})
	}
	return final, nil
}

// processClaimedBatch executes one claim batch and keeps the batch leases alive
// while it runs. The heartbeat matters when one batch takes longer than
// DefaultExtractionLeaseTimeout: without it a second worker could reset the
// unrenewed running segments and process them twice.
func (s *ExtractionService) processClaimedBatch(ctx context.Context, run *ExtractionRun, segments []ExtractionSegment) error {
	stop := s.startLeaseHeartbeat(ctx, run.ID, segments)
	defer stop()
	for index, segment := range segments {
		// Renewing the outstanding tail before each segment is cheap and keeps
		// the batch claim alive even if the background ticker is disabled.
		s.renewSegmentLeases(ctx, run.ID, segments[index:])
		result, processErr := s.processor.ProcessSegment(ctx, *run, segment)
		now := s.clock.Now()
		if processErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if markErr := s.repo.MarkSegmentFailed(ctx, run.ID, segment.Seq, processErr.Error(), now); markErr != nil {
				return markErr
			}
			s.publish(ctx, *run, "extraction.segment_failed", map[string]any{
				"seq":       segment.Seq,
				"attempts":  segment.Attempts,
				"error":     processErr.Error(),
				"retryable": segment.Attempts < DefaultExtractionMaxAttempts,
			})
			continue
		}
		if result.Skipped {
			if markErr := s.repo.MarkSegmentSkipped(ctx, run.ID, segment.Seq, result.SkipReason, now); markErr != nil {
				return markErr
			}
			continue
		}
		if markErr := s.repo.MarkSegmentDone(ctx, run.ID, segment.Seq, result.ExtractedCount, now); markErr != nil {
			return markErr
		}
		s.publish(ctx, *run, "extraction.progress", map[string]any{
			"seq":             segment.Seq,
			"extracted_count": result.ExtractedCount,
		})
	}
	return nil
}

// startLeaseHeartbeat renews the leases of one outstanding claim batch until the
// returned stop function is called. It is best-effort: a renewal failure never
// fails extraction (the lease then recovers through the normal timeout path).
func (s *ExtractionService) startLeaseHeartbeat(ctx context.Context, runID int64, segments []ExtractionSegment) func() {
	if s.heartbeat <= 0 || len(segments) == 0 || s.repo == nil {
		return func() {}
	}
	seqs := make([]int, 0, len(segments))
	for _, segment := range segments {
		seqs = append(seqs, segment.Seq)
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				_, _ = s.repo.RenewSegmentLeases(heartbeatCtx, runID, seqs, s.clock.Now())
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *ExtractionService) renewSegmentLeases(ctx context.Context, runID int64, segments []ExtractionSegment) {
	if s.repo == nil || len(segments) == 0 {
		return
	}
	seqs := make([]int, 0, len(segments))
	for _, segment := range segments {
		seqs = append(seqs, segment.Seq)
	}
	_, _ = s.repo.RenewSegmentLeases(ctx, runID, seqs, s.clock.Now())
}

// ProcessNext processes up to limit runs in ascending id order. Claims are
// segment-level, so concurrent ProcessNext workers are safe; startup may list
// the same run twice but no segment is processed twice.
func (s *ExtractionService) ProcessNext(ctx context.Context, limit int) (int, error) {
	if s == nil || s.repo == nil {
		return 0, errors.New("extraction service has no repository")
	}
	runs, err := s.repo.ListRunsByStatus(ctx,
		[]ExtractionRunStatus{ExtractionRunPending, ExtractionRunRunning}, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		if _, err := s.ProcessRun(ctx, run.ID); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

// Pause moves a pending/running run to paused (R-A7-9 UI pause).
func (s *ExtractionService) Pause(ctx context.Context, runID int64) (ExtractionRun, error) {
	run, err := s.mustGetRun(ctx, runID)
	if err != nil {
		return ExtractionRun{}, err
	}
	if run.Status != ExtractionRunPending && run.Status != ExtractionRunRunning {
		return ExtractionRun{}, ErrExtractionRunState
	}
	transitioned, err := s.repo.TransitionRun(ctx, runID,
		[]ExtractionRunStatus{ExtractionRunPending, ExtractionRunRunning}, ExtractionRunPaused, s.clock.Now())
	if err != nil {
		return ExtractionRun{}, err
	}
	if !transitioned {
		return ExtractionRun{}, ErrExtractionRunState
	}
	return s.mustGetRun(ctx, runID)
}

// Resume moves a paused run back to pending so the worker picks it up again.
func (s *ExtractionService) Resume(ctx context.Context, runID int64) (ExtractionRun, error) {
	run, err := s.mustGetRun(ctx, runID)
	if err != nil {
		return ExtractionRun{}, err
	}
	if run.Status != ExtractionRunPaused {
		return ExtractionRun{}, ErrExtractionRunState
	}
	transitioned, err := s.repo.TransitionRun(ctx, runID,
		[]ExtractionRunStatus{ExtractionRunPaused}, ExtractionRunPending, s.clock.Now())
	if err != nil {
		return ExtractionRun{}, err
	}
	if !transitioned {
		return ExtractionRun{}, ErrExtractionRunState
	}
	return s.mustGetRun(ctx, runID)
}

// RetryRun retries a failed run: failed segments get a fresh attempt budget and
// the run returns to pending. A superseded (archived) run cannot be retried.
func (s *ExtractionService) RetryRun(ctx context.Context, runID int64) (ExtractionRun, error) {
	run, err := s.mustGetRun(ctx, runID)
	if err != nil {
		return ExtractionRun{}, err
	}
	if err := s.ensureRunNotSuperseded(ctx, run); err != nil {
		return ExtractionRun{}, err
	}
	if run.Status != ExtractionRunFailed {
		return ExtractionRun{}, ErrExtractionRunState
	}
	now := s.clock.Now()
	transitioned, err := s.repo.TransitionRun(ctx, runID,
		[]ExtractionRunStatus{ExtractionRunFailed}, ExtractionRunPending, now)
	if err != nil {
		return ExtractionRun{}, err
	}
	if !transitioned {
		return ExtractionRun{}, ErrExtractionRunState
	}
	if _, err := s.repo.RetryFailedSegments(ctx, runID, now); err != nil {
		return ExtractionRun{}, err
	}
	return s.mustGetRun(ctx, runID)
}

// RetrySegment retries one terminal failed segment (and a failed run).
func (s *ExtractionService) RetrySegment(ctx context.Context, runID int64, seq int) (ExtractionRun, error) {
	run, err := s.mustGetRun(ctx, runID)
	if err != nil {
		return ExtractionRun{}, err
	}
	if err := s.ensureRunNotSuperseded(ctx, run); err != nil {
		return ExtractionRun{}, err
	}
	if seq <= 0 {
		return ExtractionRun{}, ErrExtractionSegmentNotFound
	}
	if err := s.repo.RetrySegment(ctx, runID, seq, s.clock.Now()); err != nil {
		return ExtractionRun{}, err
	}
	return s.mustGetRun(ctx, runID)
}

// GetRun reads one run (progress API, Task 18).
func (s *ExtractionService) GetRun(ctx context.Context, runID int64) (ExtractionRun, error) {
	return s.mustGetRun(ctx, runID)
}

// LatestRun reads the newest run of one attachment.
func (s *ExtractionService) LatestRun(ctx context.Context, versionID, attachmentID int64) (ExtractionRun, error) {
	if s == nil || s.repo == nil {
		return ExtractionRun{}, errors.New("extraction service has no repository")
	}
	if versionID <= 0 {
		return ExtractionRun{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if attachmentID <= 0 {
		return ExtractionRun{}, platform.Invalid("attachment_id", "must be a positive decimal ID")
	}
	return s.repo.GetLatestRun(ctx, versionID, attachmentID)
}

// ListRuns lists the runs of one version (progress API, Task 18).
func (s *ExtractionService) ListRuns(ctx context.Context, versionID int64) ([]ExtractionRun, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("extraction service has no repository")
	}
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return s.repo.ListRuns(ctx, versionID)
}

// Reconcile computes the R-A7-8 report and persists the full report plus
// expected_count/extracted_count on the run. low_confidence is derived from the
// run's requirement rows (requirement.confidence below
// DefaultLowConfidenceThreshold) instead of trusting a caller; skippedImages is
// accepted from the parser coverage and persisted in the report.
func (s *ExtractionService) Reconcile(ctx context.Context, runID int64, documentText string, skippedImages int) (ReconciliationReport, error) {
	run, err := s.mustGetRun(ctx, runID)
	if err != nil {
		return ReconciliationReport{}, err
	}
	extracted, err := s.repo.ListExtractedExternalIDs(ctx, run.VersionID, run.ID)
	if err != nil {
		return ReconciliationReport{}, err
	}
	lowConfidence, err := s.repo.CountLowConfidence(ctx, run.ID, DefaultLowConfidenceThreshold)
	if err != nil {
		return ReconciliationReport{}, err
	}
	report := ReconcileDocument(documentText, ReconciliationInput{
		ExtractedIDs:  extracted,
		LowConfidence: lowConfidence,
		SkippedImages: skippedImages,
	})
	if err := s.repo.SaveReconciliation(ctx, run.ID, report, s.clock.Now()); err != nil {
		return ReconciliationReport{}, err
	}
	return report, nil
}

// EmitExtractedRequirements implements tools.ExtractionSink: the model emits
// one validated batch per segment, the sink upserts by (version_id,
// external_id), sets run/source provenance and never inserts duplicates.
//
// Task 18 wiring: the actual database identity is requirement.source_key =
// "extraction:<external_id>" (the partial unique index is (version_id,
// source_key)); external_id is written alongside it for reconciliation. A
// superseded/archived run is rejected with ErrExtractionRunState.
func (s *ExtractionService) EmitExtractedRequirements(ctx context.Context, source ExtractionSource, requirements []ExtractionRequirement) (ExtractionEmitCounts, error) {
	if s == nil || s.repo == nil {
		return ExtractionEmitCounts{}, errors.New("extraction service has no repository")
	}
	if source.VersionID <= 0 || source.RunID <= 0 {
		return ExtractionEmitCounts{}, platform.Invalid("extraction_source", "version_id and run_id are required")
	}
	run, err := s.repo.GetRun(ctx, source.RunID)
	if err != nil {
		return ExtractionEmitCounts{}, err
	}
	if run.VersionID != source.VersionID ||
		(source.AttachmentID > 0 && run.AttachmentID != source.AttachmentID) {
		return ExtractionEmitCounts{}, ErrExtractionRunNotFound
	}
	if len(requirements) == 0 {
		return ExtractionEmitCounts{}, nil
	}
	return s.repo.UpsertExtractedRequirements(ctx, source, requirements, s.clock.Now())
}

var _ tools.ExtractionSink = (*ExtractionService)(nil)

// SegmentRetryDelay is the attempts-based exponential backoff persisted in
// segment.renewed_at before a failed segment may be claimed again.
func SegmentRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := DefaultExtractionRetryBaseDelay
	for index := 1; index < attempts; index++ {
		if delay >= 24*time.Hour {
			break
		}
		delay *= 2
	}
	return delay
}

// ---------- internals ----------

func (s *ExtractionService) mustGetRun(ctx context.Context, runID int64) (ExtractionRun, error) {
	if s == nil || s.repo == nil {
		return ExtractionRun{}, errors.New("extraction service has no repository")
	}
	if runID <= 0 {
		return ExtractionRun{}, ErrExtractionRunNotFound
	}
	return s.repo.GetRun(ctx, runID)
}

// ensureRunNotSuperseded rejects operations on a run that was archived because a
// newer run of the same attachment took over (changed input_sha256). The repo
// enforces the same fence transactionally; this check gives typed errors before
// attempting a write.
func (s *ExtractionService) ensureRunNotSuperseded(ctx context.Context, run ExtractionRun) error {
	latest, err := s.repo.GetLatestRun(ctx, run.VersionID, run.AttachmentID)
	if err != nil {
		return err
	}
	if latest.ID != run.ID {
		return ErrExtractionRunState
	}
	return nil
}

// publish emits one best-effort progress event. A nil publisher or a failing
// publisher never fails the extraction.
func (s *ExtractionService) publish(ctx context.Context, run ExtractionRun, eventType string, payload map[string]any) {
	if s.events == nil {
		return
	}
	body := make(map[string]any, len(payload)+10)
	for key, value := range payload {
		body[key] = value
	}
	body["run_id"] = platformid.Format(run.ID)
	body["attachment_id"] = platformid.Format(run.AttachmentID)
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel: "version:" + platformid.Format(run.VersionID),
		Type:    eventType,
		Scope: map[string]any{
			"version_id":        platformid.Format(run.VersionID),
			"attachment_id":     platformid.Format(run.AttachmentID),
			"extraction_run_id": platformid.Format(run.ID),
		},
		Payload:   body,
		RequestID: requestid.From(ctx),
	})
}

func validateExtractionStart(input ExtractionStartInput) error {
	if input.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if input.AttachmentID <= 0 {
		return platform.Invalid("attachment_id", "must be a positive decimal ID")
	}
	sha := strings.ToLower(strings.TrimSpace(input.InputSHA256))
	if len(sha) != 64 || !isHexSHA256(sha) {
		return platform.Invalid("input_sha256", "must be a 64-character hexadecimal sha256")
	}
	seen := make(map[int]struct{}, len(input.Segments))
	for index, segment := range input.Segments {
		if segment.Seq <= 0 {
			return platform.Invalid(fmt.Sprintf("segments[%d].seq", index), "must be positive")
		}
		if _, duplicate := seen[segment.Seq]; duplicate {
			return platform.Invalid(fmt.Sprintf("segments[%d].seq", index), "duplicate segment seq")
		}
		seen[segment.Seq] = struct{}{}
		if segment.CharStart < 0 || segment.CharEnd < segment.CharStart {
			return platform.Invalid(fmt.Sprintf("segments[%d]", index), "invalid character range")
		}
	}
	return nil
}

func isHexSHA256(value string) bool {
	for _, candidate := range value {
		switch {
		case candidate >= '0' && candidate <= '9':
		case candidate >= 'a' && candidate <= 'f':
		default:
			return false
		}
	}
	return true
}
