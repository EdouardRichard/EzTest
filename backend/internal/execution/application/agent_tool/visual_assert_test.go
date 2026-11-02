package agenttool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agenttool "github.com/EdouardRichard/EzTest/backend/internal/execution/application/agent_tool"
)

type fakePort struct {
	assertionID int64
	scope       tools.Scope
	result      any
	err         error
	calls       int
}

func (f *fakePort) VisualAssert(_ context.Context, scope tools.Scope, assertionID int64) (any, error) {
	f.calls++
	f.scope = scope
	f.assertionID = assertionID
	return f.result, f.err
}

func TestVisualAssertToolSchemaAndScope(t *testing.T) {
	port := &fakePort{result: map[string]any{"classification": "passed"}}
	tool := agenttool.NewVisualAssertTool(port)
	if tool.Name() != "execution.visual_assert" {
		t.Fatalf("name = %q", tool.Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("parameters are not valid JSON: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	if _, ok := properties["assertionId"]; !ok {
		t.Fatal("assertionId is missing from the schema")
	}
	if _, ok := properties["versionId"]; ok {
		t.Fatal("the tool schema must not expose versionId")
	}

	scope := tools.Scope{VersionID: 10, CaseRunID: 3001, CaseVersionID: 41}
	out, err := tool.Execute(context.Background(), scope, json.RawMessage(`{"assertionId":"6001"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if port.calls != 1 || port.assertionID != 6001 {
		t.Fatalf("port call = %d assertion=%d", port.calls, port.assertionID)
	}
	if out.(map[string]any)["classification"] != "passed" {
		t.Fatalf("result = %+v", out)
	}
}

func TestVisualAssertToolRequiresCaseRunScope(t *testing.T) {
	tool := agenttool.NewVisualAssertTool(&fakePort{})
	_, err := tool.Execute(context.Background(), tools.Scope{VersionID: 10}, json.RawMessage(`{"assertionId":"6001"}`))
	var toolErr *tools.Error
	if !errors.As(err, &toolErr) || toolErr.Code != tools.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestVisualAssertToolValidatesArguments(t *testing.T) {
	tool := agenttool.NewVisualAssertTool(&fakePort{})
	scope := tools.Scope{VersionID: 10, CaseRunID: 3001}
	cases := []string{
		`{"assertionId":""}`,
		`{"assertionId":"0"}`,
		`{"assertionId":"abc"}`,
		`{"assertionId":"1","versionId":"10"}`,
		`not-json`,
	}
	for _, raw := range cases {
		if _, err := tool.Execute(context.Background(), scope, json.RawMessage(raw)); err == nil {
			t.Fatalf("arguments %s must be rejected", raw)
		}
	}
}

func TestVisualAssertToolPropagatesPortErrors(t *testing.T) {
	portErr := tools.NewError(tools.CodeStateConflict, "case_run is not running")
	tool := agenttool.NewVisualAssertTool(&fakePort{err: portErr})
	_, err := tool.Execute(context.Background(), tools.Scope{VersionID: 10, CaseRunID: 3001}, json.RawMessage(`{"assertionId":"6001"}`))
	if !errors.Is(err, portErr) {
		t.Fatalf("err = %v", err)
	}
}
