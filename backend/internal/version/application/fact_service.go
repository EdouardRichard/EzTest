package versionapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// FactHighConfidenceThreshold is the confidence at (or above) which two
// contradictory current facts are a conflict that must be arbitrated instead of
// silently superseded (R-B6-2).
const FactHighConfidenceThreshold = 0.8

// FactArbitrationDecision is the host decision resolving a fact conflict:
// keep the addressed fact or drop it in favour of the other current fact.
type FactArbitrationDecision string

const (
	FactArbitrationKeep FactArbitrationDecision = "keep"
	FactArbitrationDrop FactArbitrationDecision = "drop"
)

// Fact conflict resolution errors. They are application-level because a
// conflict is a fact-store relationship, not a single-row state.
var (
	// ErrFactNotConflicting reports an arbitration request for a fact that has
	// no competing current value (nothing to arbitrate).
	ErrFactNotConflicting = errors.New("version fact has no conflicting current value")
	// ErrFactArbitrationAmbiguous reports a drop request for a fact with more
	// than one competing current value: the caller must keep the winner.
	ErrFactArbitrationAmbiguous = errors.New("version fact has multiple competing current values")
)

// FactAppendDecision is the append-time resolution of a new fact against the
// current facts sharing its (version_id, kind, key). Append-only history is
// preserved: at most superseded_by changes on an existing row.
type FactAppendDecision struct {
	Conflict        bool
	SupersedeOldIDs []int64
	SupersedeNewBy  *int64
}

// FactArbitration is the resolved state of one conflict arbitration.
type FactArbitration struct {
	VersionID  int64
	FactID     int64
	Kind       string
	Key        string
	Decision   FactArbitrationDecision
	Reason     string
	KeepID     int64
	DroppedIDs []int64
	// Facts is the current (superseded_by IS NULL) state of the same
	// (version, kind, key) after the decision, ordered by id.
	Facts   []version.Fact
	Changed bool
}

// factArbitrationStore is implemented by fact stores that can resolve a whole
// conflict group atomically. The domain repository port stays the append-only
// contract; a store without this capability answers 503 from the handler.
type factArbitrationStore interface {
	ArbitrateFact(ctx context.Context, versionID, factID int64, decision FactArbitrationDecision) (FactArbitration, error)
}

// FactService records and reads append-only version facts (spec §6.1). It
// assigns fact IDs and the canonical content hash, applies the append-time
// supersede/conflict rules (R-B6-1/R-B6-2), exposes conflicts for arbitration,
// and never overwrites or deletes a row (D28).
type FactService struct {
	repo   version.FactRepository
	ids    platformid.Generator
	events eventingapp.Publisher
	audit  auditapp.Recorder
}

// FactServiceOption configures the optional best-effort sinks of a FactService.
type FactServiceOption func(*FactService)

// WithFactAudit wires the best-effort audit recorder used by arbitration.
func WithFactAudit(recorder auditapp.Recorder) FactServiceOption {
	return func(s *FactService) { s.audit = recorder }
}

