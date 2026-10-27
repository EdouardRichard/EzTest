package tools

// This file adds the nine P4 codeintel read-only tools (design spec §8.1):
// code.repos/search/diff/facts/routes/apis/analyze/impact/anchors. They are
// thin shells that validate the model arguments, clamp every list to the
// documented cap and summarise the result of the injected codeintel
// application services. They never read Git/DB/LLM directly, never return file
// content or a diff patch, and never add an Agent profile: the composition root
// appends them to the P1 registry.
//
// The ports are declared locally, following the P3 clarification tools: this
// infrastructure package depends only on the codeintel domain language, never
// on another context's infrastructure or interfaces. The composition root
// injects the committed application services, which satisfy the ports
// structurally (the P3 AgentToolService pattern); the repository catalog is
// adapted because the tool only needs its summary projection.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Tool output caps from design spec §8.1. They bound what one tool call may
// hand back to the model; the application services apply their own (larger)
// page caps underneath.
const (
	codeIntelMaxRepos         = 50
	codeIntelMaxSearchResults = 50
	codeIntelMaxDiffFiles     = 200
	codeIntelMaxFacts         = 100
	codeIntelMaxRoutes        = 100
	codeIntelMaxAPIs          = 100
	codeIntelMaxCandidates    = 200
	codeIntelMaxAnchors       = 200

	// codeIntelMaxRequestLimit is the documented §8.1 input bound: a limit
	// outside 1..200 is clamped, never forwarded as an unbounded page.
	codeIntelMaxRequestLimit = 200
)

// codeIntelCommitPattern matches the lowercase 40-hex commit ids the codeintel
// contract requires (design spec §8.1 input validation).
var codeIntelCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// CodeIntelRepo is the P2 repository projection code.repos reports. The
// composition root maps application.RepoInfo onto it so this package never
// imports the codeintel application package.
type CodeIntelRepo struct {
	ID            int64
	Key           string
	Name          string
	Status        string
	FixedBranch   string
	LastIndexedAt *time.Time
}

// CodeIntelRepoCatalog lists the configured code repositories. The codeintel
// application serves the underlying data through application.RepoCatalog; the
// composition root adapts it to this local projection.
type CodeIntelRepoCatalog interface {
	ListRepos(ctx context.Context) ([]CodeIntelRepo, error)
}

// CodeIntelIndexPort is the latest index-run read behind code.repos. It is
// implemented by *application.IndexService.
type CodeIntelIndexPort interface {
	GetLatest(ctx context.Context, repoID int64) (codeinteldomain.IndexRun, error)
}

// CodeIntelQueryPort is the read-only T0 query subset behind
// code.search/diff/facts/routes/apis. It is implemented by
// *application.QueryService.
type CodeIntelQueryPort interface {
	Search(ctx context.Context, repoID int64, commit, q, kind string, limit int, cursor string) ([]codeinteldomain.CodeEntity, string, bool, error)
	Diff(ctx context.Context, repoID int64, base, head string, limit int, cursor string) (codeinteldomain.ChangeSetData, error)
	Facts(ctx context.Context, repoID int64, filter codeinteldomain.EntityFilter) ([]codeinteldomain.CodeEntity, string, bool, error)
}

// CodeIntelAnalysisPort is the version-scoped analysis subset behind
// code.analyze/code.impact. It is implemented by *application.AnalysisService.
type CodeIntelAnalysisPort interface {
	Start(ctx context.Context, versionID, actorID int64, headCommit string, force bool) (codeinteldomain.CodeAnalysis, bool, error)
	GetLatest(ctx context.Context, versionID int64) (codeinteldomain.CodeAnalysis, error)
	Get(ctx context.Context, versionID, analysisID int64) (codeinteldomain.CodeAnalysis, error)
	ListCandidates(ctx context.Context, analysisID int64) ([]codeinteldomain.ImpactCandidate, error)
}

// CodeIntelAnchorPort is the anchor listing subset behind code.anchors. It is
// implemented by *application.AnchorService.
type CodeIntelAnchorPort interface {
	List(ctx context.Context, versionID int64, filter codeinteldomain.AnchorFilter) ([]codeinteldomain.CodeAnchor, error)
}

