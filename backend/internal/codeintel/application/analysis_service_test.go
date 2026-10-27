package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Unit-test fixture content of the analysed files. The line numbers of the
// evidence ranges below are chosen against these exact contents.
const (
	analysisTestModel  = "test-model"
	analysisTestPrompt = PromptVersionDefault

	analysisRefundClientContent = "import { post } from './http';\n" +
		"export function refund(orderId: string) {\n" +
		"  return post('/api/refund', { orderId });\n" +
		"}\n"
	analysisRefundServerContent = "package server\n" +
		"\n" +
		"func Refund() {}\n"
	analysisUtilsContent = "export const format = (value: string) => value;\n"
)

// analysisPromptFilePath reads the path line BuildFilePrompt renders into the
// user message, so the LLM double can answer per file without knowing the
// prompt implementation details beyond the public template.
func analysisPromptFilePath(user string) string {
	for _, line := range strings.Split(user, "\n") {
		if strings.HasPrefix(line, "文件：") {
			return strings.TrimSpace(strings.TrimPrefix(line, "文件："))
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Git double with per-path commit content.
// ---------------------------------------------------------------------------

type analysisGit struct {
	head      string
	headErr   error
	headCalls []string

	ancestor      bool
	ancestorErr   error
	ancestorCalls []analysisGitAncestorCall

	diff      domain.ChangeSetData
	diffErr   error
	diffCalls []gitDiffCall

	files     map[string]string
	truncated map[string]bool
	readErr   map[string]error
	readCalls []string
}

type analysisGitAncestorCall struct {
	localPath string
	base      string
	head      string
}

func newAnalysisGit() *analysisGit {
	return &analysisGit{
		head:      testCommitB,
		ancestor:  true,
		files:     make(map[string]string),
		truncated: make(map[string]bool),
		readErr:   make(map[string]error),
	}
}

func (g *analysisGit) Head(_ context.Context, localPath, branch string) (string, error) {
	g.headCalls = append(g.headCalls, branch)
	return g.head, g.headErr
}

func (g *analysisGit) IsAncestor(_ context.Context, localPath, base, head string) (bool, error) {
	g.ancestorCalls = append(g.ancestorCalls, analysisGitAncestorCall{localPath: localPath, base: base, head: head})
	return g.ancestor, g.ancestorErr
}

func (g *analysisGit) Tree(context.Context, string, string, string, []string, []string, string, int) (domain.TreePage, error) {
	return domain.TreePage{}, nil
}

func (g *analysisGit) File(context.Context, string, string, string, int, int) (domain.FileWindow, error) {
	return domain.FileWindow{}, nil
}

func (g *analysisGit) ReadAll(_ context.Context, localPath, commit, path string, maxBytes int) ([]byte, bool, error) {
	g.readCalls = append(g.readCalls, path)
	if err, ok := g.readErr[path]; ok && err != nil {
		return nil, false, err
	}
	content, ok := g.files[path]
	if !ok {
		return nil, false, domain.NewInvalid("path", "path_not_found", "file not found")
	}
	raw := []byte(content)
	if g.truncated[path] {
		return raw, true, nil
	}
	if maxBytes > 0 && len(raw) > maxBytes {
		return raw[:maxBytes], true, nil
	}
	return raw, false, nil
}

func (g *analysisGit) Diff(_ context.Context, localPath, base, head string) (domain.ChangeSetData, error) {
	g.diffCalls = append(g.diffCalls, gitDiffCall{localPath: localPath, base: base, head: head})
	return g.diff, g.diffErr
}

func (g *analysisGit) FileDiff(context.Context, string, string, string, string, int) (domain.FileDiff, error) {
	return domain.FileDiff{}, nil
}

func (g *analysisGit) Commit(context.Context, string, string) (domain.CommitInfo, error) {
	return domain.CommitInfo{}, nil
}

// ---------------------------------------------------------------------------
// Entity repository double.
// ---------------------------------------------------------------------------

type analysisEntities struct {
	mu      sync.Mutex
	facts   map[string]domain.CodeEntity
	files   map[string]domain.CodeEntity
	upserts []domain.CodeEntity
	err     error

	listByPathsCalls [][]string
	listFilters      []domain.EntityFilter
}

func newAnalysisEntities() *analysisEntities {
	return &analysisEntities{
		facts: make(map[string]domain.CodeEntity),
		files: make(map[string]domain.CodeEntity),
	}
}

func analysisEntityKey(repoID int64, commit, factKey string) string {
	return fmt.Sprintf("%d|%s|%s", repoID, commit, factKey)
}

func (e *analysisEntities) Upsert(_ context.Context, entity domain.CodeEntity) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	if entity.FactKey == nil {
		e.files[entity.Path] = entity
		return nil
	}
	key := analysisEntityKey(entity.RepoID, entity.Commit, *entity.FactKey)
	if existing, ok := e.facts[key]; ok {
		// The real ON CONFLICT (repo_id, commit_sha, fact_key) DO UPDATE keeps
		// the existing row id and created_at; only the payload is refreshed.
		entity.ID = existing.ID
		entity.CreatedAt = existing.CreatedAt
	}
	e.facts[key] = entity
	e.upserts = append(e.upserts, entity)
	return nil
}

func (e *analysisEntities) UpsertFile(context.Context, int64, string, []domain.CodeEntity) error {
	return nil
}

func (e *analysisEntities) GetByFactKey(_ context.Context, repoID int64, commit, factKey string) (domain.CodeEntity, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entity, ok := e.facts[analysisEntityKey(repoID, commit, factKey)]
	if !ok {
		return domain.CodeEntity{}, domain.NewNotFound("entity_not_found", "entity not found")
	}
	return entity, nil
}

func (e *analysisEntities) List(_ context.Context, repoID int64, filter domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listFilters = append(e.listFilters, filter)
	entities := make([]domain.CodeEntity, 0)
	for _, entity := range e.facts {
		if entity.RepoID != repoID || entity.Commit != filter.Commit {
			continue
		}
		if filter.Kind != "" && entity.Kind != filter.Kind {
			continue
		}
		entities = append(entities, entity)
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i].ID < entities[j].ID })
	return entities, "", false, nil
}

func (e *analysisEntities) ListByPaths(_ context.Context, repoID int64, commit string, paths []string) ([]domain.CodeEntity, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listByPathsCalls = append(e.listByPathsCalls, append([]string(nil), paths...))
	wanted := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		wanted[path] = struct{}{}
	}
	entities := make([]domain.CodeEntity, 0)
	for _, entity := range e.facts {
		if entity.RepoID != repoID || entity.Commit != commit {
			continue
		}
		if _, ok := wanted[entity.Path]; !ok {
			continue
		}
		entities = append(entities, entity)
	}
	sort.Slice(entities, func(i, j int) bool {
		if entities[i].Path != entities[j].Path {
			return entities[i].Path < entities[j].Path
		}
		return entities[i].ID < entities[j].ID
	})
	return entities, nil
}

func (e *analysisEntities) DeleteFact(context.Context, int64, string, string) error { return nil }

func (e *analysisEntities) fact(repoID int64, commit, factKey string) (domain.CodeEntity, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entity, ok := e.facts[analysisEntityKey(repoID, commit, factKey)]
	return entity, ok
}

func (e *analysisEntities) seededFacts() []domain.CodeEntity {
	e.mu.Lock()
	defer e.mu.Unlock()
	entities := make([]domain.CodeEntity, 0, len(e.facts))
	for _, entity := range e.facts {
		entities = append(entities, entity)
	}
	return entities
}

// ---------------------------------------------------------------------------
// Edge repository double.
// ---------------------------------------------------------------------------

type analysisEdgeReplace struct {
	repoID  int64
	commit  string
	factKey string
	version int
	edges   []domain.CodeEdge
}

type analysisEdges struct {
	mu       sync.Mutex
	replaces []analysisEdgeReplace
	byOwner  map[string][]domain.CodeEdge
	err      error
}

func newAnalysisEdges() *analysisEdges {
	return &analysisEdges{byOwner: make(map[string][]domain.CodeEdge)}
}

func (e *analysisEdges) ReplaceFactEdges(_ context.Context, repoID int64, commit, factKey string, version int, edges []domain.CodeEdge) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	copied := append([]domain.CodeEdge(nil), edges...)
	e.replaces = append(e.replaces, analysisEdgeReplace{
		repoID: repoID, commit: commit, factKey: factKey, version: version, edges: copied,
	})
	e.byOwner[factKey] = copied
	return nil
}

func (e *analysisEdges) ListByEntity(context.Context, int64, string, int64) ([]domain.CodeEdge, error) {
	return nil, nil
}

func (e *analysisEdges) ListByRel(_ context.Context, repoID int64, commit, rel string) ([]domain.CodeEdge, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	edges := make([]domain.CodeEdge, 0)
	for _, ownerEdges := range e.byOwner {
		for _, edge := range ownerEdges {
			if edge.Rel == rel {
				edges = append(edges, edge)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return edges, nil
}

func (e *analysisEdges) replacesFor(factKey string) []analysisEdgeReplace {
	e.mu.Lock()
	defer e.mu.Unlock()
	matches := make([]analysisEdgeReplace, 0)
	for _, replace := range e.replaces {
		if replace.factKey == factKey {
			matches = append(matches, replace)
		}
	}
	return matches
}

func (e *analysisEdges) allEdges() []domain.CodeEdge {
	e.mu.Lock()
	defer e.mu.Unlock()
	edges := make([]domain.CodeEdge, 0)
	for _, ownerEdges := range e.byOwner {
		edges = append(edges, ownerEdges...)
	}
	return edges
}

// ---------------------------------------------------------------------------
// Anchor repository double.
// ---------------------------------------------------------------------------

type analysisAnchors struct {
	anchors   []domain.CodeAnchor
	err       error
	listCalls [][]string
}

func (a *analysisAnchors) Create(context.Context, domain.CodeAnchor) error { return nil }
func (a *analysisAnchors) Delete(context.Context, int64, int64) error      { return nil }
func (a *analysisAnchors) Get(context.Context, int64, int64) (domain.CodeAnchor, error) {
	return domain.CodeAnchor{}, domain.ErrNotFound
}

func (a *analysisAnchors) List(context.Context, int64, domain.AnchorFilter) ([]domain.CodeAnchor, error) {
	return a.anchors, a.err
}

func (a *analysisAnchors) ListByPaths(_ context.Context, _ int64, paths []string) ([]domain.CodeAnchor, error) {
	a.listCalls = append(a.listCalls, append([]string(nil), paths...))
	if a.err != nil {
		return nil, a.err
	}
	wanted := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		wanted[path] = struct{}{}
	}
	matches := make([]domain.CodeAnchor, 0)
	for _, anchor := range a.anchors {
		if _, ok := wanted[anchor.FilePath]; ok {
			matches = append(matches, anchor)
		}
	}
	return matches, nil
}

// ---------------------------------------------------------------------------
// Fact-version repository double.
// ---------------------------------------------------------------------------

type analysisFactVersion struct {
	body       []byte
	model      string
	prompt     string
	analysisID int64
	superseded bool
}

type analysisFactCreate struct {
	id         int64
	repoID     int64
	commit     string
	factKey    string
	model      string
	prompt     string
	version    int
	body       []byte
	analysisID int64
}

type analysisFactSupersede struct {
	repoID  int64
	commit  string
	factKey string
	before  int
	at      time.Time
}

type analysisFacts struct {
	mu         sync.Mutex
	entities   *analysisEntities
	versions   map[string]map[int]analysisFactVersion
	creates    []analysisFactCreate
	supersedes []analysisFactSupersede

	currentErr    error
	getVersionErr error
	createErr     error
	supersedeErr  error
}

func newAnalysisFacts(entities *analysisEntities) *analysisFacts {
	return &analysisFacts{entities: entities, versions: make(map[string]map[int]analysisFactVersion)}
}

func (f *analysisFacts) Current(_ context.Context, repoID int64, commit, factKey string) (*domain.CodeEntity, int, error) {
	if f.currentErr != nil {
		return nil, 0, f.currentErr
	}
	entity, ok := f.entities.fact(repoID, commit, factKey)
	if !ok {
		return nil, 0, domain.NewNotFound("fact_not_found", "current fact not found")
	}
	if entity.FactVersion == nil {
		return nil, 0, domain.NewNotFound("fact_not_found", "current fact has no version")
	}
	return &entity, *entity.FactVersion, nil
}

func (f *analysisFacts) GetVersion(_ context.Context, repoID int64, commit, factKey string, version int) ([]byte, error) {
	if f.getVersionErr != nil {
		return nil, f.getVersionErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	versions, ok := f.versions[analysisEntityKey(repoID, commit, factKey)]
	if !ok {
		return nil, domain.NewNotFound("fact_not_found", "fact version not found")
	}
	stored, ok := versions[version]
	if !ok {
		return nil, domain.NewNotFound("fact_not_found", "fact version not found")
	}
	return stored.body, nil
}

func (f *analysisFacts) Create(_ context.Context, id int64, repoID int64, commit, factKey, model, prompt string, version int, body []byte, analysisID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	key := analysisEntityKey(repoID, commit, factKey)
	if stored, ok := f.versions[key][version]; ok {
		// The real code_fact_versions unique constraint rejects a duplicate
		// (repo, commit, fact_key, version) row; model it so retry paths are
		// exercised faithfully.
		_ = stored
		return domain.NewConflict("duplicate_key",
			fmt.Sprintf("fact %q version %d already exists", factKey, version))
	}
	f.creates = append(f.creates, analysisFactCreate{
		id: id, repoID: repoID, commit: commit, factKey: factKey,
		model: model, prompt: prompt, version: version, body: append([]byte(nil), body...),
		analysisID: analysisID,
	})
	if f.versions[key] == nil {
		f.versions[key] = make(map[int]analysisFactVersion)
	}
	f.versions[key][version] = analysisFactVersion{
		body: append([]byte(nil), body...), model: model, prompt: prompt, analysisID: analysisID,
	}
	return nil
}

func (f *analysisFacts) Supersede(_ context.Context, repoID int64, commit, factKey string, before int, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.supersedeErr != nil {
		return f.supersedeErr
	}
	f.supersedes = append(f.supersedes, analysisFactSupersede{
		repoID: repoID, commit: commit, factKey: factKey, before: before, at: at,
	})
	for version, stored := range f.versions[analysisEntityKey(repoID, commit, factKey)] {
		if version < before {
			stored.superseded = true
			f.versions[analysisEntityKey(repoID, commit, factKey)][version] = stored
		}
	}
	return nil
}

func (f *analysisFacts) seed(entity domain.CodeEntity, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := analysisEntityKey(entity.RepoID, entity.Commit, *entity.FactKey)
	if f.versions[key] == nil {
		f.versions[key] = make(map[int]analysisFactVersion)
	}
	f.versions[key][*entity.FactVersion] = analysisFactVersion{body: body}
}

func (f *analysisFacts) versionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, versions := range f.versions {
		count += len(versions)
	}
	return count
}

// ---------------------------------------------------------------------------
// Cache repository double.
// ---------------------------------------------------------------------------

type analysisCacheGet struct {
	repoID int64
	commit string
	path   string
	hash   string
	model  string
	prompt string
}

type analysisCache struct {
	mu      sync.Mutex
	files   map[string]domain.CachedFile
	gets    []analysisCacheGet
	upserts []domain.CachedFile
	getErr  error
	upsErr  error
}

func newAnalysisCache() *analysisCache {
	return &analysisCache{files: make(map[string]domain.CachedFile)}
}

func analysisCacheKey(repoID int64, commit, path, hash, model, prompt string) string {
	return fmt.Sprintf("%d|%s|%s|%s|%s|%s", repoID, commit, path, hash, model, prompt)
}

func (c *analysisCache) Get(_ context.Context, repoID int64, commit, path, fileHash, model, prompt string) (*domain.CachedFile, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets = append(c.gets, analysisCacheGet{
		repoID: repoID, commit: commit, path: path, hash: fileHash, model: model, prompt: prompt,
	})
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	cached, ok := c.files[analysisCacheKey(repoID, commit, path, fileHash, model, prompt)]
	if !ok {
		return nil, false, nil
	}
	return &cached, true, nil
}

func (c *analysisCache) Upsert(_ context.Context, cached domain.CachedFile) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.upsErr != nil {
		return c.upsErr
	}
	c.upserts = append(c.upserts, cached)
	c.files[analysisCacheKey(cached.RepoID, cached.Commit, cached.Path, cached.FileHash, cached.Model, cached.PromptVersion)] = cached
	return nil
}

func (c *analysisCache) seed(cached domain.CachedFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[analysisCacheKey(cached.RepoID, cached.Commit, cached.Path, cached.FileHash, cached.Model, cached.PromptVersion)] = cached
}

func (c *analysisCache) upsertCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.upserts)
}

