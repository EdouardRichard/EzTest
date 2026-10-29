package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// version_change_repository.go persists the P8 per-batch change snapshots
// (design spec §4.2/§7.2, D58). It also serves the read-only run_batch
// projection the snapshot generation needs, following the same read-only
// cross-context pattern as the P7 version change feed: no execution row is ever
// written from this context.

// versionChangeColumnList is the shared column order of every version_change
// query and the order scanVersionChange expects.
const versionChangeColumnList = `id, version_id, batch_id, base_commit, head_commit,
	history_rewritten, changed_files, impacted_cases, unmapped_count, items,
	computed_at, created_at`

// upsertVersionChangeSQL stores one snapshot at most once per
// (version_id, batch_id); a concurrent or repeated read keeps the first row.
const upsertVersionChangeSQL = `INSERT INTO version_change
		(id, version_id, batch_id, base_commit, head_commit, history_rewritten,
		 changed_files, impacted_cases, unmapped_count, items, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11)
		ON CONFLICT (version_id, batch_id) DO NOTHING`

// listVersionBatchesSQL is the read-only run_batch projection of one version.
// It selects only the columns the snapshot generation consumes and never locks
// or mutates execution state.
const listVersionBatchesSQL = `SELECT id, version_id, batch_no, status, kind,
		parent_batch_id, base_fallback_reason,
		COALESCE(code_baseline_commit, ''), COALESCE(head_commit, ''), created_at
	FROM run_batch
	WHERE version_id = $1
	ORDER BY batch_no, id`

// versionChangeItemJSON is the persisted shape of one items entry. The domain
// type has no JSON tags, so the repository owns the stored document shape; it
// matches the API field names and never carries code content.
type versionChangeItemJSON struct {
	File       string                     `json:"file"`
	ChangeKind string                     `json:"changeKind"`
	Subjects   []versionChangeSubjectJSON `json:"subjects"`
	CaseIDs    []string                   `json:"caseIds"`
	Unmapped   bool                       `json:"unmapped"`
}

