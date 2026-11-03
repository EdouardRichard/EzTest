package explore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
)

/* --------------------------- page discovery fixtures ---------------------- */

// discoveryLinkElement extends the login element fixture with an anchor href.
func discoveryLinkElement(index int, selector, name, href string) map[string]any {
	node := element(index, "a", selector, "link", name, "")
	node["href"] = href
	return node
}

// discoveryHiddenLink is a link present in the DOM but not rendered. It belongs
// to the interactive inventory but must never be crawled.
func discoveryHiddenLink(index int, selector, name, href string) map[string]any {
	node := discoveryLinkElement(index, selector, name, href)
	node["visible"] = false
	return node
}

func discoveryPageSnapshot(pageID, rawURL, title string, elements []any) map[string]any {
	if elements == nil {
		elements = []any{}
	}
	return map[string]any{
		"page_id": pageID,
		"url":     rawURL,
		"dom_snapshot": map[string]any{
			"url":      rawURL,
			"title":    title,
			"route":    "/",
			"elements": elements,
		},
	}
}

// newPageDiscoveryExecutor scripts the fixture crawl:
//
//	/app?lang=en&token=...#/home
//	  -> /app#/reports/42  (hash route)
//	  -> /orders/42?tab=items (numeric id -> {id}; /orders/43 is deduped)
//	       -> /orders/42/edit
func newPageDiscoveryExecutor() *fakeExecutor {
	exec := newFakeExecutor()
	root := discoveryPageSnapshot("page_1", pageDiscoveryStartURL, "Workspace", []any{
		discoveryLinkElement(0, "#orders", "Orders", "/orders/42?tab=items&token=SECRET-LINK"),
		discoveryLinkElement(1, "#order-dup", "Another order", "/orders/43"),
		discoveryLinkElement(2, "#reports", "Reports", "#/reports/42"),
		discoveryLinkElement(3, "#docs", "Docs", "https://docs.example.test/guide"),
		discoveryLinkElement(4, "#asset", "Asset", "/assets/app.js"),
		discoveryLinkElement(5, "#logout", "Logout", "/logout"),
		element(6, "button", "#create", "button", "Create", ""),
		element(7, "input", "#search", "textbox", "Search", "search"),
	})
	// Network capture is a 0.3 dynamic source: an executor may still return a
	// `requests` array, but page discovery must ignore it.
	root["requests"] = []any{
		map[string]any{"method": "GET", "url": "http://sut.test/api/orders?token=SECRET-NETWORK"},
	}
	exec.queue("explore.open", root)
	exec.queue("explore.open", discoveryPageSnapshot("page_2", "http://sut.test/app#/reports/42", "Reports", []any{
		element(0, "button", "#export", "button", "Export", ""),
	}))
	exec.queue("explore.open", discoveryPageSnapshot("page_3", "http://sut.test/orders/42?tab=items", "Order 42", []any{
		discoveryHiddenLink(0, "#hidden", "Hidden", "/orders/hidden"),
		discoveryLinkElement(1, "#edit", "Edit", "/orders/42/edit"),
		discoveryLinkElement(2, "#same-template", "Order 43", "/orders/43"),
	}))
	exec.queue("explore.open", discoveryPageSnapshot("page_4", "http://sut.test/orders/42/edit", "Edit order", nil))
	return exec
}

const pageDiscoveryStartURL = "http://sut.test/app?lang=en&token=SECRET-QUERY#/home"

func runPageDiscovery(t *testing.T) (exploreapp.PageDiscoveryResult, []byte) {
	t.Helper()
	exec := newPageDiscoveryExecutor()
	facts := &fakeFacts{}
	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:       7,
			EnvironmentID:      8,
			StartURL:           pageDiscoveryStartURL,
			MaxPages:           10,
			MaxDepth:           3,
			MaxLinksPerPage:    20,
			MaxElementsPerPage: 20,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: facts},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal page discovery result: %v", err)
	}
	encodedFacts, err := json.Marshal(facts.recorded())
	if err != nil {
		t.Fatalf("marshal page facts: %v", err)
	}
	if strings.Contains(string(encoded), "SECRET-") || strings.Contains(string(encodedFacts), "SECRET-") {
		t.Fatalf("page discovery persisted a query/href secret: result=%s facts=%s", encoded, encodedFacts)
	}
	if strings.Contains(string(encodedFacts), `"token"`) {
		t.Fatalf("page facts must not record a credential query key: %s", encodedFacts)
	}
	return result, encodedFacts
}

