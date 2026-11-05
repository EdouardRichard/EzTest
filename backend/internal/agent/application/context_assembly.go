package agentapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// contextAssembly is one rendered cache-first round. Packet is the compacted
// (boundary) or append-only packet the runner persists; Messages is the exact
// model transcript derived from it.
type contextAssembly struct {
	Packet       agentcontext.ContextPacket
	Messages     []llmapp.ChatMessage
	Budget       int
	Compacted    bool
	BeforeTokens int
	AfterTokens  int
	// Decision is the policy-aware compaction decision actually applied at a
	// boundary (pressure/overflow/stage_boundary). Nil for a normal round.
	Decision *agentcontext.CompactionDecision
}

// assembleContext renders the cache-first prompt of one run round.
//
// It delegates the layout to agentcontext.Assembler: the stable prefix is built
// from the role prompt, version invariants and stage, while the version
// briefing, facts, rolling summary and transcript are append-only suffix
// segments. A declared invalidation boundary compacts the packet before it is
// persisted; a normal round never compacts and never rewrites history. When the
// assembled packet is over the resolved context threshold the round itself is a
// window_full boundary, so compaction happens exactly at a boundary.
func (r *Runner) assembleContext(
	ctx context.Context,
	state *runState,
	previous *agentcontext.ContextPacket,
	invalidation agentcontext.Invalidation,
) (contextAssembly, error) {
	return r.assembleContextWithKeepRecent(ctx, state, previous, invalidation, -1)
}

// assembleContextWithKeepRecent is assembleContext with an explicit compaction
// recent window. keepRecent < 0 uses context.keep_recent_messages; a
// non-negative value overrides it for this assembly only (the overflow rescue
// path shrinks the window after the first retry).
func (r *Runner) assembleContextWithKeepRecent(
	ctx context.Context,
	state *runState,
	previous *agentcontext.ContextPacket,
	invalidation agentcontext.Invalidation,
	keepRecent int,
) (contextAssembly, error) {
	return r.assembleContextWithDecision(ctx, state, previous, invalidation, keepRecent, nil)
}

// assembleContextWithDecision is the v0.22 compaction-aware assembly. A non-nil
// decision is the policy-aware trigger the caller already resolved (overflow /
// stage_boundary / pressure); nil derives the boundary decision from the run's
// resolved compaction policy. Compaction uses Compactor.CompactForDecision, so
// the retain budget comes from the policy instead of the retired count-only
// legacy Compact path.
func (r *Runner) assembleContextWithDecision(
	ctx context.Context,
	state *runState,
	previous *agentcontext.ContextPacket,
	invalidation agentcontext.Invalidation,
	keepRecent int,
	decision *agentcontext.CompactionDecision,
) (contextAssembly, error) {
	builder := r.builder
	if builder == nil {
		builder = NewContextBuilder(r.settings, nil)
	}
	assembler := r.assembler
	if assembler == nil {
		assembler = agentcontext.NewAssembler()
	}
	budget, threshold, _ := builder.resolveBudget(ctx, state.input)

	briefing, stage := r.versionBriefing(ctx, state)
	// Task 80: the stable prefix is a function of the profile prompt, the
	// version invariants and the stage only. The per-work-item task context
	// (goal/constraints/acceptance/scope, work-item/requirement/case ids and the
	// P3 execution snapshot) is sent through the user suffix by userMessage, so
	// every work item of the same profile/stage/version shares one cached
	// provider prefix.
	request := agentcontext.AssemblyRequest{
		System:       stableSystemContent(state.input),
		Invariants:   versionInvariantLines(state.input.VersionPack),
		Stage:        stage,
		Invalidation: invalidation,
		Previous:     previous,
		Briefing:     briefing,
		Facts:        contextFactBlocks(state.input),
		History:      state.input.Conversation.RecentMessages,
	}
	built, err := assembler.Build(ctx, request)
	if err != nil {
		return contextAssembly{}, err
	}
	result := contextAssembly{
		Packet:       built.Packet,
		Budget:       budget,
		BeforeTokens: contextPacketTokens(built.Packet),
		AfterTokens:  contextPacketTokens(built.Packet),
	}

	// The runner appends the current user turn after the assembled packet, so
	// the packet token count alone understates the model prompt. Task 80 moved
	// the per-work-item task context into that turn: count it in the window
	// decision, or a prompt pushed over the window by its task context would
	// never trigger the proactive boundary. The packet bookkeeping stays
	// packet-only; this is the same conservative estimate used for the budget.
	userTokens := EstimateTokens(state.userContent)
	if invalidation == agentcontext.InvalidationNone && decision == nil &&
		float64(result.BeforeTokens+userTokens) > threshold*float64(budget) {
		// The window is nearly full: this round is a declared boundary.
		invalidation = agentcontext.InvalidationWindowFull
	}
	if invalidation != agentcontext.InvalidationNone || decision != nil {
		// Rebuild at the boundary, carrying the persisted rolling summary so the
		// conversation memory accumulates instead of being replaced by the
		// summary of this round only.
		request.Invalidation = invalidation
		request.Previous = withConversationSummary(previous, state.input.Conversation.Summary)
		// R-A5-9: the pinned task context is rebuilt from state.input and
		// re-injected independently of the summary. The block lands in the
		// pinned facts segment, ahead of the rolling summary and the retained
		// recent turns, so compaction can never fold the goal, constraints,
		// acceptance criteria, scope or ids into the summary. The pinned facts
		// themselves are already rendered by contextFactBlocks, so the prompt
		// variant omits them and each fact is carried exactly once.
		if reinjection := assembleAfterCompactionForPrompt(newCompactionState(state)); reinjection != "" {
			request.Facts = append(request.Facts, reinjection)
		}
		rebuilt, rebuildErr := assembler.Build(ctx, request)
		if rebuildErr != nil {
			return contextAssembly{}, rebuildErr
		}
		result.Packet = rebuilt.Packet
		result.BeforeTokens = contextPacketTokens(rebuilt.Packet)
		resolved := agentcontext.CompactionDecision{}
		if decision != nil {
			resolved = *decision
		} else {
			resolved = r.boundaryCompactionDecision(state, invalidation, result.BeforeTokens+userTokens)
		}
		compactor := r.compactionCompactor(ctx, state, keepRecent, resolved)
		compacted, compactErr := compactor.CompactForDecision(ctx, result.Packet, resolved)
		if compactErr != nil {
			return contextAssembly{}, compactErr
		}
		result.Compacted = contextPacketFolded(result.Packet, compacted)
		result.Packet = compacted
		result.AfterTokens = contextPacketTokens(compacted)
		result.Decision = &resolved
	}
	result.Messages = contextPacketMessages(result.Packet)
	return result, nil
}

