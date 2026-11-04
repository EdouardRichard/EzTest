package versionapp_test

import (
	"context"
	"strconv"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeFactRepo is an in-memory version.FactRepository with the append-only +
// conflict semantics required by D28. The SQL adapter is covered by the
// integration suite; this fake lets the application contract be tested without
// a database.
type fakeFactRepo struct {
	facts map[int64]version.Fact
	order []int64
}

func newFakeFactRepo() *fakeFactRepo {
	return &fakeFactRepo{facts: map[int64]version.Fact{}}
}

// Record appends a fact unless (version_id, kind, key, content_hash) already
// exists. A different hash under the same key is kept as a second current row
// and reported as a conflict.
func (f *fakeFactRepo) Record(_ context.Context, fact version.Fact) (version.Fact, bool, error) {
	conflict := false
	for _, id := range f.order {
		existing := f.facts[id]
		if existing.VersionID != fact.VersionID || existing.Kind != fact.Kind || existing.Key != fact.Key {
			continue
		}
		if existing.ContentHash == fact.ContentHash {
			return existing, false, nil
		}
		if existing.SupersededBy == nil {
			conflict = true
		}
	}
	f.facts[fact.ID] = fact
	f.order = append(f.order, fact.ID)
	return fact, conflict, nil
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

func (f *fakeFactRepo) count() int { return len(f.facts) }

var _ version.FactRepository = (*fakeFactRepo)(nil)

// TestFactAppendOnlyAndConflict pins D28: facts are append-only; the same
// (version, kind, key, content_hash) is never duplicated; a different payload
// under the same key coexists and reports a conflict; arbitration only marks
// the losing row superseded and Current then returns the kept fact.
func TestFactAppendOnlyAndConflict(t *testing.T) {
	repo := newFakeFactRepo()
	publisher := &fakePublisher{}
	service := versionapp.NewFactService(repo, &seqIDs{}, publisher)
	ctx := context.Background()

	base := version.Fact{
		VersionID:   7,
		Kind:        "page",
		Key:         "checkout",
		Payload:     map[string]any{"url": "/checkout", "title": "结算"},
		Source:      "explorer",
		SourceRef:   "intent-1",
		EvidenceIDs: []int64{11},
	}

	first, conflict, err := service.Record(ctx, base)
	if err != nil {
		t.Fatalf("Record first: %v", err)
	}
	if conflict {
		t.Fatal("first fact reported a conflict")
	}
	if first.ID == 0 || len(first.ContentHash) != 64 {
		t.Fatalf("first fact = %#v, want an assigned ID and SHA-256 content hash", first)
	}

	// Same key, same payload (map keys deliberately reordered): canonical
	// hashing must dedupe instead of appending a second row.
	reordered := base
	reordered.Payload = map[string]any{"title": "结算", "url": "/checkout"}
	again, conflict, err := service.Record(ctx, reordered)
	if err != nil {
		t.Fatalf("Record duplicate: %v", err)
	}
	if conflict {
		t.Fatal("duplicate append reported a conflict")
	}
	if again.ID != first.ID || again.ContentHash != first.ContentHash {
		t.Fatalf("duplicate = %#v, want the existing fact %#v", again, first)
	}
	if repo.count() != 1 {
		t.Fatalf("fact count after duplicate = %d, want 1", repo.count())
	}
	if len(publisher.events) != 1 {
		t.Fatalf("fact.recorded events after duplicate = %d, want 1", len(publisher.events))
	}

	// A different payload under the same key coexists with the current fact and
	// is reported as a conflict; nothing is overwritten.
	conflicting := base
	conflicting.Payload = map[string]any{"url": "/checkout-v2", "title": "结算"}
	second, conflict, err := service.Record(ctx, conflicting)
	if err != nil {
		t.Fatalf("Record conflicting: %v", err)
	}
	if !conflict {
		t.Fatal("different payload under the same key did not report a conflict")
	}
	if second.ID == first.ID || second.ContentHash == first.ContentHash {
		t.Fatalf("conflicting fact = %#v, want a new row", second)
	}
	if repo.count() != 2 {
		t.Fatalf("fact count after conflict = %d, want 2 (coexistence)", repo.count())
	}
	current, err := service.Current(ctx, 7, "page")
	if err != nil {
		t.Fatalf("Current with conflict: %v", err)
	}
	if len(current) != 2 {
		t.Fatalf("current facts = %#v, want the two coexisting rows", current)
	}

	// fact.recorded is published once per append (not for the deduplicated
	// record) and carries the conflict flag.
	if len(publisher.events) != 2 {
		t.Fatalf("fact.recorded events = %d, want 2 (one per append)", len(publisher.events))
	}
	recorded := publisher.events[1]
	if recorded.Type != "fact.recorded" || recorded.Channel != "version:7" {
		t.Fatalf("event = %#v, want fact.recorded on version:7", recorded)
	}
	if recorded.Payload["conflict"] != true {
		t.Fatalf("event conflict = %#v, want true", recorded.Payload["conflict"])
	}
	if recorded.Payload["fact_id"] != strconv.FormatInt(second.ID, 10) {
		t.Fatalf("event fact_id = %#v, want %d", recorded.Payload["fact_id"], second.ID)
	}

	// Arbitration marks the losing row superseded; the row survives (facts are
	// never deleted) and Current only returns the kept fact.
	if err := service.Arbitrate(ctx, second.ID, first.ID, "newer exploration evidence"); err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	current, err = service.Current(ctx, 7, "page")
	if err != nil {
		t.Fatalf("Current after arbitration: %v", err)
	}
	if len(current) != 1 || current[0].ID != second.ID {
		t.Fatalf("current after arbitration = %#v, want only fact %d", current, second.ID)
	}
	if repo.count() != 2 {
		t.Fatalf("arbitration deleted a row: count = %d, want 2", repo.count())
	}
	dropped := repo.facts[first.ID]
	if dropped.SupersededBy == nil || *dropped.SupersededBy != second.ID {
		t.Fatalf("dropped fact superseded_by = %#v, want %d", dropped.SupersededBy, second.ID)
	}
}
