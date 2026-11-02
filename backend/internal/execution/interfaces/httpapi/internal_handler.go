package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// maxInternalRequestBytes bounds the push register/heartbeat JSON body.
const maxInternalRequestBytes = 1 << 20

// InstanceView is the internal register/heartbeat projection (executor-protocol
// §12.1 / p7-api.md). Unlike the admin ExecutorView it has no token hash,
// labels or last_error.
type InstanceView struct {
	InstanceID     string  `json:"instance_id"`
	Endpoint       string  `json:"endpoint"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	Capacity       int     `json:"capacity"`
	ActiveContexts int     `json:"active_contexts"`
	Version        string  `json:"version"`
	LastHeartbeat  *string `json:"last_heartbeat"`
}

func toInstanceView(instance execution.ExecutorInstance) InstanceView {
	return InstanceView{
		InstanceID:     platformid.Format(instance.ID),
		Endpoint:       instance.Endpoint,
		Name:           instance.Name,
		Status:         string(instance.Status),
		Capacity:       instance.Capacity,
		ActiveContexts: instance.ActiveContexts,
		Version:        instance.Version,
		LastHeartbeat:  formatOptionalTime(instance.LastHeartbeat),
	}
}

// internalRegisterRequest is the POST /api/internal/executors/register body.
// max_contexts is the optional contract alias of capacity.
type internalRegisterRequest struct {
	Name           string `json:"name"`
	Endpoint       string `json:"endpoint"`
	Capacity       *int   `json:"capacity"`
	MaxContexts    *int   `json:"max_contexts"`
	ActiveContexts *int   `json:"active_contexts"`
	Version        string `json:"version"`
	Status         string `json:"status"`
}

// internalHeartbeatRequest is the POST /api/internal/executors/heartbeat body.
type internalHeartbeatRequest struct {
	Name           string `json:"name"`
	Endpoint       string `json:"endpoint"`
	ActiveContexts *int   `json:"active_contexts"`
	Capacity       *int   `json:"capacity"`
	Version        string `json:"version"`
	Status         string `json:"status"`
}

// InternalRegisterExecutor POST /api/internal/executors/register.
//
// Mount it behind InternalTokenAuth (or an equivalent middleware that enforces
// the execution.executor_token Bearer). The handler itself also resolves the
// token to write the row's token_hash without persisting the clear value.
func (h *Handler) InternalRegisterExecutor(w http.ResponseWriter, r *http.Request) {
	if h.deps.Registry == nil {
		httpx.Error(w, r, platform.NewError(http.StatusInternalServerError, "internal", "executor registry is not configured"))
		return
	}
	var request internalRegisterRequest
	if err := decodeInternalRequest(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	capacity, err := registerCapacity(request.Capacity, request.MaxContexts)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	token, err := h.internalExecutorToken(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	instance, err := h.deps.Registry.RegisterPush(r.Context(), token, executionapp.ExecutorRegistration{
		Endpoint:       request.Endpoint,
		Name:           request.Name,
		Capacity:       capacity,
		ActiveContexts: intValue(request.ActiveContexts),
		Version:        request.Version,
		Status:         request.Status,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toInstanceView(instance))
}

// InternalHeartbeatExecutor POST /api/internal/executors/heartbeat.
func (h *Handler) InternalHeartbeatExecutor(w http.ResponseWriter, r *http.Request) {
	if h.deps.Registry == nil {
		httpx.Error(w, r, platform.NewError(http.StatusInternalServerError, "internal", "executor registry is not configured"))
		return
	}
	var request internalHeartbeatRequest
	if err := decodeInternalRequest(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if request.ActiveContexts == nil {
		httpx.Error(w, r, platform.Invalid("active_contexts", "active_contexts is required"))
		return
	}
	instance, err := h.deps.Registry.HeartbeatPush(r.Context(), executionapp.ExecutorHeartbeat{
		Endpoint:       request.Endpoint,
		ActiveContexts: *request.ActiveContexts,
		Capacity:       intValue(request.Capacity),
		Version:        request.Version,
		Status:         request.Status,
	})
	if err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			httpx.Error(w, r, platform.NewError(http.StatusNotFound, "executor_unknown", "executor endpoint is not registered"))
			return
		}
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toInstanceView(instance))
}

// registerCapacity merges capacity and its max_contexts alias, rejecting a
// conflicting pair (executor-protocol §12.1).
func registerCapacity(capacity, maxContexts *int) (int, error) {
	if capacity != nil && maxContexts != nil && *capacity != *maxContexts {
		return 0, platform.Invalid("max_contexts", "max_contexts must equal capacity when both are provided")
	}
	if capacity != nil {
		return *capacity, nil
	}
	return intValue(maxContexts), nil
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// internalExecutorToken reads the configured Bearer token so the register path
// can store its hash. The value is never written to logs or responses.
func (h *Handler) internalExecutorToken(ctx context.Context) (string, error) {
	if h.deps.Settings == nil {
		return "", platform.NewError(http.StatusInternalServerError, "internal", "executor settings are not configured")
	}
	token, err := h.deps.Settings.String(ctx, executionapp.SettingExecutorToken)
	if err != nil {
		return "", platform.NewError(http.StatusInternalServerError, "internal", "executor token is not configured")
	}
	if strings.TrimSpace(token) == "" {
		return "", platform.NewError(http.StatusInternalServerError, "internal", "executor token is not configured")
	}
	return token, nil
}

// decodeInternalRequest decodes exactly one bounded JSON object.
func decodeInternalRequest(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxInternalRequestBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		return platform.Invalid("body", "request body must be a single JSON object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return platform.Invalid("body", "request body must contain a single JSON object")
	}
	return nil
}

// InternalTokenAuth returns the internal Bearer middleware for the P7 executor
// push endpoints. It compares the presented token against
// execution.executor_token in constant time and never logs the value.
func InternalTokenAuth(settings executionapp.SettingsReader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if settings == nil {
				internalUnauthorized(w, r)
				return
			}
			expected, err := settings.String(r.Context(), executionapp.SettingExecutorToken)
			provided, ok := internalBearerToken(r)
			if err != nil || strings.TrimSpace(expected) == "" || !ok {
				internalUnauthorized(w, r)
				return
			}
			if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				internalUnauthorized(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rejectInternalRequests is the fail-closed middleware used when
// RegisterInternalRoutes is mounted without an auth middleware.
func rejectInternalRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalUnauthorized(w, r)
	})
}

func internalUnauthorized(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, platform.NewError(http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token"))
}

// internalBearerToken parses one Authorization: Bearer <token> header.
func internalBearerToken(r *http.Request) (string, bool) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
