package agentapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Runner defaults. Profile budgets and the frozen P0 settings override them.
const (
	defaultMaxTurns           = 12
	defaultSoftSeconds        = 300
	defaultHardSeconds        = 600
	defaultToolResultMaxBytes = 32768
	finishTimeout             = 5 * time.Second
	toolResultHeadRunes       = 2000
	scoutAttachmentTextRunes  = 4000
	contextSectionSystem      = "system"
)

// RunnerDeps wires the P1 agent run loop.
type RunnerDeps struct {
	Runtime      agent.RuntimeRepository
	Chat         llmapp.ChatGateway
	Settings     SettingsReader
	Clock        clock.Clock
	IDs          id.Generator
	Events       eventingapp.Publisher
	Audit        auditapp.Recorder
	Conversation ConversationPort
	Versions     VersionReader
	Attachments  AttachmentReader
	TeamRoom     TeamRoomPort
	Content      ContentWriter
	ToolDeps     tools.Deps
	InstanceID   string
}

// Runner executes one work item as an agent run: context packet, bounded model
// loop, scoped tool calls and the profile-specific output. It never confirms,
// deletes or closes a requirement; the model can only submit drafts and
// proposals through the scoped tools.
type Runner struct {
	repo         agent.RuntimeRepository
	chat         llmapp.ChatGateway
	settings     SettingsReader
	clock        clock.Clock
	ids          id.Generator
	events       eventingapp.Publisher
	audit        auditapp.Recorder
	conversation ConversationPort
	versions     VersionReader
	attachments  AttachmentReader
	teamRoom     TeamRoomPort
	content      ContentWriter
	toolDeps     tools.Deps
	instanceID   string
	builder      *ContextBuilder
}

// NewRunner wires the agent runner.
func NewRunner(deps RunnerDeps) *Runner {
	return &Runner{
		repo:         deps.Runtime,
		chat:         deps.Chat,
		settings:     deps.Settings,
		clock:        deps.Clock,
		ids:          deps.IDs,
		events:       deps.Events,
		audit:        deps.Audit,
		conversation: deps.Conversation,
		versions:     deps.Versions,
		attachments:  deps.Attachments,
		teamRoom:     deps.TeamRoom,
		content:      deps.Content,
		toolDeps:     deps.ToolDeps,
		instanceID:   deps.InstanceID,
		builder:      NewContextBuilder(deps.Settings, NewLLMSummarizer(deps.Chat)),
	}
}

// RunResult summarizes one finished agent run.
type RunResult struct {
	AgentRunID     int64
	Status         agent.AgentRunStatus
	Turns          int
	Usage          map[string]int
	TerminalReason string
}

// RunError classifies a run failure for the worker retry policy.
type RunError struct {
	Reason    string
	Retryable bool
	Cause     error
}

// Error implements error.
func (e *RunError) Error() string {
	if e.Cause == nil {
		return e.Reason
	}
	return e.Reason + ": " + e.Cause.Error()
}

// Unwrap exposes the cause for errors.Is/errors.As.
func (e *RunError) Unwrap() error { return e.Cause }

// IsRetryable reports whether the worker should retry the work item. Unknown
// infrastructure failures are retryable; classified failures and lease-lost
// fencing failures are not.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var runErr *RunError
	if errors.As(err, &runErr) {
		return runErr.Retryable
	}
	if IsLeaseLost(err) {
		return false
	}
	return true
}

// IsLeaseLost reports whether a run write was fenced by a newer lease epoch.
func IsLeaseLost(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, agent.ErrLeaseLost) {
		return true
	}
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Code == "lease_lost"
}

