package designapi

import (
	"encoding/json"
	"fmt"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
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
	Scenarios            []scenarioGraphDTO `json:"scenarios"`
}

type scenarioGraphDTO struct {
	ID                   string        `json:"id"`
	RequirementVersionID string        `json:"requirement_version_id"`
	Name                 string        `json:"name"`
	Description          string        `json:"description"`
	Sort                 int           `json:"sort"`
	Cases                []caseCardDTO `json:"cases"`
}

// caseCardDTO is one case card of the graph. v0.2.4-B (§3.2/§5.2): the resource
// id arrays are replaced by the natural-language contract -- sut_description and
// the acceptance-criterion ids the case covers. Breaking REST change; the OpenAPI
// document is rebuilt by T19/T20.
type caseCardDTO struct {
	CaseID           string   `json:"case_id"`
	CurrentVersionID string   `json:"current_version_id"`
	Revision         int      `json:"revision"`
	Title            string   `json:"title"`
	Priority         string   `json:"priority"`
	Status           string   `json:"status"`
	SutDescription   string   `json:"sut_description"`
	CoveredACs       []string `json:"covered_acs"`
	StepCount        int      `json:"step_count"`
	AssertionCount   int      `json:"assertion_count"`
	// ExecutionVerdict is additive: the latest execution result of the case
	// (passed/failed/blocked/inconclusive), inconclusive when never executed.
	ExecutionVerdict string `json:"execution_verdict"`
	// ReviewState is additive (T16, spec SC-E5): the judge review projection
	// of the current case version (pending/pass/reworking/escalated_fail/
	// judge_unavailable), empty when the judgment gate is unwired/unreadable.
	ReviewState string `json:"review_state"`
	CreatedAt   string `json:"created_at"`
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
			Scenarios:            make([]scenarioGraphDTO, 0, len(requirement.Scenarios)),
		}
		for _, scenario := range requirement.Scenarios {
			scenarioDTO := scenarioGraphDTO{
				ID:                   platformid.Format(scenario.ID),
				RequirementVersionID: platformid.Format(scenario.RequirementVersionID),
				Name:                 scenario.Name,
				Description:          scenario.Description,
				Sort:                 scenario.SortOrder,
				Cases:                make([]caseCardDTO, 0, len(scenario.Cases)),
			}
			for _, card := range scenario.Cases {
				scenarioDTO.Cases = append(scenarioDTO.Cases, caseCardDTO{
					CaseID:           platformid.Format(card.CaseID),
					CurrentVersionID: platformid.Format(card.CurrentVersionID),
					Revision:         card.Revision,
					Title:            card.Title,
					Priority:         card.Priority,
					Status:           card.Status,
					SutDescription:   card.SutDescription,
					CoveredACs:       nonNilStrings(card.CoveredACs),
					StepCount:        card.StepCount,
					AssertionCount:   card.AssertionCount,
					ExecutionVerdict: card.ExecutionVerdict,
					ReviewState:      string(card.ReviewState),
					CreatedAt:        formatTime(card.CreatedAt),
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
	// ReviewState is additive (T16, spec SC-E5): the judge review projection
	// of the CURRENT case version, independent of which historical revision
	// Selected shows.
	ReviewState string `json:"review_state"`
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
	CaseVersionID  string             `json:"case_version_id"`
	Revision       int                `json:"revision"`
	Title          string             `json:"title"`
	Description    string             `json:"description"`
	SutDescription string             `json:"sut_description"`
	Preconditions  string             `json:"preconditions"`
	CoveredACs     []string           `json:"covered_acs"`
	TestData       json.RawMessage    `json:"test_data"`
	Priority       string             `json:"priority"`
	Status         string             `json:"status"`
	ContentHash    string             `json:"content_hash"`
	CreatedAt      string             `json:"created_at"`
	Steps          []caseStepDTO      `json:"steps"`
	Assertions     []caseAssertionDTO `json:"assertions"`
	Sources        []caseSourceDTO    `json:"sources"`
}

type caseStepDTO struct {
	Seq               int             `json:"seq"`
	Action            string          `json:"action"`
	Expected          string          `json:"expected"`
	TargetDescription string          `json:"target_description"`
	AccountRole       string          `json:"account_role"`
	Data              json.RawMessage `json:"data"`
}

// caseAssertionDTO. v0.2.4-B (§3.2, M-B4): target_version_sut_id/target_api_id
// are gone and database_id is renamed sut_id -- a db assertion names the SUT it
// reads. Omitted for every other kind.
type caseAssertionDTO struct {
	CaseAssertionID string  `json:"case_assertion_id"`
	StepSeq         int     `json:"step_seq"`
	Kind            string  `json:"kind"`
	Target          string  `json:"target"`
	Expected        string  `json:"expected"`
	SUTID           *string `json:"sut_id"`
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
		ReviewState:          string(view.ReviewState),
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
		CaseVersionID:  platformid.Format(version.CaseVersionID),
		Revision:       version.Revision,
		Title:          version.Title,
		Description:    version.Description,
		SutDescription: version.SutDescription,
		Preconditions:  version.Preconditions,
		CoveredACs:     nonNilStrings(version.CoveredACs),
		TestData:       version.TestData,
		Priority:       version.Priority,
		Status:         version.Status,
		ContentHash:    version.ContentHash,
		CreatedAt:      formatTime(version.CreatedAt),
		Steps:          make([]caseStepDTO, 0, len(version.Steps)),
		Assertions:     make([]caseAssertionDTO, 0, len(version.Assertions)),
		Sources:        make([]caseSourceDTO, 0, len(version.Sources)),
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
			TargetDescription: step.TargetDescription,
			AccountRole:       step.AccountRole,
			Data:              data,
		})
	}
	for _, assertion := range version.Assertions {
		dto.Assertions = append(dto.Assertions, caseAssertionDTO{
			CaseAssertionID: platformid.Format(assertion.ID),
			StepSeq:         assertion.StepSeq, Kind: assertion.Kind, Target: assertion.Target,
			Expected: assertion.Expected,
			SUTID:    formatOptionalID(assertion.SUTID),
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

// ---------- discovery revision rollback ----------

type rollbackCaseVersionRequest struct {
	Reason string `json:"reason"`
}

type caseVersionRollbackDTO struct {
	CaseID         string `json:"case_id"`
	CaseVersionID  string `json:"case_version_id"`
	RolledBackFrom string `json:"rolled_back_from"`
	Revision       int    `json:"revision"`
	Status         string `json:"status"`
	Trigger        string `json:"trigger"`
}

func toCaseVersionRollbackDTO(view designapp.CaseVersionRollbackView) caseVersionRollbackDTO {
	return caseVersionRollbackDTO{
		CaseID:         platformid.Format(view.CaseID),
		CaseVersionID:  platformid.Format(view.CaseVersionID),
		RolledBackFrom: platformid.Format(view.RolledBackFrom),
		Revision:       view.Revision,
		Status:         view.Status,
		Trigger:        view.Trigger,
	}
}

// ---------- judgment override + read report (T16) ----------

type overrideJudgmentRequest struct {
	Reason string `json:"reason"`
}

type judgmentOverrideDTO struct {
	ID            string  `json:"id"`
	CaseID        string  `json:"case_id"`
	CaseVersionID string  `json:"case_version_id"`
	ContentHash   string  `json:"content_hash"`
	JudgmentID    *string `json:"judgment_id,omitempty"`
	Condition     string  `json:"condition"`
	ActorID       string  `json:"actor_id"`
	Reason        string  `json:"reason"`
	CreatedAt     string  `json:"created_at"`
}

func toJudgmentOverrideDTO(override design.Override) judgmentOverrideDTO {
	return judgmentOverrideDTO{
		ID:            platformid.Format(override.ID),
		CaseID:        platformid.Format(override.CaseID),
		CaseVersionID: platformid.Format(override.CaseVersionID),
		ContentHash:   override.ContentHash,
		JudgmentID:    formatOptionalID(override.JudgmentID),
		Condition:     override.Condition,
		ActorID:       platformid.Format(override.ActorID),
		Reason:        override.Reason,
		CreatedAt:     formatTime(override.CreatedAt),
	}
}

type judgmentFindingDTO struct {
	Dimension string `json:"dimension"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	CaseRef   string `json:"case_ref,omitempty"`
}

type judgmentDTO struct {
	ID               string               `json:"id"`
	CaseID           string               `json:"case_id"`
	CaseVersionID    string               `json:"case_version_id"`
	ContentHash      string               `json:"content_hash"`
	Round            int                  `json:"round"`
	Verdict          string               `json:"verdict"`
	Findings         []judgmentFindingDTO `json:"findings"`
	ACCoverageMatrix json.RawMessage      `json:"ac_coverage_matrix"`
	CreatedAt        string               `json:"created_at"`
}

// toJudgmentDTO projects one judge fact onto the wire. The AC coverage matrix is
// re-encoded from the stored JSON object; an encoding failure is surfaced as an
// error rather than silently replaced by an empty object (constitution VII.6,
// H-F10 T16). It is unreachable for a matrix decoded from JSONB — which is why
// the failure must be loud if it ever happens, not invisible.
func toJudgmentDTO(judgment design.Judgment) (judgmentDTO, error) {
	findings := make([]judgmentFindingDTO, 0, len(judgment.Findings))
	for _, finding := range judgment.Findings {
		findings = append(findings, judgmentFindingDTO{
			Dimension: finding.Dimension, Severity: finding.Severity,
			Message: finding.Message, CaseRef: finding.CaseRef,
		})
	}
	matrix := json.RawMessage(`{}`)
	if len(judgment.ACCoverageMatrix) > 0 {
		encoded, err := json.Marshal(judgment.ACCoverageMatrix)
		if err != nil {
			return judgmentDTO{}, fmt.Errorf("encode the AC coverage matrix of judgment %d: %w", judgment.ID, err)
		}
		matrix = encoded
	}
	return judgmentDTO{
		ID:               platformid.Format(judgment.ID),
		CaseID:           platformid.Format(judgment.CaseID),
		CaseVersionID:    platformid.Format(judgment.CaseVersionID),
		ContentHash:      judgment.ContentHash,
		Round:            judgment.Round,
		Verdict:          judgment.Verdict,
		Findings:         findings,
		ACCoverageMatrix: matrix,
		CreatedAt:        formatTime(judgment.CreatedAt),
	}, nil
}

type judgmentCaseDTO struct {
	CaseID           string       `json:"case_id"`
	CurrentVersionID string       `json:"current_version_id"`
	ReviewState      string       `json:"review_state"`
	Judgment         *judgmentDTO `json:"judgment"`
}

type judgmentReportDTO struct {
	Cases []judgmentCaseDTO `json:"cases"`
}

func toJudgmentReportDTO(view designapp.JudgmentReportView) (judgmentReportDTO, error) {
	dto := judgmentReportDTO{Cases: make([]judgmentCaseDTO, 0, len(view.Cases))}
	for _, item := range view.Cases {
		card := judgmentCaseDTO{
			CaseID:           platformid.Format(item.CaseID),
			CurrentVersionID: platformid.Format(item.CurrentVersionID),
			ReviewState:      string(item.ReviewState),
		}
		if item.Judgment != nil {
			judgment, err := toJudgmentDTO(*item.Judgment)
			if err != nil {
				return judgmentReportDTO{}, err
			}
			card.Judgment = &judgment
		}
		dto.Cases = append(dto.Cases, card)
	}
	return dto, nil
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

// nonNilStrings keeps covered_acs a JSON array on every endpoint. The graph card
// and the case detail project the same field, so without this one shared guard a
// case with no covered ACs would serialize as [] on one endpoint and null on the
// other -- and a frontend built against either reading would break on the other.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
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
