package application_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// testNow is the base of every fake version timestamp.
var testNow = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

// testCommit is a valid (40 lowercase hex) Git commit id.
const testCommit = "0123456789abcdef0123456789abcdef01234567"

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// fixedIDs is an application.IDGenerator that yields a scripted sequence.
type fixedIDs struct {
	next   int64
	issued []int64
}

// Next returns the next id in the sequence.
func (g *fixedIDs) Next() int64 {
	id := g.next
	g.next++
	g.issued = append(g.issued, id)
	return id
}

// fakeVersionRepo is an in-memory domain.VersionRepository. It records calls
// and stamps rows from the fixed test clock so list ordering is deterministic.
type fakeVersionRepo struct {
	versions   map[int64]domain.Version
	counts     map[int64]domain.VersionCounts
	warnings   map[int64]domain.Warnings
	createErr  error
	listErr    error
	updates    int
	binds      int
	statuses   int
	lastFilter domain.VersionFilter
	// beforeStatus, when set, runs once just before the next status
	// compare-and-swap. Tests use it to simulate a concurrent writer that
	// commits between the service's read and its CAS.
	beforeStatus func(id int64)
}

// newFakeVersionRepo returns an empty repository.
func newFakeVersionRepo() *fakeVersionRepo {
	return &fakeVersionRepo{
		versions: map[int64]domain.Version{},
		counts:   map[int64]domain.VersionCounts{},
		warnings: map[int64]domain.Warnings{},
	}
}

// seed stores an existing version and returns it.
func (r *fakeVersionRepo) seed(v domain.Version) domain.Version {
	if v.Status == "" {
		v.Status = domain.VersionDraft
	}
	if v.Revision == 0 {
		v.Revision = 1
	}
	v.CreatedAt = testNow.Add(time.Duration(v.ID) * time.Minute)
	v.UpdatedAt = v.CreatedAt
	r.versions[v.ID] = v
	return v
}

// stamp returns the deterministic updated_at for one write.
func (r *fakeVersionRepo) stamp(id int64, revision int) time.Time {
	return testNow.Add(time.Duration(id)*time.Minute + time.Duration(revision)*time.Second)
}

// Create stores v and writes server timestamps back to it.
func (r *fakeVersionRepo) Create(_ context.Context, v *domain.Version) error {
	if r.createErr != nil {
		return r.createErr
	}
	if _, exists := r.versions[v.ID]; exists {
		return domain.NewConflict("duplicate_key", "version already exists")
	}
	v.CreatedAt = r.stamp(v.ID, 0)
	v.UpdatedAt = v.CreatedAt
	r.versions[v.ID] = *v
	return nil
}

// Get returns one stored version.
func (r *fakeVersionRepo) Get(_ context.Context, id int64) (*domain.Version, error) {
	stored, ok := r.versions[id]
	if !ok {
		return nil, domain.NewNotFound("version", id)
	}
	return &stored, nil
}

// List filters by status and applies the requested limit.
func (r *fakeVersionRepo) List(_ context.Context, f domain.VersionFilter) ([]domain.Version, bool, error) {
	r.lastFilter = f
	if r.listErr != nil {
		return nil, false, r.listErr
	}
	ids := make([]int64, 0, len(r.versions))
	for id := range r.versions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := r.versions[ids[i]], r.versions[ids[j]]
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.ID > right.ID
	})
	matches := make([]domain.Version, 0, len(ids))
	for _, id := range ids {
		stored := r.versions[id]
		if f.Status != "" && stored.Status != f.Status {
			continue
		}
		matches = append(matches, stored)
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

// Counts returns the configured counters of one version.
func (r *fakeVersionRepo) Counts(_ context.Context, versionID int64) (domain.VersionCounts, error) {
	if _, ok := r.versions[versionID]; !ok {
		return domain.VersionCounts{}, domain.NewNotFound("version", versionID)
	}
	return r.counts[versionID], nil
}

// UpdateMetadata applies a revision-checked metadata compare-and-swap.
func (r *fakeVersionRepo) UpdateMetadata(_ context.Context, id int64, expected int, name, label, desc string, actor int64) (*domain.Version, error) {
	stored, ok := r.versions[id]
	if !ok {
		return nil, domain.NewStateConflict("revision_conflict", "version revision does not match")
	}
	if stored.Revision != expected {
		return nil, domain.NewStateConflict("revision_conflict", "version revision does not match")
	}
	r.updates++
	stored.Name, stored.Label, stored.Description = name, label, desc
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return &stored, nil
}

// UpdateBindings applies a revision-checked binding compare-and-swap.
func (r *fakeVersionRepo) UpdateBindings(_ context.Context, id int64, expected int, b domain.VersionBindings, actor int64) (*domain.Version, error) {
	stored, ok := r.versions[id]
	if !ok {
		return nil, domain.NewStateConflict("revision_conflict", "version revision does not match")
	}
	if stored.Revision != expected {
		return nil, domain.NewStateConflict("revision_conflict", "version revision does not match")
	}
	r.binds++
	stored.SystemID, stored.EnvironmentID, stored.CodeRepositoryID = b.SystemID, b.EnvironmentID, b.CodeRepositoryID
	stored.CodeBranch, stored.CodeBaselineCommit = b.CodeBranch, b.CodeBaselineCommit
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return &stored, nil
}

// UpdateStatus applies a status compare-and-swap.
func (r *fakeVersionRepo) UpdateStatus(_ context.Context, id int64, from, to string, actor int64) (*domain.Version, error) {
	if r.beforeStatus != nil {
		hook := r.beforeStatus
		r.beforeStatus = nil
		hook(id)
	}
	stored, ok := r.versions[id]
	if !ok || stored.Status != from {
		return nil, domain.NewStateConflict("version_state_conflict", "version status does not allow this action")
	}
	r.statuses++
	stored.Status = to
	stored.Revision++
	stored.UpdatedBy = actor
	stored.UpdatedAt = r.stamp(id, stored.Revision)
	r.versions[id] = stored
	return &stored, nil
}

// ListUnclarified returns the configured warnings of one version.
func (r *fakeVersionRepo) ListUnclarified(_ context.Context, versionID int64) ([]int64, int, error) {
	w := r.warnings[versionID]
	return w.UnclarifiedRequirementIDs, w.OpenQuestionCount, nil
}

// fakeCatalog is a ResourceCatalog with per-id lookups and programmable errors.
type fakeCatalog struct {
	systems      map[int64]application.ResourceRef
	environments map[int64]application.ResourceRef
	repos        map[int64]application.CodeRepoRef
	systemErrs   map[int64]error
	envErrs      map[int64]error
	repoErrs     map[int64]error
	lookups      []string
}

// newFakeCatalog returns a catalog with one active system, environment and
// repository: system 7, environment 8 (owned by 7) and repository 9.
func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{
		systems: map[int64]application.ResourceRef{
			7: {ID: 7, Name: "Order system", Status: "active", Available: true},
		},
		environments: map[int64]application.ResourceRef{
			8: {ID: 8, Name: "staging", Status: "active", SystemID: ptr(int64(7)), Available: true},
		},
		repos: map[int64]application.CodeRepoRef{
			9: {
				ID: 9, Name: "order-web", Status: "active", LocalPath: "/srv/order-web",
				FixedBranch: "main", IncludeGlobs: []string{"src/**"}, Available: true,
			},
		},
		systemErrs: map[int64]error{},
		envErrs:    map[int64]error{},
		repoErrs:   map[int64]error{},
	}
}

