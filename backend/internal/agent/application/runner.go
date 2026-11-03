package agentapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/load"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
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
	// Images resolves image part data_refs into model-ready bytes. It may be nil
	// for deployments without vision; an image-bearing message then fails the
	// run instead of silently dropping the image.
	Images   ImageLoader
	TeamRoom TeamRoomPort
	Content  ContentWriter
	ToolDeps tools.Deps
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
	// Knowledge retrieves the approved knowledge injected into the version
	// context (P7). Nil disables injection for deployments without the
	// knowledge context.
	Knowledge KnowledgeReader
	// KnowledgeUsage records knowledge_usage rows after the context packet is
	// persisted. Nil disables usage accounting.
	KnowledgeUsage KnowledgeUsageRecorder
	// Briefing builds the derived version briefing appended to the cache-first
	// context on every round (Task 48). Nil keeps the version pack facts only;
	// the assembler prefix and transcript are still rendered.
	Briefing *agentcontext.BriefingBuilder
	// Clarification applies the clarifying→requirement_review edge when a
	// clarification host turn explicitly requests requirement confirmation. Nil
	// keeps the stage unchanged (deployments without the version stage machine).
	Clarification ClarificationConverger
	// DesignRounds consumes the design.cases stage-entry work item: it opens
	// the design round for the hard-gate-confirmed requirements and returns the
	// design conversation the job card is projected into (Task 65). Nil fails a
	// design.cases item as a configuration error.
	DesignRounds DesignRoundStarter
	// Batches consumes the execute.batch stage-entry work item: it triggers the
	// execution planner for the confirmed case batch. Nil fails an execute.batch
	// item as a configuration error.
	Batches BatchStarter
	// Logger receives the log-only knowledge runtime failures. It defaults to
	// slog.Default().
	Logger *slog.Logger
	// Load counts runs currently executing in this process (P8 drain
	// observability). Nil disables the counter.
	Load       *load.Counter
	InstanceID string
}

// Runner executes one work item as an agent run: context packet, bounded model
// loop, scoped tool calls and the profile-specific output. It never confirms,
// deletes or closes a requirement; the model can only submit drafts and
// proposals through the scoped tools.
type Runner struct {
	repo                 agent.RuntimeRepository
	chat                 llmapp.ChatGateway
	settings             SettingsReader
	clock                clock.Clock
	ids                  id.Generator
	events               eventingapp.Publisher
	audit                auditapp.Recorder
	conversation         ConversationPort
	versions             VersionReader
	attachments          AttachmentReader
	images               ImageLoader
	teamRoom             TeamRoomPort
	content              ContentWriter
	toolDeps             tools.Deps
	taskProvider         TaskContextProvider
	runCanceller         RunCanceller
	reportFinalizer      ReportFinalizer
	requirementFinalizer RequirementFinalizer
	knowledge            KnowledgeReader
	knowledgeUsage       KnowledgeUsageRecorder
	logger               *slog.Logger
	load                 *load.Counter
	instanceID           string
	builder              *ContextBuilder
	briefing             *agentcontext.BriefingBuilder
	assembler            *agentcontext.Assembler
	clarification        ClarificationConverger
	designRounds         DesignRoundStarter
	batches              BatchStarter
}

// NewRunner wires the agent runner.
func NewRunner(deps RunnerDeps) *Runner {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		repo:                 deps.Runtime,
		chat:                 deps.Chat,
		settings:             deps.Settings,
		clock:                deps.Clock,
		ids:                  deps.IDs,
		events:               deps.Events,
		audit:                deps.Audit,
		conversation:         deps.Conversation,
		versions:             deps.Versions,
		attachments:          deps.Attachments,
		images:               deps.Images,
		teamRoom:             deps.TeamRoom,
		content:              deps.Content,
		toolDeps:             deps.ToolDeps,
		taskProvider:         deps.TaskProvider,
		runCanceller:         deps.RunCanceller,
		reportFinalizer:      deps.ReportFinalizer,
		requirementFinalizer: deps.RequirementFinalizer,
		knowledge:            deps.Knowledge,
		knowledgeUsage:       deps.KnowledgeUsage,
		logger:               logger,
		load:                 deps.Load,
		instanceID:           deps.InstanceID,
		builder:              NewContextBuilder(deps.Settings, NewLLMSummarizer(deps.Chat)),
		briefing:             deps.Briefing,
		assembler:            agentcontext.NewAssembler(),
		clarification:        deps.Clarification,
		designRounds:         deps.DesignRounds,
		batches:              deps.Batches,
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

// ParseWorkItemIDList reads one JSON array of decimal IDs from a work item's
// 0.2.1 inputs object (for example design.cases requirement_ids or execute.batch
// case_ids). IDs may be decimal strings (canonical) or JSON numbers; duplicates
// are removed and the input order is preserved. A missing/null field is nil.
func ParseWorkItemIDList(raw json.RawMessage, field string) ([]int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, fmt.Errorf("work item inputs must be a JSON object")
	}
	value, ok := object[field]
	if !ok {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(value, &entries); err != nil {
		return nil, fmt.Errorf("work item inputs %s must be an array of decimal IDs", field)
	}
	ids := make([]int64, 0, len(entries))
	seen := make(map[int64]struct{}, len(entries))
	for index, entry := range entries {
		text := strings.TrimSpace(string(entry))
		if strings.HasPrefix(text, `"`) {
			var decoded string
			if err := json.Unmarshal(entry, &decoded); err != nil {
				return nil, fmt.Errorf("work item inputs %s[%d] must be a decimal ID", field, index)
			}
			text = strings.TrimSpace(decoded)
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("work item inputs %s[%d] must be a positive decimal ID", field, index)
		}
		if _, duplicate := seen[parsed]; duplicate {
			continue
		}
		seen[parsed] = struct{}{}
		ids = append(ids, parsed)
	}
	return ids, nil
}

// runState carries one run's mutable state.
type runState struct {
	item    agent.WorkItem
	payload WorkItemPayload
	profile agent.AgentProfile
	run     agent.AgentRun
	input   ContextInput
	packet  agent.ContextPacket
	// contextPacket is the cache-first packet the current prompt was rendered
	// from. It is persisted through EncodeContextPacket and its cache
	// bookkeeping is overwritten by the provider usage (contract §18.2.6).
	contextPacket agentcontext.ContextPacket
	// cacheStatsApplied guards the at-most-one context.cache_stats event per
	// run (contracts/events.md §18.2 #8).
	cacheStatsApplied bool
	// providerCacheTokens/providerCacheHit carry the provider-reported cache
	// booking across a boundary rebuild so a compacted packet persisted after
	// the first model turn keeps the authoritative statistics.
	providerCacheTokens   int
	providerCacheHit      bool
	providerCacheReported bool
	model                 llmapp.ModelInfo
	registry              *tools.Registry
	definitions           []llmapp.ToolDefinition
	// roleTools is the intersection of the profile's allowed tools with the
	// catalog role whitelist (R51); it is the surface exposed to the model.
	roleTools tools.ToolSet
	// roleMapped reports whether the profile maps to one of the seven catalog
	// roles. Unmapped profiles keep their own frozen contract whitelist.
	roleMapped bool
	// roleGuard audits and rejects a call outside the role whitelist. Nil for
	// profiles not mapped to one of the seven roles (they keep their own frozen
	// contract whitelist).
	roleGuard      *RoleToolGuard
	budget         int
	resourceBudget Budget
	startedAt      time.Time
	turns          int
	usage          map[string]int
	messages       []llmapp.ChatMessage
	images         []llmapp.ChatImage
	userContent    string
	cards          []ConversationPart
	// streamParts are the 0.2.1 UI streaming parts produced by this run
	// (thinking/tool_call/error). They are written with the final assistant
	// message as conversation_message_part rows and published on
	// conversation.message. The job's own card is a separate assistant message
	// owned by projectJobCard.
	streamParts      []ConversationPart
	conversationKind string
	team             agent.Team
	overflowHits     int
	softNoted        bool
}

// Run executes one work item and projects the job into its conversation as one
// job_card: the start snapshot before the run begins, one progress snapshot per
// completed turn and the terminal run outcome. The projection is best effort
// and never changes the run verdict or the persisted work-item state.
//
// The 0.2.1 stage-entry kinds (design.cases/execute.batch) take the
// deterministic path instead of a model run: they consume the contract item by
// running the domain action and project the truthful terminal state.
func (r *Runner) Run(ctx context.Context, item agent.WorkItem) (RunResult, error) {
	payload, payloadErr := ParseWorkItemPayload(item.Payload)
	workType := WorkTypeForItem(item)
	// A dispatcher-created stage-entry row carries the same value in the frozen
	// type column (Normalize defaults Type=Kind). A synthetic row that only
	// overrides Kind keeps the normal profile path so the frozen 0.2.0
	// type/card semantics are not hijacked by a bookkeeping label.
	stageEntry := IsStageEntryWorkType(workType) &&
		strings.TrimSpace(item.Kind) == workType &&
		(strings.TrimSpace(item.Type) == "" || strings.TrimSpace(item.Type) == workType)
	item.Type = workType
	switch {
	case stageEntry && workType == agent.WorkItemKindDesignCases:
		if payloadErr != nil {
			return RunResult{}, &RunError{Reason: "invalid_payload", Retryable: false, Cause: payloadErr}
		}
		return r.runDesignCases(ctx, item, payload)
	case stageEntry && workType == agent.WorkItemKindExecuteBatch:
		if payloadErr != nil {
			return RunResult{}, &RunError{Reason: "invalid_payload", Retryable: false, Cause: payloadErr}
		}
		return r.runExecuteBatch(ctx, item, payload)
	}
	if payloadErr == nil {
		r.projectJobCard(ctx, item, payload, "running", 0, jobCardFailure{})
	}
	result, err := r.run(ctx, item)
	if payloadErr == nil {
		if status, ok := jobCardTerminalStatus(item, err); ok {
			// The run context may already be cancelled; the terminal card is a
			// short independent write like the other post-run write-backs.
			finishCtx, cancel := context.WithTimeout(context.Background(), jobCardWriteTimeout)
			failure := jobCardFailure{}
			if status == "failed" {
				failure = jobCardFailureDetails(err)
			}
			r.projectJobCard(finishCtx, item, payload, status, result.Turns, failure)
			cancel()
		}
	}
	return result, err
}

// runDesignCases consumes the design.cases stage-entry work item: it opens the
// design round for the requirements confirmed by the hard gate and projects the
// job card into the design conversation. The round's case-author tasks produce
// the actual drafts through case.graph_propose; this job only owns the entry.
func (r *Runner) runDesignCases(ctx context.Context, item agent.WorkItem, payload WorkItemPayload) (RunResult, error) {
	if r.designRounds == nil {
		return RunResult{}, &RunError{
			Reason:    "tool_failure",
			Retryable: false,
			Cause:     errors.New("agent runner has no design round starter"),
		}
	}
	requirementIDs, err := ParseWorkItemIDList(item.Inputs, "requirement_ids")
	if err != nil {
		return RunResult{}, &RunError{Reason: "invalid_payload", Retryable: false, Cause: err}
	}
	conversationID, startErr := r.designRounds.StartDesignRound(ctx, item.VersionID, requirementIDs)
	if startErr != nil {
		return RunResult{}, stageEntryRunError(startErr)
	}
	if conversationID > 0 {
		// The card lives in the design conversation the round just opened. The
		// run context may already be cancelled, so use a short independent
		// write like the other post-run projections.
		payload.ConversationID = conversationID
		finishCtx, cancel := context.WithTimeout(context.Background(), jobCardWriteTimeout)
		r.projectJobCard(finishCtx, item, payload, "done", 0, jobCardFailure{})
		cancel()
	}
	return RunResult{Status: agent.AgentRunDone}, nil
}

// runExecuteBatch consumes the execute.batch stage-entry work item: the
// deterministic execution planner owns the batch, so the agent runtime only
// triggers it for the confirmed cases and completes the entry job.
func (r *Runner) runExecuteBatch(ctx context.Context, item agent.WorkItem, payload WorkItemPayload) (RunResult, error) {
	if r.batches == nil {
		return RunResult{}, &RunError{
			Reason:    "tool_failure",
			Retryable: false,
			Cause:     errors.New("agent runner has no execution batch starter"),
		}
	}
	caseIDs, err := ParseWorkItemIDList(item.Inputs, "case_ids")
	if err != nil {
		return RunResult{}, &RunError{Reason: "invalid_payload", Retryable: false, Cause: err}
	}
	if startErr := r.batches.StartBatch(ctx, item.VersionID, caseIDs); startErr != nil {
		return RunResult{}, stageEntryRunError(startErr)
	}
	return RunResult{Status: agent.AgentRunDone}, nil
}

// stageEntryRunError classifies a deterministic stage-entry failure: a typed
// 4xx (bad scope/ownership) cannot succeed on another attempt, transport and
// server failures are retried through the normal work-item backoff.
func stageEntryRunError(err error) *RunError {
	retryable := true
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict:
			retryable = false
		default:
			retryable = apiErr.Retryable || apiErr.Status >= http.StatusInternalServerError
		}
	}
	return &RunError{Reason: "stage_entry_failed", Retryable: retryable, Cause: err}
}

