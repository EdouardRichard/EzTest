package application

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// version_change_service_test.go pins the P8 搂4.2/搂7.2 version_change snapshot
// contract: one snapshot per (version, batch), generated at read time when an
// older process did not persist it, upserted idempotently, summarised without
// any code body, and announced once through the code.changed notifier.

const (
	vcCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	vcCommitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	vcCommitC = "cccccccccccccccccccccccccccccccccccccccc"
)

// vcDiffKey names one base..head diff in the fake Git adapter.
func vcDiffKey(base, head string) string { return base + ".." + head }

// vcGit is a GitReader double that serves one deterministic diff per commit
// pair and records every call.
type vcGit struct {
	*stubGit
	diffs map[string]domain.ChangeSetData
	errs  map[string]error
	calls []gitDiffCall
}

func (g *vcGit) Diff(_ context.Context, localPath, base, head string) (domain.ChangeSetData, error) {
	g.calls = append(g.calls, gitDiffCall{localPath: localPath, base: base, head: head})
	if err, ok := g.errs[vcDiffKey(base, head)]; ok {
		return domain.ChangeSetData{}, err
	}
	return g.diffs[vcDiffKey(base, head)], nil
}

// vcEntities is an EntityRepository double that serves the entities of one
// commit/path set and records the requested paths.
type vcEntities struct {
	byCommit map[string]map[string][]domain.CodeEntity
	calls    [][]string
}

func (e *vcEntities) Upsert(context.Context, domain.CodeEntity) error { return nil }
func (e *vcEntities) UpsertFile(context.Context, int64, string, []domain.CodeEntity) error {
	return nil
}
func (e *vcEntities) GetByFactKey(context.Context, int64, string, string) (domain.CodeEntity, error) {
	return domain.CodeEntity{}, domain.ErrNotFound
}
func (e *vcEntities) List(context.Context, int64, domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
	return nil, "", false, nil
}
func (e *vcEntities) ListByPaths(_ context.Context, _ int64, commit string, paths []string) ([]domain.CodeEntity, error) {
	e.calls = append(e.calls, append([]string(nil), paths...))
	byPath := e.byCommit[commit]
	entities := make([]domain.CodeEntity, 0)
	for _, path := range paths {
		entities = append(entities, byPath[path]...)
	}
	return entities, nil
}
func (e *vcEntities) DeleteFact(context.Context, int64, string, string) error { return nil }

// vcStore is a VersionChangeRepository double. Upsert enforces the same
// (version_id, batch_id) uniqueness as the PostgreSQL adapter: a repeated write
// returns the stored row and created=false.
type vcStore struct {
	batches   []domain.VersionBatch
	changes   map[int64]domain.VersionChange
	upserts   []domain.VersionChange
	batchCall int

	listErr    error
	upsertErr  error
	batchesErr error
}

func newVCStore() *vcStore {
	return &vcStore{changes: make(map[int64]domain.VersionChange)}
}

func (s *vcStore) ListByVersion(_ context.Context, versionID int64) ([]domain.VersionChange, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	changes := make([]domain.VersionChange, 0, len(s.changes))
	for _, change := range s.changes {
		if change.VersionID == versionID {
			changes = append(changes, change)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].BatchID < changes[j].BatchID })
	return changes, nil
}

func (s *vcStore) Upsert(_ context.Context, change domain.VersionChange) (domain.VersionChange, bool, error) {
	s.upserts = append(s.upserts, change)
	if s.upsertErr != nil {
		return domain.VersionChange{}, false, s.upsertErr
	}
	if stored, ok := s.changes[change.BatchID]; ok {
		return stored, false, nil
	}
	s.changes[change.BatchID] = change
	return change, true, nil
}

func (s *vcStore) ListBatches(_ context.Context, versionID int64) ([]domain.VersionBatch, error) {
	s.batchCall++
	if s.batchesErr != nil {
		return nil, s.batchesErr
	}
	batches := make([]domain.VersionBatch, 0, len(s.batches))
	for _, batch := range s.batches {
		if batch.VersionID == versionID {
			batches = append(batches, batch)
		}
	}
	return batches, nil
}

// vcNotifier records the version-scoped code.changed events.
type vcNotifier struct {
	events   []VersionChangeEvent
	versions []int64
	err      error
}