// ---------------------------------------------------------------------------
// Analysis repository double.
// ---------------------------------------------------------------------------

type analysisFindReusableCall struct {
	versionID int64
	repoID    int64
	base      string
	head      string
	model     string
	prompt    string
}

type analysisSelectionUpdate struct {
	analysisID  int64
	candidateID int64
	actorID     int64
	selected    bool
}

type analysisRepo struct {
	mu       sync.Mutex
	byID     map[int64]domain.CodeAnalysis
	reusable *domain.CodeAnalysis
	active   bool
	next     int

	findCalls      []analysisFindReusableCall
	nextCalls      int
	creates        []domain.CodeAnalysis
	updates        []domain.CodeAnalysis
	selections     []analysisSelectionUpdate
	createErr      error
	updateErr      error
	reusableErr    error
	activeErr      error
	nextErr        error
	selectionErr   error
	recoverStaleFn func() (int64, error)
}

func newAnalysisRepo() *analysisRepo {
	return &analysisRepo{byID: make(map[int64]domain.CodeAnalysis), next: 1}
}

func (r *analysisRepo) Create(_ context.Context, analysis domain.CodeAnalysis) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.byID[analysis.ID] = analysis
	r.creates = append(r.creates, analysis)
	return nil
}

func (r *analysisRepo) Update(_ context.Context, analysis domain.CodeAnalysis) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	r.byID[analysis.ID] = analysis
	r.updates = append(r.updates, analysis)
	return nil
}

func (r *analysisRepo) GetByID(_ context.Context, versionID, id int64) (domain.CodeAnalysis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	analysis, ok := r.byID[id]
	if !ok || analysis.VersionID != versionID {
		return domain.CodeAnalysis{}, domain.NewNotFound("analysis_not_found", "analysis not found")
	}
	return analysis, nil
}

func (r *analysisRepo) GetLatest(_ context.Context, versionID int64) (domain.CodeAnalysis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest domain.CodeAnalysis
	found := false
	for _, analysis := range r.byID {
		if analysis.VersionID != versionID {
			continue
		}
		if !found || analysis.CreatedAt.After(latest.CreatedAt) ||
			(analysis.CreatedAt.Equal(latest.CreatedAt) && analysis.ID > latest.ID) {
			latest, found = analysis, true
		}
	}
	if !found {
		return domain.CodeAnalysis{}, domain.NewNotFound("analysis_not_found", "no analysis")
	}
	return latest, nil
}

func (r *analysisRepo) FindReusable(_ context.Context, versionID, repoID int64, base, head, model, prompt string) (*domain.CodeAnalysis, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findCalls = append(r.findCalls, analysisFindReusableCall{
		versionID: versionID, repoID: repoID, base: base, head: head, model: model, prompt: prompt,
	})
	if r.reusableErr != nil {
		return nil, r.reusableErr
	}
	if r.reusable == nil {
		return nil, nil
	}
	copied := *r.reusable
	return &copied, nil
}

func (r *analysisRepo) HasActive(context.Context, int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active, r.activeErr
}

func (r *analysisRepo) NextAnalysisVersion(context.Context, int64, string, string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextCalls++
	return r.next, r.nextErr
}

func (r *analysisRepo) RecoverStale(context.Context, time.Time) (int64, error) {
	if r.recoverStaleFn != nil {
		return r.recoverStaleFn()
	}
	return 0, nil
}

func (r *analysisRepo) UpdateCandidateSelection(_ context.Context, analysisID, candidateID, actorID int64, selected bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.selectionErr != nil {
		return r.selectionErr
	}
	r.selections = append(r.selections, analysisSelectionUpdate{
		analysisID: analysisID, candidateID: candidateID, actorID: actorID, selected: selected,
	})
	return nil
}

func (r *analysisRepo) stored(id int64) (domain.CodeAnalysis, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	analysis, ok := r.byID[id]
	return analysis, ok
}

func (r *analysisRepo) updateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.updates)
}

// ---------------------------------------------------------------------------
// Candidate repository double.
// ---------------------------------------------------------------------------

type analysisCandidateReplace struct {
	analysisID int64
	items      []domain.ImpactCandidate
}

type analysisCandidates struct {
	mu         sync.Mutex
	items      map[int64][]domain.ImpactCandidate
	replaces   []analysisCandidateReplace
	listErr    error
	getErr     error
	replaceErr error
}

func newAnalysisCandidates() *analysisCandidates {
	return &analysisCandidates{items: make(map[int64][]domain.ImpactCandidate)}
}

func (c *analysisCandidates) Replace(_ context.Context, analysisID int64, items []domain.ImpactCandidate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.replaceErr != nil {
		return c.replaceErr
	}
	copied := append([]domain.ImpactCandidate(nil), items...)
	c.items[analysisID] = copied
	c.replaces = append(c.replaces, analysisCandidateReplace{analysisID: analysisID, items: copied})
	return nil
}

func (c *analysisCandidates) List(_ context.Context, analysisID int64) ([]domain.ImpactCandidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listErr != nil {
		return nil, c.listErr
	}
	return append([]domain.ImpactCandidate(nil), c.items[analysisID]...), nil
}

func (c *analysisCandidates) Get(_ context.Context, analysisID, candidateID int64) (domain.ImpactCandidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return domain.ImpactCandidate{}, c.getErr
	}
	for _, candidate := range c.items[analysisID] {
		if candidate.ID == candidateID {
			return candidate, nil
		}
	}
	return domain.ImpactCandidate{}, domain.NewNotFound("candidate_not_found", "candidate not found")
}

func (c *analysisCandidates) seed(analysisID int64, items ...domain.ImpactCandidate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[analysisID] = append([]domain.ImpactCandidate(nil), items...)
}