// run is Run without the conversation job-card projection. On failure the
// agent_run row is already marked failed/interrupted and the returned error
// carries the retry classification.
func (r *Runner) run(ctx context.Context, item agent.WorkItem) (RunResult, error) {
	if r.load != nil {
		r.load.Inc()
		defer r.load.Dec()
	}
	// The 0.2.1 Kind is authoritative when present; legacy rows keep Type.
	item.Type = WorkTypeForItem(item)
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

	// An image on the triggering message requires a vision-capable model
	// (spec §7.3/§8.3). Detect image parts before resolving so the single gate
	// is ResolveModel(..., Vision:true); the typed 400 happens before any image
	// bytes are loaded and before the run row is created.
	imageRefs, err := r.triggerImageRefs(ctx, item, payload)
	if err != nil {
		r.recordPreDispatchFailure(ctx, item, profile, payload, err)
		return RunResult{}, err
	}
	requirements := modelRequirements(profile)
	if len(imageRefs) > 0 {
		requirements = append(requirements, llmapp.ModelRequirements{Vision: true})
	}
	// Task 55: the conversation-level 0.2.1 model override (a user-initiated
	// switch) takes precedence over the profile-bound model; an empty override
	// keeps the profile binding, and an empty profile model stays the config
	// default. The override is loaded before resolution and validated by the
	// same ResolveModel capability gate; an override that no longer resolves
	// fails the run and is never swapped for another model (D24).
	modelOverride := ""
	if payload.ConversationID > 0 && r.conversation != nil {
		override, overrideErr := r.conversation.ModelOverride(ctx, item.VersionID, payload.ConversationID)
		if overrideErr != nil {
			runErr := &RunError{Reason: "tool_failure", Retryable: true, Cause: overrideErr}
			r.recordPreDispatchFailure(ctx, item, profile, payload, runErr)
			return RunResult{}, runErr
		}
		modelOverride = strings.TrimSpace(override)
	}
	requestedModel := profileModel(profile)
	if modelOverride != "" {
		requestedModel = modelOverride
	}
	// Resolve the model before creating the run so agent_run records the model
	// and config actually used (spec §7.3). Capability validation happens in
	// ResolveModel against the role's tool needs.
	modelInfo, err := r.chat.ResolveModel(ctx, 0, requestedModel, requirements...)
	if err != nil {
		runErr := resolveRunError(err)
		r.recordPreDispatchFailure(ctx, item, profile, payload, runErr)
		return RunResult{}, runErr
	}

	run, err := r.createRun(ctx, item, profile, payload, modelInfo)
	if err != nil {
		return RunResult{}, &RunError{Reason: "run_create_failed", Retryable: true, Cause: err}
	}
	state := &runState{
		item:             item,
		payload:          payload,
		profile:          profile,
		run:              run,
		model:            modelInfo,
		usage:            map[string]int{},
		conversationKind: conversationKind,
		team:             teamForProfile(profileKey),
		resourceBudget:   r.resourceBudget(item, profile),
		startedAt:        run.StartedAt,
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

	images, err := r.loadTriggerImages(runCtx, imageRefs)
	if err != nil {
		return r.fail(runCtx, state, err)
	}
	state.images = images

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
	return r.finish(state, agent.AgentRunDone, "", false, Failure{})
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

func (r *Runner) createRun(ctx context.Context, item agent.WorkItem, profile agent.AgentProfile, payload WorkItemPayload, model llmapp.ModelInfo) (agent.AgentRun, error) {
	run, err := r.buildRun(item, profile, payload, model)
	if err != nil {
		return agent.AgentRun{}, err
	}
	stored, err := r.repo.CreateRun(ctx, run)
	if err != nil {
		return agent.AgentRun{}, err
	}
	return stored, nil
}

// buildRun builds the immutable part of one agent_run attempt. The caller owns
// the status: a dispatch-ready run is running, a pre-dispatch failure is
// recorded with its terminal snapshot.
func (r *Runner) buildRun(item agent.WorkItem, profile agent.AgentProfile, payload WorkItemPayload, model llmapp.ModelInfo) (agent.AgentRun, error) {
	if r.ids == nil {
		return agent.AgentRun{}, errors.New("agent runner has no ID generator")
	}
	run := agent.AgentRun{
		ID:            r.ids.Next(),
		ProfileKey:    profile.Key,
		Model:         model.Model,
		ConfigID:      model.ConfigID,
		FallbackFrom:  model.FallbackFrom,
		Tokens:        map[string]int{},
		Cache:         map[string]int{},
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
	return run, nil
}

// recordPreDispatchFailure persists one failed agent_run for a conversation
// attempt that stopped before createRun (trigger-message/image read, model
// override read, or model capability resolution). The Agent center runs list is
// backed by agent_run rows; without this snapshot a conversation job that fails
// at zero steps is invisible there even though the thread shows a failed card
// (Task 75 / admin B1). The insert is best effort and never changes the run
// verdict.
func (r *Runner) recordPreDispatchFailure(ctx context.Context, item agent.WorkItem, profile agent.AgentProfile, payload WorkItemPayload, cause error) {
	if r == nil || r.repo == nil || payload.ConversationID <= 0 {
		return
	}
	run, err := r.buildRun(item, profile, payload, llmapp.ModelInfo{})
	if err != nil {
		return
	}
	run.Status = agent.AgentRunFailed
	reason := runTerminalReason(cause)
	run.TerminalReason = &reason
	finishedAt := r.now()
	run.FinishedAt = &finishedAt
	if _, err := r.repo.CreateRun(ctx, run); err != nil && r.logger != nil {
		r.logger.Warn("agent runner: record pre-dispatch failed run failed",
			"work_item_id", item.ID, "conversation_id", payload.ConversationID, "error", err)
	}
}

// runTerminalReason is the stable terminal_reason of a pre-dispatch failure: the
// classified RunError reason normally, and the typed platform error code when
// the run stopped at model resolution (resolveRunError keeps the generic
// ai_unavailable reason). Never empty.
func runTerminalReason(err error) string {
	var runErr *RunError
	if errors.As(err, &runErr) {
		if reason := strings.TrimSpace(runErr.Reason); reason != "" && reason != "ai_unavailable" {
			return reason
		}
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		if code := strings.TrimSpace(apiErr.Code); code != "" {
			return code
		}
	}
	if runErr != nil && strings.TrimSpace(runErr.Reason) != "" {
		return strings.TrimSpace(runErr.Reason)
	}
	return "model_error"
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
	// P7 knowledge injection: approved knowledge visible to the version (global
	// items plus items matching the version's SUT families). A retrieval
	// failure is log-only supplemental context, never a run failure.
	versionPack.Knowledge = r.knowledgeForVersion(ctx, versionPack)
	state.input.VersionPack = versionPack

	if state.payload.ConversationID > 0 {
		if r.conversation == nil {
			return errors.New("agent runner has no conversation port")
		}
		snapshot, err := r.conversation.Snapshot(ctx, state.item.VersionID, state.payload.ConversationID, r.intSetting("context.keep_recent_messages", DefaultKeepRecentMessages))
		if err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		messages := conversationMessages(snapshot.Messages)
		if isDesignAuthoringWorkType(state.item.Type) {
			// Task 79: a background design author works from the work-item scope,
			// requirement.read and design.graph_read. Replaying the shared design
			// thread would replay every previous author's UI projection and grow
			// the prompt with the round; keep only the human instructions.
			messages = designAuthoringHistory(messages)
		}
		state.input.Conversation = ConversationContext{
			Summary:        snapshot.Summary,
			PinnedFacts:    snapshot.PinnedFacts,
			RecentMessages: messages,
		}
	}
	state.input.TeamRoom = r.teamRoomContext(ctx, state.item.VersionID, state.team)
	if isDesignAuthoringWorkType(state.item.Type) {
		// The team room is where every author projects its own result: replaying
		// the full post bodies turns the shared room into a second unbounded
		// prompt. Keep the digest and only the newest, clipped posts.
		state.input.TeamRoom = boundDesignAuthoringTeamRoom(state.input.TeamRoom)
	}

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
			// A deterministic case-scope rejection (unresolvable step target,
			// stale/superseded case, missing SUT binding) is a blocked case, not
			// an automation failure: the execution context persists blocked with
			// the concrete reason. Transient provider failures stay tool errors.
			reason := "tool_failure"
			if isCaseBlockedTaskError(taskErr) {
				reason = "case_blocked"
			}
			return &RunError{
				Reason:    reason,
				Retryable: executionTaskErrorRetryable(taskErr),
				Cause:     taskErr,
			}
		}
		state.input.Execution = &executionTask
	}

	// The model was resolved before the run row was created; the context packet
	// only needs its budget metadata here.
	state.input.ModelBudget = ModelBudget{ContextWindow: state.model.ContextWindow, MaxOutput: state.model.MaxOutput}
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

	// P5/P7 compose the P1–P4 registry with the codeintel/visual/knowledge tool
	// groups; the profile allowed_tools still select the surface each run may
	// call, and R51 intersects that surface with the profile role's whitelist.
	state.registry = tools.NewP7Registry(r.toolDepsForRun(state))
	state.roleTools, state.roleGuard, state.roleMapped = roleToolSurface(state.profile, r.audit)
	state.definitions = state.roleTools.Definitions(state.registry)

	// Knowledge is supplemental reference data: cap it at the injection budget
	// before the assembler renders the pinned facts (contract §10.3).
	knowledgeBudget := DefaultKnowledgeBudgetTokens
	if r.builder != nil {
		knowledgeBudget = r.builder.knowledgeBudgetTokens(ctx)
	}
	state.input.VersionPack.Knowledge = fitKnowledge(state.input.VersionPack.Knowledge, knowledgeBudget)

	// The current user turn is rendered before the assembly: the window_full
	// threshold accounts for it (Task 80 moved the per-work-item task context
	// into this turn, outside the assembler packet), and it is the exact text
	// handleOverflow re-appends after a boundary rebuild.
	state.userContent = r.userMessage(ctx, state, attachmentText)

	// Task 48 cache-first prompt: the assembler renders the byte-stable prefix
	// and the append-only suffix once per run (and again only at a declared
	// boundary rebuild). Compaction never happens on a normal round.
	assembly, err := r.assembleContext(ctx, state, nil, agentcontext.InvalidationNone)
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.contextPacket = assembly.Packet
	state.budget = assembly.Budget
	saved, err := r.repo.SaveContextPacket(ctx, EncodeContextPacket(state.run.ID, state.item.VersionID, assembly.Packet))
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.packet = saved
	state.run.ContextPacketID = &saved.ID
	// A boundary compaction persists its rolling summary and emits the
	// compaction event; an append-only round writes neither.
	if assembly.Compacted {
		r.recordContextCompaction(ctx, state, assembly.Packet, assembly.BeforeTokens, assembly.AfterTokens, true)
	}
	// knowledge_usage is written once per run from the persisted packet; a
	// failure is logged and never fails the run (contracts §10.3).
	r.recordKnowledgeUsage(ctx, state)

	systemText := systemMessageText(assembly.Packet, state.input)
	if err := r.appendTranscript(ctx, state, agent.MessageRoleSystem, systemText, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	if err := r.appendTranscript(ctx, state, agent.MessageRoleUser, state.userContent, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.messages = append(assembly.Messages, currentUserChatMessage(state, state.userContent))
	return nil
}

func (r *Runner) loop(ctx context.Context, state *runState) (string, error) {
	maxTurns := r.maxTurns(state.profile)
	for state.turns < maxTurns {
		if err := ctx.Err(); err != nil {
			return "", contextRunError(err)
		}
		r.publishActivity(state, "thinking", "", "")
		// Task 9 budget seam (spec §5.7): completion is structural (a reply
		// without tool calls ends the run), while tokens/time/turns are decided
		// here. A hit budget asks for one settlement turn (partial artifacts +
		// summary) instead of a hard stop; a settlement turn that is still over
		// budget is exhausted.
		decision := state.resourceBudget.Decide(Usage{
			Tokens:   usageTokens(state.usage),
			Seconds:  r.elapsedSeconds(state),
			Turns:    state.turns,
			Settling: state.softNoted,
		})
		if decision == BudgetExhaust {
			return "", &RunError{
				Reason:    "budget",
				Retryable: false,
				Cause:     errors.New("agent run exhausted its budget during the settlement turn"),
			}
		}
		soft := decision == BudgetSettle
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

		turnStarted := r.now()
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
			// A protocol/capability rejection cannot succeed on a later work-item
			// attempt: the model is never switched automatically (D24), so the
			// run fails after the single upstream call and the user is asked to
			// check the capability/protocol (spec §5.6).
			return "", &RunError{Reason: "model_error", Retryable: !llmapp.IsProtocol(err), Cause: err}
		}
		// 0.2.1 thinking part: the provider's reasoning text is shown collapsed
		// in the thread ("思考了 Ns"). The duration is the model turn; secrets
		// are redacted and the text is bounded before it can reach the UI.
		if reasoning := strings.TrimSpace(response.ReasoningContent); reasoning != "" {
			state.appendStreamPart(ConversationPart{
				Type: "thinking",
				Text: redactPartText(reasoning, producedPartThinkingMax),
				Data: map[string]any{"duration_ms": partDurationMS(turnStarted, r.now())},
			})
		}
		state.turns++
		mergeUsage(state.usage, response.Usage)
		// Contract §18.2.6: the provider usage is authoritative for the prompt
		// cache bookkeeping; the assembler's local estimate is overwritten.
		r.applyProviderCacheStats(ctx, state, response.Usage)
		// 0.2.1 job.progress: every completed model turn is a real step of the
		// work item. It goes through the worker's per-job coalescer.
		r.reportJobProgress(ctx, state)

		if err := r.persistAssistant(ctx, state, response); err != nil {
			return "", err
		}
		state.messages = append(state.messages, llmapp.ChatMessage{
			Role:             "assistant",
			Content:          response.Content,
			ToolCalls:        response.ToolCalls,
			ReasoningContent: response.ReasoningContent,
			ReasoningItems:   response.ReasoningItems,
		})

		if len(response.ToolCalls) == 0 {
			return strings.TrimSpace(response.Content), nil
		}
		if soft {
			// Tools are disabled during settlement; a tool-call-only reply means
			// the model ignored the wrap-up instruction. This is a budget
			// exhaustion (the settlement turn is consumed without an answer),
			// not a transient model error: another attempt cannot succeed.
			if text := strings.TrimSpace(response.Content); text != "" {
				return text, nil
			}
			return "", &RunError{
				Reason:    "budget",
				Retryable: false,
				Cause:     errors.New("model requested tools during the settlement turn"),
			}
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
		callStarted := r.now()
		var payload any
		var err error
		switch {
		case !profileAllowsTool(state.profile, call.Name):
			err = tools.Invalid("tool", "tool is not allowed for this profile: "+call.Name)
		case state.roleGuard != nil:
			// R51: the runner intersects the profile role's whitelist even when
			// a stored profile was widened outside it. A denial is a typed
			// forbidden error and an audit event, never a tool execution.
			payload, err = state.roleGuard.Execute(ctx, state.registry, scope, call.Name, json.RawMessage(call.Arguments))
		case !state.roleMapped:
			// Unmapped profile: the profile whitelist was the only surface and
			// it already passed the first case.
			payload, err = state.registry.Execute(ctx, scope, call.Name, json.RawMessage(call.Arguments))
		case !state.roleTools.Allows(call.Name):
			err = tools.Forbidden(state.roleTools.Role(), call.Name)
		default:
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

		// 0.2.1 tool_call/error parts: the UI pairs streamed rows by call_id and
		// shows the tool name/status/duration/result collapsed. Arguments and
		// results are sanitized (credentials redacted, values bounded) before
		// they can reach the conversation thread or its SSE event.
		r.appendToolParts(state, call, content, err, partDurationMS(callStarted, r.now()))

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

// handleOverflow compacts the context at a declared window_full boundary and
// retries. The first retry folds the transcript down to the configured recent
// window. Rebuilding from the same history with the same window cannot shrink
// the prompt again, so the second (last) retry halves the window and folds
// strictly more history; a configured window of 0 is already the minimum, so
// only the pinned prefix/facts remain and a third overflow is unrecoverable.
// context_overflow alone never terminates the run.
func (r *Runner) handleOverflow(ctx context.Context, state *runState) error {
	state.overflowHits++
	if state.overflowHits > 2 {
		return &RunError{
			Reason:    "context_overflow_unrecoverable",
			Retryable: true,
			Cause:     errors.New("model rejected the context window even after compaction"),
		}
	}
	keepRecent := r.keepRecentMessages(ctx)
	if state.overflowHits > 1 {
		keepRecent = shrinkKeepRecentMessages(keepRecent)
	}
	previous := state.contextPacket
	assembly, err := r.assembleContextWithKeepRecent(ctx, state, &previous, agentcontext.InvalidationWindowFull, keepRecent)
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.contextPacket = assembly.Packet
	saved, err := r.repo.SaveContextPacket(ctx, EncodeContextPacket(state.run.ID, state.item.VersionID, assembly.Packet))
	if err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	state.packet = saved
	state.run.ContextPacketID = &saved.ID
	// A boundary rebuild persists a new packet row; carry the provider cache
	// bookkeeping observed on an earlier turn so the latest row stays
	// authoritative (the context.cache_stats event is still emitted once).
	if state.providerCacheReported {
		state.contextPacket.CachedTokens = state.providerCacheTokens
		state.contextPacket.CacheHit = state.providerCacheHit
		if state.packet.TokenCounts == nil {
			state.packet.TokenCounts = map[string]int{}
		}
		state.packet.TokenCounts["cached_tokens"] = state.providerCacheTokens
		state.packet.TokenCounts["cache_hit"] = boolInt(state.providerCacheHit)
		if err := r.repo.UpdateContextPacketCacheStats(ctx, saved.ID,
			state.providerCacheTokens, state.providerCacheHit); err != nil {
			r.logContextWarning(ctx, "carry provider cache stats to the compacted packet failed", err)
		}
	}
	state.messages = append(assembly.Messages, currentUserChatMessage(state, state.userContent))
	systemText := systemMessageText(assembly.Packet, state.input)
	if err := r.appendTranscript(ctx, state, agent.MessageRoleSystem, systemText, nil, nil); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	r.recordContextCompaction(ctx, state, assembly.Packet, assembly.BeforeTokens, assembly.AfterTokens, assembly.Compacted)
	return nil
}

// shrinkKeepRecentMessages halves the recent window for the last overflow retry
// so the second compaction folds strictly more history than the first. A window
// of 0 or 1 shrinks to 0: every compactable segment is folded and only the
// pinned prefix/facts and the rolling summary remain.
func shrinkKeepRecentMessages(keepRecent int) int {
	if keepRecent <= 1 {
		return 0
	}
	return keepRecent / 2
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
			Parts:            r.assistantConversationParts(state, finalText, nil),
		}); err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		r.convergeClarification(ctx, state, finalText)
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
			Parts:            r.assistantConversationParts(state, finalText, nil),
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
			Parts:          r.assistantConversationParts(state, finalText, nil),
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
		// A case-author task enqueued by a design round carries the round's
		// conversation/round association: persist the proposal trace and the
		// summary there so the design thread shows the case.graph_propose call
		// with its evidence (Task 65), alongside the team-room update. A task
		// without the round association keeps the frozen P2 behavior (team room
		// only) and never writes into a conversation.
		if r.conversation != nil && state.payload.ConversationID > 0 && state.payload.RoundID > 0 {
			if _, err := r.conversation.AppendAssistantMessage(ctx, AssistantMessageInput{
				VersionID:        state.item.VersionID,
				ConversationID:   state.payload.ConversationID,
				AgentRunID:       state.run.ID,
				ReplyToMessageID: replyTarget(state.payload.MessageID),
				Parts:            r.assistantConversationParts(state, finalText, nil),
			}); err != nil {
				return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
			}
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
		parts := r.assistantConversationParts(state, finalText, state.cards)
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
				Parts:            r.assistantConversationParts(state, finalText, nil),
			}); err != nil {
				return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
			}
		}
	}
	return nil
}

// convergeClarification applies the clarifying→requirement_review edge when the
// host's persisted turn explicitly requests requirement confirmation. Stage
// failures are log-only: the assistant message is the source artifact and a
// later confirmation turn retries the convergence instead of failing the chat
// turn (which would re-run the model).
func (r *Runner) convergeClarification(ctx context.Context, state *runState, finalText string) {
	if r.clarification == nil || state == nil || state.item.VersionID <= 0 {
		return
	}
	if !requestsRequirementConfirmation(finalText) {
		return
	}
	if err := r.clarification.Converge(ctx, state.item.VersionID); err != nil && r.logger != nil {
		r.logger.Warn("clarification convergence failed",
			"version_id", state.item.VersionID, "error", err)
	}
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
	// Task 9 seam: Classify is the contract-facing failure taxonomy (kind/
	// retryable/user-visible/action). The run's own status stays driven by the
	// RunError reason so lease/cancel semantics keep their existing behavior;
	// the classification is attached to the failed event for the UI (spec §5.6).
	failure := Classify(err)
	status := agent.AgentRunFailed
	if reason == "interrupted" || reason == "aborted" {
		status = agent.AgentRunInterrupted
	}
	result, finishErr := r.finish(state, status, reason, retryable, failure)
	if finishErr != nil {
		return result, finishErr
	}
	return result, &RunError{Reason: reason, Retryable: retryable, Cause: err}
}

func (r *Runner) finish(state *runState, status agent.AgentRunStatus, reason string, retryable bool, failure Failure) (RunResult, error) {
	finishedAt := r.now()
	state.run.Status = status
	state.run.Turns = state.turns
	state.run.Usage = state.usage
	// Tokens mirrors the aggregated usage into the 0.2.1 token-accounting column;
	// cache stays a persisted (possibly empty) object until a provider reports
	// prompt-cache statistics (spec §7.5).
	state.run.Tokens = copyUsage(state.usage)
	if state.run.Cache == nil {
		state.run.Cache = map[string]int{}
	}
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
		if failure.Kind != "" {
			payload["failure_kind"] = string(failure.Kind)
			payload["failure_action"] = failure.Action
			payload["failure_user_visible"] = failure.UserVisible
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
				Type: state.item.Type,
				Goal: "作为设计 Host 与用户对话，读取脑图并按用户要求提交/修改场景与用例 draft。",
				Constraints: []string{
					"AI 只能提交 draft/候选，不能确认、删除或关闭用例", "确认只属于用户", "不编造需求未表达的内容",
					"收到修改请求时先 design.graph_read 读取目标用例一次，然后在同一轮内立即用 case.graph_propose 提交完整修改；不要只回复修改计划、不要等待用户二次授权",
					"每条步骤与断言的 expected 必须是非空的可观察结果；修改时补齐空 expected",
					"页面/API 来源只能引用版本已绑定的 page/api ID；未绑定时只用 requirement/knowledge 来源，不要编造 ref_id",
				},
				AcceptanceCriteria: []string{"引用用户最新消息", "修改脑图前先 design.graph_read", "同一轮内用 case.graph_propose 提交草案或修改并说明变化"},
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
		roundRequirementIDs, _ := ParseWorkItemIDList(state.item.Payload, "requirement_ids")
		roundRequirementVersionIDs, _ := ParseWorkItemIDList(state.item.Payload, "requirement_version_ids")
		roundScope := append([]string(nil), scope...)
		if len(roundRequirementIDs) > 0 {
			roundScope = append(roundScope, "requirement_ids="+joinIDText(roundRequirementIDs))
		}
		if len(roundRequirementVersionIDs) > 0 {
			roundScope = append(roundScope, "requirement_version_ids="+joinIDText(roundRequirementVersionIDs))
		}
		return TaskContext{
			Type: state.item.Type,
			Goal: "读取一次设计脑图后，立即用 case.graph_propose 为本轮范围内每条已确认需求提交场景与用例草案；不要询问用户、不要等待用户选择。",
			Constraints: []string{
				"只提交 draft/候选，不能确认、删除、关闭用例或需求",
				"只做一次 design.graph_read 获取本轮需求的 requirement_version_id 与发现事实，随后立即用 case.graph_propose 提交，不要用重复只读调用耗尽预算",
				"来源 sources.kind=requirement 的 ref_id 必须是 requirement_version_id（当前需求版本 ID），不能写 requirement_id",
				"页面/API 来源只能引用版本已绑定的 page/api ID；未绑定时只用 requirement/knowledge 来源，不要编造 ref_id",
				"场景名用需求标题原文、用例标题用对应验收要点原文，降低与并发用例作者重复的概率；同一自然键已存在时更新而不是重复创建",
				"用例必须包含可执行步骤、断言、来源与目标 version_sut；每条步骤与断言的 expected 必须是非空的可观察结果；不编造需求未表达的内容",
			},
			AcceptanceCriteria: []string{
				"对本轮每条已确认需求至少提交一次 case.graph_propose，并返回 case_ids/case_version_ids",
				"提交后简要说明新增/更新的场景与用例数量",
				"不向用户提问、不等待用户选择",
			},
			Scope: roundScope,
		}
	case WorkTypeDesignRequirement, WorkTypeDesignGap:
		goal := "为指定需求版本设计场景与用例草案：先读取脑图，再用 case.graph_propose 提交 1 个场景与至少 1 条可执行用例。"
		if state.item.Type == WorkTypeDesignGap {
			goal = "根据执行/设计缺口补充用例草案：先读取脑图，再用 case.graph_propose 新增或修改 draft 用例。"
		}
		return TaskContext{
			Type: state.item.Type,
			Goal: goal,
			Constraints: []string{
				"只提交 draft/候选，不能确认、删除或关闭用例",
				"用例必须包含步骤、断言、来源与目标 version_sut；每条步骤与断言的 expected 必须是非空的可观察结果",
				"同一需求版本内更新已有用例时传既有 ID/自然键",
				"只做一次 design.graph_read 获取 requirement_version_id 与发现事实，随后立即用 case.graph_propose 提交，不要用重复只读调用耗尽预算",
				"来源 sources.kind=requirement 的 ref_id 必须是 requirement_version_id（当前需求版本 ID），不能写 requirement_id；页面/API 来源只能引用版本已绑定的 page/api ID，未绑定时不要编造",
				"场景名用需求标题原文、用例标题用对应验收要点原文，降低与同轮设计重复的概率；同一自然键已存在时更新而不是重复创建",
			},
			AcceptanceCriteria: []string{
				"design.graph_read 后再提交",
				"case.graph_propose 返回 case_version_ids 或 case_ids",
				"向设计团队频道说明变化",
			},
			Scope: scope,
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
				"失败/阻塞/不确定的断言要留下证据（截图、console/network 或 execution.evidence）",
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
	case agent.WorkItemKindExploreLogin, agent.WorkItemKindExplorePage, agent.WorkItemKindExploreAPI,
		agent.WorkItemKindExploreCoverage, agent.WorkItemKindExploreGap:
		return TaskContext{
			Type:               state.item.Type,
			Goal:               "执行当前 explore 任务：读取版本上下文与已有发现事实，用 explore.* 工具补齐可复现的页面/接口/登录事实并说明证据来源。",
			Constraints:        []string{"只读 SUT 与版本事实，不生成/确认用例，不修改需求", "不编造事实；无法完成时说明明确原因", "记录证据引用，不写入凭据"},
			AcceptanceCriteria: []string{"产生或补充可复现的发现事实", "说明证据来源与下一步"},
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

// userMessage is the current user turn of the run: the per-work-item task
// context followed by the profile's triggering user content. Task 80: the task
// context is per-work-item data, so it belongs to the append-only user suffix,
// not the cache-stable system prefix; rendering it here keeps goal, constraints,
// acceptance and scope in front of the model without breaking the shared prefix.
func (r *Runner) userMessage(ctx context.Context, state *runState, attachmentText string) string {
	return taskContextMessage(r.baseUserMessage(ctx, state, attachmentText), state.input)
}

// baseUserMessage renders the triggering user content without the per-work-item
// task context block.
func (r *Runner) baseUserMessage(ctx context.Context, state *runState, attachmentText string) string {
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
			return "请根据最近的设计对话继续完善用例脑图。需要修改现有用例时：先 design.graph_read 读取目标用例一次，然后在同一轮内立即用 case.graph_propose 提交完整修改（保留未变更内容、补齐所有 expected），不要只回复修改计划、不要等待二次授权。"
		}
		return "请基于最近对话继续澄清需求。"
	case WorkTypeDesignRound:
		if requirementIDs, _ := ParseWorkItemIDList(state.item.Payload, "requirement_ids"); len(requirementIDs) > 0 {
			return fmt.Sprintf("本轮设计范围已确定（%d 条已确认需求，requirement_ids=%s）。请只读取一次 design.graph_read 获取 requirement_version_id 与发现事实，然后立即用 case.graph_propose 为每条需求提交场景与用例 draft；不要询问用户、不要等待用户选择，提交后简要说明结果。",
				len(requirementIDs), joinIDText(requirementIDs))
		}
		return "本轮设计范围已确定。请只读取一次 design.graph_read 获取 requirement_version_id 与发现事实，然后立即用 case.graph_propose 为每条已确认需求提交场景与用例 draft；不要询问用户、不要等待用户选择，提交后简要说明结果。"
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
	case agent.WorkItemKindExploreLogin, agent.WorkItemKindExplorePage, agent.WorkItemKindExploreAPI,
		agent.WorkItemKindExploreCoverage, agent.WorkItemKindExploreGap:
		return "请执行当前 explore 任务，读取版本上下文与已有事实，补齐可复现的页面/接口/登录发现并说明证据来源。"
	default:
		return "请根据当前任务继续执行。"
	}
}

// triggerImageRefs returns the content data_refs of the image parts on the
// user message that triggered this run. Only chat.turn/requirement.curate read
// the triggering message; background runs (execution, reports) never attach
// conversation images. Refs are deduplicated and keep message order.
func (r *Runner) triggerImageRefs(ctx context.Context, item agent.WorkItem, payload WorkItemPayload) ([]string, error) {
	switch item.Type {
	case WorkTypeChatTurn, WorkTypeRequirementCurate:
	default:
		return nil, nil
	}
	if payload.MessageID <= 0 || payload.ConversationID <= 0 || r.conversation == nil {
		return nil, nil
	}
	message, err := r.conversation.GetMessage(ctx, item.VersionID, payload.ConversationID, payload.MessageID)
	if err != nil {
		return nil, &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	refs := make([]string, 0, len(message.Parts))
	seen := make(map[string]struct{}, len(message.Parts))
	for _, part := range message.Parts {
		if part.Type != "image" {
			continue
		}
		ref := imagePartDataRef(part)
		if ref == "" {
			continue
		}
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs, nil
}

// imagePartDataRef extracts the content data_ref of one image conversation
// part. The version adapter maps the R5 image payload into Data.
func imagePartDataRef(part ConversationPart) string {
	if part.Data == nil {
		return ""
	}
	value, ok := part.Data["data_ref"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// loadTriggerImages resolves the triggering message's image refs into bytes for
// the LLM request. A missing loader or an unreadable image fails the run: the
// model must never silently miss an image the user sent.
func (r *Runner) loadTriggerImages(ctx context.Context, refs []string) ([]llmapp.ChatImage, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if r.images == nil {
		return nil, &RunError{
			Reason:    "tool_failure",
			Retryable: false,
			Cause:     errors.New("agent runner has no image loader"),
		}
	}
	images := make([]llmapp.ChatImage, 0, len(refs))
	for _, ref := range refs {
		image, err := r.images.LoadImage(ctx, ref)
		if err != nil {
			return nil, &RunError{Reason: "tool_failure", Retryable: imageLoadErrorRetryable(err), Cause: err}
		}
		if len(image.Data) == 0 {
			return nil, &RunError{
				Reason:    "tool_failure",
				Retryable: true,
				Cause:     fmt.Errorf("image %s resolved to no bytes", ref),
			}
		}
		images = append(images, llmapp.ChatImage{MIME: image.MIME, Data: image.Data})
	}
	return images, nil
}

// imageLoadErrorRetryable classifies an image resolution failure: a 4xx means
// the stored reference is bad (non-retryable unless it says otherwise), while
// transport/storage failures are retried.
func imageLoadErrorRetryable(err error) bool {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
		return apiErr.Retryable
	}
	return true
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

// reportJobProgress advances the job card with the completed model turns of
// this run: the conversation snapshot is rewritten through the conversation
// port (one card per job) and, when the worker attached its per-job coalescer,
// the same counter is published as a coalesced job.progress event. total stays
// 0 ("unknown") so the UI never derives a fabricated percentage
// (contracts/events.md §18.2 #4).
func (r *Runner) reportJobProgress(ctx context.Context, state *runState) {
	if state == nil {
		return
	}
	r.projectJobCard(ctx, state.item, state.payload, "running", state.turns, jobCardFailure{})
	emitter := jobEventsFromContext(ctx)
	if emitter == nil {
		return
	}
	emitter.Progress(state.item.VersionID, eventingapp.JobProgress{
		JobID:   state.item.ID,
		Kind:    jobKind(state.item),
		Current: state.turns,
		Total:   0,
		Status:  eventingapp.JobStatusRunning,
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

// DesignRunMaxTokens is the case-scale token budget of the design team. Usage is
// cumulative across the turns of one run and every turn re-sends the design
// thread plus the graph read, so the generic 20k chat budget was exhausted after
// the read in the final S4 run: the following settlement turn disabled tools and
// case.graph_propose could never write the modification. 200k covers a full
// round (read + one propose per activated requirement) without being unbounded.
const DesignRunMaxTokens = 200000

// Design authoring envelope (Task 79). The stored profile budgets cannot be
// trusted for design runs: profiles are seeded once and EnsureProfiles never
// overwrites an existing row, so the live 0.2.1 database still carries the
// chat-scale design budgets (case_author 8/300/600, design_planner 12/300/600)
// even after the seeds were raised. The runner therefore enforces the design
// authoring floor in code, like the token budget: a focused case-author run
// needs the read turn, the propose turn and the summary turn, and must not be
// cut off by the generic chat-scale soft timeout.
const (
	DesignRunMaxTurns       = 16
	DesignRunMinSeconds     = 600
	DesignRunMinHardSeconds = 900
	DesignRunMinAuthorTurns = 3
	// DesignAuthorTurnTokens is the conservative per-turn token envelope of one
	// focused case-author run after the Task 79 context scoping: profile prompt,
	// version briefing/facts, one design.graph_read and the bounded
	// human-history/team-room tail.
	DesignAuthorTurnTokens = 32000
)

func (r *Runner) maxTurns(profile agent.AgentProfile) int {
	limit := profile.Budgets.MaxTurns
	setting := r.intSetting("agent.max_turns", defaultMaxTurns)
	// P3 execution profiles carry their own case-scale turn budget; the design
	// team gets the authoring floor so a stored chat-scale profile value cannot
	// truncate a 67-requirement round. The generic agent.max_turns (12) is a
	// chat-scale guard and must not clamp either team.
	if isP3Profile(profile.Key) {
		if limit <= 0 {
			limit = setting
		}
	} else if isDesignProfile(profile.Key) {
		if limit < DesignRunMaxTurns {
			limit = DesignRunMaxTurns
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

// isDesignProfile reports whether the profile belongs to the design team. Design
// runs read the whole graph, write case drafts and run in one lasting round, so
// they use the design-scale token budget and are not clamped by agent.max_turns.
func isDesignProfile(key string) bool {
	switch key {
	case ProfileKeyDesignHost, ProfileKeyDesignPlanner, ProfileKeyCaseAuthor,
		ProfileKeyDesignCritic, ProfileKeyDataPlanner:
		return true
	default:
		return false
	}
}

// turnFuse is the turn-circuit-breaker value used by the default resource
// budget. It is one below the loop cap so the final allowed turn is the
// tool-less settlement turn: the loop can still execute it, Decide sees
// Turns >= fuse and asks for settlement instead of the loop silently ending
// with a hard budget error. A degenerate one-turn cap keeps fuse=1.
func (r *Runner) turnFuse(profile agent.AgentProfile) int {
	fuse := r.maxTurns(profile) - 1
	if fuse < 1 {
		fuse = 1
	}
	return fuse
}

func (r *Runner) softSeconds(profile agent.AgentProfile) int {
	seconds := profile.Budgets.SoftSeconds
	if seconds <= 0 {
		seconds = r.intSetting("agent.run_soft_timeout_seconds", defaultSoftSeconds)
	}
	if seconds < 1 {
		seconds = defaultSoftSeconds
	}
	if isDesignProfile(profile.Key) && seconds < DesignRunMinSeconds {
		seconds = DesignRunMinSeconds
	}
	return seconds
}

// resourceBudget maps the work item's 0.2.1 budget (spec §5.4: max_tokens /
// run_seconds / max_turns_fuse) onto the Task 9 Budget policy. Legacy P1-P8
// items carry no Budget JSON and fall back to the frozen profile limits; the
// token default is the contract default when neither source sets it.
func (r *Runner) resourceBudget(item agent.WorkItem, profile agent.AgentProfile) Budget {
	budget := Budget{
		MaxTokens:  DefaultBudgetMaxTokens,
		RunSeconds: r.softSeconds(profile),
		// Reserve the last loop turn for settlement. With the fuse equal to the
		// loop cap the loop exits before Decide can observe the fuse, so the
		// budget policy would never get to ask for a wrap-up turn.
		MaxTurnsFuse: r.turnFuse(profile),
	}
	if isDesignProfile(profile.Key) {
		budget.MaxTokens = DesignRunMaxTokens
		// At least the read/propose/summary turns of one focused author run must
		// fit inside the design token budget.
		if minimum := DesignRunMinAuthorTurns * DesignAuthorTurnTokens; budget.MaxTokens < minimum {
			budget.MaxTokens = minimum
		}
	}
	trimmed := strings.TrimSpace(string(item.Budget))
	if trimmed == "" || trimmed == "null" {
		return budget
	}
	var declared struct {
		MaxTokens    int `json:"max_tokens"`
		RunSeconds   int `json:"run_seconds"`
		MaxTurnsFuse int `json:"max_turns_fuse"`
	}
	if err := json.Unmarshal([]byte(trimmed), &declared); err != nil {
		return budget
	}
	if declared.MaxTokens > 0 {
		budget.MaxTokens = declared.MaxTokens
	}
	if declared.RunSeconds > 0 {
		budget.RunSeconds = declared.RunSeconds
	}
	if declared.MaxTurnsFuse > 0 {
		budget.MaxTurnsFuse = declared.MaxTurnsFuse
	}
	return budget
}

// elapsedSeconds is the wall-clock usage of the run at the decision point. The
// manual test clock advances it, matching the pre-Task-9 soft-timeout seam.
func (r *Runner) elapsedSeconds(state *runState) int {
	if state == nil || state.startedAt.IsZero() {
		return 0
	}
	seconds := int(r.now().Sub(state.startedAt).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

// usageTokens returns the aggregated token usage the budget compares against.
// Providers report total_tokens; a provider that only reports the components is
// summed instead of treated as zero usage. A present-but-zero total_tokens is
// treated as "not reported" because some providers emit it alongside a real
// component breakdown.
func usageTokens(usage map[string]int) int {
	if total, ok := usage["total_tokens"]; ok && total > 0 {
		return total
	}
	total := 0
	for key, value := range usage {
		if key == "total_tokens" || value <= 0 {
			continue
		}
		total += value
	}
	return total
}

// roleToolSurface intersects the profile's allowed tools with the catalog role
// whitelist (R51). A mapped profile executes through a RoleToolGuard, so a
// denied call is a typed forbidden error plus an audit event. Profiles outside
// the seven roles (case_executor, coverage_analyst, version_steward) are not
// constrained by this catalog and keep their own frozen contract whitelist.
func roleToolSurface(profile agent.AgentProfile, audit auditapp.Recorder) (tools.ToolSet, *RoleToolGuard, bool) {
	spec, ok := RoleForProfile(profile.Key)
	if !ok {
		return tools.NewToolSet("", profile.AllowedTools), nil, false
	}
	roleSet, ok := RoleToolSet(spec.Key)
	if !ok {
		// Unknown role is a configuration error: fail closed by exposing
		// nothing rather than granting the profile set.
		return tools.NewToolSet("", nil), nil, true
	}
	names := make([]string, 0, len(profile.AllowedTools))
	for _, name := range profile.AllowedTools {
		if roleSet.Allows(name) {
			names = append(names, name)
		}
	}
	guard, err := NewRoleToolGuard(spec.Key, audit)
	if err != nil {
		return tools.NewToolSet(string(spec.Key), names), nil, true
	}
	return tools.NewToolSet(string(spec.Key), names), guard, true
}

func (r *Runner) hardSeconds(profile agent.AgentProfile) int {
	hard := profile.Budgets.HardSeconds
	if hard <= 0 {
		hard = r.intSetting("agent.run_hard_timeout_seconds", defaultHardSeconds)
	}
	if isDesignProfile(profile.Key) && hard < DesignRunMinHardSeconds {
		hard = DesignRunMinHardSeconds
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

// modelRequirements derives the capabilities a profile's run needs before
// dispatch. Profiles that may call tools need a tools-capable model; the
// reasoning requirement is derived from the config's thinking mode by the
// gateway itself. Vision is appended by Run when the triggering message has
// image parts, so ResolveModel is the single vision gate.
func modelRequirements(profile agent.AgentProfile) []llmapp.ModelRequirements {
	if len(profile.AllowedTools) == 0 {
		return nil
	}
	return []llmapp.ModelRequirements{{Tools: true}}
}

// resolveRunError classifies a model-resolution failure. Configuration and
// capability errors are non-retryable (an operator must fix the config, D24);
// an unavailable gateway stays retryable like before.
func resolveRunError(err error) *RunError {
	retryable := true
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		retryable = apiErr.Retryable
	}
	return &RunError{Reason: "ai_unavailable", Retryable: retryable, Cause: err}
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
		"knowledge":    knowledgeRefsResult(pack.Knowledge),
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

// isCaseBlockedTaskError reports whether an execution task-provider rejection is
// a deterministic case-scope blocker rather than a transient infrastructure
// error. The P3 execution context classifies a blocked run as
// blocked/failure_class=blocked and persists the reason as evidence; treating
// the same rejection as a tool_failure produced the silent zero-result
// agent_failure rows in QA-F1.
func isCaseBlockedTaskError(err error) bool {
	if IsLeaseLost(err) {
		return false
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict:
			return true
		}
	}
	return false
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

func mergeUsage(target map[string]int, addition map[string]int) {
	for key, value := range addition {
		target[key] += value
	}
}

// copyUsage returns a non-nil copy so the persisted token snapshot cannot be
// mutated by a later usage merge.
func copyUsage(source map[string]int) map[string]int {
	target := make(map[string]int, len(source))
	for key, value := range source {
		target[key] = value
	}
	return target
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

// joinIDText renders a decimal ID list for the task context scope. It is used by
// the design round scope (Task 69); the ids remain identifiers only.
func joinIDText(ids []int64) string {
	formatted := make([]string, 0, len(ids))
	for _, id := range ids {
		formatted = append(formatted, idText(id))
	}
	return strings.Join(formatted, ",")
}

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

// ---------- 0.2.1 message parts producer ----------

// Produced part bounds. The version validator and the conversation_message_part
// payload CHECK are the last line of defense; the runner bounds and sanitizes
// before the part leaves the run so a tool result can never bloat a message or
// persist credentials.
const (
	producedPartThinkingMax = 4000
	producedPartJSONMax     = 4000
	producedPartHeadMax     = 2000
	producedPartStringMax   = 500
	producedPartMaxDepth    = 8
	maxProducedStreamParts  = 200
)

var (
	// partSecretAssignmentPattern catches a credential written into free text
	// ("password=hunter2", "token: abc"), keeping the field name for context.
	partSecretAssignmentPattern = regexp.MustCompile(
		`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|authorization|cookie|credential)s?\b\s*[:=]\s*(\S+)`)
	// partBearerPattern catches a raw bearer credential under any key.
	partBearerPattern = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]+)`)
	// partJWTPattern catches a raw JWT.
	partJWTPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
)

// appendStreamPart records one produced streaming part, bounded by the message
// limit. A dropped part never fails the run: the model reply is the artifact,
// the trace is supplementary.
func (state *runState) appendStreamPart(part ConversationPart) {
	if state == nil || len(state.streamParts) >= maxProducedStreamParts {
		return
	}
	state.streamParts = append(state.streamParts, part)
}

// hasErrorPart dedupes repeated tool validation failures so a model retrying the
// same bad call does not stack identical error cards in the thread.
func (state *runState) hasErrorPart(code, message string) bool {
	for _, part := range state.streamParts {
		if part.Type != "error" {
			continue
		}
		if stringValue(part.Data["code"]) == code && stringValue(part.Data["message"]) == message {
			return true
		}
	}
	return false
}

// appendToolParts emits the tool_call part plus, for a structured tool failure,
// one error part with the user-visible code/message. Arguments and results are
// sanitized first: credentials redacted, strings bounded, result object-shaped.
func (r *Runner) appendToolParts(state *runState, call llmapp.ToolCall, content string, toolErr error, durationMS int64) {
	callID := strings.TrimSpace(call.ID)
	if callID == "" {
		if r.ids == nil {
			return
		}
		callID = "call_" + idText(r.ids.Next())
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		return
	}
	status := "done"
	errorText := ""
	if toolErr != nil {
		status = "failed"
		errorText = redactPartText(toolErr.Error(), version.ToolCallErrorMax)
	}
	data := map[string]any{
		"call_id":     truncateRunes(callID, version.ToolCallIDMax),
		"name":        truncateRunes(name, version.ToolCallNameMax),
		"status":      status,
		"duration_ms": durationMS,
	}
	if arguments, ok := sanitizePartJSONObject(call.Arguments); ok {
		data["arguments"] = arguments
	}
	if result, ok := sanitizePartResult(content); ok {
		data["result"] = result
	}
	if errorText != "" {
		data["error"] = errorText
	}
	state.appendStreamPart(ConversationPart{Type: "tool_call", Data: data})

	var typed *tools.Error
	if toolErr == nil || !errors.As(toolErr, &typed) {
		return
	}
	code := truncateRunes(strings.TrimSpace(typed.Code), version.MessageErrorCodeMax)
	message := redactPartText(typed.Message, version.MessageErrorMessageMax)
	if code == "" || message == "" || state.hasErrorPart(code, message) {
		return
	}
	state.appendStreamPart(ConversationPart{Type: "error", Data: map[string]any{
		"code":      code,
		"message":   message,
		"retryable": false,
	}})
}

// assistantConversationParts renders the parts of one final assistant message:
// the produced trace parts (thinking/tool_call/error), the final text and the
// profile-specific extra cards. Order is the render order. The background job's
// own card is not part of this message: it lives in the dedicated card message
// written by projectJobCard, so one job never renders two job_cards.
func (r *Runner) assistantConversationParts(state *runState, finalText string, extra []ConversationPart) []ConversationPart {
	parts := make([]ConversationPart, 0, len(state.streamParts)+1+len(extra))
	parts = append(parts, state.streamParts...)
	if finalText != "" {
		parts = append(parts, ConversationPart{Type: "text", Text: finalText})
	}
	parts = append(parts, extra...)
	return parts
}

// jobCardWriteTimeout bounds the terminal card write-back, which runs after the
// run context may already be cancelled.
const jobCardWriteTimeout = 5 * time.Second

// jobCardFailure is the typed explanation persisted on a terminal failed card
// (Task 75). Code is the stable error code and Reason the user-facing message;
// the zero value means "no failure to explain".
type jobCardFailure struct {
	Code   string
	Reason string
}

// jobCardFailureDetails extracts the failure explanation of a finished run. A
// typed platform error (for example llm_capability_mismatch) supplies the stable
// code and the actionable message; otherwise the RunError classification and
// then the error text are used. Secrets are redacted and both fields are bounded
// by the MessagePart contract before they reach the conversation store.
func jobCardFailureDetails(err error) jobCardFailure {
	if err == nil {
		return jobCardFailure{}
	}
	failure := jobCardFailure{}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		failure.Code = apiErr.Code
		failure.Reason = apiErr.Message
	}
	if failure.Code == "" {
		var runErr *RunError
		if errors.As(err, &runErr) {
			failure.Code = runErr.Reason
		}
	}
	if strings.TrimSpace(failure.Reason) == "" {
		failure.Reason = err.Error()
	}
	failure.Code = truncateRunes(strings.TrimSpace(failure.Code), version.MessageErrorCodeMax)
	failure.Reason = redactPartText(strings.TrimSpace(failure.Reason), version.MessageErrorMessageMax)
	return failure
}

// projectJobCard writes the single job_card part of a background job into its
// conversation. It is a no-op when the job does not map to a card kind or the
// work item carries no conversation. The version context owns idempotency: a
// repeated start/progress/terminal projection rewrites the same card message
// instead of appending a second one. The write is best effort: a card failure
// is logged and never changes the run verdict. A terminal failed card also
// carries the typed failure code/reason so the thread explains the job.
func (r *Runner) projectJobCard(ctx context.Context, item agent.WorkItem, payload WorkItemPayload, status string, current int, failure jobCardFailure) {
	if r == nil || r.conversation == nil || item.ID <= 0 || payload.ConversationID <= 0 {
		return
	}
	kind, ok := jobKindForCard(item)
	if !ok {
		return
	}
	if current < 0 {
		current = 0
	}
	data := map[string]any{
		"job_id":     idText(item.ID),
		"kind":       kind,
		"status":     status,
		"current":    int64(current),
		"total":      int64(0),
		"cancelable": jobCardCancelable(status),
	}
	if status == "failed" {
		if failure.Code != "" {
			data["code"] = failure.Code
		}
		if failure.Reason != "" {
			data["reason"] = failure.Reason
		}
	}
	card := ConversationPart{Type: version.MessagePartJobCard, Data: data}
	if _, err := r.conversation.UpsertJobCard(ctx, item.VersionID, payload.ConversationID, card); err != nil {
		if r.logger != nil {
			r.logger.Warn("agent runner: project job card failed",
				"work_item_id", item.ID, "conversation_id", payload.ConversationID,
				"status", status, "error", err)
		}
	}
}

// jobKindForCard resolves the conversation job-card kind of a work item: the
// frozen 0.2.1 contract kind when the row carries one, otherwise the
// conversation-writing work type (chat.turn/requirement.curate/design.round).
// Anything else has no card.
func jobKindForCard(item agent.WorkItem) (string, bool) {
	kind := jobKind(item)
	if version.IsValidJobCardKind(kind) || version.IsConversationJobCardKind(kind) {
		return kind, true
	}
	return "", false
}

// jobCardCancelable mirrors the card contract: an in-flight job may be
// cancelled; a terminal card must not offer it.
func jobCardCancelable(status string) bool {
	switch status {
	case "open", "claimed", "running", "waiting_human":
		return true
	default:
		return false
	}
}

// jobCardTerminalStatus maps a finished run to the stored card status. A
// retryable failure below the attempt cap returns the job to queued (the worker
// reopens the item), matching the worker's Fail predicate; at the cap it is
// terminal failed. A hard-timeout run is interrupted+retryable, so it follows
// the same reopen/terminal mapping instead of leaving a stale running card. A
// fenced or aborted run owns no terminal card and keeps the previous projection
// (the new owner or the job-control cancel path owns it). done and failed are
// terminal.
func jobCardTerminalStatus(item agent.WorkItem, err error) (string, bool) {
	if err == nil {
		return "done", true
	}
	if IsLeaseLost(err) {
		return "", false
	}
	var runErr *RunError
	if errors.As(err, &runErr) {
		switch runErr.Reason {
		case "lease_lost", "aborted":
			return "", false
		}
	}
	if IsRetryable(err) && item.Attempts < item.MaxAttempts {
		return "open", true
	}
	return "failed", true
}

// partDurationMS renders a non-negative elapsed duration for the UI.
func partDurationMS(start, end time.Time) int64 {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

// redactPartText removes credential-shaped values from free text and bounds it.
func redactPartText(text string, max int) string {
	redacted := partSecretAssignmentPattern.ReplaceAllString(text, "${1}=[redacted]")
	redacted = partBearerPattern.ReplaceAllString(redacted, "Bearer [redacted]")
	redacted = partJWTPattern.ReplaceAllString(redacted, "[redacted]")
	if max > 0 {
		redacted = truncateRunes(redacted, max)
	}
	return redacted
}

// sanitizePartJSONObject sanitizes a model-supplied JSON object (tool call
// arguments). Malformed or non-object input is omitted instead of failing the
// run; the model already sees the raw value through the tool channel.
func sanitizePartJSONObject(raw string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil, false
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return marshalSanitizedPartJSON(redactPartValue(object, 0))
}

// sanitizePartResult sanitizes a tool result. The contract result is an object
// or null, so a scalar tool result is wrapped under "value".
func sanitizePartResult(content string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, false
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return marshalSanitizedPartJSON(map[string]any{"value": redactPartText(trimmed, producedPartStringMax)})
	}
	sanitized := redactPartValue(value, 0)
	if _, ok := sanitized.(map[string]any); !ok {
		sanitized = map[string]any{"value": sanitized}
	}
	return marshalSanitizedPartJSON(sanitized)
}

func marshalSanitizedPartJSON(value any) (json.RawMessage, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	if len(encoded) > producedPartJSONMax {
		encoded, err = json.Marshal(map[string]any{
			"truncated": true,
			"head":      truncateRunes(string(encoded), producedPartHeadMax),
		})
		if err != nil {
			return nil, false
		}
	}
	return json.RawMessage(encoded), true
}

// redactPartValue walks a decoded JSON value: credential-keyed fields become
// "[redacted]", every remaining string is credential-scanned and bounded, and
// the walk is depth/size bounded so a hostile tool result cannot explode.
func redactPartValue(value any, depth int) any {
	if depth > producedPartMaxDepth {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isCredentialKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactPartValue(item, depth+1)
		}
		return out
	case []any:
		items := typed
		if len(items) > 100 {
			items = items[:100]
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, redactPartValue(item, depth+1))
		}
		return out
	case string:
		return redactPartText(typed, producedPartStringMax)
	default:
		return value
	}
}

// isCredentialKey reports whether a JSON field name denotes a credential value
// that must never reach conversation parts.
func isCredentialKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "password", "passwd", "pwd", "secret", "token", "accesstoken", "refreshtoken",
		"sessiontoken", "apikey", "apitoken", "authorization", "cookie", "cookies",
		"credential", "credentials", "clientsecret", "secretkey", "privatekey",
		"storagestate", "sessionid", "passphrase":
		return true
	default:
		return false
	}
}