func (n *vcNotifier) VersionChanged(_ context.Context, versionID int64, event VersionChangeEvent) error {
	n.versions = append(n.versions, versionID)
	n.events = append(n.events, event)
	return n.err
}

// versionChangeFixture assembles the service over deterministic doubles.
type versionChangeFixture struct {
	git      *vcGit
	repos    *stubRepos
	versions *stubVersions
	entities *vcEntities
	anchors  *anchorStore
	store    *vcStore
	notifier *vcNotifier
	ids      *stubIDs
	clock    *stubClock
	svc      *VersionChangeService
}

func newVersionChangeFixture(t *testing.T) *versionChangeFixture {
	t.Helper()
	f := &versionChangeFixture{
		git: &vcGit{
			stubGit: &stubGit{},
			diffs:   make(map[string]domain.ChangeSetData),
			errs:    make(map[string]error),
		},
		repos: &stubRepos{infos: map[int64]RepoInfo{
			3: {
				ID: 3, Key: "web", Name: "Web", LocalPath: "/repo",
				FixedBranch: "main", Status: "active",
			},
		}},
		versions: &stubVersions{info: VersionInfo{
			ID: 9, RepoID: 3, HasRepo: true, BaselineCommit: vcCommitA,
		}},
		entities: &vcEntities{byCommit: make(map[string]map[string][]domain.CodeEntity)},
		anchors:  newAnchorStore(),
		store:    newVCStore(),
		notifier: &vcNotifier{},
		ids:      &stubIDs{next: 1000},
		clock:    &stubClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)},
	}
	f.svc = NewVersionChangeService(VersionChangeServiceDeps{
		Git:      f.git,
		Repos:    f.repos,
		Versions: f.versions,
		Entities: f.entities,
		Anchors:  f.anchors,
		Changes:  f.store,
		Notifier: f.notifier,
		IDs:      f.ids,
		Clock:    f.clock,
	})
	return f
}

// vcEvidenceFact is one AI fact whose verified evidence covers path.
func vcEvidenceFact(kind, name, path string) domain.CodeEntity {
	return domain.CodeEntity{
		Kind:     kind,
		Name:     name,
		Path:     path,
		Evidence: []domain.Evidence{{File: path, StartLine: 1, EndLine: 2}},
	}
}