// boundaryCompactionDecision maps an assembly boundary to the v0.22 trigger set
// when the caller did not already resolve a policy-aware decision. A stage
// boundary forces the stage_boundary trigger; a window_full boundary is the
// pressure trigger (with a forced decision when the threshold check itself did
// not fire, so a pre-run overflow still compacts).
func (r *Runner) boundaryCompactionDecision(state *runState, invalidation agentcontext.Invalidation, promptTokens int) agentcontext.CompactionDecision {
	policy := state.compactionPolicy
	if policy.Trigger <= 0 {
		policy = agentcontext.NewCompactionPolicy(state.input.ModelBudget.ContextWindow, state.input.ModelBudget.MaxOutput)
	}
	if invalidation == agentcontext.InvalidationStageBoundary || invalidation == agentcontext.InvalidationSessionReset {
		return policy.StageBoundaryDecision()
	}
	if decision, ok := policy.PressureDecision(0, promptTokens); ok {
		return decision
	}
	return agentcontext.CompactionDecision{
		Trigger: agentcontext.CompactionTriggerPressure,
		Policy:  policy,
		Retain:  policy.Retain,
	}
}

// withConversationSummary seeds the boundary rebuild with the persisted rolling
// summary as a prior summary segment, so compaction merges the older summary
// with the newly folded transcript (the P1 builder merged summaries the same
// way). A previous packet that already carries a summary is used unchanged.
func withConversationSummary(previous *agentcontext.ContextPacket, summary string) *agentcontext.ContextPacket {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return previous
	}
	packet := agentcontext.ContextPacket{}
	if previous != nil {
		packet = *previous
		for _, segment := range packet.Segments {
			if segment.Name == agentcontext.SegmentSummary {
				return previous
			}
		}
	}
	segments := make([]agentcontext.Segment, 0, len(packet.Segments)+1)
	segments = append(segments, packet.Segments...)
	segments = append(segments, agentcontext.Segment{
		Name:    agentcontext.SegmentSummary,
		Role:    "user",
		Content: summary,
	})
	packet.Segments = segments
	return &packet
}

// versionBriefing renders the derived version briefing (spec §6.2) and returns
// its stage. A read failure is supplemental: the run continues with the version
// pack facts and the status fallback stage, and the failure is logged.
func (r *Runner) versionBriefing(ctx context.Context, state *runState) (string, string) {
	stage := strings.TrimSpace(state.input.VersionPack.Status)
	if r.briefing == nil {
		return "", stage
	}
	briefing, err := r.briefing.BuildBriefing(ctx, state.item.VersionID)
	if err != nil {
		r.logContextWarning(ctx, "version briefing read failed; continuing without it", err)
		return "", stage
	}
	if briefingStage := strings.TrimSpace(briefing.Stage); briefingStage != "" {
		stage = briefingStage
	}
	return briefing.Render(), stage
}

