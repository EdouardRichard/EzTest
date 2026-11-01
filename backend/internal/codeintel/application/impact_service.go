package codeintelapp

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// CandidateRepository persists code_impact_candidate rows. ReplaceForAnalysis
// atomically replaces the candidate set of one analysis; UpdateSelection
// applies one explicit selection and records the actor/time.
type CandidateRepository interface {
	ReplaceForAnalysis(ctx context.Context, analysisID int64, candidates []codeintel.ImpactCandidate) error
	ListByAnalysis(ctx context.Context, analysisID int64) ([]codeintel.ImpactCandidate, error)
	UpdateSelection(ctx context.Context, analysisID, candidateID, actorID int64, selected bool) (codeintel.ImpactCandidate, error)
}

// ImpactEdge is the edge projection needed by one-hop contract propagation. It
// carries the fact keys of both endpoints so the service never needs entity IDs.
type ImpactEdge struct {
	SrcFactKey string
	DstFactKey string
	Rel        codeintel.EdgeRel
	Confidence codeintel.Confidence
}

// ImpactEdgeReader reads the materialized code_edge rows of one commit.
type ImpactEdgeReader interface {
	ImpactEdges(ctx context.Context, repoID int64, commitSHA string) ([]ImpactEdge, error)
}

// CaseSourceRef is one design case_source of kind=code (read-only compatibility,
// spec §6.1): a case version pointing at a repository path.
type CaseSourceRef struct {
	CaseVersionID int64
	RepoID        int64
	Path          string
}

// CaseSourceReader reads the design case sources of a version. The design
// context implements it; P2 tables are never written.
type CaseSourceReader interface {
	CaseSources(ctx context.Context, versionID int64) ([]CaseSourceRef, error)
}

// ImpactCandidateView tags one candidate with its analysis repo and the version
// SUTs bound to that repo (multi-repo union, spec §5.4).
type ImpactCandidateView struct {
	codeintel.ImpactCandidate
	RepoID        int64
	VersionSUTIDs []int64
}

// ImpactDeps is the impact-analysis wiring. Edges, CaseSources, Reviews and
// Subjects are optional: without them contract/case-source/review rules are
// skipped, which keeps the deterministic anchor/fact rules available.
type ImpactDeps struct {
	Analyses    AnalysisRepository
	Candidates  CandidateRepository
	Facts       FactRepository
	Entities    EntityRepository
	Anchors     AnchorRepository
	Reviews     ReviewRepository
	Edges       ImpactEdgeReader
	CaseSources CaseSourceReader
	Design      DesignReader
	Resources   ResourceReader
	Versions    VersionReader
	Extractor   FactExtractor
	Bulk        FileWindowReader
	Subjects    AnchorSubjectReader
	Events      EventPublisher
	Audit       AuditRecorder
	Settings    SettingsReader
	Clock       Clock
	IDs         IDGenerator
}

// ImpactService computes and maintains the impact candidates of one analysis
// (spec §5.4): deterministic direct/contract/relation/case candidates plus one
// AI inference call. The deterministic result survives an AI failure (degraded).
type ImpactService struct {
	analyses    AnalysisRepository
	candidates  CandidateRepository
	facts       FactRepository
	entities    EntityRepository
	anchors     AnchorRepository
	reviews     ReviewRepository
	edges       ImpactEdgeReader
	caseSources CaseSourceReader
	design      DesignReader
	resources   ResourceReader
	versions    VersionReader
	extractor   FactExtractor
	bulk        FileWindowReader
	subjects    AnchorSubjectReader
	events      EventPublisher
	audit       AuditRecorder
	settings    SettingsReader
	clock       Clock
	ids         IDGenerator
}

// NewImpactService wires the impact service with test-friendly defaults.
func NewImpactService(deps ImpactDeps) *ImpactService {
	return &ImpactService{
		analyses:    deps.Analyses,
		candidates:  deps.Candidates,
		facts:       deps.Facts,
		entities:    deps.Entities,
		anchors:     deps.Anchors,
		reviews:     deps.Reviews,
		edges:       deps.Edges,
		caseSources: deps.CaseSources,
		design:      deps.Design,
		resources:   deps.Resources,
		versions:    deps.Versions,
		extractor:   deps.Extractor,
		bulk:        deps.Bulk,
		subjects:    deps.Subjects,
		events:      deps.Events,
		audit:       deps.Audit,
		settings:    deps.Settings,
		clock:       clockOrSystem(deps.Clock),
		ids:         idsOrFallback(deps.IDs),
	}
}

