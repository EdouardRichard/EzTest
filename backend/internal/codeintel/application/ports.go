// Package codeintelapp implements the code-intelligence use cases: T0 index
// runs, T1 fact analysis, impact analysis, anchors and fact reviews. It depends
// only on the codeintel domain types and on the ports declared here;
// infrastructure adapters convert the other contexts' types.
package codeintelapp

import (
	"context"
	"encoding/json"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// GitClient is the read-only T0 git surface (spec §5.1). repoPath is the local
// repository root; every other path argument is a normalized repo-relative
// path. The adapter must resolve refs (never trust them), use argv without a
// shell and enforce the per-command timeout.
type GitClient interface {
	ResolveCommit(ctx context.Context, repoPath, ref string) (string, error)
	HeadCommit(ctx context.Context, repoPath, branch string) (string, error)
	IsAncestor(ctx context.Context, repoPath, base, head string) (bool, error)
	ListTree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) (codeintel.TreeResult, error)
	Diff(ctx context.Context, repoPath, base, head string, max int) (codeintel.DiffResult, error)
	ReadFile(ctx context.Context, repoPath, commit, path string, start, end, maxBytes int) (codeintel.FileWindow, error)
	FileDiff(ctx context.Context, repoPath, base, head, path string, maxBytes int) (codeintel.FileDiff, error)
}

// CommitReader is the additive P5-t7 read-only commit-metadata surface used by
// the T0 commit view. It is a separate port (not a GitClient method) so
// existing GitClient fakes stay valid. Implemented by infrastructure/gitx.
type CommitReader interface {
	Commit(ctx context.Context, repoPath, sha string, maxMessage int) (codeintel.CommitInfo, error)
}

// FileWindowReader is the additive P5-t8 bulk file read used by T1 analysis.
// It reads up to maxBytes of ordinary UTF-8 text without the API's 400-line
// window (maxLines<=0 means no line cap) and rejects larger blobs as
// file_too_large. Implemented by infrastructure/gitx.
type FileWindowReader interface {
	ReadFileWindow(ctx context.Context, repoPath, commit, path string, maxBytes, maxLines int) (codeintel.FileWindow, error)
}

// ModelResolver optionally reports the model name a FactExtractor resolved for
// its calls; the analysis row records it without a chat round-trip per file.
type ModelResolver interface {
	Model(ctx context.Context) (string, error)
}

// FactExtractor is the T1 AI layer: per-file fact extraction and the single
// impact inference call (spec §5.2/§5.4). The model never supplies excerpt
// hashes; the service re-reads the commit content.
type FactExtractor interface {
	Extract(ctx context.Context, in FactExtractInput) (FactExtractOutput, error)
	Impact(ctx context.Context, in ImpactInferenceInput) (ImpactInferenceOutput, error)
}

// FactExtractInput is one per-file extraction call. Lines is the file content at
// CommitSHA split on "\n" (the caller applies the file/line limits).
type FactExtractInput struct {
	Repo            RepoRef
	CommitSHA       string
	Path            string
	Lines           []string
	MaxFacts        int
	MaxEdges        int
	MaxExcerptLines int
	PromptVersion   string
}

// FactExtractOutput is one parsed model response. InvalidFacts counts facts the
// model proposed that could not be normalized.
type FactExtractOutput struct {
	Facts        []FactDraft
	Edges        []EdgeDraft
	InvalidFacts int
}

// FactDraft is one model-proposed fact before server-side validation.
type FactDraft struct {
	Kind       codeintel.EntityKind
	Name       string
	Signature  string
	Path       string
	StartLine  int
	EndLine    int
	Body       json.RawMessage
	Confidence codeintel.Confidence
	Evidence   []EvidenceDraft
}

// EvidenceDraft is a model-proposed evidence range. The server validates the
// range against the commit and recomputes ExcerptHash; model hashes are ignored.
type EvidenceDraft struct {
	File      string
	StartLine int
	EndLine   int
}

// EdgeDraft is one model-proposed relation between two fact keys.
type EdgeDraft struct {
	SrcFactKey string
	DstFactKey string
	Rel        codeintel.EdgeRel
	Confidence codeintel.Confidence
	Evidence   []EvidenceDraft
}

// ImpactInferenceInput is the single AI impact call of one analysis (spec §5.4):
// the changed summary, facts/edges and a bounded requirement list.
type ImpactInferenceInput struct {
	Repo            RepoRef
	CommitSHA       string
	ChangedFiles    []codeintel.ChangedFile
	Facts           []codeintel.Fact
	Edges           []codeintel.Edge
	Requirements    []RequirementRef
	Selected        []codeintel.ImpactCandidate
	MaxRequirements int
	PromptVersion   string
}

// ImpactInferenceOutput is one parsed model response of the impact call.
type ImpactInferenceOutput struct {
	Candidates        []ImpactDraft
	InvalidCandidates int
}

// ImpactDraft is one model-proposed impact candidate before server validation.
type ImpactDraft struct {
	SubjectKind codeintel.SubjectKind
	SubjectID   int64
	Reason      string
	Confidence  codeintel.Confidence
	Evidence    []EvidenceDraft
}

// WorkItem is one claimed codeintel work item (task `index.repo`/`code.analyze`).
type WorkItem struct {
	ID          int64
	VersionID   int64
	Type        string
	Payload     json.RawMessage
	Attempts    int
	MaxAttempts int
	LeaseEpoch  int64
}

// NewWorkItem is one deduplicated work item to ensure. Team is always "code".
type NewWorkItem struct {
	VersionID   int64
	Team        string
	Type        string
	Priority    int
	DedupeKey   string
	Payload     any
	MaxAttempts int
}

