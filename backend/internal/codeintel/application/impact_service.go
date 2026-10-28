package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Impact-service bounds and stable values. The defaults follow design spec
// §4.2/§6.6 (the impact prompt must stay bounded: R15) and configuration may
// only lower the hard caps.
const (
	// defaultImpactMaxRequirements mirrors domain.MaxRequirementsInPrompt
	// (design spec §6.6: the AI requirement list is capped at 100).
	defaultImpactMaxRequirements = domain.MaxRequirementsInPrompt
	// defaultImpactMaxFacts is R15's fact-count budget for one impact prompt.
	defaultImpactMaxFacts = 200
	// defaultImpactMaxPromptBytes is R15's total prompt byte budget (64 KiB).
	defaultImpactMaxPromptBytes = 64 << 10
	// minImpactMaxPromptBytes keeps a configured prompt budget usable.
	minImpactMaxPromptBytes = 4 << 10
	// maxImpactMaxPromptBytes is R15's documented hard upper bound (64 KiB);
	// configuration may only lower it.
	maxImpactMaxPromptBytes = defaultImpactMaxPromptBytes

	// maxImpactCandidateEvidence is §6.6 step 4's merged evidence cap.
	maxImpactCandidateEvidence = 5
	// impactReasonSeparator joins the reasons of one merged candidate.
	impactReasonSeparator = "；"
	// impactPromptSchemaMarker is the trailing section BuildImpactPrompt emits;
	// truncation markers are inserted in front of it.
	impactPromptSchemaMarker = "\n\n输出 JSON schema：\n"
	// impactSummaryTruncatedMarker marks the truncated changeSummary section of
	// the impact prompt (R15).
	impactSummaryTruncatedMarker = "\n……（变更摘要已截断）"
)

// Impact-result unmapped suggestions (the deleted-file suggestion is verbatim
// from design spec §6.6 step 7).
const (
	impactSuggestionDeleted     = "人工确认删除影响"
	impactSuggestionUnsupported = "人工确认内容是否可分析"
	impactSuggestionNoFact      = "人工确认是否影响需求"
	impactSuggestionAI          = "AI 不可用，请人工确认"
	impactSuggestionContract    = "人工确认跨栈契约"
)

// AnchorRepository and EdgeRepository alias the domain repository ports so the
// shared ImpactServiceDeps/AnchorServiceDeps field types read exactly like the
// task interfaces while the implementations stay in codeintel/domain.
type (
	AnchorRepository = domain.AnchorRepository
	EdgeRepository   = domain.EdgeRepository
)

// ImpactServiceDeps are the ports of the impact-analysis planner. AppModels and
// Versions are owned by this service (the analysis service has no app model
// catalog, R27); Repos resolves the checkout path used to verify the AI
// candidate evidence against commit content (R14). A nil optional port degrades
// gracefully: the deterministic candidates are still produced.
type ImpactServiceDeps struct {
	LLM       LLMCompleter
	Versions  VersionCatalog
	AppModels AppModelCatalog
	Anchors   AnchorRepository
	Edges     EdgeRepository
	Git       GitReader
	Repos     RepoCatalog
	IDs       IDGenerator

	MaxRequirements int
	MaxFacts        int
	MaxPromptBytes  int
}

// ImpactService implements the deterministic + AI impact pipeline of design
// spec §6.6 as the T10 ImpactPlanner. It never writes storage: the analysis
// service persists the returned candidates, unmapped entries and stats.
type ImpactService struct {
	deps   ImpactServiceDeps
	config impactConfig
}

// impactConfig is the normalised prompt/reasoning budget of one planning call.
type impactConfig struct {
	maxRequirements int
	maxFacts        int
	maxPromptBytes  int
}

// NewImpactService wires the impact planner and normalises its bounds.
func NewImpactService(deps ImpactServiceDeps) *ImpactService {
	return &ImpactService{deps: deps, config: normalizeImpactConfig(deps)}
}

// normalizeImpactConfig applies the documented defaults and hard caps.
func normalizeImpactConfig(deps ImpactServiceDeps) impactConfig {
	cfg := impactConfig{
		maxRequirements: deps.MaxRequirements,
		maxFacts:        deps.MaxFacts,
		maxPromptBytes:  deps.MaxPromptBytes,
	}
	if cfg.maxRequirements <= 0 {
		cfg.maxRequirements = defaultImpactMaxRequirements
	}
	if cfg.maxRequirements > domain.MaxRequirementsInPrompt {
		cfg.maxRequirements = domain.MaxRequirementsInPrompt
	}
	if cfg.maxFacts <= 0 {
		cfg.maxFacts = defaultImpactMaxFacts
	}
	if cfg.maxFacts > defaultImpactMaxFacts {
		cfg.maxFacts = defaultImpactMaxFacts
	}
	if cfg.maxPromptBytes <= 0 {
		cfg.maxPromptBytes = defaultImpactMaxPromptBytes
	}
	if cfg.maxPromptBytes < minImpactMaxPromptBytes {
		cfg.maxPromptBytes = minImpactMaxPromptBytes
	}
	if cfg.maxPromptBytes > maxImpactMaxPromptBytes {
		cfg.maxPromptBytes = maxImpactMaxPromptBytes
	}
	return cfg
}

// impactPlanState is the immutable planning input of one Plan call plus the
// derived deterministic views and mutable counters.
type impactPlanState struct {
	in ImpactInput

	changed      map[string]domain.ChangedFile
	changedPaths []string

	anchors       []domain.CodeAnchor
	appModels     []AppModelNode
	edges         []domain.CodeEdge
	facts         []domain.CodeEntity
	coverageFacts []domain.CodeEntity
	changedFacts  []domain.CodeEntity
	requirements  []RequirementInfo
	relations     []RelationInfo

	stats impactStats

	aiUnavailable          bool
	unmatchedContractPaths []string
	localPathResolved      bool
	localPath              string
	fileContent            map[string]impactCommitFile
}

// impactCommitFile is one commit-pinned file read used to validate AI evidence.
type impactCommitFile struct {
	content   []byte
	ok        bool
	truncated bool
}

