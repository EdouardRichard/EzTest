package llmapp

import "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"

// Model-parameter defaults frozen by v0.22 spec A §4.1/§R-A1-5. The database
// and the API always store raw tokens; only the UI renders K (=1000).
const (
	// DefaultContextWindowTokens is the effective context window when a model
	// leaves context_window at 0 (unknown/auto). It mirrors the production
	// default of the context.window_tokens setting; callers that need the
	// setting value can pass their own default in a later wiring task.
	DefaultContextWindowTokens int64 = 128_000

	// MaxRequestOutputTokens bounds what is actually sent to the provider as an
	// output limit, so an upstream-reported 393K is never forwarded verbatim.
	MaxRequestOutputTokens int64 = 16_000
)

// EffectiveContextWindow returns the model's context capacity in tokens: the
// stored context_window, or the structured-capability mirror for rows that only
// carry capabilities, or DefaultContextWindowTokens when the value is unknown
// (0). MaxInput derives from this value (A §4.1).
func EffectiveContextWindow(model llm.Model) int64 {
	window := int64(model.ContextWindow)
	if window <= 0 {
		window = int64(model.Capabilities.ContextWindow)
	}
	if window <= 0 {
		window = DefaultContextWindowTokens
	}
	return window
}

// EffectiveMaxOutput returns the output limit actually used for a request:
// O = min(max_output > 0 ? max_output : 4096, 16000) (A §4.1/§R-A1-5). The raw
// max_output remains untouched in the model row and in the API response.
func EffectiveMaxOutput(model llm.Model) int64 {
	output := int64(model.MaxOutput)
	if output <= 0 {
		output = DefaultChatMaxOutputTokens
	}
	if output > MaxRequestOutputTokens {
		output = MaxRequestOutputTokens
	}
	return output
}

// MaxInput is the derived read-only input budget: effective context window
// minus effective output limit (A §4.1). It is clamped at 0 so an inconsistent
// upstream row (output larger than window) cannot render a negative budget.
func MaxInput(model llm.Model) int64 {
	input := EffectiveContextWindow(model) - EffectiveMaxOutput(model)
	if input < 0 {
		return 0
	}
	return input
}
