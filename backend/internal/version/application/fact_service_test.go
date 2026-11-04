package versionapp_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeFactRepo is an in-memory version.FactRepository plus the conflict
// arbitration store. It applies the production append/arbitration resolutions
// (versionapp.ResolveFactAppend / ResolveFactArbitration), so the application
// tests exercise the real decision rules without a database; the SQL adapter is
// covered by the integration suite.
type fakeFactRepo struct {
	facts map[int64]version.Fact
	order []int64
}

func newFakeFactRepo() *fakeFactRepo {
	return &fakeFactRepo{facts: map[int64]version.Fact{}}
}

// Record appends a fact unless (version_id, kind, key, content_hash) already
// exists. The append resolution decides whether the previous current row is
// superseded, the new row is immediately superseded, or both coexist as a
// conflict.
func (f *fakeFactRepo) Record(_ context.Context, fact version.Fact) (version.Fact, bool, error) {
	for _, id := range f.order {
		existing := f.facts[id]
		if existing.VersionID == fact.VersionID && existing.Kind == fact.Kind &&
			existing.Key == fact.Key && existing.ContentHash == fact.ContentHash {
			return existing, false, nil
		}
	}
	decision := versionapp.ResolveFactAppend(f.siblings(fact.VersionID, fact.Kind, fact.Key, fact.ContentHash), fact)
	fact.SupersededBy = decision.SupersedeNewBy
	f.facts[fact.ID] = fact
	f.order = append(f.order, fact.ID)
	for _, oldID := range decision.SupersedeOldIDs {
		old := f.facts[oldID]
		newID := fact.ID
		old.SupersededBy = &newID
		f.facts[oldID] = old
	}
	return fact, decision.Conflict, nil
}

func (f *fakeFactRepo) Current(_ context.Context, versionID int64, kind string) ([]version.Fact, error) {
	current := make([]version.Fact, 0)
	for _, id := range f.order {
		fact := f.facts[id]
		if fact.VersionID == versionID && (kind == "" || fact.Kind == kind) && fact.SupersededBy == nil {
			current = append(current, fact)
		}
	}
	return current, nil
}

// Arbitrate is the legacy pairwise write retained for compatibility.
func (f *fakeFactRepo) Arbitrate(_ context.Context, keepID, dropID int64) error {
	keep, ok := f.facts[keepID]
	if !ok {
		return version.ErrFactNotFound
	}
	drop, ok := f.facts[dropID]
	if !ok {
		return version.ErrFactNotFound
	}
	if keep.VersionID != drop.VersionID || keep.Kind != drop.Kind || keep.Key != drop.Key {
		return version.ErrFactMismatch
	}
	if keep.SupersededBy != nil || drop.SupersededBy != nil {
		return version.ErrFactSuperseded
	}
	drop.SupersededBy = &keepID
	f.facts[dropID] = drop
	return nil
}

// ArbitrateFact applies the production conflict resolution atomically inside
// the fake store, mirroring the SQL adapter.
func (f *fakeFactRepo) ArbitrateFact(
	_ context.Context, versionID, factID int64, decision versionapp.FactArbitrationDecision,
) (versionapp.FactArbitration, error) {
	target, ok := f.facts[factID]
	if !ok || target.VersionID != versionID {
		return versionapp.FactArbitration{}, version.ErrFactNotFound
	}
	rows := f.rowsByKey(target.VersionID, target.Kind, target.Key)
	outcome, err := versionapp.ResolveFactArbitration(rows, factID, decision)
	if err != nil {
		return versionapp.FactArbitration{}, err
	}
	if outcome.Changed {
		for _, dropID := range outcome.DroppedIDs {
			drop := f.facts[dropID]
			keepID := outcome.KeepID
			drop.SupersededBy = &keepID
			f.facts[dropID] = drop
		}
	}
	current, _ := f.Current(context.Background(), target.VersionID, target.Kind)
	filtered := make([]version.Fact, 0, len(current))
	for _, fact := range current {
		if fact.Key == target.Key {
			filtered = append(filtered, fact)
		}
	}
	outcome.Facts = filtered
	return outcome, nil
}