// impactStats is the planner stats document merged into code_analysis.stats by
// the analysis service. It carries counters only (never code content, reasons or
// paths), so events/UI can explain the run without leaking source.
type impactStats struct {
	Candidates         int  `json:"candidates"`
	Requirements       int  `json:"requirements"`
	AppModels          int  `json:"appModels"`
	Direct             int  `json:"direct"`
	Contract           int  `json:"contract"`
	Relation           int  `json:"relation"`
	AI                 int  `json:"ai"`
	AIEvidenceChecks   int  `json:"aiEvidenceChecks"`
	AIEvidenceFailures int  `json:"aiEvidenceFailures"`
	UnmatchedContracts int  `json:"unmatchedContracts"`
	Unmapped           int  `json:"unmapped"`
	UnmappedTruncated  bool `json:"unmappedTruncated,omitempty"`
	InvalidCandidates  int  `json:"invalidCandidates,omitempty"`
	PromptFacts        int  `json:"promptFacts"`
	PromptTruncated    bool `json:"promptTruncated,omitempty"`
	AIUnavailable      bool `json:"aiUnavailable,omitempty"`
	AIErrors           int  `json:"aiErrors,omitempty"`
}

// Plan implements ImpactPlanner: it returns the merged/deduped candidates, the
// unmapped changes and the stats document of one analysis (design spec §6.6).
func (s *ImpactService) Plan(ctx context.Context, in ImpactInput) (ImpactResult, error) {
	state, err := s.prepare(ctx, in)
	if err != nil {
		return ImpactResult{}, err
	}

	candidates := make([]domain.ImpactCandidate, 0)
	directRequirements := make(map[int64]string)
	directEvidence := make(map[int64][]domain.Evidence)
	for _, candidate := range s.anchorCandidates(state) {
		candidates = append(candidates, candidate)
		if candidate.SubjectKind == domain.CandidateRequirement {
			directRequirements[candidate.SubjectID] = strongerImpactConfidence(
				directRequirements[candidate.SubjectID], candidate.Confidence)
			// A relation candidate inherits the triggering direct requirement's
			// changed-file evidence so every automatic candidate stays traceable
			// to a changed file (INV-2).
			directEvidence[candidate.SubjectID] = mergeImpactEvidence(
				directEvidence[candidate.SubjectID], candidate.Evidence)
		}
	}
	candidates = append(candidates, s.appModelCandidates(state)...)
	candidates = append(candidates, s.contractCandidates(state)...)
	candidates = append(candidates, s.relationCandidates(state, directRequirements, directEvidence)...)

	aiCandidates, err := s.aiCandidates(ctx, state)
	if err != nil {
		return ImpactResult{}, err
	}
	candidates = append(candidates, aiCandidates...)

	merged := mergeImpactCandidates(candidates, s.deps.IDs)
	domain.SortCandidates(merged)
	unmapped, unmappedTruncated := s.unmappedChanges(state)
	return ImpactResult{
		Candidates: merged,
		Unmapped:   unmapped,
		Stats:      marshalImpactStats(state, merged, unmapped, unmappedTruncated),
	}, nil
}

// prepare validates/resolves the planning input. Storage failures surface so the
// analysis fails deterministically; an empty optional input is filled from the
// injected catalogs/repositories (R27) when possible.
func (s *ImpactService) prepare(ctx context.Context, in ImpactInput) (*impactPlanState, error) {
	state := &impactPlanState{
		in:          in,
		changed:     make(map[string]domain.ChangedFile, len(in.ChangedFiles)),
		fileContent: make(map[string]impactCommitFile),
	}
	for _, file := range in.ChangedFiles {
		state.changed[file.Path] = file
		state.changedPaths = append(state.changedPaths, file.Path)
	}
	sort.Strings(state.changedPaths)

	versionID := in.Version.ID
	if versionID == 0 {
		versionID = in.Analysis.VersionID
	}

	state.requirements = in.Requirements
	if len(state.requirements) == 0 && versionID > 0 && s.deps.Versions != nil {
		loaded, err := s.deps.Versions.ListRequirements(ctx, versionID)
		if err != nil {
			return nil, err
		}
		state.requirements = loaded
	}
	state.relations = in.Relations
	if len(state.relations) == 0 && versionID > 0 && s.deps.Versions != nil {
		loaded, err := s.deps.Versions.ListRelations(ctx, versionID)
		if err != nil {
			return nil, err
		}
		state.relations = loaded
	}

	state.anchors = append([]domain.CodeAnchor(nil), in.Anchors...)
	if len(state.anchors) == 0 && s.deps.Anchors != nil && versionID > 0 {
		loaded, err := s.deps.Anchors.ListByPaths(ctx, versionID, state.changedPaths)
		if err != nil {
			return nil, err
		}
		state.anchors = loaded
	}
	sort.SliceStable(state.anchors, func(i, j int) bool { return state.anchors[i].ID < state.anchors[j].ID })

	// App model matching requires the version's bound system (design spec
	// §6.6 input 4 / R27); without one, no app model candidate may be created.
	if in.Version.HasSystem && in.Version.SystemID > 0 {
		state.appModels = append([]AppModelNode(nil), in.AppModels...)
		if len(state.appModels) == 0 && s.deps.AppModels != nil {
			loaded, err := s.deps.AppModels.ListBySystem(ctx, in.Version.SystemID, nil)
			if err != nil {
				return nil, err
			}
			state.appModels = loaded
		}
	}
	sort.SliceStable(state.appModels, func(i, j int) bool {
		if state.appModels[i].Kind != state.appModels[j].Kind {
			return state.appModels[i].Kind < state.appModels[j].Kind
		}
		if state.appModels[i].Key != state.appModels[j].Key {
			return state.appModels[i].Key < state.appModels[j].Key
		}
		return state.appModels[i].ID < state.appModels[j].ID
	})

	state.edges = append([]domain.CodeEdge(nil), in.Edges...)
	if len(state.edges) == 0 && s.deps.Edges != nil {
		loaded, err := s.deps.Edges.ListByRel(ctx, in.Analysis.RepoID, in.Analysis.HeadCommit, domain.RelRouteToAPI)
		if err != nil {
			return nil, err
		}
		state.edges = loaded
	}
	sort.SliceStable(state.edges, func(i, j int) bool { return state.edges[i].ID < state.edges[j].ID })

	state.facts = highMediumImpactFacts(in.FactsAtHead)
	state.coverageFacts = impactCoverageFacts(in.FactsAtHead)
	for _, fact := range state.facts {
		if impactFactTouchesPaths(fact, state.changed) {
			state.changedFacts = append(state.changedFacts, fact)
		}
	}
	return state, nil
}

