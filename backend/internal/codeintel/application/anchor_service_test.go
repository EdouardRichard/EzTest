package codeintelapp

import (
	"context"
	"errors"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// --- fakes for the anchor/review services ---------------------------------------

type fakeAnchorStore struct {
	anchors   map[int64]codeintel.Anchor
	order     []int64
	nextID    int64
	createErr error
	updateErr error
	deleteErr error
	getErr    error
	listErr   error
}

func newFakeAnchorStore() *fakeAnchorStore {
	return &fakeAnchorStore{anchors: map[int64]codeintel.Anchor{}}
}

func (f *fakeAnchorStore) Create(_ context.Context, anchor codeintel.Anchor) (codeintel.Anchor, error) {
	if f.createErr != nil {
		return codeintel.Anchor{}, f.createErr
	}
	for _, existing := range f.anchors {
		if existing.VersionID == anchor.VersionID && existing.SubjectKind == anchor.SubjectKind &&
			existing.SubjectID == anchor.SubjectID && existing.FilePath == anchor.FilePath &&
			sameLine(existing.StartLine, anchor.StartLine) && sameLine(existing.EndLine, anchor.EndLine) {
			return codeintel.Anchor{}, codeintel.ErrConflict
		}
	}
	f.nextID++
	anchor.ID = f.nextID
	f.anchors[anchor.ID] = anchor
	f.order = append(f.order, anchor.ID)
	return anchor, nil
}

func sameLine(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (f *fakeAnchorStore) List(_ context.Context, versionID int64, subjectKind string, subjectID int64) ([]codeintel.Anchor, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := make([]codeintel.Anchor, 0, len(f.order))
	for _, id := range f.order {
		anchor := f.anchors[id]
		if anchor.VersionID != versionID {
			continue
		}
		if subjectKind != "" && string(anchor.SubjectKind) != subjectKind {
			continue
		}
		if subjectID > 0 && anchor.SubjectID != subjectID {
			continue
		}
		result = append(result, anchor)
	}
	return result, nil
}

func (f *fakeAnchorStore) Get(_ context.Context, id int64) (codeintel.Anchor, error) {
	if f.getErr != nil {
		return codeintel.Anchor{}, f.getErr
	}
	anchor, ok := f.anchors[id]
	if !ok {
		return codeintel.Anchor{}, codeintel.ErrNotFound
	}
	return anchor, nil
}

func (f *fakeAnchorStore) UpdateStatus(_ context.Context, id int64, status string, validatedAt time.Time) (codeintel.Anchor, error) {
	if f.updateErr != nil {
		return codeintel.Anchor{}, f.updateErr
	}
	anchor, ok := f.anchors[id]
	if !ok {
		return codeintel.Anchor{}, codeintel.ErrNotFound
	}
	anchor.Status = status
	anchor.LastValidatedAt = &validatedAt
	anchor.UpdatedAt = validatedAt
	f.anchors[id] = anchor
	return anchor, nil
}

func (f *fakeAnchorStore) Delete(_ context.Context, id int64) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.anchors[id]; !ok {
		return codeintel.ErrNotFound
	}
	delete(f.anchors, id)
	return nil
}

type fakeAnchorSubjects struct {
	requirements        map[int64]bool
	requirementVersions map[int64]bool
	caseVersions        map[int64]bool
	issues              map[int64]bool
	err                 error
}

func (f *fakeAnchorSubjects) RequirementInVersion(_ context.Context, _, id int64) (bool, error) {
	return f.requirements[id], f.err
}

func (f *fakeAnchorSubjects) RequirementVersionInVersion(_ context.Context, _, id int64) (bool, error) {
	return f.requirementVersions[id], f.err
}

func (f *fakeAnchorSubjects) CaseVersionInVersion(_ context.Context, _, id int64) (bool, error) {
	return f.caseVersions[id], f.err
}

func (f *fakeAnchorSubjects) IssueInVersion(_ context.Context, _, id int64) (bool, error) {
	return f.issues[id], f.err
}

// --- harness ---------------------------------------------------------------------

const (
	anchorCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	anchorCommitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type anchorHarness struct {
	service  *AnchorService
	anchors  *fakeAnchorStore
	git      *fakeGit
	versions *fakeVersionReader
	resource *fakeResourceReader
	runs     *fakeIndexRepo
	subjects *fakeAnchorSubjects
	events   *fakeEvents
	audit    *fakeAudit
	now      time.Time
	ctx      context.Context
}

func newAnchorHarness() *anchorHarness {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ctx := platform.WithActor(context.Background(), platform.Actor{ID: 42, Role: "admin"})
	git := &fakeGit{windows: map[string]codeintel.FileWindow{}}
	versions := &fakeVersionReader{bindings: []VersionSUTBinding{{
		VersionSUTID: 11, VersionID: 1, SUTID: 21, RepoID: 10, Branch: "main",
		CodeBaselineCommit: anchorCommitA,
	}}}
	resources := &fakeResourceReader{repos: map[int64]RepoRef{10: {
		ID: 10, Name: "fixture", Path: "/repo", Branch: "main",
	}}}
	runs := newFakeIndexRepo()
	subjects := &fakeAnchorSubjects{
		requirements:        map[int64]bool{100: true},
		requirementVersions: map[int64]bool{110: true},
		caseVersions:        map[int64]bool{200: true},
		issues:              map[int64]bool{300: true},
	}
	events := &fakeEvents{}
	audit := &fakeAudit{}
	service := NewAnchorService(AnchorDeps{
		Anchors:   newFakeAnchorStore(),
		Git:       git,
		Versions:  versions,
		Resources: resources,
		Runs:      runs,
		Subjects:  subjects,
		Events:    events,
		Audit:     audit,
		Settings:  &fakeSettings{ints: map[string]int64{"codeintel.max_excerpt_lines": 40}},
		Clock:     fakeClock{now: now},
		IDs:       &fakeIDs{},
	})
	return &anchorHarness{
		service: service, anchors: service.anchors.(*fakeAnchorStore), git: git,
		versions: versions, resource: resources, runs: runs, subjects: subjects,
		events: events, audit: audit, now: now, ctx: ctx,
	}
}

func (h *anchorHarness) seedWindow(path string, lines ...string) {
	h.git.windows[path] = codeintel.FileWindow{
		Path: path, Commit: anchorCommitA, Lines: lines,
		StartLine: 1, EndLine: len(lines), TotalLines: len(lines),
	}
}

func (h *anchorHarness) seedDoneRun(commit string) {
	_, _ = h.runs.CreateRun(h.ctx, codeintel.IndexRun{
		ID: 501, VersionID: 1, RepoID: 10, CommitSHA: commit, Status: string(codeintel.IndexDone),
	})
}

func intPtr(value int) *int { return &value }

// --- create ----------------------------------------------------------------------

func TestAnchorCreateComputesExcerptAndAudits(t *testing.T) {
	h := newAnchorHarness()
	h.seedWindow("src/orders.ts", "line one", "line two", "line three")
	h.git.windows["src/orders.ts"] = codeintel.FileWindow{
		Path: "src/orders.ts", Lines: []string{"line two"}, StartLine: 2, EndLine: 2, TotalLines: 3,
	}
	anchor, err := h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
		RepoID: 10, FilePath: "src/orders.ts", StartLine: intPtr(2), EndLine: intPtr(2),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if anchor.Status != string(codeintel.AnchorValid) || anchor.LastValidatedAt == nil {
		t.Fatalf("anchor = %+v", anchor)
	}
	if anchor.ExcerptSHA != codeintel.ExcerptHash([]string{"line two"}) {
		t.Fatalf("excerpt sha = %q", anchor.ExcerptSHA)
	}
	if anchor.CommitSHA != anchorCommitA || anchor.CreatedBy == nil || *anchor.CreatedBy != 42 {
		t.Fatalf("anchor commit/actor = %+v", anchor)
	}
	if len(h.events.events) != 0 {
		t.Fatalf("create must not publish anchor events: %v", h.events.types())
	}
	if len(h.audit.entries) != 1 || h.audit.entries[0].Action != "code.anchor.created" {
		t.Fatalf("audit = %+v", h.audit.entries)
	}
	if h.audit.entries[0].Metadata["subject_kind"] != "requirement" ||
		h.audit.entries[0].Metadata["subject_id"] != "100" {
		t.Fatalf("audit metadata = %+v", h.audit.entries[0].Metadata)
	}
}

func TestAnchorCreateRejectsInvalidSubjectsAndRepo(t *testing.T) {
	h := newAnchorHarness()
	h.seedWindow("src/a.ts", "a")
	h.seedWindow("src/b.ts", "b")

	_, err := h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 999,
		RepoID: 10, FilePath: "src/a.ts",
	})
	assertPlatformError(t, err, 422, "invalid_subject")

	_, err = h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: "unknown", SubjectID: 100, RepoID: 10, FilePath: "src/a.ts",
	})
	assertPlatformError(t, err, 422, "invalid_subject")

	_, err = h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectCaseVersion, SubjectID: 999,
		RepoID: 10, FilePath: "src/a.ts",
	})
	assertPlatformError(t, err, 422, "invalid_subject")

	_, err = h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectVersion, SubjectID: 2, RepoID: 10, FilePath: "src/a.ts",
	})
	assertPlatformError(t, err, 422, "invalid_subject")

	_, err = h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
		RepoID: 99, FilePath: "src/a.ts",
	})
	assertPlatformError(t, err, 422, "repo_not_bound")
}

