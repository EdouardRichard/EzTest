package versionapi

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// ---------- response DTOs (OpenAPI P1 Requirement* schemas) ----------

type requirementTargetsDTO struct {
	VersionSUTs []string `json:"version_suts"`
	Pages       []string `json:"pages"`
	APIs        []string `json:"apis"`
}

type requirementSummaryDTO struct {
	ID               string                 `json:"id"`
	CurrentVersionID string                 `json:"current_version_id"`
	Revision         int                    `json:"revision"`
	Title            string                 `json:"title"`
	Status           string                 `json:"status"`
	Priority         string                 `json:"priority"`
	Targets          requirementTargetsDTO  `json:"targets"`
	UpdatedAt        string                 `json:"updated_at"`
	ConfirmedAt      *string                `json:"confirmed_at"`
	ClosedAt         *string                `json:"closed_at"`
	Closure          *requirementClosureDTO `json:"closure"`
}

// requirementClosureDTO is the P4 latest-closure projection (ClosureSummary).
type requirementClosureDTO struct {
	ClosureID   string `json:"closure_id"`
	CloseRound  int    `json:"close_round"`
	ClosedAt    string `json:"closed_at"`
	ReopenCount int    `json:"reopen_count"`
}

type requirementVersionDTO struct {
	ID                 string          `json:"id"`
	RequirementID      string          `json:"requirement_id"`
	Revision           int             `json:"revision"`
	Title              string          `json:"title"`
	Description        string          `json:"description"`
	AcceptanceCriteria string          `json:"acceptance_criteria"`
	Priority           string          `json:"priority"`
	Constraints        json.RawMessage `json:"constraints"`
	TargetVersionSUTs  []string        `json:"target_version_suts"`
	TargetPages        []string        `json:"target_pages"`
	TargetAPIs         []string        `json:"target_apis"`
	ContentHash        string          `json:"content_hash"`
	CreatedAt          string          `json:"created_at"`
}

type requirementConfirmationDTO struct {
	ID                     string   `json:"id"`
	RequirementVersionID   string   `json:"requirement_version_id"`
	Scope                  string   `json:"scope"`
	BatchID                *string  `json:"batch_id"`
	ActorID                string   `json:"actor_id"`
	ConfirmedAt            string   `json:"confirmed_at"`
	AffectedRequirementIDs []string `json:"affected_requirement_ids"`
}

type requirementRelationDTO struct {
	ID                string `json:"id"`
	FromRequirementID string `json:"from_requirement_id"`
	ToRequirementID   string `json:"to_requirement_id"`
	Kind              string `json:"kind"`
	Description       string `json:"description"`
	CreatedAt         string `json:"created_at"`
}

type attachmentErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type attachmentDTO struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	MIME             string              `json:"mime"`
	Size             int64               `json:"size"`
	ExtractionStatus string              `json:"extraction_status"`
	ExtractionError  *attachmentErrorDTO `json:"extraction_error"`
	CreatedAt        string              `json:"created_at"`
}

type requirementDetailDTO struct {
	requirementSummaryDTO
	CurrentVersion requirementVersionDTO        `json:"current_version"`
	History        []requirementVersionDTO      `json:"history"`
	Confirmations  []requirementConfirmationDTO `json:"confirmations"`
	Relations      []requirementRelationDTO     `json:"relations"`
	Attachments    []attachmentDTO              `json:"attachments"`
}

type confirmRequirementDTO struct {
	Confirmed   bool                 `json:"confirmed"`
	Requirement requirementDetailDTO `json:"requirement"`
}

type batchConfirmDTO struct {
	BatchID   string   `json:"batch_id"`
	Confirmed []string `json:"confirmed"`
	Skipped   []string `json:"skipped"`
}

// ---------- request payloads ----------

type requirementTargetsInput struct {
	VersionSUTs []string `json:"version_suts"`
	Pages       []string `json:"pages"`
	APIs        []string `json:"apis"`
}

type createRequirementRequest struct {
	Title              string                   `json:"title"`
	Description        string                   `json:"description"`
	AcceptanceCriteria string                   `json:"acceptance_criteria"`
	Priority           string                   `json:"priority"`
	Constraints        json.RawMessage          `json:"constraints"`
	Targets            *requirementTargetsInput `json:"targets"`
}

