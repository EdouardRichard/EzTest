package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// requirementColumnList is the shared column order of every requirement query
// and the order scanRequirement expects.
const requirementColumnList = `id, version_id, title, description, acceptance_criteria, priority, status,
	source, last_update_source, revision, confirmed_at, confirmed_by, created_by, updated_by,
	created_at, updated_at`

// relationColumnList is the shared column order of every relation query and the
// order scanRelation expects.
const relationColumnList = `id, version_id, from_requirement_id, to_requirement_id, relation_type,
	description, source, created_by, created_at, updated_at`

// attachmentColumnList is the shared column order of every attachment query and
// the order scanAttachment expects.
const attachmentColumnList = `id, version_id, requirement_id, kind, filename, mime_type,
	size_bytes, sha256, storage_path, created_by, created_at`

// PostgreSQL SQLSTATE codes classified by this repository.
const (
	requirementUniqueViolationCode     = "23505"
	requirementForeignKeyViolationCode = "23503"
	requirementCheckViolationCode      = "23514"

	// requirementRelationsPrimaryKey is the name of the relations primary-key
	// constraint; every other unique violation is a semantic duplicate.
	requirementRelationsPrimaryKey = "requirement_relations_pkey"
	// requirementAttachmentsPrimaryKey is the name of the attachments primary-key
	// constraint; every other unique violation is a duplicate content upload.
	requirementAttachmentsPrimaryKey = "requirement_attachments_pkey"

	// requirementRelationsListLimit bounds one version relation listing
	// (spec §7.4: not paginated, upper bound 500).
	requirementRelationsListLimit = 500
)

// requirementRowScanner is the subset of pgx.Row and pgx.Rows used to scan one
// requirement, relation or attachment row.
type requirementRowScanner interface {
	Scan(dest ...any) error
}

// RequirementRepository implements domain.RequirementRepository on a pgx pool.
// Requirement mutations are compare-and-swap operations and cascaded deletes
// rely on the ON DELETE CASCADE constraints of the version schema, which run in
// the statement's implicit transaction.
type RequirementRepository struct {
	pool *pgxpool.Pool
}

// NewRequirementRepository returns a requirement repository backed by pool.
func NewRequirementRepository(pool *pgxpool.Pool) *RequirementRepository {
	return &RequirementRepository{pool: pool}
}

var _ domain.RequirementRepository = (*RequirementRepository)(nil)

// scanRequirement scans one row in requirementColumnList order.
func scanRequirement(row requirementRowScanner) (domain.Requirement, error) {
	var requirement domain.Requirement
	if err := row.Scan(
		&requirement.ID,
		&requirement.VersionID,
		&requirement.Title,
		&requirement.Description,
		&requirement.AcceptanceCriteria,
		&requirement.Priority,
		&requirement.Status,
		&requirement.Source,
		&requirement.LastUpdateSource,
		&requirement.Revision,
		&requirement.ConfirmedAt,
		&requirement.ConfirmedBy,
		&requirement.CreatedBy,
		&requirement.UpdatedBy,
		&requirement.CreatedAt,
		&requirement.UpdatedAt,
	); err != nil {
		return domain.Requirement{}, err
	}
	return requirement, nil
}

// scanRelation scans one row in relationColumnList order.
func scanRelation(row requirementRowScanner) (domain.Relation, error) {
	var relation domain.Relation
	if err := row.Scan(
		&relation.ID,
		&relation.VersionID,
		&relation.FromRequirementID,
		&relation.ToRequirementID,
		&relation.RelationType,
		&relation.Description,
		&relation.Source,
		&relation.CreatedBy,
		&relation.CreatedAt,
		&relation.UpdatedAt,
	); err != nil {
		return domain.Relation{}, err
	}
	return relation, nil
}

// scanAttachment scans one row in attachmentColumnList order.
func scanAttachment(row requirementRowScanner) (domain.Attachment, error) {
	var attachment domain.Attachment
	if err := row.Scan(
		&attachment.ID,
		&attachment.VersionID,
		&attachment.RequirementID,
		&attachment.Kind,
		&attachment.Filename,
		&attachment.MIMEType,
		&attachment.SizeBytes,
		&attachment.SHA256,
		&attachment.StoragePath,
		&attachment.CreatedBy,
		&attachment.CreatedAt,
	); err != nil {
		return domain.Attachment{}, err
	}
	return attachment, nil
}