func TestAnchorCreateRejectsPathAndRangeViolations(t *testing.T) {
	h := newAnchorHarness()
	h.seedWindow("src/a.ts", "one", "two")
	base := CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
		RepoID: 10, FilePath: "src/a.ts",
	}

	escape := base
	escape.FilePath = "../secrets.txt"
	_, err := h.service.Create(h.ctx, escape)
	assertPlatformError(t, err, 422, "path_escape")

	outOfRange := base
	outOfRange.StartLine, outOfRange.EndLine = intPtr(2), intPtr(9)
	_, err = h.service.Create(h.ctx, outOfRange)
	assertPlatformError(t, err, 422, "invalid_range")

	inverted := base
	inverted.StartLine, inverted.EndLine = intPtr(2), intPtr(1)
	_, err = h.service.Create(h.ctx, inverted)
	assertPlatformError(t, err, 422, "invalid_range")

	onlyStart := base
	onlyStart.StartLine = intPtr(1)
	_, err = h.service.Create(h.ctx, onlyStart)
	assertPlatformError(t, err, 422, "invalid_range")

	badHash := base
	badHash.StartLine, badHash.EndLine = intPtr(1), intPtr(1)
	badHash.ExcerptSHA = "sha256:" + "00"
	_, err = h.service.Create(h.ctx, badHash)
	assertPlatformError(t, err, 422, "invalid_excerpt")

	hashWithoutLines := base
	hashWithoutLines.ExcerptSHA = codeintel.ExcerptHash([]string{"one"})
	_, err = h.service.Create(h.ctx, hashWithoutLines)
	assertPlatformError(t, err, 422, "invalid_excerpt")
}