// Compute builds, persists and returns the candidate set of one done analysis.
// It also merges analysis unmapped entries and flips analysis.degraded when the
// AI impact call is unavailable; deterministic candidates never fail the run.
func (s *ImpactService) Compute(ctx context.Context, analysisID int64) ([]codeintel.ImpactCandidate, error) {
	analysis, err := s.analyses.Get(ctx, analysisID)
	if err != nil {
		return nil, mapDomainError(err, "code analysis")
	}
	if analysis.Status != codeintel.AnalysisDone {
		return nil, stateConflict("analysis_not_done", "impact analysis requires a done code analysis")
	}
	repository, err := s.resources.CodeRepository(ctx, analysis.RepoID)
	if err != nil {
		return nil, mapDomainError(err, "code repository")
	}
	bindings, err := s.versions.VersionSUTBindings(ctx, analysis.VersionID)
	if err != nil {
		return nil, err
	}
	versionSUTIDs := versionSUTIDsForRepo(bindings, analysis.RepoID)
	changedPaths, changedFiles := decodeChangedFilesJSON(analysis.ChangedFiles)
	pages, apis, err := s.sutResources(ctx, versionSUTIDs)
	if err != nil {
		return nil, err
	}
	requirements, caseVersions, relations, err := s.designInputs(ctx, analysis.VersionID)
	if err != nil {
		return nil, err
	}

	facts, analyzedKeys, err := s.analysisFacts(ctx, analysis)
	if err != nil {
		return nil, err
	}
	incorrect := map[string]bool{}
	if s.reviews != nil && len(facts) > 0 {
		keys := make([]string, 0, len(facts))
		for _, fact := range facts {
			keys = append(keys, fact.FactKey)
		}
		verdicts, verdictErr := s.reviews.LatestVerdicts(ctx, analysis.RepoID, analysis.HeadCommitSHA, keys)
		if verdictErr != nil {
			return nil, verdictErr
		}
		for key, verdict := range verdicts {
			if verdict == string(codeintel.ReviewIncorrect) {
				incorrect[key] = true
			}
		}
	}

	merge := newCandidateMerge()
	anchorPaths := map[string]bool{}
	if s.anchors != nil {
		anchors, anchorErr := s.anchors.List(ctx, analysis.VersionID, "", 0)
		if anchorErr != nil {
			return nil, anchorErr
		}
		for _, anchor := range anchors {
			if anchor.RepoID != analysis.RepoID || !changedPaths[anchor.FilePath] {
				continue
			}
			anchorPaths[anchor.FilePath] = true
			kind, ok := impactAnchorSubjectKind(anchor.SubjectKind)
			if !ok {
				continue
			}
			merge.add(kind, anchor.SubjectID, codeintel.CandidateDirect, codeintel.ConfidenceHigh,
				"anchor path is in the change set", nil)
		}
	}
	if s.caseSources != nil {
		sources, sourceErr := s.caseSources.CaseSources(ctx, analysis.VersionID)
		if sourceErr != nil {
			return nil, sourceErr
		}
		for _, source := range sources {
			if source.RepoID != 0 && source.RepoID != analysis.RepoID {
				continue
			}
			if !changedPaths[source.Path] {
				continue
			}
			anchorPaths[source.Path] = true
			merge.add(codeintel.SubjectCaseVersion, source.CaseVersionID, codeintel.CandidateDirect,
				codeintel.ConfidenceHigh, "case source path is in the change set", nil)
		}
	}

	// Direct page/API facts (incorrect facts never enter automatic impact).
	for _, fact := range facts {
		if incorrect[fact.FactKey] {
			continue
		}
		if route, ok := impactFactRoute(fact); ok {
			if page, found := pages.byRoute[route]; found {
				merge.add(codeintel.SubjectSUTPage, page.ID, codeintel.CandidateDirect, fact.Confidence,
					"route fact matches a SUT page", factEvidence(fact))
			}
		}
		if method, path, ok := impactFactAPI(fact); ok {
			if api, found := apis.byRoute[apiRouteKey(method, path)]; found {
				merge.add(codeintel.SubjectSUTAPI, api.ID, codeintel.CandidateDirect, fact.Confidence,
					"api fact matches a SUT API", factEvidence(fact))
			}
		}
	}

	// One-hop contract propagation through route_to_api edges.
	var impactEdges []ImpactEdge
	if s.edges != nil {
		edges, edgeErr := s.edges.ImpactEdges(ctx, analysis.RepoID, analysis.HeadCommitSHA)
		if edgeErr != nil {
			return nil, edgeErr
		}
		impactEdges = edges
		for _, edge := range edges {
			if edge.Rel != codeintel.EdgeRouteToAPI {
				continue
			}
			srcChanged := analyzedKeys[edge.SrcFactKey]
			dstChanged := analyzedKeys[edge.DstFactKey]
			if !srcChanged && !dstChanged {
				continue
			}
			method, path, ok := parseImpactFactKey(edge.DstFactKey)
			if !ok {
				continue
			}
			if api, found := apis.byRoute[apiRouteKey(method, path)]; found {
				merge.add(codeintel.SubjectSUTAPI, api.ID, codeintel.CandidateContract, edge.Confidence,
					"route_to_api edge connects a changed call", nil)
			}
		}
	}

	// One-hop requirement relation propagation from directly impacted requirements.
	directRequirements := merge.requirementIDs()
	relatedRequirements := map[int64]bool{}
	for _, relation := range relations {
		if !relationKindValid(relation.Kind) {
			continue
		}
		switch {
		case directRequirements[relation.FromRequirementID]:
			relatedRequirements[relation.ToRequirementID] = true
		case directRequirements[relation.ToRequirementID]:
			relatedRequirements[relation.FromRequirementID] = true
		}
	}
	affectedRequirements := map[int64]bool{}
	for id := range directRequirements {
		affectedRequirements[id] = true
	}
	for id := range relatedRequirements {
		affectedRequirements[id] = true
		merge.add(codeintel.SubjectRequirement, id, codeintel.CandidateRelation, codeintel.ConfidenceMedium,
			"related requirement is directly impacted", nil)
	}
	// Current+confirmed case versions under impacted requirements.
	for _, caseVersion := range caseVersions {
		if !affectedRequirements[caseVersion.RequirementID] {
			continue
		}
		merge.add(codeintel.SubjectCaseVersion, caseVersion.ID, codeintel.CandidateRelation,
			codeintel.ConfidenceMedium, "current case version of an impacted requirement", nil)
	}

	// Single AI inference call; failures degrade instead of failing the run.
	aiAvailable := s.extractor != nil
	if aiAvailable {
		output, aiErr := s.extractor.Impact(ctx, ImpactInferenceInput{
			Repo:            repository,
			CommitSHA:       analysis.HeadCommitSHA,
			ChangedFiles:    changedFiles,
			Facts:           withoutIncorrectFacts(facts, incorrect),
			Edges:           impactEdgesToDomain(impactEdges),
			Requirements:    limitRequirements(requirements, codeintel.MaxRequirementsInPrompt),
			Selected:        merge.candidates(),
			MaxRequirements: codeintel.MaxRequirementsInPrompt,
			PromptVersion:   readStringSetting(ctx, s.settings, SettingPromptVersion, defaultPromptVersion),
		})
		if aiErr != nil {
			aiAvailable = false
		} else {
			for _, draft := range output.Candidates {
				reason := s.validateAIDraft(ctx, analysis, repository, draft, requirements, caseVersions, pages, apis)
				if reason == "" {
					continue
				}
				evidence := s.validateImpactEvidence(ctx, repository, analysis.HeadCommitSHA, draft.Evidence)
				confidence := draft.Confidence
				if !confidence.Valid() {
					confidence = codeintel.ConfidenceMedium
				}
				if len(draft.Evidence) > 0 && len(evidence) == 0 {
					confidence = codeintel.ConfidenceLow
				}
				merge.add(draft.SubjectKind, draft.SubjectID, codeintel.CandidateAI, confidence, reason, evidence)
			}
		}
	}

	candidates := merge.candidates()
	now := s.clock.Now()
	for index := range candidates {
		candidates[index].CreatedAt = now
		candidates[index].UpdatedAt = now
	}
	unmapped := s.mergeUnmapped(analysis, changedPaths, facts, anchorPaths, aiAvailable)
	if !aiAvailable {
		analysis.Degraded = true
	}
	analysis.Unmapped = mustJSON(unmapped)
	if err := s.analyses.Update(ctx, analysis); err != nil {
		return nil, err
	}
	if err := s.candidates.ReplaceForAnalysis(ctx, analysis.ID, candidates); err != nil {
		return nil, err
	}
	stored, err := s.candidates.ListByAnalysis(ctx, analysis.ID)
	if err != nil {
		return nil, mapDomainError(err, "code impact candidate")
	}
	countsBySource := map[string]int{}
	countsByKind := map[string]int{}
	selected := 0
	for _, candidate := range candidates {
		countsBySource[string(candidate.Source)]++
		countsByKind[string(candidate.SubjectKind)]++
		if candidate.Selected {
			selected++
		}
	}
	publishEvent(ctx, s.events, "code.impact_computed", analysis.VersionID, map[string]any{
		"analysis_id":          platformid.Format(analysis.ID),
		"candidates_by_source": countsBySource,
		"candidates_by_kind":   countsByKind,
		"selected_count":       selected,
	})
	return stored, nil
}