// GetSystem returns the configured system.
func (c *fakeCatalog) GetSystem(_ context.Context, id int64) (application.ResourceRef, error) {
	c.lookups = append(c.lookups, fmt.Sprintf("system:%d", id))
	if err := c.systemErrs[id]; err != nil {
		return application.ResourceRef{}, err
	}
	ref, ok := c.systems[id]
	if !ok {
		return application.ResourceRef{}, &application.ResourceError{Reason: "system_not_found", Message: "system not found"}
	}
	return ref, nil
}

// GetEnvironment returns the configured environment.
func (c *fakeCatalog) GetEnvironment(_ context.Context, id int64) (application.ResourceRef, error) {
	c.lookups = append(c.lookups, fmt.Sprintf("environment:%d", id))
	if err := c.envErrs[id]; err != nil {
		return application.ResourceRef{}, err
	}
	ref, ok := c.environments[id]
	if !ok {
		return application.ResourceRef{}, &application.ResourceError{Reason: "environment_not_found", Message: "environment not found"}
	}
	return ref, nil
}

// GetCodeRepository returns the configured repository.
func (c *fakeCatalog) GetCodeRepository(_ context.Context, id int64) (application.CodeRepoRef, error) {
	c.lookups = append(c.lookups, fmt.Sprintf("repository:%d", id))
	if err := c.repoErrs[id]; err != nil {
		return application.CodeRepoRef{}, err
	}
	ref, ok := c.repos[id]
	if !ok {
		return application.CodeRepoRef{}, &application.ResourceError{Reason: "code_repository_not_found", Message: "repository not found"}
	}
	return ref, nil
}

// fakeGit is a GitReader that records ResolveCommit calls.
type fakeGit struct {
	commit string
	err    error
	calls  []string
}

// ResolveCommit returns the configured commit or error.
func (g *fakeGit) ResolveCommit(_ context.Context, repoPath, branch string) (string, error) {
	g.calls = append(g.calls, repoPath+"@"+branch)
	if g.err != nil {
		return "", g.err
	}
	if g.commit != "" {
		return g.commit, nil
	}
	return testCommit, nil
}

// Tree is unused by VersionService.
func (g *fakeGit) Tree(context.Context, string, string, []string, []string, int) ([]application.CodeEntry, error) {
	return nil, nil
}

// Read is unused by VersionService.
func (g *fakeGit) Read(context.Context, string, string, string, int) (string, error) {
	return "", nil
}

// fakeEvents records published version events.
type fakeEvents struct {
	events     []application.VersionEvent
	publishErr error
}

// Publish records e and returns the configured error.
func (f *fakeEvents) Publish(_ context.Context, e application.VersionEvent) error {
	f.events = append(f.events, e)
	return f.publishErr
}

// versionFixture wires the real service to in-memory fakes.
type versionFixture struct {
	service *application.VersionService
	repo    *fakeVersionRepo
	catalog *fakeCatalog
	git     *fakeGit
	events  *fakeEvents
	ids     *fixedIDs
}

