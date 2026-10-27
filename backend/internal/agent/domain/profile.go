package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// BuiltinProfileKey is the stable key of the builtin runtime diagnostic
// profile. It is seeded once at startup and can never be renamed or deleted.
const BuiltinProfileKey = "runtime-diagnostic"

// profileKeyPattern mirrors the agent_profiles.key CHECK constraint.
const profileKeyPattern = `^[a-z][a-z0-9_-]*$`

// Validation bounds from the design spec §10.3. They mirror the DDL checks so
// an invalid update is rejected before it reaches PostgreSQL.
const (
	maxTurnsLimit         = 100
	minRunSeconds         = 30
	maxRunSeconds         = 3600
	minHardTimeoutSeconds = 5
	maxHardTimeoutSeconds = 300
	minMaxOutputTokens    = 1024
	maxMaxOutputTokens    = 131072
	maxSystemPromptChars  = 32768
)

// Allowed enum values, mirrored from the DDL CHECK constraints.
const (
	thinkingAuto     = "auto"
	thinkingEnabled  = "enabled"
	thinkingDisabled = "disabled"

	reasoningEffortNone = "none"
	reasoningEffortLow  = "low"
	reasoningEffortHigh = "high"
	reasoningEffortMax  = "max"
)

var profileKeyRE = regexp.MustCompile(profileKeyPattern)

