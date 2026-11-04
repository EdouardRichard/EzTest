package tools

// TDD contract for v0.22 requirement.extraction_emit (R-A7-4/5):
//   - fixed core schema + optional constraints_json, unknown fields rejected;
//   - priority P0..P3, category functional/non_functional/planned, confidence
//     0..1, source_quote a verbatim substring of the injected chunk text;
//   - one deduped batch per call, no partial write, counts returned.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// testExtractionChunk is the exact segment text the extraction worker injects
// for this tool call. source_quote values must be substrings of this string.
func testExtractionChunk() string {
	return "3.2 用户登录\n" +
		"用户可以使用手机号登录，验证码 6 位。\n" +
		"验证码 60 秒内有效。\n" +
		"手机号格式为 11 位数字。"
}

// fakeExtractionSink records the one deduped batch that reaches persistence.
type fakeExtractionSink struct {
	called       bool
	source       ExtractionSource
	requirements []ExtractionRequirement
	counts       ExtractionEmitCounts
	err          error
}

func (f *fakeExtractionSink) EmitExtractedRequirements(_ context.Context, source ExtractionSource, requirements []ExtractionRequirement) (ExtractionEmitCounts, error) {
	f.called = true
	f.source = source
	f.requirements = append([]ExtractionRequirement(nil), requirements...)
	return f.counts, f.err
}

var _ ExtractionSink = (*fakeExtractionSink)(nil)

func TestEmitAcceptsValidBatch(t *testing.T) {
	sink := &fakeExtractionSink{counts: ExtractionEmitCounts{Created: 1, Updated: 1}}
	tool := NewExtractionEmitTool(sink)
	raw := json.RawMessage(`{
		"requirements": [
			{
				"external_id": "FR-001",
				"title": "手机号登录",
				"description": "用户使用手机号和验证码登录",
				"acceptance_criteria": [" 手机号 11 位 ", "验证码正确"],
				"priority": "p0",
				"category": "Functional",
				"source_quote": "用户可以使用手机号登录，验证码 6 位。",
				"confidence": 0.8
			},
			{
				"external_id": "FR-002",
				"title": "验证码有效期",
				"description": "",
				"acceptance_criteria": ["60 秒内有效"],
				"priority": "P1",
				"category": "non_functional",
				"source_quote": "验证码 60 秒内有效。",
				"confidence": 0.6,
				"constraints_json": {"max_seconds": 60}
			},
			{
				"external_id": "FR-001",
				"title": "手机号登录（补充）",
				"description": "补充：验证码只能使用一次",
				"acceptance_criteria": ["验证码只能使用一次"],
				"priority": "P0",
				"category": "functional",
				"source_quote": "手机号格式为 11 位数字。",
				"confidence": 0.9
			}
		]
	}`)
	value, err := tool.Emit(t.Context(), Scope{
		VersionID: 7,
		Extraction: ExtractionScope{
			RunID:        3,
			AttachmentID: 9,
			SegmentSeq:   2,
		},
	}, testExtractionChunk(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !sink.called {
		t.Fatal("the sink was not called for a valid batch")
	}
	if len(sink.requirements) != 2 {
		t.Fatalf("requirements = %d (%+v), want 2 after external_id dedupe", len(sink.requirements), sink.requirements)
	}
	if sink.source.VersionID != 7 || sink.source.RunID != 3 || sink.source.AttachmentID != 9 || sink.source.SegmentSeq != 2 {
		t.Fatalf("source = %+v, want the injected run/segment scope", sink.source)
	}
	merged := sink.requirements[0]
	if merged.ExternalID != "FR-001" {
		t.Fatalf("first external_id = %q", merged.ExternalID)
	}
	if merged.Title != "手机号登录（补充）" || merged.Description != "补充：验证码只能使用一次" {
		t.Fatalf("duplicate external_id was not merged: %+v", merged)
	}
	wantCriteria := []string{"手机号 11 位", "验证码正确", "验证码只能使用一次"}
	if !reflect.DeepEqual(merged.AcceptanceCriteria, wantCriteria) {
		t.Fatalf("acceptance criteria = %#v, want the trimmed union %#v", merged.AcceptanceCriteria, wantCriteria)
	}
	if merged.Priority != "P0" || merged.Category != "functional" {
		t.Fatalf("normalized priority/category = %q/%q", merged.Priority, merged.Category)
	}
	if merged.Confidence != 0.9 {
		t.Fatalf("merged confidence = %v, want the later 0.9", merged.Confidence)
	}
	if merged.SourceQuote != "手机号格式为 11 位数字。" {
		t.Fatalf("merged source_quote = %q", merged.SourceQuote)
	}
	second := sink.requirements[1]
	if second.ExternalID != "FR-002" || second.Priority != "P1" || second.Category != "non_functional" {
		t.Fatalf("second requirement = %+v", second)
	}
	var constraints map[string]any
	if err := json.Unmarshal(second.ConstraintsJSON, &constraints); err != nil {
		t.Fatalf("constraints_json = %q: %v", second.ConstraintsJSON, err)
	}
	if constraints["max_seconds"] != float64(60) {
		t.Fatalf("constraints = %#v", constraints)
	}
	if second.Description != "" {
		t.Fatalf("empty description was not kept empty: %q", second.Description)
	}

	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object with counts", value)
	}
	if result["requirements"] != 2 || result["created"] != 1 || result["updated"] != 1 {
		t.Fatalf("result counts = %#v, want requirements=2 created=1 updated=1", result)
	}
}

func TestEmitRejectsMissingSourceQuote(t *testing.T) {
	sink := &fakeExtractionSink{}
	tool := NewExtractionEmitTool(sink)
	raw := json.RawMessage(`{
		"requirements": [{
			"external_id": "FR-001",
			"title": "不存在的引用",
			"description": "d",
			"acceptance_criteria": [],
			"priority": "P0",
			"category": "functional",
			"source_quote": "不存在",
			"confidence": 0.5
		}]
	}`)
	_, err := tool.Emit(t.Context(), Scope{VersionID: 7}, testExtractionChunk(), raw)
	if !errors.Is(err, ErrSourceQuoteNotFound) {
		t.Fatalf("err = %v, want ErrSourceQuoteNotFound", err)
	}
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want %q", toolErr.Code, CodeInvalidArgument)
	}
	if !toolErr.Retryable {
		t.Fatal("a field-level source_quote failure must be retryable")
	}
	if field := toolErr.Details["field"]; field != "requirements[0].source_quote" {
		t.Fatalf("details.field = %#v, want requirements[0].source_quote", field)
	}
	if sink.called {
		t.Fatal("a batch with an invalid source_quote reached the sink (partial write)")
	}

	// The production path gets the chunk from the injected extraction scope;
	// source_quote validation must work there too.
	_, err = tool.Execute(t.Context(), Scope{
		VersionID:  7,
		Extraction: ExtractionScope{SegmentText: testExtractionChunk()},
	}, raw)
	if !errors.Is(err, ErrSourceQuoteNotFound) {
		t.Fatalf("Execute err = %v, want ErrSourceQuoteNotFound", err)
	}
	if sink.called {
		t.Fatal("Execute wrote a batch with an invalid source_quote")
	}
}