// ParseWorkItemPayload reads the small reference payload. IDs may be decimal
// strings (canonical) or JSON numbers (P0 compatibility).
func ParseWorkItemPayload(raw json.RawMessage) (WorkItemPayload, error) {
	var payload WorkItemPayload
	if len(strings.TrimSpace(string(raw))) == 0 {
		return payload, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return WorkItemPayload{}, fmt.Errorf("work item payload must be a JSON object: %w", err)
	}
	parseID := func(field string) (int64, error) {
		value, ok := object[field]
		if !ok {
			return 0, nil
		}
		text := strings.TrimSpace(string(value))
		if text == "" || text == "null" || text == `""` {
			return 0, nil
		}
		if strings.HasPrefix(text, `"`) {
			var decoded string
			if err := json.Unmarshal(value, &decoded); err != nil {
				return 0, fmt.Errorf("work item payload %s must be a decimal ID", field)
			}
			text = strings.TrimSpace(decoded)
			if text == "" {
				return 0, nil
			}
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("work item payload %s must be a positive decimal ID", field)
		}
		return parsed, nil
	}
	var err error
	if payload.ConversationID, err = parseID("conversation_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.MessageID, err = parseID("message_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.AttachmentID, err = parseID("attachment_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if trigger, ok := object["trigger"]; ok {
		_ = json.Unmarshal(trigger, &payload.Trigger)
	}
	return payload, nil
}

// runState carries one run's mutable state.
type runState struct {
	item         agent.WorkItem
	payload      WorkItemPayload
	profile      agent.AgentProfile
	run          agent.AgentRun
	input        ContextInput
	packet       agent.ContextPacket
	model        llmapp.ModelInfo
	registry     *tools.Registry
	definitions  []llmapp.ToolDefinition
	budget       int
	softDeadline time.Time
	turns        int
	usage        map[string]int
	messages     []llmapp.ChatMessage
	cards        []ConversationPart
	overflowHits int
	softNoted    bool
}

// Run executes one work item. On failure the agent_run row is already marked
// failed/interrupted and the returned error carries the retry classification.
func (r *Runner) Run(ctx context.Context, item agent.WorkItem) (RunResult, error) {
	profileKey, ok := ProfileKeyForWorkType(item.Type)
	if !ok {
		return RunResult{}, &RunError{
			Reason:    "invalid_work_type",
			Retryable: false,
			Cause:     fmt.Errorf("unsupported work item type %q", item.Type),
		}
	}
	payload, err := ParseWorkItemPayload(item.Payload)
	if err != nil {
		return RunResult{}, &RunError{Reason: "invalid_payload", Retryable: false, Cause: err}
	}
	profile, err := r.repo.GetProfile(ctx, profileKey)
	if err != nil {
		return RunResult{}, &RunError{
			Reason:    "profile_not_found",
			Retryable: !errors.Is(err, agent.ErrNotFound),
			Cause:     err,
		}
	}

	run, err := r.createRun(ctx, item, profile, payload)
	if err != nil {
		return RunResult{}, &RunError{Reason: "run_create_failed", Retryable: true, Cause: err}
	}
	state := &runState{
		item:    item,
		payload: payload,
		profile: profile,
		run:     run,
		usage:   map[string]int{},
	}

	hard := time.Duration(r.hardSeconds(profile)) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, hard)
	defer cancel()

	if err := r.prepare(runCtx, state); err != nil {
		return r.fail(runCtx, state, err)
	}
	startedPayload := map[string]any{
		"run_id":       idText(state.run.ID),
		"profile_key":  profile.Key,
		"work_item_id": idText(item.ID),
	}
	if state.payload.ConversationID > 0 {
		startedPayload["conversation_id"] = idText(state.payload.ConversationID)
	}
	r.publish(state, "agent.run.started", startedPayload)
	r.publishActivity(state, "thinking", "", "")

	finalText, err := r.loop(runCtx, state)
	if err != nil {
		return r.fail(runCtx, state, err)
	}
	if err := r.writeOutputs(runCtx, state, finalText); err != nil {
		return r.fail(runCtx, state, err)
	}
	return r.finish(state, agent.AgentRunDone, "", false)
}

func (r *Runner) createRun(ctx context.Context, item agent.WorkItem, profile agent.AgentProfile, payload WorkItemPayload) (agent.AgentRun, error) {
	if r.ids == nil {
		return agent.AgentRun{}, errors.New("agent runner has no ID generator")
	}
	run := agent.AgentRun{
		ID:            r.ids.Next(),
		ProfileKey:    profile.Key,
		WorkItemID:    &item.ID,
		Status:        agent.AgentRunRunning,
		OwnerInstance: r.instanceID,
		LeaseEpoch:    item.LeaseEpoch,
		StartedAt:     r.now(),
		Usage:         map[string]int{},
	}
	if payload.ConversationID > 0 {
		conversationID := payload.ConversationID
		run.ConversationID = &conversationID
	}
	stored, err := r.repo.CreateRun(ctx, run)
	if err != nil {
		return agent.AgentRun{}, err
	}
	return stored, nil
}

