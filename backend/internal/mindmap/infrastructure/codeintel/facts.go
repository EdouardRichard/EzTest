// Package codeintel adapts the P4 code intelligence application services to the
// P5 mindmap gateway ports (spec §4.3): read-only code facts/impact and the
// case code-anchor CRUD. The adapter only projects values and translates
// errors; it never reads or writes codeintel tables directly.
package codeintel

import (
	"context"
	"errors"

	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// AnalysisReader is the narrow slice of the P4 analysis service the adapter
// needs. *codeintel/application.AnalysisService satisfies it.
type AnalysisReader interface {
	GetLatest(ctx context.Context, versionID int64) (codeinteldomain.CodeAnalysis, error)
	ListCandidates(ctx context.Context, analysisID int64) ([]codeinteldomain.ImpactCandidate, error)
}

// CodeQuery is the narrow slice of the P4 query service the adapter needs.
// *codeintel/application.QueryService satisfies it.
type CodeQuery interface {
	File(ctx context.Context, actorID, repoID int64, commit, path string, startLine, endLine int) (codeinteldomain.FileWindow, error)
	Facts(ctx context.Context, repoID int64, filter codeinteldomain.EntityFilter) ([]codeinteldomain.CodeEntity, string, bool, error)
}

// VersionReader is the narrow slice of the P3 version service used to resolve
// the repository binding and code baseline commit. *version/application.VersionService
// satisfies it.
type VersionReader interface {
	Get(ctx context.Context, id int64) (*versionapp.Version, error)
}

// CodeFactsAdapter implements application.CodeFactsGateway on top of the P4
// analysis and query services.
type CodeFactsAdapter struct {
	analyses AnalysisReader
	queries  CodeQuery
	versions VersionReader
}

// NewCodeFactsAdapter wires the adapter to the P4 analysis read, the P4 query
// service and the P3 version reader.
func NewCodeFactsAdapter(analyses AnalysisReader, queries CodeQuery, versions VersionReader) *CodeFactsAdapter {
	return &CodeFactsAdapter{analyses: analyses, queries: queries, versions: versions}
}

var _ mindmapapp.CodeFactsGateway = (*CodeFactsAdapter)(nil)

// GetLatestAnalysis returns the newest analysis of the version. A version with
// no analysis yet is the documented zero snapshot with a nil error, not a
// not-found failure.
func (a *CodeFactsAdapter) GetLatestAnalysis(ctx context.Context, versionID int64) (mindmapapp.ImpactSnapshot, error) {
	if a == nil || a.analyses == nil {
		return mindmapapp.ImpactSnapshot{}, errors.New("mindmap: analysis reader is not configured")
	}
	analysis, err := a.analyses.GetLatest(ctx, versionID)
	if err != nil {
		if isCodeIntelNotFound(err) {
			return mindmapapp.ImpactSnapshot{}, nil
		}
		return mindmapapp.ImpactSnapshot{}, mapCodeIntelError(err)
	}
	if analysis.ID == 0 {
		return mindmapapp.ImpactSnapshot{}, nil
	}
	return mindmapapp.ImpactSnapshot{
		AnalysisID: analysis.ID,
		BaseCommit: analysis.BaseCommit,
		HeadCommit: analysis.HeadCommit,
		Status:     analysis.Status,
		Degraded:   analysis.Degraded,
	}, nil
}

// ListCandidates returns the impact candidates of one analysis.
func (a *CodeFactsAdapter) ListCandidates(ctx context.Context, analysisID int64) ([]mindmapapp.ImpactCandidate, error) {
	if a == nil || a.analyses == nil {
		return nil, errors.New("mindmap: analysis reader is not configured")
	}
	candidates, err := a.analyses.ListCandidates(ctx, analysisID)
	if err != nil {
		return nil, mapCodeIntelError(err)
	}
	mapped := make([]mindmapapp.ImpactCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		mapped = append(mapped, mindmapapp.ImpactCandidate{
			ID:          candidate.ID,
			SubjectKind: candidate.SubjectKind,
			SubjectID:   candidate.SubjectID,
			Reason:      candidate.Reason,
			Confidence:  candidate.Confidence,
			Source:      candidate.Source,
		})
	}
	return mapped, nil
}

// FileExists reports whether path exists in the version's code baseline commit.
// A version without a repository/baseline binding, and a file missing from that
// commit, are both false with a nil error: the check only validates a proposed
// anchor, it never fails a run. Other codeintel failures are propagated.
func (a *CodeFactsAdapter) FileExists(ctx context.Context, versionID int64, path string) (bool, error) {
	if a == nil || a.queries == nil || a.versions == nil {
		return false, errors.New("mindmap: code query is not configured")
	}
	version, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return false, mapVersionLookupError(err)
	}
	if version == nil {
		return false, mindmapdomain.NewNotFound("version", versionID)
	}
	if version.CodeRepositoryID == nil || version.CodeBaselineCommit == nil {
		return false, nil
	}
	_, err = a.queries.File(ctx, 0, *version.CodeRepositoryID, *version.CodeBaselineCommit, path, 0, 0)
	if err != nil {
		if isCodeIntelNotFound(err) || isMissingCodePath(err) {
			return false, nil
		}
		return false, mapCodeIntelError(err)
	}
	return true, nil
}

