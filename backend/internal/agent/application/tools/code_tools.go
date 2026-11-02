package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// P5 code-intelligence tool bounds (spec §4.4). The application services apply
// the same hard caps; the tools clamp first so a model cannot ask for a page
// larger than the contract.
const (
	p5TreeLimit    = 50
	p5SearchLimit  = 50
	p5FactsLimit   = 100
	p5FactsRoute   = 100
	p5APILimit     = 100
	p5DiffLimit    = 200
	p5ImpactLimit  = 200
	p5AnchorsLimit = 200
	// p5CursorMaxChars and p5QueryMaxChars bound the free-text arguments.
	p5CursorMaxChars = 256
	p5QueryMaxChars  = 200
	p5PathMaxChars   = 4096
)

// p5Tools returns the P5 tools implemented by this package. execution.visual_assert
// is provided by the execution agent_tool group (RegisterP5ToolGroup).
func p5Tools(deps Deps) []Tool {
	return []Tool{
		&codeTreeTool{deps: deps},
		&codeReadTool{deps: deps},
		&codeSearchTool{deps: deps},
		&codeFactsTool{deps: deps},
		&codeRoutesTool{deps: deps},
		&codeAPIsTool{deps: deps},
		&codeDiffTool{deps: deps},
		&codeImpactTool{deps: deps},
		&codeAnchorsTool{deps: deps},
		&sutPageProposeTool{deps: deps},
	}
}

// --- code.tree -------------------------------------------------------------

type codeTreeTool struct{ deps Deps }

func (t *codeTreeTool) Name() string { return ToolCodeTree }

func (t *codeTreeTool) Description() string {
	return "List indexed repository files at a commit (max 50 entries, no content). Use repoId only when the version binds multiple repositories; commit defaults to the version baseline."
}

func (t *codeTreeTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId": map[string]any{"type": "string", "pattern": "^[0-9]+$", "description": "bound code repository ID; optional when the version binds exactly one repository"},
		"commit": map[string]any{"type": "string", "description": "40-character commit SHA; defaults to the version baseline commit"},
		"path":   map[string]any{"type": "string", "description": "normalized repo-relative path prefix"},
		"cursor": map[string]any{"type": "string", "description": "opaque cursor from a previous page"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": p5TreeLimit},
	})
}

