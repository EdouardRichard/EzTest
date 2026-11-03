package versionapp_test

import (
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// TestResourceBindingAllowsPages is the task-76 contract: a page that exists in
// the bound environment can be bound to a version, including a page that the
// exploration discovery hand-off proposed (status=proposed). The product-owner
// trial could not bind a page at all because discovery only wrote facts and the
// environment catalog stayed empty; this test pins that a discovered page is a
// first-class bindable resource and that the created version reports a non-zero
// page/API count.
func TestResourceBindingAllowsPages(t *testing.T) {
	env := newTestEnv(t)
	// A page proposed by discovery (task 76) and an approved page both belong to
	// the bound environment 1101. The wizard lists catalog pages regardless of
	// status, so both must bind.
	env.catalog.pages[1304] = resourceapp.PageView{
		ID:            "1304",
		SUTID:         "1001",
		EnvironmentID: "1101",
		Name:          "登录页（发现）",
		URL:           "https://orders.example.test/login",
		RouteTemplate: "/login",
		Status:        "proposed",
		Source:        "ai",
		CreatedAt:     testTime,
		UpdatedAt:     testTime,
	}

	view, err := env.svc.Create(requestContext(), versionapp.CreateCommand{
		Title: "发现资源可绑定",
		Bindings: []versionapp.BindingInput{
			{
				SUTID:         1001,
				EnvironmentID: 1101,
				PageIDs:       []int64{1301, 1304},
				APIIDs:        []int64{1401},
				DatabaseIDs:   []int64{1501},
			},
		},
	}, 42)
	if err != nil {
		t.Fatalf("Create with catalog pages: %v", err)
	}

	binding := view.Bindings[0]
	if len(binding.Pages) != 2 {
		t.Fatalf("pages = %d (%+v), want 2", len(binding.Pages), binding.Pages)
	}
	if binding.Pages[1].ID != "1304" || binding.Pages[1].Status != "proposed" {
		t.Fatalf("discovered page = %+v, want the proposed page 1304", binding.Pages[1])
	}
	if binding.Pages[1].RouteTemplate != "/login" {
		t.Fatalf("discovered page route = %q, want /login (the locator key)", binding.Pages[1].RouteTemplate)
	}
	if len(binding.APIs) != 1 || len(binding.Databases) != 1 {
		t.Fatalf("apis=%d databases=%d, want 1/1", len(binding.APIs), len(binding.Databases))
	}

	// The persisted binding must carry the IDs, not just the read projection.
	stored := findBinding(t, env.repo.bindings[view.ID], 1001)
	if len(stored.Scope.Pages) != 2 || stored.Scope.Pages[1] != 1304 {
		t.Fatalf("stored pages = %#v, want [1301 1304]", stored.Scope.Pages)
	}
}

// TestVersionBindingHasResources is the design/execution half of the task-76
// contract: once a version binds pages/APIs, the version read model exposes the
// page locator inventory (name + route template) and the API shapes that the
// agent context ("AI design") and the execution SUT adapter consume. A version
// with zero bindings would make both lists empty, which is exactly the
// product-owner failure.
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
}
