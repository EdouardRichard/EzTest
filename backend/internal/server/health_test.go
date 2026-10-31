package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct {
	err error
}

func (f fakePinger) Ping(context.Context) error { return f.err }

func healthMux(h *Health) *http.ServeMux {
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	healthMux(&Health{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %#v", body)
	}
}

func TestAPIHealthReportsReady(t *testing.T) {
	h := &Health{
		Version:        "test-version",
		DB:             fakePinger{},
		SettingsLoaded: true,
		ExecutorURL:    "http://127.0.0.1:13336",
	}
	rec := httptest.NewRecorder()
	healthMux(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data struct {
			Status         string `json:"status"`
			Version        string `json:"version"`
			DB             string `json:"db"`
			SettingsLoaded bool   `json:"settings_loaded"`
			ExecutorURL    string `json:"executor_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Status != "ok" || body.Data.DB != "ok" || !body.Data.SettingsLoaded {
		t.Fatalf("data = %#v", body.Data)
	}
	if body.Data.Version != "test-version" || body.Data.ExecutorURL != "http://127.0.0.1:13336" {
		t.Fatalf("data = %#v", body.Data)
	}
}

func TestAPIHealthReportsDatabaseDown(t *testing.T) {
	h := &Health{DB: fakePinger{err: errors.New("dial failed")}}
	rec := httptest.NewRecorder()
	healthMux(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body struct {
		Data struct {
			Status string `json:"status"`
			DB     string `json:"db"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Status != "degraded" || body.Data.DB != "down" {
		t.Fatalf("data = %#v", body.Data)
	}
}

func TestAPIHealthNilDatabaseIsDown(t *testing.T) {
	rec := httptest.NewRecorder()
	healthMux(&Health{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