func (c *analysisCandidates) replacedItems(analysisID int64) ([]domain.ImpactCandidate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.replaces) - 1; index >= 0; index-- {
		if c.replaces[index].analysisID == analysisID {
			return append([]domain.ImpactCandidate(nil), c.replaces[index].items...), true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Change-set repository double.
// ---------------------------------------------------------------------------

type analysisChangeSets struct {
	stored     domain.CodeChangeSet
	wasCreated bool
	err        error
	calls      []domain.CodeChangeSet
}

func (c *analysisChangeSets) GetOrCreate(_ context.Context, changeSet domain.CodeChangeSet) (domain.CodeChangeSet, bool, error) {
	c.calls = append(c.calls, changeSet)
	if c.err != nil {
		return domain.CodeChangeSet{}, false, c.err
	}
	if c.stored.ID != 0 {
		return c.stored, false, nil
	}
	c.stored = changeSet
	c.wasCreated = true
	return changeSet, true, nil
}

func (c *analysisChangeSets) GetByID(context.Context, int64) (domain.CodeChangeSet, error) {
	return domain.CodeChangeSet{}, domain.ErrNotFound
}

func (c *analysisChangeSets) FindByCommits(_ context.Context, repoID int64, base, head string) (domain.CodeChangeSet, error) {
	if c.stored.ID != 0 && c.stored.RepoID == repoID &&
		c.stored.BaseCommit == base && c.stored.HeadCommit == head {
		return c.stored, nil
	}
	return domain.CodeChangeSet{}, domain.NewNotFound("change_set_not_found", "change set not found")
}

// ---------------------------------------------------------------------------
// Notifier, LLM and planner doubles.
// ---------------------------------------------------------------------------

type analysisNotifier struct {
	mu              sync.Mutex
	changed         []ChangedEvent
	changedVersions []int64
	impacts         []ImpactEvent
	impactVersions  []int64
	err             error
}

func (n *analysisNotifier) Changed(_ context.Context, versionID int64, event ChangedEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.changed = append(n.changed, event)
	n.changedVersions = append(n.changedVersions, versionID)
	return nil
}

func (n *analysisNotifier) ImpactComputed(_ context.Context, versionID int64, event ImpactEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.impacts = append(n.impacts, event)
	n.impactVersions = append(n.impactVersions, versionID)
	return nil
}

type stubLLM struct {
	mu              sync.Mutex
	model           string
	responses       map[string]string
	errs            map[string]error
	defaultResponse string
	defaultErr      error
	calls           int
	users           []string
}

func newStubLLM() *stubLLM {
	return &stubLLM{
		model:     analysisTestModel,
		responses: make(map[string]string),
		errs:      make(map[string]error),
	}
}

func (l *stubLLM) Complete(_ context.Context, system, user string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.users = append(l.users, user)
	path := analysisPromptFilePath(user)
	if err, ok := l.errs[path]; ok && err != nil {
		return "", err
	}
	if response, ok := l.responses[path]; ok {
		return response, nil
	}
	if l.defaultErr != nil {
		return "", l.defaultErr
	}
	return l.defaultResponse, nil
}

func (l *stubLLM) Model() string {
	if l == nil {
		return ""
	}
	return l.model
}

func (l *stubLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

type stubPlanner struct {
	calls  []ImpactInput
	result ImpactResult
	err    error
}

func (p *stubPlanner) Plan(_ context.Context, in ImpactInput) (ImpactResult, error) {
	p.calls = append(p.calls, in)
	return p.result, p.err
}

// ---------------------------------------------------------------------------
// Fixture.
// ---------------------------------------------------------------------------

type analysisFixture struct {
	git        *analysisGit
	repos      *stubRepos
	versions   *stubVersions
	changeSets *analysisChangeSets
	entities   *analysisEntities
	edges      *analysisEdges
	facts      *analysisFacts
	cache      *analysisCache
	analyses   *analysisRepo
	candidates *analysisCandidates
	anchors    *analysisAnchors
	queue      *stubQueue
	llm        *stubLLM
	planner    *stubPlanner
	notifier   *analysisNotifier
	audit      *stubAudit
	ids        *stubIDs
	clock      *stubClock
	svc        *AnalysisService
}

func newAnalysisFixture(t *testing.T) *analysisFixture {
	t.Helper()
	now := time.Date(2026, 10, 28, 2, 3, 4, 0, time.UTC)
	entities := newAnalysisEntities()
	f := &analysisFixture{
		git:        newAnalysisGit(),
		repos:      &stubRepos{},
		versions:   &stubVersions{},
		changeSets: &analysisChangeSets{},
		entities:   entities,
		edges:      newAnalysisEdges(),
		facts:      newAnalysisFacts(entities),
		cache:      newAnalysisCache(),
		analyses:   newAnalysisRepo(),
		candidates: newAnalysisCandidates(),
		anchors:    &analysisAnchors{},
		queue:      &stubQueue{},
		llm:        newStubLLM(),
		planner:    &stubPlanner{},
		notifier:   &analysisNotifier{},
		audit:      &stubAudit{},
		ids:        &stubIDs{next: 7000},
		clock:      &stubClock{now: now},
	}
	f.repos.infos = map[int64]RepoInfo{
		3: {
			ID:           3,
			Key:          "web",
			Name:         "Web",
			LocalPath:    "/repo",
			FixedBranch:  "main",
			IncludeGlobs: nil,
			ExcludeGlobs: []string{"vendor/**"},
			Status:       "active",
		},
	}
	f.versions.info = VersionInfo{
		ID:             11,
		SystemID:       5,
		RepoID:         3,
		HasSystem:      true,
		HasRepo:        true,
		Branch:         "main",
		BaselineCommit: testCommitA,
	}
	f.versions.requirements = []RequirementInfo{
		{ID: 77, Title: "Refund reason", Description: "Refund should accept a reason", Status: "draft"},
		{ID: 78, Title: "Order list", Description: "Order list pagination", Status: "draft"},
	}
	f.versions.relations = []RelationInfo{{FromID: 77, ToID: 78, Type: "relates_to"}}
	f.git.files = map[string]string{
		"web/src/api/refund.ts": analysisRefundClientContent,
		"server/refund.go":      analysisRefundServerContent,
		"web/src/utils.ts":      analysisUtilsContent,
	}
	f.svc = f.buildService()
	return f
}

func (f *analysisFixture) buildService() *AnalysisService {
	var llm LLMCompleter
	if f.llm != nil {
		llm = f.llm
	}
	return NewAnalysisService(AnalysisServiceDeps{
		Git:        f.git,
		Repos:      f.repos,
		Versions:   f.versions,
		ChangeSets: f.changeSets,
		Entities:   f.entities,
		Edges:      f.edges,
		Facts:      f.facts,
		Cache:      f.cache,
		Analyses:   f.analyses,
		Candidates: f.candidates,
		Anchors:    f.anchors,
		Queue:      f.queue,
		LLM:        llm,
		Planner:    f.planner,
		Notifier:   f.notifier,
		Audit:      f.audit,
		IDs:        f.ids,
		Clock:      f.clock,
		Config: AnalysisConfig{
			MaxChangedFiles: 20,
			MaxFileBytes:    65536,
			MaxExcerptLines: 40,
			PromptVersion:   analysisTestPrompt,
		},
	})
}

func (f *analysisFixture) rebuild() {
	f.svc = f.buildService()
}

// startAnalysis triggers one analysis with the fixture's default version/actor.
func (f *analysisFixture) startAnalysis(t *testing.T, force bool) domain.CodeAnalysis {
	t.Helper()
	analysis, reused, err := f.svc.Start(context.Background(), 11, 42, "", force)
	if err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if reused {
		t.Fatalf("Start reused = true, want a new queued analysis")
	}
	return analysis
}

// runAnalysis executes one analysis synchronously.
func (f *analysisFixture) runAnalysis(t *testing.T, analysisID int64) {
	t.Helper()
	if err := f.svc.Run(context.Background(), analysisID); err != nil {
		t.Fatalf("Run error = %v", err)
	}
}

// runNewAnalysis starts and runs one analysis.
func (f *analysisFixture) runNewAnalysis(t *testing.T) domain.CodeAnalysis {
	t.Helper()
	analysis := f.startAnalysis(t, false)
	f.runAnalysis(t, analysis.ID)
	stored, ok := f.analyses.stored(analysis.ID)
	if !ok {
		t.Fatalf("analysis %d missing after Run", analysis.ID)
	}
	return stored
}

func (f *analysisFixture) storedAnalysis(t *testing.T, analysisID int64) domain.CodeAnalysis {
	t.Helper()
	stored, ok := f.analyses.stored(analysisID)
	if !ok {
		t.Fatalf("analysis %d missing", analysisID)
	}
	return stored
}

// changeSet configures the diff fixture and returns the changed files.
func (f *analysisFixture) changeSet(files ...domain.ChangedFile) []domain.ChangedFile {
	stats := map[string]any{
		"changedFiles": len(files),
		"additions":    len(files),
		"deletions":    0,
		"truncated":    false,
	}
	raw, _ := json.Marshal(stats)
	f.git.diff = domain.ChangeSetData{Stats: raw, Files: files}
	return files
}

// analysisChangedFile builds one changed file entry. A deleted file carries no
// head blob hash.
func analysisChangedFile(index int, path, status string) domain.ChangedFile {
	file := domain.ChangedFile{
		Path:      path,
		Status:    status,
		Additions: 1,
		Deletions: 0,
	}
	if status != "D" {
		file.HeadBlobHash = fmt.Sprintf("git-sha1:%040d", index)
	}
	return file
}

// clientFactResponse is the deterministic T1 response for the refund client
// file. Its evidence covers lines 2-3 of analysisRefundClientContent.
func clientFactResponse() string {
	return clientFactResponseWith("refund client call adds a reason parameter", "client calls the refund API")
}

// clientFactResponseWithFactSummary keeps the same change summary while varying
// the fact body, so version bumping can be exercised independently.
func clientFactResponseWithFactSummary(factSummary string) string {
	return clientFactResponseWith("refund client call adds a reason parameter", factSummary)
}

// clientFactResponseWith renders one deterministic client-fact response.
func clientFactResponseWith(changeSummary, factSummary string) string {
	payload := map[string]any{
		"changeSummary": changeSummary,
		"facts": []any{map[string]any{
			"kind":       domain.KindClientCall,
			"name":       "POST /api/refund",
			"path":       "web/src/api/refund.ts",
			"startLine":  1,
			"endLine":    3,
			"signature":  "post('/api/refund')",
			"confidence": domain.ConfidenceHigh,
			"summary":    factSummary,
			"evidence": []any{map[string]any{
				"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3,
			}},
		}},
		"edges": []any{},
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

// apiFactResponse is the deterministic T1 response for the refund server file.
func apiFactResponse() string {
	payload := map[string]any{
		"changeSummary": "refund endpoint adds a reason parameter",
		"facts": []any{map[string]any{
			"kind":       domain.KindAPI,
			"name":       "POST /api/refund",
			"path":       "server/refund.go",
			"startLine":  1,
			"endLine":    3,
			"signature":  "func Refund()",
			"confidence": domain.ConfidenceHigh,
			"summary":    "server serves the refund API",
			"evidence": []any{map[string]any{
				"file": "server/refund.go", "startLine": 1, "endLine": 3,
			}},
		}},
		"edges": []any{},
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

// emptyFactResponse is the deterministic T1 response for a file with no facts.
func emptyFactResponse(summary string) string {
	raw, _ := json.Marshal(map[string]any{
		"changeSummary": summary,
		"facts":         []any{},
		"edges":         []any{},
	})
	return string(raw)
}

// configureFileAnalyses installs the default responses of the fixture's files.
func (f *analysisFixture) configureFileAnalyses() {
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()
	f.llm.responses["server/refund.go"] = apiFactResponse()
	f.llm.responses["web/src/utils.ts"] = emptyFactResponse("format helper changes")
}

// analysisStatsJSON decodes the analysis stats document into a generic map.
func analysisStatsJSON(t *testing.T, analysis domain.CodeAnalysis) map[string]any {
	t.Helper()
	var stats map[string]any
	if err := json.Unmarshal(analysis.Stats, &stats); err != nil {
		t.Fatalf("decode analysis stats %q: %v", string(analysis.Stats), err)
	}
	return stats
}

func analysisStatInt(t *testing.T, stats map[string]any, key string) int {
	t.Helper()
	value, ok := stats[key].(float64)
	if !ok {
		t.Fatalf("stats[%q] = %v, want a number", key, stats[key])
	}
	return int(value)
}

func analysisEvidenceFor(t *testing.T, index int, path string, startLine, endLine int) domain.Evidence {
	t.Helper()
	content, ok := map[string]string{
		"web/src/api/refund.ts": analysisRefundClientContent,
		"server/refund.go":      analysisRefundServerContent,
		"web/src/utils.ts":      analysisUtilsContent,
	}[path]
	if !ok {
		t.Fatalf("no test content for %s", path)
	}
	hash, err := HashExcerpt([]byte(content), startLine, endLine)
	if err != nil {
		t.Fatalf("HashExcerpt(%s, %d, %d): %v", path, startLine, endLine, err)
	}
	return domain.Evidence{
		File: path, StartLine: startLine, EndLine: endLine, ExcerptHash: hash,
	}
}

// ---------------------------------------------------------------------------
// Start tests.
// ---------------------------------------------------------------------------

// TestStartValidationAndReuse pins the design spec §6.4/§6.8 trigger rules:
// version binding and baseline validation, explicit/live head resolution,
// analysis reuse, force versioning, the single-active conflict, same-input reuse
// and the retryable queue-full failure.
func TestStartValidationAndReuse(t *testing.T) {
	t.Run("version without repository is rejected", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.versions.info.HasRepo = false

		_, _, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if !errors.Is(err, domain.ErrInvalid) || domain.Reason(err) != "code_repository_not_bound" {
			t.Fatalf("Start error = %v, want code_repository_not_bound", err)
		}
		if len(f.analyses.creates) != 0 || len(f.queue.jobs) != 0 {
			t.Fatal("rejected Start must not create or enqueue")
		}
	})

	t.Run("version without baseline is rejected", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.versions.info.BaselineCommit = ""

		_, _, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if !errors.Is(err, domain.ErrInvalid) || domain.Reason(err) != "baseline_commit_missing" {
			t.Fatalf("Start error = %v, want baseline_commit_missing", err)
		}
	})

	t.Run("disabled repository is rejected", func(t *testing.T) {
		f := newAnalysisFixture(t)
		info := f.repos.infos[3]
		info.Status = "disabled"
		f.repos.infos[3] = info

		_, _, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if !errors.Is(err, domain.ErrInvalid) || domain.Reason(err) != "code_repository_disabled" {
			t.Fatalf("Start error = %v, want code_repository_disabled", err)
		}
	})

	t.Run("malformed explicit head is invalid", func(t *testing.T) {
		f := newAnalysisFixture(t)

		_, _, err := f.svc.Start(context.Background(), 11, 42, "not-a-commit", false)
		if !errors.Is(err, domain.ErrInvalid) || domain.Reason(err) != "invalid_head" {
			t.Fatalf("Start error = %v, want invalid_head", err)
		}
		if len(f.git.ancestorCalls) != 0 || len(f.git.headCalls) != 0 {
			t.Fatal("malformed head must not be resolved")
		}
	})

	t.Run("explicit head must descend from the baseline", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.git.ancestor = false

		_, _, err := f.svc.Start(context.Background(), 11, 42, testCommitC, false)
		if !errors.Is(err, domain.ErrInvalid) || domain.Reason(err) != "invalid_head" {
			t.Fatalf("Start error = %v, want invalid_head", err)
		}
		if len(f.git.ancestorCalls) != 1 {
			t.Fatalf("IsAncestor calls = %d, want 1", len(f.git.ancestorCalls))
		}
		call := f.git.ancestorCalls[0]
		if call.localPath != "/repo" || call.base != testCommitA || call.head != testCommitC {
			t.Fatalf("IsAncestor args = %+v, want /repo %s %s", call, testCommitA, testCommitC)
		}
		if len(f.git.headCalls) != 0 {
			t.Fatal("explicit head must not resolve the branch tip")
		}
	})

	t.Run("queued analysis carries the resolved head and identity", func(t *testing.T) {
		f := newAnalysisFixture(t)

		analysis, reused, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if err != nil {
			t.Fatalf("Start error = %v", err)
		}
		if reused {
			t.Fatal("Start reused = true, want false")
		}
		if analysis.Status != domain.RunQueued || analysis.VersionID != 11 || analysis.RepoID != 3 {
			t.Fatalf("analysis identity = %+v, want queued version 11 repo 3", analysis)
		}
		if analysis.BaseCommit != testCommitA || analysis.HeadCommit != testCommitB {
			t.Fatalf("commits = %s..%s, want %s..%s", analysis.BaseCommit, analysis.HeadCommit, testCommitA, testCommitB)
		}
		if analysis.Model != analysisTestModel || analysis.PromptVersion != analysisTestPrompt {
			t.Fatalf("model/prompt = %q/%q, want %q/%q", analysis.Model, analysis.PromptVersion, analysisTestModel, analysisTestPrompt)
		}
		if analysis.AnalysisVersion != 1 {
			t.Fatalf("analysisVersion = %d, want 1", analysis.AnalysisVersion)
		}
		if analysis.CreatedBy != 42 || !analysis.CreatedAt.Equal(f.clock.now) {
			t.Fatalf("created = %d/%v, want 42/%v", analysis.CreatedBy, analysis.CreatedAt, f.clock.now)
		}
		if analysis.StartedAt != nil || analysis.FinishedAt != nil {
			t.Fatalf("queued analysis timestamps = %v/%v, want nil", analysis.StartedAt, analysis.FinishedAt)
		}
		if analysis.Degraded {
			t.Fatal("queued analysis degraded = true")
		}
		if string(analysis.Stats) != "{}" {
			t.Fatalf("stats = %s, want {}", string(analysis.Stats))
		}
		if len(analysis.Errors) != 0 || len(analysis.Unmapped) != 0 || len(analysis.FactVersions) != 0 {
			t.Fatalf("queued arrays = %+v, want empty", analysis)
		}
		if len(f.git.headCalls) != 1 || f.git.headCalls[0] != "main" {
			t.Fatalf("Head calls = %v, want main", f.git.headCalls)
		}
		if len(f.analyses.creates) != 1 || f.analyses.creates[0].ID != analysis.ID {
			t.Fatalf("Create calls = %+v, want the queued analysis", f.analyses.creates)
		}
		if len(f.queue.jobs) != 1 || f.queue.jobs[0] != (Job{Kind: JobKindAnalysis, ID: analysis.ID}) {
			t.Fatalf("enqueued jobs = %+v, want one analysis job %d", f.queue.jobs, analysis.ID)
		}
		if len(f.audit.calls) != 1 || f.audit.calls[0].action != "code.analyze" {
			t.Fatalf("audit calls = %+v, want one code.analyze trigger entry", f.audit.calls)
		}
		assertNoCodeContentInAudit(t, f.audit.calls)
	})

	t.Run("explicit descendant head is accepted", func(t *testing.T) {
		f := newAnalysisFixture(t)

		analysis, _, err := f.svc.Start(context.Background(), 11, 42, testCommitC, false)
		if err != nil {
			t.Fatalf("Start error = %v", err)
		}
		if analysis.HeadCommit != testCommitC {
			t.Fatalf("head = %s, want %s", analysis.HeadCommit, testCommitC)
		}
		if len(f.git.headCalls) != 0 {
			t.Fatal("explicit head must not resolve the branch tip")
		}
	})

	t.Run("base equal to head is allowed", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.versions.info.BaselineCommit = testCommitB
		f.git.head = testCommitB

		analysis, _, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if err != nil {
			t.Fatalf("Start error = %v", err)
		}
		if analysis.BaseCommit != analysis.HeadCommit || analysis.HeadCommit != testCommitB {
			t.Fatalf("commits = %s..%s, want %s..%s", analysis.BaseCommit, analysis.HeadCommit, testCommitB, testCommitB)
		}
	})

	t.Run("same input reuses a done analysis", func(t *testing.T) {
		f := newAnalysisFixture(t)
		done := domain.CodeAnalysis{
			ID: 900, VersionID: 11, RepoID: 3, BaseCommit: testCommitA, HeadCommit: testCommitB,
			Status: domain.RunDone, Model: analysisTestModel, PromptVersion: analysisTestPrompt,
			AnalysisVersion: 3,
		}
		f.analyses.reusable = &done

		analysis, reused, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if err != nil {
			t.Fatalf("Start error = %v", err)
		}
		if !reused || analysis.ID != 900 || analysis.AnalysisVersion != 3 {
			t.Fatalf("Start = %+v reused=%v, want the existing done analysis", analysis, reused)
		}
		if len(f.analyses.findCalls) != 1 {
			t.Fatalf("FindReusable calls = %d, want 1", len(f.analyses.findCalls))
		}
		want := analysisFindReusableCall{
			versionID: 11, repoID: 3, base: testCommitA, head: testCommitB,
			model: analysisTestModel, prompt: analysisTestPrompt,
		}
		if !reflect.DeepEqual(f.analyses.findCalls[0], want) {
			t.Fatalf("FindReusable args = %+v, want %+v", f.analyses.findCalls[0], want)
		}
		if len(f.analyses.creates) != 0 || len(f.queue.jobs) != 0 || f.analyses.nextCalls != 0 {
			t.Fatal("reuse must not create, enqueue or allocate a new analysis version")
		}
	})

	t.Run("force creates the next analysis version", func(t *testing.T) {
		f := newAnalysisFixture(t)
		done := domain.CodeAnalysis{ID: 900, VersionID: 11, Status: domain.RunDone, AnalysisVersion: 1}
		f.analyses.reusable = &done
		f.analyses.next = 2

		analysis, reused, err := f.svc.Start(context.Background(), 11, 42, "", true)
		if err != nil {
			t.Fatalf("Start error = %v", err)
		}
		if reused {
			t.Fatal("Start reused = true, want a forced new analysis")
		}
		if analysis.AnalysisVersion != 2 || analysis.ID == 900 {
			t.Fatalf("forced analysis = %+v, want analysisVersion 2 and a new id", analysis)
		}
		if len(f.analyses.findCalls) != 0 {
			t.Fatal("force must skip the reuse lookup")
		}
		if f.analyses.nextCalls != 1 {
			t.Fatalf("NextAnalysisVersion calls = %d, want 1", f.analyses.nextCalls)
		}
	})

	t.Run("active analysis conflicts", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.analyses.active = true

		_, _, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if !errors.Is(err, domain.ErrConflict) || domain.Reason(err) != "analysis_in_progress" {
			t.Fatalf("Start error = %v, want analysis_in_progress", err)
		}
		if len(f.analyses.creates) != 0 || len(f.queue.jobs) != 0 {
			t.Fatal("conflicting Start must not create or enqueue")
		}
	})

	t.Run("missing LLM stores an empty model", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.llm = nil
		f.rebuild()

		analysis, _, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if err != nil {
			t.Fatalf("Start error = %v", err)
		}
		if analysis.Model != "" {
			t.Fatalf("model = %q, want the empty degraded model", analysis.Model)
		}
	})

	t.Run("queue full marks the analysis failed", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.queue.err = ErrQueueFull

		analysis, reused, err := f.svc.Start(context.Background(), 11, 42, "", false)
		if err == nil {
			t.Fatal("Start error = nil, want queue-full error")
		}
		if !errors.Is(err, ErrQueueFull) || domain.Reason(err) != "analysis_queue_full" {
			t.Fatalf("Start error = %v, want ErrQueueFull and analysis_queue_full", err)
		}
		if reused {
			t.Fatal("queue-full Start reused = true")
		}
		if analysis.Status != domain.RunFailed || analysis.FinishedAt == nil {
			t.Fatalf("returned analysis = %+v, want failed with finished_at", analysis)
		}
		if len(analysis.Errors) != 1 || analysis.Errors[0].Code != "analysis_queue_full" {
			t.Fatalf("analysis errors = %+v, want analysis_queue_full", analysis.Errors)
		}
		stored, ok := f.analyses.stored(analysis.ID)
		if !ok || stored.Status != domain.RunFailed {
			t.Fatalf("stored analysis = %+v (ok=%v), want failed", stored, ok)
		}
		// The failed analysis is terminal: a later trigger is allowed again.
		f.queue.err = nil
		if _, _, err := f.svc.Start(context.Background(), 11, 42, "", false); err != nil {
			t.Fatalf("Start after queue-full failure error = %v, want nil", err)
		}
	})

	t.Run("version lookup failure propagates", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.versions.err = errors.New("version catalog down")

		if _, _, err := f.svc.Start(context.Background(), 11, 42, "", false); err == nil {
			t.Fatal("Start error = nil, want the version lookup error")
		}
	})

	t.Run("analysis create failure propagates", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.analyses.createErr = errors.New("db down")

		if _, _, err := f.svc.Start(context.Background(), 11, 42, "", false); err == nil {
			t.Fatal("Start error = nil, want the create error")
		}
		if len(f.queue.jobs) != 0 {
			t.Fatal("a failed create must not enqueue")
		}
	})
}

