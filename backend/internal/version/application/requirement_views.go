package versionapp

import (
	"encoding/json"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// RequirementUserStoryView is the §5.1 three-part user story on a requirement
// read face.
type RequirementUserStoryView struct {
	AsA    string
	IWant  string
	SoThat string
}

// RequirementAcceptanceCriterionView is one structured acceptance criterion
// with its server-assigned, revision-stable id ("AC-<n>").
type RequirementAcceptanceCriterionView struct {
	ID    string
	Given string
	When  string
	Then  string
}

// RequirementAcceptanceCriterionInput is one acceptance criterion as a caller
// submits it. There is deliberately no ID field: ids are server-assigned by
// version.AssignACIDs against the previous revision, so a caller can neither
// pick an id nor resurrect one that a deleted criterion left behind.
type RequirementAcceptanceCriterionInput struct {
	Given string
	When  string
	Then  string
}

// RequirementDraft is the Create/Update payload: the design v1.3.1 §5.1 V1
// clarification contract. There are no target resource IDs any more -- a
// version binds an environment and the requirement names its subject in
// SutDescription.
type RequirementDraft struct {
	Title              string
	UserStory          RequirementUserStoryView
	Rules              string
	AcceptanceCriteria []RequirementAcceptanceCriterionInput
	Priority           string
	Constraints        json.RawMessage
	SutDescription     string
	ScopeOut           string
	AccountRolesHint   []string
}

// RequirementView is the current-revision summary returned by the list API and
// embedded in the detail. ClosedAt/Closure are the P4 closure projection: they
// are nil until the requirement closes and ClosedAt is cleared again by a
// reopen (the newest closure round keeps the historical reopen count).
type RequirementView struct {
	ID               int64
	CurrentVersionID int64
	Revision         int
	Title            string
	Status           string
	Priority         string
	UpdatedAt        time.Time
	ConfirmedAt      *time.Time
	ClosedAt         *time.Time
	Closure          *RequirementClosureView
	// Waived carries the admin requirement-waiver flag (v0.2.4-G SC-G11); users
	// can view it (Q7=A). Cleared by a requirement or version reopen.
	Waived bool
}

// RequirementClosureView is the newest requirement_closure round summary
// exposed on requirement reads (spec §4.2). Internal checks/evidence are not
// part of the projection.
type RequirementClosureView struct {
	ID          int64
	CloseRound  int
	ClosedAt    time.Time
	ReopenCount int
}

// RequirementVersionView is one immutable requirement revision.
type RequirementVersionView struct {
	ID                 int64
	RequirementID      int64
	Revision           int
	Title              string
	UserStory          RequirementUserStoryView
	Rules              string
	AcceptanceCriteria []RequirementAcceptanceCriterionView
	Priority           string
	Constraints        json.RawMessage
	SutDescription     string
	ScopeOut           string
	AccountRolesHint   []string
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

// RequirementDedupeResult is the duplicate-draft collapse response: one
// survivor per group is kept and the remaining duplicates are retired
// (obsolete).
type RequirementDedupeResult struct {
	KeptIDs     []int64
	ObsoleteIDs []int64
}

// RequirementMergeResult is the duplicate-merge response: the canonical target
// (with the merged content when it changed) and the retired sources.
type RequirementMergeResult struct {
	Requirement RequirementDetail
	MergedIDs   []int64
}

// ---------- domain -> view conversions ----------

func toRequirementVersionView(revision version.RequirementVersion) RequirementVersionView {
	return RequirementVersionView{
		ID:                 revision.ID,
		RequirementID:      revision.RequirementID,
		Revision:           revision.Revision,
		Title:              revision.Title,
		UserStory:          toUserStoryView(revision.UserStory),
		Rules:              revision.Rules,
		AcceptanceCriteria: toAcceptanceCriterionViews(revision.AcceptanceCriteria),
		Priority:           revision.Priority,
		Constraints:        revision.Constraints,
		SutDescription:     revision.SutDescription,
		ScopeOut:           revision.ScopeOut,
		AccountRolesHint:   nonNilStrings(revision.AccountRolesHint),
		ContentHash:        revision.ContentHash,
		CreatedBy:          revision.CreatedBy,
		CreatedByRunID:     revision.CreatedByRunID,
		CreatedAt:          revision.CreatedAt,
	}
}

func toUserStoryView(story version.UserStory) RequirementUserStoryView {
	return RequirementUserStoryView{AsA: story.AsA, IWant: story.IWant, SoThat: story.SoThat}
}

// toAcceptanceCriterionViews projects the criteria in the order the revision
// stores them. The ids are already canonical and unique, so a caller that wants
// the AC set in hash order can sort by id itself (version.ACIDs does).
func toAcceptanceCriterionViews(criteria []version.AcceptanceCriterion) []RequirementAcceptanceCriterionView {
	views := make([]RequirementAcceptanceCriterionView, 0, len(criteria))
	for _, criterion := range criteria {
		views = append(views, RequirementAcceptanceCriterionView{
			ID:    criterion.ID,
			Given: criterion.Given,
			When:  criterion.When,
			Then:  criterion.Then,
		})
	}
	return views
}

// toAcceptanceCriteria maps the caller-supplied criteria onto the domain shape.
// Ids are left empty on purpose: AssignACIDs is the only place that hands one
// out, and it needs the previous revision to do it.
func toAcceptanceCriteria(inputs []RequirementAcceptanceCriterionInput) []version.AcceptanceCriterion {
	criteria := make([]version.AcceptanceCriterion, 0, len(inputs))
	for _, input := range inputs {
		criteria = append(criteria, version.AcceptanceCriterion{
			Given: input.Given,
			When:  input.When,
			Then:  input.Then,
		})
	}
	return criteria
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
