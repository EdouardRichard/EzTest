package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
)

// TestAPIHealthConfigProjection pins the read-only config projection the admin
// settings tab displays: the effective rate-limit and GC thresholds are part of
// /api/health, while secrets, tokens and absolute paths stay absent.
func TestAPIHealthConfigProjection(t *testing.T) {
	cfg := config.Config{
		Env:             "test",
		RateLimitRPS:    7,
		RateLimitBurst:  9,
		RateLimitSSEMax: 3,
		GCEnabled:       true,
		GCRetentionDays: 11,
		GCBatchKeep:     2,
	}
	h := New(cfg, testLogger(), Dependencies{Pinger: fakePinger{}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			RateLimit struct {
				RPS    int `json:"rps"`
				Burst  int `json:"burst"`
				SSEMax int `json:"sse_max"`
			} `json:"rate_limit"`
			GC struct {
				Enabled       bool `json:"enabled"`
				RetentionDays int  `json:"retention_days"`
				BatchKeep     int  `json:"batch_keep"`
			} `json:"gc"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if body.Data.RateLimit.RPS != 7 || body.Data.RateLimit.Burst != 9 || body.Data.RateLimit.SSEMax != 3 {
		t.Fatalf("rate limit projection=%+v", body.Data.RateLimit)
	}
	if !body.Data.GC.Enabled || body.Data.GC.RetentionDays != 11 || body.Data.GC.BatchKeep != 2 {
		t.Fatalf("gc projection=%+v", body.Data.GC)
	}
}
