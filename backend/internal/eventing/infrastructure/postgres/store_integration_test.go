//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	eventpostgres "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

var ctx = context.Background()

// TestPublishMonotonicAndReplay is the task's concurrency acceptance test: 20
// concurrent publishers on one channel receive a gap-free seq sequence, and the
// replay returns them in ascending seq order.
func TestPublishMonotonicAndReplay(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	store := eventpostgres.NewEventStore(pool)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Publish(ctx, eventing.NewEvent{Channel: "system", Type: "system.test", Summary: fmt.Sprint(i), TS: time.Now().UTC()})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	all, err := store.ListAfter(ctx, "system", 0, 100)
	if err != nil || len(all) != 20 {
		t.Fatalf("len=%d err=%v", len(all), err)
	}
	for i, e := range all {
		if e.Seq != int64(i+1) {
			t.Fatalf("seq[%d]=%d", i, e.Seq)
		}
	}
}

// TestPublishPersistsEventEnvelope proves every V1 envelope column round-trips:
// the optional identifiers stay null when absent, detail is JSONB and refs keep
// all four arrays.
func TestPublishPersistsEventEnvelope(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	store := eventpostgres.NewEventStore(pool)

	ts := time.Date(2026, 10, 27, 2, 30, 0, 0, time.UTC)
	versionID := "version-1"
	batchID := "batch-1"
	caseID := "case-1"
	stepID := "step-1"
	agentRole := "planner"
	sessionID := "session-1"
	refs := eventing.Refs{
		Evidence:    []string{"evidence/one"},
		Case:        []string{"case/one"},
		Requirement: []string{"requirement/one"},
		Code:        []string{"backend/internal/eventing/domain/event.go"},
	}
	published, err := store.Publish(ctx, eventing.NewEvent{
		Channel:   "system",
		Type:      "system.test",
		VersionID: &versionID,
		BatchID:   &batchID,
		CaseID:    &caseID,
		StepID:    &stepID,
		AgentRole: &agentRole,
		SessionID: &sessionID,
		Summary:   "admin test event",
		Detail:    json.RawMessage(`{"index":1,"nested":{"ok":true}}`),
		Refs:      refs,
		TS:        ts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.Seq != 1 || published.Channel != "system" || published.Type != "system.test" {
		t.Fatalf("published=%+v", published)
	}

	events, err := store.ListAfter(ctx, "system", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	got := events[0]
	assertStringPtr(t, "version_id", got.VersionID, versionID)
	assertStringPtr(t, "batch_id", got.BatchID, batchID)
	assertStringPtr(t, "case_id", got.CaseID, caseID)
	assertStringPtr(t, "step_id", got.StepID, stepID)
	assertStringPtr(t, "agent_role", got.AgentRole, agentRole)
	assertStringPtr(t, "session_id", got.SessionID, sessionID)
	if got.Summary != "admin test event" {
		t.Fatalf("summary=%q", got.Summary)
	}
	if !got.TS.Equal(ts) {
		t.Fatalf("ts=%v want %v", got.TS, ts)
	}
	assertJSONEqual(t, "detail", got.Detail, json.RawMessage(`{"index":1,"nested":{"ok":true}}`))
	assertJSONEqual(t, "refs", mustJSON(t, got.Refs), mustJSON(t, refs))

	// A second event without optional fields must persist nulls and an empty
	// detail object rather than failing.
	second, err := store.Publish(ctx, eventing.NewEvent{Channel: "system", Type: "system.test", TS: ts})
	if err != nil {
		t.Fatal(err)
	}
	events, err = store.ListAfter(ctx, "system", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != 2 || events[0].Seq != second.Seq {
		t.Fatalf("second events=%+v seq=%d", events, second.Seq)
	}
	if events[0].VersionID != nil || events[0].BatchID != nil || events[0].CaseID != nil ||
		events[0].StepID != nil || events[0].AgentRole != nil || events[0].SessionID != nil {
		t.Fatalf("optional identifiers must be nil: %+v", events[0])
	}
	assertJSONEqual(t, "empty detail", events[0].Detail, json.RawMessage(`{}`))
	for name, values := range map[string][]string{
		"evidence":    events[0].Refs.Evidence,
		"case":        events[0].Refs.Case,
		"requirement": events[0].Refs.Requirement,
		"code":        events[0].Refs.Code,
	} {
		if len(values) != 0 {
			t.Fatalf("refs.%s=%v want empty", name, values)
		}
	}
}

// TestPublishStoresLowercaseRefsJSON pins the JSONB key casing: SQL consumers
// read refs->>'evidence', so the persisted object must use the envelope keys,
// not the Go field names.
func TestPublishStoresLowercaseRefsJSON(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	store := eventpostgres.NewEventStore(pool)

	published, err := store.Publish(ctx, eventing.NewEvent{
		Channel: "system",
		Type:    "system.test",
		Refs: eventing.Refs{
			Evidence:    []string{"evidence/one"},
			Case:        []string{"case/one"},
			Requirement: []string{"requirement/one"},
			Code:        []string{"code/one"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var evidence, caseRef, requirement, code *string
	if err := pool.QueryRow(ctx, `
		SELECT refs->>'evidence', refs->>'case', refs->>'requirement', refs->>'code'
		FROM event WHERE channel = $1 AND seq = $2`,
		published.Channel, published.Seq,
	).Scan(&evidence, &caseRef, &requirement, &code); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		key  string
		got  *string
		want string
	}{
		{"evidence", evidence, `["evidence/one"]`},
		{"case", caseRef, `["case/one"]`},
		{"requirement", requirement, `["requirement/one"]`},
		{"code", code, `["code/one"]`},
	} {
		if check.got == nil {
			t.Fatalf("refs->>'%s' is NULL: JSONB keys must be lowercase", check.key)
		}
		if *check.got != check.want {
			t.Fatalf("refs->>'%s'=%q want %q", check.key, *check.got, check.want)
		}
	}

	var oldEvidence, oldCase, oldRequirement, oldCode *string
	if err := pool.QueryRow(ctx, `
		SELECT refs->>'Evidence', refs->>'Case', refs->>'Requirement', refs->>'Code'
		FROM event WHERE channel = $1 AND seq = $2`,
		published.Channel, published.Seq,
	).Scan(&oldEvidence, &oldCase, &oldRequirement, &oldCode); err != nil {
		t.Fatal(err)
	}
	if oldEvidence != nil || oldCase != nil || oldRequirement != nil || oldCode != nil {
		t.Fatalf("refs JSONB still uses Go field names: %v/%v/%v/%v", oldEvidence, oldCase, oldRequirement, oldCode)
	}
}

// TestPublishRejectsInvalidEvent proves the store enforces the §7.2 envelope
// limits itself, as defence in depth: an invalid event allocates no seq and
// inserts no row.
func TestPublishRejectsInvalidEvent(t *testing.T) {
	tests := []struct {
		name  string
		event eventing.NewEvent
	}{
		{
			name: "summary over 500 runes",
			event: eventing.NewEvent{
				Channel: "system", Type: "system.test", Summary: strings.Repeat("通", 501),
			},
		},
		{
			name: "detail not a JSON object",
			event: eventing.NewEvent{
				Channel: "system", Type: "system.test", Detail: json.RawMessage(`[1,2,3]`),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := testutil.OpenTestDB(t)
			store := eventpostgres.NewEventStore(pool)

			if _, err := store.Publish(ctx, tc.event); !errors.Is(err, eventing.ErrInvalidEvent) {
				t.Fatalf("Publish error=%v, want ErrInvalidEvent", err)
			}
			var events int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM event`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if events != 0 {
				t.Fatalf("invalid event was persisted: %d rows", events)
			}
			if _, err := store.LatestSeq(ctx, tc.event.Channel); !errors.Is(err, eventing.ErrChannelNotFound) {
				t.Fatalf("LatestSeq err=%v, want ErrChannelNotFound", err)
			}
		})
	}
}

// TestListBeforePagination pins the snapshot query contract: the latest N events
// below before_seq, returned ascending, with has_more reporting an older page.
func TestListBeforePagination(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	store := eventpostgres.NewEventStore(pool)
	publishN(t, store, "system", 5)

	tests := []struct {
		name      string
		beforeSeq int64
		limit     int
		wantSeqs  []int64
		wantMore  bool
	}{
		{name: "latest page", beforeSeq: 0, limit: 2, wantSeqs: []int64{4, 5}, wantMore: true},
		{name: "before cursor", beforeSeq: 5, limit: 2, wantSeqs: []int64{3, 4}, wantMore: true},
		{name: "older than oldest", beforeSeq: 2, limit: 5, wantSeqs: []int64{1}, wantMore: false},
		{name: "no events before cursor", beforeSeq: 1, limit: 5, wantSeqs: nil, wantMore: false},
		{name: "all events", beforeSeq: 0, limit: 100, wantSeqs: []int64{1, 2, 3, 4, 5}, wantMore: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events, more, err := store.ListBefore(ctx, "system", tc.beforeSeq, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if more != tc.wantMore {
				t.Fatalf("has_more=%v want %v", more, tc.wantMore)
			}
			gotSeqs := make([]int64, len(events))
			for i, event := range events {
				gotSeqs[i] = event.Seq
			}
			if fmt.Sprint(gotSeqs) != fmt.Sprint(tc.wantSeqs) {
				t.Fatalf("seqs=%v want %v", gotSeqs, tc.wantSeqs)
			}
		})
	}
}

// TestLatestSeqAndChannels proves seq counters are per channel and that an
// unknown channel is reported distinctly so the HTTP layer can answer 404.
func TestLatestSeqAndChannels(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	store := eventpostgres.NewEventStore(pool)
	publishN(t, store, "system", 2)
	publishN(t, store, "version:1", 1)

	if seq, err := store.LatestSeq(ctx, "system"); err != nil || seq != 2 {
		t.Fatalf("system latest=%d err=%v", seq, err)
	}
	if seq, err := store.LatestSeq(ctx, "version:1"); err != nil || seq != 1 {
		t.Fatalf("version:1 latest=%d err=%v", seq, err)
	}
	if _, err := store.LatestSeq(ctx, "unknown"); !errors.Is(err, eventing.ErrChannelNotFound) {
		t.Fatalf("unknown channel err=%v want ErrChannelNotFound", err)
	}
}

// TestListAfterCursorTooOldAfterRetention covers the reset trigger: when the
// oldest events are gone (retention or a rebuilt channel), a cursor that no
// longer joins the retained sequence must be reported as too old instead of
// silently skipping events.
func TestListAfterCursorTooOldAfterRetention(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	store := eventpostgres.NewEventStore(pool)
	publishN(t, store, "system", 5)

	if _, err := pool.Exec(ctx, `DELETE FROM event WHERE channel = $1 AND seq <= 2`, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListAfter(ctx, "system", 0, 100); !errors.Is(err, eventing.ErrCursorTooOld) {
		t.Fatalf("err=%v want ErrCursorTooOld", err)
	}
	events, err := store.ListAfter(ctx, "system", 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Seq != 3 {
		t.Fatalf("events=%+v want seqs 3..5", events)
	}
}

// TestListAfterCursorTooOldAfterRebuild covers the other reset trigger: a
// channel whose seq counter was rebuilt restarts at 1, so a cursor from the
// previous incarnation is ahead of the current latest and cannot be replayed.
func TestListAfterCursorTooOldAfterRebuild(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	store := eventpostgres.NewEventStore(pool)
	publishN(t, store, "system", 2)

	if _, err := pool.Exec(ctx, `TRUNCATE TABLE event`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE event_channel SET last_seq = 0 WHERE channel = $1`, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(ctx, eventing.NewEvent{Channel: "system", Type: "system.test", TS: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ListAfter(ctx, "system", 2, 100); !errors.Is(err, eventing.ErrCursorTooOld) {
		t.Fatalf("err=%v want ErrCursorTooOld", err)
	}
	events, err := store.ListAfter(ctx, "system", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("events=%+v want the rebuilt seq 1", events)
	}
}

// TestListAfterLimit proves the replay query is bounded and ordered.
func TestListAfterLimit(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	store := eventpostgres.NewEventStore(pool)
	publishN(t, store, "system", 5)

	events, err := store.ListAfter(ctx, "system", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Seq != 2 || events[1].Seq != 3 {
		t.Fatalf("events=%+v want seqs 2,3", events)
	}
}

// publishN publishes n test events on channel and fails the test on error.
func publishN(t *testing.T, store *eventpostgres.EventStore, channel string, n int) []eventing.Event {
	t.Helper()
	events := make([]eventing.Event, 0, n)
	for i := 0; i < n; i++ {
		event, err := store.Publish(ctx, eventing.NewEvent{
			Channel: channel,
			Type:    "system.test",
			Summary: fmt.Sprintf("%s-%d", channel, i+1),
			TS:      time.Date(2026, 10, 27, 2, 30, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("publish %d on %q: %v", i+1, channel, err)
		}
		events = append(events, event)
	}
	return events
}

func assertStringPtr(t *testing.T, name string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s=%v want %q", name, got, want)
	}
}

func assertJSONEqual(t *testing.T, name string, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode %s %q: %v", name, got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want %s %q: %v", name, want, err)
	}
	if fmt.Sprint(gotValue) != fmt.Sprint(wantValue) {
		t.Fatalf("%s=%s want %s", name, got, want)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
