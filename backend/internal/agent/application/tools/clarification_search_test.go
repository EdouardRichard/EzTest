package tools

// Task 27 (v0.22 B §8.2/spec R-B5-3): clarification.search is the read-only
// cross-Host retrieval tool. These tests pin:
//   - the version scope comes only from tools.Scope, never from model input;
//   - cross-version hits can never reach the model;
//   - query/requirement_id/kinds/limit validation produces typed tool errors;
//   - results carry sources (message_id/question id/time/requirement_id) and
//     bounded snippets instead of raw chat;
//   - the JSON schema never exposes the injected version_id.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeClarificationSearcher struct {
	calls int
	query ClarificationSearchQuery
	hits  []ClarificationSearchHit
	err   error
}

func (f *fakeClarificationSearcher) SearchClarification(_ context.Context, query ClarificationSearchQuery) ([]ClarificationSearchHit, error) {
	f.calls++
	f.query = query
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

var _ ClarificationSearcher = (*fakeClarificationSearcher)(nil)

func clarificationSearchTime() time.Time {
	return time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
}

// TestClarificationSearchScoped is the plan-mandated scoping test: the injected
// VersionID 7 reaches the port, and a misbehaving adapter's VersionID 9 hit is
// dropped so the model can never see another version's clarification.
func TestClarificationSearchScoped(t *testing.T) {
	searcher := &fakeClarificationSearcher{hits: []ClarificationSearchHit{
		{
			VersionID: 7, Kind: ClarificationSearchKindQA,
			RequirementID: 101, MessageID: 9001, QuestionID: 501,
			Snippet:   "退款到账时间按 T+1 还是 T+3 验收？ → T+1",
			Timestamp: clarificationSearchTime(), Source: "human_request:501",
		},
		{
			VersionID: 9, Kind: ClarificationSearchKindDecision,
			MessageID: 9999, Snippet: "其它版本的决策，绝不能返回",
			Timestamp: clarificationSearchTime(), Source: "version_fact:901",
		},
	}}
	tool := NewClarificationSearchTool(searcher)
	value, err := tool.Execute(context.Background(), Scope{VersionID: 7}, json.RawMessage(`{"query":"退款"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if searcher.calls != 1 {
		t.Fatalf("searcher calls = %d, want 1", searcher.calls)
	}
	if searcher.query.VersionID != 7 {
		t.Fatalf("port version = %d, want the injected 7", searcher.query.VersionID)
	}
	if searcher.query.Query != "退款" {
		t.Fatalf("port query = %q", searcher.query.Query)
	}
	if searcher.query.Limit != DefaultClarificationSearchLimit {
		t.Fatalf("port limit = %d, want default %d", searcher.query.Limit, DefaultClarificationSearchLimit)
	}
	if len(searcher.query.Kinds) != len(ClarificationSearchKinds()) {
		t.Fatalf("port kinds = %v, want every kind by default", searcher.query.Kinds)
	}

	result, ok := value.(*ClarificationSearchResult)
	if !ok {
		t.Fatalf("result = %T, want *ClarificationSearchResult", value)
	}
	if result.Count != 1 || len(result.Results) != 1 {
		t.Fatalf("results = %+v, want only the VersionID 7 hit", result.Results)
	}
	hit := result.Results[0]
	if hit.VersionID != "7" || hit.Kind != "qa" || hit.RequirementID != "101" ||
		hit.MessageID != "9001" || hit.QuestionID != "501" || hit.Source == "" ||
		hit.Timestamp == "" || hit.Snippet == "" {
		t.Fatalf("hit = %+v, want full sources", hit)
	}
}

// TestClarificationSearchByRequirementID pins the structured filters: a
// requirement_id-only search is valid and the kinds/limit reach the port
// unchanged.
func TestClarificationSearchByRequirementID(t *testing.T) {
	searcher := &fakeClarificationSearcher{hits: []ClarificationSearchHit{
		{VersionID: 7, Kind: ClarificationSearchKindDecision, RequirementID: 101, Snippet: "退款 SLA 采用 T+1", Timestamp: clarificationSearchTime(), Source: "version_fact:601"},
	}}
	tool := NewClarificationSearchTool(searcher)
	value, err := tool.Execute(context.Background(), Scope{VersionID: 7},
		json.RawMessage(`{"requirement_id":"101","kinds":["decision","qa"],"limit":5}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if searcher.query.RequirementID != 101 || searcher.query.Query != "" {
		t.Fatalf("port query = %+v", searcher.query)
	}
	if len(searcher.query.Kinds) != 2 || searcher.query.Kinds[0] != "decision" || searcher.query.Kinds[1] != "qa" {
		t.Fatalf("port kinds = %v", searcher.query.Kinds)
	}
	if searcher.query.Limit != 5 {
		t.Fatalf("port limit = %d", searcher.query.Limit)
	}
	result := value.(*ClarificationSearchResult)
	if result.Count != 1 || result.Results[0].RequirementID != "101" {
		t.Fatalf("result = %+v", result)
	}
}

// TestClarificationSearchInputValidation pins the typed errors for empty and
// invalid model input. The model can supply neither version_id nor arbitrary
// kinds/limits.
func TestClarificationSearchInputValidation(t *testing.T) {
	cases := []struct {
		name  string
		scope Scope
		raw   string
		field string
	}{
		{"missing scope", Scope{}, `{"query":"退款"}`, "scope"},
		{"empty input", Scope{VersionID: 7}, `{}`, "query"},
		{"blank query", Scope{VersionID: 7}, `{"query":"   "}`, "query"},
		{"empty requirement id", Scope{VersionID: 7}, `{"requirement_id":""}`, "requirement_id"},
		{"negative requirement id", Scope{VersionID: 7}, `{"requirement_id":"-3"}`, "requirement_id"},
		{"zero limit", Scope{VersionID: 7}, `{"query":"退款","limit":0}`, "limit"},
		{"over max limit", Scope{VersionID: 7}, `{"query":"退款","limit":51}`, "limit"},
		{"unknown kind", Scope{VersionID: 7}, `{"query":"退款","kinds":["transcript"]}`, "kinds"},
		{"unknown field version_id", Scope{VersionID: 7}, `{"query":"退款","version_id":"9"}`, "parameters"},
		{"query too long", Scope{VersionID: 7}, `{"query":"` + strings.Repeat("退", MaxClarificationSearchQueryLength+1) + `"}`, "query"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			searcher := &fakeClarificationSearcher{}
			tool := NewClarificationSearchTool(searcher)
			_, err := tool.Execute(context.Background(), testCase.scope, json.RawMessage(testCase.raw))
			var toolErr *Error
			if !errors.As(err, &toolErr) {
				t.Fatalf("err = %v, want a typed *tools.Error", err)
			}
			if toolErr.Code != CodeInvalidArgument {
				t.Fatalf("code = %q, want %q", toolErr.Code, CodeInvalidArgument)
			}
			if field, _ := toolErr.Details["field"].(string); field != testCase.field {
				t.Fatalf("field = %q, want %q (details %+v)", field, testCase.field, toolErr.Details)
			}
			if searcher.calls != 0 {
				t.Fatalf("searcher calls = %d, want 0 for invalid input", searcher.calls)
			}
		})
	}
}

// TestClarificationSearchPortErrors pins the error translation: business
// failures stay typed and model-visible, unexpected failures stay internal.
func TestClarificationSearchPortErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"invalid", platform.Invalid("query", "bad"), CodeInvalidArgument},
		{"not found", platform.NotFound("clarification"), CodeNotFound},
		{"conflict", platform.Conflict("busy"), CodeStateConflict},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			searcher := &fakeClarificationSearcher{err: testCase.err}
			tool := NewClarificationSearchTool(searcher)
			_, err := tool.Execute(context.Background(), Scope{VersionID: 7}, json.RawMessage(`{"query":"退款"}`))
			var toolErr *Error
			if !errors.As(err, &toolErr) || toolErr.Code != testCase.code {
				t.Fatalf("err = %v, want typed %q", err, testCase.code)
			}
		})
	}

	internal := errors.New("database is down")
	tool := NewClarificationSearchTool(&fakeClarificationSearcher{err: internal})
	_, err := tool.Execute(context.Background(), Scope{VersionID: 7}, json.RawMessage(`{"query":"退款"}`))
	var toolErr *Error
	if errors.As(err, &toolErr) {
		t.Fatalf("internal failure leaked as a model-visible tool error: %v", err)
	}
	if !errors.Is(err, internal) {
		t.Fatalf("err = %v, want the wrapped internal failure", err)
	}
}

