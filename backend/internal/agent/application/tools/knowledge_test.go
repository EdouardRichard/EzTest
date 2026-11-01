package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeKnowledgeProposePort implements KnowledgeProposePort.
type fakeKnowledgeProposePort struct {
	calls      int
	versionID  int64
	agentRunID int64
	input      KnowledgeProposal
	result     KnowledgeProposalResult
	err        error
}

func (f *fakeKnowledgeProposePort) ProposeKnowledge(_ context.Context, versionID, agentRunID int64, in KnowledgeProposal) (KnowledgeProposalResult, error) {
	f.calls++
	f.versionID = versionID
	f.agentRunID = agentRunID
	f.input = in
	if f.err != nil {
		return KnowledgeProposalResult{}, f.err
	}
	return f.result, nil
}

func executeKnowledgePropose(t *testing.T, port *fakeKnowledgeProposePort, scope Scope, payload string) (any, error) {
	t.Helper()
	registry := NewP7Registry(Deps{P7: P7Deps{Knowledge: port}})
	return registry.Execute(context.Background(), scope, ToolKnowledgePropose, json.RawMessage(payload))
}

func TestP7RegistryRegistersKnowledgePropose(t *testing.T) {
	registry := NewP7Registry(Deps{})
	if _, ok := registry.Get(ToolKnowledgePropose); !ok {
		t.Fatalf("NewP7Registry is missing %q", ToolKnowledgePropose)
	}
	// P1–P5 tools stay registered: profiles select their own subset.
	for _, name := range append(P5ToolNames(), ToolContextTaskPack, ToolRequirementRead) {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("NewP7Registry is missing base tool %q", name)
		}
	}
	if _, ok := registry.Get("case.confirm"); ok {
		t.Fatal("NewP7Registry must not register confirmation tools")
	}
	if _, ok := registry.Get("knowledge.search"); ok {
		t.Fatal("knowledge.search is deferred and must not be registered")
	}
}

func TestKnowledgeProposeSchemaHidesServerControlledFields(t *testing.T) {
	tool, ok := NewP7Registry(Deps{}).Get(ToolKnowledgePropose)
	if !ok {
		t.Fatalf("%q is not registered", ToolKnowledgePropose)
	}
	var schema struct {
		Type       string                    `json:"type"`
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("Parameters() is not valid JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Fatalf("schema type = %q, want object", schema.Type)
	}
	for _, name := range []string{"title", "content", "tags", "scope", "system_key"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Fatalf("schema is missing property %q: %v", name, schema.Properties)
		}
	}
	for _, name := range []string{"status", "source_version_id", "promoted_from_version_id", "created_by", "reviewed_by", "reviewed_at"} {
		if _, ok := schema.Properties[name]; ok {
			t.Fatalf("schema exposes server-controlled property %q", name)
		}
		if strings.Contains(string(tool.Parameters()), `"`+name+`"`) {
			t.Fatalf("schema text mentions server-controlled field %q", name)
		}
	}
	if strings.Join(schema.Required, ",") != "title,content" {
		t.Fatalf("required = %v, want title,content", schema.Required)
	}
}

func TestKnowledgeProposeCreatesDraft(t *testing.T) {
	port := &fakeKnowledgeProposePort{result: KnowledgeProposalResult{ID: 7001, Status: "draft", Scope: "system", Revision: 1}}
	result, err := executeKnowledgePropose(t, port, Scope{VersionID: 10, AgentRunID: 77},
		`{"title":" 支付回调重试约定 ","content":" 回调必须在 5 分钟内完成幂等处理 ","tags":["支付"," 重试 ","支付"],"system_key":" payment "}`)
	if err != nil {
		t.Fatalf("knowledge.propose: %v", err)
	}
	if port.calls != 1 || port.versionID != 10 || port.agentRunID != 77 {
		t.Fatalf("port calls = %d version = %d run = %d", port.calls, port.versionID, port.agentRunID)
	}
	if port.input.Title != "支付回调重试约定" || port.input.Content != "回调必须在 5 分钟内完成幂等处理" {
		t.Fatalf("proposal text was not normalized: %#v", port.input)
	}
	if port.input.Scope != "system" || port.input.SystemKey == nil || *port.input.SystemKey != "payment" {
		t.Fatalf("proposal scope = %#v, want system/payment", port.input)
	}
	if strings.Join(port.input.Tags, ",") != "支付,重试" {
		t.Fatalf("proposal tags = %v, want deduped/trimmed", port.input.Tags)
	}

	decoded, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", result)
	}
	if decoded["knowledge_id"] != "7001" || decoded["status"] != "draft" ||
		decoded["scope"] != "system" || decoded["revision"] != 1 {
		t.Fatalf("result = %#v", decoded)
	}
}

func TestKnowledgeProposeDefaultsToGlobalScope(t *testing.T) {
	port := &fakeKnowledgeProposePort{result: KnowledgeProposalResult{ID: 7002, Status: "draft", Scope: "global", Revision: 1}}
	if _, err := executeKnowledgePropose(t, port, Scope{VersionID: 10, AgentRunID: 77},
		`{"title":"标题","content":"内容"}`); err != nil {
		t.Fatalf("knowledge.propose: %v", err)
	}
	if port.input.Scope != "global" || port.input.SystemKey != nil {
		t.Fatalf("proposal scope = %#v, want global without system key", port.input)
	}
}

