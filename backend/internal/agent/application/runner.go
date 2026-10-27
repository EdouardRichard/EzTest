package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// Runner-internal limits and decision reasons. The activity detail contract
// freezes the decision values (design spec §9.2).
const (
	// streamFlushTicker drives the mid-stream segment flush checks.
	streamFlushTicker = 250 * time.Millisecond
	// toolResultContextBytes caps one tool result before it enters the LLM
	// context; the head and tail are kept around a marker (spec §6.1).
	toolResultContextBytes = 32 * 1024
	// toolContentTruncationMarker marks a truncated model-facing tool result.
	toolContentTruncationMarker = "...[truncated]..."
	// toolArgsPreviewBytes / toolResultPreviewBytes bound detail previews to
	// 2KB (plan §T11 step 12), independent of the 32KB context cap.
	toolArgsPreviewBytes   = 2048
	toolResultPreviewBytes = 2048

	decisionBudgetStop       = "budget_stop"
	decisionContextTruncated = "context_truncated"
	decisionToolDenied       = "tool_denied"

	// errorCodeLLMUpstream is the fallback run error_code for a plain stream
	// error; port errors implementing ErrorCode() provide the precise llm_*
	// code instead.
	errorCodeLLMUpstream = "llm_upstream_error"
	// errorCodeLLMInvalidResponse is used when a stream ends without its
	// terminal event.
	errorCodeLLMInvalidResponse = "llm_invalid_response"
	// errorCodeInternal marks local failures such as an unreadable history.
	errorCodeInternal = "internal"
)

// errStreamEndedUnexpectedly reports a stream channel that closed without a
// Done or Err event.
var errStreamEndedUnexpectedly = errors.New("agent: llm stream ended without a terminal event")

// runStage tracks the phase of the run for abort-cause mapping: model output in
// flight vs tool execution vs between turns (spec §6.3).
type runStage string

const (
	runStageBetween   runStage = "between"
	runStageStreaming runStage = "streaming"
	runStageTools     runStage = "tools"
)

// runExecution is the mutable state of one run: its history, accumulated usage
// and the per-turn segment buffers.
type runExecution struct {
	service *RunService
	runID   int64
	session domain.Session
	profile domain.AgentProfile
	actor   *int64
	caller  LLMCaller
	emitter *activityEmitter

	startedAt       time.Time
	history         []domain.Message
	turns           int
	usage           domain.Usage
	lastToolName    string
	lastToolPending bool
	stage           runStage

	// assistantMessageID is the placeholder row of the model turn whose tool
	// calls are executing. tool_use events reference it so the console can
	// lazy-load the full arguments from the assistant message detail.
	assistantMessageID int64

	thinkingBuf *segmentBuffer
	textBuf     *segmentBuffer
}

// runStreamOutcome is the result of consuming one model stream.
type runStreamOutcome struct {
	text         string
	reasoning    string
	calls        []domain.ToolCall
	finishReason string
	usage        domain.Usage
	usageSeen    bool
	done         bool
	err          error
}

// runFinishOptions carries the terminal fields that vary by outcome.
type runFinishOptions struct {
	abortCode       string
	abortMessage    string
	errorCode       string
	errorMessage    string
	lastToolName    string
	lastToolPending bool
}

// run is the goroutine body: it executes the model/tool loop and always leaves
// the run in a terminal state.
func (s *RunService) run(ctx context.Context, runID int64, session domain.Session, profile domain.AgentProfile, caller LLMCaller, actor *int64) {
	execution := &runExecution{
		service: s,
		runID:   runID,
		session: session,
		profile: profile,
		actor:   actor,
		caller:  caller,
		emitter: newActivityEmitter(s.deps.Events, session.ID, profile.Key, session.ActivityChannel, session.ActivityVersionID),
	}
	execution.execute(ctx)
}

