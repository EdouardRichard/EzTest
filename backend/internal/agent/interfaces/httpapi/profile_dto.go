package agentapi

import (
	"encoding/json"
	"strconv"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

/* ------------------------------- profiles ------------------------------- */

type profileViewDTO struct {
	ID            string            `json:"id"`
	Key           string            `json:"key"`
	Name          string            `json:"name"`
	SystemPrompt  string            `json:"system_prompt"`
	AllowedTools  []string          `json:"allowed_tools"`
	Model         *string           `json:"model"`
	Budgets       profileBudgetsDTO `json:"budgets"`
	Enabled       bool              `json:"enabled"`
	PromptVersion int               `json:"prompt_version"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
}

type profileBudgetsDTO struct {
	MaxTurns    int `json:"max_turns"`
	SoftSeconds int `json:"soft_seconds"`
	HardSeconds int `json:"hard_seconds"`
}

type profileDetailDTO struct {
	Profile             profileViewDTO         `json:"profile"`
	DefaultSystemPrompt string                 `json:"default_system_prompt"`
	Variables           []agent.PromptVariable `json:"variables"`
	PromptVersion       int                    `json:"prompt_version"`
}

type promptMutationDTO struct {
	Profile profileViewDTO `json:"profile"`
	Version int            `json:"version"`
	Changed bool           `json:"changed"`
}

type promptRevisionDTO struct {
	Version      int     `json:"version"`
	SystemPrompt string  `json:"system_prompt"`
	Note         *string `json:"note"`
	Source       string  `json:"source"`
	ActorID      *string `json:"actor_id"`
	CreatedAt    string  `json:"created_at"`
}

type agentToolDTO struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func toProfileViewDTO(view agentapp.ProfileView) profileViewDTO {
	tools := view.AllowedTools
	if tools == nil {
		tools = []string{}
	}
	return profileViewDTO{
		ID:            strconv.FormatInt(view.ID, 10),
		Key:           view.Key,
		Name:          view.Name,
		SystemPrompt:  view.SystemPrompt,
		AllowedTools:  tools,
		Model:         view.Model,
		Budgets:       toProfileBudgetsDTO(view.Budgets),
		Enabled:       view.Enabled,
		PromptVersion: view.PromptVersion,
		CreatedAt:     formatTime(view.CreatedAt),
		UpdatedAt:     formatTime(view.UpdatedAt),
	}
}

func toProfileDetailDTO(detail agentapp.ProfileDetail) profileDetailDTO {
	variables := detail.Variables
	if variables == nil {
		variables = []agent.PromptVariable{}
	}
	return profileDetailDTO{
		Profile:             toProfileViewDTO(detail.Profile),
		DefaultSystemPrompt: detail.DefaultSystemPrompt,
		Variables:           variables,
		PromptVersion:       detail.PromptVersion,
	}
}

func toPromptMutationDTO(view agentapp.ProfileView, version int, changed bool) promptMutationDTO {
	return promptMutationDTO{Profile: toProfileViewDTO(view), Version: version, Changed: changed}
}

func toPromptRevisionDTO(revision agent.PromptRevision) promptRevisionDTO {
	dto := promptRevisionDTO{
		Version:      revision.Version,
		SystemPrompt: revision.SystemPrompt,
		Source:       revision.Source,
		CreatedAt:    formatTime(revision.CreatedAt),
	}
	if revision.Note != "" {
		note := revision.Note
		dto.Note = &note
	}
	if revision.ActorID != nil {
		actorID := strconv.FormatInt(*revision.ActorID, 10)
		dto.ActorID = &actorID
	}
	return dto
}

func toProfileBudgetsDTO(budgets agent.ProfileBudgets) profileBudgetsDTO {
	return profileBudgetsDTO{
		MaxTurns:    budgets.MaxTurns,
		SoftSeconds: budgets.SoftSeconds,
		HardSeconds: budgets.HardSeconds,
	}
}

/* ------------------------------- requests ------------------------------- */

type profilePatchRequest struct {
	Model        json.RawMessage `json:"model"`
	Budgets      *budgetRequest  `json:"budgets"`
	AllowedTools *[]string       `json:"allowed_tools"`
}

type budgetRequest struct {
	MaxTurns    int `json:"max_turns"`
	SoftSeconds int `json:"soft_seconds"`
	HardSeconds int `json:"hard_seconds"`
}

func (r budgetRequest) toBudgets() agent.ProfileBudgets {
	return agent.ProfileBudgets{
		MaxTurns:    r.MaxTurns,
		SoftSeconds: r.SoftSeconds,
		HardSeconds: r.HardSeconds,
	}
}

type promptSaveRequest struct {
	SystemPrompt    *string `json:"system_prompt"`
	Note            string  `json:"note"`
	ExpectedVersion *int    `json:"expected_version"`
}

type promptRestoreRequest struct {
	Version *int `json:"version"`
}

type promptPreviewRequest struct {
	SystemPrompt string            `json:"system_prompt"`
	Sample       map[string]string `json:"sample"`
}

type promptPreviewResponse struct {
	Rendered string `json:"rendered"`
}

/* ------------------------------ patch root ------------------------------ */

// profilePatchResponse is the only contract response whose success body has a
// `changed` sibling next to `data` (ProfilePatchResponse schema).
type profilePatchResponse struct {
	Data    profileViewDTO `json:"data"`
	Changed bool           `json:"changed"`
}
