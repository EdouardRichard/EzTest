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

// ListByConversation returns the attachments visible to one conversation,
// ordered by attachment ID: rows whose message belongs to the conversation,
// plus pre-registered message attachments whose conversation_message_id is
// still NULL. The second set is the attachment.ingest race window (R-E4-1): the
// message post preflight stores the row and starts extraction before the
// message insert, so attachment.list/read_text must already see it instead of
// returning 0/not_found while the parse runs. Requirement attachments keep
// their requirement scope (requirement_id/requirement_version_id) and are never
// pulled into a conversation through this path.
func (r *AttachmentRepository) ListByConversation(ctx context.Context, versionID, conversationID int64) ([]version.Attachment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.version_id, a.requirement_id, a.requirement_version_id,
			a.conversation_message_id, a.content_ref_id, a.name, a.mime, a.size,
			a.extraction_status, a.extracted_content_ref_id, a.extraction_error,
			a.created_by, a.created_at, a.updated_at
		FROM requirement_attachment a
		LEFT JOIN conversation_message m ON m.id = a.conversation_message_id
		WHERE a.version_id = $1
		  AND (
		        m.conversation_id = $2
		        OR (a.conversation_message_id IS NULL
		            AND a.requirement_id IS NULL
		            AND a.requirement_version_id IS NULL)
		      )
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

// ListUnlinkedByVersion returns the attachment rows pre-registered before their
// message insert. They have no conversation linkage yet and are reused by the
// message post retry so extraction is not duplicated.
func (r *AttachmentRepository) ListUnlinkedByVersion(ctx context.Context, versionID int64) ([]version.Attachment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+attachmentColumns+` FROM requirement_attachment
		WHERE version_id = $1 AND conversation_message_id IS NULL
		ORDER BY id
	`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list unlinked attachments: %w", err)
	}
	defer rows.Close()
	attachments := make([]version.Attachment, 0)
	for rows.Next() {
		attachment, err := scanAttachment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan unlinked attachment: %w", err)
		}
		attachments = append(attachments, attachment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unlinked attachments: %w", err)
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

// SetExtractedContentRef stores the extracted-text reference without changing
// the extraction status. It lets image analysis fail the attachment while the
// text reference survives for a later successful reanalyze.
func (r *AttachmentRepository) SetExtractedContentRef(ctx context.Context, attachmentID, contentRefID int64, updatedAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE requirement_attachment
		SET extracted_content_ref_id = $2, updated_at = $3
		WHERE id = $1
	`, attachmentID, contentRefID, updatedAt)
	if err != nil {
		return fmt.Errorf("set attachment extracted content ref: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.ensureAttachmentExists(ctx, attachmentID, "set attachment extracted content ref")
}

// CreateImage inserts one attachment_image row. The unique (attachment_id, seq)
// makes a retried extraction idempotent: an existing row is left untouched, so
// no duplicate rows or content references are created.
func (r *AttachmentRepository) CreateImage(ctx context.Context, image version.AttachmentImageRecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO attachment_image (
			id, attachment_id, seq, content_ref_id, mime, width, height, source_kind,
			status, caption, error, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (attachment_id, seq) DO NOTHING
	`, image.ID, image.AttachmentID, image.Seq, image.ContentRefID, image.MIME,
		nullablePositiveInt(image.Width), nullablePositiveInt(image.Height), image.SourceKind,
		image.Status, image.Caption, image.Error, image.CreatedAt, image.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert attachment image: %w", err)
	}
	return nil
}

// ListImages returns the image rows of one attachment ordered by seq.
func (r *AttachmentRepository) ListImages(ctx context.Context, attachmentID int64) ([]version.AttachmentImageRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+attachmentImageColumns+` FROM attachment_image
		WHERE attachment_id = $1
		ORDER BY seq
	`, attachmentID)
	if err != nil {
		return nil, fmt.Errorf("list attachment images: %w", err)
	}
	defer rows.Close()
	images := make([]version.AttachmentImageRecord, 0)
	for rows.Next() {
		image, err := scanAttachmentImage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan attachment image: %w", err)
		}
		images = append(images, image)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list attachment images: %w", err)
	}
	return images, nil
}

