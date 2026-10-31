package designapp

import (
	"context"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// TeamPostRef is one structured reference on a team-room post.
type TeamPostRef struct {
	Kind string
	ID   string
}

// TeamPost is one design team-room post written by the design context. main
// adapts it to the agent team-room service; the design app never imports the
// agent context.
type TeamPost struct {
	VersionID  int64
	Team       string
	Kind       string
	Content    string
	Refs       []TeamPostRef
	AgentRunID *int64
	CreatedBy  *int64
}

// TeamRoom is the team-room write port consumed by the design services.
type TeamRoom interface {
	Post(ctx context.Context, post TeamPost) error
}

// Review severities accepted by design.review_submit.
const (
	SeverityBlocker   = "blocker"
	SeverityImportant = "important"
	SeverityMinor     = "minor"
)

// Review limits (spec §5.3).
const (
	ReviewSummaryMax  = 4000
	ReviewMessageMax  = 2000
	MaxReviewFindings = 50
)

// ReviewFinding is one critic finding.
type ReviewFinding struct {
	Severity      string
	Message       string
	RequirementID *int64
	CaseID        *int64
}

// ReviewInput is one design.review_submit payload.
type ReviewInput struct {
	Summary  string
	Findings []ReviewFinding
}

// ReviewResult is the design.review_submit response payload.
type ReviewResult struct {
	Posted bool
	Count  int
}

// SubmitReview validates a critic review and posts one design team-room
// finding per submitted finding. No manual review table is created (spec §5.3).
func (s *GraphService) SubmitReview(ctx context.Context, versionID, requirementID, runID int64, in ReviewInput) (ReviewResult, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return ReviewResult{}, err
	}
	if err := requirePositiveID("requirement_id", requirementID); err != nil {
		return ReviewResult{}, err
	}
	if err := validation.String("summary", in.Summary, 0, ReviewSummaryMax); err != nil {
		return ReviewResult{}, err
	}
	if len(in.Findings) == 0 {
		return ReviewResult{}, platform.Invalid("findings", "at least one finding is required")
	}
	if len(in.Findings) > MaxReviewFindings {
		return ReviewResult{}, platform.Invalid("findings", "at most 50 findings are allowed")
	}
	for index, finding := range in.Findings {
		field := "findings[" + platformid.Format(int64(index)) + "]"
		if !ValidSeverity(finding.Severity) {
			return ReviewResult{}, platform.Invalid(field+".severity", "must be one of blocker, important, minor")
		}
		if err := validation.String(field+".message", strings.TrimSpace(finding.Message), 1, ReviewMessageMax); err != nil {
			return ReviewResult{}, err
		}
		if finding.RequirementID != nil && *finding.RequirementID <= 0 {
			return ReviewResult{}, platform.Invalid(field+".requirement_id", "must be a positive decimal ID")
		}
		if finding.CaseID != nil && *finding.CaseID <= 0 {
			return ReviewResult{}, platform.Invalid(field+".case_id", "must be a positive decimal ID")
		}
	}

	roundID := int64(0)
	round, err := s.repo.GetLatestRound(ctx, versionID)
	if err != nil {
		return ReviewResult{}, mapDesignError(err, "design round")
	}
	if round != nil {
		roundID = round.ID
	}

	if s.teamRoom != nil {
		requirementRef := platformid.Format(requirementID)
		for _, finding := range in.Findings {
			refs := []TeamPostRef{{Kind: "requirement", ID: requirementRef}}
			if finding.CaseID != nil {
				refs = append(refs, TeamPostRef{Kind: "case", ID: platformid.Format(*finding.CaseID)})
			}
			post := TeamPost{
				VersionID: versionID,
				Team:      "design",
				Kind:      "finding",
				Content:   strings.TrimSpace(finding.Message),
				Refs:      refs,
			}
			if runID > 0 {
				run := runID
				post.AgentRunID = &run
			}
			if err := s.teamRoom.Post(ctx, post); err != nil {
				return ReviewResult{}, mapDesignError(err, "team room")
			}
		}
	}

	s.publishEvent(ctx, versionID, "design.review_submitted", requirementID, map[string]any{
		"requirement_id": platformid.Format(requirementID),
		"round_id":       platformid.Format(roundID),
		"count":          len(in.Findings),
	}, 0)
	return ReviewResult{Posted: true, Count: len(in.Findings)}, nil
}

// ValidSeverity reports whether severity is a review finding severity.
func ValidSeverity(severity string) bool {
	switch strings.TrimSpace(severity) {
	case SeverityBlocker, SeverityImportant, SeverityMinor:
		return true
	default:
		return false
	}
}