// CodeIntelToolDeps are the narrow ports the nine tools consume. A nil port
// disables the whole tool set (the composition root always injects every one).
type CodeIntelToolDeps struct {
	Repos    CodeIntelRepoCatalog
	Indexes  CodeIntelIndexPort
	Queries  CodeIntelQueryPort
	Analyses CodeIntelAnalysisPort
	Anchors  CodeIntelAnchorPort
}

// codeIntelToolDefinition is one tool's frozen metadata plus its execution
// function.
type codeIntelToolDefinition struct {
	name        string
	description string
	schema      string
	run         func(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error)
}

// codeIntelTool implements domain.Tool over one definition and the injected
// ports.
type codeIntelTool struct {
	deps CodeIntelToolDeps
	def  codeIntelToolDefinition
}

// Name implements domain.Tool.
func (t codeIntelTool) Name() string { return t.def.name }

// Description implements domain.Tool.
func (t codeIntelTool) Description() string { return t.def.description }

// InputSchema implements domain.Tool.
func (t codeIntelTool) InputSchema() json.RawMessage { return json.RawMessage(t.def.schema) }

// Execute implements domain.Tool.
func (t codeIntelTool) Execute(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	return t.def.run(ctx, t.deps, args)
}

// NewCodeIntelTools returns the nine P4 read-only tools in the frozen §8.1
// order. A nil port disables the tool set.
func NewCodeIntelTools(deps CodeIntelToolDeps) []domain.Tool {
	if deps.Repos == nil || deps.Indexes == nil || deps.Queries == nil || deps.Analyses == nil || deps.Anchors == nil {
		return nil
	}
	definitions := []codeIntelToolDefinition{
		{
			name:        "code.repos",
			description: "List the configured code repositories with their latest index-run status (read-only; at most 50 repositories).",
			schema:      `{"type":"object","properties":{},"additionalProperties":false}`,
			run:         runCodeRepos,
		},
		{
			name:        "code.search",
			description: "Search code entities by path or name substring at a commit (read-only; returns path/name/kind/confidence summaries only, at most 50 results).",
			schema:      `{"type":"object","properties":{"repoId":{"type":"integer","minimum":1},"commit":{"type":"string","pattern":"^[0-9a-f]{40}$"},"q":{"type":"string","minLength":1},"kind":{"type":"string","enum":["file","page","route","api","client_call","component","module","symbol"]},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["repoId","q"],"additionalProperties":false}`,
			run:         runCodeSearch,
		},
		{
			name:        "code.diff",
			description: "List the file-level changes between two commits (read-only; path/status/counters only, no patch content, at most 200 files).",
			schema:      `{"type":"object","properties":{"repoId":{"type":"integer","minimum":1},"base":{"type":"string","pattern":"^[0-9a-f]{40}$"},"head":{"type":"string","pattern":"^[0-9a-f]{40}$"},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["repoId","base","head"],"additionalProperties":false}`,
			run:         runCodeDiff,
		},
		{
			name:        "code.facts",
			description: "List code facts/entities at a commit with optional kind and path filters (read-only; summaries and evidence references only, at most 100 facts).",
			schema:      `{"type":"object","properties":{"repoId":{"type":"integer","minimum":1},"commit":{"type":"string","pattern":"^[0-9a-f]{40}$"},"kind":{"type":"string","enum":["file","page","route","api","client_call","component","module","symbol"]},"path":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["repoId","commit"],"additionalProperties":false}`,
			run:         runCodeFacts,
		},
		{
			name:        "code.routes",
			description: "List route facts at a commit (read-only; at most 100 routes).",
			schema:      `{"type":"object","properties":{"repoId":{"type":"integer","minimum":1},"commit":{"type":"string","pattern":"^[0-9a-f]{40}$"},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["repoId","commit"],"additionalProperties":false}`,
			run:         runCodeRoutes,
		},
		{
			name:        "code.apis",
			description: "List API facts at a commit (read-only; at most 100 APIs).",
			schema:      `{"type":"object","properties":{"repoId":{"type":"integer","minimum":1},"commit":{"type":"string","pattern":"^[0-9a-f]{40}$"},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["repoId","commit"],"additionalProperties":false}`,
			run:         runCodeAPIs,
		},
		{
			name:        "code.analyze",
			description: "Trigger or reuse the version's code impact analysis. Returns as soon as the analysis is queued or reused; it never waits for the run to finish.",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"headCommit":{"type":"string","pattern":"^[0-9a-f]{40}$"},"force":{"type":"boolean"}},"required":["versionId"],"additionalProperties":false}`,
			run:         runCodeAnalyze,
		},
		{
			name:        "code.impact",
			description: "Summarise the candidates of the latest (or a specified) impact analysis of the version (read-only; subject/reason/confidence/selected only, at most 200 candidates).",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"analysisId":{"type":"string","pattern":"^[0-9]+$"}},"required":["versionId"],"additionalProperties":false}`,
			run:         runCodeImpact,
		},
		{
			name:        "code.anchors",
			description: "List the code anchors of the version, optionally filtered by subject (read-only; at most 200 anchors).",
			schema:      `{"type":"object","properties":{"versionId":{"type":"string","pattern":"^[0-9]+$"},"subjectKind":{"type":"string","enum":["version","requirement","app_model"]},"subjectId":{"type":"string","pattern":"^[0-9]+$"}},"required":["versionId"],"additionalProperties":false}`,
			run:         runCodeAnchors,
		},
	}
	tools := make([]domain.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, codeIntelTool{deps: deps, def: definition})
	}
	return tools
}

