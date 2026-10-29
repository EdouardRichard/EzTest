package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/filestore"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// gcFixedClock is a deterministic clock for the GC metadata.
type gcFixedClock struct{ now time.Time }

func (c gcFixedClock) Now() time.Time { return c.now }

// gcFakeRepo is an in-memory GCRepository. MarkPurged/RollbackPurged honour the
// context like the real adapter so the canceled-request tests prove the service
// detaches the destructive phase from the request cancellation.
type gcFakeRepo struct {
	candidates  []application.GCEvidence
	protected   int
	protectedID map[int64]bool
	isProtected map[int64]bool
	listErr     error
	listed      bool

	listedRetention int
	listedKeep      int
	marked          []gcMarked
	rolledBack      []int64
	purged          map[int64]bool
	rollbackCtxErr  error
}

type gcMarked struct {
	id     int64
	at     time.Time
	reason string
}

func (r *gcFakeRepo) ListCandidates(_ context.Context, retentionDays, batchKeep int) ([]application.GCEvidence, error) {
	r.listed = true
	r.listedRetention = retentionDays
	r.listedKeep = batchKeep
	return r.candidates, r.listErr
}

func (r *gcFakeRepo) CountProtected(context.Context, int) (int, error) { return r.protected, nil }

func (r *gcFakeRepo) IsProtected(_ context.Context, evidenceID int64, _ int) (bool, error) {
	return r.isProtected[evidenceID], nil
}

func (r *gcFakeRepo) MarkPurged(ctx context.Context, evidenceID int64, at time.Time, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.purged == nil {
		r.purged = make(map[int64]bool)
	}
	if r.purged[evidenceID] {
		return errors.New("evidence is not an unpurged row")
	}
	r.purged[evidenceID] = true
	r.marked = append(r.marked, gcMarked{id: evidenceID, at: at, reason: reason})
	return nil
}

func (r *gcFakeRepo) RollbackPurged(ctx context.Context, evidenceID int64, _ time.Time) error {
	if err := ctx.Err(); err != nil {
		r.rollbackCtxErr = err
		return err
	}
	if !r.purged[evidenceID] {
		return errors.New("no purge mark to roll back")
	}
	delete(r.purged, evidenceID)
	r.rolledBack = append(r.rolledBack, evidenceID)
	return nil
}

// gcCancelAfterMarkRepo cancels the request context right after MarkPurged,
// emulating a client disconnect between the metadata write and the file removal.
type gcCancelAfterMarkRepo struct {
	application.GCRepository
	afterMark func()
}

func (r gcCancelAfterMarkRepo) MarkPurged(ctx context.Context, evidenceID int64, at time.Time, reason string) error {
	err := r.GCRepository.MarkPurged(ctx, evidenceID, at, reason)
	if r.afterMark != nil {
		r.afterMark()
	}
	return err
}

// gcFakeStore is an in-memory evidence filesystem.
type gcFakeStore struct {
	files        map[string][]byte
	removed      []string
	openErr      error
	removeErr    error
	removeCtxErr error
}

