package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Test fixture commits are valid lowercase 40-hex ids.
const (
	testCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testCommitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testCommitC = "cccccccccccccccccccccccccccccccccccccccc"
	testCommitD = "dddddddddddddddddddddddddddddddddddddddd"
	testBlobA   = "1111111111111111111111111111111111111111"
	testBlobB   = "2222222222222222222222222222222222222222"
	testBlobC   = "3333333333333333333333333333333333333333"
)

// ---------------------------------------------------------------------------
// Shared stubs for the codeintel application unit tests.
// ---------------------------------------------------------------------------

type gitHeadCall struct {
	localPath string
	branch    string
}

type gitTreeCall struct {
	localPath string
	commit    string
	prefix    string
	include   []string
	exclude   []string
	cursor    string
	limit     int
}

type gitFileCall struct {
	localPath string
	commit    string
	path      string
	startLine int
	endLine   int
}

type gitDiffCall struct {
	localPath string
	base      string
	head      string
}

type gitFileDiffCall struct {
	localPath string
	base      string
	head      string
	path      string
	maxBytes  int
}

// stubGit is a configurable GitReader double. Tree serves treeEntries through
// the same cursor/limit contract as the real adapter so service pagination is
// exercised.
type stubGit struct {
	head      string
	headErr   error
	headCalls []gitHeadCall

	entries   []domain.TreeEntry
	treePage  domain.TreePage
	treeErr   error
	treeFunc  func(ctx context.Context) (domain.TreePage, error)
	treeCalls []gitTreeCall

	fileWindow domain.FileWindow
	fileErr    error
	fileCalls  []gitFileCall

	readAll     []byte
	readAllMore bool
	readAllErr  error

	diffData  domain.ChangeSetData
	diffErr   error
	diffCalls []gitDiffCall

	fileDiff      domain.FileDiff
	fileDiffErr   error
	fileDiffCalls []gitFileDiffCall

	commitInfo domain.CommitInfo
	commitErr  error

	ancestor    bool
	ancestorErr error
}

func (g *stubGit) Head(_ context.Context, localPath, branch string) (string, error) {
	g.headCalls = append(g.headCalls, gitHeadCall{localPath: localPath, branch: branch})
	return g.head, g.headErr
}

func (g *stubGit) IsAncestor(context.Context, string, string, string) (bool, error) {
	return g.ancestor, g.ancestorErr
}

func (g *stubGit) Tree(_ context.Context, localPath, commit, prefix string, include, exclude []string, cursor string, limit int) (domain.TreePage, error) {
	g.treeCalls = append(g.treeCalls, gitTreeCall{
		localPath: localPath,
		commit:    commit,
		prefix:    prefix,
		include:   append([]string(nil), include...),
		exclude:   append([]string(nil), exclude...),
		cursor:    cursor,
		limit:     limit,
	})
	if g.treeFunc != nil {
		return g.treeFunc(context.Background())
	}
	if g.treeErr != nil {
		return domain.TreePage{}, g.treeErr
	}
	if g.entries == nil {
		return g.treePage, nil
	}
	start := 0
	if cursor != "" {
		for start < len(g.entries) && g.entries[start].Path <= cursor {
			start++
		}
	}
	pageSize := limit
	if pageSize <= 0 || pageSize > domain.MaxTreeEntriesPerPage {
		pageSize = domain.MaxTreeEntriesPerPage
	}
	end := start + pageSize
	if end > len(g.entries) {
		end = len(g.entries)
	}
	page := domain.TreePage{Commit: commit}
	page.Entries = append(page.Entries, g.entries[start:end]...)
	if end < len(g.entries) {
		page.HasMore = true
		if len(page.Entries) > 0 {
			page.NextCursor = page.Entries[len(page.Entries)-1].Path
		}
	}
	return page, nil
}

func (g *stubGit) File(_ context.Context, localPath, commit, path string, startLine, endLine int) (domain.FileWindow, error) {
	g.fileCalls = append(g.fileCalls, gitFileCall{
		localPath: localPath,
		commit:    commit,
		path:      path,
		startLine: startLine,
		endLine:   endLine,
	})
	return g.fileWindow, g.fileErr
}

