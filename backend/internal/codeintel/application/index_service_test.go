package codeintelapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

const (
	testCommitA = "1111111111111111111111111111111111111111"
	testCommitB = "2222222222222222222222222222222222222222"
)

type indexFixture struct {
	service   *IndexService
	runs      *fakeIndexRepo
	entities  *fakeEntityRepo
	git       *fakeGit
	versions  *fakeVersionReader
	resources *fakeResourceReader
	queue     *fakeWorkQueue
	events    *fakeEvents
	audit     *fakeAudit
}

func newIndexFixture(t *testing.T) *indexFixture {
	t.Helper()
	fixture := &indexFixture{
		runs:      newFakeIndexRepo(),
		entities:  &fakeEntityRepo{},
		git:       &fakeGit{resolveCommit: testCommitA, head: testCommitB, ancestor: true},
		versions:  &fakeVersionReader{bindings: []VersionSUTBinding{{VersionSUTID: 11, VersionID: 1, RepoID: 10, Branch: "main"}}},
		resources: &fakeResourceReader{repos: map[int64]RepoRef{10: {ID: 10, Name: "repo", Path: "/tmp/repo", Branch: "main"}}},
		queue:     &fakeWorkQueue{},
		events:    &fakeEvents{},
		audit:     &fakeAudit{},
	}
	fixture.service = NewIndexService(IndexDeps{
		Runs:      fixture.runs,
		Entities:  fixture.entities,
		Git:       fixture.git,
		Versions:  fixture.versions,
		Resources: fixture.resources,
		Queue:     fixture.queue,
		Events:    fixture.events,
		Audit:     fixture.audit,
		Settings: &fakeSettings{
			ints: map[string]int64{"codeintel.max_index_files": 5000},
			raws: map[string]json.RawMessage{"codeintel.exclude_globs": json.RawMessage(`["**/node_modules/**"]`)},
		},
		Clock:      fakeClock{now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		IDs:        &fakeIDs{},
		InstanceID: "codeintel-test",
	})
	return fixture
}

func requireAPIError(t *testing.T, err error, status int, code string) *platform.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("error = %d/%s, want %d/%s (%v)", apiErr.Status, apiErr.Code, status, code, err)
	}
	return apiErr
}

func TestIndexStartRejectsUnboundRepo(t *testing.T) {
	fixture := newIndexFixture(t)
	_, _, err := fixture.service.Start(context.Background(), 1, 99, "")
	requireAPIError(t, err, http.StatusUnprocessableEntity, "repo_not_bound")
}

func TestIndexStartRejectsActiveRun(t *testing.T) {
	fixture := newIndexFixture(t)
	fixture.runs.runs[3] = codeintel.IndexRun{ID: 3, RepoID: 10, Status: string(codeintel.IndexQueued)}
	fixture.runs.order = []int64{3}
	_, _, err := fixture.service.Start(context.Background(), 1, 10, testCommitA)
	apiErr := requireAPIError(t, err, http.StatusConflict, "state_conflict")
	if apiErr.Details["reason"] != "index_in_progress" {
		t.Fatalf("conflict reason = %v", apiErr.Details["reason"])
	}
}

func TestIndexStartReusesDoneRun(t *testing.T) {
	fixture := newIndexFixture(t)
	fixture.runs.runs[5] = codeintel.IndexRun{ID: 5, RepoID: 10, CommitSHA: testCommitA, Status: string(codeintel.IndexDone)}
	fixture.runs.order = []int64{5}
	run, created, err := fixture.service.Start(context.Background(), 1, 10, testCommitA)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if created {
		t.Fatal("reused run must not be created")
	}
	if run.ID != 5 {
		t.Fatalf("reused run = %+v", run)
	}
	if len(fixture.queue.ensured) != 0 {
		t.Fatalf("reuse enqueued work items: %+v", fixture.queue.ensured)
	}
}

