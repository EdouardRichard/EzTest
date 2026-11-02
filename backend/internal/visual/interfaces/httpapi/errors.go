package httpapi

import (
	"errors"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// requireActor reads the authenticated actor injected by the auth middleware.
func requireActor(r *http.Request) (platform.Actor, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		return platform.Actor{}, platform.Unauthenticated()
	}
	return actor, nil
}

// mapError maps domain sentinels that a directly-tested adapter may return onto
// the shared API error envelope. Platform errors pass through untouched.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, visual.ErrNotFound):
		return platform.NotFound("visual_entity")
	case errors.Is(err, visual.ErrInvalidConfig):
		return platform.NewError(http.StatusUnprocessableEntity, "config_invalid", err.Error())
	case errors.Is(err, visual.ErrInvalidBaseline):
		return platform.NewError(http.StatusUnprocessableEntity, "baseline_evidence_invalid", err.Error())
	case errors.Is(err, visual.ErrConfigChanged):
		return platform.NewError(http.StatusUnprocessableEntity, "visual_config_changed", err.Error())
	case errors.Is(err, visual.ErrInvalidDiff):
		return platform.NewError(http.StatusUnprocessableEntity, "diff_invalid", err.Error())
	case errors.Is(err, visual.ErrStateConflict):
		return platform.StateConflict("the visual entity is not in a valid state")
	default:
		return err
	}
}