// assertNoCodeContentInAudit guarantees audit details stay on the identifier /
// status allowlist and never carry analysed code.
func assertNoCodeContentInAudit(t *testing.T, calls []auditCall) {
	t.Helper()
	allowed := map[string]struct{}{
		"repoId": {}, "versionId": {}, "commit": {}, "baseCommit": {},
		"headCommit": {}, "path": {}, "status": {}, "analysisId": {},
	}
	for _, call := range calls {
		for key := range call.detail {
			if _, ok := allowed[key]; !ok {
				t.Fatalf("audit detail key %q is not on the identifier allowlist: %+v", key, call.detail)
			}
		}
		encoded, _ := json.Marshal(call.detail)
		for _, marker := range []string{"post('/api/refund'", "package server", "func Refund"} {
			if strings.Contains(string(encoded), marker) {
				t.Fatalf("audit detail leaked code content: %s", encoded)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Run tests.
// ---------------------------------------------------------------------------

// TestRunCacheHitSkipsLLM pins the file-level cache of design spec §6.4: an
// exact six-part key hit materialises the cached facts without calling the LLM
// and without rewriting the cache row.
func TestRunCacheHitSkipsLLM(t *testing.T) {
	f := newAnalysisFixture(t)
	changed := analysisChangedFile(1, "web/src/api/refund.ts", "M")
	f.changeSet(changed)
	evidence := analysisEvidenceFor(t, 0, "web/src/api/refund.ts", 2, 3)
	cachedFacts := fmt.Sprintf(`{
		"changeSummary": "cached refund client analysis",
		"facts": [{
			"kind": "client_call",
			"name": "POST /api/refund",
			"path": "web/src/api/refund.ts",
			"startLine": 1,
			"endLine": 3,
			"signature": "post('/api/refund')",
			"confidence": "high",
			"summary": "cached client fact",
			"evidence": [{"file": %q, "startLine": 2, "endLine": 3, "excerptHash": %q}]
		}],
		"edges": []
	}`, evidence.File, evidence.ExcerptHash)
	f.cache.seed(domain.CachedFile{
		ID: 41, RepoID: 3, Commit: testCommitB, Path: "web/src/api/refund.ts",
		FileHash: changed.HeadBlobHash, Model: analysisTestModel, PromptVersion: analysisTestPrompt,
		Facts: []byte(cachedFacts), Stats: []byte(`{"facts":1}`),
	})

	analysis := f.runNewAnalysis(t)
	if f.llm.callCount() != 0 {
		t.Fatalf("LLM calls = %d, want 0 on a cache hit", f.llm.callCount())
	}
	if f.cache.upsertCount() != 0 {
		t.Fatalf("cache Upsert calls = %d, want 0 on a cache hit", f.cache.upsertCount())
	}
	if len(f.cache.gets) != 1 {
		t.Fatalf("cache Get calls = %d, want 1", len(f.cache.gets))
	}
	get := f.cache.gets[0]
	if get.repoID != 3 || get.commit != testCommitB || get.path != "web/src/api/refund.ts" ||
		get.hash != changed.HeadBlobHash || get.model != analysisTestModel || get.prompt != analysisTestPrompt {
		t.Fatalf("cache Get args = %+v, want the six-part key", get)
	}
	if analysis.Status != domain.RunDone || analysis.Degraded {
		t.Fatalf("analysis = status %q degraded %v, want done and not degraded", analysis.Status, analysis.Degraded)
	}
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "cacheHits"); got != 1 {
		t.Fatalf("stats.cacheHits = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "analyzedFiles"); got != 1 {
		t.Fatalf("stats.analyzedFiles = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "facts"); got != 1 {
		t.Fatalf("stats.facts = %d, want 1", got)
	}

	entity, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatalf("materialised entities = %+v, want the cached fact", f.entities.seededFacts())
	}
	if entity.Source != domain.SourceAI || entity.FactVersion == nil || *entity.FactVersion != 1 {
		t.Fatalf("entity provenance = %s/%v, want ai/version 1", entity.Source, entity.FactVersion)
	}
	if len(entity.Evidence) != 1 || entity.Evidence[0].ExcerptHash != evidence.ExcerptHash {
		t.Fatalf("entity evidence = %+v, want the cached verified evidence", entity.Evidence)
	}
	if len(f.planner.calls) != 1 {
		t.Fatalf("Planner calls = %d, want 1", len(f.planner.calls))
	}
	if len(f.planner.calls[0].FactsAtHead) != 1 {
		t.Fatalf("planner facts = %+v, want the materialised fact", f.planner.calls[0].FactsAtHead)
	}
}

// TestRunMaterialisesFactsAndVersions pins design spec §6.4 fact versioning:
// a new fact creates version 1, an unchanged body reuses it and a changed body
// bumps the version and supersedes the previous one.
func TestRunMaterialisesFactsAndVersions(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()

	first := f.runNewAnalysis(t)
	if len(f.facts.creates) != 1 {
		t.Fatalf("fact Create calls = %d, want 1", len(f.facts.creates))
	}
	created := f.facts.creates[0]
	if created.id == 0 || created.version != 1 || created.repoID != 3 || created.commit != testCommitB {
		t.Fatalf("created fact version = %+v, want id, repo 3, commit %s, version 1", created, testCommitB)
	}
	if created.factKey != "client:POST /api/refund" {
		t.Fatalf("factKey = %q, want client:POST /api/refund", created.factKey)
	}
	if created.model != analysisTestModel || created.prompt != analysisTestPrompt || created.analysisID != first.ID {
		t.Fatalf("created version provenance = %+v, want model/prompt/analysis", created)
	}
	if !strings.Contains(string(created.body), `"summary":"client calls the refund API"`) {
		t.Fatalf("fact body = %s, want the validated fact body", created.body)
	}

	entity, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("fact entity missing after Run")
	}
	if entity.ID == 0 || entity.Source != domain.SourceAI || entity.Confidence != domain.ConfidenceHigh {
		t.Fatalf("entity = %+v, want snowflake ai/high", entity)
	}
	if entity.FactVersion == nil || *entity.FactVersion != 1 {
		t.Fatalf("entity factVersion = %v, want 1", entity.FactVersion)
	}
	if len(entity.Evidence) != 1 || !strings.HasPrefix(entity.Evidence[0].ExcerptHash, "sha256:") {
		t.Fatalf("entity evidence = %+v, want one verified excerpt hash", entity.Evidence)
	}
	if len(first.FactVersions) != 1 || first.FactVersions[0].FactKey != "client:POST /api/refund" || first.FactVersions[0].Version != 1 {
		t.Fatalf("fact version snapshot = %+v, want the created version", first.FactVersions)
	}

	// Same body: reuse the current version, do not insert/supersede/rewrite.
	second := f.runNewAnalysis(t)
	if len(f.facts.creates) != 1 {
		t.Fatalf("fact Create calls after same body = %d, want 1", len(f.facts.creates))
	}
	if len(f.facts.supersedes) != 0 {
		t.Fatalf("supersede calls = %+v, want none when the body is equal", f.facts.supersedes)
	}
	if got := len(f.entities.upserts); got != 1 {
		t.Fatalf("entity upserts after same body = %d, want 1 (reuse)", got)
	}
	if len(second.FactVersions) != 1 || second.FactVersions[0].Version != 1 {
		t.Fatalf("second fact version snapshot = %+v, want version 1", second.FactVersions)
	}

	// Changed body: bump to version 2, supersede the old version and update the
	// current entity. The re-run is forced so the file cache (which still holds
	// the old body) is skipped and the new model output is analysed.
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponseWithFactSummary("refund now forwards a reason")
	forcedAnalysis := f.startAnalysis(t, true)
	f.runAnalysis(t, forcedAnalysis.ID)
	third := f.storedAnalysis(t, forcedAnalysis.ID)
	if len(f.facts.creates) != 2 {
		t.Fatalf("fact Create calls after body change = %d, want 2", len(f.facts.creates))
	}
	bumped := f.facts.creates[1]
	if bumped.version != 2 || bumped.id == 0 {
		t.Fatalf("bumped fact version = %+v, want a new version 2 row", bumped)
	}
	if len(f.facts.supersedes) != 1 {
		t.Fatalf("supersede calls = %+v, want one", f.facts.supersedes)
	}
	supersede := f.facts.supersedes[0]
	if supersede.factKey != "client:POST /api/refund" || supersede.before != 2 || !supersede.at.Equal(f.clock.now) {
		t.Fatalf("supersede = %+v, want before 2 at the clock time", supersede)
	}
	if got := len(f.entities.upserts); got != 2 {
		t.Fatalf("entity upserts after body change = %d, want 2", got)
	}
	updated, _ := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if updated.FactVersion == nil || *updated.FactVersion != 2 {
		t.Fatalf("updated entity factVersion = %v, want 2", updated.FactVersion)
	}
	if len(third.FactVersions) != 1 || third.FactVersions[0].Version != 2 {
		t.Fatalf("third fact version snapshot = %+v, want version 2", third.FactVersions)
	}
}

// TestRunEvidenceFailureDowngradesToLowClue pins design spec §6.2/§6.3: invalid
// or missing evidence forces the fact to a low clue and never fails the run.
func TestRunEvidenceFailureDowngradesToLowClue(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	payload := map[string]any{
		"changeSummary": "refund client analysis",
		"facts": []any{
			map[string]any{
				"kind": "client_call", "name": "POST /api/refund",
				"path": "web/src/api/refund.ts", "startLine": 1, "endLine": 3,
				"confidence": "high", "summary": "out of range evidence",
				"evidence": []any{map[string]any{
					"file": "web/src/api/refund.ts", "startLine": 99, "endLine": 100,
				}},
			},
			map[string]any{
				"kind": "route", "name": "/orders",
				"path": "web/src/api/refund.ts", "startLine": 1, "endLine": 1,
				"confidence": "high", "summary": "no evidence",
				"evidence": []any{},
			},
		},
		"edges": []any{},
	}
	raw, _ := json.Marshal(payload)
	f.llm.responses["web/src/api/refund.ts"] = string(raw)

	analysis := f.runNewAnalysis(t)
	if analysis.Degraded {
		t.Fatalf("analysis degraded = true, want evidence failures to stay non-degraded")
	}
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "evidenceChecks"); got != 1 {
		t.Fatalf("stats.evidenceChecks = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "evidenceFailures"); got != 1 {
		t.Fatalf("stats.evidenceFailures = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "facts"); got != 2 {
		t.Fatalf("stats.facts = %d, want 2 (low clues are kept)", got)
	}

	clientEntity, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("client fact missing")
	}
	if clientEntity.Confidence != domain.ConfidenceLow {
		t.Fatalf("client confidence = %q, want low", clientEntity.Confidence)
	}
	if !strings.Contains(string(clientEntity.Meta), `"clue":true`) {
		t.Fatalf("client meta = %s, want clue=true", clientEntity.Meta)
	}
	if len(clientEntity.Evidence) != 0 {
		t.Fatalf("client evidence = %+v, want invalid evidence dropped", clientEntity.Evidence)
	}
	routeEntity, ok := f.entities.fact(3, testCommitB, "route:/orders")
	if !ok || routeEntity.Confidence != domain.ConfidenceLow {
		t.Fatalf("route entity = %+v (ok=%v), want a low clue", routeEntity, ok)
	}
	if !strings.Contains(string(routeEntity.Meta), `"clue":true`) {
		t.Fatalf("route meta = %s, want clue=true", routeEntity.Meta)
	}

	if len(f.planner.calls) != 1 {
		t.Fatalf("Planner calls = %d, want 1", len(f.planner.calls))
	}
	if f.planner.calls[0].Degraded {
		t.Fatal("planner Degraded = true, want false for evidence-only degradation")
	}
	for _, fact := range f.planner.calls[0].FactsAtHead {
		if fact.Confidence != domain.ConfidenceLow {
			t.Fatalf("planner fact confidence = %q, want low", fact.Confidence)
		}
	}
}

// TestRunPartialFailureDegraded pins design spec §6.6: one failed file keeps the
// successful facts, records the file-level error and degrades the run.
func TestRunPartialFailureDegraded(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(
		analysisChangedFile(1, "web/src/api/refund.ts", "M"),
		analysisChangedFile(2, "web/src/utils.ts", "M"),
	)
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()
	f.llm.errs["web/src/utils.ts"] = errors.New("upstream 500")

	analysis := f.runNewAnalysis(t)
	if analysis.Status != domain.RunDone || !analysis.Degraded {
		t.Fatalf("analysis = status %q degraded %v, want done and degraded", analysis.Status, analysis.Degraded)
	}
	if len(analysis.Errors) != 1 {
		t.Fatalf("analysis errors = %+v, want one file-level entry", analysis.Errors)
	}
	if analysis.Errors[0].Code != "llm_error" || analysis.Errors[0].Path != "web/src/utils.ts" {
		t.Fatalf("analysis error = %+v, want llm_error for utils.ts", analysis.Errors[0])
	}
	if _, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund"); !ok {
		t.Fatal("successful file facts were not materialised")
	}
	if f.cache.upsertCount() != 1 {
		t.Fatalf("cache Upsert calls = %d, want only the successful file", f.cache.upsertCount())
	}
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "analyzedFiles"); got != 1 {
		t.Fatalf("stats.analyzedFiles = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "failedFiles"); got != 1 {
		t.Fatalf("stats.failedFiles = %d, want 1", got)
	}
	if len(f.planner.calls) != 1 || !f.planner.calls[0].Degraded {
		t.Fatalf("planner calls = %+v, want one degraded call", f.planner.calls)
	}
}

// TestRunAllLLMFailureDegradedT0Only pins the design spec §6.6 degraded path: an
// unavailable or failing LLM still completes the run as done+degraded and lets
// the planner produce T0-only results.
func TestRunAllLLMFailureDegradedT0Only(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(f *analysisFixture)
		wantErr string
	}{
		{
			name: "all calls fail",
			setup: func(f *analysisFixture) {
				f.llm.errs["web/src/api/refund.ts"] = errors.New("llm upstream down")
			},
			wantErr: "llm_error",
		},
		{
			name: "llm not configured",
			setup: func(f *analysisFixture) {
				f.llm.errs["web/src/api/refund.ts"] = ErrLLMNotConfigured
			},
			wantErr: "llm_unavailable",
		},
		{
			name: "nil llm",
			setup: func(f *analysisFixture) {
				f.llm = nil
				f.rebuild()
			},
			wantErr: "llm_unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAnalysisFixture(t)
			f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
			f.planner.result = ImpactResult{
				Candidates: []domain.ImpactCandidate{{
					SubjectKind: domain.CandidateRequirement,
					SubjectID:   77,
					Reason:      "changed file web/src/api/refund.ts",
					Confidence:  domain.ConfidenceHigh,
					Source:      domain.CandidateDirect,
					Evidence:    []domain.Evidence{{File: "web/src/api/refund.ts"}},
				}},
				Unmapped: []domain.UnmappedChange{{
					Path: "web/src/api/refund.ts", Status: "M", Reason: domain.UnmappedAIUnavailable,
				}},
			}
			tt.setup(f)

			analysis := f.runNewAnalysis(t)
			if analysis.Status != domain.RunDone || !analysis.Degraded {
				t.Fatalf("analysis = status %q degraded %v, want done and degraded", analysis.Status, analysis.Degraded)
			}
			if !ciHasErrorCode(analysis.Errors, tt.wantErr) {
				t.Fatalf("analysis errors = %+v, want code %q", analysis.Errors, tt.wantErr)
			}
			if len(f.entities.upserts) != 0 {
				t.Fatalf("entity upserts = %+v, want no AI facts on full LLM failure", f.entities.upserts)
			}
			if len(f.planner.calls) != 1 || !f.planner.calls[0].Degraded {
				t.Fatalf("planner calls = %+v, want one degraded call", f.planner.calls)
			}
			items, ok := f.candidates.replacedItems(analysis.ID)
			if !ok || len(items) != 1 || items[0].SubjectID != 77 {
				t.Fatalf("persisted candidates = %+v, want the T0 candidate", items)
			}
			if len(analysis.Unmapped) != 1 {
				t.Fatalf("analysis unmapped = %+v, want the planner result", analysis.Unmapped)
			}
		})
	}
}