// Get exposes one stored row (including superseded rows) to the tests.
func (f *fakeFactRepo) Get(id int64) version.Fact { return f.facts[id] }

// siblings returns the current rows of the same (version, kind, key) whose
// content hash differs from hash, in append order.
func (f *fakeFactRepo) siblings(versionID int64, kind, key, hash string) []version.Fact {
	items := make([]version.Fact, 0)
	for _, id := range f.order {
		fact := f.facts[id]
		if fact.VersionID == versionID && fact.Kind == kind && fact.Key == key &&
			fact.SupersededBy == nil && fact.ContentHash != hash {
			items = append(items, fact)
		}
	}
	return items
}

func (f *fakeFactRepo) rowsByKey(versionID int64, kind, key string) []version.Fact {
	items := make([]version.Fact, 0)
	for _, id := range f.order {
		fact := f.facts[id]
		if fact.VersionID == versionID && fact.Kind == kind && fact.Key == key {
			items = append(items, fact)
		}
	}
	return items
}

func (f *fakeFactRepo) count() int { return len(f.facts) }

var _ version.FactRepository = (*fakeFactRepo)(nil)

// ---------- fact service test environment ----------

type factTestEnv struct {
	service  *versionapp.FactService
	repo     *fakeFactRepo
	events   *fakePublisher
	audit    *fakeAudit
	nextFact int64
}

func newFactTestEnv(t *testing.T) *factTestEnv {
	t.Helper()
	env := &factTestEnv{
		repo:   newFakeFactRepo(),
		events: &fakePublisher{},
		audit:  &fakeAudit{},
	}
	env.service = versionapp.NewFactService(env.repo, &seqIDs{}, env.events, versionapp.WithFactAudit(env.audit))
	return env
}

// append records one fact and returns it with the repository conflict flag.
func (e *factTestEnv) append(t *testing.T, versionID int64, kind, key string, payload map[string]any, confidence *float64) (version.Fact, bool) {
	t.Helper()
	fact, conflict, err := e.service.Record(context.Background(), version.Fact{
		VersionID:  versionID,
		Kind:       kind,
		Key:        key,
		Payload:    payload,
		Confidence: confidence,
		Source:     "host",
	})
	if err != nil {
		t.Fatalf("Record(%s/%s): %v", kind, key, err)
	}
	return fact, conflict
}

func float64Ptr(value float64) *float64 { return &value }