// requirementRevisionConflict is the 409 returned by a failed requirement
// revision CAS.
func requirementRevisionConflict(id int64) error {
	return domain.NewStateConflict("revision_conflict",
		fmt.Sprintf("requirement %d revision does not match", id))
}

// requirementStateConflict is the 409 returned by a failed status CAS or a write
// against a requirement that is no longer draft.
func requirementStateConflict(id int64) error {
	return domain.NewStateConflict("requirement_state_conflict",
		fmt.Sprintf("requirement %d status does not allow this action", id))
}

// requirementOpenQuestionsConflict is the 409 returned when a confirmation is
// attempted while the requirement still has open questions.
func requirementOpenQuestionsConflict(id int64) error {
	return domain.NewStateConflict("open_questions_remaining",
		fmt.Sprintf("requirement %d still has open questions", id))
}

// requirementCreateError translates insert failures into domain errors: a
// duplicate id is a conflict, a missing version is a not-found.
func requirementCreateError(err error, versionID int64) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case requirementUniqueViolationCode:
			return domain.NewConflict("duplicate_key", "a requirement with this id already exists")
		case requirementForeignKeyViolationCode:
			return domain.NewNotFound("version", versionID)
		}
	}
	return fmt.Errorf("postgres: create requirement: %w", err)
}

// relationCreateError translates relation insert/update failures into the stable
// spec §7.1 reasons: duplicate_relation for uniqueness violations (other than a
// duplicate id) and invalid_relation/invalid_relation_type for CHECK violations.
func relationWriteError(err error, operation string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case requirementUniqueViolationCode:
			if pgErr.ConstraintName == requirementRelationsPrimaryKey {
				return domain.NewConflict("duplicate_key", "a relation with this id already exists")
			}
			return domain.NewConflict("duplicate_relation", "an equivalent relation already exists")
		case requirementCheckViolationCode:
			if pgErr.ConstraintName == "requirement_relations_relation_type_check" {
				return domain.NewInvalid("relationType", "invalid_relation_type",
					"relationType must be depends_on, conflicts_with, overlaps or shares_scope")
			}
			return domain.NewInvalid("", "invalid_relation",
				"a requirement cannot have a relation to itself")
		}
	}
	return fmt.Errorf("postgres: %s requirement relation: %w", operation, err)
}

// attachmentCreateError translates attachment insert failures into domain errors.
func attachmentCreateError(err error, requirementID int64) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case requirementUniqueViolationCode:
			if pgErr.ConstraintName == requirementAttachmentsPrimaryKey {
				return domain.NewConflict("duplicate_key", "an attachment with this id already exists")
			}
			return domain.NewConflict("duplicate_attachment",
				"this requirement already has an attachment with the same sha256")
		case requirementForeignKeyViolationCode:
			return domain.NewNotFound("requirement", requirementID)
		case requirementCheckViolationCode:
			return domain.NewInvalid("", "invalid_attachment",
				"attachment violates the attachment constraints")
		}
	}
	return fmt.Errorf("postgres: create requirement attachment: %w", err)
}

// Create inserts requirement with its explicit Snowflake id and writes the full
// stored row (server timestamps included) back to it.
func (r *RequirementRepository) Create(ctx context.Context, requirement *domain.Requirement) error {
	const query = `INSERT INTO requirements
		(id, version_id, title, description, acceptance_criteria, priority, status,
		 source, last_update_source, revision, confirmed_at, confirmed_by, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING ` + requirementColumnList
	created, err := scanRequirement(r.pool.QueryRow(ctx, query,
		requirement.ID, requirement.VersionID, requirement.Title, requirement.Description,
		requirement.AcceptanceCriteria, requirement.Priority, requirement.Status,
		requirement.Source, requirement.LastUpdateSource, requirement.Revision,
		requirement.ConfirmedAt, requirement.ConfirmedBy, requirement.CreatedBy, requirement.UpdatedBy,
	))
	if err != nil {
		return requirementCreateError(err, requirement.VersionID)
	}
	*requirement = created
	return nil
}