func TestPageDiscovery(t *testing.T) {
	result, encodedFacts := runPageDiscovery(t)

	if result.Truncated {
		t.Fatalf("truncated = true, want false for a 4 page fixture: %+v", result)
	}
	if len(result.Pages) != 4 {
		t.Fatalf("pages = %d (%+v), want 4", len(result.Pages), result.Pages)
	}
	wantKeys := []string{
		"http://sut.test/app#/home",
		"http://sut.test/app#/reports/{id}",
		"http://sut.test/orders/{id}",
		"http://sut.test/orders/{id}/edit",
	}
	for index, want := range wantKeys {
		if got := result.Pages[index].PageKey; got != want {
			t.Fatalf("page[%d].PageKey = %q, want %q (pages %+v)", index, got, want, result.Pages)
		}
	}

	root := result.Pages[0]
	if root.RouteTemplate != "/app#/home" || root.HashRoute != "/home" {
		t.Fatalf("root route = %+v, want path+hash route /app#/home", root)
	}
	if root.URL != "http://sut.test/app#/home" {
		t.Fatalf("root URL = %q, query values must be dropped", root.URL)
	}
	if len(root.QueryKeys) != 1 || root.QueryKeys[0] != "lang" {
		t.Fatalf("root query keys = %v, want [lang] (credential query keys dropped)", root.QueryKeys)
	}
	if len(root.Children) != 2 || root.Children[0] != "http://sut.test/app#/reports/{id}" || root.Children[1] != "http://sut.test/orders/{id}" {
		t.Fatalf("root children = %v, want deterministic sorted report/order children", root.Children)
	}

	reports := result.Pages[1]
	if reports.RouteTemplate != "/app#/reports/{id}" || reports.ParentKey != root.PageKey || reports.Depth != 1 {
		t.Fatalf("reports page = %+v", reports)
	}
	orders := result.Pages[2]
	if orders.RouteTemplate != "/orders/{id}" || orders.ParentKey != root.PageKey || orders.Depth != 1 {
		t.Fatalf("orders page = %+v", orders)
	}
	if len(orders.QueryKeys) != 1 || orders.QueryKeys[0] != "tab" {
		t.Fatalf("orders query keys = %v, want [tab]", orders.QueryKeys)
	}
	edit := result.Pages[3]
	if edit.RouteTemplate != "/orders/{id}/edit" || edit.ParentKey != orders.PageKey || edit.Depth != 2 {
		t.Fatalf("edit page = %+v", edit)
	}
	if children := orders.Children; len(children) != 1 || children[0] != edit.PageKey {
		t.Fatalf("orders children = %v, want [%s]", children, edit.PageKey)
	}

	// The hidden link is inventoried but never crawled; external/static/logout
	// links are dropped entirely.
	var hidden *exploreapp.PageElement
	for index := range orders.Elements {
		if orders.Elements[index].Selector == "#hidden" {
			hidden = &orders.Elements[index]
		}
	}
	if hidden == nil {
		t.Fatalf("orders elements = %+v, want the hidden inventory entry", orders.Elements)
	}
	if hidden.Visible {
		t.Fatalf("hidden element = %+v, want visible=false", hidden)
	}
	if hidden.Href != "http://sut.test/orders/hidden" {
		t.Fatalf("hidden element href = %q", hidden.Href)
	}
	if len(result.Pages) != 4 {
		t.Fatalf("hidden link must not be crawled, pages = %+v", result.Pages)
	}
	foundCreate := false
	for _, node := range root.Elements {
		if node.Selector == "#create" && node.Tag == "button" {
			foundCreate = true
		}
	}
	if !foundCreate {
		t.Fatalf("root elements = %+v, want the interactive Create button", root.Elements)
	}

	wantTemplates := []string{
		"/app#/home",
		"/app#/reports/{id}",
		"/orders/{id}",
		"/orders/{id}/edit",
	}
	if len(result.RouteTemplates) != len(wantTemplates) {
		t.Fatalf("route templates = %v, want %v", result.RouteTemplates, wantTemplates)
	}
	for index, want := range wantTemplates {
		if result.RouteTemplates[index] != want {
			t.Fatalf("route templates = %v, want %v", result.RouteTemplates, wantTemplates)
		}
	}

	// Every page produces one deterministic, credential-free `page` fact.
	if len(result.FactIDs) != len(result.Pages) {
		t.Fatalf("fact ids = %d, want one per page (%d)", len(result.FactIDs), len(result.Pages))
	}
	factCount := strings.Count(string(encodedFacts), `"Kind":"page"`)
	if factCount != len(result.Pages) {
		t.Fatalf("page facts = %d, want %d: %s", factCount, len(result.Pages), encodedFacts)
	}
	if !strings.Contains(string(encodedFacts), `"page:env:8:http://sut.test/orders/{id}"`) {
		t.Fatalf("page fact key is missing the environment-scoped route template: %s", encodedFacts)
	}
	if !strings.Contains(string(encodedFacts), `"parent_page_key":"http://sut.test/orders/{id}"`) {
		t.Fatalf("page fact is missing the page-tree parent: %s", encodedFacts)
	}

	// Discovery is deterministic: the same fixture produces the same order.
	second, _ := runPageDiscovery(t)
	firstKeys := make([]string, 0, len(result.Pages))
	secondKeys := make([]string, 0, len(second.Pages))
	for _, page := range result.Pages {
		firstKeys = append(firstKeys, page.PageKey)
	}
	for _, page := range second.Pages {
		secondKeys = append(secondKeys, page.PageKey)
	}
	if strings.Join(firstKeys, "|") != strings.Join(secondKeys, "|") {
		t.Fatalf("page order is not deterministic: %v vs %v", firstKeys, secondKeys)
	}

	// The page count is bounded and a truncation is reported, never silent.
	boundedFacts := &fakeFacts{}
	bounded := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:       7,
			EnvironmentID:      8,
			StartURL:           pageDiscoveryStartURL,
			MaxPages:           2,
			MaxDepth:           3,
			MaxLinksPerPage:    20,
			MaxElementsPerPage: 20,
		},
		exploreapp.PageDiscoveryDeps{Executor: newPageDiscoveryExecutor(), Facts: boundedFacts},
	)
	boundedResult, err := bounded.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("bounded PageDiscovery.Run: %v", err)
	}
	if len(boundedResult.Pages) != 2 || !boundedResult.Truncated {
		t.Fatalf("bounded result = %+v, want 2 pages and truncated=true", boundedResult)
	}
	if len(boundedFacts.recorded()) != 2 {
		t.Fatalf("bounded facts = %d, want 2", len(boundedFacts.recorded()))
	}
}

// TestPageDiscoveryReusesOneExecutorPage covers the executor's 8-page cap: the
// crawl navigates one page instead of opening one page per URL, and the
// storage_state_ref is only sent on the page-creating open (it is invalid on a
// navigation).
func TestPageDiscoveryReusesOneExecutorPage(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/home", "Home", []any{
		discoveryLinkElement(0, "#next", "Next", "/next"),
	}))
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/next", "Next", nil))

	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:    7,
			EnvironmentID:   8,
			StartURL:        "http://sut.test/home",
			StorageStateRef: "ss_discovery",
			MaxPages:        5,
			MaxDepth:        2,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: &fakeFacts{}},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if len(result.Pages) != 2 {
		t.Fatalf("pages = %+v, want 2", result.Pages)
	}
	opens := exec.callsOf("explore.open")
	if len(opens) != 2 {
		t.Fatalf("open calls = %d, want 2", len(opens))
	}
	if _, present := opens[0].args["page_id"]; present {
		t.Fatalf("the first open must create the crawl page, args = %+v", opens[0].args)
	}
	if opens[0].args["storage_state_ref"] != "ss_discovery" {
		t.Fatalf("the page-creating open must seed the session, args = %+v", opens[0].args)
	}
	if opens[1].args["page_id"] != "page_1" {
		t.Fatalf("the second open must navigate the existing page, args = %+v", opens[1].args)
	}
	if _, present := opens[1].args["storage_state_ref"]; present {
		t.Fatalf("storage_state_ref is only valid on a new page, args = %+v", opens[1].args)
	}
}

