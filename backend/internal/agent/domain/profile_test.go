package domain_test

import (
	"slices"
	"strings"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// builtinPrompt is the system prompt mandated by the design spec §5.4. The
// builtin profile is seeded at startup, so its wording is part of the product
// contract and is pinned verbatim here.
const builtinPrompt = `你是 EzTest 的运行时验证助手，用来验证 Agent 运行时（会话、工具、活动流、预算、中止）是否正常工作。
你可以只读地检查 EzTest 自身：用 http.get 访问本机公开端点（如 http://localhost:13333/api/health），
用 file.list / file.read 阅读仓库内文档与代码，用 test.echo / test.fail 验证工具链路。
规则：
1. 只读，不修改任何文件，不访问只读根之外或白名单之外的目标。
2. 需要事实时先调用工具，不要凭空推测；工具失败时如实说明失败原因。
3. 回答简洁：说明检查了什么、看到了什么、结论是什么；需要时可分点。
4. 不要尝试获取或输出任何密钥、令牌、密码或 .env 内容。`

func TestBuiltinProfileSeedValues(t *testing.T) {
	if agent.BuiltinProfileKey != "runtime-diagnostic" {
		t.Fatalf("BuiltinProfileKey=%q want runtime-diagnostic", agent.BuiltinProfileKey)
	}
	p := agent.BuiltinProfile()
	if p.Key != agent.BuiltinProfileKey {
		t.Fatalf("key=%q want %q", p.Key, agent.BuiltinProfileKey)
	}
	if p.Name != "运行时验证 Agent" {
		t.Fatalf("name=%q", p.Name)
	}
	if !p.Builtin || !p.Enabled {
		t.Fatalf("builtin=%v enabled=%v, both must be true", p.Builtin, p.Enabled)
	}
	if p.SystemPrompt != builtinPrompt {
		t.Fatalf("system_prompt does not match spec §5.4:\n%q", p.SystemPrompt)
	}
	if p.Model != "" {
		t.Fatalf("model=%q, want empty (default LLM configuration)", p.Model)
	}
	wantTools := []string{"file.list", "file.read", "http.get", "test.echo", "test.fail"}
	if !slices.Equal(p.AllowedTools, wantTools) {
		t.Fatalf("allowed_tools=%v want %v", p.AllowedTools, wantTools)
	}
	if p.MaxTurns != 12 || p.RunSeconds != 600 || p.HardTimeoutSeconds != 60 {
		t.Fatalf("budgets max_turns=%d run_seconds=%d hard_timeout_seconds=%d want 12/600/60",
			p.MaxTurns, p.RunSeconds, p.HardTimeoutSeconds)
	}
	if p.Thinking != "enabled" || p.ReasoningEffort != "max" || p.MaxOutputTokens != 32768 {
		t.Fatalf("thinking=%q effort=%q max_output=%d want enabled/max/32768",
			p.Thinking, p.ReasoningEffort, p.MaxOutputTokens)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("builtin profile is invalid: %v", err)
	}
}

func TestProfileApplyValidatesEveryField(t *testing.T) {
	p := agent.BuiltinProfile()
	if err := p.Validate(); err != nil {
		t.Fatalf("builtin profile invalid: %v", err)
	}

	// Every value below must be rejected by the §10.3 validation ranges. A
	// failed Apply must leave the profile untouched.
	cases := []struct {
		name   string
		params agent.UpdateParams
	}{
		{name: "max_turns negative", params: agent.UpdateParams{MaxTurns: ptr(-1)}},
		{name: "max_turns too large", params: agent.UpdateParams{MaxTurns: ptr(101)}},
		{name: "run_seconds too small", params: agent.UpdateParams{RunSeconds: ptr(29)}},
		{name: "run_seconds too large", params: agent.UpdateParams{RunSeconds: ptr(3601)}},
		{name: "hard_timeout too small", params: agent.UpdateParams{HardTimeoutSeconds: ptr(4)}},
		{name: "hard_timeout too large", params: agent.UpdateParams{HardTimeoutSeconds: ptr(301)}},
		{name: "max_output_tokens too small", params: agent.UpdateParams{MaxOutputTokens: ptr(1023)}},
		{name: "max_output_tokens too large", params: agent.UpdateParams{MaxOutputTokens: ptr(131073)}},
		{name: "empty system prompt", params: agent.UpdateParams{SystemPrompt: ptr("")}},
		{name: "system prompt too long", params: agent.UpdateParams{SystemPrompt: ptr(strings.Repeat("x", 32769))}},
		{name: "unknown thinking", params: agent.UpdateParams{Thinking: ptr("sometimes")}},
		{name: "unknown reasoning effort", params: agent.UpdateParams{ReasoningEffort: ptr("minimal")}},
		{name: "disabled thinking with max effort", params: agent.UpdateParams{
			Thinking: ptr("disabled"), ReasoningEffort: ptr("max"),
		}},
		{name: "disabled thinking with low effort", params: agent.UpdateParams{
			Thinking: ptr("disabled"), ReasoningEffort: ptr("low"),
		}},
		{name: "duplicate allowed tools", params: agent.UpdateParams{
			AllowedTools: &[]string{"file.read", "file.read"},
		}},
		{name: "blank allowed tool", params: agent.UpdateParams{
			AllowedTools: &[]string{"file.read", " "},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := p
			if err := p.Apply(tc.params); err == nil {
				t.Fatalf("Apply(%+v) succeeded, want validation error", tc.params)
			}
			if p.MaxTurns != original.MaxTurns || p.Thinking != original.Thinking ||
				p.SystemPrompt != original.SystemPrompt || !slices.Equal(p.AllowedTools, original.AllowedTools) {
				t.Fatalf("failed Apply mutated the profile: %+v", p)
			}
		})
	}
}

func TestProfileApplyUpdatesOnlyProvidedFields(t *testing.T) {
	p := agent.BuiltinProfile()
	original := p

	maxTurns := 24
	prompt := "自定义提示词"
	model := "custom-model"
	tools := []string{"file.list"}
	thinking := "disabled"
	effort := "none"
	maxOutput := 4096
	enabled := false
	params := agent.UpdateParams{
		SystemPrompt:    &prompt,
		Model:           &model,
		AllowedTools:    &tools,
		MaxTurns:        &maxTurns,
		Thinking:        &thinking,
		ReasoningEffort: &effort,
		MaxOutputTokens: &maxOutput,
		Enabled:         &enabled,
	}
	if err := p.Apply(params); err != nil {
		t.Fatalf("valid Apply failed: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("profile after Apply is invalid: %v", err)
	}
	if p.SystemPrompt != prompt || p.Model != model || p.MaxTurns != maxTurns ||
		p.Thinking != thinking || p.ReasoningEffort != effort || p.MaxOutputTokens != maxOutput ||
		p.Enabled != enabled || !slices.Equal(p.AllowedTools, tools) {
		t.Fatalf("Apply did not update the requested fields: %+v", p)
	}
	// Fields absent from the params keep their previous values.
	if p.RunSeconds != original.RunSeconds || p.HardTimeoutSeconds != original.HardTimeoutSeconds ||
		p.Name != original.Name || p.Key != original.Key || p.Builtin != original.Builtin {
		t.Fatalf("Apply changed fields that were not requested: %+v", p)
	}
	// The caller must not be able to mutate the profile through the parameter
	// slice after Apply returned.
	tools[0] = "mutated"
	if p.AllowedTools[0] != "file.list" {
		t.Fatal("Apply aliased the caller's allowed_tools slice")
	}
}

func TestProfileValidateAcceptsBoundariesAndRejectsBadKey(t *testing.T) {
	cases := []struct {
		name   string
		params agent.UpdateParams
	}{
		{name: "zero max turns means unlimited", params: agent.UpdateParams{MaxTurns: ptr(0)}},
		{name: "max turns upper bound", params: agent.UpdateParams{MaxTurns: ptr(100)}},
		{name: "run seconds lower bound", params: agent.UpdateParams{RunSeconds: ptr(30)}},
		{name: "run seconds upper bound", params: agent.UpdateParams{RunSeconds: ptr(3600)}},
		{name: "hard timeout lower bound", params: agent.UpdateParams{HardTimeoutSeconds: ptr(5)}},
		{name: "hard timeout upper bound", params: agent.UpdateParams{HardTimeoutSeconds: ptr(300)}},
		{name: "max output lower bound", params: agent.UpdateParams{MaxOutputTokens: ptr(1024)}},
		{name: "max output upper bound", params: agent.UpdateParams{MaxOutputTokens: ptr(131072)}},
		{name: "auto thinking any effort", params: agent.UpdateParams{Thinking: ptr("auto")}},
		{name: "empty reasoning effort", params: agent.UpdateParams{ReasoningEffort: ptr("")}},
		{name: "disabled thinking with empty effort", params: agent.UpdateParams{Thinking: ptr("disabled"), ReasoningEffort: ptr("")}},
		{name: "disabled thinking with none effort", params: agent.UpdateParams{Thinking: ptr("disabled"), ReasoningEffort: ptr("none")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := agent.BuiltinProfile()
			if err := p.Apply(tc.params); err != nil {
				t.Fatalf("Apply(%+v) failed: %v", tc.params, err)
			}
		})
	}

	invalidKeys := []string{"", "Runtime", "1st", "with space", "bad/key"}
	for _, key := range invalidKeys {
		t.Run("key "+key, func(t *testing.T) {
			p := agent.BuiltinProfile()
			p.Key = key
			if err := p.Validate(); err == nil {
				t.Fatalf("Validate accepted invalid key %q", key)
			}
		})
	}

	p := agent.BuiltinProfile()
	p.Name = "  "
	if err := p.Validate(); err == nil {
		t.Fatal("Validate accepted a blank name")
	}
}

func ptr[T any](value T) *T {
	return &value
}
