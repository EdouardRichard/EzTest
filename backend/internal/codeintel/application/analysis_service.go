package codeintelapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// AnalysisRepository persists code_analysis rows.
type AnalysisRepository interface {
	Create(ctx context.Context, analysis codeintel.Analysis) (codeintel.Analysis, error)
	Get(ctx context.Context, id int64) (codeintel.Analysis, error)
	Latest(ctx context.Context, versionID, repoID int64) (codeintel.Analysis, error)
	Update(ctx context.Context, analysis codeintel.Analysis) error
	Active(ctx context.Context, versionID, repoID int64) (codeintel.Analysis, bool, error)
	DoneByRange(ctx context.Context, versionID, repoID int64, base, head string) (codeintel.Analysis, bool, error)
	MaxVersion(ctx context.Context, versionID, repoID int64) (int, error)
}

// FactRepository persists immutable code_fact versions and reads the current
// projection used by T1 caching and cross-stack contract matching.
type FactRepository interface {
	InsertVersion(ctx context.Context, fact codeintel.Fact) (codeintel.Fact, bool, error)
	LatestByPath(ctx context.Context, repoID int64, commitSHA, path string) ([]codeintel.Fact, error)
	LatestByCommit(ctx context.Context, repoID int64, commitSHA string, kinds []codeintel.EntityKind) ([]codeintel.Fact, error)
	List(ctx context.Context, repoID int64, commitSHA, kind, path, cursor string, limit int) ([]codeintel.Fact, string, bool, error)
	ByID(ctx context.Context, id int64) (codeintel.Fact, error)
}

// AnalysisDeps is the T1 analysis wiring.
type AnalysisDeps struct {
	Analyses  AnalysisRepository
	Facts     FactRepository
	Entities  EntityRepository
	Git       GitClient
	Extractor FactExtractor
	Models    ModelResolver
	Bulk      FileWindowReader
	Versions  VersionReader
	Resources ResourceReader
	Design    DesignReader
	Queue     WorkQueue
	Events    EventPublisher
	Audit     AuditRecorder
	Settings  SettingsReader
	Clock     Clock
	IDs       IDGenerator
}

// AnalysisService owns T1 analyses: Start validates the version baseline/head
// and enqueues code.analyze; Execute extracts AI facts, validates evidence and
// materializes the code_entity/code_edge projection.
type AnalysisService struct {
	analyses  AnalysisRepository
	facts     FactRepository
	entities  EntityRepository
	git       GitClient
	extractor FactExtractor
	models    ModelResolver
	bulk      FileWindowReader
	versions  VersionReader
	resources ResourceReader
	queue     WorkQueue
	events    EventPublisher
	audit     AuditRecorder
	settings  SettingsReader
	clock     Clock
	ids       IDGenerator
}

// NewAnalysisService wires the T1 service with test-friendly defaults.
func NewAnalysisService(deps AnalysisDeps) *AnalysisService {
	return &AnalysisService{
		analyses:  deps.Analyses,
		facts:     deps.Facts,
		entities:  deps.Entities,
		git:       deps.Git,
		extractor: deps.Extractor,
		models:    deps.Models,
		bulk:      deps.Bulk,
		versions:  deps.Versions,
		resources: deps.Resources,
		queue:     deps.Queue,
		events:    deps.Events,
		audit:     deps.Audit,
		settings:  deps.Settings,
		clock:     clockOrSystem(deps.Clock),
		ids:       idsOrFallback(deps.IDs),
	}
}

// ChangeSet is the GET /changes preview model (base = version_sut baseline).
type ChangeSet struct {
	RepoID        int64
	BaseCommitSHA string
	HeadCommitSHA string
	Files         []codeintel.ChangedFile
	Truncated     bool
}

