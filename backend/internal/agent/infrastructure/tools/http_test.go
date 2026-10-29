package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// maxTestHTTPBody mirrors the 1 MB http.get response cap.
const maxTestHTTPBody = 1 << 20

// testHTTPTool builds an http.get tool for one test server.
func testHTTPTool(t *testing.T, allowlist []string, timeout time.Duration) domain.Tool {
	t.Helper()
	return NewHTTPGet(allowlist, timeout)
}

// jsonURLArgs builds the http.get arguments for rawURL.
func jsonURLArgs(rawURL string) string {
	return fmt.Sprintf(`{"url":%q}`, rawURL)
}

func TestHTTPGetToolContract(t *testing.T) {
	tool := NewHTTPGet(nil, 0)
	if got := tool.Name(); got != "http.get" {
		t.Fatalf("Name()=%q want http.get", got)
	}
	if tool.Description() == "" {
		t.Fatal("Description() is empty")
	}

	var schema testToolSchema
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("http.get schema is not JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Fatalf("http.get schema type=%q want object", schema.Type)
	}
	if got := schema.Properties["url"].Type; got != "string" {
		t.Fatalf("http.get url type=%q want string", got)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "url" {
		t.Fatalf("http.get required=%v want [url]", schema.Required)
	}

	hg, ok := tool.(*httpGet)
	if !ok {
		t.Fatalf("NewHTTPGet returned %T, want *httpGet", tool)
	}
	if hg.client.Timeout != 5*time.Second {
		t.Fatalf("default timeout=%s want 5s", hg.client.Timeout)
	}

	configured, ok := NewHTTPGet(nil, 2*time.Second).(*httpGet)
	if !ok {
		t.Fatalf("NewHTTPGet returned %T, want *httpGet", tool)
	}
	if configured.client.Timeout != 2*time.Second {
		t.Fatalf("configured timeout=%s want 2s", configured.client.Timeout)
	}
}

func TestHTTPGetFailsClosedWithoutAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "unreachable")
	}))
	t.Cleanup(server.Close)

	tool := testHTTPTool(t, nil, time.Second)
	_, err := executeTool(t, tool, jsonURLArgs(server.URL))
	requireToolErrorCode(t, err, "denied")
}

func TestHTTPGetAllowlistAndRedirects(t *testing.T) {
	var blockedHits atomic.Int32
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blockedHits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "secret")
	}))
	t.Cleanup(blocked.Close)

	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect-to-blocked":
			http.Redirect(w, r, blocked.URL+"/leak", http.StatusFound)
			return
		case "/three/0":
			http.Redirect(w, r, "/three/1", http.StatusFound)
			return
		case "/three/1":
			http.Redirect(w, r, "/three/2", http.StatusFound)
			return
		case "/three/2":
			http.Redirect(w, r, "/three/final", http.StatusFound)
			return
		case "/three/final":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "landed")
			return
		case "/four/0":
			http.Redirect(w, r, "/four/1", http.StatusFound)
			return
		case "/four/1":
			http.Redirect(w, r, "/four/2", http.StatusFound)
			return
		case "/four/2":
			http.Redirect(w, r, "/four/3", http.StatusFound)
			return
		case "/four/3":
			http.Redirect(w, r, "/four/final", http.StatusFound)
			return
		default:
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(allowed.Close)

	hostPort := allowed.Listener.Addr().String()
	tool := testHTTPTool(t, []string{hostPort}, time.Second)

	result, err := executeTool(t, tool, jsonURLArgs(allowed.URL+"/ok"))
	if err != nil {
		t.Fatalf("http.get allowed URL error: %v", err)
	}
	if result.IsError || result.Content != "ok" {
		t.Fatalf("http.get allowed URL result=%+v want content ok", result)
	}

	// A same-host, different-port server is not covered by a host:port entry.
	if _, err := executeTool(t, tool, jsonURLArgs(blocked.URL)); err == nil {
		t.Fatal("http.get allowed a host that is not in the allowlist")
	} else {
		requireToolErrorCode(t, err, "denied")
	}
	if got := blockedHits.Load(); got != 0 {
		t.Fatalf("http.get sent %d requests to the blocked server", got)
	}

	// Every redirect hop is re-checked against the allowlist.
	if _, err := executeTool(t, tool, jsonURLArgs(allowed.URL+"/redirect-to-blocked")); err == nil {
		t.Fatal("http.get followed a redirect to a non-allowlisted host")
	} else {
		requireToolErrorCode(t, err, "denied")
	}
	if got := blockedHits.Load(); got != 0 {
		t.Fatalf("http.get sent %d requests to the blocked redirect target", got)
	}

	// Up to three redirects are followed.
	result, err = executeTool(t, tool, jsonURLArgs(allowed.URL+"/three/0"))
	if err != nil {
		t.Fatalf("http.get three redirects error: %v", err)
	}
	if result.Content != "landed" {
		t.Fatalf("http.get three redirects content=%q want landed", result.Content)
	}

	// A fourth redirect is rejected.
	if _, err := executeTool(t, tool, jsonURLArgs(allowed.URL+"/four/0")); err == nil {
		t.Fatal("http.get followed more than three redirects")
	} else {
		requireToolErrorCode(t, err, "denied")
	}

	t.Run("host only allowlist entry matches any port", func(t *testing.T) {
		hostOnly := testHTTPTool(t, []string{"127.0.0.1"}, time.Second)
		result, err := executeTool(t, hostOnly, jsonURLArgs(allowed.URL+"/ok"))
		if err != nil {
			t.Fatalf("http.get host-only allowlist error: %v", err)
		}
		if result.Content != "ok" {
			t.Fatalf("http.get host-only allowlist content=%q want ok", result.Content)
		}
	})

	t.Run("allowlist entries are trimmed", func(t *testing.T) {
		spaced := testHTTPTool(t, []string{"  " + hostPort + "  "}, time.Second)
		if _, err := executeTool(t, spaced, jsonURLArgs(allowed.URL+"/ok")); err != nil {
			t.Fatalf("http.get trimmed allowlist entry error: %v", err)
		}
	})
}

