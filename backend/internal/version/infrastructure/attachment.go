package versionpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AttachmentRepository stores requirement_attachment runtime rows.
type AttachmentRepository struct {
	pool *pgxpool.Pool
}

// NewAttachmentRepository wraps a pgx pool.
func NewAttachmentRepository(pool *pgxpool.Pool) *AttachmentRepository {
	return &AttachmentRepository{pool: pool}
}

// Create inserts one attachment runtime row.
func (r *AttachmentRepository) Create(ctx context.Context, attachment version.Attachment) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO requirement_attachment (`+attachmentColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`, attachment.ID, attachment.VersionID, attachment.RequirementID, attachment.RequirementVersionID,
		attachment.ConversationMessageID, attachment.ContentRefID, attachment.Name, attachment.MIME,
		attachment.Size, attachment.ExtractionStatus, attachment.ExtractedContentRefID,
		encodeExtractionError(attachment.ExtractionErrorCode, attachment.ExtractionErrorText),
		attachment.CreatedBy, attachment.CreatedAt, attachment.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert requirement attachment: %w", err)
	}
	return nil
}

// Get loads one attachment by ID.
func (r *AttachmentRepository) Get(ctx context.Context, attachmentID int64) (version.Attachment, error) {
	attachment, err := scanAttachment(r.pool.QueryRow(ctx, `
		SELECT `+attachmentColumns+` FROM requirement_attachment WHERE id = $1
	`, attachmentID))
	if err != nil {
		return version.Attachment{}, wrapAttachmentDB("get requirement attachment", err)
	}
	return attachment, nil
}

// GetInVersion loads one attachment scoped to its version.
func (r *AttachmentRepository) GetInVersion(ctx context.Context, versionID, attachmentID int64) (version.Attachment, error) {
	attachment, err := scanAttachment(r.pool.QueryRow(ctx, `
		SELECT `+attachmentColumns+` FROM requirement_attachment
		WHERE id = $1 AND version_id = $2
	`, attachmentID, versionID))
	if err != nil {
		return version.Attachment{}, wrapAttachmentDB("get requirement attachment", err)
	}
	return attachment, nil
}

// ListByRequirement returns the attachments linked to one requirement.
func (r *AttachmentRepository) ListByRequirement(ctx context.Context, versionID, requirementID int64) ([]version.Attachment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+attachmentColumns+` FROM requirement_attachment
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

// ListByConversation returns the attachments linked to one conversation via
// its messages, ordered by attachment ID.
func (r *AttachmentRepository) ListByConversation(ctx context.Context, versionID, conversationID int64) ([]version.Attachment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.version_id, a.requirement_id, a.requirement_version_id,
			a.conversation_message_id, a.content_ref_id, a.name, a.mime, a.size,
			a.extraction_status, a.extracted_content_ref_id, a.extraction_error,
			a.created_by, a.created_at, a.updated_at
		FROM requirement_attachment a
		JOIN conversation_message m ON m.id = a.conversation_message_id
		WHERE a.version_id = $1 AND m.conversation_id = $2
		ORDER BY a.id
	`, versionID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list conversation attachments: %w", err)
	}
	defer rows.Close()
	attachments := make([]version.Attachment, 0)
	for rows.Next() {
		attachment, err := scanAttachment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan conversation attachment: %w", err)
		}
		attachments = append(attachments, attachment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list conversation attachments: %w", err)
	}
	return attachments, nil
}

