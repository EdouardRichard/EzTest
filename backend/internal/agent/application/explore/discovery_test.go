package explore_test

import (
	"context"
	"encoding/json"
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
