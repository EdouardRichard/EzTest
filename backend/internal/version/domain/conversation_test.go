package version_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestMessagePartJSONShapes(t *testing.T) {
	parts := []version.MessagePart{
		{Type: "text", Text: "hello"},
		{Type: "attachment", SHA256: strings.Repeat("a", 64), Name: "spec.md", MIME: "text/markdown", Size: 12},
		{Type: "requirement_card", RequirementID: 3001, Title: "Coupons", Status: "draft", Revision: 2},
		{Type: "rollback_proposal", RequirementID: 3001, Reason: "scope changed"},
	}
	for _, part := range parts {
		raw, err := json.Marshal(part)
		if err != nil {
			t.Fatalf("marshal %#v: %v", part, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if decoded["type"] != part.Type {
			t.Fatalf("type = %#v", decoded["type"])
		}
		switch part.Type {
		case "text":
			if decoded["text"] != "hello" {
				t.Fatalf("text = %s", raw)
			}
		case "attachment":
			if decoded["sha256"] != strings.Repeat("a", 64) || decoded["size"] != float64(12) {
				t.Fatalf("attachment = %s", raw)
			}
		case "requirement_card":
			if decoded["requirement_id"] != "3001" || decoded["title"] != "Coupons" ||
				decoded["status"] != "draft" || decoded["revision"] != float64(2) {
				t.Fatalf("card = %s", raw)
			}
		case "rollback_proposal":
			if decoded["requirement_id"] != "3001" || decoded["reason"] != "scope changed" {
				t.Fatalf("rollback = %s", raw)
			}
			if _, ok := decoded["requirement_title"]; ok {
				t.Fatalf("rollback must not expose requirement_title: %s", raw)
			}
		}

		var roundTripped version.MessagePart
		if err := json.Unmarshal(raw, &roundTripped); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if roundTripped.Type != part.Type {
			t.Fatalf("round trip = %#v", roundTripped)
		}
	}
}

func TestValidateUserMessageParts(t *testing.T) {
	valid := []version.MessagePart{
		{Type: "text", Text: "Find the coupon rules"},
		{Type: "attachment", SHA256: strings.Repeat("b", 64), Name: "spec.md", MIME: "text/markdown", Size: 42},
	}
	if err := version.ValidateUserMessageParts(valid); err != nil {
		t.Fatalf("valid parts rejected: %v", err)
	}

	tooMany := make([]version.MessagePart, 0, version.MaxMessageAttachments+1)
	for i := 0; i < version.MaxMessageAttachments+1; i++ {
		tooMany = append(tooMany, valid[1])
	}
	cases := []struct {
		name      string
		parts     []version.MessagePart
		wantField string
	}{
		{"empty", nil, "parts"},
		{"blank text", []version.MessagePart{{Type: "text", Text: "   "}}, "parts[0].text"},
		{"long text", []version.MessagePart{{Type: "text", Text: strings.Repeat("a", version.MessageTextMax+1)}}, "parts[0].text"},
		{"unknown type", []version.MessagePart{{Type: "requirement_card", RequirementID: 1}}, "parts[0].type"},
		{"bad sha", []version.MessagePart{{Type: "attachment", SHA256: "xyz", Name: "a.md", MIME: "text/markdown", Size: 1}}, "parts[0].sha256"},
		{"blank name", []version.MessagePart{{Type: "attachment", SHA256: strings.Repeat("b", 64), MIME: "text/markdown", Size: 1}}, "parts[0].name"},
		{"zero size", []version.MessagePart{{Type: "attachment", SHA256: strings.Repeat("b", 64), Name: "a.md", MIME: "text/markdown"}}, "parts[0].size"},
		{"too many attachments", tooMany, "parts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := version.ValidateUserMessageParts(tc.parts)
			if err == nil {
				t.Fatal("invalid parts accepted")
			}
			if field := requirementAPIField(t, err); field != tc.wantField {
				t.Fatalf("field = %q, want %q", field, tc.wantField)
			}
		})
	}
}

func TestValidateAssistantMessageParts(t *testing.T) {
	valid := []version.MessagePart{
		{Type: "text", Text: "Here is what I understood."},
		{Type: "requirement_card", RequirementID: 3001, Title: "Coupons", Status: "draft", Revision: 1},
		{Type: "rollback_proposal", RequirementID: 3001, Reason: "scope changed"},
	}
	if err := version.ValidateAssistantMessageParts(valid); err != nil {
		t.Fatalf("valid assistant parts rejected: %v", err)
	}

	cases := []struct {
		name      string
		parts     []version.MessagePart
		wantField string
	}{
		{"empty", nil, "parts"},
		{"card without id", []version.MessagePart{{Type: "requirement_card", Title: "T", Status: "draft", Revision: 1}}, "parts[0].requirement_id"},
		{"card bad status", []version.MessagePart{{Type: "requirement_card", RequirementID: 1, Title: "T", Status: "queued", Revision: 1}}, "parts[0].status"},
		{"card bad revision", []version.MessagePart{{Type: "requirement_card", RequirementID: 1, Title: "T", Status: "draft"}}, "parts[0].revision"},
		{"rollback without id", []version.MessagePart{{Type: "rollback_proposal", Reason: "x"}}, "parts[0].requirement_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := version.ValidateAssistantMessageParts(tc.parts)
			if err == nil {
				t.Fatal("invalid assistant parts accepted")
			}
			if field := requirementAPIField(t, err); field != tc.wantField {
				t.Fatalf("field = %q, want %q", field, tc.wantField)
			}
		})
	}
}

