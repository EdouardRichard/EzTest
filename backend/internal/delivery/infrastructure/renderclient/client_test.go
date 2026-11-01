package renderclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/renderclient"
)

var samplePDF = []byte("%PDF-1.7\n" + strings.Repeat("x", 2048))

// newPDFServer starts a fake renderer that records the last request and answers
// with the configured status/body.
func newPDFServer(t *testing.T, status int, body []byte, contentType string) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var recordedRequest http.Request
	var recordedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedRequest = *r.Clone(context.Background())
		recordedBody, _ = io.ReadAll(r.Body)
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &recordedRequest, &recordedBody
}

func TestClientRenderPDFPostsHTMLAndReturnsBytes(t *testing.T) {
	server, recordedRequest, recordedBody := newPDFServer(t, http.StatusOK, samplePDF, "application/pdf")
	client := renderclient.New(server.URL, "render-token", 5*time.Second)

	html := []byte("<!doctype html><html><body>报告</body></html>")
	pdf, err := client.RenderPDF(context.Background(), html)
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if !bytes.Equal(pdf, samplePDF) {
		t.Fatalf("RenderPDF returned %d bytes, want %d", len(pdf), len(samplePDF))
	}
	if recordedRequest.Method != http.MethodPost || recordedRequest.URL.Path != "/v1/pdf" {
		t.Fatalf("request = %s %s, want POST /v1/pdf", recordedRequest.Method, recordedRequest.URL.Path)
	}
	if got := recordedRequest.Header.Get("Authorization"); got != "Bearer render-token" {
		t.Fatalf("Authorization = %q", got)
	}
	var body struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(*recordedBody, &body); err != nil {
		t.Fatalf("decode request body %q: %v", *recordedBody, err)
	}
	if body.HTML != string(html) {
		t.Fatalf("request html = %q", body.HTML)
	}
}

func TestClientRenderPDFMapsRendererErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantSentinel error
		wantMessage  string
	}{
		{name: "bad request", status: 400, body: `{"ok":false,"error":{"code":"html_required","message":"html must be a non-empty string"}}`, wantSentinel: deliveryapp.ErrRenderRejected, wantMessage: "html_required"},
		{name: "unauthorized", status: 401, body: `{"ok":false,"error":{"code":"unauthorized","message":"missing or invalid bearer token"}}`, wantSentinel: deliveryapp.ErrRenderRejected},
		{name: "render failed", status: 422, body: `{"ok":false,"error":{"code":"render_failed","message":"page crashed"}}`, wantSentinel: deliveryapp.ErrRenderRejected, wantMessage: "page crashed"},
		{name: "busy", status: 429, body: `{"ok":false,"error":{"code":"busy","message":"too many renders"}}`, wantSentinel: deliveryapp.ErrRenderUnavailable},
		{name: "timeout", status: 504, body: `{"ok":false,"error":{"code":"render_timeout","message":"render timed out"}}`, wantSentinel: deliveryapp.ErrRenderUnavailable},
		{name: "internal", status: 500, body: "boom", wantSentinel: deliveryapp.ErrRenderUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _ := newPDFServer(t, test.status, []byte(test.body), "application/json")
			client := renderclient.New(server.URL, "token", 2*time.Second)
			_, err := client.RenderPDF(context.Background(), []byte("<html></html>"))
			if !errors.Is(err, test.wantSentinel) {
				t.Fatalf("error = %v, want sentinel %v", err, test.wantSentinel)
			}
			if test.wantMessage != "" && !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("error = %v, want message %q", err, test.wantMessage)
			}
		})
	}
}

func TestClientRenderPDFRejectsNonPDFResponses(t *testing.T) {
	t.Run("wrong magic", func(t *testing.T) {
		server, _, _ := newPDFServer(t, http.StatusOK, []byte("<html>not a pdf</html>"), "application/pdf")
		client := renderclient.New(server.URL, "token", 2*time.Second)
		if _, err := client.RenderPDF(context.Background(), []byte("<html></html>")); !errors.Is(err, deliveryapp.ErrRenderRejected) {
			t.Fatalf("error = %v, want ErrRenderRejected", err)
		}
	})
	t.Run("empty response", func(t *testing.T) {
		server, _, _ := newPDFServer(t, http.StatusOK, nil, "application/pdf")
		client := renderclient.New(server.URL, "token", 2*time.Second)
		if _, err := client.RenderPDF(context.Background(), []byte("<html></html>")); !errors.Is(err, deliveryapp.ErrRenderRejected) {
			t.Fatalf("error = %v, want ErrRenderRejected", err)
		}
	})
	t.Run("oversized response", func(t *testing.T) {
		oversized := []byte("%PDF-" + strings.Repeat("x", 20*1024*1024))
		server, _, _ := newPDFServer(t, http.StatusOK, oversized, "application/pdf")
		client := renderclient.New(server.URL, "token", 10*time.Second)
		if _, err := client.RenderPDF(context.Background(), []byte("<html></html>")); !errors.Is(err, deliveryapp.ErrRenderRejected) {
			t.Fatalf("error = %v, want ErrRenderRejected", err)
		}
	})
}

func TestClientRenderPDFMapsTransportErrorsToUnavailable(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		client := renderclient.New("http://127.0.0.1:1", "token", time.Second)
		if _, err := client.RenderPDF(context.Background(), []byte("<html></html>")); !errors.Is(err, deliveryapp.ErrRenderUnavailable) {
			t.Fatalf("error = %v, want ErrRenderUnavailable", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(samplePDF)
		}))
		t.Cleanup(server.Close)
		client := renderclient.New(server.URL, "token", 30*time.Millisecond)
		if _, err := client.RenderPDF(context.Background(), []byte("<html></html>")); !errors.Is(err, deliveryapp.ErrRenderUnavailable) {
			t.Fatalf("error = %v, want ErrRenderUnavailable", err)
		}
	})
}

func TestClientRenderPDFRejectsInvalidInput(t *testing.T) {
	client := renderclient.New("http://127.0.0.1:1", "token", time.Second)
	if _, err := client.RenderPDF(context.Background(), nil); !errors.Is(err, deliveryapp.ErrRenderRejected) {
		t.Fatalf("empty html error = %v, want ErrRenderRejected", err)
	}
	invalid := renderclient.New("not a url", "token", time.Second)
	if _, err := invalid.RenderPDF(context.Background(), []byte("<html></html>")); !errors.Is(err, deliveryapp.ErrRenderRejected) {
		t.Fatalf("invalid URL error = %v, want ErrRenderRejected", err)
	}
}
