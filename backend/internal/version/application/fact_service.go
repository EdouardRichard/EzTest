package versionapp

import (
	"context"
	"errors"
	"strings"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// FactService records and reads append-only version facts (spec §6.1). It
// assigns fact IDs and the canonical content hash, exposes conflicts instead of
// resolving them, and never overwrites or deletes a row (D28).
type FactService struct {
	repo   version.FactRepository
	ids    platformid.Generator
	events eventingapp.Publisher
}

// NewFactService wires the fact store. ids assigns version_fact primary keys;
// events may be nil in degraded assemblies (event delivery is best effort and
// never rolls back a fact append).
func NewFactService(repo version.FactRepository, ids platformid.Generator, events eventingapp.Publisher) *FactService {
	return &FactService{repo: repo, ids: ids, events: events}
}

// Record appends one fact. Equal (version, kind, key, content_hash) input is
// not appended again: the stored row is returned with the same ID. A different
// payload under the same (version, kind, key) coexists as a second current row
// and is reported with conflict=true; it is not an error and nothing is
// overwritten. A fact.recorded event is published only for a real append.
//
// The caller-supplied ID is ignored: the store owns fact identity, which also
// lets Record distinguish a real append from an idempotent duplicate by
// comparing the assigned ID with the stored one.
func (s *FactService) Record(ctx context.Context, fact version.Fact) (version.Fact, bool, error) {
	if fact.SupersededBy != nil {
		return version.Fact{}, false, platform.Invalid("superseded_by", "a newly recorded fact must be current")
	}
	fact.Normalize()
	contentHash, err := version.FactContentHash(fact.Payload)
	if err != nil {
		return version.Fact{}, false, platform.Invalid("payload", "must be a JSON object")
	}
	if fact.ContentHash != "" && fact.ContentHash != contentHash {
		return version.Fact{}, false, platform.Invalid("content_hash", "does not match the canonical payload hash")
	}
	fact.ContentHash = contentHash
	if err := fact.Validate(); err != nil {
		return version.Fact{}, false, err
	}

	assignedID := s.ids.Next()
	fact.ID = assignedID
	stored, conflict, err := s.repo.Record(ctx, fact)
	if err != nil {
		return version.Fact{}, false, mapFactRepositoryError(err)
	}
	s.recordAppended(ctx, stored, conflict, stored.ID == assignedID)
	return stored, conflict, nil
}

// Current returns the effective facts (superseded_by IS NULL) of one version.
// An empty kind widens the query to every kind.
func (s *FactService) Current(ctx context.Context, versionID int64, kind string) ([]version.Fact, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	facts, err := s.repo.Current(ctx, versionID, kind)
	if err != nil {
		return nil, mapFactRepositoryError(err)
	}
	return facts, nil
}

// Arbitrate resolves a coexisting conflict by marking drop superseded by keep.
// The dropped row is not deleted and its content is unchanged; Current then
// returns only the kept fact (D28). decision is the required arbitration
// rationale; the frozen version_fact schema has no decision column and the
// 0.2.1 event contract defines no arbitration event, so this store does not
// persist the rationale.
func (s *FactService) Arbitrate(ctx context.Context, keep, drop int64, decision string) error {
	if keep <= 0 {
		return platform.Invalid("keep", "must be a positive decimal ID")
	}
	if drop <= 0 {
		return platform.Invalid("drop", "must be a positive decimal ID")
	}
	if keep == drop {
		return platform.Invalid("drop", "must differ from keep")
	}
	if strings.TrimSpace(decision) == "" {
		return platform.Invalid("decision", "must not be empty")
	}
	if err := s.repo.Arbitrate(ctx, keep, drop); err != nil {
		return mapFactRepositoryError(err)
	}
	return nil
}

// recordAppended publishes fact.recorded after a real append. Event delivery is
// best effort: the DB row is the source of truth. The payload deliberately
// carries only the fact summary, never the payload body, evidence bytes or
// credentials.
func (s *FactService) recordAppended(ctx context.Context, fact version.Fact, conflict, created bool) {
	if !created || s.events == nil {
		return
	}
	versionID := platformid.Format(fact.VersionID)
	payload := map[string]any{
		"fact_id":      platformid.Format(fact.ID),
		"kind":         fact.Kind,
		"key":          fact.Key,
		"confidence":   nil,
		"source":       fact.Source,
		"content_hash": fact.ContentHash,
		"conflict":     conflict,
	}
	if fact.Confidence != nil {
		payload["confidence"] = *fact.Confidence
	}
	if fact.SupersededBy != nil {
		payload["superseded_by"] = platformid.Format(*fact.SupersededBy)
	}
	if len(fact.EvidenceIDs) > 0 {
		evidence := make([]string, 0, len(fact.EvidenceIDs))
		for _, evidenceID := range fact.EvidenceIDs {
			evidence = append(evidence, platformid.Format(evidenceID))
		}
		payload["evidence_refs"] = evidence
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + versionID,
		Type:      "fact.recorded",
		Scope:     map[string]any{"version_id": versionID},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

func mapFactRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrFactNotFound):
		return platform.NotFound("fact")
	case errors.Is(err, version.ErrFactMismatch):
		return platform.Conflict("facts do not share the same version, kind and key")
	case errors.Is(err, version.ErrFactSuperseded):
		return platform.Conflict("fact is no longer current")
	case errors.Is(err, version.ErrNotFound):
		return platform.NotFound("version")
	default:
		return err
	}
}