// prepare loads the version/conversation context, resolves the model, persists
// the context packet and the initial transcript rows.
func (r *Runner) prepare(ctx context.Context, state *runState) error {
	if r.versions == nil {
		return errors.New("agent runner has no version reader")
	}
	versionPack, err := r.versions.VersionPack(ctx, state.item.VersionID)
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.input.VersionPack = versionPack

	if state.payload.ConversationID > 0 {
		if r.conversation == nil {
			return errors.New("agent runner has no conversation port")
		}
		snapshot, err := r.conversation.Snapshot(ctx, state.item.VersionID, state.payload.ConversationID, r.intSetting("context.keep_recent_messages", DefaultKeepRecentMessages))
		if err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		state.input.Conversation = ConversationContext{
			Summary:        snapshot.Summary,
			PinnedFacts:    snapshot.PinnedFacts,
			RecentMessages: conversationMessages(snapshot.Messages),
		}
	}
	state.input.TeamRoom = r.teamRoomContext(ctx, state.item.VersionID)

	modelInfo, err := r.chat.ResolveModel(ctx, profileModel(state.profile))
	if err != nil {
		return &RunError{Reason: "ai_unavailable", Retryable: true, Cause: err}
	}
	state.model = modelInfo
	state.input.ModelBudget = ModelBudget{ContextWindow: modelInfo.ContextWindow, MaxOutput: modelInfo.MaxOutput}
	state.input.Profile = state.profile
	state.input.Task = r.taskContext(state)
	state.input.Artifacts = artifactRefs(state)

	attachmentText := ""
	if state.item.Type == WorkTypeAttachmentIngest {
		if r.attachments == nil {
			return errors.New("agent runner has no attachment reader")
		}
		attachment, err := r.attachments.GetAttachment(ctx, state.item.VersionID, state.payload.AttachmentID)
		if err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		attachmentText = attachmentUserText(attachment)
		state.input.Conversation.RecentMessages = append(state.input.Conversation.RecentMessages, Message{
			ID:      attachment.ID,
			Role:    "user",
			Content: attachmentUserText(attachment),
		})
	}

	state.registry = tools.NewP1Registry(r.toolDepsForRun(state))
	state.definitions = state.registry.Definitions(state.profile.AllowedTools)

	packet, compactInfo, err := r.builder.Build(ctx, state.input)
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	packet.RunID = state.run.ID
	saved, err := r.repo.SaveContextPacket(ctx, packet)
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.packet = saved
	state.budget = compactInfo.Budget
	state.run.ContextPacketID = &saved.ID
	state.softDeadline = r.now().Add(time.Duration(r.softSeconds(state.profile)) * time.Second)
	r.persistCompaction(ctx, state, compactInfo, "threshold")

	systemContent := sectionContent(saved, contextSectionSystem)
	userContent := r.userMessage(ctx, state, attachmentText)
	if err := r.appendTranscript(ctx, state, agent.MessageRoleSystem, systemContent, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	if err := r.appendTranscript(ctx, state, agent.MessageRoleUser, userContent, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.messages = []llmapp.ChatMessage{
		{Role: "system", Content: systemContent},
		{Role: "user", Content: userContent},
	}
	return nil
}

func (r *Runner) loop(ctx context.Context, state *runState) (string, error) {
	maxTurns := r.maxTurns(state.profile)
	for state.turns < maxTurns {
		if err := ctx.Err(); err != nil {
			return "", contextRunError(err)
		}
		r.publishActivity(state, "thinking", "", "")
		soft := !r.now().Before(state.softDeadline)
		request := llmapp.ChatRequest{
			Model:           state.model.Model,
			Messages:        append([]llmapp.ChatMessage(nil), state.messages...),
			MaxOutputTokens: state.model.MaxOutput,
		}
		if soft {
			if !state.softNoted {
				state.softNoted = true
				request.Messages = append(request.Messages, llmapp.ChatMessage{
					Role:    "system",
					Content: "运行时间即将用尽，请不要再调用工具，直接给出最终回复。",
				})
			}
		} else {
			request.Tools = state.definitions
		}

		response, err := r.chat.Chat(ctx, request)
		if err != nil {
			if llmapp.IsContextOverflow(err) {
				if overflowErr := r.handleOverflow(ctx, state); overflowErr != nil {
					return "", overflowErr
				}
				continue
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", contextRunError(ctxErr)
			}
			return "", &RunError{Reason: "model_error", Retryable: true, Cause: err}
		}
		state.turns++
		mergeUsage(state.usage, response.Usage)

		if err := r.persistAssistant(ctx, state, response); err != nil {
			return "", err
		}
		state.messages = append(state.messages, llmapp.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		if len(response.ToolCalls) == 0 {
			return strings.TrimSpace(response.Content), nil
		}
		if soft {
			// Tools are disabled at soft timeout; treat a tool-call-only reply as
			// the model ignoring the wrap-up instruction.
			if text := strings.TrimSpace(response.Content); text != "" {
				return text, nil
			}
			return "", &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("model requested tools after the soft timeout")}
		}
		if err := r.executeTools(ctx, state, response.ToolCalls); err != nil {
			return "", err
		}
	}
	return "", &RunError{
		Reason:    "budget",
		Retryable: false,
		Cause:     fmt.Errorf("agent run reached the max turn budget (%d)", maxTurns),
	}
}

func (r *Runner) executeTools(ctx context.Context, state *runState, calls []llmapp.ToolCall) error {
	scope := tools.Scope{
		VersionID:      state.item.VersionID,
		ConversationID: state.payload.ConversationID,
		AgentRunID:     state.run.ID,
	}
	maxBytes := r.intSetting("agent.tool_result_max_bytes", defaultToolResultMaxBytes)

	for _, call := range calls {
		r.publishActivity(state, "tool", call.Name, "")
		payload, err := state.registry.Execute(ctx, scope, call.Name, json.RawMessage(call.Arguments))
		content := ""
		contentRef := ""
		if err != nil {
			var toolErr *tools.Error
			if errors.As(err, &toolErr) {
				encoded, encodeErr := json.Marshal(toolErr.Payload())
				if encodeErr != nil {
					return &RunError{Reason: "tool_failure", Retryable: true, Cause: encodeErr}
				}
				content = string(encoded)
			} else {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return contextRunError(ctxErr)
				}
				return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
			}
		} else {
			encoded, encodeErr := json.Marshal(payload)
			if encodeErr != nil {
				return &RunError{Reason: "tool_failure", Retryable: true, Cause: encodeErr}
			}
			content = string(encoded)
			if card, ok := requirementCard(call.Name, payload); ok {
				state.cards = append(state.cards, card)
			}
			if maxBytes > 0 && len(encoded) > maxBytes && r.content != nil {
				ref, spillErr := r.content.PutText(ctx, "agent_tool_result", encoded)
				if spillErr != nil {
					return &RunError{Reason: "tool_failure", Retryable: true, Cause: spillErr}
				}
				contentRef = ref.Ref
				spilled, _ := json.Marshal(map[string]any{
					"head":        truncateRunes(content, toolResultHeadRunes),
					"sha256":      ref.SHA256,
					"content_ref": ref.Ref,
					"truncated":   true,
				})
				content = string(spilled)
			}
		}

		parts := []agent.MessagePart{{Type: "text", Text: content}}
		if contentRef != "" {
			parts = append(parts, agent.MessagePart{Type: "content_ref", Text: contentRef})
		}
		callID := call.ID
		message := agent.AgentMessage{
			RunID:          &state.run.ID,
			ConversationID: state.run.ConversationID,
			Role:           agent.MessageRoleTool,
			Parts:          parts,
			ToolCallID:     &callID,
		}
		if _, err := r.repo.AppendMessage(ctx, message); err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		state.messages = append(state.messages, llmapp.ChatMessage{
			Role:       "tool",
			Content:    content,
			ToolCallID: call.ID,
		})
	}
	return nil
}

// handleOverflow compacts the context and retries: half budget first, then the
// minimal context. A third overflow is unrecoverable; context_overflow alone
// never terminates the run.
func (r *Runner) handleOverflow(ctx context.Context, state *runState) error {
	state.overflowHits++
	var budget int
	switch state.overflowHits {
	case 1:
		budget = state.budget / 2
	case 2:
		budget = MinUsableBudget
	default:
		return &RunError{
			Reason:    "context_overflow_unrecoverable",
			Retryable: true,
			Cause:     errors.New("model rejected the context window even after compaction"),
		}
	}
	compacted, _ := Compact(state.input, budget)
	packet, info, err := r.builder.Build(ctx, compacted)
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	packet.RunID = state.run.ID
	saved, err := r.repo.SaveContextPacket(ctx, packet)
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.packet = saved
	state.input = compacted
	state.run.ContextPacketID = &saved.ID
	systemContent := sectionContent(saved, contextSectionSystem)
	for i := range state.messages {
		if state.messages[i].Role == "system" {
			state.messages[i].Content = systemContent
			break
		}
	}
	if err := r.appendTranscript(ctx, state, agent.MessageRoleSystem, systemContent, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	summaryVersion := 0
	if info.Compacted && strings.TrimSpace(info.Summary) != "" && state.payload.ConversationID > 0 && r.conversation != nil {
		stored, err := r.conversation.UpdateSummary(ctx, state.item.VersionID, state.payload.ConversationID, info.Summary, state.input.Conversation.PinnedFacts)
		if err == nil {
			summaryVersion = stored.Version
			r.publishSummaryUpdated(state, stored)
		}
	}
	payload := map[string]any{
		"run_id":          idText(state.run.ID),
		"summary_version": summaryVersion,
		"token_before":    info.BeforeTokens,
		"token_after":     info.AfterTokens,
	}
	if state.payload.ConversationID > 0 {
		payload["conversation_id"] = idText(state.payload.ConversationID)
	}
	r.publish(state, "context.compacted", payload)
	return nil
}

// writeOutputs persists the profile-specific result: Host/Curator conversation
// messages (Curator with requirement cards) and Scout team-room findings.
func (r *Runner) writeOutputs(ctx context.Context, state *runState, finalText string) error {
	switch state.profile.Key {
	case ProfileKeyClarificationHost:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("host produced an empty final message")}
		}
		if _, err := r.conversation.AppendAssistantMessage(ctx, AssistantMessageInput{
			VersionID:        state.item.VersionID,
			ConversationID:   state.payload.ConversationID,
			AgentRunID:       state.run.ID,
			ReplyToMessageID: replyTarget(state.payload.MessageID),
			Parts:            []ConversationPart{{Type: "text", Text: finalText}},
		}); err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
	case ProfileKeyRequirementCurator:
		if finalText == "" && len(state.cards) == 0 {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("curator produced no message or requirement card")}
		}
		parts := make([]ConversationPart, 0, 1+len(state.cards))
		if finalText != "" {
			parts = append(parts, ConversationPart{Type: "text", Text: finalText})
		}
		parts = append(parts, state.cards...)
		if _, err := r.conversation.AppendAssistantMessage(ctx, AssistantMessageInput{
			VersionID:        state.item.VersionID,
			ConversationID:   state.payload.ConversationID,
			AgentRunID:       state.run.ID,
			ReplyToMessageID: replyTarget(state.payload.MessageID),
			Parts:            parts,
		}); err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
	case ProfileKeyContextScout:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("scout produced an empty finding")}
		}
		if r.teamRoom == nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: errors.New("agent runner has no team room port")}
		}
		refs := []agent.PostRef{}
		if state.payload.AttachmentID > 0 {
			refs = append(refs, agent.PostRef{Kind: "attachment", ID: idText(state.payload.AttachmentID)})
		}
		if _, err := r.teamRoom.Post(ctx, agent.TeamRoomPost{
			VersionID:  state.item.VersionID,
			Team:       agent.TeamClarification,
			Kind:       agent.PostKindFinding,
			Content:    finalText,
			Refs:       refs,
			AgentRunID: &state.run.ID,
		}); err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
	default:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("agent produced an empty final message")}
		}
		if r.conversation != nil && state.payload.ConversationID > 0 {
			if _, err := r.conversation.AppendAssistantMessage(ctx, AssistantMessageInput{
				VersionID:        state.item.VersionID,
				ConversationID:   state.payload.ConversationID,
				AgentRunID:       state.run.ID,
				ReplyToMessageID: replyTarget(state.payload.MessageID),
				Parts:            []ConversationPart{{Type: "text", Text: finalText}},
			}); err != nil {
				return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
			}
		}
	}
	return nil
}