// newVersionFixture returns a fixture with id 1000 as the first generated id.
func newVersionFixture() *versionFixture {
	f := &versionFixture{
		repo:    newFakeVersionRepo(),
		catalog: newFakeCatalog(),
		git:     &fakeGit{commit: testCommit},
		events:  &fakeEvents{},
		ids:     &fixedIDs{next: 1000},
	}
	f.service = application.NewVersionService(
		f.repo,
		f.catalog,
		f.git,
		f.events,
		application.WithIDGenerator(f.ids),
	)
	return f
}

// assertDomainReason asserts err is a *domain.Error with kind and reason.
func assertDomainReason(t *testing.T, err error, kind domain.ErrorKind, reason string) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want *domain.Error kind=%s reason=%s, got nil", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *domain.Error, got %T (%v)", err, err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("error kind/reason = %s/%s, want %s/%s (%v)",
			domainErr.Kind, domainErr.Reason, kind, reason, err)
	}
	return domainErr
}

func TestCreateVersionValidatesBindings(t *testing.T) {
	t.Run("unknown system is a 422 reason", func(t *testing.T) {
		f := newVersionFixture()
		f.catalog.systemErrs[7] = &application.ResourceError{Reason: "system_not_found", Message: "no such system"}

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", SystemID: ptr(int64(7)),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "system_not_found")
		if len(f.repo.versions) != 0 || len(f.events.events) != 0 {
			t.Fatalf("failed create wrote rows=%d events=%d", len(f.repo.versions), len(f.events.events))
		}
	})

	t.Run("disabled system is a 422 reason", func(t *testing.T) {
		f := newVersionFixture()
		disabled := f.catalog.systems[7]
		disabled.Status = "disabled"
		f.catalog.systems[7] = disabled

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", SystemID: ptr(int64(7)),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "system_disabled")
	})

	t.Run("environment must belong to the bound system", func(t *testing.T) {
		f := newVersionFixture()
		mismatched := f.catalog.environments[8]
		mismatched.SystemID = ptr(int64(99))
		f.catalog.environments[8] = mismatched

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", SystemID: ptr(int64(7)), EnvironmentID: ptr(int64(8)),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "environment_system_mismatch")
	})

	t.Run("environment without reported ownership is a 422 reason", func(t *testing.T) {
		f := newVersionFixture()
		unowned := f.catalog.environments[8]
		unowned.SystemID = nil
		f.catalog.environments[8] = unowned

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", SystemID: ptr(int64(7)), EnvironmentID: ptr(int64(8)),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "environment_system_mismatch")
		if len(f.repo.versions) != 0 {
			t.Fatalf("fail-open ownership check created a version: %+v", f.repo.versions)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("failed create published events: %+v", f.events.events)
		}
	})

	t.Run("environment requires a system", func(t *testing.T) {
		f := newVersionFixture()

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", EnvironmentID: ptr(int64(8)),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "environment_system_mismatch")
		if len(f.catalog.lookups) != 0 {
			t.Fatalf("invalid binding triggered resource lookups: %v", f.catalog.lookups)
		}
	})

	t.Run("disabled repository is a 422 reason", func(t *testing.T) {
		f := newVersionFixture()
		disabled := f.catalog.repos[9]
		disabled.Status = "disabled"
		f.catalog.repos[9] = disabled

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", CodeRepositoryID: ptr(int64(9)),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "code_repository_disabled")
	})

	t.Run("git resolution failure is a 422 reason", func(t *testing.T) {
		f := newVersionFixture()
		f.git.err = &application.GitError{Reason: "branch_not_found", Message: "branch does not exist"}

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", CodeRepositoryID: ptr(int64(9)),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "branch_not_found")
		if len(f.git.calls) != 1 {
			t.Fatalf("git calls = %v, want exactly one fixed-branch resolution", f.git.calls)
		}
		if f.git.calls[0] != "/srv/order-web@main" {
			t.Fatalf("git call = %q, want fixed branch resolution", f.git.calls[0])
		}
		if len(f.repo.versions) != 0 {
			t.Fatal("failed binding create wrote a row")
		}
	})

	t.Run("branch and commit require a repository", func(t *testing.T) {
		f := newVersionFixture()

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", CodeBranch: ptr("main"), CodeBaselineCommit: ptr(testCommit),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "invalid_bindings")
	})

	t.Run("valid bindings snapshot the resolved commit", func(t *testing.T) {
		f := newVersionFixture()

		created, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", Label: "v2.0", Description: "second major release",
			SystemID: ptr(int64(7)), EnvironmentID: ptr(int64(8)), CodeRepositoryID: ptr(int64(9)),
		}, 42)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if created.ID != 1000 {
			t.Fatalf("id = %d, want generated id 1000", created.ID)
		}
		if created.Status != domain.VersionDraft || created.Revision != 1 {
			t.Fatalf("status/revision = %s/%d, want draft/1", created.Status, created.Revision)
		}
		if created.Name != "Order v2" || created.Label != "v2.0" || created.Description != "second major release" {
			t.Fatalf("metadata = %+v", created)
		}
		if created.SystemID == nil || *created.SystemID != 7 ||
			created.EnvironmentID == nil || *created.EnvironmentID != 8 ||
			created.CodeRepositoryID == nil || *created.CodeRepositoryID != 9 {
			t.Fatalf("bindings = %+v", created)
		}
		if created.CodeBranch == nil || *created.CodeBranch != "main" {
			t.Fatalf("code_branch = %v, want fixed branch main", created.CodeBranch)
		}
		if created.CodeBaselineCommit == nil || *created.CodeBaselineCommit != testCommit {
			t.Fatalf("code_baseline_commit = %v, want resolved %s", created.CodeBaselineCommit, testCommit)
		}
		if created.CreatedBy != 42 || created.UpdatedBy != 42 {
			t.Fatalf("created_by/updated_by = %d/%d, want 42/42", created.CreatedBy, created.UpdatedBy)
		}
		if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
			t.Fatalf("timestamps not populated: %+v", created)
		}
		if err := created.Validate(); err != nil {
			t.Fatalf("created version fails domain validation: %v", err)
		}
		if len(f.repo.versions) != 1 {
			t.Fatalf("stored versions = %d, want 1", len(f.repo.versions))
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		event := f.events.events[0]
		if event.Type != "version.created" || event.VersionID != 1000 || event.ActorID != 42 || event.Source != domain.SourceUser {
			t.Fatalf("event = %+v", event)
		}
	})

	t.Run("explicit branch overrides the fixed branch", func(t *testing.T) {
		f := newVersionFixture()

		created, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", CodeRepositoryID: ptr(int64(9)), CodeBranch: ptr("release/2.0"),
		}, 42)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if len(f.git.calls) != 1 || f.git.calls[0] != "/srv/order-web@release/2.0" {
			t.Fatalf("git calls = %v, want release/2.0 resolution", f.git.calls)
		}
		if created.CodeBranch == nil || *created.CodeBranch != "release/2.0" {
			t.Fatalf("code_branch = %v, want release/2.0", created.CodeBranch)
		}
	})

	t.Run("explicit baseline commit is kept after branch validation", func(t *testing.T) {
		f := newVersionFixture()
		f.git.commit = strings.Repeat("f", 40)

		created, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", CodeRepositoryID: ptr(int64(9)), CodeBaselineCommit: ptr(testCommit),
		}, 42)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if len(f.git.calls) != 1 || f.git.calls[0] != "/srv/order-web@main" {
			t.Fatalf("git calls = %v, want the branch to be validated", f.git.calls)
		}
		if created.CodeBaselineCommit == nil || *created.CodeBaselineCommit != testCommit {
			t.Fatalf("code_baseline_commit = %v, want the explicit %s", created.CodeBaselineCommit, testCommit)
		}
	})

	t.Run("explicit baseline commit still requires a resolvable branch", func(t *testing.T) {
		f := newVersionFixture()
		f.git.err = &application.GitError{Reason: "path_not_found", Message: "repository path is gone"}

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{
			Name: "Order v2", CodeRepositoryID: ptr(int64(9)), CodeBaselineCommit: ptr(testCommit),
		}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "path_not_found")
	})

	t.Run("invalid name is a 422 reason", func(t *testing.T) {
		f := newVersionFixture()

		_, err := f.service.Create(context.Background(), application.CreateVersionCommand{Name: "  "}, 42)

		assertDomainReason(t, err, domain.KindInvalid, "invalid_name")
	})
}

