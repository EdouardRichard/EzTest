// Package postgres implements the eventing store port with PostgreSQL. Events
// and their per-channel seq counters are persisted in `events` and
// `event_channels`; the seq assignment and the event insert share one
// transaction so a channel can never expose a gap.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// eventColumns is the column list shared by every event read and the order
// scanEvent expects. The BIGSERIAL id is an internal row identifier only; the
// public identity of an event is (channel, seq).
const eventColumns = `channel, seq, type, version_id, batch_id, case_id, step_id, agent_role, session_id, ts, summary, detail, refs`

const (
	// allocateSeqSQL atomically reserves the next channel-local seq. The first
	// event of a channel creates the counter row at 1.
	allocateSeqSQL = `INSERT INTO event_channels (channel, last_seq, updated_at)
		VALUES ($1, 1, now())
		ON CONFLICT (channel) DO UPDATE
		SET last_seq = event_channels.last_seq + 1, updated_at = now()
		RETURNING last_seq`
	insertEventSQL = `INSERT INTO events
		(channel, seq, type, version_id, batch_id, case_id, step_id, agent_role, session_id, ts, summary, detail, refs)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb)`
)

// EventStore implements eventing.Store on a pgx pool.
type EventStore struct {
	pool *pgxpool.Pool
}

// NewEventStore returns an event store backed by pool.
func NewEventStore(pool *pgxpool.Pool) *EventStore {
	return &EventStore{pool: pool}
}

// rowScanner is the subset of pgx.Row and pgx.Rows used to scan one event.
type rowScanner interface {
	Scan(dest ...any) error
}

// Publish assigns the next channel-local seq and persists the event in one
// transaction. Callers broadcast only after Publish returns, so subscribers
// never observe an event that failed to commit.
func (s *EventStore) Publish(ctx context.Context, newEvent eventing.NewEvent) (eventing.Event, error) {
	// The application layer validates before calling, but the store is defence
	// in depth: a future caller must not be able to persist an event that
	// violates the frozen envelope contract.
	if err := eventing.Validate(newEvent); err != nil {
		return eventing.Event{}, err
	}

	ts := newEvent.TS.UTC()
	if newEvent.TS.IsZero() {
		ts = time.Now().UTC()
	}
	// PostgreSQL timestamptz keeps microseconds; truncating keeps the returned
	// event byte-identical to what a later read returns.
	ts = ts.Truncate(time.Microsecond)

	detail := newEvent.Detail
	if len(bytes.TrimSpace(detail)) == 0 {
		detail = json.RawMessage(`{}`)
	}
	refs := newEvent.Refs
	if refs.Evidence == nil {
		refs.Evidence = []string{}
	}
	if refs.Case == nil {
		refs.Case = []string{}
	}
	if refs.Requirement == nil {
		refs.Requirement = []string{}
	}
	if refs.Code == nil {
		refs.Code = []string{}
	}
	refsJSON, err := json.Marshal(refs)
	if err != nil {
		return eventing.Event{}, fmt.Errorf("postgres: encode event refs: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return eventing.Event{}, fmt.Errorf("postgres: begin publish event: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var seq int64
	if err := tx.QueryRow(ctx, allocateSeqSQL, newEvent.Channel).Scan(&seq); err != nil {
		return eventing.Event{}, fmt.Errorf("postgres: allocate event seq: %w", err)
	}
	if _, err := tx.Exec(ctx, insertEventSQL,
		newEvent.Channel,
		seq,
		newEvent.Type,
		newEvent.VersionID,
		newEvent.BatchID,
		newEvent.CaseID,
		newEvent.StepID,
		newEvent.AgentRole,
		newEvent.SessionID,
		ts,
		newEvent.Summary,
		string(detail),
		string(refsJSON),
	); err != nil {
		return eventing.Event{}, fmt.Errorf("postgres: insert event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return eventing.Event{}, fmt.Errorf("postgres: commit event: %w", err)
	}

	return eventing.Event{
		Seq:       seq,
		Channel:   newEvent.Channel,
		Type:      newEvent.Type,
		VersionID: newEvent.VersionID,
		BatchID:   newEvent.BatchID,
		CaseID:    newEvent.CaseID,
		StepID:    newEvent.StepID,
		AgentRole: newEvent.AgentRole,
		SessionID: newEvent.SessionID,
		TS:        ts,
		Summary:   newEvent.Summary,
		Detail:    json.RawMessage(detail),
		Refs:      refs,
	}, nil
}

// ListAfter returns up to limit events with seq > afterSeq in ascending order.
// A cursor that no longer joins the retained sequence returns ErrCursorTooOld;
// this covers both trimmed history and a rebuilt channel whose counter restarted
// below the cursor.
func (s *EventStore) ListAfter(ctx context.Context, channel string, afterSeq int64, limit int) ([]eventing.Event, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("postgres: list events after: limit must be positive")
	}
	if err := s.checkReplayCursor(ctx, channel, afterSeq); err != nil {
		return nil, err
	}
	const query = `SELECT ` + eventColumns + `
		FROM events
		WHERE channel = $1 AND seq > $2
		ORDER BY seq ASC
		LIMIT $3`
	events, err := s.queryEvents(ctx, query, channel, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list events after: %w", err)
	}
	return events, nil
}

// ListBefore returns up to limit of the newest events with seq < beforeSeq, in
// ascending order. beforeSeq <= 0 means "from the latest event". more reports
// whether an older page exists.
func (s *EventStore) ListBefore(ctx context.Context, channel string, beforeSeq int64, limit int) ([]eventing.Event, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list events before: limit must be positive")
	}
	var (
		query string
		args  []any
	)
	if beforeSeq > 0 {
		query = `SELECT ` + eventColumns + `
			FROM events
			WHERE channel = $1 AND seq < $2
			ORDER BY seq DESC
			LIMIT $3`
		args = []any{channel, beforeSeq, limit + 1}
	} else {
		query = `SELECT ` + eventColumns + `
			FROM events
			WHERE channel = $1
			ORDER BY seq DESC
			LIMIT $2`
		args = []any{channel, limit + 1}
	}
	events, err := s.queryEvents(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list events before: %w", err)
	}
	more := len(events) > limit
	if more {
		events = events[:limit]
	}
	slices.Reverse(events)
	return events, more, nil
}

// LatestSeq returns the channel's current seq counter. An unknown channel is
// reported as ErrChannelNotFound so the snapshot endpoint can answer 404.
func (s *EventStore) LatestSeq(ctx context.Context, channel string) (int64, error) {
	const query = `SELECT last_seq FROM event_channels WHERE channel = $1`
	var seq int64
	if err := s.pool.QueryRow(ctx, query, channel).Scan(&seq); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, eventing.ErrChannelNotFound
		}
		return 0, fmt.Errorf("postgres: latest event seq: %w", err)
	}
	return seq, nil
}

