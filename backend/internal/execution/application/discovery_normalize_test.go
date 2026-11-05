package executionapp_test

import (
	"strings"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// TestNormalizeResourceTemplate pins the R-EX-2 resource template: numeric and
// UUID path segments become {id}, the query is discarded, the host is
// lowercased and static resources are filtered.
func TestNormalizeResourceTemplate(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		method   string
		rawURL   string
		wantHost string
		want     string
		wantOK   bool
	}{
		{
			name: "page numeric id and query", kind: "page", rawURL: "http://sut.test/orders/42?tab=x",
			wantHost: "sut.test", want: "/orders/{id}", wantOK: true,
		},
		{
			name: "api method prefixed path", kind: "api", method: "GET", rawURL: "http://sut.test/api/orders/42?q=1",
			wantHost: "sut.test", want: "GET /api/orders/{id}", wantOK: true,
		},
		{
			name: "hash route structure kept", kind: "page", rawURL: "http://sut.test/app#/reports/42",
			wantHost: "sut.test", want: "/app#/reports/{id}", wantOK: true,
		},
		{
			name: "hash route query dropped", kind: "page", rawURL: "http://sut.test/app#/reports/42?tab=x",
			wantHost: "sut.test", want: "/app#/reports/{id}", wantOK: true,
		},
		{
			name: "uuid segment", kind: "page",
			rawURL:   "http://sut.test/orders/550e8400-e29b-41d4-a716-446655440000",
			wantHost: "sut.test", want: "/orders/{id}", wantOK: true,
		},
		{
			name: "uppercase host", kind: "page", rawURL: "http://SUT.Example.COM/home",
			wantHost: "sut.example.com", want: "/home", wantOK: true,
		},
		{
			name: "empty path", kind: "page", rawURL: "http://sut.test",
			wantHost: "sut.test", want: "/", wantOK: true,
		},
		{
			name: "matrix param stripped", kind: "page", rawURL: "http://sut.test/orders/42;jsessionid=abc",
			wantHost: "sut.test", want: "/orders/{id}", wantOK: true,
		},
		{
			name: "page keeps no method", kind: "page", method: "GET", rawURL: "http://sut.test/home",
			wantHost: "sut.test", want: "/home", wantOK: true,
		},
		{
			name: "static script filtered", kind: "api", method: "GET", rawURL: "http://sut.test/static/app.js",
			wantOK: false,
		},
		{
			name: "static stylesheet filtered", kind: "page", rawURL: "http://sut.test/static/app.css",
			wantOK: false,
		},
		{
			name: "static image filtered", kind: "page", rawURL: "http://sut.test/logo.png",
			wantOK: false,
		},
		{
			name: "static font filtered", kind: "page", rawURL: "http://sut.test/font.woff2",
			wantOK: false,
		},
		{
			name: "static source map filtered", kind: "api", method: "GET", rawURL: "http://sut.test/app.js.map",
			wantOK: false,
		},
		{
			name: "static icon filtered", kind: "page", rawURL: "http://sut.test/favicon.ico",
			wantOK: false,
		},
		{
			name: "html document is not static", kind: "page", rawURL: "http://sut.test/legacy.html",
			wantHost: "sut.test", want: "/legacy.html", wantOK: true,
		},
		{
			name: "non http url rejected", kind: "page", rawURL: "not a url",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, template, ok := executionapp.NormalizeResourceTemplate(tc.kind, tc.method, tc.rawURL)
			if ok != tc.wantOK || host != tc.wantHost || template != tc.want {
				t.Fatalf("NormalizeResourceTemplate(%q, %q, %q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.kind, tc.method, tc.rawURL, host, template, ok, tc.wantHost, tc.want, tc.wantOK)
			}
		})
	}
}

// TestNormalizeResourceTemplateDropsSensitiveParts proves the template/host
// never carry a token, query value, userinfo or fragment query.
func TestNormalizeResourceTemplateDropsSensitiveParts(t *testing.T) {
	host, template, ok := executionapp.NormalizeResourceTemplate(
		"page", "", "https://alice:pw@Shop.Example.com/orders/42?token=secret#/receipt?token=secret",
	)
	if !ok {
		t.Fatal("page observation was filtered")
	}
	if host != "shop.example.com" {
		t.Fatalf("host = %q, want shop.example.com", host)
	}
	if template != "/orders/{id}#/receipt" {
		t.Fatalf("template = %q, want /orders/{id}#/receipt", template)
	}
	for _, leaked := range []string{"alice", "pw", "token", "secret", "?"} {
		if strings.Contains(host, leaked) || strings.Contains(template, leaked) {
			t.Fatalf("normalized output leaked %q: host=%q template=%q", leaked, host, template)
		}
	}
}

// TestNormalizeResourceTemplateAPIMethod pins the api template shape: the
// uppercase method is prefixed to the path template.
func TestNormalizeResourceTemplateAPIMethod(t *testing.T) {
	host, template, ok := executionapp.NormalizeResourceTemplate("api", "post", "http://sut.test/api/orders/42")
	if !ok {
		t.Fatal("api observation was filtered")
	}
	if host != "sut.test" || template != "POST /api/orders/{id}" {
		t.Fatalf("host=%q template=%q, want sut.test and POST /api/orders/{id}", host, template)
	}
}
