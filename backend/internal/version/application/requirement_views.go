package versionapp

import (
	"encoding/json"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// RequirementDraft is the Create/Update payload. The OpenAPI contract requires
// every content field; targets are optional decimal string arrays.
type RequirementDraft struct {
	Title              string
	Description        string
	AcceptanceCriteria string
	Priority           string
	Constraints        json.RawMessage
	TargetVersionSUTs  []string
	TargetPages        []string
	TargetAPIs         []string
}

// RequirementView is the current-revision summary returned by the list API and
// embedded in the detail.
type RequirementView struct {
	ID                int64
	CurrentVersionID  int64
	Revision          int
	Title             string
	Status            string
	Priority          string
	TargetVersionSUTs []string
	TargetPages       []string
	TargetAPIs        []string
	UpdatedAt         time.Time
	ConfirmedAt       *time.Time
}

// RequirementVersionView is one immutable requirement revision.
type RequirementVersionView struct {
	ID                 int64
	RequirementID      int64
	Revision           int
	Title              string
	Description        string
	AcceptanceCriteria string
	Priority           string
	Constraints        json.RawMessage
	TargetVersionSUTs  []string
	TargetPages        []string
	TargetAPIs         []string
	ContentHash        string
	CreatedBy          *int64
	CreatedByRunID     *int64
	CreatedAt          time.Time
}

// RequirementDetail is the requirement detail read model: summary plus the
// current revision, the older revisions, confirmations, relations and
// attachments.
type RequirementDetail struct {
	RequirementView
	CurrentVersion RequirementVersionView
	History        []RequirementVersionView
	Confirmations  []ConfirmationView
	Relations      []RelationView
	Attachments    []AttachmentView
}

// ConfirmationView is one frozen revision confirmation.
type ConfirmationView struct {
	ID                     int64
	RequirementVersionID   int64
	Scope                  string
	BatchID                *string
	ActorID                int64
	ConfirmedAt            time.Time
	AffectedRequirementIDs []string
}

// RelationView is one requirement relation.
type RelationView struct {
	ID                int64
	FromRequirementID int64
	ToRequirementID   int64
	Kind              string
	Description       string
	CreatedAt         time.Time
}

// AttachmentView is the attachment runtime state exposed on requirement detail.
type AttachmentView struct {
	ID                  int64
	Name                string
	MIME                string
	Size                int64
	ExtractionStatus    string
	ExtractionErrorCode string
	ExtractionErrorText string
	CreatedAt           time.Time
}

// RelationInput is the command for creating a requirement relation.
type RelationInput struct {
	FromRequirementID int64
	ToRequirementID   int64
	Kind              string
	Description       string
	SourceRevisionID  *int64
}

// ConfirmResult is the single-confirmation response.
type ConfirmResult struct {
	Confirmed   bool
	Requirement RequirementDetail
}

// BatchConfirmResult is the batch-confirmation response.
type BatchConfirmResult struct {
	BatchID   string
	Confirmed []int64
	Skipped   []int64
}

// ---------- domain -> view conversions ----------

func toRequirementVersionView(revision version.RequirementVersion) RequirementVersionView {
	return RequirementVersionView{
		ID:                 revision.ID,
		RequirementID:      revision.RequirementID,
		Revision:           revision.Revision,
		Title:              revision.Title,
		Description:        revision.Description,
		AcceptanceCriteria: revision.AcceptanceCriteria,
		Priority:           revision.Priority,
		Constraints:        revision.Constraints,
		TargetVersionSUTs:  nonNilStrings(revision.TargetVersionSUTs),
		TargetPages:        nonNilStrings(revision.TargetPages),
		TargetAPIs:         nonNilStrings(revision.TargetAPIs),
		ContentHash:        revision.ContentHash,
		CreatedBy:          revision.CreatedBy,
		CreatedByRunID:     revision.CreatedByRunID,
		CreatedAt:          revision.CreatedAt,
	}
}

func toConfirmationView(confirmation version.Confirmation) ConfirmationView {
	return ConfirmationView{
		ID:                     confirmation.ID,
		RequirementVersionID:   confirmation.RequirementVersionID,
		Scope:                  confirmation.Scope,
		BatchID:                confirmation.BatchID,
		ActorID:                confirmation.ActorID,
		ConfirmedAt:            confirmation.ConfirmedAt,
		AffectedRequirementIDs: nonNilStrings(confirmation.AffectedRequirementIDs),
	}
}

func toRelationView(relation version.Relation) RelationView {
	return RelationView{
		ID:                relation.ID,
		FromRequirementID: relation.FromRequirementID,
		ToRequirementID:   relation.ToRequirementID,
		Kind:              relation.Kind,
		Description:       relation.Description,
		CreatedAt:         relation.CreatedAt,
	}
}

func toAttachmentView(attachment version.Attachment) AttachmentView {
	return AttachmentView{
		ID:                  attachment.ID,
		Name:                attachment.Name,
		MIME:                attachment.MIME,
		Size:                attachment.Size,
		ExtractionStatus:    attachment.ExtractionStatus,
		ExtractionErrorCode: attachment.ExtractionErrorCode,
		ExtractionErrorText: attachment.ExtractionErrorText,
		CreatedAt:           attachment.CreatedAt,
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
