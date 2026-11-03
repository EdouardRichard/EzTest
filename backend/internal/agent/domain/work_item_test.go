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

	// Deterministic ops tasks (report.render_pdf) use their own team so the
	// generic agent worker never claims them.
	ops := valid
	ops.Team = string(agent.TeamOps)
	ops.Type = "report.render_pdf"
	ops.DedupeKey = "report:1:rev:1"
	if err := ops.Validate(); err != nil {
		t.Fatalf("ops item rejected: %v", err)
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

// TestStageEntryWorkItemClaimDefaults pins the claimability defaults of the
// 0.2.1 contract items (Task 65). A stage-entry or background kind must land in
// a product team the agent worker claims, and the frozen type must carry the
// kind so the runner can resolve its profile/port. Legacy 0.2.0 NewWorkItem
// semantics (explicit team/type, ops default for unknown kinds) stay unchanged.
func TestStageEntryWorkItemClaimDefaults(t *testing.T) {
	cases := []struct {
		kind string
		team agent.Team
	}{
		{agent.WorkItemKindDesignCases, agent.TeamDesign},
		{agent.WorkItemKindReviewCases, agent.TeamDesign},
		{agent.WorkItemKindExecuteBatch, agent.TeamExecution},
		{agent.WorkItemKindExploreLogin, agent.TeamClarification},
		{agent.WorkItemKindExplorePage, agent.TeamClarification},
		{agent.WorkItemKindExploreAPI, agent.TeamClarification},
		{agent.WorkItemKindExploreCoverage, agent.TeamClarification},
		{agent.WorkItemKindExploreGap, agent.TeamClarification},
		{agent.WorkItemKindAnalyzeFailure, agent.TeamExecution},
		{agent.WorkItemKindReportVersion, agent.TeamDelivery},
		{agent.WorkItemKindKnowledgePropose, agent.TeamClarification},
	}
	for _, testCase := range cases {
		t.Run(testCase.kind, func(t *testing.T) {
			normalized := agent.CreateWorkItemParams{VersionID: 7, Kind: testCase.kind}.Normalize()
			if normalized.Team != testCase.team {
				t.Fatalf("team = %q, want %q", normalized.Team, testCase.team)
			}
			if normalized.Type != testCase.kind {
				t.Fatalf("type = %q, want the kind %q", normalized.Type, testCase.kind)
			}
		})
	}

	// An explicit legacy team/type keeps winning: the dispatcher may still
	// address a specific owner without losing the 0.2.0 column semantics.
	explicit := agent.CreateWorkItemParams{
		VersionID: 7,
		Kind:      agent.WorkItemKindDesignCases,
		Team:      agent.TeamSteward,
		Type:      "design.round",
	}.Normalize()
	if explicit.Team != agent.TeamSteward || explicit.Type != "design.round" {
		t.Fatalf("explicit overrides = %q/%q", explicit.Team, explicit.Type)
	}

	// Unknown/legacy kinds keep the 0.2.1 compatibility default (the worker
	// never claims ops, so an unmapped kind stays inert instead of being
	// stolen by the wrong executor).
	unmapped := agent.CreateWorkItemParams{VersionID: 7, Kind: "legacy.unknown"}.Normalize()
	if unmapped.Team != agent.TeamOps {
		t.Fatalf("unmapped team = %q, want ops", unmapped.Team)
	}
	if unmapped.Type != "legacy.unknown" {
		t.Fatalf("unmapped type = %q", unmapped.Type)
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