// List returns the stored candidates of one analysis with repo/SUT tags.
func (s *ImpactService) List(ctx context.Context, analysisID int64) ([]ImpactCandidateView, error) {
	analysis, err := s.analyses.Get(ctx, analysisID)
	if err != nil {
		return nil, mapDomainError(err, "code analysis")
	}
	candidates, err := s.candidates.ListByAnalysis(ctx, analysisID)
	if err != nil {
		return nil, mapDomainError(err, "code impact candidate")
	}
	bindings, err := s.versions.VersionSUTBindings(ctx, analysis.VersionID)
	if err != nil {
		return nil, err
	}
	sutIDs := versionSUTIDsForRepo(bindings, analysis.RepoID)
	views := make([]ImpactCandidateView, 0, len(candidates))
	for _, candidate := range candidates {
		views = append(views, ImpactCandidateView{ImpactCandidate: candidate, RepoID: analysis.RepoID, VersionSUTIDs: sutIDs})
	}
	return views, nil
}

// AnalysisVersion returns the version of one analysis; the HTTP layer uses it to
// reject cross-version candidate patches with 404.
func (s *ImpactService) AnalysisVersion(ctx context.Context, analysisID int64) (int64, error) {
	analysis, err := s.analyses.Get(ctx, analysisID)
	if err != nil {
		return 0, mapDomainError(err, "code analysis")
	}
	return analysis.VersionID, nil
}

