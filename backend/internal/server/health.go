package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
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
	// RateLimit and GC are the effective, non-secret configuration thresholds
	// the admin settings tab projects read-only (spec §10.1/§10.2). They carry
	// no paths, credentials or store locations.
	RateLimit rateLimitProjection `json:"rate_limit"`
	GC        gcProjection        `json:"gc"`
}

type rateLimitProjection struct {
	RPS    int `json:"rps"`
	Burst  int `json:"burst"`
	SSEMax int `json:"sse_max"`
}

type gcProjection struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
	BatchKeep     int  `json:"batch_keep"`
}

// LLMConfigStore reports the decrypted default LLM configuration. The llm
// application ConfigService satisfies it.
type LLMConfigStore interface {
	GetDefault(ctx context.Context) (llm.Config, error)
}

// handleHealthz is the liveness probe. It deliberately does not touch any
// dependency: it answers 200 as long as the process can serve requests.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleAPIHealth is the readiness probe. It pings the database with a short
// timeout and reports 503 when the ping fails. LLM configuration is reported as
// configured only when a default configuration exists and its key decrypts.
// The effective rate-limit and GC thresholds are projected read-only.
func handleAPIHealth(cfg config.Config, pinger Pinger, llmConfigs LLMConfigStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()

		data := healthData{
			Status:  "ok",
			Version: Version,
			Commit:  Commit,
			DB:      "ok",
			RateLimit: rateLimitProjection{
				RPS:    cfg.RateLimitRPS,
				Burst:  cfg.RateLimitBurst,
				SSEMax: cfg.RateLimitSSEMax,
			},
			GC: gcProjection{
				Enabled:       cfg.GCEnabled,
				RetentionDays: cfg.GCRetentionDays,
				BatchKeep:     cfg.GCBatchKeep,
			},
		}
		status := http.StatusOK
		if pinger == nil || pinger.Ping(ctx) != nil {
			data.Status = "unavailable"
			data.DB = "down"
			status = http.StatusServiceUnavailable
		} else if llmConfigs != nil {
			// GetDefault decrypts the stored key; a decryption failure is
			// reported as not configured instead of leaking the crypto error.
			if cfg, err := llmConfigs.GetDefault(ctx); err == nil && cfg.APIKey != "" {
				data.LLMConfigured = true
			}
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(healthEnvelope{Data: data})
	}
}