// AgentProfile is an agent configuration. Field values are persisted in
// agent_profiles; Model is empty to use the default LLM configuration's model,
// and MaxTurns is 0 for "unlimited".
type AgentProfile struct {
	ID          int64
	Key         string
	Name        string
	Description string
	Builtin     bool
	Enabled     bool

	SystemPrompt       string
	Model              string
	AllowedTools       []string
	MaxTurns           int
	RunSeconds         int
	HardTimeoutSeconds int
	Thinking           string
	ReasoningEffort    string
	MaxOutputTokens    int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpdateParams is a partial profile update. A nil pointer leaves the field
// unchanged; the fields listed in the design spec §10.3 are the only mutable
// ones (key and builtin are immutable).
type UpdateParams struct {
	SystemPrompt       *string
	Model              *string
	AllowedTools       *[]string
	MaxTurns           *int
	RunSeconds         *int
	HardTimeoutSeconds *int
	Thinking           *string
	ReasoningEffort    *string
	MaxOutputTokens    *int
	Enabled            *bool
}

// BuiltinProfile returns the seed for the builtin runtime diagnostic profile.
// Its system prompt is the product text from design spec §5.4; the values are
// the ones seeded at startup when the profile does not exist yet.
func BuiltinProfile() AgentProfile {
	return AgentProfile{
		Key:                BuiltinProfileKey,
		Name:               "运行时验证 Agent",
		Description:        "",
		Builtin:            true,
		Enabled:            true,
		SystemPrompt:       builtinSystemPrompt,
		Model:              "",
		AllowedTools:       []string{"file.list", "file.read", "http.get", "test.echo", "test.fail"},
		MaxTurns:           12,
		RunSeconds:         600,
		HardTimeoutSeconds: 60,
		Thinking:           thinkingEnabled,
		ReasoningEffort:    reasoningEffortMax,
		MaxOutputTokens:    32768,
	}
}

// builtinSystemPrompt is the exact text from design spec §5.4.
const builtinSystemPrompt = `你是 EzTest 的运行时验证助手，用来验证 Agent 运行时（会话、工具、活动流、预算、中止）是否正常工作。
你可以只读地检查 EzTest 自身：用 http.get 访问本机公开端点（如 http://localhost:13333/api/health），
用 file.list / file.read 阅读仓库内文档与代码，用 test.echo / test.fail 验证工具链路。
规则：
1. 只读，不修改任何文件，不访问只读根之外或白名单之外的目标。
2. 需要事实时先调用工具，不要凭空推测；工具失败时如实说明失败原因。
3. 回答简洁：说明检查了什么、看到了什么、结论是什么；需要时可分点。
4. 不要尝试获取或输出任何密钥、令牌、密码或 .env 内容。`

// Validate checks the profile against the design spec §10.3 ranges and the
// enum values enforced by the DDL. Whether the allowed tools exist is checked
// by the application layer against the tool registry, because the registry is
// not part of the domain.
func (p AgentProfile) Validate() error {
	if !profileKeyRE.MatchString(p.Key) {
		return fmt.Errorf("agent: key %q must match %s", p.Key, profileKeyPattern)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("agent: name must not be empty")
	}
	if n := utf8.RuneCountInString(p.SystemPrompt); n < 1 || n > maxSystemPromptChars {
		return fmt.Errorf("agent: system_prompt length %d must be between 1 and %d characters", n, maxSystemPromptChars)
	}
	if p.MaxTurns < 0 || p.MaxTurns > maxTurnsLimit {
		return fmt.Errorf("agent: max_turns %d must be between 0 and %d", p.MaxTurns, maxTurnsLimit)
	}
	if p.RunSeconds < minRunSeconds || p.RunSeconds > maxRunSeconds {
		return fmt.Errorf("agent: run_seconds %d must be between %d and %d", p.RunSeconds, minRunSeconds, maxRunSeconds)
	}
	if p.HardTimeoutSeconds < minHardTimeoutSeconds || p.HardTimeoutSeconds > maxHardTimeoutSeconds {
		return fmt.Errorf("agent: hard_timeout_seconds %d must be between %d and %d",
			p.HardTimeoutSeconds, minHardTimeoutSeconds, maxHardTimeoutSeconds)
	}
	if p.MaxOutputTokens < minMaxOutputTokens || p.MaxOutputTokens > maxMaxOutputTokens {
		return fmt.Errorf("agent: max_output_tokens %d must be between %d and %d",
			p.MaxOutputTokens, minMaxOutputTokens, maxMaxOutputTokens)
	}
	switch p.Thinking {
	case thinkingAuto, thinkingEnabled, thinkingDisabled:
	default:
		return fmt.Errorf("agent: thinking %q must be one of %q, %q or %q",
			p.Thinking, thinkingAuto, thinkingEnabled, thinkingDisabled)
	}
	switch p.ReasoningEffort {
	case "", reasoningEffortNone, reasoningEffortLow, reasoningEffortHigh, reasoningEffortMax:
	default:
		return fmt.Errorf("agent: reasoning_effort %q must be empty, %q, %q, %q or %q",
			p.ReasoningEffort, reasoningEffortNone, reasoningEffortLow, reasoningEffortHigh, reasoningEffortMax)
	}
	if p.Thinking == thinkingDisabled && p.ReasoningEffort != "" && p.ReasoningEffort != reasoningEffortNone {
		return fmt.Errorf("agent: reasoning_effort %q is not allowed when thinking is %q",
			p.ReasoningEffort, thinkingDisabled)
	}
	seen := make(map[string]struct{}, len(p.AllowedTools))
	for i, tool := range p.AllowedTools {
		if strings.TrimSpace(tool) == "" {
			return fmt.Errorf("agent: allowed_tools[%d] must not be empty", i)
		}
		if _, duplicate := seen[tool]; duplicate {
			return fmt.Errorf("agent: allowed_tools contains %q more than once", tool)
		}
		seen[tool] = struct{}{}
	}
	return nil
}

// Apply merges params into the profile and validates the result. A failed
// update leaves the profile untouched, so callers can return the error without
// rolling anything back.
func (p *AgentProfile) Apply(params UpdateParams) error {
	updated := *p
	if params.SystemPrompt != nil {
		updated.SystemPrompt = *params.SystemPrompt
	}
	if params.Model != nil {
		updated.Model = *params.Model
	}
	if params.AllowedTools != nil {
		updated.AllowedTools = append([]string(nil), (*params.AllowedTools)...)
	}
	if params.MaxTurns != nil {
		updated.MaxTurns = *params.MaxTurns
	}
	if params.RunSeconds != nil {
		updated.RunSeconds = *params.RunSeconds
	}
	if params.HardTimeoutSeconds != nil {
		updated.HardTimeoutSeconds = *params.HardTimeoutSeconds
	}
	if params.Thinking != nil {
		updated.Thinking = *params.Thinking
	}
	if params.ReasoningEffort != nil {
		updated.ReasoningEffort = *params.ReasoningEffort
	}
	if params.MaxOutputTokens != nil {
		updated.MaxOutputTokens = *params.MaxOutputTokens
	}
	if params.Enabled != nil {
		updated.Enabled = *params.Enabled
	}
	if err := updated.Validate(); err != nil {
		return err
	}
	*p = updated
	return nil
}