type updateRequirementRequest struct {
	Title              string                   `json:"title"`
	Description        string                   `json:"description"`
	AcceptanceCriteria string                   `json:"acceptance_criteria"`
	Priority           string                   `json:"priority"`
	Constraints        json.RawMessage          `json:"constraints"`
	Targets            *requirementTargetsInput `json:"targets"`
	Reason             string                   `json:"reason"`
}

type batchConfirmRequest struct {
	RequirementIDs []string `json:"requirement_ids"`
}

type rollbackRequirementRequest struct {
	Reason            string  `json:"reason"`
	ProposalMessageID *string `json:"proposal_message_id"`
}

// reopenRequirementRequest is the P4 reopen payload; reason is required
// (openapi RequirementReopenRequest, 1..2000).
type reopenRequirementRequest struct {
	Reason string `json:"reason"`
}

// draft converts the request body to the application command.
func (r createRequirementRequest) draft() versionapp.RequirementDraft {
	return versionapp.RequirementDraft{
		Title:              r.Title,
		Description:        r.Description,
		AcceptanceCriteria: r.AcceptanceCriteria,
		Priority:           r.Priority,
		Constraints:        r.Constraints,
		TargetVersionSUTs:  targetValues(r.Targets, func(t *requirementTargetsInput) []string { return t.VersionSUTs }),
		TargetPages:        targetValues(r.Targets, func(t *requirementTargetsInput) []string { return t.Pages }),
		TargetAPIs:         targetValues(r.Targets, func(t *requirementTargetsInput) []string { return t.APIs }),
	}
}

func (r updateRequirementRequest) draft() versionapp.RequirementDraft {
	return versionapp.RequirementDraft{
		Title:              r.Title,
		Description:        r.Description,
		AcceptanceCriteria: r.AcceptanceCriteria,
		Priority:           r.Priority,
		Constraints:        r.Constraints,
		TargetVersionSUTs:  targetValues(r.Targets, func(t *requirementTargetsInput) []string { return t.VersionSUTs }),
		TargetPages:        targetValues(r.Targets, func(t *requirementTargetsInput) []string { return t.Pages }),
		TargetAPIs:         targetValues(r.Targets, func(t *requirementTargetsInput) []string { return t.APIs }),
	}
}

func targetValues(targets *requirementTargetsInput, pick func(*requirementTargetsInput) []string) []string {
	if targets == nil {
		return []string{}
	}
	values := pick(targets)
	if values == nil {
		return []string{}
	}
	return values
}

