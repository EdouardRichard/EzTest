package codeintelapp

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// --- index + entity repositories -------------------------------------------------

type fakeIndexRepo struct {
	mu     sync.Mutex
	runs   map[int64]codeintel.IndexRun
	order  []int64
	err    error
	update int
}

func newFakeIndexRepo() *fakeIndexRepo {
	return &fakeIndexRepo{runs: map[int64]codeintel.IndexRun{}}
}

func (f *fakeIndexRepo) CreateRun(_ context.Context, run codeintel.IndexRun) (codeintel.IndexRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return codeintel.IndexRun{}, f.err
	}
	f.runs[run.ID] = run
	f.order = append(f.order, run.ID)
	return run, nil
}

func (f *fakeIndexRepo) GetRun(_ context.Context, id int64) (codeintel.IndexRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		return codeintel.IndexRun{}, codeintel.ErrNotFound
	}
	return run, nil
}

func (f *fakeIndexRepo) LatestRun(_ context.Context, repoID int64) (codeintel.IndexRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := len(f.order) - 1; index >= 0; index-- {
		if run := f.runs[f.order[index]]; run.RepoID == repoID {
			return run, nil
		}
	}
	return codeintel.IndexRun{}, codeintel.ErrNotFound
}

func (f *fakeIndexRepo) UpdateRun(_ context.Context, run codeintel.IndexRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runs[run.ID]; !ok {
		return codeintel.ErrNotFound
	}
	f.update++
	f.runs[run.ID] = run
	return nil
}

func (f *fakeIndexRepo) ActiveRun(_ context.Context, repoID int64) (codeintel.IndexRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.order {
		run := f.runs[id]
		if run.RepoID == repoID && (run.Status == string(codeintel.IndexQueued) || run.Status == string(codeintel.IndexRunning)) {
			return run, true, nil
		}
	}
	return codeintel.IndexRun{}, false, nil
}

func (f *fakeIndexRepo) DoneRunByCommit(_ context.Context, repoID int64, commitSHA string) (codeintel.IndexRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.order {
		run := f.runs[id]
		if run.RepoID == repoID && run.CommitSHA == commitSHA && run.Status == string(codeintel.IndexDone) {
			return run, true, nil
		}
	}
	return codeintel.IndexRun{}, false, nil
}

type fileEntitiesCall struct {
	runID       int64
	repoID      int64
	commitSHA   string
	entryCount  int
	firstPath   string
	unsupported int
}

type fakeEntityRepo struct {
	mu         sync.Mutex
	fileCalls  []fileEntitiesCall
	tree       []codeintel.Entity
	treeCursor string
	treeMore   bool
	treeErr    error
	search     []codeintel.Entity
	searchMore bool
	searchErr  error
	byFactKeys map[string]codeintel.Entity
	upserts    []codeintel.Entity
	upsertErr  error
	edges      []codeintel.Edge
}

func (f *fakeEntityRepo) UpsertFileEntities(_ context.Context, runID, repoID int64, commitSHA string, entries []codeintel.TreeEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := fileEntitiesCall{runID: runID, repoID: repoID, commitSHA: commitSHA, entryCount: len(entries)}
	for _, entry := range entries {
		if entry.Unsupported {
			call.unsupported++
		}
	}
	if len(entries) > 0 {
		call.firstPath = entries[0].Path
	}
	f.fileCalls = append(f.fileCalls, call)
	return nil
}

func (f *fakeEntityRepo) ListTree(_ context.Context, _ int64, _, _, _ string, _ int) ([]codeintel.Entity, string, bool, error) {
	return f.tree, f.treeCursor, f.treeMore, f.treeErr
}

func (f *fakeEntityRepo) Search(_ context.Context, _ int64, _, _, _ string, _ int) ([]codeintel.Entity, error) {
	return f.search, f.searchErr
}