func (t *codeTreeTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID string `json:"repoId"`
		Commit string `json:"commit"`
		Path   string `json:"path"`
		Cursor string `json:"cursor"`
		Limit  *int   `json:"limit"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if t.deps.P5.Index == nil {
		return nil, missingDep("codeintel index service")
	}
	repo, err := p5ResolveRepo(ctx, t.deps, scope, args.RepoID)
	if err != nil {
		return nil, err
	}
	commit, err := p5DefaultCommit(ctx, t.deps, repo, args.Commit, "commit")
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(args.Path)
	cursor, err := p5Cursor(args.Cursor)
	if err != nil {
		return nil, err
	}
	entries, next, hasMore, err := t.deps.P5.Index.Tree(ctx, repo.RepoID, commit, path, cursor,
		p5Limit(args.Limit, p5TreeLimit, p5TreeLimit))
	if err != nil {
		return nil, p5MapError(err, "code tree")
	}
	mapped := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		mapped = append(mapped, p5TreeEntry(entry))
	}
	return map[string]any{
		"repo_id":     idString(repo.RepoID),
		"commit":      commit,
		"entries":     mapped,
		"next_cursor": next,
		"has_more":    hasMore,
	}, nil
}

func p5TreeEntry(entity codeintel.Entity) map[string]any {
	return map[string]any{
		"path":      entity.Path,
		"name":      entity.Name,
		"kind":      string(entity.Kind),
		"blob_hash": entity.BlobHash,
	}
}

// --- code.read -------------------------------------------------------------

type codeReadTool struct{ deps Deps }

func (t *codeReadTool) Name() string { return ToolCodeRead }

func (t *codeReadTool) Description() string {
	return "Read one audited, 1-based line window of a repository file at a commit (max 400 lines / 64 KiB; oversized windows are truncated)."
}

func (t *codeReadTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId":    map[string]any{"type": "string", "pattern": "^[0-9]+$", "description": "bound code repository ID; optional when the version binds exactly one repository"},
		"path":      map[string]any{"type": "string", "description": "normalized repo-relative file path"},
		"commit":    map[string]any{"type": "string", "description": "40-character commit SHA; defaults to the version baseline commit"},
		"startLine": map[string]any{"type": "integer", "minimum": 1, "description": "first line (1-based); defaults to 1"},
		"endLine":   map[string]any{"type": "integer", "minimum": 1, "description": "last line (inclusive); defaults to the end of the file"},
	}, "path")
}

func (t *codeReadTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID    string `json:"repoId"`
		Path      string `json:"path"`
		Commit    string `json:"commit"`
		StartLine int    `json:"startLine"`
		EndLine   int    `json:"endLine"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		return nil, Invalid("path", "path is required")
	}
	if len([]rune(path)) > p5PathMaxChars {
		return nil, Invalid("path", "path is too long")
	}
	if args.StartLine < 0 || args.EndLine < 0 {
		return nil, Invalid("startLine", "line numbers must be positive")
	}
	if args.StartLine > 0 && args.EndLine > 0 && args.EndLine < args.StartLine {
		return nil, Invalid("endLine", "endLine must not be before startLine")
	}
	if t.deps.P5.Query == nil {
		return nil, missingDep("codeintel query service")
	}
	repo, err := p5ResolveRepo(ctx, t.deps, scope, args.RepoID)
	if err != nil {
		return nil, err
	}
	commit, err := p5DefaultCommit(ctx, t.deps, repo, args.Commit, "commit")
	if err != nil {
		return nil, err
	}
	window, err := t.deps.P5.Query.File(ctx, repo.RepoID, commit, path, args.StartLine, args.EndLine)
	if err != nil {
		return nil, p5MapError(err, "code file")
	}
	lines := window.Lines
	if lines == nil {
		lines = []string{}
	}
	return map[string]any{
		"repo_id":     idString(repo.RepoID),
		"path":        window.Path,
		"commit":      window.Commit,
		"start_line":  window.StartLine,
		"end_line":    window.EndLine,
		"total_lines": window.TotalLines,
		"lines":       lines,
		"truncated":   window.Truncated,
	}, nil
}

// --- code.search -----------------------------------------------------------

type codeSearchTool struct{ deps Deps }

func (t *codeSearchTool) Name() string { return ToolCodeSearch }

func (t *codeSearchTool) Description() string {
	return "Substring-search indexed paths and names at a commit (max 50 results, no content)."
}

func (t *codeSearchTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId": map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		"q":      map[string]any{"type": "string", "description": "path/name substring"},
		"kind":   map[string]any{"type": "string", "enum": []string{"file", "module", "route", "api", "client_call", "component", "page", "symbol"}},
		"commit": map[string]any{"type": "string"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": p5SearchLimit},
	}, "q")
}