// TestRunEmptyChangeSet finishes without calling the LLM or the planner.
func TestRunEmptyChangeSet(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet()
	f.planner.result = ImpactResult{
		Candidates: []domain.ImpactCandidate{{SubjectKind: domain.CandidateRequirement, SubjectID: 77}},
	}
	f.llm.defaultResponse = clientFactResponse()

	analysis := f.runNewAnalysis(t)
	if analysis.Status != domain.RunDone || analysis.Degraded {
		t.Fatalf("analysis = status %q degraded %v, want done and not degraded", analysis.Status, analysis.Degraded)
	}
	if f.llm.callCount() != 0 {
		t.Fatalf("LLM calls = %d, want 0 for an empty change set", f.llm.callCount())
	}
	if len(f.planner.calls) != 0 {
		t.Fatalf("Planner calls = %d, want 0 for an empty change set", len(f.planner.calls))
	}
	items, ok := f.candidates.replacedItems(analysis.ID)
	if !ok || len(items) != 0 {
		t.Fatalf("candidates = %+v, want an empty persisted set", items)
	}
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "changedFiles"); got != 0 {
		t.Fatalf("stats.changedFiles = %d, want 0", got)
	}
	if len(analysis.FactVersions) != 0 || len(analysis.Unmapped) != 0 {
		t.Fatalf("analysis arrays = %+v, want empty", analysis)
	}
	if len(f.notifier.changed) != 1 || len(f.notifier.changed[0].Paths) != 0 {
		t.Fatalf("code.changed events = %+v, want one with no paths", f.notifier.changed)
	}
	if len(f.notifier.impacts) != 1 {
		t.Fatalf("code.impact_computed events = %d, want 1", len(f.notifier.impacts))
	}
	if f.notifier.impacts[0].Counts["candidates"] != 0 || f.notifier.impacts[0].Counts["unmapped"] != 0 {
		t.Fatalf("impact counts = %+v, want zero", f.notifier.impacts[0].Counts)
	}
}

// TestRunContractEdges pins design spec §6.5: matched client_call/api facts
// become route_to_api edges owned by an analysed fact, with merged evidence.
func TestRunContractEdges(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(
		analysisChangedFile(1, "web/src/api/refund.ts", "M"),
		analysisChangedFile(2, "server/refund.go", "M"),
	)
	f.configureFileAnalyses()

	analysis := f.runNewAnalysis(t)
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "contracts"); got != 1 {
		t.Fatalf("stats.contracts = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "unmatchedContracts"); got != 0 {
		t.Fatalf("stats.unmatchedContracts = %d, want 0", got)
	}

	clientEntity, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("client fact missing")
	}
	apiEntity, ok := f.entities.fact(3, testCommitB, "api:POST /api/refund")
	if !ok {
		t.Fatal("api fact missing")
	}

	replaces := f.edges.replacesFor("client:POST /api/refund")
	if len(replaces) != 1 {
		t.Fatalf("client edge replaces = %+v, want one", replaces)
	}
	var contract *domain.CodeEdge
	for index := range replaces[0].edges {
		if replaces[0].edges[index].Rel == domain.RelRouteToAPI {
			contract = &replaces[0].edges[index]
		}
	}
	if contract == nil {
		t.Fatalf("client edges = %+v, want one route_to_api edge", replaces[0].edges)
	}
	if contract.ID == 0 || contract.Source != domain.SourceAI {
		t.Fatalf("contract edge = %+v, want a snowflake ai edge", contract)
	}
	if contract.SrcEntityID != clientEntity.ID || contract.DstEntityID != apiEntity.ID {
		t.Fatalf("contract endpoints = %d/%d, want client %d / api %d",
			contract.SrcEntityID, contract.DstEntityID, clientEntity.ID, apiEntity.ID)
	}
	if contract.Confidence != domain.ConfidenceHigh {
		t.Fatalf("contract confidence = %q, want high", contract.Confidence)
	}
	if len(contract.Evidence) != 2 {
		t.Fatalf("contract evidence = %+v, want both sides merged", contract.Evidence)
	}
	var meta map[string]any
	if err := json.Unmarshal(contract.Meta, &meta); err != nil {
		t.Fatalf("decode edge meta %s: %v", contract.Meta, err)
	}
	if meta["method"] != "POST" || meta["normalizedPath"] != "/api/refund" || meta["match"] != "exact" {
		t.Fatalf("edge meta = %+v, want method/path/match", meta)
	}
	if contract.FactKey == nil || *contract.FactKey != "client:POST /api/refund" {
		t.Fatalf("contract edge owner = %v, want the client fact", contract.FactKey)
	}

	if len(replacesForFact(f.edges, "api:POST /api/refund")) != 1 {
		t.Fatalf("api fact edges = %+v, want one (empty) replacement", f.edges.replaces)
	}
	if len(f.edges.allEdges()) != 1 {
		t.Fatalf("materialised edges = %+v, want exactly the contract edge", f.edges.allEdges())
	}
	if len(f.planner.calls) != 1 || len(f.planner.calls[0].Edges) != 1 {
		t.Fatalf("planner edges = %+v, want the route_to_api edge", f.planner.calls)
	}
}

// replacesForFact is a small helper over the fake edge repository.
func replacesForFact(edges *analysisEdges, factKey string) []analysisEdgeReplace {
	return edges.replacesFor(factKey)
}

// TestRunCountsUnmatchedContracts pins the unmatched-contract counter when a
// client_call fact has no api counterpart in the head commit.
func TestRunCountsUnmatchedContracts(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()

	analysis := f.runNewAnalysis(t)
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "contracts"); got != 0 {
		t.Fatalf("stats.contracts = %d, want 0", got)
	}
	if got := analysisStatInt(t, stats, "unmatchedContracts"); got != 1 {
		t.Fatalf("stats.unmatchedContracts = %d, want 1", got)
	}
}

// TestRunReusesSemanticallyEqualFactBody proves the fact body comparison is
// semantic (design spec §6.4): a stored body with different key order and
// whitespace still reuses the current version.
func TestRunReusesSemanticallyEqualFactBody(t *testing.T) {
	source := newAnalysisFixture(t)
	source.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	source.llm.responses["web/src/api/refund.ts"] = clientFactResponse()
	source.runNewAnalysis(t)
	if len(source.facts.creates) != 1 {
		t.Fatalf("source fact Create calls = %d, want 1", len(source.facts.creates))
	}
	var document map[string]any
	if err := json.Unmarshal(source.facts.creates[0].body, &document); err != nil {
		t.Fatalf("decode source fact body: %v", err)
	}
	reordered, err := json.MarshalIndent(document, "", "    ")
	if err != nil {
		t.Fatalf("re-encode fact body: %v", err)
	}

	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()
	key := "client:POST /api/refund"
	version := 1
	entity := domain.CodeEntity{
		ID: 5000, RepoID: 3, Commit: testCommitB, Kind: domain.KindClientCall,
		Name: "POST /api/refund", Path: "web/src/api/refund.ts", Source: domain.SourceAI,
		Confidence: domain.ConfidenceHigh, FactKey: &key, FactVersion: &version,
	}
	f.entities.facts[analysisEntityKey(3, testCommitB, key)] = entity
	f.facts.seed(entity, reordered)

	analysis := f.runNewAnalysis(t)
	if len(f.facts.creates) != 0 {
		t.Fatalf("fact Create calls = %+v, want 0 for a semantically equal body", f.facts.creates)
	}
	if len(f.facts.supersedes) != 0 {
		t.Fatalf("supersede calls = %+v, want none", f.facts.supersedes)
	}
	if len(f.entities.upserts) != 0 {
		t.Fatalf("entity upserts = %+v, want none for a reused version", f.entities.upserts)
	}
	if len(analysis.FactVersions) != 1 || analysis.FactVersions[0].Version != 1 {
		t.Fatalf("fact version snapshot = %+v, want version 1", analysis.FactVersions)
	}
}

// TestRunMergesDuplicateFactsAcrossFiles pins the same-run duplicate merge of
// design spec §6.4: one fact/version is produced, evidence is merged and the
// duplicate is counted.
func TestRunMergesDuplicateFactsAcrossFiles(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(
		analysisChangedFile(1, "web/src/api/refund.ts", "M"),
		analysisChangedFile(2, "server/refund.go", "M"),
	)
	routeResponse := func(factPath string, evidenceLine int) string {
		payload := map[string]any{
			"changeSummary": "route reference in " + factPath,
			"facts": []any{map[string]any{
				"kind": "route", "name": "/orders",
				"path": factPath, "startLine": 1, "endLine": 1,
				"confidence": "high", "summary": "orders route",
				"evidence": []any{map[string]any{
					"file": factPath, "startLine": evidenceLine, "endLine": evidenceLine,
				}},
			}},
			"edges": []any{},
		}
		raw, _ := json.Marshal(payload)
		return string(raw)
	}
	f.llm.responses["web/src/api/refund.ts"] = routeResponse("web/src/api/refund.ts", 1)
	f.llm.responses["server/refund.go"] = routeResponse("server/refund.go", 1)

	analysis := f.runNewAnalysis(t)
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "facts"); got != 1 {
		t.Fatalf("stats.facts = %d, want 1 merged fact", got)
	}
	if got := analysisStatInt(t, stats, "duplicateFacts"); got != 1 {
		t.Fatalf("stats.duplicateFacts = %d, want 1", got)
	}
	if len(f.entities.upserts) != 1 {
		t.Fatalf("entity upserts = %d, want one merged fact", len(f.entities.upserts))
	}
	entity := f.entities.upserts[0]
	if entity.FactKey == nil || *entity.FactKey != "route:/orders" {
		t.Fatalf("merged entity factKey = %v, want route:/orders", entity.FactKey)
	}
	if len(entity.Evidence) != 2 {
		t.Fatalf("merged evidence = %+v, want both files", entity.Evidence)
	}
	if len(f.facts.creates) != 1 || f.facts.creates[0].version != 1 {
		t.Fatalf("fact versions = %+v, want exactly one version 1", f.facts.creates)
	}
}

// TestRunTimeoutMarksFailed pins design spec §6.8: an expired worker deadline is
// a failed run (analysis_timeout), never a degraded success.
func TestRunTimeoutMarksFailed(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.errs["web/src/api/refund.ts"] = context.DeadlineExceeded

	analysis := f.startAnalysis(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	if err := f.svc.Run(ctx, analysis.ID); err == nil {
		t.Fatal("Run error = nil, want the timeout failure")
	}
	stored := f.storedAnalysis(t, analysis.ID)
	if stored.Status != domain.RunFailed {
		t.Fatalf("stored status = %q, want failed", stored.Status)
	}
	if len(stored.Errors) == 0 || stored.Errors[0].Code != "analysis_timeout" {
		t.Fatalf("analysis errors = %+v, want analysis_timeout", stored.Errors)
	}
}

// TestRunNormalisesEvidencePaths pins that a model-supplied "./" path is
// normalised to the repository-relative form before validation and storage.
func TestRunNormalisesEvidencePaths(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	payload := map[string]any{
		"changeSummary": "refund client analysis",
		"facts": []any{map[string]any{
			"kind": "client_call", "name": "POST /api/refund",
			"path": "./web/src/api/refund.ts", "startLine": 1, "endLine": 3,
			"confidence": "high", "summary": "client fact",
			"evidence": []any{map[string]any{
				"file": "./web/src/api/refund.ts", "startLine": 2, "endLine": 3,
			}},
		}},
		"edges": []any{},
	}
	raw, _ := json.Marshal(payload)
	f.llm.responses["web/src/api/refund.ts"] = string(raw)

	analysis := f.runNewAnalysis(t)
	if analysis.Degraded {
		t.Fatal("analysis degraded = true, want the normalised evidence to validate")
	}
	entity, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("normalised fact entity missing")
	}
	if entity.Path != "web/src/api/refund.ts" {
		t.Fatalf("entity path = %q, want the normalised path", entity.Path)
	}
	if len(entity.Evidence) != 1 || entity.Evidence[0].File != "web/src/api/refund.ts" {
		t.Fatalf("entity evidence = %+v, want the normalised evidence path", entity.Evidence)
	}
	if entity.Evidence[0].ExcerptHash == "" {
		t.Fatal("normalised evidence has no verified excerpt hash")
	}
}

