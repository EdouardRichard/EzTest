package agentapi

// Task 46 (P2-T6): HTTP surface of the job cancel/retry control API. This
// handler only parses the path IDs, enforces a logged-in user and renders the
// shared {data: ...} envelope; every lifecycle/ownership rule lives in the
// application service.
//
// Both route shapes are mounted:
//
//	POST /api/jobs/{id}/cancel                     (canonical plan route)
//	POST /api/jobs/{id}/retry
//	POST /api/versions/{versionId}/jobs/{jobId}/cancel  (JobCard route)
//	POST /api/versions/{versionId}/jobs/{jobId}/retry
//
// The version-scoped shape passes the addressed version down so a job of
// another version is a not-found instead of a cross-version escalation.

import (
	"context"
	"net/http"
	"strconv"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// JobControlService is the application surface the handler needs.
// *agentapp.JobControl satisfies it.
type JobControlService interface {
	Cancel(ctx context.Context, actorID, versionID, jobID int64) (agentapp.JobView, error)
	Retry(ctx context.Context, actorID, versionID, jobID int64) (agentapp.JobView, error)
}

var _ JobControlService = (*agentapp.JobControl)(nil)

// JobControlHandler serves the job cancel/retry operations. Every route is
// mounted behind the bearer middleware (a user token, never admin-only).
type JobControlHandler struct {
	service JobControlService
}

// NewJobControlHandler builds the job control handler.
func NewJobControlHandler(service JobControlService) *JobControlHandler {
	return &JobControlHandler{service: service}
}

// Register mounts both the direct and the version-scoped job control routes.
// The assembly root owns the authentication middleware.
func (h *JobControlHandler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	if mux == nil || auth == nil || h == nil {
		return
	}
	// Direct route from the plan: the job's own version owns the operation.
	mux.Handle("POST /api/jobs/{id}/cancel", auth(http.HandlerFunc(h.Cancel)))
	mux.Handle("POST /api/jobs/{id}/retry", auth(http.HandlerFunc(h.Retry)))
	// Version-scoped route used by the conversation JobCard. The path version
	// is forwarded to the service, which 404s a foreign job.
	mux.Handle("POST /api/versions/{versionId}/jobs/{jobId}/cancel", auth(http.HandlerFunc(h.Cancel)))
	mux.Handle("POST /api/versions/{versionId}/jobs/{jobId}/retry", auth(http.HandlerFunc(h.Retry)))
}

// Cancel POST /api/jobs/{id}/cancel or
// POST /api/versions/{versionId}/jobs/{jobId}/cancel.
func (h *JobControlHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, true)
}

// Retry POST /api/jobs/{id}/retry or
// POST /api/versions/{versionId}/jobs/{jobId}/retry.
func (h *JobControlHandler) Retry(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, false)
}

func (h *JobControlHandler) handle(w http.ResponseWriter, r *http.Request, cancel bool) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, jobID, err := parseJobControlIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.service == nil {
		httpx.Error(w, r, platform.Unavailable("job control service is not configured"))
		return
	}

	var view agentapp.JobView
	if cancel {
		view, err = h.service.Cancel(r.Context(), actorID, versionID, jobID)
	} else {
		view, err = h.service.Retry(r.Context(), actorID, versionID, jobID)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toJobControlViewDTO(view))
}

// parseJobControlIDs reads either the direct {id} route or the version-scoped
// {versionId}/{jobId} route. A direct route reports versionID=0.
func parseJobControlIDs(r *http.Request) (versionID, jobID int64, err error) {
	if rawJobID := r.PathValue("jobId"); rawJobID != "" {
		versionID, err = validation.NonEmptyID("version_id", r.PathValue("versionId"))
		if err != nil {
			return 0, 0, err
		}
		jobID, err = validation.NonEmptyID("job_id", rawJobID)
		return versionID, jobID, err
	}
	jobID, err = validation.NonEmptyID("id", r.PathValue("id"))
	return 0, jobID, err
}

// jobControlViewDTO is the wire projection of one controlled job. The field
// names follow the JobCard contract (state) and the work_item lifecycle.
type jobControlViewDTO struct {
	JobID       string `json:"job_id"`
	VersionID   string `json:"version_id"`
	Kind        string `json:"kind"`
	State       string `json:"state"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts"`
	UpdatedAt   string `json:"updated_at"`
}

func toJobControlViewDTO(view agentapp.JobView) jobControlViewDTO {
	return jobControlViewDTO{
		JobID:       strconv.FormatInt(view.JobID, 10),
		VersionID:   strconv.FormatInt(view.VersionID, 10),
		Kind:        view.Kind,
		State:       view.State,
		Attempts:    view.Attempts,
		MaxAttempts: view.MaxAttempts,
		UpdatedAt:   formatTime(view.UpdatedAt),
	}
}
