// Package application implements the codeintel use cases (index, analysis,
// impact and anchors) on top of the codeintel domain. This file declares the
// cross-context and platform ports the application layer depends on; the
// infrastructure adapters of this context implement them and the composition
// root injects them. It deliberately imports only the standard library and the
// codeintel domain, never another context's infrastructure or interfaces.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// GitReader is the read-only Git port (design spec §3.5). It reads commit
// trees, blobs, diffs, commits and branch tips; it never mutates the working
// tree. The infrastructure/gitx adapter implements it.
type GitReader interface {
	Head(ctx context.Context, localPath, branch string) (string, error)
	IsAncestor(ctx context.Context, localPath, base, head string) (bool, error)
	Tree(ctx context.Context, localPath, commit, prefix string, include, exclude []string, cursor string, limit int) (domain.TreePage, error)
	File(ctx context.Context, localPath, commit, path string, startLine, endLine int) (domain.FileWindow, error)
	ReadAll(ctx context.Context, localPath, commit, path string, maxBytes int) ([]byte, bool, error)
	Diff(ctx context.Context, localPath, base, head string) (domain.ChangeSetData, error)
	FileDiff(ctx context.Context, localPath, base, head, path string, maxBytes int) (domain.FileDiff, error)
	Commit(ctx context.Context, localPath, sha string) (domain.CommitInfo, error)
}

// LLMCompleter is the single-shot chat-completions port used by the T1 file
// analysis and the AI impact reasoning (design spec §6.2). The
// infrastructure/llm gateway implements it over the llm context's default
// configuration; callers must degrade the run when it reports
// ErrLLMNotConfigured.
type LLMCompleter interface {
	Complete(ctx context.Context, system, user string) (string, error)
	Model() string
}

// ErrLLMNotConfigured marks the absence of a usable default LLM configuration
// (or a configuration that cannot serve chat completions). It lets the
// analysis service degrade to T0-only results instead of failing the run:
// design spec §6.2 requires "未配置/配置缺失/协议不可用 -> degraded" without
// retrying to the timeout. The underlying llm.ErrConfigNotFound is preserved
// for errors.Is when one is available.
var ErrLLMNotConfigured = errors.New("codeintel: LLM is not configured")

// RepoInfo is the slice of a P2 code repository the codeintel context needs.
type RepoInfo struct {
	ID            int64
	Key           string
	Name          string
	LocalPath     string
	FixedBranch   string
	IncludeGlobs  []string
	ExcludeGlobs  []string
	Status        string
	LastIndexedAt *time.Time
}

// RepoCatalog reads P2 code repositories through the resource application
// layer. Get returns one projection; List drains the P2 cursor pages so the
// repository list endpoint can serve every configured repository.
type RepoCatalog interface {
	Get(ctx context.Context, repoID int64) (RepoInfo, error)
	List(ctx context.Context) ([]RepoInfo, error)
}

// IndexStateWriter records the last successful index state without touching any
// other P2 column.
type IndexStateWriter interface {
	MarkIndexed(ctx context.Context, repoID int64, commit string, at time.Time) error
}

// AppModelNode is one page/route/api/component node of the P2 application
// model.
type AppModelNode struct {
	ID       int64
	SystemID int64
	Kind     string
	Key      string
	Name     string
	Path     string
	Method   string
}

// AppModelCatalog lists the application-model nodes of one system.
type AppModelCatalog interface {
	ListBySystem(ctx context.Context, systemID int64, kinds []string) ([]AppModelNode, error)
}

// VersionInfo is the slice of a P3 version binding the codeintel context needs.
type VersionInfo struct {
	ID             int64
	SystemID       int64
	RepoID         int64
	HasSystem      bool
	HasRepo        bool
	Branch         string
	BaselineCommit string
}

// RequirementInfo is one requirement of a version for the AI impact prompt.
type RequirementInfo struct {
	ID          int64
	Title       string
	Description string
	Status      string
}

// RelationInfo is one directed requirement relation for the one-hop impact
// expansion.
type RelationInfo struct {
	FromID int64
	ToID   int64
	Type   string
}

// VersionCatalog reads version bindings, requirements and requirement relations
// through the version application layer.
type VersionCatalog interface {
	Get(ctx context.Context, versionID int64) (VersionInfo, error)
	ListRequirements(ctx context.Context, versionID int64) ([]RequirementInfo, error)
	ListRelations(ctx context.Context, versionID int64) ([]RelationInfo, error)
}

// ChangedEvent is the payload of the version-scoped code.changed event.
type ChangedEvent struct {
	AnalysisID int64
	BaseCommit string
	HeadCommit string
	Stats      []byte
	Paths      []string
}

// ImpactEvent is the payload of the version-scoped code.impact_computed event.
type ImpactEvent struct {
	AnalysisID      int64
	AnalysisVersion int64
	Status          string
	Degraded        bool
	Counts          map[string]int
	Paths           []string
}

// ImpactNotifier publishes the two version-scoped SSE events. It carries only
// summaries: never code content, prompts or model responses (design spec §6.9).
type ImpactNotifier interface {
	Changed(ctx context.Context, versionID int64, e ChangedEvent) error
	ImpactComputed(ctx context.Context, versionID int64, e ImpactEvent) error
}

// AuditRecorder records code reads and analyses through the audit application
// layer. The detail map carries identifiers and status only, never code
// content.
type AuditRecorder interface {
	Record(ctx context.Context, actorID int64, action, subject string, detail map[string]any) error
}

// IDGenerator produces snowflake ids for the codeintel aggregates.
type IDGenerator interface {
	Next() int64
}

// Clock supplies the current time so tests can run deterministically.
type Clock interface {
	Now() time.Time
}
