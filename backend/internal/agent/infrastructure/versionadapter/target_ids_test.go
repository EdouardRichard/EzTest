package versionadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// targetRequirementPort records the draft requirement.draft_create forwards to
// the version context. It fails nothing: the real version service validates
// target scope, while these tests pin the tool boundary (numeric IDs accepted,
// paths rejected before any write).
type targetRequirementPort struct {
	createCalls int
	versionID   int64
	draft       tools.RequirementDraft
	created     tools.RequirementSummary
}

func (f *targetRequirementPort) ListRequirements(context.Context, int64) ([]tools.RequirementSummary, error) {
	return nil, nil
}

func (f *targetRequirementPort) GetRequirement(context.Context, int64, int64) (tools.RequirementDetail, error) {
	return tools.RequirementDetail{}, nil
}

func (f *targetRequirementPort) CreateDraft(_ context.Context, versionID int64, draft tools.RequirementDraft) (tools.RequirementSummary, error) {
	f.createCalls++
	f.versionID = versionID
	f.draft = draft
	return f.created, nil
}

func (f *targetRequirementPort) UpdateDraft(context.Context, int64, int64, tools.RequirementDraft) (tools.RequirementSummary, error) {
	return tools.RequirementSummary{}, nil
}

func (f *targetRequirementPort) ProposeRelation(context.Context, int64, int64, int64, string, string) error {
	return nil
}

// newTargetRegistry wires the real sut.page_list adapter over the resource-bound
// version fixture and a capturing requirement port, so the tool loop is the
// same one the clarifier agent executes.
func newTargetRegistry(t *testing.T) (*tools.Registry, *targetRequirementPort) {
	t.Helper()
	version := resourceBoundVersion()
	versions := &fakeVersions{getFn: func(context.Context, int64) (versionapp.VersionView, error) {
		return version, nil
	}}
	adapter := versionadapter.New(nil, nil, versions, nil, nil)
	requirements := &targetRequirementPort{
		created: tools.RequirementSummary{ID: 90001, CurrentVersionID: 90002, Revision: 1, Status: "draft"},
	}
	return tools.NewP1Registry(tools.Deps{Resource: adapter, Requirement: requirements}), requirements
}

func runTargetTool(t *testing.T, registry *tools.Registry, name, params string) (any, error) {
	t.Helper()
	return registry.Execute(context.Background(), tools.Scope{VersionID: 2001}, name, json.RawMessage(params))
}

func decodeToolResult(t *testing.T, result any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return body
}

func pageListEntries(t *testing.T, registry *tools.Registry) []any {
	t.Helper()
	result, err := runTargetTool(t, registry, tools.ToolSUTPageList, `{"version_sut_id":"3001"}`)
	if err != nil {
		t.Fatalf("sut.page_list: %v", err)
	}
	body := decodeToolResult(t, result)
	entries, ok := body["pages"].([]any)
	if !ok {
		t.Fatalf("sut.page_list result = %#v", body)
	}
	return entries
}

// lookupTargetID returns the id the model must copy into requirement targets.
func lookupTargetID(t *testing.T, entries []any, kind, path string) string {
	t.Helper()
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok || entry["kind"] != kind || entry["path"] != path {
			continue
		}
		id, ok := entry["id"].(string)
		if !ok {
			t.Fatalf("%s %s entry has no string id: %#v", kind, path, entry)
		}
		return id
	}
	t.Fatalf("sut.page_list has no %s entry for %s: %#v", kind, path, entries)
	return ""
}

func isPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// TestSutPageListExposesNumericIDs is the O5 regression: the clarifier model
// receives the page/API inventory but had no numeric ID to put into
// requirement.draft_create targets. Every inventory row must expose the
// catalog ID as a decimal string.
func TestSutPageListExposesNumericIDs(t *testing.T) {
	registry, _ := newTargetRegistry(t)
	entries := pageListEntries(t, registry)

	ids := make(map[string]string, len(entries))
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("sut.page_list entry = %#v", raw)
		}
		id, ok := entry["id"].(string)
		if !ok || !isPositiveDecimal(id) {
			t.Fatalf("sut.page_list entry %v id = %#v, want decimal string", entry["path"], entry["id"])
		}
		ids[fmt.Sprintf("%v %v", entry["kind"], entry["path"])] = id
	}
	if got := ids["page /orders"]; got != "1301" {
		t.Fatalf("page /orders id = %q, want 1301", got)
	}
	if got := ids["page /login"]; got != "1304" {
		t.Fatalf("page /login id = %q, want 1304", got)
	}
	if got := ids["api /api/orders"]; got != "1401" {
		t.Fatalf("api /api/orders id = %q, want 1401", got)
	}
}