// TestPageDiscoveryDropsNonRouterFragments covers a redirected OAuth callback
// whose fragment carries a credential (`#access_token=...`): a non-router
// fragment is not part of the page identity and must never reach the result
// URL, the page facts or an element href. A hash-router fragment (`#/home`)
// remains part of the URL because SPA routes live there.
func TestPageDiscoveryDropsNonRouterFragments(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.open", discoveryPageSnapshot("page_1",
		"http://sut.test/auth/callback?code=SECRET-CODE#access_token=SECRET-ACCESS", "Callback", []any{
			discoveryLinkElement(0, "#plain", "Section", "#section"),
			discoveryLinkElement(1, "#cred", "Credential", "/auth/callback#access_token=SECRET-LINK"),
			discoveryLinkElement(2, "#router", "App", "/app#/home"),
			discoveryLinkElement(3, "#query", "Orders", "/orders/42?tab=items#access_token=SECRET-QFRAG"),
		}))
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/app#/home", "App", nil))
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/orders/42?tab=items", "Order", nil))

	facts := &fakeFacts{}
	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:       7,
			EnvironmentID:      8,
			StartURL:           "http://sut.test/login",
			MaxPages:           10,
			MaxDepth:           2,
			MaxLinksPerPage:    20,
			MaxElementsPerPage: 20,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: facts},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if len(result.Pages) != 3 {
		t.Fatalf("pages = %d (%+v), want callback + app + orders", len(result.Pages), result.Pages)
	}
	callback := result.Pages[0]
	if callback.PageKey != "http://sut.test/auth/callback" {
		t.Fatalf("callback PageKey = %q, want the fragment-free redirect target", callback.PageKey)
	}
	if callback.URL != "http://sut.test/auth/callback" {
		t.Fatalf("callback URL = %q, want the credential-bearing fragment dropped", callback.URL)
	}
	if callback.RouteTemplate != "/auth/callback" {
		t.Fatalf("callback route = %q, want /auth/callback", callback.RouteTemplate)
	}
	if len(callback.QueryKeys) != 1 || callback.QueryKeys[0] != "code" {
		t.Fatalf("callback query keys = %v, want [code] (values dropped, key kept)", callback.QueryKeys)
	}
	if got := result.Pages[1].URL; got != "http://sut.test/app#/home" {
		t.Fatalf("router page URL = %q, want the hash route preserved", got)
	}
	if got := result.Pages[2].URL; got != "http://sut.test/orders/42" {
		t.Fatalf("orders URL = %q, want query and fragment dropped", got)
	}

	wantHrefs := map[string]string{
		"#plain":  "http://sut.test/auth/callback",
		"#cred":   "http://sut.test/auth/callback",
		"#router": "http://sut.test/app#/home",
		"#query":  "http://sut.test/orders/42",
	}
	for _, el := range callback.Elements {
		want, ok := wantHrefs[el.Selector]
		if !ok {
			continue
		}
		if el.Href != want {
			t.Fatalf("element %s href = %q, want %q", el.Selector, el.Href, want)
		}
		delete(wantHrefs, el.Selector)
	}
	if len(wantHrefs) != 0 {
		t.Fatalf("missing fixture elements: %v (elements %+v)", wantHrefs, callback.Elements)
	}

	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	encodedFacts, err := json.Marshal(facts.recorded())
	if err != nil {
		t.Fatalf("marshal facts: %v", err)
	}
	for name, encoded := range map[string]string{"result": string(encodedResult), "facts": string(encodedFacts)} {
		for _, secret := range []string{"SECRET-ACCESS", "SECRET-LINK", "SECRET-QFRAG", "SECRET-CODE", "access_token", "#section"} {
			if strings.Contains(encoded, secret) {
				t.Fatalf("%s leaked %q: %s", name, secret, encoded)
			}
		}
	}
	recorded := facts.recorded()
	if len(recorded) != 3 {
		t.Fatalf("page facts = %d, want 3", len(recorded))
	}
	if recorded[0].SourceRef != "http://sut.test/auth/callback" {
		t.Fatalf("callback fact source ref = %q, want the sanitized URL", recorded[0].SourceRef)
	}
	if url, _ := recorded[0].Payload["url"].(string); url != "http://sut.test/auth/callback" {
		t.Fatalf("callback fact url = %q, want the sanitized URL", url)
	}
	if url, _ := recorded[2].Payload["url"].(string); url != "http://sut.test/orders/42" {
		t.Fatalf("orders fact url = %q, want query/fragment dropped", url)
	}
}

// TestPageDiscoveryClosesCreatedPage covers the executor's 8-page cap: a crawl
// seeded only by StartURL opens one page and must release it itself before Run
// returns, because the caller never receives that page id. A failed close is
// best effort and must not fail the discovery.
func TestPageDiscoveryClosesCreatedPage(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/home", "Home", []any{
		discoveryLinkElement(0, "#next", "Next", "/next"),
	}))
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/next", "Next", nil))

	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:    7,
			EnvironmentID:   8,
			StartURL:        "http://sut.test/home",
			StorageStateRef: "ss_discovery",
			MaxPages:        5,
			MaxDepth:        2,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: &fakeFacts{}},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if len(result.Pages) != 2 {
		t.Fatalf("pages = %+v, want 2", result.Pages)
	}
	closed := exec.closedPageIDs()
	if len(closed) != 1 || closed[0] != "page_1" {
		t.Fatalf("closed pages = %v, want the crawl-created page_1", closed)
	}
	if exec.pageIsOpen("page_1") {
		t.Fatalf("page_1 is still open; the crawl must close the page it created")
	}
	if result.PageID != "" {
		t.Fatalf("result.PageID = %q, want empty: discovery closed the page it created", result.PageID)
	}
}

// TestPageDiscoveryKeepsCallerPageOpen covers the other ownership side: a
// caller-supplied PageID belongs to the caller, so discovery navigates it and
// never closes it. The result reports the open page back to the caller.
func TestPageDiscoveryKeepsCallerPageOpen(t *testing.T) {
	exec := newFakeExecutor()
	exec.registerPage("page_caller")
	exec.queue("explore.open", discoveryPageSnapshot("page_caller", "http://sut.test/home", "Home", nil))

	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:  7,
			EnvironmentID: 8,
			StartURL:      "http://sut.test/home",
			PageID:        "page_caller",
			MaxPages:      5,
			MaxDepth:      2,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: &fakeFacts{}},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if len(result.Pages) != 1 {
		t.Fatalf("pages = %+v, want 1", result.Pages)
	}
	opens := exec.callsOf("explore.open")
	if len(opens) != 1 || opens[0].args["page_id"] != "page_caller" {
		t.Fatalf("opens = %+v, want one navigation of the caller page", opens)
	}
	if closed := exec.closedPageIDs(); len(closed) != 0 {
		t.Fatalf("closed pages = %v, want none: the caller owns page_caller", closed)
	}
	if !exec.pageIsOpen("page_caller") {
		t.Fatalf("the caller page must stay open after discovery")
	}
	if result.PageID != "page_caller" {
		t.Fatalf("result.PageID = %q, want the caller-owned page id", result.PageID)
	}
}

