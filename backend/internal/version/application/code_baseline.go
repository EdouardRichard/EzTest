package versionapp

import (
	"context"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// CodeBaselineResolver resolves the current HEAD commit of a bound code
// repository (P5 §5.5). It is implemented by the codeintel context's read-only
// Git layer and keeps the version context free of Git/exec imports.
type CodeBaselineResolver interface {
	ResolveHeadCommit(ctx context.Context, repo resourceapp.CodeRepositoryView) (string, error)
}

// codeBaselineAuditWarning is the audit action recorded when a binding with a
// repository cannot be resolved. Resolution failures never block version
// creation (spec §5.5).
const codeBaselineAuditWarning = "version.code_baseline_unresolved"

// codeBaselineWarning is one binding whose baseline could not be resolved at
// creation time.
type codeBaselineWarning struct {
	versionSUTID int64
	repoID       int64
}

// resolveCodeBaselines fills Binding.CodeBaselineCommit for every binding with
// a code repository. A resolver error or a malformed SHA leaves the column
// empty and is returned as a warning so Create can record the audit entry after
// the version is persisted.
func (s *Service) resolveCodeBaselines(ctx context.Context, bindings []version.Binding) []codeBaselineWarning {
	if s.resolver == nil {
		return nil
	}
	var warnings []codeBaselineWarning
	for index := range bindings {
		repoID := bindings[index].CodeRepositoryID
		if repoID == nil || *repoID <= 0 {
			continue
		}
		repository, err := s.catalog.GetRepository(ctx, *repoID)
		if err != nil {
			warnings = append(warnings, codeBaselineWarning{versionSUTID: bindings[index].ID, repoID: *repoID})
			continue
		}
		commit, err := s.resolver.ResolveHeadCommit(ctx, repository)
		if err != nil || !version.IsValidCodeBaselineCommit(commit) {
			warnings = append(warnings, codeBaselineWarning{versionSUTID: bindings[index].ID, repoID: *repoID})
			continue
		}
		bindings[index].CodeBaselineCommit = commit
	}
	return warnings
}

// recordCodeBaselineWarnings records one audit entry per unresolved binding.
// The metadata deliberately carries only IDs: a resolver error may contain a
// filesystem path, and audit entries must not leak repository paths.
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
				"version_sut_id": platformid.Format(warning.versionSUTID),
				"repo_id":        platformid.Format(warning.repoID),
			},
		})
	}
}
