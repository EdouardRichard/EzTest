package design

import (
	"context"
	"encoding/json"
	"time"
)

// RequirementRef is the read-only design projection of a version-context
// requirement joined with its current immutable revision.
type RequirementRef struct {
	ID                  int64
	VersionID           int64
	CurrentVersionID    int64
	Revision            int
	Title               string
	Status              string
	TargetVersionSUTIDs []int64
}

// VersionContext is the design-relevant slice of a version's binding set. The
// target maps answer "which version_sut does this page/api/database belong
// to?" for proposal validation.
type VersionContext struct {
	VersionID                 int64
	Status                    string
	AutoDesignNewRequirements bool
	AllowedVersionSUTs        []int64
	PageToSUT                 map[int64]int64
	APIToSUT                  map[int64]int64
	DBTOSUT                   map[int64]int64
}

// CaseWithCurrent pairs a logical case with its current immutable version. A
// P2 case always has a current version; Current is nil only for corrupt rows.
type CaseWithCurrent struct {
	Case    CaseEntity
	Current *CaseVersion
}

// CaseVersionSummary is one row of the case version history.
type CaseVersionSummary struct {
	ID          int64
	Revision    int
	Status      string
	Title       string
	CreatedAt   time.Time
	ConfirmedAt *time.Time
}

// CaseData is the repository read model behind GET /design/cases/{caseId}.
type CaseData struct {
	Case        CaseEntity
	Scenario    Scenario
	Requirement RequirementRef
	Versions    []CaseVersionSummary
	Selected    CaseVersion
}

// CaseGraph is one case card in the design graph.
type CaseGraph struct {
	Case           CaseEntity
	Current        CaseVersion
	StepCount      int
	AssertionCount int
}

// ScenarioGraph is one scenario node with its case cards.
type ScenarioGraph struct {
	Scenario Scenario
	Cases    []CaseGraph
}

// RequirementGraph is one requirement node of the version design graph.
type RequirementGraph struct {
	Requirement  RequirementRef
	DesignStatus string
	Scenarios    []ScenarioGraph
}

// GraphStats are the aggregate counts shown in the design header.
type GraphStats struct {
	Requirements int
	Scenarios    int
	Cases        int
	Draft        int
	Confirmed    int
	Stale        int
}

// GraphData is the full design graph snapshot of one version.
type GraphData struct {
	VersionID                 int64
	Requirements              []RequirementGraph
	PendingDesignCount        int
	AutoDesignNewRequirements bool
	Stats                     GraphStats
}

// OverviewData is the version design overview projection.
type OverviewData struct {
	PendingDesignCount        int
	AutoDesignNewRequirements bool
	RoundCount                int
	ActiveRound               *Round
	ActiveRoundNo             int
}

// RequirementSnapshot is the transactional view of one requirement version's
// design graph used by case.graph_propose.
type RequirementSnapshot struct {
	RequirementVersionID int64
	Scenarios            []Scenario
	Cases                []CaseWithCurrent
}

// CaseCounts are the current-case aggregates of one version.
type CaseCounts struct {
	Draft     int
	Confirmed int
	Stale     int
}

// RequirementCoverage is the planner projection of one activated requirement:
// how many scenarios/cases cover its current requirement version.
type RequirementCoverage struct {
	Requirement    RequirementRef
	ScenarioCount  int
	DraftCount     int
	ConfirmedCount int
	StaleCount     int
}

// OutdatedRequirement is a requirement whose current revision moved while
// confirmed cases still point at an older revision (stale safety net).
type OutdatedRequirement struct {
	VersionID                   int64
	RequirementID               int64
	CurrentRequirementVersionID int64
}

// ConfirmationTarget is the resolved scope target of one confirm call.
type ConfirmationTarget struct {
	Scope         string
	TargetID      int64
	RequirementID int64
	ScenarioID    int64
	CaseID        int64
}

// ConfirmCaseVersionCommand freezes one case version and projects the entity
// status inside the confirmation transaction.
type ConfirmCaseVersionCommand struct {
	Confirmation Confirmation
	EntityStatus string
}

// DataPlanPatch is the editable part of a data-planner draft revision.
type DataPlanPatch struct {
	Preconditions *string
	TestData      json.RawMessage
}

// ExecutableCase is the P3 execution read projection: one current+confirmed
// case version with its requirement context. The execution context maps this
// onto its own domain snapshot; design never imports execution.
type ExecutableCase struct {
	CaseID               int64
	RequirementID        int64
	RequirementVersionID int64
	Version              CaseVersion
}

// GraphRepository reads design graph snapshots, case detail, overview and the
// version/requirement projections needed for validation.
type GraphRepository interface {
	ReadGraph(ctx context.Context, versionID int64) (GraphData, error)
	ReadCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (CaseData, error)
	VersionContext(ctx context.Context, versionID int64) (VersionContext, error)
	RequirementContext(ctx context.Context, versionID, requirementID int64) (RequirementRef, error)
	VersionTitle(ctx context.Context, versionID int64) (string, error)
	Overview(ctx context.Context, versionID int64) (OverviewData, error)

	// ReadExecutableCase loads one current+confirmed case version that belongs
	// to the version and whose scenario still targets the current requirement
	// revision. A stale/superseded/draft case version is ErrNotFound.
	ReadExecutableCase(ctx context.Context, versionID, caseVersionID int64) (ExecutableCase, error)
	// ListExecutableCases lists every current+confirmed case version of a
	// version in ascending case-version ID order.
	ListExecutableCases(ctx context.Context, versionID int64) ([]ExecutableCase, error)

	// ReadCaseVersionsByIDs loads case versions by ID regardless of their
	// current/confirmed status, scoped to one version (a case version of
	// another version is not returned). It is the historical reader of the
	// exploration chain: superseded rows keep their title. Missing IDs are
	// simply absent from the result. It never relaxes the current+confirmed
	// semantics of the P3 execution readers.
	ReadCaseVersionsByIDs(ctx context.Context, versionID int64, caseVersionIDs []int64) ([]CaseVersionSummary, error)
}