// highMediumImpactFacts keeps the high/medium AI facts (INV-1: low clues and T0
// file entities never produce automatic candidates) in deterministic order.
func highMediumImpactFacts(facts []domain.CodeEntity) []domain.CodeEntity {
	kept := make([]domain.CodeEntity, 0, len(facts))
	for _, fact := range facts {
		if !isImpactFactKind(fact.Kind) {
			continue
		}
		if fact.Confidence != domain.ConfidenceHigh && fact.Confidence != domain.ConfidenceMedium {
			continue
		}
		kept = append(kept, fact)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		left, right := kept[i], kept[j]
		if impactFactKey(left) != impactFactKey(right) {
			return impactFactKey(left) < impactFactKey(right)
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return left.ID < right.ID
	})
	return kept
}

// isImpactFactKind reports whether kind is an AI fact kind (never the T0 file
// entity kind).
func isImpactFactKind(kind string) bool {
	switch kind {
	case domain.KindPage, domain.KindRoute, domain.KindAPI,
		domain.KindClientCall, domain.KindComponent, domain.KindModule, domain.KindSymbol:
		return true
	default:
		return false
	}
}

// impactCoverageFacts keeps the facts that can cover a changed file in the
// unmapped_changes view (design spec §6.6 step 7 / INV-8): AI fact kinds with
// verified evidence regardless of confidence, never the T0 file entities. A low
// clue still proves the file was analysed; only automatic candidate generation
// is restricted to high/medium facts (INV-1).
func impactCoverageFacts(facts []domain.CodeEntity) []domain.CodeEntity {
	kept := make([]domain.CodeEntity, 0, len(facts))
	for _, fact := range facts {
		if !isImpactFactKind(fact.Kind) {
			continue
		}
		if !impactFactHasVerifiedEvidence(fact) {
			continue
		}
		kept = append(kept, fact)
	}
	return kept
}

// impactFactHasVerifiedEvidence reports whether the fact carries at least one
// verified evidence location. Analysis drops invalid evidence before
// materialisation, so a non-empty file entry is the persistence marker of a
// verified excerpt.
func impactFactHasVerifiedEvidence(fact domain.CodeEntity) bool {
	for _, evidence := range fact.Evidence {
		if evidence.File != "" {
			return true
		}
	}
	return false
}

// impactFactKey returns the fact identity or "" when the entity has none.
func impactFactKey(entity domain.CodeEntity) string {
	if entity.FactKey == nil {
		return ""
	}
	return *entity.FactKey
}

// impactFactTouchesPaths reports whether fact was produced for (or its verified
// evidence points at) one of the changed files.
func impactFactTouchesPaths(fact domain.CodeEntity, changed map[string]domain.ChangedFile) bool {
	if _, ok := changed[fact.Path]; ok {
		return true
	}
	for _, evidence := range fact.Evidence {
		if _, ok := changed[evidence.File]; ok {
			return true
		}
	}
	return false
}

// impactAppModelFacts returns the facts allowed to produce app model candidates:
// facts that touch a changed file (their path or verified evidence) plus facts
// this analysis run produced even when the model attributed them to an
// out-of-path file (design spec §6.6 input 3). The head-wide FactsAtHead
// snapshot also carries facts from other analyses/versions; none of those may
// become a direct candidate (review finding 1). state.facts is deterministic, so
// the filtered view is too.
func impactAppModelFacts(state *impactPlanState) []domain.CodeEntity {
	analysisID := state.in.Analysis.ID
	kept := make([]domain.CodeEntity, 0, len(state.changedFacts))
	for _, fact := range state.facts {
		if impactFactTouchesPaths(fact, state.changed) ||
			(analysisID != 0 && fact.AnalysisID != nil && *fact.AnalysisID == analysisID) {
			kept = append(kept, fact)
		}
	}
	return kept
}

// anchorCandidates projects every anchor of a changed file onto a candidate.
// Version anchors are display-only (design spec §6.6 step 3).
func (s *ImpactService) anchorCandidates(state *impactPlanState) []domain.ImpactCandidate {
	candidates := make([]domain.ImpactCandidate, 0)
	for _, anchor := range state.anchors {
		if _, ok := state.changed[anchor.FilePath]; !ok {
			continue
		}
		switch anchor.SubjectKind {
		case domain.CandidateRequirement, domain.CandidateAppModel:
		default:
			continue
		}
		candidates = append(candidates, domain.ImpactCandidate{
			SubjectKind: anchor.SubjectKind,
			SubjectID:   anchor.SubjectID,
			Reason:      "changed file " + anchor.FilePath,
			Confidence:  impactAnchorConfidence(anchor.Confidence),
			Source:      domain.CandidateDirect,
			Evidence:    []domain.Evidence{{File: anchor.FilePath}},
		})
	}
	return candidates
}

// impactAnchorConfidence defaults an anchor confidence to high, matching the
// repository's DDL default.
func impactAnchorConfidence(confidence string) string {
	if confidence == "" {
		return domain.ConfidenceHigh
	}
	return confidence
}

// appModelCandidates matches the facts of this change against the version-bound
// system's app model nodes by exact key (design spec §6.6 step 3). Only facts
// that touch the change set (or were produced by this run) are considered, so
// the head-wide fact snapshot cannot turn an unrelated fact into a candidate.
func (s *ImpactService) appModelCandidates(state *impactPlanState) []domain.ImpactCandidate {
	byKey := impactAppModelNodeIndex(state.appModels)
	candidates := make([]domain.ImpactCandidate, 0)
	for _, fact := range impactAppModelFacts(state) {
		key, ok := impactAppModelKeyForFact(fact)
		if !ok {
			continue
		}
		node, ok := byKey[fact.Kind+"\x00"+key]
		if !ok {
			continue
		}
		candidates = append(candidates, domain.ImpactCandidate{
			SubjectKind: domain.CandidateAppModel,
			SubjectID:   node.ID,
			Reason:      fmt.Sprintf("changed %s %s", fact.Kind, fact.Name),
			Confidence:  fact.Confidence,
			Source:      domain.CandidateDirect,
			Evidence:    cloneImpactEvidence(fact.Evidence),
		})
	}
	return candidates
}