// versionInvariantLines renders the stable version invariants of the prefix:
// the version goal (its title) and the requirement baseline. Dynamic situation
// data belongs to the briefing suffix, never here.
func versionInvariantLines(pack VersionPack) []string {
	lines := make([]string, 0, len(pack.Requirements)+1)
	if title := strings.TrimSpace(pack.Title); title != "" {
		lines = append(lines, "版本目标: "+title)
	}
	for _, requirement := range pack.Requirements {
		if line := strings.TrimSpace(requirement); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// contextFactBlocks renders the pinned, append-only context material: session
// memory (rolling summary + pinned facts), the version pack, injected
// knowledge, the team-room situation and artifact references. Facts are never
// compacted, so pinned data always survives a boundary.
func contextFactBlocks(in ContextInput) []string {
	blocks := make([]string, 0, 5)
	if memory := conversationText(in.Conversation.Summary, in.Conversation.PinnedFacts, nil); strings.TrimSpace(memory) != "" {
		blocks = append(blocks, "会话记忆:\n"+memory)
	}
	if pack := versionPackContent(in.VersionPack); strings.TrimSpace(pack) != "" {
		blocks = append(blocks, "版本包:\n"+pack)
	}
	if knowledge := knowledgeContent(in.VersionPack.Knowledge); strings.TrimSpace(knowledge) != "" {
		blocks = append(blocks, strings.Replace(knowledge, "# 知识", "知识:", 1))
	}
	if room := teamRoomContent(in.TeamRoom); strings.TrimSpace(room) != "" {
		blocks = append(blocks, room)
	}
	if artifacts := artifactsContent(in.Artifacts); strings.TrimSpace(artifacts) != "" {
		blocks = append(blocks, "引用:\n"+artifacts)
	}
	return blocks
}

// contextPacketMessages replays the packet segments as the model transcript in
// packet order. It is the single render path for both the assembled and the
// compacted packet, so the model always receives exactly the segments the
// persisted packet records (summary + the retained recent N after a boundary).
func contextPacketMessages(packet agentcontext.ContextPacket) []llmapp.ChatMessage {
	messages := make([]llmapp.ChatMessage, 0, len(packet.Segments))
	for _, segment := range packet.Segments {
		messages = append(messages, llmapp.ChatMessage{
			Role:       segment.Role,
			Content:    segment.Content,
			ToolCallID: segment.ToolCallID,
		})
	}
	return messages
}

// contextPacketPrefix returns the persisted stable-prefix content of a packet.
// It is the system message of the rendered transcript.
func contextPacketPrefix(packet agentcontext.ContextPacket) string {
	for _, segment := range packet.Segments {
		if segment.Prefix {
			return segment.Content
		}
	}
	return ""
}

// systemMessageText is the system message of one assembled round: the persisted
// stable prefix, or the stable system content when the packet carries no prefix
// (defensive fallback). Task 80: the per-work-item task context lives in the
// user turn, so the fallback must not render the legacy systemContent (prompt +
// task context) or the block would be sent twice.
func systemMessageText(packet agentcontext.ContextPacket, in ContextInput) string {
	if text := strings.TrimSpace(contextPacketPrefix(packet)); text != "" {
		return text
	}
	return stableSystemContent(in)
}

// contextPacketTokens is the conservative local estimate of one packet.
func contextPacketTokens(packet agentcontext.ContextPacket) int {
	total := 0
	for _, segment := range packet.Segments {
		total += segment.Tokens
	}
	return total
}

// contextPacketHasSummary reports whether a packet carries a rolling summary
// segment (a boundary compaction actually folded history).
func contextPacketHasSummary(packet agentcontext.ContextPacket) bool {
	for _, segment := range packet.Segments {
		if segment.Name == agentcontext.SegmentSummary && strings.TrimSpace(segment.Content) != "" {
			return true
		}
	}
	return false
}

// contextPacketFolded reports whether a boundary compaction actually folded
// transcript segments. It compares the compactable segment counts instead of the
// total segment count: appending the rolling summary in place of exactly one
// folded segment leaves the total unchanged, so a count-only test misses a
// foldCount==1 compaction and the summary would never be persisted.
func contextPacketFolded(before, after agentcontext.ContextPacket) bool {
	if !contextPacketHasSummary(after) {
		return false
	}
	return contextPacketCompactableCount(before) > contextPacketCompactableCount(after)
}

// contextPacketCompactableCount counts the suffix segments a boundary compaction
// may fold. Pinned prefix/briefing/facts segments and derived summaries are not
// counted, so the result decreases exactly by the number of folded segments.
func contextPacketCompactableCount(packet agentcontext.ContextPacket) int {
	count := 0
	for _, segment := range packet.Segments {
		switch segment.Name {
		case agentcontext.SegmentHistory, agentcontext.SegmentToolResult:
			count++
		}
	}
	return count
}

// packetSummary returns the rolling summary segment a compaction appended.
func packetSummary(packet agentcontext.ContextPacket) string {
	summary := ""
	for _, segment := range packet.Segments {
		if segment.Name == agentcontext.SegmentSummary {
			summary = segment.Content
		}
	}
	return strings.TrimSpace(summary)
}

// compactionCompactor wires policy-aware compaction with the configured recent
// window (context.keep_recent_messages, default 12) and the v0.22
// CompactionSummarizer (nine-section summary, prefix reuse, watermark, H5 pinned
// leak filter) with its deterministic fallback.
func (r *Runner) compactionCompactor(ctx context.Context, state *runState, keepRecent int, decision agentcontext.CompactionDecision) *agentcontext.Compactor {
	if keepRecent < 0 {
		keepRecent = r.keepRecentMessages(ctx)
	}
	summarizer := compactionSummaryAdapter{runner: r, state: state, decision: decision}
	return agentcontext.NewCompactor(
		summarizer,
		agentcontext.WithKeepRecent(keepRecent),
		agentcontext.WithLogger(r.logger),
	)
}

// compactionSummaryAdapter bridges the context package's narrow
// []Message summarizer port to the v0.22 CompactionSummarizer (SummaryInput /
// SummaryResult). The folded messages carry their conversation IDs, so the
// adapter computes the R-A5-12 covered_message_id watermark of this transaction.
type compactionSummaryAdapter struct {
	runner   *Runner
	state    *runState
	decision agentcontext.CompactionDecision
}

func (a compactionSummaryAdapter) Summarize(ctx context.Context, messages []agentcontext.Message) (string, error) {
	state := a.state
	if state == nil {
		return "", errors.New("compaction summary: run state is not configured")
	}
	covered := int64(0)
	replay := make([]SummaryReplayMessage, 0, len(messages))
	images := 0
	for _, message := range messages {
		if message.ID > covered {
			covered = message.ID
		}
		replay = append(replay, SummaryReplayMessage{
			ID: message.ID, Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID,
		})
	}
	policy := a.decision.Policy
	if policy.Trigger <= 0 && policy.Usable <= 0 {
		policy = state.compactionPolicy
	}
	compactionID := state.lastCompactionID
	if strings.TrimSpace(compactionID) == "" {
		compactionID = fmt.Sprintf("compaction-run-%d", state.run.ID)
	}
	input := SummaryInput{
		ConversationID:           state.payload.ConversationID,
		VersionID:                state.item.VersionID,
		CompactionID:             compactionID,
		Trigger:                  a.decision.Trigger,
		PreviousCoveredMessageID: 0,
		CoveredMessageID:         covered,
		StablePrefix:             stableSystemContent(state.input),
		Messages:                 replay,
		ImageCount:               images,
		Task:                     state.input.Task,
		WorkItemID:               state.item.ID,
		Plan:                     nil,
		Facts:                    nil,
		PinnedFacts:              state.input.Conversation.PinnedFacts,
		PriorSummary:             state.input.Conversation.Summary,
		Policy:                   policy,
	}
	summarizer := NewCompactionSummarizer(a.runner.chat, WithSummaryLogger(a.runner.logger))
	result := summarizer.Summarize(ctx, input)
	state.lastCompactionSummary = result
	state.lastCompactedMessageID = covered
	if strings.TrimSpace(result.Text) == "" {
		return "", errors.New("compaction summary is empty")
	}
	return result.Text, nil
}

// keepRecentMessages resolves context.keep_recent_messages. Unlike the generic
// intSetting helper, zero is a valid configured window (fold everything).
func (r *Runner) keepRecentMessages(ctx context.Context) int {
	if r.settings == nil {
		return DefaultKeepRecentMessages
	}
	value, err := r.settings.Int(ctx, "context.keep_recent_messages")
	if err != nil || value < 0 {
		return DefaultKeepRecentMessages
	}
	return int(value)
}

// resolveCompactionPolicy resolves the R-A5-1 policy of this run: the effective
// model window (or the H2 128000 fallback) and max_output, overridden by the
// seeded compaction.* settings and validated. An invalid configuration falls
// back to the derived default policy and is logged instead of failing the run.
func (r *Runner) resolveCompactionPolicy(ctx context.Context, state *runState) agentcontext.CompactionPolicy {
	window := state.input.ModelBudget.ContextWindow
	if window <= 0 && r.settings != nil {
		if value, err := r.settings.Int(ctx, "context.window_tokens"); err == nil && value > 0 {
			window = int(value)
		}
	}
	output := state.input.ModelBudget.MaxOutput
	policy := agentcontext.NewCompactionPolicy(window, output)
	if r.settings != nil {
		usable := policy.Usable
		if ratio, err := r.settings.Float(ctx, "compaction.threshold_ratio"); err == nil && ratio > 0 && ratio <= 1 {
			policy.Trigger = int(float64(usable) * ratio)
		}
		if ratio, err := r.settings.Float(ctx, "compaction.retain_ratio"); err == nil && ratio >= 0 && ratio <= 1 {
			policy.Retain = int(float64(usable) * ratio)
		}
		if max, err := r.settings.Int(ctx, "compaction.summary_max_tokens"); err == nil && max > 0 {
			if max > agentcontext.MaxCompactionSummaryTokens {
				max = agentcontext.MaxCompactionSummaryTokens
			}
			policy.SummaryMax = int(max)
		}
	}
	if err := policy.Validate(); err != nil {
		r.logContextWarning(ctx, "compaction policy is invalid; using the derived default", err)
		return agentcontext.NewCompactionPolicy(window, output)
	}
	return policy
}

// persistCompactionSummary writes the R-A5-12 summary revision of one completed
// compaction transaction. It returns the stored revision (0 when persistence is
// unavailable or fails; a failure is logged and never fails the run).
func (r *Runner) persistCompactionSummary(ctx context.Context, state *runState, summary string, covered int64, trigger agentcontext.CompactionTrigger, meta map[string]any) int {
	if r.compactionSummary == nil || summary == "" || state.payload.ConversationID <= 0 {
		return 0
	}
	if meta == nil {
		meta = map[string]any{"trigger": string(trigger)}
	}
	commit := SummaryCommit{
		VersionID:        state.item.VersionID,
		ConversationID:   state.payload.ConversationID,
		CompactionID:     state.lastCompactionID,
		Trigger:          trigger,
		Summary:          summary,
		PinnedFacts:      capPinnedFacts(state.input.Conversation.PinnedFacts),
		CoveredMessageID: covered,
		SummaryMeta:      meta,
		TokenEstimate:    EstimateSummaryTokens(summary),
	}
	stored, err := r.compactionSummary.AppendSummary(ctx, commit)
	if err != nil {
		r.logContextWarning(ctx, "persist compaction summary failed", err)
		return 0
	}
	return stored.Version
}

// recordContextCompaction publishes the extended R-A5-15 context.compacted
// payload (trigger/before/after/retain/summary_version/covered_message_id/
// cached) after a compaction transaction. The summary is persisted first when
// this path completed outside a manager transaction.
func (r *Runner) recordContextCompaction(ctx context.Context, state *runState, assembly contextAssembly, persistSummary bool) {
	if assembly.Decision == nil {
		return
	}
	trigger := assembly.Decision.Trigger
	retain := assembly.Decision.Retain
	summaryVersion := 0
	covered := state.lastCompactedMessageID
	if persistSummary && assembly.Compacted {
		summary := packetSummary(assembly.Packet)
		if summary != "" {
			meta := map[string]any{
				"trigger":       string(trigger),
				"truncated":     state.lastCompactionSummary.Truncated,
				"used_fallback": state.lastCompactionSummary.UsedFallback,
			}
			summaryVersion = r.persistCompactionSummary(ctx, state, summary, covered, trigger, meta)
		}
	}
	event := agentcontext.CompactionEvent{
		Trigger:          trigger,
		BeforeTokens:     assembly.BeforeTokens,
		AfterTokens:      assembly.AfterTokens,
		RetainTokens:     retain,
		SummaryVersion:   summaryVersion,
		CoveredMessageID: covered,
		CachedTokens:     state.providerCacheTokens,
	}
	r.publishCompactionEvent(state, event)
}

// publishCompactionEvent publishes the version-channel context.compacted event
// with the runner's run/conversation scope.
func (r *Runner) publishCompactionEvent(state *runState, event agentcontext.CompactionEvent) {
	payload := event.Payload()
	payload["run_id"] = idText(state.run.ID)
	if state.payload.ConversationID > 0 {
		payload["conversation_id"] = idText(state.payload.ConversationID)
	}
	r.publish(state, agentcontext.EventContextCompacted, payload)
}

// compactContext runs one policy-aware compaction transaction (R-A5-10) for the
// pressure/overflow triggers. On a recoverable stop (busy/thrashing/failure)
// the run keeps its original context; only an unrecoverable overflow is
// returned as a terminal error.
//
// A decision only replaces the live context when the compacted packet actually
// folded transcript (result.Compacted). A no-op decision leaves state.messages
// untouched and persists/publishes nothing: it must not write a duplicate
// summary revision nor emit a before==after context.compacted event (fix round
// 2).
func (r *Runner) compactContext(ctx context.Context, state *runState, decision agentcontext.CompactionDecision, keepRecent int) error {
	previous := state.contextPacket
	var assembly contextAssembly
	operation := func(operationCtx context.Context, transaction agentcontext.CompactionTransaction) (agentcontext.CompactionOutcome, error) {
		state.lastCompactionID = transaction.ID
		// The summary bookkeeping below belongs to this transaction only: a
		// no-op attempt must not inherit a previous compaction's watermark.
		state.lastCompactionSummary = SummaryResult{}
		state.lastCompactedMessageID = 0
		result, err := r.assembleContextWithDecision(operationCtx, state, &previous,
			agentcontext.InvalidationWindowFull, keepRecent, &decision)
		if err != nil {
			return agentcontext.CompactionOutcome{}, err
		}
		assembly = result
		// Persist a new rolling-summary revision only when the compacted packet
		// really folded transcript. A no-op rebuild also carries the persisted
		// summary segment, so packetSummary alone cannot decide this.
		covered := int64(0)
		summaryVersion := 0
		if result.Compacted {
			covered = state.lastCompactedMessageID
			if summary := packetSummary(result.Packet); summary != "" {
				meta := state.lastCompactionSummary.Commit(SummaryInput{
					VersionID:        state.item.VersionID,
					ConversationID:   state.payload.ConversationID,
					CompactionID:     transaction.ID,
					Trigger:          decision.Trigger,
					CoveredMessageID: covered,
					PinnedFacts:      state.input.Conversation.PinnedFacts,
					Policy:           decision.Policy,
				}).SummaryMeta
				summaryVersion = r.persistCompactionSummary(operationCtx, state, summary, covered, decision.Trigger, meta)
			}
		}
		return agentcontext.CompactionOutcome{
			BeforeTokens:     result.BeforeTokens,
			AfterTokens:      result.AfterTokens,
			RetainTokens:     decision.Retain,
			SummaryVersion:   summaryVersion,
			CoveredMessageID: covered,
			CachedTokens:     state.providerCacheTokens,
		}, nil
	}
	var outcome agentcontext.CompactionOutcome
	var err error
	if state.payload.ConversationID <= 0 {
		// A run without a conversation (for example an attachment ingest task)
		// has no per-conversation lock or summary to persist: run the operation
		// directly so the context can still be compacted.
		outcome, err = operation(ctx, agentcontext.CompactionTransaction{ID: state.lastCompactionID})
	} else {
		outcome, err = r.compactionManager.RunDecision(ctx, state.payload.ConversationID, decision, operation)
	}
	if err != nil {
		if agentcontext.IsRecoverableCompactionError(err) {
			r.logContextWarning(ctx, "context compaction stopped; keeping the original context", err)
			return nil
		}
		return compactionRunError(err)
	}
	if assembly.Decision == nil || !assembly.Compacted {
		// No-op decision: nothing was folded, so the live transcript, the
		// persisted packet chain and the summary ledger stay exactly as they
		// were. Applying the rebuilt packet here would drop the run's in-flight
		// assistant/tool messages and publish a before==after event.
		return nil
	}
	// The manager publishes its own event when a publisher is wired; the runner
	// publishes the extended payload exactly once so the run/conversation scope
	// and cached_tokens are present in production. A no-op decision returned
	// above, so before/after are always a real reduction here.
	event := agentcontext.CompactionEvent{
		Trigger:          decision.Trigger,
		BeforeTokens:     outcome.BeforeTokens,
		AfterTokens:      outcome.AfterTokens,
		RetainTokens:     outcome.RetainTokens,
		SummaryVersion:   outcome.SummaryVersion,
		CoveredMessageID: outcome.CoveredMessageID,
		CachedTokens:     state.providerCacheTokens,
	}
	r.publishCompactionEvent(state, event)
	if err := r.applyCompactedAssembly(ctx, state, assembly); err != nil {
		return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
	}
	return nil
}

// compactionRunError classifies a failed compaction transaction instead of
// reporting every failure as an unrecoverable context overflow (fix round 2):
// a canceled run is aborted/interrupted, a prefix-cache violation or an
// unexpected transaction failure is a retryable tool failure (a fresh attempt
// can rebuild the chain), and an invalid policy/decision is a terminal payload
// error. Only the R-A5-11 unrecoverable overflow keeps the dedicated terminal
// reason.
func compactionRunError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return contextRunError(err)
	}
	if errors.Is(err, agentcontext.ErrContextOverflowUnrecoverable) {
		return &RunError{Reason: "context_overflow_unrecoverable", Retryable: false, Cause: err}
	}
	var apiErr *platform.Error
	if errors.Is(err, agentcontext.ErrInvalidCompactionPolicy) ||
		(errors.As(err, &apiErr) && apiErr.Code == "invalid_argument") {
		return &RunError{Reason: "invalid_payload", Retryable: false, Cause: err}
	}
	return &RunError{Reason: "tool_failure", Retryable: true, Cause: err}
}