func (r *Runner) fail(ctx context.Context, state *runState, err error) (RunResult, error) {
	reason, retryable := classifyFailure(ctx, err)
	status := agent.AgentRunFailed
	if reason == "interrupted" || reason == "aborted" {
		status = agent.AgentRunInterrupted
	}
	result, finishErr := r.finish(state, status, reason, retryable)
	if finishErr != nil {
		return result, finishErr
	}
	return result, &RunError{Reason: reason, Retryable: retryable, Cause: err}
}

func (r *Runner) finish(state *runState, status agent.AgentRunStatus, reason string, retryable bool) (RunResult, error) {
	finishedAt := r.now()
	state.run.Status = status
	state.run.Turns = state.turns
	state.run.Usage = state.usage
	state.run.FinishedAt = &finishedAt
	state.run.TerminalReason = nil
	if reason != "" {
		state.run.TerminalReason = &reason
	}

	finishCtx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	if err := r.repo.FinishRun(finishCtx, state.run); err != nil {
		result := RunResult{
			AgentRunID:     state.run.ID,
			Status:         status,
			Turns:          state.turns,
			Usage:          state.usage,
			TerminalReason: reason,
		}
		if errors.Is(err, agent.ErrLeaseLost) {
			return result, &RunError{
				Reason:    "lease_lost",
				Retryable: false,
				Cause:     platform.NewError(409, "lease_lost", "agent run lease was taken over"),
			}
		}
		return result, &RunError{Reason: "run_finish_failed", Retryable: true, Cause: err}
	}

	eventType := "agent.run.finished"
	payload := map[string]any{
		"run_id": idText(state.run.ID),
		"turns":  state.turns,
		"usage":  state.usage,
	}
	if status != agent.AgentRunDone {
		eventType = "agent.run.failed"
		payload = map[string]any{
			"run_id":          idText(state.run.ID),
			"terminal_reason": reason,
			"retryable":       retryable,
		}
	}
	r.publish(state, eventType, payload)
	r.recordAudit(state, status, reason)

	return RunResult{
		AgentRunID:     state.run.ID,
		Status:         status,
		Turns:          state.turns,
		Usage:          state.usage,
		TerminalReason: reason,
	}, nil
}