// Detail returns one analysis with its tagged candidate set. It backs the
// analysis-detail view so GET analyses/{id} can include candidates (spec §4.2
// #15) without the T1 service depending on the impact cache.
func (s *ImpactService) Detail(ctx context.Context, analysisID int64) (codeintel.Analysis, []ImpactCandidateView, error) {
	analysis, err := s.analyses.Get(ctx, analysisID)
	if err != nil {
		return codeintel.Analysis{}, nil, mapDomainError(err, "code analysis")
	}
	views, err := s.List(ctx, analysisID)
	if err != nil {
		return codeintel.Analysis{}, nil, err
	}
	return analysis, views, nil
}

// SetSelected records one explicit candidate selection.
func (s *ImpactService) SetSelected(ctx context.Context, analysisID, candidateID, actorID int64, selected bool) (codeintel.ImpactCandidate, error) {
	if _, err := s.analyses.Get(ctx, analysisID); err != nil {
		return codeintel.ImpactCandidate{}, mapDomainError(err, "code analysis")
	}
	updated, err := s.candidates.UpdateSelection(ctx, analysisID, candidateID, actorID, selected)
	if err != nil {
		return codeintel.ImpactCandidate{}, mapDomainError(err, "code impact candidate")
	}
	recordAudit(ctx, s.audit, "code.impact.candidate_selected", "code_impact_candidate", platformid.Format(updated.ID), map[string]any{
		"analysis_id": platformid.Format(analysisID),
		"selected":    selected,
		"actor_id":    platformid.Format(actorID),
	})
	return updated, nil
}

// Union merges the latest candidate set of every repository bound to the
// version. Evidence is never merged across repositories; the highest-priority
// source wins and reasons are joined (spec §5.4).
func (s *ImpactService) Union(ctx context.Context, versionID int64) ([]ImpactCandidateView, error) {
	bindings, err := s.versions.VersionSUTBindings(ctx, versionID)
	if err != nil {
		return nil, err
	}
	repos := distinctRepoIDs(bindings)
	merged := newCandidateMerge()
	repoBySubject := map[string]int64{}
	sutsBySubject := map[string][]int64{}
	for _, repoID := range repos {
		analysis, latestErr := s.analyses.Latest(ctx, versionID, repoID)
		if latestErr != nil {
			if errors.Is(latestErr, codeintel.ErrNotFound) {
				continue
			}
			return nil, mapDomainError(latestErr, "code analysis")
		}
		candidates, listErr := s.candidates.ListByAnalysis(ctx, analysis.ID)
		if listErr != nil {
			return nil, mapDomainError(listErr, "code impact candidate")
		}
		sutIDs := versionSUTIDsForRepo(bindings, repoID)
		for _, candidate := range candidates {
			key := candidateSubjectKey(candidate.SubjectKind, candidate.SubjectID)
			merged.add(candidate.SubjectKind, candidate.SubjectID, candidate.Source, candidate.Confidence,
				candidate.Reason, decodeEvidence(candidate.Evidence))
			if _, ok := repoBySubject[key]; !ok {
				repoBySubject[key] = repoID
				sutsBySubject[key] = sutIDs
			}
		}
	}
	candidates := merged.candidates()
	views := make([]ImpactCandidateView, 0, len(candidates))
	sorted := sortCandidates(candidates)
	for _, candidate := range sorted {
		key := candidateSubjectKey(candidate.SubjectKind, candidate.SubjectID)
		views = append(views, ImpactCandidateView{
			ImpactCandidate: candidate, RepoID: repoBySubject[key], VersionSUTIDs: sutsBySubject[key],
		})
	}
	return views, nil
}

// ImpactWorkHandler composes the code.analyze handler with impact computation:
// after the T1 analysis succeeds the deterministic+AI candidates are persisted.
type ImpactWorkHandler struct {
	Analysis Handler
	Impact   *ImpactService
}

