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
	TargetVersionSUTIDs  []int64
	Scenarios            []ScenarioGraphView
}

// ScenarioGraphView is one scenario node.
type ScenarioGraphView struct {
	ID                   int64
	RequirementVersionID int64
	Name                 string
	Description          string
	VersionSUTIDs        []int64
	PageIDs              []int64
	APIIDs               []int64
	SortOrder            int
	Cases                []CaseCardView
}

// CaseCardView is one case card of the design graph.
type CaseCardView struct {
	CaseID              int64
	CurrentVersionID    int64
	Revision            int
	Title               string
	Priority            string
	Status              string
	DefaultVersionSUTID *int64
	VersionSUTIDs       []int64
	PageIDs             []int64
	APIIDs              []int64
	StepCount           int
	AssertionCount      int
	CreatedAt           time.Time
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
	Seq                int
	Action             string
	Expected           string
	TargetVersionSUTID *int64
	TargetPageID       *int64
	TargetAPIID        *int64
	AccountRole        string
	Data               json.RawMessage
}

// CaseAssertionView is one assertion of a selected case version.
type CaseAssertionView struct {
	ID                 int64
	StepSeq            int
	Kind               string
	Target             string
	Expected           string
	TargetVersionSUTID *int64
	TargetAPIID        *int64
	DatabaseID         *int64
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
	CaseVersionID       int64
	Revision            int
	Title               string
	Description         string
	Preconditions       string
	TestData            json.RawMessage
	Priority            string
	DefaultVersionSUTID *int64
	VersionSUTIDs       []int64
	PageIDs             []int64
	APIIDs              []int64
	Status              string
	ContentHash         string
	CreatedAt           time.Time
	Steps               []CaseStepView
	Assertions          []CaseAssertionView
	Sources             []CaseSourceView
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
	Ref           string
	ID            *int64
	Name          string
	Description   string
	VersionSUTIDs []int64
	PageIDs       []int64
	APIIDs        []int64
	SortOrder     int
}

// CaseInput is one submitted logical case.
type CaseInput struct {
	Ref                 string
	ID                  *int64
	ScenarioRef         string
	Title               string
	Description         string
	Preconditions       string
	TestData            json.RawMessage
	Priority            string
	DefaultVersionSUTID *int64
	VersionSUTIDs       []int64
	PageIDs             []int64
	APIIDs              []int64
	Steps               []StepInput
	Assertions          []AssertionInput
	Sources             []SourceInput
}

// StepInput is one submitted step.
type StepInput struct {
	Seq                int
	Action             string
	Expected           string
	TargetVersionSUTID *int64
	TargetPageID       *int64
	TargetAPIID        *int64
	AccountRole        string
	Data               json.RawMessage
}

// AssertionInput is one submitted assertion.
type AssertionInput struct {
	StepSeq            int
	Kind               string
	Target             string
	Expected           string
	TargetVersionSUTID *int64
	TargetAPIID        *int64
	DatabaseID         *int64
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
			Description: scenario.Description, VersionSUTIDs: scenario.VersionSUTIDs,
			PageIDs: scenario.PageIDs, APIIDs: scenario.APIIDs, SortOrder: scenario.SortOrder,
		})
	}
	for _, item := range in.Cases {
		proposalCase := design.CaseProposal{
			Ref: item.Ref, ID: item.ID, ScenarioRef: item.ScenarioRef, Title: item.Title,
			Description: item.Description, Preconditions: item.Preconditions,
			TestData: item.TestData, Priority: item.Priority,
			DefaultVersionSUTID: item.DefaultVersionSUTID, VersionSUTIDs: item.VersionSUTIDs,
			PageIDs: item.PageIDs, APIIDs: item.APIIDs,
			Steps:      make([]design.StepProposal, 0, len(item.Steps)),
			Assertions: make([]design.AssertionProposal, 0, len(item.Assertions)),
			Sources:    make([]design.SourceProposal, 0, len(item.Sources)),
		}
		for _, step := range item.Steps {
			proposalCase.Steps = append(proposalCase.Steps, design.StepProposal{
				Seq: step.Seq, Action: step.Action, Expected: step.Expected,
				TargetVersionSUTID: step.TargetVersionSUTID, TargetPageID: step.TargetPageID,
				TargetAPIID: step.TargetAPIID, AccountRole: step.AccountRole, Data: step.Data,
			})
		}
		for _, assertion := range item.Assertions {
			proposalCase.Assertions = append(proposalCase.Assertions, design.AssertionProposal{
				StepSeq: assertion.StepSeq, Kind: assertion.Kind, Target: assertion.Target,
				Expected: assertion.Expected, TargetVersionSUTID: assertion.TargetVersionSUTID,
				TargetAPIID: assertion.TargetAPIID, DatabaseID: assertion.DatabaseID,
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