func (t *codeSearchTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID string `json:"repoId"`
		Query  string `json:"q"`
		Kind   string `json:"kind"`
		Commit string `json:"commit"`
		Limit  *int   `json:"limit"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return nil, Invalid("q", "q is required")
	}
	if len([]rune(query)) > p5QueryMaxChars {
		return nil, Invalid("q", "q is too long")
	}
	kind := strings.TrimSpace(args.Kind)
	if kind != "" && !codeintel.EntityKind(kind).Valid() {
		return nil, Invalid("kind", "unknown fact kind")
	}
	if t.deps.P5.Analysis == nil {
		return nil, missingDep("codeintel analysis service")
	}
	repo, err := p5ResolveRepo(ctx, t.deps, scope, args.RepoID)
	if err != nil {
		return nil, err
	}
	commit, err := p5DefaultCommit(ctx, t.deps, repo, args.Commit, "commit")
	if err != nil {
		return nil, err
	}
	entities, err := t.deps.P5.Analysis.Search(ctx, repo.RepoID, commit, query, kind,
		p5Limit(args.Limit, p5SearchLimit, p5SearchLimit))
	if err != nil {
		return nil, p5MapError(err, "code search")
	}
	results := make([]map[string]any, 0, len(entities))
	for _, entity := range entities {
		results = append(results, p5EntitySummary(entity))
	}
	return map[string]any{
		"repo_id": idString(repo.RepoID),
		"commit":  commit,
		"q":       query,
		"entries": results,
	}, nil
}

func p5EntitySummary(entity codeintel.Entity) map[string]any {
	return map[string]any{
		"id":         idString(entity.ID),
		"kind":       string(entity.Kind),
		"path":       entity.Path,
		"name":       entity.Name,
		"signature":  entity.Signature,
		"start_line": optionalInt(entity.StartLine),
		"end_line":   optionalInt(entity.EndLine),
		"fact_key":   entity.FactKey,
		"confidence": string(entity.Confidence),
	}
}

// --- code.facts ------------------------------------------------------------

type codeFactsTool struct{ deps Deps }

func (t *codeFactsTool) Name() string { return ToolCodeFacts }

func (t *codeFactsTool) Description() string {
	return "List AI-extracted fact summaries of a commit (max 100 facts, no code body)."
}

func (t *codeFactsTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId": map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		"kind":   map[string]any{"type": "string", "enum": []string{"file", "module", "route", "api", "client_call", "component", "page", "symbol"}},
		"path":   map[string]any{"type": "string", "description": "normalized repo-relative path filter"},
		"commit": map[string]any{"type": "string"},
		"cursor": map[string]any{"type": "string"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": p5FactsLimit},
	})
}

func (t *codeFactsTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID string `json:"repoId"`
		Kind   string `json:"kind"`
		Path   string `json:"path"`
		Commit string `json:"commit"`
		Cursor string `json:"cursor"`
		Limit  *int   `json:"limit"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	kind := strings.TrimSpace(args.Kind)
	if kind != "" && !codeintel.EntityKind(kind).Valid() {
		return nil, Invalid("kind", "unknown fact kind")
	}
	if t.deps.P5.Analysis == nil {
		return nil, missingDep("codeintel analysis service")
	}
	repo, err := p5ResolveRepo(ctx, t.deps, scope, args.RepoID)
	if err != nil {
		return nil, err
	}
	commit, err := p5DefaultCommit(ctx, t.deps, repo, args.Commit, "commit")
	if err != nil {
		return nil, err
	}
	cursor, err := p5Cursor(args.Cursor)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(args.Path)
	facts, next, hasMore, err := t.deps.P5.Analysis.Facts(ctx, repo.RepoID, commit, kind, path, cursor,
		p5Limit(args.Limit, p5FactsLimit, p5FactsLimit))
	if err != nil {
		return nil, p5MapError(err, "code facts")
	}
	mapped := make([]map[string]any, 0, len(facts))
	for _, fact := range facts {
		mapped = append(mapped, p5FactSummary(fact))
	}
	return map[string]any{
		"repo_id":     idString(repo.RepoID),
		"commit":      commit,
		"facts":       mapped,
		"next_cursor": next,
		"has_more":    hasMore,
	}, nil
}

func p5FactSummary(fact codeintel.Fact) map[string]any {
	return map[string]any{
		"id":         idString(fact.ID),
		"kind":       string(fact.Kind),
		"path":       fact.Path,
		"name":       fact.Name,
		"signature":  fact.Signature,
		"start_line": optionalInt(fact.StartLine),
		"end_line":   optionalInt(fact.EndLine),
		"confidence": string(fact.Confidence),
		"source":     fact.Source,
		"fact_key":   fact.FactKey,
		"version":    fact.Version,
	}
}

// --- code.routes / code.apis ----------------------------------------------

type codeRoutesTool struct{ deps Deps }

func (t *codeRoutesTool) Name() string { return ToolCodeRoutes }

func (t *codeRoutesTool) Description() string {
	return "List route and page facts of a commit (max 100, no content)."
}

func (t *codeRoutesTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId": map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		"commit": map[string]any{"type": "string"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": p5FactsRoute},
	})
}

