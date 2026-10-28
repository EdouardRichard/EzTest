package application

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Anchor-service stable values.
const (
	// anchorCreatedByKindUser is the only P4 anchor origin (design spec §6.7;
	// AI anchors are a later milestone).
	anchorCreatedByKindUser = "user"
	// anchorConfidenceDefault matches the code_anchor DDL default.
	anchorConfidenceDefault = domain.ConfidenceHigh
)

// AnchorServiceDeps are the ports of the version-scoped anchor use case. The
// repository catalog resolves the checkout used to validate the anchored file at
// the fixed branch tip, and the P2/P3 catalogs validate subject ownership.
type AnchorServiceDeps struct {
	Repos     RepoCatalog
	Versions  VersionCatalog
	AppModels AppModelCatalog
	Git       GitReader
	Anchors   AnchorRepository
	IDs       IDGenerator
	Clock     Clock
}

// AnchorService implements the anchor CRUD of design spec §6.7: subject
// ownership, fixed-branch file existence, optional line-range validation, point
// uniqueness, version-scoped deletion and the P4 subject-kind restriction.
type AnchorService struct {
	deps AnchorServiceDeps
}

// NewAnchorService wires the anchor use case.
func NewAnchorService(deps AnchorServiceDeps) *AnchorService {
	return &AnchorService{deps: deps}
}

// Create validates and persists one anchor of versionID (design spec §6.7).
func (s *AnchorService) Create(ctx context.Context, versionID, actorID int64, p domain.CreateAnchorParams) (domain.CodeAnchor, error) {
	if err := domain.ValidateSubjectKind(p.SubjectKind); err != nil {
		return domain.CodeAnchor{}, err
	}
	if s.deps.Anchors == nil {
		return domain.CodeAnchor{}, errors.New("codeintel: anchor repository is not configured")
	}
	if s.deps.Versions == nil {
		return domain.CodeAnchor{}, errors.New("codeintel: version catalog is not configured")
	}
	info, err := s.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return domain.CodeAnchor{}, err
	}
	if err := s.validateAnchorSubject(ctx, versionID, info, p.SubjectKind, p.SubjectID); err != nil {
		return domain.CodeAnchor{}, err
	}

	if !info.HasRepo || info.RepoID <= 0 {
		return domain.CodeAnchor{}, domain.NewInvalid("versionId", "code_repository_not_bound",
			fmt.Sprintf("codeintel: version %d has no code repository binding", versionID))
	}
	if s.deps.Repos == nil {
		return domain.CodeAnchor{}, errors.New("codeintel: repository catalog is not configured")
	}
	repo, err := s.deps.Repos.Get(ctx, info.RepoID)
	if err != nil {
		return domain.CodeAnchor{}, err
	}
	if repo.Status != codeRepositoryActive {
		return domain.CodeAnchor{}, domain.NewInvalid("repoId", "code_repository_disabled",
			fmt.Sprintf("codeintel: code repository %d is disabled", info.RepoID))
	}

	filePath, err := normalizeAnchorFilePath(p.FilePath)
	if err != nil {
		return domain.CodeAnchor{}, err
	}
	startLine, endLine, err := validateAnchorLines(p.StartLine, p.EndLine)
	if err != nil {
		return domain.CodeAnchor{}, err
	}
	if err := s.validateAnchorFile(ctx, repo, filePath); err != nil {
		return domain.CodeAnchor{}, err
	}

	subjectID := p.SubjectID
	existing, err := s.deps.Anchors.List(ctx, versionID, domain.AnchorFilter{
		SubjectKind: p.SubjectKind,
		SubjectID:   &subjectID,
	})
	if err != nil {
		return domain.CodeAnchor{}, err
	}
	for _, anchor := range existing {
		if anchor.FilePath == filePath &&
			anchorLineValue(anchor.StartLine) == anchorLineValue(startLine) &&
			anchorLineValue(anchor.EndLine) == anchorLineValue(endLine) {
			return domain.CodeAnchor{}, domain.NewConflict("anchor_duplicate",
				"codeintel: an anchor with the same subject, file and line range already exists")
		}
	}

	id := int64(0)
	if s.deps.IDs != nil {
		id = s.deps.IDs.Next()
	}
	if id == 0 {
		return domain.CodeAnchor{}, errors.New("codeintel: id generator returned a zero anchor id")
	}
	anchor := domain.CodeAnchor{
		ID:            id,
		VersionID:     versionID,
		RepoID:        repo.ID,
		SubjectKind:   p.SubjectKind,
		SubjectID:     p.SubjectID,
		FilePath:      filePath,
		StartLine:     startLine,
		EndLine:       endLine,
		Confidence:    anchorConfidenceDefault,
		CreatedByKind: anchorCreatedByKindUser,
		CreatedBy:     actorID,
		CreatedAt:     s.now(),
	}
	if err := s.deps.Anchors.Create(ctx, anchor); err != nil {
		return domain.CodeAnchor{}, err
	}
	return anchor, nil
}

// Delete removes one anchor of versionID. An anchor outside the version (or a
// missing one) is reported as not-found by the repository (design spec §6.7).
func (s *AnchorService) Delete(ctx context.Context, versionID, anchorID int64) error {
	if s.deps.Anchors == nil {
		return errors.New("codeintel: anchor repository is not configured")
	}
	return s.deps.Anchors.Delete(ctx, versionID, anchorID)
}