// execute drives the run lifecycle: budget and context assembly, one streamed
// model turn, the tool loop, and the terminal write.
func (ex *runExecution) execute(ctx context.Context) {
	service := ex.service
	ex.startedAt = service.now()
	runSeconds := time.Duration(ex.profile.RunSeconds) * time.Second
	softDeadline := ex.startedAt.Add(runSeconds)
	hardTimeout := time.Duration(ex.profile.HardTimeoutSeconds) * time.Second
	if hardTimeout <= 0 {
		hardTimeout = service.hardTimeout
	}
	hardCtx, cancelHard := context.WithTimeoutCause(ctx, runSeconds+hardTimeout, &abortCause{code: abortCodeHardTimeout})
	defer cancelHard()

	ex.emit(ctx, domain.KindRunStarted, "开始运行", map[string]any{
		"profile_key":       ex.profile.Key,
		"model":             ex.caller.Model(),
		"thinking":          ex.profile.Thinking,
		"reasoning_effort":  ex.profile.ReasoningEffort,
		"max_output_tokens": ex.profile.MaxOutputTokens,
		"max_turns":         ex.profile.MaxTurns,
		"run_seconds":       ex.profile.RunSeconds,
	})

	// The initial history read happens before the tool loop. A stop that lands
	// during that read must terminate the run as aborted, not as a model or
	// internal failure (the read itself fails with the context error).
	ex.stage = runStageBetween
	history, err := service.deps.Messages.ListBySession(hardCtx, ex.session.ID)
	if err != nil {
		if cause := context.Cause(hardCtx); cause != nil {
			ex.finishAborted(ctx, cause)
			return
		}
		slog.ErrorContext(ctx, "agent: load run history", "run_id", ex.runID, "error", err)
		ex.finish(ctx, domain.RunFailed, domain.ReasonModelError, runFinishOptions{
			errorCode:    errorCodeInternal,
			errorMessage: err.Error(),
		})
		return
	}
	ex.history = history

	for {
		ex.stage = runStageBetween
		if cause := context.Cause(hardCtx); cause != nil {
			ex.finishAborted(ctx, cause)
			return
		}
		if ex.profile.MaxTurns > 0 && ex.turns >= ex.profile.MaxTurns {
			ex.emitDecision(ctx, decisionBudgetStop, "max_turns", map[string]any{
				"turns":     ex.turns,
				"max_turns": ex.profile.MaxTurns,
			})
			ex.finish(ctx, domain.RunDone, domain.ReasonMaxTurns, runFinishOptions{})
			return
		}
		if runSeconds > 0 && service.now().After(softDeadline) {
			ex.emitDecision(ctx, decisionBudgetStop, "timeout", map[string]any{
				"turns":      ex.turns,
				"elapsed_ms": service.now().Sub(ex.startedAt).Milliseconds(),
			})
			ex.finish(ctx, domain.RunDone, domain.ReasonTimeout, runFinishOptions{})
			return
		}

		window := ex.caller.ContextWindowTokens()
		budgetChars := window - max(2048, window/4) - 2000
		built, err := service.deps.Context.Build(ex.profile.SystemPrompt, ex.history, budgetChars)
		if err != nil {
			// The stage is "between" here; a stop that landed while the context
			// was assembled wins over the build error.
			if cause := context.Cause(hardCtx); cause != nil {
				ex.finishAborted(ctx, cause)
				return
			}
			if errors.Is(err, domain.ErrContextOverflow) {
				ex.finish(ctx, domain.RunFailed, domain.ReasonContextOverflow, runFinishOptions{
					errorCode:    "context_overflow",
					errorMessage: err.Error(),
				})
			} else {
				ex.finish(ctx, domain.RunFailed, domain.ReasonModelError, runFinishOptions{
					errorCode:    errorCodeInternal,
					errorMessage: err.Error(),
				})
			}
			return
		}
		if built.DroppedMessages > 0 {
			ex.emitDecision(ctx, decisionContextTruncated, "context_window", map[string]any{"dropped": built.DroppedMessages})
		}

		assistantID, placeholderErr := service.deps.Messages.AppendAssistantPlaceholder(hardCtx, ex.session.ID, ex.runID)
		if placeholderErr != nil {
			// The run can still proceed; streamed chunks reference message 0 and
			// the completion write will fail loudly.
			slog.ErrorContext(ctx, "agent: append assistant placeholder", "run_id", ex.runID, "error", placeholderErr)
		}
		now := service.now()
		ex.thinkingBuf = newSegmentBuffer(domain.KindThinking, ex.runID, assistantID, now)
		ex.textBuf = newSegmentBuffer(domain.KindText, ex.runID, assistantID, now)
		ex.assistantMessageID = assistantID

		request := LLMRequest{
			Model:           ex.caller.Model(),
			Messages:        built.Messages,
			Tools:           service.deps.Tools.Specs(ex.profile.AllowedTools),
			MaxTokens:       ex.profile.MaxOutputTokens,
			Thinking:        wireThinking(ex.profile.Thinking),
			ReasoningEffort: ex.profile.ReasoningEffort,
		}
		ex.stage = runStageStreaming
		stream, streamErr := ex.caller.ChatStream(hardCtx, request)
		if streamErr != nil {
			if cause := context.Cause(hardCtx); cause != nil {
				ex.completeEmptyAssistant(ctx, assistantID)
				ex.finishAborted(ctx, cause)
				return
			}
			ex.finish(ctx, domain.RunFailed, domain.ReasonModelError, runFinishOptions{
				errorCode:    llmErrorCode(streamErr),
				errorMessage: streamErr.Error(),
			})
			return
		}

		outcome := ex.consumeStream(hardCtx, stream)
		ex.flushSegments(ctx, true)

		// Whatever happened — Done, stream error or abort — the accumulated
		// text/reasoning is persisted so partial output survives.
		parts := make([]domain.Part, 0, 1)
		if outcome.text != "" {
			parts = append(parts, domain.Part{Type: "text", Text: outcome.text})
		}
		var usage *domain.Usage
		if outcome.usageSeen {
			recorded := outcome.usage
			usage = &recorded
		}
		writeCtx := runNoCancelContext(hardCtx)
		if err := service.deps.Messages.CompleteAssistant(writeCtx, assistantID, parts, outcome.reasoning, outcome.calls, usage); err != nil {
			slog.ErrorContext(ctx, "agent: complete assistant message", "run_id", ex.runID, "message_id", assistantID, "error", err)
		}
		ex.history = append(ex.history, domain.Message{
			ID:        assistantID,
			SessionID: ex.session.ID,
			RunID:     &ex.runID,
			Role:      "assistant",
			Parts:     parts,
			Reasoning: outcome.reasoning,
			ToolCalls: outcome.calls,
			Usage:     usage,
		})
		ex.turns++
		if outcome.usageSeen {
			ex.addUsage(outcome.usage)
			ex.emit(ctx, domain.KindUsage, "模型用量", map[string]any{
				"turn":              ex.turns,
				"prompt_tokens":     outcome.usage.PromptTokens,
				"completion_tokens": outcome.usage.CompletionTokens,
				"reasoning_tokens":  outcome.usage.ReasoningTokens,
				"total_tokens":      outcome.usage.TotalTokens,
			})
		}
		if err := service.deps.Runs.UpdateProgress(writeCtx, ex.runID, ex.turns, ex.usage); err != nil {
			slog.ErrorContext(ctx, "agent: update run progress", "run_id", ex.runID, "error", err)
		}

		if cause := context.Cause(hardCtx); cause != nil {
			// The assistant message may already carry tool calls; answer them
			// with cancelled tool messages so the persisted history keeps the
			// tool_calls <-> tool protocol pairing valid for the next run.
			ex.persistCancelledCalls(writeCtx, outcome.calls)
			ex.finishAborted(ctx, cause)
			return
		}
		if outcome.err != nil {
			if cause := context.Cause(hardCtx); cause != nil {
				ex.finishAborted(ctx, cause)
				return
			}
			ex.finish(ctx, domain.RunFailed, domain.ReasonModelError, runFinishOptions{
				errorCode:    llmErrorCode(outcome.err),
				errorMessage: outcome.err.Error(),
			})
			return
		}
		if len(outcome.calls) == 0 {
			ex.finishForStop(ctx, outcome.finishReason)
			return
		}
		if ex.executeTools(hardCtx, outcome.calls) {
			ex.finishAborted(ctx, context.Cause(hardCtx))
			return
		}
	}
}