func findFactEvent(t *testing.T, publisher *fakePublisher, eventType string) eventingapp.Event {
	t.Helper()
	for _, event := range publisher.events {
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("event %q not published; got %v", eventType, eventTypes(publisher))
	return eventingapp.Event{}
}

func countFactEvents(publisher *fakePublisher, eventType string) int {
	count := 0
	for _, event := range publisher.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// ---------- append resolution ----------

// TestFactSupersedesOldValue pins R-B6-1: a new fact under the same
// (version, kind, key) whose confidence is not lower than the current row
// supersedes it atomically; the old row is preserved, not deleted.
func TestFactSupersedesOldValue(t *testing.T) {
	t.Run("higher confidence", func(t *testing.T) {
		env := newFactTestEnv(t)
		old, conflict := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+1"}, float64Ptr(0.6))
		if conflict {
			t.Fatal("first fact reported a conflict")
		}
		newer, conflict := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+3"}, float64Ptr(0.7))
		if conflict {
			t.Fatal("a supersede must not be reported as an unresolved conflict")
		}

		stored := env.repo.Get(old.ID)
		if stored.SupersededBy == nil || *stored.SupersededBy != newer.ID {
			t.Fatalf("old superseded_by = %#v, want %d", stored.SupersededBy, newer.ID)
		}
		if newer.SupersededBy != nil {
			t.Fatalf("new fact %d must stay current, superseded_by = %#v", newer.ID, newer.SupersededBy)
		}
		current, err := env.service.Current(context.Background(), 7, "decision")
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if len(current) != 1 || current[0].ID != newer.ID {
			t.Fatalf("current = %#v, want only the new fact %d", current, newer.ID)
		}
		if env.repo.count() != 2 {
			t.Fatalf("history rows = %d, want 2 (append-only)", env.repo.count())
		}
		if countFactEvents(env.events, "fact.conflict") != 0 {
			t.Fatalf("supersede published fact.conflict: %v", eventTypes(env.events))
		}
	})

	t.Run("equal confidence", func(t *testing.T) {
		env := newFactTestEnv(t)
		old, _ := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+1"}, float64Ptr(0.7))
		newer, conflict := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+3"}, float64Ptr(0.7))
		if conflict {
			t.Fatal("equal medium confidence must supersede, not conflict")
		}
		stored := env.repo.Get(old.ID)
		if stored.SupersededBy == nil || *stored.SupersededBy != newer.ID {
			t.Fatalf("old superseded_by = %#v, want %d", stored.SupersededBy, newer.ID)
		}
	})
}

// TestFactLowConfidenceDoesNotSupersede pins the negative side of R-B6-1: a
// lower (or unknown) confidence cannot replace the current fact. The new row is
// kept for history as immediately superseded, so no duplicate current row
// remains without a conflict.
func TestFactLowConfidenceDoesNotSupersede(t *testing.T) {
	t.Run("lower confidence", func(t *testing.T) {
		env := newFactTestEnv(t)
		old, _ := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+1"}, float64Ptr(0.9))
		lower, conflict := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+3"}, float64Ptr(0.4))
		if conflict {
			t.Fatal("a lower-confidence contradiction is not a high-confidence conflict")
		}
		if got := env.repo.Get(old.ID); got.SupersededBy != nil {
			t.Fatalf("old fact was superseded by a lower-confidence fact: %#v", got.SupersededBy)
		}
		if lower.SupersededBy == nil || *lower.SupersededBy != old.ID {
			t.Fatalf("lower fact superseded_by = %#v, want the current fact %d", lower.SupersededBy, old.ID)
		}
		current, err := env.service.Current(context.Background(), 7, "decision")
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if len(current) != 1 || current[0].ID != old.ID {
			t.Fatalf("current = %#v, want only the high-confidence fact %d", current, old.ID)
		}
		if env.repo.count() != 2 {
			t.Fatalf("history rows = %d, want 2 (append-only)", env.repo.count())
		}
		if countFactEvents(env.events, "fact.conflict") != 0 {
			t.Fatalf("lower-confidence append published fact.conflict: %v", eventTypes(env.events))
		}
	})

	t.Run("unknown new confidence", func(t *testing.T) {
		env := newFactTestEnv(t)
		old, _ := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+1"}, float64Ptr(0.9))
		unknown, _ := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+3"}, nil)
		if got := env.repo.Get(old.ID); got.SupersededBy != nil {
			t.Fatalf("old fact was superseded by an unknown-confidence fact: %#v", got.SupersededBy)
		}
		if unknown.SupersededBy == nil || *unknown.SupersededBy != old.ID {
			t.Fatalf("unknown-confidence fact superseded_by = %#v, want %d", unknown.SupersededBy, old.ID)
		}
	})
}

// TestFactConflictArbitrated pins R-B6-2: two high-confidence contradictory
// facts both stay current, fact.conflict is emitted, and an explicit keep/drop
// arbitration writes superseded_by, emits fact.arbitrated, audits the decision
// and is idempotent on repeat.
func TestFactConflictArbitrated(t *testing.T) {
	t.Run("keep", func(t *testing.T) {
		env := newFactTestEnv(t)
		first, _ := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+1"}, float64Ptr(0.9))
		second, conflict := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+3"}, float64Ptr(0.9))
		if !conflict {
			t.Fatal("two high-confidence contradictory facts did not report a conflict")
		}
		current, err := env.service.Current(context.Background(), 7, "")
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if len(current) != 2 {
			t.Fatalf("current = %#v, want both conflicting facts current", current)
		}
		conflictEvent := findFactEvent(t, env.events, "fact.conflict")
		if conflictEvent.Channel != "version:7" {
			t.Fatalf("fact.conflict channel = %q", conflictEvent.Channel)
		}
		if conflictEvent.Payload["fact_id"] != strconv.FormatInt(second.ID, 10) {
			t.Fatalf("fact.conflict fact_id = %#v, want %d", conflictEvent.Payload["fact_id"], second.ID)
		}
		if conflictEvent.Payload["conflict"] != true {
			t.Fatalf("fact.conflict conflict flag = %#v", conflictEvent.Payload["conflict"])
		}

		outcome, err := env.service.ArbitrateFact(context.Background(), 7, second.ID,
			versionapp.FactArbitrationKeep, "host prefers T+3", 42)
		if err != nil {
			t.Fatalf("ArbitrateFact keep: %v", err)
		}
		if !outcome.Changed || outcome.KeepID != second.ID {
			t.Fatalf("keep outcome = %+v, want changed with keep %d", outcome, second.ID)
		}
		if len(outcome.DroppedIDs) != 1 || outcome.DroppedIDs[0] != first.ID {
			t.Fatalf("keep dropped ids = %#v, want [%d]", outcome.DroppedIDs, first.ID)
		}
		if len(outcome.Facts) != 1 || outcome.Facts[0].ID != second.ID {
			t.Fatalf("keep current facts = %#v, want only %d", outcome.Facts, second.ID)
		}
		dropped := env.repo.Get(first.ID)
		if dropped.SupersededBy == nil || *dropped.SupersededBy != second.ID {
			t.Fatalf("dropped superseded_by = %#v, want %d", dropped.SupersededBy, second.ID)
		}
		if env.repo.count() != 2 {
			t.Fatalf("arbitration deleted history: rows = %d, want 2", env.repo.count())
		}

		arbitrated := findFactEvent(t, env.events, "fact.arbitrated")
		if arbitrated.Channel != "version:7" || arbitrated.Payload["decision"] != "keep" {
			t.Fatalf("fact.arbitrated = %#v", arbitrated)
		}
		if arbitrated.Payload["keep_id"] != strconv.FormatInt(second.ID, 10) {
			t.Fatalf("fact.arbitrated keep_id = %#v, want %d", arbitrated.Payload["keep_id"], second.ID)
		}
		if arbitrated.Payload["reason"] != "host prefers T+3" {
			t.Fatalf("fact.arbitrated reason = %#v", arbitrated.Payload["reason"])
		}
		if len(env.audit.events) != 1 {
			t.Fatalf("audit events = %d, want 1", len(env.audit.events))
		}
		audit := env.audit.events[0]
		if audit.Action != "fact.arbitrated" || audit.TargetKind != "version_fact" ||
			audit.TargetID != strconv.FormatInt(second.ID, 10) {
			t.Fatalf("audit = %+v", audit)
		}
		if audit.ActorID == nil || *audit.ActorID != 42 {
			t.Fatalf("audit actor = %#v, want 42", audit.ActorID)
		}
		if audit.Metadata["decision"] != "keep" || audit.Metadata["reason"] != "host prefers T+3" {
			t.Fatalf("audit metadata = %#v", audit.Metadata)
		}

		// Repeating the same decision is an idempotent success: the desired
		// current state already holds, so no second plan/event/audit is written.
		eventsBefore := len(env.events.events)
		auditsBefore := len(env.audit.events)
		repeated, err := env.service.ArbitrateFact(context.Background(), 7, second.ID,
			versionapp.FactArbitrationKeep, "host prefers T+3", 42)
		if err != nil {
			t.Fatalf("repeated ArbitrateFact: %v", err)
		}
		if repeated.Changed {
			t.Fatal("repeated arbitration reported a second change")
		}
		if len(env.events.events) != eventsBefore || len(env.audit.events) != auditsBefore {
			t.Fatalf("repeated arbitration duplicated event/audit: events %d->%d audits %d->%d",
				eventsBefore, len(env.events.events), auditsBefore, len(env.audit.events))
		}
	})

	t.Run("drop", func(t *testing.T) {
		env := newFactTestEnv(t)
		first, _ := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+1"}, float64Ptr(0.9))
		second, conflict := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+3"}, float64Ptr(0.9))
		if !conflict {
			t.Fatal("two high-confidence contradictory facts did not report a conflict")
		}

		outcome, err := env.service.ArbitrateFact(context.Background(), 7, first.ID,
			versionapp.FactArbitrationDrop, "T+1 conflicts with the accepted answer", 42)
		if err != nil {
			t.Fatalf("ArbitrateFact drop: %v", err)
		}
		if !outcome.Changed || outcome.KeepID != second.ID {
			t.Fatalf("drop outcome = %+v, want changed with keep %d", outcome, second.ID)
		}
		if len(outcome.DroppedIDs) != 1 || outcome.DroppedIDs[0] != first.ID {
			t.Fatalf("drop dropped ids = %#v, want [%d]", outcome.DroppedIDs, first.ID)
		}
		dropped := env.repo.Get(first.ID)
		if dropped.SupersededBy == nil || *dropped.SupersededBy != second.ID {
			t.Fatalf("dropped superseded_by = %#v, want %d", dropped.SupersededBy, second.ID)
		}
		current, err := env.service.Current(context.Background(), 7, "decision")
		if err != nil {
			t.Fatalf("Current after drop: %v", err)
		}
		if len(current) != 1 || current[0].ID != second.ID {
			t.Fatalf("current after drop = %#v, want only %d", current, second.ID)
		}

		repeated, err := env.service.ArbitrateFact(context.Background(), 7, first.ID,
			versionapp.FactArbitrationDrop, "T+1 conflicts with the accepted answer", 42)
		if err != nil {
			t.Fatalf("repeated drop arbitration: %v", err)
		}
		if repeated.Changed {
			t.Fatal("repeated drop arbitration reported a second change")
		}
		if got := countFactEvents(env.events, "fact.arbitrated"); got != 1 {
			t.Fatalf("fact.arbitrated events = %d, want 1", got)
		}
		if len(env.audit.events) != 1 {
			t.Fatalf("audit events = %d, want 1", len(env.audit.events))
		}
	})
}

// TestFactAppendOnlyAndConflict keeps the pre-existing append-only contract:
// duplicate rows are deduplicated by content hash, contradictory high-confidence
// rows coexist, and the legacy pairwise Arbitrate only writes superseded_by.
func TestFactAppendOnlyAndConflict(t *testing.T) {
	env := newFactTestEnv(t)
	payload := map[string]any{"url": "/checkout", "title": "结算"}
	first, conflict := env.append(t, 7, "page", "checkout", payload, float64Ptr(0.9))
	if conflict {
		t.Fatal("first fact reported a conflict")
	}
	if first.ID == 0 || len(first.ContentHash) != 64 {
		t.Fatalf("first fact = %#v, want an assigned ID and SHA-256 content hash", first)
	}

	reordered := map[string]any{"title": "结算", "url": "/checkout"}
	again, conflict := env.append(t, 7, "page", "checkout", reordered, float64Ptr(0.9))
	if conflict {
		t.Fatal("duplicate append reported a conflict")
	}
	if again.ID != first.ID || env.repo.count() != 1 {
		t.Fatalf("duplicate = %#v rows=%d, want existing %d", again, env.repo.count(), first.ID)
	}
	if got := countFactEvents(env.events, "fact.recorded"); got != 1 {
		t.Fatalf("fact.recorded events after duplicate = %d, want 1", got)
	}

	second, conflict := env.append(t, 7, "page", "checkout", map[string]any{"url": "/checkout-v2"}, float64Ptr(0.9))
	if !conflict {
		t.Fatal("different high-confidence payload under the same key did not report a conflict")
	}
	if second.ID == first.ID || second.ContentHash == first.ContentHash || env.repo.count() != 2 {
		t.Fatalf("conflicting fact = %#v rows=%d", second, env.repo.count())
	}
	current, err := env.service.Current(context.Background(), 7, "page")
	if err != nil {
		t.Fatalf("Current with conflict: %v", err)
	}
	if len(current) != 2 {
		t.Fatalf("current facts = %#v, want the two coexisting rows", current)
	}
	if got := countFactEvents(env.events, "fact.recorded"); got != 2 {
		t.Fatalf("fact.recorded events = %d, want one per append", got)
	}
	recorded := findFactEvent(t, env.events, "fact.recorded")
	if recorded.Channel != "version:7" {
		t.Fatalf("fact.recorded channel = %q", recorded.Channel)
	}

	// The legacy pairwise arbitration keeps working for existing callers; it
	// only writes superseded_by and never deletes the losing row.
	if err := env.service.Arbitrate(context.Background(), second.ID, first.ID, "newer exploration evidence"); err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	current, err = env.service.Current(context.Background(), 7, "page")
	if err != nil {
		t.Fatalf("Current after arbitration: %v", err)
	}
	if len(current) != 1 || current[0].ID != second.ID {
		t.Fatalf("current after arbitration = %#v, want only fact %d", current, second.ID)
	}
	if env.repo.count() != 2 {
		t.Fatalf("arbitration deleted a row: count = %d, want 2", env.repo.count())
	}
	dropped := env.repo.Get(first.ID)
	if dropped.SupersededBy == nil || *dropped.SupersededBy != second.ID {
		t.Fatalf("dropped fact superseded_by = %#v, want %d", dropped.SupersededBy, second.ID)
	}
}

// TestFactArbitrationErrors pins the typed failure surface: unknown or
// cross-version facts, non-conflicting facts, ambiguous drop requests, invalid
// decisions and missing reasons, and a keep that contradicts an earlier drop.
func TestFactArbitrationErrors(t *testing.T) {
	env := newFactTestEnv(t)
	first, _ := env.append(t, 7, "decision", "refund", map[string]any{"value": "T+1"}, float64Ptr(0.9))
	env.append(t, 7, "decision", "refund", map[string]any{"value": "T+3"}, float64Ptr(0.9))

	t.Run("unknown fact", func(t *testing.T) {
		_, err := env.service.ArbitrateFact(context.Background(), 7, 999999,
			versionapp.FactArbitrationKeep, "keep", 42)
		if apiErr := apiError(t, err); apiErr.Status != 404 || apiErr.Code != "not_found" {
			t.Fatalf("unknown fact error = %+v", apiErr)
		}
	})

	t.Run("cross-version fact", func(t *testing.T) {
		_, err := env.service.ArbitrateFact(context.Background(), 8, first.ID,
			versionapp.FactArbitrationKeep, "keep", 42)
		if apiErr := apiError(t, err); apiErr.Status != 404 || apiErr.Code != "not_found" {
			t.Fatalf("cross-version fact error = %+v", apiErr)
		}
	})

	t.Run("non-conflicting fact", func(t *testing.T) {
		solo, _ := env.append(t, 9, "decision", "refund", map[string]any{"value": "solo"}, float64Ptr(0.9))
		_, err := env.service.ArbitrateFact(context.Background(), 9, solo.ID,
			versionapp.FactArbitrationKeep, "keep", 42)
		if apiErr := apiError(t, err); apiErr.Status != 409 || apiErr.Code != "state_conflict" {
			t.Fatalf("non-conflicting error = %+v", apiErr)
		}
	})

	t.Run("ambiguous drop", func(t *testing.T) {
		ambiguous := newFactTestEnv(t)
		a, _ := ambiguous.append(t, 7, "decision", "refund", map[string]any{"value": "A"}, float64Ptr(0.9))
		ambiguous.append(t, 7, "decision", "refund", map[string]any{"value": "B"}, float64Ptr(0.9))
		ambiguous.append(t, 7, "decision", "refund", map[string]any{"value": "C"}, float64Ptr(0.9))
		_, err := ambiguous.service.ArbitrateFact(context.Background(), 7, a.ID,
			versionapp.FactArbitrationDrop, "drop A", 42)
		if apiErr := apiError(t, err); apiErr.Status != 409 || apiErr.Code != "state_conflict" {
			t.Fatalf("ambiguous drop error = %+v", apiErr)
		}
	})

	t.Run("keep on a dropped fact", func(t *testing.T) {
		dropped := newFactTestEnv(t)
		a, _ := dropped.append(t, 7, "decision", "refund", map[string]any{"value": "A"}, float64Ptr(0.9))
		dropped.append(t, 7, "decision", "refund", map[string]any{"value": "B"}, float64Ptr(0.9))
		if _, err := dropped.service.ArbitrateFact(context.Background(), 7, a.ID,
			versionapp.FactArbitrationDrop, "drop A", 42); err != nil {
			t.Fatalf("seed drop: %v", err)
		}
		_, err := dropped.service.ArbitrateFact(context.Background(), 7, a.ID,
			versionapp.FactArbitrationKeep, "keep A", 42)
		if apiErr := apiError(t, err); apiErr.Status != 409 || apiErr.Code != "state_conflict" {
			t.Fatalf("keep-after-drop error = %+v", apiErr)
		}
	})

	t.Run("invalid decision", func(t *testing.T) {
		_, err := env.service.ArbitrateFact(context.Background(), 7, first.ID,
			versionapp.FactArbitrationDecision("maybe"), "reason", 42)
		if apiErr := apiError(t, err); apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
			t.Fatalf("invalid decision error = %+v", apiErr)
		}
	})

	t.Run("missing reason", func(t *testing.T) {
		_, err := env.service.ArbitrateFact(context.Background(), 7, first.ID,
			versionapp.FactArbitrationKeep, "  ", 42)
		if apiErr := apiError(t, err); apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
			t.Fatalf("missing reason error = %+v", apiErr)
		}
	})

}

