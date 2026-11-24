package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.2.4-C T12 (P8 lift): the model-facing http.request / db.query /
// account.credentials schemas and descriptions reflect the LIFTED contract.
// Expectations come from GD-01/P8, not from observed output.

func TestHTTPRequestDescriptionAndSchemaLifted(t *testing.T) {
	registry := executionRegistry(&fakeExecutionPort{})
	tool, ok := registry.Get(ToolHTTPRequest)
	if !ok {
		t.Fatalf("http.request not registered")
	}
	desc := tool.Description()
	if strings.Contains(desc, "only that SUT host") {
		t.Fatalf("http.request description still claims a SUT-host gate: %q", desc)
	}
	if strings.Contains(desc, "redacted") {
		t.Fatalf("http.request description still claims header redaction (GD-01 lifted it): %q", desc)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("parameters: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["timeout_ms"]; !ok {
		t.Fatalf("http.request schema must expose the configurable timeout_ms")
	}
}

// TestHTTPRequestBodyCapLifted: a request body larger than the old 64KB cap is
// accepted (the lift removes the request-body size gate).
func TestHTTPRequestBodyCapLifted(t *testing.T) {
	port := &fakeExecutionPort{}
	registry := executionRegistry(port)
	bigBody := strings.Repeat("x", 100<<10)
	payload, _ := json.Marshal(map[string]any{"path": "/api/orders", "method": "POST", "body": bigBody})
	if _, err := executeTool(t, registry, ToolHTTPRequest, string(payload)); err != nil {
		t.Fatalf("http.request rejected a >64KB request body (cap not lifted?): %v", err)
	}
	if !strings.Contains(string(port.httpArgs), "xxxxxxxx") {
		t.Fatalf("http args did not carry the body: %s", port.httpArgs)
	}
}

func TestDBQueryDescriptionAndSchemaLifted(t *testing.T) {
	registry := executionRegistry(&fakeExecutionPort{})
	tool, ok := registry.Get(ToolDBQuery)
	if !ok {
		t.Fatalf("db.query not registered")
	}
	desc := tool.Description()
	if strings.Contains(desc, "read-only") {
		t.Fatalf("db.query description still claims read-only: %q", desc)
	}
	if strings.Contains(desc, "200 rows") {
		t.Fatalf("db.query description still claims the 200-row cap: %q", desc)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("parameters: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	maxRows, _ := props["max_rows"].(map[string]any)
	if _, hasMax := maxRows["maximum"]; hasMax {
		t.Fatalf("max_rows schema still carries a maximum (the 200 cap is lifted): %#v", maxRows)
	}
	if _, ok := props["statement_timeout_ms"]; !ok {
		t.Fatalf("db.query schema must expose the configurable statement_timeout_ms")
	}
}

// TestDBQueryMaxRowsAbove200Accepted: max_rows=1000 is accepted (old cap 200).
func TestDBQueryMaxRowsAbove200Accepted(t *testing.T) {
	port := &fakeExecutionPort{}
	registry := executionRegistry(port)
	if _, err := executeTool(t, registry, ToolDBQuery, `{"sql":"SELECT 1","max_rows":1000}`); err != nil {
		t.Fatalf("db.query rejected max_rows=1000 (cap not lifted?): %v", err)
	}
	if !strings.Contains(string(port.dbArgs), "1000") {
		t.Fatalf("db args did not carry max_rows=1000: %s", port.dbArgs)
	}
}

// TestDBQueryWriteSQLDelegates: write SQL passes the tool layer (no read-only
// gate at the schema/validation surface).
func TestDBQueryWriteSQLDelegates(t *testing.T) {
	port := &fakeExecutionPort{}
	registry := executionRegistry(port)
	if _, err := executeTool(t, registry, ToolDBQuery, `{"sql":"UPDATE users SET admin = true"}`); err != nil {
		t.Fatalf("db.query tool layer rejected write SQL: %v", err)
	}
	if !strings.Contains(string(port.dbArgs), "UPDATE users") {
		t.Fatalf("db args = %s", port.dbArgs)
	}
}

func TestAccountCredentialsDescriptionEvidenceEligible(t *testing.T) {
	registry := executionRegistry(&fakeExecutionPort{})
	tool, ok := registry.Get(ToolAccountCredentials)
	if !ok {
		t.Fatalf("account.credentials not registered")
	}
	desc := tool.Description()
	if strings.Contains(desc, "never put them into evidence") {
		t.Fatalf("account.credentials description still forbids evidence (GD-01 lifted it): %q", desc)
	}
}