// versionChangeSubjectJSON is one changed entity summary.
type versionChangeSubjectJSON struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// marshalVersionChangeItems encodes the snapshot summaries as the JSONB document
// stored in version_change.items. An empty list is stored as [] and the nested
// arrays are always [] rather than null.
func marshalVersionChangeItems(items []domain.VersionChangeItem) (string, error) {
	entries := make([]versionChangeItemJSON, 0, len(items))
	for _, item := range items {
		subjects := make([]versionChangeSubjectJSON, 0, len(item.Subjects))
		for _, subject := range item.Subjects {
			subjects = append(subjects, versionChangeSubjectJSON{Kind: subject.Kind, Name: subject.Name})
		}
		caseIDs := make([]string, 0, len(item.CaseIDs))
		caseIDs = append(caseIDs, item.CaseIDs...)
		entries = append(entries, versionChangeItemJSON{
			File:       item.File,
			ChangeKind: item.ChangeKind,
			Subjects:   subjects,
			CaseIDs:    caseIDs,
			Unmapped:   item.Unmapped,
		})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// unmarshalVersionChangeItems decodes a stored items document.
func unmarshalVersionChangeItems(raw []byte) ([]domain.VersionChangeItem, error) {
	if len(raw) == 0 {
		return []domain.VersionChangeItem{}, nil
	}
	var entries []versionChangeItemJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	items := make([]domain.VersionChangeItem, 0, len(entries))
	for _, entry := range entries {
		subjects := make([]domain.VersionChangeSubject, 0, len(entry.Subjects))
		for _, subject := range entry.Subjects {
			subjects = append(subjects, domain.VersionChangeSubject{Kind: subject.Kind, Name: subject.Name})
		}
		caseIDs := make([]string, 0, len(entry.CaseIDs))
		caseIDs = append(caseIDs, entry.CaseIDs...)
		items = append(items, domain.VersionChangeItem{
			File:       entry.File,
			ChangeKind: entry.ChangeKind,
			Subjects:   subjects,
			CaseIDs:    caseIDs,
			Unmapped:   entry.Unmapped,
		})
	}
	return items, nil
}

// scanVersionChange scans one row in versionChangeColumnList order and decodes
// the JSONB items document.
func scanVersionChange(row rowScanner) (domain.VersionChange, error) {
	var (
		change domain.VersionChange
		items  []byte
	)
	if err := row.Scan(
		&change.ID,
		&change.VersionID,
		&change.BatchID,
		&change.BaseCommit,
		&change.HeadCommit,
		&change.HistoryRewritten,
		&change.ChangedFiles,
		&change.ImpactedCases,
		&change.UnmappedCount,
		&items,
		&change.ComputedAt,
		&change.CreatedAt,
	); err != nil {
		return domain.VersionChange{}, err
	}
	decoded, err := unmarshalVersionChangeItems(items)
	if err != nil {
		return domain.VersionChange{}, fmt.Errorf("decode version_change items: %w", err)
	}
	change.Items = decoded
	return change, nil
}

// VersionChangeRepo implements domain.VersionChangeRepository on a pgx pool.
type VersionChangeRepo struct {
	pool *pgxpool.Pool
}

// NewVersionChangeRepository returns a version-change repository backed by pg.
func NewVersionChangeRepository(pg *pgxpool.Pool) *VersionChangeRepo {
	return &VersionChangeRepo{pool: pg}
}

var _ domain.VersionChangeRepository = (*VersionChangeRepo)(nil)

// ListByVersion returns the persisted snapshots of one version ordered by
// batch id. The service reorders the entries by batch number.
func (r *VersionChangeRepo) ListByVersion(ctx context.Context, versionID int64) ([]domain.VersionChange, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+versionChangeColumnList+`
		 FROM version_change
		 WHERE version_id = $1
		 ORDER BY batch_id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list version changes: %w", err)
	}
	defer rows.Close()

	changes := make([]domain.VersionChange, 0)
	for rows.Next() {
		change, err := scanVersionChange(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan version change: %w", err)
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list version changes: %w", err)
	}
	return changes, nil
}

// Upsert stores change once per (version_id, batch_id) and returns the stored
// row plus whether this call created it. The first snapshot wins: a later read
// never overwrites a batch's recorded history (D58).
func (r *VersionChangeRepo) Upsert(ctx context.Context, change domain.VersionChange) (domain.VersionChange, bool, error) {
	if change.ID == 0 {
		return domain.VersionChange{}, false, missingIDError("version_change")
	}
	items, err := marshalVersionChangeItems(change.Items)
	if err != nil {
		return domain.VersionChange{}, false, fmt.Errorf("postgres: encode version_change items: %w", err)
	}
	tag, err := r.pool.Exec(ctx, upsertVersionChangeSQL,
		change.ID, change.VersionID, change.BatchID, change.BaseCommit, change.HeadCommit,
		change.HistoryRewritten, change.ChangedFiles, change.ImpactedCases,
		change.UnmappedCount, items, change.ComputedAt,
	)
	if err != nil {
		return domain.VersionChange{}, false, fmt.Errorf("postgres: upsert version change: %w", err)
	}
	created := tag.RowsAffected() == 1
	stored, err := r.getByBatch(ctx, change.VersionID, change.BatchID)
	if err != nil {
		return domain.VersionChange{}, false, fmt.Errorf("postgres: read version change after upsert: %w", err)
	}
	return stored, created, nil
}

// getByBatch reads one stored snapshot of a batch.
func (r *VersionChangeRepo) getByBatch(ctx context.Context, versionID, batchID int64) (domain.VersionChange, error) {
	change, err := scanVersionChange(r.pool.QueryRow(ctx,
		`SELECT `+versionChangeColumnList+`
		 FROM version_change
		 WHERE version_id = $1 AND batch_id = $2`, versionID, batchID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.VersionChange{}, domain.NewNotFound("version_change_not_found",
				fmt.Sprintf("codeintel: version change of version %d batch %d not found", versionID, batchID))
		}
		return domain.VersionChange{}, err
	}
	return change, nil
}

// ListBatches returns the version's run_batch projection ordered by batch
// number. It is a read-only projection of the execution context's rows: the
// codeintel context never writes or locks execution state.
func (r *VersionChangeRepo) ListBatches(ctx context.Context, versionID int64) ([]domain.VersionBatch, error) {
	rows, err := r.pool.Query(ctx, listVersionBatchesSQL, versionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list version change batches: %w", err)
	}
	defer rows.Close()

	batches := make([]domain.VersionBatch, 0)
	for rows.Next() {
		var batch domain.VersionBatch
		if err := rows.Scan(
			&batch.ID,
			&batch.VersionID,
			&batch.BatchNo,
			&batch.Status,
			&batch.Kind,
			&batch.ParentBatchID,
			&batch.BaseFallbackReason,
			&batch.CodeBaselineCommit,
			&batch.HeadCommit,
			&batch.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan version change batch: %w", err)
		}
		batches = append(batches, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list version change batches: %w", err)
	}
	return batches, nil
}