// Get returns one requirement by id.
func (r *RequirementRepository) Get(ctx context.Context, id int64) (*domain.Requirement, error) {
	requirement, err := scanRequirement(r.pool.QueryRow(ctx,
		`SELECT `+requirementColumnList+` FROM requirements WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFound("requirement", id)
		}
		return nil, fmt.Errorf("postgres: get requirement: %w", err)
	}
	return &requirement, nil
}

// List returns one page ordered by created_at, id ascending. An empty status
// matches every status. The keyset cursor decodes to the last seen id and
// compares the full (created_at, id) tuple so pages stay stable when several rows
// share a created_at value. A cursor whose row has been deleted yields an empty
// page.
func (r *RequirementRepository) List(ctx context.Context, versionID int64, status string, p domain.Page) ([]domain.Requirement, bool, error) {
	if p.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list requirements: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(p.Cursor)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list requirements: %w", err)
	}
	var (
		cursorCreated time.Time
		hasCursor     bool
	)
	if cursorID != 0 {
		if err := r.pool.QueryRow(ctx,
			`SELECT created_at FROM requirements WHERE id = $1`, cursorID).Scan(&cursorCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return []domain.Requirement{}, false, nil
			}
			return nil, false, fmt.Errorf("postgres: list requirements: read cursor: %w", err)
		}
		hasCursor = true
	}

	const query = `SELECT ` + requirementColumnList + `
		FROM requirements
		WHERE version_id = $1
		  AND ($2 = '' OR status = $2)
		  AND ($3::boolean = false OR (created_at, id) > ($4::timestamptz, $5::bigint))
		ORDER BY created_at ASC, id ASC
		LIMIT $6`
	rows, err := r.pool.Query(ctx, query, versionID, status, hasCursor, cursorCreated, cursorID, p.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list requirements: %w", err)
	}
	defer rows.Close()

	requirements := make([]domain.Requirement, 0, p.Limit+1)
	for rows.Next() {
		requirement, err := scanRequirement(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan requirement: %w", err)
		}
		requirements = append(requirements, requirement)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list requirements: %w", err)
	}

	more := len(requirements) > p.Limit
	if more {
		requirements = requirements[:p.Limit]
	}
	return requirements, more, nil
}

// Update rewrites the draft-editable fields under the revision CAS and writes the
// stored row back to requirement. Non-draft rows (a concurrent confirmation) and
// stale revisions are state conflicts and never write.
func (r *RequirementRepository) Update(ctx context.Context, requirement *domain.Requirement, expected int) error {
	const query = `UPDATE requirements
		SET title = $3, description = $4, acceptance_criteria = $5, priority = $6,
		    last_update_source = $7, revision = revision + 1, updated_by = $8, updated_at = now()
		WHERE id = $1 AND revision = $2 AND status = 'draft'
		RETURNING ` + requirementColumnList
	updated, err := scanRequirement(r.pool.QueryRow(ctx, query,
		requirement.ID, expected, requirement.Title, requirement.Description,
		requirement.AcceptanceCriteria, requirement.Priority, requirement.LastUpdateSource,
		requirement.UpdatedBy,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r.requirementWriteConflict(ctx, requirement.ID)
		}
		return fmt.Errorf("postgres: update requirement: %w", err)
	}
	*requirement = updated
	return nil
}

// requirementWriteConflict classifies a zero-row update: a row that is gone or
// whose revision no longer matches is a revision_conflict, a non-draft row is a
// requirement_state_conflict (INV-9).
func (r *RequirementRepository) requirementWriteConflict(ctx context.Context, id int64) error {
	var status string
	if err := r.pool.QueryRow(ctx, `SELECT status FROM requirements WHERE id = $1`, id).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return requirementRevisionConflict(id)
		}
		return fmt.Errorf("postgres: read requirement status after update CAS: %w", err)
	}
	if status != domain.RequirementDraft {
		return requirementStateConflict(id)
	}
	return requirementRevisionConflict(id)
}

// Delete hard-deletes one draft requirement. Relations, questions, decisions and
// attachment metadata are removed by the schema's ON DELETE CASCADE constraints
// in the same statement (INV-10). Clarified requirements are a state conflict.
func (r *RequirementRepository) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM requirements WHERE id = $1 AND status = 'draft'`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete requirement: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var status string
		if err := r.pool.QueryRow(ctx, `SELECT status FROM requirements WHERE id = $1`, id).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.NewNotFound("requirement", id)
			}
			return fmt.Errorf("postgres: read requirement status after delete: %w", err)
		}
		return requirementStateConflict(id)
	}
	return nil
}

