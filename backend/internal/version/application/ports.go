// Package application holds the version context's use cases and the ports they
// consume from the outside world: persistence (version/domain repositories),
// the P2 resource catalog, the read-only Git reader, the P1 agent sessions, the
// event publisher and the attachment filesystem store.
//
// This file is the single home of the P3 cross-context port declarations
// (spec §3.5). It contains types and interfaces only: adapters live in
// infrastructure and are wired in the composition root (T16). P3 application
// code must not import the P1/P2 application packages; it depends on these
// narrow, primitive ports instead.
package application

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Domain aliases keep the service signatures of the plan's Shared Interfaces
// unqualified while the single source of truth stays in version/domain.
type (
	// Version is a version aggregate.
	Version = domain.Version
	// VersionFilter selects a page of versions.
	VersionFilter = domain.VersionFilter
	// VersionBindings is the replaceable resource binding of a version.
	VersionBindings = domain.VersionBindings
	// Warnings is the enter-design soft gate.
	Warnings = domain.Warnings
	// VersionRepository is the persistence port for versions.
	VersionRepository = domain.VersionRepository
)

// ResourceRef is the minimal P2 resource projection the version context needs
// when validating a binding. Status is the P2 lifecycle status ("active" or
// "disabled"); Available is false when the resource has been deleted.
//
// SystemID is only set for environments: the catalog adapter must fill it with
// the owning system so version bindings can reject an environment that belongs
// to another system (spec §6.8). The ownership check is fail-closed: a nil
// SystemID on a bound environment is rejected as environment_system_mismatch
// rather than accepted unverified.
type ResourceRef struct {
	ID        int64
	Name      string
	Status    string
	SystemID  *int64
	Available bool
}

// CodeRepoRef is the P2 code repository projection. LocalPath and FixedBranch
// are used to resolve the baseline commit; IncludeGlobs/ExcludeGlobs/LastCommit
// are consumed by the P3 code tools (T13). Available is false when the
// repository has been deleted.
type CodeRepoRef struct {
	ID           int64
	Name         string
	Status       string
	LocalPath    string
	FixedBranch  string
	IncludeGlobs []string
	ExcludeGlobs []string
	LastCommit   string
	Available    bool
}

// ResourceCatalog is the read-only P2 resource lookup port (spec §3.5).
// Adapters report missing/disabled resources as *ResourceError.
type ResourceCatalog interface {
	GetSystem(ctx context.Context, id int64) (ResourceRef, error)
	GetEnvironment(ctx context.Context, id int64) (ResourceRef, error)
	GetCodeRepository(ctx context.Context, id int64) (CodeRepoRef, error)
}

// ResourceError is returned by a ResourceCatalog adapter when a P2 resource
// cannot be used. Reason is the stable spec §7.1 details.reason value:
// system_not_found, system_disabled, environment_not_found,
// environment_system_mismatch, code_repository_not_found,
// code_repository_disabled or resource_unavailable.
type ResourceError struct {
	Reason  string
	Message string
}

// Error implements error.
func (e *ResourceError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

// GitReader is the read-only local Git port (spec §3.5). It never mutates the
// checkout; implementations run git with GIT_OPTIONAL_LOCKS=0 and a timeout.
// Adapters report failures as *GitError with a spec §7.1 reason
// (branch_not_found, path_not_found, path_not_git_repo or resource_unavailable).
type GitReader interface {
	ResolveCommit(ctx context.Context, repoPath, branch string) (string, error)
	Tree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) ([]CodeEntry, error)
	Read(ctx context.Context, repoPath, commit, path string, maxBytes int) (string, error)
}

// CodeEntry is one entry of a code tree listing. Type is "blob" or "tree".
type CodeEntry struct {
	Path string
	Type string
	Size int64
}

// GitError is returned by a GitReader adapter when a Git read fails. Reason is
// the stable spec §7.1 details.reason value.
type GitError struct {
	Reason  string
	Message string
}

// Error implements error.
func (e *GitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

// VersionEvent is the version context's event payload (spec §6.11). The
// eventing adapter fixes the channel to version:{VersionID} and maps the
// optional refs to eventing.Refs. Detail is free-form (bounded by the eventing
// domain's 64 KiB limit) and carries per-type keys such as changed/from/to.
type VersionEvent struct {
	VersionID        int64
	Type             string
	Summary          string
	RefRequirementID *int64
	RefSessionID     *int64
	RefDecisionID    *int64
	RefAttachmentID  *int64
	ActorID          int64
	Source           string
	Detail           map[string]any
}

// EventPublisher publishes one version event on the version channel (spec
// §6.10: events are published after the database commit and their failure is
// only logged, never returned to the caller).
type EventPublisher interface {
	Publish(ctx context.Context, e VersionEvent) error
}

// AgentPart is one part of a P1 agent message. Type is "text" or "image";
// ImageRef is an attachment:<id> reference resolved by P1 before the LLM call.
type AgentPart struct {
	Type     string
	Text     string
	ImageRef string
}

// AgentMessage is one projected P1 agent message (spec §7.5). ToolCalls is the
// raw JSON persisted by P1; P3 never parses or rewrites it.
type AgentMessage struct {
	ID        int64
	RunID     int64
	Role      string
	Parts     []AgentPart
	Reasoning string
	ToolCalls json.RawMessage
	ToolName  string
	CreatedAt time.Time
}

// AgentSessions is the P1 agent application port (spec §3.5/§8.2). Channel is
// "version:{id}" for every P3 session; versionID is the decimal string form.
type AgentSessions interface {
	Create(ctx context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error)
	StartText(ctx context.Context, agentSessionID int64, text string, actor int64) (int64, error)
	StartParts(ctx context.Context, agentSessionID int64, parts []AgentPart, actor int64) (int64, error)
	Messages(ctx context.Context, agentSessionID int64, beforeID int64, limit int) ([]AgentMessage, bool, error)
	HasRunningByChannel(ctx context.Context, channel string) (bool, error)
}

// SavedAttachment is the result of writing one attachment file to the version
// attachment root. StoragePath is relative to EZTEST_VERSION_ATTACHMENT_DIR.
type SavedAttachment struct {
	StoragePath string
	SHA256      string
	Size        int64
}

// AttachmentStore is the filesystem attachment port (spec §6.7). Save streams
// r to <root>/<versionID>/<attachmentID>_<safeFilename>, enforcing the size,
// path and permission rules; Open validates that storagePath stays inside the
// root and returns seekable content; Remove is best-effort.
type AttachmentStore interface {
	Save(ctx context.Context, versionID, attachmentID int64, filename string, r io.Reader) (SavedAttachment, error)
	Open(ctx context.Context, storagePath string) (io.ReadSeekCloser, error)
	Remove(ctx context.Context, storagePath string) error
}

// Clock returns the current time. It is injectable so services can be tested
// without sleeping. Version row timestamps are server-assigned by the
// repository; the clock is available to services that need wall-clock values.
type Clock interface {
	Now() time.Time
}

// IDGenerator returns the next Snowflake ID (spec §5.4/INV-15). P3 services
// generate every owned id in the application layer and pass it explicitly to
// the repository; tests inject a fixed sequence.
type IDGenerator interface {
	Next() int64
}
