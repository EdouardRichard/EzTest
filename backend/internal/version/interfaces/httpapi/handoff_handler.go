package versionapi

// Task 27 (v0.22 B §8.1, spec R-B5-1): the version handoff read route.
//
//	GET /api/versions/{id}/handoff[?revision=]
//
// The handler only parses the path/query and renders the stored handoff
// document: the six-block content, revision and generated_at are owned by
// *versionapp.HandoffService. Route registration and the OpenAPI schema are
// wired by the server task (Task 37). A version without a stored handoff (or
// without the requested revision) is a 404.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// HandoffService is the version handoff surface the read handler needs.
// *versionapp.HandoffService implements it. Generate is part of the interface so
// the composition root can use the same value for the stage-machine wiring
// (Task 25/36); the read handler only calls Latest.
type HandoffService interface {
	Generate(ctx context.Context, versionID int64) (versionapp.Handoff, error)
	Latest(ctx context.Context, versionID int64, revision int) (versionapp.Handoff, error)
}

var _ HandoffService = (*versionapp.HandoffService)(nil)

// HandoffHandler serves the handoff read route. It is mounted behind the shared
// bearer-authentication middleware by the server task.
type HandoffHandler struct {
	svc HandoffService
}

// NewHandoffHandler builds the handoff handler.
func NewHandoffHandler(svc HandoffService) *HandoffHandler {
	return &HandoffHandler{svc: svc}
}

// Get GET /api/versions/{id}/handoff. Without ?revision= the newest revision is
// returned; an unknown version/revision is a 404. The response is the stored
// document with decimal-string IDs and RFC3339 timestamps.
func (h *HandoffHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	revision, err := parseHandoffRevision(r.URL.Query().Get("revision"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.svc == nil {
		httpx.Error(w, r, platform.Unavailable("clarification handoff service is not configured"))
		return
	}
	document, err := h.svc.Latest(r.Context(), versionID, revision)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toHandoffDTO(document))
}

// parseHandoffRevision parses the optional revision query. Empty means latest
// (0); a non-numeric, zero or negative value is a field-level 400.
func parseHandoffRevision(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	revision, err := strconv.Atoi(trimmed)
	if err != nil || revision <= 0 {
		return 0, platform.Invalid("revision", "must be a positive integer")
	}
	return revision, nil
}

// ---------- DTOs ----------

type handoffDTO struct {
	VersionID   string `json:"version_id"`
	Revision    int    `json:"revision"`
	GeneratedAt string `json:"generated_at"`

	Requirements []handoffRequirementDTO `json:"requirements"`
	QA           []handoffQADTO          `json:"qa"`
	Decisions    []handoffDecisionDTO    `json:"decisions"`
	Open         []handoffOpenDTO        `json:"open"`
	Plan         handoffPlanDTO          `json:"plan"`
	Resources    handoffResourcesDTO     `json:"resources"`
}

type handoffRequirementDTO struct {
	ID                 string   `json:"id"`
	CurrentVersionID   string   `json:"current_version_id"`
	Revision           int      `json:"revision"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptance_criteria"`
	Priority           string   `json:"priority"`
	Status             string   `json:"status"`
	SourceQuote        string   `json:"source_quote"`
	Confidence         *float64 `json:"confidence,omitempty"`
	ConfirmedAt        *string  `json:"confirmed_at,omitempty"`
}

type handoffQAOptionDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type handoffQADTO struct {
	RequestID       string               `json:"question_id"`
	MessageID       string               `json:"message_id,omitempty"`
	ConversationID  string               `json:"conversation_id,omitempty"`
	QuestionKey     string               `json:"question_key,omitempty"`
	Question        string               `json:"question"`
	Options         []handoffQAOptionDTO `json:"options"`
	SourceRefs      []string             `json:"source_refs"`
	RequirementID   string               `json:"requirement_id,omitempty"`
	RequirementRefs []string             `json:"requirement_refs"`
	Status          string               `json:"status"`
	AnswerSelected  []string             `json:"answer_selected"`
	AnswerText      string               `json:"answer_text"`
	AskedAt         string               `json:"asked_at,omitempty"`
	AnsweredAt      *string              `json:"answered_at,omitempty"`
}

type handoffDecisionDTO struct {
	FactID     string   `json:"fact_id"`
	Kind       string   `json:"kind"`
	Key        string   `json:"key"`
	Summary    string   `json:"summary"`
	Confidence *float64 `json:"confidence,omitempty"`
	Source     string   `json:"source"`
	SourceRef  string   `json:"source_ref,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
}

type handoffOpenDTO struct {
	Kind        string  `json:"kind"`
	Summary     string  `json:"summary"`
	Status      string  `json:"status"`
	Source      string  `json:"source"`
	ReferenceID string  `json:"reference_id,omitempty"`
	CreatedAt   *string `json:"created_at,omitempty"`
}

type handoffPlanItemDTO struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Decision string `json:"decision"`
}

type handoffPlanDTO struct {
	Stage   string              `json:"stage"`
	Current *handoffPlanItemDTO `json:"current"`
	Next    *handoffPlanItemDTO `json:"next"`
}

type handoffSUTDTO struct {
	VersionSUTID  string `json:"version_sut_id"`
	SUTID         string `json:"sut_id"`
	Name          string `json:"name"`
	EnvironmentID string `json:"environment_id"`
	Environment   string `json:"environment"`
	BaseURL       string `json:"base_url"`
}

type handoffPageDTO struct {
	ID            string `json:"id"`
	VersionSUTID  string `json:"version_sut_id,omitempty"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	RouteTemplate string `json:"route_template,omitempty"`
}

type handoffAPIDTO struct {
	ID           string `json:"id"`
	VersionSUTID string `json:"version_sut_id,omitempty"`
	Name         string `json:"name"`
	Method       string `json:"method"`
	Path         string `json:"path"`
}

type handoffAccountDTO struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	Role     string   `json:"role,omitempty"`
}

type handoffDatabaseDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	ReadOnly bool   `json:"read_only"`
}

type handoffRepositoryDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role,omitempty"`
	Branch string `json:"branch,omitempty"`
	Source string `json:"source"`
}

type handoffResourcesDTO struct {
	SUTs         []handoffSUTDTO        `json:"suts"`
	Pages        []handoffPageDTO       `json:"pages"`
	APIs         []handoffAPIDTO        `json:"apis"`
	Accounts     []handoffAccountDTO    `json:"accounts"`
	Databases    []handoffDatabaseDTO   `json:"databases"`
	Repositories []handoffRepositoryDTO `json:"repositories"`
}

func toHandoffDTO(document versionapp.Handoff) handoffDTO {
	dto := handoffDTO{
		VersionID:    formatDecimal(document.VersionID),
		Revision:     document.Revision,
		GeneratedAt:  formatTime(document.GeneratedAt),
		Requirements: make([]handoffRequirementDTO, 0, len(document.Requirements)),
		QA:           make([]handoffQADTO, 0, len(document.QA)),
		Decisions:    make([]handoffDecisionDTO, 0, len(document.Decisions)),
		Open:         make([]handoffOpenDTO, 0, len(document.Open)),
		Plan:         toHandoffPlanDTO(document.Plan),
		Resources:    toHandoffResourcesDTO(document.Resources),
	}
	for _, item := range document.Requirements {
		dto.Requirements = append(dto.Requirements, handoffRequirementDTO{
			ID:                 formatDecimal(item.ID),
			CurrentVersionID:   formatDecimal(item.CurrentVersionID),
			Revision:           item.Revision,
			Title:              item.Title,
			Description:        item.Description,
			AcceptanceCriteria: item.AcceptanceCriteria,
			Priority:           item.Priority,
			Status:             item.Status,
			SourceQuote:        item.SourceQuote,
			Confidence:         item.Confidence,
			ConfirmedAt:        formatOptionalTime(item.ConfirmedAt),
		})
	}
	for _, item := range document.QA {
		options := make([]handoffQAOptionDTO, 0, len(item.Options))
		for _, option := range item.Options {
			options = append(options, handoffQAOptionDTO{ID: option.ID, Label: option.Label})
		}
		dto.QA = append(dto.QA, handoffQADTO{
			RequestID:       formatDecimal(item.RequestID),
			MessageID:       formatOptionalID(item.MessageID),
			ConversationID:  formatOptionalID(item.ConversationID),
			QuestionKey:     item.QuestionKey,
			Question:        item.Question,
			Options:         options,
			SourceRefs:      nonNil(item.SourceRefs),
			RequirementID:   formatOptionalID(item.RequirementID),
			RequirementRefs: formatIDs(item.RequirementRefs),
			Status:          item.Status,
			AnswerSelected:  nonNil(item.AnswerSelected),
			AnswerText:      item.AnswerText,
			AskedAt:         formatTimeIfSet(item.AskedAt),
			AnsweredAt:      formatOptionalTime(item.AnsweredAt),
		})
	}
	for _, item := range document.Decisions {
		dto.Decisions = append(dto.Decisions, handoffDecisionDTO{
			FactID:     formatDecimal(item.FactID),
			Kind:       item.Kind,
			Key:        item.Key,
			Summary:    item.Summary,
			Confidence: item.Confidence,
			Source:     item.Source,
			SourceRef:  item.SourceRef,
			CreatedAt:  formatTimeIfSet(item.CreatedAt),
		})
	}
	for _, item := range document.Open {
		dto.Open = append(dto.Open, handoffOpenDTO{
			Kind:        item.Kind,
			Summary:     item.Summary,
			Status:      item.Status,
			Source:      item.Source,
			ReferenceID: formatOptionalID(item.ReferenceID),
			CreatedAt:   formatOptionalTime(item.CreatedAt),
		})
	}
	return dto
}