// codeIntelReposOutput is the model-facing payload of code.repos.
type codeIntelReposOutput struct {
	Repos     []codeIntelRepoView `json:"repos"`
	Truncated bool                `json:"truncated"`
}

// codeIntelRepoView is one repository summary. The P2 repository id stays a
// JSON number (D29); the codeintel run id is a decimal string.
type codeIntelRepoView struct {
	ID            int64                  `json:"id"`
	Key           string                 `json:"key"`
	Name          string                 `json:"name"`
	Status        string                 `json:"status"`
	FixedBranch   string                 `json:"fixedBranch"`
	LastIndexedAt *time.Time             `json:"lastIndexedAt,omitempty"`
	Index         *codeIntelIndexRunView `json:"index,omitempty"`
}

// codeIntelIndexRunView is the latest index-run projection of one repository.
type codeIntelIndexRunView struct {
	RunID      string     `json:"runId"`
	Commit     string     `json:"commit"`
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// runCodeRepos executes code.repos.
func runCodeRepos(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	var input struct{}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	repositories, err := deps.Repos.ListRepos(ctx)
	if err != nil {
		return codeIntelToolOutcome(err)
	}
	repositories, truncated := truncateCodeIntelList(repositories, codeIntelMaxRepos)
	output := codeIntelReposOutput{
		Repos:     make([]codeIntelRepoView, 0, len(repositories)),
		Truncated: truncated,
	}
	for _, repository := range repositories {
		view := codeIntelRepoView{
			ID:            repository.ID,
			Key:           repository.Key,
			Name:          repository.Name,
			Status:        repository.Status,
			FixedBranch:   repository.FixedBranch,
			LastIndexedAt: repository.LastIndexedAt,
		}
		run, err := deps.Indexes.GetLatest(ctx, repository.ID)
		switch {
		case err == nil:
			view.Index = &codeIntelIndexRunView{
				RunID:      formatCodeIntelID(run.ID),
				Commit:     run.CommitSHA,
				Status:     run.Status,
				StartedAt:  run.StartedAt,
				FinishedAt: run.FinishedAt,
			}
		case errors.Is(err, codeinteldomain.ErrNotFound):
			// A repository that was never indexed keeps an absent index block.
		default:
			return codeIntelToolOutcome(err)
		}
		output.Repos = append(output.Repos, view)
	}
	return codeIntelToolJSON(output)
}

// codeIntelSearchOutput is the model-facing payload of code.search.
type codeIntelSearchOutput struct {
	Results   []codeIntelSearchView `json:"results"`
	Truncated bool                  `json:"truncated"`
	HasMore   bool                  `json:"hasMore"`
}

// codeIntelSearchView is one search summary: path/name/kind/confidence only.
type codeIntelSearchView struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Confidence string `json:"confidence"`
}

