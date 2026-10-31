package versionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const requirementColumns = `id, version_id, current_version_id, status, target_version_sut_ids,
	closed_at, closed_by_run_id, created_by, created_by_run_id, source_conversation_id,
	created_at, updated_at`

const requirementVersionColumns = `id, requirement_id, revision, title, description,
	acceptance_criteria, priority, constraints, target_version_suts, target_pages,
	target_apis, content_hash, created_by, created_by_run_id, created_at`

const requirementConfirmationColumns = `id, requirement_version_id, scope, batch_id,
	actor_id, confirmed_at, affected_requirement_ids`

const requirementRelationColumns = `id, version_id, from_requirement_id, to_requirement_id,
	kind, description, source_revision_id, created_by, created_by_run_id, created_at`

// RequirementRepository stores the P1 requirement aggregate: the logical
// requirement, its immutable revisions, confirmations, relations and the
// attachment runtime rows linked to it.
type RequirementRepository struct {
	pool *pgxpool.Pool
}

// NewRequirementRepository wraps a pgx pool. IDs are assigned by the
// application service.
func NewRequirementRepository(pool *pgxpool.Pool) *RequirementRepository {
	return &RequirementRepository{pool: pool}
}

// Create inserts the requirement row and its first revision in one
// transaction.
func (r *RequirementRepository) Create(ctx context.Context, requirement version.Requirement, revision version.RequirementVersion) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create requirement: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, insertRequirementSQL, requirementArgs(requirement)...); err != nil {
		return wrapDB("insert requirement", err)
	}
	if _, err := tx.Exec(ctx, insertRequirementVersionSQL, requirementVersionArgs(revision)...); err != nil {
		return wrapDB("insert requirement version", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create requirement: commit: %w", err)
	}
	return nil
}

// Get loads the full requirement aggregate.
func (r *RequirementRepository) Get(ctx context.Context, versionID, requirementID int64) (version.RequirementAggregate, error) {
	requirement, err := scanRequirement(r.pool.QueryRow(ctx, `
		SELECT `+requirementColumns+` FROM requirement WHERE version_id = $1 AND id = $2
	`, versionID, requirementID))
	if err != nil {
		return version.RequirementAggregate{}, wrapRequirementLookup("get requirement", err)
	}
	aggregate := version.RequirementAggregate{Requirement: requirement}

	versionRows, err := r.pool.Query(ctx, `
		SELECT `+requirementVersionColumns+` FROM requirement_version
		WHERE requirement_id = $1 ORDER BY revision DESC, id DESC
	`, requirementID)
	if err != nil {
		return version.RequirementAggregate{}, fmt.Errorf("get requirement versions: %w", err)
	}
	defer versionRows.Close()
	for versionRows.Next() {
		revision, err := scanRequirementVersion(versionRows)
		if err != nil {
			return version.RequirementAggregate{}, fmt.Errorf("scan requirement version: %w", err)
		}
		aggregate.Versions = append(aggregate.Versions, revision)
	}
	if err := versionRows.Err(); err != nil {
		return version.RequirementAggregate{}, fmt.Errorf("get requirement versions: %w", err)
	}

	confirmationRows, err := r.pool.Query(ctx, `
		SELECT `+prefixedConfirmationColumns("c")+`
		FROM requirement_confirmation c
		JOIN requirement_version v ON v.id = c.requirement_version_id
		WHERE v.requirement_id = $1
		ORDER BY c.id DESC
	`, requirementID)
	if err != nil {
		return version.RequirementAggregate{}, fmt.Errorf("get requirement confirmations: %w", err)
	}
	defer confirmationRows.Close()
	for confirmationRows.Next() {
		confirmation, err := scanConfirmation(confirmationRows)
		if err != nil {
			return version.RequirementAggregate{}, fmt.Errorf("scan requirement confirmation: %w", err)
		}
		aggregate.Confirmations = append(aggregate.Confirmations, confirmation)
	}
	if err := confirmationRows.Err(); err != nil {
		return version.RequirementAggregate{}, fmt.Errorf("get requirement confirmations: %w", err)
	}

	relationRows, err := r.pool.Query(ctx, `
		SELECT `+requirementRelationColumns+`
		FROM requirement_relation
		WHERE version_id = $1 AND (from_requirement_id = $2 OR to_requirement_id = $2)
		ORDER BY id
	`, versionID, requirementID)
	if err != nil {
		return version.RequirementAggregate{}, fmt.Errorf("get requirement relations: %w", err)
	}
	defer relationRows.Close()
	for relationRows.Next() {
		relation, err := scanRelation(relationRows)
		if err != nil {
			return version.RequirementAggregate{}, fmt.Errorf("scan requirement relation: %w", err)
		}
		aggregate.Relations = append(aggregate.Relations, relation)
	}
	if err := relationRows.Err(); err != nil {
		return version.RequirementAggregate{}, fmt.Errorf("get requirement relations: %w", err)
	}

	attachments, err := r.ListAttachments(ctx, versionID, requirementID)
	if err != nil {
		return version.RequirementAggregate{}, err
	}
	aggregate.Attachments = attachments
	return aggregate, nil
}