func TestEmitRejectsInvalidFields(t *testing.T) {
	valid := func(overrides string) string {
		payload := `{
			"external_id": "FR-001",
			"title": "手机号登录",
			"description": "用户使用手机号和验证码登录",
			"acceptance_criteria": ["手机号 11 位"],
			"priority": "P0",
			"category": "functional",
			"source_quote": "用户可以使用手机号登录，验证码 6 位。",
			"confidence": 0.8`
		if overrides != "" {
			payload += "," + overrides
		}
		return payload + "}"
	}
	tests := []struct {
		name       string
		raw        string
		field      string
		hasAllowed bool
	}{
		{"bad priority", `{"requirements":[` + valid(`"priority":"P9"`) + `]}`, "requirements[0].priority", true},
		{"bad category", `{"requirements":[` + valid(`"category":"security"`) + `]}`, "requirements[0].category", true},
		{"confidence over one", `{"requirements":[` + valid(`"confidence":1.5`) + `]}`, "requirements[0].confidence", false},
		{"negative confidence", `{"requirements":[` + valid(`"confidence":-0.1`) + `]}`, "requirements[0].confidence", false},
		{"empty external id", `{"requirements":[` + valid(`"external_id":" "`) + `]}`, "requirements[0].external_id", false},
		{"empty title", `{"requirements":[` + valid(`"title":""`) + `]}`, "requirements[0].title", false},
		{"empty source quote", `{"requirements":[` + valid(`"source_quote":""`) + `]}`, "requirements[0].source_quote", false},
		{"constraints not an object", `{"requirements":[` + valid(`"constraints_json":[1]`) + `]}`, "requirements[0].constraints_json", false},
		{"unknown requirement field", `{"requirements":[` + valid(`"bogus":1`) + `]}`, "parameters", false},
		{"unknown top-level field", `{"requirements":[],"version_id":"7"}`, "parameters", false},
		{"missing requirements", `{}`, "requirements", false},
		{"not an object", `[]`, "parameters", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := &fakeExtractionSink{}
			_, err := NewExtractionEmitTool(sink).Emit(t.Context(), Scope{VersionID: 7}, testExtractionChunk(), json.RawMessage(test.raw))
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
				t.Fatalf("err = %+v, want a retryable invalid_argument", toolErr)
			}
			if field := toolErr.Details["field"]; field != test.field {
				t.Fatalf("details.field = %#v, want %q", field, test.field)
			}
			if test.hasAllowed {
				if allowed, ok := toolErr.Details["allowed_values"].([]string); !ok || len(allowed) == 0 {
					t.Fatalf("details.allowed_values = %#v, want the allowed enum values", toolErr.Details["allowed_values"])
				}
			}
			if sink.called {
				t.Fatal("invalid input reached the sink")
			}
		})
	}
}

