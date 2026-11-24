package versionapp_test

import (
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// TestBindingUsesApprovedEnvironmentResources pins the resource ownership rule
// after the v0.2.4-B inversion: the version range is the set of approved
// pages/APIs and active databases of every SUT of the bound environment. A
// resource proposed by discovery is not silently added to the version scope (it
// becomes effective only after approval, R-C6); the binding read never mutates
// the catalog status.
func TestBindingUsesApprovedEnvironmentResources(t *testing.T) {
	env := newTestEnv(t)

	view := createVersion(t, env, "发现资源")
	binding := view.Binding
	// SUT 1001 owns the approved pages 1301/1302 and SUT 1002 owns 1303; the
	// proposed page 1304 stays out of the effective scope.
	if len(binding.Pages) != 3 {
		t.Fatalf("pages = %d (%+v), want the three approved pages", len(binding.Pages), binding.Pages)
	}
	for _, page := range binding.Pages {
		if page.Status != "approved" {
			t.Fatalf("page %+v is not approved", page)
		}
	}
	if len(binding.APIs) != 2 || binding.APIs[0].ID != "1401" || binding.APIs[1].ID != "1402" {
		t.Fatalf("apis = %#v", binding.APIs)
	}
	// 1503 is disabled, so only the two active databases are in range.
	if len(binding.Databases) != 2 {
		t.Fatalf("databases = %#v", binding.Databases)
	}
	for _, database := range binding.Databases {
		if database.Status != "active" {
			t.Fatalf("database %+v is not active", database)
		}
	}

	// Every effective asset traces back to a SUT of the bound environment.
	sutIDs := map[string]bool{}
	for _, sut := range binding.Environment.SUTs {
		sutIDs[sut.ID] = true
	}
	for _, page := range binding.Pages {
		if !sutIDs[page.SUTID] {
			t.Fatalf("page %+v belongs to SUT %q outside the bound environment", page, page.SUTID)
		}
	}

	// The catalog still carries the proposed resources untouched: the binding
	// read is a projection, never a destructive rewrite.
	if proposed, ok := env.catalog.pages[1304]; !ok || proposed.Status != "proposed" {
		t.Fatalf("catalog proposed page = %#v", proposed)
	}
	if proposed, ok := env.catalog.apis[1403]; !ok || proposed.Status != "proposed" {
		t.Fatalf("catalog proposed api = %#v", proposed)
	}

	// Nothing but the environment id and the account subset is persisted: the
	// derived scope is recomputed from the catalog on every read.
	if got := storedEnvironment(t, env, view.ID); got != 1101 {
		t.Fatalf("stored environment_id = %d", got)
	}
	if accounts := storedAccounts(t, env, view.ID); len(accounts) != 3 {
		t.Fatalf("stored accounts = %#v", accounts)
	}
}

// TestVersionBindingHasResources is the design/execution half of the resource
// contract: once a version binds an environment, the version read model exposes
// the page locator inventory (name + route template) and the API shapes that the
// agent context ("AI design") and the execution SUT adapter consume.
func TestVersionBindingHasResources(t *testing.T) {
	env := newTestEnv(t)
	view := createVersion(t, env, "页面/API 资源")

	binding := view.Binding
	if len(binding.Pages) == 0 {
		t.Fatal("version page count = 0, want > 0")
	}
	if len(binding.APIs) == 0 {
		t.Fatal("version API count = 0, want > 0")
	}
	page := binding.Pages[0]
	if page.RouteTemplate == "" {
		t.Fatalf("page %+v has no route template: the design locator key is missing", page)
	}
	api := binding.APIs[0]
	if api.Method == "" || api.PathTemplate == "" {
		t.Fatalf("api %+v has no method/path: the design/execution API shape is missing", api)
	}
	// The per-SUT base_url is what the execution SUT adapter dials; it moved from
	// the environment to the SUT in v0.2.4-B.
	for _, sut := range binding.Environment.SUTs {
		if sut.BaseURL == "" {
			t.Fatalf("SUT %+v has no base_url", sut)
		}
	}
	var _ resourceapp.CatalogReader = env.catalog
}
