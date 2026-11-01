package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P5 tool names (contracts/agent-profiles-tools.md §8.3). Nine code tools are
// strictly read-only; the candidate writers are sut.page_propose (created by
// this package) and execution.visual_assert (registered by the execution
// agent_tool package through RegisterP5ToolGroup).
const (
	ToolCodeTree              = "code.tree"
	ToolCodeRead              = "code.read"
	ToolCodeSearch            = "code.search"
	ToolCodeFacts             = "code.facts"
	ToolCodeRoutes            = "code.routes"
	ToolCodeAPIs              = "code.apis"
	ToolCodeDiff              = "code.diff"
	ToolCodeImpact            = "code.impact"
	ToolCodeAnchors           = "code.anchors"
	ToolExecutionVisualAssert = "execution.visual_assert"
	ToolSUTPagePropose        = "sut.page_propose"
)

// CodeUnavailable is the P5 stable tool error code for a temporarily
// unavailable codeintel service (contracts/agent-profiles-tools.md §8.2).
const CodeUnavailable = "unavailable"

// p5ToolNames is the frozen P5 registration order.
var p5ToolNames = []string{
	ToolCodeTree,
	ToolCodeRead,
	ToolCodeSearch,
	ToolCodeFacts,
	ToolCodeRoutes,
	ToolCodeAPIs,
	ToolCodeDiff,
	ToolCodeImpact,
	ToolCodeAnchors,
	ToolExecutionVisualAssert,
	ToolSUTPagePropose,
}

// P5ToolNames returns a copy of the frozen P5 tool names.
func P5ToolNames() []string {
	return append([]string(nil), p5ToolNames...)
}

// P5Deps is the additive P5 service surface. It is nested inside Deps so P1–P4
// wiring keeps compiling untouched; nil services make the P5 tools fail as an
// internal configuration error, never as a model-visible business error.
type P5Deps struct {
	// Versions resolves the version's bound code repositories.
	Versions CodeVersionReader
	// Index is the T0 tree/index projection (code.tree).
	Index CodeIndexReader
	// Query is the read-only T0 file/diff projection (code.read/code.diff).
	Query CodeQueryReader
	// Analysis is the T1 fact/search projection (code.facts/search/routes/apis).
	Analysis CodeAnalysisReader
	// Impact exposes analysis candidates (code.impact).
	Impact CodeImpactReader
	// Anchors exposes the unified code anchors (code.anchors).
	Anchors CodeAnchorReader
	// Propose writes one candidate page/API proposal (sut.page_propose).
	Propose SutProposalWriter
	// VisualAssert is the execution CaseRunner port used by the
	// execution.visual_assert group registered from the execution tool package.
	VisualAssert VisualAssertRunner
}

// CodeVersionReader is the version context's code binding read surface. The
// codeintel VersionReader adapter satisfies it directly.
type CodeVersionReader interface {
	VersionSUTBindings(ctx context.Context, versionID int64) ([]codeintelapp.VersionSUTBinding, error)
}

// CodeIndexReader is the T0 index service surface used by code.tree.
type CodeIndexReader interface {
	Tree(ctx context.Context, repoID int64, commit, pathPrefix, cursor string, limit int) ([]codeintel.Entity, string, bool, error)
	Latest(ctx context.Context, repoID int64) (codeintel.IndexRun, error)
}

// CodeQueryReader is the read-only T0 query surface used by code.read/code.diff.
type CodeQueryReader interface {
	File(ctx context.Context, repoID int64, commit, filePath string, start, end int) (codeintel.FileWindow, error)
	Diff(ctx context.Context, repoID int64, base, head, pathFilter, cursor string, limit int) (codeintel.DiffResult, string, bool, error)
}

// CodeAnalysisReader is the T1 fact/search surface used by code.facts,
// code.search, code.routes, code.apis and code.impact.
type CodeAnalysisReader interface {
	Facts(ctx context.Context, repoID int64, commit, kind, pathFilter, cursor string, limit int) ([]codeintel.Fact, string, bool, error)
	Search(ctx context.Context, repoID int64, commit, query, kind string, limit int) ([]codeintel.Entity, error)
	Latest(ctx context.Context, versionID, repoID int64) (codeintel.Analysis, error)
	Get(ctx context.Context, analysisID int64) (codeintel.Analysis, error)
}

// CodeImpactReader is the impact service surface used by code.impact.
type CodeImpactReader interface {
	List(ctx context.Context, analysisID int64) ([]codeintelapp.ImpactCandidateView, error)
	Union(ctx context.Context, versionID int64) ([]codeintelapp.ImpactCandidateView, error)
}

// CodeAnchorReader is the anchor service surface used by code.anchors.
type CodeAnchorReader interface {
	List(ctx context.Context, versionID int64, subjectKind string, subjectID int64) ([]codeintel.Anchor, error)
}