// UpdateStatus performs a status compare-and-swap. Confirmation additionally
// requires zero open questions of the requirement, checked inside the same
// status-CAS UPDATE statement (INV-3). Under READ COMMITTED a question that a
// concurrent transaction commits after this statement's snapshot may not be
// visible, so the gate is CAS-consistent rather than serializable; the spec
// accepts this concurrency model (question creation is owned by T10).
// Confirmed_at/by are stamped on confirmation and cleared on reopen; both
// transitions are human-only, so last_update_source becomes user.
func (r *RequirementRepository) UpdateStatus(ctx context.Context, id int64, from, to string, actor int64) (*domain.Requirement, error) {
	const query = `UPDATE requirements
		SET status = $3,
		    confirmed_at = CASE WHEN $3 = 'clarified' THEN now() ELSE NULL END,
		    confirmed_by = CASE WHEN $3 = 'clarified' THEN $4::bigint ELSE NULL END,
		    last_update_source = 'user',
		    revision = revision + 1, updated_by = $4, updated_at = now()
		WHERE id = $1 AND status = $2
		  AND ($3 <> 'clarified' OR NOT EXISTS (
		      SELECT 1 FROM clarification_questions q
		      WHERE q.requirement_id = $1 AND q.status = 'open'))
		RETURNING ` + requirementColumnList
	updated, err := scanRequirement(r.pool.QueryRow(ctx, query, id, from, to, actor))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.requirementStatusConflict(ctx, id, from, to)
		}
		return nil, fmt.Errorf("postgres: update requirement status: %w", err)
	}
	return &updated, nil
}

// requirementStatusConflict classifies a zero-row status CAS: a missing row or a
// mismatched current status is a requirement_state_conflict, while a matching
// draft with open questions is the confirmation gate's open_questions_remaining.
func (r *RequirementRepository) requirementStatusConflict(ctx context.Context, id int64, from, to string) error {
	var status string
	if err := r.pool.QueryRow(ctx, `SELECT status FROM requirements WHERE id = $1`, id).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return requirementStateConflict(id)
		}
		return fmt.Errorf("postgres: read requirement status after status CAS: %w", err)
	}
	if status != from {
		return requirementStateConflict(id)
	}
	if to == domain.RequirementClarified {
		open, err := r.CountOpenQuestions(ctx, id)
		if err != nil {
			return err
		}
		if open > 0 {
			return requirementOpenQuestionsConflict(id)
		}
	}
	return requirementStateConflict(id)
}

