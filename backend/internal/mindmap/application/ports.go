// Package application holds the P5 mindmap use cases and every port they consume
// from the outside world: persistence repositories, the version lifecycle and
// requirement reader, the resource/code-intel/agent gateways, the event and
// audit sinks, the id generator and the clock.
//
// This file is the single home of the P5 cross-context port declarations
// (plan Shared Interfaces). It contains value types and interfaces only: no
// pgx, HTTP or other-context imports. Adapters live in infrastructure and are
// wired in the composition root. Later P5 tasks consume these declarations as
// they are; adding a port method is a Lead decision, not a task-local one.
package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// ProfileSeed is the profile seed consumed by AgentRuntime.EnsureProfile. The
// single source of truth stays in mindmap/domain (P5Profiles).
type ProfileSeed = domain.AgentProfileSeed

// VersionSnapshot is the version projection the mindmap context needs. The
// version status drives editability and the review state machine; the resource
// bindings and code baseline commit feed run context assembly.
type VersionSnapshot struct {
	ID                 int64
	Name               string
	Status             string
	SystemID           *int64
	EnvironmentID      *int64
	CodeRepositoryID   *int64
	CodeBaselineCommit *string
}

// RequirementSnapshot is one confirmed requirement injected into run context.
type RequirementSnapshot struct {
	ID                 int64
	VersionID          int64
	Title              string
	Description        string
	AcceptanceCriteria string
	Priority           string
	Status             string
	// AttachmentSummary is a short plain-text digest of the requirement's text
	// attachments (images are not injected; spec §8.3).
	AttachmentSummary string
}

// RelationSnapshot is one requirement relation injected into run context.
type RelationSnapshot struct {
	ID                int64
	VersionID         int64
	FromRequirementID int64
	ToRequirementID   int64
	RelationType      string
	Description       string
}

// EnvironmentSnapshot is the resource environment projection used by the
// exploration service and the run context.
type EnvironmentSnapshot struct {
	ID           int64
	SystemID     int64
	Name         string
	BaseURL      string
	Kind         string
	AllowedHosts []string
}

// AppModelSnapshot is one application model element injected into run context.
type AppModelSnapshot struct {
	ID       int64
	SystemID int64
	ParentID *int64
	Key      string
	Kind     string
	Name     string
	Path     string
	Method   string
	Source   string
}

// AppModelInput is one application model element proposed by the exploration
// agent. Source is set by the adapter to 'explored' when importing.
type AppModelInput struct {
	Key       string
	Kind      string
	Name      string
	Path      string
	Method    string
	ParentKey string
	Meta      json.RawMessage
	Source    string
}

// SessionSnapshot is the P2 session projection used by the exploration service.
// Cookies is parsed from the session storage state at adapter time; only
// cookies are ever sent (no localStorage or token injection; spec §6.9).
type SessionSnapshot struct {
	ID            int64
	State         string
	EnvironmentID int64
	Cookies       map[string]string
}

// ImpactSnapshot is the latest P4 code analysis projection. A zero snapshot with
// a nil error means the version has no analysis yet.
type ImpactSnapshot struct {
	AnalysisID int64
	BaseCommit string
	HeadCommit string
	Status     string
	Degraded   bool
}

// ImpactCandidate is one P4 impact candidate used to derive impactCases.
type ImpactCandidate struct {
	ID          int64
	SubjectKind string
	SubjectID   int64
	Reason      string
	Confidence  string
	Source      string
}

// FactSummary is one read-only code fact injected into run context.
type FactSummary struct {
	Kind   string
	Key    string
	Path   string
	Name   string
	Detail string
}

// AnchorParams is the input of a case code-anchor creation. SubjectID is the
// case node id; the adapter always writes subject_kind='case'.
type AnchorParams struct {
	SubjectID  int64
	FilePath   string
	StartLine  *int
	EndLine    *int
	Confidence string
}

