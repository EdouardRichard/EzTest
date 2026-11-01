package codeintelapp

import (
	"context"
	"strings"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ReviewRepository stores the append-only code_fact_review rows. Create never
// updates an existing row; LatestVerdicts returns the newest verdict per fact
// key (latest review id wins) for the given commit.
type ReviewRepository interface {
	List(ctx context.Context, factID int64) ([]codeintel.FactReview, error)
	Create(ctx context.Context, review codeintel.FactReview) (codeintel.FactReview, error)
	LatestVerdicts(ctx context.Context, repoID int64, commitSHA string, factKeys []string) (map[string]string, error)
}

// ReviewDeps is the fact-review service wiring.
type ReviewDeps struct {
	Reviews  ReviewRepository
	Facts    FactRepository
	Versions VersionReader
	Impact   *ImpactService
	Events   EventPublisher
	Audit    AuditRecorder
	Clock    Clock
	IDs      IDGenerator
}

// ReviewService records append-only fact reviews (spec §6.2). The latest review
// is the effective verdict; incorrect facts are excluded from automatic impact
// candidates by the impact service.
type ReviewService struct {
	reviews  ReviewRepository
	facts    FactRepository
	versions VersionReader
	impact   *ImpactService
	events   EventPublisher
	audit    AuditRecorder
	clock    Clock
	ids      IDGenerator
}

// NewReviewService wires the review service with test-friendly defaults.
func NewReviewService(deps ReviewDeps) *ReviewService {
	return &ReviewService{
		reviews:  deps.Reviews,
		facts:    deps.Facts,
		versions: deps.Versions,
		impact:   deps.Impact,
		events:   deps.Events,
		audit:    deps.Audit,
		clock:    clockOrSystem(deps.Clock),
		ids:      idsOrFallback(deps.IDs),
	}
}

// List returns every review of one fact in insertion order.
func (s *ReviewService) List(ctx context.Context, versionID, factID int64) ([]codeintel.FactReview, error) {
	if _, err := s.factOfVersion(ctx, versionID, factID); err != nil {
		return nil, err
	}
	reviews, err := s.reviews.List(ctx, factID)
	if err != nil {
		return nil, mapDomainError(err, "code fact review")
	}
	return reviews, nil
}

// Create appends one review. The reviewer is the authenticated actor; reason is
// bounded by MaxReasonChars and never persisted as code content.
func (s *ReviewService) Create(ctx context.Context, versionID, factID, reviewerID int64, verdict codeintel.ReviewVerdict, reason string) (codeintel.FactReview, error) {
	if reviewerID <= 0 {
		return codeintel.FactReview{}, platform.Unauthenticated()
	}
	verdict = codeintel.ReviewVerdict(strings.TrimSpace(string(verdict)))
	if !verdict.Valid() {
		return codeintel.FactReview{}, unprocessable("invalid_verdict", "verdict must be correct or incorrect")
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > codeintel.MaxReasonChars {
		return codeintel.FactReview{}, unprocessable("invalid_reason", "reason exceeds the maximum length")
	}
	fact, err := s.factOfVersion(ctx, versionID, factID)
	if err != nil {
		return codeintel.FactReview{}, err
	}
	now := s.clock.Now()
	review := codeintel.FactReview{
		ID:          s.ids.Next(),
		FactID:      fact.ID,
		FactVersion: fact.Version,
		RepoID:      fact.RepoID,
		CommitSHA:   fact.CommitSHA,
		Verdict:     string(verdict),
		Reason:      reason,
		ReviewerID:  reviewerID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	created, err := s.reviews.Create(ctx, review)
	if err != nil {
		return codeintel.FactReview{}, mapDomainError(err, "code fact review")
	}
	publishEvent(ctx, s.events, "code.fact_reviewed", versionID, map[string]any{
		"fact_id":      platformid.Format(created.FactID),
		"fact_version": created.FactVersion,
		"verdict":      created.Verdict,
		"reviewer_id":  platformid.Format(created.ReviewerID),
	})
	recordAudit(ctx, s.audit, "code.fact_review", "code_fact", platformid.Format(created.FactID), map[string]any{
		"version_id":   platformid.Format(versionID),
		"fact_key":     fact.FactKey,
		"fact_version": created.FactVersion,
		"verdict":      created.Verdict,
		"reviewer_id":  platformid.Format(created.ReviewerID),
	})
	// A review changes which facts are eligible for automatic impact; recompute
	// the persisted candidate set so the latest verdict is reflected immediately
	// even when the T1 analysis itself is served from cache.
	if s.impact != nil && fact.AnalysisID != nil && *fact.AnalysisID > 0 {
		_, _ = s.impact.Compute(ctx, *fact.AnalysisID)
	}
	return created, nil
}

// factOfVersion loads a fact and verifies its repository belongs to the version.
func (s *ReviewService) factOfVersion(ctx context.Context, versionID, factID int64) (codeintel.Fact, error) {
	fact, err := s.facts.ByID(ctx, factID)
	if err != nil {
		return codeintel.Fact{}, mapDomainError(err, "code fact")
	}
	bindings, err := s.versions.VersionSUTBindings(ctx, versionID)
	if err != nil {
		return codeintel.Fact{}, err
	}
	for _, binding := range bindings {
		if binding.RepoID == fact.RepoID {
			return fact, nil
		}
	}
	return codeintel.Fact{}, unprocessable("fact_not_in_version", "fact does not belong to a repository bound to this version")
}
