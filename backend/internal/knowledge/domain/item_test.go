package knowledge_test

import (
	"strings"
	"testing"

	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestCanTransition(t *testing.T) {
	statuses := []knowledge.Status{
		knowledge.StatusDraft,
		knowledge.StatusApproved,
		knowledge.StatusRejected,
		knowledge.StatusRevoked,
	}
	allowed := map[[2]knowledge.Status]bool{
		{knowledge.StatusDraft, knowledge.StatusApproved}:   true,
		{knowledge.StatusDraft, knowledge.StatusRejected}:   true,
		{knowledge.StatusApproved, knowledge.StatusRevoked}: true,
		{knowledge.StatusRevoked, knowledge.StatusApproved}: true,
		{knowledge.StatusRejected, knowledge.StatusDraft}:   true,
	}
	for _, from := range statuses {
		for _, to := range statuses {
			want := allowed[[2]knowledge.Status{from, to}]
			if got := knowledge.CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
	if knowledge.CanTransition("bogus", knowledge.StatusApproved) {
		t.Fatal("unknown source status must not transition")
	}
	if knowledge.CanTransition(knowledge.StatusApproved, "bogus") {
		t.Fatal("unknown target status must not transition")
	}
}

func TestNormalizeScope(t *testing.T) {
	lower := "payments"
	globalKey := "payments"
	tests := []struct {
		name    string
		scope   knowledge.Scope
		key     *string
		wantErr string
		wantKey *string
	}{
		{name: "global without key", scope: knowledge.ScopeGlobal},
		{name: "global rejects key", scope: knowledge.ScopeGlobal, key: &globalKey, wantErr: "system_key"},
		{name: "system requires key", scope: knowledge.ScopeSystem, wantErr: "system_key"},
		{name: "system accepts lower slug", scope: knowledge.ScopeSystem, key: &lower, wantKey: &lower},
		{name: "system rejects uppercase", scope: knowledge.ScopeSystem, key: stringPtr("Payments"), wantErr: "system_key"},
		{name: "system rejects blank", scope: knowledge.ScopeSystem, key: stringPtr("   "), wantErr: "system_key"},
		{name: "system rejects spaces", scope: knowledge.ScopeSystem, key: stringPtr("pay ments"), wantErr: "system_key"},
		{name: "system rejects 65 runes", scope: knowledge.ScopeSystem, key: stringPtr(strings.Repeat("a", 65)), wantErr: "system_key"},
		{name: "unknown scope", scope: knowledge.Scope("tenant"), wantErr: "scope"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, gotKey, err := knowledge.NormalizeScope(test.scope, test.key)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if test.wantKey == nil && gotKey != nil {
					t.Fatalf("key = %v, want nil", *gotKey)
				}
				if test.wantKey != nil && (gotKey == nil || *gotKey != *test.wantKey) {
					t.Fatalf("key = %v, want %q", gotKey, *test.wantKey)
				}
				return
			}
			assertInvalidField(t, err, test.wantErr)
		})
	}
}

func TestValidateBounds(t *testing.T) {
	system := knowledge.ScopeSystem
	tests := []struct {
		name    string
		scope   knowledge.Scope
		key     *string
		title   string
		content string
		tags    []string
		wantErr string
	}{
		{name: "valid global", scope: knowledge.ScopeGlobal, title: "Retry contract", content: "Callback must be idempotent.", tags: []string{"payments"}},
		{name: "valid system", scope: system, key: stringPtr("payments"), title: "t", content: "c"},
		{name: "empty title", scope: knowledge.ScopeGlobal, title: "   ", content: "c", wantErr: "title"},
		{name: "title 200 runes", scope: knowledge.ScopeGlobal, title: strings.Repeat("题", 200), content: "c"},
		{name: "title 201 runes", scope: knowledge.ScopeGlobal, title: strings.Repeat("题", 201), content: "c", wantErr: "title"},
		{name: "empty content", scope: knowledge.ScopeGlobal, title: "t", content: " ", wantErr: "content"},
		{name: "content 20000 runes", scope: knowledge.ScopeGlobal, title: "t", content: strings.Repeat("内", 20000)},
		{name: "content 20001 runes", scope: knowledge.ScopeGlobal, title: "t", content: strings.Repeat("内", 20001), wantErr: "content"},
		{name: "empty tag", scope: knowledge.ScopeGlobal, title: "t", content: "c", tags: []string{""}, wantErr: "tags"},
		{name: "tag 40 runes", scope: knowledge.ScopeGlobal, title: "t", content: "c", tags: []string{strings.Repeat("a", 40)}},
		{name: "tag 41 runes", scope: knowledge.ScopeGlobal, title: "t", content: "c", tags: []string{strings.Repeat("a", 41)}, wantErr: "tags"},
		{name: "20 tags", scope: knowledge.ScopeGlobal, title: "t", content: "c", tags: nTags(20)},
		{name: "21 tags", scope: knowledge.ScopeGlobal, title: "t", content: "c", tags: nTags(21), wantErr: "tags"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := knowledge.Validate(test.scope, test.key, test.title, test.content, test.tags)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			assertInvalidField(t, err, test.wantErr)
		})
	}
}