// Anchor is the projection of a code anchor attached to a case node.
type Anchor struct {
	ID         int64
	VersionID  int64
	RepoID     int64
	SubjectID  int64
	FilePath   string
	StartLine  *int
	EndLine    *int
	CommitSHA  *string
	Confidence string
	CreatedBy  int64
}

// Event is one mindmap event published on the version channel
// (channel = version:{VersionID}, spec §6.11). Detail carries ids/counts/status
// summaries only: never mindmap bodies, LLM input/output or export content.
type Event struct {
	VersionID  int64
	Type       string
	Summary    string
	RefCaseIDs []int64
	ActorID    int64
	Source     string
	Detail     map[string]any
}

// AuditEvent is one audit entry. The same body rules as Event apply: no export
// or LLM content is ever recorded (D30).
type AuditEvent struct {
	Action     string
	ActorID    int64
	TargetType string
	TargetID   string
	Metadata   map[string]any
}

// RunPatch is the optional part of a run status transition. Nil fields are left
// unchanged by the repository.
type RunPatch struct {
	AgentSessionID *int64
	AgentRunID     *int64
	Model          *string
	PromptVersion  *string
	Input          json.RawMessage
	Result         json.RawMessage
	Stats          json.RawMessage
	Errors         json.RawMessage
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// AgentRunStatus is the terminal/running state of the P1 run behind an agent
// session.
type AgentRunStatus struct {
	ID     int64
	Status string
	Error  string
}

// AgentSessionStatus is the P1 agent session projection used by the run
// coordinator. Status is the session status; LastRun is nil before the first
// run.
type AgentSessionStatus struct {
	SessionID int64
	Status    string
	LastRun   *AgentRunStatus
}

// NodeRepository persists case_node rows and owns the node revision CAS.
type NodeRepository interface {
	Create(ctx context.Context, n domain.CaseNode) (domain.CaseNode, error)
	Get(ctx context.Context, versionID, nodeID int64) (domain.CaseNode, error)
	ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseNode, error)
	// Update compares expectedRevision with the stored revision (CAS) and bumps
	// revision by one. A mismatch is a case_revision_conflict.
	Update(ctx context.Context, n domain.CaseNode, expectedRevision int) (domain.CaseNode, error)
	// Delete removes the node when its stored revision matches expectedRevision.
	Delete(ctx context.Context, versionID, nodeID int64, expectedRevision int) error
	// UpdateStatusByVersion moves every node of the version from one status to
	// another without a per-node CAS (review freeze and transitions) and returns
	// the number of affected rows.
	UpdateStatusByVersion(ctx context.Context, versionID int64, from, to string) (int, error)
	// RestoreStatusByVersion rewrites status and revision exactly for the given
	// nodes. It exists only for the approve compensation path (spec §6.4).
	RestoreStatusByVersion(ctx context.Context, versionID int64, nodes []domain.CaseNode) error
	SetSortOrders(ctx context.Context, versionID int64, ordered []int64) error
	CountByVersion(ctx context.Context, versionID int64) (nodes int, cases int, err error)
}

// StepRepository persists case_step rows.
type StepRepository interface {
	ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseStep, error)
	Create(ctx context.Context, s domain.CaseStep) (domain.CaseStep, error)
	Update(ctx context.Context, s domain.CaseStep) error
	Delete(ctx context.Context, nodeID, stepID int64) error
	// ReplaceSeq rewrites the 1-based seq of the given steps in order inside one
	// transaction.
	ReplaceSeq(ctx context.Context, nodeID int64, ordered []int64) error
	CountByNode(ctx context.Context, nodeID int64) (int, error)
}

// AssertionRepository persists case_assertion rows.
type AssertionRepository interface {
	ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseAssertion, error)
	Create(ctx context.Context, a domain.CaseAssertion) (domain.CaseAssertion, error)
	Update(ctx context.Context, a domain.CaseAssertion) error
	Delete(ctx context.Context, nodeID, assertionID int64) error
	CountByNode(ctx context.Context, nodeID int64) (int, error)
}