// List returns the anchors of versionID filtered by subject.
func (s *AnchorService) List(ctx context.Context, versionID int64, f domain.AnchorFilter) ([]domain.CodeAnchor, error) {
	if s.deps.Anchors == nil {
		return nil, errors.New("codeintel: anchor repository is not configured")
	}
	return s.deps.Anchors.List(ctx, versionID, f)
}

// validateAnchorSubject enforces the P4 ownership rules: requirement must belong
// to the version, app_model must exist in the version's bound system, version
// must equal the path version.
func (s *AnchorService) validateAnchorSubject(ctx context.Context, versionID int64, info VersionInfo, subjectKind string, subjectID int64) error {
	switch subjectKind {
	case domain.SubjectVersion:
		if subjectID != versionID {
			return domain.NewInvalid("subjectId", "subject_not_in_version",
				fmt.Sprintf("codeintel: version anchor subject %d is not the path version %d", subjectID, versionID))
		}
		return nil
	case domain.SubjectRequirement:
		if s.deps.Versions == nil {
			return errors.New("codeintel: version catalog is not configured")
		}
		requirements, err := s.deps.Versions.ListRequirements(ctx, versionID)
		if err != nil {
			return err
		}
		for _, requirement := range requirements {
			if requirement.ID == subjectID {
				return nil
			}
		}
		return domain.NewInvalid("subjectId", "subject_not_in_version",
			fmt.Sprintf("codeintel: requirement %d does not belong to version %d", subjectID, versionID))
	case domain.SubjectAppModel:
		if !info.HasSystem || info.SystemID <= 0 {
			return domain.NewInvalid("subjectId", "app_model_not_found",
				fmt.Sprintf("codeintel: version %d has no system binding for an app model anchor", versionID))
		}
		if s.deps.AppModels == nil {
			return errors.New("codeintel: app model catalog is not configured")
		}
		nodes, err := s.deps.AppModels.ListBySystem(ctx, info.SystemID, nil)
		if err != nil {
			return err
		}
		for _, node := range nodes {
			if node.ID == subjectID && node.SystemID == info.SystemID {
				return nil
			}
		}
		return domain.NewInvalid("subjectId", "app_model_not_found",
			fmt.Sprintf("codeintel: app model %d does not belong to the version system %d", subjectID, info.SystemID))
	default:
		// domain.ValidateSubjectKind already rejected every other kind.
		return domain.NewInvalid("subjectKind", "unsupported_subject_kind",
			"codeintel: subjectKind must be one of version, requirement or app_model")
	}
}

// normalizeAnchorFilePath validates and cleans one repository-relative anchor
// path (slash-normalised, never absolute, never escaping the root).
func normalizeAnchorFilePath(raw string) (string, error) {
	normalized := strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/"))
	if normalized == "" {
		return "", domain.NewInvalid("filePath", "path_not_found", "codeintel: filePath is required")
	}
	if strings.HasPrefix(normalized, "/") || path.IsAbs(normalized) {
		return "", domain.NewInvalid("filePath", "path_escape",
			"codeintel: filePath must be repository-relative")
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", domain.NewInvalid("filePath", "path_escape",
			"codeintel: filePath escapes the repository root")
	}
	if len(cleaned) > domain.MaxPathChars {
		return "", domain.NewInvalid("filePath", "path_escape",
			fmt.Sprintf("codeintel: filePath exceeds %d characters", domain.MaxPathChars))
	}
	return cleaned, nil
}

// validateAnchorLines enforces the §6.7 line rule: both bounds are absent or
// both are present with 1 <= start <= end.
func validateAnchorLines(startLine, endLine *int) (*int, *int, error) {
	if startLine == nil && endLine == nil {
		return nil, nil, nil
	}
	if startLine == nil || endLine == nil {
		return nil, nil, domain.NewInvalid("startLine", "invalid_line_range",
			"codeintel: startLine and endLine must be provided together")
	}
	if *startLine < 1 || *endLine < *startLine {
		return nil, nil, domain.NewInvalid("startLine", "invalid_line_range",
			fmt.Sprintf("codeintel: invalid anchor line range %d..%d", *startLine, *endLine))
	}
	return startLine, endLine, nil
}

// validateAnchorFile proves the anchored path exists as an ordinary file at the
// repository's fixed branch tip (design spec §6.7). A binary/oversized file that
// Git cannot serve as text still exists and is accepted.
func (s *AnchorService) validateAnchorFile(ctx context.Context, repo RepoInfo, filePath string) error {
	if s.deps.Git == nil {
		return errors.New("codeintel: git reader is not configured")
	}
	branch := strings.TrimSpace(repo.FixedBranch)
	if branch == "" {
		// The fixed branch is part of the P2 repository contract; guessing
		// "main" would validate the anchor against the wrong tip.
		return domain.NewInvalid("repoId", "code_repository_not_bound",
			fmt.Sprintf("codeintel: code repository %d has no fixed branch", repo.ID))
	}
	commit, err := s.deps.Git.Head(ctx, repo.LocalPath, branch)
	if err != nil {
		return err
	}
	if _, err := s.deps.Git.File(ctx, repo.LocalPath, commit, filePath, 1, 1); err != nil {
		if domain.Reason(err) == "unsupported_content" {
			// The file exists but its bytes are binary or oversized: still an
			// ordinary file at the fixed branch tip.
			return nil
		}
		return err
	}
	return nil
}

// anchorLineValue flattens a nullable line for comparison.
func anchorLineValue(line *int) int {
	if line == nil {
		return 0
	}
	return *line
}

// now returns the injected clock time (wall time when a partially wired process
// leaves Clock nil).
func (s *AnchorService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now().UTC()
}