func TestNormalizeTitleContentTags(t *testing.T) {
	title, err := knowledge.NormalizeTitle("  Retry contract  ")
	if err != nil || title != "Retry contract" {
		t.Fatalf("title = %q err = %v", title, err)
	}
	content, err := knowledge.NormalizeContent("  body  ")
	if err != nil || content != "body" {
		t.Fatalf("content = %q err = %v", content, err)
	}
	tags, err := knowledge.NormalizeTags([]string{" payments ", "payments", "callback", ""})
	if err == nil {
		t.Fatal("empty tag must be rejected")
	}
	_ = tags
	tags, err = knowledge.NormalizeTags([]string{" payments ", "payments", "callback"})
	if err != nil {
		t.Fatalf("normalize tags: %v", err)
	}
	if len(tags) != 2 || tags[0] != "payments" || tags[1] != "callback" {
		t.Fatalf("tags = %#v", tags)
	}
	if empty, err := knowledge.NormalizeTags(nil); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("nil tags = %#v err = %v", empty, err)
	}
}

func TestValidateReason(t *testing.T) {
	if _, err := knowledge.ValidateReason("  "); err == nil {
		t.Fatal("blank reason must be rejected")
	}
	reason, err := knowledge.ValidateReason("  duplicated by newer convention  ")
	if err != nil || reason != "duplicated by newer convention" {
		t.Fatalf("reason = %q err = %v", reason, err)
	}
	if _, err := knowledge.ValidateReason(strings.Repeat("r", 2001)); err == nil {
		t.Fatal("2001 rune reason must be rejected")
	}
	if _, err := knowledge.ValidateReason(strings.Repeat("r", 2000)); err != nil {
		t.Fatalf("2000 rune reason must be accepted: %v", err)
	}
}

func TestTokenEstimate(t *testing.T) {
	item := knowledge.Item{
		Title:   "retry",
		Content: strings.Repeat("x", 30),
		Tags:    []string{"abc"},
	}
	// (5 + 30 + 3) / 3 = 12
	if got := item.TokenEstimate(); got != 12 {
		t.Fatalf("TokenEstimate = %d, want 12", got)
	}
	if got := (knowledge.Item{Title: "t"}).TokenEstimate(); got != 1 {
		t.Fatalf("minimum TokenEstimate = %d, want 1", got)
	}
}

func TestSnippet(t *testing.T) {
	item := knowledge.Item{Content: "short body"}
	if got := item.Snippet(); got != "short body" {
		t.Fatalf("snippet = %q", got)
	}
	long := knowledge.Item{Content: strings.Repeat("界", knowledge.SnippetMaxRunes+10)}
	got := long.Snippet()
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated snippet must end with ellipsis: %q", got)
	}
	if runes := []rune(strings.TrimSuffix(got, "…")); len(runes) != knowledge.SnippetMaxRunes {
		t.Fatalf("truncated snippet runes = %d, want %d", len(runes), knowledge.SnippetMaxRunes)
	}
}

func stringPtr(value string) *string { return &value }

func nTags(count int) []string {
	tags := make([]string, 0, count)
	for i := 0; i < count; i++ {
		tags = append(tags, "tag-"+string(rune('a'+i)))
	}
	return tags
}

func assertInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected invalid_argument for %s, got nil", field)
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("error = %+v, want 400 invalid_argument", apiErr)
	}
	if details := apiErr.Details; details == nil || details["field"] != field {
		t.Fatalf("error details = %#v, want field %q", details, field)
	}
}