// TestRunPersistsCandidatesAndSelection pins the persistence handoff of the
// planner result: snowflake ids, default selection, unmapped/stats snapshots,
// events and the code.analyze audit entry.
func TestRunPersistsCandidatesAndSelection(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()
	f.planner.result = ImpactResult{
		Candidates: []domain.ImpactCandidate{
			{
				ID: 999, SubjectKind: domain.CandidateRequirement, SubjectID: 77,
				Reason: "changed client_call POST /api/refund", Confidence: domain.ConfidenceHigh,
				Source:   domain.CandidateDirect,
				Evidence: []domain.Evidence{analysisEvidenceFor(t, 0, "web/src/api/refund.ts", 2, 3)},
				Selected: false,
			},
			{
				SubjectKind: domain.CandidateAppModel, SubjectID: 88,
				Reason: "low confidence app model link", Confidence: domain.ConfidenceLow,
				Source: domain.CandidateAI, Selected: true,
			},
		},
		Unmapped: []domain.UnmappedChange{{
			Path: "web/src/utils.ts", Status: "M", Reason: domain.UnmappedNoFact, Suggestion: "人工确认影响",
		}},
		Stats: []byte(`{"requirements":1,"appModels":1}`),
	}

	analysis := f.runNewAnalysis(t)
	if analysis.Status != domain.RunDone || analysis.Degraded {
		t.Fatalf("analysis = status %q degraded %v, want done", analysis.Status, analysis.Degraded)
	}
	items, ok := f.candidates.replacedItems(analysis.ID)
	if !ok || len(items) != 2 {
		t.Fatalf("candidate Replace = %+v (ok=%v), want 2 candidates", items, ok)
	}
	if items[0].ID == 0 || items[1].ID == 0 || items[0].ID == items[1].ID || items[0].ID == 999 {
		t.Fatalf("candidate ids = %d/%d, want fresh distinct snowflake ids", items[0].ID, items[1].ID)
	}
	if items[0].AnalysisID != analysis.ID || items[1].AnalysisID != analysis.ID {
		t.Fatalf("candidate analysis ids = %d/%d, want %d", items[0].AnalysisID, items[1].AnalysisID, analysis.ID)
	}
	if !items[0].Selected {
		t.Fatal("high-confidence candidate selected = false, want the default true")
	}
	if items[1].Selected {
		t.Fatal("low-confidence candidate selected = true, want the default false")
	}
	if items[0].Reason != "changed client_call POST /api/refund" {
		t.Fatalf("candidate reason = %q, want preserved", items[0].Reason)
	}

	if len(analysis.Unmapped) != 1 || analysis.Unmapped[0].Reason != domain.UnmappedNoFact {
		t.Fatalf("analysis unmapped = %+v, want the planner result", analysis.Unmapped)
	}
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "changedFiles"); got != 1 {
		t.Fatalf("stats.changedFiles = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "requirements"); got != 1 {
		t.Fatalf("merged stats.requirements = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "unmapped"); got != 1 {
		t.Fatalf("stats.unmapped = %d, want 1", got)
	}
	if summary, _ := stats["changeSummary"].(string); !strings.Contains(summary, "refund client call") {
		t.Fatalf("stats.changeSummary = %q, want the aggregated file summary", summary)
	}

	if len(f.notifier.impacts) != 1 {
		t.Fatalf("impact events = %d, want 1", len(f.notifier.impacts))
	}
	impact := f.notifier.impacts[0]
	if impact.AnalysisID != analysis.ID || impact.Status != domain.RunDone || impact.Degraded {
		t.Fatalf("impact event = %+v, want the done analysis", impact)
	}
	if impact.Counts["requirements"] != 1 || impact.Counts["appModels"] != 1 ||
		impact.Counts["unmapped"] != 1 || impact.Counts["candidates"] != 2 {
		t.Fatalf("impact counts = %+v, want 1/1/1/2", impact.Counts)
	}
	if len(impact.Paths) != 1 || impact.Paths[0] != "web/src/api/refund.ts" {
		t.Fatalf("impact paths = %v, want the candidate evidence path", impact.Paths)
	}

	if len(f.audit.calls) != 2 {
		t.Fatalf("audit calls = %+v, want one trigger and one completion", f.audit.calls)
	}
	completion := f.audit.calls[len(f.audit.calls)-1]
	if completion.action != "code.analyze" || completion.detail["status"] != domain.RunDone {
		t.Fatalf("completion audit = %+v, want code.analyze done", completion)
	}
	assertNoCodeContentInAudit(t, f.audit.calls)
}

// TestRunPlannerReceivesCrossFileFacts pins that a fact whose own path is not a
// changed file (a valid AI attribution) still reaches the planner as "facts at
// head": the derivation input is the fact set this run materialised, not only
// the rows addressable by changed path.
func TestRunPlannerReceivesCrossFileFacts(t *testing.T) {
	f := newAnalysisFixture(t)
	f.git.files["web/src/router.ts"] = "export const routes = ['/orders'];\n"
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	payload := map[string]any{
		"changeSummary": "refund client analysis mentions a route",
		"facts": []any{map[string]any{
			"kind": "route", "name": "/orders",
			"path": "web/src/router.ts", "startLine": 1, "endLine": 1,
			"confidence": "high", "summary": "orders route",
			"evidence": []any{map[string]any{
				"file": "web/src/router.ts", "startLine": 1, "endLine": 1,
			}},
		}},
		"edges": []any{},
	}
	raw, _ := json.Marshal(payload)
	f.llm.responses["web/src/api/refund.ts"] = string(raw)

	f.runNewAnalysis(t)
	if len(f.planner.calls) != 1 {
		t.Fatalf("Planner calls = %d, want 1", len(f.planner.calls))
	}
	found := false
	for _, fact := range f.planner.calls[0].FactsAtHead {
		if fact.FactKey != nil && *fact.FactKey == "route:/orders" {
			found = true
		}
	}
	if !found {
		t.Fatalf("planner facts = %+v, want the cross-file route fact", f.planner.calls[0].FactsAtHead)
	}
}

// TestRunPassesPlannerInput pins the ImpactInput handed to the planner.
func TestRunPassesPlannerInput(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()
	f.anchors.anchors = []domain.CodeAnchor{{
		ID: 31, VersionID: 11, RepoID: 3, SubjectKind: domain.SubjectRequirement, SubjectID: 77,
		FilePath: "web/src/api/refund.ts", Confidence: domain.ConfidenceHigh,
	}}

	analysis := f.runNewAnalysis(t)
	if len(f.planner.calls) != 1 {
		t.Fatalf("Planner calls = %d, want 1", len(f.planner.calls))
	}
	in := f.planner.calls[0]
	if in.Version.ID != 11 || in.Version.SystemID != 5 || !in.Version.HasRepo {
		t.Fatalf("planner version = %+v, want the loaded version binding", in.Version)
	}
	if in.Analysis.ID != analysis.ID {
		t.Fatalf("planner analysis = %d, want %d", in.Analysis.ID, analysis.ID)
	}
	if len(in.Requirements) != 2 || in.Requirements[0].ID != 77 {
		t.Fatalf("planner requirements = %+v, want the version requirements", in.Requirements)
	}
	if len(in.Relations) != 1 || in.Relations[0].FromID != 77 || in.Relations[0].ToID != 78 {
		t.Fatalf("planner relations = %+v, want the version relations", in.Relations)
	}
	if len(in.Anchors) != 1 || in.Anchors[0].ID != 31 {
		t.Fatalf("planner anchors = %+v, want the change-set anchors", in.Anchors)
	}
	if len(in.ChangedFiles) != 1 || in.ChangedFiles[0].Path != "web/src/api/refund.ts" {
		t.Fatalf("planner changed files = %+v, want the change set", in.ChangedFiles)
	}
	if len(in.FactsAtHead) != 1 || in.FactsAtHead[0].FactKey == nil ||
		*in.FactsAtHead[0].FactKey != "client:POST /api/refund" {
		t.Fatalf("planner facts = %+v, want the materialised fact", in.FactsAtHead)
	}
	if in.Degraded {
		t.Fatal("planner degraded = true, want false")
	}
}

// TestRunOrdersFilesByAnchorThenPath pins the deterministic file selection of
// design spec §6.2: anchored files first, then path ascending, capped by
// MaxChangedFiles.
func TestRunOrdersFilesByAnchorThenPath(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(
		analysisChangedFile(1, "web/src/api/refund.ts", "M"),
		analysisChangedFile(2, "server/refund.go", "M"),
		analysisChangedFile(3, "web/src/utils.ts", "M"),
	)
	f.anchors.anchors = []domain.CodeAnchor{{
		ID: 32, VersionID: 11, RepoID: 3, SubjectKind: domain.SubjectRequirement,
		SubjectID: 77, FilePath: "web/src/utils.ts", Confidence: domain.ConfidenceHigh,
	}}
	f.configureFileAnalyses()
	f.rebuild()
	// Restrict the analysis to one file: the anchored utils.ts must be kept and
	// the alphabetical first file (server/refund.go) still wins the second slot
	// only after the anchored one.
	f.svc.config.MaxChangedFiles = 2

	analysis := f.runNewAnalysis(t)
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "skipped"); got != 1 {
		t.Fatalf("stats.skipped = %d, want 1 capped file", got)
	}
	if analysis.HeadCommit != testCommitB {
		t.Fatalf("head = %s, want %s", analysis.HeadCommit, testCommitB)
	}
	if len(f.llm.users) != 2 {
		t.Fatalf("LLM calls = %d, want 2 capped files", len(f.llm.users))
	}
	analysed := analysisPromptFilePath(f.llm.users[0])
	if analysed != "web/src/utils.ts" {
		t.Fatalf("first analysed file = %q, want the anchored utils.ts", analysed)
	}
	second := analysisPromptFilePath(f.llm.users[1])
	if second != "server/refund.go" {
		t.Fatalf("second analysed file = %q, want the alphabetical server/refund.go", second)
	}
}

// TestRunSkipsUnsupportedFiles pins the deleted/binary/oversize exclusion of
// design spec §6.2 and that only supported files cost an LLM call.
func TestRunSkipsUnsupportedFiles(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(
		analysisChangedFile(1, "web/src/api/refund.ts", "M"),
		analysisChangedFile(2, "web/src/deleted.ts", "D"),
		analysisChangedFile(3, "web/src/image.ts", "M"),
	)
	f.git.diff.Files[2].Binary = true
	f.changeSetStats(t)
	f.truncatedGitFile("web/src/api/refund.ts", true)
	f.llm.defaultResponse = clientFactResponse()

	analysis := f.runNewAnalysis(t)
	if f.llm.callCount() != 0 {
		t.Fatalf("LLM calls = %d, want 0 when every file is unsupported", f.llm.callCount())
	}
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "analyzedFiles"); got != 0 {
		t.Fatalf("stats.analyzedFiles = %d, want 0", got)
	}
	if got := analysisStatInt(t, stats, "skipped"); got != 3 {
		t.Fatalf("stats.skipped = %d, want 3", got)
	}
	if analysis.Degraded {
		t.Fatal("unsupported content must not degrade the run")
	}
}

// truncatedGitFile marks one file content as cut at MaxFileBytes.
func (f *analysisFixture) truncatedGitFile(path string, truncated bool) {
	f.git.truncated[path] = truncated
}

// changeSetStats rewrites the diff stats after the test mutated the file list.
func (f *analysisFixture) changeSetStats(t *testing.T) {
	t.Helper()
	stats := map[string]any{
		"changedFiles": len(f.git.diff.Files),
		"additions":    len(f.git.diff.Files),
		"deletions":    0,
		"truncated":    false,
	}
	raw, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("encode change set stats: %v", err)
	}
	f.git.diff.Stats = raw
}

// TestRunUsesPersistedChangeSetAndPublishesEvent pins change-set reuse
// (ChangeSetID from the repository, not the freshly minted id) and the
// code.changed event payload.
func TestRunUsesPersistedChangeSetAndPublishesEvent(t *testing.T) {
	f := newAnalysisFixture(t)
	files := []domain.ChangedFile{
		analysisChangedFile(1, "web/src/api/refund.ts", "M"),
		analysisChangedFile(2, "server/refund.go", "M"),
	}
	stats := []byte(`{"changedFiles":2,"additions":5,"deletions":1,"truncated":false}`)
	f.git.diff = domain.ChangeSetData{Stats: stats, Files: files}
	f.changeSets.stored = domain.CodeChangeSet{
		ID: 555, RepoID: 3, BaseCommit: testCommitA, HeadCommit: testCommitB,
		Stats: stats, Files: files,
	}
	f.llm.defaultResponse = emptyFactResponse("no facts")

	analysis := f.runNewAnalysis(t)
	if analysis.ChangeSetID == nil || *analysis.ChangeSetID != 555 {
		t.Fatalf("analysis changeSetId = %v, want the persisted 555", analysis.ChangeSetID)
	}
	if len(f.changeSets.calls) != 1 {
		t.Fatalf("GetOrCreate calls = %d, want 1", len(f.changeSets.calls))
	}
	call := f.changeSets.calls[0]
	if call.ID == 0 || call.ID == 555 {
		t.Fatalf("GetOrCreate id = %d, want a freshly minted snowflake candidate id", call.ID)
	}
	if call.RepoID != 3 || call.BaseCommit != testCommitA || call.HeadCommit != testCommitB {
		t.Fatalf("GetOrCreate change set = %+v, want repo 3 %s..%s", call, testCommitA, testCommitB)
	}
	if string(call.Stats) != string(stats) {
		t.Fatalf("GetOrCreate stats = %s, want the gitx stats verbatim", call.Stats)
	}
	if !reflect.DeepEqual(call.Files, files) {
		t.Fatalf("GetOrCreate files = %+v, want the gitx files verbatim", call.Files)
	}
	if len(f.notifier.changed) != 1 {
		t.Fatalf("code.changed events = %d, want 1", len(f.notifier.changed))
	}
	event := f.notifier.changed[0]
	if event.AnalysisID != analysis.ID || event.BaseCommit != testCommitA || event.HeadCommit != testCommitB {
		t.Fatalf("code.changed = %+v, want analysis %d and the commit pair", event, analysis.ID)
	}
	if string(event.Stats) != string(stats) {
		t.Fatalf("code.changed stats = %s, want the change-set stats as-is", event.Stats)
	}
	wantPaths := []string{"server/refund.go", "web/src/api/refund.ts"}
	if !reflect.DeepEqual(event.Paths, wantPaths) {
		t.Fatalf("code.changed paths = %v, want the sorted changed paths %v", event.Paths, wantPaths)
	}
	if len(f.notifier.changedVersions) != 1 || f.notifier.changedVersions[0] != 11 {
		t.Fatalf("code.changed version = %v, want 11", f.notifier.changedVersions)
	}
}