// consumeStream reads one model stream, coalescing reasoning/text deltas into
// segment activities. It returns as soon as the stream terminates, fails or the
// run context is cancelled; partial accumulations are kept in every case.
func (ex *runExecution) consumeStream(ctx context.Context, stream <-chan LLMStreamEvent) runStreamOutcome {
	outcome := runStreamOutcome{}
	var text, reasoning strings.Builder
	ticker := time.NewTicker(streamFlushTicker)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			outcome.text = text.String()
			outcome.reasoning = reasoning.String()
			return outcome
		case <-ticker.C:
			ex.flushSegments(ctx, false)
		case event, ok := <-stream:
			if !ok {
				outcome.text = text.String()
				outcome.reasoning = reasoning.String()
				if outcome.err == nil {
					outcome.err = errStreamEndedUnexpectedly
				}
				return outcome
			}
			if event.Err != nil {
				outcome.err = event.Err
				outcome.text = text.String()
				outcome.reasoning = reasoning.String()
				return outcome
			}
			if event.ReasoningDelta != "" {
				reasoning.WriteString(event.ReasoningDelta)
				ex.thinkingBuf.Append(event.ReasoningDelta)
			}
			if event.Delta != "" {
				text.WriteString(event.Delta)
				ex.textBuf.Append(event.Delta)
			}
			if event.Done {
				outcome.done = true
				outcome.calls = event.ToolCalls
				outcome.finishReason = event.FinishReason
				outcome.usage = event.Usage
				outcome.usageSeen = event.Usage != (domain.Usage{})
				outcome.text = text.String()
				outcome.reasoning = reasoning.String()
				return outcome
			}
		}
	}
}

