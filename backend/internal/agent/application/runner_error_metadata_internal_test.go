package agentapp

// v0.22 W0 fix round 1 (Task 36 review Important 8): a structured tool failure
// must reach the UI error part with its retry metadata (retryable/field/
// allowed/hint), bounded by the version contract. appendToolParts is the single
// producer of those parts, so the internal test drives it directly.

import (
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func toolCallForTest(id, name string) llmapp.ToolCall {
	return llmapp.ToolCall{ID: id, Name: name, Arguments: `{}`}
}

func TestAppendToolPartsForwardsStructuredErrorMetadata(t *testing.T) {
	runner := &Runner{}
	toolErr := tools.InvalidWithAllowed("cases[0].assertions[0].database_id",
		"database_id is not bound to this version", []string{"11", "12"},
		"call sut.database_list first")

	state := &runState{}
	runner.appendToolParts(state, toolCallForTest("call_meta", "case.graph_propose"), `{"error":{}}`, toolErr, 5)
	part, ok := foundErrorPart(state.streamParts)
	if !ok {
		t.Fatalf("error part missing: %#v", state.streamParts)
	}
	if retryable, _ := part.Data["retryable"].(bool); !retryable {
		t.Fatalf("retryable = %#v, want true (InvalidWithAllowed is retryable)", part.Data["retryable"])
	}
	if part.Data["field"] != toolErr.Field {
		t.Fatalf("field = %#v, want %q", part.Data["field"], toolErr.Field)
	}
	if hint, _ := part.Data["hint"].(string); hint == "" || !strings.Contains(hint, "sut.database_list") {
		t.Fatalf("hint = %#v", part.Data["hint"])
	}
	allowed, ok := part.Data["allowed"].([]string)
	if !ok || len(allowed) != 2 || allowed[0] != "11" {
		t.Fatalf("allowed = %#v", part.Data["allowed"])
	}

	// The metadata is bounded: a tool error cannot persist an unbounded payload
	// in the conversation snapshot.
	bounded := tools.InvalidWithAllowed(strings.Repeat("f", version.MessageErrorFieldMax+20),
		"bad", nil, strings.Repeat("h", version.MessageErrorHintMax+20))
	for index := 0; index < version.MessageErrorAllowedMax+10; index++ {
		bounded.Allowed = append(bounded.Allowed, strings.Repeat("a", version.MessageErrorAllowedItemMax+20))
	}
	state = &runState{}
	runner.appendToolParts(state, toolCallForTest("call_bounded", "x"), `{}`, bounded, 1)
	part, ok = foundErrorPart(state.streamParts)
	if !ok {
		t.Fatalf("bounded error part missing: %#v", state.streamParts)
	}
	if field, _ := part.Data["field"].(string); len([]rune(field)) > version.MessageErrorFieldMax {
		t.Fatalf("field not bounded: %d runes", len([]rune(field)))
	}
	if hint, _ := part.Data["hint"].(string); len([]rune(hint)) > version.MessageErrorHintMax {
		t.Fatalf("hint not bounded: %d runes", len([]rune(hint)))
	}
	allowed, _ = part.Data["allowed"].([]string)
	if len(allowed) > version.MessageErrorAllowedMax {
		t.Fatalf("allowed not bounded: %d entries", len(allowed))
	}
	for _, value := range allowed {
		if len([]rune(value)) > version.MessageErrorAllowedItemMax {
			t.Fatalf("allowed entry not bounded: %d runes", len([]rune(value)))
		}
	}
}

func foundErrorPart(parts []ConversationPart) (ConversationPart, bool) {
	for _, part := range parts {
		if part.Type == "error" {
			return part, true
		}
	}
	return ConversationPart{}, false
}
