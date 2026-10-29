package resource_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionresource "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/resource"
)

// fakeSystems is a narrow P2 catalog stand-in for systems.
type fakeSystems struct {
	systems map[int64]*domain.System
	err     error
}

// GetSystem returns the configured system.
func (f *fakeSystems) GetSystem(_ context.Context, id int64) (*domain.System, error) {
	if f.err != nil {
		return nil, f.err
	}
	system, ok := f.systems[id]
	if !ok {
		return nil, domain.NewNotFound("system", id)
	}
	return system, nil
}

// fakeEnvironments is a narrow P2 catalog stand-in for environments.
type fakeEnvironments struct {
	environments map[int64]*domain.Environment
	err          error
}

// GetEnvironment returns the configured environment.
func (f *fakeEnvironments) GetEnvironment(_ context.Context, id int64) (*domain.Environment, error) {
	if f.err != nil {
		return nil, f.err
	}
	environment, ok := f.environments[id]
	if !ok {
		return nil, domain.NewNotFound("environment", id)
	}
	return environment, nil
}

// fakeCodeRepos is a narrow P2 code-repository stand-in.
type fakeCodeRepos struct {
	repos map[int64]*domain.CodeRepository
	err   error
}

// Get returns the configured code repository.
func (f *fakeCodeRepos) Get(_ context.Context, id int64) (*domain.CodeRepository, error) {
	if f.err != nil {
		return nil, f.err
	}
	repo, ok := f.repos[id]
	if !ok {
		return nil, domain.NewNotFound("code repository", id)
	}
	return repo, nil
}

// resourceErrorReason returns the *application.ResourceError reason carried by
// err.
func resourceErrorReason(t *testing.T, err error) string {
	t.Helper()
	var resourceErr *application.ResourceError
	if !errors.As(err, &resourceErr) {
		t.Fatalf("error %v is not *application.ResourceError", err)
	}
	return resourceErr.Reason
}

