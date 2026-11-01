// Package knowledgepg is the PostgreSQL adapter of the P7 knowledge context. It
// implements the knowledge application repository ports over its own frozen
// tables (knowledge_item/knowledge_revision/knowledge_usage) and never reads
// another context's tables.
package knowledgepg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const itemColumns = `id, scope, system_key, title, content, tags, status, confidence::float8,
	source_version_id, promoted_from_version_id, revision, review_reason, reviewed_by,
	reviewed_at, revoked_at, created_by, created_at, updated_at`

const revisionColumns = `id, knowledge_id, revision, title, content, tags, changed_by, created_at`

// Repository persists and reads knowledge_item/knowledge_revision/
// knowledge_usage rows.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository wires the knowledge repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Insert writes the item and its initial revision in one transaction.
func (r *Repository) Insert(ctx context.Context, item knowledge.Item, revision knowledge.Revision) error {
	if err := r.ready(); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO knowledge_item (id, scope, system_key, title, content, tags, status,
			confidence, source_version_id, promoted_from_version_id, revision, review_reason,
			reviewed_by, reviewed_at, revoked_at, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $17)
	`, item.ID, string(item.Scope), item.SystemKey, item.Title, item.Content, tagsForWrite(item.Tags),
		string(item.Status), item.Confidence, item.SourceVersionID, item.PromotedFromVersionID,
		item.Revision, item.ReviewReason, item.ReviewedBy, item.ReviewedAt, item.RevokedAt,
		item.CreatedBy, item.CreatedAt); err != nil {
		return fmt.Errorf("insert knowledge item: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO knowledge_revision (id, knowledge_id, revision, title, content, tags,
			changed_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, revision.ID, revision.KnowledgeID, revision.Revision, revision.Title, revision.Content,
		tagsForWrite(revision.Tags), revision.ChangedBy, revision.CreatedAt); err != nil {
		return fmt.Errorf("insert knowledge revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit knowledge insert: %w", err)
	}
	return nil
}

// Get returns one item or knowledge.ErrNotFound.
func (r *Repository) Get(ctx context.Context, id int64) (knowledge.Item, error) {
	if err := r.ready(); err != nil {
		return knowledge.Item{}, err
	}
	item, err := scanItem(r.pool.QueryRow(ctx,
		`SELECT `+itemColumns+` FROM knowledge_item WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledge.Item{}, knowledge.ErrNotFound
	}
	if err != nil {
		return knowledge.Item{}, fmt.Errorf("get knowledge item: %w", err)
	}
	return item, nil
}

