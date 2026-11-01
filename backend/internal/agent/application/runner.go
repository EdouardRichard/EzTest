package agentapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	// TaskProvider loads the P3 execution task snapshot (execute.case runs).
	// P1/P2 work items may leave it nil.
	TaskProvider TaskContextProvider
	// RunCanceller registers P3 run cancellation for pause/cancel/human
	// takeover. It may be nil for P1/P2-only deployments.
	RunCanceller RunCanceller
	// ReportFinalizer verifies the published revision of report.wave/
	// report.version runs (spec §4.4). It may be nil for deployments without the
	// P4 report worker, but a report task then fails as a configuration error.
	ReportFinalizer ReportFinalizer
	// RequirementFinalizer verifies that requirement.close runs recorded the
	// closure for their scoped requirement version (retryable if absent).
	RequirementFinalizer RequirementFinalizer
	InstanceID           string
}

// Runner executes one work item as an agent run: context packet, bounded model
// loop, scoped tool calls and the profile-specific output. It never confirms,
// deletes or closes a requirement; the model can only submit drafts and
// proposals through the scoped tools.
type Runner struct {
	repo            agent.RuntimeRepository
	chat            llmapp.ChatGateway
	settings        SettingsReader
	clock           clock.Clock
	ids             id.Generator
	events          eventingapp.Publisher
	audit           auditapp.Recorder
	conversation    ConversationPort
	versions        VersionReader
	attachments     AttachmentReader
	teamRoom        TeamRoomPort
	content         ContentWriter
	toolDeps        tools.Deps
	taskProvider    TaskContextProvider
	runCanceller    RunCanceller
	reportFinalizer      ReportFinalizer
	requirementFinalizer RequirementFinalizer
	instanceID      string
	builder         *ContextBuilder
}

