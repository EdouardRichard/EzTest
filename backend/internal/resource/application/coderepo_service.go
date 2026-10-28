package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// defaultCodeRepoBranch mirrors the code_repository DDL default: a create
// request that omits fixedBranch gets main.
const defaultCodeRepoBranch = "main"

// CodeRepoService implements the local code repository use cases (spec 搂6.7).
// A repository is validated read-only through Git before it is stored, and the
// last_commit/last_validated_at audit fields are written by the same create or
// update call, so a failed validation never persists a row.
type CodeRepoService struct {
	repos domain.CodeRepoRepository
	git   GitValidator
	clock Clock
}

// CreateCodeRepoInput is the create-code-repository request.
type CreateCodeRepoInput struct {
	Key          string
	Name         string
	LocalPath    string
	FixedBranch  string
	IncludeGlobs []string
	ExcludeGlobs []string
}

// UpdateCodeRepoInput is the partial update-code-repository request. A nil
// field is left unchanged; key and company are immutable after creation.
type UpdateCodeRepoInput struct {
	Name         *string
	LocalPath    *string
	FixedBranch  *string
	IncludeGlobs *[]string
	ExcludeGlobs *[]string
	Status       *string
}

// NewCodeRepoService wires the code repository use cases to their repository,
// the read-only Git validator and the injectable clock.
func NewCodeRepoService(repos domain.CodeRepoRepository, git GitValidator, clock Clock) *CodeRepoService {
	return &CodeRepoService{repos: repos, git: git, clock: clock}
}

// validateCodeRepoStatus enforces the code_repository.status CHECK constraint.
func validateCodeRepoStatus(status string) error {
	if status != domain.StatusActive && status != domain.StatusDisabled {
		return domain.NewInvalid("status", "invalid_status", "status must be active or disabled")
	}
	return nil
}

// cloneCodeRepoStrings copies glob arrays so a stored row never aliases the
// caller's request slice.
func cloneCodeRepoStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

// gitValidationError keeps a client-facing validation verdict as-is (the HTTP
// layer maps it to 422) and wraps an operational failure (git missing, timeout,
// canceled context) so it stays an internal error instead of a 422.
func gitValidationError(err error) error {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		return err
	}
	return fmt.Errorf("application: validate code repository: %w", err)
}

// validateCheckout runs the read-only Git check against the checkout described
// by codeRepo. On success it refreshes last_commit/last_validated_at and clears
// last_validation_error; on failure codeRepo is left untouched.
func (s *CodeRepoService) validateCheckout(ctx context.Context, codeRepo *domain.CodeRepository) error {
	validation, err := s.git.Validate(ctx, codeRepo.LocalPath, codeRepo.FixedBranch)
	if err != nil {
		return gitValidationError(err)
	}
	now := s.clock.Now()
	codeRepo.LastCommit = validation.Commit
	codeRepo.LastValidatedAt = &now
	codeRepo.LastValidationError = ""
	return nil
}

// Get returns one code repository by id.
func (s *CodeRepoService) Get(ctx context.Context, id int64) (*domain.CodeRepository, error) {
	return s.repos.GetCodeRepo(ctx, id)
}

// List returns one page of code repositories and whether more rows exist.
func (s *CodeRepoService) List(ctx context.Context, f domain.CodeRepoFilter) ([]domain.CodeRepository, bool, error) {
	return s.repos.ListCodeRepos(ctx, f)
}

// Create validates the checkout and stores it in one write. Validation runs
// before the insert, so a bad path or branch leaves no row behind.
func (s *CodeRepoService) Create(ctx context.Context, in CreateCodeRepoInput) (*domain.CodeRepository, error) {
	if err := domain.ValidateResourceKey(in.Key); err != nil {
		return nil, err
	}
	if err := domain.ValidateGlobs(in.IncludeGlobs); err != nil {
		return nil, err
	}
	if err := domain.ValidateGlobs(in.ExcludeGlobs); err != nil {
		return nil, err
	}
	branch := in.FixedBranch
	if branch == "" {
		branch = defaultCodeRepoBranch
	}
	if err := domain.ValidateBranch(branch); err != nil {
		return nil, err
	}

	codeRepo := &domain.CodeRepository{
		Key:          in.Key,
		Name:         in.Name,
		LocalPath:    strings.TrimSpace(in.LocalPath),
		FixedBranch:  branch,
		IncludeGlobs: cloneCodeRepoStrings(in.IncludeGlobs),
		ExcludeGlobs: cloneCodeRepoStrings(in.ExcludeGlobs),
		Status:       domain.StatusActive,
	}
	if err := s.validateCheckout(ctx, codeRepo); err != nil {
		return nil, err
	}
	if err := s.repos.CreateCodeRepo(ctx, codeRepo); err != nil {
		return nil, err
	}
	return codeRepo, nil
}

