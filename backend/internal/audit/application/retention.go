package auditapp

import (
	"context"
	"errors"
	"time"
)

// RetentionStore is the bounded-delete surface the retention worker needs. The
// PostgreSQL repository implements it; keeping it separate from Repository
// leaves the append-only use-case interface unchanged.
type RetentionStore interface {
	DeleteOlderThan(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}

// RetentionResult reports what one cleanup pass removed.
type RetentionResult struct {
	EventsDeleted int64
	AuditDeleted  int64
}

// RetentionService deletes event/audit rows older than the configured cutoffs
// and records one audit action. It is invoked by the leader-gated daily worker
// (P8). The caller decides the cutoffs from settings.
type RetentionService struct {
	audit    RetentionStore
	events   RetentionStore
	recorder Recorder
	batch    int
}

// NewRetentionService wires the retention service. audit/events may be nil in
// tests; a nil recorder disables the audit row. batch falls back to 500.
func NewRetentionService(audit, events RetentionStore, recorder Recorder, batch int) *RetentionService {
	if batch <= 0 {
		batch = 500
	}
	return &RetentionService{audit: audit, events: events, recorder: recorder, batch: batch}
}

// Run executes one cleanup pass. Both stores are drained in batches; an event
// failure does not prevent the audit cleanup (and vice versa). The returned
// error joins both failures.
func (s *RetentionService) Run(ctx context.Context, auditCutoff, eventCutoff time.Time) (RetentionResult, error) {
	var result RetentionResult
	var errs []error
	if s.events != nil {
		deleted, err := deleteAll(ctx, s.events, eventCutoff, s.batch)
		result.EventsDeleted = deleted
		if err != nil {
			errs = append(errs, err)
		}
	}
	if s.audit != nil {
		deleted, err := deleteAll(ctx, s.audit, auditCutoff, s.batch)
		result.AuditDeleted = deleted
		if err != nil {
			errs = append(errs, err)
		}
	}
	if s.recorder != nil {
		cutoff := auditCutoff
		if s.audit == nil {
			cutoff = eventCutoff
		}
		_ = s.recorder.Record(ctx, Event{
			Action:     "retention.cleanup",
			TargetKind: "retention",
			Metadata: map[string]any{
				"events_deleted": result.EventsDeleted,
				"audit_deleted":  result.AuditDeleted,
				"cutoff":         cutoff.UTC().Format(time.RFC3339),
			},
		})
	}
	return result, errors.Join(errs...)
}

func deleteAll(ctx context.Context, store RetentionStore, cutoff time.Time, batch int) (int64, error) {
	var total int64
	for {
		deleted, err := store.DeleteOlderThan(ctx, cutoff, batch)
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < int64(batch) {
			return total, nil
		}
	}
}