// TestRunFailureMarksAnalysisFailed pins the failed terminal state for Git and
// planner failures and the terminal no-op semantics of a repeated job.
func TestRunFailureMarksAnalysisFailed(t *testing.T) {
	t.Run("git diff failure", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
		f.git.diffErr = errors.New("git exploded")

		analysis := f.startAnalysis(t, false)
		err := f.svc.Run(context.Background(), analysis.ID)
		if err == nil {
			t.Fatal("Run error = nil, want the git failure")
		}
		stored := f.storedAnalysis(t, analysis.ID)
		if stored.Status != domain.RunFailed || stored.FinishedAt == nil {
			t.Fatalf("stored analysis = %+v, want failed with finished_at", stored)
		}
		if len(stored.Errors) == 0 || stored.Errors[0].Code != "internal" {
			t.Fatalf("analysis errors = %+v, want internal", stored.Errors)
		}
	})

	t.Run("planner failure", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
		f.llm.defaultResponse = clientFactResponse()
		f.planner.err = errors.New("planner exploded")

		analysis := f.startAnalysis(t, false)
		if err := f.svc.Run(context.Background(), analysis.ID); err == nil {
			t.Fatal("Run error = nil, want the planner failure")
		}
		stored := f.storedAnalysis(t, analysis.ID)
		if stored.Status != domain.RunFailed {
			t.Fatalf("stored status = %q, want failed", stored.Status)
		}
	})

	t.Run("terminal analysis is a no-op", func(t *testing.T) {
		f := newAnalysisFixture(t)
		analysis := f.startAnalysis(t, false)
		stored := f.storedAnalysis(t, analysis.ID)
		stored.Status = domain.RunDone
		f.analyses.byID[analysis.ID] = stored
		updatesBefore := f.analyses.updateCount()

		if err := f.svc.Run(context.Background(), analysis.ID); err != nil {
			t.Fatalf("Run(done) error = %v, want nil", err)
		}
		if f.analyses.updateCount() != updatesBefore {
			t.Fatal("Run(done) must not update the analysis again")
		}
		if len(f.git.diffCalls) != 0 {
			t.Fatal("Run(done) must not recompute the change set")
		}
	})

	t.Run("running analysis conflicts", func(t *testing.T) {
		f := newAnalysisFixture(t)
		analysis := f.startAnalysis(t, false)
		stored := f.storedAnalysis(t, analysis.ID)
		stored.Status = domain.RunRunning
		f.analyses.byID[analysis.ID] = stored

		err := f.svc.Run(context.Background(), analysis.ID)
		if !errors.Is(err, domain.ErrConflict) || domain.Reason(err) != "analysis_in_progress" {
			t.Fatalf("Run(running) error = %v, want analysis_in_progress", err)
		}
	})

	t.Run("unknown analysis is not found", func(t *testing.T) {
		f := newAnalysisFixture(t)
		err := f.svc.Run(context.Background(), 12345)
		if !errors.Is(err, domain.ErrNotFound) || domain.Reason(err) != "analysis_not_found" {
			t.Fatalf("Run(unknown) error = %v, want analysis_not_found", err)
		}
	})

	t.Run("analysis not owned by this service is not found", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.analyses.byID[4321] = domain.CodeAnalysis{ID: 4321, VersionID: 11, Status: domain.RunQueued}
		err := f.svc.Run(context.Background(), 4321)
		if !errors.Is(err, domain.ErrNotFound) || domain.Reason(err) != "analysis_not_found" {
			t.Fatalf("Run(foreign) error = %v, want analysis_not_found", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Candidate PATCH and read pass-through.
// ---------------------------------------------------------------------------

// TestPatchCandidateValidation pins the PATCH rules of design spec §7.3.
func TestPatchCandidateValidation(t *testing.T) {
	done := domain.CodeAnalysis{ID: 100, VersionID: 11, Status: domain.RunDone}
	candidate := domain.ImpactCandidate{ID: 500, AnalysisID: 100, SubjectID: 77, Selected: false}

	t.Run("selection is persisted and returned", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.analyses.byID[100] = done
		f.candidates.seed(100, candidate)

		updated, err := f.svc.PatchCandidate(context.Background(), 11, 100, 500, 42, true)
		if err != nil {
			t.Fatalf("PatchCandidate error = %v", err)
		}
		if !updated.Selected || updated.SelectionUpdatedBy == nil || *updated.SelectionUpdatedBy != 42 {
			t.Fatalf("updated candidate = %+v, want selected by 42", updated)
		}
		if updated.SelectionUpdatedAt == nil || !updated.SelectionUpdatedAt.Equal(f.clock.now) {
			t.Fatalf("selection updatedAt = %v, want the clock time", updated.SelectionUpdatedAt)
		}
		if len(f.analyses.selections) != 1 {
			t.Fatalf("selection updates = %+v, want one", f.analyses.selections)
		}
		update := f.analyses.selections[0]
		if update.analysisID != 100 || update.candidateID != 500 || update.actorID != 42 || !update.selected {
			t.Fatalf("selection update = %+v, want (100,500,42,true)", update)
		}
	})

	t.Run("analysis of another version is not found", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.analyses.byID[100] = done
		f.candidates.seed(100, candidate)

		_, err := f.svc.PatchCandidate(context.Background(), 12, 100, 500, 42, true)
		if !errors.Is(err, domain.ErrNotFound) || domain.Reason(err) != "analysis_not_found" {
			t.Fatalf("PatchCandidate error = %v, want analysis_not_found", err)
		}
	})

	t.Run("unfinished analysis conflicts", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.analyses.byID[100] = domain.CodeAnalysis{ID: 100, VersionID: 11, Status: domain.RunRunning}
		f.candidates.seed(100, candidate)

		_, err := f.svc.PatchCandidate(context.Background(), 11, 100, 500, 42, true)
		if !errors.Is(err, domain.ErrConflict) || domain.Reason(err) != "analysis_not_done" {
			t.Fatalf("PatchCandidate error = %v, want analysis_not_done", err)
		}
	})

	t.Run("missing candidate is not found", func(t *testing.T) {
		f := newAnalysisFixture(t)
		f.analyses.byID[100] = done
		f.candidates.seed(100, candidate)

		_, err := f.svc.PatchCandidate(context.Background(), 11, 100, 501, 42, true)
		if !errors.Is(err, domain.ErrNotFound) || domain.Reason(err) != "candidate_not_found" {
			t.Fatalf("PatchCandidate error = %v, want candidate_not_found", err)
		}
	})
}

// TestAnalysisReads pins the Get/GetLatest/ListCandidates pass-through used by
// the HTTP layer.
func TestAnalysisReads(t *testing.T) {
	f := newAnalysisFixture(t)
	f.analyses.byID[100] = domain.CodeAnalysis{ID: 100, VersionID: 11, Status: domain.RunDone, CreatedAt: f.clock.now}
	f.candidates.seed(100, domain.ImpactCandidate{ID: 500, AnalysisID: 100})

	got, err := f.svc.Get(context.Background(), 11, 100)
	if err != nil || got.ID != 100 {
		t.Fatalf("Get = %+v, %v; want analysis 100", got, err)
	}
	if _, err := f.svc.Get(context.Background(), 12, 100); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get(other version) error = %v, want ErrNotFound", err)
	}
	latest, err := f.svc.GetLatest(context.Background(), 11)
	if err != nil || latest.ID != 100 {
		t.Fatalf("GetLatest = %+v, %v; want analysis 100", latest, err)
	}
	candidates, err := f.svc.ListCandidates(context.Background(), 100)
	if err != nil || len(candidates) != 1 || candidates[0].ID != 500 {
		t.Fatalf("ListCandidates = %+v, %v; want candidate 500", candidates, err)
	}
}

// ---------------------------------------------------------------------------
// Review fix round 1 regression tests.
// ---------------------------------------------------------------------------