// SourceRepository persists case_source rows.
type SourceRepository interface {
	ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseSource, error)
	Create(ctx context.Context, s domain.CaseSource) (domain.CaseSource, error)
	Delete(ctx context.Context, nodeID, sourceID int64) error
	// CountRequirementSources counts ref_kind='requirement' sources of one node;
	// deleting the last one is invalid_case_tree (spec §6.2).
	CountRequirementSources(ctx context.Context, nodeID int64) (int, error)
}

// ReviewRepository persists case_review rows.
type ReviewRepository interface {
	Create(ctx context.Context, r domain.CaseReview) (domain.CaseReview, error)
	GetLatest(ctx context.Context, versionID int64) (domain.CaseReview, error)
	GetApproved(ctx context.Context, versionID int64) (domain.CaseReview, error)
	Decide(ctx context.Context, reviewID int64, status string, comment string, actor int64) (domain.CaseReview, error)
	List(ctx context.Context, versionID int64, limit int, cursor string) ([]domain.CaseReview, string, bool, error)
}

// FindingRepository persists case_review_finding rows.
type FindingRepository interface {
	CreateBatch(ctx context.Context, findings []domain.CaseReviewFinding) error
	List(ctx context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error)
	UpdateStatus(ctx context.Context, findingID int64, status string, actor int64) (domain.CaseReviewFinding, error)
	// DismissOpenAI dismisses all still-open AI findings of the version
	// (superseded by a newer review run) and returns the number dismissed.
	DismissOpenAI(ctx context.Context, versionID int64) (int, error)
	CountOpenBlockers(ctx context.Context, versionID int64) (int, error)
}

// BaselineRepository persists the single case_baseline row of a version.
type BaselineRepository interface {
	Create(ctx context.Context, b domain.CaseBaseline) (domain.CaseBaseline, error)
	GetByVersion(ctx context.Context, versionID int64) (domain.CaseBaseline, error)
	DeleteByVersion(ctx context.Context, versionID int64) error
}

// RunRepository persists case_design_run rows.
type RunRepository interface {
	Create(ctx context.Context, r domain.CaseDesignRun) (domain.CaseDesignRun, error)
	Get(ctx context.Context, versionID, runID int64) (domain.CaseDesignRun, error)
	List(ctx context.Context, versionID int64, kind, status string, limit int, cursor string) ([]domain.CaseDesignRun, string, bool, error)
	// UpdateStatus is a CAS transition: it applies patch only when the stored
	// status equals from and returns the updated row.
	UpdateStatus(ctx context.Context, runID int64, from, to string, patch RunPatch) (domain.CaseDesignRun, error)
	LatestByKind(ctx context.Context, versionID int64, kind string) (domain.CaseDesignRun, error)
	ActiveByKind(ctx context.Context, versionID int64, kind string) (domain.CaseDesignRun, error)
	// ListActive returns queued/running runs across versions (oldest first),
	// bounded by limit. The run worker uses it to poll agent sessions.
	ListActive(ctx context.Context, limit int) ([]domain.CaseDesignRun, error)
	// RecoverStale marks queued/running runs updated before the cutoff as failed
	// and returns the number recovered.
	RecoverStale(ctx context.Context, before time.Time) (int64, error)
}

// ExportRepository persists case_export rows.
type ExportRepository interface {
	Create(ctx context.Context, e domain.CaseExport) (domain.CaseExport, error)
	Get(ctx context.Context, versionID, exportID int64) (domain.CaseExport, error)
	List(ctx context.Context, versionID int64, format string, limit int, cursor string) ([]domain.CaseExport, string, bool, error)
	// FindByBaselineFormat returns the reusable export of the same
	// (version, format, baseline). A nil baselineID matches an unfrozen export.
	FindByBaselineFormat(ctx context.Context, versionID int64, format string, baselineID *int64) (domain.CaseExport, error)
}

// GraphTx exposes the repositories bound to one database transaction. It is how
// materialization and baseline freezing write the whole graph atomically.
type GraphTx interface {
	Nodes() NodeRepository
	Steps() StepRepository
	Assertions() AssertionRepository
	Sources() SourceRepository
	Reviews() ReviewRepository
	Findings() FindingRepository
	Baselines() BaselineRepository
	Runs() RunRepository
}

