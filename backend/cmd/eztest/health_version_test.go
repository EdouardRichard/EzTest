package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/server"
)

// semverPattern accepts the release identities the packaging pipeline stamps:
// MAJOR.MINOR.PATCH with an optional prerelease suffix.
var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

// TestHealthVersionSingleSource proves the repository-root VERSION file is the
// single authority for the release identity.
//
// It asserts two invariants that keep that authority honest:
//
//  1. the composition-root default stays "dev", so a build the release pipeline
//     did not stamp reports "dev" rather than a stale hardcoded version;
//  2. the repository-root VERSION file holds a valid semver, which is the value
//     the release pipeline injects with
//     -X github.com/EdouardRichard/EzTest/backend/cmd/eztest.version=$(VERSION).
//
// GET /api/health must report the value the composition root was built with.
func TestHealthVersionSingleSource(t *testing.T) {
	if version != "dev" {
		t.Fatalf("composition-root version default = %q, want %q (release builds must inject VERSION)", version, "dev")
	}

	versionFile := filepath.Join("..", "..", "..", "VERSION")
	raw, err := os.ReadFile(versionFile)
	if err != nil {
		t.Fatalf("read repository-root VERSION: %v", err)
	}
	stamped := strings.TrimSpace(string(raw))
	if !semverPattern.MatchString(stamped) {
		t.Fatalf("VERSION %q is not a valid semver", stamped)
	}

	// No database is wired in this unit test, so readiness is 503; the health
	// envelope still reports the release identity the composition root passes.
	rec := httptest.NewRecorder()
	server.New(server.Deps{Version: version}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want 503 (no db in this unit test)", rec.Code)
	}
	var body struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/health: %v", err)
	}
	if body.Data.Version != version {
		t.Fatalf("/api/health version = %q, want %q (the composition-root value)", body.Data.Version, version)
	}
}