func TestEventPublishFailureIsNonFatal(t *testing.T) {
	f := newVersionFixture()
	f.events.publishErr = errors.New("event hub unavailable")

	created, err := f.service.Create(context.Background(), application.CreateVersionCommand{Name: "Order v2"}, 42)
	if err != nil {
		t.Fatalf("Create must succeed when event publication fails (spec §6.10): %v", err)
	}
	if created == nil || created.ID != 1000 {
		t.Fatalf("created = %+v", created)
	}
	if len(f.repo.versions) != 1 {
		t.Fatalf("stored versions = %d, want 1", len(f.repo.versions))
	}
	if len(f.events.events) != 1 {
		t.Fatalf("publish attempts = %d, want 1", len(f.events.events))
	}
}

func TestUpdateMetadataRevisionConflict(t *testing.T) {
	t.Run("stale revision is a 409 with no write", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Old name", Revision: 2, CreatedBy: 1, UpdatedBy: 1})

		_, err := f.service.UpdateMetadata(context.Background(), stored.ID, 1, "New name", "v2", "desc", 42)

		assertDomainReason(t, err, domain.KindStateConflict, "revision_conflict")
		if got := f.repo.versions[stored.ID]; got.Name != "Old name" || got.Revision != 2 {
			t.Fatalf("stale update was applied: %+v", got)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("failed update published events: %+v", f.events.events)
		}
	})

	t.Run("matching revision updates metadata without changing status", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Old name", Status: domain.VersionDesigning, Revision: 2, CreatedBy: 1, UpdatedBy: 1})

		updated, err := f.service.UpdateMetadata(context.Background(), stored.ID, 2, "New name", "v2", "desc", 42)
		if err != nil {
			t.Fatalf("UpdateMetadata: %v", err)
		}

		if updated.Name != "New name" || updated.Label != "v2" || updated.Description != "desc" {
			t.Fatalf("metadata = %+v", updated)
		}
		if updated.Status != domain.VersionDesigning || updated.Revision != 3 || updated.UpdatedBy != 42 {
			t.Fatalf("status/revision/updatedBy = %s/%d/%d", updated.Status, updated.Revision, updated.UpdatedBy)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		event := f.events.events[0]
		if event.Type != "version.updated" || event.VersionID != stored.ID {
			t.Fatalf("event = %+v", event)
		}
		changed, ok := event.Detail["changed"].([]string)
		if !ok || len(changed) != 3 {
			t.Fatalf("event changed = %#v, want name/label/description", event.Detail["changed"])
		}
	})

	t.Run("invalid metadata is a 422 with no write", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Old name", CreatedBy: 1, UpdatedBy: 1})

		_, err := f.service.UpdateMetadata(context.Background(), stored.ID, 1, "  ", "", "", 42)

		assertDomainReason(t, err, domain.KindInvalid, "invalid_name")
		if got := f.repo.versions[stored.ID]; got.Name != "Old name" || got.Revision != 1 {
			t.Fatalf("invalid update was applied: %+v", got)
		}
	})

	t.Run("missing version is 404", func(t *testing.T) {
		f := newVersionFixture()

		_, err := f.service.UpdateMetadata(context.Background(), 999, 1, "Name", "", "", 42)

		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})
}