// TestVersionChangeTimelineGeneratesBatchSnapshots pins the read-time generation
// path: one snapshot per batch, base defaults to the version baseline, the batch
// base wins when set, history_rewritten comes from base_fallback_reason, items
// carry only summaries, mapped/unmapped coverage follows the P4 fact/anchor
// rule, and each created snapshot publishes exactly one summary-only event.
func TestVersionChangeTimelineGeneratesBatchSnapshots(t *testing.T) {
	f := newVersionChangeFixture(t)

	f.store.batches = []domain.VersionBatch{
		{
			ID: 11, VersionID: 9, BatchNo: 1, Status: "done", Kind: "baseline",
			HeadCommit: vcCommitB,
		},
		{
			ID: 12, VersionID: 9, BatchNo: 2, Status: "done", Kind: "incremental",
			CodeBaselineCommit: vcCommitB, HeadCommit: vcCommitC,
			BaseFallbackReason: domain.BaseFallbackHistoryRewritten,
		},
	}

	f.git.diffs[vcDiffKey(vcCommitA, vcCommitB)] = domain.ChangeSetData{Files: []domain.ChangedFile{
		{Path: "src/routes/orders.ts", Status: "A", HeadBlobHash: "git-sha1:orders"},
		{Path: "src/api/refund.ts", Status: "M", HeadBlobHash: "git-sha1:refund"},
		{Path: "src/deleted.ts", Status: "D"},
	}}
	f.entities.byCommit[vcCommitB] = map[string][]domain.CodeEntity{
		"src/api/refund.ts":    {vcEvidenceFact(domain.KindAPI, "POST /api/refund", "src/api/refund.ts")},
		"src/routes/orders.ts": {vcEvidenceFact(domain.KindRoute, "GET /orders", "src/routes/orders.ts")},
	}
	f.git.diffs[vcDiffKey(vcCommitB, vcCommitC)] = domain.ChangeSetData{Files: []domain.ChangedFile{
		{Path: "src/api/refund.ts", Status: "M", HeadBlobHash: "git-sha1:refund-2"},
		{Path: "src/components/refund-form.tsx", Status: "A", HeadBlobHash: "git-sha1:form"},
		{Path: "src/unmapped.ts", Status: "M", HeadBlobHash: "git-sha1:unmapped"},
	}}
	f.entities.byCommit[vcCommitC] = map[string][]domain.CodeEntity{
		"src/api/refund.ts":              {vcEvidenceFact(domain.KindComponent, "RefundForm", "src/api/refund.ts")},
		"src/components/refund-form.tsx": {vcEvidenceFact(domain.KindRoute, "GET /refund", "src/components/refund-form.tsx")},
	}

	// Version-scoped anchors: case 501 points at the refund API, case 502 and a
	// requirement anchor point at the orders route. Anchors cover a changed file
	// for the unmapped view even when they are not case anchors.
	for _, anchor := range []domain.CodeAnchor{
		{ID: 501, VersionID: 9, SubjectKind: domain.SubjectCase, SubjectID: 501, FilePath: "src/api/refund.ts"},
		{ID: 502, VersionID: 9, SubjectKind: domain.SubjectCase, SubjectID: 502, FilePath: "src/routes/orders.ts"},
		{ID: 503, VersionID: 9, SubjectKind: domain.SubjectRequirement, SubjectID: 9001, FilePath: "src/routes/orders.ts"},
	} {
		if err := f.anchors.Create(context.Background(), anchor); err != nil {
			t.Fatalf("seed anchor %d: %v", anchor.ID, err)
		}
	}

	entries, err := f.svc.Timeline(context.Background(), 9)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("timeline entries = %d, want 2", len(entries))
	}
	if entries[0].BatchNo != 1 || entries[1].BatchNo != 2 {
		t.Fatalf("timeline order = %d,%d want Batch 1 then Batch 2",
			entries[0].BatchNo, entries[1].BatchNo)
	}

	first := entries[0].Change
	if first.BaseCommit != vcCommitA || first.HeadCommit != vcCommitB {
		t.Fatalf("batch 1 commits = %s..%s, want the version baseline..batch head",
			first.BaseCommit, first.HeadCommit)
	}
	if first.HistoryRewritten {
		t.Fatal("batch 1 historyRewritten = true, want false")
	}
	if first.ChangedFiles != 3 || first.ImpactedCases != 2 || first.UnmappedCount != 1 {
		t.Fatalf("batch 1 counts = files %d cases %d unmapped %d, want 3/2/1",
			first.ChangedFiles, first.ImpactedCases, first.UnmappedCount)
	}
	// Items are path-sorted summaries: file, changeKind, subjects, caseIds,
	// unmapped; no code content exists in the domain payload.
	wantKinds := map[string]string{
		"src/api/refund.ts":    domain.VersionChangeModified,
		"src/routes/orders.ts": domain.VersionChangeAdded,
		"src/deleted.ts":       domain.VersionChangeDeleted,
	}
	if len(first.Items) != 3 {
		t.Fatalf("batch 1 items = %+v, want 3 files", first.Items)
	}
	for _, item := range first.Items {
		if want := wantKinds[item.File]; item.ChangeKind != want {
			t.Fatalf("item %s changeKind = %q, want %q", item.File, item.ChangeKind, want)
		}
	}
	refundItem := versionChangeItemAt(t, first.Items, "src/api/refund.ts")
	if len(refundItem.Subjects) != 1 || refundItem.Subjects[0].Kind != domain.KindAPI ||
		refundItem.Subjects[0].Name != "POST /api/refund" {
		t.Fatalf("refund subjects = %+v, want the API fact summary", refundItem.Subjects)
	}
	if len(refundItem.CaseIDs) != 1 || refundItem.CaseIDs[0] != "501" || refundItem.Unmapped {
		t.Fatalf("refund item = %+v, want case 501 and mapped", refundItem)
	}
	ordersItem := versionChangeItemAt(t, first.Items, "src/routes/orders.ts")
	if len(ordersItem.Subjects) != 1 || ordersItem.Subjects[0].Kind != domain.KindRoute ||
		ordersItem.CaseIDs[0] != "502" || ordersItem.Unmapped {
		t.Fatalf("orders item = %+v, want the route subject, case 502 and mapped", ordersItem)
	}
	deletedItem := versionChangeItemAt(t, first.Items, "src/deleted.ts")
	if len(deletedItem.Subjects) != 0 || len(deletedItem.CaseIDs) != 0 || !deletedItem.Unmapped {
		t.Fatalf("deleted item = %+v, want an unmapped summary", deletedItem)
	}

	second := entries[1].Change
	if second.BaseCommit != vcCommitB || second.HeadCommit != vcCommitC {
		t.Fatalf("batch 2 commits = %s..%s, want the incremental base..batch head",
			second.BaseCommit, second.HeadCommit)
	}
	if !second.HistoryRewritten {
		t.Fatal("batch 2 historyRewritten = false, want true from base_fallback_reason")
	}
	if second.ChangedFiles != 3 || second.ImpactedCases != 1 || second.UnmappedCount != 1 {
		t.Fatalf("batch 2 counts = files %d cases %d unmapped %d, want 3/1/1",
			second.ChangedFiles, second.ImpactedCases, second.UnmappedCount)
	}

	if len(f.store.upserts) != 2 {
		t.Fatalf("upserts = %d, want one per batch", len(f.store.upserts))
	}
	for _, snapshot := range f.store.upserts {
		if snapshot.ID == 0 {
			t.Fatal("snapshot id = 0, want an application-generated id")
		}
		if snapshot.ComputedAt.IsZero() {
			t.Fatal("computedAt is zero, want the clock value")
		}
	}

	if len(f.notifier.events) != 2 {
		t.Fatalf("code.changed events = %d, want one per created snapshot", len(f.notifier.events))
	}
	if f.notifier.events[0].BatchID != 11 || f.notifier.events[1].BatchID != 12 {
		t.Fatalf("event batches = %+v, want 11 then 12", f.notifier.events)
	}
	if f.notifier.events[0].ChangedFiles != 3 || f.notifier.events[0].ImpactedCases != 2 ||
		f.notifier.events[0].HistoryRewritten {
		t.Fatalf("event 1 = %+v", f.notifier.events[0])
	}
	if !f.notifier.events[1].HistoryRewritten {
		t.Fatalf("event 2 = %+v, want historyRewritten", f.notifier.events[1])
	}
	for _, versionID := range f.notifier.versions {
		if versionID != 9 {
			t.Fatalf("event version = %d, want 9", versionID)
		}
	}
}

