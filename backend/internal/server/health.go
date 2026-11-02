package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Pinger is the minimal database capability the readiness probe needs.
type Pinger interface {
	Ping(context.Context) error
}

// Health serves the liveness and readiness probes.
type Health struct {
	Version        string
	DB             Pinger
	SettingsLoaded bool
	ExecutorURL    string
}

type healthData struct {
	Status         string `json:"status"`
	Version        string `json:"version"`
	DB             string `json:"db"`
	SettingsLoaded bool   `json:"settings_loaded"`
	ExecutorURL    string `json:"executor_url"`
}

// Register mounts GET /healthz and GET /api/health.
func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.liveness)
	mux.HandleFunc("GET /api/health", h.readiness)
}

func (h *Health) liveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Health) readiness(w http.ResponseWriter, r *http.Request) {
	data := healthData{
		Status:         "ok",
		Version:        h.Version,
		DB:             "ok",
		SettingsLoaded: h.SettingsLoaded,
		ExecutorURL:    h.ExecutorURL,
	}
	status := http.StatusOK
	if h.DB == nil {
		data.Status = "degraded"
		data.DB = "down"
		status = http.StatusServiceUnavailable
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.DB.Ping(ctx); err != nil {
			data.Status = "degraded"
			data.DB = "down"
			status = http.StatusServiceUnavailable
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}
