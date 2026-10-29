package domain

import (
	"context"
	"time"
)

// ChangeSetRepository persists the deterministic file-level change sets.
type ChangeSetRepository interface {
	GetOrCreate(ctx context.Context, cs CodeChangeSet) (CodeChangeSet, bool, error)
	GetByID(ctx context.Context, id int64) (CodeChangeSet, error)
	// FindByCommits returns the stored change set of one deterministic
	// (repo, base, head) trio so the read-only §7.3 preview can return its id. A
	// trio without a stored row is reported as a not-found error.
	FindByCommits(ctx context.Context, repoID int64, base, head string) (CodeChangeSet, error)
}

// IndexRunRepository persists T0 repository index runs.
type IndexRunRepository interface {
	Create(ctx context.Context, r IndexRun) error
	Update(ctx context.Context, r IndexRun) error
	GetByID(ctx context.Context, repoID, id int64) (IndexRun, error)
	GetLatest(ctx context.Context, repoID int64) (IndexRun, error)
	RecoverStale(ctx context.Context, before time.Time) (int64, error)
}

// EntityRepository persists code entities and their current fact versions.
type EntityRepository interface {
	Upsert(ctx context.Context, e CodeEntity) error
	UpsertFile(ctx context.Context, repoID int64, commit string, files []CodeEntity) error
	GetByFactKey(ctx context.Context, repoID int64, commit, factKey string) (CodeEntity, error)
	List(ctx context.Context, repoID int64, f EntityFilter) ([]CodeEntity, string, bool, error)
	ListByPaths(ctx context.Context, repoID int64, commit string, paths []string) ([]CodeEntity, error)
	DeleteFact(ctx context.Context, repoID int64, commit, factKey string) error
}

// EdgeRepository persists the edges of one fact version.
type EdgeRepository interface {
	ReplaceFactEdges(ctx context.Context, repoID int64, commit, factKey string, version int, edges []CodeEdge) error
	ListByEntity(ctx context.Context, repoID int64, commit string, entityID int64) ([]CodeEdge, error)
	ListByRel(ctx context.Context, repoID int64, commit, rel string) ([]CodeEdge, error)
}

// FactVersionRepository owns the append-only history of AI fact bodies.
type FactVersionRepository interface {
	Current(ctx context.Context, repoID int64, commit, factKey string) (*CodeEntity, int, error)
	// GetVersion returns the canonical body stored for one fact version so the
	// service can compare a newly computed body with the persisted one before
	// deciding whether to reuse the version or bump it (design spec §6.4). A
	// version that has no stored body is reported as a not-found error.
	GetVersion(ctx context.Context, repoID int64, commit, factKey string, version int) ([]byte, error)
	// Create appends one fact version. The caller supplies the Snowflake id
	// minted in the application layer (D29); the repository never generates it.
	Create(ctx context.Context, id int64, repoID int64, commit, factKey, model, prompt string, version int, body []byte, analysisID int64) error
	Supersede(ctx context.Context, repoID int64, commit, factKey string, before int, at time.Time) error
}

// CacheRepository persists the file-level AI analysis cache keyed by
// (repo, commit, path, file hash, model, prompt version).
type CacheRepository interface {
	Get(ctx context.Context, repoID int64, commit, path, fileHash, model, prompt string) (*CachedFile, bool, error)
	Upsert(ctx context.Context, c CachedFile) error
}

// CachedFile is one successful cached file analysis. The caller supplies ID,
// the Snowflake id minted in the application layer (D29).
type CachedFile struct {
	ID            int64
	RepoID        int64
	Commit        string
	Path          string
	FileHash      string
	Model         string
	PromptVersion string
	Facts         []byte // JSONB
	Stats         []byte // JSONB
	CreatedAt     time.Time
	LastUsedAt    time.Time
}

// AnalysisRepository persists version-scoped impact analyses.
type AnalysisRepository interface {
	Create(ctx context.Context, a CodeAnalysis) error
	Update(ctx context.Context, a CodeAnalysis) error
	GetByID(ctx context.Context, versionID, id int64) (CodeAnalysis, error)
	GetLatest(ctx context.Context, versionID int64) (CodeAnalysis, error)
	FindReusable(ctx context.Context, versionID, repoID int64, base, head, model, prompt string) (*CodeAnalysis, error)
	HasActive(ctx context.Context, versionID int64) (bool, error)
	NextAnalysisVersion(ctx context.Context, versionID int64, base, head string) (int, error)
	RecoverStale(ctx context.Context, before time.Time) (int64, error)
	UpdateCandidateSelection(ctx context.Context, analysisID, candidateID, actorID int64, selected bool) error
}

// CandidateRepository persists impact candidates of one analysis.
type CandidateRepository interface {
	Replace(ctx context.Context, analysisID int64, items []ImpactCandidate) error
	List(ctx context.Context, analysisID int64) ([]ImpactCandidate, error)
	Get(ctx context.Context, analysisID, candidateID int64) (ImpactCandidate, error)
}

// AnchorRepository persists version-scoped code anchors.
type AnchorRepository interface {
	Create(ctx context.Context, a CodeAnchor) error
	Delete(ctx context.Context, versionID, id int64) error
	Get(ctx context.Context, versionID, id int64) (CodeAnchor, error)
	List(ctx context.Context, versionID int64, f AnchorFilter) ([]CodeAnchor, error)
	ListByPaths(ctx context.Context, versionID int64, paths []string) ([]CodeAnchor, error)
}

// VersionChangeRepository persists the P8 per-batch code-change snapshots and
// serves the read-only run_batch projection the snapshot generation needs
// (design spec §4.2/§7.2, D58). Upsert is keyed by (version_id, batch_id) and
// never overwrites an existing snapshot; the second return value reports whether
// this call created the row.
type VersionChangeRepository interface {
	ListByVersion(ctx context.Context, versionID int64) ([]VersionChange, error)
	Upsert(ctx context.Context, change VersionChange) (VersionChange, bool, error)
	// ListBatches returns the version's run_batch projection ordered by batch
	// number. It never writes execution state.
	ListBatches(ctx context.Context, versionID int64) ([]VersionBatch, error)
}