// NewFactService wires the fact store. ids assigns version_fact primary keys;
// events and audit may be nil in degraded assemblies (both are best effort and
// never roll back a fact write).
func NewFactService(repo version.FactRepository, ids platformid.Generator, events eventingapp.Publisher, opts ...FactServiceOption) *FactService {
	service := &FactService{repo: repo, ids: ids, events: events}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// ResolveFactAppend decides how a new fact interacts with the current facts of
// the same (version_id, kind, key), which must have a different content hash.
// R-B6-2 wins over R-B6-1: two high-confidence contradictory facts coexist and
// are marked as a conflict. Otherwise a not-lower (or old-unknown) confidence
// supersedes the old row; a lower/unknown new confidence leaves the old row
// current and stores the new row as immediately superseded, so no duplicate
// current row is left without a conflict (R-B6-3).
func ResolveFactAppend(current []version.Fact, fact version.Fact) FactAppendDecision {
	switch len(current) {
	case 0:
		return FactAppendDecision{}
	case 1:
		// The whole decision is about one current predecessor.
	default:
		// An unresolved conflict is already a conflict; a new fact joins it as
		// another current row instead of silently choosing a winner.
		return FactAppendDecision{Conflict: true}
	}
	old := current[0]
	if old.ContentHash != "" && old.ContentHash == fact.ContentHash {
		// Identical content is a store-level dedupe, not an append.
		return FactAppendDecision{}
	}
	if isHighConfidence(old.Confidence) && isHighConfidence(fact.Confidence) {
		// R-B6-2: both high-confidence and contradictory -> keep both current
		// and let the host arbitrate.
		return FactAppendDecision{Conflict: true}
	}
	if old.Confidence == nil || (fact.Confidence != nil && *fact.Confidence >= *old.Confidence) {
		return FactAppendDecision{SupersedeOldIDs: []int64{old.ID}}
	}
	oldID := old.ID
	return FactAppendDecision{SupersedeNewBy: &oldID}
}

// ResolveFactArbitration applies keep/drop to the rows of one key and reports
// the current state after the decision. It is pure: the store writes the
// superseded_by values. Repeating a decision whose outcome already holds is an
// idempotent no-op (Changed=false).
func ResolveFactArbitration(rows []version.Fact, factID int64, decision FactArbitrationDecision) (FactArbitration, error) {
	if decision != FactArbitrationKeep && decision != FactArbitrationDrop {
		return FactArbitration{}, fmt.Errorf("resolve fact arbitration: unknown decision %q", decision)
	}
	target, ok := factByID(rows, factID)
	if !ok {
		return FactArbitration{}, version.ErrFactNotFound
	}
	current := currentFacts(rows)
	outcome := FactArbitration{
		VersionID:  target.VersionID,
		FactID:     factID,
		Kind:       target.Kind,
		Key:        target.Key,
		Decision:   decision,
		DroppedIDs: []int64{},
		Facts:      current,
	}

	if target.SupersededBy != nil {
		if decision == FactArbitrationKeep {
			// The fact already lost; a keep cannot silently reinstate it.
			return FactArbitration{}, version.ErrFactSuperseded
		}
		// The desired post-state (the addressed fact is not current) already
		// holds, so this is an idempotent success.
		outcome.DroppedIDs = []int64{factID}
		if len(current) > 0 {
			outcome.KeepID = current[0].ID
		}
		return outcome, nil
	}

	others := make([]version.Fact, 0, len(current))
	for _, fact := range current {
		if fact.ID != factID {
			others = append(others, fact)
		}
	}
	switch decision {
	case FactArbitrationKeep:
		if len(others) == 0 {
			if len(rows) == 1 {
				return FactArbitration{}, ErrFactNotConflicting
			}
			// The addressed fact is already the only current fact: repeating a
			// previous keep is idempotent.
			outcome.KeepID = factID
			return outcome, nil
		}
		outcome.KeepID = factID
		for _, other := range others {
			outcome.DroppedIDs = append(outcome.DroppedIDs, other.ID)
		}
	case FactArbitrationDrop:
		if len(others) == 0 {
			return FactArbitration{}, ErrFactNotConflicting
		}
		if len(others) > 1 {
			return FactArbitration{}, ErrFactArbitrationAmbiguous
		}
		outcome.KeepID = others[0].ID
		outcome.DroppedIDs = []int64{factID}
	}
	outcome.Changed = true
	outcome.Facts = currentFactsAfter(current, outcome.DroppedIDs)
	return outcome, nil
}

// ArbitrateFact resolves one fact conflict with the host decision. The store
// writes superseded_by and the returned result; fact.arbitrated and the audit
// record are best-effort after the write.
func (s *FactService) ArbitrateFact(ctx context.Context, versionID, factID int64, decision FactArbitrationDecision, reason string, actorID int64) (FactArbitration, error) {
	if versionID <= 0 {
		return FactArbitration{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if factID <= 0 {
		return FactArbitration{}, platform.Invalid("fid", "must be a positive decimal ID")
	}
	normalized := FactArbitrationDecision(strings.ToLower(strings.TrimSpace(string(decision))))
	if normalized != FactArbitrationKeep && normalized != FactArbitrationDrop {
		return FactArbitration{}, platform.Invalid("decision", "must be one of keep, drop")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return FactArbitration{}, platform.Invalid("reason", "must not be empty")
	}
	store, ok := s.repo.(factArbitrationStore)
	if !ok {
		return FactArbitration{}, platform.Unavailable("fact arbitration is not configured")
	}
	outcome, err := store.ArbitrateFact(ctx, versionID, factID, normalized)
	if err != nil {
		return FactArbitration{}, mapFactRepositoryError(err)
	}
	outcome.Decision = normalized
	outcome.Reason = reason
	if outcome.Changed {
		s.recordArbitrated(ctx, outcome, actorID)
	}
	return outcome, nil
}

func isHighConfidence(confidence *float64) bool {
	return confidence != nil && *confidence >= FactHighConfidenceThreshold
}

func factByID(rows []version.Fact, id int64) (version.Fact, bool) {
	for _, fact := range rows {
		if fact.ID == id {
			return fact, true
		}
	}
	return version.Fact{}, false
}

func currentFacts(rows []version.Fact) []version.Fact {
	current := make([]version.Fact, 0, len(rows))
	for _, fact := range rows {
		if fact.SupersededBy == nil {
			current = append(current, fact)
		}
	}
	return current
}

// currentFactsAfter projects the current rows that survive the arbitration.
func currentFactsAfter(current []version.Fact, droppedIDs []int64) []version.Fact {
	dropped := make(map[int64]struct{}, len(droppedIDs))
	for _, id := range droppedIDs {
		dropped[id] = struct{}{}
	}
	kept := make([]version.Fact, 0, len(current))
	for _, fact := range current {
		if _, ok := dropped[fact.ID]; !ok {
			kept = append(kept, fact)
		}
	}
	return kept
}

// Record appends one fact. Equal (version, kind, key, content_hash) input is
// not appended again: the stored row is returned with the same ID. Otherwise
// the store applies ResolveFactAppend: a not-lower confidence supersedes the
// current row, a lower/unknown confidence is stored as superseded, and two
// high-confidence contradictions coexist and report conflict=true. Nothing is
// ever overwritten or deleted, and conflicts are not errors (D28).
//
// The caller-supplied ID is ignored: the store owns fact identity, which also
// lets Record distinguish a real append from an idempotent duplicate by
// comparing the assigned ID with the stored one. fact.recorded and
// fact.conflict are published only for a real append.
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
	if stored.ID == assignedID {
		s.recordAppended(ctx, stored, conflict, true)
		if conflict {
			s.recordConflict(ctx, stored)
		}
	}
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
		return platform.StateConflict("fact is no longer current")
	case errors.Is(err, ErrFactNotConflicting):
		return platform.StateConflict("fact has no conflicting current value")
	case errors.Is(err, ErrFactArbitrationAmbiguous):
		return platform.StateConflict("fact has multiple competing current values; keep the winner instead")
	case errors.Is(err, version.ErrNotFound):
		return platform.NotFound("version")
	default:
		return err
	}
}

// recordConflict publishes fact.conflict for a real append that left two (or
// more) current facts of the same key. Delivery is best effort; the rows are the
// source of truth and the conflict is the coexistence itself, not a column.
func (s *FactService) recordConflict(ctx context.Context, fact version.Fact) {
	if s.events == nil {
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
		"conflict":     true,
	}
	if fact.Confidence != nil {
		payload["confidence"] = *fact.Confidence
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + versionID,
		Type:      "fact.conflict",
		Scope:     map[string]any{"version_id": versionID},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

// recordArbitrated writes the best-effort audit row and fact.arbitrated event
// of one real arbitration. A repeated no-op decision writes neither.
func (s *FactService) recordArbitrated(ctx context.Context, outcome FactArbitration, actorID int64) {
	var actor *int64
	if actorID > 0 {
		value := actorID
		actor = &value
	}
	versionID := platformid.Format(outcome.VersionID)
	factID := platformid.Format(outcome.FactID)
	keepID := platformid.Format(outcome.KeepID)
	droppedIDs := decimalIDStrings(outcome.DroppedIDs)
	metadata := map[string]any{
		"version_id":  versionID,
		"fact_id":     factID,
		"kind":        outcome.Kind,
		"key":         outcome.Key,
		"decision":    string(outcome.Decision),
		"reason":      outcome.Reason,
		"keep_id":     keepID,
		"dropped_ids": droppedIDs,
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    actor,
			Action:     "fact.arbitrated",
			TargetKind: "version_fact",
			TargetID:   factID,
			RequestID:  requestid.From(ctx),
			Metadata:   metadata,
		})
	}
	if s.events == nil {
		return
	}
	payload := map[string]any{
		"fact_id":     factID,
		"kind":        outcome.Kind,
		"key":         outcome.Key,
		"decision":    string(outcome.Decision),
		"reason":      outcome.Reason,
		"keep_id":     keepID,
		"dropped_ids": droppedIDs,
		"changed":     true,
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + versionID,
		Type:      "fact.arbitrated",
		Scope:     map[string]any{"version_id": versionID},
		Payload:   payload,
		ActorID:   actor,
		RequestID: requestid.From(ctx),
	})
}

func decimalIDStrings(ids []int64) []string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, platformid.Format(id))
	}
	return values
}