// parseBatchIDs parses the decimal string IDs of the batch request.
func (r batchConfirmRequest) ids() ([]int64, error) {
	ids := make([]int64, 0, len(r.RequirementIDs))
	for index, raw := range r.RequirementIDs {
		id, err := validation.NonEmptyID("requirement_ids["+strconv.Itoa(index)+"]", raw)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ---------- view -> DTO conversions ----------

func toRequirementTargetsDTO(versionSUTs, pages, apis []string) requirementTargetsDTO {
	return requirementTargetsDTO{
		VersionSUTs: nonNil(versionSUTs),
		Pages:       nonNil(pages),
		APIs:        nonNil(apis),
	}
}

func toRequirementSummaryDTO(view versionapp.RequirementView) requirementSummaryDTO {
	return requirementSummaryDTO{
		ID:               strconv.FormatInt(view.ID, 10),
		CurrentVersionID: strconv.FormatInt(view.CurrentVersionID, 10),
		Revision:         view.Revision,
		Title:            view.Title,
		Status:           view.Status,
		Priority:         view.Priority,
		Targets:          toRequirementTargetsDTO(view.TargetVersionSUTs, view.TargetPages, view.TargetAPIs),
		UpdatedAt:        formatTime(view.UpdatedAt),
		ConfirmedAt:      formatOptionalTime(view.ConfirmedAt),
		ClosedAt:         formatOptionalTime(view.ClosedAt),
		Closure:          toRequirementClosureDTO(view.Closure),
	}
}

func toRequirementClosureDTO(view *versionapp.RequirementClosureView) *requirementClosureDTO {
	if view == nil {
		return nil
	}
	return &requirementClosureDTO{
		ClosureID:   strconv.FormatInt(view.ID, 10),
		CloseRound:  view.CloseRound,
		ClosedAt:    formatTime(view.ClosedAt),
		ReopenCount: view.ReopenCount,
	}
}

func toRequirementVersionDTO(view versionapp.RequirementVersionView) requirementVersionDTO {
	constraints := view.Constraints
	if len(constraints) == 0 {
		constraints = json.RawMessage(`{}`)
	}
	return requirementVersionDTO{
		ID:                 strconv.FormatInt(view.ID, 10),
		RequirementID:      strconv.FormatInt(view.RequirementID, 10),
		Revision:           view.Revision,
		Title:              view.Title,
		Description:        view.Description,
		AcceptanceCriteria: view.AcceptanceCriteria,
		Priority:           view.Priority,
		Constraints:        constraints,
		TargetVersionSUTs:  nonNil(view.TargetVersionSUTs),
		TargetPages:        nonNil(view.TargetPages),
		TargetAPIs:         nonNil(view.TargetAPIs),
		ContentHash:        view.ContentHash,
		CreatedAt:          formatTime(view.CreatedAt),
	}
}

func toRequirementConfirmationDTO(view versionapp.ConfirmationView) requirementConfirmationDTO {
	return requirementConfirmationDTO{
		ID:                     strconv.FormatInt(view.ID, 10),
		RequirementVersionID:   strconv.FormatInt(view.RequirementVersionID, 10),
		Scope:                  view.Scope,
		BatchID:                view.BatchID,
		ActorID:                strconv.FormatInt(view.ActorID, 10),
		ConfirmedAt:            formatTime(view.ConfirmedAt),
		AffectedRequirementIDs: nonNil(view.AffectedRequirementIDs),
	}
}

func toRequirementRelationDTO(view versionapp.RelationView) requirementRelationDTO {
	return requirementRelationDTO{
		ID:                strconv.FormatInt(view.ID, 10),
		FromRequirementID: strconv.FormatInt(view.FromRequirementID, 10),
		ToRequirementID:   strconv.FormatInt(view.ToRequirementID, 10),
		Kind:              view.Kind,
		Description:       view.Description,
		CreatedAt:         formatTime(view.CreatedAt),
	}
}

func toAttachmentDTO(view versionapp.AttachmentView) attachmentDTO {
	var extractionError *attachmentErrorDTO
	if view.ExtractionErrorCode != "" {
		extractionError = &attachmentErrorDTO{Code: view.ExtractionErrorCode, Message: view.ExtractionErrorText}
	}
	return attachmentDTO{
		ID:               strconv.FormatInt(view.ID, 10),
		Name:             view.Name,
		MIME:             view.MIME,
		Size:             view.Size,
		ExtractionStatus: view.ExtractionStatus,
		ExtractionError:  extractionError,
		CreatedAt:        formatTime(view.CreatedAt),
	}
}

func toRequirementDetailDTO(view versionapp.RequirementDetail) requirementDetailDTO {
	history := make([]requirementVersionDTO, 0, len(view.History))
	for _, revision := range view.History {
		history = append(history, toRequirementVersionDTO(revision))
	}
	confirmations := make([]requirementConfirmationDTO, 0, len(view.Confirmations))
	for _, confirmation := range view.Confirmations {
		confirmations = append(confirmations, toRequirementConfirmationDTO(confirmation))
	}
	relations := make([]requirementRelationDTO, 0, len(view.Relations))
	for _, relation := range view.Relations {
		relations = append(relations, toRequirementRelationDTO(relation))
	}
	attachments := make([]attachmentDTO, 0, len(view.Attachments))
	for _, attachment := range view.Attachments {
		attachments = append(attachments, toAttachmentDTO(attachment))
	}
	return requirementDetailDTO{
		requirementSummaryDTO: toRequirementSummaryDTO(view.RequirementView),
		CurrentVersion:        toRequirementVersionDTO(view.CurrentVersion),
		History:               history,
		Confirmations:         confirmations,
		Relations:             relations,
		Attachments:           attachments,
	}
}

func toConfirmRequirementDTO(view versionapp.ConfirmResult) confirmRequirementDTO {
	return confirmRequirementDTO{
		Confirmed:   view.Confirmed,
		Requirement: toRequirementDetailDTO(view.Requirement),
	}
}

func toBatchConfirmDTO(view versionapp.BatchConfirmResult) batchConfirmDTO {
	confirmed := make([]string, 0, len(view.Confirmed))
	for _, id := range view.Confirmed {
		confirmed = append(confirmed, strconv.FormatInt(id, 10))
	}
	skipped := make([]string, 0, len(view.Skipped))
	for _, id := range view.Skipped {
		skipped = append(skipped, strconv.FormatInt(id, 10))
	}
	return batchConfirmDTO{BatchID: view.BatchID, Confirmed: confirmed, Skipped: skipped}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}