func (g *stubGit) ReadAll(context.Context, string, string, string, int) ([]byte, bool, error) {
	return g.readAll, g.readAllMore, g.readAllErr
}

func (g *stubGit) Diff(_ context.Context, localPath, base, head string) (domain.ChangeSetData, error) {
	g.diffCalls = append(g.diffCalls, gitDiffCall{localPath: localPath, base: base, head: head})
	return g.diffData, g.diffErr
}

func (g *stubGit) FileDiff(_ context.Context, localPath, base, head, path string, maxBytes int) (domain.FileDiff, error) {
	g.fileDiffCalls = append(g.fileDiffCalls, gitFileDiffCall{
		localPath: localPath,
		base:      base,
		head:      head,
		path:      path,
		maxBytes:  maxBytes,
	})
	return g.fileDiff, g.fileDiffErr
}

func (g *stubGit) Commit(context.Context, string, string) (domain.CommitInfo, error) {
	return g.commitInfo, g.commitErr
}

// stubRepos serves one or more repository projections.
type stubRepos struct {
	infos map[int64]RepoInfo
	err   error
	calls []int64
}

func (r *stubRepos) Get(_ context.Context, repoID int64) (RepoInfo, error) {
	r.calls = append(r.calls, repoID)
	if r.err != nil {
		return RepoInfo{}, r.err
	}
	if info, ok := r.infos[repoID]; ok {
		return info, nil
	}
	return RepoInfo{}, domain.NewInvalid("repoId", "code_repository_not_found",
		"code repository not found")
}

// stubIndexRuns is an in-memory IndexRunRepository.
type stubIndexRuns struct {
	mu         sync.Mutex
	runs       map[int64]domain.IndexRun
	latest     map[int64]domain.IndexRun
	creates    []domain.IndexRun
	updates    []domain.IndexRun
	createErr  error
	updateErr  error
	getErr     error
	latestErr  error
	recovered  int64
	recoverErr error
}

func newStubIndexRuns() *stubIndexRuns {
	return &stubIndexRuns{
		runs:   make(map[int64]domain.IndexRun),
		latest: make(map[int64]domain.IndexRun),
	}
}

func (r *stubIndexRuns) Create(_ context.Context, run domain.IndexRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.runs[run.ID] = run
	r.creates = append(r.creates, run)
	return nil
}

func (r *stubIndexRuns) Update(_ context.Context, run domain.IndexRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	r.runs[run.ID] = run
	r.updates = append(r.updates, run)
	if existing, ok := r.latest[run.RepoID]; !ok || existing.ID <= run.ID {
		r.latest[run.RepoID] = run
	}
	return nil
}

func (r *stubIndexRuns) GetByID(_ context.Context, repoID, id int64) (domain.IndexRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return domain.IndexRun{}, r.getErr
	}
	run, ok := r.runs[id]
	if !ok || run.RepoID != repoID {
		return domain.IndexRun{}, domain.NewNotFound("index_run_not_found", "index run not found")
	}
	return run, nil
}

func (r *stubIndexRuns) GetLatest(_ context.Context, repoID int64) (domain.IndexRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.latestErr != nil {
		return domain.IndexRun{}, r.latestErr
	}
	if run, ok := r.latest[repoID]; ok {
		return run, nil
	}
	var (
		found bool
		best  domain.IndexRun
	)
	for _, run := range r.runs {
		if run.RepoID != repoID {
			continue
		}
		if !found || run.ID > best.ID {
			best, found = run, true
		}
	}
	if !found {
		return domain.IndexRun{}, domain.NewNotFound("index_run_not_found", "no index run")
	}
	return best, nil
}

func (r *stubIndexRuns) RecoverStale(context.Context, time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recovered, r.recoverErr
}

func (r *stubIndexRuns) stored(id int64) (domain.IndexRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	return run, ok
}

func (r *stubIndexRuns) updateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.updates)
}

// stubEntities is an in-memory EntityRepository.
type stubEntities struct {
	upsertFileCalls []struct {
		repoID int64
		commit string
		files  []domain.CodeEntity
	}
	upsertFileErr error
	listFilters   []domain.EntityFilter
	listResult    []domain.CodeEntity
	listCursor    string
	listMore      bool
	listErr       error
}

func (e *stubEntities) Upsert(context.Context, domain.CodeEntity) error { return nil }

