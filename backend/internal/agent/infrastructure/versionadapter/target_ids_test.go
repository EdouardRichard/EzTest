package versionadapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// targetRequirementPort satisfies tools.Deps.Requirement so the registry can be
// built. v0.2.4-B (T8) removed the requirement target-ID boundary these tests
// used to pin -- a requirement no longer names resource IDs at all -- so what is
// left here is the inventory side: sut.page_list must still expose catalog IDs.
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
	// W1-1 moved the unused test-only P1 constructor out of production; the
	// superset registry still registers the P1 sut.page_list tool.
	return tools.NewDesignRegistry(tools.Deps{Resource: adapter, Requirement: requirements}), requirements
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
	result, err := runTargetTool(t, registry, tools.ToolSUTPageList, `{"sut_id":"1001"}`)
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
// receives the page/API inventory, and every inventory row must expose the
// catalog ID as a decimal string. The IDs are no longer copied into requirement
// targets (v0.2.4-B removed those); the inventory still has to identify each
// row unambiguously for the model and for the knowledge-key mapping.
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
	if got := ids["api /api/orders"]; got != "1401" {
		t.Fatalf("api /api/orders id = %q, want 1401", got)
	}
	// v0.2.4-B review round 1: /login (1304) is a PROPOSED page in the shared
	// fixture. It is catalog state, not version scope, so the inventory must not
	// offer the clarifier model an id for a page nobody approved.
	if got, leaked := ids["page /login"]; leaked {
		t.Fatalf("sut.page_list exposes the proposed page /login as %q; only approved pages are in scope", got)
	}
}
