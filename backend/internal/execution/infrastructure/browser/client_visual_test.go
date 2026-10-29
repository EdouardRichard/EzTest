package browser_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/browser"
)

// visualArtifact renders one executor artifact payload.
func visualArtifact(kind string, data []byte) map[string]any {
	sum := sha256.Sum256(data)
	return map[string]any{
		"kind": kind, "mime": "image/png", "filename": "",
		"base64": base64.StdEncoding.EncodeToString(data),
		"sha256": hex.EncodeToString(sum[:]),
	}
}

func decodeVisualArgs(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload struct {
		Action string         `json:"action"`
		Args   map[string]any `json:"args"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode action body: %v", err)
	}
	if payload.Action != "visual.compare" {
		t.Fatalf("action = %q, want visual.compare", payload.Action)
	}
	return payload.Args
}

func TestCompareVisualSendsBaselineAndDecodesResult(t *testing.T) {
	baseline := []byte("baseline-png-bytes")
	actual := []byte("actual-png-bytes")
	diff := []byte("diff-png-bytes")
	var gotArgs map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/contexts/ctx_1/actions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotArgs = decodeVisualArgs(t, body)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"result": map[string]any{
				"diff_ratio":                 0.012,
				"width":                      1280,
				"height":                     720,
				"environment_match":          true,
				"actual_width":               1280,
				"actual_height":              720,
				"actual_device_scale_factor": 1,
				"mismatch_boxes": []map[string]any{
					{"x": 10, "y": 20, "width": 30, "height": 40},
				},
			},
			"artifacts": []map[string]any{
				visualArtifact("visual_actual", actual),
				visualArtifact("visual_diff", diff),
			},
		})
	}))
	defer server.Close()

	client := browser.New(server.URL, testToken, time.Second)
	result, err := client.CompareVisual(context.Background(), application.ContextRef{ID: "ctx_1"}, application.VisualCompareRequest{
		Baseline:          baseline,
		Selector:          "#chart",
		Region:            &application.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3},
		Threshold:         0.02,
		Viewport:          application.Viewport{Width: 1280, Height: 720},
		DeviceScaleFactor: 1,
	})
	if err != nil {
		t.Fatalf("CompareVisual: %v", err)
	}
	if result.DiffRatio != 0.012 || result.Width != 1280 || result.Height != 720 ||
		!result.EnvironmentMatch || result.ActualWidth != 1280 ||
		result.ActualDeviceScaleFactor != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.MismatchBoxes) != 1 || result.MismatchBoxes[0] != (application.VisualMismatchBox{X: 10, Y: 20, Width: 30, Height: 40}) {
		t.Fatalf("mismatch boxes = %+v", result.MismatchBoxes)
	}
	if string(result.ActualPNG) != string(actual) || string(result.DiffPNG) != string(diff) {
		t.Fatalf("artifacts = %q / %q", result.ActualPNG, result.DiffPNG)
	}

	// The request carries the baseline copy and the configuration, never an
	// actual screenshot (the executor captures it).
	if gotArgs["baseline_base64"] != base64.StdEncoding.EncodeToString(baseline) {
		t.Fatalf("baseline arg = %v", gotArgs["baseline_base64"])
	}
	if gotArgs["selector"] != "#chart" || gotArgs["threshold"] != 0.02 || gotArgs["device_scale_factor"] != float64(1) {
		t.Fatalf("args = %+v", gotArgs)
	}
	if _, exists := gotArgs["actual_base64"]; exists {
		t.Fatalf("request leaked an actual screenshot: %+v", gotArgs)
	}
	region, ok := gotArgs["region"].(map[string]any)
	if !ok || region["x"] != 0.1 || region["width"] != 0.5 {
		t.Fatalf("region arg = %+v", gotArgs["region"])
	}
	viewport, ok := gotArgs["viewport"].(map[string]any)
	if !ok || viewport["width"] != float64(1280) || viewport["height"] != float64(720) {
		t.Fatalf("viewport arg = %+v", gotArgs["viewport"])
	}
}

func TestCompareVisualEnvironmentMismatchHasNoDiffArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"result": map[string]any{
				"diff_ratio": 0, "width": 0, "height": 0,
				"environment_match": false,
				"actual_width":      1920, "actual_height": 1080,
				"actual_device_scale_factor": 2,
			},
			"artifacts": []map[string]any{visualArtifact("visual_actual", []byte("actual"))},
		})
	}))
	defer server.Close()

	result, err := browser.New(server.URL, testToken, time.Second).CompareVisual(
		context.Background(), application.ContextRef{ID: "ctx_1"}, application.VisualCompareRequest{
			Baseline: []byte("base"), Threshold: 0.01,
			Viewport: application.Viewport{Width: 1280, Height: 720}, DeviceScaleFactor: 1,
		})
	if err != nil {
		t.Fatalf("CompareVisual: %v", err)
	}
	if result.EnvironmentMatch || result.ActualWidth != 1920 || result.DiffPNG != nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestCompareVisualProtocolErrorIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": map[string]any{"code": "invalid_arguments", "message": "bad region"},
		})
	}))
	defer server.Close()

	_, err := browser.New(server.URL, testToken, time.Second).CompareVisual(
		context.Background(), application.ContextRef{ID: "ctx_1"}, application.VisualCompareRequest{
			Baseline: []byte("base"), Threshold: 0.01,
			Viewport: application.Viewport{Width: 1280, Height: 720}, DeviceScaleFactor: 1,
		})
	assertBrowserErrorCode(t, err, "invalid_arguments")
}

func TestCompareVisualRejectsOversizedBaselineBeforeCalling(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()

	_, err := browser.New(server.URL, testToken, time.Second).CompareVisual(
		context.Background(), application.ContextRef{ID: "ctx_1"}, application.VisualCompareRequest{
			Baseline:          make([]byte, 6<<20),
			Threshold:         0.01,
			Viewport:          application.Viewport{Width: 1280, Height: 720},
			DeviceScaleFactor: 1,
			MaxBaselineBytes:  5 << 20,
		})
	assertBrowserErrorCode(t, err, "invalid_request")
	if calls != 0 {
		t.Fatalf("the executor was called %d times for an oversized baseline", calls)
	}
}

func TestCompareVisualRejectsInvalidConfiguration(t *testing.T) {
	client := browser.New("http://127.0.0.1:1", testToken, time.Second)
	cases := []application.VisualCompareRequest{
		{Baseline: nil, Threshold: 0.01, Viewport: application.Viewport{Width: 1280, Height: 720}, DeviceScaleFactor: 1},
		{Baseline: []byte("x"), Threshold: 0.5, Viewport: application.Viewport{Width: 1280, Height: 720}, DeviceScaleFactor: 1},
		{Baseline: []byte("x"), Threshold: 0.01, Viewport: application.Viewport{}, DeviceScaleFactor: 1},
	}
	for index, request := range cases {
		if _, err := client.CompareVisual(context.Background(), application.ContextRef{ID: "ctx_1"}, request); err == nil {
			t.Fatalf("configuration %d was accepted", index)
		}
	}
}