// runCodeSearch executes code.search.
func runCodeSearch(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		RepoID int64  `json:"repoId"`
		Commit string `json:"commit"`
		Query  string `json:"q"`
		Kind   string `json:"kind"`
		Limit  int    `json:"limit"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	if input.RepoID <= 0 {
		return invalidToolArguments("repoId must be a positive integer"), nil
	}
	commit, ok := codeIntelOptionalCommit(input.Commit)
	if !ok {
		return invalidToolArguments("commit must be a lowercase 40-hex commit id"), nil
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return invalidToolArguments("q is required"), nil
	}
	entities, _, more, err := deps.Queries.Search(ctx, input.RepoID, commit, query,
		strings.TrimSpace(input.Kind), codeIntelBoundedLimit(input.Limit, codeIntelMaxSearchResults), "")
	if err != nil {
		return codeIntelToolOutcome(err)
	}
	entities, truncated := truncateCodeIntelList(entities, codeIntelMaxSearchResults)
	views := make([]codeIntelSearchView, 0, len(entities))
	for _, entity := range entities {
		views = append(views, codeIntelSearchView{
			Path:       entity.Path,
			Name:       entity.Name,
			Kind:       entity.Kind,
			Confidence: entity.Confidence,
		})
	}
	return codeIntelToolJSON(codeIntelSearchOutput{Results: views, Truncated: truncated, HasMore: more})
}

// codeIntelDiffOutput is the model-facing payload of code.diff.
type codeIntelDiffOutput struct {
	Stats     json.RawMessage            `json:"stats"`
	Files     []codeIntelChangedFileView `json:"files"`
	Truncated bool                       `json:"truncated"`
}

// codeIntelChangedFileView is one file-level change summary. It deliberately
// carries no patch, old path or blob hash.
type codeIntelChangedFileView struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

// runCodeDiff executes code.diff.
func runCodeDiff(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		RepoID int64  `json:"repoId"`
		Base   string `json:"base"`
		Head   string `json:"head"`
		Limit  int    `json:"limit"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	if input.RepoID <= 0 {
		return invalidToolArguments("repoId must be a positive integer"), nil
	}
	base, ok := codeIntelCommit(input.Base)
	if !ok {
		return invalidToolArguments("base must be a lowercase 40-hex commit id"), nil
	}
	head, ok := codeIntelCommit(input.Head)
	if !ok {
		return invalidToolArguments("head must be a lowercase 40-hex commit id"), nil
	}
	data, err := deps.Queries.Diff(ctx, input.RepoID, base, head,
		codeIntelBoundedLimit(input.Limit, codeIntelMaxDiffFiles), "")
	if err != nil {
		return codeIntelToolOutcome(err)
	}
	files, truncated := truncateCodeIntelList(data.Files, codeIntelMaxDiffFiles)
	views := make([]codeIntelChangedFileView, 0, len(files))
	for _, file := range files {
		views = append(views, codeIntelChangedFileView{
			Path:      file.Path,
			Status:    file.Status,
			Additions: file.Additions,
			Deletions: file.Deletions,
			Binary:    file.Binary,
		})
	}
	return codeIntelToolJSON(codeIntelDiffOutput{
		Stats:     codeIntelStatsDocument(data.Stats),
		Files:     views,
		Truncated: data.Truncated || truncated,
	})
}

// codeIntelFactsOutput is the model-facing payload of code.facts/routes/apis.
type codeIntelFactsOutput struct {
	Facts     []codeIntelFactView `json:"facts"`
	Truncated bool                `json:"truncated"`
	HasMore   bool                `json:"hasMore"`
}

// codeIntelFactView is one fact summary plus its verified evidence references.
// It carries no signature, meta document or source text.
type codeIntelFactView struct {
	Kind       string                     `json:"kind"`
	Name       string                     `json:"name"`
	Path       string                     `json:"path"`
	StartLine  *int                       `json:"startLine,omitempty"`
	EndLine    *int                       `json:"endLine,omitempty"`
	Confidence string                     `json:"confidence"`
	Evidence   []codeinteldomain.Evidence `json:"evidence"`
}