// MarkImageAnalyzing flips an extracted/skipped/failed row to analyzing and
// reports whether this call changed it. A done/analyzing row is untouched.
func (r *AttachmentRepository) MarkImageAnalyzing(ctx context.Context, imageID int64, updatedAt time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE attachment_image SET status = 'analyzing', updated_at = $2
		WHERE id = $1 AND status IN ('extracted', 'skipped', 'failed')
	`, imageID, updatedAt)
	if err != nil {
		return false, fmt.Errorf("mark attachment image analyzing: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, r.ensureAttachmentImageExists(ctx, imageID, "mark attachment image analyzing")
}

// MarkImageDone stores the caption and clears any previous analysis error.
func (r *AttachmentRepository) MarkImageDone(ctx context.Context, imageID int64, caption string, updatedAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE attachment_image
		SET status = 'done', caption = $2, error = '', updated_at = $3
		WHERE id = $1
	`, imageID, caption, updatedAt)
	if err != nil {
		return fmt.Errorf("mark attachment image done: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.ensureAttachmentImageExists(ctx, imageID, "mark attachment image done")
}

// MarkImageFailed stores the analysis failure reason and clears any caption
// written before the failure, so a failed row never exposes an analyzed caption
// to API/event consumers.
func (r *AttachmentRepository) MarkImageFailed(ctx context.Context, imageID int64, message string, updatedAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE attachment_image
		SET status = 'failed', caption = '', error = $2, updated_at = $3
		WHERE id = $1
	`, imageID, message, updatedAt)
	if err != nil {
		return fmt.Errorf("mark attachment image failed: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.ensureAttachmentImageExists(ctx, imageID, "mark attachment image failed")
}

// ResetImagesForReanalysis re-queues failed/skipped rows as extracted and
// clears caption/error. The content reference is preserved, so reanalyze never
// creates a duplicate content object.
func (r *AttachmentRepository) ResetImagesForReanalysis(ctx context.Context, attachmentID int64, updatedAt time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE attachment_image
		SET status = 'extracted', caption = '', error = '', updated_at = $2
		WHERE attachment_id = $1 AND status IN ('failed', 'skipped')
	`, attachmentID, updatedAt)
	if err != nil {
		return 0, fmt.Errorf("reset attachment images for reanalysis: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *AttachmentRepository) ensureAttachmentImageExists(ctx context.Context, imageID int64, op string) error {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM attachment_image WHERE id = $1)`, imageID).Scan(&exists); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if !exists {
		return version.ErrAttachmentNotFound
	}
	return nil
}

func nullablePositiveInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

const attachmentImageColumns = `id, attachment_id, seq, content_ref_id, mime, width, height,
	source_kind, status, caption, error, created_at, updated_at`

func scanAttachmentImage(row rowScanner) (version.AttachmentImageRecord, error) {
	var image version.AttachmentImageRecord
	var width, height *int
	if err := row.Scan(&image.ID, &image.AttachmentID, &image.Seq, &image.ContentRefID,
		&image.MIME, &width, &height, &image.SourceKind, &image.Status,
		&image.Caption, &image.Error, &image.CreatedAt, &image.UpdatedAt); err != nil {
		return version.AttachmentImageRecord{}, err
	}
	if width != nil {
		image.Width = *width
	}
	if height != nil {
		image.Height = *height
	}
	return image, nil
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

// LinkMessage binds a pre-registered row to the inserted message. Linking the
// same message twice is idempotent; a row already owned by another message is
// left untouched.
func (r *AttachmentRepository) LinkMessage(ctx context.Context, attachmentID, messageID int64, updatedAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE requirement_attachment
		SET conversation_message_id = $2, updated_at = $3
		WHERE id = $1 AND (conversation_message_id IS NULL OR conversation_message_id = $2)
	`, attachmentID, messageID, updatedAt)
	if err != nil {
		return fmt.Errorf("link attachment to message: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.ensureAttachmentExists(ctx, attachmentID, "link attachment to message")
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