func (e *stubEntities) UpsertFile(_ context.Context, repoID int64, commit string, files []domain.CodeEntity) error {
	e.upsertFileCalls = append(e.upsertFileCalls, struct {
		repoID int64
		commit string
		files  []domain.CodeEntity
	}{repoID: repoID, commit: commit, files: append([]domain.CodeEntity(nil), files...)})
	return e.upsertFileErr
}

func (e *stubEntities) GetByFactKey(context.Context, int64, string, string) (domain.CodeEntity, error) {
	return domain.CodeEntity{}, domain.ErrNotFound
}

func (e *stubEntities) List(_ context.Context, _ int64, f domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
	e.listFilters = append(e.listFilters, f)
	return e.listResult, e.listCursor, e.listMore, e.listErr
}

func (e *stubEntities) ListByPaths(context.Context, int64, string, []string) ([]domain.CodeEntity, error) {
	return nil, nil
}

func (e *stubEntities) DeleteFact(context.Context, int64, string, string) error { return nil }

// stubChangeSets is a ChangeSetRepository double.
type stubChangeSets struct {
	getOrCreateCalls []domain.CodeChangeSet
}

func (c *stubChangeSets) GetOrCreate(_ context.Context, cs domain.CodeChangeSet) (domain.CodeChangeSet, bool, error) {
	c.getOrCreateCalls = append(c.getOrCreateCalls, cs)
	return cs, true, nil
}

func (c *stubChangeSets) GetByID(context.Context, int64) (domain.CodeChangeSet, error) {
	return domain.CodeChangeSet{}, domain.ErrNotFound
}

// stubIndexState records MarkIndexed calls.
type markIndexedCall struct {
	repoID int64
	commit string
	at     time.Time
}

type stubIndexState struct {
	calls []markIndexedCall
	err   error
}

func (s *stubIndexState) MarkIndexed(_ context.Context, repoID int64, commit string, at time.Time) error {
	s.calls = append(s.calls, markIndexedCall{repoID: repoID, commit: commit, at: at})
	return s.err
}

// stubQueue is an Enqueuer double.
type stubQueue struct {
	jobs  []Job
	calls int
	err   error
}

func (q *stubQueue) Enqueue(job Job) error {
	q.calls++
	if q.err != nil {
		return q.err
	}
	q.jobs = append(q.jobs, job)
	return nil
}

// stubAudit records AuditRecorder calls.
type auditCall struct {
	actorID   int64
	action    string
	subject   string
	detail    map[string]any
	requestID string
}

type stubAudit struct {
	calls []auditCall
	err   error
}

func (a *stubAudit) Record(ctx context.Context, actorID int64, action, subject string, detail map[string]any) error {
	a.calls = append(a.calls, auditCall{
		actorID:   actorID,
		action:    action,
		subject:   subject,
		detail:    detail,
		requestID: platform.RequestID(ctx),
	})
	return a.err
}

// stubIDs is a deterministic IDGenerator.
type stubIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *stubIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

// stubClock is a fixed Clock.
type stubClock struct {
	now time.Time
}

func (c *stubClock) Now() time.Time { return c.now }

// stubVersions is a VersionCatalog double.
type stubVersions struct {
	info VersionInfo
	err  error

	requirements   []RequirementInfo
	requirementErr error
	relations      []RelationInfo
	relationErr    error
	calls          []int64
}

func (v *stubVersions) Get(_ context.Context, versionID int64) (VersionInfo, error) {
	v.calls = append(v.calls, versionID)
	return v.info, v.err
}

func (v *stubVersions) ListRequirements(context.Context, int64) ([]RequirementInfo, error) {
	return v.requirements, v.requirementErr
}

func (v *stubVersions) ListRelations(context.Context, int64) ([]RelationInfo, error) {
	return v.relations, v.relationErr
}

// ---------------------------------------------------------------------------
// IndexService fixtures and tests.
// ---------------------------------------------------------------------------

type indexFixture struct {
	git        *stubGit
	repos      *stubRepos
	runs       *stubIndexRuns
	entities   *stubEntities
	changeSets *stubChangeSets
	state      *stubIndexState
	queue      *stubQueue
	audit      *stubAudit
	ids        *stubIDs
	clock      *stubClock
	svc        *IndexService
}