// Handle runs the wrapped analysis handler, then computes the impact candidates.
func (h ImpactWorkHandler) Handle(ctx context.Context, item WorkItem) error {
	if h.Analysis != nil {
		if err := h.Analysis.Handle(ctx, item); err != nil {
			return err
		}
	}
	if h.Impact == nil {
		return nil
	}
	analysisID, err := decodeWorkItemID(item.Payload, "analysis_id")
	if err != nil {
		return err
	}
	_, err = h.Impact.Compute(ctx, analysisID)
	return err
}

// --- candidate merge --------------------------------------------------------------

type candidateMerge struct {
	entries map[string]*candidateEntry
}

type candidateEntry struct {
	kind       codeintel.SubjectKind
	id         int64
	source     codeintel.CandidateSource
	confidence codeintel.Confidence
	reasons    []string
	evidence   []map[string]any
}

func newCandidateMerge() *candidateMerge {
	return &candidateMerge{entries: map[string]*candidateEntry{}}
}

func candidateSubjectKey(kind codeintel.SubjectKind, id int64) string {
	return string(kind) + ":" + platformid.Format(id)
}

func (m *candidateMerge) add(kind codeintel.SubjectKind, id int64, source codeintel.CandidateSource, confidence codeintel.Confidence, reason string, evidence []map[string]any) {
	if !kind.Valid() || id <= 0 {
		return
	}
	if !confidence.Valid() {
		confidence = codeintel.ConfidenceMedium
	}
	key := candidateSubjectKey(kind, id)
	entry, ok := m.entries[key]
	if !ok {
		entry = &candidateEntry{kind: kind, id: id, source: source, confidence: confidence}
		m.entries[key] = entry
	} else {
		if candidateSourceRank(source) < candidateSourceRank(entry.source) {
			entry.source = source
		}
		if confidenceRank(confidence) > confidenceRank(entry.confidence) {
			entry.confidence = confidence
		}
	}
	reason = strings.TrimSpace(reason)
	if reason != "" && len([]rune(reason)) <= codeintel.MaxReasonChars {
		duplicate := false
		for _, existing := range entry.reasons {
			if existing == reason {
				duplicate = true
				break
			}
		}
		if !duplicate {
			entry.reasons = append(entry.reasons, reason)
		}
	}
	for _, item := range evidence {
		if len(entry.evidence) >= codeintel.MaxEvidencePerFact {
			break
		}
		entry.evidence = append(entry.evidence, item)
	}
}

func (m *candidateMerge) candidates() []codeintel.ImpactCandidate {
	result := make([]codeintel.ImpactCandidate, 0, len(m.entries))
	for _, entry := range m.entries {
		reason := strings.Join(entry.reasons, "; ")
		if len([]rune(reason)) > codeintel.MaxReasonChars {
			reason = string([]rune(reason)[:codeintel.MaxReasonChars])
		}
		evidence := entry.evidence
		if evidence == nil {
			evidence = []map[string]any{}
		}
		result = append(result, codeintel.ImpactCandidate{
			SubjectKind: entry.kind,
			SubjectID:   entry.id,
			Reason:      reason,
			Confidence:  entry.confidence,
			Source:      entry.source,
			Evidence:    mustJSON(evidence),
			Selected:    entry.confidence != codeintel.ConfidenceLow,
		})
	}
	return sortCandidates(result)
}

func (m *candidateMerge) requirementIDs() map[int64]bool {
	result := map[int64]bool{}
	for _, entry := range m.entries {
		if entry.kind == codeintel.SubjectRequirement {
			result[entry.id] = true
		}
	}
	return result
}

// sortCandidates applies source priority, confidence, subject kind and id.
func sortCandidates(candidates []codeintel.ImpactCandidate) []codeintel.ImpactCandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if candidateSourceRank(left.Source) != candidateSourceRank(right.Source) {
			return candidateSourceRank(left.Source) < candidateSourceRank(right.Source)
		}
		if confidenceRank(left.Confidence) != confidenceRank(right.Confidence) {
			return confidenceRank(left.Confidence) > confidenceRank(right.Confidence)
		}
		if subjectKindRank(left.SubjectKind) != subjectKindRank(right.SubjectKind) {
			return subjectKindRank(left.SubjectKind) < subjectKindRank(right.SubjectKind)
		}
		return left.SubjectID < right.SubjectID
	})
	return candidates
}

func candidateSourceRank(source codeintel.CandidateSource) int {
	switch source {
	case codeintel.CandidateDirect:
		return 1
	case codeintel.CandidateContract:
		return 2
	case codeintel.CandidateRelation:
		return 3
	default:
		return 4
	}
}

func confidenceRank(confidence codeintel.Confidence) int {
	switch confidence {
	case codeintel.ConfidenceHigh:
		return 3
	case codeintel.ConfidenceMedium:
		return 2
	default:
		return 1
	}
}