// GraphRepository runs a function inside one database transaction. The callback
// must not issue events: events are published only after the commit.
type GraphRepository interface {
	InTx(ctx context.Context, fn func(GraphTx) error) error
}

// VersionLifecycle is the mindmap context's write port into the version state
// machine (spec §6.3). The mindmap context never updates the version table
// directly.
type VersionLifecycle interface {
	GetVersion(ctx context.Context, versionID int64) (VersionSnapshot, error)
	EnterReview(ctx context.Context, versionID, actor int64) (VersionSnapshot, error)
	RejectReview(ctx context.Context, versionID, actor int64) (VersionSnapshot, error)
	ApproveBaseline(ctx context.Context, versionID, actor int64) (VersionSnapshot, error)
}

// RequirementReader is the read-only requirement port: confirmed requirements
// (status='clarified') and their relations.
type RequirementReader interface {
	ListConfirmed(ctx context.Context, versionID int64) ([]RequirementSnapshot, error)
	ListRelations(ctx context.Context, versionID int64) ([]RelationSnapshot, error)
}

// CatalogGateway is the P2 resource catalog port. The mindmap context reads
// environments/app models and imports explored app model nodes (admin-only,
// spec §6.9).
type CatalogGateway interface {
	GetEnvironment(ctx context.Context, envID int64) (EnvironmentSnapshot, error)
	ListAppModels(ctx context.Context, systemID int64) ([]AppModelSnapshot, error)
	ImportAppModels(ctx context.Context, systemID int64, inputs []AppModelInput) (created, updated int, err error)
}

// SessionGateway is the P2 session port: the exploration service only reads
// session cookies of a valid/expiring session.
type SessionGateway interface {
	GetForExplore(ctx context.Context, sessionID int64) (SessionSnapshot, error)
}

// CodeFactsGateway is the read-only P4 code intelligence port.
type CodeFactsGateway interface {
	GetLatestAnalysis(ctx context.Context, versionID int64) (ImpactSnapshot, error)
	ListCandidates(ctx context.Context, analysisID int64) ([]ImpactCandidate, error)
	// FileExists validates a path against the version's code baseline commit.
	FileExists(ctx context.Context, versionID int64, path string) (bool, error)
	ListFacts(ctx context.Context, versionID int64, limit int) ([]FactSummary, error)
}

// CodeAnchorGateway manages case-anchor rows through the codeintel anchor
// service (subject_kind='case').
type CodeAnchorGateway interface {
	Create(ctx context.Context, versionID, actorID int64, p AnchorParams) (Anchor, error)
	List(ctx context.Context, versionID int64, nodeID int64) ([]Anchor, error)
	Delete(ctx context.Context, versionID, anchorID int64) error
}

// AgentRuntime is the P1 agent runtime port: profile seeding, sessions, text
// runs, status polling and abort.
type AgentRuntime interface {
	EnsureProfile(ctx context.Context, profile ProfileSeed) error
	CreateSession(ctx context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error)
	StartText(ctx context.Context, sessionID int64, text string, actor int64) (int64, error)
	GetSession(ctx context.Context, sessionID int64) (AgentSessionStatus, error)
	Abort(ctx context.Context, sessionID int64, actor int64) error
}

// EventPublisher publishes one mindmap event on the version channel. Publishing
// happens after the database commit and its failure is only logged, never
// returned to the caller.
type EventPublisher interface {
	Publish(ctx context.Context, e Event) error
}

// AuditRecorder records one mindmap audit entry. Like EventPublisher, a failure
// is logged but never fails the request.
type AuditRecorder interface {
	Record(ctx context.Context, e AuditEvent) error
}

// IDGenerator returns the next Snowflake ID. Every P5 id is generated in the
// application and passed explicitly to the repository (D29).
type IDGenerator interface {
	Next() int64
}

// Clock returns the current time; injectable so services can be tested without
// sleeping.
type Clock interface {
	Now() time.Time
}