func newIndexFixture(maxIndexFiles int) *indexFixture {
	now := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	f := &indexFixture{
		git:        &stubGit{head: testCommitA},
		repos:      &stubRepos{},
		runs:       newStubIndexRuns(),
		entities:   &stubEntities{},
		changeSets: &stubChangeSets{},
		state:      &stubIndexState{},
		queue:      &stubQueue{},
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
			IncludeGlobs: []string{"src/**"},
			ExcludeGlobs: []string{"vendor/**"},
			Status:       "active",
		},
	}
	f.svc = NewIndexService(IndexServiceDeps{
		Git:           f.git,
		Repos:         f.repos,
		IndexRuns:     f.runs,
		Entities:      f.entities,
		ChangeSets:    f.changeSets,
		State:         f.state,
		Queue:         f.queue,
		Audit:         f.audit,
		IDs:           f.ids,
		Clock:         f.clock,
		MaxIndexFiles: maxIndexFiles,
	})
	return f
}

// queuedRun starts one run and returns the stored queued run.
func (f *indexFixture) queuedRun(t *testing.T) domain.IndexRun {
	t.Helper()
	run, err := f.svc.Start(context.Background(), 3, 42)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return run
}

// TestIndexServiceStartCreatesQueuedRun pins the trigger semantics: the fixed
// branch tip is resolved, a queued run is created with the run identity and the
// index job is enqueued.
func TestIndexServiceStartCreatesQueuedRun(t *testing.T) {
	f := newIndexFixture(10)

	run, err := f.svc.Start(context.Background(), 3, 42)
	if err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if run.RepoID != 3 || run.CommitSHA != testCommitA || run.Source != "t0" {
		t.Fatalf("run identity = %+v, want repo 3, commit %s, source t0", run, testCommitA)
	}
	if run.Status != domain.RunQueued {
		t.Fatalf("run status = %q, want %q", run.Status, domain.RunQueued)
	}
	if run.CreatedBy != 42 {
		t.Fatalf("run createdBy = %d, want 42", run.CreatedBy)
	}
	if !run.CreatedAt.Equal(f.clock.now) {
		t.Fatalf("run createdAt = %v, want %v", run.CreatedAt, f.clock.now)
	}
	if run.StartedAt != nil || run.FinishedAt != nil {
		t.Fatalf("queued run has started/finished timestamps: %+v", run)
	}
	if len(f.runs.creates) != 1 {
		t.Fatalf("Create calls = %d, want 1", len(f.runs.creates))
	}
	if len(f.git.headCalls) != 1 || f.git.headCalls[0].localPath != "/repo" || f.git.headCalls[0].branch != "main" {
		t.Fatalf("Head calls = %+v, want /repo main", f.git.headCalls)
	}
	if len(f.queue.jobs) != 1 || f.queue.jobs[0] != (Job{Kind: JobKindIndex, ID: run.ID}) {
		t.Fatalf("enqueued jobs = %+v, want one index job %d", f.queue.jobs, run.ID)
	}
	if len(f.state.calls) != 0 || len(f.entities.upsertFileCalls) != 0 {
		t.Fatal("Start must not run the index work")
	}
}

// TestIndexServiceStartRejectsActiveRun pins the repo-scoped single active run
// rule (design spec §6.1/§6.8).
func TestIndexServiceStartRejectsActiveRun(t *testing.T) {
	for _, status := range []string{domain.RunQueued, domain.RunRunning} {
		t.Run(status, func(t *testing.T) {
			f := newIndexFixture(10)
			f.runs.latest[3] = domain.IndexRun{ID: 99, RepoID: 3, Status: status}

			_, err := f.svc.Start(context.Background(), 3, 42)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("Start error = %v, want ErrConflict", err)
			}
			if reason := domain.Reason(err); reason != "index_in_progress" {
				t.Fatalf("reason = %q, want index_in_progress", reason)
			}
			if len(f.runs.creates) != 0 || len(f.queue.jobs) != 0 || len(f.git.headCalls) != 0 {
				t.Fatal("conflicting Start must not resolve HEAD, create a run or enqueue")
			}
		})
	}
}

