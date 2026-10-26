package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// healthPingTimeout bounds the database check performed by /api/health so a
// degraded database cannot hold the readiness probe open.
const healthPingTimeout = 2 * time.Second

// Version and Commit describe the running build. They can be overridden at
// build time with -ldflags "-X github.com/EdouardRichard/EzTest/backend/internal/server.Version=...".
var (
	Version = "dev"
	Commit  = ""
)

type healthEnvelope struct {
	Data healthData `json:"data"`
}

type healthData struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	DB            string `json:"db"`
	LLMConfigured bool   `json:"llm_configured"`
}

// handleHealthz is the liveness probe. It deliberately does not touch any
// dependency: it answers 200 as long as the process can serve requests.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleAPIHealth is the readiness probe. It pings the database with a short
// timeout and reports 503 when the ping fails. LLM configuration is reported
// as configured once the LLM context provides a decryptable default config.
func handleAPIHealth(pinger Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()

		data := healthData{
			Status:  "ok",
			Version: Version,
			Commit:  Commit,
			DB:      "ok",
		}
		status := http.StatusOK
		if pinger == nil || pinger.Ping(ctx) != nil {
			data.Status = "unavailable"
			data.DB = "down"
			status = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(healthEnvelope{Data: data})
	}
}