// TestPageDiscoverySeedURLs covers multi-seed crawls: StartURL stays first,
// extra seeds are deduplicated by page identity, sorted deterministically and
// an invalid extra seed does not abort a crawl that has a start URL.
func TestPageDiscoverySeedURLs(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/home", "Home", nil))
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/orders/42", "Order 42", nil))

	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:  7,
			EnvironmentID: 8,
			StartURL:      "http://sut.test/home",
			SeedURLs: []string{
				"http://sut.test/orders/1",
				"http://sut.test/home",
				"http://sut.test/orders/2",
				"not-a-url",
			},
			AllowedHosts: []string{"sut.test"},
			MaxPages:     10,
			MaxDepth:     1,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: &fakeFacts{}},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if len(result.Pages) != 2 {
		t.Fatalf("pages = %+v, want the StartURL and one deduplicated seed", result.Pages)
	}
	if result.Pages[0].PageKey != "http://sut.test/home" || result.Pages[0].Depth != 0 || result.Pages[0].ParentKey != "" {
		t.Fatalf("first page = %+v, want the StartURL root", result.Pages[0])
	}
	if result.Pages[1].PageKey != "http://sut.test/orders/{id}" || result.Pages[1].Depth != 0 || result.Pages[1].ParentKey != "" {
		t.Fatalf("second page = %+v, want the deduplicated seed root", result.Pages[1])
	}
	if opens := exec.callsOf("explore.open"); len(opens) != 2 {
		t.Fatalf("open calls = %d, want one per distinct seed", len(opens))
	}
}

// TestPageDiscoverySeedsFromPageID covers a caller that only has an open page:
// discovery snapshots it once to learn the current URL, then navigates the same
// caller-owned page and never closes it.
func TestPageDiscoverySeedsFromPageID(t *testing.T) {
	exec := newFakeExecutor()
	exec.registerPage("page_caller")
	exec.queue("explore.snapshot", discoveryPageSnapshot("page_caller", "http://sut.test/dashboard", "Dashboard", nil))
	exec.queue("explore.open", discoveryPageSnapshot("page_caller", "http://sut.test/dashboard", "Dashboard", nil))

	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{VersionSUTID: 7, EnvironmentID: 8, PageID: "page_caller", MaxPages: 5},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: &fakeFacts{}},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	snapshots := exec.callsOf("explore.snapshot")
	if len(snapshots) != 1 || snapshots[0].args["page_id"] != "page_caller" {
		t.Fatalf("snapshot calls = %+v, want one seed snapshot of the caller page", snapshots)
	}
	opens := exec.callsOf("explore.open")
	if len(opens) != 1 || opens[0].args["page_id"] != "page_caller" {
		t.Fatalf("open calls = %+v, want the caller page navigated", opens)
	}
	if len(result.Pages) != 1 || result.Pages[0].URL != "http://sut.test/dashboard" {
		t.Fatalf("pages = %+v, want the page-id seed URL", result.Pages)
	}
	if len(exec.closedPageIDs()) != 0 || !exec.pageIsOpen("page_caller") {
		t.Fatalf("closed pages = %v, want the caller page untouched", exec.closedPageIDs())
	}
	if result.PageID != "page_caller" {
		t.Fatalf("result.PageID = %q, want the caller page", result.PageID)
	}
}

// TestPageDiscoveryRespectsMaxDepth covers the depth cap: a page at MaxDepth is
// inventoried but its links are not followed.
func TestPageDiscoveryRespectsMaxDepth(t *testing.T) {
	exec := newFakeExecutor()
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/home", "Home", []any{
		discoveryLinkElement(0, "#level1", "Level 1", "/level1"),
	}))
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/level1", "Level 1", []any{
		discoveryLinkElement(0, "#level2", "Level 2", "/level2"),
	}))

	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:    7,
			EnvironmentID:   8,
			StartURL:        "http://sut.test/home",
			MaxPages:        10,
			MaxDepth:        1,
			MaxLinksPerPage: 10,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: &fakeFacts{}},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if len(result.Pages) != 2 {
		t.Fatalf("pages = %+v, want the seed and one level", result.Pages)
	}
	level1 := result.Pages[1]
	if level1.Depth != 1 || len(level1.Children) != 0 {
		t.Fatalf("level1 page = %+v, want depth 1 without followed children", level1)
	}
	if opens := exec.callsOf("explore.open"); len(opens) != 2 {
		t.Fatalf("open calls = %d, want /level2 never opened at MaxDepth", len(opens))
	}
}

// TestPageDiscoveryCaps covers the per-page link/element caps: both stop at the
// configured bound, set Truncated, and never silently overrun.
func TestPageDiscoveryCaps(t *testing.T) {
	exec := newFakeExecutor()
	root := discoveryPageSnapshot("page_1", "http://sut.test/home", "Home", []any{
		discoveryLinkElement(0, "#a", "A", "/a"),
		discoveryLinkElement(1, "#b", "B", "/b"),
		discoveryLinkElement(2, "#c", "C", "/c"),
	})
	exec.queue("explore.open", root)
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/a", "A", nil))

	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:       7,
			EnvironmentID:      8,
			StartURL:           "http://sut.test/home",
			MaxPages:           10,
			MaxDepth:           2,
			MaxLinksPerPage:    1,
			MaxElementsPerPage: 2,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: &fakeFacts{}},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if !result.Truncated {
		t.Fatalf("truncated = false, want true at the element/link caps")
	}
	if len(result.Pages[0].Elements) != 2 {
		t.Fatalf("root elements = %d, want the MaxElementsPerPage cap", len(result.Pages[0].Elements))
	}
	if len(result.Pages[0].Children) != 1 {
		t.Fatalf("root children = %v, want the MaxLinksPerPage cap", result.Pages[0].Children)
	}
	if len(result.Pages) != 2 {
		t.Fatalf("pages = %+v, want the seed and one followed link", result.Pages)
	}
}