// CountOpenQuestions returns the number of the requirement's own open questions.
// Version-level questions have no requirement_id and never block a confirmation.
func (r *RequirementRepository) CountOpenQuestions(ctx context.Context, requirementID int64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*)::int FROM clarification_questions
		WHERE requirement_id = $1 AND status = 'open'`, requirementID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count requirement open questions: %w", err)
	}
	return count, nil
}

// ListRelations returns up to 500 relations of one version ordered by id. The
// spec §7.4 relation list is not paginated but has an upper bound of 500; the
// service applies the same cap as defence in depth.
func (r *RequirementRepository) ListRelations(ctx context.Context, versionID int64) ([]domain.Relation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+relationColumnList+` FROM requirement_relations
		 WHERE version_id = $1 ORDER BY id ASC LIMIT $2`,
		versionID, requirementRelationsListLimit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list requirement relations: %w", err)
	}
	defer rows.Close()

	relations := make([]domain.Relation, 0)
	for rows.Next() {
		relation, err := scanRelation(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan requirement relation: %w", err)
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list requirement relations: %w", err)
	}
	return relations, nil
}

// CreateRelation inserts relation after checking in the same statement that both
// endpoints belong to the relation's version (INV-7); the partial unique indexes
// reject normalized duplicates.
func (r *RequirementRepository) CreateRelation(ctx context.Context, relation *domain.Relation) error {
	const query = `INSERT INTO requirement_relations
		(id, version_id, from_requirement_id, to_requirement_id, relation_type, description, source, created_by)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8
		WHERE EXISTS (SELECT 1 FROM requirements WHERE id = $3 AND version_id = $2)
		  AND EXISTS (SELECT 1 FROM requirements WHERE id = $4 AND version_id = $2)
		RETURNING ` + relationColumnList
	created, err := scanRelation(r.pool.QueryRow(ctx, query,
		relation.ID, relation.VersionID, relation.FromRequirementID, relation.ToRequirementID,
		relation.RelationType, relation.Description, relation.Source, relation.CreatedBy,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewInvalid("", "relation_requirement_mismatch",
				"both requirements of a relation must belong to its version")
		}
		return relationWriteError(err, "create")
	}
	*relation = created
	return nil
}

// UpdateRelation rewrites one relation's type and description.
func (r *RequirementRepository) UpdateRelation(ctx context.Context, id int64, relType, desc string) (*domain.Relation, error) {
	const query = `UPDATE requirement_relations
		SET relation_type = $2, description = $3, updated_at = now()
		WHERE id = $1
		RETURNING ` + relationColumnList
	updated, err := scanRelation(r.pool.QueryRow(ctx, query, id, relType, desc))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFound("relation", id)
		}
		return nil, relationWriteError(err, "update")
	}
	return &updated, nil
}

// DeleteRelation removes one relation.
func (r *RequirementRepository) DeleteRelation(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM requirement_relations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete requirement relation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("relation", id)
	}
	return nil
}

// CreateAttachment inserts one attachment metadata row.
func (r *RequirementRepository) CreateAttachment(ctx context.Context, attachment *domain.Attachment) error {
	const query = `INSERT INTO requirement_attachments
		(id, version_id, requirement_id, kind, filename, mime_type, size_bytes, sha256, storage_path, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING ` + attachmentColumnList
	created, err := scanAttachment(r.pool.QueryRow(ctx, query,
		attachment.ID, attachment.VersionID, attachment.RequirementID, attachment.Kind,
		attachment.Filename, attachment.MIMEType, attachment.SizeBytes, attachment.SHA256,
		attachment.StoragePath, attachment.CreatedBy,
	))
	if err != nil {
		return attachmentCreateError(err, attachment.RequirementID)
	}
	*attachment = created
	return nil
}

// ListAttachments returns the requirement's attachments ordered by id.
func (r *RequirementRepository) ListAttachments(ctx context.Context, requirementID int64) ([]domain.Attachment, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+attachmentColumnList+` FROM requirement_attachments WHERE requirement_id = $1 ORDER BY id ASC`,
		requirementID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list requirement attachments: %w", err)
	}
	defer rows.Close()

	attachments := make([]domain.Attachment, 0)
	for rows.Next() {
		attachment, err := scanAttachment(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan requirement attachment: %w", err)
		}
		attachments = append(attachments, attachment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list requirement attachments: %w", err)
	}
	return attachments, nil
}

// GetAttachment returns one attachment metadata row by id.
func (r *RequirementRepository) GetAttachment(ctx context.Context, id int64) (*domain.Attachment, error) {
	attachment, err := scanAttachment(r.pool.QueryRow(ctx,
		`SELECT `+attachmentColumnList+` FROM requirement_attachments WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NewNotFound("attachment", id)
		}
		return nil, fmt.Errorf("postgres: get requirement attachment: %w", err)
	}
	return &attachment, nil
}

// DeleteAttachment removes one attachment metadata row.
func (r *RequirementRepository) DeleteAttachment(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM requirement_attachments WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete requirement attachment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("attachment", id)
	}
	return nil
}

// CountAttachments returns the attachment counts of one requirement and of its
// whole version.
func (r *RequirementRepository) CountAttachments(ctx context.Context, requirementID, versionID int64) (int, int, error) {
	var requirementCount, versionCount int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*)::int FROM requirement_attachments WHERE requirement_id = $1),
			(SELECT count(*)::int FROM requirement_attachments WHERE version_id = $2)`,
		requirementID, versionID).Scan(&requirementCount, &versionCount); err != nil {
		return 0, 0, fmt.Errorf("postgres: count requirement attachments: %w", err)
	}
	return requirementCount, versionCount, nil
}