// runCodeFacts executes code.facts.
func runCodeFacts(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	return runCodeFactsQuery(ctx, deps, args, "", codeIntelMaxFacts)
}

// runCodeRoutes executes code.routes.
func runCodeRoutes(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	return runCodeFactsQuery(ctx, deps, args, codeinteldomain.KindRoute, codeIntelMaxRoutes)
}

// runCodeAPIs executes code.apis.
func runCodeAPIs(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	return runCodeFactsQuery(ctx, deps, args, codeinteldomain.KindAPI, codeIntelMaxAPIs)
}

// runCodeFactsQuery is the shared implementation of the three fact queries. A
// fixedKind pins routes/apis; the facts tool forwards the model's kind and path
// filters.
func runCodeFactsQuery(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage, fixedKind string, maxResults int) (domain.ToolResult, error) {
	var input struct {
		RepoID int64  `json:"repoId"`
		Commit string `json:"commit"`
		Kind   string `json:"kind"`
		Path   string `json:"path"`
		Limit  int    `json:"limit"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	if input.RepoID <= 0 {
		return invalidToolArguments("repoId must be a positive integer"), nil
	}
	commit, ok := codeIntelCommit(input.Commit)
	if !ok {
		return invalidToolArguments("commit must be a lowercase 40-hex commit id"), nil
	}
	kind, pathFilter := fixedKind, ""
	if fixedKind == "" {
		kind = strings.TrimSpace(input.Kind)
		pathFilter = strings.TrimSpace(input.Path)
	}
	filter := codeinteldomain.EntityFilter{
		Commit: commit,
		Kind:   kind,
		Path:   pathFilter,
		Limit:  codeIntelBoundedLimit(input.Limit, maxResults),
	}
	entities, _, more, err := deps.Queries.Facts(ctx, input.RepoID, filter)
	if err != nil {
		return codeIntelToolOutcome(err)
	}
	entities, truncated := truncateCodeIntelList(entities, maxResults)
	views := make([]codeIntelFactView, 0, len(entities))
	for _, entity := range entities {
		evidence := entity.Evidence
		if len(evidence) == 0 {
			evidence = []codeinteldomain.Evidence{}
		}
		views = append(views, codeIntelFactView{
			Kind:       entity.Kind,
			Name:       entity.Name,
			Path:       entity.Path,
			StartLine:  entity.StartLine,
			EndLine:    entity.EndLine,
			Confidence: entity.Confidence,
			Evidence:   evidence,
		})
	}
	return codeIntelToolJSON(codeIntelFactsOutput{Facts: views, Truncated: truncated, HasMore: more})
}

// codeIntelAnalyzeOutput is the model-facing payload of code.analyze. It is the
// trigger acknowledgement, not the analysis result: the tool never waits for
// the run to finish.
type codeIntelAnalyzeOutput struct {
	AnalysisID string `json:"analysisId"`
	Status     string `json:"status"`
	Reused     bool   `json:"reused"`
}

// runCodeAnalyze executes code.analyze.
func runCodeAnalyze(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	actor, _ := toolInvocationActor(ctx)
	if failure, missing := requireToolActor(actor); missing {
		return failure, nil
	}
	var input struct {
		VersionID  string `json:"versionId"`
		HeadCommit string `json:"headCommit"`
		Force      bool   `json:"force"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	headCommit, ok := codeIntelOptionalCommit(input.HeadCommit)
	if !ok {
		return invalidToolArguments("headCommit must be a lowercase 40-hex commit id"), nil
	}
	analysis, reused, err := deps.Analyses.Start(ctx, versionID, actor.id, headCommit, input.Force)
	if err != nil {
		return codeIntelToolOutcome(err)
	}
	return codeIntelToolJSON(codeIntelAnalyzeOutput{
		AnalysisID: formatCodeIntelID(analysis.ID),
		Status:     analysis.Status,
		Reused:     reused,
	})
}

// codeIntelImpactOutput is the model-facing payload of code.impact.
type codeIntelImpactOutput struct {
	AnalysisID string                   `json:"analysisId"`
	Status     string                   `json:"status"`
	Candidates []codeIntelCandidateView `json:"candidates"`
	Truncated  bool                     `json:"truncated"`
}

// codeIntelCandidateView is one candidate summary. Subject ids are decimal
// strings (D29) and the payload carries no evidence content.
type codeIntelCandidateView struct {
	SubjectKind string `json:"subjectKind"`
	SubjectID   string `json:"subjectId"`
	Reason      string `json:"reason"`
	Confidence  string `json:"confidence"`
	Selected    bool   `json:"selected"`
}

// runCodeImpact executes code.impact.
func runCodeImpact(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		VersionID  string `json:"versionId"`
		AnalysisID string `json:"analysisId"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	var analysis codeinteldomain.CodeAnalysis
	var err error
	if strings.TrimSpace(input.AnalysisID) != "" {
		analysisID, ok := parsePositiveID(input.AnalysisID)
		if !ok {
			return invalidToolArguments("analysisId must be a positive decimal id"), nil
		}
		analysis, err = deps.Analyses.Get(ctx, versionID, analysisID)
	} else {
		analysis, err = deps.Analyses.GetLatest(ctx, versionID)
	}
	if err != nil {
		return codeIntelToolOutcome(err)
	}
	candidates, err := deps.Analyses.ListCandidates(ctx, analysis.ID)
	if err != nil {
		return codeIntelToolOutcome(err)
	}
	candidates, truncated := truncateCodeIntelList(candidates, codeIntelMaxCandidates)
	views := make([]codeIntelCandidateView, 0, len(candidates))
	for _, candidate := range candidates {
		views = append(views, codeIntelCandidateView{
			SubjectKind: candidate.SubjectKind,
			SubjectID:   formatCodeIntelID(candidate.SubjectID),
			Reason:      candidate.Reason,
			Confidence:  candidate.Confidence,
			Selected:    candidate.Selected,
		})
	}
	return codeIntelToolJSON(codeIntelImpactOutput{
		AnalysisID: formatCodeIntelID(analysis.ID),
		Status:     analysis.Status,
		Candidates: views,
		Truncated:  truncated,
	})
}

// codeIntelAnchorsOutput is the model-facing payload of code.anchors.
type codeIntelAnchorsOutput struct {
	Anchors   []codeIntelAnchorView `json:"anchors"`
	Truncated bool                  `json:"truncated"`
}

// codeIntelAnchorView is one anchor summary. Codeintel-owned ids are decimal
// strings (D29); optional lines/commit are omitted when absent.
type codeIntelAnchorView struct {
	ID            string  `json:"id"`
	VersionID     string  `json:"versionId"`
	SubjectKind   string  `json:"subjectKind"`
	SubjectID     string  `json:"subjectId"`
	FilePath      string  `json:"filePath"`
	StartLine     *int    `json:"startLine,omitempty"`
	EndLine       *int    `json:"endLine,omitempty"`
	CommitSHA     *string `json:"commitSha,omitempty"`
	Confidence    string  `json:"confidence"`
	CreatedByKind string  `json:"createdByKind"`
}

// runCodeAnchors executes code.anchors.
func runCodeAnchors(ctx context.Context, deps CodeIntelToolDeps, args json.RawMessage) (domain.ToolResult, error) {
	var input struct {
		VersionID   string `json:"versionId"`
		SubjectKind string `json:"subjectKind"`
		SubjectID   string `json:"subjectId"`
	}
	if failure, invalid := decodeToolArguments(args, &input); invalid {
		return failure, nil
	}
	versionID, ok := parsePositiveID(input.VersionID)
	if !ok {
		return invalidToolArguments("versionId must be a positive decimal id"), nil
	}
	subjectKind := strings.TrimSpace(input.SubjectKind)
	switch subjectKind {
	case "", codeinteldomain.SubjectVersion, codeinteldomain.SubjectRequirement, codeinteldomain.SubjectAppModel:
	default:
		return invalidToolArguments("subjectKind must be one of version, requirement or app_model"), nil
	}
	subjectID, ok := codeIntelOptionalID(input.SubjectID)
	if !ok {
		return invalidToolArguments("subjectId must be a positive decimal id"), nil
	}
	anchors, err := deps.Anchors.List(ctx, versionID, codeinteldomain.AnchorFilter{
		SubjectKind: subjectKind,
		SubjectID:   subjectID,
	})
	if err != nil {
		return codeIntelToolOutcome(err)
	}
	anchors, truncated := truncateCodeIntelList(anchors, codeIntelMaxAnchors)
	views := make([]codeIntelAnchorView, 0, len(anchors))
	for _, anchor := range anchors {
		views = append(views, codeIntelAnchorView{
			ID:            formatCodeIntelID(anchor.ID),
			VersionID:     formatCodeIntelID(anchor.VersionID),
			SubjectKind:   anchor.SubjectKind,
			SubjectID:     formatCodeIntelID(anchor.SubjectID),
			FilePath:      anchor.FilePath,
			StartLine:     anchor.StartLine,
			EndLine:       anchor.EndLine,
			CommitSHA:     anchor.CommitSHA,
			Confidence:    anchor.Confidence,
			CreatedByKind: anchor.CreatedByKind,
		})
	}
	return codeIntelToolJSON(codeIntelAnchorsOutput{Anchors: views, Truncated: truncated})
}

// codeIntelCommit parses a required lowercase 40-hex commit id.
func codeIntelCommit(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if !codeIntelCommitPattern.MatchString(value) {
		return "", false
	}
	return value, true
}

// codeIntelOptionalCommit parses an optional commit id; an absent value is the
// empty commit the application resolves to the fixed branch tip.
func codeIntelOptionalCommit(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", true
	}
	return codeIntelCommit(value)
}

// codeIntelOptionalID parses an optional positive decimal string id.
func codeIntelOptionalID(raw string) (*int64, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	id, ok := parsePositiveID(raw)
	if !ok {
		return nil, false
	}
	return &id, true
}

// codeIntelBoundedLimit applies the §8.1 1..200 input bound, using fallback
// when the model omitted the limit.
func codeIntelBoundedLimit(requested, fallback int) int {
	if requested <= 0 {
		requested = fallback
	}
	if requested > codeIntelMaxRequestLimit {
		requested = codeIntelMaxRequestLimit
	}
	return requested
}

// truncateCodeIntelList applies one output cap and reports whether it cut the
// list.
func truncateCodeIntelList[T any](items []T, max int) ([]T, bool) {
	if len(items) <= max {
		return items, false
	}
	return items[:max], true
}

// codeIntelStatsDocument returns the stored change-set stats document, falling
// back to an empty object when it is absent or malformed.
func codeIntelStatsDocument(raw []byte) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(raw)
}

// formatCodeIntelID renders one codeintel-owned id as the decimal string the
// tool contract uses (D29).
func formatCodeIntelID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// codeIntelToolOutcome maps one codeintel application error onto the tool
// result contract: a typed codeintel domain error is a correctable business
// failure (reason-prefixed, model-facing content), anything else is an
// infrastructure failure returned to the P1 executor. This mirrors the P3
// AgentToolService outcome split.
func codeIntelToolOutcome(err error) (domain.ToolResult, error) {
	if err == nil {
		return domain.ToolResult{}, nil
	}
	if reason := codeinteldomain.Reason(err); reason != "" {
		return domain.ToolResult{Content: codeIntelBusinessContent(reason, err.Error()), IsError: true}, nil
	}
	return domain.ToolResult{}, err
}

// codeIntelBusinessContent prefixes the stable reason code so the model gets an
// actionable, correctable message even though Go error messages are free-form.
func codeIntelBusinessContent(reason, message string) string {
	if reason == "" || strings.Contains(message, reason) {
		return message
	}
	return reason + ": " + message
}

// codeIntelToolJSON marshals one successful tool payload.
func codeIntelToolJSON(payload any) (domain.ToolResult, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.ToolResult{}, fmt.Errorf("agent: encode codeintel tool result: %w", err)
	}
	return domain.ToolResult{Content: string(encoded)}, nil
}
