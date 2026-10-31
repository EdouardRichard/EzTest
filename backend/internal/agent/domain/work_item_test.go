package agent_test

import (
	"encoding/json"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestWorkItemStatusValidityAndTerminal(t *testing.T) {
	valid := []agent.WorkItemStatus{
		agent.WorkItemOpen,
		agent.WorkItemClaimed,
		agent.WorkItemRunning,
		agent.WorkItemWaitingHuman,
		agent.WorkItemDone,
		agent.WorkItemFailed,
		agent.WorkItemDead,
	}
	for _, status := range valid {
		if !status.Valid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if agent.WorkItemStatus("unknown").Valid() {
		t.Fatal("unknown status must be invalid")
	}

	terminal := map[agent.WorkItemStatus]bool{
		agent.WorkItemOpen:         false,
		agent.WorkItemClaimed:      false,
		agent.WorkItemRunning:      false,
		agent.WorkItemWaitingHuman: false,
		agent.WorkItemDone:         true,
		agent.WorkItemFailed:       true,
		agent.WorkItemDead:         true,
	}
	for status, want := range terminal {
		if got := status.Terminal(); got != want {
			t.Fatalf("status %q terminal = %v, want %v", status, got, want)
		}
	}
}

func TestValidateNewWorkItem(t *testing.T) {
	valid := agent.NewWorkItem{
		VersionID:   101,
		Team:        "clarification",
		Type:        "chat.turn",
		Priority:    5,
		DedupeKey:   "reply:7",
		Payload:     map[string]any{"conversation_id": 9},
		MaxAttempts: 3,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid item rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*agent.NewWorkItem)
		field  string
	}{
		{"missing version", func(in *agent.NewWorkItem) { in.VersionID = 0 }, "version_id"},
		{"unknown team", func(in *agent.NewWorkItem) { in.Team = "nope" }, "team"},
		{"empty type", func(in *agent.NewWorkItem) { in.Type = " " }, "type"},
		{"empty dedupe", func(in *agent.NewWorkItem) { in.DedupeKey = "" }, "dedupe_key"},
		{"too many attempts", func(in *agent.NewWorkItem) { in.MaxAttempts = 11 }, "max_attempts"},
		{"negative attempts", func(in *agent.NewWorkItem) { in.MaxAttempts = -1 }, "max_attempts"},
		{"unencodable payload", func(in *agent.NewWorkItem) { in.Payload = make(chan int) }, "payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.mutate(&input)
			err := input.Validate()
			if err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
			apiErr := platform.AsError(err)
			if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
				t.Fatalf("error = %#v", apiErr)
			}
			if field, _ := apiErr.Details["field"].(string); field != tc.field {
				t.Fatalf("field = %q, want %q", field, tc.field)
			}
		})
	}
}

func TestWorkItemIsMutableSnapshot(t *testing.T) {
	payload := json.RawMessage(`{"conversation_id":"9"}`)
	item := agent.WorkItem{
		ID:          1,
		VersionID:   2,
		Team:        "clarification",
		Type:        "chat.turn",
		Status:      agent.WorkItemOpen,
		DedupeKey:   "opening:9",
		Payload:     payload,
		MaxAttempts: 3,
	}
	if item.Status.Valid() != true {
		t.Fatal("work item status should validate")
	}
	if string(item.Payload) != `{"conversation_id":"9"}` {
		t.Fatalf("payload = %s", item.Payload)
	}
}