func (f *fakeEntityRepo) EntitiesByFactKeys(_ context.Context, _ int64, _ string, keys []string) ([]codeintel.Entity, error) {
	result := make([]codeintel.Entity, 0, len(keys))
	for _, key := range keys {
		if entity, ok := f.byFactKeys[key]; ok {
			result = append(result, entity)
		}
	}
	return result, nil
}

func (f *fakeEntityRepo) UpsertFactEntity(_ context.Context, entity codeintel.Entity) (codeintel.Entity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return codeintel.Entity{}, f.upsertErr
	}
	if entity.ID == 0 {
		entity.ID = int64(len(f.upserts) + 100)
	}
	f.upserts = append(f.upserts, entity)
	if f.byFactKeys == nil {
		f.byFactKeys = map[string]codeintel.Entity{}
	}
	f.byFactKeys[entity.FactKey] = entity
	return entity, nil
}

func (f *fakeEntityRepo) UpsertEdge(_ context.Context, edge codeintel.Edge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edges = append(f.edges, edge)
	return nil
}

// --- version / resource readers --------------------------------------------------

type baselineSetCall struct {
	versionSUTID int64
	commit       string
}

type fakeVersionReader struct {
	bindings    []VersionSUTBinding
	bindingsErr error
	setCalls    []baselineSetCall
	setErr      error
}

func (f *fakeVersionReader) VersionSUTBindings(_ context.Context, _ int64) ([]VersionSUTBinding, error) {
	return f.bindings, f.bindingsErr
}

func (f *fakeVersionReader) SetCodeBaselineCommit(_ context.Context, versionSUTID int64, commit string) error {
	f.setCalls = append(f.setCalls, baselineSetCall{versionSUTID: versionSUTID, commit: commit})
	return f.setErr
}

type fakeResourceReader struct {
	repos      map[int64]RepoRef
	repoErr    error
	pages      map[int64][]PageRef
	apis       map[int64][]APIRef
	pagesErr   error
	apisErr    error
	network    []NetworkRequest
	evidence   map[int64]EvidenceRef
	networkErr error
}

func (f *fakeResourceReader) CodeRepository(_ context.Context, repoID int64) (RepoRef, error) {
	if f.repoErr != nil {
		return RepoRef{}, f.repoErr
	}
	repository, ok := f.repos[repoID]
	if !ok {
		return RepoRef{}, codeintel.ErrNotFound
	}
	return repository, nil
}

func (f *fakeResourceReader) SUTPages(_ context.Context, versionSUTID int64) ([]PageRef, error) {
	return f.pages[versionSUTID], f.pagesErr
}

func (f *fakeResourceReader) SUTAPIs(_ context.Context, versionSUTID int64) ([]APIRef, error) {
	return f.apis[versionSUTID], f.apisErr
}

// --- work queue ------------------------------------------------------------------

type ensuredWork struct {
	item    NewWorkItem
	created bool
}

type failedWork struct {
	id        int64
	epoch     int64
	message   string
	retryable bool
}

type renewCall struct {
	id    int64
	epoch int64
}

type fakeWorkQueue struct {
	ensured     []ensuredWork
	items       []WorkItem
	claims      [][]string
	claimants   []string
	claimErr    error
	markRunning []int64
	completed   []int64
	failed      []failedWork
	renewed     []renewCall
	renewErr    error
	completeErr error
	failErr     error
}

func (f *fakeWorkQueue) Ensure(_ context.Context, in NewWorkItem) (WorkItem, bool, error) {
	f.ensured = append(f.ensured, ensuredWork{item: in, created: true})
	return WorkItem{ID: int64(len(f.ensured)), VersionID: in.VersionID, Type: in.Type}, true, nil
}

func (f *fakeWorkQueue) ClaimForTeams(_ context.Context, claimant string, teams []string) (WorkItem, bool, error) {
	f.claims = append(f.claims, teams)
	f.claimants = append(f.claimants, claimant)
	if f.claimErr != nil {
		return WorkItem{}, false, f.claimErr
	}
	if len(f.items) == 0 {
		return WorkItem{}, false, nil
	}
	item := f.items[0]
	f.items = f.items[1:]
	return item, true, nil
}

