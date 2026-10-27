package httpapi

import (
	"fmt"
	"strings"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// versionResourceDTO is one resolved P2 binding target. Its id stays a number
// (P2 owns it); available is false when the resource was deleted after binding.
type versionResourceDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// versionBindingsDTO is the resolved binding view; unbound entries are null.
type versionBindingsDTO struct {
	System         *versionResourceDTO `json:"system"`
	Environment    *versionResourceDTO `json:"environment"`
	CodeRepository *versionResourceDTO `json:"codeRepository"`
}

// versionDTO is the VersionDTO of spec §7.2. The P3-owned id is a decimal JSON
// string; the P2 resource ids and the user ids stay numbers.
type versionDTO struct {
	ID                 int64              `json:"id,string"`
	Name               string             `json:"name"`
	Label              string             `json:"label"`
	Description        string             `json:"description"`
	Status             string             `json:"status"`
	SystemID           *int64             `json:"systemId"`
	EnvironmentID      *int64             `json:"environmentId"`
	CodeRepositoryID   *int64             `json:"codeRepositoryId"`
	CodeBranch         *string            `json:"codeBranch"`
	CodeBaselineCommit *string            `json:"codeBaselineCommit"`
	Bindings           versionBindingsDTO `json:"bindings"`
	RequirementCount   int                `json:"requirementCount"`
	ClarifiedCount     int                `json:"clarifiedCount"`
	OpenQuestionCount  int                `json:"openQuestionCount"`
	Revision           int                `json:"revision"`
	CreatedBy          int64              `json:"createdBy"`
	UpdatedBy          int64              `json:"updatedBy"`
	CreatedAt          time.Time          `json:"createdAt"`
	UpdatedAt          time.Time          `json:"updatedAt"`
}

// newVersionDTO maps a version aggregate, its aggregate counters and its
// resolved bindings to the API shape.
func newVersionDTO(version *versionapp.Version, counts versionCountsView, bindings versionBindingsDTO) versionDTO {
	return versionDTO{
		ID:                 version.ID,
		Name:               version.Name,
		Label:              version.Label,
		Description:        version.Description,
		Status:             version.Status,
		SystemID:           version.SystemID,
		EnvironmentID:      version.EnvironmentID,
		CodeRepositoryID:   version.CodeRepositoryID,
		CodeBranch:         version.CodeBranch,
		CodeBaselineCommit: version.CodeBaselineCommit,
		Bindings:           bindings,
		RequirementCount:   counts.requirementCount,
		ClarifiedCount:     counts.clarifiedCount,
		OpenQuestionCount:  counts.openQuestionCount,
		Revision:           version.Revision,
		CreatedBy:          version.CreatedBy,
		UpdatedBy:          version.UpdatedBy,
		CreatedAt:          version.CreatedAt,
		UpdatedAt:          version.UpdatedAt,
	}
}

// versionCountsView is the handler-local projection of the version counters so
// dto.go does not depend on the domain package for one struct.
type versionCountsView struct {
	requirementCount  int
	clarifiedCount    int
	openQuestionCount int
}

// createVersionRequest is the POST /api/versions body (spec §7.2). P2 resource
// ids are numbers.
type createVersionRequest struct {
	Name               string  `json:"name"`
	Label              string  `json:"label"`
	Description        string  `json:"description"`
	SystemID           *int64  `json:"systemId"`
	EnvironmentID      *int64  `json:"environmentId"`
	CodeRepositoryID   *int64  `json:"codeRepositoryId"`
	CodeBranch         *string `json:"codeBranch"`
	CodeBaselineCommit *string `json:"codeBaselineCommit"`
}

// updateVersionRequest is the PATCH /api/versions/{id} body: revision is
// required, the metadata fields are optional and keep their stored value when
// omitted.
type updateVersionRequest struct {
	Revision    int     `json:"revision"`
	Name        *string `json:"name"`
	Label       *string `json:"label"`
	Description *string `json:"description"`
}

// putVersionBindingsRequest replaces the whole binding set; an omitted or null
// field clears that binding.
type putVersionBindingsRequest struct {
	Revision           int     `json:"revision"`
	SystemID           *int64  `json:"systemId"`
	EnvironmentID      *int64  `json:"environmentId"`
	CodeRepositoryID   *int64  `json:"codeRepositoryId"`
	CodeBranch         *string `json:"codeBranch"`
	CodeBaselineCommit *string `json:"codeBaselineCommit"`
}

// enterDesignRequest is the optional POST enter-design body.
type enterDesignRequest struct {
	AcknowledgeUnclarified bool `json:"acknowledgeUnclarified"`
}

// enterDesignResultDTO is the enter-design success body (spec §7.2): the
// version plus the flattened human-readable warnings. Warnings is always an
// array, empty when nothing is left to clarify.
type enterDesignResultDTO struct {
	Version  versionDTO `json:"version"`
	Warnings []string   `json:"warnings"`
}

// warningMessages flattens the structured enter-design warnings into the string
// array the frontend renders (lead ruling 2), for example
// "3 条需求未澄清 / 2 个待澄清问题".
func warningMessages(warnings versionapp.Warnings) []string {
	clauses := make([]string, 0, 2)
	if count := len(warnings.UnclarifiedRequirementIDs); count > 0 {
		clauses = append(clauses, fmt.Sprintf("%d 条需求未澄清", count))
	}
	if warnings.OpenQuestionCount > 0 {
		clauses = append(clauses, fmt.Sprintf("%d 个待澄清问题", warnings.OpenQuestionCount))
	}
	if len(clauses) == 0 {
		return []string{}
	}
	return []string{strings.Join(clauses, " / ")}
}

// requirementDTO is the RequirementDTO of spec §7.3. Every P3-owned id is a
// decimal JSON string; user ids stay numbers.
type requirementDTO struct {
	ID                 int64            `json:"id,string"`
	VersionID          int64            `json:"versionId,string"`
	Title              string           `json:"title"`
	Description        string           `json:"description"`
	AcceptanceCriteria string           `json:"acceptanceCriteria"`
	Priority           string           `json:"priority"`
	Status             string           `json:"status"`
	Source             string           `json:"source"`
	LastUpdateSource   string           `json:"lastUpdateSource"`
	Revision           int              `json:"revision"`
	ConfirmedAt        *time.Time       `json:"confirmedAt"`
	ConfirmedBy        *int64           `json:"confirmedBy"`
	OpenQuestionCount  int              `json:"openQuestionCount"`
	RelationCount      int              `json:"relationCount"`
	AttachmentCount    int              `json:"attachmentCount"`
	Attachments        *[]attachmentDTO `json:"attachments,omitempty"`
	CreatedBy          int64            `json:"createdBy"`
	UpdatedBy          int64            `json:"updatedBy"`
	CreatedAt          time.Time        `json:"createdAt"`
	UpdatedAt          time.Time        `json:"updatedAt"`
}

// newRequirementDTO maps a requirement aggregate and its counters to the API
// shape. attachments stays nil (omitted) on list/create/action responses and is
// set to a non-nil array on the requirement detail.
func newRequirementDTO(requirement *versionapp.Requirement, openQuestionCount, relationCount, attachmentCount int, attachments []attachmentDTO, includeAttachments bool) requirementDTO {
	dto := requirementDTO{
		ID:                 requirement.ID,
		VersionID:          requirement.VersionID,
		Title:              requirement.Title,
		Description:        requirement.Description,
		AcceptanceCriteria: requirement.AcceptanceCriteria,
		Priority:           requirement.Priority,
		Status:             requirement.Status,
		Source:             requirement.Source,
		LastUpdateSource:   requirement.LastUpdateSource,
		Revision:           requirement.Revision,
		ConfirmedAt:        requirement.ConfirmedAt,
		ConfirmedBy:        requirement.ConfirmedBy,
		OpenQuestionCount:  openQuestionCount,
		RelationCount:      relationCount,
		AttachmentCount:    attachmentCount,
		CreatedBy:          requirement.CreatedBy,
		UpdatedBy:          requirement.UpdatedBy,
		CreatedAt:          requirement.CreatedAt,
		UpdatedAt:          requirement.UpdatedAt,
	}
	if includeAttachments {
		list := attachments
		if list == nil {
			list = []attachmentDTO{}
		}
		dto.Attachments = &list
	}
	return dto
}

// createRequirementRequest is the POST requirement body (spec §7.3).
type createRequirementRequest struct {
	Title              string `json:"title"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptanceCriteria"`
	Priority           string `json:"priority"`
}

// updateRequirementRequest is the PATCH requirement body: revision is required
// and every editable field is optional.
type updateRequirementRequest struct {
	Revision           int     `json:"revision"`
	Title              *string `json:"title"`
	Description        *string `json:"description"`
	AcceptanceCriteria *string `json:"acceptanceCriteria"`
	Priority           *string `json:"priority"`
}

// relationDTO is the RelationDTO of spec §7.4. Both endpoint ids are P3
// decimal strings.
type relationDTO struct {
	ID                int64     `json:"id,string"`
	VersionID         int64     `json:"versionId,string"`
	FromRequirementID int64     `json:"fromRequirementId,string"`
	ToRequirementID   int64     `json:"toRequirementId,string"`
	RelationType      string    `json:"relationType"`
	Description       string    `json:"description"`
	Source            string    `json:"source"`
	CreatedBy         int64     `json:"createdBy"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// newRelationDTO maps one relation aggregate to the API shape.
func newRelationDTO(relation *versionapp.Relation) relationDTO {
	return relationDTO{
		ID:                relation.ID,
		VersionID:         relation.VersionID,
		FromRequirementID: relation.FromRequirementID,
		ToRequirementID:   relation.ToRequirementID,
		RelationType:      relation.RelationType,
		Description:       relation.Description,
		Source:            relation.Source,
		CreatedBy:         relation.CreatedBy,
		CreatedAt:         relation.CreatedAt,
		UpdatedAt:         relation.UpdatedAt,
	}
}

// newRelationDTOs maps a relation slice to a non-nil DTO slice.
func newRelationDTOs(relations []versionapp.Relation) []relationDTO {
	items := make([]relationDTO, 0, len(relations))
	for i := range relations {
		items = append(items, newRelationDTO(&relations[i]))
	}
	return items
}

// createRelationRequest is the POST relation body; the endpoint ids are decimal
// strings, so they are decoded with the ,string JSON tag.
type createRelationRequest struct {
	FromRequirementID int64  `json:"fromRequirementId,string"`
	ToRequirementID   int64  `json:"toRequirementId,string"`
	RelationType      string `json:"relationType"`
	Description       string `json:"description"`
}

// updateRelationRequest is the relation PATCH body. Relations have no revision;
// pointer fields keep true PATCH semantics so an omitted field stays nil while
// an explicit empty relationType reaches the service and is rejected.
type updateRelationRequest struct {
	RelationType *string `json:"relationType"`
	Description  *string `json:"description"`
}

// attachmentDTO is the attachment metadata shape of spec §7.3: no storagePath
// is ever returned (the bytes stay behind the authenticated download route).
type attachmentDTO struct {
	ID        int64     `json:"id,string"`
	Kind      string    `json:"kind"`
	Filename  string    `json:"filename"`
	MIMEType  string    `json:"mimeType"`
	SizeBytes int64     `json:"sizeBytes"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
}

// newAttachmentDTO maps one attachment aggregate to its metadata shape.
func newAttachmentDTO(attachment *versionapp.Attachment) attachmentDTO {
	return attachmentDTO{
		ID:        attachment.ID,
		Kind:      attachment.Kind,
		Filename:  attachment.Filename,
		MIMEType:  attachment.MIMEType,
		SizeBytes: attachment.SizeBytes,
		SHA256:    attachment.SHA256,
		CreatedAt: attachment.CreatedAt,
	}
}

// newAttachmentDTOs maps an attachment slice to a non-nil DTO slice.
func newAttachmentDTOs(attachments []versionapp.Attachment) []attachmentDTO {
	items := make([]attachmentDTO, 0, len(attachments))
	for i := range attachments {
		items = append(items, newAttachmentDTO(&attachments[i]))
	}
	return items
}
