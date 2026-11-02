package agentapi_test

// P4-T4: the Agent center run DTO exposes the actual model, config,
// fallback_from and the token/cache accounting recorded on agent_run
// (spec §7.3/§7.5). Frontend contracts stay untouched: the fields are additive.

import (
	"net/http"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

func TestAgentRunDTOExposesObservability(t *testing.T) {
	service := &stubAgentCenterService{runs: []agentapp.RunView{{
		ID:         31,
		ProfileKey: agentapp.ProfileKeyCaseExecutor,
		Status:     "failed",
		Turns:      2,
		Model:      "actual-llm",
		ConfigID:   3,
		Tokens:     map[string]int{"prompt_tokens": 120, "completion_tokens": 30, "total_tokens": 150},
		Cache:      map[string]int{},
	}}}
	handler := newAgentCenterServer(t, service)

	recorder := agentCenterRequest(t, handler, "/api/agent-center/runs", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	runs := decodeAgentCenterBody(t, recorder)["data"].([]any)
	run := runs[0].(map[string]any)
	if run["model"] != "actual-llm" {
		t.Fatalf("model = %#v, want the actual model", run["model"])
	}
	if run["config_id"] != "3" {
		t.Fatalf("config_id = %#v, want \"3\"", run["config_id"])
	}
	if fallback, ok := run["fallback_from"]; !ok || fallback != "" {
		t.Fatalf("fallback_from = %#v (present=%v), want an empty string", fallback, ok)
	}
	tokens, ok := run["tokens"].(map[string]any)
	if !ok || tokens["total_tokens"].(float64) != 150 {
		t.Fatalf("tokens = %#v", run["tokens"])
	}
	if _, ok := run["cache"].(map[string]any); !ok {
		t.Fatalf("cache = %#v, want a JSON object", run["cache"])
	}
}

// TestAgentRunDTOOmitsUnsetConfig pins the nullable config pointer: a run whose
// config was not recorded (legacy rows) reports null rather than a fake 0.
func TestAgentRunDTOOmitsUnsetConfig(t *testing.T) {
	service := &stubAgentCenterService{runs: []agentapp.RunView{{
		ID: 32, ProfileKey: agentapp.ProfileKeyCaseExecutor, Status: "done",
		Tokens: map[string]int{}, Cache: map[string]int{},
	}}}
	handler := newAgentCenterServer(t, service)

	recorder := agentCenterRequest(t, handler, "/api/agent-center/runs", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	run := decodeAgentCenterBody(t, recorder)["data"].([]any)[0].(map[string]any)
	if run["config_id"] != nil {
		t.Fatalf("config_id = %#v, want null", run["config_id"])
	}
	if run["tokens"] == nil || run["cache"] == nil {
		t.Fatalf("tokens/cache = %#v/%#v, want empty objects", run["tokens"], run["cache"])
	}
}