// ListFacts returns at most limit read-only code facts at the version's code
// baseline commit. A version without a repository/baseline binding has no
// facts. Key is the fact key when the entity has one, otherwise its path.
func (a *CodeFactsAdapter) ListFacts(ctx context.Context, versionID int64, limit int) ([]mindmapapp.FactSummary, error) {
	if a == nil || a.queries == nil || a.versions == nil {
		return nil, errors.New("mindmap: code query is not configured")
	}
	version, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return nil, mapVersionLookupError(err)
	}
	if version == nil {
		return nil, mindmapdomain.NewNotFound("version", versionID)
	}
	if version.CodeRepositoryID == nil || version.CodeBaselineCommit == nil {
		return []mindmapapp.FactSummary{}, nil
	}
	entities, _, _, err := a.queries.Facts(ctx, *version.CodeRepositoryID, codeinteldomain.EntityFilter{
		Commit: *version.CodeBaselineCommit,
		Limit:  limit,
	})
	if err != nil {
		return nil, mapCodeIntelError(err)
	}
	facts := make([]mindmapapp.FactSummary, 0, len(entities))
	for _, entity := range entities {
		key := entity.Path
		if entity.FactKey != nil && *entity.FactKey != "" {
			key = *entity.FactKey
		}
		facts = append(facts, mindmapapp.FactSummary{
			Kind:   entity.Kind,
			Key:    key,
			Path:   entity.Path,
			Name:   entity.Name,
			Detail: entity.Signature,
		})
	}
	return facts, nil
}

// isCodeIntelNotFound reports whether err is the codeintel not-found class.
func isCodeIntelNotFound(err error) bool {
	return errors.Is(err, codeinteldomain.ErrNotFound)
}

// isMissingCodePath reports whether a file read failed because the path (or the
// baseline commit) does not exist. The P4 git reader reports both as validation
// errors with a stable reason.
func isMissingCodePath(err error) bool {
	switch codeinteldomain.Reason(err) {
	case "path_not_found", "commit_not_found":
		return true
	default:
		return false
	}
}

// mapVersionLookupError translates a P3 version error raised by a binding
// lookup. Only not_found and invalid are expected on this read path.
func mapVersionLookupError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *versiondomain.Error
	if !errors.As(err, &domainErr) {
		return err
	}
	switch domainErr.Kind {
	case versiondomain.KindNotFound:
		return &mindmapdomain.Error{
			Kind:    mindmapdomain.KindNotFound,
			Reason:  "not_found",
			Message: domainErr.Error(),
		}
	case versiondomain.KindInvalid:
		return mindmapdomain.NewInvalid(domainErr.Field, codeIntelReasonOrDefault(domainErr.Reason, "invalid"), domainErr.Error())
	default:
		return err
	}
}

// mapCodeIntelError translates a P4 domain error into the mindmap error kinds
// and reasons (spec §7.4), preserving the stable reason and message. Operational
// failures are returned unchanged.
func mapCodeIntelError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *codeinteldomain.Error
	if errors.As(err, &domainErr) {
		switch {
		case errors.Is(domainErr.Kind, codeinteldomain.ErrNotFound):
			return &mindmapdomain.Error{
				Kind:    mindmapdomain.KindNotFound,
				Reason:  codeIntelReasonOrDefault(domainErr.Reason, "not_found"),
				Message: domainErr.Error(),
			}
		case errors.Is(domainErr.Kind, codeinteldomain.ErrConflict):
			return mindmapdomain.NewConflict(codeIntelReasonOrDefault(domainErr.Reason, "conflict"), domainErr.Error())
		default:
			return mindmapdomain.NewInvalid(domainErr.Field, codeIntelReasonOrDefault(domainErr.Reason, "invalid"), domainErr.Error())
		}
	}
	switch {
	case errors.Is(err, codeinteldomain.ErrNotFound):
		return &mindmapdomain.Error{Kind: mindmapdomain.KindNotFound, Reason: "not_found", Message: err.Error()}
	case errors.Is(err, codeinteldomain.ErrConflict):
		return mindmapdomain.NewConflict("conflict", err.Error())
	case errors.Is(err, codeinteldomain.ErrInvalid):
		return mindmapdomain.NewInvalid("", "invalid", err.Error())
	default:
		return err
	}
}

// codeIntelReasonOrDefault returns reason, or fallback when the P4 error carried
// none.
func codeIntelReasonOrDefault(reason, fallback string) string {
	if reason == "" {
		return fallback
	}
	return reason
}
