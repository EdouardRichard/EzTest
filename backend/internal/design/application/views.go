package designapp

import (
	"encoding/json"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// GraphStatsView is the design header aggregate.
type GraphStatsView struct {
	Requirements int
	Scenarios    int
	Cases        int
	Draft        int
	Confirmed    int
	Stale        int
}

// RequirementGraphView is one requirement node of the design graph.
type RequirementGraphView struct {
	RequirementID        int64
	RequirementVersionID int64
	Revision             int
	Title                string
	Status               string
	DesignStatus         string
	Scenarios            []ScenarioGraphView
}

// ScenarioGraphView is one scenario node.
//
// v0.2.4-B (§3.2): the resource ID arrays are gone with the scenario fields
// they projected.
type ScenarioGraphView struct {
	ID                   int64
	RequirementVersionID int64
	Name                 string
	Description          string
	SortOrder            int
	Cases                []CaseCardView
}

// CaseCardView is one case card of the design graph.
//
// v0.2.4-B (§3.2/§5.2): the card names the system under test in prose and lists
// the acceptance criteria the case covers, instead of default/version SUT and
// page/API ids.
type CaseCardView struct {
	CaseID           int64
	CurrentVersionID int64
	Revision         int
	Title            string
	Priority         string
	Status           string
	SutDescription   string
	CoveredACs       []string
	StepCount        int
	AssertionCount   int
	// ExecutionVerdict is the latest execution result of the current case
	// version (spec §9.3); a case without an execution is inconclusive.
	ExecutionVerdict string
	// ReviewState is the judge review projection of the current case version
	// (spec SC-E5, T16); empty when the judgment gate is not wired or its read
	// fails -- a read-projection gap, never a fabricated verdict.
	ReviewState design.ReviewState
	CreatedAt   time.Time
}

// GraphView is the full version design graph snapshot.
type GraphView struct {
	VersionID                 int64
	GeneratedAt               time.Time
	GraphHash                 string
	PendingDesignCount        int
	AutoDesignNewRequirements bool
	Stats                     GraphStatsView
	Requirements              []RequirementGraphView
}

// CaseVersionSummaryView is one history row of a case.
type CaseVersionSummaryView struct {
	CaseVersionID int64
	Revision      int
	Status        string
	Title         string
	CreatedAt     time.Time
	ConfirmedAt   *time.Time
}

// CaseStepView is one step of a selected case version.
type CaseStepView struct {
	Seq               int
	Action            string
	Expected          string
	TargetDescription string
	AccountRole       string
	Data              json.RawMessage
}

// CaseAssertionView is one assertion of a selected case version. SUTID is the
// SUT a kind=db assertion reads (v0.2.4-B §3.2, renamed from database_id).
type CaseAssertionView struct {
	ID       int64
	StepSeq  int
	Kind     string
	Target   string
	Expected string
	SUTID    *int64
}

// CaseSourceView is one provenance row of a selected case version.
type CaseSourceView struct {
	Kind  string
	RefID string
	Path  string
	Line  *int
	SHA   string
}

// CaseVersionDetailView is the selected immutable case version.
type CaseVersionDetailView struct {
	CaseVersionID  int64
	Revision       int
	Title          string
	Description    string
	SutDescription string
	Preconditions  string
	CoveredACs     []string
	TestData       json.RawMessage
	Priority       string
	Status         string
	ContentHash    string
	CreatedAt      time.Time
	Steps          []CaseStepView
	Assertions     []CaseAssertionView
	Sources        []CaseSourceView
}

// CaseDetailView is the case detail read model.
type CaseDetailView struct {
	CaseID               int64
	Status               string
	CurrentVersionID     *int64
	RequirementID        int64
	RequirementVersionID int64
	ScenarioID           int64
	ScenarioName         string
	Versions             []CaseVersionSummaryView
	Selected             CaseVersionDetailView
	Confirmable          bool
	// ReviewState is the judge review projection of the CURRENT case version
	// (spec SC-E5, T16) -- always the current version, even when Selected is a
	// historical revision; empty when the judgment gate is unwired/unreadable
	// or the case has no current version.
	ReviewState design.ReviewState
}

// RoundView is one design round projection.
type RoundView struct {
	ID                    int64
	VersionID             int64
	ConversationID        int64
	RoundNo               int
	Activation            string
	RequirementIDs        []int64
	RequirementVersionIDs []int64
	Status                string
	CreatedAt             time.Time
}

// OverviewView is the version design overview projection.
type OverviewView struct {
	PendingDesignCount        int
	AutoDesignNewRequirements bool
	RoundCount                int
	ActiveRound               *RoundView
}

// JudgmentCaseJudgmentView is one case's entry in the judgment read report
// (T16, spec SC-E5): its current version, the single-authority review_state
// projection, and the latest judge fact bound to that exact version (nil when
// the case has not been judged yet, or only a stale verdict exists).
type JudgmentCaseJudgmentView struct {
	CaseID           int64
	CurrentVersionID int64
	ReviewState      design.ReviewState
	Judgment         *design.Judgment
}

// JudgmentReportView is the read-only judgment report of a version, optionally
// narrowed to one requirement (T16). Cases enumerates every current case of
// the version (or requirement) -- not only the judged ones -- so the report
// doubles as the case_review/designing review-state view.
type JudgmentReportView struct {
	Cases []JudgmentCaseJudgmentView
}

// ProposeInput is one case.graph_propose payload.
type ProposeInput struct {
	RequirementID        int64
	RequirementVersionID int64
	BaseGraphHash        string
	Scenarios            []ScenarioInput
	Cases                []CaseInput
}

// ScenarioInput is one submitted scenario node.
type ScenarioInput struct {
	Ref         string
	ID          *int64
	Name        string
	Description string
	SortOrder   int
}

// CaseInput is one submitted logical case.
//
// v0.2.4-B (§5.2): SutDescription, Preconditions and CoveredACs are required
// contract elements; ValidateProposal rejects a proposal missing any of them.
type CaseInput struct {
	Ref            string
	ID             *int64
	ScenarioRef    string
	Title          string
	Description    string
	SutDescription string
	Preconditions  string
	CoveredACs     []string
	TestData       json.RawMessage
	Priority       string
	Steps          []StepInput
	Assertions     []AssertionInput
	Sources        []SourceInput
	// Trigger/SourceObservationID/AutoConfirmedBy/RollbackOf carry the
	// discovery/rollback provenance of a programmatic proposal (spec DB-30).
	// A zero Trigger means the normal manual propose.
	Trigger             string
	SourceObservationID *int64
	AutoConfirmedBy     string
	RollbackOf          *int64
}

// StepInput is one submitted step.
type StepInput struct {
	Seq               int
	Action            string
	Expected          string
	TargetDescription string
	AccountRole       string
	Data              json.RawMessage
}

// AssertionInput is one submitted assertion. SUTID is required for kind=db and
// ignored otherwise.
type AssertionInput struct {
	StepSeq  int
	Kind     string
	Target   string
	Expected string
	SUTID    *int64
}

// SourceInput is one submitted provenance row.
type SourceInput struct {
	Kind  string
	RefID string
	Path  string
	Line  *int
	SHA   string
}

// ProposeResult is the case.graph_propose response payload.
type ProposeResult struct {
	ScenarioIDs    []int64
	CaseIDs        []int64
	CaseVersionIDs []int64
	GraphHash      string
}

func toDomainProposal(in ProposeInput) design.Proposal {
	proposal := design.Proposal{
		RequirementID:        in.RequirementID,
		RequirementVersionID: in.RequirementVersionID,
		BaseGraphHash:        in.BaseGraphHash,
		Scenarios:            make([]design.ScenarioProposal, 0, len(in.Scenarios)),
		Cases:                make([]design.CaseProposal, 0, len(in.Cases)),
	}
	for _, scenario := range in.Scenarios {
		proposal.Scenarios = append(proposal.Scenarios, design.ScenarioProposal{
			Ref: scenario.Ref, ID: scenario.ID, Name: scenario.Name,
			Description: scenario.Description, SortOrder: scenario.SortOrder,
		})
	}
	for _, item := range in.Cases {
		proposalCase := design.CaseProposal{
			Ref: item.Ref, ID: item.ID, ScenarioRef: item.ScenarioRef, Title: item.Title,
			Description: item.Description, SutDescription: item.SutDescription,
			Preconditions: item.Preconditions, CoveredACs: item.CoveredACs,
			TestData: item.TestData, Priority: item.Priority,
			Steps:      make([]design.StepProposal, 0, len(item.Steps)),
			Assertions: make([]design.AssertionProposal, 0, len(item.Assertions)),
			Sources:    make([]design.SourceProposal, 0, len(item.Sources)),
		}
		for _, step := range item.Steps {
			proposalCase.Steps = append(proposalCase.Steps, design.StepProposal{
				Seq: step.Seq, Action: step.Action, Expected: step.Expected,
				TargetDescription: step.TargetDescription,
				AccountRole:       step.AccountRole, Data: step.Data,
			})
		}
		for _, assertion := range item.Assertions {
			proposalCase.Assertions = append(proposalCase.Assertions, design.AssertionProposal{
				StepSeq: assertion.StepSeq, Kind: assertion.Kind, Target: assertion.Target,
				Expected: assertion.Expected, SUTID: assertion.SUTID,
			})
		}
		for _, source := range item.Sources {
			proposalCase.Sources = append(proposalCase.Sources, design.SourceProposal{
				Kind: source.Kind, RefID: source.RefID, Path: source.Path, Line: source.Line, SHA: source.SHA,
			})
		}
		proposal.Cases = append(proposal.Cases, proposalCase)
	}
	return proposal
}

func graphStatsView(stats design.GraphStats) GraphStatsView {
	return GraphStatsView{
		Requirements: stats.Requirements, Scenarios: stats.Scenarios, Cases: stats.Cases,
		Draft: stats.Draft, Confirmed: stats.Confirmed, Stale: stats.Stale,
	}
}

func roundView(round design.Round, roundNo int) RoundView {
	return RoundView{
		ID: round.ID, VersionID: round.VersionID, ConversationID: round.ConversationID,
		RoundNo: roundNo, Activation: round.Activation,
		RequirementIDs: round.RequirementIDs, RequirementVersionIDs: round.RequirementVersionIDs,
		Status: round.Status, CreatedAt: utcNow(round.CreatedAt),
	}
}