// TestIndexServiceStartRejectsDisabledRepo pins the admin trigger rule: only an
// active repository can be indexed.
func TestIndexServiceStartRejectsDisabledRepo(t *testing.T) {
	f := newIndexFixture(10)
	info := f.repos.infos[3]
	info.Status = "disabled"
	f.repos.infos[3] = info

	_, err := f.svc.Start(context.Background(), 3, 42)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Start error = %v, want ErrInvalid", err)
	}
	if reason := domain.Reason(err); reason != "code_repository_disabled" {
		t.Fatalf("reason = %q, want code_repository_disabled", reason)
	}
	if len(f.runs.creates) != 0 || len(f.queue.jobs) != 0 {
		t.Fatal("disabled repo must not create or enqueue a run")
	}
}

// TestIndexServiceStartPropagatesHeadFailure proves a failed fixed-branch tip
// resolution does not leave a queued run behind.
func TestIndexServiceStartPropagatesHeadFailure(t *testing.T) {
	f := newIndexFixture(10)
	f.git.headErr = domain.NewInvalid("head", "invalid_head", "branch does not resolve")

	_, err := f.svc.Start(context.Background(), 3, 42)
	if !errors.Is(err, domain.ErrInvalid) || domain.Reason(err) != "invalid_head" {
		t.Fatalf("Start error = %v, want invalid_head", err)
	}
	if len(f.runs.creates) != 0 || len(f.queue.jobs) != 0 {
		t.Fatal("Head failure must not create a run or enqueue")
	}
}

// TestIndexServiceStartQueueFullMarksRunFailed pins the retryable queue-full
// contract: the run is persisted as failed(analysis_queue_full) and the error
// keeps both the domain reason and ErrQueueFull for callers.
func TestIndexServiceStartQueueFullMarksRunFailed(t *testing.T) {
	f := newIndexFixture(10)
	f.queue.err = ErrQueueFull

	run, err := f.svc.Start(context.Background(), 3, 42)
	if err == nil {
		t.Fatal("Start error = nil, want queue-full error")
	}
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Start error = %v, want ErrQueueFull", err)
	}
	if domain.Reason(err) != "analysis_queue_full" {
		t.Fatalf("reason = %q, want analysis_queue_full", domain.Reason(err))
	}
	if run.ID == 0 || run.Status != domain.RunFailed {
		t.Fatalf("returned run = %+v, want failed run", run)
	}
	if run.FinishedAt == nil {
		t.Fatal("failed run has no finished_at")
	}
	if len(run.Errors) != 1 || run.Errors[0].Code != "analysis_queue_full" {
		t.Fatalf("run errors = %+v, want one analysis_queue_full entry", run.Errors)
	}
	stored, ok := f.runs.stored(run.ID)
	if !ok || stored.Status != domain.RunFailed || len(stored.Errors) != 1 {
		t.Fatalf("stored run = %+v (ok=%v), want failed run persisted", stored, ok)
	}

	// The failed run is terminal, so a later trigger is allowed again.
	f.queue.err = nil
	if _, err := f.svc.Start(context.Background(), 3, 42); err != nil {
		t.Fatalf("Start after queue-full failure error = %v, want nil", err)
	}
}

