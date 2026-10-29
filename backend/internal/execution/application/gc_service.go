package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// ErrGCDisabled is returned for a non-dry-run purge when EZTEST_GC_ENABLED is
// false. The HTTP layer maps it to 409 conflict. A dry-run is always allowed.
var ErrGCDisabled = errors.New("execution: evidence GC is disabled; set EZTEST_GC_ENABLED=true")

// errGCHashMismatch marks an artifact whose stored bytes no longer match the
// recorded sha256/size. It is a skip, not a delete: the row stays unpurged for
// investigation.
var errGCHashMismatch = errors.New("execution: evidence bytes do not match the recorded sha256/size")

// GCEvidence is one unpurged evidence row returned by the protection query.
type GCEvidence struct {
	ID        int64
	BatchID   int64
	Path      string
	SHA256    string
	SizeBytes int64
	Kind      string
	CreatedAt time.Time
}

// GCRequest is one GC invocation. DryRun is the safe default chosen by the
// HTTP handler; Reason is a bounded operator note copied into purged_reason
// and the audit entry.
type GCRequest struct {
	DryRun  bool
	ActorID int64
	Reason  string
}

// GCReport is the stable response of one GC run (spec 10.2): candidates and
// bytes describe the retained set, protected counts the evidence excluded by
// the protection rules.
type GCReport struct {
	DryRun        bool      `json:"dryRun"`
	Candidates    int       `json:"candidates"`
	Protected     int       `json:"protected"`
	Bytes         int64     `json:"bytes"`
	Deleted       int       `json:"deleted"`
	Failed        int       `json:"failed"`
	Skipped       int       `json:"skipped"`
	RetentionDays int       `json:"retentionDays"`
	BatchKeep     int       `json:"batchKeep"`
	RunAt         time.Time `json:"runAt"`
}

// GCRepository is the persistence surface of the evidence GC. The protection
// rules live in the SQL adapter; the service re-checks IsProtected immediately
// before deleting so a reference created after the listing is never deleted.
type GCRepository interface {
	// ListCandidates returns unpurged evidence older than retentionDays that is
	// not protected by the latest batchKeep batches, an active batch, a
	// published report, an open/confirmed issue or a visual baseline.
	ListCandidates(ctx context.Context, retentionDays, batchKeep int) ([]GCEvidence, error)
	// CountProtected counts the unpurged evidence rows currently protected.
	CountProtected(ctx context.Context, batchKeep int) (int, error)
	// IsProtected re-checks one evidence row against the protection rules.
	IsProtected(ctx context.Context, evidenceID int64, batchKeep int) (bool, error)
	// MarkPurged writes purged_at/purged_reason on one still-unpurged row.
	MarkPurged(ctx context.Context, evidenceID int64, at time.Time, reason string) error
	// RollbackPurged clears the mark when the file removal failed, so the row
	// never claims a purge whose artifact is still present.
	RollbackPurged(ctx context.Context, evidenceID int64, at time.Time) error
}

// GCFileStore is the filesystem subset used by the GC: read for the sha256
// pre-check and remove for the actual purge. *filestore.Store satisfies it.
type GCFileStore interface {
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
	Remove(ctx context.Context, rel string) error
}

// GCAuditEvent is one evidence-GC audit entry. It carries counts, thresholds,
// the actor and the dry-run flag only: never a path, sha256 or evidence content.
type GCAuditEvent struct {
	ActorID       int64
	DryRun        bool
	Candidates    int
	Protected     int
	Deleted       int
	Failed        int
	Skipped       int
	Bytes         int64
	RetentionDays int
	BatchKeep     int
	Reason        string
}

// GCAuditor records one GC run. A nil auditor disables auditing.
type GCAuditor interface {
	GCRunCompleted(ctx context.Context, event GCAuditEvent) error
}

// GCDeps wires one GC service.
type GCDeps struct {
	Repository    GCRepository
	Files         GCFileStore
	Audit         GCAuditor
	Clock         Clock
	Logger        *slog.Logger
	Enabled       bool
	RetentionDays int
	BatchKeep     int
}

// GCService implements the evidence GC use case of spec 10.2. It never deletes
// a protected, unreadable, tampered or unpurgeable artifact.
type GCService struct {
	repo          GCRepository
	files         GCFileStore
	audit         GCAuditor
	clock         Clock
	logger        *slog.Logger
	enabled       bool
	retentionDays int
	batchKeep     int
}

