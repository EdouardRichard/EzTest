package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// visualEvidenceMaxRead is the hard read bound of one baseline artifact: the
// spec §9.3 5 MiB default plus the legacy 10 MiB evidence cap; a corrupt
// size_bytes row can never force an unbounded allocation.
const visualEvidenceMaxRead int64 = 16 << 20

// visualEvidenceFileStore is the read side of the execution evidence
// filesystem store consumed by the baseline reader.
type visualEvidenceFileStore interface {
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
}

// VisualBaselineEvidenceRepository implements
// application.VisualBaselineEvidenceReader on the execution evidence tables. The
// evidence row is verified to belong to the version (through its batch) and to a
// case run of the case node (through case_run.case_node_id) before any bytes are
// read; the file itself is opened through the execution filestore and its
// sha256/size are re-verified.
type VisualBaselineEvidenceRepository struct {
	q     querier
	files visualEvidenceFileStore
}

// NewVisualBaselineEvidenceRepository returns the baseline evidence reader.
func NewVisualBaselineEvidenceRepository(pool *pgxpool.Pool, files visualEvidenceFileStore) application.VisualBaselineEvidenceReader {
	return &VisualBaselineEvidenceRepository{q: pool, files: files}
}

var _ application.VisualBaselineEvidenceReader = (*VisualBaselineEvidenceRepository)(nil)

// ReadForBaseline reads and verifies one baseline candidate artifact.
func (r *VisualBaselineEvidenceRepository) ReadForBaseline(ctx context.Context, versionID, caseNodeID, evidenceID int64) (application.VisualEvidence, error) {
	var (
		evidence application.VisualEvidence
		path     string
	)
	err := r.q.QueryRow(ctx, `
		SELECT e.id, e.kind, e.mime, e.path, e.sha256, e.size_bytes
		FROM evidence AS e
		JOIN run_batch AS b ON b.id = e.batch_id
		JOIN case_run AS cr ON cr.id = e.case_run_id
		WHERE e.id = $1 AND b.version_id = $2 AND cr.case_node_id = $3`,
		evidenceID, versionID, caseNodeID).Scan(
		&evidence.ID, &evidence.Kind, &evidence.Mime, &path,
		&evidence.SHA256, &evidence.SizeBytes,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.VisualEvidence{}, domain.NewNotFound("evidence", evidenceID)
		}
		return application.VisualEvidence{}, fmt.Errorf("postgres: read visual baseline evidence: %w", err)
	}
	if r.files == nil {
		return application.VisualEvidence{}, errors.New("mindmap: evidence file store is not configured")
	}
	if evidence.SizeBytes < 0 || evidence.SizeBytes > visualEvidenceMaxRead {
		return application.VisualEvidence{}, domain.NewInvalid("evidenceId", "visual_baseline_invalid",
			"the evidence artifact size is outside the readable range")
	}
	file, err := r.files.Open(ctx, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return application.VisualEvidence{}, domain.NewNotFound("evidence", evidenceID)
		}
		return application.VisualEvidence{}, fmt.Errorf("mindmap: open visual baseline artifact: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, evidence.SizeBytes+1))
	if err != nil {
		return application.VisualEvidence{}, fmt.Errorf("mindmap: read visual baseline artifact: %w", err)
	}
	if int64(len(data)) != evidence.SizeBytes {
		return application.VisualEvidence{}, domain.NewInvalid("evidenceId", "visual_baseline_invalid",
			"the evidence bytes do not match the recorded size")
	}
	sum := sha256.Sum256(data)
	if !equalFoldHex(hex.EncodeToString(sum[:]), evidence.SHA256) {
		return application.VisualEvidence{}, domain.NewInvalid("evidenceId", "visual_baseline_invalid",
			"the evidence bytes do not match the recorded sha256")
	}
	evidence.Data = data
	return evidence, nil
}

// equalFoldHex compares two hex digests case-insensitively.
func equalFoldHex(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := 0; index < len(left); index++ {
		a := left[index]
		b := right[index]
		if a >= 'A' && a <= 'F' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'F' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
