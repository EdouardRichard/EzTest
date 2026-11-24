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
	// defaultToolRetryMax/defaultOverflowRetries mirror the catalog defaults so
	// an unwired settings reader keeps retrying tools and rescuing overflow.
	// Tool retry keeps the pre-W1-2 default of 2; overflow is 1 per spec ST-5
	// (the old hard-coded 2 was drift, not intended behavior).
	defaultToolRetryMax      = 2
	defaultOverflowRetries   = 1
	finishTimeout            = 5 * time.Second
	toolResultHeadRunes      = 2000
	scoutAttachmentTextRunes = 4000
	// maxLoopIterations is the P6 defensive loop floor (design §4.5 兜底). An
	// unlimited turn budget (execution.max_case_turns=0) is still bounded by this
	// hard cap so a runaway loop can never spin forever. It is a safety floor, not
	// a configurable budget: the configurable knob is execution.max_case_turns.
	maxLoopIterations = 10000
	// defaultStallRounds is the P6 thinking-only stall gate threshold (design
	// §4.5 防失控): this many consecutive rounds with no tool call and no
	// substantive assistant content force the settlement winding turn.
	defaultStallRounds = 2
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
	// Knowledge retrieves the approved knowledge injected into the version
	// context (P7). Nil disables injection for deployments without the
	// knowledge context.
	Knowledge KnowledgeReader
	// KnowledgeUsage records knowledge_usage rows after the context packet is
	// persisted. Nil disables usage accounting.
	KnowledgeUsage KnowledgeUsageRecorder
	// Skills loads the resident skill metadata layer injected into the context
	// (v0.2.4-C T19 progressive disclosure). Nil disables the layer; bodies are
	// read on demand through skill.read (ToolDeps.Skill).
	Skills SkillMetadataReader
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
	// PlanWriter is the v0.22 plan.update port (R-A8-2). The tool is registered
	// per run and additionally filtered by the profile/role allow-lists, so a
	// nil writer simply leaves plan.update unregistered.
	PlanWriter tools.PlanWriter
	// QuestionAsker is the v0.22 question.ask port (R-B3-3). The execution
	// question service satisfies it through the composition-root adapter; nil
	// leaves question.ask unregistered.
	QuestionAsker tools.QuestionAsker
	// ClarificationSearch is the v0.22 clarification.search retrieval port
	// (R-B5). A nil searcher leaves the tool unregistered.
	ClarificationSearch tools.ClarificationSearcher
	// Completion builds the per-run server-side completion validator/writer of
	// task.complete (R-A3). A nil builder or a builder error leaves
	// task.complete unregistered: a forged completion can never end a run.
	Completion CompletionBuilder
	// Settlement resolves the per-run settlement writeback (R-A8-3/A-14). It
	// reuses the completion target resolver and writes the unresolved plan
	// item's status/decision plus a progress fact when a run ends on the
	// turn/time settlement turn without a verified completion. A nil builder or
	// a builder/store error only logs a warning: the writeback is best effort
	// and never changes the run verdict.
	Settlement SettlementBuilder
	// CompactionSummary persists the R-A5-12 summary revision (version =
	// latest+1, monotonic covered_message_id). Nil disables summary persistence
	// (the rolling summary still reaches the packet but not the DB).
	CompactionSummary SummaryStore
	// CompactionManager coordinates the R-A5-10 compaction transactions (one
	// live compaction per conversation, thrashing guard, context.compacted
	// event). Nil builds an in-process manager with the runner's logger.
	CompactionManager *agentcontext.CompactionManager
	// Logger receives the log-only knowledge runtime failures. It defaults to
	// slog.Default().
	Logger *slog.Logger
	// Load counts runs currently executing in this process (P8 drain
	// observability). Nil disables the counter.
	Load       *load.Counter
	InstanceID string
}

// CompletionContext is the runtime scope of one task.complete target. The
// builder resolves the single plan item the verified completion writes; the
// model never supplies any of these values.
type CompletionContext struct {
	WorkType       string
	ProfileKey     string
	VersionID      int64
	WorkItemID     int64
	ConversationID int64
	AgentRunID     int64
	RoundID        int64
	// PlanItemID is the explicit plan target carried by the work-item payload
	// (plan_item_id), when the creating path linked one. The builder still
	// verifies it belongs to the version and falls back to the single
	// in-progress plan item when it is zero.
	PlanItemID int64
}

// CompletionBuilder resolves the server-side completion criterion of one run
// (R-A3-2). A nil/zero CompletionDeps or an error leaves task.complete
// unregistered for the run, so a task whose completion cannot be verified never
// exposes a half-wired completion tool.
type CompletionBuilder func(ctx context.Context, run CompletionContext) (tools.CompletionDeps, error)

// SettlementBuilder resolves the settlement writeback path of one run: the
// target plan item resolved by the same ordered resolver task.complete uses,
// bound to a SettlementRecorder. It is called only when a run actually ends on
// the turn/time settlement turn without a verified completion.
type SettlementBuilder func(ctx context.Context, run CompletionContext) (SettlementRecorder, error)

// conversationWriterPort is the v0.22 single-writer lease surface of the
// conversation context (R-B1-2, Task 21). The runner type-asserts its
// ConversationPort: when the adapter implements the lease, Host replies are
// written as the leased writer and the legacy writer-less append is never used.
type conversationWriterPort interface {
	AcquireWriter(ctx context.Context, conversationID int64, agent string, turnID int64) error
	RenewWriter(ctx context.Context, conversationID int64, agent string, turnID int64) error
	ReleaseWriter(ctx context.Context, conversationID int64, agent string, turnID int64) error
	AppendWriterAssistant(ctx context.Context, conversationID int64, agent string, turnID int64, runID int64, replyTo *int64, parts []ConversationPart) (ConversationMessage, error)
}

// Runner executes one work item as an agent run: context packet, bounded model
// loop, scoped tool calls and the profile-specific output. It never confirms,
// deletes or closes a requirement; the model can only submit drafts and
// proposals through the scoped tools.
type Runner struct {
	repo                agent.RuntimeRepository
	chat                llmapp.ChatGateway
	settings            SettingsReader
	clock               clock.Clock
	ids                 id.Generator
	events              eventingapp.Publisher
	audit               auditapp.Recorder
	conversation        ConversationPort
	versions            VersionReader
	attachments         AttachmentReader
	images              ImageLoader
	teamRoom            TeamRoomPort
	content             ContentWriter
	toolDeps            tools.Deps
	taskProvider        TaskContextProvider
	runCanceller        RunCanceller
	reportFinalizer     ReportFinalizer
	knowledge           KnowledgeReader
	knowledgeUsage      KnowledgeUsageRecorder
	skills              SkillMetadataReader
	logger              *slog.Logger
	load                *load.Counter
	instanceID          string
	builder             *ContextBuilder
	briefing            *agentcontext.BriefingBuilder
	assembler           *agentcontext.Assembler
	clarification       ClarificationConverger
	designRounds        DesignRoundStarter
	batches             BatchStarter
	planWriter          tools.PlanWriter
	questionAsker       tools.QuestionAsker
	clarificationSearch tools.ClarificationSearcher
	completion          CompletionBuilder
	settlement          SettlementBuilder
	compactionSummary   SummaryStore
	compactionManager   *agentcontext.CompactionManager
}

// NewRunner wires the agent runner.
func NewRunner(deps RunnerDeps) *Runner {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	compactionManager := deps.CompactionManager
	if compactionManager == nil {
		compactionManager = agentcontext.NewCompactionManager(agentcontext.WithCompactionLogger(logger))
	}
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
		images:          deps.Images,
		teamRoom:        deps.TeamRoom,
		content:         deps.Content,
		toolDeps:        deps.ToolDeps,
		taskProvider:    deps.TaskProvider,
		runCanceller:    deps.RunCanceller,
		reportFinalizer: deps.ReportFinalizer,
		knowledge:       deps.Knowledge,
		knowledgeUsage:  deps.KnowledgeUsage,
		skills:          deps.Skills,
		logger:          logger,
		load:            deps.Load,
		instanceID:      deps.InstanceID,
		// The v0.22 context assembly owns compaction (CompactionSummarizer +
		// CompactionManager); the legacy builder only resolves the token budget,
		// so it is wired without the retired LLM summary path.
		builder:             NewContextBuilder(deps.Settings, nil),
		briefing:            deps.Briefing,
		assembler:           agentcontext.NewAssembler(),
		clarification:       deps.Clarification,
		designRounds:        deps.DesignRounds,
		batches:             deps.Batches,
		planWriter:          deps.PlanWriter,
		questionAsker:       deps.QuestionAsker,
		clarificationSearch: deps.ClarificationSearch,
		completion:          deps.Completion,
		settlement:          deps.Settlement,
		compactionSummary:   deps.CompactionSummary,
		compactionManager:   compactionManager,
	}
}

