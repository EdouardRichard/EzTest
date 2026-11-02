package eventingpg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultListenerRetry = time.Second
	maxListenerRetry     = 15 * time.Second
)

// Listener is the per-instance cross-instance event fan-out: one dedicated
// PostgreSQL connection holding LISTEN eztest_events. For every notification it
// loads the persisted event and delivers it to the local hub through the
// application Dispatcher. Connection errors are retried with bounded
// exponential backoff; the session LISTEN registration dies with the
// connection, which is why the connection is not taken from the pool.
type Listener struct {
	pool       *pgxpool.Pool
	events     eventingapp.EventReader
	sink       eventingapp.LocalBroadcaster
	observer   eventingapp.EventObserver
	dispatcher *eventingapp.Dispatcher
	logger     *slog.Logger
	retry      time.Duration
	ready      chan struct{}
	readyOnce  bool
}

// ListenerOption customizes the listener. Wiring only overrides the logger;
// tests may shorten the reconnect delay.
type ListenerOption func(*Listener)

// WithListenerLogger sets the structured logger (nil keeps slog.Default()).
func WithListenerLogger(logger *slog.Logger) ListenerOption {
	return func(l *Listener) {
		if logger != nil {
			l.logger = logger
		}
	}
}

// WithListenerRetry sets the first reconnect delay; it doubles up to 15s.
func WithListenerRetry(delay time.Duration) ListenerOption {
	return func(l *Listener) {
		if delay > 0 {
			l.retry = delay
		}
	}
}

// WithListenerObserver installs an observer invoked after each persisted event
// is broadcast to this replica's local hub (for example, to reload settings).
func WithListenerObserver(observer eventingapp.EventObserver) ListenerOption {
	return func(l *Listener) {
		l.observer = observer
	}
}

// NewListener builds the cross-instance listener. events loads persisted events
// (the PostgreSQL repository) and sink is the local hub.
func NewListener(
	pool *pgxpool.Pool,
	events eventingapp.EventReader,
	sink eventingapp.LocalBroadcaster,
	options ...ListenerOption,
) *Listener {
	listener := &Listener{
		pool:   pool,
		events: events,
		sink:   sink,
		logger: slog.Default(),
		retry:  defaultListenerRetry,
		ready:  make(chan struct{}),
	}
	for _, option := range options {
		option(listener)
	}
	dispatcherOptions := []eventingapp.DispatcherOption{}
	if listener.observer != nil {
		dispatcherOptions = append(dispatcherOptions,
			eventingapp.WithDispatcherObserver(listener.observer))
	}
	listener.dispatcher = eventingapp.NewDispatcher(events, sink, dispatcherOptions...)
	return listener
}

// Ready is closed once the first LISTEN has been registered. It exists so the
// assembly root and integration tests can wait for fan-out to be live before
// driving traffic.
func (l *Listener) Ready() <-chan struct{} {
	if l == nil || l.ready == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return l.ready
}

// Run blocks until ctx is cancelled, reconnecting whenever the LISTEN session
// fails. A nil configuration returns an error instead of spinning.
func (l *Listener) Run(ctx context.Context) error {
	if l == nil || l.pool == nil || l.dispatcher == nil {
		return errors.New("event listener is not configured")
	}
	delay := l.retry
	for {
		err := l.listen(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			l.logger.Warn("event listener disconnected; retrying", "error", err, "delay", delay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay *= 2
		if delay > maxListenerRetry {
			delay = maxListenerRetry
		}
	}
}

// listen opens one dedicated connection, registers LISTEN and processes
// notifications until the connection fails.
func (l *Listener) listen(ctx context.Context) error {
	config := l.pool.Config().ConnConfig.Copy()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect event listener: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	if _, err := conn.Exec(ctx, "LISTEN "+eventingapp.NotifyChannel); err != nil {
		return fmt.Errorf("listen %s: %w", eventingapp.NotifyChannel, err)
	}
	l.markReady()

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for event notification: %w", err)
		}
		if err := l.dispatcher.Handle(ctx, notification.Payload); err != nil {
			l.logger.Warn("event notification skipped", "error", err)
		}
	}
}

func (l *Listener) markReady() {
	if l.readyOnce {
		return
	}
	l.readyOnce = true
	close(l.ready)
}