func toHandoffPlanDTO(plan versionapp.HandoffPlan) handoffPlanDTO {
	return handoffPlanDTO{
		Stage:   plan.Stage,
		Current: toHandoffPlanItemDTO(plan.Current),
		Next:    toHandoffPlanItemDTO(plan.Next),
	}
}

func toHandoffPlanItemDTO(item *versionapp.HandoffPlanItem) *handoffPlanItemDTO {
	if item == nil {
		return nil
	}
	return &handoffPlanItemDTO{
		ID:       formatDecimal(item.ID),
		Phase:    item.Phase,
		Title:    item.Title,
		Status:   item.Status,
		Decision: item.Decision,
	}
}

func toHandoffResourcesDTO(resources versionapp.HandoffResources) handoffResourcesDTO {
	dto := handoffResourcesDTO{
		SUTs:         make([]handoffSUTDTO, 0, len(resources.SUTs)),
		Pages:        make([]handoffPageDTO, 0, len(resources.Pages)),
		APIs:         make([]handoffAPIDTO, 0, len(resources.APIs)),
		Accounts:     make([]handoffAccountDTO, 0, len(resources.Accounts)),
		Databases:    make([]handoffDatabaseDTO, 0, len(resources.Databases)),
		Repositories: make([]handoffRepositoryDTO, 0, len(resources.Repositories)),
	}
	for _, item := range resources.SUTs {
		dto.SUTs = append(dto.SUTs, handoffSUTDTO{
			VersionSUTID:  formatDecimal(item.VersionSUTID),
			SUTID:         formatDecimal(item.SUTID),
			Name:          item.Name,
			EnvironmentID: formatDecimal(item.EnvironmentID),
			Environment:   item.Environment,
			BaseURL:       item.BaseURL,
		})
	}
	for _, item := range resources.Pages {
		dto.Pages = append(dto.Pages, handoffPageDTO{
			ID:            formatDecimal(item.ID),
			VersionSUTID:  formatOptionalID(item.VersionSUTID),
			Name:          item.Name,
			URL:           item.URL,
			RouteTemplate: item.RouteTemplate,
		})
	}
	for _, item := range resources.APIs {
		dto.APIs = append(dto.APIs, handoffAPIDTO{
			ID:           formatDecimal(item.ID),
			VersionSUTID: formatOptionalID(item.VersionSUTID),
			Name:         item.Name,
			Method:       item.Method,
			Path:         item.Path,
		})
	}
	for _, item := range resources.Accounts {
		dto.Accounts = append(dto.Accounts, handoffAccountDTO{
			ID:       formatDecimal(item.ID),
			Username: item.Username,
			Roles:    nonNil(item.Roles),
			Role:     item.Role,
		})
	}
	for _, item := range resources.Databases {
		dto.Databases = append(dto.Databases, handoffDatabaseDTO{
			ID:       formatDecimal(item.ID),
			Name:     item.Name,
			Kind:     item.Kind,
			ReadOnly: item.ReadOnly,
		})
	}
	for _, item := range resources.Repositories {
		dto.Repositories = append(dto.Repositories, handoffRepositoryDTO{
			ID:     formatDecimal(item.ID),
			Name:   item.Name,
			Role:   item.Role,
			Branch: item.Branch,
			Source: item.Source,
		})
	}
	return dto
}

// formatTimeIfSet renders a required-by-contract timestamp and leaves a zero
// time out of the JSON payload.
func formatTimeIfSet(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}

func formatOptionalID(value int64) string {
	if value <= 0 {
		return ""
	}
	return formatDecimal(value)
}