// NewRunner wires the agent runner.
func NewRunner(deps RunnerDeps) *Runner {
	return &Runner{
		repo:            deps.Runtime,
		chat:            deps.Chat,
		settings:        deps.Settings,
		clock:           deps.Clock,
		ids:             deps.IDs,
		events:          deps.Events,
		audit:           deps.Audit,
		conversation:    deps.Conversation,
		versions:        deps.Versions,
		attachments:     deps.Attachments,
		teamRoom:        deps.TeamRoom,
		content:         deps.Content,
		toolDeps:        deps.ToolDeps,
		taskProvider:    deps.TaskProvider,
		runCanceller:    deps.RunCanceller,
		reportFinalizer: deps.ReportFinalizer,
		requirementFinalizer: deps.RequirementFinalizer,
		instanceID:      deps.InstanceID,
		builder:         NewContextBuilder(deps.Settings, NewLLMSummarizer(deps.Chat)),
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
	decodeDecimal := func(field string, value json.RawMessage) (int64, error) {
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
	parseID := func(field string) (int64, error) {
		value, ok := object[field]
		if !ok {
			return 0, nil
		}
		return decodeDecimal(field, value)
	}
	// parseInt32 decodes a revision/round. The P4 report payloads carry these
	// as JSON numbers or decimal strings; the first present key wins.
	parseInt32 := func(fields ...string) (int32, error) {
		for _, field := range fields {
			value, ok := object[field]
			if !ok {
				continue
			}
			parsed, err := decodeDecimal(field, value)
			if err != nil {
				return 0, err
			}
			if parsed > math.MaxInt32 {
				return 0, fmt.Errorf("work item payload %s exceeds int32 range", field)
			}
			return int32(parsed), nil
		}
		return 0, nil
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
	if payload.RequirementID, err = parseID("requirement_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.RequirementVersionID, err = parseID("requirement_version_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.CaseID, err = parseID("case_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.CaseVersionID, err = parseID("case_version_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.RoundID, err = parseID("round_id"); err != nil {
		return WorkItemPayload{}, err
	}
	// P3 execution payload (contracts/task-types.md §5). DispatchEpoch is a
	// decimal string/number; DispatchToken is opaque and only ever compared.
	if payload.IntentID, err = parseID("intent_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.WaveID, err = parseID("wave_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.CaseRunID, err = parseID("case_run_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.DispatchEpoch, err = parseID("dispatch_epoch"); err != nil {
		return WorkItemPayload{}, err
	}
	// P4 delivery/steward payload (contracts/task-types.md §6). ReportRevision
	// is canonically "report_revision"; the frozen report.wave/report.version
	// payload spells it "revision", accepted as a compatibility alias.
	if payload.IssueID, err = parseID("issue_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.ReportID, err = parseID("report_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.ReportRevision, err = parseInt32("report_revision", "revision"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.CloseRound, err = parseInt32("close_round"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.ReportRevisionID, err = parseID("report_revision_id"); err != nil {
		return WorkItemPayload{}, err
	}
	// P5 codeintel payload (contracts/task-types.md §7). IDs use the same
	// decimal-string/number convention; commit SHAs must be strings.
	if payload.RepoID, err = parseID("repo_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.IndexRunID, err = parseID("index_run_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.AnalysisID, err = parseID("analysis_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if baseSHA, ok := object["base_commit_sha"]; ok {
		if err := json.Unmarshal(baseSHA, &payload.BaseCommitSHA); err != nil {
			return WorkItemPayload{}, fmt.Errorf("work item payload base_commit_sha must be a string")
		}
	}
	if headSHA, ok := object["head_commit_sha"]; ok {
		if err := json.Unmarshal(headSHA, &payload.HeadCommitSHA); err != nil {
			return WorkItemPayload{}, fmt.Errorf("work item payload head_commit_sha must be a string")
		}
	}
	if fingerprint, ok := object["predicate_fingerprint"]; ok {
		if err := json.Unmarshal(fingerprint, &payload.PredicateFingerprint); err != nil {
			return WorkItemPayload{}, fmt.Errorf("work item payload predicate_fingerprint must be a string")
		}
	}
	if token, ok := object["dispatch_token"]; ok {
		_ = json.Unmarshal(token, &payload.DispatchToken)
	}
	if trigger, ok := object["trigger"]; ok {
		_ = json.Unmarshal(trigger, &payload.Trigger)
	}
	return payload, nil
}

// runState carries one run's mutable state.
type runState struct {
	item             agent.WorkItem
	payload          WorkItemPayload
	profile          agent.AgentProfile
	run              agent.AgentRun
	input            ContextInput
	packet           agent.ContextPacket
	model            llmapp.ModelInfo
	registry         *tools.Registry
	definitions      []llmapp.ToolDefinition
	budget           int
	softDeadline     time.Time
	turns            int
	usage            map[string]int
	messages         []llmapp.ChatMessage
	userContent      string
	cards            []ConversationPart
	conversationKind string
	team             agent.Team
	overflowHits     int
	softNoted        bool
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
	conversationKind := ""
	if item.Type == WorkTypeChatTurn && payload.ConversationID > 0 {
		if r.conversation == nil {
			return RunResult{}, &RunError{
				Reason:    "tool_failure",
				Retryable: true,
				Cause:     errors.New("agent runner has no conversation port"),
			}
		}
		kind, kindErr := r.conversation.ConversationKind(ctx, item.VersionID, payload.ConversationID)
		if kindErr != nil {
			return RunResult{}, &RunError{Reason: "tool_failure", Retryable: true, Cause: kindErr}
		}
		conversationKind = kind
		if key, ok := ProfileKeyForConversationKind(kind); ok {
			profileKey = key
		}
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
		item:             item,
		payload:          payload,
		profile:          profile,
		run:              run,
		usage:            map[string]int{},
		conversationKind: conversationKind,
		team:             teamForProfile(profileKey),
	}

	hard := time.Duration(r.hardSeconds(profile)) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, hard)
	defer cancel()
	// Expose the run cancellation to the execution context so pause/cancel/human
	// takeover can interrupt a long execution.wait or a stuck agent loop.
	if r.runCanceller != nil && state.payload.CaseRunID > 0 {
		r.runCanceller.RegisterRun(state.payload.CaseRunID, cancel)
		defer r.runCanceller.ClearRun(state.payload.CaseRunID)
	}

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
	// report.wave/report.version only complete when the targeted revision is
	// published; a missing published revision is a retryable task failure.
	if err := r.finalizeReport(runCtx, state); err != nil {
		return r.fail(runCtx, state, err)
	}
	// requirement.close only completes when the closure round was actually
	// written; a transient predicate rejection is retried through the normal
	// work-item backoff instead of wedging the current close round.
	if err := r.finalizeRequirement(runCtx, state); err != nil {
		return r.fail(runCtx, state, err)
	}
	return r.finish(state, agent.AgentRunDone, "", false)
}

// finalizeReport runs the report finalizer hook for report tasks. It is a
// no-op for every other work type: only report.wave/report.version own a report
// revision whose publication gates the task (spec §4.4/§6.5).
func (r *Runner) finalizeReport(ctx context.Context, state *runState) error {
	switch state.item.Type {
	case WorkTypeReportWave, WorkTypeReportVersion:
	default:
		return nil
	}
	if r.reportFinalizer == nil {
		return &RunError{
			Reason:    "tool_failure",
			Retryable: false,
			Cause:     errors.New("agent runner has no report finalizer"),
		}
	}
	if state.payload.ReportID <= 0 || state.payload.ReportRevision <= 0 {
		return &RunError{
			Reason:    "invalid_payload",
			Retryable: false,
			Cause:     errors.New("report task payload has no report_id/revision"),
		}
	}
	if err := r.reportFinalizer.Finalize(ctx, state.payload.ReportID, state.payload.ReportRevision); err != nil {
		return &RunError{
			Reason:    "report_finalize_failed",
			Retryable: true,
			Cause:     err,
		}
	}
	return nil
}

// finalizeRequirement runs the requirement.close finalizer. A transient
// predicate 409 leaves the run marked done by the model loop; without this
// check the work item would be done with no closure and no new close round.
func (r *Runner) finalizeRequirement(ctx context.Context, state *runState) error {
	if state.item.Type != WorkTypeRequirementClose {
		return nil
	}
	if r.requirementFinalizer == nil {
		return &RunError{
			Reason:    "tool_failure",
			Retryable: false,
			Cause:     errors.New("agent runner has no requirement finalizer"),
		}
	}
	if state.payload.RequirementID <= 0 || state.payload.RequirementVersionID <= 0 {
		return &RunError{
			Reason:    "invalid_payload",
			Retryable: false,
			Cause:     errors.New("requirement.close payload has no requirement_id/requirement_version_id"),
		}
	}
	if err := r.requirementFinalizer.Finalize(ctx, state.payload.RequirementID, state.payload.RequirementVersionID); err != nil {
		return &RunError{
			Reason:    "requirement_finalize_failed",
			Retryable: true,
			Cause:     err,
		}
	}
	return nil
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
	state.input.TeamRoom = r.teamRoomContext(ctx, state.item.VersionID, state.team)

	// P3 execution tasks are loaded from the execution context (CaseRunner),
	// which also validates the case_run/intent and claims the dispatch lease.
	if isExecutionWorkType(state.item.Type) {
		if r.taskProvider == nil {
			return &RunError{
				Reason:    "tool_failure",
				Retryable: false,
				Cause:     errors.New("agent runner has no execution task provider"),
			}
		}
		executionTask, taskErr := r.taskProvider.ExecutionTask(ctx, state.item)
		if taskErr != nil {
			return &RunError{
				Reason:    "tool_failure",
				Retryable: executionTaskErrorRetryable(taskErr),
				Cause:     taskErr,
			}
		}
		state.input.Execution = &executionTask
	}

	modelInfo, err := r.chat.ResolveModel(ctx, profileModel(state.profile))
	if err != nil {
		return &RunError{Reason: "ai_unavailable", Retryable: true, Cause: err}
	}
	state.model = modelInfo
	state.input.ModelBudget = ModelBudget{ContextWindow: modelInfo.ContextWindow, MaxOutput: modelInfo.MaxOutput}
	state.input.Profile = state.profile
	state.input.Task = r.taskContext(ctx, state)
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

	// P5 composes the P1–P4 registry with the codeintel/visual tool groups; the
	// profile allowed_tools still select the surface each run may call.
	state.registry = tools.NewP5Registry(r.toolDepsForRun(state))
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
	state.userContent = userContent
	if err := r.appendTranscript(ctx, state, agent.MessageRoleSystem, systemContent, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	if err := r.appendTranscript(ctx, state, agent.MessageRoleUser, userContent, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.messages = promptMessages(saved, userContent)
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

func profileAllowsTool(profile agent.AgentProfile, name string) bool {
	for _, allowed := range profile.AllowedTools {
		if allowed == name {
			return true
		}
	}
	return false
}

func (r *Runner) executeTools(ctx context.Context, state *runState, calls []llmapp.ToolCall) error {
	ctx = platform.WithAgentRun(ctx, state.run.ID, state.item.ID)
	scope := tools.Scope{
		VersionID:            state.item.VersionID,
		ConversationID:       state.payload.ConversationID,
		RequirementID:        state.payload.RequirementID,
		RequirementVersionID: state.payload.RequirementVersionID,
		CaseID:               state.payload.CaseID,
		CaseVersionID:        state.payload.CaseVersionID,
		RoundID:              state.payload.RoundID,
		AgentRunID:           state.run.ID,
		WaveID:               state.payload.WaveID,
		CaseRunID:            state.payload.CaseRunID,
		IntentID:             state.payload.IntentID,
		DispatchEpoch:        state.payload.DispatchEpoch,
		DispatchToken:        state.payload.DispatchToken,
		// P4 delivery/steward task targets, copied from the work-item payload.
		IssueID:              state.payload.IssueID,
		ReportID:             state.payload.ReportID,
		ReportRevision:       state.payload.ReportRevision,
		CloseRound:           state.payload.CloseRound,
		ReportRevisionID:     state.payload.ReportRevisionID,
		PredicateFingerprint: state.payload.PredicateFingerprint,
	}
	maxBytes := r.intSetting("agent.tool_result_max_bytes", defaultToolResultMaxBytes)

	for _, call := range calls {
		r.publishActivity(state, "tool", call.Name, "")
		var payload any
		var err error
		if !profileAllowsTool(state.profile, call.Name) {
			err = tools.Invalid("tool", "tool is not allowed for this profile: "+call.Name)
		} else {
			payload, err = state.registry.Execute(ctx, scope, call.Name, json.RawMessage(call.Arguments))
		}
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
	state.messages = promptMessages(saved, state.userContent)
	if err := r.appendTranscript(ctx, state, agent.MessageRoleSystem, systemContent, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	summaryVersion := 0
	if info.Compacted && strings.TrimSpace(info.Summary) != "" && state.payload.ConversationID > 0 && r.conversation != nil {
		stored, err := r.conversation.UpdateSummary(ctx, state.item.VersionID, state.payload.ConversationID, info.Summary, state.input.Conversation.PinnedFacts)
		if err == nil {
			summaryVersion = stored.Version
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

// writeOutputs persists the profile-specific result: Host/Curator/Design Host/
// Design Planner conversation messages (Curator with requirement cards),
// Scout/Case Author/Design Critic/Data Planner team-room updates.
func (r *Runner) writeOutputs(ctx context.Context, state *runState, finalText string) error {
	switch state.profile.Key {
	case ProfileKeyCaseExecutor, ProfileKeyFailureAnalyst, ProfileKeyCoverageAnalyst,
		ProfileKeyIssueTriager, ProfileKeyReportWriter:
		// P3/P4 profiles persist their results through the scoped tools (or the
		// report finalizer); the final message is not a persisted artifact, so
		// an empty reply is not a run failure.
		return nil
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
	case ProfileKeyDesignHost:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("design host produced an empty final message")}
		}
		if state.payload.ConversationID <= 0 {
			return &RunError{Reason: "invalid_payload", Retryable: false, Cause: errors.New("design host run has no conversation")}
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
	case ProfileKeyDesignPlanner:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("design planner produced an empty scope message")}
		}
		if state.payload.ConversationID <= 0 {
			return &RunError{Reason: "invalid_payload", Retryable: false, Cause: errors.New("design planner run has no conversation")}
		}
		if _, err := r.conversation.AppendAssistantMessage(ctx, AssistantMessageInput{
			VersionID:      state.item.VersionID,
			ConversationID: state.payload.ConversationID,
			AgentRunID:     state.run.ID,
			Parts:          []ConversationPart{{Type: "text", Text: finalText}},
		}); err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		if err := r.postDesignUpdate(ctx, state, agent.PostKindStatus, finalText); err != nil {
			return err
		}
	case ProfileKeyCaseAuthor:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("case author produced no design update")}
		}
		if err := r.postDesignUpdate(ctx, state, agent.PostKindStatus, finalText); err != nil {
			return err
		}
	case ProfileKeyDesignCritic:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("design critic produced no review summary")}
		}
		if err := r.postDesignUpdate(ctx, state, agent.PostKindFinding, finalText); err != nil {
			return err
		}
	case ProfileKeyDataPlanner:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("data planner produced no data update")}
		}
		if err := r.postDesignUpdate(ctx, state, agent.PostKindStatus, finalText); err != nil {
			return err
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

// postDesignUpdate writes one design team-room update attributed to the run and
// returns a retryable failure when the team-room port is unavailable.
func (r *Runner) postDesignUpdate(ctx context.Context, state *runState, kind agent.PostKind, content string) error {
	if r.teamRoom == nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: errors.New("agent runner has no team room port")}
	}
	refs := []agent.PostRef{}
	if state.payload.RequirementID > 0 {
		refs = append(refs, agent.PostRef{Kind: "requirement", ID: idText(state.payload.RequirementID)})
	}
	if state.payload.CaseID > 0 {
		refs = append(refs, agent.PostRef{Kind: "case", ID: idText(state.payload.CaseID)})
	}
	if state.payload.RoundID > 0 {
		refs = append(refs, agent.PostRef{Kind: "design_round", ID: idText(state.payload.RoundID)})
	}
	if _, err := r.teamRoom.Post(ctx, agent.TeamRoomPost{
		VersionID:  state.item.VersionID,
		Team:       state.team,
		Kind:       kind,
		Content:    content,
		Refs:       refs,
		AgentRunID: &state.run.ID,
	}); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
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
		return taskPackResult(versionID, state.payload.ConversationID, state.run.ID, task, state.input.Execution, state.input.P4Task), nil
	}
	deps.TeamRoomRead = func(ctx context.Context, scope tools.Scope, maxPosts int) (any, error) {
		room := r.teamRoomContext(ctx, scope.VersionID, state.team)
		return teamRoomResult(scope.VersionID, state.team, room), nil
	}
	deps.SummaryUpdate = func(ctx context.Context, scope tools.Scope, summary string, pinned []string) (any, error) {
		if r.conversation == nil {
			return nil, errors.New("conversation port is not configured")
		}
		stored, err := r.conversation.UpdateSummary(ctx, scope.VersionID, scope.ConversationID, summary, pinned)
		if err != nil {
			return nil, err
		}
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

func (r *Runner) taskContext(ctx context.Context, state *runState) TaskContext {
	scope := []string{
		"version_id=" + idText(state.item.VersionID),
		"work_item_id=" + idText(state.item.ID),
	}
	if state.payload.ConversationID > 0 {
		scope = append(scope, "conversation_id="+idText(state.payload.ConversationID))
	}
	if state.payload.RequirementID > 0 {
		scope = append(scope, "requirement_id="+idText(state.payload.RequirementID))
	}
	if state.payload.RequirementVersionID > 0 {
		scope = append(scope, "requirement_version_id="+idText(state.payload.RequirementVersionID))
	}
	if state.payload.CaseID > 0 {
		scope = append(scope, "case_id="+idText(state.payload.CaseID))
	}
	if state.payload.CaseVersionID > 0 {
		scope = append(scope, "case_version_id="+idText(state.payload.CaseVersionID))
	}
	if state.payload.RoundID > 0 {
		scope = append(scope, "round_id="+idText(state.payload.RoundID))
	}
	if state.payload.WaveID > 0 {
		scope = append(scope, "wave_id="+idText(state.payload.WaveID))
	}
	if state.payload.CaseRunID > 0 {
		scope = append(scope, "case_run_id="+idText(state.payload.CaseRunID))
	}
	if state.payload.IntentID > 0 {
		scope = append(scope, "intent_id="+idText(state.payload.IntentID))
	}
	switch state.item.Type {
	case WorkTypeChatTurn:
		if state.conversationKind == "design" {
			return TaskContext{
				Type:               state.item.Type,
				Goal:               "作为设计 Host 与用户对话，读取脑图并按用户要求提交/修改场景与用例 draft。",
				Constraints:        []string{"AI 只能提交 draft/候选，不能确认、删除或关闭用例", "确认只属于用户", "不编造需求未表达的内容"},
				AcceptanceCriteria: []string{"引用用户最新消息", "修改脑图前先 design.graph_read", "用 case.graph_propose 提交草案并说明变化"},
				Scope:              scope,
			}
		}
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
	case WorkTypeDesignRound:
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "根据服务端扫描出的本轮设计范围，向用户说明待设计需求与预计场景/用例数量。",
			Constraints:        []string{"只输出范围说明", "不写库、不调用写工具", "不确认、不删除"},
			AcceptanceCriteria: []string{"说明本轮作用域与下一步", "不编造未激活需求"},
			Scope:              scope,
		}
	case WorkTypeDesignRequirement, WorkTypeDesignGap:
		goal := "为指定需求版本设计场景与用例草案：先读取脑图，再用 case.graph_propose 提交 1 个场景与至少 1 条可执行用例。"
		if state.item.Type == WorkTypeDesignGap {
			goal = "根据执行/设计缺口补充用例草案：先读取脑图，再用 case.graph_propose 新增或修改 draft 用例。"
		}
		return TaskContext{
			Type:               state.item.Type,
			Goal:               goal,
			Constraints:        []string{"只提交 draft/候选，不能确认、删除或关闭用例", "用例必须包含步骤、断言、来源与目标 version_sut", "同一需求版本内更新已有用例时传既有 ID/自然键"},
			AcceptanceCriteria: []string{"design.graph_read 后再提交", "case.graph_propose 返回 case_version_ids", "向设计团队频道说明变化"},
			Scope:              scope,
		}
	case WorkTypeDesignCritic:
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "内部自审当前需求的场景/用例草案，提交覆盖遗漏、不可执行步骤与断言缺失等 finding。",
			Constraints:        []string{"只读脑图并使用 design.review_submit", "不修改用例、不确认", "内部自审不构成人工关卡"},
			AcceptanceCriteria: []string{"至少 1 条可执行 finding", "finding 带 severity 与 message"},
			Scope:              scope,
		}
	case WorkTypeDesignDataPlan:
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "为当前 draft 用例补充前置条件与测试数据，生成新的 draft revision。",
			Constraints:        []string{"先用 design.graph_read 找到当前 draft 用例", "只使用 design.data_plan_submit", "不确认、不删除；已确认或 stale 版本不可修改"},
			AcceptanceCriteria: []string{"仅更新当前 draft 版本", "数据可复现且不含真实密钥"},
			Scope:              scope,
		}
	case WorkTypeExecuteCase:
		return TaskContext{
			Type: state.item.Type,
			Goal: "严格按任务上下文中的用例步骤顺序执行，逐步用 execution.step_record 记录，每条断言用 execution.assertion_record 记录，" +
				"需要等待时用 execution.wait，完成后用 execution.case_finish 提交候选终态。",
			Constraints: []string{
				"不得修改需求/用例/确认，不得跳过步骤或断言",
				"不得虚构 passed；expected 以任务上下文为准",
				"等待/时间窗口用 execution.wait，不要用重复 snapshot 空转",
				"密码只经 account.credentials 读取，不得写入证据/日志/回复",
				"登录失败/2FA 用 human.request 请求人工接管",
				"无法继续时用 execution.case_finish(blocked) 说明原因",
			},
			AcceptanceCriteria: []string{
				"每个执行过的 step 都有 execution.step_record",
				"每条执行的断言都有 execution.assertion_record",
				"最终调用一次 execution.case_finish",
			},
			Scope: scope,
		}
	case WorkTypeAnalyzeFailure:
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "阅读当前 wave 的失败 case_run 结果与证据，校验/细化 failure_class/category/severity/summary 并用 analysis.failure_submit 逐条提交。",
			Constraints:        []string{"只读浏览器结果与证据；不执行浏览器、不修改用例/需求", "只能细化 application，不得把非 application 升级为 application", "只在 wave scope 内提交 case_run"},
			AcceptanceCriteria: []string{"每条提交都基于已有结果/证据", "使用 analysis.failure_submit"},
			Scope:              scope,
		}
	case WorkTypeAnalyzeCoverage:
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "比对 current 需求、已确认用例与最近执行结果，找出执行覆盖缺口并用 analysis.coverage_submit 提交。",
			Constraints:        []string{"只读覆盖与需求数据；不修改用例/需求、不创建问题", "缺口原因只取 execution_not_run 或 execution_failed_coverage"},
			AcceptanceCriteria: []string{"每条缺口引用属于当前版本的 requirement 与 wave 内证据", "使用 analysis.coverage_submit"},
			Scope:              scope,
		}
	default:
		// P4 domains register their task-context providers from init hooks. When
		// one claims the type, its JSON-ready payload is rendered by
		// context.task_pack; the P3 switch above owns the P1/P2/P3 descriptions.
		if details, ok := p4TaskDetails(ctx, state.item); ok {
			state.input.P4Task = details
		}
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
			if state.conversationKind == "design" {
				return "这是设计会话的开场。请向用户简要说明当前设计范围，并提示用户可以在对话中要求新增或修改用例。"
			}
			return "这是澄清会话的开场。请向用户提一个简短的开场问题，引导其描述需求。"
		}
		if state.conversationKind == "design" {
			return "请根据最近的设计对话继续完善用例脑图；需要修改时先 design.graph_read 再 case.graph_propose。"
		}
		return "请基于最近对话继续澄清需求。"
	case WorkTypeDesignRound:
		return "请根据本轮设计范围，向用户简要说明待设计需求与预计生成的场景/用例数量。"
	case WorkTypeDesignRequirement:
		return fmt.Sprintf("请为需求 %s（需求版本 %s）设计场景与用例草案。",
			idText(state.payload.RequirementID), idText(state.payload.RequirementVersionID))
	case WorkTypeDesignGap:
		return fmt.Sprintf("请补齐需求 %s（需求版本 %s）对应的用例缺口并提交 draft。",
			idText(state.payload.RequirementID), idText(state.payload.RequirementVersionID))
	case WorkTypeDesignCritic:
		return fmt.Sprintf("请内部自审需求 %s（需求版本 %s）当前的场景/用例草案并提交 finding。",
			idText(state.payload.RequirementID), idText(state.payload.RequirementVersionID))
	case WorkTypeDesignDataPlan:
		return fmt.Sprintf("请为需求 %s（需求版本 %s）当前 draft 用例补充前置条件与测试数据。",
			idText(state.payload.RequirementID), idText(state.payload.RequirementVersionID))
	case WorkTypeExecuteCase:
		if state.input.Execution != nil {
			if message := strings.TrimSpace(state.input.Execution.UserMessage); message != "" {
				return message
			}
		}
		return "请按任务上下文中的用例步骤开始执行，并逐步记录结果。"
	case WorkTypeAnalyzeFailure:
		return fmt.Sprintf("请分析 wave %s 的失败结果并提交 failure 候选细化。", idText(state.payload.WaveID))
	case WorkTypeAnalyzeCoverage:
		return fmt.Sprintf("请分析 wave %s 的执行覆盖缺口并提交覆盖候选。", idText(state.payload.WaveID))
	default:
		return "请根据当前任务继续执行。"
	}
}

func (r *Runner) teamRoomContext(ctx context.Context, versionID int64, team agent.Team) TeamRoomContext {
	if r.teamRoom == nil {
		return TeamRoomContext{}
	}
	room, err := r.teamRoom.Context(ctx, versionID, string(team), 10)
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
	// P3 execution profiles carry their own case-scale budget (P3 spec §7.2:
	// case_executor 60 turns); the generic agent.max_turns (12) is a chat-scale
	// guard and must not truncate an execution run.
	if isP3Profile(profile.Key) {
		if limit <= 0 {
			limit = setting
		}
	} else {
		switch {
		case limit <= 0:
			limit = setting
		case setting > 0 && setting < limit:
			limit = setting
		}
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

// isP3Profile reports whether the profile belongs to the P3 execution team and
// therefore uses its own case-scale budgets instead of the chat-scale
// agent.max_turns guard.
func isP3Profile(key string) bool {
	switch key {
	case ProfileKeyCaseExecutor, ProfileKeyFailureAnalyst, ProfileKeyCoverageAnalyst:
		return true
	default:
		return false
	}
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
	if info.Status != "" && info.Status != "done" && info.Status != "ready" && info.Status != "extracted" {
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

func taskPackResult(versionID, conversationID, runID int64, task TaskContext, execution *ExecutionTask, p4Task any) map[string]any {
	result := map[string]any{
		"version_id":          idText(versionID),
		"conversation_id":     idText(conversationID),
		"agent_run_id":        idText(runID),
		"type":                task.Type,
		"goal":                task.Goal,
		"constraints":         task.Constraints,
		"acceptance_criteria": task.AcceptanceCriteria,
		"scope":               task.Scope,
	}
	if execution != nil {
		result["execution"] = executionTaskResult(*execution)
	}
	if p4Task != nil {
		result["p4_task"] = p4Task
	}
	return result
}

// executionTaskResult renders the P3 execution snapshot for context.task_pack.
// The one-time human response stays only in the context packet: the tool result
// is persisted in the agent transcript and must not duplicate a sensitive code.
func executionTaskResult(task ExecutionTask) map[string]any {
	suts := make([]map[string]any, 0, len(task.VersionSUTs))
	for _, sut := range task.VersionSUTs {
		suts = append(suts, map[string]any{
			"version_sut_id":   idText(sut.VersionSUTID),
			"sut_name":         sut.SUTName,
			"environment_name": sut.EnvironmentName,
			"base_url":         sut.BaseURL,
			"page_urls":        sut.PageURLs,
			"api_endpoints":    sut.APIEndpoints,
			"account_roles":    sut.AccountRoles,
		})
	}
	steps := make([]map[string]any, 0, len(task.Steps))
	for _, step := range task.Steps {
		entry := map[string]any{
			"seq":                   step.Seq,
			"action":                step.Action,
			"expected":              step.Expected,
			"account_role":          step.AccountRole,
			"target_version_sut_id": idText(step.TargetVersionSUTID),
		}
		if step.PageID != nil {
			entry["page_id"] = idText(*step.PageID)
		}
		if step.APIID != nil {
			entry["api_id"] = idText(*step.APIID)
		}
		steps = append(steps, entry)
	}
	assertions := make([]map[string]any, 0, len(task.Assertions))
	for _, assertion := range task.Assertions {
		entry := map[string]any{
			"step_seq":              assertion.StepSeq,
			"assertion_seq":         assertion.AssertionSeq,
			"kind":                  assertion.Kind,
			"expected":              assertion.Expected,
			"target_version_sut_id": idText(assertion.TargetVersionSUTID),
		}
		if assertion.Kind == "visual" {
			if assertion.CaseAssertionID > 0 {
				entry["case_assertion_id"] = idText(assertion.CaseAssertionID)
			}
			if assertion.VisualAssertionID > 0 {
				entry["visual_assertion_id"] = idText(assertion.VisualAssertionID)
			}
		}
		assertions = append(assertions, entry)
	}
	priorSteps := make([]map[string]any, 0, len(task.PriorSteps))
	for _, prior := range task.PriorSteps {
		priorSteps = append(priorSteps, map[string]any{
			"seq":                   prior.Seq,
			"status":                prior.Status,
			"observed":              prior.Observed,
			"error":                 prior.Error,
			"target_version_sut_id": idText(prior.TargetVersionSUTID),
		})
	}
	priorAssertions := make([]map[string]any, 0, len(task.PriorAssertions))
	for _, prior := range task.PriorAssertions {
		priorAssertions = append(priorAssertions, map[string]any{
			"step_seq":              prior.StepSeq,
			"assertion_seq":         prior.AssertionSeq,
			"kind":                  prior.Kind,
			"verdict":               prior.Verdict,
			"expected":              prior.Expected,
			"actual":                prior.Actual,
			"reason":                prior.Reason,
			"target_version_sut_id": idText(prior.TargetVersionSUTID),
		})
	}
	return map[string]any{
		"case_run_id":      idText(task.CaseRunID),
		"wave_id":          idText(task.WaveID),
		"intent_id":        idText(task.IntentID),
		"case_version_id":  idText(task.CaseVersionID),
		"case_title":       task.CaseTitle,
		"description":      task.Description,
		"preconditions":    task.Preconditions,
		"priority":         task.Priority,
		"version_suts":     suts,
		"steps":            steps,
		"assertions":       assertions,
		"prior_steps":      priorSteps,
		"prior_assertions": priorAssertions,
	}
}

func teamRoomResult(versionID int64, team agent.Team, room TeamRoomContext) map[string]any {
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
		"team":       string(team),
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
	if state.payload.RequirementID > 0 {
		refs = append(refs, ArtifactRef{Kind: "requirement", ID: idText(state.payload.RequirementID)})
	}
	if state.payload.RequirementVersionID > 0 {
		refs = append(refs, ArtifactRef{Kind: "requirement_version", ID: idText(state.payload.RequirementVersionID)})
	}
	if state.payload.CaseID > 0 {
		refs = append(refs, ArtifactRef{Kind: "case", ID: idText(state.payload.CaseID)})
	}
	if state.payload.CaseVersionID > 0 {
		refs = append(refs, ArtifactRef{Kind: "case_version", ID: idText(state.payload.CaseVersionID)})
	}
	if state.payload.RoundID > 0 {
		refs = append(refs, ArtifactRef{Kind: "design_round", ID: idText(state.payload.RoundID)})
	}
	if state.payload.WaveID > 0 {
		refs = append(refs, ArtifactRef{Kind: "run_wave", ID: idText(state.payload.WaveID)})
	}
	if state.payload.CaseRunID > 0 {
		refs = append(refs, ArtifactRef{Kind: "case_run", ID: idText(state.payload.CaseRunID)})
	}
	if state.payload.IntentID > 0 {
		refs = append(refs, ArtifactRef{Kind: "execution_intent", ID: idText(state.payload.IntentID)})
	}
	return refs
}

// teamForProfile selects the collaboration channel: design profiles use the
// design team room, P3 execution profiles use the execution team room, P4
// delivery profiles use the delivery room, the P4 steward uses the steward
// room, and every P1 profile keeps the clarification team room.
func teamForProfile(profileKey string) agent.Team {
	switch profileKey {
	case ProfileKeyDesignHost, ProfileKeyDesignPlanner, ProfileKeyCaseAuthor,
		ProfileKeyDesignCritic, ProfileKeyDataPlanner:
		return agent.TeamDesign
	case ProfileKeyCaseExecutor, ProfileKeyFailureAnalyst, ProfileKeyCoverageAnalyst:
		return agent.TeamExecution
	case ProfileKeyIssueTriager, ProfileKeyReportWriter:
		return agent.TeamDelivery
	case ProfileKeyVersionSteward:
		return agent.TeamSteward
	default:
		return agent.TeamClarification
	}
}

// isExecutionWorkType reports whether the work item needs the P3 execution task
// context provider. Only execute.case claims a case_run/dispatch lease;
// analyze.failure/coverage use the generic wave-scoped task context.
func isExecutionWorkType(workType string) bool {
	return workType == WorkTypeExecuteCase
}

// executionTaskErrorRetryable classifies a task-provider failure: stale scope,
// missing rows and dispatch-fence conflicts are terminal, infrastructure
// failures are retryable.
func executionTaskErrorRetryable(err error) bool {
	if IsLeaseLost(err) {
		return false
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 400, 404, 409:
			return false
		}
		return apiErr.Retryable || apiErr.Status >= 500
	}
	return true
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

// promptMessages renders the persisted context packet into the actual model
// prompt. The system section carries the profile/task instructions; every
// non-system section (version pack, rolling summary/recent window, team room,
// artifacts) is injected ahead of the current user message so context is not
// merely persisted without ever reaching the model.
func promptMessages(packet agent.ContextPacket, userContent string) []llmapp.ChatMessage {
	system := sectionContent(packet, contextSectionSystem)
	var builder strings.Builder
	for _, section := range packet.Sections {
		if section.Name == contextSectionSystem || strings.TrimSpace(section.Content) == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		fmt.Fprintf(&builder, "## %s\n%s", section.Name, section.Content)
	}
	if builder.Len() > 0 {
		builder.WriteString("\n\n# 当前用户消息\n")
	}
	builder.WriteString(userContent)
	return []llmapp.ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: builder.String()},
	}
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