// TestPageDiscoveryClipsElementText locks the inventory bounds: a chatty DOM
// node cannot push an unbounded name/text into facts.
func TestPageDiscoveryClipsElementText(t *testing.T) {
	exec := newFakeExecutor()
	longName := strings.Repeat("N", 300)
	longText := strings.Repeat("T", 300)
	node := element(0, "button", "#long", "button", longName, "")
	node["text"] = longText
	exec.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/home", "Home", []any{node}))

	facts := &fakeFacts{}
	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{VersionSUTID: 7, EnvironmentID: 8, StartURL: "http://sut.test/home", MaxPages: 2},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: facts},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if len(result.Pages) != 1 || len(result.Pages[0].Elements) != 1 {
		t.Fatalf("pages = %+v, want one page with one element", result.Pages)
	}
	long := result.Pages[0].Elements[0]
	if len([]rune(long.Name)) != 200 || len([]rune(long.Text)) != 200 {
		t.Fatalf("element name/text = %d/%d runes, want the 200-rune clip", len([]rune(long.Name)), len([]rune(long.Text)))
	}
	recorded := facts.recorded()
	if len(recorded) != 1 {
		t.Fatalf("facts = %d, want 1", len(recorded))
	}
	payloadElements, _ := recorded[0].Payload["elements"].([]any)
	if len(payloadElements) != 1 {
		t.Fatalf("fact elements = %+v, want 1", recorded[0].Payload["elements"])
	}
	payload, _ := payloadElements[0].(map[string]any)
	name, _ := payload["name"].(string)
	text, _ := payload["text"].(string)
	if len([]rune(name)) != 200 || len([]rune(text)) != 200 {
		t.Fatalf("fact element name/text = %d/%d runes, want the 200-rune clip", len([]rune(name)), len([]rune(text)))
	}
}

// executorWithoutPageCloser models an Executor that predates the optional
// per-page cleanup capability.
type executorWithoutPageCloser struct {
	inner exploreapp.Executor
}

func (e executorWithoutPageCloser) Action(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	return e.inner.Action(ctx, action, args)
}

// TestPageDiscoveryReportsUnclosedCreatedPage covers the degraded assembly: an
// executor without PageCloser cannot be closed by discovery, so the created page
// id is reported for the caller to release instead of being silently leaked.
func TestPageDiscoveryReportsUnclosedCreatedPage(t *testing.T) {
	t.Run("no closer reports the created page", func(t *testing.T) {
		fake := newFakeExecutor()
		fake.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/home", "Home", nil))
		discovery := exploreapp.NewPageDiscovery(
			exploreapp.PageDiscoveryConfig{VersionSUTID: 7, EnvironmentID: 8, StartURL: "http://sut.test/home", MaxPages: 2},
			exploreapp.PageDiscoveryDeps{Executor: executorWithoutPageCloser{inner: fake}, Facts: &fakeFacts{}},
		)
		result, err := discovery.Run(context.Background(), 10)
		if err != nil {
			t.Fatalf("PageDiscovery.Run: %v", err)
		}
		if fake.closeCount() != 0 {
			t.Fatalf("close calls = %d, want none without a closer", fake.closeCount())
		}
		if result.PageID != "page_1" || !fake.pageIsOpen("page_1") {
			t.Fatalf("result.PageID = %q (open=%v), want the created page reported for caller cleanup",
				result.PageID, fake.pageIsOpen("page_1"))
		}
	})

	t.Run("explicit closer is used and a close failure stays best effort", func(t *testing.T) {
		fake := newFakeExecutor()
		fake.closeErr = fmt.Errorf("executor close failed")
		fake.queue("explore.open", discoveryPageSnapshot("page_1", "http://sut.test/home", "Home", nil))
		discovery := exploreapp.NewPageDiscovery(
			exploreapp.PageDiscoveryConfig{VersionSUTID: 7, EnvironmentID: 8, StartURL: "http://sut.test/home", MaxPages: 2},
			exploreapp.PageDiscoveryDeps{
				Executor:   executorWithoutPageCloser{inner: fake},
				Facts:      &fakeFacts{},
				PageCloser: fake,
			},
		)
		result, err := discovery.Run(context.Background(), 10)
		if err != nil {
			t.Fatalf("PageDiscovery.Run must not fail when best-effort cleanup fails: %v", err)
		}
		if fake.closeCount() != 1 || fake.closedPageIDs()[0] != "page_1" {
			t.Fatalf("closed pages = %v, want the explicit closer to release page_1", fake.closedPageIDs())
		}
		if result.PageID != "" {
			t.Fatalf("result.PageID = %q, want empty after a close attempt", result.PageID)
		}
	})
}

/* --------------------------- static API fixtures -------------------------- */

type fakeSources struct {
	files     []codeintelapp.SourceFile
	err       error
	versionID int64
}

func (f *fakeSources) SourceFiles(_ context.Context, versionID int64) ([]codeintelapp.SourceFile, error) {
	f.versionID = versionID
	return f.files, f.err
}

func ginRouteSources() []codeintelapp.SourceFile {
	return []codeintelapp.SourceFile{
		{
			Path: "backend/internal/server/routes.go",
			Content: `package server

import "github.com/gin-gonic/gin"

func Register(r *gin.Engine) {
	api := r.Group("/api/v1", middleware.Auth())
	api.GET("/orders/:orderID", listOrders)
	api.POST("/orders", createOrder)
	api.DELETE("/orders/:id", deleteOrder)
	api.GET("/health", health)
}
`,
		},
		{
			// Route registrations in tests are not production declarations.
			Path: "backend/internal/server/routes_test.go",
			Content: `package server

func registerTestRoutes(r any) {
	_ = r
	// r.GET("/api/v1/test-only", handler)
}
`,
		},
		{
			Path: "docs/openapi.json",
			Content: `{
  "openapi": "3.0.3",
  "info": {"title": "Demo", "version": "1.0.0"},
  "paths": {
    "/api/v1/invoices/{invoiceId}": {
      "get": {
        "operationId": "getInvoice",
        "parameters": [{"name": "expand", "in": "query"}],
        "security": [{"bearerAuth": []}]
      }
    },
    "/api/v1/invoices": {
      "post": {
        "operationId": "createInvoice",
        "security": [{"bearerAuth": []}]
      }
    }
  },
  "components": {
    "securitySchemes": {
      "bearerAuth": {"type": "http", "scheme": "bearer"}
    }
  }
}
`,
		},
		{
			Path: "docs/swagger.yaml",
			Content: `swagger: "2.0"
info:
  title: Legacy
  version: "1"
paths:
  /api/v1/legacy:
    get:
      operationId: legacyStatus
      security:
        - bearerAuth: []
`,
		},
	}
}

func discoverySourceProvider(t *testing.T, files []codeintelapp.SourceFile) *fakeSources {
	t.Helper()
	return &fakeSources{files: files}
}