func (f *fakeWorkQueue) MarkRunning(_ context.Context, id, _ int64) error {
	f.markRunning = append(f.markRunning, id)
	return nil
}

func (f *fakeWorkQueue) Renew(_ context.Context, id, epoch int64, _ time.Duration) error {
	f.renewed = append(f.renewed, renewCall{id: id, epoch: epoch})
	return f.renewErr
}

func (f *fakeWorkQueue) Complete(_ context.Context, id, _ int64) error {
	f.completed = append(f.completed, id)
	return f.completeErr
}

func (f *fakeWorkQueue) Fail(_ context.Context, id, epoch int64, message string, retryable bool, _ time.Duration) error {
	f.failed = append(f.failed, failedWork{id: id, epoch: epoch, message: message, retryable: retryable})
	return f.failErr
}

// --- git -------------------------------------------------------------------------

type fakeGit struct {
	resolveCommit string
	resolveErr    error
	head          string
	headErr       error
	ancestor      bool
	ancestorErr   error
	tree          codeintel.TreeResult
	treeErr       error
	diff          codeintel.DiffResult
	diffErr       error
	windows       map[string]codeintel.FileWindow
	readErr       error
	fileDiffs     map[string]codeintel.FileDiff
	fileDiffErr   error
	commits       map[string]codeintel.CommitInfo
	commitErr     error
	resolvedRefs  []string
	readPaths     []string
}

func (f *fakeGit) ResolveCommit(_ context.Context, _ string, ref string) (string, error) {
	f.resolvedRefs = append(f.resolvedRefs, ref)
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return f.resolveCommit, nil
}

func (f *fakeGit) HeadCommit(_ context.Context, _ string, _ string) (string, error) {
	return f.head, f.headErr
}

func (f *fakeGit) IsAncestor(_ context.Context, _, _, _ string) (bool, error) {
	return f.ancestor, f.ancestorErr
}

func (f *fakeGit) ListTree(_ context.Context, _, _ string, _, _ []string, _ int) (codeintel.TreeResult, error) {
	return f.tree, f.treeErr
}

func (f *fakeGit) Diff(_ context.Context, _, _, _ string, _ int) (codeintel.DiffResult, error) {
	return f.diff, f.diffErr
}

func (f *fakeGit) ReadFile(_ context.Context, _, _, path string, _, _, _ int) (codeintel.FileWindow, error) {
	f.readPaths = append(f.readPaths, path)
	if f.readErr != nil {
		return codeintel.FileWindow{}, f.readErr
	}
	return f.windows[path], nil
}

func (f *fakeGit) FileDiff(_ context.Context, _, _, _, path string, _ int) (codeintel.FileDiff, error) {
	if f.fileDiffErr != nil {
		return codeintel.FileDiff{}, f.fileDiffErr
	}
	return f.fileDiffs[path], nil
}

func (f *fakeGit) Commit(_ context.Context, _, sha string, _ int) (codeintel.CommitInfo, error) {
	if f.commitErr != nil {
		return codeintel.CommitInfo{}, f.commitErr
	}
	return f.commits[sha], nil
}

func (f *fakeGit) ReadFileWindow(_ context.Context, _, _, path string, _, _ int) (codeintel.FileWindow, error) {
	f.readPaths = append(f.readPaths, path)
	if f.readErr != nil {
		return codeintel.FileWindow{}, f.readErr
	}
	return f.windows[path], nil
}

// --- analysis / fact repositories ------------------------------------------------

type fakeAnalysisRepo struct {
	runs  map[int64]codeintel.Analysis
	order []int64
	err   error
}

func newFakeAnalysisRepo() *fakeAnalysisRepo {
	return &fakeAnalysisRepo{runs: map[int64]codeintel.Analysis{}}
}

