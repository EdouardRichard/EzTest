package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// sutBearingExecutionTools are the remaining P3 execution tools whose model-facing
// contract carries the SUT selector. v0.2.4-C T14 (R-C1) renames that selector
// from the stale version_sut_id wire name to sut_id on EVERY surface at once.
var sutBearingExecutionTools = []string{
	ToolHTTPRequest,
	ToolDBQuery,
	ToolDatabaseSchemaRead,
	ToolExecutionStepRecord,
	ToolExecutionAssertionRecord,
	ToolHumanRequest,
	ToolAccountCredentials,
}

// TestWireRenameToolSchemas pins surface ① (the JSON schema published to the
// model): every SUT-bearing tool advertises sut_id and no P3 tool schema still
// advertises the stale version_sut_id key.
func TestWireRenameToolSchemas(t *testing.T) {
	registry := executionRegistry(&fakeExecutionPort{})
	for _, name := range CaseExecutionToolNames() {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("registry missing %q", name)
		}
		schema := string(tool.Parameters())
		if strings.Contains(schema, "version_sut_id") {
			t.Fatalf("%s schema still carries version_sut_id: %s", name, schema)
		}
	}
	for _, name := range sutBearingExecutionTools {
		tool, _ := registry.Get(name)
		var document map[string]any
		if err := json.Unmarshal(tool.Parameters(), &document); err != nil {
			t.Fatalf("%s schema is not JSON: %v", name, err)
		}
		properties, _ := document["properties"].(map[string]any)
		if _, ok := properties["sut_id"]; !ok {
			t.Fatalf("%s schema does not advertise sut_id: %s", name, tool.Parameters())
		}
	}
}

// TestWireRenameDecodeRoundTrip pins surface ② (the parameter decode tags): a
// model call that sends sut_id is decoded into the SUT selector, and the
// re-marshaled http/db/schema arguments forwarded to the execution port carry
// sut_id (the two sides share one wire contract).
func TestWireRenameDecodeRoundTrip(t *testing.T) {
	port := &fakeExecutionPort{}
	registry := executionRegistry(port)

	if _, err := executeTool(t, registry, ToolExecutionStepRecord, `{"seq":1,"status":"passed","observed":"ok","sut_id":"7"}`); err != nil {
		t.Fatalf("step_record with sut_id: %v", err)
	}
	if port.step.VersionSUTID == nil || *port.step.VersionSUTID != 7 {
		t.Fatalf("step_record sut_id did not decode: %+v", port.step)
	}

	if _, err := executeTool(t, registry, ToolExecutionAssertionRecord, `{"step_seq":1,"assertion_seq":1,"verdict":"passed","sut_id":"7"}`); err != nil {
		t.Fatalf("assertion_record with sut_id: %v", err)
	}
	if port.assertion.VersionSUTID == nil || *port.assertion.VersionSUTID != 7 {
		t.Fatalf("assertion_record sut_id did not decode: %+v", port.assertion)
	}

	if _, err := executeTool(t, registry, ToolHumanRequest, `{"kind":"login_takeover","reason":"2fa","sut_id":"7"}`); err != nil {
		t.Fatalf("human.request with sut_id: %v", err)
	}
	if port.human.VersionSUTID == nil || *port.human.VersionSUTID != 7 {
		t.Fatalf("human.request sut_id did not decode: %+v", port.human)
	}

	if _, err := executeTool(t, registry, ToolAccountCredentials, `{"sut_id":"7","role":"admin"}`); err != nil {
		t.Fatalf("account.credentials with sut_id: %v", err)
	}
	if port.creds.VersionSUTID == nil || *port.creds.VersionSUTID != 7 {
		t.Fatalf("account.credentials sut_id did not decode: %+v", port.creds)
	}

	for _, call := range []struct {
		name string
		raw  string
		arg  *json.RawMessage
	}{
		{ToolHTTPRequest, `{"path":"/api/orders","method":"GET","sut_id":"7"}`, &port.httpArgs},
		{ToolDBQuery, `{"sql":"select 1","sut_id":"7"}`, &port.dbArgs},
		{ToolDatabaseSchemaRead, `{"schema":"public","sut_id":"7"}`, &port.schemaArgs},
	} {
		if _, err := executeTool(t, registry, call.name, call.raw); err != nil {
			t.Fatalf("%s with sut_id: %v", call.name, err)
		}
		forwarded := string(*call.arg)
		if !strings.Contains(forwarded, `"sut_id"`) {
			t.Fatalf("%s forwarded args lost sut_id: %s", call.name, forwarded)
		}
		if strings.Contains(forwarded, "version_sut_id") {
			t.Fatalf("%s forwarded args still carry version_sut_id: %s", call.name, forwarded)
		}
	}
}

// TestWireRenameErrorField pins surface ④ (the platform.Invalid error field
// name): a malformed sut_id is reported under the field name the model actually
// sent, so the correction is actionable. Naming version_sut_id here would tell
// the model to fix an argument that is not in its schema.
func TestWireRenameErrorField(t *testing.T) {
	registry := executionRegistry(&fakeExecutionPort{})
	for _, call := range []struct {
		name string
		raw  string
	}{
		{ToolAccountCredentials, `{"sut_id":"not-a-number","role":"admin"}`},
		{ToolExecutionStepRecord, `{"seq":1,"status":"passed","sut_id":"not-a-number"}`},
		{ToolDBQuery, `{"sql":"select 1","sut_id":"not-a-number"}`},
		{ToolHTTPRequest, `{"path":"/x","sut_id":"not-a-number"}`},
	} {
		_, err := executeTool(t, registry, call.name, call.raw)
		if err == nil {
			t.Fatalf("%s accepted a malformed sut_id", call.name)
		}
		var toolErr *Error
		if !errors.As(err, &toolErr) {
			t.Fatalf("%s error %v is not a *tools.Error", call.name, err)
		}
		if got, _ := toolErr.Details["field"].(string); got != "sut_id" {
			t.Fatalf("%s error field = %q, want sut_id (the model's own argument name)", call.name, got)
		}
	}
}

// TestWireRenameRejectsStaleKey is the mutation guard: after the rename the old
// version_sut_id key is an unknown field and is hard-rejected. Reverting any one
// decode tag back to version_sut_id would make this call succeed again (and the
// round-trip test fail), so a one-sided rename cannot stay green.
func TestWireRenameRejectsStaleKey(t *testing.T) {
	registry := executionRegistry(&fakeExecutionPort{})
	for _, call := range []struct {
		name string
		raw  string
	}{
		{ToolHTTPRequest, `{"path":"/x","version_sut_id":"7"}`},
		{ToolDBQuery, `{"sql":"select 1","version_sut_id":"7"}`},
		{ToolAccountCredentials, `{"version_sut_id":"7","role":"admin"}`},
		{ToolExecutionStepRecord, `{"seq":1,"status":"passed","version_sut_id":"7"}`},
	} {
		if _, err := executeTool(t, registry, call.name, call.raw); err == nil {
			t.Fatalf("%s still accepts the stale version_sut_id key", call.name)
		}
	}
}
