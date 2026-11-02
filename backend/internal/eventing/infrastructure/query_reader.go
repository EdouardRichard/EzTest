package eventingpg

import (
	"context"
	"fmt"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ eventingapp.QueryReader = (*QueryReader)(nil)

// QueryReader is the eventing context's P6 read-only count source. It only
// counts persisted events; the application layer validates the event type and
// the channel vocabulary.
type QueryReader struct {
	pool *pgxpool.Pool
}

// NewQueryReader wraps a pgx pool. A nil pool is a wiring error.
func NewQueryReader(pool *pgxpool.Pool) (*QueryReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("eventing query reader: nil pool")
	}
	return &QueryReader{pool: pool}, nil
}

// CountEvents counts persisted events of one type. An empty channel counts the
// global event log; a non-empty channel restricts the count to that channel.
func (r *QueryReader) CountEvents(ctx context.Context, eventType, channel string) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM event
		WHERE type = $1
		  AND ($2::text = '' OR channel = $2::text)
	`, eventType, channel).Scan(&count); err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return count, nil
}
