package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/httpclient"
)

// newTestServer starts an httptest server and registers its cleanup.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// testHost returns the allowlist entry of one server URL.
func testHost(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	return parsed.Hostname()
}

// newTestClient builds a client that allows the given hosts.
func newTestClient(hosts ...string) *httpclient.Client {
	return httpclient.New(httpclient.Options{
		AllowedHosts: hosts,
		Timeout:      2 * time.Second,
	})
}

func TestClientFetchParsesHTML(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html>
<html><head><title> P5 &amp; Home </title>
<script src="/static/app.js"></script>
</head><body>
<a href="/orders">Orders</a>
<a href='/orders/1'>One</a>
<form method="get" action="/orders"><input name="q" /></form>
</body></html>`)
	})
	client := newTestClient(testHost(t, server.URL))

	got, err := client.Fetch(context.Background(), server.URL+"/", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", got.Status)
	}
	if !strings.Contains(got.ContentType, "text/html") {
		t.Fatalf("content type = %q", got.ContentType)
	}
	if got.Title != "P5 & Home" {
		t.Fatalf("title = %q, want %q", got.Title, "P5 & Home")
	}
	if len(got.Links) != 2 || got.Links[0] != "/orders" || got.Links[1] != "/orders/1" {
		t.Fatalf("links = %#v", got.Links)
	}
	if len(got.Forms) != 1 || got.Forms[0].Action != "/orders" || !strings.EqualFold(got.Forms[0].Method, "get") {
		t.Fatalf("forms = %#v", got.Forms)
	}
	if len(got.Scripts) != 1 || got.Scripts[0] != "/static/app.js" {
		t.Fatalf("scripts = %#v", got.Scripts)
	}
	if len(got.Body) == 0 {
		t.Fatal("body is empty")
	}
	if got.Truncated {
		t.Fatal("unexpected body truncation")
	}
	if _, err := url.Parse(got.URL); err != nil {
		t.Fatalf("result url %q: %v", got.URL, err)
	}
}

func TestClientAllowsOnlyGETAndHEAD(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>ok</title>")
	})
	client := newTestClient(testHost(t, server.URL))

	if _, err := client.FetchMethod(context.Background(), http.MethodPost, server.URL+"/", ""); !errors.Is(err, httpclient.ErrMethodNotAllowed) {
		t.Fatalf("POST error = %v, want ErrMethodNotAllowed", err)
	}
	head, err := client.FetchMethod(context.Background(), http.MethodHead, server.URL+"/", "")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if head.Status != http.StatusOK {
		t.Fatalf("HEAD status = %d", head.Status)
	}
	if len(head.Body) != 0 {
		t.Fatalf("HEAD body = %d bytes, want 0", len(head.Body))
	}
}

func TestClientRejectsUnsafeURLs(t *testing.T) {
	client := newTestClient("127.0.0.1")
	cases := []string{
		"file:///etc/passwd",
		"ftp://127.0.0.1/",
		"http://user:secret@127.0.0.1/",
	}
	for _, raw := range cases {
		if _, err := client.Fetch(context.Background(), raw, ""); !errors.Is(err, httpclient.ErrURLNotAllowed) {
			t.Fatalf("Fetch(%q) error = %v, want ErrURLNotAllowed", raw, err)
		}
	}
}

func TestClientRejectsHostOutsideAllowlist(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should not be reached")
	})
	client := newTestClient("example.test")

	if _, err := client.Fetch(context.Background(), server.URL+"/", ""); !errors.Is(err, httpclient.ErrHostNotAllowed) {
		t.Fatalf("error = %v, want ErrHostNotAllowed", err)
	}
}

func TestClientRejectsLoopbackUnlessIPAllowlisted(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<title>loopback</title>")
	})
	// A hostname allowlist entry does not unblock a resolved private/loopback
	// address: only an explicit IP entry does (spec §6.9 SSRF rule).
	loopbackURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	client := newTestClient("localhost")
	if _, err := client.Fetch(context.Background(), loopbackURL, ""); !errors.Is(err, httpclient.ErrPrivateIP) {
		t.Fatalf("localhost error = %v, want ErrPrivateIP", err)
	}

	// An explicit loopback IP in the allowlist is the documented test escape
	// hatch and must work.
	allowed := newTestClient("127.0.0.1")
	if _, err := allowed.Fetch(context.Background(), server.URL+"/", ""); err != nil {
		t.Fatalf("explicitly allowlisted loopback: %v", err)
	}
}

func TestClientRejectsCrossHostRedirect(t *testing.T) {
	target := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<title>target</title>")
	})
	source := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	})
	client := newTestClient("127.0.0.1")

	if _, err := client.Fetch(context.Background(), source.URL+"/", ""); !errors.Is(err, httpclient.ErrCrossHostRedirect) {
		t.Fatalf("error = %v, want ErrCrossHostRedirect", err)
	}
}

func TestClientFollowsSameHostRedirect(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			fmt.Fprint(w, "<title>Final</title>")
		default:
			http.NotFound(w, r)
		}
	})
	client := newTestClient(testHost(t, server.URL))

	got, err := client.Fetch(context.Background(), server.URL+"/", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Title != "Final" {
		t.Fatalf("title = %q, want Final", got.Title)
	}
	if !strings.HasSuffix(got.URL, "/final") {
		t.Fatalf("final url = %q, want /final suffix", got.URL)
	}
}

func TestClientTruncatesOversizedBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<title>big</title>%s", strings.Repeat("x", 4096))
	})
	client := httpclient.New(httpclient.Options{
		AllowedHosts: []string{testHost(t, server.URL)},
		Timeout:      2 * time.Second,
		MaxBodyBytes: 64,
	})

	got, err := client.Fetch(context.Background(), server.URL+"/", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(got.Body) != 64 {
		t.Fatalf("body = %d bytes, want 64", len(got.Body))
	}
}

func TestClientSendsCookieHeader(t *testing.T) {
	const want = "fixture_session=ok; theme=dark"
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.Header.Get("Cookie") == want {
			fmt.Fprint(w, "<title>with-cookie</title>")
			return
		}
		fmt.Fprint(w, "<title>missing-cookie</title>")
	})
	client := newTestClient(testHost(t, server.URL))

	withCookie, err := client.Fetch(context.Background(), server.URL+"/", want)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if withCookie.Title != "with-cookie" {
		t.Fatalf("title = %q, want with-cookie", withCookie.Title)
	}
	withoutCookie, err := client.Fetch(context.Background(), server.URL+"/", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if withoutCookie.Title != "missing-cookie" {
		t.Fatalf("title = %q, want missing-cookie", withoutCookie.Title)
	}
}

func TestClientHonorsTimeout(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "<title>slow</title>")
	})
	client := httpclient.New(httpclient.Options{
		AllowedHosts: []string{testHost(t, server.URL)},
		Timeout:      30 * time.Millisecond,
	})

	started := time.Now()
	if _, err := client.Fetch(context.Background(), server.URL+"/", ""); err == nil {
		t.Fatal("Fetch succeeded, want timeout error")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestClientProbeMapsResult(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html>
<html><head><title>Probe &amp; Mapping</title>
<script src="/static/app.js"></script>
</head><body>
<a href="/orders">Orders</a>
<a href='/orders/1'>One</a>
<form method="get" action="/orders"><input name="q" /></form>
<form method="post" action="/orders/save"><input name="n" /></form>
</body></html>`)
	})
	client := newTestClient(testHost(t, server.URL))

	got, err := client.Probe(context.Background(), server.URL+"/", "")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", got.Status)
	}
	if !strings.Contains(got.ContentType, "text/html") {
		t.Fatalf("content type = %q", got.ContentType)
	}
	if got.Title != "Probe & Mapping" {
		t.Fatalf("title = %q, want %q", got.Title, "Probe & Mapping")
	}
	if len(got.Links) != 2 || got.Links[0] != "/orders" || got.Links[1] != "/orders/1" {
		t.Fatalf("links = %#v", got.Links)
	}
	wantForms := []string{"GET /orders", "POST /orders/save"}
	if len(got.Forms) != len(wantForms) || got.Forms[0] != wantForms[0] || got.Forms[1] != wantForms[1] {
		t.Fatalf("forms = %#v, want %#v", got.Forms, wantForms)
	}
	if len(got.Scripts) != 1 || got.Scripts[0] != "/static/app.js" {
		t.Fatalf("scripts = %#v", got.Scripts)
	}
	if !strings.Contains(got.Body, `<title>Probe &amp; Mapping</title>`) {
		t.Fatalf("body = %q, want the raw HTML body string", got.Body)
	}
	if got.Truncated {
		t.Fatal("unexpected body truncation")
	}
	if _, err := url.Parse(got.URL); err != nil {
		t.Fatalf("result url %q: %v", got.URL, err)
	}
}

func TestClientProbeTruncatesBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<title>big</title>%s", strings.Repeat("x", 4096))
	})
	client := httpclient.New(httpclient.Options{
		AllowedHosts: []string{testHost(t, server.URL)},
		Timeout:      2 * time.Second,
		MaxBodyBytes: 64,
	})

	got, err := client.Probe(context.Background(), server.URL+"/", "")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(got.Body) != 64 {
		t.Fatalf("body = %d bytes, want 64", len(got.Body))
	}
}

func TestClientProbeRejectsUnsafeTargets(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should not be reached")
	})
	loopbackURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	cases := []struct {
		name   string
		client *httpclient.Client
		rawURL string
	}{
		{name: "scheme", client: newTestClient("127.0.0.1"), rawURL: "file:///etc/passwd"},
		{name: "credentials", client: newTestClient("127.0.0.1"), rawURL: "http://user:secret@127.0.0.1/"},
		{name: "host", client: newTestClient("example.test"), rawURL: server.URL + "/"},
		{name: "loopback", client: newTestClient("localhost"), rawURL: loopbackURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.client.Probe(context.Background(), tc.rawURL, ""); !errors.Is(err, application.ErrProbeTargetNotAllowed) {
				t.Fatalf("Probe(%q) error = %v, want ErrProbeTargetNotAllowed", tc.rawURL, err)
			}
		})
	}
}