func (t *codeRoutesTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID string `json:"repoId"`
		Commit string `json:"commit"`
		Limit  *int   `json:"limit"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	return t.execute(ctx, scope, args.RepoID, args.Commit, args.Limit, []string{"route", "page"})
}

func (t *codeRoutesTool) execute(ctx context.Context, scope Scope, rawRepoID, rawCommit string, limit *int, kinds []string) (any, error) {
	return p5FactKinds(ctx, t.deps, scope, rawRepoID, rawCommit, limit, kinds, "code routes")
}

type codeAPIsTool struct{ deps Deps }

func (t *codeAPIsTool) Name() string { return ToolCodeAPIs }

func (t *codeAPIsTool) Description() string {
	return "List api and client_call facts of a commit (max 100, no content)."
}

func (t *codeAPIsTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId": map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		"commit": map[string]any{"type": "string"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": p5APILimit},
	})
}

func (t *codeAPIsTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID string `json:"repoId"`
		Commit string `json:"commit"`
		Limit  *int   `json:"limit"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	return p5FactKinds(ctx, t.deps, scope, args.RepoID, args.Commit, args.Limit,
		[]string{"api", "client_call"}, "code apis")
}

// p5FactKinds reads two fact groups and merges them into one bounded list.
func p5FactKinds(ctx context.Context, deps Deps, scope Scope, rawRepoID, rawCommit string, limit *int, kinds []string, kindName string) (any, error) {
	if deps.P5.Analysis == nil {
		return nil, missingDep("codeintel analysis service")
	}
	repo, err := p5ResolveRepo(ctx, deps, scope, rawRepoID)
	if err != nil {
		return nil, err
	}
	commit, err := p5DefaultCommit(ctx, deps, repo, rawCommit, "commit")
	if err != nil {
		return nil, err
	}
	max := p5Limit(limit, p5FactsRoute, p5FactsRoute)
	merged := make([]map[string]any, 0, max*2)
	for _, kind := range kinds {
		facts, _, _, err := deps.P5.Analysis.Facts(ctx, repo.RepoID, commit, kind, "", "", max)
		if err != nil {
			return nil, p5MapError(err, kindName)
		}
		for _, fact := range facts {
			merged = append(merged, p5FactSummary(fact))
			if len(merged) >= max {
				break
			}
		}
		if len(merged) >= max {
			break
		}
	}
	return map[string]any{
		"repo_id": idString(repo.RepoID),
		"commit":  commit,
		"facts":   merged,
	}, nil
}

// --- code.diff -------------------------------------------------------------

type codeDiffTool struct{ deps Deps }

func (t *codeDiffTool) Name() string { return ToolCodeDiff }

func (t *codeDiffTool) Description() string {
	return "List changed files between the version baseline and a head commit (max 200 summaries, never patch bodies)."
}

func (t *codeDiffTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId": map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		"base":   map[string]any{"type": "string", "description": "40-character base commit SHA; defaults to the version baseline"},
		"head":   map[string]any{"type": "string", "description": "40-character head commit SHA; defaults to the latest indexed commit"},
		"path":   map[string]any{"type": "string", "description": "normalized repo-relative path filter"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": p5DiffLimit},
	})
}

