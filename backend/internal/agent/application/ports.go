package agentapp

import "context"

// SettingsReader is the agent context's view of the settings-center service.
// settingsapp.Service satisfies it; there is no agent-specific setting catalog,
// only the frozen P0 keys consumed by P1.
type SettingsReader interface {
	// Int reads an integer setting.
	Int(ctx context.Context, key string) (int64, error)
	// Float reads a numeric setting.
	Float(ctx context.Context, key string) (float64, error)
}

// Summarizer compresses older conversation messages into a rolling summary.
// The runner/context builder uses the LLM-backed implementation and falls back
// to a deterministic summary when it fails; it must never fail the run.
type Summarizer interface {
	Summarize(ctx context.Context, messages []Message) (string, error)
}
