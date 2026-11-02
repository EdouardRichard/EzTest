package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	"github.com/EdouardRichard/EzTest/backend/internal/orchestration/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// readinessTimeout bounds the DB ping + status lookup + storage write probe so
// a hung dependency cannot hold a load-balancer health check open.
const readinessTimeout = 3 * time.Second

// instanceView is the public api_instance representation. IDs stay decimal
// strings and timestamps are RFC3339 UTC, matching the rest of the 0.2 API.
type instanceView struct {
	ID          string `json:"id"`
	InstanceKey string `json:"instance_key"`
	Hostname    string `json:"hostname"`
	PID         int    `json:"pid"`
	APIPort     int    `json:"api_port"`
	NodeID      int    `json:"node_id"`
	Status      string `json:"status"`
	Version     string `json:"version"`
	// ActiveRequests/ActiveWork are the in-flight counters reported by the last
	// heartbeat (P8 drain observability). They are informational only.
	ActiveRequests int     `json:"active_requests"`
	ActiveWork     int     `json:"active_work"`
	LastError      *string `json:"last_error"`
	StartedAt      string  `json:"started_at"`
	HeartbeatAt    string  `json:"heartbeat_at"`
	DrainingAt     *string `json:"draining_at"`
	StoppedAt      *string `json:"stopped_at"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

func toInstanceView(instance orchestration.Instance) instanceView {
	view := instanceView{
		ID:             platformid.Format(instance.ID),
		InstanceKey:    instance.InstanceKey,
		Hostname:       instance.Hostname,
		PID:            instance.PID,
		APIPort:        instance.APIPort,
		NodeID:         instance.NodeID,
		Status:         string(instance.Status),
		Version:        instance.Version,
		ActiveRequests: instance.ActiveRequests,
		ActiveWork:     instance.ActiveWork,
		LastError:      instance.LastError,
		StartedAt:      formatTime(instance.StartedAt),
		HeartbeatAt:    formatTime(instance.HeartbeatAt),
		CreatedAt:      formatTime(instance.CreatedAt),
		UpdatedAt:      formatTime(instance.UpdatedAt),
	}
	if instance.DrainingAt != nil {
		formatted := formatTime(*instance.DrainingAt)
		view.DrainingAt = &formatted
	}
	if instance.StoppedAt != nil {
		formatted := formatTime(*instance.StoppedAt)
		view.StoppedAt = &formatted
	}
	return view
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// readyView is the GET /api/health/ready body.
type readyView struct {
	Status     string `json:"status"`
	Database   string `json:"database"`
	Storage    string `json:"storage"`
	Draining   bool   `json:"draining"`
	InstanceID string `json:"instance_id"`
	// Inflight counters are observed for drain/rolling-upgrade; Ready() ignores
	// them (a draining replica stays 503 while its work finishes).
	InflightRequests int `json:"inflight_requests"`
	InflightWork     int `json:"inflight_work"`
}

func toReadyView(readiness orchestrationapp.Readiness) readyView {
	view := readyView{
		Status:           "ok",
		Database:         "ok",
		Storage:          "ok",
		Draining:         readiness.Draining,
		InstanceID:       platformid.Format(readiness.InstanceID),
		InflightRequests: readiness.InflightRequests,
		InflightWork:     readiness.InflightWork,
	}
	if readiness.InstanceID <= 0 {
		view.InstanceID = ""
	}
	if !readiness.Database {
		view.Database = "down"
	}
	if !readiness.Storage {
		view.Storage = "down"
	}
	if !readiness.Ready() {
		view.Status = "degraded"
	}
	return view
}

// Service is the orchestration application surface the handlers need.
type Service interface {
	List(ctx context.Context) ([]orchestration.Instance, error)
	Drain(ctx context.Context, id int64, actor platform.Actor) (orchestration.Instance, error)
	Activate(ctx context.Context, id int64, actor platform.Actor) (orchestration.Instance, error)
	Readiness(ctx context.Context) orchestrationapp.Readiness
}

// Handler serves the P7 instance/admin and readiness endpoints.
type Handler struct {
	service Service
}

// New builds the orchestration HTTP handler. A nil service is allowed; every
// endpoint then fails closed with 503.
func New(service Service) *Handler { return &Handler{service: service} }

var _ Service = (*orchestrationapp.Service)(nil)

// ListInstances GET /api/admin/instances (admin).
func (h *Handler) ListInstances(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdmin(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("orchestration service is not configured"))
		return
	}
	instances, err := h.service.List(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]instanceView, 0, len(instances))
	for _, instance := range instances {
		views = append(views, toInstanceView(instance))
	}
	// The registry is a small bounded roster: no cursor, every instance per page.
	httpx.List(w, http.StatusOK, views, "", false)
}

// DrainInstance POST /api/admin/instances/{id}/drain (admin).
func (h *Handler) DrainInstance(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, orchestration.StatusDraining)
}

// ActivateInstance POST /api/admin/instances/{id}/activate (admin).
func (h *Handler) ActivateInstance(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, orchestration.StatusActive)
}

// Ready GET /api/health/ready (public). 200 only when the database is
// reachable, the shared storage probe succeeds and this instance is not
// draining; otherwise 503 with the failing condition marked "down".
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		httpx.JSON(w, http.StatusServiceUnavailable, readyView{
			Status:   "degraded",
			Database: "down",
			Storage:  "down",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	view := toReadyView(h.service.Readiness(ctx))
	status := http.StatusOK
	if !(view.Database == "ok" && view.Storage == "ok") || view.Draining {
		status = http.StatusServiceUnavailable
	}
	httpx.JSON(w, status, view)
}

func (h *Handler) transition(w http.ResponseWriter, r *http.Request, target orchestration.Status) {
	actor, err := requireAdmin(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	id, err := parseInstanceID(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("orchestration service is not configured"))
		return
	}
	var instance orchestration.Instance
	if target == orchestration.StatusDraining {
		instance, err = h.service.Drain(r.Context(), id, actor)
	} else {
		instance, err = h.service.Activate(r.Context(), id, actor)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toInstanceView(instance))
}

func parseInstanceID(raw string) (int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, platform.Invalid("id", "is required")
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, platform.Invalid("id", "must be a positive decimal ID")
	}
	return parsed, nil
}

func requireAdmin(r *http.Request) (platform.Actor, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		return platform.Actor{}, platform.Unauthenticated()
	}
	if !actor.IsAdmin() {
		return platform.Actor{}, platform.Forbidden()
	}
	return actor, nil
}