// GetMany loads requirements with their current revision in the caller's ID
// order. A missing ID is reported as ErrRequirementNotFound.
func (r *RequirementRepository) GetMany(ctx context.Context, versionID int64, ids []int64) ([]version.RequirementWithCurrent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+prefixedRequirementColumns("r")+`, `+prefixedVersionColumns("v")+`
		FROM unnest($2::BIGINT[]) WITH ORDINALITY AS requested(id, ord)
		JOIN requirement r ON r.id = requested.id AND r.version_id = $1
		JOIN requirement_version v ON v.id = r.current_version_id
		ORDER BY requested.ord
	`, versionID, ids)
	if err != nil {
		return nil, fmt.Errorf("get requirements: %w", err)
	}
	defer rows.Close()

	items := make([]version.RequirementWithCurrent, 0, len(ids))
	for rows.Next() {
		requirement, revision, err := scanRequirementWithVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan requirement: %w", err)
		}
		items = append(items, version.RequirementWithCurrent{Requirement: requirement, Current: revision})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get requirements: %w", err)
	}
	if len(items) != len(ids) {
		return nil, version.ErrRequirementNotFound
	}
	return items, nil
}

// List returns the current-revision requirement summaries filtered by status,
// ordered by ID descending.
func (r *RequirementRepository) List(ctx context.Context, versionID int64, status version.RequirementStatus, limit int, beforeID *int64) ([]version.RequirementListItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+prefixedRequirementColumns("r")+`, `+prefixedVersionColumns("v")+`, c.confirmed_at
		FROM requirement r
		JOIN requirement_version v ON v.id = r.current_version_id
		LEFT JOIN requirement_confirmation c ON c.requirement_version_id = r.current_version_id
		WHERE r.version_id = $1
		  AND ($2 = '' OR r.status = $2)
		  AND ($3::BIGINT IS NULL OR r.id < $3)
		ORDER BY r.id DESC
		LIMIT $4
	`, versionID, string(status), beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list requirements: %w", err)
	}
	defer rows.Close()

	items := make([]version.RequirementListItem, 0)
	for rows.Next() {
		var confirmedAt *time.Time
		requirement, revision, err := scanRequirementWithVersion(rows, &confirmedAt)
		if err != nil {
			return nil, fmt.Errorf("scan requirement: %w", err)
		}
		items = append(items, version.RequirementListItem{
			Requirement: requirement, Current: revision, ConfirmedAt: confirmedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list requirements: %w", err)
	}
	return items, nil
}

// AddRevision inserts revision+1 and moves the requirement pointer in one
// transaction.
func (r *RequirementRepository) AddRevision(ctx context.Context, requirement version.Requirement, revision version.RequirementVersion) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("add requirement revision: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE requirement
		SET status = $3, current_version_id = $4, target_version_sut_ids = $5::jsonb, updated_at = $6
		WHERE id = $1 AND version_id = $2
	`, requirement.ID, requirement.VersionID, string(requirement.Status),
		requirement.CurrentVersionID, encodeStringSlice(requirement.TargetVersionSUTIDs), requirement.UpdatedAt)
	if err != nil {
		return wrapDB("update requirement", err)
	}
	if tag.RowsAffected() == 0 {
		return version.ErrRequirementNotFound
	}
	if _, err := tx.Exec(ctx, insertRequirementVersionSQL, requirementVersionArgs(revision)...); err != nil {
		if isUniqueViolation(err) {
			return version.ErrStateConflict
		}
		return wrapDB("insert requirement revision", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("add requirement revision: commit: %w", err)
	}
	return nil
}

// Confirm freezes the current revision and records the confirmation in one
// transaction. The status/current-version guards make concurrent confirmation
// a state conflict.
func (r *RequirementRepository) Confirm(ctx context.Context, requirement version.Requirement, confirmation version.Confirmation) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("confirm requirement: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE requirement SET status = 'clarified', updated_at = $4
		WHERE id = $1 AND version_id = $2 AND status = 'draft' AND current_version_id = $3
	`, requirement.ID, requirement.VersionID, confirmation.RequirementVersionID, requirement.UpdatedAt)
	if err != nil {
		return wrapDB("update requirement status", err)
	}
	if tag.RowsAffected() == 0 {
		return version.ErrStateConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO requirement_confirmation (`+requirementConfirmationColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
	`, confirmation.ID, confirmation.RequirementVersionID, confirmation.Scope, confirmation.BatchID,
		confirmation.ActorID, confirmation.ConfirmedAt, encodeStringSlice(confirmation.AffectedRequirementIDs)); err != nil {
		if isUniqueViolation(err) {
			return version.ErrStateConflict
		}
		return wrapDB("insert requirement confirmation", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("confirm requirement: commit: %w", err)
	}
	return nil
}

// BatchConfirm writes every confirmation and status change in one transaction.
// Any stale requirement (not draft / different current revision) rolls the
// whole batch back.
func (r *RequirementRepository) BatchConfirm(ctx context.Context, requirements []version.Requirement, confirmations []version.Confirmation) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("batch confirm requirements: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	byRevision := make(map[int64]version.Confirmation, len(confirmations))
	for _, confirmation := range confirmations {
		byRevision[confirmation.RequirementVersionID] = confirmation
	}
	for _, requirement := range requirements {
		if requirement.CurrentVersionID == nil {
			return version.ErrStateConflict
		}
		confirmation, ok := byRevision[*requirement.CurrentVersionID]
		if !ok {
			return fmt.Errorf("batch confirm: confirmation missing for requirement %d", requirement.ID)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE requirement SET status = 'clarified', updated_at = $4
			WHERE id = $1 AND version_id = $2 AND status = 'draft' AND current_version_id = $3
		`, requirement.ID, requirement.VersionID, *requirement.CurrentVersionID, requirement.UpdatedAt)
		if err != nil {
			return wrapDB("update requirement status", err)
		}
		if tag.RowsAffected() == 0 {
			return version.ErrStateConflict
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO requirement_confirmation (`+requirementConfirmationColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
		`, confirmation.ID, confirmation.RequirementVersionID, confirmation.Scope, confirmation.BatchID,
			confirmation.ActorID, confirmation.ConfirmedAt,
			encodeStringSlice(confirmation.AffectedRequirementIDs)); err != nil {
			if isUniqueViolation(err) {
				return version.ErrStateConflict
			}
			return wrapDB("insert requirement confirmation", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("batch confirm requirements: commit: %w", err)
	}
	return nil
}

// CreateRelation inserts one relation, mapping the unique constraint to
// ErrRelationDuplicate.
func (r *RequirementRepository) CreateRelation(ctx context.Context, relation version.Relation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO requirement_relation (`+requirementRelationColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, relation.ID, relation.VersionID, relation.FromRequirementID, relation.ToRequirementID,
		relation.Kind, relation.Description, relation.SourceRevisionID,
		relation.CreatedBy, relation.CreatedByRunID, relation.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return version.ErrRelationDuplicate
		}
		return wrapDB("insert requirement relation", err)
	}
	return nil
}

// ListRelations returns every relation touching one requirement.
func (r *RequirementRepository) ListRelations(ctx context.Context, versionID, requirementID int64) ([]version.Relation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+requirementRelationColumns+`
		FROM requirement_relation
		WHERE version_id = $1 AND (from_requirement_id = $2 OR to_requirement_id = $2)
		ORDER BY id
	`, versionID, requirementID)
	if err != nil {
		return nil, fmt.Errorf("list requirement relations: %w", err)
	}
	defer rows.Close()
	relations := make([]version.Relation, 0)
	for rows.Next() {
		relation, err := scanRelation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan requirement relation: %w", err)
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list requirement relations: %w", err)
	}
	return relations, nil
}

// ListAttachments returns the attachment runtime rows linked to one
// requirement.
func (r *RequirementRepository) ListAttachments(ctx context.Context, versionID, requirementID int64) ([]version.Attachment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+attachmentColumns+`
		FROM requirement_attachment
		WHERE version_id = $1 AND requirement_id = $2
		ORDER BY id
	`, versionID, requirementID)
	if err != nil {
		return nil, fmt.Errorf("list requirement attachments: %w", err)
	}
	defer rows.Close()
	attachments := make([]version.Attachment, 0)
	for rows.Next() {
		attachment, err := scanAttachment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan requirement attachment: %w", err)
		}
		attachments = append(attachments, attachment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list requirement attachments: %w", err)
	}
	return attachments, nil
}

