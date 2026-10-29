package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// knowledgeColumnList is the shared column order of every knowledge entry query
// and the order scanKnowledgeEntry expects. The version name is read through the
// owning version row (the version context owns that table).
const knowledgeColumnList = `k.id, k.version_id, v.name, k.kind, k.title, k.body,
	k.tags, k.source_refs, k.status, k.confidence, k.created_by, k.reviewed_by,
	k.review_note, k.revision, k.created_at, k.updated_at`

// knowledgeSelectFrom is the shared FROM clause of the read queries.
const knowledgeSelectFrom = ` FROM knowledge_entry k LEFT JOIN version v ON v.id = k.version_id`

// knowledgeReturningList is the column list of the INSERT/UPDATE RETURNING
// clauses. A RETURNING clause has no table alias to prefix (the INSERT has no
// FROM), so every column is unqualified and the owning version name is read
// through a scalar subquery on the row's own version_id.
const knowledgeReturningList = `id, version_id, (SELECT name FROM version WHERE id = version_id),
	kind, title, body, tags, source_refs, status, confidence, created_by, reviewed_by,
	review_note, revision, created_at, updated_at`

// KnowledgeRepository implements domain.KnowledgeRepository on a pgx pool. The
// content edit and the review are revision compare-and-swaps that only apply to
// a draft; a zero-row write is classified as not-found, state conflict or
// revision conflict by re-reading the row.
type KnowledgeRepository struct {
	pool *pgxpool.Pool
}

// NewKnowledgeRepository returns a knowledge repository backed by pool.
func NewKnowledgeRepository(pool *pgxpool.Pool) *KnowledgeRepository {
	return &KnowledgeRepository{pool: pool}
}

var _ domain.KnowledgeRepository = (*KnowledgeRepository)(nil)

// knowledgeWriteError translates knowledge insert/update failures into stable
// domain errors: a duplicate explicit id is a conflict, a missing version is a
// not-found and the CHECK backstops are invalid requests.
func knowledgeWriteError(err error, operation string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case clarificationUniqueViolationCode:
			return domain.NewConflict("duplicate_key", "a knowledge entry with this id already exists")
		case clarificationForeignKeyViolationCode:
			return domain.NewNotFound("version", 0)
		case clarificationCheckViolationCode:
			return domain.NewInvalid("", "invalid_knowledge",
				"the knowledge entry violates the entry constraints")
		}
	}
	return fmt.Errorf("postgres: %s knowledge entry: %w", operation, err)
}

// scanKnowledgeEntry scans one row in knowledgeColumnList order.
func scanKnowledgeEntry(row requirementRowScanner) (domain.KnowledgeEntry, error) {
	var entry domain.KnowledgeEntry
	var tags []byte
	var sourceRefs []byte
	if err := row.Scan(
		&entry.ID,
		&entry.VersionID,
		&entry.VersionName,
		&entry.Kind,
		&entry.Title,
		&entry.Body,
		&tags,
		&sourceRefs,
		&entry.Status,
		&entry.Confidence,
		&entry.CreatedBy,
		&entry.ReviewedBy,
		&entry.ReviewNote,
		&entry.Revision,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	); err != nil {
		return domain.KnowledgeEntry{}, err
	}
	entry.Tags = []string{}
	if len(tags) > 0 {
		if err := json.Unmarshal(tags, &entry.Tags); err != nil {
			return domain.KnowledgeEntry{}, fmt.Errorf("decode knowledge tags: %w", err)
		}
	}
	refs, err := domain.UnmarshalKnowledgeSourceRefs(sourceRefs)
	if err != nil {
		return domain.KnowledgeEntry{}, err
	}
	entry.SourceRefs = refs
	return entry, nil
}

// knowledgeValues encodes the JSONB columns of one write.
func knowledgeValues(entry *domain.KnowledgeEntry) ([]byte, []byte, error) {
	tags, err := json.Marshal(knowledgeTagsOrDefault(entry.Tags))
	if err != nil {
		return nil, nil, fmt.Errorf("encode knowledge tags: %w", err)
	}
	refs, err := domain.MarshalKnowledgeSourceRefs(entry.SourceRefs)
	if err != nil {
		return nil, nil, fmt.Errorf("encode knowledge source refs: %w", err)
	}
	return tags, refs, nil
}

