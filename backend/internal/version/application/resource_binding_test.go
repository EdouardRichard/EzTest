package versionapp_test

import (
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// TestBindingUsesApprovedEnvironmentResources pins the v0.22 C-1 resource
// ownership: the version range is the environment's approved pages/APIs/
// databases. A resource proposed by discovery is not silently added to the
// version scope (it becomes effective only after approval, R-C6); the binding
// write path never mutates the catalog status.
func TestBindingUsesApprovedEnvironmentResources(t *testing.T) {
	env := newTestEnv(t)

	view := createVersion(t, env, "发现资源")
	binding := view.Bindings[0]
	if len(binding.Pages) != 2 {
		t.Fatalf("pages = %d (%+v), want the two approved pages", len(binding.Pages), binding.Pages)
	}
	for _, page := range binding.Pages {
		if page.Status != "approved" {
			t.Fatalf("page %+v is not approved", page)
		}
	}
	if len(binding.APIs) != 1 || binding.APIs[0].ID != "1401" {
		t.Fatalf("apis = %#v", binding.APIs)
	}
	if len(binding.Databases) != 2 {
		t.Fatalf("databases = %#v", binding.Databases)
	}

	// The catalog still carries the proposed resources untouched: the binding
	// read is a projection, never a destructive rewrite.
	if proposed, ok := env.catalog.pages[1304]; !ok || proposed.Status != "proposed" {
		t.Fatalf("catalog proposed page = %#v", proposed)
	}
	if proposed, ok := env.catalog.apis[1403]; !ok || proposed.Status != "proposed" {
		t.Fatalf("catalog proposed api = %#v", proposed)
	}

	stored := storedBinding(t, env, view.ID)
	if len(stored.ScopeExclude.Pages) != 0 || len(stored.ScopeExclude.APIs) != 0 || len(stored.ScopeExclude.Databases) != 0 {
		t.Fatalf("default scope_exclude must be empty: %#v", stored.ScopeExclude)
	}
}

// TestVersionBindingHasResources is the design/execution half of the resource
// contract: once a version binds an environment, the version read model exposes
// the page locator inventory (name + route template) and the API shapes that the
// agent context ("AI design") and the execution SUT adapter consume.
func TestVersionBindingHasResources(t *testing.T) {
	env := newTestEnv(t)
	view := createVersion(t, env, "页面/API 资源")

	binding := view.Bindings[0]
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
	var _ resourceapp.CatalogReader = env.catalog
}