func TestAnchorCreateExplicitCommitAndDuplicate(t *testing.T) {
	h := newAnchorHarness()
	h.seedWindow("src/a.ts", "one")
	input := CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
		RepoID: 10, FilePath: "src/a.ts", CommitSHA: anchorCommitB,
	}
	anchor, err := h.service.Create(h.ctx, input)
	if err != nil {
		t.Fatalf("Create explicit commit: %v", err)
	}
	if anchor.CommitSHA != anchorCommitB {
		t.Fatalf("commit = %q", anchor.CommitSHA)
	}

	_, err = h.service.Create(h.ctx, input)
	assertPlatformError(t, err, 409, "anchor_duplicate")

	bad := input
	bad.CommitSHA = "not-a-sha"
	_, err = h.service.Create(h.ctx, bad)
	assertPlatformError(t, err, 422, "invalid_commit")
}

func TestAnchorCreateFallsBackToLatestIndexCommit(t *testing.T) {
	h := newAnchorHarness()
	h.versions.bindings[0].CodeBaselineCommit = ""
	h.seedWindow("src/a.ts", "one")
	h.seedDoneRun(anchorCommitB)
	anchor, err := h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectIssue, SubjectID: 300,
		RepoID: 10, FilePath: "src/a.ts",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if anchor.CommitSHA != anchorCommitB {
		t.Fatalf("commit = %q", anchor.CommitSHA)
	}
}

// --- validate --------------------------------------------------------------------

