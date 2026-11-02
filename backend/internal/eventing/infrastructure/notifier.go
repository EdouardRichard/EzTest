package eventingpg

import (
	"context"
	"fmt"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Notifier publishes the (channel, seq) reference of a committed event to
// every API instance through PostgreSQL NOTIFY. NOTIFY is transactional: it is
// delivered on commit of the transaction that executes it. The event append
// runs in its own committed transaction (Repository.Append), so this notifier
// executes right after that commit; a lost notification is recovered by SSE
// Last-Event-ID replay.
type Notifier struct {
	pool *pgxpool.Pool
}

// NewNotifier wires the NOTIFY adapter.
func NewNotifier(pool *pgxpool.Pool) *Notifier {
	return &Notifier{pool: pool}
}

// Notify sends one reference on the eztest_events channel.
func (n *Notifier) Notify(ctx context.Context, ref eventingapp.EventRef) error {
	if n == nil || n.pool == nil {
		return fmt.Errorf("event notifier is not configured")
	}
	payload, err := eventingapp.EncodeEventRef(ref)
	if err != nil {
		return err
	}
	if _, err := n.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, eventingapp.NotifyChannel, payload); err != nil {
		return fmt.Errorf("notify event %s/%d: %w", ref.Channel, ref.Seq, err)
	}
	return nil
}

var _ eventingapp.Notifier = (*Notifier)(nil)