// TestDraftCreateAcceptsPageTargetIDs chains the exact model loop: read the
// inventory, copy the numeric IDs into targets, create the draft. The version
// context receives decimal IDs, never path slugs.
func TestDraftCreateAcceptsPageTargetIDs(t *testing.T) {
	registry, requirements := newTargetRegistry(t)
	entries := pageListEntries(t, registry)
	pageID := lookupTargetID(t, entries, "page", "/orders")
	apiID := lookupTargetID(t, entries, "api", "/api/orders")

	payload := fmt.Sprintf(`{
		"title":"订单列表",
		"description":"用户可以查看订单列表",
		"acceptance_criteria":"订单列表展示当前用户的订单",
		"priority":"P0",
		"target_version_suts":["3001"],
		"target_pages":[%q],
		"target_apis":[%q]
	}`, pageID, apiID)
	result, err := runTargetTool(t, registry, tools.ToolRequirementDraftCreate, payload)
	if err != nil {
		t.Fatalf("requirement.draft_create with page_list IDs: %v", err)
	}
	if requirements.createCalls != 1 || requirements.versionID != 2001 {
		t.Fatalf("create calls/version = %d/%d", requirements.createCalls, requirements.versionID)
	}
	if !reflect.DeepEqual(requirements.draft.TargetPages, []string{"1301"}) {
		t.Fatalf("target pages = %#v, want the page_list id", requirements.draft.TargetPages)
	}
	if !reflect.DeepEqual(requirements.draft.TargetAPIs, []string{"1401"}) {
		t.Fatalf("target apis = %#v, want the page_list id", requirements.draft.TargetAPIs)
	}
	if body := decodeToolResult(t, result); body["requirement_id"] != "90001" || body["status"] != "draft" {
		t.Fatalf("result = %#v", body)
	}
}

// TestDraftCreateRejectsNonNumericTargets keeps the numeric-ID guard: paths,
// slugs and method+path strings are rejected with invalid_argument before the
// version context is touched, and the model is told to use sut.page_list ids.
func TestDraftCreateRejectsNonNumericTargets(t *testing.T) {
	registry, requirements := newTargetRegistry(t)
	cases := []struct {
		name   string
		field  string
		params string
	}{
		{
			name:  "page path",
			field: "target_pages",
			params: `{"title":"t","description":"d","acceptance_criteria":"a",
				"target_pages":["/orders"]}`,
		},
		{
			name:  "page route template",
			field: "target_pages",
			params: `{"title":"t","description":"d","acceptance_criteria":"a",
				"target_pages":["/orders/{id}"]}`,
		},
		{
			name:  "api method and path",
			field: "target_apis",
			params: `{"title":"t","description":"d","acceptance_criteria":"a",
				"target_apis":["GET /api/orders"]}`,
		},
		{
			name:  "sut name",
			field: "target_version_suts",
			params: `{"title":"t","description":"d","acceptance_criteria":"a",
				"target_version_suts":["order-center"]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runTargetTool(t, registry, tools.ToolRequirementDraftCreate, tc.params)
			if err == nil {
				t.Fatalf("requirement.draft_create accepted %s", tc.name)
			}
			var toolErr *tools.Error
			if !errors.As(err, &toolErr) {
				t.Fatalf("error = %v, want a structured tool error", err)
			}
			if toolErr.Code != tools.CodeInvalidArgument {
				t.Fatalf("code = %q, want invalid_argument", toolErr.Code)
			}
			if toolErr.Details["field"] != tc.field {
				t.Fatalf("field = %#v, want %q", toolErr.Details["field"], tc.field)
			}
			if !strings.Contains(toolErr.Message, "positive decimal") {
				t.Fatalf("message = %q, want the numeric-ID guidance", toolErr.Message)
			}
		})
	}
	if requirements.createCalls != 0 {
		t.Fatalf("version context created %d drafts from non-numeric targets", requirements.createCalls)
	}
}