// TestClarificationSearchBoundsSnippetAndLimit pins text safety: only snippets
// (never whole messages) leave the adapter, the tool truncates an oversized
// snippet again and honors the validated result cap.
func TestClarificationSearchBoundsSnippetAndLimit(t *testing.T) {
	huge := strings.Repeat("原", 10000)
	hits := make([]ClarificationSearchHit, 0, 6)
	for index := 0; index < 6; index++ {
		hits = append(hits, ClarificationSearchHit{
			VersionID: 7, Kind: ClarificationSearchKindMessage,
			MessageID: int64(9000 + index), Snippet: huge,
			Timestamp: clarificationSearchTime().Add(time.Duration(index) * time.Minute),
			Source:    "conversation_message:9000",
		})
	}
	searcher := &fakeClarificationSearcher{hits: hits}
	tool := NewClarificationSearchTool(searcher)
	value, err := tool.Execute(context.Background(), Scope{VersionID: 7}, json.RawMessage(`{"query":"原","limit":2}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	result := value.(*ClarificationSearchResult)
	if len(result.Results) != 2 || result.Count != 2 {
		t.Fatalf("results = %d, want the validated cap of 2", len(result.Results))
	}
	for _, hit := range result.Results {
		if runes := len([]rune(hit.Snippet)); runes > MaxClarificationSearchSnippet {
			t.Fatalf("snippet runes = %d, want <= %d", runes, MaxClarificationSearchSnippet)
		}
	}
}

// TestClarificationSearchSchemaHidesScope pins that the tool schema never tells
// the model about version_id: the scope is injected out of band.
func TestClarificationSearchSchemaHidesScope(t *testing.T) {
	tool := NewClarificationSearchTool(&fakeClarificationSearcher{})
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("decode parameters schema: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	if _, exposed := properties["version_id"]; exposed {
		t.Fatal("tool schema exposes version_id")
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false", schema["additionalProperties"])
	}
	for _, field := range []string{"query", "requirement_id", "kinds", "limit"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("schema is missing %q: %#v", field, properties)
		}
	}
}