// ---------- SQL fragments ----------

const insertRequirementSQL = `
	INSERT INTO requirement (` + requirementColumns + `)
	VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12)
`

const insertRequirementVersionSQL = `
	INSERT INTO requirement_version (` + requirementVersionColumns + `)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11::jsonb, $12, $13, $14, $15)
`

func requirementArgs(requirement version.Requirement) []any {
	return []any{
		requirement.ID, requirement.VersionID, requirement.CurrentVersionID, string(requirement.Status),
		encodeStringSlice(requirement.TargetVersionSUTIDs), requirement.ClosedAt, requirement.ClosedByRunID,
		requirement.CreatedBy, requirement.CreatedByRunID, requirement.SourceConversationID,
		requirement.CreatedAt, requirement.UpdatedAt,
	}
}

func requirementVersionArgs(revision version.RequirementVersion) []any {
	return []any{
		revision.ID, revision.RequirementID, revision.Revision, revision.Title, revision.Description,
		revision.AcceptanceCriteria, revision.Priority, encodeConstraints(revision.Constraints),
		encodeStringSlice(revision.TargetVersionSUTs), encodeStringSlice(revision.TargetPages),
		encodeStringSlice(revision.TargetAPIs), revision.ContentHash,
		revision.CreatedBy, revision.CreatedByRunID, revision.CreatedAt,
	}
}

