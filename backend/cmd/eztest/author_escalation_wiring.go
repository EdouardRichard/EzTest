package main

import (
	"context"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// authorExhaustedFactKind is the version_fact kind of the authoring-budget
// escalation fact (H-F5b, designapp.AuthorExhaustedStore). Like judge_exhausted it
// lives in the version context's append-only version_fact mechanism;
// version_fact.kind is a free-text CHECK (non-empty), so this kind needs no
// migration. It is readable through GET /api/versions/{id}/facts?kind=author_exhausted
// (the production fact read model), which is what makes the stall visible to a
// human instead of silent.
const authorExhaustedFactKind = "author_exhausted"

// authorExhaustedFactKey binds an escalation to exactly one requirement revision.
// A later revision has its own authoring budget, so its stall is its own fact.
func authorExhaustedFactKey(requirementID, requirementVersionID int64) string {
	return "requirement:" + platformid.Format(requirementID) +
		":requirement_version:" + platformid.Format(requirementVersionID)
}

// authorExhaustedFactStore adapts the version FactService to the design context's
// neutral AuthorExhaustedStore port. Insert rides FactService.RecordFact so the
// version context stays the single authority for fact ID / content-hash /
// supersede rules, and the append's own dedupe outcome (AlreadyRecorded) tells the
// caller whether this is a NEW escalation — decided by the database, not by a
// read-then-write race. Same shape as judgeExhaustedFactStore.
type authorExhaustedFactStore struct {
	facts *versionapp.FactService
}

var _ designapp.AuthorExhaustedStore = (*authorExhaustedFactStore)(nil)

func (s *authorExhaustedFactStore) InsertAuthorExhausted(ctx context.Context, versionID, requirementID, requirementVersionID int64, generations int) (bool, error) {
	if s == nil || s.facts == nil {
		return false, designapp.ErrAuthorExhaustedStoreUnavailable
	}
	outcome, err := s.facts.RecordFact(ctx, versiondomain.Fact{
		VersionID: versionID,
		Kind:      authorExhaustedFactKind,
		Key:       authorExhaustedFactKey(requirementID, requirementVersionID),
		Payload: map[string]any{
			"requirement_id":         platformid.Format(requirementID),
			"requirement_version_id": platformid.Format(requirementVersionID),
			"generations":            generations,
			"reason":                 "authoring_generation_budget_exhausted",
		},
		Source: "design_author",
	})
	if err != nil {
		return false, err
	}
	return !outcome.AlreadyRecorded, nil
}