// impactAppModelNodeIndex indexes the app model nodes by kind and key. The first
// node wins so a duplicate key stays deterministic (state.appModels is sorted by
// kind, key and id).
func impactAppModelNodeIndex(appModels []AppModelNode) map[string]AppModelNode {
	index := make(map[string]AppModelNode, len(appModels))
	for _, node := range appModels {
		switch node.Kind {
		case domain.KindPage, domain.KindRoute, domain.KindAPI, domain.KindComponent:
		default:
			continue
		}
		key := node.Kind + "\x00" + node.Key
		if _, exists := index[key]; !exists {
			index[key] = node
		}
	}
	return index
}

// impactAppModelKeyForFact returns the P2 app model key of one fact
// (page:<path>, route:<path>, api:<METHOD> <path>, component:<Name>).
func impactAppModelKeyForFact(fact domain.CodeEntity) (string, bool) {
	name := strings.TrimSpace(fact.Name)
	if name == "" {
		return "", false
	}
	switch fact.Kind {
	case domain.KindPage:
		return "page:" + impactLeadingSlash(name), true
	case domain.KindRoute:
		return "route:" + impactLeadingSlash(name), true
	case domain.KindAPI:
		method, path := domain.NormalizeMethodPath("", name)
		if method == "" || path == "" {
			return "", false
		}
		return "api:" + method + " " + path, true
	case domain.KindComponent:
		return "component:" + name, true
	default:
		return "", false
	}
}

