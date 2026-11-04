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

// TestSUTDatabaseListReturnsVersionDatabases pins R-E1-2: the read-only tool
// exposes the environment's database ids/names/kinds so the model never guesses
// a database_id. The additive tool is registered by the P4 group mechanism the
// runtime registry composes (NewP4Registry/NewP7Registry), not the frozen P1
// order.
func TestSUTDatabaseListReturnsVersionDatabases(t *testing.T) {
	port := &fakeDatabasePort{databases: []DatabaseSummary{
		{ID: "1501", Name: "order_db", Kind: "postgres", ReadOnly: true},
		{ID: "1502", Name: "audit_db", Kind: "postgres", ReadOnly: false},
	}}
	registry := NewP7Registry(Deps{Resource: port})
	tool, ok := registry.Get(ToolSUTDatabaseList)
	if !ok {
		t.Fatalf("NewP7Registry is missing %q when the resource port is wired", ToolSUTDatabaseList)
	}
	if description := tool.Description(); !strings.Contains(description, "database_id") {
		t.Fatalf("sut.database_list description does not name database_id: %s", description)
	}
	if _, ok := NewP7Registry(Deps{}).Get(ToolSUTDatabaseList); ok {
		t.Fatal("sut.database_list must stay unregistered without a resource port")
	}

	result, err := registry.Execute(context.Background(), Scope{VersionID: 2001}, ToolSUTDatabaseList,
		json.RawMessage(`{"version_sut_id":"3001"}`))
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
}

// TestSUTDatabaseListRejectsUnknownParams keeps the read-only tool strict.
func TestSUTDatabaseListRejectsUnknownParams(t *testing.T) {
	registry := NewP7Registry(Deps{Resource: &fakeDatabasePort{}})
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