func TestUpdateBindingsBlockedInDesigning(t *testing.T) {
	validBindings := func() domain.VersionBindings {
		return domain.VersionBindings{
			SystemID:         ptr(int64(7)),
			EnvironmentID:    ptr(int64(8)),
			CodeRepositoryID: ptr(int64(9)),
		}
	}

	t.Run("designing rejects binding changes", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{
			ID: 100, Name: "Order v2", Status: domain.VersionDesigning, Revision: 1, CreatedBy: 1, UpdatedBy: 1,
		})

		_, err := f.service.UpdateBindings(context.Background(), stored.ID, 1, validBindings(), 42)

		assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
		if f.repo.binds != 0 {
			t.Fatalf("repository write happened for a blocked binding update")
		}
		if got := f.repo.versions[stored.ID]; got.Revision != 1 || got.SystemID != nil {
			t.Fatalf("blocked update changed the row: %+v", got)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("blocked update published events: %+v", f.events.events)
		}
	})

	t.Run("draft replaces the whole binding set and snapshots code", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{
			ID: 100, Name: "Order v2", Status: domain.VersionDraft, Revision: 2, CreatedBy: 1, UpdatedBy: 1,
		})

		updated, err := f.service.UpdateBindings(context.Background(), stored.ID, 2, validBindings(), 42)
		if err != nil {
			t.Fatalf("UpdateBindings: %v", err)
		}

		if updated.SystemID == nil || *updated.SystemID != 7 ||
			updated.EnvironmentID == nil || *updated.EnvironmentID != 8 ||
			updated.CodeRepositoryID == nil || *updated.CodeRepositoryID != 9 {
			t.Fatalf("bindings = %+v", updated)
		}
		if updated.CodeBranch == nil || *updated.CodeBranch != "main" ||
			updated.CodeBaselineCommit == nil || *updated.CodeBaselineCommit != testCommit {
			t.Fatalf("code snapshot = %v/%v", updated.CodeBranch, updated.CodeBaselineCommit)
		}
		if updated.Revision != 3 || updated.UpdatedBy != 42 || updated.Status != domain.VersionDraft {
			t.Fatalf("revision/updatedBy/status = %d/%d/%s", updated.Revision, updated.UpdatedBy, updated.Status)
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "version.updated" {
			t.Fatalf("events = %+v", f.events.events)
		}
	})

	t.Run("clarifying allows clearing all bindings", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{
			ID: 100, Name: "Order v2", Status: domain.VersionClarifying, Revision: 1,
			SystemID: ptr(int64(7)), CreatedBy: 1, UpdatedBy: 1,
		})

		updated, err := f.service.UpdateBindings(context.Background(), stored.ID, 1, domain.VersionBindings{}, 42)
		if err != nil {
			t.Fatalf("UpdateBindings: %v", err)
		}

		if updated.SystemID != nil || updated.EnvironmentID != nil || updated.CodeRepositoryID != nil ||
			updated.CodeBranch != nil || updated.CodeBaselineCommit != nil {
			t.Fatalf("cleared bindings = %+v", updated)
		}
		if updated.Status != domain.VersionClarifying || updated.Revision != 2 {
			t.Fatalf("status/revision = %s/%d", updated.Status, updated.Revision)
		}
	})

	t.Run("stale revision is a 409", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Revision: 2, CreatedBy: 1, UpdatedBy: 1})

		_, err := f.service.UpdateBindings(context.Background(), stored.ID, 1, validBindings(), 42)

		assertDomainReason(t, err, domain.KindStateConflict, "revision_conflict")
		if f.repo.binds != 0 {
			t.Fatal("stale binding update wrote the repository")
		}
	})

	t.Run("invalid resource binding is a 422", func(t *testing.T) {
		f := newVersionFixture()
		mismatched := f.catalog.environments[8]
		mismatched.SystemID = ptr(int64(99))
		f.catalog.environments[8] = mismatched
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Revision: 1, CreatedBy: 1, UpdatedBy: 1})

		_, err := f.service.UpdateBindings(context.Background(), stored.ID, 1, validBindings(), 42)

		assertDomainReason(t, err, domain.KindInvalid, "environment_system_mismatch")
		if f.repo.binds != 0 {
			t.Fatal("invalid binding update wrote the repository")
		}
	})
}