func TestHTTPGetAllowsHTTPS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "secure ok")
	}))
	t.Cleanup(server.Close)

	tool := testHTTPTool(t, []string{server.Listener.Addr().String()}, time.Second)
	hg, ok := tool.(*httpGet)
	if !ok {
		t.Fatalf("NewHTTPGet returned %T, want *httpGet", tool)
	}
	// Trust the test server's certificate without weakening production.
	hg.client.Transport = server.Client().Transport

	result, err := executeTool(t, tool, jsonURLArgs(server.URL))
	if err != nil {
		t.Fatalf("http.get https error: %v", err)
	}
	if result.Content != "secure ok" {
		t.Fatalf("http.get https content=%q want secure ok", result.Content)
	}
}

func TestHTTPGetRejectsCredentialsAndNonGET(t *testing.T) {
	var mu sync.Mutex
	var methods, authorizations, cookies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		cookies = append(cookies, r.Header.Get("Cookie"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)

	tool := testHTTPTool(t, []string{server.Listener.Addr().String()}, time.Second)

	// URL userinfo is refused even when the host is allowlisted.
	userinfoURL := strings.Replace(server.URL, "http://", "http://user:pass@", 1)
	if _, err := executeTool(t, tool, jsonURLArgs(userinfoURL)); err == nil {
		t.Fatal("http.get accepted a URL with userinfo credentials")
	} else {
		requireToolErrorCode(t, err, "invalid_arguments")
	}

	invalid := []struct {
		name string
		args string
	}{
		{name: "ftp scheme", args: `{"url":"ftp://127.0.0.1/resource"}`},
		{name: "file scheme", args: `{"url":"file:///C:/Windows/win.ini"}`},
		{name: "scheme without host", args: `{"url":"http:///no-host"}`},
		{name: "not a URL", args: `{"url":"not-a-url"}`},
		{name: "empty URL", args: `{"url":""}`},
		{name: "missing URL", args: `{}`},
		{name: "non-string URL", args: `{"url":12}`},
		{name: "malformed arguments", args: `{"url":`},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := executeTool(t, tool, tc.args); err == nil {
				t.Fatalf("http.get %s returned no error", tc.args)
			} else {
				requireToolErrorCode(t, err, "invalid_arguments")
			}
		})
	}

	// The tool only ever sends GET; extra arguments cannot select a method.
	result, err := executeTool(t, tool, fmt.Sprintf(`{"url":%q,"method":"POST"}`, server.URL+"/echo"))
	if err != nil {
		t.Fatalf("http.get with extra arguments error: %v", err)
	}
	if result.Content != "ok" {
		t.Fatalf("http.get with extra arguments content=%q want ok", result.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 1 || methods[0] != http.MethodGet {
		t.Fatalf("server observed methods=%v want exactly one GET", methods)
	}
	if authorizations[0] != "" || cookies[0] != "" {
		t.Fatalf("http.get leaked credentials: Authorization=%q Cookie=%q", authorizations[0], cookies[0])
	}
}

func TestHTTPGetCapsBodyAndRejectsBinary(t *testing.T) {
	big := strings.Repeat("x", 2*maxTestHTTPBody)
	exact := strings.Repeat("y", maxTestHTTPBody)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, big)
		case "/exact":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, exact)
		case "/binary":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0x00, 0x01, 0x02, 0xff})
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"ok":true}`)
		case "/xml":
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, "<ok/>")
		case "/yaml":
			w.Header().Set("Content-Type", "application/yaml")
			io.WriteString(w, "ok: true")
		case "/no-content-type":
			w.Header()["Content-Type"] = []string{""}
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "untyped")
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "plain")
		}
	}))
	t.Cleanup(server.Close)

	tool := testHTTPTool(t, []string{server.Listener.Addr().String()}, time.Second)

	t.Run("truncates bodies over 1MB", func(t *testing.T) {
		result, err := executeTool(t, tool, jsonURLArgs(server.URL+"/big"))
		if err != nil {
			t.Fatalf("http.get big error: %v", err)
		}
		if result.IsError {
			t.Fatalf("http.get big IsError=true content=%q", result.Content)
		}
		if !strings.HasSuffix(result.Content, "...[truncated]") {
			t.Fatalf("http.get big content does not end with the truncation marker")
		}
		if want := maxTestHTTPBody + len("\n...[truncated]"); len(result.Content) != want {
			t.Fatalf("http.get big content length=%d want %d", len(result.Content), want)
		}
	})

	t.Run("keeps bodies at the limit intact", func(t *testing.T) {
		result, err := executeTool(t, tool, jsonURLArgs(server.URL+"/exact"))
		if err != nil {
			t.Fatalf("http.get exact error: %v", err)
		}
		if strings.Contains(result.Content, "...[truncated]") {
			t.Fatal("http.get truncated a body exactly at the limit")
		}
		if len(result.Content) != maxTestHTTPBody {
			t.Fatalf("http.get exact content length=%d want %d", len(result.Content), maxTestHTTPBody)
		}
	})

	t.Run("rejects binary content types", func(t *testing.T) {
		if _, err := executeTool(t, tool, jsonURLArgs(server.URL+"/binary")); err == nil {
			t.Fatal("http.get accepted application/octet-stream")
		} else {
			requireToolErrorCode(t, err, "unsupported_content")
		}
	})

	t.Run("rejects a missing content type", func(t *testing.T) {
		if _, err := executeTool(t, tool, jsonURLArgs(server.URL+"/no-content-type")); err == nil {
			t.Fatal("http.get accepted a response without a content type")
		} else {
			requireToolErrorCode(t, err, "unsupported_content")
		}
	})

	accepted := []struct {
		name string
		path string
		want string
	}{
		{name: "text/plain", path: "/plain", want: "plain"},
		{name: "application/json", path: "/json", want: `{"ok":true}`},
		{name: "application/xml", path: "/xml", want: "<ok/>"},
		{name: "application/yaml", path: "/yaml", want: "ok: true"},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			result, err := executeTool(t, tool, jsonURLArgs(server.URL+tc.path))
			if err != nil {
				t.Fatalf("http.get %s error: %v", tc.path, err)
			}
			if result.Content != tc.want {
				t.Fatalf("http.get %s content=%q want %q", tc.path, result.Content, tc.want)
			}
		})
	}
}

func TestHTTPGetHonorsTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "slow")
	}))
	t.Cleanup(server.Close)

	t.Run("timeout", func(t *testing.T) {
		tool := testHTTPTool(t, []string{server.Listener.Addr().String()}, 50*time.Millisecond)
		started := time.Now()
		_, err := executeTool(t, tool, jsonURLArgs(server.URL))
		requireToolErrorCode(t, err, "timeout")
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("http.get took %s, the 50ms timeout did not fire", elapsed)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		tool := testHTTPTool(t, []string{server.Listener.Addr().String()}, time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := tool.Execute(ctx, json.RawMessage(jsonURLArgs(server.URL)))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("http.get err=%v want context.Canceled", err)
		}
	})
}