// TestCatalogAdapterMapsResources pins the P2→P3 resource projection: every
// lookup fills the P3 minimal fields, and an environment always reports its
// owning system so the fail-closed binding check can verify ownership.
func TestCatalogAdapterMapsResources(t *testing.T) {
	ctx := context.Background()
	system := &domain.System{ID: 7, Name: "Order system", Status: domain.StatusActive}
	environment := &domain.Environment{ID: 8, SystemID: 7, Name: "staging"}
	repo := &domain.CodeRepository{
		ID: 9, Name: "order-web", Status: domain.StatusActive,
		LocalPath: "/srv/order-web", FixedBranch: "main",
		IncludeGlobs: []string{"src/**"}, ExcludeGlobs: []string{"**/*_test.go"},
		LastCommit: "0123456789abcdef0123456789abcdef01234567",
	}
	adapter := versionresource.NewCatalog(
		&fakeSystems{systems: map[int64]*domain.System{7: system}},
		&fakeEnvironments{environments: map[int64]*domain.Environment{8: environment}},
		&fakeCodeRepos{repos: map[int64]*domain.CodeRepository{9: repo}},
	)

	t.Run("system", func(t *testing.T) {
		ref, err := adapter.GetSystem(ctx, 7)
		if err != nil {
			t.Fatalf("GetSystem: %v", err)
		}
		if ref.ID != 7 || ref.Name != "Order system" || ref.Status != domain.StatusActive || !ref.Available {
			t.Fatalf("GetSystem=%+v", ref)
		}
		if ref.SystemID != nil {
			t.Fatalf("GetSystem SystemID=%v want nil", *ref.SystemID)
		}
	})

	t.Run("environment carries its owning system", func(t *testing.T) {
		ref, err := adapter.GetEnvironment(ctx, 8)
		if err != nil {
			t.Fatalf("GetEnvironment: %v", err)
		}
		if ref.ID != 8 || ref.Name != "staging" || ref.Status != "active" || !ref.Available {
			t.Fatalf("GetEnvironment=%+v", ref)
		}
		if ref.SystemID == nil || *ref.SystemID != 7 {
			t.Fatalf("GetEnvironment SystemID=%v want 7", ref.SystemID)
		}
	})

	t.Run("code repository", func(t *testing.T) {
		ref, err := adapter.GetCodeRepository(ctx, 9)
		if err != nil {
			t.Fatalf("GetCodeRepository: %v", err)
		}
		if ref.ID != 9 || ref.Name != "order-web" || ref.LocalPath != "/srv/order-web" ||
			ref.FixedBranch != "main" || ref.Status != domain.StatusActive || !ref.Available {
			t.Fatalf("GetCodeRepository=%+v", ref)
		}
		if len(ref.IncludeGlobs) != 1 || ref.IncludeGlobs[0] != "src/**" ||
			len(ref.ExcludeGlobs) != 1 || ref.ExcludeGlobs[0] != "**/*_test.go" {
			t.Fatalf("GetCodeRepository globs=%v/%v", ref.IncludeGlobs, ref.ExcludeGlobs)
		}
		if ref.LastCommit != "0123456789abcdef0123456789abcdef01234567" {
			t.Fatalf("GetCodeRepository LastCommit=%q", ref.LastCommit)
		}
	})

	t.Run("disabled resources carry their status", func(t *testing.T) {
		disabledRepo := *repo
		disabledRepo.Status = domain.StatusDisabled
		disabled := versionresource.NewCatalog(
			&fakeSystems{systems: map[int64]*domain.System{}},
			&fakeEnvironments{environments: map[int64]*domain.Environment{}},
			&fakeCodeRepos{repos: map[int64]*domain.CodeRepository{9: &disabledRepo}},
		)
		ref, err := disabled.GetCodeRepository(ctx, 9)
		if err != nil {
			t.Fatalf("GetCodeRepository(disabled): %v", err)
		}
		if ref.Status != domain.StatusDisabled || !ref.Available {
			t.Fatalf("GetCodeRepository(disabled)=%+v", ref)
		}
	})

	t.Run("missing resources are typed with the spec reasons", func(t *testing.T) {
		if _, err := adapter.GetSystem(ctx, 404); err == nil {
			t.Fatal("GetSystem(missing) returned nil error")
		} else if reason := resourceErrorReason(t, err); reason != "system_not_found" {
			t.Fatalf("GetSystem(missing) reason=%q", reason)
		}
		if _, err := adapter.GetEnvironment(ctx, 404); err == nil {
			t.Fatal("GetEnvironment(missing) returned nil error")
		} else if reason := resourceErrorReason(t, err); reason != "environment_not_found" {
			t.Fatalf("GetEnvironment(missing) reason=%q", reason)
		}
		if _, err := adapter.GetCodeRepository(ctx, 404); err == nil {
			t.Fatal("GetCodeRepository(missing) returned nil error")
		} else if reason := resourceErrorReason(t, err); reason != "code_repository_not_found" {
			t.Fatalf("GetCodeRepository(missing) reason=%q", reason)
		}
	})

	t.Run("operational failures stay infrastructure errors", func(t *testing.T) {
		failing := versionresource.NewCatalog(
			&fakeSystems{err: errors.New("database unavailable")},
			&fakeEnvironments{err: errors.New("database unavailable")},
			&fakeCodeRepos{err: errors.New("database unavailable")},
		)
		if _, err := failing.GetSystem(ctx, 7); err == nil {
			t.Fatal("GetSystem(operational failure) returned nil error")
		} else {
			var resourceErr *application.ResourceError
			if errors.As(err, &resourceErr) {
				t.Fatalf("GetSystem(operational failure)=%v want a plain error", err)
			}
		}
	})

	t.Run("the adapter satisfies the application port", func(t *testing.T) {
		var _ application.ResourceCatalog = adapter
		// Guard against an accidental zero environment owner: the adapter must
		// never fabricate an ownership it cannot see. A P2 environment always
		// has a positive system id, so a zero one is still reported as 0 and the
		// version service's fail-closed check rejects the mismatch.
		zeroOwner := &domain.Environment{ID: 10, SystemID: 0, Name: "orphan", CreatedAt: time.Time{}}
		zeroAdapter := versionresource.NewCatalog(nil, &fakeEnvironments{environments: map[int64]*domain.Environment{10: zeroOwner}}, nil)
		ref, err := zeroAdapter.GetEnvironment(ctx, 10)
		if err != nil {
			t.Fatalf("GetEnvironment(zero owner): %v", err)
		}
		if ref.SystemID == nil || *ref.SystemID != 0 {
			t.Fatalf("GetEnvironment(zero owner) SystemID=%v want a reported 0", ref.SystemID)
		}
	})
}