func TestEmitExecuteUsesInjectedSegmentText(t *testing.T) {
	sink := &fakeExtractionSink{counts: ExtractionEmitCounts{Created: 1}}
	tool := NewExtractionEmitTool(sink)
	raw := json.RawMessage(`{"requirements":[{
		"external_id":"FR-003","title":"验证码","description":"","acceptance_criteria":["60 秒内有效"],
		"priority":"P2","category":"non_functional","source_quote":"验证码 60 秒内有效。","confidence":0.7
	}]}`)
	value, err := tool.Execute(t.Context(), Scope{
		VersionID: 11,
		Extraction: ExtractionScope{
			RunID:        5,
			AttachmentID: 8,
			SegmentSeq:   1,
			SegmentText:  testExtractionChunk(),
		},
	}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !sink.called || len(sink.requirements) != 1 {
		t.Fatalf("sink = %+v", sink)
	}
	if sink.source != (ExtractionSource{VersionID: 11, AttachmentID: 8, RunID: 5, SegmentSeq: 1}) {
		t.Fatalf("source = %+v", sink.source)
	}
	result, ok := value.(map[string]any)
	if !ok || result["requirements"] != 1 || result["created"] != 1 {
		t.Fatalf("result = %#v", value)
	}
}

func TestExtractionEmitToolRegisteredOnlyWithSink(t *testing.T) {
	sink := &fakeExtractionSink{}
	registry := NewP7Registry(Deps{Extraction: ExtractionDeps{Sink: sink}})
	tool, ok := registry.Get(ToolRequirementExtractionEmit)
	if !ok {
		t.Fatalf("NewP7Registry is missing %q when the sink is wired", ToolRequirementExtractionEmit)
	}
	if tool.Name() != ToolRequirementExtractionEmit {
		t.Fatalf("tool name = %q", tool.Name())
	}
	if _, ok := NewP7Registry(Deps{}).Get(ToolRequirementExtractionEmit); ok {
		t.Fatalf("%q must stay unregistered until the sink is wired", ToolRequirementExtractionEmit)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema must be a strict object: %#v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["requirements"] == nil {
		t.Fatalf("schema properties = %#v, want requirements", properties)
	}
	if raw := strings.ToLower(string(tool.Parameters())); strings.Contains(raw, "version_id") {
		t.Fatalf("schema leaks the injected version scope: %s", tool.Parameters())
	}
}

func TestEmitAllowsEmptyBatchWithoutCallingSink(t *testing.T) {
	sink := &fakeExtractionSink{}
	value, err := NewExtractionEmitTool(sink).Emit(
		t.Context(), Scope{VersionID: 7}, testExtractionChunk(), json.RawMessage(`{"requirements":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if sink.called {
		t.Fatal("an empty batch should not reach the sink")
	}
	result, ok := value.(map[string]any)
	if !ok || result["requirements"] != 0 || result["created"] != 0 || result["updated"] != 0 {
		t.Fatalf("result = %#v", value)
	}
}