func subjectKindRank(kind codeintel.SubjectKind) int {
	switch kind {
	case codeintel.SubjectRequirement:
		return 1
	case codeintel.SubjectRequirementVersion:
		return 2
	case codeintel.SubjectCaseVersion:
		return 3
	case codeintel.SubjectSUTPage:
		return 4
	default:
		return 5
	}
}

// --- deterministic inputs ---------------------------------------------------------

type pageIndex struct {
	byRoute map[string]PageRef
	byID    map[int64]PageRef
}

type apiIndex struct {
	byRoute map[string]APIRef
	byID    map[int64]APIRef
}

func (s *ImpactService) sutResources(ctx context.Context, versionSUTIDs []int64) (pageIndex, apiIndex, error) {
	pages := pageIndex{byRoute: map[string]PageRef{}, byID: map[int64]PageRef{}}
	apis := apiIndex{byRoute: map[string]APIRef{}, byID: map[int64]APIRef{}}
	if s.resources == nil {
		return pages, apis, nil
	}
	for _, versionSUTID := range versionSUTIDs {
		refs, err := s.resources.SUTPages(ctx, versionSUTID)
		if err != nil {
			return pages, apis, err
		}
		for _, page := range refs {
			if page.Status == "rejected" {
				continue
			}
			pages.byRoute[page.RouteTemplate] = page
			pages.byID[page.ID] = page
		}
		apiRefs, err := s.resources.SUTAPIs(ctx, versionSUTID)
		if err != nil {
			return pages, apis, err
		}
		for _, api := range apiRefs {
			if api.Status == "rejected" {
				continue
			}
			apis.byRoute[apiRouteKey(api.Method, api.PathTemplate)] = api
			apis.byID[api.ID] = api
		}
	}
	return pages, apis, nil
}

func (s *ImpactService) designInputs(ctx context.Context, versionID int64) ([]RequirementRef, []CaseVersionRef, []RequirementRelationRef, error) {
	if s.design == nil {
		return nil, nil, nil, nil
	}
	requirements, err := s.design.Requirements(ctx, versionID)
	if err != nil {
		return nil, nil, nil, err
	}
	caseVersions, err := s.design.CaseVersions(ctx, versionID)
	if err != nil {
		return nil, nil, nil, err
	}
	relations, err := s.design.RequirementRelations(ctx, versionID)
	if err != nil {
		return nil, nil, nil, err
	}
	return requirements, caseVersions, relations, nil
}

// analysisFacts returns the facts recorded in the analysis snapshot (falling
// back to all head facts) and the analyzed fact-key set.
func (s *ImpactService) analysisFacts(ctx context.Context, analysis codeintel.Analysis) ([]codeintel.Fact, map[string]bool, error) {
	facts, err := s.facts.LatestByCommit(ctx, analysis.RepoID, analysis.HeadCommitSHA, nil)
	if err != nil {
		return nil, nil, err
	}
	snapshot := map[string]int{}
	if len(analysis.FactVersions) > 0 {
		_ = json.Unmarshal(analysis.FactVersions, &snapshot)
	}
	analyzedKeys := map[string]bool{}
	for key := range snapshot {
		analyzedKeys[key] = true
	}
	result := make([]codeintel.Fact, 0, len(facts))
	for _, fact := range facts {
		if len(snapshot) > 0 {
			if _, ok := snapshot[fact.FactKey]; !ok {
				continue
			}
		}
		analyzedKeys[fact.FactKey] = true
		result = append(result, fact)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].FactKey < result[j].FactKey })
	return result, analyzedKeys, nil
}

func withoutIncorrectFacts(facts []codeintel.Fact, incorrect map[string]bool) []codeintel.Fact {
	result := make([]codeintel.Fact, 0, len(facts))
	for _, fact := range facts {
		if !incorrect[fact.FactKey] {
			result = append(result, fact)
		}
	}
	return result
}

// --- AI validation ----------------------------------------------------------------