func TestIndexStartCreatesRunAndWorkItem(t *testing.T) {
	fixture := newIndexFixture(t)
	fixture.versions.bindings[0].CodeBaselineCommit = testCommitA
	run, created, err := fixture.service.Start(context.Background(), 1, 10, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !created {
		t.Fatal("new run must be created")
	}
	if run.Status != string(codeintel.IndexQueued) || run.CommitSHA != testCommitA || run.RepoID != 10 || run.VersionID != 1 {
		t.Fatalf("run = %+v", run)
	}
	if run.ID == 0 || run.CreatedAt.IsZero() {
		t.Fatalf("run id/timestamps = %+v", run)
	}
	if len(fixture.queue.ensured) != 1 {
		t.Fatalf("work items = %+v", fixture.queue.ensured)
	}
	work := fixture.queue.ensured[0].item
	if work.Team != "code" || work.Type != "index.repo" || work.Priority != 50 ||
		work.MaxAttempts != 3 || work.VersionID != 1 || work.DedupeKey != "code:index:"+platformid.Format(run.ID) {
		t.Fatalf("work item = %+v", work)
	}
	payload, ok := work.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", work.Payload)
	}
	if payload["commit_sha"] != testCommitA || payload["repo_id"] != "10" || payload["index_run_id"] != platformid.Format(run.ID) {
		t.Fatalf("payload = %+v", payload)
	}
	if len(fixture.audit.entries) != 1 || fixture.audit.entries[0].Action != "code.index.start" {
		t.Fatalf("audit = %+v", fixture.audit.entries)
	}
}

func TestIndexStartResolvesExplicitCommitAndHead(t *testing.T) {
	fixture := newIndexFixture(t)
	run, _, err := fixture.service.Start(context.Background(), 1, 10, testCommitB)
	if err != nil {
		t.Fatalf("Start explicit: %v", err)
	}
	if run.CommitSHA != testCommitA {
		t.Fatalf("resolved explicit commit = %q", run.CommitSHA)
	}
	if len(fixture.git.resolvedRefs) != 1 || fixture.git.resolvedRefs[0] != testCommitB {
		t.Fatalf("resolved refs = %v", fixture.git.resolvedRefs)
	}

	fixture.runs.runs = map[int64]codeintel.IndexRun{}
	fixture.runs.order = nil
	fixture.git.resolveCommit = testCommitB
	run, _, err = fixture.service.Start(context.Background(), 1, 10, "")
	if err != nil {
		t.Fatalf("Start head: %v", err)
	}
	if run.CommitSHA != testCommitB {
		t.Fatalf("branch head commit = %q", run.CommitSHA)
	}
}

func TestIndexExecuteIndexesFilesAndBackfillsBaseline(t *testing.T) {
	fixture := newIndexFixture(t)
	run := codeintel.IndexRun{
		ID: 7, VersionID: 1, RepoID: 10, CommitSHA: testCommitA, Branch: "main",
		Status: string(codeintel.IndexQueued),
		Stats:  json.RawMessage(`{}`), Errors: json.RawMessage(`[]`),
	}
	fixture.runs.runs[7] = run
	fixture.runs.order = []int64{7}
	fixture.git.tree = codeintel.TreeResult{
		Entries: []codeintel.TreeEntry{
			{Path: "src/a.ts", Mode: "100644", Type: "blob", BlobSHA: "abc", Size: 10},
			{Path: "src/link.ts", Mode: "120000", Type: "blob", BlobSHA: "def", Unsupported: true},
		},
		Total: 2,
	}
	if err := fixture.service.Execute(context.Background(), 7); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	stored := fixture.runs.runs[7]
	if stored.Status != string(codeintel.IndexDone) || stored.FilesTotal != 2 || stored.FilesIndexed != 2 {
		t.Fatalf("stored run = %+v", stored)
	}
	if stored.StartedAt == nil || stored.FinishedAt == nil {
		t.Fatalf("run timestamps = %+v", stored)
	}
	if len(fixture.entities.fileCalls) != 1 || fixture.entities.fileCalls[0].entryCount != 2 ||
		fixture.entities.fileCalls[0].runID != 7 || fixture.entities.fileCalls[0].unsupported != 1 {
		t.Fatalf("entity calls = %+v", fixture.entities.fileCalls)
	}
	if len(fixture.versions.setCalls) != 1 || fixture.versions.setCalls[0] != (baselineSetCall{versionSUTID: 11, commit: testCommitA}) {
		t.Fatalf("baseline calls = %+v", fixture.versions.setCalls)
	}
	types := fixture.events.types()
	if len(types) != 2 || types[0] != "code.index_started" || types[1] != "code.index_finished" {
		t.Fatalf("events = %v", types)
	}
}