func (t *codeDiffTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID string `json:"repoId"`
		Base   string `json:"base"`
		Head   string `json:"head"`
		Path   string `json:"path"`
		Limit  *int   `json:"limit"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if t.deps.P5.Query == nil {
		return nil, missingDep("codeintel query service")
	}
	repo, err := p5ResolveRepo(ctx, t.deps, scope, args.RepoID)
	if err != nil {
		return nil, err
	}
	base, err := p5BaseCommit(ctx, t.deps, repo, args.Base, "base")
	if err != nil {
		return nil, err
	}
	head, err := p5HeadCommit(ctx, t.deps, repo, args.Head, "head")
	if err != nil {
		return nil, err
	}
	result, next, hasMore, err := t.deps.P5.Query.Diff(ctx, repo.RepoID, base, head,
		strings.TrimSpace(args.Path), "", p5Limit(args.Limit, p5DiffLimit, p5DiffLimit))
	if err != nil {
		return nil, p5MapError(err, "code diff")
	}
	files := make([]map[string]any, 0, len(result.Files))
	for _, file := range result.Files {
		files = append(files, map[string]any{
			"path":      file.Path,
			"old_path":  file.OldPath,
			"status":    file.Status,
			"additions": file.Additions,
			"deletions": file.Deletions,
			"binary":    file.Binary,
			"head_blob": optionalString(file.HeadBlobSHA),
		})
	}
	return map[string]any{
		"repo_id":     idString(repo.RepoID),
		"base":        base,
		"head":        head,
		"files":       files,
		"next_cursor": next,
		"truncated":   hasMore || result.Truncated,
	}, nil
}

// --- code.impact -----------------------------------------------------------

type codeImpactTool struct{ deps Deps }

func (t *codeImpactTool) Name() string { return ToolCodeImpact }

func (t *codeImpactTool) Description() string {
	return "List impact candidates of the latest analysis (max 200; cross-repository union when repoId and analysisId are omitted)."
}

func (t *codeImpactTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId":     map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		"analysisId": map[string]any{"type": "string", "pattern": "^[0-9]+$", "description": "decimal code analysis ID; defaults to the latest analysis"},
		"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": p5ImpactLimit},
	})
}

func (t *codeImpactTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID     string `json:"repoId"`
		AnalysisID string `json:"analysisId"`
		Limit      *int   `json:"limit"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if scope.VersionID <= 0 {
		return nil, Invalid("scope", "the version scope is required")
	}
	if t.deps.P5.Impact == nil || t.deps.P5.Analysis == nil {
		return nil, missingDep("codeintel impact service")
	}
	limit := p5Limit(args.Limit, p5ImpactLimit, p5ImpactLimit)
	analysisID := int64(0)
	var views []codeintelapp.ImpactCandidateView
	switch {
	case strings.TrimSpace(args.AnalysisID) != "":
		parsed, parseErr := parsePositiveID("analysisId", args.AnalysisID)
		if parseErr != nil {
			return nil, parseErr
		}
		analysis, getErr := t.deps.P5.Analysis.Get(ctx, parsed)
		if getErr != nil {
			return nil, p5MapError(getErr, "code analysis")
		}
		if analysis.VersionID != scope.VersionID {
			return nil, NotFound("code analysis")
		}
		if strings.TrimSpace(args.RepoID) != "" {
			repo, repoErr := p5ResolveRepo(ctx, t.deps, scope, args.RepoID)
			if repoErr != nil {
				return nil, repoErr
			}
			if repo.RepoID != analysis.RepoID {
				return nil, Invalid("repoId", "the analysis belongs to another repository")
			}
		}
		list, listErr := t.deps.P5.Impact.List(ctx, parsed)
		if listErr != nil {
			return nil, p5MapError(listErr, "code impact")
		}
		views, analysisID = list, parsed
	case strings.TrimSpace(args.RepoID) != "":
		repo, repoErr := p5ResolveRepo(ctx, t.deps, scope, args.RepoID)
		if repoErr != nil {
			return nil, repoErr
		}
		latest, latestErr := t.deps.P5.Analysis.Latest(ctx, scope.VersionID, repo.RepoID)
		if latestErr != nil {
			if p5IsNotFound(latestErr) {
				return p5ImpactResult(0, nil, limit), nil
			}
			return nil, p5MapError(latestErr, "code analysis")
		}
		list, listErr := t.deps.P5.Impact.List(ctx, latest.ID)
		if listErr != nil {
			return nil, p5MapError(listErr, "code impact")
		}
		views, analysisID = list, latest.ID
	default:
		union, unionErr := t.deps.P5.Impact.Union(ctx, scope.VersionID)
		if unionErr != nil {
			return nil, p5MapError(unionErr, "code impact")
		}
		views = union
	}
	return p5ImpactResult(analysisID, views, limit), nil
}

