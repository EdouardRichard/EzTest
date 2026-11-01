package codeintelapi

import (
	"errors"
	"net/http"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

// writeError maps codeintel domain sentinels to the shared HTTP envelope. Stable
// platform errors (git/app/service) pass through unchanged.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		httpx.Error(w, r, err)
		return
	}
	switch {
	case errors.Is(err, codeintel.ErrNotFound):
		httpx.Error(w, r, platform.NotFound("resource"))
	case errors.Is(err, codeintel.ErrInvalid):
		httpx.Error(w, r, unprocessable("invalid_argument", "the request is not valid"))
	case errors.Is(err, codeintel.ErrConflict):
		httpx.Error(w, r, platform.Conflict("the request conflicts with the current state"))
	case errors.Is(err, codeintel.ErrUnavailable):
		httpx.Error(w, r, platform.Unavailable("a dependency is unavailable"))
	default:
		httpx.Error(w, r, err)
	}
}

// unprocessable builds a 422 with a stable API code.
func unprocessable(code, message string) *platform.Error {
	return platform.NewError(http.StatusUnprocessableEntity, code, message)
}

// binaryFileCode reports whether the error is the read-only binary marker that
// GET /file renders as an empty binary window instead of an error.
func binaryFileCode(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Code == "binary_file"
}