func (r *Runner) appendTranscript(ctx context.Context, state *runState, role agent.MessageRole, content string, toolCallID *string, toolCalls []agent.ToolCall) error {
	message := agent.AgentMessage{
		RunID:          &state.run.ID,
		ConversationID: state.run.ConversationID,
		Role:           role,
		ToolCalls:      toolCalls,
		ToolCallID:     toolCallID,
	}
	if content != "" {
		message.Parts = []agent.MessagePart{{Type: "text", Text: content}}
	}
	_, err := r.repo.AppendMessage(ctx, message)
	return err
}

func (r *Runner) persistAssistant(ctx context.Context, state *runState, response llmapp.ChatResponse) error {
	parts := []agent.MessagePart{}
	if strings.TrimSpace(response.Content) != "" {
		parts = append(parts, agent.MessagePart{Type: "text", Text: response.Content})
	}
	message := agent.AgentMessage{
		RunID:          &state.run.ID,
		ConversationID: state.run.ConversationID,
		Role:           agent.MessageRoleAssistant,
		Parts:          parts,
		Usage:          response.Usage,
	}
	for _, call := range response.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, agent.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	if _, err := r.repo.AppendMessage(ctx, message); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	return nil
}

func (r *Runner) persistCompaction(ctx context.Context, state *runState, info CompactInfo, reason string) {
	if !info.Compacted || strings.TrimSpace(info.Summary) == "" {
		return
	}
	summaryVersion := 0
	if state.payload.ConversationID > 0 && r.conversation != nil {
		stored, err := r.conversation.UpdateSummary(ctx, state.item.VersionID, state.payload.ConversationID, info.Summary, state.input.Conversation.PinnedFacts)
		if err == nil {
			summaryVersion = stored.Version
			r.publishSummaryUpdated(state, stored)
		}
	}
	payload := map[string]any{
		"run_id":          idText(state.run.ID),
		"summary_version": summaryVersion,
		"token_before":    info.BeforeTokens,
		"token_after":     info.AfterTokens,
	}
	if state.payload.ConversationID > 0 {
		payload["conversation_id"] = idText(state.payload.ConversationID)
	}
	r.publish(state, "context.compacted", payload)
}

