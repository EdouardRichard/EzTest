// Package domain contains the Agent bounded context's entities, read models,
// repository ports and domain errors. It depends only on the standard library;
// the application, infrastructure and interface layers depend on it, never the
// other way around.
package domain

import "errors"

// Domain errors are returned by the repositories and the application services
// and are mapped to the public API error envelope by the HTTP layer. They are
// deliberately coarse: the HTTP mapping (404/409/422/503) decides on the code,
// while the reason is carried in the response details.
var (
	// ErrNotFound is returned when a profile, session, run or message does not
	// exist (or a session-scoped lookup does not match).
	ErrNotFound = errors.New("agent: not found")
	// ErrRunActive is returned when a session already has a running run. The
	// partial unique index uq_agent_runs_running is the final authority.
	ErrRunActive = errors.New("agent: run already active")
	// ErrProfileDisabled is returned when a run is started for a disabled
	// profile.
	ErrProfileDisabled = errors.New("agent: profile disabled")
	// ErrCapacity is returned when the global concurrent-run budget is
	// exhausted; a run is not created.
	ErrCapacity = errors.New("agent: run capacity exhausted")
	// ErrEmptyMessage is returned when a user message is blank.
	ErrEmptyMessage = errors.New("agent: empty message")
	// ErrLLMNotConfigured is returned when no usable default LLM
	// configuration exists.
	ErrLLMNotConfigured = errors.New("agent: llm is not configured")
	// ErrToolsUnsupported is returned when tools are requested but the default
	// LLM configuration uses the responses protocol.
	ErrToolsUnsupported = errors.New("agent: tools require the chat_completions protocol")
	// ErrContextOverflow is returned when the conversation cannot fit the
	// model context after truncation.
	ErrContextOverflow = errors.New("agent: context overflow")
)