// impactLeadingSlash gives a UI/route path its leading slash so a fact name
// without one still matches the P2 app model key convention.
func impactLeadingSlash(value string) string {
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

// contractCandidates propagates changed client_call/api facts one hop over the
// route_to_api edges (design spec §6.5/§6.6, CI-M0):
//
//   - a changed client_call reaches the counterpart api node (api:<METHOD> <path>);
//   - a changed api reaches the component/page facts recorded at the counterpart
//     client_call's path and matches their app model nodes (R38: the analysis
//     service supplies those companion facts in FactsAtHead).
func (s *ImpactService) contractCandidates(state *impactPlanState) []domain.ImpactCandidate {
	nodes := impactAppModelNodeIndex(state.appModels)
	factsByID := make(map[int64]domain.CodeEntity, len(state.facts))
	for _, fact := range state.facts {
		if _, exists := factsByID[fact.ID]; !exists {
			factsByID[fact.ID] = fact
		}
	}

	unmatchedPaths := make(map[string]struct{})
	candidates := make([]domain.ImpactCandidate, 0)
	for _, fact := range state.changedFacts {
		if fact.Kind != domain.KindClientCall && fact.Kind != domain.KindAPI {
			continue
		}
		touched := false
		for _, edge := range state.edges {
			if edge.Rel != domain.RelRouteToAPI {
				continue
			}
			if edge.SrcEntityID != fact.ID && edge.DstEntityID != fact.ID {
				continue
			}
			touched = true
			method, path, ok := impactContractEndpoint(edge, fact, state.facts)
			if !ok {
				continue
			}
			if fact.Kind == domain.KindAPI {
				// Changed api → counterpart client_call → its component/page
				// facts → their app model nodes (design spec §6.5 传播).
				counterpartID := edge.SrcEntityID
				if counterpartID == fact.ID {
					counterpartID = edge.DstEntityID
				}
				counterpart, ok := factsByID[counterpartID]
				if !ok || counterpart.Kind != domain.KindClientCall {
					continue
				}
				for _, companion := range impactCompanionFacts(state.facts, counterpart.Path) {
					key, ok := impactAppModelKeyForFact(companion)
					if !ok {
						continue
					}
					node, ok := nodes[companion.Kind+"\x00"+key]
					if !ok {
						continue
					}
					candidates = append(candidates, domain.ImpactCandidate{
						SubjectKind: domain.CandidateAppModel,
						SubjectID:   node.ID,
						Reason:      fmt.Sprintf("contract %s %s changed", method, path),
						Confidence: weakerConfidence(
							weakerConfidence(fact.Confidence, impactEdgeConfidence(edge.Confidence)),
							companion.Confidence),
						Source:   domain.CandidateContract,
						Evidence: mergeImpactEvidence(fact.Evidence, edge.Evidence, companion.Evidence),
					})
				}
				continue
			}
			node, ok := nodes[domain.KindAPI+"\x00"+"api:"+method+" "+path]
			if !ok {
				continue
			}
			candidates = append(candidates, domain.ImpactCandidate{
				SubjectKind: domain.CandidateAppModel,
				SubjectID:   node.ID,
				Reason:      fmt.Sprintf("contract %s %s changed", method, path),
				Confidence:  weakerConfidence(fact.Confidence, impactEdgeConfidence(edge.Confidence)),
				Source:      domain.CandidateContract,
				Evidence:    mergeImpactEvidence(edge.Evidence, fact.Evidence),
			})
		}
		if !touched {
			state.stats.UnmatchedContracts++
			if path := impactFactChangedPath(fact, state.changed); path != "" {
				unmatchedPaths[path] = struct{}{}
			}
		}
	}
	for path := range unmatchedPaths {
		state.unmatchedContractPaths = append(state.unmatchedContractPaths, path)
	}
	sort.Strings(state.unmatchedContractPaths)
	return candidates
}

// impactCompanionFacts returns the high/medium component/page facts recorded at
// one contract-counterpart path (R38) in deterministic fact order.
func impactCompanionFacts(facts []domain.CodeEntity, path string) []domain.CodeEntity {
	if path == "" {
		return nil
	}
	companions := make([]domain.CodeEntity, 0)
	for _, fact := range facts {
		if fact.Path != path {
			continue
		}
		if fact.Kind != domain.KindComponent && fact.Kind != domain.KindPage {
			continue
		}
		companions = append(companions, fact)
	}
	return companions
}

// impactContractEndpoint resolves the api side of one route_to_api edge from its
// meta document, falling back to the triggering fact name and then to the
// endpoint fact name when the meta is absent/corrupt.
func impactContractEndpoint(edge domain.CodeEdge, trigger domain.CodeEntity, facts []domain.CodeEntity) (string, string, bool) {
	if len(edge.Meta) > 0 {
		var meta struct {
			Method         string `json:"method"`
			NormalizedPath string `json:"normalizedPath"`
		}
		if err := json.Unmarshal(edge.Meta, &meta); err == nil &&
			strings.TrimSpace(meta.Method) != "" && strings.TrimSpace(meta.NormalizedPath) != "" {
			return strings.ToUpper(strings.TrimSpace(meta.Method)), strings.TrimSpace(meta.NormalizedPath), true
		}
	}
	if method, path := domain.NormalizeMethodPath("", trigger.Name); method != "" && path != "" {
		return method, path, true
	}
	for _, fact := range facts {
		if fact.ID != edge.SrcEntityID && fact.ID != edge.DstEntityID {
			continue
		}
		method, path := domain.NormalizeMethodPath("", fact.Name)
		if method != "" && path != "" {
			return method, path, true
		}
	}
	return "", "", false
}

// impactFactChangedPath returns the changed file a contract fact belongs to.
func impactFactChangedPath(fact domain.CodeEntity, changed map[string]domain.ChangedFile) string {
	if _, ok := changed[fact.Path]; ok {
		return fact.Path
	}
	for _, evidence := range fact.Evidence {
		if _, ok := changed[evidence.File]; ok {
			return evidence.File
		}
	}
	return ""
}

// impactEdgeConfidence defaults a contract edge confidence to medium.
func impactEdgeConfidence(confidence string) string {
	if confidence == "" {
		return domain.ConfidenceMedium
	}
	return confidence
}

// relationCandidates expands the directly hit requirements one hop through the
// requirement relations in any direction (design spec §6.6 step 3). It never
// recurses and never adds an already directly hit requirement. The relation
// candidate carries the triggering direct candidate's evidence so it stays
// traceable to the changed file (INV-2).
func (s *ImpactService) relationCandidates(state *impactPlanState, direct map[int64]string, directEvidence map[int64][]domain.Evidence) []domain.ImpactCandidate {
	type relationHit struct {
		relatedID int64
		triggerID int64
		relation  string
	}
	relations := append([]RelationInfo(nil), state.relations...)
	sort.SliceStable(relations, func(i, j int) bool {
		if relations[i].FromID != relations[j].FromID {
			return relations[i].FromID < relations[j].FromID
		}
		if relations[i].ToID != relations[j].ToID {
			return relations[i].ToID < relations[j].ToID
		}
		return relations[i].Type < relations[j].Type
	})

	hits := make([]relationHit, 0)
	for _, relation := range relations {
		_, fromDirect := direct[relation.FromID]
		_, toDirect := direct[relation.ToID]
		switch {
		case fromDirect && !toDirect:
			hits = append(hits, relationHit{
				relatedID: relation.ToID, triggerID: relation.FromID,
				relation: fmt.Sprintf("related to R%d via %s", relation.FromID, relation.Type),
			})
		case toDirect && !fromDirect:
			hits = append(hits, relationHit{
				relatedID: relation.FromID, triggerID: relation.ToID,
				relation: fmt.Sprintf("related to R%d via %s", relation.ToID, relation.Type),
			})
		}
	}

	candidates := make([]domain.ImpactCandidate, 0, len(hits))
	for _, hit := range hits {
		candidates = append(candidates, domain.ImpactCandidate{
			SubjectKind: domain.CandidateRequirement,
			SubjectID:   hit.relatedID,
			Reason:      hit.relation,
			Confidence:  weakerConfidence(domain.ConfidenceMedium, direct[hit.triggerID]),
			Source:      domain.CandidateRelation,
			Evidence:    mergeImpactEvidence(directEvidence[hit.triggerID]),
		})
	}
	return candidates
}

// aiCandidates runs the one impact-reasoning call and validates its output
// (design spec §6.6 step 3, §8.4). Every failure degrades only the AI source: it
// records stats/aiUnavailable and never fails the analysis.
func (s *ImpactService) aiCandidates(ctx context.Context, state *impactPlanState) ([]domain.ImpactCandidate, error) {
	if state.in.Degraded {
		state.aiUnavailable = true
		return nil, nil
	}
	if s.deps.LLM == nil {
		state.aiUnavailable = true
		return nil, nil
	}
	if len(state.requirements) == 0 {
		return nil, nil
	}

	system, user, promptPlan := s.buildImpactPrompt(state)
	state.stats.PromptFacts = len(promptPlan.facts)
	state.stats.PromptTruncated = promptPlan.truncated

	raw, err := s.deps.LLM.Complete(ctx, system, user)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		state.aiUnavailable = true
		state.stats.AIErrors++
		return nil, nil
	}
	parsed, err := parseImpactReasoning(raw)
	if err != nil {
		state.aiUnavailable = true
		state.stats.AIErrors++
		return nil, nil
	}

	requirements := make(map[int64]struct{}, len(state.requirements))
	for _, requirement := range state.requirements {
		requirements[requirement.ID] = struct{}{}
	}

	candidates := make([]domain.ImpactCandidate, 0, len(parsed))
	for _, affected := range parsed {
		if affected.RequirementID == nil || affected.Reason == nil || affected.Confidence == nil {
			state.stats.InvalidCandidates++
			continue
		}
		requirementID, convErr := strconv.ParseInt(strings.TrimSpace(*affected.RequirementID), 10, 64)
		if convErr != nil {
			state.stats.InvalidCandidates++
			continue
		}
		if _, known := requirements[requirementID]; !known {
			state.stats.InvalidCandidates++
			continue
		}
		confidence := strings.TrimSpace(*affected.Confidence)
		if confidence != domain.ConfidenceHigh && confidence != domain.ConfidenceMedium && confidence != domain.ConfidenceLow {
			state.stats.InvalidCandidates++
			continue
		}
		reason := strings.TrimSpace(*affected.Reason)
		if reason == "" {
			state.stats.InvalidCandidates++
			continue
		}

		evidence := make([]domain.Evidence, 0, len(affected.Evidence))
		for index, rawEvidence := range affected.Evidence {
			if index >= domain.MaxEvidencePerFact {
				// The per-fact evidence cap of design spec §6.2 applies to the
				// model output regardless of which prompt produced it.
				break
			}
			state.stats.AIEvidenceChecks++
			if rawEvidence.File == nil || rawEvidence.StartLine == nil || rawEvidence.EndLine == nil {
				state.stats.AIEvidenceFailures++
				continue
			}
			validated, ok := s.validateAIEvidence(ctx, state, *rawEvidence.File, *rawEvidence.StartLine, *rawEvidence.EndLine)
			if !ok {
				state.stats.AIEvidenceFailures++
				continue
			}
			evidence = append(evidence, validated)
		}
		mergedEvidence := mergeImpactEvidence(evidence)
		if len(mergedEvidence) == 0 {
			confidence = domain.ConfidenceLow
		}
		candidates = append(candidates, domain.ImpactCandidate{
			SubjectKind: domain.CandidateRequirement,
			SubjectID:   requirementID,
			Reason:      reason,
			Confidence:  confidence,
			Source:      domain.CandidateAI,
			Evidence:    mergedEvidence,
		})
	}
	if ctx.Err() != nil {
		// A worker deadline during evidence validation must fail the run, not
		// silently degrade the AI source.
		return nil, ctx.Err()
	}
	return candidates, nil
}