// TestVersionChangeTimelineRepeatedReadsAreIdempotent pins the read-time
// generation ruling: a second read reuses the stored snapshots, produces no
// duplicate row, recomputes no diff and publishes no additional event.
func TestVersionChangeTimelineRepeatedReadsAreIdempotent(t *testing.T) {
	f := newVersionChangeFixture(t)
	f.store.batches = []domain.VersionBatch{
		{ID: 11, VersionID: 9, BatchNo: 1, Status: "done", HeadCommit: vcCommitB},
	}
	f.git.diffs[vcDiffKey(vcCommitA, vcCommitB)] = domain.ChangeSetData{Files: []domain.ChangedFile{
		{Path: "src/api/refund.ts", Status: "M", HeadBlobHash: "git-sha1:refund"},
	}}

	for attempt := 1; attempt <= 2; attempt++ {
		entries, err := f.svc.Timeline(context.Background(), 9)
		if err != nil {
			t.Fatalf("Timeline (read %d): %v", attempt, err)
		}
		if len(entries) != 1 {
			t.Fatalf("read %d timeline entries = %d, want 1", attempt, len(entries))
		}
	}

	if len(f.git.calls) != 1 {
		t.Fatalf("git diff calls = %d, want 1 across repeated reads", len(f.git.calls))
	}
	if len(f.store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1 across repeated reads", len(f.store.upserts))
	}
	if len(f.store.changes) != 1 {
		t.Fatalf("stored snapshots = %d, want 1", len(f.store.changes))
	}
	if len(f.notifier.events) != 1 {
		t.Fatalf("code.changed events = %d, want 1 across repeated reads", len(f.notifier.events))
	}
}

// TestVersionChangeTimelineNoRepositoryIsEmpty pins the empty state: a version
// without a code repository yields no snapshots and no error, and never touches
// Git.
func TestVersionChangeTimelineNoRepositoryIsEmpty(t *testing.T) {
	f := newVersionChangeFixture(t)
	f.versions.info = VersionInfo{ID: 9}
	f.store.batches = []domain.VersionBatch{
		{ID: 11, VersionID: 9, BatchNo: 1, Status: "done", HeadCommit: vcCommitB},
	}

	entries, err := f.svc.Timeline(context.Background(), 9)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("timeline = %+v, want a non-nil empty list", entries)
	}
	if len(f.git.calls) != 0 || len(f.store.upserts) != 0 || len(f.notifier.events) != 0 {
		t.Fatalf("unbound version touched Git/upserts/events: %d/%d/%d",
			len(f.git.calls), len(f.store.upserts), len(f.notifier.events))
	}
}