// Update applies the provided fields to an existing code repository. Key and
// company are immutable. When the checkout location or branch changes, the new
// checkout is validated before anything is written: a failure returns the 422
// verdict and leaves the stored row untouched, a success refreshes the audit
// fields in the same update.
func (s *CodeRepoService) Update(ctx context.Context, id int64, in UpdateCodeRepoInput) (*domain.CodeRepository, error) {
	codeRepo, err := s.repos.GetCodeRepo(ctx, id)
	if err != nil {
		return nil, err
	}

	if in.Status != nil {
		if err := validateCodeRepoStatus(*in.Status); err != nil {
			return nil, err
		}
	}
	if in.IncludeGlobs != nil {
		if err := domain.ValidateGlobs(*in.IncludeGlobs); err != nil {
			return nil, err
		}
	}
	if in.ExcludeGlobs != nil {
		if err := domain.ValidateGlobs(*in.ExcludeGlobs); err != nil {
			return nil, err
		}
	}
	if in.FixedBranch != nil {
		if err := domain.ValidateBranch(*in.FixedBranch); err != nil {
			return nil, err
		}
	}

	if in.Name != nil {
		codeRepo.Name = *in.Name
	}
	if in.LocalPath != nil {
		codeRepo.LocalPath = strings.TrimSpace(*in.LocalPath)
	}
	if in.FixedBranch != nil {
		codeRepo.FixedBranch = *in.FixedBranch
	}
	if in.IncludeGlobs != nil {
		codeRepo.IncludeGlobs = cloneCodeRepoStrings(*in.IncludeGlobs)
	}
	if in.ExcludeGlobs != nil {
		codeRepo.ExcludeGlobs = cloneCodeRepoStrings(*in.ExcludeGlobs)
	}
	if in.Status != nil {
		codeRepo.Status = *in.Status
	}

	// Only a changed checkout needs a fresh Git verdict; a name or glob update
	// keeps the stored last_commit/last_validated_at untouched.
	if in.LocalPath != nil || in.FixedBranch != nil {
		if err := s.validateCheckout(ctx, codeRepo); err != nil {
			return nil, err
		}
	}

	if err := s.repos.UpdateCodeRepo(ctx, codeRepo); err != nil {
		return nil, err
	}
	return codeRepo, nil
}

// Delete removes a code repository. P2 registers no dependents (spec 搂6.7).
func (s *CodeRepoService) Delete(ctx context.Context, id int64) error {
	return s.repos.DeleteCodeRepo(ctx, id)
}

// Validate re-runs the read-only Git check against the stored checkout and
// refreshes last_commit/last_validated_at. A failed recheck returns the 422
// verdict without changing the stored values.
func (s *CodeRepoService) Validate(ctx context.Context, id int64) (*domain.CodeRepository, error) {
	codeRepo, err := s.repos.GetCodeRepo(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.validateCheckout(ctx, codeRepo); err != nil {
		return nil, err
	}
	if err := s.repos.UpdateCodeRepo(ctx, codeRepo); err != nil {
		return nil, err
	}
	return codeRepo, nil
}

// MarkIndexed records a successful code index run (P4 index-state port). It
// writes only last_indexed_at through the narrow, status-guarded repository
// method (ruling R44/INV-14), so a concurrent admin edit of the P2 configuration
// or status is never overwritten. The commit parameter names the indexed commit
// for the caller but is deliberately not stored: last_commit stays the last
// validated commit. A missing repository stays a not-found error and a disabled
// one stays the code_repository_disabled validation error; the repository port
// reports both from the guarded UPDATE, so this service never reads the whole
// row first and therefore has no read-modify-write window.
func (s *CodeRepoService) MarkIndexed(ctx context.Context, repoID int64, commit string, at time.Time) error {
	return s.repos.UpdateLastIndexedAt(ctx, repoID, at)
}