func (r *Runner) toolDepsForRun(state *runState) tools.Deps {
	deps := r.toolDeps
	versionID := state.item.VersionID
	versionPack := state.input.VersionPack
	deps.VersionPack = func(context.Context, tools.Scope) (any, error) {
		return versionPackResult(versionPack), nil
	}
	task := state.input.Task
	deps.TaskPack = func(context.Context, tools.Scope) (any, error) {
		return taskPackResult(versionID, state.payload.ConversationID, state.run.ID, task), nil
	}
	deps.TeamRoomRead = func(ctx context.Context, scope tools.Scope, maxPosts int) (any, error) {
		room := r.teamRoomContext(ctx, scope.VersionID)
		return teamRoomResult(scope.VersionID, room), nil
	}
	deps.SummaryUpdate = func(ctx context.Context, scope tools.Scope, summary string, pinned []string) (any, error) {
		if r.conversation == nil {
			return nil, errors.New("conversation port is not configured")
		}
		stored, err := r.conversation.UpdateSummary(ctx, scope.VersionID, scope.ConversationID, summary, pinned)
		if err != nil {
			return nil, err
		}
		r.publishSummaryUpdated(state, stored)
		return map[string]any{
			"conversation_id": idText(scope.ConversationID),
			"version":         stored.Version,
			"summary":         stored.Summary,
		}, nil
	}
	deps.RollbackProposal = func(ctx context.Context, scope tools.Scope, requirementID int64, reason string) (any, error) {
		if r.conversation == nil {
			return nil, errors.New("conversation port is not configured")
		}
		message, err := r.conversation.AppendAssistantMessage(ctx, AssistantMessageInput{
			VersionID:      scope.VersionID,
			ConversationID: scope.ConversationID,
			AgentRunID:     scope.AgentRunID,
			Parts: []ConversationPart{{
				Type: "rollback_proposal",
				Data: map[string]any{
					"requirement_id": idText(requirementID),
					"reason":         reason,
					"created_at":     r.now().UTC().Format(time.RFC3339),
				},
			}},
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"message_id": idText(message.ID)}, nil
	}
	return deps
}

func (r *Runner) taskContext(state *runState) TaskContext {
	scope := []string{
		"version_id=" + idText(state.item.VersionID),
		"work_item_id=" + idText(state.item.ID),
	}
	if state.payload.ConversationID > 0 {
		scope = append(scope, "conversation_id="+idText(state.payload.ConversationID))
	}
	switch state.item.Type {
	case WorkTypeChatTurn:
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "回复用户最新消息，推进需求澄清并指出仍需确认的信息。",
			Constraints:        []string{"不确认、不删除、不修改已确认需求", "只做澄清对话与会话摘要更新"},
			AcceptanceCriteria: []string{"引用用户最新消息", "给出必要的澄清问题或明确的下一步"},
			Scope:              scope,
		}
	case WorkTypeRequirementCurate:
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "从最近的澄清对话中创建或更新需求草案，并说明草案变化。",
			Constraints:        []string{"只创建/更新 draft 需求", "不确认、不删除、不关闭需求", "已确认需求只写回退提议"},
			AcceptanceCriteria: []string{"工具结果包含 requirement_id/revision/status", "用简短文本说明草案变化"},
			Scope:              scope,
		}
	case WorkTypeAttachmentIngest:
		ingestScope := append(scope, "attachment_id="+idText(state.payload.AttachmentID))
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "总结附件中的关键事实、约束、风险与需要澄清的问题。",
			Constraints:        []string{"只读附件与上下文", "不修改需求、不确认、不删除"},
			AcceptanceCriteria: []string{"输出精简 finding 并引用附件名", "列出至少一个待澄清问题（如有）"},
			Scope:              ingestScope,
		}
	default:
		return TaskContext{Type: state.item.Type, Scope: scope}
	}
}