func TestKnowledgeProposeIgnoresServerControlledArguments(t *testing.T) {
	port := &fakeKnowledgeProposePort{result: KnowledgeProposalResult{ID: 7003, Status: "draft", Scope: "global", Revision: 1}}
	if _, err := executeKnowledgePropose(t, port, Scope{VersionID: 10, AgentRunID: 77},
		`{"title":"标题","content":"内容","status":"approved","source_version_id":"999","promoted_from_version_id":"999","created_by":"1","reviewed_by":"1"}`); err != nil {
		t.Fatalf("server-controlled arguments must be ignored, got: %v", err)
	}
	if port.calls != 1 {
		t.Fatalf("port calls = %d, want 1", port.calls)
	}
	// The proposal type has no status/source/creator fields at all: only the
	// current version and run scope reach the writer.
	if port.versionID != 10 || port.agentRunID != 77 {
		t.Fatalf("writer scope = %d/%d", port.versionID, port.agentRunID)
	}
}

func TestKnowledgeProposeRejectsInvalidArgumentsWithoutCallingWriter(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		field   string
	}{
		{name: "missing title", payload: `{"content":"内容"}`, field: "title"},
		{name: "blank title", payload: `{"title":"   ","content":"内容"}`, field: "title"},
		{name: "missing content", payload: `{"title":"标题"}`, field: "content"},
		{name: "oversized content", payload: `{"title":"标题","content":"` + strings.Repeat("长", 20001) + `"}`, field: "content"},
		{name: "unknown scope", payload: `{"title":"标题","content":"内容","scope":"team"}`, field: "scope"},
		{name: "global with system key", payload: `{"title":"标题","content":"内容","scope":"global","system_key":"payment"}`, field: "system_key"},
		{name: "system without key", payload: `{"title":"标题","content":"内容","scope":"system"}`, field: "system_key"},
		{name: "uppercase system key", payload: `{"title":"标题","content":"内容","system_key":"Payment"}`, field: "system_key"},
		{name: "too many tags", payload: `{"title":"标题","content":"内容","tags":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q","r","s","t","u"]}`, field: "tags"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := &fakeKnowledgeProposePort{}
			_, err := executeKnowledgePropose(t, port, Scope{VersionID: 10, AgentRunID: 77}, tc.payload)
			if err == nil {
				t.Fatal("invalid arguments were accepted")
			}
			var toolErr *Error
			if !errors.As(err, &toolErr) {
				t.Fatalf("error = %v, want *tools.Error", err)
			}
			if toolErr.Code != CodeInvalidArgument {
				t.Fatalf("code = %q, want %q", toolErr.Code, CodeInvalidArgument)
			}
			if toolErr.Details["field"] != tc.field {
				t.Fatalf("field = %v, want %q (error %#v)", toolErr.Details["field"], tc.field, toolErr)
			}
			if port.calls != 0 {
				t.Fatalf("writer called %d times for invalid arguments", port.calls)
			}
		})
	}
}

func TestKnowledgeProposeRejectsMalformedParameters(t *testing.T) {
	port := &fakeKnowledgeProposePort{}
	_, err := executeKnowledgePropose(t, port, Scope{VersionID: 10, AgentRunID: 77}, `{"title":42,"content":"内容"}`)
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument {
		t.Fatalf("error = %v, want invalid_argument", err)
	}
	if port.calls != 0 {
		t.Fatalf("writer called %d times", port.calls)
	}
}

func TestKnowledgeProposeMapsWriterValidationErrors(t *testing.T) {
	port := &fakeKnowledgeProposePort{err: platform.Invalid("title", "title must be between 1 and 200 characters")}
	_, err := executeKnowledgePropose(t, port, Scope{VersionID: 10, AgentRunID: 77}, `{"title":"标题","content":"内容"}`)
	var toolErr *Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("error = %v, want *tools.Error", err)
	}
	if toolErr.Code != CodeInvalidArgument || toolErr.Details["field"] != "title" {
		t.Fatalf("mapped error = %#v", toolErr)
	}
}

func TestKnowledgeProposeRequiresVersionScopeAndWriter(t *testing.T) {
	port := &fakeKnowledgeProposePort{}
	_, err := executeKnowledgePropose(t, port, Scope{AgentRunID: 77}, `{"title":"标题","content":"内容"}`)
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Code != CodeInvalidArgument {
		t.Fatalf("missing version scope error = %v, want invalid_argument", err)
	}

	registry := NewP7Registry(Deps{})
	tool, _ := registry.Get(ToolKnowledgePropose)
	if _, err := tool.Execute(context.Background(), Scope{VersionID: 10}, json.RawMessage(`{"title":"标题","content":"内容"}`)); err == nil {
		t.Fatal("unwired knowledge writer must fail as an internal configuration error")
	}
}
