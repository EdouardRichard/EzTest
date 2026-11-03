package agentapp

import (
	"context"
	"log/slog"
	"strings"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
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
	builder := r.builder
	if builder == nil {
		builder = NewContextBuilder(r.settings, NewLLMSummarizer(r.chat))
	}
	assembler := r.assembler
	if assembler == nil {
		assembler = agentcontext.NewAssembler()
	}
	budget, threshold, _ := builder.resolveBudget(ctx, state.input)

	briefing, stage := r.versionBriefing(ctx, state)
	request := agentcontext.AssemblyRequest{
		System:       systemContent(state.input),
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

	if invalidation == agentcontext.InvalidationNone &&
		float64(result.BeforeTokens) > threshold*float64(budget) {
		// The window is nearly full: this round is a declared boundary.
		invalidation = agentcontext.InvalidationWindowFull
	}
	if invalidation != agentcontext.InvalidationNone {
		// Rebuild at the boundary, carrying the persisted rolling summary so the
		// conversation memory accumulates instead of being replaced by the
		// summary of this round only.
		request.Invalidation = invalidation
		request.Previous = withConversationSummary(previous, state.input.Conversation.Summary)
		rebuilt, rebuildErr := assembler.Build(ctx, request)
		if rebuildErr != nil {
			return contextAssembly{}, rebuildErr
		}
		result.Packet = rebuilt.Packet
		result.BeforeTokens = contextPacketTokens(rebuilt.Packet)
		compacted, compactErr := r.contextCompactor(ctx).Compact(ctx, result.Packet)
		if compactErr != nil {
			return contextAssembly{}, compactErr
		}
		result.Compacted = contextPacketHasSummary(compacted) &&
			len(compacted.Segments) != len(result.Packet.Segments)
		result.Packet = compacted
		result.AfterTokens = contextPacketTokens(compacted)
	}
	result.Messages = contextPacketMessages(result.Packet)
	return result, nil
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

// contextCompactor wires boundary compaction with the configured recent window
// (context.keep_recent_messages, default 12, zero folds everything compactable)
// and the LLM summarizer with its deterministic fallback.
func (r *Runner) contextCompactor(ctx context.Context) *agentcontext.Compactor {
	return agentcontext.NewCompactor(
		NewLLMSummarizer(r.chat),
		agentcontext.WithKeepRecent(r.keepRecentMessages(ctx)),
		agentcontext.WithLogger(r.logger),
	)
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

// recordContextCompaction persists the rolling summary of a boundary
// compaction through the conversation memory and publishes context.compacted.
// At most one summary segment is appended per compaction; a boundary that folds
// nothing publishes the rebuild without a summary.
func (r *Runner) recordContextCompaction(ctx context.Context, state *runState, packet agentcontext.ContextPacket, beforeTokens, afterTokens int, persistSummary bool) {
	summary := packetSummary(packet)
	summaryVersion := 0
	if persistSummary && summary != "" && state.payload.ConversationID > 0 && r.conversation != nil {
		stored, err := r.conversation.UpdateSummary(ctx, state.item.VersionID,
			state.payload.ConversationID, summary, state.input.Conversation.PinnedFacts)
		if err == nil {
			summaryVersion = stored.Version
		}
	}
	payload := map[string]any{
		"run_id":          idText(state.run.ID),
		"summary_version": summaryVersion,
		"token_before":    beforeTokens,
		"token_after":     afterTokens,
	}
	if state.payload.ConversationID > 0 {
		payload["conversation_id"] = idText(state.payload.ConversationID)
	}
	r.publish(state, "context.compacted", payload)
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

// providerCacheUsageKeys are the provider usage keys that carry prompt-cache
// statistics. The LLM adapters flatten the protocol-specific nested usage
// details into these keys; the runner only consumes the normalized map.
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