func TestGetAndListVersions(t *testing.T) {
	t.Run("get returns the stored version", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", CreatedBy: 1, UpdatedBy: 1})

		loaded, err := f.service.Get(context.Background(), stored.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if loaded.ID != stored.ID || loaded.Name != "Order v2" {
			t.Fatalf("version = %+v", loaded)
		}
	})

	t.Run("get missing version is 404", func(t *testing.T) {
		f := newVersionFixture()

		_, err := f.service.Get(context.Background(), 999)

		assertDomainReason(t, err, domain.KindNotFound, "not_found")
	})

	t.Run("list passes the filter to the repository", func(t *testing.T) {
		f := newVersionFixture()
		f.repo.seed(domain.Version{ID: 100, Name: "Draft", CreatedBy: 1, UpdatedBy: 1})
		f.repo.seed(domain.Version{ID: 101, Name: "Clarifying", Status: domain.VersionClarifying, CreatedBy: 1, UpdatedBy: 1})

		versions, more, err := f.service.List(context.Background(), domain.VersionFilter{
			Status: domain.VersionClarifying,
			Page:   domain.Page{Limit: 10, Cursor: "opaque"},
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(versions) != 1 || versions[0].ID != 101 || more {
			t.Fatalf("versions = %+v more=%v", versions, more)
		}
		if f.repo.lastFilter.Status != domain.VersionClarifying ||
			f.repo.lastFilter.Page.Limit != 10 || f.repo.lastFilter.Page.Cursor != "opaque" {
			t.Fatalf("filter passed to repository = %+v", f.repo.lastFilter)
		}
	})
}

func TestStartClarificationRequiresRequirement(t *testing.T) {
	t.Run("no requirements is a 422 reason", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", CreatedBy: 1, UpdatedBy: 1})

		_, err := f.service.StartClarification(context.Background(), stored.ID, 42)

		assertDomainReason(t, err, domain.KindInvalid, "requirements_required")
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionDraft || got.Revision != 1 {
			t.Fatalf("status/revision = %s/%d, want draft/1", got.Status, got.Revision)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("failed action published events: %+v", f.events.events)
		}
	})

	t.Run("draft with a requirement becomes clarifying", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", CreatedBy: 1, UpdatedBy: 1})
		f.repo.counts[stored.ID] = domain.VersionCounts{RequirementCount: 1}

		updated, err := f.service.StartClarification(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("StartClarification: %v", err)
		}

		if updated.Status != domain.VersionClarifying || updated.Revision != 2 {
			t.Fatalf("status/revision = %s/%d, want clarifying/2", updated.Status, updated.Revision)
		}
		if updated.UpdatedBy != 42 {
			t.Fatalf("updated_by = %d, want 42", updated.UpdatedBy)
		}
		if len(f.events.events) != 1 {
			t.Fatalf("events = %d, want 1", len(f.events.events))
		}
		event := f.events.events[0]
		if event.Type != "version.status_changed" || event.VersionID != stored.ID || event.ActorID != 42 {
			t.Fatalf("event = %+v", event)
		}
		if event.Detail["from"] != domain.VersionDraft || event.Detail["to"] != domain.VersionClarifying {
			t.Fatalf("event detail = %+v", event.Detail)
		}
	})
}

func TestStartClarificationOnlyFromDraft(t *testing.T) {
	for _, status := range []string{
		domain.VersionClarifying, domain.VersionDesigning, domain.VersionReviewing,
	} {
		t.Run(status, func(t *testing.T) {
			f := newVersionFixture()
			stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: status, CreatedBy: 1, UpdatedBy: 1})
			f.repo.counts[stored.ID] = domain.VersionCounts{RequirementCount: 1}

			_, err := f.service.StartClarification(context.Background(), stored.ID, 42)

			assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
			if got := f.repo.versions[stored.ID]; got.Status != status || got.Revision != 1 {
				t.Fatalf("status/revision = %s/%d, want %s/1", got.Status, got.Revision, status)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("failed action published events: %+v", f.events.events)
			}
		})
	}
}

