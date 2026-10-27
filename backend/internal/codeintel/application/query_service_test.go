package application

import (
	"context"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// ---------------------------------------------------------------------------
// QueryService fixtures and tests.
// ---------------------------------------------------------------------------

type queryFixture struct {
	git        *stubGit
	repos      *stubRepos
	entities   *stubEntities
	changeSets *stubChangeSets
	versions   *stubVersions
	audit      *stubAudit
	svc        *QueryService
}

func newQueryFixture() *queryFixture {
	return newQueryFixtureWithConfig(QueryConfig{MaxDiffBytes: 4096})
}

func newQueryFixtureWithConfig(cfg QueryConfig) *queryFixture {
	f := &queryFixture{
		git:        &stubGit{head: testCommitC},
		repos:      &stubRepos{},
		entities:   &stubEntities{},
		changeSets: &stubChangeSets{},
		versions:   &stubVersions{},
		audit:      &stubAudit{},
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
	f.svc = NewQueryService(QueryServiceDeps{
		Git:        f.git,
		Repos:      f.repos,
		Entities:   f.entities,
		ChangeSets: f.changeSets,
		Versions:   f.versions,
		Audit:      f.audit,
		Config:     cfg,
	})
	return f
}

// TestQueryServiceTreeResolvesFixedBranchHeadAndForwardsFilters pins the tree
// read path: fixed-branch tip defaulting, repo globs, prefix and opaque cursor
// are forwarded to the Git adapter and the page bound is clamped.
func TestQueryServiceTreeResolvesFixedBranchHeadAndForwardsFilters(t *testing.T) {
	f := newQueryFixture()
	f.git.treePage = domain.TreePage{
		Commit:     testCommitC,
		Entries:    []domain.TreeEntry{{Path: "src/a.ts", Type: "blob"}},
		NextCursor: "src/a.ts",
		HasMore:    true,
	}

	page, err := f.svc.Tree(context.Background(), 3, "src", "", "cursor-x", 999)
	if err != nil {
		t.Fatalf("Tree error = %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Path != "src/a.ts" || !page.HasMore {
		t.Fatalf("Tree page = %+v, want the fake page", page)
	}
	if len(f.git.headCalls) != 1 || f.git.headCalls[0].localPath != "/repo" || f.git.headCalls[0].branch != "main" {
		t.Fatalf("Head calls = %+v, want /repo main", f.git.headCalls)
	}
	if len(f.git.treeCalls) != 1 {
		t.Fatalf("Tree calls = %d, want 1", len(f.git.treeCalls))
	}
	call := f.git.treeCalls[0]
	if call.commit != testCommitC || call.prefix != "src" || call.cursor != "cursor-x" || call.limit != domain.MaxTreeEntriesPerPage {
		t.Fatalf("Tree args = %+v, want commit %s prefix src cursor-x limit %d", call, testCommitC, domain.MaxTreeEntriesPerPage)
	}
	if len(call.include) != 1 || call.include[0] != "src/**" ||
		len(call.exclude) != 1 || call.exclude[0] != "vendor/**" {
		t.Fatalf("Tree globs = include %v exclude %v, want repo globs", call.include, call.exclude)
	}

	// An explicit commit is used as-is: no fixed-branch resolution.
	if _, err := f.svc.Tree(context.Background(), 3, "", testCommitB, "", 5); err != nil {
		t.Fatalf("Tree(explicit commit) error = %v", err)
	}
	if len(f.git.headCalls) != 1 {
		t.Fatalf("Head calls = %d, want 1 (explicit commit must not resolve HEAD)", len(f.git.headCalls))
	}
	if last := f.git.treeCalls[len(f.git.treeCalls)-1]; last.commit != testCommitB || last.limit != 5 {
		t.Fatalf("Tree(explicit) args = %+v, want commit %s limit 5", last, testCommitB)
	}

	// gitx domain errors surface unchanged for the HTTP layer.
	f.git.treeErr = domain.NewInvalid("path", "path_escape", "path escapes the repository")
	if _, err := f.svc.Tree(context.Background(), 3, "", testCommitB, "", 5); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Tree error = %v, want ErrInvalid", err)
	} else if reason := domain.Reason(err); reason != "path_escape" {
		t.Fatalf("reason = %q, want path_escape", reason)
	}
}

// TestQueryServiceRejectsDisabledRepository pins the read-side repository
// status rule.
func TestQueryServiceRejectsDisabledRepository(t *testing.T) {
	f := newQueryFixture()
	info := f.repos.infos[3]
	info.Status = "disabled"
	f.repos.infos[3] = info

	if _, err := f.svc.Tree(context.Background(), 3, "", "", "", 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Tree error = %v, want ErrInvalid", err)
	} else if domain.Reason(err) != "code_repository_disabled" {
		t.Fatalf("reason = %q, want code_repository_disabled", domain.Reason(err))
	}
	if _, err := f.svc.File(context.Background(), 7, 3, "", "src/a.ts", 1, 2); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("File error = %v, want ErrInvalid", err)
	}
	if len(f.git.headCalls) != 0 || len(f.git.fileCalls) != 0 {
		t.Fatal("disabled repository must not reach Git")
	}
}

// TestQueryServiceFileAuditsRead pins the code.read audit entry: the identifiers
// are recorded and no content is ever included.
func TestQueryServiceFileAuditsRead(t *testing.T) {
	f := newQueryFixture()
	f.git.fileWindow = domain.FileWindow{
		Commit:     testCommitB,
		Path:       "src/a.ts",
		StartLine:  2,
		EndLine:    3,
		TotalLines: 10,
		Content:    "two\nthree",
	}

	window, err := f.svc.File(context.Background(), 7, 3, testCommitB, "src/a.ts", 2, 3)
	if err != nil {
		t.Fatalf("File error = %v", err)
	}
	if window.Content != "two\nthree" || window.TotalLines != 10 {
		t.Fatalf("File window = %+v, want the fake window", window)
	}
	if len(f.git.fileCalls) != 1 {
		t.Fatalf("File calls = %d, want 1", len(f.git.fileCalls))
	}
	call := f.git.fileCalls[0]
	if call.localPath != "/repo" || call.commit != testCommitB || call.path != "src/a.ts" || call.startLine != 2 || call.endLine != 3 {
		t.Fatalf("File args = %+v", call)
	}
	if len(f.audit.calls) != 1 {
		t.Fatalf("audit calls = %d, want 1", len(f.audit.calls))
	}
	entry := f.audit.calls[0]
	if entry.actorID != 7 || entry.action != "code.read" || entry.subject != "code_repository:3" {
		t.Fatalf("audit entry = %+v, want actor 7 code.read code_repository:3", entry)
	}
	if entry.detail["repoId"] != int64(3) || entry.detail["commit"] != testCommitB || entry.detail["path"] != "src/a.ts" {
		t.Fatalf("audit detail = %+v, want repoId/commit/path", entry.detail)
	}
	if _, ok := entry.detail["content"]; ok {
		t.Fatal("audit detail must never carry code content")
	}

	// Audit failures do not block the read.
	f.audit.err = errors.New("audit storage down")
	if _, err := f.svc.File(context.Background(), 7, 3, testCommitB, "src/a.ts", 1, 1); err != nil {
		t.Fatalf("File with failing audit error = %v, want nil", err)
	}

	// An empty commit defaults to the fixed branch tip.
	f.git.headCalls = nil
	if _, err := f.svc.File(context.Background(), 7, 3, "", "src/a.ts", 1, 1); err != nil {
		t.Fatalf("File(default commit) error = %v", err)
	}
	last := f.git.fileCalls[len(f.git.fileCalls)-1]
	if last.commit != testCommitC || len(f.git.headCalls) != 1 {
		t.Fatalf("File(default commit) args = %+v headCalls=%d, want commit %s", last, len(f.git.headCalls), testCommitC)
	}

	f.git.fileErr = domain.NewInvalid("path", "path_not_found", "missing file")
	if _, err := f.svc.File(context.Background(), 7, 3, testCommitB, "src/missing.ts", 1, 1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("File error = %v, want ErrInvalid", err)
	} else if domain.Reason(err) != "path_not_found" {
		t.Fatalf("reason = %q, want path_not_found", domain.Reason(err))
	}
}

// TestQueryServiceDiffCapsPagesAndDefaultsHead pins the read-only diff preview:
// the fixed-branch tip defaults head, file pages are bounded with the last path
// as cursor and the service never writes a change set.
func TestQueryServiceDiffCapsPagesAndDefaultsHead(t *testing.T) {
	f := newQueryFixture()
	files := []domain.ChangedFile{
		{Path: "a.ts", Status: "M"},
		{Path: "b.ts", Status: "A"},
		{Path: "c.ts", Status: "D"},
		{Path: "d.ts", Status: "R100"},
		{Path: "e.ts", Status: "M"},
	}
	f.git.diffData = domain.ChangeSetData{
		Stats:     []byte(`{"files":5,"additions":7,"deletions":2,"truncated":false}`),
		Files:     files,
		Truncated: false,
	}

	data, err := f.svc.Diff(context.Background(), 3, testCommitA, "", 2, "")
	if err != nil {
		t.Fatalf("Diff error = %v", err)
	}
	if len(f.git.headCalls) != 1 || f.git.headCalls[0].branch != "main" {
		t.Fatalf("Head calls = %+v, want fixed branch main", f.git.headCalls)
	}
	if len(f.git.diffCalls) != 1 || f.git.diffCalls[0].base != testCommitA || f.git.diffCalls[0].head != testCommitC {
		t.Fatalf("Diff calls = %+v, want base %s head %s", f.git.diffCalls, testCommitA, testCommitC)
	}
	if len(data.Files) != 2 || data.Files[0].Path != "a.ts" || data.Files[1].Path != "b.ts" {
		t.Fatalf("first page = %+v, want a.ts,b.ts", data.Files)
	}
	if !data.Truncated {
		t.Fatal("page with remaining files must be truncated")
	}
	if data.ChangeSetID != nil {
		t.Fatalf("changeSetId = %v, want nil (read-only preview)", *data.ChangeSetID)
	}
	if string(data.Stats) != `{"files":5,"additions":7,"deletions":2,"truncated":false}` {
		t.Fatalf("stats = %s, want the deterministic change-set stats", string(data.Stats))
	}
	if len(f.changeSets.getOrCreateCalls) != 0 {
		t.Fatal("Diff preview must not persist a change set")
	}

	// The next page resumes strictly after the last returned path.
	next, err := f.svc.Diff(context.Background(), 3, testCommitA, testCommitC, 2, "b.ts")
	if err != nil {
		t.Fatalf("Diff(next) error = %v", err)
	}
	if len(next.Files) != 2 || next.Files[0].Path != "c.ts" || next.Files[1].Path != "d.ts" {
		t.Fatalf("second page = %+v, want c.ts,d.ts", next.Files)
	}

	// A zero limit returns the whole (bounded) change list.
	all, err := f.svc.Diff(context.Background(), 3, testCommitA, testCommitC, 0, "")
	if err != nil {
		t.Fatalf("Diff(default limit) error = %v", err)
	}
	if len(all.Files) != 5 || all.Truncated {
		t.Fatalf("default-limit diff = %+v, want all 5 files not truncated", all)
	}

	f.git.diffErr = domain.NewInvalid("base", "commit_not_found", "unknown base commit")
	if _, err := f.svc.Diff(context.Background(), 3, testCommitA, testCommitC, 0, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Diff error = %v, want ErrInvalid", err)
	} else if domain.Reason(err) != "commit_not_found" {
		t.Fatalf("reason = %q, want commit_not_found", domain.Reason(err))
	}
}

// TestQueryServiceFileDiffUsesConfiguredMaxBytes pins the per-file unified diff
// pass-through and the configured size bound.
func TestQueryServiceFileDiffUsesConfiguredMaxBytes(t *testing.T) {
	f := newQueryFixture()
	f.git.fileDiff = domain.FileDiff{
		BaseCommit: testCommitA,
		HeadCommit: testCommitC,
		Path:       "src/a.ts",
		Status:     "M",
		Binary:     true,
		Truncated:  true,
	}

	got, err := f.svc.FileDiff(context.Background(), 3, testCommitA, "", "src/a.ts")
	if err != nil {
		t.Fatalf("FileDiff error = %v", err)
	}
	if !got.Binary || !got.Truncated || got.Path != "src/a.ts" {
		t.Fatalf("FileDiff = %+v, want binary+truncated pass-through", got)
	}
	if len(f.git.headCalls) != 1 || len(f.git.fileDiffCalls) != 1 {
		t.Fatalf("Head/FileDiff calls = %d/%d, want 1/1", len(f.git.headCalls), len(f.git.fileDiffCalls))
	}
	call := f.git.fileDiffCalls[0]
	if call.base != testCommitA || call.head != testCommitC || call.path != "src/a.ts" || call.maxBytes != 4096 {
		t.Fatalf("FileDiff args = %+v, want base %s head %s maxBytes 4096", call, testCommitA, testCommitC)
	}

	f.git.fileDiffErr = domain.NewInvalid("path", "not_changed", "path is not in the change set")
	if _, err := f.svc.FileDiff(context.Background(), 3, testCommitA, testCommitC, "src/other.ts"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("FileDiff error = %v, want ErrInvalid", err)
	} else if domain.Reason(err) != "not_changed" {
		t.Fatalf("reason = %q, want not_changed", domain.Reason(err))
	}
}

// TestQueryServiceFileAppliesReadLineCap pins the query-layer line bound: a
// window beyond the configured cap is cut and reported truncated.
func TestQueryServiceFileAppliesReadLineCap(t *testing.T) {
	f := newQueryFixtureWithConfig(QueryConfig{MaxReadLines: 2, MaxDiffBytes: 4096})
	f.git.fileWindow = domain.FileWindow{
		Commit:     testCommitB,
		Path:       "src/a.ts",
		StartLine:  1,
		EndLine:    2,
		TotalLines: 5,
		Content:    "one\ntwo",
	}

	window, err := f.svc.File(context.Background(), 7, 3, testCommitB, "src/a.ts", 1, 0)
	if err != nil {
		t.Fatalf("File error = %v", err)
	}
	if call := f.git.fileCalls[0]; call.endLine != 2 {
		t.Fatalf("File endLine = %d, want the configured cap 2", call.endLine)
	}
	if !window.Truncated {
		t.Fatal("a file continuing past the configured cap must be truncated")
	}
}

// TestQueryServiceDefaultsConfigBounds pins the default configuration applied
// when the composition root leaves QueryConfig zero.
func TestQueryServiceDefaultsConfigBounds(t *testing.T) {
	f := newQueryFixtureWithConfig(QueryConfig{})
	if _, err := f.svc.FileDiff(context.Background(), 3, testCommitA, testCommitC, "src/a.ts"); err != nil {
		t.Fatalf("FileDiff error = %v", err)
	}
	if got := f.git.fileDiffCalls[0].maxBytes; got != 256<<10 {
		t.Fatalf("maxBytes = %d, want the 256 KiB default", got)
	}
	if _, err := f.svc.Tree(context.Background(), 3, "", testCommitC, "", 0); err != nil {
		t.Fatalf("Tree error = %v", err)
	}
	if got := f.git.treeCalls[0].limit; got != domain.MaxTreeEntriesPerPage {
		t.Fatalf("tree limit = %d, want %d", got, domain.MaxTreeEntriesPerPage)
	}
}

// TestQueryServiceFactsAndSearch pins the entity read paths: commit defaulting,
// page clamps, substring search and its audit.
func TestQueryServiceFactsAndSearch(t *testing.T) {
	f := newQueryFixture()
	f.entities.listResult = []domain.CodeEntity{{ID: 11, Kind: domain.KindAPI}}
	f.entities.listCursor = "next-cursor"
	f.entities.listMore = true

	entities, cursor, more, err := f.svc.Facts(context.Background(), 3, domain.EntityFilter{
		Kind:  domain.KindAPI,
		Path:  "src/a.ts",
		Query: "refund",
	})
	if err != nil {
		t.Fatalf("Facts error = %v", err)
	}
	if len(entities) != 1 || entities[0].ID != 11 || cursor != "next-cursor" || !more {
		t.Fatalf("Facts = %+v %q %v, want the fake page", entities, cursor, more)
	}
	first := f.entities.listFilters[0]
	if first.Commit != testCommitC || first.Limit != 100 {
		t.Fatalf("Facts filter = %+v, want commit %s limit 100", first, testCommitC)
	}
	if first.Kind != domain.KindAPI || first.Path != "src/a.ts" || first.Query != "refund" {
		t.Fatalf("Facts filter = %+v, want kind/path/query preserved", first)
	}

	// An explicit commit skips HEAD and large limits are clamped.
	if _, _, _, err := f.svc.Facts(context.Background(), 3, domain.EntityFilter{Commit: testCommitB, Limit: 999}); err != nil {
		t.Fatalf("Facts(explicit) error = %v", err)
	}
	second := f.entities.listFilters[len(f.entities.listFilters)-1]
	if second.Commit != testCommitB || second.Limit != 200 {
		t.Fatalf("Facts(explicit) filter = %+v, want commit %s limit 200", second, testCommitB)
	}
	if len(f.git.headCalls) != 1 {
		t.Fatalf("Head calls = %d, want 1 (default commit only)", len(f.git.headCalls))
	}

	if _, _, _, err := f.svc.Search(context.Background(), 3, "", "refund", domain.KindAPI, 5, "page-2"); err != nil {
		t.Fatalf("Search error = %v", err)
	}
	search := f.entities.listFilters[len(f.entities.listFilters)-1]
	if search.Commit != testCommitC || search.Query != "refund" || search.Kind != domain.KindAPI ||
		search.Limit != 5 || search.Cursor != "page-2" {
		t.Fatalf("Search filter = %+v, want commit %s query refund kind api limit 5 cursor page-2", search, testCommitC)
	}
	if len(f.audit.calls) != 1 {
		t.Fatalf("audit calls = %d, want 1 code.read for search", len(f.audit.calls))
	}
	if entry := f.audit.calls[0]; entry.action != "code.read" || entry.subject != "code_repository:3" ||
		entry.detail["repoId"] != int64(3) || entry.detail["commit"] != testCommitC {
		t.Fatalf("search audit = %+v, want code.read with repoId/commit", entry)
	}

	f.entities.listErr = errors.New("db down")
	if _, _, _, err := f.svc.Facts(context.Background(), 3, domain.EntityFilter{Commit: testCommitB}); err == nil {
		t.Fatal("Facts error = nil, want the repository error")
	}
}

// TestQueryServiceVersionChangesUsesBaseline pins the version-scoped change
// preview: base is always the version baseline, head is explicit or the fixed
// branch tip, and the preview never persists anything.
func TestQueryServiceVersionChangesUsesBaseline(t *testing.T) {
	f := newQueryFixture()
	f.versions.info = VersionInfo{
		ID:             9,
		RepoID:         3,
		HasRepo:        true,
		BaselineCommit: testCommitA,
		Branch:         "release",
	}
	f.git.diffData = domain.ChangeSetData{
		Stats: []byte(`{"files":1}`),
		Files: []domain.ChangedFile{{Path: "src/a.ts", Status: "M"}},
	}

	data, base, head, err := f.svc.VersionChanges(context.Background(), 9, testCommitB)
	if err != nil {
		t.Fatalf("VersionChanges error = %v", err)
	}
	if base != testCommitA || head != testCommitB {
		t.Fatalf("base/head = %s/%s, want %s/%s", base, head, testCommitA, testCommitB)
	}
	if len(data.Files) != 1 || data.ChangeSetID != nil {
		t.Fatalf("VersionChanges data = %+v, want preview without change set id", data)
	}
	if len(f.git.diffCalls) != 1 || f.git.diffCalls[0].localPath != "/repo" ||
		f.git.diffCalls[0].base != testCommitA || f.git.diffCalls[0].head != testCommitB {
		t.Fatalf("Diff calls = %+v", f.git.diffCalls)
	}
	if len(f.git.headCalls) != 0 {
		t.Fatal("explicit head must not resolve the fixed branch")
	}
	if len(f.changeSets.getOrCreateCalls) != 0 {
		t.Fatal("VersionChanges preview must not persist a change set")
	}

	// No explicit head: resolve the version branch (falling back to the repo's
	// fixed branch when the version carries none).
	if _, _, head, err = f.svc.VersionChanges(context.Background(), 9, ""); err != nil {
		t.Fatalf("VersionChanges(default head) error = %v", err)
	}
	if head != testCommitC {
		t.Fatalf("head = %s, want %s", head, testCommitC)
	}
	if len(f.git.headCalls) != 1 || f.git.headCalls[0].branch != "release" {
		t.Fatalf("Head calls = %+v, want version branch release", f.git.headCalls)
	}
	f.versions.info.Branch = ""
	if _, _, _, err = f.svc.VersionChanges(context.Background(), 9, ""); err != nil {
		t.Fatalf("VersionChanges(fallback branch) error = %v", err)
	}
	if last := f.git.headCalls[len(f.git.headCalls)-1]; last.branch != "main" {
		t.Fatalf("fallback branch = %q, want repo fixed branch main", last.branch)
	}

	f.versions.info.HasRepo = false
	if _, _, _, err := f.svc.VersionChanges(context.Background(), 9, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unbound version error = %v, want ErrInvalid", err)
	} else if domain.Reason(err) != "code_repository_not_bound" {
		t.Fatalf("reason = %q, want code_repository_not_bound", domain.Reason(err))
	}

	f.versions.info.HasRepo = true
	f.versions.info.BaselineCommit = ""
	if _, _, _, err := f.svc.VersionChanges(context.Background(), 9, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing baseline error = %v, want ErrInvalid", err)
	} else if domain.Reason(err) != "baseline_commit_missing" {
		t.Fatalf("reason = %q, want baseline_commit_missing", domain.Reason(err))
	}

	f.versions.err = domain.NewNotFound("not_found", "version not found")
	if _, _, _, err := f.svc.VersionChanges(context.Background(), 9, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing version error = %v, want ErrNotFound", err)
	}
}

// TestQueryServiceVersionChangesPropagatesRepoAndDiffErrors pins error
// propagation for the preview path.
func TestQueryServiceVersionChangesPropagatesRepoAndDiffErrors(t *testing.T) {
	f := newQueryFixture()
	f.versions.info = VersionInfo{ID: 9, RepoID: 3, HasRepo: true, BaselineCommit: testCommitA}

	f.git.diffErr = errors.New("git unavailable")
	if _, _, _, err := f.svc.VersionChanges(context.Background(), 9, testCommitB); err == nil {
		t.Fatal("VersionChanges error = nil, want the diff error")
	}

	delete(f.repos.infos, 3)
	if _, _, _, err := f.svc.VersionChanges(context.Background(), 9, testCommitB); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing repo error = %v, want ErrInvalid", err)
	}
}