// TestIndexServiceRunIndexesTreeAndMarksIndexed pins the T0 index execution:
// only regular blobs become file entities with git provenance, the resource
// state is marked indexed once and the run finishes done with stats.
func TestIndexServiceRunIndexesTreeAndMarksIndexed(t *testing.T) {
	f := newIndexFixture(10)
	f.git.entries = []domain.TreeEntry{
		{Path: "src/a.ts", Type: "blob", GitObjectID: testBlobA, Mode: "100644", Size: 10},
		{Path: "src/b.ts", Type: "blob", GitObjectID: testBlobB, Mode: "100755", Size: 20},
		{Path: "src/c/d.ts", Type: "blob", GitObjectID: testBlobC, Mode: "100644", Size: 30},
		{Path: "src", Type: "tree", Mode: "040000"},
		{Path: "src/link", Type: "blob", GitObjectID: "4444444444444444444444444444444444444444", Mode: "120000"},
		{Path: "src/sub", Type: "commit", Mode: "160000"},
	}
	run := f.queuedRun(t)

	if err := f.svc.Run(context.Background(), run.ID); err != nil {
		t.Fatalf("Run error = %v", err)
	}

	stored, ok := f.runs.stored(run.ID)
	if !ok {
		t.Fatal("run missing after Run")
	}
	if stored.Status != domain.RunDone {
		t.Fatalf("status = %q, want done", stored.Status)
	}
	if stored.StartedAt == nil || stored.FinishedAt == nil {
		t.Fatalf("done run timestamps = %v/%v, want both set", stored.StartedAt, stored.FinishedAt)
	}
	var stats struct {
		Files          int   `json:"files"`
		Truncated      bool  `json:"truncated"`
		TruncatedFiles int   `json:"truncatedFiles"`
		DurationMs     int64 `json:"durationMs"`
	}
	if err := json.Unmarshal(stored.Stats, &stats); err != nil {
		t.Fatalf("decode stats %s: %v", string(stored.Stats), err)
	}
	if stats.Files != 3 || stats.Truncated || stats.TruncatedFiles != 0 {
		t.Fatalf("stats = %+v, want files=3 truncated=false", stats)
	}
	if len(stored.Errors) != 0 {
		t.Fatalf("done run errors = %+v, want none", stored.Errors)
	}

	if len(f.entities.upsertFileCalls) != 1 {
		t.Fatalf("UpsertFile calls = %d, want 1", len(f.entities.upsertFileCalls))
	}
	call := f.entities.upsertFileCalls[0]
	if call.repoID != 3 || call.commit != testCommitA {
		t.Fatalf("UpsertFile(%d, %s), want (3, %s)", call.repoID, call.commit, testCommitA)
	}
	if len(call.files) != 3 {
		t.Fatalf("indexed files = %d, want 3 (trees and symlinks skipped)", len(call.files))
	}
	for i, want := range []struct {
		path string
		blob string
		size int64
		mode string
	}{
		{"src/a.ts", testBlobA, 10, "100644"},
		{"src/b.ts", testBlobB, 20, "100755"},
		{"src/c/d.ts", testBlobC, 30, "100644"},
	} {
		file := call.files[i]
		if file.Kind != domain.KindFile || file.Source != domain.SourceGit || file.Confidence != domain.ConfidenceHigh {
			t.Fatalf("file[%d] provenance = %s/%s/%s, want file/git/high", i, file.Kind, file.Source, file.Confidence)
		}
		if file.Path != want.path || file.BlobHash != "git-sha1:"+want.blob {
			t.Fatalf("file[%d] = %s/%s, want %s/git-sha1:%s", i, file.Path, file.BlobHash, want.path, want.blob)
		}
		if file.ID == 0 || file.RepoID != 3 || file.Commit != testCommitA {
			t.Fatalf("file[%d] identity = %+v, want snowflake id, repo 3, commit %s", i, file, testCommitA)
		}
		var meta struct {
			Size int64  `json:"size"`
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(file.Meta, &meta); err != nil {
			t.Fatalf("file[%d] meta %s: %v", i, string(file.Meta), err)
		}
		if meta.Size != want.size || meta.Mode != want.mode {
			t.Fatalf("file[%d] meta = %+v, want size=%d mode=%s", i, meta, want.size, want.mode)
		}
	}

	if len(f.state.calls) != 1 {
		t.Fatalf("MarkIndexed calls = %d, want 1", len(f.state.calls))
	}
	if f.state.calls[0].repoID != 3 || f.state.calls[0].commit != testCommitA {
		t.Fatalf("MarkIndexed args = %+v, want repo 3 commit %s", f.state.calls[0], testCommitA)
	}
	if !f.state.calls[0].at.Equal(*stored.FinishedAt) {
		t.Fatalf("MarkIndexed at = %v, want run finished_at %v", f.state.calls[0].at, *stored.FinishedAt)
	}

	if len(f.git.treeCalls) != 1 {
		t.Fatalf("Tree calls = %d, want 1", len(f.git.treeCalls))
	}
	treeCall := f.git.treeCalls[0]
	if treeCall.localPath != "/repo" || treeCall.commit != testCommitA || treeCall.prefix != "" {
		t.Fatalf("Tree args = %+v, want /repo @ %s prefix empty", treeCall, testCommitA)
	}
	if len(treeCall.include) != 1 || treeCall.include[0] != "src/**" ||
		len(treeCall.exclude) != 1 || treeCall.exclude[0] != "vendor/**" {
		t.Fatalf("Tree globs = include %v exclude %v, want repo globs", treeCall.include, treeCall.exclude)
	}
}

// TestIndexTruncatesAtMaxFiles pins the MaxIndexFiles cap and its stats.
func TestIndexTruncatesAtMaxFiles(t *testing.T) {
	f := newIndexFixture(2)
	f.git.entries = []domain.TreeEntry{
		{Path: "a.ts", Type: "blob", GitObjectID: testBlobA, Mode: "100644", Size: 1},
		{Path: "b.ts", Type: "blob", GitObjectID: testBlobB, Mode: "100644", Size: 2},
		{Path: "c.ts", Type: "blob", GitObjectID: testBlobC, Mode: "100644", Size: 3},
		{Path: "d.ts", Type: "blob", GitObjectID: "4444444444444444444444444444444444444444", Mode: "100644", Size: 4},
		{Path: "e.ts", Type: "blob", GitObjectID: "5555555555555555555555555555555555555555", Mode: "100644", Size: 5},
	}
	run := f.queuedRun(t)

	if err := f.svc.Run(context.Background(), run.ID); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	stored, _ := f.runs.stored(run.ID)
	var stats struct {
		Files          int  `json:"files"`
		Truncated      bool `json:"truncated"`
		TruncatedFiles int  `json:"truncatedFiles"`
	}
	if err := json.Unmarshal(stored.Stats, &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Files != 2 || !stats.Truncated || stats.TruncatedFiles <= 0 {
		t.Fatalf("stats = %+v, want files=2 truncated=true truncatedFiles>0", stats)
	}
	if len(f.entities.upsertFileCalls) != 1 || len(f.entities.upsertFileCalls[0].files) != 2 {
		t.Fatalf("UpsertFile files = %+v, want exactly 2", f.entities.upsertFileCalls)
	}
	if len(f.state.calls) != 1 {
		t.Fatalf("MarkIndexed calls = %d, want 1 on a truncated run", len(f.state.calls))
	}
}

// TestIndexServiceRunPagesThroughTree proves the service drains the gitx tree
// cursor instead of indexing only the first page.
func TestIndexServiceRunPagesThroughTree(t *testing.T) {
	f := newIndexFixture(250)
	head := testCommitD
	f.git.head = head
	for i := 0; i < 250; i++ {
		f.git.entries = append(f.git.entries, domain.TreeEntry{
			Path:        fmt.Sprintf("src/file-%03d.ts", i),
			Type:        "blob",
			GitObjectID: strings.Repeat("a", 40),
			Mode:        "100644",
			Size:        int64(i),
		})
	}
	run := f.queuedRun(t)

	if err := f.svc.Run(context.Background(), run.ID); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(f.git.treeCalls) < 2 {
		t.Fatalf("Tree calls = %d, want at least 2 pages", len(f.git.treeCalls))
	}
	if len(f.entities.upsertFileCalls) != 1 || len(f.entities.upsertFileCalls[0].files) != 250 {
		t.Fatalf("indexed files = %d, want 250", len(f.entities.upsertFileCalls[0].files))
	}
}

// TestIndexServiceRunMarksFailedOnError pins the failed terminal state for each
// execution error source.
func TestIndexServiceRunMarksFailedOnError(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(f *indexFixture)
		afterStart func(f *indexFixture)
		wantCode   string
	}{
		{
			name:     "tree failure",
			setup:    func(f *indexFixture) { f.git.treeErr = errors.New("git blew up") },
			wantCode: "internal",
		},
		{
			name: "domain tree failure keeps its reason",
			setup: func(f *indexFixture) {
				f.git.treeErr = domain.NewInvalid("path", "path_not_found", "missing path")
			},
			wantCode: "path_not_found",
		},
		{
			name:     "upsert failure",
			setup:    func(f *indexFixture) { f.entities.upsertFileErr = errors.New("db write failed") },
			wantCode: "internal",
		},
		{
			name:     "mark indexed failure",
			setup:    func(f *indexFixture) { f.state.err = errors.New("resource write failed") },
			wantCode: "internal",
		},
		{
			name: "repo disabled after start",
			afterStart: func(f *indexFixture) {
				info := f.repos.infos[3]
				info.Status = "disabled"
				f.repos.infos[3] = info
			},
			wantCode: "code_repository_disabled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIndexFixture(10)
			if tt.setup != nil {
				tt.setup(f)
			}
			run := f.queuedRun(t)
			if tt.afterStart != nil {
				tt.afterStart(f)
			}

			err := f.svc.Run(context.Background(), run.ID)
			if err == nil {
				t.Fatal("Run error = nil, want an error")
			}
			stored, _ := f.runs.stored(run.ID)
			if stored.Status != domain.RunFailed {
				t.Fatalf("status = %q, want failed", stored.Status)
			}
			if stored.FinishedAt == nil {
				t.Fatal("failed run has no finished_at")
			}
			if len(stored.Errors) == 0 || stored.Errors[0].Code != tt.wantCode {
				t.Fatalf("errors = %+v, want first code %q", stored.Errors, tt.wantCode)
			}
		})
	}
}

