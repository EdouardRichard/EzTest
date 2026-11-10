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
	// Replace mode uses the incoming acceptance criteria (never a union): a
	// re-upload must be able to drop a scenario that was removed from the source
	// document instead of append-only duplicating it.
	wantCriteria := []string{"验证码只能使用一次"}
	if !reflect.DeepEqual(merged.AcceptanceCriteria, wantCriteria) {
		t.Fatalf("acceptance criteria = %#v, want the later duplicate's %#v", merged.AcceptanceCriteria, wantCriteria)
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
		{"pipe in title", `{"requirements":[` + valid(`"title":"手机号|登录"`) + `]}`, "requirements[0].title", false},
		{"pipe in description", `{"requirements":[` + valid(`"description":"手机号|登录"`) + `]}`, "requirements[0].description", false},
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

// TestEmitRequiresDescription covers the schema/runtime consistency fix: the
// fixed core schema marks description required, so an omitted description must
// be an explicit required-field error instead of silently becoming "".
func TestEmitRequiresDescription(t *testing.T) {
	sink := &fakeExtractionSink{}
	raw := json.RawMessage(`{"requirements":[{
		"external_id":"FR-001","title":"手机号登录","acceptance_criteria":[],
		"priority":"P0","category":"functional","source_quote":"手机号","confidence":0.8
	}]}`)
	_, err := NewExtractionEmitTool(sink).Emit(t.Context(), Scope{VersionID: 7}, testExtractionChunk(), raw)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable {
		t.Fatalf("err = %+v, want a retryable invalid_argument", toolErr)
	}
	if field := toolErr.Details["field"]; field != "requirements[0].description" {
		t.Fatalf("details.field = %#v, want requirements[0].description", field)
	}
	if sink.called {
		t.Fatal("a requirement without description reached the sink")
	}
}

// TestEmitConfidenceIsOptionalAndMergesOnlyWhenSupplied covers the confidence
// contract: confidence is not schema-required, an omitted confidence is valid,
// a duplicate external_id without confidence must not zero the earlier supplied
// value, and an explicitly supplied 0 still overwrites.
func TestEmitConfidenceIsOptionalAndMergesOnlyWhenSupplied(t *testing.T) {
	requirement := func(confidence string) string {
		value := `{"external_id":"FR-001","title":"手机号登录","description":"","acceptance_criteria":[],` +
			`"priority":"P0","category":"functional","source_quote":"手机号"`
		if confidence != "" {
			value += `,"confidence":` + confidence
		}
		return value + "}"
	}
	t.Run("duplicate without confidence keeps the earlier value", func(t *testing.T) {
		sink := &fakeExtractionSink{}
		raw := json.RawMessage(`{"requirements":[` + requirement("0.9") + `,` + requirement("") + `]}`)
		if _, err := NewExtractionEmitTool(sink).Emit(t.Context(), Scope{VersionID: 7}, testExtractionChunk(), raw); err != nil {
			t.Fatal(err)
		}
		if len(sink.requirements) != 1 || sink.requirements[0].Confidence != 0.9 {
			t.Fatalf("merged requirements = %+v, want the supplied 0.9 confidence kept", sink.requirements)
		}
	})
	t.Run("explicit zero is supplied and overwrites", func(t *testing.T) {
		sink := &fakeExtractionSink{}
		raw := json.RawMessage(`{"requirements":[` + requirement("0.9") + `,` + requirement("0") + `]}`)
		if _, err := NewExtractionEmitTool(sink).Emit(t.Context(), Scope{VersionID: 7}, testExtractionChunk(), raw); err != nil {
			t.Fatal(err)
		}
		if len(sink.requirements) != 1 || sink.requirements[0].Confidence != 0 {
			t.Fatalf("merged requirements = %+v, want the explicit 0 confidence", sink.requirements)
		}
	})
	t.Run("omitted confidence alone is accepted", func(t *testing.T) {
		sink := &fakeExtractionSink{}
		raw := json.RawMessage(`{"requirements":[` + requirement("") + `]}`)
		if _, err := NewExtractionEmitTool(sink).Emit(t.Context(), Scope{VersionID: 7}, testExtractionChunk(), raw); err != nil {
			t.Fatalf("omitted confidence must be accepted: %v", err)
		}
		if len(sink.requirements) != 1 || sink.requirements[0].Confidence != 0 {
			t.Fatalf("requirements = %+v, want one requirement with confidence 0", sink.requirements)
		}
	})
}

// TestEmitMissingVersionScopeIsInternalError covers the internal/missing-
// dependency classification: an unwired scope is a server wiring bug, never a
// model-retryable invalid_argument.
func TestEmitMissingVersionScopeIsInternalError(t *testing.T) {
	sink := &fakeExtractionSink{}
	_, err := NewExtractionEmitTool(sink).Emit(
		t.Context(), Scope{}, testExtractionChunk(), json.RawMessage(`{"requirements":[]}`))
	if err == nil {
		t.Fatal("Emit accepted a missing version scope")
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		t.Fatalf("missing version scope returned a model-visible tool error %+v, want an internal error", toolErr)
	}
	if !strings.Contains(err.Error(), "version scope") {
		t.Fatalf("err = %v, want a version-scope missing-dependency error", err)
	}
	if sink.called {
		t.Fatal("the sink was called without a version scope")
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
	requirements, ok := properties["requirements"].(map[string]any)
	if !ok {
		t.Fatalf("requirements schema = %#v, want an object", properties["requirements"])
	}
	items, ok := requirements["items"].(map[string]any)
	if !ok {
		t.Fatalf("requirements.items schema = %#v, want an object", requirements["items"])
	}
	required := make(map[string]bool)
	if fields, ok := items["required"].([]any); ok {
		for _, field := range fields {
			if name, ok := field.(string); ok {
				required[name] = true
			}
		}
	}
	if !required["description"] {
		t.Fatalf("description must stay schema-required: %#v", items["required"])
	}
	if required["confidence"] {
		t.Fatalf("confidence must be optional in the schema (omitted confidence is accepted): %#v", items["required"])
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

// TestMergeExtractionRequirementFillEmpty covers the C-03 in-batch merge: a
// catalog row merged into an already-emitted body row must only fill empty
// fields, may replace the priority with a valid value, keeps the higher
// confidence and merges constraints instead of replacing them.
func TestMergeExtractionRequirementFillEmpty(t *testing.T) {
	body := ExtractionRequirement{
		ExternalID: "FR-131", Title: "内容排期与工作量看板", Description: "正文描述",
		AcceptanceCriteria: []string{"场景 1"}, Priority: "P2",
		SourceQuote: "正文来源", Confidence: 0.9,
		ConstraintsJSON: json.RawMessage(`{"module":"内容管理"}`),
		MergeMode:       ExtractionMergeReplace,
	}
	catalog := ExtractionRequirement{
		ExternalID: "FR-131", Title: "目录标题", Description: "目录摘要",
		AcceptanceCriteria: nil, Priority: "P1",
		SourceQuote: "目录来源", Confidence: 0.6,
		ConstraintsJSON: json.RawMessage(`{"ac_count":2}`),
		MergeMode:       ExtractionMergeFillEmpty,
	}
	merged := MergeExtractionRequirement(body, catalog)
	if merged.Title != body.Title || merged.Description != body.Description {
		t.Fatalf("fill_empty downgraded title/description: %#v", merged)
	}
	if !reflect.DeepEqual(merged.AcceptanceCriteria, body.AcceptanceCriteria) {
		t.Fatalf("fill_empty overwrote non-empty AC: %#v", merged.AcceptanceCriteria)
	}
	if merged.Priority != "P1" {
		t.Fatalf("priority = %q, want the valid catalog P1", merged.Priority)
	}
	if merged.Confidence != 0.9 {
		t.Fatalf("confidence = %v, want max(0.9, 0.6)", merged.Confidence)
	}
	var constraints map[string]any
	if err := json.Unmarshal(merged.ConstraintsJSON, &constraints); err != nil {
		t.Fatalf("constraints = %q: %v", merged.ConstraintsJSON, err)
	}
	if constraints["module"] != "内容管理" || constraints["ac_count"] != float64(2) {
		t.Fatalf("constraints were not merged: %#v", constraints)
	}
}

// TestMergeExtractionRequirementFillEmptyFillsBlankBody covers the other half:
// a fill_empty row still fills a body row whose field is empty.
func TestMergeExtractionRequirementFillEmptyFillsBlankBody(t *testing.T) {
	body := ExtractionRequirement{ExternalID: "FR-131", Title: "正文标题", Priority: "P2"}
	catalog := ExtractionRequirement{
		ExternalID: "FR-131", Description: "目录摘要", AcceptanceCriteria: []string{"场景 1"},
		Priority: "P1", MergeMode: ExtractionMergeFillEmpty,
	}
	merged := MergeExtractionRequirement(body, catalog)
	if merged.Description != "目录摘要" || len(merged.AcceptanceCriteria) != 1 {
		t.Fatalf("fill_empty did not fill blank body fields: %#v", merged)
	}
	if merged.Title != "正文标题" {
		t.Fatalf("fill_empty replaced the non-empty title: %q", merged.Title)
	}
}

// TestMergeExtractionRequirementReplaceAcceptanceCriteria pins the C-03
// re-upload fix: replace mode takes the incoming multi-line acceptance criteria
// verbatim (no union with the current criteria) and only keeps the current
// criteria when the incoming list is empty. An omitted/zero confidence keeps the
// current score, as the in-batch Emit path documents.
func TestMergeExtractionRequirementReplaceAcceptanceCriteria(t *testing.T) {
	current := ExtractionRequirement{
		ExternalID: "FR-201", Title: "正文标题", Confidence: 0.9,
		AcceptanceCriteria: []string{"场景 1\nGiven a\nWhen b\nThen c", "场景 2"},
	}
	incoming := current
	incoming.Title = "正文标题（更新）"
	incoming.AcceptanceCriteria = []string{"场景 1\nGiven a\nWhen b\nThen c"}
	incoming.Confidence = 0
	merged := MergeExtractionRequirement(current, incoming)
	if !reflect.DeepEqual(merged.AcceptanceCriteria, incoming.AcceptanceCriteria) {
		t.Fatalf("replace unioned acceptance criteria: %#v", merged.AcceptanceCriteria)
	}
	if merged.Confidence != current.Confidence {
		t.Fatalf("omitted confidence = %v, want the current %v", merged.Confidence, current.Confidence)
	}

	// An explicit non-empty incoming list replaces an empty current one.
	emptyCurrent := ExtractionRequirement{ExternalID: "FR-202"}
	withCriteria := ExtractionRequirement{
		ExternalID: "FR-202", AcceptanceCriteria: []string{"场景 1"},
	}
	// A blank incoming list never clears the current criteria.
	blankIncoming := ExtractionRequirement{ExternalID: "FR-202", MergeMode: ExtractionMergeReplace}
	kept := MergeExtractionRequirement(ExtractionRequirement{
		ExternalID: "FR-202", AcceptanceCriteria: []string{"场景 1"},
	}, blankIncoming)
	if !reflect.DeepEqual(kept.AcceptanceCriteria, []string{"场景 1"}) {
		t.Fatalf("blank incoming AC cleared the current criteria: %#v", kept.AcceptanceCriteria)
	}
	filled := MergeExtractionRequirement(emptyCurrent, withCriteria)
	if !reflect.DeepEqual(filled.AcceptanceCriteria, withCriteria.AcceptanceCriteria) {
		t.Fatalf("incoming AC not applied: %#v", filled.AcceptanceCriteria)
	}
}