// executeTools runs the model's tool calls in order. It returns true when the
// run context was cancelled; the remaining calls are persisted as cancelled tool
// messages first so the history stays protocol-valid.
func (ex *runExecution) executeTools(ctx context.Context, calls []domain.ToolCall) bool {
	ex.stage = runStageTools
	for index, call := range calls {
		if context.Cause(ctx) != nil {
			ex.persistCancelledCalls(runNoCancelContext(ctx), calls[index:])
			return true
		}

		argsPreview, argsTruncated := activityTruncateBytes(call.Arguments, toolArgsPreviewBytes)
		ex.emit(ctx, domain.KindToolUse, "调用工具 "+call.Name, map[string]any{
			"call_id":        call.ID,
			"tool":           call.Name,
			"args":           argsPreview,
			"args_truncated": argsTruncated,
			"message_id":     ex.assistantMessageID,
		})

		ex.lastToolName = call.Name
		ex.lastToolPending = true
		execution := ex.service.deps.Executor.Execute(ctx, ToolCallInput{
			RunID:       ex.runID,
			SessionID:   ex.session.ID,
			ActorUserID: ex.actor,
			ProfileKey:  ex.profile.Key,
			Call:        call,
			Allowed:     ex.profile.AllowedTools,
		})
		ex.lastToolPending = false

		content := truncateToolResultForContext(execution.Result.Content)
		message := domain.Message{
			SessionID:  ex.session.ID,
			RunID:      &ex.runID,
			Role:       "tool",
			Parts:      []domain.Part{{Type: "text", Text: content}},
			ToolCallID: call.ID,
			ToolName:   call.Name,
		}
		stored, err := ex.service.deps.Messages.Append(runNoCancelContext(ctx), message)
		if err != nil {
			slog.ErrorContext(ctx, "agent: append tool message", "run_id", ex.runID, "tool", call.Name, "error", err)
			stored = message
		}
		ex.history = append(ex.history, stored)

		resultPreview, resultTruncated := activityTruncateBytes(execution.Result.Content, toolResultPreviewBytes)
		ex.emit(ctx, domain.KindToolResult, "工具 "+call.Name+" "+execution.Status, map[string]any{
			"call_id":          call.ID,
			"tool":             call.Name,
			"status":           execution.Status,
			"duration_ms":      execution.DurationMS,
			"result":           resultPreview,
			"result_truncated": resultTruncated,
			"error_code":       execution.ErrorCode,
			"message_id":       stored.ID,
		})
		if execution.Status == ToolStatusDenied {
			ex.emitDecision(ctx, decisionToolDenied, "not_allowed", map[string]any{
				"call_id": call.ID,
				"tool":    call.Name,
			})
		}

		if context.Cause(ctx) != nil {
			ex.persistCancelledCalls(runNoCancelContext(ctx), calls[index+1:])
			return true
		}
	}
	return false
}