func (r *Runner) userMessage(ctx context.Context, state *runState, attachmentText string) string {
	switch state.item.Type {
	case WorkTypeAttachmentIngest:
		return attachmentText
	case WorkTypeChatTurn, WorkTypeRequirementCurate:
		if state.payload.MessageID > 0 && r.conversation != nil {
			message, err := r.conversation.GetMessage(ctx, state.item.VersionID, state.payload.ConversationID, state.payload.MessageID)
			if err == nil && strings.TrimSpace(message.Text) != "" {
				return strings.TrimSpace(message.Text)
			}
		}
		if state.payload.Trigger == "opening" || state.payload.MessageID == 0 {
			return "这是澄清会话的开场。请向用户提一个简短的开场问题，引导其描述需求。"
		}
		return "请基于最近对话继续澄清需求。"
	default:
		return "请根据当前任务继续执行。"
	}
}

func (r *Runner) teamRoomContext(ctx context.Context, versionID int64) TeamRoomContext {
	if r.teamRoom == nil {
		return TeamRoomContext{}
	}
	room, err := r.teamRoom.Context(ctx, versionID, string(agent.TeamClarification), 10)
	if err != nil {
		return TeamRoomContext{}
	}
	return room
}

func (r *Runner) publish(state *runState, eventType string, payload map[string]any) {
	if r.events == nil {
		return
	}
	scope := map[string]any{
		"version_id":   idText(state.item.VersionID),
		"agent_run_id": idText(state.run.ID),
	}
	if state.payload.ConversationID > 0 {
		scope["conversation_id"] = idText(state.payload.ConversationID)
	}
	_ = r.events.Publish(context.Background(), eventingapp.Event{
		Channel: "version:" + idText(state.item.VersionID),
		Type:    eventType,
		Scope:   scope,
		Payload: payload,
	})
}

// publishActivity emits the non-token "AI thinking/tool" activity event.
func (r *Runner) publishActivity(state *runState, status, tool, summary string) {
	payload := map[string]any{
		"run_id": idText(state.run.ID),
		"status": status,
	}
	if state.payload.ConversationID > 0 {
		payload["conversation_id"] = idText(state.payload.ConversationID)
	}
	if tool != "" {
		payload["tool"] = tool
	}
	if summary != "" {
		payload["summary"] = summary
	}
	r.publish(state, "agent.activity", payload)
}

func (r *Runner) publishSummaryUpdated(state *runState, stored ConversationSummary) {
	if state.payload.ConversationID <= 0 {
		return
	}
	r.publish(state, "conversation.summary_updated", map[string]any{
		"conversation_id": idText(state.payload.ConversationID),
		"version":         stored.Version,
		"token_estimate":  EstimateTokens(stored.Summary),
	})
}

func (r *Runner) recordAudit(state *runState, status agent.AgentRunStatus, reason string) {
	if r.audit == nil {
		return
	}
	actor := int64(0)
	action := "agent.run.finished"
	if status != agent.AgentRunDone {
		action = "agent.run.failed"
	}
	metadata := map[string]any{
		"agent_run_id": idText(state.run.ID),
		"work_item_id": idText(state.item.ID),
		"version_id":   idText(state.item.VersionID),
		"profile_key":  state.profile.Key,
		"status":       string(status),
	}
	if reason != "" {
		metadata["terminal_reason"] = reason
	}
	_ = r.audit.Record(context.Background(), auditapp.Event{
		ActorID:    &actor,
		Action:     action,
		TargetKind: "agent_run",
		TargetID:   idText(state.run.ID),
		Metadata:   metadata,
	})
}

