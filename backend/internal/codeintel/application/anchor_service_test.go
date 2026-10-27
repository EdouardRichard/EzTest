package application

import (
	"context"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// anchorStore is a stateful in-memory AnchorRepository used by the anchor
// service unit tests. It enforces the same point uniqueness and version-scoped
// delete rules as the PostgreSQL adapter.
type anchorStore struct {
	mu        sync.Mutex
	anchors   map[int64]domain.CodeAnchor
	createErr error
	listErr   error
	deleteErr error

	listCalls   []domain.AnchorFilter
	pathCalls   [][]string
	deleteCalls []string
}

func newAnchorStore() *anchorStore {
	return &anchorStore{anchors: make(map[int64]domain.CodeAnchor)}
}

func (s *anchorStore) Create(_ context.Context, anchor domain.CodeAnchor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	for _, existing := range s.anchors {
		if existing.VersionID == anchor.VersionID &&
			existing.SubjectKind == anchor.SubjectKind &&
			existing.SubjectID == anchor.SubjectID &&
			existing.FilePath == anchor.FilePath &&
			anchorLine(existing.StartLine) == anchorLine(anchor.StartLine) &&
			anchorLine(existing.EndLine) == anchorLine(anchor.EndLine) {
			return domain.NewConflict("anchor_duplicate", "duplicate anchor")
		}
	}
	s.anchors[anchor.ID] = anchor
	return nil
}

func anchorLine(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func (s *anchorStore) Delete(_ context.Context, versionID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	anchor, ok := s.anchors[id]
	if !ok || anchor.VersionID != versionID {
		return domain.NewNotFound("anchor_not_found", "anchor not found")
	}
	delete(s.anchors, id)
	s.deleteCalls = append(s.deleteCalls, anchor.FilePath)
	return nil
}

func (s *anchorStore) Get(_ context.Context, versionID, id int64) (domain.CodeAnchor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	anchor, ok := s.anchors[id]
	if !ok || anchor.VersionID != versionID {
		return domain.CodeAnchor{}, domain.NewNotFound("anchor_not_found", "anchor not found")
	}
	return anchor, nil
}

func (s *anchorStore) List(_ context.Context, versionID int64, filter domain.AnchorFilter) ([]domain.CodeAnchor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls = append(s.listCalls, filter)
	if s.listErr != nil {
		return nil, s.listErr
	}
	anchors := make([]domain.CodeAnchor, 0)
	for _, anchor := range s.anchors {
		if anchor.VersionID != versionID {
			continue
		}
		if filter.SubjectKind != "" && anchor.SubjectKind != filter.SubjectKind {
			continue
		}
		if filter.SubjectID != nil && anchor.SubjectID != *filter.SubjectID {
			continue
		}
		anchors = append(anchors, anchor)
	}
	sortAnchorsByID(anchors)
	return anchors, nil
}

func (s *anchorStore) ListByPaths(_ context.Context, versionID int64, paths []string) ([]domain.CodeAnchor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pathCalls = append(s.pathCalls, append([]string(nil), paths...))
	wanted := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		wanted[path] = struct{}{}
	}
	anchors := make([]domain.CodeAnchor, 0)
	for _, anchor := range s.anchors {
		if anchor.VersionID != versionID {
			continue
		}
		if _, ok := wanted[anchor.FilePath]; ok {
			anchors = append(anchors, anchor)
		}
	}
	sortAnchorsByID(anchors)
	return anchors, nil
}

func sortAnchorsByID(anchors []domain.CodeAnchor) {
	for i := 1; i < len(anchors); i++ {
		for j := i; j > 0 && anchors[j].ID < anchors[j-1].ID; j-- {
			anchors[j], anchors[j-1] = anchors[j-1], anchors[j]
		}
	}
}

// anchorFixture assembles the anchor service over deterministic test doubles.
type anchorFixture struct {
	repos     *stubRepos
	versions  *stubVersions
	appModels *impactAppModels
	git       *stubGit
	anchors   *anchorStore
	ids       *stubIDs
	clock     *stubClock
	svc       *AnchorService
}

func newAnchorFixture(t *testing.T) *anchorFixture {
	t.Helper()
	f := &anchorFixture{
		repos: &stubRepos{infos: map[int64]RepoInfo{
			impactRepoID: {
				ID:          impactRepoID,
				Key:         "web",
				LocalPath:   "/repo",
				FixedBranch: "main",
				Status:      "active",
			},
		}},
		versions: &stubVersions{
			info: VersionInfo{
				ID:             impactVersionID,
				SystemID:       impactSystemID,
				HasSystem:      true,
				RepoID:         impactRepoID,
				HasRepo:        true,
				Branch:         "main",
				BaselineCommit: testCommitA,
			},
			requirements: []RequirementInfo{{ID: impactRequirementID, Title: "退款"}},
		},
		appModels: &impactAppModels{nodes: []AppModelNode{{
			ID:       impactAppModelID,
			SystemID: impactSystemID,
			Kind:     domain.KindPage,
			Key:      "page:/orders",
			Name:     "Orders",
			Path:     "/orders",
		}}},
		git:     &stubGit{head: testCommitA},
		anchors: newAnchorStore(),
		ids:     &stubIDs{next: 6000},
		clock:   &stubClock{now: impactTestTime},
	}
	f.svc = NewAnchorService(AnchorServiceDeps{
		Repos:     f.repos,
		Versions:  f.versions,
		AppModels: f.appModels,
		Git:       f.git,
		Anchors:   f.anchors,
		IDs:       f.ids,
		Clock:     f.clock,
	})
	return f
}

// TestAnchorCreateValidationAndDuplicate pins design spec §6.7: subject
// ownership, file existence at the fixed branch tip, line validation, path
// normalisation, uniqueness, version-scoped deletion and the case/issue
// rejection.
func TestAnchorCreateValidationAndDuplicate(t *testing.T) {
	f := newAnchorFixture(t)
	ctx := context.Background()
	start, end := 2, 3

	anchor, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement,
		SubjectID:   impactRequirementID,
		FilePath:    impactClientPath,
		StartLine:   &start,
		EndLine:     &end,
	})
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}
	if anchor.ID == 0 || anchor.VersionID != impactVersionID || anchor.RepoID != impactRepoID {
		t.Fatalf("created anchor = %+v, want snowflake id and the version repository", anchor)
	}
	if anchor.SubjectKind != domain.SubjectRequirement || anchor.SubjectID != impactRequirementID {
		t.Fatalf("created subject = %s/%d", anchor.SubjectKind, anchor.SubjectID)
	}
	if anchor.FilePath != impactClientPath || anchorLine(anchor.StartLine) != 2 || anchorLine(anchor.EndLine) != 3 {
		t.Fatalf("created anchor location = %+v", anchor)
	}
	if anchor.Confidence != domain.ConfidenceHigh || anchor.CreatedByKind != "user" || anchor.CreatedBy != 7 {
		t.Fatalf("created anchor provenance = %+v", anchor)
	}
	if !anchor.CreatedAt.Equal(impactTestTime) {
		t.Fatalf("created at = %v, want the injected clock", anchor.CreatedAt)
	}
	if len(f.git.fileCalls) != 1 || f.git.fileCalls[0].commit != testCommitA ||
		f.git.fileCalls[0].path != impactClientPath || f.git.fileCalls[0].localPath != "/repo" {
		t.Fatalf("file validation calls = %+v, want the fixed branch tip", f.git.fileCalls)
	}

	// Duplicate point: same version/subject/file/lines -> 409 anchor_duplicate.
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath, StartLine: &start, EndLine: &end,
	}); domain.Reason(err) != "anchor_duplicate" {
		t.Fatalf("duplicate error = %v, want anchor_duplicate", err)
	}

	// Same point without the line range is a different anchor.
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath,
	}); err != nil {
		t.Fatalf("same-file anchor without lines error = %v", err)
	}

	// App model anchor: node must belong to the version's bound system.
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectAppModel, SubjectID: impactAppModelID,
		FilePath: impactServerPath,
	}); err != nil {
		t.Fatalf("app model anchor error = %v", err)
	}
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectAppModel, SubjectID: 999999,
		FilePath: impactServerPath,
	}); domain.Reason(err) != "app_model_not_found" {
		t.Fatalf("unknown app model error = %v, want app_model_not_found", err)
	}
	// A version without a system binding cannot accept app model anchors.
	f.versions.info.HasSystem = false
	f.versions.info.SystemID = 0
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectAppModel, SubjectID: impactAppModelID,
		FilePath: impactServerPath,
	}); domain.Reason(err) != "app_model_not_found" {
		t.Fatalf("unbound system app model error = %v, want app_model_not_found", err)
	}
	f.versions.info.HasSystem = true
	f.versions.info.SystemID = impactSystemID

	// Version subject must equal the path version.
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectVersion, SubjectID: impactVersionID,
		FilePath: impactServerPath,
	}); err != nil {
		t.Fatalf("version anchor error = %v", err)
	}
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectVersion, SubjectID: impactVersionID + 1,
		FilePath: impactServerPath,
	}); domain.Reason(err) != "subject_not_in_version" {
		t.Fatalf("version subject mismatch error = %v, want subject_not_in_version", err)
	}

	// Requirement must belong to the version.
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: 999999,
		FilePath: impactServerPath,
	}); domain.Reason(err) != "subject_not_in_version" {
		t.Fatalf("foreign requirement error = %v, want subject_not_in_version", err)
	}

	// case/issue are rejected before any lookup.
	for _, kind := range []string{"case", "issue"} {
		if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
			SubjectKind: kind, SubjectID: impactRequirementID, FilePath: impactClientPath,
		}); domain.Reason(err) != "unsupported_subject_kind" {
			t.Fatalf("%s error = %v, want unsupported_subject_kind", kind, err)
		}
	}

	// Escaping paths are rejected; backslashes are normalised.
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: "../outside.ts",
	}); domain.Reason(err) != "path_escape" {
		t.Fatalf("escape error = %v, want path_escape", err)
	}
	normalized, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: `web\src\utils.ts`,
	})
	if err != nil {
		t.Fatalf("normalized path error = %v", err)
	}
	if normalized.FilePath != "web/src/utils.ts" {
		t.Fatalf("normalized path = %q, want web/src/utils.ts", normalized.FilePath)
	}

	// Line ranges must be complete and ordered.
	wideStart, narrowEnd := 5, 2
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath, StartLine: &wideStart, EndLine: &narrowEnd,
	}); domain.Reason(err) != "invalid_line_range" {
		t.Fatalf("reversed line range error = %v, want invalid_line_range", err)
	}
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath, StartLine: &start,
	}); domain.Reason(err) != "invalid_line_range" {
		t.Fatalf("start without end error = %v, want invalid_line_range", err)
	}

	// Missing or non-regular files are rejected at the fixed branch tip.
	f.git.fileErr = domain.NewInvalid("path", "path_not_found", "missing file")
	if _, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactPlainPath,
	}); domain.Reason(err) != "path_not_found" {
		t.Fatalf("missing file error = %v, want path_not_found", err)
	}
	f.git.fileErr = nil

	// Listing applies the subject filter.
	requirementSubject := impactRequirementID
	listed, err := f.svc.List(ctx, impactVersionID, domain.AnchorFilter{
		SubjectKind: domain.SubjectRequirement, SubjectID: &requirementSubject,
	})
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("listed anchors = %+v, want the three requirement anchors", listed)
	}
	for _, item := range listed {
		if item.SubjectKind != domain.SubjectRequirement || item.SubjectID != impactRequirementID {
			t.Fatalf("filtered anchor = %+v", item)
		}
	}
	if len(f.anchors.listCalls) == 0 {
		t.Fatal("List did not reach the repository")
	}
}

// TestAnchorDeleteScopedToVersion pins §6.7's delete ownership: an anchor must
// belong to the path version, deleting it twice is a not-found.
func TestAnchorDeleteScopedToVersion(t *testing.T) {
	f := newAnchorFixture(t)
	ctx := context.Background()

	created, err := f.svc.Create(ctx, impactVersionID, 7, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath,
	})
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}
	// A foreign version id must not delete the anchor.
	if err := f.svc.Delete(ctx, impactVersionID+1, created.ID); domain.Reason(err) != "anchor_not_found" {
		t.Fatalf("foreign delete error = %v, want anchor_not_found", err)
	}
	if err := f.svc.Delete(ctx, impactVersionID, created.ID); err != nil {
		t.Fatalf("Delete error = %v", err)
	}
	if err := f.svc.Delete(ctx, impactVersionID, created.ID); domain.Reason(err) != "anchor_not_found" {
		t.Fatalf("second delete error = %v, want anchor_not_found", err)
	}
}