// SutProposalInput is one candidate page/API proposal. The writer owns the
// dedupe, the proposed/source fields and the admin confirmation semantics; the
// tool only validates the shape and forwards the version scope.
type SutProposalInput struct {
	Kind          string
	Name          string
	URL           string
	RouteTemplate string
	Method        string
	PathTemplate  string
	Reason        string
}

// SutProposalResult is the writer's created/skipped outcome.
type SutProposalResult struct {
	Created bool
	ID      int64
	Status  string
}

// SutProposalWriter writes one status=proposed resource.
type SutProposalWriter interface {
	ProposeSUTResource(ctx context.Context, versionID int64, in SutProposalInput) (SutProposalResult, error)
}

// VisualAssertRunner is the execution case-runner method used by
// execution.visual_assert. *executionapp.CaseRunner satisfies it.
type VisualAssertRunner interface {
	VisualAssert(ctx context.Context, scope Scope, assertionID int64) (any, error)
}

// P5 tool groups register themselves from context init() hooks (the execution
// visual tool) so the tools package never imports the execution context.
var (
	p5ToolGroupMu sync.RWMutex
	p5ToolGroups  = []func(deps Deps) []Tool{}
)

// The concrete codeintel services satisfy the P5 port surface directly, so the
// wiring only has to pass the existing services (no agent-side adapter). These
// compile-time assertions fail the build if a service signature drifts.
var (
	_ CodeIndexReader    = (*codeintelapp.IndexService)(nil)
	_ CodeQueryReader    = (*codeintelapp.QueryService)(nil)
	_ CodeAnalysisReader = (*codeintelapp.AnalysisService)(nil)
	_ CodeImpactReader   = (*codeintelapp.ImpactService)(nil)
	_ CodeAnchorReader   = (*codeintelapp.AnchorService)(nil)
	_ CodeVersionReader  = codeintelapp.VersionReader(nil)
)

// RegisterP5ToolGroup appends one P5 tool group factory. Groups are evaluated
// every time a registry is built with the deps resolved for that run. Nil
// factories are ignored.
func RegisterP5ToolGroup(fn func(deps Deps) []Tool) {
	if fn == nil {
		return
	}
	p5ToolGroupMu.Lock()
	defer p5ToolGroupMu.Unlock()
	p5ToolGroups = append(p5ToolGroups, fn)
}

// NewP5Registry registers the frozen P1–P4 tool set plus the P5 code/page tools
// and every P5 group registered by context init hooks. P5 groups are empty
// outside the contexts that register them; profiles still filter the registry
// by allowed_tools, so earlier phases keep their surface.
func NewP5Registry(deps Deps) *Registry {
	base := NewP4Registry(deps)
	all := make([]Tool, 0, len(base.byName)+len(p5ToolNames))
	all = append(all, p5Tools(deps)...)
	for _, name := range base.Names() {
		tool, _ := base.Get(name)
		all = append(all, tool)
	}
	p5ToolGroupMu.RLock()
	groups := make([]func(deps Deps) []Tool, len(p5ToolGroups))
	copy(groups, p5ToolGroups)
	p5ToolGroupMu.RUnlock()
	for _, group := range groups {
		all = append(all, group(deps)...)
	}
	return NewRegistry(all...)
}

// --- shared P5 helpers -----------------------------------------------------

// p5RepoBinding is one uniquely bound code repository of the version scope.
type p5RepoBinding struct {
	RepoID         int64
	VersionSUTID   int64
	Branch         string
	BaselineCommit string
}

// p5ResolveRepo resolves the business repoId (never a version scope) against
// the version's bound repositories. A single binding may omit repoId; multiple
// bindings require it (spec §4.4). code.impact never needs this helper when it
// spans repositories: it uses the multi-repo union instead.
func p5ResolveRepo(ctx context.Context, deps Deps, scope Scope, rawRepoID string) (p5RepoBinding, error) {
	if scope.VersionID <= 0 {
		return p5RepoBinding{}, Invalid("scope", "the version scope is required")
	}
	if deps.P5.Versions == nil {
		return p5RepoBinding{}, missingDep("codeintel version reader")
	}
	var requested int64
	if trimmed := strings.TrimSpace(rawRepoID); trimmed != "" {
		parsed, err := parsePositiveID("repoId", trimmed)
		if err != nil {
			return p5RepoBinding{}, err
		}
		requested = parsed
	}
	bindings, err := deps.P5.Versions.VersionSUTBindings(ctx, scope.VersionID)
	if err != nil {
		return p5RepoBinding{}, p5MapError(err, "code repository")
	}
	seen := make(map[int64]struct{}, len(bindings))
	repos := make([]p5RepoBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.RepoID <= 0 {
			continue
		}
		if _, duplicate := seen[binding.RepoID]; duplicate {
			continue
		}
		seen[binding.RepoID] = struct{}{}
		repos = append(repos, p5RepoBinding{
			RepoID:         binding.RepoID,
			VersionSUTID:   binding.VersionSUTID,
			Branch:         binding.Branch,
			BaselineCommit: strings.TrimSpace(binding.CodeBaselineCommit),
		})
	}
	if requested > 0 {
		for _, repo := range repos {
			if repo.RepoID == requested {
				return repo, nil
			}
		}
		return p5RepoBinding{}, Invalid("repoId", "repository is not bound to this version")
	}
	switch {
	case len(repos) == 0:
		return p5RepoBinding{}, Invalid("repoId", "the version has no bound code repository")
	case len(repos) == 1:
		return repos[0], nil
	default:
		return p5RepoBinding{}, Invalid("repoId", "the version binds multiple repositories; repoId is required")
	}
}