// impactReasoningEntry is one affected requirement of the §8.4 output schema.
// Required fields are pointers so a missing value is distinguishable.
type impactReasoningEntry struct {
	RequirementID *string       `json:"requirementId"`
	Reason        *string       `json:"reason"`
	Confidence    *string       `json:"confidence"`
	Evidence      []rawEvidence `json:"evidence"`
}

// parseImpactReasoning strictly parses the `{affected:[...]}` object of the
// impact prompt. A Markdown fence/explanation is tolerated by extracting the
// first balanced object; missing/ill-typed required fields fail the parse.
func parseImpactReasoning(raw string) ([]impactReasoningEntry, error) {
	payload, err := extractJSONObject(raw)
	if err != nil {
		return nil, err
	}
	var document struct {
		Affected *[]impactReasoningEntry `json:"affected"`
	}
	if err := json.Unmarshal([]byte(payload), &document); err != nil {
		return nil, err
	}
	if document.Affected == nil {
		return nil, errors.New("codeintel: impact response is missing affected")
	}
	return *document.Affected, nil
}

// validateAIEvidence verifies one model-supplied evidence interval against the
// head commit content (design spec §6.3/R14: any file of the commit) and
// returns it with the server-recomputed excerpt hash.
func (s *ImpactService) validateAIEvidence(ctx context.Context, state *impactPlanState, file string, startLine, endLine int) (domain.Evidence, bool) {
	if s.deps.Git == nil || state.in.Analysis.HeadCommit == "" {
		return domain.Evidence{}, false
	}
	normalized, err := normalizeRepoRelativePath(file)
	if err != nil {
		return domain.Evidence{}, false
	}
	content, ok := s.commitFileContent(ctx, state, normalized)
	if !ok {
		return domain.Evidence{}, false
	}
	validated, err := ValidateEvidence(content, domain.Evidence{
		File:      normalized,
		StartLine: startLine,
		EndLine:   endLine,
	}, defaultMaxExcerptLines)
	if err != nil {
		return domain.Evidence{}, false
	}
	return validated, true
}

// commitFileContent lazily reads one commit-pinned file for evidence validation.
// The read is bounded by the domain file-read cap; an oversized/binary file is
// treated as unverifiable.
func (s *ImpactService) commitFileContent(ctx context.Context, state *impactPlanState, file string) ([]byte, bool) {
	if cached, ok := state.fileContent[file]; ok {
		return cached.content, cached.ok && !cached.truncated
	}
	entry := impactCommitFile{}
	content, truncated, err := s.deps.Git.ReadAll(ctx, s.repositoryLocalPath(ctx, state),
		state.in.Analysis.HeadCommit, file, domain.MaxFileReadBytes)
	if err != nil {
		entry = impactCommitFile{truncated: truncated}
	} else if truncated {
		entry = impactCommitFile{truncated: true}
	} else {
		entry = impactCommitFile{content: content, ok: true}
	}
	state.fileContent[file] = entry
	return entry.content, entry.ok
}

// repositoryLocalPath resolves the repository checkout once per Plan call so the
// AI evidence validator can read commit content. A missing Repos port leaves the
// path empty; a Git double may still serve content and a real adapter fails
// closed.
func (s *ImpactService) repositoryLocalPath(ctx context.Context, state *impactPlanState) string {
	if state.localPathResolved {
		return state.localPath
	}
	state.localPathResolved = true
	repoID := state.in.Analysis.RepoID
	if repoID == 0 {
		repoID = state.in.Version.RepoID
	}
	if s.deps.Repos == nil || repoID <= 0 {
		return ""
	}
	repo, err := s.deps.Repos.Get(ctx, repoID)
	if err != nil {
		return ""
	}
	state.localPath = repo.LocalPath
	return state.localPath
}

// impactPromptPlan is the selected, bounded prompt content of one AI call.
type impactPromptPlan struct {
	facts     []AIFact
	truncated bool
}