// persistCancelledCalls appends a tool message for every call the run never
// executed. The model requires every tool_call in the history to have a
// matching tool message, otherwise the next run's request is rejected.
func (ex *runExecution) persistCancelledCalls(ctx context.Context, calls []domain.ToolCall) {
	for _, call := range calls {
		content := fmt.Sprintf("tool %q was cancelled because the run was aborted", call.Name)
		message := domain.Message{
			SessionID:  ex.session.ID,
			RunID:      &ex.runID,
			Role:       "tool",
			Parts:      []domain.Part{{Type: "text", Text: content}},
			ToolCallID: call.ID,
			ToolName:   call.Name,
		}
		stored, err := ex.service.deps.Messages.Append(ctx, message)
		if err != nil {
			slog.ErrorContext(ctx, "agent: append cancelled tool message", "run_id", ex.runID, "tool", call.Name, "error", err)
			continue
		}
		ex.history = append(ex.history, stored)
	}
}

// flushSegments publishes due thinking/text chunks. final closes both segments
// at the end of the model turn.
func (ex *runExecution) flushSegments(ctx context.Context, final bool) {
	now := ex.service.now()
	for _, buffer := range []*segmentBuffer{ex.thinkingBuf, ex.textBuf} {
		if buffer == nil {
			continue
		}
		if !final && !buffer.ShouldFlush(now) {
			continue
		}
		text, part, ok := buffer.Flush(now, final)
		if !ok {
			continue
		}
		_ = ex.emitter.emitSegment(runNoCancelContext(ctx), buffer, text, part, final)
	}
}

// finishForStop maps the upstream finish_reason of a turn without tool calls to
// the run terminal state (design spec §7.2).
func (ex *runExecution) finishForStop(ctx context.Context, finishReason string) {
	if finishReason == "" || finishReason == "stop" {
		ex.finish(ctx, domain.RunDone, domain.ReasonCompleted, runFinishOptions{})
		return
	}
	ex.finish(ctx, domain.RunFailed, domain.ReasonModelError, runFinishOptions{
		errorCode:    "finish_reason=" + finishReason,
		errorMessage: fmt.Sprintf("llm finished with finish_reason=%q and no tool calls", finishReason),
	})
}

