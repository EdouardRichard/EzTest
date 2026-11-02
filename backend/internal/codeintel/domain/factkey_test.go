package codeintel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// TestFactKeyNormalization pins the fact identity rules of spec §5.2:
// route/page keep the route name, api/client_call use "<METHOD> <normalizedPath>",
// component/symbol use "<repo path>#<name>", and invalid facts are dropped.
func TestFactKeyNormalization(t *testing.T) {
	cases := []struct {
		label          string
		kind           EntityKind
		path           string
		factName       string
		method         string
		normalizedPath string
		want           string
		ok             bool
	}{
		{"route", EntityRoute, "", "/orders", "", "", "route:/orders", true},
		{"route without leading slash", EntityRoute, "", "orders", "", "", "route:/orders", true},
		{"page with param", EntityPage, "", "/orders/:id", "", "", "page:/orders/:id", true},
		{"empty route name", EntityRoute, "", " ", "", "", "", false},
		{"api from name with query", EntityAPI, "src/api/orders.ts", "POST /api/orders/42?expand=1", "post", "", "api:POST /api/orders/{id}", true},
		{"api from normalized path", EntityAPI, "", "", "get", "/api//orders/42/", "api:GET /api/orders/{id}", true},
		{"client with uuid", EntityClientCall, "", "DELETE /api/customers/550e8400-e29b-41d4-a716-446655440000", "DELETE", "", "client:DELETE /api/customers/{id}", true},
		{"api already normalized", EntityAPI, "", "", "PUT", "/api/orders/{id}", "api:PUT /api/orders/{id}", true},
		{"api missing method", EntityAPI, "", "/api/orders", "", "", "", false},
		{"api invalid method", EntityAPI, "", "", "G3T", "/api/orders", "", false},
		{"api conflicting methods", EntityAPI, "", "POST /api/orders", "GET", "", "", false},
		{"component", EntityComponent, "src/pages/OrdersPage.tsx", "OrdersPage", "", "", "component:src/pages/OrdersPage.tsx#OrdersPage", true},
		{"symbol", EntitySymbol, "src/api/orders.ts", "loadOrders", "", "", "symbol:src/api/orders.ts#loadOrders", true},
		{"component escaping path", EntityComponent, "../etc/passwd", "OrdersPage", "", "", "", false},
		{"component absolute path", EntityComponent, "/src/pages/OrdersPage.tsx", "OrdersPage", "", "", "", false},
		{"component empty name", EntityComponent, "src/pages/OrdersPage.tsx", " ", "", "", "", false},
		{"file is not a fact kind", EntityFile, "src/main.go", "main.go", "", "", "", false},
		{"module is not a fact kind", EntityModule, "src/main.go", "main", "", "", "", false},
		{"over long key", EntitySymbol, "src/api/orders.ts", strings.Repeat("x", MaxFactKeyChars), "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, ok := FactKey(tc.kind, tc.path, tc.factName, tc.method, tc.normalizedPath)
			if ok != tc.ok {
				t.Fatalf("FactKey() ok = %v, want %v (key %q)", ok, tc.ok, got)
			}
			if got != tc.want {
				t.Fatalf("FactKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNormalizeAPIPath pins the normalizedPath rules: query/fragment removal,
// repeated/trailing slash folding, and numeric/UUID/long-hex segment collapse.
func TestNormalizeAPIPath(t *testing.T) {
	cases := map[string]string{
		"/api//orders/42/?tab=1#top":                       "/api/orders/{id}",
		"https://api.example.test/api/orders/42?q=1":       "/api/orders/{id}",
		"/api/orders/{id}":                                 "/api/orders/{id}",
		"/api/orders/550e8400-e29b-41d4-a716-446655440000": "/api/orders/{id}",
		"/api/orders/0123456789abcdef0123":                 "/api/orders/{id}",
		"/api/orders/abc":                                  "/api/orders/abc",
		"api/orders":                                       "/api/orders",
		"/":                                                "/",
		"":                                                 "",
		"/api/orders/../admin":                             "",
		"/api/bad path":                                    "",
		"/api/back\\slash":                                 "",
	}
	for raw, want := range cases {
		if got := NormalizeAPIPath(raw); got != want {
			t.Fatalf("NormalizeAPIPath(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestRepoPathRejectsEscape pins the T0 path safety rules of spec §5.1/§6.1:
// repo paths are relative slash paths and absolute/`..`/backslash/symlink
// displays are rejected.
func TestRepoPathRejectsEscape(t *testing.T) {
	valid := map[string]string{
		"src/main.go":         "src/main.go",
		"./src/main.go":       "src/main.go",
		"src//main.go":        "src/main.go",
		"src/a/./b.ts":        "src/a/b.ts",
		" src/with space.ts ": "src/with space.ts",
		"src/main.go/":        "src/main.go",
	}
	for raw, want := range valid {
		got, err := NormalizeRepoPath(raw)
		if err != nil {
			t.Fatalf("NormalizeRepoPath(%q) error = %v", raw, err)
		}
		if got != want {
			t.Fatalf("NormalizeRepoPath(%q) = %q, want %q", raw, got, want)
		}
	}

	invalid := []string{
		"",
		"/etc/passwd",
		"C:/repos/app",
		`C:\repos\app`,
		`src\main.go`,
		"..",
		"../secret",
		"src/../main.go",
		"src/../../etc/passwd",
		"src/\x00main.go",
		"src/link -> /etc/passwd",
		"src/\nmain.go",
	}
	for _, raw := range invalid {
		if got, err := NormalizeRepoPath(raw); err == nil {
			t.Fatalf("NormalizeRepoPath(%q) = %q, want error", raw, got)
		} else if !errors.Is(err, ErrInvalid) {
			t.Fatalf("NormalizeRepoPath(%q) error = %v, want ErrInvalid", raw, err)
		}
	}

	if got, err := NormalizeRepoPath(strings.Repeat("a", MaxPathChars+1)); err == nil {
		t.Fatalf("over-long path accepted as %q", got)
	}
}

// TestExcerptHash pins the evidence excerpt hash of spec §5.2: lines are
// CRLF-normalized, joined with a single LF and never carry a trailing newline.
func TestExcerptHash(t *testing.T) {
	empty := sha256.Sum256(nil)
	if got, want := ExcerptHash(nil), "sha256:"+hex.EncodeToString(empty[:]); got != want {
		t.Fatalf("ExcerptHash(nil) = %q, want %q", got, want)
	}

	// A 1-based closed interval start=2,end=3 selects indexes 1..2.
	lines := []string{"package codeintel\r", "func FactKey() {}\r", "}"}
	got := ExcerptHash(lines[1:3])
	want := "sha256:" + sha256Hex("func FactKey() {}\n}")
	if got != want {
		t.Fatalf("ExcerptHash(2..3) = %q, want %q", got, want)
	}

	// Trailing CR is normalized away and no trailing newline is added.
	if got, want := ExcerptHash([]string{"a\r"}), "sha256:"+sha256Hex("a"); got != want {
		t.Fatalf("CR normalization = %q, want %q", got, want)
	}
	if ExcerptHash([]string{"a"}) == ExcerptHash([]string{"a", ""}) {
		t.Fatal("a trailing empty line must change the hash")
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