func TestEnterDesignWarningsRequireAck(t *testing.T) {
	warnings := domain.Warnings{UnclarifiedRequirementIDs: []int64{11, 12}, OpenQuestionCount: 2}

	t.Run("unacknowledged warnings return a 409 with details", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: domain.VersionClarifying, CreatedBy: 1, UpdatedBy: 1})
		f.repo.warnings[stored.ID] = warnings

		_, gotWarnings, err := f.service.EnterDesign(context.Background(), stored.ID, false, 42)

		assertDomainReason(t, err, domain.KindStateConflict, "unclarified_requirements")
		if len(gotWarnings.UnclarifiedRequirementIDs) != 2 || gotWarnings.OpenQuestionCount != 2 {
			t.Fatalf("warnings = %+v, want the gate warnings", gotWarnings)
		}
		var gateErr *application.UnclarifiedRequirementsError
		if !errors.As(err, &gateErr) {
			t.Fatalf("error %T does not expose warnings to the HTTP layer", err)
		}
		if len(gateErr.Warnings.UnclarifiedRequirementIDs) != 2 {
			t.Fatalf("gate warnings = %+v", gateErr.Warnings)
		}
		details := gateErr.Details()
		if ids, ok := details["unclarifiedRequirementIds"].([]string); !ok || len(ids) != 2 || ids[0] != "11" || ids[1] != "12" {
			t.Fatalf("details ids = %#v", details["unclarifiedRequirementIds"])
		}
		if count, ok := details["openQuestionCount"].(int); !ok || count != 2 {
			t.Fatalf("details openQuestionCount = %#v", details["openQuestionCount"])
		}
		if got := f.repo.versions[stored.ID]; got.Status != domain.VersionClarifying || got.Revision != 1 {
			t.Fatalf("status/revision = %s/%d, want clarifying/1", got.Status, got.Revision)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("failed action published events: %+v", f.events.events)
		}
	})

	t.Run("acknowledged warnings still return designing plus warnings", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: domain.VersionClarifying, CreatedBy: 1, UpdatedBy: 1})
		f.repo.warnings[stored.ID] = warnings

		updated, gotWarnings, err := f.service.EnterDesign(context.Background(), stored.ID, true, 42)
		if err != nil {
			t.Fatalf("EnterDesign: %v", err)
		}

		if updated.Status != domain.VersionDesigning || updated.Revision != 2 {
			t.Fatalf("status/revision = %s/%d, want designing/2", updated.Status, updated.Revision)
		}
		if len(gotWarnings.UnclarifiedRequirementIDs) != 2 || gotWarnings.OpenQuestionCount != 2 {
			t.Fatalf("warnings = %+v, want the gate warnings", gotWarnings)
		}
		if len(f.events.events) != 1 || f.events.events[0].Type != "version.status_changed" {
			t.Fatalf("events = %+v", f.events.events)
		}
		if f.events.events[0].Detail["from"] != domain.VersionClarifying ||
			f.events.events[0].Detail["to"] != domain.VersionDesigning {
			t.Fatalf("event detail = %+v", f.events.events[0].Detail)
		}
	})

	t.Run("no warnings enters designing without acknowledgement", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: domain.VersionClarifying, CreatedBy: 1, UpdatedBy: 1})

		updated, gotWarnings, err := f.service.EnterDesign(context.Background(), stored.ID, false, 42)
		if err != nil {
			t.Fatalf("EnterDesign: %v", err)
		}

		if updated.Status != domain.VersionDesigning {
			t.Fatalf("status = %s, want designing", updated.Status)
		}
		if len(gotWarnings.UnclarifiedRequirementIDs) != 0 || gotWarnings.OpenQuestionCount != 0 {
			t.Fatalf("warnings = %+v, want none", gotWarnings)
		}
	})

	t.Run("only clarifying can enter designing", func(t *testing.T) {
		for _, status := range []string{domain.VersionDraft, domain.VersionDesigning} {
			t.Run(status, func(t *testing.T) {
				f := newVersionFixture()
				stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: status, CreatedBy: 1, UpdatedBy: 1})

				_, _, err := f.service.EnterDesign(context.Background(), stored.ID, true, 42)

				assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
				if got := f.repo.versions[stored.ID]; got.Status != status {
					t.Fatalf("status = %s, want %s", got.Status, status)
				}
			})
		}
	})
}

// reviewAction is one P5 version review lifecycle action under test.
type reviewAction struct {
	name string
	call func(*application.VersionService, int64) (*domain.Version, error)
}

// reviewActions returns the three review lifecycle actions bound to actor 42.
func reviewActions() []reviewAction {
	return []reviewAction{
		{name: "EnterReview", call: func(s *application.VersionService, id int64) (*domain.Version, error) {
			return s.EnterReview(context.Background(), id, 42)
		}},
		{name: "RejectReview", call: func(s *application.VersionService, id int64) (*domain.Version, error) {
			return s.RejectReview(context.Background(), id, 42)
		}},
		{name: "ApproveBaseline", call: func(s *application.VersionService, id int64) (*domain.Version, error) {
			return s.ApproveBaseline(context.Background(), id, 42)
		}},
	}
}

// assertStatusChangedEvent asserts one version.status_changed event carrying the
// expected from/to detail.
func assertStatusChangedEvent(t *testing.T, events *fakeEvents, versionID int64, from, to string) {
	t.Helper()
	if len(events.events) != 1 {
		t.Fatalf("events = %d, want 1 (%+v)", len(events.events), events.events)
	}
	event := events.events[0]
	if event.Type != "version.status_changed" || event.VersionID != versionID ||
		event.ActorID != 42 || event.Source != domain.SourceUser {
		t.Fatalf("event = %+v", event)
	}
	if event.Detail["from"] != from || event.Detail["to"] != to {
		t.Fatalf("event detail = %+v, want %s -> %s", event.Detail, from, to)
	}
}

func TestEnterReviewMovesDesigningToReviewing(t *testing.T) {
	f := newVersionFixture()
	stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: domain.VersionDesigning, CreatedBy: 1, UpdatedBy: 1})

	updated, err := f.service.EnterReview(context.Background(), stored.ID, 42)
	if err != nil {
		t.Fatalf("EnterReview: %v", err)
	}

	if updated.Status != domain.VersionReviewing || updated.Revision != 2 || updated.UpdatedBy != 42 {
		t.Fatalf("status/revision/updatedBy = %s/%d/%d, want reviewing/2/42",
			updated.Status, updated.Revision, updated.UpdatedBy)
	}
	if got := f.repo.versions[stored.ID]; got.Status != domain.VersionReviewing {
		t.Fatalf("stored status = %s, want reviewing", got.Status)
	}
	assertStatusChangedEvent(t, f.events, stored.ID, domain.VersionDesigning, domain.VersionReviewing)
}