// finishAborted maps the cancellation cause and the current stage to the abort
// terminal state.
func (ex *runExecution) finishAborted(ctx context.Context, cause error) {
	code, message := abortCodeFromCause(cause)
	status := domain.RunAborted
	var reason domain.TerminalReason
	switch code {
	case abortCodeShutdown:
		reason = domain.ReasonShutdown
	case abortCodeHardTimeout:
		reason = domain.ReasonRunHardTimeout
	default:
		switch ex.stage {
		case runStageStreaming:
			reason = domain.ReasonAbortedStreaming
		case runStageTools:
			reason = domain.ReasonAbortedTools
		default:
			reason = domain.ReasonAborted
		}
	}
	ex.finish(ctx, status, reason, runFinishOptions{
		abortCode:       code,
		abortMessage:    message,
		lastToolName:    ex.lastToolName,
		lastToolPending: ex.lastToolPending || ex.stage == runStageTools,
	})
}

// completeEmptyAssistant persists the assistant placeholder as completed with
// empty content when the run aborts after the placeholder exists but before any
// stream output arrived. The stream abort path completes its message the same
// way, so history never keeps an unanswered placeholder.
func (ex *runExecution) completeEmptyAssistant(ctx context.Context, assistantID int64) {
	if assistantID == 0 {
		return
	}
	err := ex.service.deps.Messages.CompleteAssistant(
		runNoCancelContext(ctx), assistantID, []domain.Part{}, "", nil, nil,
	)
	if err != nil {
		slog.ErrorContext(ctx, "agent: complete assistant message", "run_id", ex.runID, "message_id", assistantID, "error", err)
	}
}

// finish writes the terminal run row and only then publishes run_finished. A
// failed terminal write is logged and the event is skipped, matching the spec's
// "write the terminal row, then publish" ordering.
func (ex *runExecution) finish(ctx context.Context, status domain.RunStatus, reason domain.TerminalReason, options runFinishOptions) {
	if reason == "" {
		// Defensive: the schema requires a terminal_reason for terminal runs.
		switch status {
		case domain.RunDone:
			reason = domain.ReasonCompleted
		case domain.RunAborted:
			reason = domain.ReasonAborted
		default:
			reason = domain.ReasonModelError
		}
	}
	lastToolName := options.lastToolName
	if lastToolName == "" {
		lastToolName = ex.lastToolName
	}
	lastToolPending := options.lastToolPending || ex.lastToolPending
	result := domain.RunResult{
		Status:          status,
		TerminalReason:  reason,
		AbortCode:       options.abortCode,
		AbortMessage:    options.abortMessage,
		ErrorCode:       options.errorCode,
		ErrorMessage:    options.errorMessage,
		Turns:           ex.turns,
		Usage:           ex.usage,
		LastToolName:    lastToolName,
		LastToolPending: lastToolPending,
	}

	writeCtx := runNoCancelContext(ctx)
	if err := ex.service.deps.Runs.Finish(writeCtx, ex.runID, result); err != nil {
		slog.ErrorContext(ctx, "agent: finish run", "run_id", ex.runID, "status", status, "error", err)
		return
	}
	ex.emitRunFinished(ctx, result)
}

// emitRunFinished publishes the terminal activity with the frozen detail
// fields (spec §9.2).
func (ex *runExecution) emitRunFinished(ctx context.Context, result domain.RunResult) {
	detail := map[string]any{
		"status":          string(result.Status),
		"terminal_reason": string(result.TerminalReason),
		"turns":           result.Turns,
		"duration_ms":     ex.service.now().Sub(ex.startedAt).Milliseconds(),
		"usage": map[string]any{
			"prompt":     result.Usage.PromptTokens,
			"completion": result.Usage.CompletionTokens,
			"reasoning":  result.Usage.ReasoningTokens,
			"total":      result.Usage.PromptTokens + result.Usage.CompletionTokens,
		},
	}
	if result.AbortCode != "" {
		detail["abort_code"] = result.AbortCode
	}
	if result.ErrorCode != "" {
		detail["error_code"] = result.ErrorCode
	}
	if result.LastToolName != "" {
		detail["last_tool_name"] = result.LastToolName
	}
	if result.LastToolPending {
		detail["last_tool_pending"] = true
	}
	ex.emit(ctx, domain.KindRunFinished, "运行结束："+string(result.TerminalReason), detail)
}

