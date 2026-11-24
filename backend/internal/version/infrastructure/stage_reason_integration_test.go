//go:build integration

package versionpg_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

// TestStageReasonSurvivesEventDeletion is the V-0020 guarantee: ApplyStage persists
// the transition reason as a durable stage_transition fact in the same transaction
// as the stage row, and the fact-backed R49 reader still resolves the reason AFTER
// the event log is swept by retention. The old event-backed reader returned "" once
// retention.event_days deleted the version.stage_changed event, silently disabling
// the R49 stage-entry-job repair.
func TestStageReasonSurvivesEventDeletion(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	reader := versionpg.NewStageReasonReader(pool)
	events := eventingpg.New(pool)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := repo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	channel := "version:" + strconv.FormatInt(v.ID, 10)

	// Walk to designing so the final visit is a job-owning transition R49 repairs.
	// Each ApplyStage appends its own stage_transition fact; each step also mirrors
	// the stage event the machine publishes, so the test proves the fact outlives it.
	steps := []struct {
		from, to version.Stage
		reason   string
		at       time.Time
	}{
		{version.StageCreated, version.StageClarifying, "discovery_started", testTime.Add(time.Hour)},
		{version.StageClarifying, version.StageRequirementReview, "clarification_converged", testTime.Add(2 * time.Hour)},
		{version.StageRequirementReview, version.StageDesigning, "requirement_confirmed", testTime.Add(3 * time.Hour)},
	}
	for _, s := range steps {
		if err := repo.ApplyStage(ctx, s.from, version.StageState{
			VersionID: v.ID, Stage: s.to, StageEnteredAt: s.at, Reason: s.reason, Actor: "user",
		}); err != nil {
			t.Fatalf("ApplyStage %s -> %s: %v", s.from, s.to, err)
		}
		if _, err := events.Append(ctx, eventing.Event{
			Channel: channel,
			Type:    "version.stage_changed",
			Payload: map[string]any{
				"from": string(s.from), "to": string(s.to), "reason": s.reason,
				"stage_entered_at": s.at.UTC().Format(time.RFC3339),
			},
		}); err != nil {
			t.Fatalf("append stage event: %v", err)
		}
	}

	designingAt := steps[2].at

	// One current stage_transition fact per visit, keyed by stage_entered_at.
	var factCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM version_fact
		WHERE version_id = $1 AND kind = $2 AND superseded_by IS NULL
	`, v.ID, version.FactKindStageTransition).Scan(&factCount); err != nil {
		t.Fatalf("count stage facts: %v", err)
	}
	if factCount != len(steps) {
		t.Fatalf("stage_transition fact count = %d, want %d", factCount, len(steps))
	}

	// The fact body is {from,to,reason,actor} with a valid content hash.
	var key, hash string
	var payload []byte
	if err := pool.QueryRow(ctx, `
		SELECT key, content_hash, payload FROM version_fact
		WHERE version_id = $1 AND kind = $2 AND key = $3
	`, v.ID, version.FactKindStageTransition, designingAt.UTC().Format(time.RFC3339)).Scan(&key, &hash, &payload); err != nil {
		t.Fatalf("read designing stage fact: %v", err)
	}
	if !version.IsContentHash(hash) {
		t.Fatalf("stage fact content_hash = %q, want a 64-hex SHA-256", hash)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("decode stage fact payload: %v", err)
	}
	if body["from"] != "requirement_review" || body["to"] != "designing" ||
		body["reason"] != "requirement_confirmed" || body["actor"] != "user" {
		t.Fatalf("stage fact payload = %#v", body)
	}

	// The reader resolves the reason, including against the timestamp GetStage
	// returns from the DB (the key match survives the round-trip).
	state, err := repo.GetStage(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetStage: %v", err)
	}
	if got, err := reader.LastStageReason(ctx, v.ID, state.StageEnteredAt); err != nil || got != "requirement_confirmed" {
		t.Fatalf("LastStageReason(GetStage visit) = %q, %v; want requirement_confirmed", got, err)
	}

	// Retention sweeps the entire event log.
	if _, err := pool.Exec(ctx, `DELETE FROM event`); err != nil {
		t.Fatalf("delete events: %v", err)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event`).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("event count after retention = %d, want 0", eventCount)
	}

	// V-0020 core: the reason STILL resolves from the fact after the event is gone.
	got, err := reader.LastStageReason(ctx, v.ID, designingAt)
	if err != nil {
		t.Fatalf("LastStageReason after event deletion: %v", err)
	}
	if got != "requirement_confirmed" {
		t.Fatalf("reason lost after event deletion: %q, want requirement_confirmed", got)
	}

	// An earlier visit keeps its own reason; a different visit or version is unknown.
	if got, err := reader.LastStageReason(ctx, v.ID, steps[0].at); err != nil || got != "discovery_started" {
		t.Fatalf("earlier visit reason = %q, %v; want discovery_started", got, err)
	}
	if got, err := reader.LastStageReason(ctx, v.ID, designingAt.Add(time.Minute)); err != nil || got != "" {
		t.Fatalf("unknown visit reason = %q, %v; want empty", got, err)
	}
	if got, err := reader.LastStageReason(ctx, v.ID+1, designingAt); err != nil || got != "" {
		t.Fatalf("unknown version reason = %q, %v; want empty", got, err)
	}
}

// TestStageReasonReaderDegradesClosed pins the reader's fail-closed contract: a nil
// pool is an error (not a panic), and a non-positive version id is a typed invalid.
func TestStageReasonReaderDegradesClosed(t *testing.T) {
	ctx := context.Background()
	var nilReader *versionpg.StageReasonReader
	if _, err := nilReader.LastStageReason(ctx, 1, testTime); err == nil {
		t.Fatal("nil reader must report an error, not panic")
	}
	empty := versionpg.NewStageReasonReader(nil)
	if _, err := empty.LastStageReason(ctx, 1, testTime); err == nil {
		t.Fatal("nil-pool reader must report an error")
	}
	pool := testutil.NewDB(t)
	reader := versionpg.NewStageReasonReader(pool)
	if _, err := reader.LastStageReason(ctx, 0, testTime); err == nil {
		t.Fatal("non-positive version id must be rejected")
	}
}