func TestEnterReviewIsIdempotent(t *testing.T) {
	t.Run("already reviewing returns the current version without a duplicate event", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{
			ID: 100, Name: "Order v2", Status: domain.VersionReviewing, Revision: 3, CreatedBy: 1, UpdatedBy: 2,
		})

		updated, err := f.service.EnterReview(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("EnterReview must be idempotent when already reviewing (spec §6.3): %v", err)
		}

		if updated.ID != stored.ID || updated.Status != domain.VersionReviewing || updated.Revision != 3 {
			t.Fatalf("version = %+v, want the unchanged reviewing version", updated)
		}
		if f.repo.statuses != 0 {
			t.Fatalf("idempotent EnterReview wrote the repository %d times", f.repo.statuses)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("idempotent EnterReview published a duplicate event: %+v", f.events.events)
		}
	})

	t.Run("losing the CAS to a concurrent reviewer is also idempotent", func(t *testing.T) {
		f := newVersionFixture()
		stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: domain.VersionDesigning, CreatedBy: 1, UpdatedBy: 1})
		// Another writer commits reviewing between the service's read and its CAS.
		f.repo.beforeStatus = func(id int64) {
			row := f.repo.versions[id]
			row.Status = domain.VersionReviewing
			row.Revision++
			f.repo.versions[id] = row
		}

		updated, err := f.service.EnterReview(context.Background(), stored.ID, 42)
		if err != nil {
			t.Fatalf("EnterReview must re-read and succeed after a lost CAS when reviewing: %v", err)
		}

		if updated.Status != domain.VersionReviewing {
			t.Fatalf("status = %s, want the concurrently committed reviewing", updated.Status)
		}
		if f.repo.statuses != 0 {
			t.Fatalf("losing CAS still wrote %d status transitions", f.repo.statuses)
		}
		if len(f.events.events) != 0 {
			t.Fatalf("losing CAS published a transition event: %+v", f.events.events)
		}
	})
}

func TestRejectReviewReturnsVersionToDesigning(t *testing.T) {
	f := newVersionFixture()
	stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: domain.VersionReviewing, CreatedBy: 1, UpdatedBy: 1})

	updated, err := f.service.RejectReview(context.Background(), stored.ID, 42)
	if err != nil {
		t.Fatalf("RejectReview: %v", err)
	}

	if updated.Status != domain.VersionDesigning || updated.Revision != 2 || updated.UpdatedBy != 42 {
		t.Fatalf("status/revision/updatedBy = %s/%d/%d, want designing/2/42",
			updated.Status, updated.Revision, updated.UpdatedBy)
	}
	if got := f.repo.versions[stored.ID]; got.Status != domain.VersionDesigning {
		t.Fatalf("stored status = %s, want designing", got.Status)
	}
	assertStatusChangedEvent(t, f.events, stored.ID, domain.VersionReviewing, domain.VersionDesigning)
}

func TestApproveBaselineStartsExecuting(t *testing.T) {
	f := newVersionFixture()
	stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: domain.VersionReviewing, CreatedBy: 1, UpdatedBy: 1})

	updated, err := f.service.ApproveBaseline(context.Background(), stored.ID, 42)
	if err != nil {
		t.Fatalf("ApproveBaseline: %v", err)
	}

	if updated.Status != domain.VersionExecuting || updated.Revision != 2 || updated.UpdatedBy != 42 {
		t.Fatalf("status/revision/updatedBy = %s/%d/%d, want executing/2/42",
			updated.Status, updated.Revision, updated.UpdatedBy)
	}
	if got := f.repo.versions[stored.ID]; got.Status != domain.VersionExecuting {
		t.Fatalf("stored status = %s, want executing", got.Status)
	}
	assertStatusChangedEvent(t, f.events, stored.ID, domain.VersionReviewing, domain.VersionExecuting)
}

func TestReviewActionsRejectInvalidFromState(t *testing.T) {
	cases := []struct {
		action string
		status string
	}{
		{"EnterReview", domain.VersionDraft},
		{"EnterReview", domain.VersionClarifying},
		{"EnterReview", domain.VersionExecuting},
		{"RejectReview", domain.VersionDesigning},
		{"RejectReview", domain.VersionExecuting},
		{"ApproveBaseline", domain.VersionDesigning},
		{"ApproveBaseline", domain.VersionClarifying},
	}
	actions := map[string]reviewAction{}
	for _, action := range reviewActions() {
		actions[action.name] = action
	}

	for _, tc := range cases {
		t.Run(tc.action+" from "+tc.status, func(t *testing.T) {
			f := newVersionFixture()
			stored := f.repo.seed(domain.Version{ID: 100, Name: "Order v2", Status: tc.status, CreatedBy: 1, UpdatedBy: 1})

			_, err := actions[tc.action].call(f.service, stored.ID)

			assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")
			if got := f.repo.versions[stored.ID]; got.Status != tc.status || got.Revision != 1 {
				t.Fatalf("status/revision = %s/%d, want %s/1", got.Status, got.Revision, tc.status)
			}
			if f.repo.statuses != 0 {
				t.Fatalf("invalid transition wrote the repository %d times", f.repo.statuses)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("invalid transition published events: %+v", f.events.events)
			}
		})
	}
}

func TestReviewActionsRequireExistingVersion(t *testing.T) {
	for _, action := range reviewActions() {
		t.Run(action.name, func(t *testing.T) {
			f := newVersionFixture()

			_, err := action.call(f.service, 999)

			assertDomainReason(t, err, domain.KindNotFound, "not_found")
			if len(f.events.events) != 0 {
				t.Fatalf("missing version published events: %+v", f.events.events)
			}
		})
	}
}
