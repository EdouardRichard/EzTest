package version

import (
	"context"
	"errors"
	"strings"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	// requirementPageSize is the cursor page size used while draining a
	// version's confirmed requirements. It matches the P3 HTTP page bound.
	requirementPageSize = 100
	// maxAttachmentDigestAttachments bounds the text attachments folded into one
	// requirement's digest (spec §8.3: at most 20 per requirement).
	maxAttachmentDigestAttachments = 20
	// maxAttachmentDigestBytes bounds one text attachment read (spec §8.3:
	// at most 8 KiB per attachment). Image attachments are never read.
	maxAttachmentDigestBytes = 8 << 10
)

// RequirementLister is the narrow slice of the P3 requirement service the
// adapter needs. *version/application.RequirementService satisfies it.
type RequirementLister interface {
	List(ctx context.Context, versionID int64, status string, page versiondomain.Page) ([]versionapp.Requirement, bool, error)
}

// RelationLister is the narrow slice of the P3 relation service the adapter
// needs. *version/application.RelationService satisfies it.
type RelationLister interface {
	List(ctx context.Context, versionID int64) ([]versionapp.Relation, error)
}

// AttachmentReader is the optional slice of the P3 attachment service used to
// build the requirement text-attachment digest. *version/application.AttachmentService
// satisfies it. A nil reader leaves AttachmentSummary empty.
type AttachmentReader interface {
	List(ctx context.Context, versionID, requirementID int64) ([]versionapp.Attachment, error)
	ReadText(ctx context.Context, versionID, attachmentID int64, maxBytes int) (string, error)
}

// RequirementReaderAdapter implements application.RequirementReader on top of
// the P3 requirement, relation and attachment services.
type RequirementReaderAdapter struct {
	requirements RequirementLister
	relations    RelationLister
	attachments  AttachmentReader
}

// NewRequirementReader wires the adapter to the P3 requirement and relation
// services plus the optional attachment reader (nil disables the digest).
func NewRequirementReader(requirements RequirementLister, relations RelationLister, attachments AttachmentReader) *RequirementReaderAdapter {
	return &RequirementReaderAdapter{requirements: requirements, relations: relations, attachments: attachments}
}

var _ mindmapapp.RequirementReader = (*RequirementReaderAdapter)(nil)

// ListConfirmed returns every clarified requirement of versionID, draining the
// P3 cursor pages so run context assembly never sees only the first 100 rows.
func (a *RequirementReaderAdapter) ListConfirmed(ctx context.Context, versionID int64) ([]mindmapapp.RequirementSnapshot, error) {
	if a == nil || a.requirements == nil {
		return nil, errors.New("mindmap: requirement reader is not configured")
	}
	confirmed := make([]mindmapapp.RequirementSnapshot, 0)
	cursor := ""
	for {
		page, more, err := a.requirements.List(ctx, versionID, versiondomain.RequirementClarified, versiondomain.Page{
			Limit:  requirementPageSize,
			Cursor: cursor,
		})
		if err != nil {
			return nil, mapVersionError(err)
		}
		for _, requirement := range page {
			summary, err := a.attachmentSummary(ctx, versionID, requirement.ID)
			if err != nil {
				return nil, err
			}
			confirmed = append(confirmed, mindmapapp.RequirementSnapshot{
				ID:                 requirement.ID,
				VersionID:          requirement.VersionID,
				Title:              requirement.Title,
				Description:        requirement.Description,
				AcceptanceCriteria: requirement.AcceptanceCriteria,
				Priority:           requirement.Priority,
				Status:             requirement.Status,
				AttachmentSummary:  summary,
			})
		}
		if !more {
			return confirmed, nil
		}
		if len(page) == 0 {
			return nil, errors.New("mindmap: list requirements: empty page with more set")
		}
		next := platform.EncodeCursor(page[len(page)-1].ID)
		if next == cursor {
			return nil, errors.New("mindmap: list requirements: cursor did not advance")
		}
		cursor = next
	}
}

// ListRelations returns every requirement relation of versionID. The P3 relation
// service already bounds one version's listing, so the adapter maps one-to-one.
func (a *RequirementReaderAdapter) ListRelations(ctx context.Context, versionID int64) ([]mindmapapp.RelationSnapshot, error) {
	if a == nil || a.relations == nil {
		return nil, errors.New("mindmap: relation reader is not configured")
	}
	relations, err := a.relations.List(ctx, versionID)
	if err != nil {
		return nil, mapVersionError(err)
	}
	mapped := make([]mindmapapp.RelationSnapshot, 0, len(relations))
	for _, relation := range relations {
		mapped = append(mapped, mindmapapp.RelationSnapshot{
			ID:                relation.ID,
			VersionID:         relation.VersionID,
			FromRequirementID: relation.FromRequirementID,
			ToRequirementID:   relation.ToRequirementID,
			RelationType:      relation.RelationType,
			Description:       relation.Description,
		})
	}
	return mapped, nil
}

// attachmentSummary builds the plain-text digest of one requirement's text
// attachments (spec §8.3): at most 20 text files, at most 8 KiB each, joined in
// the P3 listing order. Image attachments are skipped and never read.
func (a *RequirementReaderAdapter) attachmentSummary(ctx context.Context, versionID, requirementID int64) (string, error) {
	if a.attachments == nil {
		return "", nil
	}
	attachments, err := a.attachments.List(ctx, versionID, requirementID)
	if err != nil {
		return "", mapVersionError(err)
	}
	parts := make([]string, 0, min(len(attachments), maxAttachmentDigestAttachments))
	for _, attachment := range attachments {
		if len(parts) >= maxAttachmentDigestAttachments {
			break
		}
		if attachment.Kind != versiondomain.AttachmentText {
			continue
		}
		text, err := a.attachments.ReadText(ctx, versionID, attachment.ID, maxAttachmentDigestBytes)
		if err != nil {
			return "", mapVersionError(err)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n"), nil
}