// ---------- pure resolution rules ----------

func TestResolveFactAppend(t *testing.T) {
	medium := float64Ptr(0.7)
	high := float64Ptr(0.9)
	low := float64Ptr(0.5)
	fact := func(id int64, confidence *float64, hash string) version.Fact {
		return version.Fact{ID: id, Confidence: confidence, ContentHash: hash}
	}
	cases := []struct {
		name         string
		current      []version.Fact
		newFact      version.Fact
		conflict     bool
		supersedeOld []int64
		supersedeNew *int64
	}{
		{name: "first fact", newFact: fact(2, high, "b")},
		{name: "higher confidence supersedes", current: []version.Fact{fact(1, medium, "a")}, newFact: fact(2, high, "b"), supersedeOld: []int64{1}},
		{name: "equal confidence supersedes", current: []version.Fact{fact(1, medium, "a")}, newFact: fact(2, medium, "b"), supersedeOld: []int64{1}},
		{name: "old unknown confidence supersedes", current: []version.Fact{fact(1, nil, "a")}, newFact: fact(2, low, "b"), supersedeOld: []int64{1}},
		{name: "lower confidence keeps old", current: []version.Fact{fact(1, high, "a")}, newFact: fact(2, low, "b"), supersedeNew: int64Ptr(1)},
		{name: "unknown confidence keeps old", current: []version.Fact{fact(1, high, "a")}, newFact: fact(2, nil, "b"), supersedeNew: int64Ptr(1)},
		{name: "both high confidence conflict", current: []version.Fact{fact(1, high, "a")}, newFact: fact(2, float64Ptr(0.8), "b"), conflict: true},
		{name: "existing conflict stays a conflict", current: []version.Fact{fact(1, high, "a"), fact(2, high, "b")}, newFact: fact(3, low, "c"), conflict: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := versionapp.ResolveFactAppend(tc.current, tc.newFact)
			if decision.Conflict != tc.conflict {
				t.Fatalf("conflict = %v, want %v", decision.Conflict, tc.conflict)
			}
			if len(decision.SupersedeOldIDs) != len(tc.supersedeOld) {
				t.Fatalf("supersede old = %#v, want %#v", decision.SupersedeOldIDs, tc.supersedeOld)
			}
			for index := range tc.supersedeOld {
				if decision.SupersedeOldIDs[index] != tc.supersedeOld[index] {
					t.Fatalf("supersede old = %#v, want %#v", decision.SupersedeOldIDs, tc.supersedeOld)
				}
			}
			switch {
			case tc.supersedeNew == nil && decision.SupersedeNewBy != nil:
				t.Fatalf("supersede new = %d, want nil", *decision.SupersedeNewBy)
			case tc.supersedeNew != nil && (decision.SupersedeNewBy == nil || *decision.SupersedeNewBy != *tc.supersedeNew):
				t.Fatalf("supersede new = %#v, want %d", decision.SupersedeNewBy, *tc.supersedeNew)
			}
		})
	}
}