func TestIndexExecuteFailureMarksRunFailed(t *testing.T) {
	fixture := newIndexFixture(t)
	run := codeintel.IndexRun{ID: 8, VersionID: 1, RepoID: 10, CommitSHA: testCommitA, Status: string(codeintel.IndexQueued)}
	fixture.runs.runs[8] = run
	fixture.runs.order = []int64{8}
	fixture.git.treeErr = &platform.Error{Status: http.StatusServiceUnavailable, Code: "git_unavailable", Message: "git unavailable", Retryable: true}
	err := fixture.service.Execute(context.Background(), 8)
	requireAPIError(t, err, http.StatusServiceUnavailable, "git_unavailable")
	stored := fixture.runs.runs[8]
	if stored.Status != string(codeintel.IndexFailed) {
		t.Fatalf("failed run status = %q", stored.Status)
	}
	var recorded []map[string]any
	if err := json.Unmarshal(stored.Errors, &recorded); err != nil {
		t.Fatalf("errors JSON: %v (%s)", err, stored.Errors)
	}
	if len(recorded) != 1 || recorded[0]["code"] != "git_unavailable" {
		t.Fatalf("errors = %+v", recorded)
	}
	if strings.Contains(string(stored.Errors), "/tmp/repo") {
		t.Fatalf("errors leak the repository path: %s", stored.Errors)
	}
	types := fixture.events.types()
	if len(types) != 2 || types[1] != "code.index_failed" {
		t.Fatalf("events = %v", types)
	}
}

func TestIndexRepoStatusAndTree(t *testing.T) {
	fixture := newIndexFixture(t)
	fixture.versions.bindings[0].CodeBaselineCommit = testCommitA
	fixture.runs.runs[2] = codeintel.IndexRun{ID: 2, RepoID: 10, CommitSHA: testCommitA, Status: string(codeintel.IndexDone)}
	fixture.runs.order = []int64{2}
	statuses, err := fixture.service.RepoStatus(context.Background(), 1)
	if err != nil {
		t.Fatalf("RepoStatus: %v", err)
	}
	if len(statuses) != 1 || statuses[0].RepoID != 10 || statuses[0].BaselineCommit != testCommitA ||
		statuses[0].Latest == nil || statuses[0].Latest.ID != 2 || statuses[0].IndexInProgress {
		t.Fatalf("statuses = %+v", statuses)
	}

	fixture.entities.tree = []codeintel.Entity{{ID: 31, RepoID: 10, CommitSHA: testCommitA, Kind: codeintel.EntityFile, Path: "src/a.ts", Source: "git"}}
	entities, cursor, more, err := fixture.service.Tree(context.Background(), 10, testCommitA, "", "", 50)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(entities) != 1 || cursor != "" || more {
		t.Fatalf("tree = %+v cursor=%q more=%v", entities, cursor, more)
	}
	if _, _, _, err := fixture.service.Tree(context.Background(), 10, "not-a-sha", "", "", 50); err == nil {
		t.Fatal("invalid commit accepted")
	}
}

// --- QueryService ---------------------------------------------------------------

type queryFixture struct {
	service   *QueryService
	entities  *fakeEntityRepo
	git       *fakeGit
	resources *fakeResourceReader
	audit     *fakeAudit
}

func newQueryFixture() *queryFixture {
	fixture := &queryFixture{
		entities:  &fakeEntityRepo{},
		git:       &fakeGit{head: testCommitB},
		resources: &fakeResourceReader{repos: map[int64]RepoRef{10: {ID: 10, Name: "repo", Path: "/tmp/repo", Branch: "main", Include: []string{"src/**"}}}},
		audit:     &fakeAudit{},
	}
	fixture.service = NewQueryService(QueryDeps{
		Entities:  fixture.entities,
		Git:       fixture.git,
		Resources: fixture.resources,
		Audit:     fixture.audit,
		Commits:   fixture.git,
		Bulk:      fixture.git,
		Settings: &fakeSettings{
			ints: map[string]int64{"codeintel.max_diff_bytes": 262144},
		},
	})
	return fixture
}

func TestQueryFileAuditsWithoutContent(t *testing.T) {
	fixture := newQueryFixture()
	fixture.git.windows = map[string]codeintel.FileWindow{
		"src/a.ts": {Path: "src/a.ts", Commit: testCommitA, Lines: []string{"one", "two"}, StartLine: 1, EndLine: 2, TotalLines: 2},
	}
	window, err := fixture.service.File(context.Background(), 10, testCommitA, "src/a.ts", 0, 0)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if len(window.Lines) != 2 {
		t.Fatalf("window = %+v", window)
	}
	if len(fixture.audit.entries) != 1 || fixture.audit.entries[0].Action != "code.file.read" {
		t.Fatalf("audit = %+v", fixture.audit.entries)
	}
	entry := fixture.audit.entries[0]
	if entry.Metadata["path"] != "src/a.ts" || entry.Metadata["commit_sha"] != testCommitA {
		t.Fatalf("audit metadata = %+v", entry.Metadata)
	}
}

