// Package knowledgepg is the PostgreSQL adapter of the P7 knowledge context. It
// implements the knowledge application repository ports over its own frozen
// tables (knowledge_item/knowledge_revision/knowledge_usage) and never reads
// another context's tables.
package knowledgepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const itemColumns = `id, scope, system_key, title, content, tags, status, confidence::float8,
	source_version_id, promoted_from_version_id, revision, review_reason, reviewed_by,
	reviewed_at, revoked_at, created_by, created_at, updated_at, bindings, last_verified_at`

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
	bindings, err := encodeBindings(item.Bindings)
	if err != nil {
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
			reviewed_by, reviewed_at, revoked_at, created_by, created_at, updated_at,
			bindings, last_verified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
			$17, $17, $18::jsonb, $19)
	`, item.ID, string(item.Scope), item.SystemKey, item.Title, item.Content, tagsForWrite(item.Tags),
		string(item.Status), item.Confidence, item.SourceVersionID, item.PromotedFromVersionID,
		item.Revision, item.ReviewReason, item.ReviewedBy, item.ReviewedAt, item.RevokedAt,
		item.CreatedBy, item.CreatedAt, bindings, item.LastVerifiedAt); err != nil {
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
	if !filter.Bindings.IsZero() {
		bindings, err := encodeBindings(filter.Bindings)
		if err != nil {
			return nil, err
		}
		args = append(args, bindings)
		where = append(where, fmt.Sprintf("bindings @> $%d::jsonb", len(args)))
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
	bindings, err := encodeBindings(item.Bindings)
	if err != nil {
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
			reviewed_by = $10, reviewed_at = $11, revoked_at = $12, updated_at = $13,
			bindings = $15::jsonb, last_verified_at = $16
		WHERE id = $1 AND revision = $14
	`, item.ID, item.Title, item.Content, tagsForWrite(item.Tags), string(item.Scope),
		item.SystemKey, string(item.Status), item.Revision, item.ReviewReason, item.ReviewedBy,
		item.ReviewedAt, item.RevokedAt, item.UpdatedAt, item.Revision-1, bindings,
		item.LastVerifiedAt)
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
			reviewed_at = $5, revoked_at = $6, updated_at = $7, last_verified_at = $8
		WHERE id = $1 AND status = $9
	`, item.ID, string(item.Status), item.ReviewReason, item.ReviewedBy, item.ReviewedAt,
		item.RevokedAt, item.UpdatedAt, item.LastVerifiedAt, string(from))
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
// keys, ordered by ts_rank desc then id desc. Only status=approved rows are
// returned, so revoked/suspect/stale/draft items never inject. Source
// staleness is filtered by the application service.
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
	args := []any{keys, query.Query}
	where := []string{
		"status = 'approved'",
		"(scope = 'global' OR (scope = 'system' AND system_key = ANY($1::text[])))",
	}
	if !query.Bindings.IsZero() {
		bindings, err := encodeBindings(query.Bindings)
		if err != nil {
			return nil, err
		}
		args = append(args, bindings)
		where = append(where, fmt.Sprintf("bindings @> $%d::jsonb", len(args)))
	}
	args = append(args, limit)
	sql := fmt.Sprintf(`SELECT `+itemColumns+` FROM knowledge_item
		WHERE %s
		ORDER BY ts_rank(search_vector, websearch_to_tsquery('simple', $2)) DESC, id DESC
		LIMIT $%d`, strings.Join(where, " AND "), len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
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

// pg_trgm retrieval floors. pg_trgm's defaults are latin-word tuned (0.3
// whole-string similarity / 0.6 word similarity); Chinese text has no word
// separators, so a short query against a longer field scores far below them.
// The 2-rune near miss 登陆 vs 登录配方：管理员 is 0.0909 whole-string / 0.333
// word similarity, and a 3-rune query matching inside a longer CJK word scores
// 0.25 word similarity (登录页 inside 先打开登录页…). The floors are calibrated
// just below those values so the GIN index admits them. SQL is only the
// candidate stage: the application re-scores every candidate with the same
// character n-grams and drops unrelated rows, and the caller bounds the
// candidate count. Both settings are applied inside the query transaction with
// set_config(..., is_local = true) so they never leak to the pooled connection.
const (
	pgTrgmSimilarityThreshold     = "0.08"
	pgTrgmWordSimilarityThreshold = "0.2"
)

// likeContainsPatternEscaper makes a free-text query literal for the ILIKE
// "contains" branches: the default backslash escape character is used, so a
// query containing % or _ is not interpreted as a wildcard.
var likeContainsPatternEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func likeContainsPattern(text string) string {
	return "%" + likeContainsPatternEscaper.Replace(text) + "%"
}

// topKCandidatePredicate is the SQL candidate superset of the Go scorer's
// positive-match rule (a shared character trigram/bigram, an exact substring or
// a tag match). The pg_trgm operators are GIN-indexable and read the
// transaction-local floors above. The ILIKE branches cover a short query that
// is an exact substring of a longer CJK word: pg_trgm pads the query with word
// boundaries, so its trigrams cannot match the field's internal trigrams (登录
// inside 先打开登录页… has whole-string and word similarity 0, while the Go
// bigram scorer accepts it). $2 is the query text, $3 the escaped contains
// pattern.
const topKCandidatePredicate = `(
			title OPERATOR(public.%) $2
			OR $2 OPERATOR(public.<%) title
			OR content OPERATOR(public.%) $2
			OR $2 OPERATOR(public.<%) content
			OR eztest_text_array_to_string(tags) OPERATOR(public.%) $2
			OR $2 OPERATOR(public.<%) eztest_text_array_to_string(tags)
			OR title ILIKE $3
			OR content ILIKE $3
			OR eztest_text_array_to_string(tags) ILIKE $3
		)`

// SearchTopK returns up to query.Limit approved candidates visible to the
// requested system keys, ordered by pg_trgm similarity of the query against
// the item title/content desc then id desc. It is the trigram retrieval stage
// that replaces the bounded ts_rank candidate window: the GIN trigram index
// finds rows sharing trigrams with the query, so an old item whose Chinese text
// only matches fuzzily is still recalled. The candidate predicate covers
// title, content and tags because the application's character n-gram scorer
// scores all three; a tag-only match is therefore not dropped when another row
// has a title/content hit. Bindings apply the same JSONB containment filter as
// ListForInjection. An empty query delegates to the id-desc injection window
// (structured browse has no similarity to rank by).
//
// The predicate and thresholds use the public-installed pg_trgm objects and the
// tag expression from the retrieval index (see knowledge_0003_trgm.sql); the
// query is fully parameterized and the candidate count is bounded by the
// caller.
func (r *Repository) SearchTopK(ctx context.Context, query knowledge.InjectionQuery) ([]knowledge.Item, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(query.Query)
	if text == "" {
		return r.ListForInjection(ctx, query)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	keys := query.SystemKeys
	if keys == nil {
		keys = []string{}
	}
	// The % operator is the trigram-indexable operator; writing it as
	// OPERATOR(public.%) keeps it resolvable when the session search_path does
	// not include public (the integration harness narrows it to a test schema).
	args := []any{keys, text, likeContainsPattern(text)}
	where := []string{
		"status = 'approved'",
		"(scope = 'global' OR (scope = 'system' AND system_key = ANY($1::text[])))",
		topKCandidatePredicate,
	}
	if !query.Bindings.IsZero() {
		bindings, err := encodeBindings(query.Bindings)
		if err != nil {
			return nil, err
		}
		args = append(args, bindings)
		where = append(where, fmt.Sprintf("bindings @> $%d::jsonb", len(args)))
	}
	args = append(args, limit)
	sql := fmt.Sprintf(`SELECT `+itemColumns+` FROM knowledge_item
		WHERE %s
		ORDER BY greatest(public.similarity(title, $2), public.similarity(content, $2)) DESC, id DESC
		LIMIT $%d`, strings.Join(where, " AND "), len(args))

	// set_config(..., is_local=true) makes both trigram floors transaction
	// scoped; the GIN-indexable operators consult them while the query runs.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin knowledge top-k: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT set_config('pg_trgm.similarity_threshold', $1, true),
		       set_config('pg_trgm.word_similarity_threshold', $2, true)
	`, pgTrgmSimilarityThreshold, pgTrgmWordSimilarityThreshold); err != nil {
		return nil, fmt.Errorf("set knowledge trigram thresholds: %w", err)
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search knowledge top-k: %w", err)
	}
	items := make([]knowledge.Item, 0, limit)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan knowledge top-k item: %w", err)
		}
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge top-k items: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit knowledge top-k: %w", err)
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
	var bindingsRaw []byte
	if err := row.Scan(&item.ID, &scope, &item.SystemKey, &item.Title, &item.Content, &item.Tags,
		&status, &item.Confidence, &item.SourceVersionID, &item.PromotedFromVersionID,
		&item.Revision, &item.ReviewReason, &item.ReviewedBy, &item.ReviewedAt, &item.RevokedAt,
		&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt, &bindingsRaw, &item.LastVerifiedAt); err != nil {
		return knowledge.Item{}, err
	}
	item.Scope = knowledge.Scope(scope)
	item.Status = knowledge.Status(status)
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if len(bindingsRaw) > 0 {
		if err := json.Unmarshal(bindingsRaw, &item.Bindings); err != nil {
			return knowledge.Item{}, fmt.Errorf("decode knowledge bindings: %w", err)
		}
	}
	return item, nil
}

// encodeBindings renders the bindings JSONB object; a zero value stays "{}" so
// the NOT NULL column and the jsonb_typeof CHECK both hold.
func encodeBindings(bindings knowledge.Bindings) (string, error) {
	encoded, err := json.Marshal(bindings)
	if err != nil {
		return "", fmt.Errorf("encode knowledge bindings: %w", err)
	}
	if string(encoded) == "null" {
		return "{}", nil
	}
	return string(encoded), nil
}

// tagsForWrite keeps the NOT NULL TEXT[] column an empty array rather than
// NULL when a caller passes nil.
func tagsForWrite(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}
