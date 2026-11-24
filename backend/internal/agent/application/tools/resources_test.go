package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeDatabasePort is a ResourcePort that also implements the additive
// DatabaseLister surface consumed by sut.database_list and graph_propose.
type fakeDatabasePort struct {
	databases  []DatabaseSummary
	err        error
	versionID  int64
	versionSUT int64
}

func (f *fakeDatabasePort) ListSUTs(context.Context, int64) ([]SUTSummary, error) {
	return nil, nil
}

func (f *fakeDatabasePort) ListPages(context.Context, int64, int64) ([]ResourcePage, error) {
	return nil, nil
}

func (f *fakeDatabasePort) ListDatabases(_ context.Context, versionID, versionSUTID int64) ([]DatabaseSummary, error) {
	f.versionID = versionID
	f.versionSUT = versionSUTID
	return f.databases, f.err
}

// TestSUTDatabaseListReturnsVersionDatabases pins the read-only tool: it exposes
// the environment's database ids/names/kinds so the model never guesses one. The
// additive tool is registered by the P4 group mechanism the runtime registry
// composes (NewToolGroupRegistry/NewKnowledgeRegistry), not the frozen P1 order.
//
// v0.2.4-B: the description must redirect the design db assertion to sut_id. It
// used to tell the model to pass one of these ids as assertions[].database_id,
// which the case.graph_propose parser now rejects as an unknown field.
func TestSUTDatabaseListReturnsVersionDatabases(t *testing.T) {
	port := &fakeDatabasePort{databases: []DatabaseSummary{
		// One row with a resolved SUT and one without: the pair is what pins the
		// `if item.SUTID > 0` guard. A fixture with no SUTID at all lets the guard
		// be deleted with every assertion still passing, which is exactly how this
		// surface went unpinned while the other two were fixed.
		{ID: "1501", SUTID: 9001, Name: "order_db", Kind: "postgres", ReadOnly: true},
		{ID: "1502", SUTID: 0, Name: "orphan_db", Kind: "postgres", ReadOnly: false},
	}}
	registry := NewKnowledgeRegistry(Deps{Resource: port})
	tool, ok := registry.Get(ToolSUTDatabaseList)
	if !ok {
		t.Fatalf("NewKnowledgeRegistry is missing %q when the resource port is wired", ToolSUTDatabaseList)
	}
	description := tool.Description()
	if !strings.Contains(description, "sut_id") {
		t.Fatalf("sut.database_list description must point db assertions at sut_id: %s", description)
	}
	if !strings.Contains(description, "不再使用 database_id") {
		t.Fatalf("sut.database_list description must say database_id is retired for design assertions: %s", description)
	}
	if _, ok := NewKnowledgeRegistry(Deps{}).Get(ToolSUTDatabaseList); ok {
		t.Fatal("sut.database_list must stay unregistered without a resource port")
	}

	result, err := registry.Execute(context.Background(), Scope{VersionID: 2001}, ToolSUTDatabaseList,
		json.RawMessage(`{"sut_id":"3001"}`))
	if err != nil {
		t.Fatalf("sut.database_list: %v", err)
	}
	if port.versionID != 2001 || port.versionSUT != 3001 {
		t.Fatalf("scope = version %d version_sut %d", port.versionID, port.versionSUT)
	}
	body := resultMap(t, result)
	databases, _ := body["databases"].([]any)
	if len(databases) != 2 || body["count"] != float64(2) {
		t.Fatalf("result = %#v", body)
	}
	first, _ := databases[0].(map[string]any)
	if first["id"] != "1501" || first["name"] != "order_db" ||
		first["kind"] != "postgres" || first["read_only"] != true {
		t.Fatalf("database entry = %#v", first)
	}
	// sut_id must be present AND a decimal string. This is the surface the design
	// hints point at first (sut.list is primary, but this is the per-row source
	// they name second), and case.graph_propose parses assertions[].sut_id as
	// *string against a ^[1-9][0-9]*$ schema -- a number here costs the model a
	// turn on an unmarshal type error. The type expectation derives from the single
	// idStringSchema() authority (H-B6), not a re-hardcoded "string".
	requireSUTIDWireType(t, first["sut_id"])
	if first["sut_id"] != "9001" {
		t.Fatalf("database entry sut_id = %#v, want the decimal string \"9001\"", first["sut_id"])
	}
	// The unresolved row must OMIT the key rather than emit "0": a zero sut_id
	// fails the design gate's positive-id presence check, so offering one is worse
	// than offering none.
	second, _ := databases[1].(map[string]any)
	if _, present := second["sut_id"]; present {
		t.Fatalf("an unresolved SUT must not emit a sut_id key: %#v", second)
	}
	if second["id"] != "1502" || second["name"] != "orphan_db" {
		t.Fatalf("second database entry = %#v", second)
	}
	// The result hint must not send the model back to the retired field.
	if hint, _ := body["hint"].(string); !strings.Contains(hint, "sut_id") || strings.Contains(hint, "作为 database_id") {
		t.Fatalf("result hint = %#v, want it to name sut_id for design db assertions", body["hint"])
	}
}

// TestSUTDatabaseListRejectsMalformedSUTID pins the fix: a malformed
// optional filter is a model-fixable argument error, so it must be retryable
// and carry the top-level field name (not only details.field), and it must not
// reach the resource port.
func TestSUTDatabaseListRejectsMalformedSUTID(t *testing.T) {
	for _, payload := range []string{`{"sut_id":"abc"}`, `{"sut_id":"  "}`} {
		t.Run(payload, func(t *testing.T) {
			port := &fakeDatabasePort{databases: []DatabaseSummary{{ID: "1501"}}}
			registry := NewKnowledgeRegistry(Deps{Resource: port})
			_, err := registry.Execute(context.Background(), Scope{VersionID: 2001}, ToolSUTDatabaseList,
				json.RawMessage(payload))
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("malformed sut_id error = %+v, want retryable invalid_argument", toolErr)
			}
			if toolErr.Field != "sut_id" {
				t.Fatalf("field = %q, want top-level sut_id", toolErr.Field)
			}
			if field, _ := toolErr.Details["field"].(string); field != "sut_id" {
				t.Fatalf("details.field = %#v, want sut_id", toolErr.Details["field"])
			}
			if port.versionID != 0 {
				t.Fatalf("malformed filter reached the resource port: versionID=%d", port.versionID)
			}
		})
	}
}

// TestSUTDatabaseListRejectsUnknownParams keeps the read-only tool strict.
func TestSUTDatabaseListRejectsUnknownParams(t *testing.T) {
	registry := NewKnowledgeRegistry(Deps{Resource: &fakeDatabasePort{}})
	_, err := registry.Execute(context.Background(), Scope{VersionID: 2001}, ToolSUTDatabaseList,
		json.RawMessage(`{"database_id":"1501"}`))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("unknown parameter error = %+v", toolErr)
	}
	if field, _ := toolErr.Details["field"].(string); field != "parameters" {
		t.Fatalf("field = %#v", toolErr.Details["field"])
	}
}
