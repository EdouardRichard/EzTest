package httpapi

import (
	"strconv"
	"strings"
	"time"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

/* --------------------------------- requests -------------------------------- */

// createItemRequest is the POST /api/knowledge/items body. Unknown fields
// (including a client-sent status) are ignored: the server owns status,
// review metadata and revision. IDs arrive as decimal strings.
type createItemRequest struct {
	Title                 string   `json:"title"`
	Content               string   `json:"content"`
	Scope                 string   `json:"scope"`
	SystemKey             *string  `json:"system_key"`
	Tags                  []string `json:"tags"`
	SourceVersionID       *string  `json:"source_version_id"`
	PromotedFromVersionID *string  `json:"promoted_from_version_id"`
}

func (r createItemRequest) toInput() (knowledgeapp.CreateInput, error) {
	sourceVersionID, err := parseOptionalID("source_version_id", r.SourceVersionID)
	if err != nil {
		return knowledgeapp.CreateInput{}, err
	}
	promotedFrom, err := parseOptionalID("promoted_from_version_id", r.PromotedFromVersionID)
	if err != nil {
		return knowledgeapp.CreateInput{}, err
	}
	return knowledgeapp.CreateInput{
		Title:                 r.Title,
		Content:               r.Content,
		Scope:                 knowledge.Scope(strings.TrimSpace(r.Scope)),
		SystemKey:             r.SystemKey,
		Tags:                  r.Tags,
		SourceVersionID:       sourceVersionID,
		PromotedFromVersionID: promotedFrom,
	}, nil
}

// updateItemRequest is the PATCH /api/knowledge/items/{knowledgeId} body. Nil
// pointers keep the stored value.
type updateItemRequest struct {
	Title     *string   `json:"title"`
	Content   *string   `json:"content"`
	Tags      *[]string `json:"tags"`
	Scope     *string   `json:"scope"`
	SystemKey *string   `json:"system_key"`
}

func (r updateItemRequest) toInput() knowledgeapp.UpdateInput {
	input := knowledgeapp.UpdateInput{
		Title:     r.Title,
		Content:   r.Content,
		Tags:      r.Tags,
		SystemKey: r.SystemKey,
	}
	if r.Scope != nil {
		scope := knowledge.Scope(strings.TrimSpace(*r.Scope))
		input.Scope = &scope
	}
	return input
}

// reasonRequest is the shared body of approve/reject/revoke.
type reasonRequest struct {
	Reason string `json:"reason"`
}

func parseOptionalID(field string, raw *string) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, platform.Invalid(field, "must be a positive decimal ID")
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, platform.Invalid(field, "must be a positive decimal ID")
	}
	return &parsed, nil
}

/* ---------------------------------- views ---------------------------------- */

// knowledgeItemView is the frozen KnowledgeItemView field set (contracts/
// p7-api.md §3).
type knowledgeItemView struct {
	ID                    string   `json:"id"`
	Scope                 string   `json:"scope"`
	SystemKey             *string  `json:"system_key"`
	Title                 string   `json:"title"`
	Content               string   `json:"content"`
	Tags                  []string `json:"tags"`
	Status                string   `json:"status"`
	Confidence            *float64 `json:"confidence"`
	SourceVersionID       *string  `json:"source_version_id"`
	PromotedFromVersionID *string  `json:"promoted_from_version_id"`
	Revision              int      `json:"revision"`
	ReviewReason          *string  `json:"review_reason"`
	ReviewedBy            *string  `json:"reviewed_by"`
	ReviewedAt            *string  `json:"reviewed_at"`
	RevokedAt             *string  `json:"revoked_at"`
	SourceStale           bool     `json:"source_stale"`
	CreatedBy             *string  `json:"created_by"`
	CreatedAt             string   `json:"created_at"`
	UpdatedAt             string   `json:"updated_at"`
}

// knowledgeItemDetailView is the GET detail: the item fields plus the
// append-only content history (p7-api.md §1).
type knowledgeItemDetailView struct {
	knowledgeItemView
	Revisions []knowledgeRevisionView `json:"revisions"`
}

// knowledgeRevisionView is the frozen KnowledgeRevisionView field set.
type knowledgeRevisionView struct {
	ID          string   `json:"id"`
	KnowledgeID string   `json:"knowledge_id"`
	Revision    int      `json:"revision"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
	ChangedBy   *string  `json:"changed_by"`
	CreatedAt   string   `json:"created_at"`
}

func toItemView(item knowledge.Item) knowledgeItemView {
	return knowledgeItemView{
		ID:                    platformid.Format(item.ID),
		Scope:                 string(item.Scope),
		SystemKey:             item.SystemKey,
		Title:                 item.Title,
		Content:               item.Content,
		Tags:                  tagsView(item.Tags),
		Status:                string(item.Status),
		Confidence:            item.Confidence,
		SourceVersionID:       idStringPointer(item.SourceVersionID),
		PromotedFromVersionID: idStringPointer(item.PromotedFromVersionID),
		Revision:              item.Revision,
		ReviewReason:          item.ReviewReason,
		ReviewedBy:            idStringPointer(item.ReviewedBy),
		ReviewedAt:            timeStringPointer(item.ReviewedAt),
		RevokedAt:             timeStringPointer(item.RevokedAt),
		SourceStale:           item.SourceStale,
		CreatedBy:             idStringPointer(item.CreatedBy),
		CreatedAt:             formatTime(item.CreatedAt),
		UpdatedAt:             formatTime(item.UpdatedAt),
	}
}

func toItemDetailView(item knowledge.Item, revisions []knowledge.Revision) knowledgeItemDetailView {
	views := make([]knowledgeRevisionView, 0, len(revisions))
	for _, revision := range revisions {
		views = append(views, toRevisionView(revision))
	}
	return knowledgeItemDetailView{knowledgeItemView: toItemView(item), Revisions: views}
}

func toRevisionView(revision knowledge.Revision) knowledgeRevisionView {
	return knowledgeRevisionView{
		ID:          platformid.Format(revision.ID),
		KnowledgeID: platformid.Format(revision.KnowledgeID),
		Revision:    revision.Revision,
		Title:       revision.Title,
		Content:     revision.Content,
		Tags:        tagsView(revision.Tags),
		ChangedBy:   idStringPointer(revision.ChangedBy),
		CreatedAt:   formatTime(revision.CreatedAt),
	}
}

func tagsView(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return append([]string{}, tags...)
}

func idStringPointer(value *int64) *string {
	if value == nil {
		return nil
	}
	formatted := platformid.Format(*value)
	return &formatted
}

func timeStringPointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