// Start creates or reuses a version×repo analysis. A missing baseline maps to
// baseline_commit_missing; a non-descendant head maps to invalid_head.
func (s *AnalysisService) Start(ctx context.Context, versionID, repoID int64, headCommit string, force bool) (codeintel.Analysis, bool, error) {
	bindings, err := s.versions.VersionSUTBindings(ctx, versionID)
	if err != nil {
		return codeintel.Analysis{}, false, err
	}
	if _, ok := bindingForRepo(bindings, repoID); !ok {
		return codeintel.Analysis{}, false, unprocessable("repo_not_bound", "repository is not bound to this version")
	}
	base, baselineErr := baselineForRepo(bindings, repoID)
	if baselineErr != nil {
		return codeintel.Analysis{}, false, baselineErr
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return codeintel.Analysis{}, false, unprocessable("baseline_commit_missing", "the version baseline commit is not resolved; index the repository first")
	}
	if !ValidCommitSHA(base) {
		return codeintel.Analysis{}, false, unprocessable("baseline_commit_missing", "the version baseline commit is not valid")
	}
	repository, err := s.resources.CodeRepository(ctx, repoID)
	if err != nil {
		return codeintel.Analysis{}, false, mapDomainError(err, "code repository")
	}
	head := strings.TrimSpace(headCommit)
	if head != "" {
		resolved, resolveErr := s.git.ResolveCommit(ctx, repository.Path, head)
		if resolveErr != nil {
			return codeintel.Analysis{}, false, resolveErr
		}
		head = resolved
	} else {
		resolved, headErr := s.git.HeadCommit(ctx, repository.Path, repository.Branch)
		if headErr != nil {
			return codeintel.Analysis{}, false, headErr
		}
		head = resolved
	}
	if head != base {
		ancestor, ancestorErr := s.git.IsAncestor(ctx, repository.Path, base, head)
		if ancestorErr != nil {
			return codeintel.Analysis{}, false, ancestorErr
		}
		if !ancestor {
			return codeintel.Analysis{}, false, unprocessable("invalid_head", "head commit must descend from the version baseline commit")
		}
	}

	if _, active, activeErr := s.analyses.Active(ctx, versionID, repoID); activeErr != nil {
		return codeintel.Analysis{}, false, activeErr
	} else if active {
		return codeintel.Analysis{}, false, stateConflict("analysis_in_progress", "an analysis is already in progress for this version and repository")
	}
	if !force {
		if existing, found, doneErr := s.analyses.DoneByRange(ctx, versionID, repoID, base, head); doneErr != nil {
			return codeintel.Analysis{}, false, doneErr
		} else if found {
			return existing, false, nil
		}
	}

	version, err := s.analyses.MaxVersion(ctx, versionID, repoID)
	if err != nil {
		return codeintel.Analysis{}, false, err
	}
	now := s.clock.Now()
	analysis := codeintel.Analysis{
		ID:              s.ids.Next(),
		VersionID:       versionID,
		RepoID:          repoID,
		BaseCommitSHA:   base,
		HeadCommitSHA:   head,
		Status:          codeintel.AnalysisQueued,
		AnalysisVersion: version + 1,
		ChangedFiles:    json.RawMessage(`[]`),
		Stats:           json.RawMessage(`{}`),
		Unmapped:        json.RawMessage(`[]`),
		Errors:          json.RawMessage(`[]`),
		FactVersions:    json.RawMessage(`{}`),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID := actor.ID
		analysis.CreatedBy = &actorID
	}

	if base == head {
		// A zero-change range is completed synchronously; no LLM and no work
		// item are needed (spec §5.3 step 5).
		analysis.Status = codeintel.AnalysisDone
		analysis.FinishedAt = &now
		analysis.Stats = mustJSON(map[string]any{"changedFiles": 0, "facts": 0, "edges": 0})
		stored, createErr := s.analyses.Create(ctx, analysis)
		if createErr != nil {
			return codeintel.Analysis{}, false, createErr
		}
		s.publishAnalysis(ctx, "code.analysis_finished", stored, map[string]any{
			"analysis_id":   platformidFormat(stored.ID),
			"status":        string(stored.Status),
			"degraded":      stored.Degraded,
			"changed_files": 0,
			"facts":         0,
			"candidates":    0,
			"unmapped":      0,
		})
		return stored, true, nil
	}

	stored, err := s.analyses.Create(ctx, analysis)
	if err != nil {
		return codeintel.Analysis{}, false, err
	}
	payload := map[string]any{
		"analysis_id":     platformidFormat(stored.ID),
		"repo_id":         platformidFormat(repoID),
		"base_commit_sha": base,
		"head_commit_sha": head,
	}
	if _, _, err := s.queue.Ensure(ctx, NewWorkItem{
		VersionID:   versionID,
		Team:        CodeTeam,
		Type:        WorkTypeCodeAnalyze,
		Priority:    codePriority,
		DedupeKey:   "code:analyze:" + platformidFormat(stored.ID),
		Payload:     payload,
		MaxAttempts: codeMaxAttempts,
	}); err != nil {
		s.failAnalysis(ctx, stored, err)
		return codeintel.Analysis{}, false, err
	}
	s.recordAudit(ctx, "code.analysis.start", "code_analysis", platformidFormat(stored.ID), map[string]any{
		"version_id":      platformidFormat(versionID),
		"repo_id":         platformidFormat(repoID),
		"base_commit_sha": base,
		"head_commit_sha": head,
	})
	return stored, true, nil
}

// analysisAccumulator collects bounded T1 results while Execute walks files.
type analysisAccumulator struct {
	changedFiles   []map[string]any
	dominantFacts  []codeintel.Fact
	factVersions   map[string]int
	unmapped       []map[string]any
	errors         []map[string]any
	analyzedFiles  int
	skipped        int
	cacheHits      int
	aiUnavailable  int
	invalidFacts   int
	evidenceChecks int
	evidenceFail   int
	factsStored    int
	edgesStored    int
	truncated      bool
	edgeDrafts     []edgeCandidate
}

type edgeCandidate struct {
	srcKey     string
	dstKey     string
	rel        codeintel.EdgeRel
	confidence codeintel.Confidence
}

