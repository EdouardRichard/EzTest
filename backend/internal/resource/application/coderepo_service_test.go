package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// codeRepoTestNow is the fixed instant every code repository test uses.
var codeRepoTestNow = time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)

// codeRepoTestCommit is a plausible full Git commit id.
const codeRepoTestCommit = "0123456789abcdef0123456789abcdef01234567"

// codeRepoTestClock is a fixed application.Clock.
type codeRepoTestClock struct{ now time.Time }

// Now returns the fixed instant.
func (c codeRepoTestClock) Now() time.Time { return c.now }

// indexedAtCall records one UpdateLastIndexedAt invocation.
type indexedAtCall struct {
	id int64
	at time.Time
}

// fakeCodeRepoRepo is an in-memory domain.CodeRepoRepository.
type fakeCodeRepoRepo struct {
	codeRepos      map[int64]domain.CodeRepository
	nextID         int64
	createCalls    int
	getCalls       int
	updateCalls    int
	deleteCalls    int
	indexedAtCalls []indexedAtCall
	lastFilter     domain.CodeRepoFilter
}

func newFakeCodeRepoRepo() *fakeCodeRepoRepo {
	return &fakeCodeRepoRepo{codeRepos: map[int64]domain.CodeRepository{}, nextID: 1}
}

// seed stores an existing row and returns the stored copy.
func (r *fakeCodeRepoRepo) seed(codeRepo domain.CodeRepository) domain.CodeRepository {
	if codeRepo.Status == "" {
		codeRepo.Status = domain.StatusActive
	}
	codeRepo.ID = r.nextID
	r.nextID++
	r.codeRepos[codeRepo.ID] = codeRepo
	return codeRepo
}

func (r *fakeCodeRepoRepo) CreateCodeRepo(_ context.Context, codeRepo *domain.CodeRepository) error {
	r.createCalls++
	codeRepo.ID = r.nextID
	r.nextID++
	r.codeRepos[codeRepo.ID] = *codeRepo
	return nil
}

func (r *fakeCodeRepoRepo) GetCodeRepo(_ context.Context, id int64) (*domain.CodeRepository, error) {
	r.getCalls++
	codeRepo, ok := r.codeRepos[id]
	if !ok {
		return nil, domain.NewNotFound("code repository", id)
	}
	return &codeRepo, nil
}