func TestAnchorValidateReportsValidStaleBroken(t *testing.T) {
	h := newAnchorHarness()
	h.seedWindow("src/a.ts", "one", "two")
	anchor, err := h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
		RepoID: 10, FilePath: "src/a.ts", StartLine: intPtr(1), EndLine: intPtr(2),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h.seedDoneRun(anchorCommitB)

	valid, err := h.service.Validate(h.ctx, 1, anchor.ID)
	if err != nil {
		t.Fatalf("Validate valid: %v", err)
	}
	if valid.Status != string(codeintel.AnchorValid) {
		t.Fatalf("valid status = %q", valid.Status)
	}
	if len(h.events.events) != 1 || h.events.events[0].Type != "code.anchor_validated" {
		t.Fatalf("events = %v", h.events.types())
	}
	if _, ok := h.events.events[0].Payload["file_path"]; ok {
		t.Fatal("anchor event must not contain a file path")
	}

	// The excerpt changed in the target commit -> stale.
	h.git.windows["src/a.ts"] = codeintel.FileWindow{
		Path: "src/a.ts", Lines: []string{"one", "changed"}, StartLine: 1, EndLine: 2, TotalLines: 2,
	}
	stale, err := h.service.Validate(h.ctx, 1, anchor.ID)
	if err != nil {
		t.Fatalf("Validate stale: %v", err)
	}
	if stale.Status != string(codeintel.AnchorStale) {
		t.Fatalf("stale status = %q", stale.Status)
	}

	// The file disappeared -> broken.
	h.git.readErr = &platform.Error{Status: 404, Code: "path_not_found", Message: "missing"}
	broken, err := h.service.Validate(h.ctx, 1, anchor.ID)
	if err != nil {
		t.Fatalf("Validate broken: %v", err)
	}
	if broken.Status != string(codeintel.AnchorBroken) {
		t.Fatalf("broken status = %q", broken.Status)
	}

	if len(h.audit.entries) != 4 { // created + three validations
		t.Fatalf("audit entries = %+v", h.audit.entries)
	}
}

func TestAnchorValidateMarksMovedCommitStaleWithoutExcerpt(t *testing.T) {
	h := newAnchorHarness()
	h.seedWindow("src/a.ts", "one")
	anchor, err := h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
		RepoID: 10, FilePath: "src/a.ts", CommitSHA: anchorCommitA,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h.seedDoneRun(anchorCommitB)
	updated, err := h.service.Validate(h.ctx, 1, anchor.ID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if updated.Status != string(codeintel.AnchorStale) {
		t.Fatalf("status = %q", updated.Status)
	}
}

func TestAnchorValidateUnknownAnchorOrVersion(t *testing.T) {
	h := newAnchorHarness()
	_, err := h.service.Validate(h.ctx, 1, 999)
	assertPlatformError(t, err, 404, "not_found")

	h.seedWindow("src/a.ts", "one")
	anchor, err := h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
		RepoID: 10, FilePath: "src/a.ts",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = h.service.Validate(h.ctx, 2, anchor.ID)
	assertPlatformError(t, err, 404, "not_found")
}

// --- list/delete -----------------------------------------------------------------

func TestAnchorListAndDelete(t *testing.T) {
	h := newAnchorHarness()
	h.seedWindow("src/a.ts", "one")
	first, err := h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
		RepoID: 10, FilePath: "src/a.ts",
	})
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := h.service.Create(h.ctx, CreateAnchorInput{
		VersionID: 1, SubjectKind: codeintel.AnchorSubjectCaseVersion, SubjectID: 200,
		RepoID: 10, FilePath: "src/a.ts",
	})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	list, err := h.service.List(h.ctx, 1, "requirement", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("list = %+v", list)
	}
	list, err = h.service.List(h.ctx, 1, "", 200)
	if err != nil {
		t.Fatalf("List by subject: %v", err)
	}
	if len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("list by subject = %+v", list)
	}
	if _, err := h.service.List(h.ctx, 1, "unknown", 0); err == nil {
		t.Fatal("invalid subject kind must fail")
	}

	if err := h.service.Delete(h.ctx, 1, first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := h.service.Delete(h.ctx, 1, first.ID); err == nil {
		t.Fatal("second delete must fail")
	}
	if err := h.service.Delete(h.ctx, 2, second.ID); err == nil {
		t.Fatal("delete from another version must fail")
	}
	entries := 0
	for _, entry := range h.audit.entries {
		if entry.Action == "code.anchor.deleted" {
			entries++
		}
	}
	if entries != 1 {
		t.Fatalf("delete audits = %d", entries)
	}
}

// --- shared assertions -----------------------------------------------------------

func assertPlatformError(t *testing.T, err error, status int, code string) *platform.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %d/%s, got nil", status, code)
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *platform.Error", err)
	}
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("error = %d/%s, want %d/%s (%v)", apiErr.Status, apiErr.Code, status, code, err)
	}
	return apiErr
}