func (a *analysisAccumulator) addUnmapped(path, reason string) {
	if len(a.unmapped) >= codeintel.MaxUnmappedEntries {
		return
	}
	a.unmapped = append(a.unmapped, map[string]any{"path": path, "reason": reason})
}

func (a *analysisAccumulator) addError(code, message string) {
	if len(a.errors) >= codeintel.MaxAnalysisErrors {
		return
	}
	a.errors = append(a.errors, map[string]any{"code": code, "message": truncateString(message, 500)})
}

func (a *analysisAccumulator) addFactVersion(factKey string, version int) {
	if len(a.factVersions) >= codeintel.MaxFactVersionsSnapshot {
		return
	}
	a.factVersions[factKey] = version
}

// Execute runs one queued T1 analysis. LLM failures degrade the run to
// done+degraded; git/internal failures mark it failed so the worker can retry.
func (s *AnalysisService) Execute(ctx context.Context, analysisID int64) error {
	analysis, err := s.analyses.Get(ctx, analysisID)
	if err != nil {
		return err
	}
	repository, err := s.resources.CodeRepository(ctx, analysis.RepoID)
	if err != nil {
		s.failAnalysis(ctx, analysis, err)
		return mapDomainError(err, "code repository")
	}

	timeoutSeconds := readIntSetting(ctx, s.settings, SettingAnalysisTimeout, defaultAnalysisTimeout, 1800)
	if timeoutSeconds < 10 {
		timeoutSeconds = 10
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	settleCtx := context.WithoutCancel(ctx)

	now := s.clock.Now()
	analysis.Status = codeintel.AnalysisRunning
	analysis.StartedAt = &now
	analysis.UpdatedAt = now
	if err := s.analyses.Update(settleCtx, analysis); err != nil {
		return err
	}
	s.publishAnalysis(ctx, "code.analysis_started", analysis, map[string]any{
		"analysis_id":     platformidFormat(analysis.ID),
		"repo_id":         platformidFormat(analysis.RepoID),
		"base_commit_sha": analysis.BaseCommitSHA,
		"head_commit_sha": analysis.HeadCommitSHA,
	})

	diffResult, err := s.git.Diff(runCtx, repository.Path, analysis.BaseCommitSHA, analysis.HeadCommitSHA, codeintel.MaxChangedFilesStored)
	if err != nil {
		if runCtx.Err() != nil {
			err = analysisTimeoutError()
		}
		s.failAnalysis(settleCtx, analysis, err)
		return err
	}
	acc := &analysisAccumulator{
		changedFiles: make([]map[string]any, 0, len(diffResult.Files)),
		unmapped:     make([]map[string]any, 0),
		errors:       make([]map[string]any, 0),
		factVersions: map[string]int{},
		truncated:    diffResult.Truncated,
	}
	for _, file := range diffResult.Files {
		acc.changedFiles = append(acc.changedFiles, changedFileJSON(file))
	}

	promptVersion := readStringSetting(ctx, s.settings, SettingPromptVersion, defaultPromptVersion)
	maxChangedFiles := readIntSetting(ctx, s.settings, SettingMaxChangedFiles, defaultMaxChangedFiles, codeintel.MaxChangedFilesStored)
	maxFileBytes := readIntSetting(ctx, s.settings, SettingMaxFileBytes, defaultMaxFileBytes, 1<<20)
	maxExcerptLines := readIntSetting(ctx, s.settings, SettingMaxExcerptLines, defaultMaxExcerptLines, codeintel.MaxExcerptLines)

	model := ""
	aiAvailable := s.extractor != nil && s.bulk != nil
	if aiAvailable && s.models != nil {
		resolved, modelErr := s.models.Model(runCtx)
		if modelErr != nil {
			aiAvailable = false
			acc.aiUnavailable++
			acc.addError("ai_unavailable", "the configured language model is unavailable")
		} else {
			model = resolved
		}
	}

	candidates := make([]codeintel.ChangedFile, 0, len(diffResult.Files))
	for _, file := range diffResult.Files {
		switch {
		case file.Status == "deleted":
			acc.addUnmapped(file.Path, "deleted_file")
		case file.Binary:
			acc.addUnmapped(file.Path, "unsupported_content")
		default:
			candidates = append(candidates, file)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
	if len(candidates) > maxChangedFiles {
		acc.skipped = len(candidates) - maxChangedFiles
		candidates = candidates[:maxChangedFiles]
	}

	entityIDs := make(map[string]int64)
	for _, file := range candidates {
		if !aiAvailable {
			acc.addUnmapped(file.Path, "ai_unavailable")
			continue
		}
		blobHash := ""
		if file.HeadBlobSHA != nil {
			blobHash = "git-sha1:" + *file.HeadBlobSHA
		}
		cached, cacheErr := s.facts.LatestByPath(runCtx, analysis.RepoID, analysis.HeadCommitSHA, file.Path)
		if cacheErr != nil {
			s.failAnalysis(settleCtx, analysis, cacheErr)
			return cacheErr
		}
		if isFileCacheHit(cached, blobHash, model, promptVersion) {
			acc.cacheHits++
			acc.analyzedFiles++
			for _, fact := range cached {
				acc.addFactVersion(fact.FactKey, fact.Version)
				acc.dominantFacts = append(acc.dominantFacts, fact)
			}
			continue
		}

		window, readErr := s.bulk.ReadFileWindow(runCtx, repository.Path, analysis.HeadCommitSHA, file.Path, maxFileBytes, 0)
		if readErr != nil {
			if errors.Is(readErr, context.DeadlineExceeded) || runCtx.Err() != nil {
				timeoutErr := analysisTimeoutError()
				s.failAnalysis(settleCtx, analysis, timeoutErr)
				return timeoutErr
			}
			acc.addUnmapped(file.Path, "unsupported_content")
			acc.skipped++
			continue
		}
		output, extractErr := s.extractor.Extract(runCtx, FactExtractInput{
			Repo:            repository,
			CommitSHA:       analysis.HeadCommitSHA,
			Path:            file.Path,
			Lines:           window.Lines,
			MaxFacts:        codeintel.MaxFactsPerFile,
			MaxEdges:        codeintel.MaxEdgesPerFile,
			MaxExcerptLines: maxExcerptLines,
			PromptVersion:   promptVersion,
		})
		if extractErr != nil {
			if errors.Is(extractErr, context.DeadlineExceeded) || runCtx.Err() != nil {
				timeoutErr := analysisTimeoutError()
				s.failAnalysis(settleCtx, analysis, timeoutErr)
				return timeoutErr
			}
			acc.aiUnavailable++
			acc.addUnmapped(file.Path, "ai_unavailable")
			acc.addError("ai_unavailable", "t1 fact extraction failed for one file")
			continue
		}
		acc.analyzedFiles++
		facts, drafts, factStats := s.validateDrafts(analysis, file.Path, window.Lines, output, blobHash, model, promptVersion, maxExcerptLines)
		acc.invalidFacts += factStats.invalid
		acc.evidenceChecks += factStats.checks
		acc.evidenceFail += factStats.failures
		for _, fact := range facts {
			stored, reused, insertErr := s.facts.InsertVersion(runCtx, fact)
			if insertErr != nil {
				s.failAnalysis(settleCtx, analysis, insertErr)
				return insertErr
			}
			_ = reused
			acc.factsStored++
			acc.addFactVersion(stored.FactKey, stored.Version)
			acc.dominantFacts = append(acc.dominantFacts, stored)
			entity := codeintel.Entity{
				RepoID:      analysis.RepoID,
				CommitSHA:   analysis.HeadCommitSHA,
				Kind:        stored.Kind,
				Path:        stored.Path,
				Name:        stored.Name,
				Signature:   stored.Signature,
				StartLine:   stored.StartLine,
				EndLine:     stored.EndLine,
				BlobHash:    stored.BlobHash,
				Source:      "ai",
				Confidence:  stored.Confidence,
				Evidence:    stored.Evidence,
				Meta:        json.RawMessage(`{}`),
				FactKey:     stored.FactKey,
				FactVersion: stored.Version,
				AnalysisID:  &analysis.ID,
			}
			projected, projectionErr := s.entities.UpsertFactEntity(runCtx, entity)
			if projectionErr != nil {
				s.failAnalysis(settleCtx, analysis, projectionErr)
				return projectionErr
			}
			entityIDs[stored.FactKey] = projected.ID
		}
		for _, draft := range drafts {
			acc.edgeDrafts = append(acc.edgeDrafts, draft)
		}
	}

	if aiAvailable {
		if len(acc.dominantFacts) > 0 {
			keys := make([]string, 0, len(acc.dominantFacts))
			for _, fact := range acc.dominantFacts {
				keys = append(keys, fact.FactKey)
			}
			projected, projectionErr := s.entities.EntitiesByFactKeys(runCtx, analysis.RepoID, analysis.HeadCommitSHA, keys)
			if projectionErr != nil {
				s.failAnalysis(settleCtx, analysis, projectionErr)
				return projectionErr
			}
			for _, entity := range projected {
				entityIDs[entity.FactKey] = entity.ID
			}
		}

		for _, draft := range acc.edgeDrafts {
			srcID, srcOK := entityIDs[draft.srcKey]
			dstID, dstOK := entityIDs[draft.dstKey]
			if !srcOK || !dstOK {
				continue
			}
			edge := codeintel.Edge{
				ID: s.ids.Next(), RepoID: analysis.RepoID, CommitSHA: analysis.HeadCommitSHA,
				AnalysisID: &analysis.ID, SrcEntityID: srcID, DstEntityID: dstID,
				Rel: draft.rel, Source: "ai", Confidence: draft.confidence,
				Evidence: json.RawMessage(`[]`), Meta: json.RawMessage(`{}`),
				FactKey:     truncateString(draft.srcKey+"->"+draft.dstKey, codeintel.MaxFactKeyChars),
				FactVersion: 1, CreatedAt: s.clock.Now(), UpdatedAt: s.clock.Now(),
			}
			if err := s.entities.UpsertEdge(runCtx, edge); err != nil {
				s.failAnalysis(settleCtx, analysis, err)
				return err
			}
			acc.edgesStored++
		}
		if err := s.insertContractEdges(runCtx, analysis, entityIDs, acc); err != nil {
			s.failAnalysis(settleCtx, analysis, err)
			return err
		}
	}

	finished := s.clock.Now()
	analysis.Status = codeintel.AnalysisDone
	analysis.Degraded = acc.aiUnavailable > 0 || (len(candidates) > 0 && !aiAvailable)
	analysis.Model = model
	analysis.PromptVersion = promptVersion
	analysis.ChangedFiles = mustJSON(acc.changedFiles)
	analysis.Stats = mustJSON(map[string]any{
		"changedFiles":     len(acc.changedFiles),
		"truncated":        acc.truncated,
		"analyzedFiles":    acc.analyzedFiles,
		"skipped":          acc.skipped,
		"cacheHits":        acc.cacheHits,
		"aiUnavailable":    acc.aiUnavailable,
		"invalidFacts":     acc.invalidFacts,
		"evidenceChecks":   acc.evidenceChecks,
		"evidenceFailures": acc.evidenceFail,
		"facts":            acc.factsStored,
		"edges":            acc.edgesStored,
		"unmapped":         len(acc.unmapped),
	})
	analysis.Unmapped = mustJSON(acc.unmapped)
	analysis.Errors = mustJSON(acc.errors)
	analysis.FactVersions = mustJSON(acc.factVersions)
	analysis.FinishedAt = &finished
	analysis.UpdatedAt = finished
	if err := s.analyses.Update(settleCtx, analysis); err != nil {
		return err
	}
	s.publishAnalysis(settleCtx, "code.analysis_finished", analysis, map[string]any{
		"analysis_id":   platformidFormat(analysis.ID),
		"status":        string(analysis.Status),
		"degraded":      analysis.Degraded,
		"changed_files": len(acc.changedFiles),
		"facts":         acc.factsStored,
		"candidates":    0,
		"unmapped":      len(acc.unmapped),
	})
	return nil
}

// factDraftStats counts the server-side evidence checks of one file.
type factDraftStats struct {
	invalid  int
	checks   int
	failures int
}

// validateDrafts turns model drafts into server-validated facts. The model never
// supplies excerpt hashes; the server re-reads the window lines and recomputes
// them. Invalid evidence downgrades the fact to low (clue) instead of failing.
func (s *AnalysisService) validateDrafts(
	analysis codeintel.Analysis,
	filePath string,
	lines []string,
	output FactExtractOutput,
	blobHash, model, promptVersion string,
	maxExcerptLines int,
) ([]codeintel.Fact, []edgeCandidate, factDraftStats) {
	stats := factDraftStats{invalid: output.InvalidFacts}
	facts := make([]codeintel.Fact, 0, len(output.Facts))
	seenKeys := make(map[string]bool, len(output.Facts))
	maxFacts := len(output.Facts)
	if maxFacts > codeintel.MaxFactsPerFile {
		maxFacts = codeintel.MaxFactsPerFile
	}

	for index, draft := range output.Facts {
		if index >= maxFacts {
			stats.invalid++
			continue
		}
		kind := codeintel.EntityKind(strings.TrimSpace(string(draft.Kind)))
		if !kind.Valid() || kind == codeintel.EntityFile {
			stats.invalid++
			continue
		}
		name := truncateString(strings.TrimSpace(draft.Name), 512)
		if name == "" || strings.ContainsAny(name, "\x00\r\n") {
			stats.invalid++
			continue
		}
		factKey, ok := codeintel.FactKey(kind, filePath, name, "", "")
		if !ok || seenKeys[factKey] {
			if !ok {
				stats.invalid++
			}
			continue
		}
		seenKeys[factKey] = true

		startLine, endLine := validDraftLines(draft.StartLine, draft.EndLine, len(lines))
		evidence := make([]map[string]any, 0, len(draft.Evidence))
		invalidEvidence := false
		for _, candidate := range draft.Evidence {
			stats.checks++
			evidencePath := filePath
			if strings.TrimSpace(candidate.File) != "" {
				normalized, normalizeErr := codeintel.NormalizeRepoPath(candidate.File)
				if normalizeErr != nil || normalized != filePath {
					stats.failures++
					invalidEvidence = true
					continue
				}
				evidencePath = normalized
			}
			if candidate.StartLine < 1 || candidate.EndLine < candidate.StartLine ||
				candidate.EndLine > len(lines) || candidate.EndLine-candidate.StartLine+1 > maxExcerptLines {
				stats.failures++
				invalidEvidence = true
				continue
			}
			evidence = append(evidence, map[string]any{
				"file":         evidencePath,
				"start_line":   candidate.StartLine,
				"end_line":     candidate.EndLine,
				"excerpt_hash": codeintel.ExcerptHash(lines[candidate.StartLine-1 : candidate.EndLine]),
			})
			if len(evidence) >= codeintel.MaxEvidencePerFact {
				break
			}
		}
		confidence := draft.Confidence
		if !confidence.Valid() {
			confidence = codeintel.ConfidenceMedium
		}
		meta := map[string]any{}
		if len(evidence) == 0 || invalidEvidence {
			confidence = codeintel.ConfidenceLow
			if len(evidence) == 0 && !invalidEvidence {
				meta["clue"] = true
			}
		}
		if invalidEvidence {
			meta["invalidEvidence"] = true
		}
		body := validJSONObject(draft.Body)
		if len(meta) > 0 {
			body = bodyWithMarkers(body, meta)
		}
		facts = append(facts, codeintel.Fact{
			RepoID:        analysis.RepoID,
			CommitSHA:     analysis.HeadCommitSHA,
			FactKey:       factKey,
			Kind:          kind,
			Path:          filePath,
			Name:          name,
			Signature:     truncateString(strings.TrimSpace(draft.Signature), 2000),
			StartLine:     startLine,
			EndLine:       endLine,
			BlobHash:      blobHash,
			Body:          body,
			Evidence:      mustJSON(evidence),
			Source:        "ai",
			Confidence:    confidence,
			Model:         model,
			PromptVersion: promptVersion,
			AnalysisID:    analysisIDPtr(analysis.ID),
			CreatedAt:     s.clock.Now(),
			UpdatedAt:     s.clock.Now(),
		})
	}

	drafts := make([]edgeCandidate, 0, len(output.Edges))
	for _, draft := range output.Edges {
		if len(drafts) >= codeintel.MaxEdgesPerFile {
			break
		}
		if !draft.Rel.Valid() {
			continue
		}
		src := strings.TrimSpace(draft.SrcFactKey)
		dst := strings.TrimSpace(draft.DstFactKey)
		if src == "" || dst == "" || src == dst || len(src) > codeintel.MaxFactKeyChars || len(dst) > codeintel.MaxFactKeyChars {
			continue
		}
		confidence := draft.Confidence
		if !confidence.Valid() {
			confidence = codeintel.ConfidenceMedium
		}
		drafts = append(drafts, edgeCandidate{srcKey: src, dstKey: dst, rel: draft.Rel, confidence: confidence})
	}
	return facts, drafts, stats
}

// insertContractEdges materializes route_to_api edges for exact
// (method, normalizedPath) client_call × api matches (spec §5.2).
func (s *AnalysisService) insertContractEdges(ctx context.Context, analysis codeintel.Analysis, entityIDs map[string]int64, acc *analysisAccumulator) error {
	latest, err := s.facts.LatestByCommit(ctx, analysis.RepoID, analysis.HeadCommitSHA,
		[]codeintel.EntityKind{codeintel.EntityAPI, codeintel.EntityClientCall})
	if err != nil {
		return err
	}
	// Facts may come from an earlier analysis, so their entity projection is not
	// necessarily in the in-memory map.
	factKeys := make([]string, 0, len(latest))
	for _, fact := range latest {
		factKeys = append(factKeys, fact.FactKey)
	}
	if len(factKeys) > 0 {
		projected, projectionErr := s.entities.EntitiesByFactKeys(ctx, analysis.RepoID, analysis.HeadCommitSHA, factKeys)
		if projectionErr != nil {
			return projectionErr
		}
		for _, entity := range projected {
			entityIDs[entity.FactKey] = entity.ID
		}
	}
	apis := make(map[string]codeintel.Fact)
	clients := make([]codeintel.Fact, 0, len(latest))
	for _, fact := range latest {
		switch fact.Kind {
		case codeintel.EntityAPI:
			apis[strings.TrimPrefix(fact.FactKey, "api:")] = fact
		case codeintel.EntityClientCall:
			clients = append(clients, fact)
		}
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].FactKey < clients[j].FactKey })

	remaining := codeintel.MaxEdgesPerFile
	for _, client := range clients {
		if remaining <= 0 {
			break
		}
		tail := strings.TrimPrefix(client.FactKey, "client:")
		api, ok := apis[tail]
		if !ok {
			acc.addUnmapped(client.Path, "unmatched_contract")
			continue
		}
		srcID, srcOK := entityIDs[client.FactKey]
		dstID, dstOK := entityIDs[api.FactKey]
		if !srcOK || !dstOK {
			acc.addUnmapped(client.Path, "unmatched_contract")
			continue
		}
		edge := codeintel.Edge{
			ID: s.ids.Next(), RepoID: analysis.RepoID, CommitSHA: analysis.HeadCommitSHA,
			AnalysisID: &analysis.ID, SrcEntityID: srcID, DstEntityID: dstID,
			Rel: codeintel.EdgeRouteToAPI, Source: "ai",
			Confidence: minConfidence(client.Confidence, api.Confidence),
			Evidence:   json.RawMessage(`[]`), Meta: json.RawMessage(`{}`),
			FactKey:     truncateString(client.FactKey+"->"+api.FactKey, codeintel.MaxFactKeyChars),
			FactVersion: 1, CreatedAt: s.clock.Now(), UpdatedAt: s.clock.Now(),
		}
		if err := s.entities.UpsertEdge(ctx, edge); err != nil {
			return err
		}
		acc.edgesStored++
		remaining--
	}
	return nil
}

// Latest returns the newest analysis of a version×repo.
func (s *AnalysisService) Latest(ctx context.Context, versionID, repoID int64) (codeintel.Analysis, error) {
	analysis, err := s.analyses.Latest(ctx, versionID, repoID)
	if err != nil {
		return codeintel.Analysis{}, mapDomainError(err, "code analysis")
	}
	return analysis, nil
}

// Get returns one analysis.
func (s *AnalysisService) Get(ctx context.Context, analysisID int64) (codeintel.Analysis, error) {
	analysis, err := s.analyses.Get(ctx, analysisID)
	if err != nil {
		return codeintel.Analysis{}, mapDomainError(err, "code analysis")
	}
	return analysis, nil
}

// Facts pages the current facts of a commit.
func (s *AnalysisService) Facts(ctx context.Context, repoID int64, commit, kind, pathFilter, cursor string, limit int) ([]codeintel.Fact, string, bool, error) {
	commit = strings.TrimSpace(commit)
	if !ValidCommitSHA(commit) {
		return nil, "", false, platform.Invalid("commit", "must be a 40-character lowercase hex commit SHA")
	}
	if kind != "" && !codeintel.EntityKind(kind).Valid() {
		return nil, "", false, platform.Invalid("kind", "must be a supported code entity kind")
	}
	if pathFilter != "" {
		normalized, err := codeintel.NormalizeRepoPath(pathFilter)
		if err != nil {
			return nil, "", false, unprocessable("path_escape", "path must be a normalized repo-relative path")
		}
		pathFilter = normalized
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return s.facts.List(ctx, repoID, commit, kind, pathFilter, strings.TrimSpace(cursor), limit)
}

// Search finds entities by path/name substring.
func (s *AnalysisService) Search(ctx context.Context, repoID int64, commit, query, kind string, limit int) ([]codeintel.Entity, error) {
	commit = strings.TrimSpace(commit)
	if !ValidCommitSHA(commit) {
		return nil, platform.Invalid("commit", "must be a 40-character lowercase hex commit SHA")
	}
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 500 {
		return nil, platform.Invalid("q", "must be between 1 and 500 characters")
	}
	if kind != "" && !codeintel.EntityKind(kind).Valid() {
		return nil, platform.Invalid("kind", "must be a supported code entity kind")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	return s.entities.Search(ctx, repoID, commit, query, kind, limit)
}

// Changes previews the version baseline..head change set without persisting it.
func (s *AnalysisService) Changes(ctx context.Context, versionID, repoID int64, headCommit string) (ChangeSet, error) {
	bindings, err := s.versions.VersionSUTBindings(ctx, versionID)
	if err != nil {
		return ChangeSet{}, err
	}
	if _, ok := bindingForRepo(bindings, repoID); !ok {
		return ChangeSet{}, platform.NotFound("repository")
	}
	base, baselineErr := baselineForRepo(bindings, repoID)
	if baselineErr != nil {
		return ChangeSet{}, baselineErr
	}
	base = strings.TrimSpace(base)
	if base == "" || !ValidCommitSHA(base) {
		return ChangeSet{}, unprocessable("baseline_commit_missing", "the version baseline commit is not resolved; index the repository first")
	}
	repository, err := s.resources.CodeRepository(ctx, repoID)
	if err != nil {
		return ChangeSet{}, mapDomainError(err, "code repository")
	}
	head := strings.TrimSpace(headCommit)
	if head != "" {
		resolved, resolveErr := s.git.ResolveCommit(ctx, repository.Path, head)
		if resolveErr != nil {
			return ChangeSet{}, resolveErr
		}
		head = resolved
	} else {
		resolved, headErr := s.git.HeadCommit(ctx, repository.Path, repository.Branch)
		if headErr != nil {
			return ChangeSet{}, headErr
		}
		head = resolved
	}
	if head != base {
		ancestor, ancestorErr := s.git.IsAncestor(ctx, repository.Path, base, head)
		if ancestorErr != nil {
			return ChangeSet{}, ancestorErr
		}
		if !ancestor {
			return ChangeSet{}, unprocessable("invalid_head", "head commit must descend from the version baseline commit")
		}
	}
	result, err := s.git.Diff(ctx, repository.Path, base, head, codeintel.MaxChangedFilesStored)
	if err != nil {
		return ChangeSet{}, err
	}
	return ChangeSet{RepoID: repoID, BaseCommitSHA: base, HeadCommitSHA: head, Files: result.Files, Truncated: result.Truncated}, nil
}

// failAnalysis persists the failed terminal state and publishes the failure.
func (s *AnalysisService) failAnalysis(ctx context.Context, analysis codeintel.Analysis, cause error) {
	code := "internal"
	message := "code analysis failed"
	var apiErr *platform.Error
	if errors.As(cause, &apiErr) {
		code = apiErr.Code
		if strings.TrimSpace(apiErr.Message) != "" {
			message = apiErr.Message
		}
	}
	var recorded []map[string]any
	if len(analysis.Errors) > 0 {
		_ = json.Unmarshal(analysis.Errors, &recorded)
	}
	if len(recorded) < codeintel.MaxAnalysisErrors {
		recorded = append(recorded, map[string]any{"code": code, "message": truncateString(message, 500)})
	}
	now := s.clock.Now()
	analysis.Status = codeintel.AnalysisFailed
	analysis.FinishedAt = &now
	analysis.UpdatedAt = now
	analysis.Errors = mustJSON(recorded)
	if err := s.analyses.Update(ctx, analysis); err != nil {
		return
	}
	s.publishAnalysis(ctx, "code.analysis_failed", analysis, map[string]any{
		"analysis_id": platformidFormat(analysis.ID),
		"error_code":  code,
		"message":     truncateString(message, 500),
	})
}

// publishAnalysis emits one analysis event on the version channel.
func (s *AnalysisService) publishAnalysis(ctx context.Context, eventType string, analysis codeintel.Analysis, payload map[string]any) {
	publishEvent(ctx, s.events, eventType, analysis.VersionID, payload)
}

func (s *AnalysisService) recordAudit(ctx context.Context, action, targetKind, targetID string, metadata map[string]any) {
	recordAudit(ctx, s.audit, action, targetKind, targetID, metadata)
}

// AnalysisWorkHandler adapts AnalysisService.Execute to the code.analyze work
// type.
type AnalysisWorkHandler struct {
	Service *AnalysisService
}

// Handle decodes {analysis_id} and executes that T1 analysis.
func (h AnalysisWorkHandler) Handle(ctx context.Context, item WorkItem) error {
	analysisID, err := decodeWorkItemID(item.Payload, "analysis_id")
	if err != nil {
		return err
	}
	if h.Service == nil {
		return platform.Unavailable("codeintel analysis service is not configured")
	}
	return h.Service.Execute(ctx, analysisID)
}

// analysisTimeoutError is the stable retryable timeout used by the worker.
func analysisTimeoutError() error {
	apiErr := platform.NewError(http.StatusServiceUnavailable, "analysis_timeout", "code analysis timed out")
	apiErr.Retryable = true
	return apiErr
}

func analysisIDPtr(id int64) *int64 { return &id }

// validDraftLines bounds the model-provided fact line range to the file.
func validDraftLines(start, end, total int) (*int, *int) {
	if start < 1 || end < start || end > total {
		return nil, nil
	}
	return &start, &end
}

// validJSONObject returns the draft body when it is a JSON object, otherwise an
// empty object.
func validJSONObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// bodyWithMarkers merges reserved validation markers (clue/invalidEvidence) into
// the structured fact body. code_fact has no meta column (spec §3.3), so the
// low-confidence markers travel with the body object.
func bodyWithMarkers(body json.RawMessage, markers map[string]any) json.RawMessage {
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil || value == nil {
		value = map[string]any{}
	}
	for key, marker := range markers {
		value[key] = marker
	}
	return mustJSON(value)
}

// isFileCacheHit implements the file-level fact cache:
// (repo, commit, path, blob_hash, model, prompt_version) already extracted.
func isFileCacheHit(facts []codeintel.Fact, blobHash, model, promptVersion string) bool {
	if len(facts) == 0 {
		return false
	}
	for _, fact := range facts {
		if fact.BlobHash != blobHash || fact.PromptVersion != promptVersion {
			return false
		}
		if model != "" && fact.Model != model {
			return false
		}
	}
	return true
}

// minConfidence returns the weaker of two confidences.
func minConfidence(left, right codeintel.Confidence) codeintel.Confidence {
	rank := func(value codeintel.Confidence) int {
		switch value {
		case codeintel.ConfidenceHigh:
			return 3
		case codeintel.ConfidenceMedium:
			return 2
		default:
			return 1
		}
	}
	if rank(left) <= rank(right) {
		return left
	}
	return right
}

// changedFileJSON renders one changed file in the persisted/changed_files shape.
func changedFileJSON(file codeintel.ChangedFile) map[string]any {
	var oldPath any
	if file.OldPath != "" {
		oldPath = file.OldPath
	}
	var headBlob any
	if file.HeadBlobSHA != nil {
		headBlob = *file.HeadBlobSHA
	}
	return map[string]any{
		"path":           file.Path,
		"old_path":       oldPath,
		"status":         file.Status,
		"additions":      file.Additions,
		"deletions":      file.Deletions,
		"binary":         file.Binary,
		"head_blob_hash": headBlob,
	}
}

// platformidFormat keeps the decimal-string contract in one place.
func platformidFormat(value int64) string { return formatID(value) }
