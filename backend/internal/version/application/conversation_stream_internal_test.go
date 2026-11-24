package versionapp

// Internal pin of streamDelta's mode selection (v0.2.4-D T5 review fix #1). The
// append mode is only legal when the previous snapshot decodes to the SAME part
// type and the new text purely extends it; a same-seq type change or a previous
// that fails to decode must be a full-snapshot replacement, never an
// append-from-empty (which the frontend would skip or mis-apply by offset).

import (
	"encoding/json"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func rawPart(t *testing.T, part version.MessagePart) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal part: %v", err)
	}
	return raw
}

func TestStreamDeltaMode(t *testing.T) {
	text := func(s string) version.MessagePart {
		return version.MessagePart{Type: version.MessagePartText, Text: s}
	}
	thinking := func(s string) version.MessagePart {
		return version.MessagePart{Type: version.MessagePartThinking, Text: s, DurationMS: 10}
	}
	toolCall := version.MessagePart{Type: version.MessagePartToolCall, CallID: "c1",
		Name: "context.task_pack", Status: "running", Arguments: json.RawMessage(`{"a":1}`)}

	cases := []struct {
		name          string
		previous      json.RawMessage
		part          version.MessagePart
		wantAppend    bool
		wantOffset    int
		wantIncrement string
	}{
		{"first text appends from empty", nil, text("Hel"), true, 0, "Hel"},
		{"first thinking appends from empty", nil, thinking("核对"), true, 0, "核对"},
		{"text growth appends suffix", rawPart(t, text("Hel")), text("Hello"), true, 3, "lo"},
		{"thinking growth appends suffix", rawPart(t, thinking("核对")), thinking("核对退款"), true, 2, "退款"},
		{"text rewrite is replacement", rawPart(t, text("Hello")), text("Goodbye"), false, 0, ""},
		{"text identical is replacement", rawPart(t, text("Hello")), text("Hello"), false, 0, ""},
		{"thinking duration-only is replacement", rawPart(t, thinking("核对")), thinking("核对"), false, 0, ""},
		// review fix #1: same-seq type change must NOT append-from-empty.
		{"type change thinking->text is replacement", rawPart(t, thinking("核对")), text("Hello"), false, 0, ""},
		{"type change text->thinking is replacement", rawPart(t, text("Hello")), thinking("核对"), false, 0, ""},
		{"type change tool_call->text is replacement", rawPart(t, toolCall), text("Hello"), false, 0, ""},
		// review fix #1: an undecodable previous must NOT append-from-empty.
		{"corrupt previous is replacement", json.RawMessage(`{not json`), text("Hello"), false, 0, ""},
		{"tool_call always replacement", nil, toolCall, false, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offset, increment, replacePart := streamDelta(tc.previous, tc.part)
			if tc.wantAppend {
				if increment != tc.wantIncrement || offset != tc.wantOffset || len(replacePart) != 0 {
					t.Fatalf("append mode = offset:%d increment:%q part:%s, want offset:%d increment:%q no part",
						offset, increment, replacePart, tc.wantOffset, tc.wantIncrement)
				}
				return
			}
			if increment != "" || offset != 0 || len(replacePart) == 0 {
				t.Fatalf("replace mode = offset:%d increment:%q part:%s, want offset 0 no increment + full part",
					offset, increment, replacePart)
			}
			var snapshot version.MessagePart
			if err := json.Unmarshal(replacePart, &snapshot); err != nil {
				t.Fatalf("decode replace part %s: %v", replacePart, err)
			}
			if snapshot.Type != tc.part.Type {
				t.Fatalf("replace part type = %q, want %q", snapshot.Type, tc.part.Type)
			}
		})
	}
}