// applyCompactedAssembly installs a successful compaction result in the run:
// persist the packet, replay the compacted transcript and refresh the prompt
// size bookkeeping. Only the packet base and the current user turn are rebuilt
// from the compacted packet; the run's in-flight assistant/tool messages (not
// part of the conversation snapshot the packet replays) are re-appended in
// order after it, so tool calls and their results are never lost.
func (r *Runner) applyCompactedAssembly(ctx context.Context, state *runState, assembly contextAssembly) error {
	inflightStart := state.promptBase
	if inflightStart < 0 || inflightStart > len(state.messages) {
		inflightStart = len(state.messages)
	}
	inflight := append([]llmapp.ChatMessage(nil), state.messages[inflightStart:]...)
	state.contextPacket = assembly.Packet
	saved, err := r.repo.SaveContextPacket(ctx, EncodeContextPacket(state.run.ID, state.item.VersionID, assembly.Packet))
	if err != nil {
		return err
	}
	state.packet = saved
	state.run.ContextPacketID = &saved.ID
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
	state.messages = append(state.messages, inflight...)
	state.promptBase = len(state.messages) - len(inflight)
	if err := r.appendTranscript(ctx, state, agent.MessageRoleSystem,
		systemMessageText(assembly.Packet, state.input), nil, nil); err != nil {
		return err
	}
	state.lastPromptTokens = assembly.AfterTokens
	state.pendingNewTokens = 0
	return nil
}

