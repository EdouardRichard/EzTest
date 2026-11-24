package agent_test

// v0.2.4-D T10 (SC-D5, Q04=A-lite): agent_activity is the coarse fact ledger of
// tool calls and compaction boundaries. The type vocabulary has ONE authority
// here in the domain; the writers and the read side derive from it.

import (
	"errors"
	"strings"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestActivityTypeAuthority pins the two fact kinds of the Q04 ledger and their
// stable persisted spellings. The expected spellings come from the spec
// (SC-D5: type distinguishes tool_call/compaction), not from observed output.
func TestActivityTypeAuthority(t *testing.T) {
	if string(agent.ActivityTypeToolCall) != "tool_call" {
		t.Fatalf("ActivityTypeToolCall = %q, want tool_call", agent.ActivityTypeToolCall)
	}
	if string(agent.ActivityTypeCompaction) != "compaction" {
		t.Fatalf("ActivityTypeCompaction = %q, want compaction", agent.ActivityTypeCompaction)
	}
	want := []string{string(agent.ActivityTypeToolCall), string(agent.ActivityTypeCompaction)}
	got := agent.ActivityTypes()
	if len(got) != len(want) {
		t.Fatalf("ActivityTypes() = %v, want %v", got, want)
	}
	for index, value := range want {
		if got[index] != value {
			t.Fatalf("ActivityTypes()[%d] = %q, want %q", index, got[index], value)
		}
	}
	// Valid is derived from the authority, never a second hand-written switch.
	for _, value := range agent.ActivityTypes() {
		if !agent.ActivityType(value).Valid() {
			t.Fatalf("ActivityType(%q).Valid() = false, want true for an authority member", value)
		}
	}
	for _, value := range []string{"", "thinking", "tool", "TOOL_CALL"} {
		if agent.ActivityType(value).Valid() {
			t.Fatalf("ActivityType(%q).Valid() = true, want false outside the authority", value)
		}
	}
}

func TestActivityValidate(t *testing.T) {
	started := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	ended := started.Add(2 * time.Second)
	valid := agent.Activity{
		RunID:     42,
		Seq:       1,
		Type:      agent.ActivityTypeToolCall,
		Title:     "requirement.read",
		StartedAt: started,
		EndedAt:   &ended,
		Payload:   map[string]any{"tool": "requirement.read"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(valid) = %v", err)
	}

	cases := []struct {
		name   string
		field  string
		mutate func(*agent.Activity)
	}{
		{"missing run", "run_id", func(a *agent.Activity) { a.RunID = 0 }},
		{"missing seq", "seq", func(a *agent.Activity) { a.Seq = 0 }},
		{"negative seq", "seq", func(a *agent.Activity) { a.Seq = -1 }},
		{"unknown type", "type", func(a *agent.Activity) { a.Type = agent.ActivityType("thinking") }},
		{"end before start", "ended_at", func(a *agent.Activity) {
			before := started.Add(-time.Second)
			a.EndedAt = &before
		}},
	}
	for _, tc := range cases {
		row := valid
		tc.mutate(&row)
		err := row.Validate()
		var typed *platform.Error
		if !errors.As(err, &typed) || typed.Status != 400 {
			t.Fatalf("%s: Validate err = %v, want a 400 field error", tc.name, err)
		}
		if field, _ := typed.Details["field"].(string); field != tc.field {
			t.Fatalf("%s: field = %q, want %q", tc.name, field, tc.field)
		}
		// The type error text is DERIVED from the authority: every kind must be
		// named, so a third kind cannot silently drift out of the message
		// (T10 review #3, constitution 五.3).
		if tc.field == "type" {
			for _, kind := range agent.ActivityTypes() {
				if !strings.Contains(typed.Message, kind) {
					t.Fatalf("type error message %q does not name the authority kind %q", typed.Message, kind)
				}
			}
		}
	}
}