func findAPI(apis []exploreapp.APICandidate, apiKey string) (exploreapp.APICandidate, bool) {
	for _, api := range apis {
		if api.APIKey == apiKey {
			return api, true
		}
	}
	return exploreapp.APICandidate{}, false
}

func TestAPIDiscoveryFromGinRoutes(t *testing.T) {
	sources := discoverySourceProvider(t, ginRouteSources())
	facts := &fakeFacts{}
	discovery := exploreapp.NewAPIDiscovery(
		exploreapp.APIDiscoveryConfig{EnvironmentID: 8, MaxAPIs: 100},
		exploreapp.APIDiscoveryDeps{Sources: sources, Facts: facts},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("APIDiscovery.Run: %v", err)
	}
	if sources.versionID != 10 {
		t.Fatalf("source provider version = %d, want 10", sources.versionID)
	}

	wantKeys := []string{
		"DELETE /api/v1/orders/{id}",
		"GET /api/v1/health",
		"GET /api/v1/invoices/{id}",
		"GET /api/v1/legacy",
		"GET /api/v1/orders/{id}",
		"POST /api/v1/invoices",
		"POST /api/v1/orders",
	}
	if len(result.APIs) != len(wantKeys) {
		t.Fatalf("APIs = %+v, want %d endpoints", result.APIs, len(wantKeys))
	}
	for index, want := range wantKeys {
		if got := result.APIs[index].APIKey; got != want {
			t.Fatalf("apis[%d] = %q, want %q (deterministic order)", index, got, want)
		}
	}
	if result.DeclaredUnused != len(wantKeys) || result.UsedUndeclared != 0 || result.DeclaredUsed != 0 {
		t.Fatalf("state counts = %d/%d/%d, want all declared_unused", result.DeclaredUnused, result.UsedUndeclared, result.DeclaredUsed)
	}
	for _, api := range result.APIs {
		if api.State != exploreapp.APIStateDeclaredUnused {
			t.Fatalf("api %s state = %q, want declared_unused (no used observation)", api.APIKey, api.State)
		}
	}

	ginRoute, ok := findAPI(result.APIs, "GET /api/v1/orders/{id}")
	if !ok {
		t.Fatal("missing the Gin GET /orders/:orderID declaration")
	}
	if len(ginRoute.DeclaredSources) != 1 || ginRoute.DeclaredSources[0] != "gin_route" {
		t.Fatalf("gin route sources = %v, want [gin_route]", ginRoute.DeclaredSources)
	}
	if len(ginRoute.Params) != 1 || ginRoute.Params[0] != "orderID" {
		t.Fatalf("gin route params = %v, want [orderID]", ginRoute.Params)
	}
	openAPI, ok := findAPI(result.APIs, "GET /api/v1/invoices/{id}")
	if !ok {
		t.Fatal("missing the OpenAPI GET /invoices/{invoiceId} declaration")
	}
	if len(openAPI.DeclaredSources) != 1 || openAPI.DeclaredSources[0] != "openapi" {
		t.Fatalf("openapi sources = %v", openAPI.DeclaredSources)
	}
	if openAPI.Auth != "bearer" {
		t.Fatalf("openapi auth = %q, want bearer from the security scheme", openAPI.Auth)
	}
	legacy, ok := findAPI(result.APIs, "GET /api/v1/legacy")
	if !ok {
		t.Fatal("missing the Swagger YAML declaration")
	}
	if len(legacy.DeclaredSources) != 1 || legacy.DeclaredSources[0] != "openapi" {
		t.Fatalf("swagger yaml sources = %v", legacy.DeclaredSources)
	}
	if legacy.Auth != "bearer" {
		t.Fatalf("swagger yaml auth = %q, want bearer from the security requirement", legacy.Auth)
	}
	if _, ok := findAPI(result.APIs, "GET /api/v1/test-only"); ok {
		t.Fatal("a route declaration from a _test.go file must not be declared")
	}
	health, ok := findAPI(result.APIs, "GET /api/v1/health")
	if !ok || health.Auth != "" {
		t.Fatalf("health = %+v, want no declared auth", health)
	}

	if len(result.FactIDs) != len(result.APIs) {
		t.Fatalf("fact ids = %d, want one per api (%d)", len(result.FactIDs), len(result.APIs))
	}
	recorded := facts.recorded()
	if len(recorded) != len(result.APIs) {
		t.Fatalf("api facts = %d, want %d", len(recorded), len(result.APIs))
	}
	for _, fact := range recorded {
		if fact.Kind != exploreapp.KindAPI {
			t.Fatalf("fact kind = %q, want %q", fact.Kind, exploreapp.KindAPI)
		}
		if fact.Payload["state"] != string(exploreapp.APIStateDeclaredUnused) {
			t.Fatalf("fact state = %v", fact.Payload["state"])
		}
		if fact.Payload["source"] != "codeintel_static" {
			t.Fatalf("fact source payload = %v, want codeintel_static", fact.Payload["source"])
		}
	}
	encoded, err := json.Marshal(recorded)
	if err != nil {
		t.Fatalf("marshal api facts: %v", err)
	}
	if !strings.Contains(string(encoded), `"api:env:8:GET /api/v1/health"`) {
		t.Fatalf("api fact key is missing the environment-scoped endpoint: %s", encoded)
	}
	if strings.Contains(string(encoded), "func Register") || strings.Contains(string(encoded), "gin.Engine") {
		t.Fatalf("api facts must not carry source code bodies: %s", encoded)
	}
}

func threeStateSources() []codeintelapp.SourceFile {
	return []codeintelapp.SourceFile{
		{
			Path: "backend/internal/http/routes.go",
			Content: `package http

func register(r router) {
	r.GET("/api/orders", listOrders)
	r.POST("/api/orders", createOrder)
	r.GET("/api/users/:id", getUser)
	r.PUT("/api/users/:id", updateUser)
}
`,
		},
		{
			Path: "docs/openapi.json",
			Content: `{
  "openapi": "3.0.3",
  "paths": {
    "/api/users/{userId}": {
      "get": {"operationId": "getUser", "security": [{"bearerAuth": []}]}
    },
    "/api/reports/{reportId}": {
      "get": {"operationId": "getReport", "security": [{"bearerAuth": []}]}
    }
  },
  "components": {
    "securitySchemes": {"bearerAuth": {"type": "http", "scheme": "bearer"}}
  }
}
`,
		},
		{
			Path: "web/src/api/client.ts",
			Content: `import axios from 'axios'

export const api = axios.create({ baseURL: '/api' })

export function listOrders() {
  return api.get('/api/orders?token=SECRET-USED-QUERY')
}

export function refund(id: string) {
  return api.post(` + "`/api/refunds/${id}`" + `, { reason: 'duplicate' })
}

export async function currentUser() {
  return fetch('/api/users/42', {
    method: 'GET',
    headers: { Authorization: 'Bearer SUPER-SECRET-TOKEN' },
  })
}

export function report(id: string) {
  return apiClient.get<Report>('/api/reports/7')
}

export function profile() {
  // Relative to the axios baseURL '/api': /api/profile.
  return api.get('/profile')
}
`,
		},
		{
			Path: "web/src/api/types.gen.ts",
			Content: `export interface paths {
  "/api/admin/audit": {
    get: operations["listAudit"];
  };
}
`,
		},
		{
			// Calls in test files are not production usage.
			Path: "web/src/api/client.test.ts",
			Content: `import { api } from './client'

it('calls', () => api.get('/api/secret-used-only'))
`,
		},
	}
}

