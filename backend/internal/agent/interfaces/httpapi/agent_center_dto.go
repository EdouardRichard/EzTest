package agentapi

import (
	"strconv"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

/* -------------------------------- catalog -------------------------------- */

type agentCatalogDTO struct {
	Profiles []catalogProfileDTO `json:"profiles"`
	Teams    []agentTeamDTO      `json:"teams"`
}

type catalogProfileDTO struct {
	ProfileKey        string         `json:"profile_key"`
	Name              string         `json:"name"`
	Team              string         `json:"team"`
	Order             int            `json:"order"`
	Mode              string         `json:"mode"`
	Model             *string        `json:"model"`
	Enabled           bool           `json:"enabled"`
	Budgets           budgetDTO      `json:"budgets"`
	AllowedToolsCount int            `json:"allowed_tools_count"`
	PromptVersion     int            `json:"prompt_version"`
	PromptUpdatedAt   *string        `json:"prompt_updated_at"`
	UpdatedAt         string         `json:"updated_at"`
	Stats             agentRunStatsDTO `json:"stats"`
}

type agentTeamDTO struct {
	Team        string   `json:"team"`
	Label       string   `json:"label"`
	ProfileKeys []string `json:"profile_keys"`
	Note        string   `json:"note,omitempty"`
}

type budgetDTO struct {
	MaxTurns    int `json:"max_turns"`
	SoftSeconds int `json:"soft_seconds"`
	HardSeconds int `json:"hard_seconds"`
}

type agentRunStatsDTO struct {
	RunCount         int     `json:"run_count"`
	DoneCount        int     `json:"done_count"`
	FailedCount      int     `json:"failed_count"`
	AbortedCount     int     `json:"aborted_count"`
	InterruptedCount int     `json:"interrupted_count"`
	SuccessRate      float64 `json:"success_rate"`
	LastRunAt        *string `json:"last_run_at"`
}

func toAgentCatalogDTO(catalog agentapp.Catalog) agentCatalogDTO {
	profiles := make([]catalogProfileDTO, 0, len(catalog.Profiles))
	for _, profile := range catalog.Profiles {
		var promptUpdatedAt *string
		if profile.PromptUpdatedAt != nil {
			formatted := formatTime(*profile.PromptUpdatedAt)
			promptUpdatedAt = &formatted
		}
		profiles = append(profiles, catalogProfileDTO{
			ProfileKey:        profile.ProfileKey,
			Name:              profile.Name,
			Team:              profile.Team,
			Order:             profile.Order,
			Mode:              profile.Mode,
			Model:             profile.Model,
			Enabled:           profile.Enabled,
			Budgets:           toBudgetDTO(profile.Budgets),
			AllowedToolsCount: profile.AllowedToolsCount,
			PromptVersion:     profile.PromptVersion,
			PromptUpdatedAt:   promptUpdatedAt,
			UpdatedAt:         formatTime(profile.UpdatedAt),
			Stats:             toAgentRunStatsDTO(profile.Stats),
		})
	}
	teams := make([]agentTeamDTO, 0, len(catalog.Teams))
	for _, team := range catalog.Teams {
		keys := team.ProfileKeys
		if keys == nil {
			keys = []string{}
		}
		teams = append(teams, agentTeamDTO{
			Team:        team.Team,
			Label:       team.Label,
			ProfileKeys: keys,
			Note:        team.Note,
		})
	}
	return agentCatalogDTO{Profiles: profiles, Teams: teams}
}

/* ---------------------------------- runs --------------------------------- */

type agentRunDTO struct {
	ID             string         `json:"id"`
	ProfileKey     string         `json:"profile_key"`
	WorkItemID     *string        `json:"work_item_id"`
	WorkType       *string        `json:"work_type"`
	VersionID      *string        `json:"version_id"`
	VersionTitle   string         `json:"version_title,omitempty"`
	ConversationID *string        `json:"conversation_id"`
	Status         string         `json:"status"`
	TerminalReason *string        `json:"terminal_reason"`
	Turns          int            `json:"turns"`
	Usage          map[string]int `json:"usage"`
	StartedAt      *string        `json:"started_at"`
	FinishedAt     *string        `json:"finished_at"`
}

func toAgentRunDTO(view agentapp.RunView) agentRunDTO {
	usage := view.Usage
	if usage == nil {
		usage = map[string]int{}
	}
	return agentRunDTO{
		ID:             strconv.FormatInt(view.ID, 10),
		ProfileKey:     view.ProfileKey,
		WorkItemID:     formatIDPointer(view.WorkItemID),
		WorkType:       view.WorkType,
		VersionID:      formatIDPointer(view.VersionID),
		VersionTitle:   view.VersionTitle,
		ConversationID: formatIDPointer(view.ConversationID),
		Status:         view.Status,
		TerminalReason: view.TerminalReason,
		Turns:          view.Turns,
		Usage:          usage,
		StartedAt:      formatTimePointer(view.StartedAt),
		FinishedAt:     formatTimePointer(view.FinishedAt),
	}
}

/* -------------------------------- quality -------------------------------- */

type agentCenterQualityDTO struct {
	Latest   *evalSummaryDTO  `json:"latest"`
	RunStats agentRunStatsDTO `json:"run_stats"`
}

type evalSummaryDTO struct {
	ID            string `json:"id"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	MetricsCount  int    `json:"metrics_count"`
	MetricsPassed int    `json:"metrics_passed"`
}

func toAgentCenterQualityDTO(view agentapp.QualityView) agentCenterQualityDTO {
	var latest *evalSummaryDTO
	if view.Latest != nil {
		latest = &evalSummaryDTO{
			ID:            strconv.FormatInt(view.Latest.ID, 10),
			Mode:          view.Latest.Mode,
			Status:        view.Latest.Status,
			CreatedAt:     formatTime(view.Latest.CreatedAt),
			MetricsCount:  view.Latest.MetricsCount,
			MetricsPassed: view.Latest.MetricsPassed,
		}
	}
	return agentCenterQualityDTO{
		Latest:   latest,
		RunStats: toAgentRunStatsDTO(view.RunStats),
	}
}

/* -------------------------------- helpers -------------------------------- */

func toBudgetDTO(budgets agent.ProfileBudgets) budgetDTO {
	return budgetDTO{
		MaxTurns:    budgets.MaxTurns,
		SoftSeconds: budgets.SoftSeconds,
		HardSeconds: budgets.HardSeconds,
	}
}

func toAgentRunStatsDTO(stats agentapp.RunStats) agentRunStatsDTO {
	return agentRunStatsDTO{
		RunCount:         stats.RunCount,
		DoneCount:        stats.DoneCount,
		FailedCount:      stats.FailedCount,
		AbortedCount:     stats.AbortedCount,
		InterruptedCount: stats.InterruptedCount,
		SuccessRate:      stats.SuccessRate(),
		LastRunAt:        formatTimePointer(stats.LastRunAt),
	}
}

func formatIDPointer(value *int64) *string {
	if value == nil {
		return nil
	}
	formatted := strconv.FormatInt(*value, 10)
	return &formatted
}

func formatTimePointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}