// WorkQueue is the codeintel worker's port to the agent task board (P1
// work_item). ClaimForTeams must never return work of another team.
type WorkQueue interface {
	Ensure(ctx context.Context, in NewWorkItem) (WorkItem, bool, error)
	ClaimForTeams(ctx context.Context, claimant string, teams []string) (WorkItem, bool, error)
	MarkRunning(ctx context.Context, id, epoch int64) error
	Renew(ctx context.Context, id, epoch int64, lease time.Duration) error
	Complete(ctx context.Context, id, epoch int64) error
	Fail(ctx context.Context, id, epoch int64, message string, retryable bool, backoff time.Duration) error
}

// VersionReader is the codeintel view of the version context (spec §5.5).
type VersionReader interface {
	VersionSUTBindings(ctx context.Context, versionID int64) ([]VersionSUTBinding, error)
	SetCodeBaselineCommit(ctx context.Context, versionSUTID int64, commit string) error
}

// VersionSUTBinding is one version_sut binding with its code repository.
// CodeBaselineCommit is empty when the baseline could not be resolved.
type VersionSUTBinding struct {
	VersionSUTID       int64
	VersionID          int64
	SUTID              int64
	RepoID             int64
	Branch             string
	CodeBaselineCommit string
}

// DesignReader is the codeintel view of the design context: requirements,
// requirement relations and current+confirmed case versions of a version.
type DesignReader interface {
	Requirements(ctx context.Context, versionID int64) ([]RequirementRef, error)
	CaseVersions(ctx context.Context, versionID int64) ([]CaseVersionRef, error)
	RequirementRelations(ctx context.Context, versionID int64) ([]RequirementRelationRef, error)
}

// RequirementRef is one non-obsolete requirement of a version.
type RequirementRef struct {
	ID     int64
	Title  string
	Status string
}

// RequirementRelationRef is one requirement_relation edge of a version.
type RequirementRelationRef struct {
	FromRequirementID int64
	ToRequirementID   int64
	Kind              string
}

// CaseVersionRef is one current+confirmed case version of a version.
type CaseVersionRef struct {
	ID            int64
	CaseID        int64
	RequirementID int64
	Status        string
}

// ResourceReader is the codeintel view of the resource context.
type ResourceReader interface {
	SUTPages(ctx context.Context, versionSUTID int64) ([]PageRef, error)
	SUTAPIs(ctx context.Context, versionSUTID int64) ([]APIRef, error)
	CodeRepository(ctx context.Context, repoID int64) (RepoRef, error)
}

// RepoRef is one code_repository row with its index configuration. The
// CreatedBy/CreatedAt/UpdatedAt fields are the additive P5-T7 view fields used
// by CodeRepoStatusView.repository.
type RepoRef struct {
	ID        int64
	Name      string
	Path      string
	Branch    string
	Include   []string
	Exclude   []string
	Status    string
	CreatedBy int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PageRef is one version SUT page.
type PageRef struct {
	ID            int64
	Name          string
	RouteTemplate string
	Status        string
	Source        string
}

// APIRef is one version SUT API.
type APIRef struct {
	ID           int64
	Name         string
	Method       string
	PathTemplate string
	Status       string
	Source       string
}

// ExecutionReader is the codeintel view of the execution context (proposals and
// evidence metadata; never evidence bytes).
type ExecutionReader interface {
	NetworkEvidence(ctx context.Context, versionID int64) ([]NetworkRequest, error)
	EvidenceMeta(ctx context.Context, versionID, evidenceID int64) (EvidenceRef, error)
}

// NetworkRequest is one captured network evidence request.
type NetworkRequest struct {
	ID           int64
	CaseRunID    int64
	VersionSUTID *int64
	Method       string
	URL          string
	Status       int
}

// EvidenceRef is one immutable evidence metadata row.
type EvidenceRef struct {
	ID            int64
	CaseRunID     int64
	CaseVersionID int64
	VersionSUTID  *int64
	Kind          string
	MIME          string
	SHA256        string
	SizeBytes     int64
}

// Event is one codeintel event (spec §4.3). The adapter publishes it on the
// version channel (system when VersionID is 0) and the payload never carries
// code bodies, credentials or filesystem paths.
type Event struct {
	Type      string
	VersionID int64
	Scope     map[string]any
	Payload   map[string]any
	ActorID   *int64
	RequestID string
}

// EventPublisher publishes one codeintel event. Publish failures never roll
// back the business transaction.
type EventPublisher interface {
	Publish(ctx context.Context, event Event) error
}

// AuditEntry is one best-effort codeintel audit record.
type AuditEntry struct {
	ActorID    *int64
	Action     string
	TargetKind string
	TargetID   string
	RequestID  string
	Metadata   map[string]any
}

// AuditRecorder records the best-effort audit entry of one codeintel write.
type AuditRecorder interface {
	Record(ctx context.Context, entry AuditEntry) error
}

// SettingsReader is the codeintel view of the settings center (codeintel.* and
// visual.* keys; settings may only lower the hard domain limits).
type SettingsReader interface {
	String(ctx context.Context, key string) (string, error)
	Int(ctx context.Context, key string) (int64, error)
	Float(ctx context.Context, key string) (float64, error)
	Raw(ctx context.Context, key string) (json.RawMessage, error)
}

// Clock is the injectable time source used by the codeintel services.
type Clock interface {
	Now() time.Time
}

// IDGenerator hands out snowflake IDs.
type IDGenerator interface {
	Next() int64
}