func TestAPIThreeStateMerge(t *testing.T) {
	sources := discoverySourceProvider(t, threeStateSources())
	facts := &fakeFacts{}
	discovery := exploreapp.NewAPIDiscovery(
		exploreapp.APIDiscoveryConfig{EnvironmentID: 8, MaxAPIs: 100},
		exploreapp.APIDiscoveryDeps{Sources: sources, Facts: facts},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("APIDiscovery.Run: %v", err)
	}

	want := []struct {
		key   string
		state exploreapp.APIState
	}{
		{"GET /api/admin/audit", exploreapp.APIStateDeclaredUnused},
		{"GET /api/orders", exploreapp.APIStateDeclaredUsed},
		{"GET /api/profile", exploreapp.APIStateUsedUndeclared},
		{"GET /api/reports/{id}", exploreapp.APIStateDeclaredUsed},
		{"GET /api/users/{id}", exploreapp.APIStateDeclaredUsed},
		{"POST /api/orders", exploreapp.APIStateDeclaredUnused},
		{"POST /api/refunds/{id}", exploreapp.APIStateUsedUndeclared},
		{"PUT /api/users/{id}", exploreapp.APIStateDeclaredUnused},
	}
	if len(result.APIs) != len(want) {
		t.Fatalf("APIs = %+v, want %d merged endpoints", result.APIs, len(want))
	}
	for index, expected := range want {
		got := result.APIs[index]
		if got.APIKey != expected.key {
			t.Fatalf("apis[%d] = %q, want %q (deterministic order)", index, got.APIKey, expected.key)
		}
		if got.State != expected.state {
			t.Fatalf("api %s state = %q, want %q", got.APIKey, got.State, expected.state)
		}
	}
	if result.DeclaredUnused != 3 || result.UsedUndeclared != 2 || result.DeclaredUsed != 3 {
		t.Fatalf("state counts = %d/%d/%d, want 3/2/3", result.DeclaredUnused, result.UsedUndeclared, result.DeclaredUsed)
	}

	declaredUsed, ok := findAPI(result.APIs, "GET /api/orders")
	if !ok {
		t.Fatal("missing GET /api/orders")
	}
	if len(declaredUsed.DeclaredSources) != 1 || declaredUsed.DeclaredSources[0] != "gin_route" {
		t.Fatalf("declared used sources = %v, want [gin_route]", declaredUsed.DeclaredSources)
	}
	if len(declaredUsed.UsedSources) != 1 || declaredUsed.UsedSources[0] != "api_client" {
		t.Fatalf("declared used observation sources = %v, want [api_client]", declaredUsed.UsedSources)
	}
	usedUndeclared, ok := findAPI(result.APIs, "POST /api/refunds/{id}")
	if !ok {
		t.Fatal("missing POST /api/refunds/{id}")
	}
	if len(usedUndeclared.DeclaredSources) != 0 {
		t.Fatalf("used-undeclared declared sources = %v, want none", usedUndeclared.DeclaredSources)
	}
	if len(usedUndeclared.UsedSources) != 1 || usedUndeclared.UsedSources[0] != "api_client" {
		t.Fatalf("used-undeclared sources = %v", usedUndeclared.UsedSources)
	}
	if len(usedUndeclared.Params) != 1 || usedUndeclared.Params[0] != "id" {
		t.Fatalf("used-undeclared params = %v, want [id]", usedUndeclared.Params)
	}
	users, ok := findAPI(result.APIs, "GET /api/users/{id}")
	if !ok {
		t.Fatal("missing GET /api/users/{id}")
	}
	if users.Auth != "bearer" {
		t.Fatalf("users auth = %q, want bearer", users.Auth)
	}
	if _, ok := findAPI(result.APIs, "GET /api/secret-used-only"); ok {
		t.Fatal("an api-client call in a test file must not count as used")
	}
	if len(result.FactIDs) != len(result.APIs) {
		t.Fatalf("fact ids = %d, want %d", len(result.FactIDs), len(result.APIs))
	}

	encodedFacts, err := json.Marshal(facts.recorded())
	if err != nil {
		t.Fatalf("marshal facts: %v", err)
	}
	if strings.Contains(string(encodedFacts), "SUPER-SECRET-TOKEN") ||
		strings.Contains(string(encodedFacts), "SECRET-USED-QUERY") {
		t.Fatalf("api facts leaked a credential/query value: %s", encodedFacts)
	}
	if !strings.Contains(string(encodedFacts), `"state":"used_undeclared"`) ||
		!strings.Contains(string(encodedFacts), `"state":"declared_used"`) ||
		!strings.Contains(string(encodedFacts), `"state":"declared_unused"`) {
		t.Fatalf("api facts are missing the three coverage states: %s", encodedFacts)
	}
	if strings.Contains(string(encodedFacts), "axios") || strings.Contains(string(encodedFacts), "fetch(") {
		t.Fatalf("api facts must not carry api-client source code: %s", encodedFacts)
	}

	// The endpoint count is bounded; a truncation is reported, never silent.
	boundedFacts := &fakeFacts{}
	bounded := exploreapp.NewAPIDiscovery(
		exploreapp.APIDiscoveryConfig{EnvironmentID: 8, MaxAPIs: 3},
		exploreapp.APIDiscoveryDeps{Sources: discoverySourceProvider(t, threeStateSources()), Facts: boundedFacts},
	)
	boundedResult, err := bounded.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("bounded APIDiscovery.Run: %v", err)
	}
	if len(boundedResult.APIs) != 3 || !boundedResult.Truncated {
		t.Fatalf("bounded result = %+v, want 3 endpoints and truncated=true", boundedResult)
	}
	if len(boundedFacts.recorded()) != 3 {
		t.Fatalf("bounded facts = %d, want 3", len(boundedFacts.recorded()))
	}
}