func (f *fakeAnalysisRepo) Create(_ context.Context, analysis codeintel.Analysis) (codeintel.Analysis, error) {
	if f.err != nil {
		return codeintel.Analysis{}, f.err
	}
	f.runs[analysis.ID] = analysis
	f.order = append(f.order, analysis.ID)
	return analysis, nil
}

func (f *fakeAnalysisRepo) Get(_ context.Context, id int64) (codeintel.Analysis, error) {
	analysis, ok := f.runs[id]
	if !ok {
		return codeintel.Analysis{}, codeintel.ErrNotFound
	}
	return analysis, nil
}

func (f *fakeAnalysisRepo) Latest(_ context.Context, versionID, repoID int64) (codeintel.Analysis, error) {
	for index := len(f.order) - 1; index >= 0; index-- {
		analysis := f.runs[f.order[index]]
		if analysis.VersionID == versionID && analysis.RepoID == repoID {
			return analysis, nil
		}
	}
	return codeintel.Analysis{}, codeintel.ErrNotFound
}

func (f *fakeAnalysisRepo) Update(_ context.Context, analysis codeintel.Analysis) error {
	if _, ok := f.runs[analysis.ID]; !ok {
		return codeintel.ErrNotFound
	}
	f.runs[analysis.ID] = analysis
	return nil
}

func (f *fakeAnalysisRepo) Active(_ context.Context, versionID, repoID int64) (codeintel.Analysis, bool, error) {
	for _, id := range f.order {
		analysis := f.runs[id]
		if analysis.VersionID == versionID && analysis.RepoID == repoID &&
			(analysis.Status == codeintel.AnalysisQueued || analysis.Status == codeintel.AnalysisRunning) {
			return analysis, true, nil
		}
	}
	return codeintel.Analysis{}, false, nil
}

func (f *fakeAnalysisRepo) DoneByRange(_ context.Context, versionID, repoID int64, base, head string) (codeintel.Analysis, bool, error) {
	for _, id := range f.order {
		analysis := f.runs[id]
		if analysis.VersionID == versionID && analysis.RepoID == repoID &&
			analysis.BaseCommitSHA == base && analysis.HeadCommitSHA == head &&
			analysis.Status == codeintel.AnalysisDone {
			return analysis, true, nil
		}
	}
	return codeintel.Analysis{}, false, nil
}

func (f *fakeAnalysisRepo) MaxVersion(_ context.Context, versionID, repoID int64) (int, error) {
	maximum := 0
	for _, analysis := range f.runs {
		if analysis.VersionID == versionID && analysis.RepoID == repoID && analysis.AnalysisVersion > maximum {
			maximum = analysis.AnalysisVersion
		}
	}
	return maximum, nil
}

type fakeFactRepo struct {
	mu        sync.Mutex
	current   []codeintel.Fact
	inserted  []codeintel.Fact
	reused    int
	insertErr error
	nextID    int64
}

func (f *fakeFactRepo) InsertVersion(_ context.Context, fact codeintel.Fact) (codeintel.Fact, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return codeintel.Fact{}, false, f.insertErr
	}
	for index, existing := range f.current {
		if existing.RepoID == fact.RepoID && existing.CommitSHA == fact.CommitSHA && existing.FactKey == fact.FactKey {
			if string(existing.Body) == string(fact.Body) || (len(fact.Body) == 0 && len(existing.Body) == 0) {
				f.reused++
				return existing, true, nil
			}
			fact.Version = existing.Version + 1
			f.current[index] = fact
			f.inserted = append(f.inserted, fact)
			return fact, false, nil
		}
	}
	if fact.Version == 0 {
		fact.Version = 1
	}
	f.nextID++
	fact.ID = f.nextID
	f.current = append(f.current, fact)
	f.inserted = append(f.inserted, fact)
	return fact, false, nil
}