func (r *Runner) maxTurns(profile agent.AgentProfile) int {
	limit := profile.Budgets.MaxTurns
	setting := r.intSetting("agent.max_turns", defaultMaxTurns)
	switch {
	case limit <= 0:
		limit = setting
	case setting > 0 && setting < limit:
		limit = setting
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

func (r *Runner) softSeconds(profile agent.AgentProfile) int {
	seconds := profile.Budgets.SoftSeconds
	if seconds <= 0 {
		seconds = r.intSetting("agent.run_soft_timeout_seconds", defaultSoftSeconds)
	}
	if seconds < 1 {
		seconds = defaultSoftSeconds
	}
	return seconds
}

func (r *Runner) hardSeconds(profile agent.AgentProfile) int {
	hard := profile.Budgets.HardSeconds
	if hard <= 0 {
		hard = r.intSetting("agent.run_hard_timeout_seconds", defaultHardSeconds)
	}
	soft := r.softSeconds(profile)
	if hard < soft {
		hard = soft
	}
	if hard < 1 {
		hard = defaultHardSeconds
	}
	return hard
}

func (r *Runner) intSetting(key string, fallback int) int {
	if r.settings == nil {
		return fallback
	}
	value, err := r.settings.Int(context.Background(), key)
	if err != nil || value <= 0 {
		return fallback
	}
	return int(value)
}

func (r *Runner) now() time.Time {
	if r.clock == nil {
		return time.Now()
	}
	return r.clock.Now()
}

// --- helpers ---------------------------------------------------------------

func profileModel(profile agent.AgentProfile) string {
	if profile.Model == nil {
		return ""
	}
	return strings.TrimSpace(*profile.Model)
}

func conversationMessages(messages []ConversationMessage) []Message {
	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		result = append(result, Message{ID: message.ID, Role: message.Role, Content: message.Text})
	}
	return result
}

func attachmentUserText(info AttachmentInfo) string {
	if info.Status != "" && info.Status != "ready" && info.Status != "extracted" {
		return fmt.Sprintf("附件《%s》解析状态为 %s，错误：%s。请向用户说明无法读取。", info.Name, info.Status, info.Error)
	}
	text := truncateRunes(strings.TrimSpace(info.Text), scoutAttachmentTextRunes)
	return fmt.Sprintf("请阅读附件《%s》（%s，共 %d 字符）并总结要点：\n%s", info.Name, info.MIME, info.TotalChars, text)
}

func versionPackResult(pack VersionPack) map[string]any {
	return map[string]any{
		"version_id":   idText(pack.VersionID),
		"title":        pack.Title,
		"status":       pack.Status,
		"suts":         pack.SUTs,
		"pages":        pack.Pages,
		"apis":         pack.APIs,
		"requirements": pack.Requirements,
		"attachments":  pack.Attachments,
	}
}

func taskPackResult(versionID, conversationID, runID int64, task TaskContext) map[string]any {
	return map[string]any{
		"version_id":          idText(versionID),
		"conversation_id":     idText(conversationID),
		"agent_run_id":        idText(runID),
		"type":                task.Type,
		"goal":                task.Goal,
		"constraints":         task.Constraints,
		"acceptance_criteria": task.AcceptanceCriteria,
		"scope":               task.Scope,
	}
}

func teamRoomResult(versionID int64, room TeamRoomContext) map[string]any {
	posts := make([]map[string]any, 0, len(room.Posts))
	for _, post := range room.Posts {
		refs := make([]map[string]any, 0, len(post.Refs))
		for _, ref := range post.Refs {
			refs = append(refs, map[string]any{"kind": ref.Kind, "id": ref.ID})
		}
		posts = append(posts, map[string]any{
			"id":         idText(post.ID),
			"kind":       string(post.Kind),
			"content":    post.Content,
			"refs":       refs,
			"created_at": post.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	result := map[string]any{
		"version_id": idText(versionID),
		"team":       string(agent.TeamClarification),
		"posts":      posts,
	}
	if room.Digest != nil {
		result["digest"] = map[string]any{
			"summary":    room.Digest.Summary,
			"version":    room.Digest.Version,
			"updated_at": room.Digest.UpdatedAt.UTC().Format(time.RFC3339),
		}
	}
	return result
}

func artifactRefs(state *runState) []ArtifactRef {
	refs := []ArtifactRef{}
	if state.payload.ConversationID > 0 {
		refs = append(refs, ArtifactRef{Kind: "conversation", ID: idText(state.payload.ConversationID)})
	}
	if state.payload.MessageID > 0 {
		refs = append(refs, ArtifactRef{Kind: "conversation_message", ID: idText(state.payload.MessageID)})
	}
	if state.payload.AttachmentID > 0 {
		refs = append(refs, ArtifactRef{Kind: "attachment", ID: idText(state.payload.AttachmentID)})
	}
	return refs
}

// requirementCard turns a successful draft tool result into a UI card part.
func requirementCard(toolName string, payload any) (ConversationPart, bool) {
	if toolName != tools.ToolRequirementDraftCreate && toolName != tools.ToolRequirementDraftUpdate {
		return ConversationPart{}, false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ConversationPart{}, false
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return ConversationPart{}, false
	}
	requirementID, ok := result["requirement_id"].(string)
	if !ok || requirementID == "" {
		return ConversationPart{}, false
	}
	data := map[string]any{
		"requirement_id": requirementID,
		"revision":       intValue(result["revision"]),
		"status":         stringValue(result["status"]),
	}
	if versionID := stringValue(result["version_id"]); versionID != "" {
		data["version_id"] = versionID
	}
	if title := stringValue(result["title"]); title != "" {
		data["title"] = title
	}
	return ConversationPart{Type: "requirement_card", Data: data}, true
}

func sectionContent(packet agent.ContextPacket, name string) string {
	for _, section := range packet.Sections {
		if section.Name == name {
			return section.Content
		}
	}
	return ""
}

func mergeUsage(target map[string]int, addition map[string]int) {
	for key, value := range addition {
		target[key] += value
	}
}

func contextRunError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &RunError{Reason: "interrupted", Retryable: true, Cause: err}
	}
	return &RunError{Reason: "aborted", Retryable: false, Cause: err}
}

func classifyFailure(ctx context.Context, err error) (string, bool) {
	var runErr *RunError
	if errors.As(err, &runErr) {
		return runErr.Reason, runErr.Retryable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "interrupted", true
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "aborted", false
	}
	if IsLeaseLost(err) {
		return "lease_lost", false
	}
	return "model_error", true
}

func replyTarget(messageID int64) *int64 {
	if messageID <= 0 {
		return nil
	}
	return &messageID
}

func idText(value int64) string { return strconv.FormatInt(value, 10) }

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}