func prefixedRequirementColumns(alias string) string {
	return `r.id, r.version_id, r.current_version_id, r.status, r.target_version_sut_ids,
		r.closed_at, r.closed_by_run_id, r.created_by, r.created_by_run_id, r.source_conversation_id,
		r.created_at, r.updated_at`
}

func prefixedVersionColumns(alias string) string {
	return `v.id, v.requirement_id, v.revision, v.title, v.description,
		v.acceptance_criteria, v.priority, v.constraints, v.target_version_suts, v.target_pages,
		v.target_apis, v.content_hash, v.created_by, v.created_by_run_id, v.created_at`
}

func prefixedConfirmationColumns(alias string) string {
	return `c.id, c.requirement_version_id, c.scope, c.batch_id,
		c.actor_id, c.confirmed_at, c.affected_requirement_ids`
}

// ---------- scanning ----------

func scanRequirement(row rowScanner) (version.Requirement, error) {
	var requirement version.Requirement
	var status string
	var targetSUTs []byte
	if err := row.Scan(&requirement.ID, &requirement.VersionID, &requirement.CurrentVersionID, &status,
		&targetSUTs, &requirement.ClosedAt, &requirement.ClosedByRunID, &requirement.CreatedBy,
		&requirement.CreatedByRunID, &requirement.SourceConversationID, &requirement.CreatedAt,
		&requirement.UpdatedAt); err != nil {
		return version.Requirement{}, err
	}
	requirement.Status = version.RequirementStatus(status)
	requirement.TargetVersionSUTIDs = decodeStringSlice(targetSUTs)
	return requirement, nil
}