// currentUserChatMessage appends the triggering user turn (and its images) to
// the assembled transcript.
func currentUserChatMessage(state *runState, userContent string) llmapp.ChatMessage {
	message := llmapp.ChatMessage{Role: "user", Content: userContent}
	if len(state.images) > 0 {
		message.Images = append([]llmapp.ChatImage(nil), state.images...)
	}
	return message
}

// logContextWarning reports a supplemental context failure without failing the
// run.
func (r *Runner) logContextWarning(ctx context.Context, message string, err error) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.WarnContext(ctx, message, "error", err)
}

// providerCacheUsageKeys are the usage keys that carry prompt-cache statistics.
// The LLM adapters normalize the protocol-specific provider payloads (OpenAI
// prompt_tokens_details.cached_tokens, Responses
// input_tokens_details.cached_tokens, DeepSeek prompt_cache_hit_tokens and the
// cache_read_input_tokens alias) into "cached_tokens"; the provider-native keys
// are still accepted here so a caller that forwards the raw usage map (or an
// adapter that reports the provider key directly) keeps working. The first
// reported key wins and an explicit "cache_hit" (0/1) overrides the derived
// cached_tokens > 0.
var providerCacheUsageKeys = []string{
	"cached_tokens",
	"prompt_cache_hit_tokens",
	"cache_read_input_tokens",
	"prompt_tokens_details_cached_tokens",
}

