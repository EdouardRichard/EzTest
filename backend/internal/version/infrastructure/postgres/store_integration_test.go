//go:build integration

package versionpg_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newIDs(t *testing.T) platformid.Generator {
	t.Helper()
	generator, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

func createVersion(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO version (id, title, created_by) VALUES ($1, 'integration version', 1)`, id); err != nil {
		t.Fatalf("insert version: %v", err)
	}
}

// TestFactRepositoryAppendOnlyConflictAndArbitration pins the SQL half of D28:
// the unique (version, kind, key, content_hash) index deduplicates, a different
// hash coexists and reports conflict, and arbitration only sets superseded_by.
func TestFactRepositoryAppendOnlyConflictAndArbitration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	repo := versionpg.NewFactRepository(pool, ids)
	ctx := context.Background()

	payload := map[string]any{"url": "/checkout", "title": "结算"}
	contentHash, err := version.FactContentHash(payload)
	if err != nil {
		t.Fatalf("FactContentHash: %v", err)
	}
	confidence := 0.875
	candidate := version.Fact{
		VersionID:   versionID,
		Kind:        "page",
		Key:         "checkout",
		Payload:     payload,
		Confidence:  &confidence,
		Source:      "p9",
		SourceRef:   "intent-1",
		EvidenceIDs: []int64{ids.Next()},
		ContentHash: contentHash,
	}

	first, conflict, err := repo.Record(ctx, candidate)
	if err != nil {
		t.Fatalf("Record first: %v", err)
	}
	if conflict {
		t.Fatal("first fact reported a conflict")
	}
	if first.ID == 0 || first.SupersededBy != nil {
		t.Fatalf("first fact = %#v", first)
	}
	if first.Confidence == nil || *first.Confidence != confidence {
		t.Fatalf("first confidence = %#v, want %v", first.Confidence, confidence)
	}
	if len(first.EvidenceIDs) != 1 || first.EvidenceIDs[0] != candidate.EvidenceIDs[0] {
		t.Fatalf("first evidence refs = %#v, want %v", first.EvidenceIDs, candidate.EvidenceIDs)
	}

	// Same hash: the unique index must not append a second row.
	duplicate := candidate
	duplicate.Payload = map[string]any{"title": "结算", "url": "/checkout"}
	same, conflict, err := repo.Record(ctx, duplicate)
	if err != nil {
		t.Fatalf("Record duplicate: %v", err)
	}
	if conflict || same.ID != first.ID {
		t.Fatalf("duplicate = %#v conflict=%v, want existing %d", same, conflict, first.ID)
	}
	if count := countFacts(t, pool, versionID); count != 1 {
		t.Fatalf("fact rows after duplicate = %d, want 1", count)
	}

	// Different hash: coexist and report the conflict.
	conflictingPayload := map[string]any{"url": "/checkout-v2"}
	conflictingHash, err := version.FactContentHash(conflictingPayload)
	if err != nil {
		t.Fatalf("FactContentHash conflicting: %v", err)
	}
	conflicting := candidate
	conflicting.Payload = conflictingPayload
	conflicting.ContentHash = conflictingHash
	second, conflict, err := repo.Record(ctx, conflicting)
	if err != nil {
		t.Fatalf("Record conflicting: %v", err)
	}
	if !conflict {
		t.Fatal("different hash did not report a conflict")
	}
	if second.ID == first.ID {
		t.Fatalf("conflicting fact reused id %d", first.ID)
	}
	if count := countFacts(t, pool, versionID); count != 2 {
		t.Fatalf("fact rows after conflict = %d, want 2", count)
	}
	current, err := repo.Current(ctx, versionID, "page")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(current) != 2 {
		t.Fatalf("current facts = %#v, want 2 coexisting rows", current)
	}

	if err := repo.Arbitrate(ctx, second.ID, first.ID); err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	current, err = repo.Current(ctx, versionID, "page")
	if err != nil {
		t.Fatalf("Current after arbitration: %v", err)
	}
	if len(current) != 1 || current[0].ID != second.ID {
		t.Fatalf("current after arbitration = %#v, want only %d", current, second.ID)
	}
	if count := countFacts(t, pool, versionID); count != 2 {
		t.Fatalf("arbitration deleted rows: %d, want 2", count)
	}
	var supersededBy *int64
	if err := pool.QueryRow(ctx, `SELECT superseded_by FROM version_fact WHERE id = $1`, first.ID).Scan(&supersededBy); err != nil {
		t.Fatalf("read superseded_by: %v", err)
	}
	if supersededBy == nil || *supersededBy != second.ID {
		t.Fatalf("superseded_by = %#v, want %d", supersededBy, second.ID)
	}

	// The losing row cannot be arbitrated twice.
	if err := repo.Arbitrate(ctx, second.ID, first.ID); !errors.Is(err, version.ErrFactSuperseded) {
		t.Fatalf("second Arbitrate = %v, want ErrFactSuperseded", err)
	}

	// Facts under another key cannot arbitrate each other.
	otherHash, err := version.FactContentHash(map[string]any{"url": "/cart"})
	if err != nil {
		t.Fatalf("FactContentHash other: %v", err)
	}
	other, _, err := repo.Record(ctx, version.Fact{
		VersionID: versionID, Kind: "page", Key: "cart", Payload: map[string]any{"url": "/cart"},
		Source: "p9", ContentHash: otherHash,
	})
	if err != nil {
		t.Fatalf("Record other key: %v", err)
	}
	if err := repo.Arbitrate(ctx, other.ID, first.ID); !errors.Is(err, version.ErrFactMismatch) {
		t.Fatalf("mismatched Arbitrate = %v, want ErrFactMismatch", err)
	}

	// A fact under a missing version violates the same-context FK and is a
	// not-found, not an opaque database error.
	if _, _, err := repo.Record(ctx, version.Fact{
		VersionID: ids.Next(), Kind: "page", Key: "checkout", Payload: map[string]any{"url": "/checkout"},
		Source: "p9", ContentHash: contentHash,
	}); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing version Record = %v, want ErrNotFound", err)
	}
}

// TestFactRepositoryAutoSupersedeConflictAndArbitrate pins the v0.22 SQL
// behavior of R-B6: a not-lower confidence supersedes atomically, a lower
// confidence stays historical under the current row, two high-confidence
// contradictions coexist, and keep/drop arbitration writes only superseded_by
// (idempotently) against a real schema.
func TestFactRepositoryAutoSupersedeConflictAndArbitrate(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	repo := versionpg.NewFactRepository(pool, ids)
	ctx := context.Background()

	record := func(key string, payload map[string]any, confidence *float64) (version.Fact, bool) {
		t.Helper()
		hash, err := version.FactContentHash(payload)
		if err != nil {
			t.Fatalf("FactContentHash: %v", err)
		}
		fact, conflict, err := repo.Record(ctx, version.Fact{
			VersionID: versionID, Kind: "decision", Key: key, Payload: payload,
			Confidence: confidence, Source: "host", ContentHash: hash,
		})
		if err != nil {
			t.Fatalf("Record(%s): %v", key, err)
		}
		return fact, conflict
	}
	confidence := func(value float64) *float64 { return &value }
	snapshot := func(label string) {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT id, confidence, superseded_by FROM version_fact
			WHERE version_id = $1 AND kind = 'decision' AND key = 'refund'
			ORDER BY id
		`, versionID)
		if err != nil {
			t.Fatalf("snapshot %s: %v", label, err)
		}
		defer rows.Close()
		t.Logf("%s:", label)
		for rows.Next() {
			var id int64
			var confidence *float64
			var supersededBy *int64
			if err := rows.Scan(&id, &confidence, &supersededBy); err != nil {
				t.Fatalf("snapshot %s scan: %v", label, err)
			}
			confidenceText, supersededText := "null", "current"
			if confidence != nil {
				confidenceText = fmt.Sprintf("%.3f", *confidence)
			}
			if supersededBy != nil {
				supersededText = fmt.Sprintf("superseded_by=%d", *supersededBy)
			}
			t.Logf("  id=%d confidence=%s %s", id, confidenceText, supersededText)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("snapshot %s: %v", label, err)
		}
	}

	old, conflict := record("refund", map[string]any{"value": "T+1"}, confidence(0.6))
	if conflict {
		t.Fatal("first fact reported a conflict")
	}
	newer, conflict := record("refund", map[string]any{"value": "T+3"}, confidence(0.7))
	if conflict {
		t.Fatal("a not-lower confidence reported a conflict instead of superseding")
	}
	var supersededBy *int64
	if err := pool.QueryRow(ctx, `SELECT superseded_by FROM version_fact WHERE id = $1`, old.ID).Scan(&supersededBy); err != nil {
		t.Fatalf("read superseded_by: %v", err)
	}
	if supersededBy == nil || *supersededBy != newer.ID {
		t.Fatalf("old superseded_by = %#v, want %d", supersededBy, newer.ID)
	}
	current, err := repo.Current(ctx, versionID, "decision")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(current) != 1 || current[0].ID != newer.ID {
		t.Fatalf("current = %#v, want only %d", current, newer.ID)
	}
	snapshot("after not-lower-confidence append (supersede)")

	// C14: re-appending the exact payload of the now-superseded row is an
	// intentional unique-index no-op. The historical row is returned unchanged
	// (still superseded by the current row); the higher replay confidence does
	// not supersede the current row and no new row is inserted.
	replayed, conflict := record("refund", map[string]any{"value": "T+1"}, confidence(0.9))
	if conflict {
		t.Fatal("same-content replay reported a conflict")
	}
	if replayed.ID != old.ID {
		t.Fatalf("replay id = %d, want the historical row %d", replayed.ID, old.ID)
	}
	if replayed.SupersededBy == nil || *replayed.SupersededBy != newer.ID {
		t.Fatalf("replay superseded_by = %#v, want %d (no resurrection)", replayed.SupersededBy, newer.ID)
	}
	if replayed.Confidence == nil || *replayed.Confidence != 0.6 {
		t.Fatalf("replay confidence = %#v, want the historical 0.6 (row unchanged)", replayed.Confidence)
	}
	if count := countFacts(t, pool, versionID); count != 2 {
		t.Fatalf("fact rows after same-content replay = %d, want 2", count)
	}
	if err := pool.QueryRow(ctx, `SELECT superseded_by FROM version_fact WHERE id = $1`, newer.ID).Scan(&supersededBy); err != nil {
		t.Fatalf("read current superseded_by after replay: %v", err)
	}
	if supersededBy != nil {
		t.Fatalf("current row superseded by same-content replay: %#v", supersededBy)
	}
	current, err = repo.Current(ctx, versionID, "decision")
	if err != nil {
		t.Fatalf("Current after replay: %v", err)
	}
	if len(current) != 1 || current[0].ID != newer.ID {
		t.Fatalf("current after replay = %#v, want only %d", current, newer.ID)
	}
	snapshot("after same-content replay of a superseded fact (C14 no-op)")

	// A lower confidence cannot replace the current value; it is stored as
	// history superseded by the winner (no duplicate current row).
	lower, conflict := record("refund", map[string]any{"value": "T+1 old"}, confidence(0.4))
	if conflict {
		t.Fatal("a lower-confidence contradiction reported a conflict")
	}
	if err := pool.QueryRow(ctx, `SELECT superseded_by FROM version_fact WHERE id = $1`, lower.ID).Scan(&supersededBy); err != nil {
		t.Fatalf("read lower superseded_by: %v", err)
	}
	if supersededBy == nil || *supersededBy != newer.ID {
		t.Fatalf("lower superseded_by = %#v, want %d", supersededBy, newer.ID)
	}
	current, err = repo.Current(ctx, versionID, "decision")
	if err != nil {
		t.Fatalf("Current after lower: %v", err)
	}
	if len(current) != 1 || current[0].ID != newer.ID {
		t.Fatalf("current after lower = %#v, want only %d", current, newer.ID)
	}
	snapshot("after lower-confidence append (old wins)")

	// Two high-confidence contradictions coexist and need a host decision.
	high1, conflict := record("refund", map[string]any{"value": "T+5"}, confidence(0.9))
	if conflict {
		t.Fatal("a high-confidence fact over a lower-confidence one reported a conflict")
	}
	high2, conflict := record("refund", map[string]any{"value": "T+7"}, confidence(0.9))
	if !conflict {
		t.Fatal("two high-confidence contradictions did not report a conflict")
	}
	current, err = repo.Current(ctx, versionID, "decision")
	if err != nil {
		t.Fatalf("Current at conflict: %v", err)
	}
	if len(current) != 2 {
		t.Fatalf("current at conflict = %#v, want both high-confidence rows", current)
	}
	snapshot("high-confidence conflict before arbitration")
	historyRows := countFacts(t, pool, versionID)

	kept, err := repo.ArbitrateFact(ctx, versionID, high2.ID, versionapp.FactArbitrationKeep)
	if err != nil {
		t.Fatalf("ArbitrateFact keep: %v", err)
	}
	if !kept.Changed || kept.KeepID != high2.ID || len(kept.DroppedIDs) != 1 || kept.DroppedIDs[0] != high1.ID {
		t.Fatalf("keep outcome = %+v", kept)
	}
	if err := pool.QueryRow(ctx, `SELECT superseded_by FROM version_fact WHERE id = $1`, high1.ID).Scan(&supersededBy); err != nil {
		t.Fatalf("read kept superseded_by: %v", err)
	}
	if supersededBy == nil || *supersededBy != high2.ID {
		t.Fatalf("kept superseded_by = %#v, want %d", supersededBy, high2.ID)
	}
	repeated, err := repo.ArbitrateFact(ctx, versionID, high2.ID, versionapp.FactArbitrationKeep)
	if err != nil {
		t.Fatalf("repeated ArbitrateFact keep: %v", err)
	}
	if repeated.Changed {
		t.Fatalf("repeated keep changed the store again: %+v", repeated)
	}
	if countFacts(t, pool, versionID) != historyRows {
		t.Fatal("arbitration deleted or added history rows")
	}
	snapshot("after keep arbitration (idempotent repeat applied)")

	// Drop the addressed fact in favour of the single competing current row.
	high3, conflict := record("refund", map[string]any{"value": "T+9"}, confidence(0.9))
	if !conflict {
		t.Fatal("a third high-confidence contradiction did not report a conflict")
	}
	dropped, err := repo.ArbitrateFact(ctx, versionID, high3.ID, versionapp.FactArbitrationDrop)
	if err != nil {
		t.Fatalf("ArbitrateFact drop: %v", err)
	}
	if !dropped.Changed || dropped.KeepID != high2.ID || len(dropped.DroppedIDs) != 1 || dropped.DroppedIDs[0] != high3.ID {
		t.Fatalf("drop outcome = %+v", dropped)
	}
	current, err = repo.Current(ctx, versionID, "decision")
	if err != nil {
		t.Fatalf("Current after drop: %v", err)
	}
	if len(current) != 1 || current[0].ID != high2.ID {
		t.Fatalf("current after drop = %#v, want only %d", current, high2.ID)
	}
	snapshot("after drop arbitration")

	// A keep on an already-dropped fact contradicts the stored decision.
	if _, err := repo.ArbitrateFact(ctx, versionID, high3.ID, versionapp.FactArbitrationKeep); !errors.Is(err, version.ErrFactSuperseded) {
		t.Fatalf("keep after drop = %v, want ErrFactSuperseded", err)
	}

	// A single fact has no conflict to arbitrate.
	solo, _ := record("solo", map[string]any{"value": "only"}, confidence(0.9))
	if _, err := repo.ArbitrateFact(ctx, versionID, solo.ID, versionapp.FactArbitrationKeep); !errors.Is(err, versionapp.ErrFactNotConflicting) {
		t.Fatalf("solo keep = %v, want ErrFactNotConflicting", err)
	}

	// Three current rows make a bare drop ambiguous: keep the winner instead.
	for _, value := range []string{"A", "B", "C"} {
		record("multi", map[string]any{"value": value}, confidence(0.9))
	}
	multiRows := countFacts(t, pool, versionID)
	current, err = repo.Current(ctx, versionID, "decision")
	if err != nil {
		t.Fatalf("Current multi: %v", err)
	}
	var multiID int64
	for _, fact := range current {
		if fact.Key == "multi" {
			multiID = fact.ID
			break
		}
	}
	if _, err := repo.ArbitrateFact(ctx, versionID, multiID, versionapp.FactArbitrationDrop); !errors.Is(err, versionapp.ErrFactArbitrationAmbiguous) {
		t.Fatalf("ambiguous drop = %v, want ErrFactArbitrationAmbiguous", err)
	}
	if countFacts(t, pool, versionID) != multiRows {
		t.Fatal("failed arbitration wrote history")
	}

	// Cross-version and unknown facts are not addressable through this version.
	otherVersion := ids.Next()
	createVersion(t, pool, otherVersion)
	if _, err := repo.ArbitrateFact(ctx, otherVersion, high2.ID, versionapp.FactArbitrationKeep); !errors.Is(err, version.ErrFactNotFound) {
		t.Fatalf("cross-version arbitration = %v, want ErrFactNotFound", err)
	}
	if _, err := repo.ArbitrateFact(ctx, versionID, ids.Next(), versionapp.FactArbitrationKeep); !errors.Is(err, version.ErrFactNotFound) {
		t.Fatalf("unknown fact arbitration = %v, want ErrFactNotFound", err)
	}
}

