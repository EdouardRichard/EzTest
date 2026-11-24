package adapters

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// v0.2.4-C T12 (P8 lift): the HTTP adapter no longer silently caps the response
// body at 1MiB (the old io.LimitReader). The FULL body is read; any bound is
// explicit downstream. The per-request timeout is configurable (default 15s).

// TestHTTPClientReadsFullBodyBeyond1MiB: a 2MiB response is returned complete,
// not silently truncated to 1MiB.
func TestHTTPClientReadsFullBodyBeyond1MiB(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 2<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	adapter := NewHTTPClient()
	response, err := adapter.Do(context.Background(), executionapp.HTTPRequestData{
		Method: "GET", URL: server.URL + "/large",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(response.Body) != len(body) {
		t.Fatalf("body length = %d, want the full %d (the 1MiB silent cap is lifted)", len(response.Body), len(body))
	}
	if !bytes.Equal(response.Body, body) {
		t.Fatalf("body content differs from the full response")
	}
}

// TestHTTPClientPerRequestTimeout: an explicit timeout_ms bounds the request;
// a slow server past the deadline yields the typed unavailable error.
func TestHTTPClientPerRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()
	adapter := NewHTTPClient()
	_, err := adapter.Do(context.Background(), executionapp.HTTPRequestData{
		Method: "GET", URL: server.URL + "/slow", TimeoutMS: 50,
	})
	if err == nil {
		t.Fatalf("expected a timeout error for a 50ms deadline against a 300ms server")
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v, want the typed timeout/unavailable error", err)
	}
}

// TestHTTPClientDefaultTimeoutServesFastRequest: with no timeout_ms the default
// (15s) applies and a fast request succeeds.
func TestHTTPClientDefaultTimeoutServesFastRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	adapter := NewHTTPClient()
	response, err := adapter.Do(context.Background(), executionapp.HTTPRequestData{
		Method: "GET", URL: server.URL + "/fast",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(response.Body) != "ok" || response.Status != 200 {
		t.Fatalf("response = %+v", response)
	}
}