// p5DefaultCommit resolves the effective commit of a read tool: an explicit
// commit wins over the version baseline, which wins over the latest indexed
// commit. The commit is validated here so the model gets a stable
// invalid_argument instead of an internal git error.
func p5DefaultCommit(ctx context.Context, deps Deps, repo p5RepoBinding, explicit, field string) (string, error) {
	commit := strings.TrimSpace(explicit)
	if commit == "" {
		commit = repo.BaselineCommit
	}
	return p5CommitOrLatest(ctx, deps, repo, commit, field)
}

// p5HeadCommit resolves a diff head: explicit commit, then the latest indexed
// commit, then the version baseline.
func p5HeadCommit(ctx context.Context, deps Deps, repo p5RepoBinding, explicit, field string) (string, error) {
	return p5CommitOrLatest(ctx, deps, repo, strings.TrimSpace(explicit), field)
}

// p5BaseCommit resolves a diff base: explicit commit, then the version
// baseline, then the latest indexed commit.
func p5BaseCommit(ctx context.Context, deps Deps, repo p5RepoBinding, explicit, field string) (string, error) {
	commit := strings.TrimSpace(explicit)
	if commit == "" {
		commit = repo.BaselineCommit
	}
	return p5CommitOrLatest(ctx, deps, repo, commit, field)
}

func p5CommitOrLatest(ctx context.Context, deps Deps, repo p5RepoBinding, candidate, field string) (string, error) {
	if candidate == "" && deps.P5.Index != nil {
		if run, err := deps.P5.Index.Latest(ctx, repo.RepoID); err == nil {
			candidate = strings.TrimSpace(run.CommitSHA)
		}
	}
	if candidate == "" {
		return "", Invalid(field, "commit is required: the version has no indexed baseline")
	}
	if !p5ValidCommit(candidate) {
		return "", Invalid(field, "must be a 40-character lowercase hex commit SHA")
	}
	return candidate, nil
}

func p5ValidCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// p5Limit clamps a model-supplied limit to the frozen per-tool bounds.
func p5Limit(requested *int, fallback, maximum int) int {
	limit := fallback
	if requested != nil && *requested > 0 {
		limit = *requested
	}
	if limit > maximum {
		limit = maximum
	}
	return limit
}

// p5MapError maps service failures onto the four stable P5 tool codes. Internal
// failures stay internal (they fail the run instead of teaching the model a
// fake retry).
func p5MapError(err error, kind string) error {
	if err == nil {
		return nil
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		return toolErr
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == CodeLeaseLost:
			return &Error{Code: CodeLeaseLost, Message: apiErr.Message, Details: apiErr.Details}
		case apiErr.Status == 404 || apiErr.Status == 403:
			// Cross-scope IDs are reported as not_found, never as a hint that
			// another version exists.
			return &Error{Code: CodeNotFound, Message: apiErr.Message, Details: apiErr.Details}
		case apiErr.Status == 409:
			return &Error{Code: CodeStateConflict, Message: apiErr.Message, Details: apiErr.Details}
		case apiErr.Status == 400 || apiErr.Status == 422:
			return &Error{Code: CodeInvalidArgument, Message: apiErr.Message, Details: apiErr.Details}
		case apiErr.Status >= 500:
			return &Error{Code: CodeUnavailable, Message: apiErr.Message, Details: apiErr.Details}
		}
		return fmt.Errorf("tool port failure: %w", err)
	}
	switch {
	case errors.Is(err, codeintel.ErrNotFound):
		return NotFound(kind)
	case errors.Is(err, codeintel.ErrConflict):
		return StateConflict(kind + " conflicts with the current state")
	case errors.Is(err, codeintel.ErrInvalid):
		return Invalid(kind, kind+" is not valid")
	case errors.Is(err, codeintel.ErrUnavailable):
		return &Error{Code: CodeUnavailable, Message: kind + " is unavailable"}
	default:
		return fmt.Errorf("tool port failure: %w", err)
	}
}

// idList renders int64 IDs as the decimal strings promised by the tool
// contract.
func idList(values []int64) []string {
	rendered := make([]string, 0, len(values))
	for _, value := range values {
		rendered = append(rendered, idString(value))
	}
	return rendered
}

// rawJSON renders a JSON document without double-encoding it.
func rawJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