// TestVersionChangeTimelineNoChangedFilesIsEmptySnapshot pins the "鏈壒鏃犱唬鐮佸彉鏇?
// state: a batch whose base..head range has no file change still gets a durable
// zero-count snapshot (so repeated reads stay cheap) and publishes no error.
func TestVersionChangeTimelineNoChangedFilesIsEmptySnapshot(t *testing.T) {
	f := newVersionChangeFixture(t)
	f.store.batches = []domain.VersionBatch{
		{ID: 11, VersionID: 9, BatchNo: 1, Status: "done", HeadCommit: vcCommitB},
	}
	f.git.diffs[vcDiffKey(vcCommitA, vcCommitB)] = domain.ChangeSetData{}

	entries, err := f.svc.Timeline(context.Background(), 9)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("timeline entries = %d, want 1", len(entries))
	}
	change := entries[0].Change
	if change.ChangedFiles != 0 || change.ImpactedCases != 0 || change.UnmappedCount != 0 ||
		len(change.Items) != 0 {
		t.Fatalf("empty snapshot = %+v, want zero counts and no items", change)
	}
	if len(f.store.changes) != 1 {
		t.Fatalf("stored snapshots = %d, want the empty snapshot persisted", len(f.store.changes))
	}
	if len(f.notifier.events) != 1 || f.notifier.events[0].ChangedFiles != 0 {
		t.Fatalf("events = %+v, want one zero-count event", f.notifier.events)
	}
}

// TestVersionChangeTimelineSkipsBatchesWithoutHead pins the no-code-history
// case: a batch that never resolved a head commit contributes no snapshot and
// no error.
func TestVersionChangeTimelineSkipsBatchesWithoutHead(t *testing.T) {
	f := newVersionChangeFixture(t)
	f.store.batches = []domain.VersionBatch{
		{ID: 11, VersionID: 9, BatchNo: 1, Status: "planned"},
		{ID: 12, VersionID: 9, BatchNo: 2, Status: "done", HeadCommit: vcCommitB},
	}
	f.git.diffs[vcDiffKey(vcCommitA, vcCommitB)] = domain.ChangeSetData{Files: []domain.ChangedFile{
		{Path: "src/api/refund.ts", Status: "M", HeadBlobHash: "git-sha1:refund"},
	}}

	entries, err := f.svc.Timeline(context.Background(), 9)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(entries) != 1 || entries[0].Change.BatchID != 12 {
		t.Fatalf("timeline = %+v, want only the batch with a head commit", entries)
	}
	if len(f.git.calls) != 1 {
		t.Fatalf("git diff calls = %d, want 1", len(f.git.calls))
	}
}

// TestVersionChangeTimelinePropagatesOperationalFailures pins that a real Git
// failure is surfaced instead of silently rendering an empty timeline.
func TestVersionChangeTimelinePropagatesOperationalFailures(t *testing.T) {
	f := newVersionChangeFixture(t)
	f.store.batches = []domain.VersionBatch{
		{ID: 11, VersionID: 9, BatchNo: 1, Status: "done", HeadCommit: vcCommitB},
	}
	wantErr := errors.New("git diff failed")
	f.git.errs[vcDiffKey(vcCommitA, vcCommitB)] = wantErr

	if _, err := f.svc.Timeline(context.Background(), 9); !errors.Is(err, wantErr) {
		t.Fatalf("Timeline error = %v, want the git failure", err)
	}
	if len(f.store.upserts) != 0 || len(f.notifier.events) != 0 {
		t.Fatalf("failed generation wrote %d snapshots and %d events, want none",
			len(f.store.upserts), len(f.notifier.events))
	}
}

// versionChangeItemAt returns the item of path or fails the test.
func versionChangeItemAt(t *testing.T, items []domain.VersionChangeItem, path string) domain.VersionChangeItem {
	t.Helper()
	for _, item := range items {
		if item.File == path {
			return item
		}
	}
	t.Fatalf("item %s missing from %+v", path, items)
	return domain.VersionChangeItem{}
}