// providerCacheStats extracts the provider-reported prompt-cache bookkeeping.
// ok is false when the provider reported no cache statistics at all, in which
// case the assembler's local estimate stays authoritative.
func providerCacheStats(usage map[string]int) (cachedTokens int, cacheHit bool, ok bool) {
	found := false
	for _, key := range providerCacheUsageKeys {
		if value, present := usage[key]; present {
			cachedTokens = value
			found = true
			break
		}
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if value, present := usage["cache_hit"]; present {
		return cachedTokens, value != 0, true
	}
	return cachedTokens, found && cachedTokens > 0, found
}

// applyProviderCacheStats implements contract §18.2.6 on the runner path: the
// provider usage overwrites the assembler's CachedTokens/CacheHit estimate,
// the persisted packet bookkeeping is updated in place and at most one
// context.cache_stats event is published per run. It is best effort: a
// persistence failure is logged and never fails the model turn.
func (r *Runner) applyProviderCacheStats(ctx context.Context, state *runState, usage map[string]int) {
	if state == nil || state.cacheStatsApplied {
		return
	}
	cachedTokens, cacheHit, ok := providerCacheStats(usage)
	if !ok {
		return
	}
	state.cacheStatsApplied = true
	state.providerCacheTokens = cachedTokens
	state.providerCacheHit = cacheHit
	state.providerCacheReported = true
	state.contextPacket.CachedTokens = cachedTokens
	state.contextPacket.CacheHit = cacheHit
	if state.run.Cache == nil {
		state.run.Cache = map[string]int{}
	}
	state.run.Cache["cached_tokens"] = cachedTokens
	state.run.Cache["cache_hit"] = boolInt(cacheHit)
	if state.packet.TokenCounts == nil {
		state.packet.TokenCounts = map[string]int{}
	}
	state.packet.TokenCounts["cached_tokens"] = cachedTokens
	state.packet.TokenCounts["cache_hit"] = boolInt(cacheHit)
	if state.packet.ID > 0 {
		if err := r.repo.UpdateContextPacketCacheStats(ctx, state.packet.ID, cachedTokens, cacheHit); err != nil {
			r.logContextWarning(ctx, "persist provider cache stats failed", err)
		}
	}
	payload := map[string]any{
		"run_id":        idText(state.run.ID),
		"prefix_hash":   state.contextPacket.PrefixHash,
		"cached_tokens": cachedTokens,
		"cache_hit":     cacheHit,
		"briefing_hash": state.contextPacket.BriefingHash,
		"segments":      len(state.contextPacket.Segments),
	}
	if reason := strings.TrimSpace(state.contextPacket.InvalidationReason); reason != "" {
		payload["invalidation_reason"] = reason
	}
	if state.payload.ConversationID > 0 {
		payload["conversation_id"] = idText(state.payload.ConversationID)
	}
	r.publish(state, "context.cache_stats", payload)
}

// compactionState is the pinned material of one post-compaction rebuild
// (R-A5-4/R-A5-9). It is derived from the run input, never from the rolling
// summary or the folded transcript: Task is the per-work-item task context,
// Plan/Facts are the DB truth snapshots and Briefing is the derived version
// briefing. PinnedFacts defaults to Input.Conversation.PinnedFacts when nil.
type compactionState struct {
	Input          ContextInput
	WorkItemID     int64
	RequirementIDs []int64
	CaseIDs        []int64
	Plan           []agentcontext.PlanItemSnapshot
	Facts          []agentcontext.FactSnapshot
	Briefing       string
	PinnedFacts    []string
}

// newCompactionState derives the reinjection input from the runner state. Task
// 36 may fill Plan/Facts from the briefing source; the task context, work item
// id and requirement/case ids are always rebuilt from state.input/payload.
func newCompactionState(state *runState) compactionState {
	if state == nil {
		return compactionState{}
	}
	reinjection := compactionState{
		Input:      state.input,
		WorkItemID: state.item.ID,
	}
	if state.payload.RequirementID > 0 {
		reinjection.RequirementIDs = []int64{state.payload.RequirementID}
	}
	if state.payload.CaseID > 0 {
		reinjection.CaseIDs = []int64{state.payload.CaseID}
	}
	return reinjection
}

// assembleAfterCompaction renders the R-A5-6/R-A5-9 pinned suffix block that is
// re-injected after a compaction: the task context rebuilt from state.input, the
// pinned facts (≤20), the plan/fact snapshots and the derived briefing. It
// renders pinned material only, so the folded history can never leak back into
// the prompt through this path.
func assembleAfterCompaction(state compactionState) string {
	return assembleCompactionBlock(state, true)
}

// assembleAfterCompactionForPrompt renders the reinjection block that the runner
// appends to the pinned facts segment of the live prompt. That segment already
// renders the pinned facts through contextFactBlocks (session memory), so this
// variant omits them: the prompt carries every pinned fact exactly once
// (R-A5-4/R-A5-9) while the standalone block still renders them for callers that
// use it on its own.
func assembleAfterCompactionForPrompt(state compactionState) string {
	return assembleCompactionBlock(state, false)
}

func assembleCompactionBlock(state compactionState, includePinnedFacts bool) string {
	lines := []string{"压缩后重注入（pinned task context，来自 state.input，不依赖摘要）"}
	lines = append(lines, summaryTaskContextLines(
		state.Input.Task, state.WorkItemID, state.RequirementIDs, state.CaseIDs, nil)...)

	if includePinnedFacts {
		pinnedFacts := state.PinnedFacts
		if pinnedFacts == nil {
			pinnedFacts = state.Input.Conversation.PinnedFacts
		}
		pinnedFacts = capPinnedFacts(pinnedFacts)
		lines = append(lines, fmt.Sprintf("pinned_facts (%d/%d):", len(pinnedFacts), MaxPinnedFacts))
		if len(pinnedFacts) == 0 {
			lines = append(lines, "- "+summaryNoneText)
		}
		for _, fact := range pinnedFacts {
			if line := summaryOneLine(fact); line != "" {
				lines = append(lines, "- "+line)
			}
		}
	}
	if len(state.Plan) > 0 {
		lines = append(lines, "plan:")
		for _, item := range state.Plan {
			lines = append(lines, summaryPlanItemLine(item))
		}
	}
	if len(state.Facts) > 0 {
		lines = append(lines, "facts:")
		for _, fact := range state.Facts {
			lines = append(lines, summaryFactLine(fact))
		}
	}
	if briefing := strings.TrimSpace(state.Briefing); briefing != "" {
		lines = append(lines, "briefing:")
		lines = append(lines, briefing)
	}
	return strings.Join(lines, "\n")
}