// buildImpactPrompt renders the §8.4 prompt with the R15 caps: the fact count is
// capped by MaxFacts and the whole prompt by MaxPromptBytes (first reached
// wins), and every cut is marked inside the user message. The changeSummary is
// the last section to shrink so the final system+user prompt never exceeds the
// configured byte budget.
func (s *ImpactService) buildImpactPrompt(state *impactPlanState) (string, string, impactPromptPlan) {
	allFacts := impactPromptFacts(state.changedFacts)
	files := append([]domain.ChangedFile(nil), state.in.ChangedFiles...)
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	requirements := append([]RequirementInfo(nil), state.requirements...)
	sort.SliceStable(requirements, func(i, j int) bool { return requirements[i].ID < requirements[j].ID })

	factLimit := len(allFacts)
	if factLimit > s.config.maxFacts {
		factLimit = s.config.maxFacts
	}
	fileLimit := len(files)
	requirementLimit := len(requirements)
	if requirementLimit > s.config.maxRequirements {
		requirementLimit = s.config.maxRequirements
	}

	summary := impactChangeSummary(state.in.Analysis.Stats)
	build := func(summary string, factCount, fileCount, requirementCount int) (string, string, bool) {
		system, user := BuildImpactPrompt(ImpactPromptInput{
			Requirements:  requirements[:requirementCount],
			ChangedFiles:  files[:fileCount],
			Facts:         allFacts[:factCount],
			ChangeSummary: summary,
		})
		truncated := factCount < len(allFacts) || fileCount < len(files) || requirementCount < len(requirements)
		if truncated {
			user = injectImpactPromptMarkers(user, factCount, len(allFacts), fileCount, len(files), requirementCount, len(requirements))
		}
		return system, user, truncated
	}
	fits := func(system, user string) bool {
		return len(system)+len(user) <= s.config.maxPromptBytes
	}

	system, user, truncated := build(summary, factLimit, len(files), requirementLimit)
	if !fits(system, user) {
		// Facts first: they are the largest and least stable section of the
		// prompt; changed files and the requirement list stay complete as long
		// as the byte budget allows.
		factLimit = largestImpactFit(factLimit, func(count int) bool {
			candidateSystem, candidateUser, _ := build(summary, count, len(files), requirementLimit)
			return fits(candidateSystem, candidateUser)
		})
		fileLimit = largestImpactFit(len(files), func(count int) bool {
			candidateSystem, candidateUser, _ := build(summary, factLimit, count, requirementLimit)
			return fits(candidateSystem, candidateUser)
		})
		requirementLimit = largestImpactFit(requirementLimit, func(count int) bool {
			candidateSystem, candidateUser, _ := build(summary, factLimit, fileLimit, count)
			return fits(candidateSystem, candidateUser)
		})
		system, user, truncated = build(summary, factLimit, fileLimit, requirementLimit)
	}
	if !fits(system, user) && summary != "" {
		// The summary is the only unbounded free-text section left. Cut it down
		// (with an explicit marker) until the whole prompt fits the budget.
		summaryRunes := []rune(summary)
		summaryLimit := 0
		if len(summaryRunes) > 1 {
			summaryLimit = largestImpactFit(len(summaryRunes)-1, func(count int) bool {
				candidateSystem, candidateUser, _ := build(
					impactTruncatedChangeSummary(summaryRunes, count), factLimit, fileLimit, requirementLimit)
				return fits(candidateSystem, candidateUser)
			})
		}
		system, user, truncated = build(
			impactTruncatedChangeSummary(summaryRunes, summaryLimit), factLimit, fileLimit, requirementLimit)
		truncated = true
		if !fits(system, user) {
			// The marker itself no longer fits: drop the summary entirely.
			system, user, _ = build("", factLimit, fileLimit, requirementLimit)
		}
	}
	return system, user, impactPromptPlan{
		facts:     allFacts[:factLimit],
		truncated: truncated,
	}
}

// impactTruncatedChangeSummary returns the first count runes of the summary with
// the R15 truncation marker appended.
func impactTruncatedChangeSummary(summary []rune, count int) string {
	if count <= 0 {
		return impactSummaryTruncatedMarker
	}
	return string(summary[:count]) + impactSummaryTruncatedMarker
}