// List returns up to filter.Limit rows ordered id DESC, or by ts_rank DESC then
// id DESC when filter.Query is set.
func (r *Repository) List(ctx context.Context, filter knowledge.Filter) ([]knowledge.Item, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	args := make([]any, 0, 7)
	where := make([]string, 0, 6)
	if filter.Scope != "" {
		args = append(args, string(filter.Scope))
		where = append(where, fmt.Sprintf("scope = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, string(filter.Status))
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if filter.SystemKey != "" {
		args = append(args, filter.SystemKey)
		where = append(where, fmt.Sprintf("system_key = $%d", len(args)))
	}
	if filter.Tag != "" {
		args = append(args, filter.Tag)
		where = append(where, fmt.Sprintf("tags @> ARRAY[$%d]::text[]", len(args)))
	}
	order := "id DESC"
	if filter.Query != "" {
		args = append(args, filter.Query)
		index := len(args)
		where = append(where, fmt.Sprintf("search_vector @@ websearch_to_tsquery('simple', $%d)", index))
		order = fmt.Sprintf("ts_rank(search_vector, websearch_to_tsquery('simple', $%d)) DESC, id DESC", index)
	}
	if filter.BeforeID > 0 {
		args = append(args, filter.BeforeID)
		where = append(where, fmt.Sprintf("id < $%d", len(args)))
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)

	builder := strings.Builder{}
	builder.WriteString(`SELECT ` + itemColumns + ` FROM knowledge_item`)
	if len(where) > 0 {
		builder.WriteString(" WHERE ")
		builder.WriteString(strings.Join(where, " AND "))
	}
	builder.WriteString(" ORDER BY ")
	builder.WriteString(order)
	builder.WriteString(fmt.Sprintf(" LIMIT $%d", len(args)))

	rows, err := r.pool.Query(ctx, builder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list knowledge items: %w", err)
	}
	defer rows.Close()
	items := make([]knowledge.Item, 0, limit)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan knowledge item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge items: %w", err)
	}
	return items, nil
}

// Update writes the edited content, the next revision and the append-only
// revision row. The item revision must still be item.Revision-1.
func (r *Repository) Update(ctx context.Context, item knowledge.Item, revision knowledge.Revision) error {
	if err := r.ready(); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE knowledge_item SET title = $2, content = $3, tags = $4, scope = $5,
			system_key = $6, status = $7, revision = $8, review_reason = $9,
			reviewed_by = $10, reviewed_at = $11, revoked_at = $12, updated_at = $13
		WHERE id = $1 AND revision = $14
	`, item.ID, item.Title, item.Content, tagsForWrite(item.Tags), string(item.Scope),
		item.SystemKey, string(item.Status), item.Revision, item.ReviewReason, item.ReviewedBy,
		item.ReviewedAt, item.RevokedAt, item.UpdatedAt, item.Revision-1)
	if err != nil {
		return fmt.Errorf("update knowledge item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return knowledge.ErrStateConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO knowledge_revision (id, knowledge_id, revision, title, content, tags,
			changed_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, revision.ID, revision.KnowledgeID, revision.Revision, revision.Title, revision.Content,
		tagsForWrite(revision.Tags), revision.ChangedBy, revision.CreatedAt); err != nil {
		return fmt.Errorf("insert knowledge revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit knowledge update: %w", err)
	}
	return nil
}

// UpdateStatus changes the review columns when the stored status still equals
// from.
func (r *Repository) UpdateStatus(ctx context.Context, item knowledge.Item, from knowledge.Status) error {
	if err := r.ready(); err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE knowledge_item SET status = $2, review_reason = $3, reviewed_by = $4,
			reviewed_at = $5, revoked_at = $6, updated_at = $7
		WHERE id = $1 AND status = $8
	`, item.ID, string(item.Status), item.ReviewReason, item.ReviewedBy, item.ReviewedAt,
		item.RevokedAt, item.UpdatedAt, string(from))
	if err != nil {
		return fmt.Errorf("update knowledge status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return knowledge.ErrStateConflict
	}
	return nil
}

// ListRevisions returns the append-only history ordered revision DESC.
func (r *Repository) ListRevisions(ctx context.Context, knowledgeID int64) ([]knowledge.Revision, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+revisionColumns+` FROM knowledge_revision WHERE knowledge_id = $1 ORDER BY revision DESC`,
		knowledgeID)
	if err != nil {
		return nil, fmt.Errorf("list knowledge revisions: %w", err)
	}
	defer rows.Close()
	revisions := make([]knowledge.Revision, 0)
	for rows.Next() {
		var revision knowledge.Revision
		if err := rows.Scan(&revision.ID, &revision.KnowledgeID, &revision.Revision, &revision.Title,
			&revision.Content, &revision.Tags, &revision.ChangedBy, &revision.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan knowledge revision: %w", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge revisions: %w", err)
	}
	return revisions, nil
}

// ListForInjection returns approved candidates visible to the requested system
// keys, ordered by ts_rank desc then id desc. Source staleness is filtered by
// the application service.
func (r *Repository) ListForInjection(ctx context.Context, query knowledge.InjectionQuery) ([]knowledge.Item, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	keys := query.SystemKeys
	if keys == nil {
		keys = []string{}
	}
	sql := `SELECT ` + itemColumns + ` FROM knowledge_item
		WHERE status = 'approved'
		  AND (scope = 'global' OR (scope = 'system' AND system_key = ANY($1::text[])))
		ORDER BY ts_rank(search_vector, websearch_to_tsquery('simple', $2)) DESC, id DESC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, sql, keys, query.Query, limit)
	if err != nil {
		return nil, fmt.Errorf("list knowledge for injection: %w", err)
	}
	defer rows.Close()
	items := make([]knowledge.Item, 0, limit)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan knowledge injection item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge injection items: %w", err)
	}
	return items, nil
}

// UsageRepository appends knowledge_usage rows. It is a separate type because
// the item repository already exposes Insert for item+revision transactions;
// both are wired into the application service as distinct ports.
type UsageRepository struct {
	pool *pgxpool.Pool
}

// NewUsageRepository wires the knowledge usage repository.
func NewUsageRepository(pool *pgxpool.Pool) *UsageRepository {
	return &UsageRepository{pool: pool}
}

// Insert appends knowledge_usage rows for one injection in one transaction.
func (r *UsageRepository) Insert(ctx context.Context, usages []knowledge.Usage) error {
	if len(usages) == 0 {
		return nil
	}
	if r == nil || r.pool == nil {
		return errors.New("knowledge usage repository is not configured")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge usage insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, usage := range usages {
		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_usage (id, knowledge_id, agent_run_id, work_item_id,
				token_estimate, injected_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, usage.ID, usage.KnowledgeID, usage.AgentRunID, usage.WorkItemID,
			usage.TokenEstimate, usage.InjectedAt, usage.CreatedAt, usage.UpdatedAt); err != nil {
			return fmt.Errorf("insert knowledge usage: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit knowledge usage insert: %w", err)
	}
	return nil
}

func (r *Repository) ready() error {
	if r == nil || r.pool == nil {
		return errors.New("knowledge repository is not configured")
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(row rowScanner) (knowledge.Item, error) {
	var item knowledge.Item
	var scope, status string
	if err := row.Scan(&item.ID, &scope, &item.SystemKey, &item.Title, &item.Content, &item.Tags,
		&status, &item.Confidence, &item.SourceVersionID, &item.PromotedFromVersionID,
		&item.Revision, &item.ReviewReason, &item.ReviewedBy, &item.ReviewedAt, &item.RevokedAt,
		&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return knowledge.Item{}, err
	}
	item.Scope = knowledge.Scope(scope)
	item.Status = knowledge.Status(status)
	if item.Tags == nil {
		item.Tags = []string{}
	}
	return item, nil
}

// tagsForWrite keeps the NOT NULL TEXT[] column an empty array rather than
// NULL when a caller passes nil.
func tagsForWrite(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}