// TestAPIDiscoveryKeepsAllCallSites covers provenance of a per-source dedupe:
// the same endpoint declared (Gin) or observed (api-client) in two files is one
// candidate, but both call-site files stay in the result/facts instead of the
// first one silently winning.
func TestAPIDiscoveryKeepsAllCallSites(t *testing.T) {
	sources := discoverySourceProvider(t, []codeintelapp.SourceFile{
		{
			Path: "backend/internal/server/routes_a.go",
			Content: `package server

func registerA(r router) {
	r.GET("/api/health", health)
}
`,
		},
		{
			Path: "backend/internal/server/routes_b.go",
			Content: `package server

func registerB(r router) {
	r.GET("/api/health", health)
}
`,
		},
		{
			Path: "web/src/api/client.ts",
			Content: `export const api = axios.create({ baseURL: '/api' })

export function listOrders() {
  return api.get('/orders')
}
`,
		},
		{
			Path: "web/src/pages/orders.ts",
			Content: `export function loadOrders() {
  return api.get('/api/orders')
}
`,
		},
	})
	facts := &fakeFacts{}
	discovery := exploreapp.NewAPIDiscovery(
		exploreapp.APIDiscoveryConfig{EnvironmentID: 8, MaxAPIs: 100},
		exploreapp.APIDiscoveryDeps{Sources: sources, Facts: facts},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("APIDiscovery.Run: %v", err)
	}
	if len(result.APIs) != 2 {
		t.Fatalf("APIs = %+v, want the deduped orders + health endpoints", result.APIs)
	}
	orders, ok := findAPI(result.APIs, "GET /api/orders")
	if !ok {
		t.Fatal("missing GET /api/orders")
	}
	if got := strings.Join(orders.UsedFiles, ","); got != "web/src/api/client.ts,web/src/pages/orders.ts" {
		t.Fatalf("orders used files = %v, want every api-client call site", orders.UsedFiles)
	}
	health, ok := findAPI(result.APIs, "GET /api/health")
	if !ok {
		t.Fatal("missing GET /api/health")
	}
	if got := strings.Join(health.DeclaredFiles, ","); got != "backend/internal/server/routes_a.go,backend/internal/server/routes_b.go" {
		t.Fatalf("health declared files = %v, want every route-registration site", health.DeclaredFiles)
	}
	encodedFacts, err := json.Marshal(facts.recorded())
	if err != nil {
		t.Fatalf("marshal facts: %v", err)
	}
	for _, file := range []string{"web/src/pages/orders.ts", "backend/internal/server/routes_b.go"} {
		if !strings.Contains(string(encodedFacts), file) {
			t.Fatalf("facts are missing call-site file %q: %s", file, encodedFacts)
		}
	}
}

func TestAPIDiscoveryHardBounds(t *testing.T) {
	t.Run("files are clamped to the hard default", func(t *testing.T) {
		extra := 20
		files := make([]codeintelapp.SourceFile, 0, codeintelapp.DefaultStaticMaxFiles+extra)
		for index := 0; index < codeintelapp.DefaultStaticMaxFiles+extra; index++ {
			files = append(files, codeintelapp.SourceFile{
				Path:    fmt.Sprintf("backend/internal/generated/file_%03d.go", index),
				Content: "package generated\n",
			})
		}
		discovery := exploreapp.NewAPIDiscovery(
			exploreapp.APIDiscoveryConfig{EnvironmentID: 8, MaxFiles: 10000},
			exploreapp.APIDiscoveryDeps{Sources: discoverySourceProvider(t, files)},
		)
		result, err := discovery.Run(context.Background(), 10)
		if err != nil {
			t.Fatalf("APIDiscovery.Run: %v", err)
		}
		if result.FilesScanned != codeintelapp.DefaultStaticMaxFiles {
			t.Fatalf("files scanned = %d, want the hard default %d", result.FilesScanned, codeintelapp.DefaultStaticMaxFiles)
		}
		if !result.Truncated {
			t.Fatalf("truncated = false, want true when files exceed the hard default")
		}
	})

	t.Run("file bytes are clamped to the hard default", func(t *testing.T) {
		content := "package generated\n" + strings.Repeat("// padding\n", (codeintelapp.DefaultStaticMaxFileBytes/11)+2)
		discovery := exploreapp.NewAPIDiscovery(
			exploreapp.APIDiscoveryConfig{EnvironmentID: 8, MaxFileBytes: codeintelapp.DefaultStaticMaxFileBytes + 1000},
			exploreapp.APIDiscoveryDeps{Sources: discoverySourceProvider(t, []codeintelapp.SourceFile{
				{Path: "backend/internal/generated/huge.go", Content: content},
			})},
		)
		result, err := discovery.Run(context.Background(), 10)
		if err != nil {
			t.Fatalf("APIDiscovery.Run: %v", err)
		}
		if result.FilesScanned != 1 || result.FilesSkipped != 1 {
			t.Fatalf("files scanned/skipped = %d/%d, want 1/1: an oversized file must stay over the hard byte cap",
				result.FilesScanned, result.FilesSkipped)
		}
	})

	t.Run("api count is clamped to the hard default", func(t *testing.T) {
		var content strings.Builder
		content.WriteString("package server\n\nfunc register(r router) {\n")
		for index := 0; index < exploreapp.DefaultMaxAPIs+50; index++ {
			fmt.Fprintf(&content, "\tr.GET(\"/api/generated/item%d\", handler)\n", index)
		}
		content.WriteString("}\n")
		discovery := exploreapp.NewAPIDiscovery(
			exploreapp.APIDiscoveryConfig{EnvironmentID: 8, MaxAPIs: 10000},
			exploreapp.APIDiscoveryDeps{Sources: discoverySourceProvider(t, []codeintelapp.SourceFile{
				{Path: "backend/internal/server/generated_routes.go", Content: content.String()},
			})},
		)
		result, err := discovery.Run(context.Background(), 10)
		if err != nil {
			t.Fatalf("APIDiscovery.Run: %v", err)
		}
		if len(result.APIs) != exploreapp.DefaultMaxAPIs {
			t.Fatalf("apis = %d, want the hard default %d", len(result.APIs), exploreapp.DefaultMaxAPIs)
		}
		if !result.Truncated {
			t.Fatalf("truncated = false, want true when endpoints exceed the hard default")
		}
	})
}