// validateAIDraft returns the reason when the draft subject exists in the
// version, or "" when the draft must be dropped.
func (s *ImpactService) validateAIDraft(ctx context.Context, analysis codeintel.Analysis, repository RepoRef, draft ImpactDraft, requirements []RequirementRef, caseVersions []CaseVersionRef, pages pageIndex, apis apiIndex) string {
	kind := codeintel.SubjectKind(strings.TrimSpace(string(draft.SubjectKind)))
	if !kind.Valid() || draft.SubjectID <= 0 || strings.TrimSpace(draft.Reason) == "" {
		return ""
	}
	switch kind {
	case codeintel.SubjectRequirement:
		for _, requirement := range requirements {
			if requirement.ID == draft.SubjectID {
				return truncateString(strings.TrimSpace(draft.Reason), codeintel.MaxReasonChars)
			}
		}
		return ""
	case codeintel.SubjectCaseVersion:
		for _, caseVersion := range caseVersions {
			if caseVersion.ID == draft.SubjectID {
				return truncateString(strings.TrimSpace(draft.Reason), codeintel.MaxReasonChars)
			}
		}
		return ""
	case codeintel.SubjectSUTPage:
		if _, ok := pages.byID[draft.SubjectID]; ok {
			return truncateString(strings.TrimSpace(draft.Reason), codeintel.MaxReasonChars)
		}
		return ""
	case codeintel.SubjectSUTAPI:
		if _, ok := apis.byID[draft.SubjectID]; ok {
			return truncateString(strings.TrimSpace(draft.Reason), codeintel.MaxReasonChars)
		}
		return ""
	case codeintel.SubjectRequirementVersion:
		if s.subjects == nil {
			return ""
		}
		ok, err := s.subjects.RequirementVersionInVersion(ctx, analysis.VersionID, draft.SubjectID)
		if err != nil || !ok {
			return ""
		}
		return truncateString(strings.TrimSpace(draft.Reason), codeintel.MaxReasonChars)
	default:
		return ""
	}
}

// validateImpactEvidence re-reads the head content and recomputes excerpt
// hashes. Invalid evidence is dropped; the caller downgrades confidence when the
// model supplied evidence but none survived.
func (s *ImpactService) validateImpactEvidence(ctx context.Context, repository RepoRef, commit string, drafts []EvidenceDraft) []map[string]any {
	if s.bulk == nil || len(drafts) == 0 {
		return nil
	}
	maxBytes := readIntSetting(ctx, s.settings, SettingMaxFileBytes, defaultMaxFileBytes, 1<<20)
	maxExcerpt := readIntSetting(ctx, s.settings, SettingMaxExcerptLines, defaultMaxExcerptLines, codeintel.MaxExcerptLines)
	result := make([]map[string]any, 0, len(drafts))
	for _, draft := range drafts {
		if len(result) >= codeintel.MaxEvidencePerFact {
			break
		}
		path, err := codeintel.NormalizeRepoPath(draft.File)
		if err != nil || draft.StartLine < 1 || draft.EndLine < draft.StartLine ||
			draft.EndLine-draft.StartLine+1 > maxExcerpt {
			continue
		}
		window, readErr := s.bulk.ReadFileWindow(ctx, repository.Path, commit, path, maxBytes, 0)
		if readErr != nil || draft.EndLine > window.TotalLines {
			continue
		}
		offset := draft.StartLine - window.StartLine
		if window.StartLine < 1 || offset < 0 || draft.EndLine > len(window.Lines) {
			continue
		}
		result = append(result, map[string]any{
			"file":         path,
			"start_line":   draft.StartLine,
			"end_line":     draft.EndLine,
			"excerpt_hash": codeintel.ExcerptHash(window.Lines[offset : offset+draft.EndLine-draft.StartLine+1]),
		})
	}
	return result
}

// --- unmapped ---------------------------------------------------------------------

func (s *ImpactService) mergeUnmapped(analysis codeintel.Analysis, changedPaths map[string]bool, facts []codeintel.Fact, anchorPaths map[string]bool, aiAvailable bool) []map[string]any {
	unmapped := make([]map[string]any, 0)
	seen := map[string]bool{}
	appendEntry := func(path, reason string) {
		key := path + "\x00" + reason
		if seen[key] || len(unmapped) >= codeintel.MaxUnmappedEntries {
			return
		}
		seen[key] = true
		unmapped = append(unmapped, map[string]any{"path": path, "reason": reason})
	}
	if len(analysis.Unmapped) > 0 {
		var existing []map[string]any
		if err := json.Unmarshal(analysis.Unmapped, &existing); err == nil {
			for _, entry := range existing {
				path, _ := entry["path"].(string)
				reason, _ := entry["reason"].(string)
				if reason != "" {
					appendEntry(path, reason)
				}
			}
		}
	}
	factPaths := map[string]bool{}
	for _, fact := range facts {
		factPaths[fact.Path] = true
	}
	for path := range changedPaths {
		if !factPaths[path] && !anchorPaths[path] {
			appendEntry(path, "no_fact_or_anchor")
		}
	}
	if !aiAvailable {
		appendEntry("", "ai_unavailable")
	}
	return unmapped
}

// --- small helpers ----------------------------------------------------------------

