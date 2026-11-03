package agentcontext

import (
	"context"
	"time"
)

// Reader is the narrow read port behind the version briefing (spec §6.2). The
// production implementation issues deterministic, read-only SQL for one
// version; tests inject fakes. The briefing is derived and never persisted, so
// the port only has to supply the data one render needs.
type Reader interface {
	// Load returns the briefing source of one version. It returns an error
	// (typically a not-found error) when the version does not exist. The
	// Requirements, Cases and Plan data must come back in its semantic order;
	// facts and knowledge may arrive in any order because the builder ranks
	// them deterministically before rendering.
	Load(ctx context.Context, versionID int64) (Source, error)
}

// Source is the complete read model of one version briefing.
type Source struct {
	Version       VersionSnapshot
	Requirements  RequirementsSnapshot
	Cases         CasesSnapshot
	Plan          []PlanItemSnapshot
	Findings      []FactSnapshot
	Execution     ExecutionSnapshot
	Knowledge     []KnowledgeSnapshot
	OpenQuestions OpenQuestionsSnapshot
}

// VersionSnapshot is the goal/stage head of a version. The version store only
// persists a title, so the adapter maps it to Goal.
type VersionSnapshot struct {
	// Goal is the version objective.
	Goal string
	// Stage is the version.stage machine state (spec §5.3).
	Stage string
	// Blocker is the user-visible blocked/paused reason, empty otherwise. The
	// frozen Briefing shape has no blocker field, so the builder folds it into
	// the rendered stage.
	Blocker string
}

// RequirementsSnapshot is the confirmed requirement baseline and its gaps.
type RequirementsSnapshot struct {
	// Confirmed lists the requirements past the clarification gate.
	Confirmed []RequirementSnapshot
	// Gaps lists the open clarification gaps that are not settled yet.
	Gaps []string
}

// RequirementSnapshot is one confirmed requirement.
type RequirementSnapshot struct {
	Title      string
	Acceptance string
}

// CasesSnapshot is the case-state summary and its coverage gaps.
type CasesSnapshot struct {
	Draft        int
	Confirmed    int
	Stale        int
	CoverageGaps []string
}

// PlanItemSnapshot is one open plan item, supplied in dependency order.
type PlanItemSnapshot struct {
	ID        int64
	Title     string
	Status    string
	DependsOn []string
}

// FactSnapshot is one current finding of the version.
type FactSnapshot struct {
	ID         int64
	Kind       string
	Key        string
	Summary    string
	Confidence *float64
	CreatedAt  time.Time
}

// ExecutionSnapshot is the summary of the latest execution wave.
type ExecutionSnapshot struct {
	WaveStatus string
	Passed     int
	Failed     int
	Blocked    int
	Skipped    int
	Blocker    string
}

// KnowledgeSnapshot is one knowledge item relevant to the version.
type KnowledgeSnapshot struct {
	ID         int64
	Title      string
	Summary    string
	Confidence *float64
	// Status is the knowledge review/verification state ("approved",
	// "suspect", "stale", ...). The briefing only injects approved items; an
	// empty value means the read port did not supply the state and the item is
	// rendered. The production SQL reader supplies it together with SourceStale.
	Status string
	// SourceStale is the read-time marker of a knowledge item whose source
	// version no longer exists. It is never injected.
	SourceStale bool
}

// OpenQuestionsSnapshot is the unresolved question and human-request set.
type OpenQuestionsSnapshot struct {
	Questions     []string
	HumanRequests []string
}