// NewGCService returns the evidence GC service. RetentionDays and BatchKeep are
// clamped to the documented minimums so a zero-valued test configuration still
// cannot delete everything.
func NewGCService(deps GCDeps) *GCService {
	clock := deps.Clock
	if clock == nil {
		clock = wallClock{}
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	retentionDays := deps.RetentionDays
	if retentionDays < 1 {
		retentionDays = 1
	}
	batchKeep := deps.BatchKeep
	if batchKeep < 1 {
		batchKeep = 1
	}
	return &GCService{
		repo:          deps.Repository,
		files:         deps.Files,
		audit:         deps.Audit,
		clock:         clock,
		logger:        logger,
		enabled:       deps.Enabled,
		retentionDays: retentionDays,
		batchKeep:     batchKeep,
	}
}

// Run performs one dry-run or purge. A disabled service rejects a non-dry-run
// before reading candidates; dry-runs always work so an administrator can
// preview the effect before enabling execution.
func (s *GCService) Run(ctx context.Context, req GCRequest) (GCReport, error) {
	if s == nil || s.repo == nil || s.files == nil {
		return GCReport{}, errors.New("execution: GC service is not configured")
	}
	if !req.DryRun && !s.enabled {
		return GCReport{}, ErrGCDisabled
	}
	now := s.clock.Now()
	report := GCReport{
		DryRun:        req.DryRun,
		RetentionDays: s.retentionDays,
		BatchKeep:     s.batchKeep,
		RunAt:         now,
	}
	candidates, err := s.repo.ListCandidates(ctx, s.retentionDays, s.batchKeep)
	if err != nil {
		return GCReport{}, fmt.Errorf("execution: list GC candidates: %w", err)
	}
	report.Candidates = len(candidates)
	report.Protected, err = s.repo.CountProtected(ctx, s.batchKeep)
	if err != nil {
		return GCReport{}, fmt.Errorf("execution: count protected evidence: %w", err)
	}
	for _, candidate := range candidates {
		report.Bytes += candidate.SizeBytes
		if req.DryRun {
			continue
		}
		switch s.purgeOne(ctx, candidate, req) {
		case gcDeleted:
			report.Deleted++
		case gcSkipped:
			report.Skipped++
		case gcFailed:
			report.Failed++
		}
	}
	s.recordAudit(ctx, req, report)
	return report, nil
}

// gcOutcome classifies one candidate purge.
type gcOutcome int

const (
	gcSkipped gcOutcome = iota
	gcFailed
	gcDeleted
)

// purgeOne re-checks references and the artifact digest, marks the row, then
// removes the bytes. The mark is rolled back if removal fails so the metadata
// always reflects whether the file still exists. Failures are logged and never
// abort the rest of the run.
func (s *GCService) purgeOne(ctx context.Context, candidate GCEvidence, req GCRequest) gcOutcome {
	protected, err := s.repo.IsProtected(ctx, candidate.ID, s.batchKeep)
	if err != nil {
		s.logger.ErrorContext(ctx, "evidence gc: re-check reference protection",
			"evidence_id", candidate.ID, "error", err)
		return gcFailed
	}
	if protected {
		s.logger.InfoContext(ctx, "evidence gc: skipped, became protected",
			"evidence_id", candidate.ID)
		return gcSkipped
	}
	if err := s.verifyArtifact(ctx, candidate); err != nil {
		// A missing/tampered artifact is deliberately not deleted and not
		// marked: the metadata row stays unpurged for a later investigation.
		s.logger.WarnContext(ctx, "evidence gc: skipped, artifact verification failed",
			"evidence_id", candidate.ID, "error", err)
		if errors.Is(err, errGCHashMismatch) {
			return gcSkipped
		}
		return gcFailed
	}
	at := s.clock.Now()
	reason := s.purgedReason(req)
	if err := s.repo.MarkPurged(ctx, candidate.ID, at, reason); err != nil {
		s.logger.ErrorContext(ctx, "evidence gc: mark purged",
			"evidence_id", candidate.ID, "error", err)
		return gcFailed
	}
	if err := s.files.Remove(ctx, candidate.Path); err != nil {
		s.logger.ErrorContext(ctx, "evidence gc: remove artifact",
			"evidence_id", candidate.ID, "error", err)
		if rollbackErr := s.repo.RollbackPurged(ctx, candidate.ID, at); rollbackErr != nil {
			s.logger.ErrorContext(ctx, "evidence gc: rollback purged mark",
				"evidence_id", candidate.ID, "error", rollbackErr)
		}
		return gcFailed
	}
	return gcDeleted
}

// verifyArtifact proves the bytes still match the recorded sha256 and size
// before any deletion. Reading at most size+1 detects an oversized file without
// trusting a corrupted size row.
func (s *GCService) verifyArtifact(ctx context.Context, candidate GCEvidence) error {
	if candidate.SizeBytes < 0 {
		return fmt.Errorf("%w: negative recorded size", errGCHashMismatch)
	}
	file, err := s.files.Open(ctx, candidate.Path)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, candidate.SizeBytes+1))
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}
	if int64(len(data)) != candidate.SizeBytes {
		return fmt.Errorf("%w: size %d != recorded %d", errGCHashMismatch, len(data), candidate.SizeBytes)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != candidate.SHA256 {
		return fmt.Errorf("%w: digest mismatch", errGCHashMismatch)
	}
	return nil
}

// purgedReason is the bounded, operator-visible reason stored on the row.
func (s *GCService) purgedReason(req GCRequest) string {
	reason := fmt.Sprintf("gc: retention=%dd batch_keep=%d", s.retentionDays, s.batchKeep)
	if note := sanitizeGCReason(req.Reason); note != "" {
		reason += "; reason=" + note
	}
	return reason
}

// sanitizeGCReason keeps a short printable note. Operator input never carries
// paths or secrets into the database/audit log.
func sanitizeGCReason(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == ' ', r == '-', r == '_', r == '.', r == ':', r == '/':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
		if builder.Len() >= 120 {
			break
		}
	}
	return builder.String()
}

// recordAudit appends the single per-run audit entry. Auditing is best effort:
// the purge already committed, so a sink outage is logged rather than reported
// as a failed GC.
func (s *GCService) recordAudit(ctx context.Context, req GCRequest, report GCReport) {
	if s.audit == nil {
		return
	}
	event := GCAuditEvent{
		ActorID:       req.ActorID,
		DryRun:        report.DryRun,
		Candidates:    report.Candidates,
		Protected:     report.Protected,
		Deleted:       report.Deleted,
		Failed:        report.Failed,
		Skipped:       report.Skipped,
		Bytes:         report.Bytes,
		RetentionDays: report.RetentionDays,
		BatchKeep:     report.BatchKeep,
		Reason:        sanitizeGCReason(req.Reason),
	}
	if err := s.audit.GCRunCompleted(ctx, event); err != nil {
		s.logger.WarnContext(ctx, "evidence gc: audit record failed", "error", err)
	}
}