func (f *fakeFactRepo) LatestByPath(_ context.Context, repoID int64, commitSHA, filePath string) ([]codeintel.Fact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]codeintel.Fact, 0)
	for _, fact := range f.current {
		if fact.RepoID == repoID && fact.CommitSHA == commitSHA && fact.Path == filePath {
			result = append(result, fact)
		}
	}
	return result, nil
}

func (f *fakeFactRepo) LatestByCommit(_ context.Context, repoID int64, commitSHA string, kinds []codeintel.EntityKind) ([]codeintel.Fact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]codeintel.Fact, 0)
	for _, fact := range f.current {
		if fact.RepoID != repoID || fact.CommitSHA != commitSHA {
			continue
		}
		if len(kinds) > 0 && !containsKind(kinds, fact.Kind) {
			continue
		}
		result = append(result, fact)
	}
	return result, nil
}

func (f *fakeFactRepo) List(_ context.Context, repoID int64, commitSHA, kind, filePath, cursor string, limit int) ([]codeintel.Fact, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]codeintel.Fact, 0)
	for _, fact := range f.current {
		if fact.RepoID != repoID || fact.CommitSHA != commitSHA {
			continue
		}
		if kind != "" && string(fact.Kind) != kind {
			continue
		}
		if filePath != "" && fact.Path != filePath {
			continue
		}
		result = append(result, fact)
	}
	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}
	return result, "", hasMore, nil
}

func (f *fakeFactRepo) ByID(_ context.Context, id int64) (codeintel.Fact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fact := range f.current {
		if fact.ID == id {
			return fact, nil
		}
	}
	return codeintel.Fact{}, codeintel.ErrNotFound
}

func containsKind(kinds []codeintel.EntityKind, kind codeintel.EntityKind) bool {
	for _, candidate := range kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

type fakeExtractor struct {
	outputs   map[string]FactExtractOutput
	errs      map[string]error
	inputs    []FactExtractInput
	model     string
	modelErr  error
	impactOut ImpactInferenceOutput
	impactErr error
	impactIn  []ImpactInferenceInput
}

func (f *fakeExtractor) Extract(_ context.Context, in FactExtractInput) (FactExtractOutput, error) {
	f.inputs = append(f.inputs, in)
	if err := f.errs[in.Path]; err != nil {
		return FactExtractOutput{}, err
	}
	return f.outputs[in.Path], nil
}

func (f *fakeExtractor) Impact(_ context.Context, in ImpactInferenceInput) (ImpactInferenceOutput, error) {
	f.impactIn = append(f.impactIn, in)
	return f.impactOut, f.impactErr
}

func (f *fakeExtractor) Model(_ context.Context) (string, error) {
	return f.model, f.modelErr
}

// --- events / audit / settings / clock / ids -------------------------------------

type fakeEvents struct {
	events []Event
}

func (f *fakeEvents) Publish(_ context.Context, event Event) error {
	f.events = append(f.events, event)
	return nil
}

func (f *fakeEvents) types() []string {
	result := make([]string, 0, len(f.events))
	for _, event := range f.events {
		result = append(result, event.Type)
	}
	return result
}

type fakeAudit struct {
	entries []AuditEntry
	err     error
}

func (f *fakeAudit) Record(_ context.Context, entry AuditEntry) error {
	f.entries = append(f.entries, entry)
	return f.err
}

type fakeSettings struct {
	strings map[string]string
	ints    map[string]int64
	floats  map[string]float64
	raws    map[string]json.RawMessage
	err     error
}

func (f *fakeSettings) String(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.strings[key], nil
}

func (f *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.ints[key], nil
}

func (f *fakeSettings) Float(_ context.Context, key string) (float64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.floats[key], nil
}

func (f *fakeSettings) Raw(_ context.Context, key string) (json.RawMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.raws[key], nil
}

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

type fakeIDs struct {
	mu   sync.Mutex
	next int64
}

func (f *fakeIDs) Next() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	return f.next
}
