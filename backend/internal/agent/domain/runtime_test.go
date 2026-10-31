package agent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var testTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func TestAgentProfileValidate(t *testing.T) {
	valid := agent.AgentProfile{
		Key:          "clarification_host",
		Name:         "澄清 Host",
		SystemPrompt: "[profile:clarification_host] 你是澄清 Host",
		AllowedTools: []string{"requirement.read"},
		Budgets:      agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
		Enabled:      true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*agent.AgentProfile)
		field  string
	}{
		{"empty key", func(p *agent.AgentProfile) { p.Key = " " }, "key"},
		{"empty name", func(p *agent.AgentProfile) { p.Name = "" }, "name"},
		{"empty prompt", func(p *agent.AgentProfile) { p.SystemPrompt = "" }, "system_prompt"},
		{"negative budget", func(p *agent.AgentProfile) { p.Budgets.MaxTurns = -1 }, "budgets.max_turns"},
		{"bad soft/hard", func(p *agent.AgentProfile) { p.Budgets.SoftSeconds = 900 }, "budgets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := valid
			tc.mutate(&profile)
			err := profile.Validate()
			if err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
			if field := platform.AsError(err).Details["field"]; field != tc.field {
				t.Fatalf("field = %#v, want %q", field, tc.field)
			}
		})
	}
}

func TestAgentRunStatuses(t *testing.T) {
	for _, status := range []agent.AgentRunStatus{
		agent.AgentRunRunning,
		agent.AgentRunDone,
		agent.AgentRunFailed,
		agent.AgentRunAborted,
		agent.AgentRunInterrupted,
	} {
		if !status.Valid() {
			t.Fatalf("run status %q should be valid", status)
		}
	}
	if agent.AgentRunStatus("dead").Valid() {
		t.Fatal("unknown run status must be invalid")
	}
	for status, want := range map[agent.AgentRunStatus]bool{
		agent.AgentRunRunning:     false,
		agent.AgentRunDone:        true,
		agent.AgentRunFailed:      true,
		agent.AgentRunAborted:     true,
		agent.AgentRunInterrupted: true,
	} {
		if got := status.Terminal(); got != want {
			t.Fatalf("run status %q terminal = %v, want %v", status, got, want)
		}
	}

	run := agent.AgentRun{
		ProfileKey:    "clarification_host",
		Status:        agent.AgentRunRunning,
		OwnerInstance: "instance-1",
		LeaseEpoch:    3,
		Usage:         map[string]int{"input_tokens": 10},
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("valid run rejected: %v", err)
	}
	run.Status = "unknown"
	if err := run.Validate(); err == nil {
		t.Fatal("unknown run status must fail validation")
	}
	run.Status = agent.AgentRunRunning
	run.OwnerInstance = ""
	if err := run.Validate(); err == nil {
		t.Fatal("empty owner instance must fail validation")
	}
}

func TestAgentMessageValidate(t *testing.T) {
	valid := agent.AgentMessage{
		Role:      agent.MessageRoleAssistant,
		Parts:     []agent.MessagePart{{Type: "text", Text: "hello"}},
		ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "requirement.read", Arguments: `{}`}},
		Usage:     map[string]int{"output_tokens": 4},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid message rejected: %v", err)
	}
	for _, role := range []agent.MessageRole{
		agent.MessageRoleSystem,
		agent.MessageRoleUser,
		agent.MessageRoleAssistant,
		agent.MessageRoleTool,
	} {
		if !role.Valid() {
			t.Fatalf("role %q should be valid", role)
		}
	}
	bad := valid
	bad.Role = "critic"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown role must fail validation")
	}
}

func TestContextPacketValidateAndStableSections(t *testing.T) {
	packet := agent.ContextPacket{
		RunID:     9,
		VersionID: 10,
		Sections: []agent.ContextSection{
			{Name: "system", Content: "scope", Optional: false},
			{Name: "version_pack", Content: "title", Optional: true, Refs: []string{"version:10"}},
		},
		TokenCounts: map[string]int{"system": 20, "version_pack": 18},
		Hash:        strings.Repeat("a", 64),
		CreatedAt:   testTime,
	}
	if err := packet.Validate(); err != nil {
		t.Fatalf("valid packet rejected: %v", err)
	}
	bad := packet
	bad.Hash = "not-a-hash"
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid hash must fail validation")
	}
	bad = packet
	bad.Sections = []agent.ContextSection{{Name: "", Content: "x"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("empty section name must fail validation")
	}
}
