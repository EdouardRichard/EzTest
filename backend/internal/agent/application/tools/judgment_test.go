package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// judgmentSubmitRaw is a valid two-case design.judgment_submit payload: one pass
// with a minor note and one fail with a blocker finding carrying a case_ref.
const judgmentSubmitRaw = `{"requirement_id":"10","judgments":[` +
	`{"case_id":"7001","case_version_id":"5","verdict":"pass","findings":[` +
	`{"dimension":"coverage","severity":"minor","message":"补充边界注记"}]},` +
	`{"case_id":"7002","case_version_id":"6","verdict":"fail","findings":[` +
	`{"dimension":"executability","severity":"blocker","message":"步骤不可执行","case_ref":"7001"}]}]}`

func judgmentScope() Scope {
	return Scope{VersionID: 1001, RequirementID: 10, AgentRunID: 99}
}

// TestJudgmentSubmitToolSchemaAndExecute is the T13 core acceptance: the schema
// exposes the per-case verdict enum (pass/fail) and the findings array, Execute
// settles every case through DesignPort.SubmitJudgment, and the result struct's
// json tags carry the promised "submitted" surface (six-surface consistency).
func TestJudgmentSubmitToolSchemaAndExecute(t *testing.T) {
	port := &fakeDesignPort{judgmentResult: map[string]any{
		"judgment_id": "9001", "case_id": "7001", "case_version_id": "5",
		"content_hash": strings.Repeat("a", 64), "round": 1, "verdict": "pass",
	}}
	tool := &designJudgmentSubmit{deps: Deps{Design: port}}

	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	judgments, _ := props["judgments"].(map[string]any)
	if judgments["type"] != "array" {
		t.Fatalf("judgments schema = %#v, want array", judgments)
	}
	items, _ := judgments["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)
	verdict, _ := itemProps["verdict"].(map[string]any)
	enum, _ := verdict["enum"].([]any)
	if len(enum) != 2 || enum[0] != "pass" || enum[1] != "fail" {
		t.Fatalf("verdict enum = %#v, want [pass fail]", enum)
	}
	findings, _ := itemProps["findings"].(map[string]any)
	if findings["type"] != "array" {
		t.Fatalf("findings schema = %#v, want array", findings)
	}
	// Round is server-supplied: it must never appear in the model-facing schema.
	if strings.Contains(string(tool.Parameters()), "round") {
		t.Fatalf("schema must not expose round: %s", tool.Parameters())
	}

	res, err := tool.Execute(context.Background(), judgmentScope(), json.RawMessage(judgmentSubmitRaw))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if port.judgmentCalls != 2 {
		t.Fatalf("SubmitJudgment calls = %d, want 2 (one per case)", port.judgmentCalls)
	}
	if port.judgmentVersionID != 1001 || port.judgmentRequirementID != 10 || port.judgmentRunID != 99 {
		t.Fatalf("submit scope = (%d,%d,%d)", port.judgmentVersionID, port.judgmentRequirementID, port.judgmentRunID)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"submitted"`) {
		t.Fatalf("result tag mismatch: %s", b)
	}
	if !strings.Contains(string(b), `"count":2`) {
		t.Fatalf("result count mismatch: %s", b)
	}
	// The first settled candidate crossed the port with its parsed fields.
	first := port.judgmentInputs[0]
	if first.CaseID != 7001 || first.CaseVersionID != 5 || first.Verdict != "pass" {
		t.Fatalf("first judgment input = %#v", first)
	}
	if len(first.Findings) != 1 || first.Findings[0].Dimension != "coverage" ||
		first.Findings[0].Severity != "minor" || first.Findings[0].CaseRef != "" {
		t.Fatalf("first finding = %#v", first.Findings)
	}
	second := port.judgmentInputs[1]
	if second.Verdict != "fail" || len(second.Findings) != 1 || second.Findings[0].CaseRef != "7001" {
		t.Fatalf("second judgment input = %#v", second)
	}
}

// TestJudgmentSubmitToolName pins the published contract name.
func TestJudgmentSubmitToolName(t *testing.T) {
	tool := &designJudgmentSubmit{}
	if tool.Name() != ToolDesignJudgmentSubmit || tool.Name() != "design.judgment_submit" {
		t.Fatalf("name = %q", tool.Name())
	}
}

// TestJudgmentSubmitACCoverageMatrix pins the SC-E3 rubric ① / SC-E5 surface
// alignment (T13 fix round 1): the judge prompt teaches ac_coverage_matrix, so
// the tool must ACCEPT it (optional object), carry it through the DTO to the
// port, and still work without it. A non-object matrix is a precise field error.
func TestJudgmentSubmitACCoverageMatrix(t *testing.T) {
	tool := &designJudgmentSubmit{deps: Deps{Design: &fakeDesignPort{}}}

	// The schema exposes the optional object field the prompt names.
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	judgments, _ := props["judgments"].(map[string]any)
	items, _ := judgments["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)
	matrix, _ := itemProps["ac_coverage_matrix"].(map[string]any)
	if matrix["type"] != "object" {
		t.Fatalf("ac_coverage_matrix schema = %#v, want object", matrix)
	}
	required, _ := items["required"].([]any)
	for _, field := range required {
		if field == "ac_coverage_matrix" {
			t.Fatal("ac_coverage_matrix must stay optional")
		}
	}

	t.Run("with matrix reaches the port", func(t *testing.T) {
		port := &fakeDesignPort{}
		tool := &designJudgmentSubmit{deps: Deps{Design: port}}
		raw := `{"requirement_id":"10","judgments":[{"case_id":"7001","case_version_id":"5","verdict":"pass",` +
			`"ac_coverage_matrix":{"AC-1":{"covered":true,"case_refs":["7001"]},"AC-2":{"covered":false}}}]}`
		if _, err := tool.Execute(context.Background(), judgmentScope(), json.RawMessage(raw)); err != nil {
			t.Fatalf("execute with matrix: %v", err)
		}
		got := port.judgmentInputs[0].ACCoverageMatrix
		if len(got) != 2 {
			t.Fatalf("matrix = %#v, want 2 AC entries", got)
		}
		ac1, _ := got["AC-1"].(map[string]any)
		if ac1 == nil || ac1["covered"] != true {
			t.Fatalf("AC-1 entry = %#v", got["AC-1"])
		}
	})
	t.Run("without matrix still works", func(t *testing.T) {
		port := &fakeDesignPort{}
		tool := &designJudgmentSubmit{deps: Deps{Design: port}}
		raw := `{"requirement_id":"10","judgments":[{"case_id":"7001","case_version_id":"5","verdict":"pass"}]}`
		if _, err := tool.Execute(context.Background(), judgmentScope(), json.RawMessage(raw)); err != nil {
			t.Fatalf("execute without matrix: %v", err)
		}
		if port.judgmentInputs[0].ACCoverageMatrix != nil {
			t.Fatalf("absent matrix must stay nil: %#v", port.judgmentInputs[0].ACCoverageMatrix)
		}
	})
	t.Run("non-object matrix is a field error", func(t *testing.T) {
		raw := `{"requirement_id":"10","judgments":[{"case_id":"7001","case_version_id":"5","verdict":"pass","ac_coverage_matrix":["AC-1"]}]}`
		_, err := tool.Execute(context.Background(), judgmentScope(), json.RawMessage(raw))
		toolErr := asToolError(t, err)
		if field, _ := toolErr.Details["field"].(string); field != "judgments[0].ac_coverage_matrix" {
			t.Fatalf("field = %q, want judgments[0].ac_coverage_matrix", field)
		}
	})
}

// TestJudgmentSubmitSixSurfaces pins that the schema field names, the decode
// tags and the Invalid field paths agree (B invariant 9): a wrong-typed or
// unknown field is rejected with the exact JSON path the schema documents.
func TestJudgmentSubmitSixSurfaces(t *testing.T) {
	tool := &designJudgmentSubmit{deps: Deps{Design: &fakeDesignPort{}}}
	cases := []struct {
		name      string
		raw       string
		wantField string
	}{
		{"unknown top field", `{"requirement_id":"10","judgments":[],"bogus":1}`, "parameters"},
		{"bad verdict", `{"requirement_id":"10","judgments":[{"case_id":"7001","case_version_id":"5","verdict":"maybe"}]}`, "judgments[0].verdict"},
		{"bad severity", `{"requirement_id":"10","judgments":[{"case_id":"7001","case_version_id":"5","verdict":"fail","findings":[{"dimension":"coverage","severity":"critical","message":"x"}]}]}`, "judgments[0].findings[0].severity"},
		{"missing case_id", `{"requirement_id":"10","judgments":[{"case_version_id":"5","verdict":"pass"}]}`, "judgments[0].case_id"},
		{"missing case_version_id", `{"requirement_id":"10","judgments":[{"case_id":"7001","verdict":"pass"}]}`, "judgments[0].case_version_id"},
		{"empty dimension", `{"requirement_id":"10","judgments":[{"case_id":"7001","case_version_id":"5","verdict":"fail","findings":[{"dimension":"","severity":"minor","message":"x"}]}]}`, "judgments[0].findings[0].dimension"},
		{"empty message", `{"requirement_id":"10","judgments":[{"case_id":"7001","case_version_id":"5","verdict":"fail","findings":[{"dimension":"coverage","severity":"minor","message":""}]}]}`, "judgments[0].findings[0].message"},
		{"empty judgments", `{"requirement_id":"10","judgments":[]}`, "judgments"},
		{"requirement mismatch", `{"requirement_id":"999","judgments":[{"case_id":"7001","case_version_id":"5","verdict":"pass"}]}`, "requirement_id"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), judgmentScope(), json.RawMessage(testCase.raw))
			if err == nil {
				t.Fatal("expected a field error")
			}
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument {
				t.Fatalf("code = %q, want invalid_argument", toolErr.Code)
			}
			field, _ := toolErr.Details["field"].(string)
			if field != testCase.wantField {
				t.Fatalf("field = %q, want %q (err=%v)", field, testCase.wantField, err)
			}
		})
	}
}

// TestJudgmentSubmitRequiresRequirementScope pins that an unscoped run is
// rejected before any port call (the judge run is requirement-scoped).
func TestJudgmentSubmitRequiresRequirementScope(t *testing.T) {
	tool := &designJudgmentSubmit{deps: Deps{Design: &fakeDesignPort{}}}
	_, err := tool.Execute(context.Background(), Scope{VersionID: 1001}, json.RawMessage(judgmentSubmitRaw))
	if err == nil {
		t.Fatal("expected a requirement scope error")
	}
}

// TestJudgmentSubmitMissingDepFailsClosed pins the internal configuration error
// when the design port is unwired (never a model-visible business error).
func TestJudgmentSubmitMissingDepFailsClosed(t *testing.T) {
	tool := &designJudgmentSubmit{deps: Deps{}}
	_, err := tool.Execute(context.Background(), judgmentScope(), json.RawMessage(judgmentSubmitRaw))
	if err == nil {
		t.Fatal("expected a missing dependency error")
	}
	var toolErr *Error
	if errorsAs(err, &toolErr) {
		t.Fatalf("missing dep must be an internal error, got tools.Error: %v", err)
	}
}

// TestJudgmentSubmitRegistered pins the tool is part of the design tool group
// (and therefore the runtime + admin catalog registries).
func TestJudgmentSubmitRegistered(t *testing.T) {
	registry := NewDesignRegistry(Deps{Design: &fakeDesignPort{}})
	if _, ok := registry.Get(ToolDesignJudgmentSubmit); !ok {
		t.Fatal("design.judgment_submit not registered in the design tool group")
	}
	if _, ok := AdminCatalogRegistry().Get(ToolDesignJudgmentSubmit); !ok {
		t.Fatal("design.judgment_submit not registered in the admin catalog")
	}
}