// TestIndexServiceRunMarksTimeout pins the worker-deadline terminal state.
func TestIndexServiceRunMarksTimeout(t *testing.T) {
	f := newIndexFixture(10)
	run := f.queuedRun(t)
	f.git.treeErr = context.DeadlineExceeded

	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	if err := f.svc.Run(ctx, run.ID); err == nil {
		t.Fatal("Run error = nil, want timeout error")
	}
	stored, _ := f.runs.stored(run.ID)
	if stored.Status != domain.RunFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
	if len(stored.Errors) == 0 || stored.Errors[0].Code != "analysis_timeout" {
		t.Fatalf("errors = %+v, want analysis_timeout", stored.Errors)
	}
}

// TestIndexServiceRunRecoversPanic proves a panicking Git adapter is recorded as
// a failed run instead of killing the worker goroutine.
func TestIndexServiceRunRecoversPanic(t *testing.T) {
	f := newIndexFixture(10)
	run := f.queuedRun(t)
	f.git.treeFunc = func(context.Context) (domain.TreePage, error) {
		panic("tree adapter exploded")
	}

	if err := f.svc.Run(context.Background(), run.ID); err == nil {
		t.Fatal("Run error = nil, want panic error")
	}
	stored, _ := f.runs.stored(run.ID)
	if stored.Status != domain.RunFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
	if len(stored.Errors) == 0 || stored.Errors[0].Code != "internal" {
		t.Fatalf("errors = %+v, want internal", stored.Errors)
	}
}