func TestQueryDiffPaginatesByPath(t *testing.T) {
	fixture := newQueryFixture()
	fixture.git.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{
		{Path: "src/a.ts"}, {Path: "src/b.ts"}, {Path: "src/c.ts"},
	}}
	page, cursor, more, err := fixture.service.Diff(context.Background(), 10, testCommitA, testCommitB, "", "", 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(page.Files) != 2 || !more || cursor != "src/b.ts" {
		t.Fatalf("page = %+v cursor=%q more=%v", page.Files, cursor, more)
	}
	next, nextCursor, nextMore, err := fixture.service.Diff(context.Background(), 10, testCommitA, testCommitB, "", cursor, 2)
	if err != nil {
		t.Fatalf("Diff page 2: %v", err)
	}
	if len(next.Files) != 1 || nextMore || nextCursor != "" || next.Files[0].Path != "src/c.ts" {
		t.Fatalf("page 2 = %+v cursor=%q more=%v", next.Files, nextCursor, nextMore)
	}
}

func TestQueryFileDiffAndCommit(t *testing.T) {
	fixture := newQueryFixture()
	fixture.git.fileDiffs = map[string]codeintel.FileDiff{"src/a.ts": {Path: "src/a.ts", Patch: "diff --git a/src/a.ts"}}
	fileDiff, err := fixture.service.FileDiff(context.Background(), 10, testCommitA, testCommitB, "src/a.ts")
	if err != nil {
		t.Fatalf("FileDiff: %v", err)
	}
	if !strings.Contains(fileDiff.Patch, "diff --git") {
		t.Fatalf("file diff = %+v", fileDiff)
	}
	fixture.git.commits = map[string]codeintel.CommitInfo{testCommitA: {
		SHA: testCommitA, Message: "commit A", AuthorName: "Dev",
		AuthoredAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CommittedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}}
	commit, err := fixture.service.Commit(context.Background(), 10, testCommitA)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commit.SHA != testCommitA || commit.Branch != "main" {
		t.Fatalf("commit view = %+v", commit)
	}
	if commit.IsHead {
		t.Fatalf("commit %s must not be head %s", commit.SHA, fixture.git.head)
	}
}

// --- worker ---------------------------------------------------------------------

type recordedHandler struct {
	calls []WorkItem
	err   error
}

func (h *recordedHandler) Handle(_ context.Context, item WorkItem) error {
	h.calls = append(h.calls, item)
	return h.err
}

func testWorker(queue *fakeWorkQueue, handlers map[string]Handler, options ...WorkerOption) *Worker {
	base := []WorkerOption{
		WithWorkerInstanceID("codeintel-test"),
		WithWorkerPollInterval(5 * time.Millisecond),
		WithWorkerHeartbeatInterval(5 * time.Millisecond),
		WithWorkerRecoveryInterval(5 * time.Millisecond),
		WithWorkerConcurrency(2),
	}
	return NewWorker(queue, handlers, append(base, options...)...)
}

func TestWorkerClaimsOnlyCodeTeam(t *testing.T) {
	queue := &fakeWorkQueue{items: []WorkItem{{ID: 1, Type: "index.repo", LeaseEpoch: 2}}}
	worker := testWorker(queue, map[string]Handler{"index.repo": &recordedHandler{}})
	item, ok, err := worker.claim(context.Background())
	if err != nil || !ok || item.ID != 1 {
		t.Fatalf("claim = %+v %v %v", item, ok, err)
	}
	if len(queue.claims) != 1 || len(queue.claims[0]) != 1 || queue.claims[0][0] != "code" {
		t.Fatalf("claimed teams = %+v", queue.claims)
	}
}

func TestWorkerDispatchesAndCompletes(t *testing.T) {
	queue := &fakeWorkQueue{}
	handler := &recordedHandler{}
	worker := testWorker(queue, map[string]Handler{"index.repo": handler})
	worker.process(context.Background(), WorkItem{ID: 9, Type: "index.repo", LeaseEpoch: 3})
	if len(handler.calls) != 1 || handler.calls[0].ID != 9 {
		t.Fatalf("handler calls = %+v", handler.calls)
	}
	if len(queue.markRunning) != 1 || queue.markRunning[0] != 9 || len(queue.completed) != 1 || queue.completed[0] != 9 {
		t.Fatalf("queue calls = %+v / %+v", queue.markRunning, queue.completed)
	}
	if len(queue.failed) != 0 {
		t.Fatalf("failed = %+v", queue.failed)
	}
}

func TestWorkerRetryableFailureKeepsBackoff(t *testing.T) {
	queue := &fakeWorkQueue{}
	handler := &recordedHandler{err: &platform.Error{Status: http.StatusServiceUnavailable, Code: "git_unavailable", Message: "unavailable", Retryable: true}}
	worker := testWorker(queue, map[string]Handler{"index.repo": handler})
	worker.process(context.Background(), WorkItem{ID: 4, Type: "index.repo", LeaseEpoch: 1})
	if len(queue.failed) != 1 || !queue.failed[0].retryable {
		t.Fatalf("fail calls = %+v", queue.failed)
	}
}