func countFacts(t *testing.T, pool *pgxpool.Pool, versionID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM version_fact WHERE version_id = $1`, versionID).Scan(&count); err != nil {
		t.Fatalf("count facts: %v", err)
	}
	return count
}

// TestPlanRepositoryUpsertListAndDependencies pins the plan store contract:
// create/update with a changed flag, (sort_order, id) listing, status filter,
// dependency existence, append-style no-op updates and the revision column
// seeded at 1 and bumped by plain updates.
func TestPlanRepositoryUpsertListAndDependencies(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	repo := versionpg.NewPlanRepository(pool)
	ctx := context.Background()

	first, changed, err := repo.Upsert(ctx, version.PlanItem{
		ID: ids.Next(), VersionID: versionID, Phase: "execute", Title: "run cases",
		Status: version.PlanItemStatusPending, Order: 2,
	})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	if !changed || first.Status != version.PlanItemStatusPending {
		t.Fatalf("created plan item = %#v changed=%v", first, changed)
	}
	if first.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", first.Revision)
	}

	if _, changed, err := repo.Upsert(ctx, first); err != nil || changed {
		t.Fatalf("unchanged Upsert = changed %v, err %v; want no change", changed, err)
	}

	first.Status = "done"
	updated, changed, err := repo.Upsert(ctx, first)
	if err != nil {
		t.Fatalf("Upsert status: %v", err)
	}
	if !changed || updated.Status != "done" {
		t.Fatalf("status update = %#v changed=%v", updated, changed)
	}
	if updated.Revision != 2 {
		t.Fatalf("status update revision = %d, want 2 (plain Upsert bumps too)", updated.Revision)
	}

	dependent, changed, err := repo.Upsert(ctx, version.PlanItem{
		ID: ids.Next(), VersionID: versionID, Phase: "execute", Title: "fix failures",
		Status: version.PlanItemStatusPending, Order: 1, DependsOn: []int64{first.ID},
	})
	if err != nil {
		t.Fatalf("Upsert dependent: %v", err)
	}
	if !changed || len(dependent.DependsOn) != 1 || dependent.DependsOn[0] != first.ID {
		t.Fatalf("dependent = %#v changed=%v", dependent, changed)
	}

	items, err := repo.List(ctx, versionID, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || items[0].ID != dependent.ID || items[1].ID != first.ID {
		t.Fatalf("items = %#v, want (sort_order, id) order", items)
	}
	if items[0].Revision != 1 || items[1].Revision != 2 {
		t.Fatalf("listed revisions = %d/%d, want 1/2", items[0].Revision, items[1].Revision)
	}
	done, err := repo.List(ctx, versionID, "done")
	if err != nil {
		t.Fatalf("List done: %v", err)
	}
	if len(done) != 1 || done[0].ID != first.ID {
		t.Fatalf("done items = %#v, want only %d", done, first.ID)
	}

	// A depends_on entry that is not a plan item of this version is rejected.
	if _, _, err := repo.Upsert(ctx, version.PlanItem{
		ID: ids.Next(), VersionID: versionID, Phase: "execute", Title: "bad dependency",
		Status: version.PlanItemStatusPending, DependsOn: []int64{ids.Next()},
	}); !errors.Is(err, version.ErrPlanDependencyNotFound) {
		t.Fatalf("missing dependency = %v, want ErrPlanDependencyNotFound", err)
	}

	// The same primary key cannot be moved to another version.
	otherVersionID := ids.Next()
	createVersion(t, pool, otherVersionID)
	if _, _, err := repo.Upsert(ctx, version.PlanItem{
		ID: first.ID, VersionID: otherVersionID, Phase: "execute", Title: "moved",
		Status: version.PlanItemStatusPending,
	}); !errors.Is(err, version.ErrPlanNotFound) {
		t.Fatalf("cross-version upsert = %v, want ErrPlanNotFound", err)
	}

	// A missing version is a not-found, not a dependency error.
	if _, _, err := repo.Upsert(ctx, version.PlanItem{
		ID: ids.Next(), VersionID: ids.Next(), Phase: "execute", Title: "orphan",
		Status: version.PlanItemStatusPending,
	}); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing version = %v, want ErrNotFound", err)
	}
}

// TestPlanRepositoryUpdateOptimisticLock pins the SQL half of R-A8-2: Update
// writes only when id/version_id match and the stored revision equals the
// caller's expected revision, bumps revision by one on a real change, reports a
// no-op patch as unchanged, and returns a typed conflict carrying the current
// revision when the caller is stale.
func TestPlanRepositoryUpdateOptimisticLock(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	versionID := ids.Next()
	createVersion(t, pool, versionID)
	repo := versionpg.NewPlanRepository(pool)
	ctx := context.Background()

	itemID := ids.Next()
	created, changed, err := repo.Upsert(ctx, version.PlanItem{
		ID: itemID, VersionID: versionID, Phase: "execute", Title: "run cases",
		Status: version.PlanItemStatusPending, Order: 1,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !changed || created.Revision != 1 {
		t.Fatalf("created = %+v changed=%v, want revision 1", created, changed)
	}

	order := 4
	updated, changed, err := repo.Update(ctx, versionID, itemID, version.PlanPatch{
		Status: "done", Decision: "ok", Order: &order,
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !changed {
		t.Fatal("Update reported no change for a real write")
	}
	if updated.Status != "done" || updated.Decision != "ok" || updated.Order != 4 || updated.Revision != 2 {
		t.Fatalf("updated = %+v, want status=done decision=ok order=4 revision=2", updated)
	}

	// Empty patch fields keep the stored values and a no-op patch must not bump
	// the revision again.
	noop, changed, err := repo.Update(ctx, versionID, itemID, version.PlanPatch{Title: "", Status: ""}, 2)
	if err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	if changed {
		t.Fatalf("no-op Update changed = true, want false: %+v", noop)
	}
	if noop.Status != "done" || noop.Decision != "ok" || noop.Title != "run cases" || noop.Revision != 2 {
		t.Fatalf("no-op Update = %+v, want stored row with revision 2", noop)
	}

	// A stale expected revision is a typed conflict that reports the stored
	// revision; nothing is written.
	_, changed, err = repo.Update(ctx, versionID, itemID, version.PlanPatch{Status: "pending"}, 1)
	if changed || !errors.Is(err, version.ErrPlanRevisionConflict) {
		t.Fatalf("stale Update changed=%v err=%v, want ErrPlanRevisionConflict", changed, err)
	}
	var conflict *version.PlanRevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale Update err = %v, want *version.PlanRevisionConflictError", err)
	}
	if conflict.ItemID != itemID || conflict.Expected != 1 || conflict.Actual != 2 {
		t.Fatalf("conflict = %+v, want item=%d expected=1 actual=2", conflict, itemID)
	}

	// A missing item, or an item owned by another version, is a not-found even
	// when the revision matches.
	if _, _, err := repo.Update(ctx, versionID, ids.Next(), version.PlanPatch{Status: "done"}, 2); !errors.Is(err, version.ErrPlanNotFound) {
		t.Fatalf("missing item Update = %v, want ErrPlanNotFound", err)
	}
	otherVersionID := ids.Next()
	createVersion(t, pool, otherVersionID)
	if _, _, err := repo.Update(ctx, otherVersionID, itemID, version.PlanPatch{Status: "done"}, 2); !errors.Is(err, version.ErrPlanNotFound) {
		t.Fatalf("cross-version Update = %v, want ErrPlanNotFound", err)
	}

	// The stored row still carries the original values after the rejected
	// writes.
	items, err := repo.List(ctx, versionID, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Status != "done" || items[0].Decision != "ok" ||
		items[0].Order != 4 || items[0].Revision != 2 {
		t.Fatalf("stored after rejected writes = %+v", items)
	}
}
