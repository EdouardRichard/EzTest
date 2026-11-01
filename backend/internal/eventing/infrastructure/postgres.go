// Package eventingpg is the PostgreSQL adapter for the persistent event log.
package eventingpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository stores events and their per-channel sequences.
type Repository struct {
	pool *pgxpool.Pool
}

// New wraps a pgx pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Append allocates the next channel-local sequence and inserts the event in one
// transaction.
func (r *Repository) Append(ctx context.Context, event eventing.Event) (eventing.Event, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return eventing.Event{}, fmt.Errorf("begin event append: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var seq int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO event_channel (channel, last_seq, updated_at)
		VALUES ($1, 1, now())
		ON CONFLICT (channel) DO UPDATE
		SET last_seq = event_channel.last_seq + 1, updated_at = now()
		RETURNING last_seq
	`, event.Channel).Scan(&seq); err != nil {
		return eventing.Event{}, fmt.Errorf("allocate event seq: %w", err)
	}

	envelope, err := encodeEnvelope(event)
	if err != nil {
		return eventing.Event{}, err
	}
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO event (channel, seq, type, envelope)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING created_at
	`, event.Channel, seq, event.Type, string(envelope)).Scan(&createdAt); err != nil {
		return eventing.Event{}, fmt.Errorf("insert event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return eventing.Event{}, fmt.Errorf("commit event append: %w", err)
	}

	event.Seq = seq
	event.CreatedAt = createdAt
	return event, nil
}

// Get loads one event by its channel cursor. Missing rows are reported as
// eventing.ErrNotFound so the cross-instance listener can distinguish a lost
// race (row gone) from a transient database failure.
func (r *Repository) Get(ctx context.Context, channel string, seq int64) (eventing.Event, error) {
	event, err := scanEvent(r.pool.QueryRow(ctx, `
		SELECT channel, seq, type, envelope, created_at
		FROM event
		WHERE channel = $1 AND seq = $2
	`, channel, seq))
	if errors.Is(err, pgx.ErrNoRows) {
		return eventing.Event{}, eventing.ErrNotFound
	}
	if err != nil {
		return eventing.Event{}, fmt.Errorf("get event: %w", err)
	}
	return event, nil
}

// Snapshot returns the newest page (after == 0) or events after a cursor.
func (r *Repository) Snapshot(ctx context.Context, channel string, after int64, limit int) (eventing.SnapshotPage, error) {
	var lastSeq int64
	if err := r.pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT last_seq FROM event_channel WHERE channel = $1), 0)
	`, channel).Scan(&lastSeq); err != nil {
		return eventing.SnapshotPage{}, fmt.Errorf("read event channel cursor: %w", err)
	}

	var rows pgxRows
	var err error
	if after > 0 {
		rows, err = r.pool.Query(ctx, `
			SELECT channel, seq, type, envelope, created_at
			FROM event
			WHERE channel = $1 AND seq > $2
			ORDER BY seq ASC
			LIMIT $3
		`, channel, after, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT channel, seq, type, envelope, created_at
			FROM event
			WHERE channel = $1
			ORDER BY seq DESC
			LIMIT $2
		`, channel, limit+1)
	}
	if err != nil {
		return eventing.SnapshotPage{}, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	events := make([]eventing.Event, 0, limit+1)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return eventing.SnapshotPage{}, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return eventing.SnapshotPage{}, fmt.Errorf("scan events: %w", err)
	}

	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	if after == 0 {
		// The newest page is read descending for the LIMIT, then returned in
		// ascending cursor order.
		for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
			events[left], events[right] = events[right], events[left]
		}
	}
	return eventing.SnapshotPage{Events: events, Cursor: lastSeq, HasMore: hasMore}, nil
}

// Bounds returns the persisted cursor range for a channel.
func (r *Repository) Bounds(ctx context.Context, channel string) (eventing.Bounds, error) {
	var bounds eventing.Bounds
	if err := r.pool.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT last_seq FROM event_channel WHERE channel = $1), 0),
			COALESCE((SELECT MIN(seq) FROM event WHERE channel = $1), 0)
	`, channel).Scan(&bounds.LastSeq, &bounds.FirstSeq); err != nil {
		return eventing.Bounds{}, fmt.Errorf("read event bounds: %w", err)
	}
	return bounds, nil
}

type pgxRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type rowScanner interface {
	Scan(dest ...any) error
}

type storedEnvelope struct {
	Scope     map[string]any  `json:"scope"`
	Payload   map[string]any  `json:"payload"`
	ActorID   json.RawMessage `json:"actor_id"`
	RequestID string          `json:"request_id"`
}

func encodeEnvelope(event eventing.Event) ([]byte, error) {
	scope := event.Scope
	if scope == nil {
		scope = map[string]any{}
	}
	payload := event.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	envelope := map[string]any{"scope": scope, "payload": payload}
	if event.ActorID != nil {
		envelope["actor_id"] = strconv.FormatInt(*event.ActorID, 10)
	}
	if event.RequestID != "" {
		envelope["request_id"] = event.RequestID
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode event envelope: %w", err)
	}
	return encoded, nil
}

func scanEvent(scanner rowScanner) (eventing.Event, error) {
	var event eventing.Event
	var raw []byte
	if err := scanner.Scan(&event.Channel, &event.Seq, &event.Type, &raw, &event.CreatedAt); err != nil {
		return eventing.Event{}, fmt.Errorf("scan event: %w", err)
	}
	var envelope storedEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return eventing.Event{}, fmt.Errorf("decode event envelope: %w", err)
	}
	actorID, err := decodeActorID(envelope.ActorID)
	if err != nil {
		return eventing.Event{}, err
	}
	event.Scope = envelope.Scope
	event.Payload = envelope.Payload
	event.ActorID = actorID
	event.RequestID = envelope.RequestID
	return event, nil
}

func decodeActorID(raw json.RawMessage) (*int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("decode event actor id: %w", err)
		}
		return &value, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode event actor id: %w", err)
	}
	return &value, nil
}