// checkReplayCursor rejects a replay cursor that leaves a gap: either the
// channel counter is behind the cursor (the channel was rebuilt and restarted)
// or events older than min(seq) were removed.
func (s *EventStore) checkReplayCursor(ctx context.Context, channel string, afterSeq int64) error {
	var lastSeq int64
	if err := s.pool.QueryRow(ctx,
		`SELECT last_seq FROM event_channels WHERE channel = $1`, channel).Scan(&lastSeq); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No such channel: a fresh stream (cursor 0) simply has no history;
			// a non-zero cursor came from a channel that no longer exists.
			if afterSeq > 0 {
				return eventing.ErrCursorTooOld
			}
			return nil
		}
		return fmt.Errorf("postgres: read event channel: %w", err)
	}
	if afterSeq > lastSeq {
		return eventing.ErrCursorTooOld
	}

	var firstSeq *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT MIN(seq) FROM events WHERE channel = $1`, channel).Scan(&firstSeq); err != nil {
		return fmt.Errorf("postgres: read first event seq: %w", err)
	}
	if firstSeq != nil && afterSeq < *firstSeq-1 {
		return eventing.ErrCursorTooOld
	}
	return nil
}

// queryEvents runs query and scans every row in seq order. Callers may request
// limit+1 rows to detect whether a further page exists.
func (s *EventStore) queryEvents(ctx context.Context, query string, args ...any) ([]eventing.Event, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]eventing.Event, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// scanEvent scans one row in eventColumns order. JSONB is decoded into a
// non-nil detail object and refs with the four arrays always present.
func scanEvent(row rowScanner) (eventing.Event, error) {
	var (
		event  eventing.Event
		detail []byte
		refs   []byte
	)
	if err := row.Scan(
		&event.Channel,
		&event.Seq,
		&event.Type,
		&event.VersionID,
		&event.BatchID,
		&event.CaseID,
		&event.StepID,
		&event.AgentRole,
		&event.SessionID,
		&event.TS,
		&event.Summary,
		&detail,
		&refs,
	); err != nil {
		return eventing.Event{}, err
	}
	event.TS = event.TS.UTC()
	event.Detail = json.RawMessage(`{}`)
	if len(bytes.TrimSpace(detail)) > 0 {
		event.Detail = append(json.RawMessage(nil), detail...)
	}
	event.Refs = eventing.Refs{Evidence: []string{}, Case: []string{}, Requirement: []string{}, Code: []string{}}
	if len(bytes.TrimSpace(refs)) > 0 {
		if err := json.Unmarshal(refs, &event.Refs); err != nil {
			return eventing.Event{}, fmt.Errorf("decode event refs: %w", err)
		}
		if event.Refs.Evidence == nil {
			event.Refs.Evidence = []string{}
		}
		if event.Refs.Case == nil {
			event.Refs.Case = []string{}
		}
		if event.Refs.Requirement == nil {
			event.Refs.Requirement = []string{}
		}
		if event.Refs.Code == nil {
			event.Refs.Code = []string{}
		}
	}
	return event, nil
}