func TestConversationTitleAndKind(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if got := version.DefaultConversationTitle(now); got != "需求澄清 2026-10-31 12:00" {
		t.Fatalf("default title = %q", got)
	}
	if err := version.ValidateConversationKind("clarification"); err != nil {
		t.Fatalf("clarification rejected: %v", err)
	}
	if err := version.ValidateConversationKind("design"); err != nil {
		t.Fatalf("design kind rejected at domain level: %v", err)
	}
	if err := version.ValidateConversationKind("chat"); err == nil {
		t.Fatal("invalid kind accepted")
	}
	if err := version.ValidateConversationTitle(strings.Repeat("a", version.ConversationTitleMax+1)); err == nil {
		t.Fatal("long title accepted")
	}
	if err := version.ValidateConversationTitle("  "); err == nil {
		t.Fatal("blank title accepted")
	}
}

func TestValidateClientMessageID(t *testing.T) {
	if err := version.ValidateClientMessageID(strings.Repeat("a", 64)); err != nil {
		t.Fatalf("64-char id rejected: %v", err)
	}
	if err := version.ValidateClientMessageID(""); err != nil {
		t.Fatalf("empty id rejected: %v", err)
	}
	if err := version.ValidateClientMessageID(strings.Repeat("a", 65)); err == nil {
		t.Fatal("oversized client_message_id accepted")
	}
}

func TestValidateConversationSummary(t *testing.T) {
	pinned := make([]string, 0, version.PinnedFactsMax)
	for i := 0; i < version.PinnedFactsMax; i++ {
		pinned = append(pinned, "fact")
	}
	if err := version.ValidateConversationSummary("summary", pinned); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	if err := version.ValidateConversationSummary("summary", append(pinned, "extra")); err == nil {
		t.Fatal("too many pinned facts accepted")
	}
	if err := version.ValidateConversationSummary("summary", []string{strings.Repeat("x", version.PinnedFactMaxLength+1)}); err == nil {
		t.Fatal("oversized pinned fact accepted")
	}
	if err := version.ValidateConversationSummary("summary", []string{"  "}); err == nil {
		t.Fatal("blank pinned fact accepted")
	}
	if err := version.ValidateConversationSummary("  ", nil); err == nil {
		t.Fatal("blank summary accepted")
	}
}