// TestIndexServiceRunSkipsTerminalRun pins the per-run terminal state: a late
// duplicate job cannot rewrite a done run.
func TestIndexServiceRunSkipsTerminalRun(t *testing.T) {
	f := newIndexFixture(10)
	run := f.queuedRun(t)
	f.runs.mu.Lock()
	done := f.runs.runs[run.ID]
	done.Status = domain.RunDone
	f.runs.runs[run.ID] = done
	f.runs.mu.Unlock()
	updatesBefore := f.runs.updateCount()

	if err := f.svc.Run(context.Background(), run.ID); err != nil {
		t.Fatalf("Run(done) error = %v", err)
	}
	if f.runs.updateCount() != updatesBefore {
		t.Fatalf("Run(done) updates = %d, want none", f.runs.updateCount()-updatesBefore)
	}
	if len(f.entities.upsertFileCalls) != 0 {
		t.Fatal("Run(done) must not index again")
	}
}

// TestIndexServiceGetAndGetLatest pins the read pass-through used by the HTTP
// handlers.
func TestIndexServiceGetAndGetLatest(t *testing.T) {
	f := newIndexFixture(10)
	run := f.queuedRun(t)

	got, err := f.svc.Get(context.Background(), 3, run.ID)
	if err != nil || got.ID != run.ID {
		t.Fatalf("Get = %+v, %v; want run %d", got, err, run.ID)
	}
	latest, err := f.svc.GetLatest(context.Background(), 3)
	if err != nil || latest.ID != run.ID {
		t.Fatalf("GetLatest = %+v, %v; want run %d", latest, err, run.ID)
	}
	if _, err := f.svc.Get(context.Background(), 3, run.ID+1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
}