func (r *fakeCodeRepoRepo) ListCodeRepos(_ context.Context, f domain.CodeRepoFilter) ([]domain.CodeRepository, bool, error) {
	r.lastFilter = f
	ids := make([]int64, 0, len(r.codeRepos))
	for id := range r.codeRepos {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	matches := make([]domain.CodeRepository, 0, len(ids))
	for _, id := range ids {
		codeRepo := r.codeRepos[id]
		if codeRepo.ID <= f.Page.AfterID {
			continue
		}
		if f.Status != "" && codeRepo.Status != f.Status {
			continue
		}
		matches = append(matches, codeRepo)
	}
	limit := f.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

func (r *fakeCodeRepoRepo) UpdateCodeRepo(_ context.Context, codeRepo *domain.CodeRepository) error {
	if _, ok := r.codeRepos[codeRepo.ID]; !ok {
		return domain.NewNotFound("code repository", codeRepo.ID)
	}
	r.updateCalls++
	r.codeRepos[codeRepo.ID] = *codeRepo
	return nil
}

// UpdateLastIndexedAt mimics the narrow repository write: only an active row is
// updated, a disabled row keeps the 422 reason and a missing row is not found.
// The call is recorded first so the tests can prove the service delegates.
func (r *fakeCodeRepoRepo) UpdateLastIndexedAt(_ context.Context, id int64, at time.Time) error {
	r.indexedAtCalls = append(r.indexedAtCalls, indexedAtCall{id: id, at: at})
	codeRepo, ok := r.codeRepos[id]
	if !ok {
		return domain.NewNotFound("code repository", id)
	}
	if codeRepo.Status != domain.StatusActive {
		return domain.NewInvalid("repoId", "code_repository_disabled",
			fmt.Sprintf("code repository %d is disabled", id))
	}
	indexedAt := at
	codeRepo.LastIndexedAt = &indexedAt
	r.codeRepos[id] = codeRepo
	return nil
}

func (r *fakeCodeRepoRepo) DeleteCodeRepo(_ context.Context, id int64) error {
	if _, ok := r.codeRepos[id]; !ok {
		return domain.NewNotFound("code repository", id)
	}
	r.deleteCalls++
	delete(r.codeRepos, id)
	return nil
}

// fakeGitValidator is a domain.GitValidator whose verdict the test controls.
type fakeGitValidator struct {
	validation domain.GitValidation
	err        error
	calls      int
	paths      []string
	branches   []string
}

func (v *fakeGitValidator) Validate(_ context.Context, path, branch string) (domain.GitValidation, error) {
	v.calls++
	v.paths = append(v.paths, path)
	v.branches = append(v.branches, branch)
	if v.err != nil {
		return domain.GitValidation{}, v.err
	}
	return v.validation, nil
}

// newCodeRepoServiceForTest wires the service to the fakes.
func newCodeRepoServiceForTest() (*CodeRepoService, *fakeCodeRepoRepo, *fakeGitValidator) {
	repos := newFakeCodeRepoRepo()
	git := &fakeGitValidator{validation: domain.GitValidation{Commit: codeRepoTestCommit}}
	service := NewCodeRepoService(repos, git, codeRepoTestClock{now: codeRepoTestNow})
	return service, repos, git
}

// assertCodeRepoDomainError asserts the domain kind; field and reason are only
// checked when non-empty.
func assertCodeRepoDomainError(t *testing.T, err error, kind domain.ErrorKind, field, reason string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %v (%T) is not a *domain.Error", err, err)
	}
	if domainErr.Kind != kind {
		t.Fatalf("kind=%q want %q (message=%q)", domainErr.Kind, kind, domainErr.Message)
	}
	if field != "" && domainErr.Field != field {
		t.Fatalf("field=%q want %q", domainErr.Field, field)
	}
	if reason != "" && domainErr.Reason != reason {
		t.Fatalf("reason=%q want %q", domainErr.Reason, reason)
	}
}

// codeRepoStringPtr returns a pointer to s.
func codeRepoStringPtr(s string) *string { return &s }

func TestCodeRepoServiceCreateValidatesAndStoresCommit(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()
	include := []string{"src/**"}

	created, err := service.Create(context.Background(), CreateCodeRepoInput{
		Key:          "web",
		Name:         "Web",
		LocalPath:    " /srv/repo ",
		FixedBranch:  "main",
		IncludeGlobs: include,
		ExcludeGlobs: []string{"dist/**"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 || repos.createCalls != 1 {
		t.Fatalf("created=%+v createCalls=%d", created, repos.createCalls)
	}
	if created.LocalPath != "/srv/repo" {
		t.Fatalf("localPath=%q want trimmed /srv/repo", created.LocalPath)
	}
	if created.Status != domain.StatusActive {
		t.Fatalf("status=%q want active", created.Status)
	}
	if created.LastCommit != codeRepoTestCommit {
		t.Fatalf("lastCommit=%q want %q", created.LastCommit, codeRepoTestCommit)
	}
	if created.LastValidatedAt == nil || !created.LastValidatedAt.Equal(codeRepoTestNow) {
		t.Fatalf("lastValidatedAt=%v want %v", created.LastValidatedAt, codeRepoTestNow)
	}
	if created.LastValidationError != "" {
		t.Fatalf("lastValidationError=%q want empty", created.LastValidationError)
	}
	if git.calls != 1 || git.paths[0] != "/srv/repo" || git.branches[0] != "main" {
		t.Fatalf("git path=%v branch=%v calls=%d", git.paths, git.branches, git.calls)
	}

	stored := repos.codeRepos[created.ID]
	if stored.LastCommit != codeRepoTestCommit || stored.LastValidatedAt == nil {
		t.Fatalf("stored row=%+v", stored)
	}
	// The caller's slice must not alias the stored row.
	include[0] = "mutated"
	if stored.IncludeGlobs[0] != "src/**" {
		t.Fatalf("includeGlobs=%v want a defensive copy", stored.IncludeGlobs)
	}
}

func TestCodeRepoServiceCreateDefaultsFixedBranch(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()

	created, err := service.Create(context.Background(), CreateCodeRepoInput{
		Key:       "web",
		Name:      "Web",
		LocalPath: "/srv/repo",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.FixedBranch != "main" {
		t.Fatalf("fixedBranch=%q want main", created.FixedBranch)
	}
	if len(git.branches) != 1 || git.branches[0] != "main" {
		t.Fatalf("git branches=%v want [main]", git.branches)
	}
	if repos.codeRepos[created.ID].FixedBranch != "main" {
		t.Fatalf("stored branch=%q want main", repos.codeRepos[created.ID].FixedBranch)
	}
}

func TestCodeRepoServiceCreateValidationFailureDoesNotPersist(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()
	git.err = domain.NewInvalid("localPath", "path_not_git_repo", "local path is not a Git work tree")

	created, err := service.Create(context.Background(), CreateCodeRepoInput{
		Key:         "web",
		Name:        "Web",
		LocalPath:   "/srv/not-a-repo",
		FixedBranch: "main",
	})
	if created != nil {
		t.Fatalf("created=%+v want nil", created)
	}
	assertCodeRepoDomainError(t, err, domain.KindInvalid, "localPath", "path_not_git_repo")
	if repos.createCalls != 0 {
		t.Fatalf("createCalls=%d want 0 on a failed validation", repos.createCalls)
	}
	if len(repos.codeRepos) != 0 {
		t.Fatalf("persisted rows=%v want none", repos.codeRepos)
	}
}

func TestCodeRepoServiceCreateOperationalGitErrorDoesNotPersist(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()
	// A timeout is not a verdict about the checkout: it must not become a 422.
	git.err = fmt.Errorf("gitexec: git rev-parse: %w", context.DeadlineExceeded)

	_, err := service.Create(context.Background(), CreateCodeRepoInput{
		Key:         "web",
		Name:        "Web",
		LocalPath:   "/srv/repo",
		FixedBranch: "main",
	})
	if err == nil {
		t.Fatal("want an error")
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		t.Fatalf("operational git failure mapped to a domain error: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v does not wrap the timeout", err)
	}
	if repos.createCalls != 0 {
		t.Fatalf("createCalls=%d want 0", repos.createCalls)
	}
}

func TestCodeRepoServiceCreateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		in     CreateCodeRepoInput
		field  string
		reason string
	}{
		{
			name:   "key",
			in:     CreateCodeRepoInput{Key: "Bad Key", Name: "Web", LocalPath: "/srv/repo", FixedBranch: "main"},
			field:  "key",
			reason: "invalid_key",
		},
		{
			name: "include glob",
			in: CreateCodeRepoInput{Key: "web", Name: "Web", LocalPath: "/srv/repo", FixedBranch: "main",
				IncludeGlobs: []string{""}},
			field:  "globs",
			reason: "invalid_glob",
		},
		{
			name: "exclude glob",
			in: CreateCodeRepoInput{Key: "web", Name: "Web", LocalPath: "/srv/repo", FixedBranch: "main",
				ExcludeGlobs: []string{""}},
			field:  "globs",
			reason: "invalid_glob",
		},
		{
			name:   "branch",
			in:     CreateCodeRepoInput{Key: "web", Name: "Web", LocalPath: "/srv/repo", FixedBranch: "-evil"},
			field:  "fixedBranch",
			reason: "invalid_branch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, repos, git := newCodeRepoServiceForTest()
			_, err := service.Create(context.Background(), tc.in)
			assertCodeRepoDomainError(t, err, domain.KindInvalid, tc.field, tc.reason)
			if repos.createCalls != 0 {
				t.Fatalf("createCalls=%d want 0", repos.createCalls)
			}
			if git.calls != 0 {
				t.Fatalf("git calls=%d want 0: invalid input is rejected before the checkout check", git.calls)
			}
		})
	}
}

func TestCodeRepoServiceUpdateRevalidatesAndRefreshesCommit(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()
	old := codeRepoTestNow.Add(-time.Hour)
	seeded := repos.seed(domain.CodeRepository{
		Key:             "web",
		Name:            "Web",
		LocalPath:       "/srv/old",
		FixedBranch:     "main",
		Status:          domain.StatusActive,
		LastCommit:      "old-commit",
		LastValidatedAt: &old,
	})

	updated, err := service.Update(context.Background(), seeded.ID, UpdateCodeRepoInput{
		Name:        codeRepoStringPtr("Web v2"),
		LocalPath:   codeRepoStringPtr("/srv/new"),
		FixedBranch: codeRepoStringPtr("develop"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Web v2" || updated.LocalPath != "/srv/new" || updated.FixedBranch != "develop" {
		t.Fatalf("updated=%+v", updated)
	}
	if updated.LastCommit != codeRepoTestCommit {
		t.Fatalf("lastCommit=%q want %q", updated.LastCommit, codeRepoTestCommit)
	}
	if updated.LastValidatedAt == nil || !updated.LastValidatedAt.Equal(codeRepoTestNow) {
		t.Fatalf("lastValidatedAt=%v want %v", updated.LastValidatedAt, codeRepoTestNow)
	}
	if updated.LastValidationError != "" {
		t.Fatalf("lastValidationError=%q want empty", updated.LastValidationError)
	}
	if repos.updateCalls != 1 {
		t.Fatalf("updateCalls=%d want 1", repos.updateCalls)
	}
	if len(git.paths) != 1 || git.paths[0] != "/srv/new" || git.branches[0] != "develop" {
		t.Fatalf("git path=%v branch=%v", git.paths, git.branches)
	}
	if stored := repos.codeRepos[seeded.ID]; stored.LastCommit != codeRepoTestCommit {
		t.Fatalf("stored commit=%q want %q", stored.LastCommit, codeRepoTestCommit)
	}
}

func TestCodeRepoServiceUpdateValidationFailurePreservesOldRow(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()
	git.err = domain.NewInvalid("fixedBranch", "branch_not_found", `branch "nope" does not resolve to a commit`)
	old := codeRepoTestNow.Add(-time.Hour)
	seeded := repos.seed(domain.CodeRepository{
		Key:                 "web",
		Name:                "Web",
		LocalPath:           "/srv/old",
		FixedBranch:         "main",
		Status:              domain.StatusActive,
		LastCommit:          "old-commit",
		LastValidatedAt:     &old,
		LastValidationError: "",
	})

	updated, err := service.Update(context.Background(), seeded.ID, UpdateCodeRepoInput{
		Name:        codeRepoStringPtr("Web v2"),
		LocalPath:   codeRepoStringPtr("/srv/new"),
		FixedBranch: codeRepoStringPtr("nope"),
	})
	if updated != nil {
		t.Fatalf("updated=%+v want nil", updated)
	}
	assertCodeRepoDomainError(t, err, domain.KindInvalid, "fixedBranch", "branch_not_found")
	if repos.updateCalls != 0 {
		t.Fatalf("updateCalls=%d want 0 on a failed validation", repos.updateCalls)
	}
	stored := repos.codeRepos[seeded.ID]
	if stored.Name != "Web" || stored.LocalPath != "/srv/old" || stored.FixedBranch != "main" {
		t.Fatalf("stored row changed: %+v", stored)
	}
	if stored.LastCommit != "old-commit" || stored.LastValidatedAt == nil || !stored.LastValidatedAt.Equal(old) {
		t.Fatalf("stored validation fields changed: %+v", stored)
	}
}

func TestCodeRepoServiceUpdateWithoutCheckoutChangeKeepsValidation(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()
	old := codeRepoTestNow.Add(-time.Hour)
	seeded := repos.seed(domain.CodeRepository{
		Key:             "web",
		Name:            "Web",
		LocalPath:       "/srv/repo",
		FixedBranch:     "main",
		Status:          domain.StatusActive,
		LastCommit:      "old-commit",
		LastValidatedAt: &old,
	})

	updated, err := service.Update(context.Background(), seeded.ID, UpdateCodeRepoInput{
		Name:         codeRepoStringPtr("Web v2"),
		Status:       codeRepoStringPtr(domain.StatusDisabled),
		ExcludeGlobs: &[]string{"dist/**", "vendor/**"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if git.calls != 0 {
		t.Fatalf("git calls=%d want 0: neither path nor branch changed", git.calls)
	}
	if repos.updateCalls != 1 {
		t.Fatalf("updateCalls=%d want 1", repos.updateCalls)
	}
	if updated.Status != domain.StatusDisabled || updated.Name != "Web v2" {
		t.Fatalf("updated=%+v", updated)
	}
	if updated.LastCommit != "old-commit" || updated.LastValidatedAt == nil || !updated.LastValidatedAt.Equal(old) {
		t.Fatalf("validation fields changed without a checkout check: %+v", updated)
	}
	if got := repos.codeRepos[seeded.ID].ExcludeGlobs; len(got) != 2 || got[1] != "vendor/**" {
		t.Fatalf("excludeGlobs=%v", got)
	}
}

func TestCodeRepoServiceUpdateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		in     UpdateCodeRepoInput
		field  string
		reason string
	}{
		{
			name:   "status",
			in:     UpdateCodeRepoInput{Status: codeRepoStringPtr("bogus")},
			field:  "status",
			reason: "invalid_status",
		},
		{
			name:   "globs",
			in:     UpdateCodeRepoInput{IncludeGlobs: &[]string{""}},
			field:  "globs",
			reason: "invalid_glob",
		},
		{
			name:   "branch",
			in:     UpdateCodeRepoInput{FixedBranch: codeRepoStringPtr("-evil")},
			field:  "fixedBranch",
			reason: "invalid_branch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, repos, git := newCodeRepoServiceForTest()
			seeded := repos.seed(domain.CodeRepository{
				Key: "web", Name: "Web", LocalPath: "/srv/repo", FixedBranch: "main",
				Status: domain.StatusActive,
			})
			_, err := service.Update(context.Background(), seeded.ID, tc.in)
			assertCodeRepoDomainError(t, err, domain.KindInvalid, tc.field, tc.reason)
			if repos.updateCalls != 0 {
				t.Fatalf("updateCalls=%d want 0", repos.updateCalls)
			}
			if git.calls != 0 {
				t.Fatalf("git calls=%d want 0", git.calls)
			}
		})
	}
}

func TestCodeRepoServiceValidateRefreshesCommit(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()
	old := codeRepoTestNow.Add(-time.Hour)
	seeded := repos.seed(domain.CodeRepository{
		Key:             "web",
		Name:            "Web",
		LocalPath:       "/srv/repo",
		FixedBranch:     "release/1.2.3",
		Status:          domain.StatusActive,
		LastCommit:      "old-commit",
		LastValidatedAt: &old,
	})

	validated, err := service.Validate(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if validated.LastCommit != codeRepoTestCommit {
		t.Fatalf("lastCommit=%q want %q", validated.LastCommit, codeRepoTestCommit)
	}
	if validated.LastValidatedAt == nil || !validated.LastValidatedAt.Equal(codeRepoTestNow) {
		t.Fatalf("lastValidatedAt=%v want %v", validated.LastValidatedAt, codeRepoTestNow)
	}
	if len(git.paths) != 1 || git.paths[0] != "/srv/repo" || git.branches[0] != "release/1.2.3" {
		t.Fatalf("git path=%v branch=%v", git.paths, git.branches)
	}
	if repos.updateCalls != 1 {
		t.Fatalf("updateCalls=%d want 1", repos.updateCalls)
	}
	if stored := repos.codeRepos[seeded.ID]; stored.LastCommit != codeRepoTestCommit {
		t.Fatalf("stored commit=%q", stored.LastCommit)
	}
}

func TestCodeRepoServiceValidateFailureKeepsStoredValues(t *testing.T) {
	service, repos, git := newCodeRepoServiceForTest()
	git.err = domain.NewInvalid("localPath", "path_not_found", "local path does not exist")
	old := codeRepoTestNow.Add(-time.Hour)
	seeded := repos.seed(domain.CodeRepository{
		Key:             "web",
		Name:            "Web",
		LocalPath:       "/srv/gone",
		FixedBranch:     "main",
		Status:          domain.StatusActive,
		LastCommit:      "old-commit",
		LastValidatedAt: &old,
	})

	validated, err := service.Validate(context.Background(), seeded.ID)
	if validated != nil {
		t.Fatalf("validated=%+v want nil", validated)
	}
	assertCodeRepoDomainError(t, err, domain.KindInvalid, "localPath", "path_not_found")
	if repos.updateCalls != 0 {
		t.Fatalf("updateCalls=%d want 0", repos.updateCalls)
	}
	stored := repos.codeRepos[seeded.ID]
	if stored.LastCommit != "old-commit" || stored.LastValidatedAt == nil || !stored.LastValidatedAt.Equal(old) {
		t.Fatalf("stored validation fields changed: %+v", stored)
	}
}

func TestCodeRepoServiceValidateNotFound(t *testing.T) {
	service, _, git := newCodeRepoServiceForTest()
	_, err := service.Validate(context.Background(), 99)
	assertCodeRepoDomainError(t, err, domain.KindNotFound, "", "not_found")
	if git.calls != 0 {
		t.Fatalf("git calls=%d want 0", git.calls)
	}
}

func TestCodeRepoServiceGetAndDelete(t *testing.T) {
	service, repos, _ := newCodeRepoServiceForTest()
	seeded := repos.seed(domain.CodeRepository{Key: "web", Name: "Web", LocalPath: "/srv/repo", FixedBranch: "main"})

	got, err := service.Get(context.Background(), seeded.ID)
	if err != nil || got.Key != "web" {
		t.Fatalf("get=%+v err=%v", got, err)
	}

	if err := service.Delete(context.Background(), seeded.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if repos.deleteCalls != 1 || len(repos.codeRepos) != 0 {
		t.Fatalf("deleteCalls=%d rows=%v", repos.deleteCalls, repos.codeRepos)
	}

	_, err = service.Get(context.Background(), seeded.ID)
	assertCodeRepoDomainError(t, err, domain.KindNotFound, "", "not_found")
	err = service.Delete(context.Background(), seeded.ID)
	assertCodeRepoDomainError(t, err, domain.KindNotFound, "", "not_found")
}

// TestCodeRepoServiceMarkIndexed pins the P4 index-state write: the repository
// must exist and be active, and a successful call writes last_indexed_at through
// the narrow port only. The service must not read or rewrite the whole P2 row
// (ruling R44), so a concurrent admin edit can never be lost.
func TestCodeRepoServiceMarkIndexed(t *testing.T) {
	t.Run("records the index timestamp only", func(t *testing.T) {
		service, repos, git := newCodeRepoServiceForTest()
		old := codeRepoTestNow.Add(-48 * time.Hour)
		seeded := repos.seed(domain.CodeRepository{
			Key:             "web",
			Name:            "Web",
			LocalPath:       "/srv/repo",
			FixedBranch:     "main",
			Status:          domain.StatusActive,
			LastCommit:      "old-commit",
			LastValidatedAt: &old,
			LastIndexedAt:   &old,
		})

		indexedAt := codeRepoTestNow.Add(time.Minute)
		if err := service.MarkIndexed(context.Background(), seeded.ID, codeRepoTestCommit, indexedAt); err != nil {
			t.Fatalf("MarkIndexed: %v", err)
		}

		if len(repos.indexedAtCalls) != 1 || repos.indexedAtCalls[0].id != seeded.ID || !repos.indexedAtCalls[0].at.Equal(indexedAt) {
			t.Fatalf("UpdateLastIndexedAt calls=%+v want one %d/%v", repos.indexedAtCalls, seeded.ID, indexedAt)
		}
		if repos.updateCalls != 0 {
			t.Fatalf("updateCalls=%d want 0: MarkIndexed must not rewrite the whole row", repos.updateCalls)
		}
		if repos.getCalls != 0 {
			t.Fatalf("getCalls=%d want 0: the narrow write needs no full-row read", repos.getCalls)
		}

		stored := repos.codeRepos[seeded.ID]
		if stored.LastIndexedAt == nil || !stored.LastIndexedAt.Equal(indexedAt) {
			t.Fatalf("lastIndexedAt=%v want %v", stored.LastIndexedAt, indexedAt)
		}
		if stored.LastCommit != "old-commit" {
			t.Fatalf("lastCommit=%q want the validation commit untouched", stored.LastCommit)
		}
		if stored.Status != domain.StatusActive {
			t.Fatalf("status=%q want active", stored.Status)
		}
		if stored.LastValidatedAt == nil || !stored.LastValidatedAt.Equal(old) {
			t.Fatalf("lastValidatedAt=%v want %v", stored.LastValidatedAt, old)
		}
		if git.calls != 0 {
			t.Fatalf("git calls=%d want 0: indexing must not re-validate the checkout", git.calls)
		}
	})

	t.Run("missing repository is not found", func(t *testing.T) {
		service, repos, _ := newCodeRepoServiceForTest()

		err := service.MarkIndexed(context.Background(), 404, codeRepoTestCommit, codeRepoTestNow)
		assertCodeRepoDomainError(t, err, domain.KindNotFound, "", "not_found")
		if len(repos.indexedAtCalls) != 1 {
			t.Fatalf("UpdateLastIndexedAt calls=%d want 1 (the port reports not found)", len(repos.indexedAtCalls))
		}
		if repos.updateCalls != 0 || repos.getCalls != 0 {
			t.Fatalf("updateCalls=%d getCalls=%d want 0/0", repos.updateCalls, repos.getCalls)
		}
	})

	t.Run("disabled repository is rejected", func(t *testing.T) {
		service, repos, _ := newCodeRepoServiceForTest()
		seeded := repos.seed(domain.CodeRepository{
			Key:         "web",
			Name:        "Web",
			LocalPath:   "/srv/repo",
			FixedBranch: "main",
			Status:      domain.StatusDisabled,
			LastCommit:  "old-commit",
		})

		err := service.MarkIndexed(context.Background(), seeded.ID, codeRepoTestCommit, codeRepoTestNow)
		assertCodeRepoDomainError(t, err, domain.KindInvalid, "repoId", "code_repository_disabled")
		if len(repos.indexedAtCalls) != 1 {
			t.Fatalf("UpdateLastIndexedAt calls=%d want 1 (the port reports disabled)", len(repos.indexedAtCalls))
		}
		if repos.updateCalls != 0 || repos.getCalls != 0 {
			t.Fatalf("updateCalls=%d getCalls=%d want 0/0", repos.updateCalls, repos.getCalls)
		}
		if stored := repos.codeRepos[seeded.ID]; stored.LastIndexedAt != nil {
			t.Fatalf("lastIndexedAt=%v want nil", stored.LastIndexedAt)
		}
	})
}

func TestCodeRepoServiceList(t *testing.T) {
	service, repos, _ := newCodeRepoServiceForTest()
	active := repos.seed(domain.CodeRepository{Key: "web", Status: domain.StatusActive})
	disabled := repos.seed(domain.CodeRepository{Key: "old", Status: domain.StatusDisabled})

	page, hasMore, err := service.List(context.Background(), domain.CodeRepoFilter{
		Page: domain.Page{Limit: 1},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page) != 1 || page[0].ID != active.ID || !hasMore {
		t.Fatalf("page=%v hasMore=%v", page, hasMore)
	}

	page, hasMore, err = service.List(context.Background(), domain.CodeRepoFilter{
		Status: domain.StatusDisabled,
		Page:   domain.Page{Limit: 20, AfterID: active.ID},
	})
	if err != nil {
		t.Fatalf("list disabled: %v", err)
	}
	if len(page) != 1 || page[0].ID != disabled.ID || hasMore {
		t.Fatalf("page=%v hasMore=%v", page, hasMore)
	}
	if repos.lastFilter.Status != domain.StatusDisabled {
		t.Fatalf("filter status=%q", repos.lastFilter.Status)
	}
}