func p5ImpactResult(analysisID int64, views []codeintelapp.ImpactCandidateView, limit int) map[string]any {
	if len(views) > limit {
		views = views[:limit]
	}
	candidates := make([]map[string]any, 0, len(views))
	for _, view := range views {
		candidates = append(candidates, map[string]any{
			"id":              idString(view.ID),
			"analysis_id":     idString(view.AnalysisID),
			"subject_kind":    string(view.SubjectKind),
			"subject_id":      idString(view.SubjectID),
			"reason":          view.Reason,
			"confidence":      string(view.Confidence),
			"source":          string(view.Source),
			"selected":        view.Selected,
			"repo_id":         idString(view.RepoID),
			"version_sut_ids": idList(view.VersionSUTIDs),
			"evidence":        rawJSON(view.Evidence),
		})
	}
	result := map[string]any{"candidates": candidates}
	if analysisID > 0 {
		result["analysis_id"] = idString(analysisID)
	}
	return result
}

// --- code.anchors ----------------------------------------------------------

type codeAnchorsTool struct{ deps Deps }

func (t *codeAnchorsTool) Name() string { return ToolCodeAnchors }

func (t *codeAnchorsTool) Description() string {
	return "List code anchors of the version, optionally filtered by repository and subject (max 200)."
}

func (t *codeAnchorsTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"repoId":      map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		"subjectKind": map[string]any{"type": "string", "enum": []string{"version", "requirement", "requirement_version", "case_version", "issue"}},
		"subjectId":   map[string]any{"type": "string", "pattern": "^[0-9]+$"},
		"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": p5AnchorsLimit},
	})
}

func (t *codeAnchorsTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		RepoID      string `json:"repoId"`
		SubjectKind string `json:"subjectKind"`
		SubjectID   string `json:"subjectId"`
		Limit       *int   `json:"limit"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if scope.VersionID <= 0 {
		return nil, Invalid("scope", "the version scope is required")
	}
	if t.deps.P5.Anchors == nil {
		return nil, missingDep("codeintel anchor service")
	}
	subjectKind := strings.TrimSpace(args.SubjectKind)
	if subjectKind != "" && !codeintel.AnchorSubject(subjectKind).Valid() {
		return nil, Invalid("subjectKind", "unknown anchor subject kind")
	}
	var subjectID int64
	if trimmed := strings.TrimSpace(args.SubjectID); trimmed != "" {
		parsed, err := parsePositiveID("subjectId", trimmed)
		if err != nil {
			return nil, err
		}
		subjectID = parsed
	}
	var repoID int64
	if strings.TrimSpace(args.RepoID) != "" {
		repo, err := p5ResolveRepo(ctx, t.deps, scope, args.RepoID)
		if err != nil {
			return nil, err
		}
		repoID = repo.RepoID
	}
	anchors, err := t.deps.P5.Anchors.List(ctx, scope.VersionID, subjectKind, subjectID)
	if err != nil {
		return nil, p5MapError(err, "code anchors")
	}
	limit := p5Limit(args.Limit, p5AnchorsLimit, p5AnchorsLimit)
	mapped := make([]map[string]any, 0, len(anchors))
	for _, anchor := range anchors {
		if repoID > 0 && anchor.RepoID != repoID {
			continue
		}
		mapped = append(mapped, map[string]any{
			"id":           idString(anchor.ID),
			"subject_kind": string(anchor.SubjectKind),
			"subject_id":   idString(anchor.SubjectID),
			"repo_id":      idString(anchor.RepoID),
			"file_path":    anchor.FilePath,
			"start_line":   optionalInt(anchor.StartLine),
			"end_line":     optionalInt(anchor.EndLine),
			"status":       anchor.Status,
			"confidence":   anchor.Confidence,
			"commit_sha":   anchor.CommitSHA,
			"excerpt_sha":  anchor.ExcerptSHA,
		})
		if len(mapped) >= limit {
			break
		}
	}
	return map[string]any{"anchors": mapped}, nil
}

// --- small result helpers --------------------------------------------------

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func p5Cursor(value string) (string, error) {
	cursor := strings.TrimSpace(value)
	if len([]rune(cursor)) > p5CursorMaxChars {
		return "", Invalid("cursor", "cursor is too long")
	}
	return cursor, nil
}

func p5IsNotFound(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Code == "not_found")
}
