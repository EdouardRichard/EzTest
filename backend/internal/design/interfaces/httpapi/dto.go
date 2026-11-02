package designapi

import (
	"encoding/json"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ---------- graph ----------

type graphStatsDTO struct {
	Requirements int `json:"requirements"`
	Scenarios    int `json:"scenarios"`
	Cases        int `json:"cases"`
	Draft        int `json:"draft"`
	Confirmed    int `json:"confirmed"`
	Stale        int `json:"stale"`
}

type graphViewDTO struct {
	VersionID                 string                `json:"version_id"`
	GeneratedAt               string                `json:"generated_at"`
	GraphHash                 string                `json:"graph_hash"`
	PendingDesignCount        int                   `json:"pending_design_count"`
	AutoDesignNewRequirements bool                  `json:"auto_design_new_requirements"`
	Stats                     graphStatsDTO         `json:"stats"`
	Requirements              []requirementGraphDTO `json:"requirements"`
}

type requirementGraphDTO struct {
	RequirementID        string             `json:"requirement_id"`
	RequirementVersionID string             `json:"requirement_version_id"`
	Revision             int                `json:"revision"`
	Title                string             `json:"title"`
	Status               string             `json:"status"`
	DesignStatus         string             `json:"design_status"`
	TargetVersionSUTIDs  []string           `json:"target_version_sut_ids"`
	Scenarios            []scenarioGraphDTO `json:"scenarios"`
}

type scenarioGraphDTO struct {
	ID                   string        `json:"id"`
	RequirementVersionID string        `json:"requirement_version_id"`
	Name                 string        `json:"name"`
	Description          string        `json:"description"`
	VersionSUTIDs        []string      `json:"version_sut_ids"`
	PageIDs              []string      `json:"page_ids"`
	APIIDs               []string      `json:"api_ids"`
	Sort                 int           `json:"sort"`
	Cases                []caseCardDTO `json:"cases"`
}

type caseCardDTO struct {
	CaseID              string   `json:"case_id"`
	CurrentVersionID    string   `json:"current_version_id"`
	Revision            int      `json:"revision"`
	Title               string   `json:"title"`
	Priority            string   `json:"priority"`
	Status              string   `json:"status"`
	DefaultVersionSUTID *string  `json:"default_version_sut_id"`
	VersionSUTIDs       []string `json:"version_sut_ids"`
	PageIDs             []string `json:"page_ids"`
	APIIDs              []string `json:"api_ids"`
	StepCount           int      `json:"step_count"`
	AssertionCount      int      `json:"assertion_count"`
	CreatedAt           string   `json:"created_at"`
}

func toGraphDTO(view designapp.GraphView) graphViewDTO {
	dto := graphViewDTO{
		VersionID:                 platformid.Format(view.VersionID),
		GeneratedAt:               formatTime(view.GeneratedAt),
		GraphHash:                 view.GraphHash,
		PendingDesignCount:        view.PendingDesignCount,
		AutoDesignNewRequirements: view.AutoDesignNewRequirements,
		Stats: graphStatsDTO{
			Requirements: view.Stats.Requirements, Scenarios: view.Stats.Scenarios,
			Cases: view.Stats.Cases, Draft: view.Stats.Draft,
			Confirmed: view.Stats.Confirmed, Stale: view.Stats.Stale,
		},
		Requirements: make([]requirementGraphDTO, 0, len(view.Requirements)),
	}
	for _, requirement := range view.Requirements {
		requirementDTO := requirementGraphDTO{
			RequirementID:        platformid.Format(requirement.RequirementID),
			RequirementVersionID: platformid.Format(requirement.RequirementVersionID),
			Revision:             requirement.Revision,
			Title:                requirement.Title,
			Status:               requirement.Status,
			DesignStatus:         requirement.DesignStatus,
			TargetVersionSUTIDs:  formatIDs(requirement.TargetVersionSUTIDs),
			Scenarios:            make([]scenarioGraphDTO, 0, len(requirement.Scenarios)),
		}
		for _, scenario := range requirement.Scenarios {
			scenarioDTO := scenarioGraphDTO{
				ID:                   platformid.Format(scenario.ID),
				RequirementVersionID: platformid.Format(scenario.RequirementVersionID),
				Name:                 scenario.Name,
				Description:          scenario.Description,
				VersionSUTIDs:        formatIDs(scenario.VersionSUTIDs),
				PageIDs:              formatIDs(scenario.PageIDs),
				APIIDs:               formatIDs(scenario.APIIDs),
				Sort:                 scenario.SortOrder,
				Cases:                make([]caseCardDTO, 0, len(scenario.Cases)),
			}
			for _, card := range scenario.Cases {
				scenarioDTO.Cases = append(scenarioDTO.Cases, caseCardDTO{
					CaseID:              platformid.Format(card.CaseID),
					CurrentVersionID:    platformid.Format(card.CurrentVersionID),
					Revision:            card.Revision,
					Title:               card.Title,
					Priority:            card.Priority,
					Status:              card.Status,
					DefaultVersionSUTID: formatOptionalID(card.DefaultVersionSUTID),
					VersionSUTIDs:       formatIDs(card.VersionSUTIDs),
					PageIDs:             formatIDs(card.PageIDs),
					APIIDs:              formatIDs(card.APIIDs),
					StepCount:           card.StepCount,
					AssertionCount:      card.AssertionCount,
					CreatedAt:           formatTime(card.CreatedAt),
				})
			}
			requirementDTO.Scenarios = append(requirementDTO.Scenarios, scenarioDTO)
		}
		dto.Requirements = append(dto.Requirements, requirementDTO)
	}
	return dto
}

// ---------- overview ----------

type overviewDTO struct {
	PendingDesignCount        int       `json:"pending_design_count"`
	AutoDesignNewRequirements bool      `json:"auto_design_new_requirements"`
	RoundCount                int       `json:"round_count"`
	ActiveRound               *roundDTO `json:"active_round"`
}

type roundDTO struct {
	ID             string   `json:"id"`
	RoundNo        int      `json:"round_no"`
	ConversationID string   `json:"conversation_id"`
	Activation     string   `json:"activation"`
	RequirementIDs []string `json:"requirement_ids"`
	CreatedAt      string   `json:"created_at"`
}

func toOverviewDTO(view designapp.OverviewView) overviewDTO {
	dto := overviewDTO{
		PendingDesignCount:        view.PendingDesignCount,
		AutoDesignNewRequirements: view.AutoDesignNewRequirements,
		RoundCount:                view.RoundCount,
	}
	if view.ActiveRound != nil {
		active := *view.ActiveRound
		dto.ActiveRound = &roundDTO{
			ID:             platformid.Format(active.ID),
			RoundNo:        active.RoundNo,
			ConversationID: platformid.Format(active.ConversationID),
			Activation:     active.Activation,
			RequirementIDs: formatIDs(active.RequirementIDs),
			CreatedAt:      formatTime(active.CreatedAt),
		}
	}
	return dto
}

// ---------- case detail ----------

type caseDetailDTO struct {
	CaseID               string                  `json:"case_id"`
	Status               string                  `json:"status"`
	CurrentVersionID     *string                 `json:"current_version_id"`
	RequirementID        string                  `json:"requirement_id"`
	RequirementVersionID string                  `json:"requirement_version_id"`
	ScenarioID           string                  `json:"scenario_id"`
	ScenarioName         string                  `json:"scenario_name"`
	Versions             []caseVersionSummaryDTO `json:"versions"`
	Selected             caseVersionDetailDTO    `json:"selected"`
	Confirmable          bool                    `json:"confirmable"`
}

type caseVersionSummaryDTO struct {
	CaseVersionID string  `json:"case_version_id"`
	Revision      int     `json:"revision"`
	Status        string  `json:"status"`
	Title         string  `json:"title"`
	CreatedAt     string  `json:"created_at"`
	ConfirmedAt   *string `json:"confirmed_at"`
}

type caseVersionDetailDTO struct {
	CaseVersionID       string             `json:"case_version_id"`
	Revision            int                `json:"revision"`
	Title               string             `json:"title"`
	Description         string             `json:"description"`
	Preconditions       string             `json:"preconditions"`
	TestData            json.RawMessage    `json:"test_data"`
	Priority            string             `json:"priority"`
	DefaultVersionSUTID *string            `json:"default_version_sut_id"`
	VersionSUTIDs       []string           `json:"version_sut_ids"`
	PageIDs             []string           `json:"page_ids"`
	APIIDs              []string           `json:"api_ids"`
	Status              string             `json:"status"`
	ContentHash         string             `json:"content_hash"`
	CreatedAt           string             `json:"created_at"`
	Steps               []caseStepDTO      `json:"steps"`
	Assertions          []caseAssertionDTO `json:"assertions"`
	Sources             []caseSourceDTO    `json:"sources"`
}

type caseStepDTO struct {
	Seq                int             `json:"seq"`
	Action             string          `json:"action"`
	Expected           string          `json:"expected"`
	TargetVersionSUTID *string         `json:"target_version_sut_id"`
	TargetPageID       *string         `json:"target_page_id"`
	TargetAPIID        *string         `json:"target_api_id"`
	AccountRole        string          `json:"account_role"`
	Data               json.RawMessage `json:"data"`
}

type caseAssertionDTO struct {
	CaseAssertionID    string  `json:"case_assertion_id"`
	StepSeq            int     `json:"step_seq"`
	Kind               string  `json:"kind"`
	Target             string  `json:"target"`
	Expected           string  `json:"expected"`
	TargetVersionSUTID *string `json:"target_version_sut_id"`
	TargetAPIID        *string `json:"target_api_id"`
	DatabaseID         *string `json:"database_id"`
}

type caseSourceDTO struct {
	Kind  string `json:"kind"`
	RefID string `json:"ref_id"`
	Path  string `json:"path"`
	Line  *int   `json:"line"`
	SHA   string `json:"sha"`
}

func toCaseDetailDTO(view designapp.CaseDetailView) caseDetailDTO {
	dto := caseDetailDTO{
		CaseID:               platformid.Format(view.CaseID),
		Status:               view.Status,
		CurrentVersionID:     formatOptionalID(view.CurrentVersionID),
		RequirementID:        platformid.Format(view.RequirementID),
		RequirementVersionID: platformid.Format(view.RequirementVersionID),
		ScenarioID:           platformid.Format(view.ScenarioID),
		ScenarioName:         view.ScenarioName,
		Versions:             make([]caseVersionSummaryDTO, 0, len(view.Versions)),
		Selected:             toCaseVersionDetailDTO(view.Selected),
		Confirmable:          view.Confirmable,
	}
	for _, version := range view.Versions {
		summary := caseVersionSummaryDTO{
			CaseVersionID: platformid.Format(version.CaseVersionID),
			Revision:      version.Revision,
			Status:        version.Status,
			Title:         version.Title,
			CreatedAt:     formatTime(version.CreatedAt),
			ConfirmedAt:   formatOptionalTime(version.ConfirmedAt),
		}
		dto.Versions = append(dto.Versions, summary)
	}
	return dto
}

func toCaseVersionDetailDTO(version designapp.CaseVersionDetailView) caseVersionDetailDTO {
	dto := caseVersionDetailDTO{
		CaseVersionID:       platformid.Format(version.CaseVersionID),
		Revision:            version.Revision,
		Title:               version.Title,
		Description:         version.Description,
		Preconditions:       version.Preconditions,
		TestData:            version.TestData,
		Priority:            version.Priority,
		DefaultVersionSUTID: formatOptionalID(version.DefaultVersionSUTID),
		VersionSUTIDs:       formatIDs(version.VersionSUTIDs),
		PageIDs:             formatIDs(version.PageIDs),
		APIIDs:              formatIDs(version.APIIDs),
		Status:              version.Status,
		ContentHash:         version.ContentHash,
		CreatedAt:           formatTime(version.CreatedAt),
		Steps:               make([]caseStepDTO, 0, len(version.Steps)),
		Assertions:          make([]caseAssertionDTO, 0, len(version.Assertions)),
		Sources:             make([]caseSourceDTO, 0, len(version.Sources)),
	}
	if len(dto.TestData) == 0 {
		dto.TestData = json.RawMessage(`{}`)
	}
	for _, step := range version.Steps {
		data := step.Data
		if len(data) == 0 {
			data = json.RawMessage(`{}`)
		}
		dto.Steps = append(dto.Steps, caseStepDTO{
			Seq: step.Seq, Action: step.Action, Expected: step.Expected,
			TargetVersionSUTID: formatOptionalID(step.TargetVersionSUTID),
			TargetPageID:       formatOptionalID(step.TargetPageID),
			TargetAPIID:        formatOptionalID(step.TargetAPIID),
			AccountRole:        step.AccountRole,
			Data:               data,
		})
	}
	for _, assertion := range version.Assertions {
		dto.Assertions = append(dto.Assertions, caseAssertionDTO{
			CaseAssertionID: platformid.Format(assertion.ID),
			StepSeq:         assertion.StepSeq, Kind: assertion.Kind, Target: assertion.Target,
			Expected:           assertion.Expected,
			TargetVersionSUTID: formatOptionalID(assertion.TargetVersionSUTID),
			TargetAPIID:        formatOptionalID(assertion.TargetAPIID),
			DatabaseID:         formatOptionalID(assertion.DatabaseID),
		})
	}
	for _, source := range version.Sources {
		dto.Sources = append(dto.Sources, caseSourceDTO{
			Kind: source.Kind, RefID: source.RefID, Path: source.Path, Line: source.Line, SHA: source.SHA,
		})
	}
	return dto
}

// ---------- confirmation ----------

type confirmRequest struct {
	Scope    string  `json:"scope"`
	TargetID string  `json:"target_id"`
	BatchID  *string `json:"batch_id"`
}

type confirmedCaseDTO struct {
	CaseID        string `json:"case_id"`
	CaseVersionID string `json:"case_version_id"`
}

type skippedCaseDTO struct {
	CaseID        string `json:"case_id"`
	CaseVersionID string `json:"case_version_id"`
	Status        string `json:"status"`
}

type confirmResultDTO struct {
	Scope       string             `json:"scope"`
	TargetID    string             `json:"target_id"`
	BatchID     string             `json:"batch_id"`
	Confirmed   []confirmedCaseDTO `json:"confirmed"`
	Skipped     []skippedCaseDTO   `json:"skipped"`
	ConfirmedAt string             `json:"confirmed_at"`
}

func toConfirmResultDTO(result designapp.ConfirmResult) confirmResultDTO {
	dto := confirmResultDTO{
		Scope:       result.Scope,
		TargetID:    platformid.Format(result.TargetID),
		BatchID:     result.BatchID,
		Confirmed:   make([]confirmedCaseDTO, 0, len(result.Confirmed)),
		Skipped:     make([]skippedCaseDTO, 0, len(result.Skipped)),
		ConfirmedAt: formatTime(result.ConfirmedAt),
	}
	for _, item := range result.Confirmed {
		dto.Confirmed = append(dto.Confirmed, confirmedCaseDTO{
			CaseID: platformid.Format(item.CaseID), CaseVersionID: platformid.Format(item.CaseVersionID),
		})
	}
	for _, item := range result.Skipped {
		dto.Skipped = append(dto.Skipped, skippedCaseDTO{
			CaseID: platformid.Format(item.CaseID), CaseVersionID: platformid.Format(item.CaseVersionID),
			Status: item.Status,
		})
	}
	return dto
}

// ---------- exports ----------

type createExportRequest struct {
	OwnerKind string `json:"owner_kind"`
	OwnerID   string `json:"owner_id"`
	Format    string `json:"format"`
}

type exportViewDTO struct {
	ID          string `json:"id"`
	VersionID   string `json:"version_id"`
	OwnerKind   string `json:"owner_kind"`
	OwnerID     string `json:"owner_id"`
	Revision    int    `json:"revision"`
	Format      string `json:"format"`
	Size        int64  `json:"size"`
	MIME        string `json:"mime"`
	Filename    string `json:"filename"`
	SHA256      string `json:"sha256"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	DownloadURL string `json:"download_url"`
}

func toExportViewDTO(view designapp.ExportView) exportViewDTO {
	return exportViewDTO{
		ID:          platformid.Format(view.ID),
		VersionID:   platformid.Format(view.VersionID),
		OwnerKind:   view.OwnerKind,
		OwnerID:     platformid.Format(view.OwnerID),
		Revision:    view.Revision,
		Format:      view.Format,
		Size:        view.Size,
		MIME:        view.MIME,
		Filename:    view.Filename,
		SHA256:      view.SHA256,
		Status:      view.Status,
		CreatedAt:   formatTime(view.CreatedAt),
		DownloadURL: view.DownloadURL,
	}
}

// ---------- helpers ----------

func formatIDs(values []int64) []string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, platformid.Format(value))
	}
	return ids
}

func formatOptionalID(value *int64) *string {
	if value == nil || *value <= 0 {
		return nil
	}
	formatted := platformid.Format(*value)
	return &formatted
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}
