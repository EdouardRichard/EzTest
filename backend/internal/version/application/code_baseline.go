package versionapp

import (
	"context"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// CodeBaselineResolver resolves the current HEAD commit of a code repository
// (P5 §5.5). It is implemented by the codeintel context's read-only Git layer
// and keeps the version context free of Git/exec imports.
type CodeBaselineResolver interface {
	ResolveHeadCommit(ctx context.Context, repo resourceapp.CodeRepositoryView) (string, error)
}

// codeBaselineAuditWarning is the audit action recorded when a repository of the
// environment code pool cannot be resolved. Resolution failures never block
// version creation (spec §5.5).
const codeBaselineAuditWarning = "version.code_baseline_unresolved"

// codeBaselineWarning is one (SUT, repository) pair of the environment code pool
// whose baseline could not be resolved. The pair is the key the baseline is
// stored under (sut_repository.code_baseline_commit, v0.2.4-B).
type codeBaselineWarning struct {
	sutID  int64
	repoID int64
}

// resolveEnvironmentBaselines resolves the code baseline of the environment code
// pool's deterministic baseline repository — the lowest repository ID, which is
// what the version pins and what execution fingerprints (P5 §5.5).
//
// v0.2.4-B: the baseline lives on sut_repository (one row per (SUT, repository)
// pair) instead of the dropped version_sut row. A repository that already carries
// a pinned commit is left untouched, so a version created later never moves the
// baseline an earlier version or a completed code index pinned; the remaining
// repositories of the pool are backfilled by the code index after a successful
// run. A resolver error or a malformed SHA leaves the commit empty and is
// returned as a warning so the caller can record the audit entry; it never fails
// the version write.
func (s *Service) resolveEnvironmentBaselines(ctx context.Context, environmentID int64) []codeBaselineWarning {
	if s.resolver == nil {
		return nil
	}
	repositories, err := s.environmentRepositories(ctx, environmentID)
	if err != nil {
		return nil
	}
	if len(repositories) == 0 {
		return nil
	}
	baseline := repositories[0]
	repoID, err := parseCatalogID(baseline.Repository.ID)
	if err != nil {
		return []codeBaselineWarning{{sutID: baseline.SUTID}}
	}
	if baseline.CodeBaselineCommit != "" {
		// Already pinned by an earlier version or by the code index backfill.
		return nil
	}
	commit, err := s.resolver.ResolveHeadCommit(ctx, baseline.Repository)
	if err != nil || validation.GitCommitSHA("code_baseline_commit", commit) != nil {
		return []codeBaselineWarning{{sutID: baseline.SUTID, repoID: repoID}}
	}
	if s.baselines == nil {
		return nil
	}
	if err := s.baselines.SetSUTRepositoryBaselineCommit(ctx, baseline.SUTID, repoID, commit); err != nil {
		return []codeBaselineWarning{{sutID: baseline.SUTID, repoID: repoID}}
	}
	return nil
}

// recordCodeBaselineWarnings records one audit entry per unresolved repository
// of the environment code pool. The metadata deliberately carries only IDs: a
// resolver error may contain a filesystem path, and audit entries must not leak
// repository paths.
func (s *Service) recordCodeBaselineWarnings(ctx context.Context, versionID int64, warnings []codeBaselineWarning, actorID int64) {
	if s.audit == nil {
		return
	}
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	targetID := platformid.Format(versionID)
	for _, warning := range warnings {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    actor,
			Action:     codeBaselineAuditWarning,
			TargetKind: "version",
			TargetID:   targetID,
			RequestID:  requestid.From(ctx),
			Metadata: map[string]any{
				"sut_id":  platformid.Format(warning.sutID),
				"repo_id": platformid.Format(warning.repoID),
			},
		})
	}
}