func TestResolveFactArbitration(t *testing.T) {
	supersededBy := func(id int64) *int64 { return &id }
	row := func(id int64, superseded *int64) version.Fact {
		return version.Fact{ID: id, VersionID: 7, Kind: "decision", Key: "refund", SupersededBy: superseded}
	}

	t.Run("keep wins over one competitor", func(t *testing.T) {
		outcome, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, nil), row(2, nil)}, 2, versionapp.FactArbitrationKeep)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !outcome.Changed || outcome.KeepID != 2 || len(outcome.DroppedIDs) != 1 || outcome.DroppedIDs[0] != 1 {
			t.Fatalf("outcome = %+v", outcome)
		}
		if len(outcome.Facts) != 1 || outcome.Facts[0].ID != 2 {
			t.Fatalf("current = %#v", outcome.Facts)
		}
	})

	t.Run("drop loses to one competitor", func(t *testing.T) {
		outcome, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, nil), row(2, nil)}, 1, versionapp.FactArbitrationDrop)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !outcome.Changed || outcome.KeepID != 2 || len(outcome.DroppedIDs) != 1 || outcome.DroppedIDs[0] != 1 {
			t.Fatalf("outcome = %+v", outcome)
		}
	})

	t.Run("keep wins over several competitors", func(t *testing.T) {
		outcome, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, nil), row(2, nil), row(3, nil)}, 2, versionapp.FactArbitrationKeep)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !outcome.Changed || outcome.KeepID != 2 || len(outcome.DroppedIDs) != 2 {
			t.Fatalf("outcome = %+v", outcome)
		}
	})

	t.Run("repeat keep is idempotent", func(t *testing.T) {
		outcome, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, supersededBy(2)), row(2, nil)}, 2, versionapp.FactArbitrationKeep)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if outcome.Changed || outcome.KeepID != 2 {
			t.Fatalf("outcome = %+v, want an unchanged idempotent keep", outcome)
		}
	})

	t.Run("repeat drop is idempotent", func(t *testing.T) {
		outcome, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, supersededBy(2)), row(2, nil)}, 1, versionapp.FactArbitrationDrop)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if outcome.Changed || outcome.KeepID != 2 {
			t.Fatalf("outcome = %+v, want an unchanged idempotent drop", outcome)
		}
	})

	t.Run("keep on a dropped fact is a state conflict", func(t *testing.T) {
		_, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, supersededBy(2)), row(2, nil)}, 1, versionapp.FactArbitrationKeep)
		if !errors.Is(err, version.ErrFactSuperseded) {
			t.Fatalf("err = %v, want ErrFactSuperseded", err)
		}
	})

	t.Run("single fact is not conflicting", func(t *testing.T) {
		_, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, nil)}, 1, versionapp.FactArbitrationKeep)
		if !errors.Is(err, versionapp.ErrFactNotConflicting) {
			t.Fatalf("err = %v, want ErrFactNotConflicting", err)
		}
	})

	t.Run("drop with several competitors is ambiguous", func(t *testing.T) {
		_, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, nil), row(2, nil), row(3, nil)}, 1, versionapp.FactArbitrationDrop)
		if !errors.Is(err, versionapp.ErrFactArbitrationAmbiguous) {
			t.Fatalf("err = %v, want ErrFactArbitrationAmbiguous", err)
		}
	})

	t.Run("unknown fact", func(t *testing.T) {
		_, err := versionapp.ResolveFactArbitration(
			[]version.Fact{row(1, nil)}, 2, versionapp.FactArbitrationKeep)
		if !errors.Is(err, version.ErrFactNotFound) {
			t.Fatalf("err = %v, want ErrFactNotFound", err)
		}
	})
}