// largestImpactFit returns the largest n in [0, limit] for which fits(n) holds.
// fits is expected to be monotonic (a larger section never shrinks the prompt).
func largestImpactFit(limit int, fits func(int) bool) int {
	if limit <= 0 {
		return 0
	}
	if !fits(0) {
		return 0
	}
	low, high := 0, limit
	for low < high {
		mid := (low + high + 1) / 2
		if fits(mid) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low
}

// injectImpactPromptMarkers inserts the truncation notices in front of the
// output schema so the model knows a section is incomplete.
func injectImpactPromptMarkers(user string, factsShown, factsTotal, filesShown, filesTotal, requirementsShown, requirementsTotal int) string {
	markers := make([]string, 0, 3)
	if factsShown < factsTotal {
		markers = append(markers, fmt.Sprintf("……（事实过多，仅显示前 %d 条，其余已截断）", factsShown))
	}
	if filesShown < filesTotal {
		markers = append(markers, fmt.Sprintf("……（变更文件过多，仅显示前 %d 个，其余已截断）", filesShown))
	}
	if requirementsShown < requirementsTotal {
		markers = append(markers, fmt.Sprintf("……（需求过多，仅显示前 %d 条，其余已截断）", requirementsShown))
	}
	if len(markers) == 0 {
		return user
	}
	return strings.Replace(user, impactPromptSchemaMarker,
		"\n"+strings.Join(markers, "\n")+impactPromptSchemaMarker, 1)
}

// impactPromptFacts projects the changed AI facts onto the prompt fact entries.
// The fact entity has no summary field, so the fact signature is the prompt
// summary.
func impactPromptFacts(facts []domain.CodeEntity) []AIFact {
	promptFacts := make([]AIFact, 0, len(facts))
	for _, fact := range facts {
		promptFacts = append(promptFacts, AIFact{
			Kind:       fact.Kind,
			Name:       fact.Name,
			Path:       fact.Path,
			Signature:  fact.Signature,
			Confidence: fact.Confidence,
			Summary:    fact.Signature,
			Evidence:   cloneImpactEvidence(fact.Evidence),
		})
	}
	return promptFacts
}

// impactChangeSummary reads the aggregated AI change summary persisted by the
// analysis service in stats["changeSummary"] (R28).
func impactChangeSummary(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var stats map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stats); err != nil {
		return ""
	}
	summary, ok := stats["changeSummary"]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(summary, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// mergeImpactCandidates deduplicates by (subject_kind, subject_id): the highest
// priority source (direct < contract < relation < ai), the highest confidence,
// the concatenated reasons and the merged evidence win (design spec §6.6 step
// 4), then the default selection and the snowflake id are assigned.
func mergeImpactCandidates(items []domain.ImpactCandidate, ids IDGenerator) []domain.ImpactCandidate {
	byKey := make(map[string]*domain.ImpactCandidate, len(items))
	for _, item := range items {
		key := item.SubjectKind + "\x00" + strconv.FormatInt(item.SubjectID, 10)
		existing, ok := byKey[key]
		if !ok {
			copied := item
			copied.Evidence = cloneImpactEvidence(item.Evidence)
			byKey[key] = &copied
			continue
		}
		if impactSourceRank(item.Source) < impactSourceRank(existing.Source) {
			existing.Source = item.Source
		}
		if impactConfidenceRank(item.Confidence) < impactConfidenceRank(existing.Confidence) {
			existing.Confidence = item.Confidence
		}
		existing.Reason = mergeImpactReason(existing.Reason, item.Reason)
		existing.Evidence = mergeImpactEvidence(existing.Evidence, item.Evidence)
	}

	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	merged := make([]domain.ImpactCandidate, 0, len(keys))
	for _, key := range keys {
		candidate := *byKey[key]
		if len([]rune(candidate.Reason)) > domain.MaxReasonChars {
			candidate.Reason = truncateRunesLimit(candidate.Reason, domain.MaxReasonChars)
		}
		candidate.Evidence = mergeImpactEvidence(candidate.Evidence)
		candidate.Selected = domain.DefaultSelected(candidate.Confidence)
		if ids != nil {
			candidate.ID = ids.Next()
		}
		merged = append(merged, candidate)
	}
	return merged
}

// mergeImpactReason concatenates two non-empty reasons once each.
func mergeImpactReason(left, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "" || left == right:
		return left
	default:
		return left + impactReasonSeparator + right
	}
}

// mergeImpactEvidence deduplicates, sorts and caps candidate evidence at the
// §6.6 step 4 cap.
func mergeImpactEvidence(parts ...[]domain.Evidence) []domain.Evidence {
	merged := mergeContractEvidence(parts...)
	if len(merged) > maxImpactCandidateEvidence {
		merged = merged[:maxImpactCandidateEvidence]
	}
	return merged
}

// cloneImpactEvidence copies an evidence slice so candidates never alias
// repository state.
func cloneImpactEvidence(evidence []domain.Evidence) []domain.Evidence {
	if len(evidence) == 0 {
		return nil
	}
	cloned := make([]domain.Evidence, len(evidence))
	copy(cloned, evidence)
	return cloned
}

// impactSourceRank orders candidate sources by merge priority.
func impactSourceRank(source string) int {
	switch source {
	case domain.CandidateDirect:
		return 0
	case domain.CandidateContract:
		return 1
	case domain.CandidateRelation:
		return 2
	case domain.CandidateAI:
		return 3
	default:
		return 4
	}
}

// impactConfidenceRank orders high < medium < low.
func impactConfidenceRank(confidence string) int {
	switch confidence {
	case domain.ConfidenceHigh:
		return 0
	case domain.ConfidenceMedium:
		return 1
	case domain.ConfidenceLow:
		return 2
	default:
		return 3
	}
}

// strongerImpactConfidence returns the higher of two confidence levels.
func strongerImpactConfidence(left, right string) string {
	if impactConfidenceRank(right) < impactConfidenceRank(left) {
		return right
	}
	return left
}

// unmappedChanges builds the §6.6 step 7 list: one entry per uncovered changed
// file plus the extra unmatched-contract entries, sorted together by path and
// capped at the documented maximum with the truncation flag. Sorting the combined
// list keeps the output deterministic and path-ordered even though the
// unmatched-contract extras are not part of the per-file dedupe.
func (s *ImpactService) unmappedChanges(state *impactPlanState) ([]domain.UnmappedChange, bool) {
	covered := make(map[string]struct{})
	for _, anchor := range state.anchors {
		covered[anchor.FilePath] = struct{}{}
	}
	for _, fact := range state.coverageFacts {
		if fact.Path != "" {
			covered[fact.Path] = struct{}{}
		}
		for _, evidence := range fact.Evidence {
			if evidence.File != "" {
				covered[evidence.File] = struct{}{}
			}
		}
	}

	entries := make([]domain.UnmappedChange, 0, len(state.changedPaths))
	for _, path := range state.changedPaths {
		if _, ok := covered[path]; ok {
			continue
		}
		file := state.changed[path]
		reason := domain.UnmappedNoFact
		suggestion := impactSuggestionNoFact
		switch {
		case file.Status == statusChangedFileDeleted:
			reason = domain.UnmappedDeleted
			suggestion = impactSuggestionDeleted
		case file.Binary || file.HeadBlobHash == "":
			reason = domain.UnmappedUnsupported
			suggestion = impactSuggestionUnsupported
		case state.aiUnavailable || state.in.Degraded || s.deps.LLM == nil:
			reason = domain.UnmappedAIUnavailable
			suggestion = impactSuggestionAI
		}
		entries = append(entries, domain.UnmappedChange{
			Path:       path,
			Status:     file.Status,
			Reason:     reason,
			Suggestion: suggestion,
		})
	}

	for _, path := range state.unmatchedContractPaths {
		file := state.changed[path]
		entries = append(entries, domain.UnmappedChange{
			Path:       path,
			Status:     file.Status,
			Reason:     domain.UnmappedContract,
			Suggestion: impactSuggestionContract,
		})
	}

	// Deterministic, path-ascending combined order. The sort is stable so a
	// regular file entry stays before the unmatched-contract extra of the same
	// path.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	truncated := false
	if len(entries) > domain.MaxUnmappedEntries {
		entries = entries[:domain.MaxUnmappedEntries]
		truncated = true
	}
	return entries, truncated
}

// marshalImpactStats encodes the planner counters. The struct has no map fields
// and no timestamps, so the document is deterministic (INV-4).
func marshalImpactStats(state *impactPlanState, candidates []domain.ImpactCandidate, unmapped []domain.UnmappedChange, unmappedTruncated bool) []byte {
	stats := state.stats
	stats.Candidates = len(candidates)
	stats.Unmapped = len(unmapped)
	stats.UnmappedTruncated = unmappedTruncated
	stats.AIUnavailable = state.aiUnavailable
	for _, candidate := range candidates {
		switch candidate.SubjectKind {
		case domain.CandidateRequirement:
			stats.Requirements++
		case domain.CandidateAppModel:
			stats.AppModels++
		}
		switch candidate.Source {
		case domain.CandidateDirect:
			stats.Direct++
		case domain.CandidateContract:
			stats.Contract++
		case domain.CandidateRelation:
			stats.Relation++
		case domain.CandidateAI:
			stats.AI++
		}
	}
	raw, err := json.Marshal(stats)
	if err != nil {
		// The struct only contains JSON-safe scalar fields; this line is
		// unreachable but keeps the function total without panicking.
		return []byte("{}")
	}
	return raw
}