// RunResult summarizes one finished agent run.
type RunResult struct {
	AgentRunID     int64
	Status         agent.AgentRunStatus
	Turns          int
	Usage          map[string]int
	TerminalReason string
	// SettlementCause is the stable budget cause (turn_budget/time_budget) when
	// the run's last turn was the tool-less settlement turn. It stays empty when
	// the model finished on its own. token_budget is only a legacy mapping and
	// is never produced since v0.22 (tokens are telemetry). The execution
	// context uses it to explain an incomplete case run instead of silently
	// aggregating.
	SettlementCause string
	// SettlementDetail is one bounded, secret-free sentence with the observed
	// usage and the limit that triggered the settlement.
	SettlementDetail string
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
	if payload.PlanItemID, err = parseID("plan_item_id"); err != nil {
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
	// P4 delivery payload (contracts/task-types.md §6). ReportRevision is
	// canonically "report_revision"; the frozen report.wave/report.version
	// payload spells it "revision", accepted as a compatibility alias. GD-17
	// removed the close-task payload (close_round/report_revision_id/
	// predicate_fingerprint) with the requirement.close/version.close work types.
	if payload.IssueID, err = parseID("issue_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.ReportID, err = parseID("report_id"); err != nil {
		return WorkItemPayload{}, err
	}
	if payload.ReportRevision, err = parseInt32("report_revision", "revision"); err != nil {
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
	if token, ok := object["dispatch_token"]; ok {
		_ = json.Unmarshal(token, &payload.DispatchToken)
	}
	if trigger, ok := object["trigger"]; ok {
		_ = json.Unmarshal(trigger, &payload.Trigger)
	}
	// v0.2.4-E quality gate (T14): the design.judge scheduling payload (T8).
	// draft_case_version_ids is the generation anchor string, not an ID list
	// to parse; cases[] is the pending draft set, each entry using the same
	// decimal-string/number ID convention as every other work-item field (a
	// generic JSON round-trip through map[string]any decodes numbers as
	// float64, so both shapes must be accepted, never assumed pre-typed).
	if anchor, ok := object["draft_case_version_ids"]; ok {
		if err := json.Unmarshal(anchor, &payload.DraftCaseVersionIDs); err != nil {
			return WorkItemPayload{}, fmt.Errorf("work item payload draft_case_version_ids must be a string")
		}
	}
	if casesRaw, ok := object["cases"]; ok {
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(casesRaw, &entries); err != nil {
			return WorkItemPayload{}, fmt.Errorf("work item payload cases must be an array of objects")
		}
		payload.JudgeCases = make([]JudgeCaseRef, 0, len(entries))
		for index, entry := range entries {
			ref := JudgeCaseRef{}
			var err error
			if raw, ok := entry["case_id"]; ok {
				if ref.CaseID, err = decodeDecimal(fmt.Sprintf("cases[%d].case_id", index), raw); err != nil {
					return WorkItemPayload{}, err
				}
			}
			if raw, ok := entry["case_version_id"]; ok {
				if ref.CaseVersionID, err = decodeDecimal(fmt.Sprintf("cases[%d].case_version_id", index), raw); err != nil {
					return WorkItemPayload{}, err
				}
			}
			if raw, ok := entry["content_hash"]; ok {
				if err := json.Unmarshal(raw, &ref.ContentHash); err != nil {
					return WorkItemPayload{}, fmt.Errorf("work item payload cases[%d].content_hash must be a string", index)
				}
			}
			// H-F10b #34: a judge review entry MUST name a real case version. A
			// missing/zero id used to survive parsing and render a degenerate
			// judge_case=case_id:0,case_version_id:0 scope line, pointing the judge
			// at nothing. Fail closed at the parse boundary instead (the scheduler
			// always supplies both), so the renderer can never see a zero id.
			if ref.CaseID <= 0 {
				return WorkItemPayload{}, fmt.Errorf("work item payload cases[%d].case_id must be a positive decimal ID", index)
			}
			if ref.CaseVersionID <= 0 {
				return WorkItemPayload{}, fmt.Errorf("work item payload cases[%d].case_version_id must be a positive decimal ID", index)
			}
			payload.JudgeCases = append(payload.JudgeCases, ref)
		}
	}
	// v0.2.4-E T19 fix round 1 (SC-E4): the design.rework scheduling payload
	// (handleJudgeFail) carries the next judgment round and the judge findings
	// summary. Both are rendered into the rework task context so the author
	// sees WHY the case failed; a malformed value fails the payload closed
	// instead of silently dropping the findings.
	if payload.ReworkRound, err = parseInt32("round"); err != nil {
		return WorkItemPayload{}, err
	}
	if findings, ok := object["findings"]; ok {
		if err := json.Unmarshal(findings, &payload.ReworkFindings); err != nil {
			return WorkItemPayload{}, fmt.Errorf("work item payload findings must be a string")
		}
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
	// promptBase is the number of leading state.messages rendered from the
	// persisted packet plus the current user turn. Everything after it is this
	// run's in-flight assistant/tool history, which the conversation snapshot
	// does not contain yet and a mid-run compaction must preserve (fix round 2).
	promptBase  int
	images      []llmapp.ChatImage
	userContent string
	cards       []ConversationPart
	// streamParts are the 0.2.1 UI streaming parts produced by this run
	// (thinking/tool_call/error, plus the live text parts of a Host incremental
	// reply). They are written with the final assistant message as
	// conversation_message_part rows and published on conversation.message; a
	// Host incremental run mirrors them into its message head by seq while it
	// streams (v0.2.4-D T6). The job's own card is a separate assistant message
	// owned by projectJobCard.
	streamParts []ConversationPart
	// droppedStreamParts counts produced parts refused by the message cap. The
	// drop is surfaced explicitly (error part + log) where the parts are
	// rendered, never silent (constitution VII.6).
	droppedStreamParts int
	// firstDeltaSeen marks the R-D7 retry boundary of the current streaming
	// model turn: before the first delta the overflow compaction rescue stays
	// available; after it any error ends the turn without retry.
	firstDeltaSeen bool
	// hostStream is the v0.2.4-D incremental Host reply head (nil for non-Host
	// profiles, adapters without the stream surface, or a run whose head could
	// not be opened). hostStreamCause carries the user-visible failure text the
	// terminal interrupted seal explains.
	hostStream      *hostStreamState
	hostStreamCause string
	// transcript is the v0.2.4-D T8 incremental agent_message row of the CURRENT
	// model turn (every profile). It is nil before the turn's first delta and
	// after the turn is sealed; a run whose row could not be opened keeps a
	// broken marker and falls back to the batch append.
	transcript *transcriptStreamState
	// finalText is the reply the model loop returned. The Host incremental seal
	// reads it so a reply the produced-part cap kept out of the live stream is
	// still written (the cap bounds the trace, never the reply).
	finalText        string
	conversationKind string
	team             agent.Team
	overflowHits     int
	softNoted        bool
	// settlementCause/settlementDetail describe the budget that opened the
	// tool-less settlement turn. They survive into RunResult and, for a settled
	// done run, into agent_run.terminal_reason so a 1/4-step stop is explainable.
	settlementCause  string
	settlementDetail string
	// completed is the v0.22 task.complete signal: a successful verified
	// completion ends the model loop immediately and allows exactly one
	// completion write per run (R-A3-1/R-A3-4).
	completed bool
	// stallGate is the P6 thinking-only spinning guard; stallTripped records that
	// it forced the settlement winding turn (design §4.5 防失控).
	stallGate    *stallGate
	stallTripped bool
	// writerLeased records the v0.22 Host single-writer lease acquired before
	// the loop and released when the run ends (R-B1-2).
	writerLeased bool
	writerStop   chan struct{}
	// recordedSteps/recordedAssertions are the successful execution recording
	// calls of this run (Task 98). The case executor's settlement turn uses them
	// to name the assertions of already executed steps that still need a real
	// result.
	recordedSteps      map[int]string
	recordedAssertions map[[2]int]struct{}

	// v0.22 R-A5 compaction wiring (fix round 1). compactionPolicy is the
	// resolved window/output/ratio budget of this run; lastPromptTokens is the
	// provider prompt size of the last model call and pendingNewTokens the
	// conservative estimate of what was appended since, so the pressure trigger
	// matches R-A5-2. lastCompactionSummary/lastCompactedMessageID/lastCompactionID
	// carry the summary transaction bookkeeping into the persistence + event.
	compactionPolicy       agentcontext.CompactionPolicy
	lastPromptTokens       int
	pendingNewTokens       int
	lastPressureTurn       int
	lastCompactionSummary  SummaryResult
	lastCompactedMessageID int64
	lastCompactionID       string
	// activitySeq is the per-run ordinal of the agent_activity fact ledger
	// (v0.2.4-D T10, Q04). Every recorded boundary takes the next value; the end
	// of a tool call reuses the seq its start took, so (run_id, seq) stays the
	// idempotency key of one fact.
	activitySeq int
}

// nextActivitySeq allocates the next per-run fact ordinal.
func (state *runState) nextActivitySeq() int {
	state.activitySeq++
	return state.activitySeq
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
		stallGate:        newStallGate(defaultStallRounds),
	}

	runCtx, cancel := r.runContext(ctx, profile)
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

	// v0.22 single-writer lease: a Host turn owns the conversation for the
	// duration of its run. Acquiring before the loop fences every legacy
	// writer-less append; a busy conversation fails the run as retryable.
	if err := r.beginWriterLease(runCtx, state); err != nil {
		return r.fail(runCtx, state, err)
	}
	defer r.endWriterLease(state)

	if err := r.prepare(runCtx, state); err != nil {
		return r.fail(runCtx, state, err)
	}
	startedPayload := map[string]any{
		"run_id":       idText(state.run.ID),
		"profile_key":  profile.Key,
		"work_item_id": idText(item.ID),
	}
	if state.payload.ConversationID > 0 && !isDesignJudgeWorkType(WorkTypeForItem(item)) {
		startedPayload["conversation_id"] = idText(state.payload.ConversationID)
	}
	r.publish(state, "agent.run.started", startedPayload)
	r.publishActivity(state, "thinking", "", "", 0)

	finalText, err := r.loop(runCtx, state)
	if err != nil {
		return r.fail(runCtx, state, err)
	}
	state.finalText = finalText
	if err := r.writeOutputs(runCtx, state, finalText); err != nil {
		return r.fail(runCtx, state, err)
	}
	// report.wave/report.version only complete when the targeted revision is
	// published; a missing published revision is a retryable task failure.
	if err := r.finalizeReport(runCtx, state); err != nil {
		return r.fail(runCtx, state, err)
	}
	// R-A3-1: a run ended by the verified task.complete signal carries the
	// stable terminal_reason=completed, distinct from a run that merely used up
	// its turns/wall clock. It is written to agent_run.terminal_reason and
	// surfaced in RunResult.
	reason := ""
	if state.completed {
		reason = "completed"
	}
	return r.finish(runCtx, state, agent.AgentRunDone, reason, false, Failure{})
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
	// v0.2.4-E quality gate (I-B4): the judge run has no conversation binding
	// even if a payload conversation_id is present, so its transcript rows
	// (appendTranscript/assistantTranscriptMessage) never carry one either.
	if payload.ConversationID > 0 && !isDesignJudgeWorkType(WorkTypeForItem(item)) {
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

	// v0.2.4-E quality gate (I-B4): the independent judge must not read the
	// design author's chat or the design team room, even if a payload
	// conversation_id is present (today's scheduler never attaches one; this
	// guard makes the independence structural, not scheduler-dependent). The
	// judge's inputs are versionPack (read-only version facts, loaded above)
	// plus requirement.read/design.graph_read; Conversation/TeamRoom stay the
	// zero value, never bounded-but-present.
	judgeRun := isDesignJudgeWorkType(WorkTypeForItem(state.item))
	if state.payload.ConversationID > 0 && !judgeRun {
		if r.conversation == nil {
			return errors.New("agent runner has no conversation port")
		}
		snapshot, err := r.conversation.Snapshot(ctx, state.item.VersionID, state.payload.ConversationID, r.intSetting("context.keep_recent_messages", DefaultKeepRecentMessages))
		if err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		messages := conversationMessages(snapshot.Messages)
		designConversation := strings.EqualFold(strings.TrimSpace(state.conversationKind), "design")
		if isDesignAuthoringWorkType(state.item.Type) {
			// Task 79: a background design author works from the work-item scope,
			// requirement.read and design.graph_read. Replaying the shared design
			// thread would replay every previous author's UI projection and grow
			// the prompt with the round; keep only the human instructions.
			messages = designAuthoringHistory(messages)
		} else if designConversation {
			// The design Host chat still needs its own recent turns, but not the
			// whole background-author transcript that S3 appends to the thread.
			messages = designHostHistory(messages)
		}
		state.input.Conversation = ConversationContext{
			Summary:        snapshot.Summary,
			PinnedFacts:    snapshot.PinnedFacts,
			RecentMessages: messages,
		}
	}
	if !judgeRun {
		state.input.TeamRoom = r.teamRoomContext(ctx, state.item.VersionID, state.team)
		if isDesignAuthoringWorkType(state.item.Type) ||
			strings.EqualFold(strings.TrimSpace(state.conversationKind), "design") {
			// The team room is where every author projects its own result: replaying
			// the full post bodies turns the shared room into a second unbounded
			// prompt. Keep the digest and only the newest, clipped posts.
			state.input.TeamRoom = boundDesignAuthoringTeamRoom(state.input.TeamRoom)
		}
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
	// R-A5-1: resolve the compaction policy from the effective model window,
	// max_output and the seeded compaction.* settings before the first assembly
	// so every pressure/boundary decision uses the same validated budget.
	state.compactionPolicy = r.resolveCompactionPolicy(ctx, state)
	state.lastCompactionID = fmt.Sprintf("compaction-run-%d", state.run.ID)
	state.input.Profile = state.profile
	state.input.Task = r.taskContext(ctx, state)
	state.input.Artifacts = artifactRefs(state)

	// P5/P7 compose the P1–P4 registry with the codeintel/knowledge tool
	// groups; the profile allowed_tools still select the surface each run may
	// call, and R51 intersects that surface with the profile role's whitelist.
	state.registry = r.registryForRun(ctx, state)
	state.roleTools, state.roleGuard, state.roleMapped = roleToolSurface(state.profile, r.audit)
	state.definitions = state.roleTools.Definitions(state.registry)

	// Knowledge is supplemental reference data: cap it at the injection budget
	// before the assembler renders the pinned facts (contract §10.3).
	knowledgeBudget := DefaultKnowledgeBudgetTokens
	if r.builder != nil {
		knowledgeBudget = r.builder.knowledgeBudgetTokens(ctx)
	}
	state.input.VersionPack.Knowledge = fitKnowledge(state.input.VersionPack.Knowledge, knowledgeBudget)

	// T19 progressive disclosure: inject the resident skill metadata layer
	// (name/description only; bodies stay in the runtime library until the
	// model reads them through skill.read). Pinned facts are never compacted,
	// so the layer stays resident for the whole run. A read failure is
	// supplemental and logged, never a run failure.
	state.input.Skills = r.skillMetadataForRun(ctx)

	// The current user turn is rendered before the assembly: the window_full
	// threshold accounts for it (Task 80 moved the per-work-item task context
	// into this turn, outside the assembler packet), and it is the exact text
	// handleOverflow re-appends after a boundary rebuild.
	state.userContent = r.userMessage(ctx, state)

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
	state.lastPromptTokens = assembly.AfterTokens + EstimateTokens(state.userContent)
	state.pendingNewTokens = 0
	// A boundary compaction persists its rolling summary and emits the
	// compaction event; an append-only round writes neither.
	if assembly.Compacted {
		r.recordContextCompaction(ctx, state, assembly, true)
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
	// Everything appended after this point is the run's in-flight transcript
	// (assistant tool calls and tool results). A mid-run compaction rebuilds the
	// leading packet+user base and re-appends this suffix via promptBase.
	state.promptBase = len(state.messages)
	return nil
}

func (r *Runner) loop(ctx context.Context, state *runState) (string, error) {
	maxTurns := r.maxTurns(state.profile)
	for state.turns < maxTurns {
		if err := ctx.Err(); err != nil {
			return "", contextRunError(err)
		}
		r.publishActivity(state, "thinking", "", "", 0)
		// Task 9 budget seam (spec §5.7): completion is structural (a reply
		// without tool calls ends the run), while tokens/time/turns are decided
		// here. A hit budget asks for one settlement turn (partial artifacts +
		// summary) instead of a hard stop; a settlement turn that is still over
		// budget is exhausted.
		usage := Usage{
			Tokens:    usageTokens(state.usage),
			Seconds:   r.elapsedSeconds(state),
			Turns:     state.turns,
			Settling:  state.softNoted,
			Completed: state.completed,
		}
		decision := state.resourceBudget.Decide(usage)
		if decision == BudgetExhaust {
			detail := state.settlementDetail
			if detail == "" {
				detail = budgetSettlementDetail(state.resourceBudget.ExceededBy(usage), state.resourceBudget, usage)
			}
			return "", &RunError{
				Reason:    "budget",
				Retryable: false,
				Cause:     fmt.Errorf("agent run exhausted its budget during the settlement turn (%s)", detail),
			}
		}
		// P6: the thinking-only stall gate forces the same settlement winding turn
		// as a hit budget, so a spinning model ends with a recorded partial outcome
		// instead of looping to the defensive floor (design §4.5 防失控).
		soft := decision == BudgetSettle || state.stallTripped
		// v0.22 R-A5-2 pressure trigger: when the last provider prompt plus the
		// conservative estimate of what was appended since reaches the policy
		// trigger, compact before re-sending. Only one pressure attempt per turn
		// so a non-reducing compaction cannot spin the loop; the manager's
		// thrashing guard bounds consecutive attempts.
		if state.compactionPolicy.Trigger > 0 && state.turns > state.lastPressureTurn {
			if pressure, ok := state.compactionPolicy.PressureDecision(state.lastPromptTokens, state.pendingNewTokens); ok {
				state.lastPressureTurn = state.turns
				if err := r.compactContext(ctx, state, pressure, -1); err != nil {
					return "", err
				}
			}
		}
		// Task 98: every turn re-sends the whole transcript, and a read-tool
		// result can be a large page/file/schema payload. The prompt transcript
		// keeps only the newest reads; the persisted transcript stays complete.
		r.pruneCaseExecutorReads(state)
		// v0.22 A-6 (R-A6-4): tool results older than the newest two call/result
		// pairs are folded to head/tail + content_ref before the prompt is
		// re-sent. Pairing is preserved by construction and the persisted
		// transcript is not rewritten. The case executor keeps its specialized
		// read pruner for now: the generic envelope would replace the
		// specialized stub (which names the read tool) a second time.
		if state.profile.Key != ProfileKeyCaseExecutor {
			r.pruneToolResults(ctx, state)
		}
		request := llmapp.ChatRequest{
			Model:           state.model.Model,
			Messages:        append([]llmapp.ChatMessage(nil), state.messages...),
			MaxOutputTokens: state.model.MaxOutput,
		}
		var settlementTools []llmapp.ToolDefinition
		if soft {
			if !state.softNoted {
				state.softNoted = true
				state.settlementCause = state.resourceBudget.ExceededBy(usage)
				if state.settlementCause == "" && state.stallTripped {
					// The stall gate, not a resource budget, opened the settlement.
					state.settlementCause = BudgetCauseStall
				}
				state.settlementDetail = budgetSettlementDetail(state.settlementCause, state.resourceBudget, usage)
				// Task 98: the case executor keeps a recording-only settlement
				// surface so the assertions of already executed steps are really
				// written before the run ends. Every other profile keeps the
				// frozen tool-less settlement turn.
				settlementTools = caseExecutorSettlementDefinitions(state)
				instruction := settlementInstruction(state.settlementCause)
				if len(settlementTools) > 0 {
					request.Tools = settlementTools
					instruction = caseExecutorSettlementInstruction(
						state.settlementCause, pendingCaseExecutorAssertions(state))
				}
				request.Messages = append(request.Messages, llmapp.ChatMessage{
					Role:    "system",
					Content: instruction,
				})
			}
		} else {
			request.Tools = state.definitions
		}

		turnStarted := r.now()
		// v0.2.4-D: the model turn is consumed as a stream through the same
		// ChatGateway port (capability gate/telemetry parity). The Host profiles
		// write the conversation reply incrementally from the first delta (T6);
		// every other profile keeps the batch write. A stream failure never
		// falls back to Chat.
		response, err := r.streamChatTurn(ctx, state, request, turnStarted)
		if err != nil {
			if state.firstDeltaSeen {
				// R-D7: content already reached the caller, so neither a
				// compaction retry nor another upstream attempt may replay it.
				// Keep the partial turn and fail with the unchanged taxonomy.
				// The failure is also non-retryable at the work-item level: a
				// retryable RunError reopens the item (worker recordFailure →
				// queue.Fail → reclaim), which would replay the same model turn
				// one layer up and duplicate what the user already saw.
				r.preservePartialTurn(ctx, state, response, turnStarted, err)
				if ctxErr := ctx.Err(); ctxErr != nil {
					return "", contextRunError(ctxErr)
				}
				return "", &RunError{Reason: "model_error", Retryable: false, Cause: err}
			}
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
		// in the thread ("思考了 Ns"). The duration is the model turn; the text
		// is bounded before it can reach the UI (GD-01: credentials stay plaintext).
		// appendThinkingPart is the single producer shared with the preserved
		// partial turn; a Host streaming turn finalizes its live part in place.
		r.appendThinkingPart(state, response.ReasoningContent, turnStarted)
		// A turn whose content only arrived on the terminal frame still owns a
		// text part, so the incremental head never loses the reply text. H-E13:
		// trim the edges to match the batch loop's final text (same reply, same
		// first/last whitespace on both paths).
		if content := strings.TrimSpace(response.Content); content != "" && state.hostStream != nil && state.hostStream.textSeq == 0 {
			state.hostStream.textSeq = r.upsertHostLivePart(state, 0, ConversationPart{Type: "text", Text: content})
		}
		r.syncHostStream(ctx, state)
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
		// v0.22 R-A5-2 pressure bookkeeping: the provider prompt size is the
		// authoritative anchor; every appended assistant/tool message since is
		// the conservative delta for the next round's trigger check.
		if promptTokens := response.Usage["prompt_tokens"]; promptTokens > 0 {
			state.lastPromptTokens = promptTokens
		} else {
			state.lastPromptTokens = contextPacketTokens(state.contextPacket) + state.pendingNewTokens
		}
		state.pendingNewTokens = EstimateTokens(response.Content)
		for _, call := range response.ToolCalls {
			state.pendingNewTokens += EstimateTokens(call.Arguments)
		}

		if len(response.ToolCalls) == 0 {
			text := strings.TrimSpace(response.Content)
			if text != "" {
				return text, nil
			}
			// P6 thinking-only round: no tool call and no substantive content. For
			// the case executor, do not end on an empty reply or spin forever —
			// count consecutive stalls and force the settlement winding turn after
			// the threshold (design §4.5 防失控). A settlement turn (soft) still ends
			// on its empty reply below.
			if state.profile.Key == ProfileKeyCaseExecutor && !soft {
				if state.stallGate.record(false, false) {
					state.stallTripped = true
				}
				continue
			}
			return text, nil
		}
		// A tool-call round breaks the thinking-only streak.
		state.stallGate.record(true, false)
		if soft {
			if len(settlementTools) > 0 {
				// Task 98: the recording-only settlement turn. The recording
				// calls reach the execution context (a new browser/network
				// action is answered with a tool error by executeTools), then
				// the run ends with the model's wrap-up text instead of spending
				// another over-budget model turn.
				if err := r.executeTools(ctx, state, response.ToolCalls); err != nil {
					return "", err
				}
				return strings.TrimSpace(response.Content), nil
			}
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
		// R-A3-1: a verified task.complete ends the run without another model
		// turn (and without spending a settlement turn): the completion is the
		// terminal condition of the task.
		if state.completed {
			return strings.TrimSpace(response.Content), nil
		}
	}
	return "", &RunError{
		Reason:    "budget",
		Retryable: false,
		Cause:     fmt.Errorf("agent run reached the max turn budget (%d turns)", maxTurns),
	}
}

// settlementInstruction is the system message of the tool-less wrap-up turn. It
// must name the budget that actually triggered the settlement: the pre-QA-Q2
// fixed text always blamed the clock, so a turn/time-budget stop produced a
// model summary that pointed at the wrong cause. The token branch is retained
// only as a legacy mapping; v0.22 tokens are telemetry and never settle.
func settlementInstruction(cause string) string {
	switch cause {
	case BudgetCauseTokens:
		return "本次运行的 token 预算即将用尽，请不要再调用工具，直接给出最终回复，并如实说明已完成与未完成的步骤。"
	case BudgetCauseTurns:
		return "本次运行的轮次预算即将用尽，请不要再调用工具，直接给出最终回复，并如实说明已完成与未完成的步骤。"
	case BudgetCauseTime:
		return "运行时间即将用尽，请不要再调用工具，直接给出最终回复，并如实说明已完成与未完成的步骤。"
	case BudgetCauseStall:
		return "连续多轮只有思考而没有调用工具或给出实质内容，运行即将结束。请不要再空转，直接给出最终回复，并如实说明已完成与未完成的步骤。"
	default:
		return "本次运行即将结束，请不要再调用工具，直接给出最终回复，并如实说明已完成与未完成的步骤。"
	}
}

// budgetSettlementDetail renders the observable usage and limit of one
// settlement cause. It carries counts only (no prompts, tool results or
// credentials) and is persisted on the case_run summary.
func budgetSettlementDetail(cause string, budget Budget, usage Usage) string {
	normalized := budget.normalized()
	switch cause {
	case BudgetCauseTokens:
		return fmt.Sprintf("token budget: used %d of %d tokens after %d turns",
			usage.Tokens, normalized.MaxTokens, usage.Turns)
	case BudgetCauseTime:
		return fmt.Sprintf("time budget: elapsed %ds of %ds after %d turns",
			usage.Seconds, normalized.RunSeconds, usage.Turns)
	case BudgetCauseTurns:
		return fmt.Sprintf("turn budget: reached the turn fuse after %d turns", usage.Turns)
	case BudgetCauseStall:
		return fmt.Sprintf("stall gate: %d consecutive thinking-only rounds with no tool call or substantive content after %d turns",
			defaultStallRounds, usage.Turns)
	default:
		return fmt.Sprintf("resource budget: the run entered its settlement turn after %d turns", usage.Turns)
	}
}

// profileAllowsTool gates one tool call against the profile whitelist. The
// matching rule (exact names + "namespace.*" capability-domain wildcards) has
// exactly ONE implementation — tools.ToolSet.Allows — shared with the role
// whitelist; the former exact-only loop here was a divergent second copy
// (constitution art.5). Profiles list ~30 tools and tool calls are LLM-paced,
// so building the set per call is negligible.
func profileAllowsTool(profile agent.AgentProfile, name string) bool {
	return tools.NewToolSet(profile.Key, profile.AllowedTools).Allows(name)
}

func (r *Runner) executeTools(ctx context.Context, state *runState, calls []llmapp.ToolCall) error {
	ctx = platform.WithAgentRun(ctx, state.run.ID, state.item.ID)
	// v0.2.4-D T6: the assembled calls are announced as running before they
	// execute, so a Host thread shows the call while it is in flight.
	r.emitHostToolCallParts(ctx, state, calls)
	// H-E15: every early return below (a tool error that aborts the round, a
	// cancelled context, an encode/spill failure) leaves the announced calls'
	// status:"running" tool_call parts in place. That is the FAITHFUL state of those
	// calls — they were announced and never reached appendToolParts' final result —
	// not a missing seal. The run's terminal interrupted seal appends the error part
	// that explains why the round stopped (emitHostToolCallParts' own contract); a
	// later reader must not "fix" this by fabricating a result for a call that never
	// finished.
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
		// P4 delivery task targets, copied from the work-item payload.
		IssueID:        state.payload.IssueID,
		ReportID:       state.payload.ReportID,
		ReportRevision: state.payload.ReportRevision,
	}
	for _, call := range calls {
		callStarted := r.now()
		// Q04 fact hook (v0.2.4-D T10): the call is recorded as a fact first; the
		// existing agent.activity emission is derived from that write.
		activitySeq := r.recordToolCallStart(ctx, state, call, callStarted)
		var payload any
		var err error
		switch {
		case settlementRecordingActive(state) && !isCaseExecutorSettlementTool(call.Name):
			// Task 98: the case executor's settlement turn may only record
			// results it already produced. A new browser/network/database
			// action is rejected before it can touch the SUT; so is
			// execution.case_finish, whose deterministic replacement (Finalize)
			// must write the verdict with the budget cause/handoff.
			err = caseExecutorSettlementRejection(call.Name)
		case call.Name == tools.ToolTaskComplete && state.completed:
			// R-A3-4: every completion writes exactly one plan item and one
			// progress fact. A second declaration in the same run is rejected
			// instead of writing a duplicate completion.
			err = tools.Invalid("completion", "task.complete was already accepted for this run")
		case !profileAllowsTool(state.profile, call.Name):
			err = tools.Invalid("tool", "tool is not allowed for this profile: "+call.Name)
		case state.roleGuard != nil:
			// R51: the runner intersects the profile role's whitelist even when
			// a stored profile was widened outside it. A denial is a typed
			// forbidden error and an audit event, never a tool execution.
			payload, err = r.executeToolWithRetry(ctx, state, call.Name, func() (any, error) {
				return state.roleGuard.Execute(ctx, state.registry, scope, call.Name, json.RawMessage(call.Arguments))
			})
		case !state.roleMapped:
			// Unmapped profile: the profile whitelist was the only surface and
			// it already passed the first case.
			payload, err = r.executeToolWithRetry(ctx, state, call.Name, func() (any, error) {
				return state.registry.Execute(ctx, scope, call.Name, json.RawMessage(call.Arguments))
			})
		case !state.roleTools.Allows(call.Name):
			err = tools.Forbidden(state.roleTools.Role(), call.Name)
		default:
			payload, err = r.executeToolWithRetry(ctx, state, call.Name, func() (any, error) {
				return state.registry.Execute(ctx, scope, call.Name, json.RawMessage(call.Arguments))
			})
		}
		// Q04 fact hook: the call has ended. Its outcome folds into the same
		// (run_id, seq) fact row as its start, before the result is encoded or
		// spilled — a later runner-side failure must not lose the recorded end.
		r.recordToolCallEnd(ctx, state, call, activitySeq, callStarted, err)
		if err == nil {
			// Task 98: successful execution recording calls are remembered so a
			// budget settlement turn can name the assertions still missing.
			state.trackCaseExecutorRecording(call)
			// v0.22 R-A3-1: a successful, server-verified task.complete result
			// signals the completion criterion. The run ends after this tool
			// round; the completion is never declared twice.
			if signal, ok := payload.(tools.CompletionSignal); ok && signal.SignalsCompletion() {
				state.completed = true
			}
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
			// v0.22 A-6 (R-A6-3): an oversized result spills in full to the
			// content ledger and is replaced by head + tail + content_ref. The
			// policy comes from agent.tool_result_max_bytes (catalog default
			// 32768 bytes).
			policy := tools.LoadToolResultPolicy(ctx, r.settings)
			if policy.MaxBytes > 0 && len(encoded) > policy.MaxBytes && r.content != nil {
				spilled, spillErr := tools.SpillToolResult(ctx, runnerContentWriter{writer: r.content}, content, policy)
				if spillErr != nil {
					return &RunError{Reason: "tool_failure", Retryable: true, Cause: spillErr}
				}
				contentRef = spilled.ContentRef
				envelope, encodeErr := json.Marshal(spilled)
				if encodeErr != nil {
					return &RunError{Reason: "tool_failure", Retryable: true, Cause: encodeErr}
				}
				content = string(envelope)
			}
		}

		// 0.2.1 tool_call/error parts: the UI pairs streamed rows by call_id and
		// shows the tool name/status/duration/result collapsed. Arguments and
		// results are bounded (GD-01: credentials stay plaintext) before
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
		storedToolRow, err := r.repo.AppendMessage(ctx, message)
		if err != nil {
			return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
		}
		r.publishTranscriptSnapshot(ctx, state, storedToolRow)
		state.messages = append(state.messages, llmapp.ChatMessage{
			Role:       "tool",
			Content:    content,
			ToolCallID: call.ID,
		})
		state.pendingNewTokens += EstimateTokens(content)
	}
	r.syncHostStream(ctx, state)
	return nil
}

// handleOverflow compacts the context at a forced overflow decision (R-A5-2/
// R-A5-11) and retries. The provider rejected the prompt, so the forced
// overflow decision runs with the halved 8% retain budget; every retry after the
// first additionally halves the recent-segment window so strictly more history
// folds. The retry budget comes from compaction.overflow_retries (default 1);
// zero disables the rescue, so the first overflow is terminal. A recoverable
// compaction stop keeps the original context and still retries; exhausting the
// budget is the terminal unrecoverable case.
func (r *Runner) handleOverflow(ctx context.Context, state *runState) error {
	state.overflowHits++
	if state.overflowHits > r.overflowRetries() {
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
	policy := state.compactionPolicy
	if policy.Trigger <= 0 {
		policy = agentcontext.NewCompactionPolicy(state.input.ModelBudget.ContextWindow, state.input.ModelBudget.MaxOutput)
	}
	decision := policy.OverflowDecision(-1)
	if err := r.compactContext(ctx, state, decision, keepRecent); err != nil {
		return err
	}
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

// writeOutputs persists the profile-specific result: the two conversation Hosts
// write assistant messages (the clarification Host also carries requirement
// cards), while Design Author/Critic write team-room updates and background jobs
// never append assistant text.
// beginWriterLease acquires the v0.22 conversation single-writer lease for a
// Host turn (R-B1-2). Non-Host runs and adapters without the lease surface keep
// the pre-v0.22 behavior. The run ID is the lease turn ID; the profile key is
// the writing agent. A heartbeat renews the lease while the model loop runs.
func (r *Runner) beginWriterLease(ctx context.Context, state *runState) error {
	if state == nil || state.payload.ConversationID <= 0 || !isHostConversationProfile(state.profile.Key) {
		return nil
	}
	port, ok := r.conversation.(conversationWriterPort)
	if !ok {
		return nil
	}
	if err := port.AcquireWriter(ctx, state.payload.ConversationID, state.profile.Key, state.run.ID); err != nil {
		return &RunError{Reason: "conversation_busy", Retryable: true, Cause: err}
	}
	state.writerLeased = true
	// v0.2.4-D T6 (SC-D4 crash recovery): acquiring the lease takes over from a
	// dead Host turn, so any head of this conversation still streaming is an
	// orphan. Seal it before this run opens its own head.
	r.recoverOrphanHostStreams(ctx, state)
	stop := make(chan struct{})
	state.writerStop = stop
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				renewCtx, cancel := context.WithTimeout(context.Background(), finishTimeout)
				err := port.RenewWriter(renewCtx, state.payload.ConversationID, state.profile.Key, state.run.ID)
				cancel()
				if err != nil {
					r.logger.Warn("conversation writer lease renewal failed",
						"conversation_id", state.payload.ConversationID,
						"run_id", state.run.ID, "error", err)
				}
			}
		}
	}()
	return nil
}

// endWriterLease releases the Host single-writer lease. Expiry covers a crashed
// process, so a release failure is logged and never fails the run.
func (r *Runner) endWriterLease(state *runState) {
	if state == nil || !state.writerLeased {
		return
	}
	if state.writerStop != nil {
		close(state.writerStop)
		state.writerStop = nil
	}
	port, ok := r.conversation.(conversationWriterPort)
	if !ok {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	if err := port.ReleaseWriter(releaseCtx, state.payload.ConversationID, state.profile.Key, state.run.ID); err != nil {
		r.logger.Warn("conversation writer lease release failed",
			"conversation_id", state.payload.ConversationID,
			"run_id", state.run.ID, "error", err)
	}
	state.writerLeased = false
}

// isHostConversationProfile reports whether the profile writes assistant text
// into a conversation and therefore must own the writer lease. Background jobs
// (design author/critic) never append assistant text.
func isHostConversationProfile(profileKey string) bool {
	return profileKey == ProfileKeyClarificationHost || profileKey == ProfileKeyDesignHost
}

// appendAssistantReply writes one Host reply through the single-writer lease.
// The lease-less fallback keeps the previous behavior for deployments that do
// not wire the v0.22 conversation lease.
func (r *Runner) appendAssistantReply(ctx context.Context, state *runState, parts []ConversationPart) error {
	if state == nil || r.conversation == nil {
		return errors.New("agent runner has no conversation port")
	}
	if port, ok := r.conversation.(conversationWriterPort); ok && state.writerLeased {
		_, err := port.AppendWriterAssistant(ctx, state.payload.ConversationID, state.profile.Key,
			state.run.ID, state.run.ID, replyTarget(state.payload.MessageID), parts)
		return err
	}
	_, err := r.conversation.AppendAssistantMessage(ctx, AssistantMessageInput{
		VersionID:        state.item.VersionID,
		ConversationID:   state.payload.ConversationID,
		AgentRunID:       state.run.ID,
		ReplyToMessageID: replyTarget(state.payload.MessageID),
		Parts:            parts,
	})
	return err
}

// runnerContentWriter adapts the runtime's content writer to the tools
// package's content-ledger ref (R-A6-3/R-A6-4). The two structs are kept
// separate so the tools package never imports the agent application.
type runnerContentWriter struct {
	writer ContentWriter
}

func (w runnerContentWriter) PutText(ctx context.Context, owner string, data []byte) (tools.ContentRef, error) {
	if w.writer == nil {
		return tools.ContentRef{}, errors.New("tool result content storage is not configured")
	}
	ref, err := w.writer.PutText(ctx, owner, data)
	if err != nil {
		return tools.ContentRef{}, err
	}
	return tools.ContentRef{Ref: ref.Ref, SHA256: ref.SHA256, Size: len(data)}, nil
}

// pruneToolResults folds old tool results to head/tail + content_ref before a
// prompt turn (R-A6-4). Failures are log-only: the unpruned transcript is
// always a valid prompt and pruning must never fail the run.
func (r *Runner) pruneToolResults(ctx context.Context, state *runState) {
	if r.content == nil || state == nil {
		return
	}
	pruned, err := tools.PruneToolResultMessages(ctx, state.messages,
		runnerContentWriter{writer: r.content}, tools.PruneKeepRecentToolPairs)
	if err != nil {
		r.logger.Warn("tool result pruning failed; keeping the full transcript",
			"run_id", state.run.ID, "error", err)
		return
	}
	state.messages = pruned
}

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
			if state.completed {
				// task.complete already answered/closed the turn; there is no
				// free-text reply to project. An incremental head still has to
				// be sealed, which the terminal funnel does.
				return nil
			}
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("host produced an empty final message")}
		}
		// The clarification Host owns requirement drafting after the v0.22
		// Curator removal, so requirement cards created by its tools ride on the
		// same assistant message. The reply goes through the single-writer lease.
		if err := r.writeHostReply(ctx, state, finalText); err != nil {
			return err
		}
		r.convergeClarification(ctx, state, finalText)
	case ProfileKeyDesignHost:
		if finalText == "" {
			if state.completed {
				return nil
			}
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("design host produced an empty final message")}
		}
		if state.payload.ConversationID <= 0 {
			return &RunError{Reason: "invalid_payload", Retryable: false, Cause: errors.New("design host run has no conversation")}
		}
		if err := r.writeHostReply(ctx, state, finalText); err != nil {
			return err
		}
	case ProfileKeyDesignAuthor:
		if finalText == "" {
			return &RunError{Reason: "model_error", Retryable: true, Cause: errors.New("design author produced no design update")}
		}
		// R-B1-2: a background job never appends assistant text into the shared
		// design conversation. Its artifacts are the case revisions and the
		// team-room post; the job card is projected by the worker. This removes
		// the pre-v0.22 conversation append so a concurrent author cannot spam
		// the Host thread.
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
	case ProfileKeyDesignJudge:
		// v0.2.4-E quality gate (I-B4): the judge's only artifacts are the
		// design_judgment facts written through design.judgment_submit and the
		// worker's job card. It never appends assistant text into the design
		// conversation and never posts to the design team room — an explicit
		// no-write case, not the default branch's ConversationID>0 guard doing
		// it by accident. An empty final message is not a failure: the verdicts
		// were already submitted through the tool.
		return nil
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
	// v0.2.4-D T6 (SC-D4/R-D7): once the Host reply head exists the content is
	// visible, so the work item must never be replayed — a retry would run the
	// model again and write a second message with the same reply. This is the
	// single retry gate; it covers the mid-stream deadline, a failed terminal
	// seal and a post-visible tool failure alike.
	retryable = state.allowRunReplay(retryable)
	// The interrupted head explains the stop in the user's register (the job
	// card suppresses an aborted run's raw error; the thread must not leak it).
	state.hostStreamCause = hostStreamFailureMessage(reason, err)
	status := agent.AgentRunFailed
	switch reason {
	case "interrupted":
		status = agent.AgentRunInterrupted
	case "aborted":
		// Q06 / C-4 (H-A12): aborted is its own terminal status with an existing
		// read side (Agent Center AbortedCount filter); it must not collapse into
		// interrupted. finish remains the single terminal write entry.
		status = agent.AgentRunAborted
	}
	result, finishErr := r.finish(ctx, state, status, reason, retryable, failure)
	if finishErr != nil {
		return result, finishErr
	}
	return result, &RunError{Reason: reason, Retryable: retryable, Cause: err}
}

func (r *Runner) finish(ctx context.Context, state *runState, status agent.AgentRunStatus, reason string, retryable bool, failure Failure) (RunResult, error) {
	finishedAt := r.now()
	state.run.Status = status
	state.run.Turns = state.turns
	state.run.Usage = state.usage
	// A done run that needed the settlement turn explains itself: the concrete
	// budget cause becomes its terminal_reason so the Agent Center does not show
	// an empty reason for a run that stopped at 1/4 steps (QA Q2).
	if reason == "" && status == agent.AgentRunDone && state.settlementCause != "" {
		reason = state.settlementCause
	}
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

	// v0.2.4-D T6 (SC-D4): a terminal run never leaves its Host conversation head
	// streaming. This is the single terminal funnel, so every failure, cancel,
	// soft/hard timeout and lease loss seals the head here — including a run whose
	// own row write below fails. The write rides a fresh bounded context because
	// the run context may already be cancelled.
	r.sealHostStreamAtTerminal(ctx, state, status == agent.AgentRunDone, reason)
	// T8: the same guarantee for the agent transcript row of every profile.
	r.sealTranscriptAtTerminal(ctx, state)

	finishCtx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	if err := r.repo.FinishRun(finishCtx, state.run); err != nil {
		result := RunResult{
			AgentRunID:       state.run.ID,
			Status:           status,
			Turns:            state.turns,
			Usage:            state.usage,
			TerminalReason:   reason,
			SettlementCause:  state.settlementCause,
			SettlementDetail: state.settlementDetail,
		}
		if errors.Is(err, agent.ErrLeaseLost) {
			return result, &RunError{
				Reason:    "lease_lost",
				Retryable: false,
				Cause:     platform.NewError(409, "lease_lost", "agent run lease was taken over"),
			}
		}
		return result, &RunError{Reason: "run_finish_failed", Retryable: state.allowRunReplay(true), Cause: err}
	}

	// R-A8-3/A-14: a run that ended on the turn/time settlement turn without a
	// verified completion writes the plan item status/decision and a progress
	// fact back, so the next run's briefing does not keep the stale pre-run
	// state. This is best effort: the run row above is already terminal and a
	// resolution/store failure only logs a warning.
	r.recordSettlementWriteback(state, status, reason)

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
	} else if reason != "" {
		// A settled done run still carries its concrete terminal reason so live
		// consumers can explain it without waiting for the next list request.
		payload["terminal_reason"] = reason
	}
	r.publish(state, eventType, payload)
	r.recordAudit(state, status, reason)

	return RunResult{
		AgentRunID:       state.run.ID,
		Status:           status,
		Turns:            state.turns,
		Usage:            state.usage,
		TerminalReason:   reason,
		SettlementCause:  state.settlementCause,
		SettlementDetail: state.settlementDetail,
	}, nil
}

// settlementEndedRun reports whether a terminal run outcome is the settlement
// writeback trigger: a done run whose terminal reason is the budget that opened
// the tool-less wrap-up turn, or a budget exhaustion during that wrap-up turn.
// An interrupted/aborted or model-error run is not "ended by turn/time
// settlement" and is never written back.
func settlementEndedRun(status agent.AgentRunStatus, reason, cause string) bool {
	if cause == "" {
		return false
	}
	switch status {
	case agent.AgentRunDone:
		return reason == cause
	case agent.AgentRunFailed:
		// The settlement turn itself was still over budget; the plan item is
		// still incomplete and the next run must see that.
		return reason == "budget"
	default:
		return false
	}
}

// recordSettlementWriteback persists the R-A8-3/A-14 plan/fact writeback of a
// settled run. The completion target resolver is reused through the Settlement
// builder, so the same ordered link/payload/single-in-progress resolution picks
// the plan item. Failures are log-only: the run is already finished, and an
// unresolvable target or a store outage must not change the run verdict.
func (r *Runner) recordSettlementWriteback(state *runState, status agent.AgentRunStatus, reason string) {
	if r == nil || r.settlement == nil || state == nil {
		return
	}
	if state.completed || state.settlementCause == "" {
		return
	}
	if !settlementEndedRun(status, reason, state.settlementCause) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	recorder, err := r.settlement(ctx, CompletionContext{
		WorkType:       state.item.Type,
		ProfileKey:     state.profile.Key,
		VersionID:      state.item.VersionID,
		WorkItemID:     state.item.ID,
		ConversationID: state.payload.ConversationID,
		AgentRunID:     state.run.ID,
		RoundID:        state.payload.RoundID,
		PlanItemID:     state.payload.PlanItemID,
	})
	if err != nil {
		r.logger.Warn("settlement plan writeback target could not be resolved",
			"version_id", state.item.VersionID, "work_item_id", state.item.ID,
			"run_id", state.run.ID, "error", err)
		return
	}
	if recorder == nil {
		r.logger.Warn("settlement plan writeback target could not be resolved",
			"version_id", state.item.VersionID, "work_item_id", state.item.ID,
			"run_id", state.run.ID, "error", "settlement builder returned no recorder")
		return
	}
	result, err := recorder.RecordSettlement(ctx, Settlement{
		VersionID:  state.item.VersionID,
		AgentRunID: state.run.ID,
		Reason:     state.settlementCause,
	})
	if err != nil {
		r.logger.Warn("settlement plan writeback failed",
			"version_id", state.item.VersionID, "run_id", state.run.ID, "error", err)
		return
	}
	r.logger.Info("settlement plan writeback recorded",
		"version_id", state.item.VersionID, "run_id", state.run.ID,
		"plan_item_id", result.PlanItemID, "plan_status", result.PlanStatus,
		"decision", result.Decision, "fact_id", result.FactID)
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
	stored, err := r.repo.AppendMessage(ctx, message)
	if err != nil {
		return err
	}
	// Every transcript row reaches the admin live transcript, not only the
	// streaming assistant rows: the drawer must see the system/user/tool rows too.
	r.publishTranscriptSnapshot(ctx, state, stored)
	return nil
}

func (r *Runner) persistAssistant(ctx context.Context, state *runState, response llmapp.ChatResponse) error {
	return r.writeAssistantTurn(ctx, state, response, agent.MessageStreamStateComplete)
}

// registryForRun builds the per-run tool registry: the frozen P1-P7 surface
// (which includes the self-registered task.complete/tool_result groups when
// their deps are wired) plus the additive v0.22 Host tools that have no Deps
// group (plan.update, question.ask, clarification.search). Profile and role
// allow-lists still filter every registered tool, so adding a tool here can
// never widen a profile that does not list it.
func (r *Runner) registryForRun(ctx context.Context, state *runState) *tools.Registry {
	base := tools.NewKnowledgeRegistry(r.toolDepsForRun(ctx, state))
	extras := make([]tools.Tool, 0, 3)
	add := func(tool tools.Tool) {
		if tool == nil {
			return
		}
		if _, exists := base.Get(tool.Name()); exists {
			return
		}
		extras = append(extras, tool)
	}
	if r.planWriter != nil {
		add(tools.NewPlanUpdateTool(r.planWriter))
	}
	if r.questionAsker != nil {
		add(tools.NewQuestionAskTool(r.questionAsker))
	}
	if r.clarificationSearch != nil {
		add(tools.NewClarificationSearchTool(r.clarificationSearch))
	}
	if len(extras) == 0 {
		return base
	}
	all := make([]tools.Tool, 0, len(base.Names())+len(extras))
	for _, name := range base.Names() {
		tool, ok := base.Get(name)
		if !ok {
			continue
		}
		all = append(all, tool)
	}
	all = append(all, extras...)
	return tools.NewRegistry(all...)
}

func (r *Runner) toolDepsForRun(ctx context.Context, state *runState) tools.Deps {
	deps := r.toolDeps
	// v0.22 task.complete: the validator/writer are resolved per run from the
	// work-item target. A builder failure leaves the tool unregistered rather
	// than exposing a half-wired completion that could end the run without the
	// plan/fact write (R-A3-4).
	if r.completion != nil {
		completion, err := r.completion(ctx, CompletionContext{
			WorkType:       state.item.Type,
			ProfileKey:     state.profile.Key,
			VersionID:      state.item.VersionID,
			WorkItemID:     state.item.ID,
			ConversationID: state.payload.ConversationID,
			AgentRunID:     state.run.ID,
			RoundID:        state.payload.RoundID,
			PlanItemID:     state.payload.PlanItemID,
		})
		if err != nil {
			r.logger.Warn("task.complete is disabled for this run: completion target could not be resolved",
				"version_id", state.item.VersionID, "work_item_id", state.item.ID, "error", err)
		} else {
			deps.Completion = completion
		}
	}
	versionID := state.item.VersionID
	versionPack := state.input.VersionPack
	deps.VersionPack = func(context.Context, tools.Scope) (any, error) {
		return versionPackResult(versionPack), nil
	}
	task := state.input.Task
	deps.TaskPack = func(context.Context, tools.Scope) (any, error) {
		return taskPackResult(versionID, state.payload.ConversationID, state.run.ID, task, state.input.Execution, state.input.DeliveryTask), nil
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
	if state.payload.ConversationID > 0 && !isDesignJudgeWorkType(WorkTypeForItem(state.item)) {
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
	// v0.2.4-E quality gate (T14): the judge's review scope is the pending
	// draft set the scheduler selected (cases[]), not the full-draft-set
	// generation anchor (draft_case_version_ids, a scheduler bookkeeping key).
	// Each entry names the exact case_version_id/content_hash the judge must
	// review; requirement.read/design.graph_read give it the full content.
	for _, ref := range state.payload.JudgeCases {
		scope = append(scope, fmt.Sprintf("judge_case=case_id:%s,case_version_id:%s,content_hash:%s",
			idText(ref.CaseID), idText(ref.CaseVersionID), ref.ContentHash))
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
					"断言 kind 用 ui/text/api（需要数据库校验时 db）；“页面不显示某标识/文案”用 text/ui 断言",
					"用例契约是自然语言的：必填 sut_description/preconditions（确实没有前置时显式写“无”）/covered_acs（取自该需求当前 AC 集）；步骤目标写 target_description，db 断言给 sut_id；不要再传 version_sut/page/api/database 的数字 ID（已移除，传了会被拒）",
					"page/api 来源只作为溯源记录，ref_id 写你所依据的目录 ID；不确定时只用 requirement/knowledge 来源，不要编造 ref_id",
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
				"用例必须包含步骤、断言、来源，并写明自然语言用例契约：sut_description（被测系统，1..2000 字）、preconditions（前置条件，必填，无前置时显式写“无”）、covered_acs（本用例覆盖的验收标准 id，至少 1 个且必须取自该需求当前的 AC 集）；每条步骤与断言的 expected 必须是非空的可观察结果",
				"步骤目标写 target_description（自然语言定位），断言 kind=db 时给 sut_id；不要再传 version_sut/page/api/database 的数字 ID（这些字段已移除，传了会被拒）",
				"断言 kind 用 ui/text/api（需要数据库校验时 db）；“页面不显示某标识/文案”用 text/ui 断言",
				"同一需求版本内更新已有用例时传既有 ID/自然键",
				"只做一次 design.graph_read 获取 requirement_version_id 与发现事实；covered_acs 所需的验收标准 id 集不在 graph_read 里，要用 requirement.read（传 requirement_id）读取其 ac_ids 字段——这两个只读调用各一次即可，随后立即用 case.graph_propose 提交，不要用重复只读调用耗尽预算",
				"来源 sources.kind=requirement 的 ref_id 必须是 requirement_version_id（当前需求版本 ID），不能写 requirement_id；page/api 来源只作为溯源记录，ref_id 写你所依据的目录 ID，不确定时改用 requirement/knowledge 来源，不要编造",
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
	case WorkTypeDesignJudge:
		return TaskContext{
			Type: state.item.Type,
			Goal: "独立审查当前需求的用例草案（三维 rubric：覆盖率/可执行标准/关联系统），逐用例用 design.judgment_submit 提交 verdict 与 findings。",
			Constraints: []string{
				"只读需求（requirement.read）、用例图（design.graph_read）与代码事实（code.* 只读工具），不修改用例",
				"不确认用例或需求；判定只是候选，确认永远由人工完成",
				"不写 assistant 消息、不进入设计对话、不发团队频道帖子；唯一写入口是 design.judgment_submit",
				"只依据需求/用例图/代码事实/环境目录独立判断，不受设计对话或团队频道中作者结论的影响",
				"审查范围以作用域列出的 judge_case 条目为准（case_id/case_version_id/content_hash），逐条用 design.graph_read 读取完整步骤与断言后再判定",
			},
			AcceptanceCriteria: []string{
				"作用域内每个 judge_case 都提交一条 verdict（pass/fail）",
				"存在 blocker/important finding 时该用例 verdict=fail；仅 minor 可 pass 并附注记",
				"findings 每条带 dimension（coverage/executability/system）与 severity（blocker/important/minor）",
			},
			Scope: scope,
		}
	case WorkTypeDesignRework:
		// v0.2.4-E T19 fix round 1 (SC-E4): the rework author must see WHY the
		// case failed. The scheduler payload carries the judge findings summary
		// and the next judgment round (handleJudgeFail); render both into the
		// task block — without them the rework quality loop is blind and only a
		// deterministic fake could "revise" anything.
		if state.payload.ReworkRound > 0 {
			scope = append(scope, fmt.Sprintf("rework_round=%d", state.payload.ReworkRound))
		}
		if findings := strings.TrimSpace(state.payload.ReworkFindings); findings != "" {
			scope = append(scope, "judge_findings="+findings)
		}
		return TaskContext{
			Type: state.item.Type,
			Goal: "修订被独立裁判打回的用例：逐条对照作用域中的 judge_findings 补齐覆盖率/可执行标准/关联系统问题，" +
				"先读取该用例的完整步骤与断言，再用 case.graph_propose 重新提交修订后的 draft。",
			Constraints: []string{
				"只提交 draft/候选，不能确认、删除或关闭用例",
				"修订作用域 case_id 指向的用例：传既有 ID/自然键，保留未变更内容，不新建重复用例",
				"逐条回应 judge_findings 中的每个发现；不得编造需求未表达的要求来凑覆盖",
				"用例契约是自然语言的：必填 sut_description/preconditions（确实没有前置时显式写“无”）/covered_acs（取自该需求当前 AC 集）；步骤目标写 target_description，db 断言给 sut_id；每条步骤与断言的 expected 必须是非空的可观察结果",
				"先 design.graph_read（传 case_id）读取该用例完整内容，covered_acs 所需的 AC id 集用 requirement.read 读取；随后立即用 case.graph_propose 提交修订，不要用重复只读调用耗尽预算",
			},
			AcceptanceCriteria: []string{
				"case.graph_propose 返回新的 case_version_ids",
				"修订逐条回应 judge_findings 的发现",
				"向设计团队频道说明针对每个发现做了什么修订",
			},
			Scope: scope,
		}
	case WorkTypeExecuteCase:
		return TaskContext{
			Type: state.item.Type,
			Goal: "严格按任务上下文中的用例步骤顺序执行，逐步用 execution.step_record 记录，每条断言用 execution.assertion_record 记录，" +
				"需要等待时用 execution.wait，完成后用 execution.case_finish 提交候选终态。",
			Constraints: []string{
				"不得修改需求/用例/确认，不得跳过步骤或断言",
				"不得虚构 passed；expected 以任务上下文为准",
				"用例是自然语言契约：sut_description/covered_acs/preconditions 与每步 target_description 以任务上下文为准（context.task_pack 可回读），按 sut_description 与 SUT 目录语义匹配并自行定位目标",
				"执行步骤前用 db.query 通用 SQL 或 exec.run seed 建立 preconditions（Given）并验证，用后清理；seed 失败=前置不成立，记录该步 failed/blocked 并写明错误，不得静默跳过",
				"等待/时间窗口用 execution.wait，不要用重复读取空转",
				"操作被测系统用能力域工具（exec/shell/file/pty/http/db）；命令与请求证据自动落库",
				"工具需要 sut_id 时取任务上下文 SUT 目录里的值（context.task_pack 的 execution.suts[].sut_id，提示渲染为 sut=<id>）；单 SUT 版本可省略，多 SUT 版本必须显式指定，不要拿 database_id/page_id 顶替",
				"密码经 account.credentials 读取（凭据明文 by design，可入证据/日志）",
				"登录失败/2FA 用 human.request 请求人工接管",
				"失败/阻塞/不确定的断言要留下证据（命令/HTTP/DB 自动证据或 execution.evidence 文本）",
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
		if details, ok := deliveryTaskDetails(ctx, state.item); ok {
			state.input.DeliveryTask = details
		}
		return TaskContext{Type: state.item.Type, Scope: scope}
	}
}

// userMessage is the current user turn of the run: the per-work-item task
// context followed by the profile's triggering user content. Task 80: the task
// context is per-work-item data, so it belongs to the append-only user suffix,
// not the cache-stable system prefix; rendering it here keeps goal, constraints,
// acceptance and scope in front of the model without breaking the shared prefix.
func (r *Runner) userMessage(ctx context.Context, state *runState) string {
	return taskContextMessage(r.baseUserMessage(ctx, state), state.input)
}

// baseUserMessage renders the triggering user content without the per-work-item
// task context block.
func (r *Runner) baseUserMessage(ctx context.Context, state *runState) string {
	switch state.item.Type {
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
	case WorkTypeDesignRequirement:
		return fmt.Sprintf("请为需求 %s（需求版本 %s）设计场景与用例草案。",
			idText(state.payload.RequirementID), idText(state.payload.RequirementVersionID))
	case WorkTypeDesignGap:
		return fmt.Sprintf("请补齐需求 %s（需求版本 %s）对应的用例缺口并提交 draft。",
			idText(state.payload.RequirementID), idText(state.payload.RequirementVersionID))
	case WorkTypeDesignCritic:
		return fmt.Sprintf("请内部自审需求 %s（需求版本 %s）当前的场景/用例草案并提交 finding。",
			idText(state.payload.RequirementID), idText(state.payload.RequirementVersionID))
	case WorkTypeDesignJudge:
		return fmt.Sprintf("请独立审查需求 %s（需求版本 %s）作用域内列出的 %d 个待审用例草案，逐用例提交 verdict 与 findings。",
			idText(state.payload.RequirementID), idText(state.payload.RequirementVersionID), len(state.payload.JudgeCases))
	case WorkTypeDesignRework:
		// SC-E4 (T19 fix round 1): name the rejected case and the revision round
		// so the author's triggering turn carries the rejection, not the generic
		// default; the findings themselves ride the task block's judge_findings.
		if state.payload.ReworkRound > 0 {
			return fmt.Sprintf("用例 %s（用例版本 %s）被独立裁判打回，本轮为第 %d 轮修订：请逐条对照任务上下文中的 judge_findings 修订该用例，并用 case.graph_propose 重新提交 draft。",
				idText(state.payload.CaseID), idText(state.payload.CaseVersionID), state.payload.ReworkRound)
		}
		return fmt.Sprintf("用例 %s（用例版本 %s）被独立裁判打回：请逐条对照任务上下文中的 judge_findings 修订该用例，并用 case.graph_propose 重新提交 draft。",
			idText(state.payload.CaseID), idText(state.payload.CaseVersionID))
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
	// v0.2.4-E quality gate (I-B4): the judge run's events never carry a
	// conversation_id, mirroring buildRun's suppressed conversation binding.
	if state.payload.ConversationID > 0 && !isDesignJudgeWorkType(WorkTypeForItem(state.item)) {
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

// publishActivity emits the non-token "AI thinking/tool" activity event. It is
// the ONE agent.activity emitter. The event is a PROJECTION: whenever the
// emission also has a fact of record (a tool call, a compaction boundary), the
// fact is written first by the caller and this event is derived only after that
// write succeeded (v0.2.4-D T10, constitution 五.7/五.9). The per-turn "thinking"
// heartbeat has no fact: it decides nothing and is an immediate projection
// (五.11), which is why Q04 scopes the ledger to tool/compaction boundaries.
//
// activitySeq is the ledger ordinal of the fact this event was derived from, or
// 0 for a fact-less heartbeat. With run_id it is the (run_id, seq) key, so an
// operator reading the event stream can locate the exact ledger row (七.7).
func (r *Runner) publishActivity(state *runState, status, tool, summary string, activitySeq int) {
	payload := map[string]any{
		"run_id": idText(state.run.ID),
		"status": status,
	}
	if state.payload.ConversationID > 0 && !isDesignJudgeWorkType(WorkTypeForItem(state.item)) {
		payload["conversation_id"] = idText(state.payload.ConversationID)
	}
	if tool != "" {
		payload["tool"] = tool
	}
	if summary != "" {
		payload["summary"] = summary
	}
	if activitySeq > 0 {
		payload["activity_seq"] = activitySeq
	}
	r.publish(state, "agent.activity", payload)
}

// recordActivityFact writes one agent_activity row BEFORE any event derived from
// it, and reports whether the fact is committed. A failed write is logged with
// the run/kind/seq and the caller skips the derived event: the projection must
// never claim an activity the ledger does not hold, and the failure is explicit
// rather than a silent drop (constitution 五.7, 七.6). Observability never gates
// the model's work, so a fact failure does not fail the run.
func (r *Runner) recordActivityFact(ctx context.Context, state *runState, activity agent.Activity) bool {
	if r.repo == nil {
		return false
	}
	if err := r.repo.InsertActivity(ctx, activity); err != nil {
		if r.logger != nil {
			r.logger.WarnContext(ctx, "agent activity fact could not be recorded; its event is not emitted",
				"run_id", state.run.ID, "activity_type", string(activity.Type),
				"activity_seq", activity.Seq, "title", activity.Title, "error", err)
		}
		return false
	}
	return true
}

// recordToolCallStart writes the tool call's fact and only then derives the
// existing agent.activity emission. It returns the seq the call owns, which its
// end reuses so start and outcome are one fact row.
func (r *Runner) recordToolCallStart(ctx context.Context, state *runState, call llmapp.ToolCall, started time.Time) int {
	seq := state.nextActivitySeq()
	fact := agent.Activity{
		RunID:     state.run.ID,
		Seq:       seq,
		Type:      agent.ActivityTypeToolCall,
		Title:     call.Name,
		StartedAt: started,
		Payload:   map[string]any{"tool": call.Name, "tool_call_id": call.ID},
	}
	if r.recordActivityFact(ctx, state, fact) {
		r.publishActivity(state, "tool", call.Name, "", seq)
	}
	return seq
}

// recordToolCallEnd folds the call's outcome into the SAME (run_id, seq) row. It
// derives no event: the tool result already reaches the conversation and the run
// transcript through the tool parts, and a second activity event would only
// duplicate that projection. The outcome is recorded before the result is
// encoded/spilled, so a later runner-side failure cannot lose the fact that the
// call itself ended.
func (r *Runner) recordToolCallEnd(ctx context.Context, state *runState, call llmapp.ToolCall, seq int, started time.Time, toolErr error) {
	if seq <= 0 {
		return
	}
	ended := r.now()
	fact := agent.Activity{
		RunID:     state.run.ID,
		Seq:       seq,
		Type:      agent.ActivityTypeToolCall,
		Title:     call.Name,
		StartedAt: started,
		EndedAt:   &ended,
		Payload: map[string]any{
			"tool":         call.Name,
			"tool_call_id": call.ID,
			"duration_ms":  partDurationMS(started, ended),
		},
	}
	if toolErr != nil {
		fact.Error = toolErr.Error()
	}
	// The outcome is a fact of record even when the call ended because the run
	// was cancelled or timed out. The write therefore rides a fresh bounded
	// context, exactly like the terminal transcript/Host seals: an aborted call
	// must not leave its fact without an end.
	sealCtx, cancel := terminalSealContext(ctx)
	defer cancel()
	r.recordActivityFact(sealCtx, state, fact)
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

// Design authoring envelope (Task 79, kept by v0.22). The stored profile
// budgets cannot be trusted for design runs: profiles are seeded once and
// EnsureProfiles never overwrites an existing row, so an older database can
// still carry the chat-scale design budgets (design_author 8/300/600,
// design_planner 12/300/600) even after the seeds were raised. The runner
// therefore enforces the design authoring floor in code: a focused design-author
// run needs the read turn, the propose turn and the summary turn, and must not
// be cut off by the generic chat-scale soft timeout. Tokens are telemetry only
// since v0.22, so the envelope consists of turns and wall-clock time only
// (R-A2-1/R-A2-2).
//
// The envelope is profile-scoped, so it deliberately covers every design
// profile. design_critic can face a full design.graph_read + submit turn, so
// its runtime envelope is expanded to 16/600/900 as well. This is a documented
// expansion, pinned by TestDesignBudgetExpandsCritic, not an accidental
// override.
const (
	DesignRunMaxTurns       = 16
	DesignRunMinSeconds     = 600
	DesignRunMinHardSeconds = 900
)

func (r *Runner) maxTurns(profile agent.AgentProfile) int {
	limit := profile.Budgets.MaxTurns
	setting := r.intSetting("agent.max_turns", defaultMaxTurns)
	// P6 (design §4.5, R-C5): the case executor's turn budget is default-open and
	// configurable through execution.max_case_turns. The seed is MaxTurns 0
	// (T13); a non-positive profile limit reads the setting, and 0 means unlimited
	// (bounded only by the maxLoopIterations defensive floor). This closes the T13
	// disclosed window where a 0 seed fell back to the chat-scale agent.max_turns
	// (12) and starved a real case run. A positive stored profile limit (a live
	// pre-T13 row) is still honored as an explicit case-scale budget.
	if profile.Key == ProfileKeyCaseExecutor {
		if limit <= 0 {
			limit = r.intSettingAllowZero("execution.max_case_turns", 0)
		}
		if limit <= 0 {
			return maxLoopIterations
		}
		if limit > maxLoopIterations {
			return maxLoopIterations
		}
		return limit
	}
	// P3 execution profiles carry their own case-scale turn budget; the design
	// team gets the authoring floor so a stored chat-scale profile value cannot
	// truncate a 67-requirement round. The generic agent.max_turns (12) is a
	// chat-scale guard and must not clamp either team.
	if isExecutionProfile(profile.Key) {
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

// isExecutionProfile reports whether the profile belongs to the P3 execution team and
// therefore uses its own case-scale budgets instead of the chat-scale
// agent.max_turns guard.
func isExecutionProfile(key string) bool {
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
	case ProfileKeyDesignHost, ProfileKeyDesignAuthor, ProfileKeyDesignCritic:
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
// run_seconds / max_turns_fuse) onto the v0.22 Budget policy. Legacy P1-P8
// items carry no Budget JSON and fall back to the frozen profile limits. Token
// accounting is telemetry only (R-A2-1): every profile runs with the explicit
// unlimited shape (MaxTokens=0 + UnlimitedTokens=true) and a declared legacy
// max_tokens is recorded without ever terminating the run.
func (r *Runner) resourceBudget(item agent.WorkItem, profile agent.AgentProfile) Budget {
	budget := Budget{
		MaxTokens:       0,
		UnlimitedTokens: true,
		RunSeconds:      r.softSeconds(profile),
		// Reserve the last loop turn for settlement. With the fuse equal to the
		// loop cap the loop exits before Decide can observe the fuse, so the
		// budget policy would never get to ask for a wrap-up turn.
		MaxTurnsFuse: r.turnFuse(profile),
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
		// Kept as the future token-quota hook (R-A2-5); ExceededBy ignores it.
		budget.MaxTokens = declared.MaxTokens
		budget.UnlimitedTokens = false
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
// the seven roles (case_executor, coverage_analyst, knowledge_curator) are not
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

// caseHardTimeoutSeconds is the opt-in hard case timeout (P6, design §4.5).
// execution.case_timeout_seconds=0 (the default) means no hard deadline: the run
// ends through soft winding, the thinking-only stall gate or the defensive loop
// floor, never a hard kill. A positive value enables the orderly hard timeout.
func (r *Runner) caseHardTimeoutSeconds(profile agent.AgentProfile) int {
	if profile.Key != ProfileKeyCaseExecutor {
		return r.hardSeconds(profile)
	}
	return r.intSettingAllowZero("execution.case_timeout_seconds", 0)
}

// runContext derives the run context. P6 removes the hard-kill default for the
// case executor: with the case timeout off (0) the run has no deadline and is
// bounded by soft winding / stall gate / maxLoopIterations; an enabled case
// timeout applies an orderly hard deadline. Other profiles keep their hard
// timeout envelope (the design authoring floor still applies).
func (r *Runner) runContext(ctx context.Context, profile agent.AgentProfile) (context.Context, context.CancelFunc) {
	if profile.Key == ProfileKeyCaseExecutor {
		if seconds := r.caseHardTimeoutSeconds(profile); seconds > 0 {
			return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
		}
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(r.hardSeconds(profile))*time.Second)
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

// intSettingAllowZero is intSetting for keys whose zero value is meaningful
// (an explicit "disabled"), such as tool.retry_max and
// compaction.overflow_retries. A missing/unreadable/negative value falls back.
func (r *Runner) intSettingAllowZero(key string, fallback int) int {
	if r.settings == nil {
		return fallback
	}
	value, err := r.settings.Int(context.Background(), key)
	if err != nil || value < 0 {
		return fallback
	}
	return int(value)
}

// toolRetryMax is the automatic retry budget for retryable tool errors
// (tool.retry_max). Zero disables automatic retries.
func (r *Runner) toolRetryMax() int {
	return r.intSettingAllowZero("tool.retry_max", defaultToolRetryMax)
}

// overflowRetries is the forced-compaction rescue budget after a provider
// context-overflow (compaction.overflow_retries). Zero disables the rescue.
func (r *Runner) overflowRetries() int {
	return r.intSettingAllowZero("compaction.overflow_retries", defaultOverflowRetries)
}

// executeToolWithRetry runs one tool invocation and immediately retries a
// retryable typed tool error up to the configured budget (no backoff). Every
// retry publishes tool.retry with the attempt number and the error so the UI
// can show the automatic retry. Non-retryable errors and a zero budget execute
// exactly once.
func (r *Runner) executeToolWithRetry(ctx context.Context, state *runState, toolName string, execute func() (any, error)) (any, error) {
	maxRetries := r.toolRetryMax()
	payload, err := execute()
	for attempt := 1; attempt <= maxRetries; attempt++ {
		var toolErr *tools.Error
		if !errors.As(err, &toolErr) || !toolErr.Retryable {
			return payload, err
		}
		r.publish(state, "tool.retry", map[string]any{
			"tool":    toolName,
			"attempt": attempt,
			"error":   err.Error(),
		})
		payload, err = execute()
	}
	return payload, err
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
	// R-E3-4/R-E3-5: every embedded image reference/caption is part of the task
	// pack and is charged 1500 tokens (AttachmentImageTokenBudget); the extracted
	// text budget shrinks by the same charge so the combined prompt stays within
	// the intent of the attachment task pack (one token ≈ two CJK runes).
	textRuneBudget := scoutAttachmentTextRunes
	if reserve := info.ImageTokens * 2; reserve > 0 {
		if reserve >= textRuneBudget {
			textRuneBudget = textRuneBudget / 4
		} else {
			textRuneBudget -= reserve
		}
	}
	text := truncateRunes(strings.TrimSpace(info.Text), textRuneBudget)
	header := fmt.Sprintf("请阅读附件《%s》（%s，共 %d 字符）并总结要点：\n%s", info.Name, info.MIME, info.TotalChars, text)
	if len(info.Images) == 0 {
		return header
	}
	var builder strings.Builder
	builder.WriteString(header)
	builder.WriteString(fmt.Sprintf("\n\n附件内嵌图片（%d 张，image_tokens=%d；字节在内容库，可按 sha256 续读）：",
		len(info.Images), info.ImageTokens))
	for _, image := range info.Images {
		line := fmt.Sprintf("\n- image_id=%d seq=%d status=%s mime=%s", image.ID, image.Seq,
			strings.Join(strings.Fields(image.Status), " "), strings.Join(strings.Fields(image.MIME), " "))
		if image.Width > 0 && image.Height > 0 {
			line += fmt.Sprintf(" %dx%d", image.Width, image.Height)
		}
		if image.SHA256 != "" {
			line += " sha256=" + image.SHA256
		}
		if caption := strings.TrimSpace(image.Caption); caption != "" {
			line += " caption=" + truncateRunes(boundPartText(caption, producedPartStringMax), producedPartStringMax)
		}
		if image.Error != "" {
			line += " error=" + truncateRunes(boundPartText(image.Error, producedPartStringMax), producedPartStringMax)
		}
		builder.WriteString(line)
	}
	return builder.String()
}

func versionPackResult(pack VersionPack) map[string]any {
	return map[string]any{
		"version_id":   idText(pack.VersionID),
		"title":        pack.Title,
		"status":       pack.Status,
		"suts":         pack.SUTs,
		"pages":        pack.Pages,
		"apis":         pack.APIs,
		"databases":    pack.Databases,
		"requirements": pack.Requirements,
		"attachments":  pack.Attachments,
		"knowledge":    knowledgeRefsResult(pack.Knowledge),
	}
}

func taskPackResult(versionID, conversationID, runID int64, task TaskContext, execution *ExecutionTask, deliveryTask any) map[string]any {
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
	if deliveryTask != nil {
		result["delivery_task"] = deliveryTask
	}
	return result
}

// executionTaskResult renders the P3 execution snapshot for context.task_pack.
// The one-time human response stays only in the context packet: the tool result
// is persisted in the agent transcript and must not duplicate a sensitive code.
//
// v0.2.4-F T12 (SC-F1): the natural-language contract fields (sut_description /
// covered_acs / per-step target_description, SUT intro / environment_id) are
// rendered here in lockstep with the executionBlock prompt segment. Steps carry
// NO design-supplied target SUT/page/API id (the design columns are gone);
// prior steps/assertions keep target_sut_id as FACTUAL attribution (the SUT a
// recorded result actually ran against — resume needs it), and assertions keep
// the design's live case_assertion.sut_id.
func executionTaskResult(task ExecutionTask) map[string]any {
	suts := make([]map[string]any, 0, len(task.SUTs))
	for _, sut := range task.SUTs {
		suts = append(suts, map[string]any{
			"sut_id":           idText(sut.SUTID),
			"sut_name":         sut.SUTName,
			"intro":            sut.Intro,
			"environment_id":   idText(sut.EnvironmentID),
			"environment_name": sut.EnvironmentName,
			"base_url":         sut.BaseURL,
			"page_urls":        sut.PageURLs,
			"api_endpoints":    sut.APIEndpoints,
			"account_roles":    sut.AccountRoles,
		})
	}
	steps := make([]map[string]any, 0, len(task.Steps))
	for _, step := range task.Steps {
		steps = append(steps, map[string]any{
			"seq":                step.Seq,
			"action":             step.Action,
			"expected":           step.Expected,
			"account_role":       step.AccountRole,
			"target_description": step.TargetDescription,
		})
	}
	assertions := make([]map[string]any, 0, len(task.Assertions))
	for _, assertion := range task.Assertions {
		entry := map[string]any{
			"step_seq":      assertion.StepSeq,
			"assertion_seq": assertion.AssertionSeq,
			"kind":          assertion.Kind,
			"expected":      assertion.Expected,
			"target_sut_id": idText(assertion.TargetSUTID),
		}
		assertions = append(assertions, entry)
	}
	priorSteps := make([]map[string]any, 0, len(task.PriorSteps))
	for _, prior := range task.PriorSteps {
		priorSteps = append(priorSteps, map[string]any{
			"seq":           prior.Seq,
			"status":        prior.Status,
			"observed":      prior.Observed,
			"error":         prior.Error,
			"target_sut_id": idText(prior.TargetSUTID),
		})
	}
	priorAssertions := make([]map[string]any, 0, len(task.PriorAssertions))
	for _, prior := range task.PriorAssertions {
		priorAssertions = append(priorAssertions, map[string]any{
			"step_seq":      prior.StepSeq,
			"assertion_seq": prior.AssertionSeq,
			"kind":          prior.Kind,
			"verdict":       prior.Verdict,
			"expected":      prior.Expected,
			"actual":        prior.Actual,
			"reason":        prior.Reason,
			"target_sut_id": idText(prior.TargetSUTID),
		})
	}
	return map[string]any{
		"case_run_id":      idText(task.CaseRunID),
		"wave_id":          idText(task.WaveID),
		"intent_id":        idText(task.IntentID),
		"case_version_id":  idText(task.CaseVersionID),
		"environment_id":   idText(task.EnvironmentID),
		"case_title":       task.CaseTitle,
		"description":      task.Description,
		"sut_description":  task.SutDescription,
		"preconditions":    task.Preconditions,
		"covered_acs":      task.CoveredACs,
		"priority":         task.Priority,
		"suts":             suts,
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
	// I-B4 judge independence: a design.judge run carries no conversation-scoped
	// artifact ref. H-F10b #32 extended the gate from conversation_id to
	// conversation_message and attachment: the judge payload has none of them
	// today, but a ref is a read handle, so the exclusion must cover every
	// conversation-scoped kind rather than only the one that happened to leak.
	if state.payload.ConversationID > 0 && !isDesignJudgeWorkType(WorkTypeForItem(state.item)) {
		refs = append(refs, ArtifactRef{Kind: "conversation", ID: idText(state.payload.ConversationID)})
	}
	if state.payload.MessageID > 0 && !isDesignJudgeWorkType(WorkTypeForItem(state.item)) {
		refs = append(refs, ArtifactRef{Kind: "conversation_message", ID: idText(state.payload.MessageID)})
	}
	if state.payload.AttachmentID > 0 && !isDesignJudgeWorkType(WorkTypeForItem(state.item)) {
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
// delivery profiles use the delivery room, and every P1 profile keeps the
// clarification team room. GD-17 removed the steward room with the Steward profile.
func teamForProfile(profileKey string) agent.Team {
	switch profileKey {
	case ProfileKeyDesignHost, ProfileKeyDesignAuthor, ProfileKeyDesignCritic:
		return agent.TeamDesign
	case ProfileKeyCaseExecutor, ProfileKeyFailureAnalyst, ProfileKeyCoverageAnalyst:
		return agent.TeamExecution
	case ProfileKeyIssueTriager, ProfileKeyReportWriter:
		return agent.TeamDelivery
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

// isDesignJudgeWorkType reports whether the work item is the independent
// design judge run (v0.2.4-E quality gate, I-B4). Its context assembly must
// force out every channel that could carry the design author's chat or
// reasoning: the design conversation, the design team room, and the
// clarification→design handoff briefing group. The judge reads the current
// requirement/case facts directly through requirement.read/design.graph_read
// instead. design.rework is deliberately NOT included: it reuses the
// design_author profile and legitimately needs the judge's findings to revise
// the case (SC-E4).
//
// Every call site passes WorkTypeForItem(item) — the single effective-work-type
// convention (kind authoritative when present) — never the raw Type column
// (T14 fix round 1, review Minor#3).
func isDesignJudgeWorkType(workType string) bool {
	return workType == WorkTypeDesignJudge
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
// payload CHECK are the last line of defense; the runner bounds the part before
// it leaves the run so a tool result can never bloat a message. GD-01 (Q05=B):
// credentials are plaintext by design and stay verbatim in the trace — these
// bounds are size/depth limits, not redaction.
const (
	producedPartThinkingMax = 4000
	producedPartJSONMax     = 4000
	producedPartHeadMax     = 2000
	producedPartStringMax   = 500
	producedPartMaxDepth    = 8
	maxProducedStreamParts  = 200
)

// appendStreamPart records one produced streaming part, bounded by the message
// limit. A dropped part never fails the run (the model reply is the artifact,
// the trace is supplementary), but the drop is never silent (constitution
// VII.6): the first refusal of a run is logged here at the drop point, so the
// profiles that never render the trace (case executor, failure/coverage
// analysts, background design jobs) still surface it; the render site adds the
// user-facing error part and the final tally.
func (r *Runner) appendStreamPart(state *runState, part ConversationPart) {
	if state == nil {
		return
	}
	if len(state.streamParts) >= maxProducedStreamParts {
		state.droppedStreamParts++
		if state.droppedStreamParts == 1 && r.logger != nil {
			r.logger.Warn("produced stream parts exceeded the message limit; dropping further trace parts",
				"run_id", state.run.ID, "profile", state.profile.Key, "limit", maxProducedStreamParts)
		}
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
// bounded first: strings bounded, result object-shaped (GD-01: credentials stay
// plaintext, no redaction).
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
		errorText = boundPartText(toolErr.Error(), version.ToolCallErrorMax)
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
	r.recordToolCallPart(state, callID, data)

	var typed *tools.Error
	if toolErr == nil || !errors.As(toolErr, &typed) {
		return
	}
	code := truncateRunes(strings.TrimSpace(typed.Code), version.MessageErrorCodeMax)
	message := boundPartText(typed.Message, version.MessageErrorMessageMax)
	if code == "" || message == "" || state.hasErrorPart(code, message) {
		return
	}
	// v0.22 fix round 1: the structured retry metadata of the typed tool error
	// is forwarded into the error part so the UI can explain how to recover
	// (which field to fix, which values are allowed, the next step). Every
	// value is bounded by the version contract; `error` parts are persisted in
	// the conversation snapshot and streamed to the browser.
	errorData := map[string]any{
		"code":      code,
		"message":   message,
		"retryable": typed.Retryable,
	}
	if field := truncateRunes(strings.TrimSpace(typed.Field), version.MessageErrorFieldMax); field != "" {
		errorData["field"] = field
	}
	if len(typed.Allowed) > 0 {
		allowed := make([]string, 0, len(typed.Allowed))
		for _, value := range typed.Allowed {
			if len(allowed) == version.MessageErrorAllowedMax {
				break
			}
			value = truncateRunes(strings.TrimSpace(value), version.MessageErrorAllowedItemMax)
			if value != "" {
				allowed = append(allowed, value)
			}
		}
		if len(allowed) > 0 {
			errorData["allowed"] = allowed
		}
	}
	if hint := boundPartText(typed.Hint, version.MessageErrorHintMax); hint != "" {
		errorData["hint"] = hint
	}
	r.appendStreamPart(state, ConversationPart{Type: "error", Data: errorData})
}

// assistantConversationParts renders the parts of one final assistant message:
// the produced trace parts (thinking/tool_call/error), the final text and the
// profile-specific extra cards. Order is the render order. The background job's
// own card is not part of this message: it lives in the dedicated card message
// written by projectJobCard, so one job never renders two job_cards.
func (r *Runner) assistantConversationParts(state *runState, finalText string, extra []ConversationPart) []ConversationPart {
	parts := make([]ConversationPart, 0, len(state.streamParts)+1+len(extra))
	parts = append(parts, state.streamParts...)
	if state.droppedStreamParts > 0 {
		// Explicit truncation signal (constitution VII.6): the cap refused
		// produced parts, so the rendered trace says so instead of silently
		// looking complete. The drop point already logged the onset for every
		// profile; this is the user-facing surface plus the final tally.
		if r.logger != nil {
			r.logger.Warn("rendered trace is truncated by the produced-part limit",
				"run_id", state.run.ID, "profile", state.profile.Key,
				"dropped_parts", state.droppedStreamParts, "limit", maxProducedStreamParts)
		}
		parts = append(parts, ConversationPart{Type: "error", Data: map[string]any{
			"code":          streamPartsTruncatedCode,
			"message":       fmt.Sprintf("执行过程记录超出单条消息上限（%d 条），后续 %d 条过程记录未展示；最终回复不受影响。", maxProducedStreamParts, state.droppedStreamParts),
			"retryable":     false,
			"dropped_parts": state.droppedStreamParts,
		}})
	}
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
// then the error text are used. Both fields are bounded (GD-01: credentials stay
// plaintext) by the MessagePart contract before they reach the conversation store.
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
	failure.Reason = boundPartText(strings.TrimSpace(failure.Reason), version.MessageErrorMessageMax)
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
// conversation-writing work type (chat.turn/requirement.curate; design.round is
// a historical read-only value, not in the write set). Anything else has no card.
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

// boundPartText bounds free text to max runes. GD-01 (Q05=B): credentials are
// plaintext by design, so the text is never redacted — only a size bound applies.
func boundPartText(text string, max int) string {
	if max > 0 {
		return truncateRunes(text, max)
	}
	return text
}

// sanitizePartJSONObject bounds and shapes a model-supplied JSON object (tool
// call arguments); GD-01 (Q05=B): credentials stay plaintext, nothing is masked.
// Malformed or non-object input is omitted instead of failing the run; the model
// already sees the raw value through the tool channel.
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
	return marshalSanitizedPartJSON(boundPartValue(object, 0))
}

// sanitizePartResult bounds and shapes a tool result (GD-01: credentials stay
// plaintext). The contract result is an object or null, so a scalar tool result
// is wrapped under "value".
func sanitizePartResult(content string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, false
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return marshalSanitizedPartJSON(map[string]any{"value": boundPartText(trimmed, producedPartStringMax)})
	}
	sanitized := boundPartValue(value, 0)
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

// boundPartValue walks a decoded JSON value, bounding every string and the
// depth/size of the walk so a hostile tool result cannot explode. GD-01
// (Q05=B): credential-keyed fields and credential-shaped strings are kept
// verbatim (plaintext); no field is masked.
func boundPartValue(value any, depth int) any {
	if depth > producedPartMaxDepth {
		return "[truncated]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = boundPartValue(item, depth+1)
		}
		return out
	case []any:
		items := typed
		if len(items) > 100 {
			items = items[:100]
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, boundPartValue(item, depth+1))
		}
		return out
	case string:
		return boundPartText(typed, producedPartStringMax)
	default:
		return value
	}
}
