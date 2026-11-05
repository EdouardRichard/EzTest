package executionapp

// W1-14B (spec R-EX-5): the resource catalog is the system of record for
// approved/rejected pages and APIs. Execution-time discovery writes proposed
// observations, so when an admin confirms or rejects a discovered resource the
// matching `proposed` observations must settle to the same decision. The
// resource context publishes `resource.page|api.confirmed|rejected` on the
// system channel with the decimal resource id; this observer is the execution
// side of that best-effort write.

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// ResourceDecisionMarker settles the DB-29 observations of one discovered
// resource. Only rows whose decision is still `proposed` may change, so a
// replayed/duplicated event can never rewrite an already-settled row; the
// returned count reports how many observations were updated.
type ResourceDecisionMarker interface {
	MarkDecisionByResource(ctx context.Context, resourceID int64, decision string) (int64, error)
}

// resourceDecisionEvents maps the four resource lifecycle events that settle a
// proposed discovery observation to the DB-29 decision value.
var resourceDecisionEvents = map[string]string{
	"resource.page.confirmed": "approved",
	"resource.api.confirmed":  "approved",
	"resource.page.rejected":  "rejected",
	"resource.api.rejected":   "rejected",
}

// NewResourceDecisionObserver returns the system-channel observer that writes
// the resource confirm/reject decision back to the discovery observations.
//
// The observer is best effort: the EventObserver contract runs after broadcast
// and ignores returned errors, so a failed write is logged (WARN) and reported
// as nil rather than blocking or failing the publish. A nil marker is a no-op
// (degraded assembly); every other event type and every other channel returns
// immediately without parsing or I/O.
func NewResourceDecisionObserver(marker ResourceDecisionMarker, logger *slog.Logger) eventingapp.EventObserver {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, event eventing.Event) error {
		if event.Channel != "system" {
			return nil
		}
		decision, ok := resourceDecisionEvents[event.Type]
		if !ok {
			return nil
		}
		resourceID, ok := discoveryResourceID(event.Payload)
		if !ok {
			return nil
		}
		if marker == nil {
			return nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		rows, err := marker.MarkDecisionByResource(ctx, resourceID, decision)
		if err != nil {
			logger.WarnContext(ctx, "execution discovery: resource decision sync failed",
				slog.String("type", event.Type),
				slog.Int64("resource_id", resourceID),
				slog.String("decision", decision),
				slog.String("error", err.Error()),
			)
			return nil
		}
		logger.DebugContext(ctx, "execution discovery: resource decision synced",
			slog.String("type", event.Type),
			slog.Int64("resource_id", resourceID),
			slog.String("decision", decision),
			slog.Int64("updated", rows),
		)
		return nil
	}
}

// discoveryResourceID parses the `id` payload field of a resource event. The
// resource service publishes a decimal string; an in-process producer may carry
// an int64. An absent/non-numeric/zero/negative value is rejected so the
// observer never queries the repo with a bogus resource identity.
func discoveryResourceID(payload map[string]any) (int64, bool) {
	if payload == nil {
		return 0, false
	}
	switch value := payload["id"].(type) {
	case string:
		raw := strings.TrimSpace(value)
		if raw == "" {
			return 0, false
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	case int64:
		if value <= 0 {
			return 0, false
		}
		return value, true
	default:
		return 0, false
	}
}