func TestWorkerNonRetryableFailureIsDead(t *testing.T) {
	queue := &fakeWorkQueue{}
	handler := &recordedHandler{err: errors.New("invalid payload")}
	worker := testWorker(queue, map[string]Handler{"code.analyze": handler})
	worker.process(context.Background(), WorkItem{ID: 4, Type: "code.analyze", LeaseEpoch: 1})
	if len(queue.failed) != 1 || queue.failed[0].retryable {
		t.Fatalf("fail calls = %+v", queue.failed)
	}
}

func TestWorkerUnknownTypeFailsWithoutHandler(t *testing.T) {
	queue := &fakeWorkQueue{}
	worker := testWorker(queue, map[string]Handler{})
	worker.process(context.Background(), WorkItem{ID: 5, Type: "unknown.type", LeaseEpoch: 1})
	if len(queue.failed) != 1 || queue.failed[0].retryable {
		t.Fatalf("fail calls = %+v", queue.failed)
	}
}

func TestWorkerHeartbeatRenewsTrackedLeases(t *testing.T) {
	queue := &fakeWorkQueue{}
	worker := testWorker(queue, map[string]Handler{})
	worker.track(21, &workerRun{epoch: 7})
	worker.heartbeatAll(context.Background())
	if len(queue.renewed) != 1 || queue.renewed[0] != (renewCall{id: 21, epoch: 7}) {
		t.Fatalf("renew calls = %+v", queue.renewed)
	}
}

// TestWorkerDrainGateStopsClaims pins P7 §4.5 for the codeintel worker: a
// draining replica never claims new code-team items (in-flight keeps running).
func TestWorkerDrainGateStopsClaims(t *testing.T) {
	queue := &fakeWorkQueue{items: []WorkItem{{ID: 31, Type: "index.repo", LeaseEpoch: 2}}}
	handler := &recordedHandler{}
	draining := true
	worker := testWorker(queue, map[string]Handler{"index.repo": handler},
		WithWorkerDrainGate(func() bool { return draining }))
	var inflight sync.WaitGroup
	sem := make(chan struct{}, 1)

	worker.drain(context.Background(), sem, &inflight)
	inflight.Wait()
	if len(queue.claims) != 0 {
		t.Fatalf("claims while draining = %+v, want none", queue.claims)
	}

	draining = false
	worker.drain(context.Background(), sem, &inflight)
	inflight.Wait()
	if len(queue.claims) != 1 || len(handler.calls) != 1 || handler.calls[0].ID != 31 {
		t.Fatalf("claims after drain cleared = %+v handler=%+v", queue.claims, handler.calls)
	}
}

// TestWorkerDrainGateStopsRunClaims proves the same gate through the public Run
// loop: a replica that starts draining stops claiming even with queued work.
func TestWorkerDrainGateStopsRunClaims(t *testing.T) {
	queue := &fakeWorkQueue{items: []WorkItem{
		{ID: 41, Type: "index.repo", LeaseEpoch: 1},
		{ID: 42, Type: "index.repo", LeaseEpoch: 1},
	}}
	started := make(chan int64, 4)
	handler := &signalingHandler{started: started}
	var draining atomic.Bool
	worker := NewWorker(queue, map[string]Handler{"index.repo": handler},
		WithWorkerInstanceID("codeintel-drain-test"),
		WithWorkerPollInterval(2*time.Millisecond),
		WithWorkerHeartbeatInterval(time.Hour),
		WithWorkerRecoveryInterval(time.Hour),
		WithWorkerConcurrency(1),
		WithWorkerDrainGate(draining.Load),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("worker never claimed its first item")
	}
	draining.Store(true)
	time.Sleep(40 * time.Millisecond)
	cancel()
	<-done
	// The worker has stopped, so the fake queue is no longer mutated.
	if len(queue.claims) != 1 {
		t.Fatalf("claims after drain = %d, want only the first (in-flight) claim", len(queue.claims))
	}
}

// signalingHandler reports each handled item without racing the worker.
type signalingHandler struct {
	started chan int64
}

func (h *signalingHandler) Handle(_ context.Context, item WorkItem) error {
	select {
	case h.started <- item.ID:
	default:
	}
	return nil
}

func TestWorkerRunStopsOnContextCancel(t *testing.T) {
	queue := &fakeWorkQueue{}
	worker := testWorker(queue, map[string]Handler{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run canceled: %v", err)
	}
}