// knowledgeTagsOrDefault never persists a NULL tags value.
func knowledgeTagsOrDefault(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

// Create inserts entry with its explicit Snowflake id and writes the stored row
// back to it.
func (r *KnowledgeRepository) Create(ctx context.Context, entry *domain.KnowledgeEntry) error {
	tags, refs, err := knowledgeValues(entry)
	if err != nil {
		return err
	}
	const query = `INSERT INTO knowledge_entry
		(id, version_id, kind, title, body, tags, source_refs, status, confidence,
		 created_by, reviewed_by, review_note, revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING ` + knowledgeReturningList
	created, err := scanKnowledgeEntry(r.pool.QueryRow(ctx, query,
		entry.ID, entry.VersionID, entry.Kind, entry.Title, entry.Body, tags, refs,
		entry.Status, entry.Confidence, entry.CreatedBy, entry.ReviewedBy,
		entry.ReviewNote, entry.Revision,
	))
	if err != nil {
		return knowledgeWriteError(err, "create")
	}
	*entry = created
	return nil
}

// Get returns one entry by id.
func (r *KnowledgeRepository) Get(ctx context.Context, id int64) (*domain.KnowledgeEntry, error) {
	entry, err := scanKnowledgeEntry(r.pool.QueryRow(ctx,
		`SELECT `+knowledgeColumnList+knowledgeSelectFrom+` WHERE k.id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFound("knowledge", id)
		}
		return nil, fmt.Errorf("postgres: get knowledge entry: %w", err)
	}
	return &entry, nil
}

// List returns one page of a version's entries ordered by id ascending with an
// optional status/kind filter and keyset cursor.
func (r *KnowledgeRepository) List(ctx context.Context, filter domain.KnowledgeFilter) ([]domain.KnowledgeEntry, bool, error) {
	if filter.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list knowledge entries: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(filter.Page.Cursor)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list knowledge entries: %w", err)
	}
	const query = `SELECT ` + knowledgeColumnList + knowledgeSelectFrom + `
		WHERE k.version_id = $1
		  AND ($2 = '' OR k.status = $2)
		  AND ($3 = '' OR k.kind = $3)
		  AND ($4::boolean = false OR k.id > $5::bigint)
		  AND ($7::bigint IS NULL OR k.status = 'approved' OR k.created_by = $7)
		ORDER BY k.id ASC
		LIMIT $6`
	rows, err := r.pool.Query(ctx, query,
		filter.VersionID, filter.Status, filter.Kind, cursorID != 0, cursorID, filter.Page.Limit+1,
		filter.ViewerID)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list knowledge entries: %w", err)
	}
	defer rows.Close()
	entries, err := scanKnowledgeEntries(rows, filter.Page.Limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(entries) > filter.Page.Limit
	if more {
		entries = entries[:filter.Page.Limit]
	}
	return entries, more, nil
}

// ListGlobal returns one page of the cross-version read. An empty status means
// approved only.
func (r *KnowledgeRepository) ListGlobal(ctx context.Context, filter domain.GlobalKnowledgeFilter) ([]domain.KnowledgeEntry, bool, error) {
	if filter.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list global knowledge entries: limit must be positive")
	}
	status := filter.Status
	if status == "" {
		status = domain.KnowledgeApproved
	}
	cursorID, err := platform.DecodeCursor(filter.Page.Cursor)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list global knowledge entries: %w", err)
	}
	const query = `SELECT ` + knowledgeColumnList + knowledgeSelectFrom + `
		WHERE k.status = $1
		  AND ($2::bigint IS NULL OR k.version_id = $2)
		  AND ($3 = '' OR k.kind = $3)
		  AND ($4::boolean = false OR k.id > $5::bigint)
		ORDER BY k.id ASC
		LIMIT $6`
	rows, err := r.pool.Query(ctx, query,
		status, filter.VersionID, filter.Kind, cursorID != 0, cursorID, filter.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list global knowledge entries: %w", err)
	}
	defer rows.Close()
	entries, err := scanKnowledgeEntries(rows, filter.Page.Limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(entries) > filter.Page.Limit
	if more {
		entries = entries[:filter.Page.Limit]
	}
	return entries, more, nil
}

// ListApproved returns up to limit approved entries of one version ordered by id
// ascending. It is the prompt-injection read: draft/rejected rows never appear.
func (r *KnowledgeRepository) ListApproved(ctx context.Context, versionID int64, limit int) ([]domain.KnowledgeEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+knowledgeColumnList+knowledgeSelectFrom+`
		 WHERE k.version_id = $1 AND k.status = 'approved'
		 ORDER BY k.id ASC LIMIT $2`, versionID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list approved knowledge entries: %w", err)
	}
	defer rows.Close()
	return scanKnowledgeEntries(rows, limit)
}

// scanKnowledgeEntries drains rows into the entry list.
func scanKnowledgeEntries(rows pgx.Rows, capacity int) ([]domain.KnowledgeEntry, error) {
	entries := make([]domain.KnowledgeEntry, 0, capacity)
	for rows.Next() {
		entry, err := scanKnowledgeEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan knowledge entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list knowledge entries: %w", err)
	}
	return entries, nil
}

// UpdateContent rewrites the editable fields under the revision compare-and-swap
// and writes the stored row back to entry.
func (r *KnowledgeRepository) UpdateContent(ctx context.Context, entry *domain.KnowledgeEntry, expectedRevision int) error {
	tags, refs, err := knowledgeValues(entry)
	if err != nil {
		return err
	}
	updated, err := scanKnowledgeEntry(r.pool.QueryRow(ctx,
		`UPDATE knowledge_entry
		 SET kind = $3, title = $4, body = $5, tags = $6, source_refs = $7,
		     confidence = $8, revision = revision + 1, updated_at = now()
		 WHERE id = $1 AND revision = $2 AND status = 'draft'
		 RETURNING `+knowledgeReturningList,
		entry.ID, expectedRevision, entry.Kind, entry.Title, entry.Body, tags, refs,
		entry.Confidence,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r.knowledgeCasError(ctx, entry.ID, expectedRevision)
		}
		return knowledgeWriteError(err, "update")
	}
	*entry = updated
	return nil
}

// Review stores the terminal review decision under the revision compare-and-swap.
func (r *KnowledgeRepository) Review(ctx context.Context, entry *domain.KnowledgeEntry, expectedRevision int) error {
	updated, err := scanKnowledgeEntry(r.pool.QueryRow(ctx,
		`UPDATE knowledge_entry
		 SET status = $3, reviewed_by = $4, review_note = $5,
		     revision = revision + 1, updated_at = now()
		 WHERE id = $1 AND revision = $2 AND status = 'draft'
		 RETURNING `+knowledgeReturningList,
		entry.ID, expectedRevision, entry.Status, entry.ReviewedBy, entry.ReviewNote,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r.knowledgeCasError(ctx, entry.ID, expectedRevision)
		}
		return knowledgeWriteError(err, "review")
	}
	*entry = updated
	return nil
}

// knowledgeCasError classifies a zero-row knowledge CAS: a missing row is a
// not-found, an already reviewed row a knowledge_state_conflict and a revision
// mismatch the knowledge_revision_conflict of spec §9.4.
func (r *KnowledgeRepository) knowledgeCasError(ctx context.Context, id int64, expectedRevision int) error {
	var status string
	var revision int
	if err := r.pool.QueryRow(ctx,
		`SELECT status, revision FROM knowledge_entry WHERE id = $1`, id).
		Scan(&status, &revision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewNotFound("knowledge", id)
		}
		return fmt.Errorf("postgres: read knowledge entry after CAS: %w", err)
	}
	if status != domain.KnowledgeDraft {
		return domain.NewStateConflict("knowledge_state_conflict",
			fmt.Sprintf("knowledge entry %d is %s, not draft", id, status))
	}
	return domain.NewStateConflict("knowledge_revision_conflict",
		fmt.Sprintf("knowledge entry %d is at revision %d, not %d", id, revision, expectedRevision))
}