func (s *gcFakeStore) Open(_ context.Context, rel string) (io.ReadCloser, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	data, ok := s.files[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *gcFakeStore) Remove(ctx context.Context, rel string) error {
	s.removeCtxErr = ctx.Err()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.removeErr != nil {
		return s.removeErr
	}
	s.removed = append(s.removed, rel)
	delete(s.files, rel)
	return nil
}

// gcFakeAudit captures the GC audit events.
type gcFakeAudit struct {
	events []application.GCAuditEvent
	err    error
}

func (a *gcFakeAudit) GCRunCompleted(_ context.Context, event application.GCAuditEvent) error {
	a.events = append(a.events, event)
	return a.err
}

func gcTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func gcSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func gcService(repo application.GCRepository, store application.GCFileStore, audit application.GCAuditor, now time.Time) *application.GCService {
	return application.NewGCService(application.GCDeps{
		Repository:    repo,
		Files:         store,
		Audit:         audit,
		Clock:         gcFixedClock{now: now},
		Logger:        gcTestLogger(),
		Enabled:       true,
		RetentionDays: 30,
		BatchKeep:     3,
	})
}

func gcCandidates() ([]application.GCEvidence, *gcFakeStore) {
	data := []byte("evidence-bytes")
	store := &gcFakeStore{files: map[string][]byte{"1/2/10.png": data}}
	return []application.GCEvidence{{
		ID: 10, BatchID: 1, Path: "1/2/10.png", SHA256: gcSHA(data), SizeBytes: int64(len(data)),
		Kind: "screenshot", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}}, store
}

// TestGCDryRunReportsCandidatesWithoutDeleting pins the default-safe mode:
// candidates and bytes are reported, but no file is removed, no row is marked
// and no hash is verified.
func TestGCDryRunReportsCandidatesWithoutDeleting(t *testing.T) {
	candidates, store := gcCandidates()
	repo := &gcFakeRepo{candidates: candidates, protected: 4, isProtected: map[int64]bool{}}
	audit := &gcFakeAudit{}
	service := gcService(repo, store, audit, time.Now())

	report, err := service.Run(context.Background(), application.GCRequest{DryRun: true, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Candidates != 1 || report.Bytes != int64(len([]byte("evidence-bytes"))) {
		t.Fatalf("report=%+v", report)
	}
	if report.Protected != 4 {
		t.Fatalf("protected=%d want 4", report.Protected)
	}
	if report.CandidateLimit != application.GCCandidateLimit {
		t.Fatalf("candidateLimit=%d want %d", report.CandidateLimit, application.GCCandidateLimit)
	}
	if report.Deleted != 0 || report.Skipped != 0 || report.Failed != 0 {
		t.Fatalf("dry-run changed counters: %+v", report)
	}
	if len(store.removed) != 0 || len(repo.marked) != 0 {
		t.Fatalf("dry-run deleted files (%v) or marked rows (%v)", store.removed, repo.marked)
	}
	if repo.listedRetention != 30 || repo.listedKeep != 3 {
		t.Fatalf("repository received retention=%d keep=%d", repo.listedRetention, repo.listedKeep)
	}
	if len(audit.events) != 1 || audit.events[0].DryRun != true || audit.events[0].Candidates != 1 {
		t.Fatalf("audit=%+v", audit.events)
	}
}

// TestGCDisabledRejectsNonDryRun pins the production opt-in: a non-dry-run purge
// fails closed when EZTEST_GC_ENABLED is false, before any candidate read.
func TestGCDisabledRejectsNonDryRun(t *testing.T) {
	candidates, store := gcCandidates()
	repo := &gcFakeRepo{candidates: candidates, isProtected: map[int64]bool{}}
	service := application.NewGCService(application.GCDeps{
		Repository:    repo,
		Files:         store,
		Clock:         gcFixedClock{now: time.Now()},
		Enabled:       false,
		RetentionDays: 30,
		BatchKeep:     3,
	})

	_, err := service.Run(context.Background(), application.GCRequest{DryRun: false, ActorID: 7})
	if !errors.Is(err, application.ErrGCDisabled) {
		t.Fatalf("error=%v want ErrGCDisabled", err)
	}
	if repo.listed || len(store.removed) != 0 {
		t.Fatalf("disabled GC touched the repository or files")
	}
	// A dry-run is always allowed, even when execution is disabled.
	if _, err := service.Run(context.Background(), application.GCRequest{DryRun: true}); err != nil {
		t.Fatalf("dry-run with GC disabled: %v", err)
	}
}

// TestGCRechecksProtectedReferencesBeforeDelete proves the pre-delete reference
// re-check: a candidate that became protected after the listing is skipped and
// its file/row are left untouched.
func TestGCRechecksProtectedReferencesBeforeDelete(t *testing.T) {
	candidates, store := gcCandidates()
	repo := &gcFakeRepo{
		candidates:  candidates,
		isProtected: map[int64]bool{10: true},
	}
	service := gcService(repo, store, &gcFakeAudit{}, time.Now())

	report, err := service.Run(context.Background(), application.GCRequest{DryRun: false, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Skipped != 1 || report.Deleted != 0 {
		t.Fatalf("report=%+v", report)
	}
	if len(store.removed) != 0 || len(repo.marked) != 0 {
		t.Fatalf("protected candidate was touched")
	}
}

// TestGCHashMismatchDoesNotDelete pins the sha256 pre-delete check: bytes that
// do not match the recorded digest are never deleted or marked purged.
func TestGCHashMismatchDoesNotDelete(t *testing.T) {
	candidates, store := gcCandidates()
	candidates[0].SHA256 = strings.Repeat("0", 64)
	repo := &gcFakeRepo{candidates: candidates, isProtected: map[int64]bool{}}
	service := gcService(repo, store, &gcFakeAudit{}, time.Now())

	report, err := service.Run(context.Background(), application.GCRequest{DryRun: false, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Skipped != 1 || report.Deleted != 0 || report.Failed != 0 {
		t.Fatalf("report=%+v", report)
	}
	if len(store.removed) != 0 || len(repo.marked) != 0 {
		t.Fatalf("hash mismatch still touched the file/row")
	}
}

// TestGCMissingFileIsSkipped pins the missing-artifact classification: the row
// is already orphaned (file gone), so it is reported as skipped, not as a
// failure, and neither marked nor removed.
func TestGCMissingFileIsSkipped(t *testing.T) {
	candidates, store := gcCandidates()
	delete(store.files, candidates[0].Path)
	repo := &gcFakeRepo{candidates: candidates, isProtected: map[int64]bool{}}
	service := gcService(repo, store, &gcFakeAudit{}, time.Now())

	report, err := service.Run(context.Background(), application.GCRequest{DryRun: false, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Skipped != 1 || report.Failed != 0 || report.Deleted != 0 {
		t.Fatalf("report=%+v", report)
	}
	if len(repo.marked) != 0 || len(store.removed) != 0 {
		t.Fatalf("missing file still marked/removed")
	}
}

// TestGCPurgeMarksMetadataDeletesFileAndAudits pins the successful non-dry-run:
// the row is marked with the purged metadata, the verified file is removed and
// one audit entry records the actor and counts without evidence content.
func TestGCPurgeMarksMetadataDeletesFileAndAudits(t *testing.T) {
	candidates, store := gcCandidates()
	repo := &gcFakeRepo{candidates: candidates, protected: 2, isProtected: map[int64]bool{}}
	audit := &gcFakeAudit{}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	service := gcService(repo, store, audit, now)

	report, err := service.Run(context.Background(), application.GCRequest{DryRun: false, ActorID: 7, Reason: "manual"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Deleted != 1 || report.Failed != 0 || report.Skipped != 0 || report.Bytes != int64(len([]byte("evidence-bytes"))) {
		t.Fatalf("report=%+v", report)
	}
	if len(repo.marked) != 1 {
		t.Fatalf("marked=%v want one row", repo.marked)
	}
	if repo.marked[0].id != 10 || !repo.marked[0].at.Equal(now) {
		t.Fatalf("marked=%+v", repo.marked[0])
	}
	if !strings.Contains(repo.marked[0].reason, "retention=30d") || !strings.Contains(repo.marked[0].reason, "manual") {
		t.Fatalf("purged reason=%q", repo.marked[0].reason)
	}
	if len(store.removed) != 1 || store.removed[0] != "1/2/10.png" {
		t.Fatalf("removed=%v", store.removed)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events=%v", audit.events)
	}
	event := audit.events[0]
	if event.ActorID != 7 || event.DryRun || event.Deleted != 1 || event.Bytes != report.Bytes {
		t.Fatalf("audit event=%+v", event)
	}
}

// TestGCPurgeReasonPreservesUnicode pins the operator note: Chinese/Unicode text
// is kept in purged_reason instead of being replaced by underscores; only
// control characters are sanitized.
func TestGCPurgeReasonPreservesUnicode(t *testing.T) {
	candidates, store := gcCandidates()
	repo := &gcFakeRepo{candidates: candidates, isProtected: map[int64]bool{}}
	service := gcService(repo, store, &gcFakeAudit{}, time.Now())

	_, err := service.Run(context.Background(), application.GCRequest{
		DryRun: false, ActorID: 7, Reason: "季度清理：证据-保留\n第二行",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(repo.marked) != 1 {
		t.Fatalf("marked=%v", repo.marked)
	}
	reason := repo.marked[0].reason
	if !strings.Contains(reason, "季度清理：证据-保留") {
		t.Fatalf("purged reason=%q lost the Unicode note", reason)
	}
	if strings.ContainsAny(reason, "\n\r") {
		t.Fatalf("purged reason=%q kept a control character", reason)
	}
}

// TestGCRemoveFailureRollsBackPurgeMark proves a failed file removal rolls the
// metadata mark back, so the row is never left claiming a file it still has.
func TestGCRemoveFailureRollsBackPurgeMark(t *testing.T) {
	candidates, store := gcCandidates()
	store.removeErr = errors.New("disk is read-only")
	repo := &gcFakeRepo{candidates: candidates, isProtected: map[int64]bool{}}
	service := gcService(repo, store, &gcFakeAudit{}, time.Now())

	report, err := service.Run(context.Background(), application.GCRequest{DryRun: false, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Failed != 1 || report.Deleted != 0 {
		t.Fatalf("report=%+v", report)
	}
	if len(repo.rolledBack) != 1 || repo.rolledBack[0] != 10 {
		t.Fatalf("rolledBack=%v want [10]", repo.rolledBack)
	}
}

// TestGCPurgeRemovesAfterRequestContextCanceled pins the purge ordering under a
// client disconnect after MarkPurged: the removal must run on a detached
// context, so the file is actually deleted and the row is not left half-purged.
func TestGCPurgeRemovesAfterRequestContextCanceled(t *testing.T) {
	candidates, store := gcCandidates()
	repo := &gcFakeRepo{candidates: candidates, isProtected: map[int64]bool{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAfterMark := gcCancelAfterMarkRepo{GCRepository: repo, afterMark: cancel}
	service := gcService(cancelAfterMark, store, &gcFakeAudit{}, time.Now())

	report, err := service.Run(ctx, application.GCRequest{DryRun: false, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Deleted != 1 || report.Failed != 0 {
		t.Fatalf("report=%+v want deleted=1", report)
	}
	if store.removeCtxErr != nil {
		t.Fatalf("Remove received a canceled context: %v", store.removeCtxErr)
	}
	if len(store.removed) != 1 {
		t.Fatalf("removed=%v want the artifact removed", store.removed)
	}
	if len(repo.rolledBack) != 0 || !repo.purged[10] {
		t.Fatalf("purged=%v rolledBack=%v want the purged mark kept", repo.purged, repo.rolledBack)
	}
}

// TestGCPurgeRollbackAfterRequestContextCanceled pins the critical failure path:
// once MarkPurged committed, a canceled request context must not prevent the
// compensating rollback. The rollback has to clear purged_at so the row never
// claims a file it still has.
func TestGCPurgeRollbackAfterRequestContextCanceled(t *testing.T) {
	candidates, store := gcCandidates()
	store.removeErr = errors.New("disk is read-only")
	repo := &gcFakeRepo{candidates: candidates, isProtected: map[int64]bool{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAfterMark := gcCancelAfterMarkRepo{GCRepository: repo, afterMark: cancel}
	service := gcService(cancelAfterMark, store, &gcFakeAudit{}, time.Now())

	report, err := service.Run(ctx, application.GCRequest{DryRun: false, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Failed != 1 || report.Deleted != 0 {
		t.Fatalf("report=%+v want failed=1", report)
	}
	if store.removeCtxErr != nil {
		t.Fatalf("Remove received a canceled context: %v", store.removeCtxErr)
	}
	if repo.rollbackCtxErr != nil {
		t.Fatalf("RollbackPurged received a canceled context: %v", repo.rollbackCtxErr)
	}
	if len(repo.rolledBack) != 1 || repo.rolledBack[0] != 10 {
		t.Fatalf("rolledBack=%v want [10]", repo.rolledBack)
	}
	if repo.purged[10] {
		t.Fatal("purged_at was not cleared after the failed removal")
	}
}

// TestGCAuditFailureDoesNotFailTheRun pins audit as best-effort: the purge has
// already committed, so an audit sink outage is logged, not reported as a GC
// failure.
func TestGCAuditFailureDoesNotFailTheRun(t *testing.T) {
	candidates, store := gcCandidates()
	repo := &gcFakeRepo{candidates: candidates, isProtected: map[int64]bool{}}
	audit := &gcFakeAudit{err: errors.New("audit down")}
	service := gcService(repo, store, audit, time.Now())

	report, err := service.Run(context.Background(), application.GCRequest{DryRun: false, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Deleted != 1 {
		t.Fatalf("report=%+v", report)
	}
}

// TestGCRepositoryProtectionIntegration exercises the real reference-protection
// SQL against PostgreSQL. It is skipped unless EZTEST_TEST_DATABASE_URL points
// at a dedicated _test database.
func TestGCRepositoryProtectionIntegration(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-60 * 24 * time.Hour)

	insertBatch := func(id int64, batchNo int, status string) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash, created_by, created_at, updated_at)
			VALUES ($1, 9001, $2, $3, $4, $4, 7, now(), now())`,
			id, batchNo, status, strings.Repeat("a", 64))
		if err != nil {
			t.Fatalf("insert batch %d: %v", id, err)
		}
	}
	for i, status := range []string{"done", "done", "done", "done", "running"} {
		insertBatch(int64(100+i), i+1, status)
	}

	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatalf("filestore: %v", err)
	}
	relByID := make(map[int64]string)
	insertEvidence := func(id, batchID int64, kind string, created time.Time) {
		t.Helper()
		rel := filepath.ToSlash(filepath.Join("9001", "1", strconv.FormatInt(id, 10)+".png"))
		data := []byte("evidence-" + strconv.FormatInt(id, 10))
		path, digest, size, err := store.Write(ctx, rel, data)
		if err != nil {
			t.Fatalf("write evidence %d: %v", id, err)
		}
		relByID[id] = path
		_, err = pool.Exec(ctx, `
			INSERT INTO evidence (id, batch_id, kind, path, sha256, size_bytes, mime, metadata, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'image/png', '{}'::jsonb, $7, $7)`,
			id, batchID, kind, path, digest, size, created)
		if err != nil {
			t.Fatalf("insert evidence %d: %v", id, err)
		}
	}

	// The visual-baseline evidence kind is added by the parallel visual-assertion
	// migration. When this worktree does not have it yet, the baseline-protection
	// row is exercised as ordinary evidence; the rest of the rules still apply.
	var kindConstraint string
	_ = pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'ck_evidence_kind'`).Scan(&kindConstraint)
	baselineAvailable := strings.Contains(kindConstraint, "visual_baseline")
	baselineKind := "screenshot"
	if baselineAvailable {
		baselineKind = "visual_baseline"
	}

	insertEvidence(1, 100, "screenshot", old)  // unreferenced + expired -> candidate
	insertEvidence(2, 100, baselineKind, old)  // baseline (when available) -> protected
	insertEvidence(3, 100, "screenshot", old)  // published report ref -> NOT a protection claim
	insertEvidence(4, 101, "screenshot", old)  // open issue ref -> protected
	insertEvidence(5, 101, "screenshot", old)  // dismissed issue ref -> candidate
	insertEvidence(6, 102, "screenshot", old)  // latest-N batch -> protected
	insertEvidence(7, 103, "screenshot", old)  // latest-N batch -> protected
	insertEvidence(8, 104, "screenshot", old)  // running batch -> protected
	insertEvidence(9, 100, "screenshot", now)  // young -> not a candidate
	insertEvidence(10, 100, "screenshot", old) // stale report ref -> NOT a protection claim

	insertReport := func(id int64, revision int, status string, evidenceID string) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO report (id, version_id, batch_id, revision, status, content, input_fingerprint, created_at, updated_at)
			VALUES ($1, 9001, 100, $2, $3, '{}'::jsonb, $4, now(), now())`,
			id, revision, status, strings.Repeat("b", 64))
		if err != nil {
			t.Fatalf("insert report %d: %v", id, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO report_ref (report_id, ref_kind, ref_id) VALUES ($1, 'evidence', $2)`,
			id, evidenceID); err != nil {
			t.Fatalf("insert report ref %d: %v", id, err)
		}
	}
	insertReport(500, 1, "published", "3")
	insertReport(501, 2, "stale", "10")

	insertIssue := func(id int64, status, reason string) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO issue (id, version_id, source, category, severity, status, title,
				created_by, updated_by, dismissed_reason, created_at, updated_at)
			VALUES ($1, 9001, 'human', 'defect', 'minor', $2, 'GC issue', 7, 7, $3, now(), now())`,
			id, status, reason)
		if err != nil {
			t.Fatalf("insert issue %d: %v", id, err)
		}
	}
	insertIssue(600, "open", "")
	insertIssue(601, "dismissed", "not a defect")
	if _, err = pool.Exec(ctx, `INSERT INTO issue_ref (issue_id, ref_kind, ref_id) VALUES (600, 'evidence', '4'), (601, 'evidence', '5')`); err != nil {
		t.Fatalf("insert issue refs: %v", err)
	}

	repo := postgres.NewGCRepository(pool)
	protectedFalse := func(id int64) {
		t.Helper()
		got, err := repo.IsProtected(ctx, id, 3)
		if err != nil {
			t.Fatalf("IsProtected(%d): %v", id, err)
		}
		if got {
			t.Fatalf("IsProtected(%d)=true want false", id)
		}
	}
	protectedTrue := func(id int64) {
		t.Helper()
		got, err := repo.IsProtected(ctx, id, 3)
		if err != nil {
			t.Fatalf("IsProtected(%d): %v", id, err)
		}
		if !got {
			t.Fatalf("IsProtected(%d)=false want true", id)
		}
	}

	wantProtected := 4
	if baselineAvailable {
		wantProtected = 5
	}
	wantCandidates := []int64{1, 3, 5, 10}
	if !baselineAvailable {
		wantCandidates = []int64{1, 2, 3, 5, 10}
	}

	// Every protection class answers true for its own row: open-issue
	// issue_ref, the latest-N batches, the running batch and (when the visual
	// migration is present) the visual baseline.
	for _, id := range []int64{4, 6, 7, 8} {
		protectedTrue(id)
	}
	if baselineAvailable {
		protectedTrue(2)
	}
	// A report_ref(ref_kind='evidence') row is not written by report generation:
	// the report snapshot stores evidence counts, never evidence ids. Neither a
	// published nor a stale report therefore claims evidence, so the GC must not
	// pretend to protect it (fix round 1, option B).
	protectedFalse(3)
	protectedFalse(10)
	protectedFalse(1)
	protectedFalse(5)
	protectedFalse(9)

	gotProtected, err := repo.CountProtected(ctx, 3)
	if err != nil {
		t.Fatalf("CountProtected: %v", err)
	}
	if gotProtected != wantProtected {
		t.Fatalf("protected=%d want %d (baseline kind available=%v)", gotProtected, wantProtected, baselineAvailable)
	}
	candidates, err := repo.ListCandidates(ctx, 30, 3)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	if len(ids) != len(wantCandidates) {
		t.Fatalf("candidates=%v want %v", ids, wantCandidates)
	}
	for _, want := range wantCandidates {
		if !containsID(ids, want) {
			t.Fatalf("candidates=%v want %v", ids, wantCandidates)
		}
	}

	// Create a reference after the candidate listing, then flip it: the
	// pre-delete IsProtected re-check must observe both transitions.
	insertIssue(602, "open", "")
	if _, err = pool.Exec(ctx, `INSERT INTO issue_ref (issue_id, ref_kind, ref_id) VALUES (602, 'evidence', '1')`); err != nil {
		t.Fatalf("insert late issue ref: %v", err)
	}
	protectedTrue(1)
	if _, err = pool.Exec(ctx, `UPDATE issue SET status = 'dismissed', dismissed_reason = 'duplicate' WHERE id = 602`); err != nil {
		t.Fatalf("dismiss late issue: %v", err)
	}
	protectedFalse(1)
	if _, err = pool.Exec(ctx, `UPDATE issue SET status = 'open', dismissed_reason = '' WHERE id = 601`); err != nil {
		t.Fatalf("reopen dismissed issue: %v", err)
	}
	protectedTrue(5)
	if _, err = pool.Exec(ctx, `UPDATE issue SET status = 'dismissed', dismissed_reason = 'not a defect' WHERE id = 601`); err != nil {
		t.Fatalf("dismiss reopened issue: %v", err)
	}
	protectedFalse(5)

	audit := &gcFakeAudit{}
	service := application.NewGCService(application.GCDeps{
		Repository:    repo,
		Files:         store,
		Audit:         audit,
		Clock:         gcFixedClock{now: now},
		Logger:        gcTestLogger(),
		Enabled:       true,
		RetentionDays: 30,
		BatchKeep:     3,
	})
	report, err := service.Run(ctx, application.GCRequest{DryRun: false, ActorID: 7, Reason: "integration"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Deleted != len(wantCandidates) || report.Failed != 0 || report.Skipped != 0 {
		t.Fatalf("report=%+v", report)
	}
	for _, id := range wantCandidates {
		var purged *time.Time
		if err := pool.QueryRow(ctx, `SELECT purged_at FROM evidence WHERE id = $1`, id).Scan(&purged); err != nil {
			t.Fatalf("read purged_at %d: %v", id, err)
		}
		if purged == nil {
			t.Fatalf("evidence %d was not marked purged", id)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relByID[id]))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("evidence %d file still exists after purge (err=%v)", id, err)
		}
	}
	for _, id := range []int64{2, 3, 4, 6, 7, 8, 9, 10} {
		if containsID(wantCandidates, id) {
			continue
		}
		var purged *time.Time
		if err := pool.QueryRow(ctx, `SELECT purged_at FROM evidence WHERE id = $1`, id).Scan(&purged); err != nil {
			t.Fatalf("read purged_at %d: %v", id, err)
		}
		if purged != nil {
			t.Fatalf("protected evidence %d was purged", id)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relByID[id]))); err != nil {
			t.Fatalf("protected evidence %d file was removed: %v", id, err)
		}
	}
	// A purged row is part of the protection set, so a second run never
	// re-processes it.
	protectedTrue(wantCandidates[0])
}

// TestGCRepositoryPurgeMetadataIntegration covers the purged_at transitions of
// the real SQL: MarkPurged is a one-row conditional update, RollbackPurged only
// clears the exact mark written by this run.
func TestGCRepositoryPurgeMetadataIntegration(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-60 * 24 * time.Hour)

	_, err := pool.Exec(ctx, `
		INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash, created_by, created_at, updated_at)
		VALUES (200, 9002, 1, 'done', $1, $1, 7, now(), now())`, strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO evidence (id, batch_id, kind, path, sha256, size_bytes, mime, metadata, created_at, updated_at)
		VALUES (20, 200, 'screenshot', $1, $2, 12, 'image/png', '{}'::jsonb, $3, $3)`,
		"9002/1/20.png", strings.Repeat("d", 64), old); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}

	repo := postgres.NewGCRepository(pool)
	at := now.Add(-time.Minute)
	if err := repo.MarkPurged(ctx, 20, at, "gc: retention=30d batch_keep=3"); err != nil {
		t.Fatalf("MarkPurged: %v", err)
	}
	var purged *time.Time
	var reason string
	if err := pool.QueryRow(ctx, `SELECT purged_at, purged_reason FROM evidence WHERE id = 20`).Scan(&purged, &reason); err != nil {
		t.Fatalf("read purged row: %v", err)
	}
	if purged == nil || !purged.UTC().Equal(at) || reason != "gc: retention=30d batch_keep=3" {
		t.Fatalf("purged=%v reason=%q", purged, reason)
	}

	// MarkPurged is conditional on purged_at IS NULL: a second call and a
	// missing row both report a zero-row update instead of silently succeeding.
	if err := repo.MarkPurged(ctx, 20, at, "again"); err == nil {
		t.Fatal("MarkPurged on an already purged row succeeded; want zero-row error")
	}
	if err := repo.MarkPurged(ctx, 999999, at, "missing"); err == nil {
		t.Fatal("MarkPurged on a missing row succeeded; want zero-row error")
	}

	// RollbackPurged only clears the exact mark this run wrote: a foreign
	// timestamp leaves the row untouched (and is reported as an error).
	if err := repo.RollbackPurged(ctx, 20, at.Add(time.Second)); err == nil {
		t.Fatal("RollbackPurged with a foreign mark succeeded; want zero-row error")
	}
	if err := pool.QueryRow(ctx, `SELECT purged_at FROM evidence WHERE id = 20`).Scan(&purged); err != nil {
		t.Fatalf("read purged row after foreign rollback: %v", err)
	}
	if purged == nil {
		t.Fatal("RollbackPurged with a foreign mark cleared the row")
	}
	if err := repo.RollbackPurged(ctx, 20, at); err != nil {
		t.Fatalf("RollbackPurged: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT purged_at, purged_reason FROM evidence WHERE id = 20`).Scan(&purged, &reason); err != nil {
		t.Fatalf("read rolled back row: %v", err)
	}
	if purged != nil || reason != "" {
		t.Fatalf("rolled back purged=%v reason=%q want NULL/empty", purged, reason)
	}
}

// gcCancelAfterMarkRepository wraps the real repository and cancels the request
// context immediately after MarkPurged commits.
type gcCancelAfterMarkRepository struct {
	application.GCRepository
	cancel context.CancelFunc
}

func (r gcCancelAfterMarkRepository) MarkPurged(ctx context.Context, evidenceID int64, at time.Time, reason string) error {
	err := r.GCRepository.MarkPurged(ctx, evidenceID, at, reason)
	r.cancel()
	return err
}

// gcContextRecordingStore wraps the real filestore, records the context state
// Remove receives and can fail one artifact removal.
type gcContextRecordingStore struct {
	inner     application.GCFileStore
	failPath  string
	removeCtx []error
}

func (s *gcContextRecordingStore) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	return s.inner.Open(ctx, rel)
}

func (s *gcContextRecordingStore) Remove(ctx context.Context, rel string) error {
	s.removeCtx = append(s.removeCtx, ctx.Err())
	if rel == s.failPath {
		return errors.New("simulated remove failure")
	}
	return s.inner.Remove(ctx, rel)
}

// TestGCPurgeRollbackIntegrationAfterCanceledRequest proves end to end that a
// request cancellation between MarkPurged and the file removal still rolls the
// real purged_at back, and the file is retained.
func TestGCPurgeRollbackIntegrationAfterCanceledRequest(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-60 * 24 * time.Hour)

	for id, batchNo := range map[int64]int{300: 1, 301: 2} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash, created_by, created_at, updated_at)
			VALUES ($1, 9003, $2, 'done', $3, $3, 7, now(), now())`,
			id, batchNo, strings.Repeat("e", 64)); err != nil {
			t.Fatalf("insert batch %d: %v", id, err)
		}
	}
	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatalf("filestore: %v", err)
	}
	rel, digest, size, err := store.Write(ctx, "9003/1/30.png", []byte("evidence-30"))
	if err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO evidence (id, batch_id, kind, path, sha256, size_bytes, mime, metadata, created_at, updated_at)
		VALUES (30, 300, 'screenshot', $1, $2, $3, 'image/png', '{}'::jsonb, $4, $4)`,
		rel, digest, size, old); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}

	repo := postgres.NewGCRepository(pool)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recordingStore := &gcContextRecordingStore{inner: store, failPath: rel}
	service := application.NewGCService(application.GCDeps{
		Repository:    gcCancelAfterMarkRepository{GCRepository: repo, cancel: cancel},
		Files:         recordingStore,
		Audit:         &gcFakeAudit{},
		Clock:         gcFixedClock{now: now},
		Logger:        gcTestLogger(),
		Enabled:       true,
		RetentionDays: 30,
		BatchKeep:     1,
	})
	report, err := service.Run(requestCtx, application.GCRequest{DryRun: false, ActorID: 7})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Failed != 1 || report.Deleted != 0 {
		t.Fatalf("report=%+v want failed=1", report)
	}
	if len(recordingStore.removeCtx) != 1 || recordingStore.removeCtx[0] != nil {
		t.Fatalf("Remove contexts=%v want one live (non-canceled) context", recordingStore.removeCtx)
	}
	var purged *time.Time
	if err := pool.QueryRow(ctx, `SELECT purged_at FROM evidence WHERE id = 30`).Scan(&purged); err != nil {
		t.Fatalf("read purged_at: %v", err)
	}
	if purged != nil {
		t.Fatal("purged_at was not rolled back after the failed removal")
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("artifact was removed despite the failed purge: %v", err)
	}
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