func versionSUTIDsForRepo(bindings []VersionSUTBinding, repoID int64) []int64 {
	ids := make([]int64, 0)
	seen := map[int64]bool{}
	for _, binding := range bindings {
		if binding.RepoID != repoID || seen[binding.VersionSUTID] {
			continue
		}
		seen[binding.VersionSUTID] = true
		ids = append(ids, binding.VersionSUTID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func distinctRepoIDs(bindings []VersionSUTBinding) []int64 {
	ids := make([]int64, 0)
	seen := map[int64]bool{}
	for _, binding := range bindings {
		if binding.RepoID <= 0 || seen[binding.RepoID] {
			continue
		}
		seen[binding.RepoID] = true
		ids = append(ids, binding.RepoID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func impactAnchorSubjectKind(kind codeintel.AnchorSubject) (codeintel.SubjectKind, bool) {
	switch kind {
	case codeintel.AnchorSubjectRequirement:
		return codeintel.SubjectRequirement, true
	case codeintel.AnchorSubjectRequirementVersion:
		return codeintel.SubjectRequirementVersion, true
	case codeintel.AnchorSubjectCaseVersion:
		return codeintel.SubjectCaseVersion, true
	default:
		return "", false
	}
}

func relationKindValid(kind string) bool {
	switch kind {
	case "depends_on", "conflicts_with", "overlaps", "shares_scope":
		return true
	default:
		return false
	}
}

func limitRequirements(requirements []RequirementRef, limit int) []RequirementRef {
	if len(requirements) > limit {
		return requirements[:limit]
	}
	return requirements
}

func factEvidence(fact codeintel.Fact) []map[string]any {
	if len(fact.Evidence) == 0 {
		return nil
	}
	var evidence []map[string]any
	if err := json.Unmarshal(fact.Evidence, &evidence); err != nil {
		return nil
	}
	return evidence
}

func decodeEvidence(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var evidence []map[string]any
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return nil
	}
	return evidence
}

// impactFactRoute returns the normalized route of a route/page fact.
func impactFactRoute(fact codeintel.Fact) (string, bool) {
	switch fact.Kind {
	case codeintel.EntityRoute, codeintel.EntityPage:
	default:
		return "", false
	}
	route := ""
	if strings.HasPrefix(fact.FactKey, "route:") {
		route = strings.TrimPrefix(fact.FactKey, "route:")
	} else if strings.HasPrefix(fact.FactKey, "page:") {
		route = strings.TrimPrefix(fact.FactKey, "page:")
	} else {
		route = strings.TrimSpace(fact.Name)
	}
	if route == "" {
		return "", false
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	return route, true
}

// impactFactAPI returns the normalized method/path of an api/client_call fact.
func impactFactAPI(fact codeintel.Fact) (string, string, bool) {
	switch fact.Kind {
	case codeintel.EntityAPI, codeintel.EntityClientCall:
	default:
		return "", "", false
	}
	return parseImpactFactKey(fact.FactKey)
}

func parseImpactFactKey(key string) (string, string, bool) {
	key = strings.TrimSpace(key)
	if colon := strings.IndexByte(key, ':'); colon >= 0 {
		key = key[colon+1:]
	}
	fields := strings.Fields(key)
	if len(fields) != 2 {
		return "", "", false
	}
	method := strings.ToUpper(fields[0])
	path := codeintel.NormalizeAPIPath(fields[1])
	if method == "" || path == "" {
		return "", "", false
	}
	return method, path, true
}

func apiRouteKey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + normalizeAPITemplate(path)
}

// normalizeAPITemplate keeps declared {param} placeholders while folding
// concrete ID segments, so an API template and a normalized fact path key meet.
func normalizeAPITemplate(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return codeintel.NormalizeAPIPath(path)
}

// decodeChangedFilesJSON parses the persisted changed_files array (snake_case)
// into the domain shape and a path lookup.
func decodeChangedFilesJSON(raw json.RawMessage) (map[string]bool, []codeintel.ChangedFile) {
	paths := map[string]bool{}
	if len(raw) == 0 {
		return paths, nil
	}
	var rows []struct {
		Path         string  `json:"path"`
		OldPath      *string `json:"old_path"`
		Status       string  `json:"status"`
		Additions    int     `json:"additions"`
		Deletions    int     `json:"deletions"`
		Binary       bool    `json:"binary"`
		HeadBlobHash *string `json:"head_blob_hash"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return paths, nil
	}
	files := make([]codeintel.ChangedFile, 0, len(rows))
	for _, row := range rows {
		if row.Path == "" {
			continue
		}
		paths[row.Path] = true
		file := codeintel.ChangedFile{
			Path: row.Path, Status: row.Status, Additions: row.Additions,
			Deletions: row.Deletions, Binary: row.Binary, HeadBlobSHA: row.HeadBlobHash,
		}
		if row.OldPath != nil {
			file.OldPath = *row.OldPath
		}
		files = append(files, file)
	}
	return paths, files
}

func impactEdgesToDomain(edges []ImpactEdge) []codeintel.Edge {
	result := make([]codeintel.Edge, 0, len(edges))
	for _, edge := range edges {
		result = append(result, codeintel.Edge{
			Rel:        edge.Rel,
			Confidence: edge.Confidence,
			FactKey:    truncateString(edge.SrcFactKey+"->"+edge.DstFactKey, codeintel.MaxFactKeyChars),
		})
	}
	return result
}