func scanRequirementVersion(row rowScanner) (version.RequirementVersion, error) {
	var revision version.RequirementVersion
	var constraints, targetSUTs, targetPages, targetAPIs []byte
	if err := row.Scan(&revision.ID, &revision.RequirementID, &revision.Revision, &revision.Title,
		&revision.Description, &revision.AcceptanceCriteria, &revision.Priority, &constraints,
		&targetSUTs, &targetPages, &targetAPIs, &revision.ContentHash, &revision.CreatedBy,
		&revision.CreatedByRunID, &revision.CreatedAt); err != nil {
		return version.RequirementVersion{}, err
	}
	revision.Constraints = decodeConstraints(constraints)
	revision.TargetVersionSUTs = decodeStringSlice(targetSUTs)
	revision.TargetPages = decodeStringSlice(targetPages)
	revision.TargetAPIs = decodeStringSlice(targetAPIs)
	return revision, nil
}

func scanConfirmation(row rowScanner) (version.Confirmation, error) {
	var confirmation version.Confirmation
	var affected []byte
	if err := row.Scan(&confirmation.ID, &confirmation.RequirementVersionID, &confirmation.Scope,
		&confirmation.BatchID, &confirmation.ActorID, &confirmation.ConfirmedAt, &affected); err != nil {
		return version.Confirmation{}, err
	}
	confirmation.AffectedRequirementIDs = decodeStringSlice(affected)
	return confirmation, nil
}

func scanRelation(row rowScanner) (version.Relation, error) {
	var relation version.Relation
	if err := row.Scan(&relation.ID, &relation.VersionID, &relation.FromRequirementID,
		&relation.ToRequirementID, &relation.Kind, &relation.Description, &relation.SourceRevisionID,
		&relation.CreatedBy, &relation.CreatedByRunID, &relation.CreatedAt); err != nil {
		return version.Relation{}, err
	}
	return relation, nil
}

// scanRequirementWithVersion scans the two joined aggregate rows plus the
// optional confirmed_at column appended by List.
func scanRequirementWithVersion(row rowScanner, extra ...any) (version.Requirement, version.RequirementVersion, error) {
	var requirement version.Requirement
	var status string
	var requirementTargetSUTs []byte
	var revision version.RequirementVersion
	var constraints, versionTargetSUTs, targetPages, targetAPIs []byte
	dest := []any{
		&requirement.ID, &requirement.VersionID, &requirement.CurrentVersionID, &status,
		&requirementTargetSUTs, &requirement.ClosedAt, &requirement.ClosedByRunID, &requirement.CreatedBy,
		&requirement.CreatedByRunID, &requirement.SourceConversationID, &requirement.CreatedAt,
		&requirement.UpdatedAt,
		&revision.ID, &revision.RequirementID, &revision.Revision, &revision.Title,
		&revision.Description, &revision.AcceptanceCriteria, &revision.Priority, &constraints,
		&versionTargetSUTs, &targetPages,
		&targetAPIs, &revision.ContentHash, &revision.CreatedBy,
		&revision.CreatedByRunID, &revision.CreatedAt,
	}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		return version.Requirement{}, version.RequirementVersion{}, err
	}
	requirement.Status = version.RequirementStatus(status)
	requirement.TargetVersionSUTIDs = decodeStringSlice(requirementTargetSUTs)
	revision.Constraints = decodeConstraints(constraints)
	revision.TargetVersionSUTs = decodeStringSlice(versionTargetSUTs)
	revision.TargetPages = decodeStringSlice(targetPages)
	revision.TargetAPIs = decodeStringSlice(targetAPIs)
	return requirement, revision, nil
}

// ---------- JSON codecs ----------
func encodeStringSlice(values []string) string {
	if values == nil {
		values = []string{}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func decodeStringSlice(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return []string{}
	}
	if values == nil {
		return []string{}
	}
	return values
}

func encodeConstraints(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func decodeConstraints(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func encodeExtractionError(code, message string) *string {
	if code == "" {
		return nil
	}
	raw, err := json.Marshal(struct {
		Code    string `json:"code"`
		Message string `json:"message,omitempty"`
	}{Code: code, Message: message})
	if err != nil {
		value := code
		return &value
	}
	encoded := string(raw)
	return &encoded
}

func decodeExtractionError(raw *string) (string, string) {
	if raw == nil || *raw == "" {
		return "", ""
	}
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(*raw), &payload); err != nil {
		return *raw, ""
	}
	return payload.Code, payload.Message
}

// ---------- error mapping ----------

func wrapRequirementLookup(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return version.ErrRequirementNotFound
	}
	return wrapDB(op, err)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var _ version.RequirementRepository = (*RequirementRepository)(nil)
