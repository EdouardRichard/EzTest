package designapp

import (
	"context"
	"errors"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// publishDesignEvent emits one requirement-scoped design event on the version
// channel (best effort: a publish failure never rolls back the write). It is the
// SINGLE builder of that envelope — GraphService and JudgmentService both route
// through it instead of keeping two verbatim copies of the same shape.
func publishDesignEvent(ctx context.Context, events eventingapp.Publisher, versionID int64, eventType string, requirementID int64, payload map[string]any, actorID int64) {
	if events == nil {
		return
	}
	event := eventingapp.Event{
		Channel: "version:" + platformid.Format(versionID),
		Type:    eventType,
		Scope: map[string]any{
			"version_id":     platformid.Format(versionID),
			"requirement_id": platformid.Format(requirementID),
		},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	}
	if actorID > 0 {
		actor := actorID
		event.ActorID = &actor
	}
	_ = events.Publish(ctx, event)
}

// mapDesignError maps repository/domain sentinel errors onto the stable API
// error envelope. Already-mapped platform errors pass through.
func mapDesignError(err error, kind string) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, design.ErrNotFound):
		return platform.NotFound(kind)
	case errors.Is(err, design.ErrStateConflict):
		return platform.StateConflict("the design entity is not in a valid state for this operation")
	case errors.Is(err, design.ErrDuplicate):
		return platform.Conflict("design entity already exists")
	default:
		return err
	}
}

// requirePositiveID validates internal IDs parsed by the HTTP layer.
func requirePositiveID(field string, value int64) error {
	if value <= 0 {
		return platform.Invalid(field, "must be a positive decimal ID")
	}
	return nil
}

// asInt64Ptr projects the domain's zero-id "absent" convention (design.OptionalID)
// onto optional wire IDs. The convention has ONE authority in the domain; neither
// the application nor the store keeps its own copy (H-F10 T6).
func asInt64Ptr(value int64) *int64 { return design.OptionalID(value) }

// derefInt64 returns 0 for a nil pointer.
func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// utcNow normalizes a timestamp for wire output.
func utcNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC()
}