// analysisClientAndRouteResponse renders one client_call + route fact pair plus
// the supplied AI edges. The summaries are parameters so a test can bump a fact
// body without changing the fact identity.
func analysisClientAndRouteResponse(changeSummary, clientSummary, routeSummary string, edges []any) string {
	payload := map[string]any{
		"changeSummary": changeSummary,
		"facts": []any{
			map[string]any{
				"kind": domain.KindClientCall, "name": "POST /api/refund",
				"path": "web/src/api/refund.ts", "startLine": 1, "endLine": 3,
				"signature": "post('/api/refund')", "confidence": domain.ConfidenceHigh,
				"summary": clientSummary,
				"evidence": []any{map[string]any{
					"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3,
				}},
			},
			map[string]any{
				"kind": domain.KindRoute, "name": "/orders",
				"path": "web/src/api/refund.ts", "startLine": 1, "endLine": 1,
				"signature": "routes", "confidence": domain.ConfidenceHigh,
				"summary": routeSummary,
				"evidence": []any{map[string]any{
					"file": "web/src/api/refund.ts", "startLine": 1, "endLine": 1,
				}},
			},
		},
		"edges": edges,
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

// analysisClientFactWithEdges renders one client_call fact plus the supplied AI
// edges (used for endpoint-resolution coverage).
func analysisClientFactWithEdges(factSummary string, edges []any) string {
	payload := map[string]any{
		"changeSummary": "refund client analysis",
		"facts": []any{map[string]any{
			"kind": domain.KindClientCall, "name": "POST /api/refund",
			"path": "web/src/api/refund.ts", "startLine": 1, "endLine": 3,
			"signature": "post('/api/refund')", "confidence": domain.ConfidenceHigh,
			"summary": factSummary,
			"evidence": []any{map[string]any{
				"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3,
			}},
		}},
		"edges": edges,
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

// analysisTestEdge builds one AI edge payload.
func analysisTestEdge(fromKind, fromName, toKind, toName, rel string, evidence ...map[string]any) map[string]any {
	return map[string]any{
		"from":       map[string]any{"kind": fromKind, "name": fromName},
		"to":         map[string]any{"kind": toKind, "name": toName},
		"rel":        rel,
		"confidence": domain.ConfidenceHigh,
		"evidence":   evidence,
	}
}

// TestRunAIEdgeUsesPersistedEntityAfterFactVersionBump pins Critical 1: the
// Upsert of a changed fact preserves the existing row id, so the materialised
// edge must reference the persisted entity ids (the previously minted id would
// dangle).
func TestRunAIEdgeUsesPersistedEntityAfterFactVersionBump(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	edge := analysisTestEdge(domain.KindClientCall, "POST /api/refund", domain.KindRoute, "/orders",
		domain.RelCalls, map[string]any{"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3})
	f.llm.responses["web/src/api/refund.ts"] = analysisClientAndRouteResponse(
		"first", "client calls refund", "orders route", []any{edge})

	f.runNewAnalysis(t)
	client, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("client fact missing after first run")
	}
	route, ok := f.entities.fact(3, testCommitB, "route:/orders")
	if !ok {
		t.Fatal("route fact missing after first run")
	}
	firstEdges := f.edges.allEdges()
	if len(firstEdges) != 1 {
		t.Fatalf("first-run edges = %+v, want the AI calls edge", firstEdges)
	}
	if firstEdges[0].SrcEntityID != client.ID || firstEdges[0].DstEntityID != route.ID {
		t.Fatalf("first-run edge = %d->%d, want %d->%d", firstEdges[0].SrcEntityID,
			firstEdges[0].DstEntityID, client.ID, route.ID)
	}
	if firstEdges[0].FactVersion == nil || *firstEdges[0].FactVersion != 1 {
		t.Fatalf("first-run edge factVersion = %v, want 1", firstEdges[0].FactVersion)
	}

	// Forced re-run with a changed client body and the same AI edge: the entity
	// row keeps its id, so the version-2 edge must use it.
	f.llm.responses["web/src/api/refund.ts"] = analysisClientAndRouteResponse(
		"second", "client calls refund with a reason", "orders route", []any{edge})
	forced := f.startAnalysis(t, true)
	f.runAnalysis(t, forced.ID)

	persistedClient, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("client fact missing after the body bump")
	}
	persistedRoute, ok := f.entities.fact(3, testCommitB, "route:/orders")
	if !ok {
		t.Fatal("route fact missing after the body bump")
	}
	if persistedClient.ID != client.ID || persistedRoute.ID != route.ID {
		t.Fatalf("persisted ids = %d/%d, want the stable row ids %d/%d",
			persistedClient.ID, persistedRoute.ID, client.ID, route.ID)
	}
	if persistedClient.FactVersion == nil || *persistedClient.FactVersion != 2 {
		t.Fatalf("persisted client factVersion = %v, want 2", persistedClient.FactVersion)
	}

	replaces := f.edges.replacesFor("client:POST /api/refund")
	if len(replaces) != 2 {
		t.Fatalf("client edge replacements = %+v, want one per run", replaces)
	}
	latest := replaces[len(replaces)-1]
	if latest.version != 2 || len(latest.edges) != 1 {
		t.Fatalf("version-2 replacement = %+v, want the single AI edge", latest)
	}
	bumped := latest.edges[0]
	if bumped.SrcEntityID != persistedClient.ID || bumped.DstEntityID != persistedRoute.ID {
		t.Fatalf("bumped edge endpoints = %d/%d, want persisted ids %d/%d",
			bumped.SrcEntityID, bumped.DstEntityID, persistedClient.ID, persistedRoute.ID)
	}

	// The planner handoff must carry the same persisted ids.
	if len(f.planner.calls) != 2 {
		t.Fatalf("Planner calls = %d, want 2", len(f.planner.calls))
	}
	plannerIDs := make(map[string]int64)
	for _, fact := range f.planner.calls[1].FactsAtHead {
		if fact.FactKey != nil {
			plannerIDs[*fact.FactKey] = fact.ID
		}
	}
	if plannerIDs["client:POST /api/refund"] != persistedClient.ID ||
		plannerIDs["route:/orders"] != persistedRoute.ID {
		t.Fatalf("planner fact ids = %+v, want persisted %d/%d",
			plannerIDs, persistedClient.ID, persistedRoute.ID)
	}
}

// TestRunCountsUnresolvedAIEdges pins design spec §6.2: an AI edge with an
// endpoint that does not resolve to a current fact is dropped and counted.
func TestRunCountsUnresolvedAIEdges(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	edge := analysisTestEdge(domain.KindClientCall, "POST /api/refund", domain.KindComponent, "MissingWidget",
		domain.RelImports, map[string]any{"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3})
	f.llm.responses["web/src/api/refund.ts"] = analysisClientFactWithEdges("client calls refund", []any{edge})

	analysis := f.runNewAnalysis(t)
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "unresolvedEdges"); got != 1 {
		t.Fatalf("stats.unresolvedEdges = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "edges"); got != 0 {
		t.Fatalf("stats.edges = %d, want 0", got)
	}
	if edges := f.edges.allEdges(); len(edges) != 0 {
		t.Fatalf("materialised edges = %+v, want none for an unresolved endpoint", edges)
	}
}

// TestRunContractEdgeOwnerIsStableAcrossReruns pins R31's deterministic
// per-(fact_key, fact_version) replacement: both contract sides materialised in
// one run have exactly one owner (the client side) on every rerun, never a
// duplicate or ownership-flipped write.
func TestRunContractEdgeOwnerIsStableAcrossReruns(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(
		analysisChangedFile(1, "web/src/api/refund.ts", "M"),
		analysisChangedFile(2, "server/refund.go", "M"),
	)
	f.configureFileAnalyses()
	f.runNewAnalysis(t)
	forced := f.startAnalysis(t, true)
	f.runAnalysis(t, forced.ID)

	contractEdges := make([]domain.CodeEdge, 0)
	for _, edge := range f.edges.allEdges() {
		if edge.Rel == domain.RelRouteToAPI {
			contractEdges = append(contractEdges, edge)
		}
	}
	if len(contractEdges) != 1 {
		t.Fatalf("route_to_api edges = %+v, want exactly one owned edge", contractEdges)
	}
	if contractEdges[0].FactKey == nil || *contractEdges[0].FactKey != "client:POST /api/refund" {
		t.Fatalf("route_to_api owner = %v, want the deterministic client owner", contractEdges[0].FactKey)
	}
	apiReplaces := f.edges.replacesFor("api:POST /api/refund")
	if len(apiReplaces) == 0 {
		t.Fatal("api fact edges were not replaced")
	}
	if last := apiReplaces[len(apiReplaces)-1]; len(last.edges) != 0 {
		t.Fatalf("api owner edges = %+v, want none (client owns the pair)", last.edges)
	}
}

// TestStartUsesRepositoryFixedBranchForLiveHead pins design spec §6.1/INV-3: a
// live head resolution always uses the repository fixed branch, never the
// version's code branch.
func TestStartUsesRepositoryFixedBranchForLiveHead(t *testing.T) {
	f := newAnalysisFixture(t)
	f.versions.info.Branch = "feature/version-branch"
	repo := f.repos.infos[3]
	repo.FixedBranch = "release"
	f.repos.infos[3] = repo

	if _, _, err := f.svc.Start(context.Background(), 11, 42, "", false); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if len(f.git.headCalls) != 1 || f.git.headCalls[0] != "release" {
		t.Fatalf("Head calls = %v, want the repository fixed branch release", f.git.headCalls)
	}
}

// TestRunPlannerInputCarriesAggregatedStats pins the finding-3 fix: the
// aggregated stats document (including changeSummary) is merged into
// Analysis.Stats before Planner.Plan runs.
func TestRunPlannerInputCarriesAggregatedStats(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()

	f.runNewAnalysis(t)
	if len(f.planner.calls) != 1 {
		t.Fatalf("Planner calls = %d, want 1", len(f.planner.calls))
	}
	var stats map[string]any
	if err := json.Unmarshal(f.planner.calls[0].Analysis.Stats, &stats); err != nil {
		t.Fatalf("decode planner analysis stats %q: %v", string(f.planner.calls[0].Analysis.Stats), err)
	}
	if got, ok := stats["facts"].(float64); !ok || int(got) != 1 {
		t.Fatalf("planner stats.facts = %v, want 1", stats["facts"])
	}
	if got, ok := stats["changedFiles"].(float64); !ok || int(got) != 1 {
		t.Fatalf("planner stats.changedFiles = %v, want 1", stats["changedFiles"])
	}
	summary, _ := stats["changeSummary"].(string)
	if !strings.Contains(summary, "refund client call") {
		t.Fatalf("planner stats.changeSummary = %q, want the aggregated summary", summary)
	}
}

// TestRunPlannerInputIncludesContractCounterparts pins the finding-3 fix: the
// head counterpart of a contract pair (an api fact whose file is not a changed
// file) is part of FactsAtHead so T13 can propagate the contract.
func TestRunPlannerInputIncludesContractCounterparts(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()
	apiKey := "api:POST /api/refund"
	apiVersion := 1
	f.entities.facts[analysisEntityKey(3, testCommitB, apiKey)] = domain.CodeEntity{
		ID: 9100, RepoID: 3, Commit: testCommitB, Kind: domain.KindAPI,
		Name: "POST /api/refund", Path: "server/refund.go", Source: domain.SourceAI,
		Confidence: domain.ConfidenceHigh,
		Evidence:   []domain.Evidence{analysisEvidenceFor(t, 0, "server/refund.go", 1, 3)},
		FactKey:    &apiKey, FactVersion: &apiVersion,
	}

	analysis := f.runNewAnalysis(t)
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "contracts"); got != 1 {
		t.Fatalf("stats.contracts = %d, want 1", got)
	}
	if len(f.planner.calls) != 1 {
		t.Fatalf("Planner calls = %d, want 1", len(f.planner.calls))
	}
	in := f.planner.calls[0]
	ids := make(map[string]int64)
	for _, fact := range in.FactsAtHead {
		if fact.FactKey != nil {
			ids[*fact.FactKey] = fact.ID
		}
	}
	client, _ := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if ids["client:POST /api/refund"] != client.ID {
		t.Fatalf("planner client fact = %d, want persisted %d", ids["client:POST /api/refund"], client.ID)
	}
	if ids[apiKey] != 9100 {
		t.Fatalf("planner facts = %+v, want the contract counterpart api fact 9100", ids)
	}
	var contract *domain.CodeEdge
	for index := range in.Edges {
		if in.Edges[index].Rel == domain.RelRouteToAPI {
			contract = &in.Edges[index]
		}
	}
	if contract == nil || contract.SrcEntityID != client.ID || contract.DstEntityID != 9100 {
		t.Fatalf("planner contract edge = %+v, want %d->9100", contract, client.ID)
	}
}

// TestRunMixedEvidenceForcesLowClue pins §6.2/§6.3: any evidence-validation
// failure (not only a fully invalid set) forces the fact to a low clue and the
// edge to low confidence.
func TestRunMixedEvidenceForcesLowClue(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	payload := map[string]any{
		"changeSummary": "mixed evidence",
		"facts": []any{
			map[string]any{
				"kind": domain.KindClientCall, "name": "POST /api/refund",
				"path": "web/src/api/refund.ts", "startLine": 1, "endLine": 3,
				"confidence": domain.ConfidenceHigh, "summary": "mixed client evidence",
				"evidence": []any{
					map[string]any{"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3},
					map[string]any{"file": "web/src/api/refund.ts", "startLine": 99, "endLine": 100},
				},
			},
			map[string]any{
				"kind": domain.KindRoute, "name": "/orders",
				"path": "web/src/api/refund.ts", "startLine": 1, "endLine": 1,
				"confidence": domain.ConfidenceHigh, "summary": "orders route",
				"evidence": []any{
					map[string]any{"file": "web/src/api/refund.ts", "startLine": 1, "endLine": 1},
				},
			},
		},
		"edges": []any{map[string]any{
			"from": map[string]any{"kind": domain.KindClientCall, "name": "POST /api/refund"},
			"to":   map[string]any{"kind": domain.KindRoute, "name": "/orders"},
			"rel":  domain.RelCalls, "confidence": domain.ConfidenceHigh,
			"evidence": []any{
				map[string]any{"file": "web/src/api/refund.ts", "startLine": 1, "endLine": 2},
				map[string]any{"file": "web/src/api/refund.ts", "startLine": 99, "endLine": 100},
			},
		}},
	}
	raw, _ := json.Marshal(payload)
	f.llm.responses["web/src/api/refund.ts"] = string(raw)

	analysis := f.runNewAnalysis(t)
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "evidenceChecks"); got != 5 {
		t.Fatalf("stats.evidenceChecks = %d, want 5", got)
	}
	if got := analysisStatInt(t, stats, "evidenceFailures"); got != 2 {
		t.Fatalf("stats.evidenceFailures = %d, want 2", got)
	}
	client, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("client fact missing")
	}
	if client.Confidence != domain.ConfidenceLow {
		t.Fatalf("client confidence = %q, want low for mixed evidence", client.Confidence)
	}
	if !strings.Contains(string(client.Meta), `"clue":true`) {
		t.Fatalf("client meta = %s, want clue=true", client.Meta)
	}
	if len(client.Evidence) != 1 {
		t.Fatalf("client evidence = %+v, want only the valid entry", client.Evidence)
	}
	var edge *domain.CodeEdge
	for _, candidate := range f.edges.allEdges() {
		if candidate.Rel == domain.RelCalls {
			edge = &candidate
		}
	}
	if edge == nil {
		t.Fatal("calls edge missing")
	}
	if edge.Confidence != domain.ConfidenceLow {
		t.Fatalf("edge confidence = %q, want low for mixed evidence", edge.Confidence)
	}
	if len(edge.Evidence) != 1 {
		t.Fatalf("edge evidence = %+v, want only the valid entry", edge.Evidence)
	}
}

// TestMergeMaterializedFactsForcesClueOnInvalidEvidence pins the duplicate-merge
// half of the same §6.3 rule: a duplicate contribution with an invalid evidence
// entry keeps the merged fact a low clue even when valid evidence survives.
func TestMergeMaterializedFactsForcesClueOnInvalidEvidence(t *testing.T) {
	valid := &materializedFact{
		Key: "route:/orders", Kind: domain.KindRoute, Name: "/orders", Path: "web/a.ts",
		Confidence: domain.ConfidenceHigh, LinesValid: true,
		Evidence: []domain.Evidence{{File: "web/a.ts", StartLine: 1, EndLine: 1}},
	}
	mixed := &materializedFact{
		Key: "route:/orders", Kind: domain.KindRoute, Name: "/orders", Path: "web/a.ts",
		Confidence: domain.ConfidenceLow, LinesValid: true, Clue: true, InvalidEvidence: 1,
		Evidence: []domain.Evidence{{File: "web/b.ts", StartLine: 2, EndLine: 2}},
	}
	merged, _ := mergeMaterializedFacts([]analysisFileResult{
		{facts: []*materializedFact{valid}},
		{facts: []*materializedFact{mixed}},
	})
	if len(merged) != 1 {
		t.Fatalf("merged facts = %d, want 1", len(merged))
	}
	if !merged[0].Clue || merged[0].Confidence != domain.ConfidenceLow {
		t.Fatalf("merged fact = %+v, want a low clue after an invalid evidence entry", merged[0])
	}
}

// TestRunSkipsSymlinkLikeFiles pins §6.2: a changed file that GitReader reports
// as path_not_file (symlink/submodule) is skipped, not degraded.
func TestRunSkipsSymlinkLikeFiles(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/link.ts", "M"))
	f.git.readErr["web/src/link.ts"] = domain.NewInvalid("path", "path_not_file",
		"codeintel: path is not a regular file at the requested commit")

	analysis := f.runNewAnalysis(t)
	if analysis.Degraded {
		t.Fatal("path_not_file must not degrade the run")
	}
	if len(analysis.Errors) != 0 {
		t.Fatalf("analysis errors = %+v, want none for an unsupported file kind", analysis.Errors)
	}
	stats := analysisStatsJSON(t, analysis)
	if got := analysisStatInt(t, stats, "skipped"); got != 1 {
		t.Fatalf("stats.skipped = %d, want 1", got)
	}
	if got := analysisStatInt(t, stats, "failedFiles"); got != 0 {
		t.Fatalf("stats.failedFiles = %d, want 0", got)
	}
	if f.llm.callCount() != 0 {
		t.Fatalf("LLM calls = %d, want 0 for a skipped file", f.llm.callCount())
	}
}

// TestRunDuplicateFactVersionRetryRecovers pins the R30 idempotency rule: a
// partial materialisation that left the fact-version row but no entity is
// recovered on retry when the retried body is identical, and the failure was
// persisted as failed.
func TestRunDuplicateFactVersionRetryRecovers(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponse()

	f.entities.err = errors.New("entity write failed")
	failed := f.startAnalysis(t, false)
	if err := f.svc.Run(context.Background(), failed.ID); err == nil {
		t.Fatal("Run error = nil, want the partial entity failure")
	}
	storedFailed := f.storedAnalysis(t, failed.ID)
	if storedFailed.Status != domain.RunFailed {
		t.Fatalf("partial failure status = %q, want failed", storedFailed.Status)
	}
	if len(f.facts.creates) != 1 {
		t.Fatalf("fact creates after the partial failure = %+v, want one", f.facts.creates)
	}
	if _, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund"); ok {
		t.Fatal("entity exists after the forced partial failure")
	}

	// Retry with the same body: the duplicate version row is accepted and the
	// missing entity write is completed.
	f.entities.err = nil
	retry := f.startAnalysis(t, true)
	f.runAnalysis(t, retry.ID)
	recovered := f.storedAnalysis(t, retry.ID)
	if recovered.Status != domain.RunDone {
		t.Fatalf("retry status = %q, want done", recovered.Status)
	}
	entity, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("entity missing after the retry")
	}
	if entity.FactVersion == nil || *entity.FactVersion != 1 {
		t.Fatalf("retry entity factVersion = %v, want 1", entity.FactVersion)
	}
	if len(f.facts.creates) != 1 {
		t.Fatalf("fact creates after the retry = %+v, want the recovered version only", f.facts.creates)
	}
	if stats := analysisStatsJSON(t, recovered); analysisStatInt(t, stats, "conflicts") != 0 {
		t.Fatalf("retry stats = %+v, want no conflict", stats)
	}
}

// TestRunChangedBodyAfterPartialFailureIsDeterministic pins the R30 rule for a
// retried version whose body changed: the persisted (DB) version record wins,
// the run completes, and repeated retries stay deterministic instead of
// wedging on duplicate_key.
func TestRunChangedBodyAfterPartialFailureIsDeterministic(t *testing.T) {
	f := newAnalysisFixture(t)
	f.changeSet(analysisChangedFile(1, "web/src/api/refund.ts", "M"))
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponseWithFactSummary("body A")
	f.runNewAnalysis(t)
	initial, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("initial fact missing")
	}

	// Partial failure: version 2 exists but the entity upsert fails.
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponseWithFactSummary("body B")
	f.entities.err = errors.New("entity write failed")
	failed := f.startAnalysis(t, true)
	if err := f.svc.Run(context.Background(), failed.ID); err == nil {
		t.Fatal("Run error = nil, want the partial entity failure")
	}
	if stored := f.storedAnalysis(t, failed.ID); stored.Status != domain.RunFailed {
		t.Fatalf("partial failure status = %q, want failed", stored.Status)
	}
	f.entities.err = nil

	// Retry with a third body: the stored version-2 body is the DB authority.
	f.llm.responses["web/src/api/refund.ts"] = clientFactResponseWithFactSummary("body C")
	retry := f.startAnalysis(t, true)
	f.runAnalysis(t, retry.ID)
	recovered := f.storedAnalysis(t, retry.ID)
	if recovered.Status != domain.RunDone {
		t.Fatalf("retry status = %q, want done", recovered.Status)
	}
	persisted, ok := f.entities.fact(3, testCommitB, "client:POST /api/refund")
	if !ok {
		t.Fatal("fact missing after the retry")
	}
	if persisted.ID != initial.ID {
		t.Fatalf("retried entity id = %d, want the preserved %d", persisted.ID, initial.ID)
	}
	if persisted.FactVersion == nil || *persisted.FactVersion != 2 {
		t.Fatalf("retried factVersion = %v, want the persisted version 2", persisted.FactVersion)
	}
	if len(f.facts.creates) != 2 {
		t.Fatalf("fact creates = %+v, want the existing version 1 and 2 only", f.facts.creates)
	}
	stats := analysisStatsJSON(t, recovered)
	if got := analysisStatInt(t, stats, "conflicts"); got != 1 {
		t.Fatalf("stats.conflicts = %d, want 1 for the DB-persisted version", got)
	}
	storedBody, err := f.facts.GetVersion(context.Background(), 3, testCommitB, "client:POST /api/refund", 2)
	if err != nil {
		t.Fatalf("read persisted version body: %v", err)
	}
	if !strings.Contains(string(storedBody), `"summary":"body B"`) {
		t.Fatalf("persisted version-2 body = %s, want the DB authority body B", storedBody)
	}

	// The next retry computes body C again. The DB now holds body B as the
	// current version, so C is a real body change and legitimately becomes
	// version 3; a repeated retry then reuses version 3 (idempotent).
	again := f.startAnalysis(t, true)
	f.runAnalysis(t, again.ID)
	if got := len(f.facts.creates); got != 3 {
		t.Fatalf("fact creates after the second retry = %d, want the version-3 row", got)
	}
	converged := f.storedAnalysis(t, again.ID)
	if persisted, _ := f.entities.fact(3, testCommitB, "client:POST /api/refund"); persisted.FactVersion == nil || *persisted.FactVersion != 3 {
		t.Fatalf("converged factVersion = %v, want 3", persisted.FactVersion)
	}
	if stats := analysisStatsJSON(t, converged); analysisStatInt(t, stats, "conflicts") != 0 {
		t.Fatalf("second retry stats = %+v, want no conflict", stats)
	}

	repeat := f.startAnalysis(t, true)
	f.runAnalysis(t, repeat.ID)
	if got := len(f.facts.creates); got != 3 {
		t.Fatalf("fact creates after the repeated retry = %d, want 3", got)
	}
	repeatStats := analysisStatsJSON(t, f.storedAnalysis(t, repeat.ID))
	if got := analysisStatInt(t, repeatStats, "conflicts"); got != 0 {
		t.Fatalf("repeated retry stats.conflicts = %d, want 0", got)
	}
}

// ciHasErrorCode reports whether a persisted analysis error list carries code.
func ciHasErrorCode(errs []domain.AnalysisError, code string) bool {
	for _, entry := range errs {
		if entry.Code == code {
			return true
		}
	}
	return false
}