// emit publishes one run activity. The emitter logs sink failures; the run is
// never failed by an event error.
func (ex *runExecution) emit(ctx context.Context, kind, summary string, detail map[string]any) {
	_ = ex.emitter.emit(runNoCancelContext(ctx), ex.runID, kind, summary, detail)
}

// emitDecision publishes a decision activity.
func (ex *runExecution) emitDecision(ctx context.Context, decision, reason string, extra map[string]any) {
	detail := make(map[string]any, len(extra)+2)
	detail["decision"] = decision
	if reason != "" {
		detail["reason"] = reason
	}
	for key, value := range extra {
		detail[key] = value
	}
	ex.emit(ctx, domain.KindDecision, "决策："+decision, detail)
}

// addUsage accumulates one turn's usage into the run totals.
func (ex *runExecution) addUsage(usage domain.Usage) {
	ex.usage.PromptTokens += usage.PromptTokens
	ex.usage.CompletionTokens += usage.CompletionTokens
	ex.usage.ReasoningTokens += usage.ReasoningTokens
	ex.usage.TotalTokens += usage.TotalTokens
}

// wireThinking maps the profile's thinking mode to the upstream request value.
// "auto" is the provider-decides mode: the design spec §13 requires omitting
// the thinking parameter entirely (sending {"type":"auto"} is rejected by the
// strict upstream), so it maps to the empty, omitted value. reasoning_effort is
// an independent parameter and is still sent when configured.
func wireThinking(mode string) string {
	if mode == "auto" {
		return ""
	}
	return mode
}

// abortCodeFromCause extracts the named abort code. A bare deadline maps to the
// hard timeout; anything else is treated as a user abort.
func abortCodeFromCause(cause error) (code, message string) {
	var named *abortCause
	if errors.As(cause, &named) && named != nil {
		return named.code, named.message
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return abortCodeHardTimeout, "run exceeded the hard timeout"
	}
	return abortCodeUserAbort, ""
}

// CodedError is implemented by port errors that carry a stable public error
// code. The LLM gateway adapter wraps llm_* codes in an error implementing
// ErrorCode() so the runner can persist the precise code in agent_runs.
type CodedError interface {
	error
	ErrorCode() string
}

// llmErrorCode extracts the stable error code of a port error. Errors may
// implement CodedError (the gateway adapter wraps llm_* codes that way); plain
// errors fall back to the generic upstream code.
func llmErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded CodedError
	if errors.As(err, &coded) {
		if code := strings.TrimSpace(coded.ErrorCode()); code != "" {
			return code
		}
	}
	if errors.Is(err, errStreamEndedUnexpectedly) {
		return errorCodeLLMInvalidResponse
	}
	return errorCodeLLMUpstream
}

// runNoCancelContext detaches a context from its cancellation so terminal
// writes and activities still happen while the run is aborting.
func runNoCancelContext(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// truncateToolResultForContext caps a tool result at 32KB, keeping the head and
// the tail around a marker.
func truncateToolResultForContext(content string) string {
	if len(content) <= toolResultContextBytes {
		return content
	}
	budget := toolResultContextBytes - len(toolContentTruncationMarker)
	if budget <= 0 {
		return toolContentTruncationMarker
	}
	head := budget / 2
	tail := budget - head
	return runSafeBytePrefix(content, head) + toolContentTruncationMarker + runSafeByteSuffix(content, tail)
}

// runSafeBytePrefix returns at most n leading bytes without splitting a rune.
func runSafeBytePrefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n < 0 {
		n = 0
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// runSafeByteSuffix returns at most n trailing bytes without splitting a rune.
func runSafeByteSuffix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n < 0 {
		n = 0
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