// ConversationExists reports whether the conversation exists in the version.
// Attachments never cross versions.
func (r *AttachmentRepository) ConversationExists(ctx context.Context, versionID, conversationID int64) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM conversation WHERE id = $1 AND version_id = $2)
	`, conversationID, versionID).Scan(&exists); err != nil {
		return false, fmt.Errorf("attachment conversation check: %w", err)
	}
	return exists, nil
}

// ListPendingForIngest returns the oldest pending attachments together with
// their conversation/message linkage (zero when a message is not stored yet).
func (r *AttachmentRepository) ListPendingForIngest(ctx context.Context, limit int) ([]version.PendingAttachment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.version_id, COALESCE(m.conversation_id, 0), COALESCE(a.conversation_message_id, 0)
		FROM requirement_attachment a
		LEFT JOIN conversation_message m ON m.id = a.conversation_message_id
		WHERE a.extraction_status = 'pending'
		ORDER BY a.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending attachments: %w", err)
	}
	defer rows.Close()
	pending := make([]version.PendingAttachment, 0)
	for rows.Next() {
		var item version.PendingAttachment
		if err := rows.Scan(&item.ID, &item.VersionID, &item.ConversationID, &item.MessageID); err != nil {
			return nil, fmt.Errorf("scan pending attachment: %w", err)
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending attachments: %w", err)
	}
	return pending, nil
}

// MarkProcessing flips pending -> processing and reports whether this call
// changed the row.
func (r *AttachmentRepository) MarkProcessing(ctx context.Context, attachmentID int64, updatedAt time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE requirement_attachment SET extraction_status = 'processing', updated_at = $2
		WHERE id = $1 AND extraction_status = 'pending'
	`, attachmentID, updatedAt)
	if err != nil {
		return false, fmt.Errorf("mark attachment processing: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, r.ensureAttachmentExists(ctx, attachmentID, "mark attachment processing")
}

// MarkDone stores the extracted text reference. Repeated calls are idempotent
// and keep the first reference; a done row is never overwritten.
func (r *AttachmentRepository) MarkDone(ctx context.Context, attachmentID int64, extractedContentRefID int64, updatedAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE requirement_attachment
		SET extraction_status = 'done', extracted_content_ref_id = $2,
		    extraction_error = NULL, updated_at = $3
		WHERE id = $1 AND extraction_status <> 'done'
	`, attachmentID, extractedContentRefID, updatedAt)
	if err != nil {
		return fmt.Errorf("mark attachment done: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.ensureAttachmentExists(ctx, attachmentID, "mark attachment done")
}

// MarkFailed stores the stable error code/message. A done row is never
// overwritten; repeated failures are idempotent.
func (r *AttachmentRepository) MarkFailed(ctx context.Context, attachmentID int64, code, message string, updatedAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE requirement_attachment
		SET extraction_status = 'failed', extraction_error = $2, updated_at = $3
		WHERE id = $1 AND extraction_status <> 'done'
	`, attachmentID, encodeExtractionError(code, message), updatedAt)
	if err != nil {
		return fmt.Errorf("mark attachment failed: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.ensureAttachmentExists(ctx, attachmentID, "mark attachment failed")
}

func (r *AttachmentRepository) ensureAttachmentExists(ctx context.Context, attachmentID int64, op string) error {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM requirement_attachment WHERE id = $1)`, attachmentID).Scan(&exists); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if !exists {
		return version.ErrAttachmentNotFound
	}
	return nil
}

// attachmentColumns is shared by the requirement aggregate (attachment list)
// and the attachment repository.
const attachmentColumns = `id, version_id, requirement_id, requirement_version_id,
	conversation_message_id, content_ref_id, name, mime, size, extraction_status,
	extracted_content_ref_id, extraction_error, created_by, created_at, updated_at`

func scanAttachment(row rowScanner) (version.Attachment, error) {
	var attachment version.Attachment
	var extractionError *string
	if err := row.Scan(&attachment.ID, &attachment.VersionID, &attachment.RequirementID,
		&attachment.RequirementVersionID, &attachment.ConversationMessageID, &attachment.ContentRefID,
		&attachment.Name, &attachment.MIME, &attachment.Size, &attachment.ExtractionStatus,
		&attachment.ExtractedContentRefID, &extractionError, &attachment.CreatedBy,
		&attachment.CreatedAt, &attachment.UpdatedAt); err != nil {
		return version.Attachment{}, fmt.Errorf("scan attachment: %w", err)
	}
	attachment.ExtractionErrorCode, attachment.ExtractionErrorText = decodeExtractionError(extractionError)
	return attachment, nil
}

func wrapAttachmentDB(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return version.ErrAttachmentNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

var _ version.AttachmentRepository = (*AttachmentRepository)(nil)