// Tx is the write surface of one requirement-scoped advisory-lock transaction.
type Tx interface {
	CurrentGraphHash(ctx context.Context) (string, error)
	RequirementSnapshot(ctx context.Context, versionID, requirementID, requirementVersionID int64) (RequirementSnapshot, error)
	InsertScenario(ctx context.Context, scenario Scenario) (Scenario, error)
	UpdateScenario(ctx context.Context, scenario Scenario) (Scenario, error)
	InsertCase(ctx context.Context, entity CaseEntity) error
	InsertCaseVersion(ctx context.Context, version CaseVersion) (CaseVersion, error)
	SetCurrentVersion(ctx context.Context, caseID, caseVersionID int64, status string, at time.Time) error
}

// ProposeRepository runs the case.graph_propose write transaction.
type ProposeRepository interface {
	WithRequirementLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, Tx) error) error
}

// ConfirmationTx is the row-locking read/write surface of one confirmation
// transaction.
type ConfirmationTx interface {
	Requirement(ctx context.Context) (RequirementRef, error)
	Scenario(ctx context.Context, scenarioID int64) (Scenario, error)
	Case(ctx context.Context, caseID int64) (CaseWithCurrent, error)
	CasesByScenario(ctx context.Context, scenarioID int64) ([]CaseWithCurrent, error)
	CasesByRequirement(ctx context.Context) ([]CaseWithCurrent, error)
	ConfirmCaseVersion(ctx context.Context, command ConfirmCaseVersionCommand) error
	SupersedeVersions(ctx context.Context, caseID, keepVersionID int64, at time.Time) error
	// SupersededVersionIDs lists the older confirmed/stale versions that
	// SupersedeVersions would move, so the service can emit one event each.
	SupersededVersionIDs(ctx context.Context, caseID, keepVersionID int64) ([]int64, error)
}

// ConfirmationRepository resolves a confirmation target and runs the
// confirmation transaction.
type ConfirmationRepository interface {
	ResolveConfirmationTarget(ctx context.Context, versionID int64, scope string, targetID int64) (ConfirmationTarget, error)
	WithConfirmationLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, ConfirmationTx) error) error
}

// StaleTx is the row-locking surface of one stale-propagation transaction.
type StaleTx interface {
	Requirement(ctx context.Context) (RequirementRef, error)
	CasesByRequirement(ctx context.Context) ([]CaseWithCurrent, error)
	MarkCaseVersionStale(ctx context.Context, caseVersionID int64, at time.Time) error
	MarkCaseEntityStale(ctx context.Context, caseID int64, at time.Time) error
}

// StaleRepository runs stale propagation and the reconciler safety-net query.
type StaleRepository interface {
	WithStaleLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, StaleTx) error) error
	ListOutdatedConfirmedRequirements(ctx context.Context, limit int) ([]OutdatedRequirement, error)
}

// PlannerRepository holds the deterministic planner/reconciler and metrics
// queries. None of these methods writes case content.
type PlannerRepository interface {
	WithVersionLock(ctx context.Context, versionID int64, fn func(context.Context) error) error
	ListCandidateVersionIDs(ctx context.Context, limit int) ([]int64, error)
	ListClarifiedRequirements(ctx context.Context, versionID int64) ([]RequirementRef, error)
	ListActiveScopes(ctx context.Context, versionID int64) ([]Scope, error)
	ListRequirementCoverage(ctx context.Context, versionID int64) ([]RequirementCoverage, error)
	LatestDesignConversation(ctx context.Context, versionID int64) (int64, bool, error)
	AutoDesignEnabled(ctx context.Context, versionID int64) (bool, error)
	GetRoundByConversation(ctx context.Context, conversationID int64) (*Round, error)
	GetRound(ctx context.Context, versionID, roundID int64) (*Round, error)
	GetLatestRound(ctx context.Context, versionID int64) (*Round, error)
	ListRounds(ctx context.Context, versionID int64) ([]Round, error)
	CreateRound(ctx context.Context, round Round) (Round, error)
	UpdateRoundRequirements(ctx context.Context, roundID int64, requirementIDs, requirementVersionIDs []int64) error
	UpsertScope(ctx context.Context, scope Scope) (Scope, bool, error)
	CountPendingDesign(ctx context.Context, versionID int64) (int, error)
	CaseCounts(ctx context.Context, versionID int64) (CaseCounts, error)
}

// DataPlanRepository rewrites a draft case version for the data planner.
type DataPlanRepository interface {
	// CurrentCaseVersion loads one case version that is the current version of
	// its case inside the version scope.
	CurrentCaseVersion(ctx context.Context, versionID, caseVersionID int64) (CaseVersion, error)
	// InsertDataPlanRevision inserts a prepared draft revision of
	// baseCaseVersionID and moves the case's current pointer to it. It
	// re-validates current+draft+revision inside the transaction and returns
	// design.ErrStateConflict when the base revision changed.
	InsertDataPlanRevision(ctx context.Context, versionID, baseCaseVersionID int64, revision CaseVersion, at time.Time) (CaseVersion, error)
}

// Repository is the full design persistence port consumed by the application
// services. It is deliberately composed of narrow role interfaces so tests can
// fake only the behaviour under test.
type Repository interface {
	GraphRepository
	ProposeRepository
	ConfirmationRepository
	StaleRepository
	PlannerRepository
	DataPlanRepository
}
