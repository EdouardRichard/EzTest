package agentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeRuntimeRepo implements agent.RuntimeRepository in memory.
type fakeRuntimeRepo struct {
	mu sync.Mutex

	profiles map[string]agent.AgentProfile
	nextID   int64

	runs         map[int64]agent.AgentRun
	finished     []agent.AgentRun
	createRunErr error
	finishRunErr error

	messages  []agent.AgentMessage
	packets   []agent.ContextPacket
	packetIDs map[int64]agent.ContextPacket
	// updates records every in-place transcript rewrite (v0.2.4-D T8), in call
	// order, so "one row per turn, updated in place" is observable.
	updates []agent.AgentMessage
	// updateFailures makes the next N in-place writes fail, mirroring a
	// transient store failure the runner must log and reconcile at the seal.
	updateFailures int
	// sealFailures makes the next N *sealing* writes (a write carrying a terminal
	// stream state) fail, while per-delta streaming writes keep succeeding. That
	// isolates "the turn's own seal was lost" from "every write failed", which is
	// what the run-terminal backstop has to recover.
	sealFailures int
	// packetCacheUpdates counts the provider cache-stat overwrites persisted
	// through UpdateContextPacketCacheStats.
	packetCacheUpdates int
	// activities is the agent_activity fact ledger (v0.2.4-D T10, Q04): one row
	// per (run_id, seq), upserted with the production ON CONFLICT semantics.
	activities []agent.Activity
	// activityErr makes every fact write fail, so "the event is derived only
	// after the fact succeeded" is observable.
	activityErr error
	// activityWrites records every write attempt in call order (start and end
	// of one call are two writes of the SAME row).
	activityWrites []agent.Activity
	// activityEventCounts snapshots how many agent.activity events had been
	// published at each fact write. It is only filled when events is set, and
	// it is what makes "fact BEFORE event" a real ordering assertion.
	activityEventCounts []int
	events              *fakePublisher
	// priorPacket is the version's previously persisted context packet, the
	// stage-boundary trigger's only prior-stage source (H-E3). Nil means the
	// version has no packet yet.
	priorPacket      *agent.ContextPacket
	priorPacketErr   error
	priorPacketReads []int64
}

func newFakeRuntimeRepo() *fakeRuntimeRepo {
	return &fakeRuntimeRepo{
		profiles:  map[string]agent.AgentProfile{},
		runs:      map[int64]agent.AgentRun{},
		packetIDs: map[int64]agent.ContextPacket{},
	}
}

func (f *fakeRuntimeRepo) next() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeRuntimeRepo) EnsureProfile(_ context.Context, profile agent.AgentProfile) (agent.AgentProfile, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.profiles[profile.Key]; ok {
		return existing, false, nil
	}
	profile.ID = f.next()
	f.profiles[profile.Key] = profile
	return profile, true, nil
}

func (f *fakeRuntimeRepo) GetProfile(_ context.Context, key string) (agent.AgentProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	profile, ok := f.profiles[key]
	if !ok {
		return agent.AgentProfile{}, agent.ErrNotFound
	}
	return profile, nil
}

func (f *fakeRuntimeRepo) ListProfiles(_ context.Context) ([]agent.AgentProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]agent.AgentProfile, 0, len(f.profiles))
	for _, profile := range f.profiles {
		list = append(list, profile)
	}
	return list, nil
}

func (f *fakeRuntimeRepo) CreateRun(_ context.Context, run agent.AgentRun) (agent.AgentRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createRunErr != nil {
		return agent.AgentRun{}, f.createRunErr
	}
	if run.ID == 0 {
		run.ID = f.next()
	}
	f.runs[run.ID] = run
	return run, nil
}

func (f *fakeRuntimeRepo) FinishRun(_ context.Context, run agent.AgentRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finishRunErr != nil {
		return f.finishRunErr
	}
	f.finished = append(f.finished, run)
	f.runs[run.ID] = run
	return nil
}

func (f *fakeRuntimeRepo) AppendMessage(ctx context.Context, message agent.AgentMessage) (agent.AgentMessage, error) {
	// A real pgx write on a cancelled context fails. Honouring ctx here is what
	// makes the "partial content survives cancellation" assertions discriminating
	// (v0.2.4-D T6, carried T3 review #4).
	if err := ctx.Err(); err != nil {
		return agent.AgentMessage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	message.ID = f.next()
	message.CreatedAt = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	// The column default is complete: a row appended without an explicit state
	// is a finished (batch/legacy) message, exactly like the production DB.
	message.StreamState = agent.NormalizeMessageStreamState(message.StreamState)
	f.messages = append(f.messages, message)
	return message, nil
}

// UpdateMessage mirrors the production in-place rewrite AND its terminal freeze:
// only a row that is still streaming may be rewritten, and a sealed row refuses
// every later write (agent.ErrMessageStreamClosed). A permissive fake here would
// make the freeze assertions non-discriminating.
func (f *fakeRuntimeRepo) UpdateMessage(ctx context.Context, message agent.AgentMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateFailures > 0 {
		f.updateFailures--
		return errors.New("transcript store is unavailable")
	}
	if f.sealFailures > 0 && agent.NormalizeMessageStreamState(message.StreamState) != agent.MessageStreamStateStreaming {
		f.sealFailures--
		return errors.New("transcript seal write failed")
	}
	for index := range f.messages {
		if f.messages[index].ID != message.ID {
			continue
		}
		if agent.NormalizeMessageStreamState(f.messages[index].StreamState) != agent.MessageStreamStateStreaming {
			return agent.ErrMessageStreamClosed
		}
		f.messages[index].Parts = message.Parts
		f.messages[index].ToolCalls = message.ToolCalls
		f.messages[index].Usage = message.Usage
		f.messages[index].StreamState = agent.NormalizeMessageStreamState(message.StreamState)
		f.updates = append(f.updates, f.messages[index])
		return nil
	}
	return agent.ErrNotFound
}

// InsertActivity mirrors the production (run_id, seq) UPSERT of the fact ledger:
// the first write of a seq inserts the row, a later write of the SAME seq folds
// into it (the end of a call adds ended_at/error and payload keys), the recorded
// start and outcome are never erased by a replayed start, and a different seq is
// a different fact. A permissive "append every write" fake would make the
// idempotency assertions non-discriminating.
func (f *fakeRuntimeRepo) InsertActivity(ctx context.Context, activity agent.Activity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activityWrites = append(f.activityWrites, activity)
	if f.events != nil {
		count := 0
		for _, event := range f.events.events {
			// Only the TOOL activity event is derived from a tool fact; the
			// per-turn "thinking" heartbeat is an unrelated projection.
			if event.Type == "agent.activity" && event.Payload["status"] == "tool" {
				count++
			}
		}
		f.activityEventCounts = append(f.activityEventCounts, count)
	}
	if f.activityErr != nil {
		return f.activityErr
	}
	if err := activity.Validate(); err != nil {
		return err
	}
	for index := range f.activities {
		stored := &f.activities[index]
		if stored.RunID != activity.RunID || stored.Seq != activity.Seq {
			continue
		}
		stored.Type = activity.Type
		stored.Title = activity.Title
		if activity.EndedAt != nil {
			stored.EndedAt = activity.EndedAt
		}
		if activity.Error != "" {
			stored.Error = activity.Error
		}
		merged := map[string]any{}
		for key, value := range stored.Payload {
			merged[key] = value
		}
		for key, value := range activity.Payload {
			merged[key] = value
		}
		stored.Payload = merged
		stored.UpdatedAt = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
		return nil
	}
	activity.ID = f.next()
	activity.CreatedAt = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	activity.UpdatedAt = activity.CreatedAt
	f.activities = append(f.activities, activity)
	return nil
}

// activityRows returns a copy of the fact ledger in write order.
func (f *fakeRuntimeRepo) activityRows() []agent.Activity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.Activity(nil), f.activities...)
}

// transcript returns a copy of every persisted transcript row.
func (f *fakeRuntimeRepo) transcript() []agent.AgentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.AgentMessage(nil), f.messages...)
}

// transcriptUpdates returns a copy of the in-place rewrite log.
func (f *fakeRuntimeRepo) transcriptUpdates() []agent.AgentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.AgentMessage(nil), f.updates...)
}

// failNextUpdates makes the next n in-place transcript writes fail.
func (f *fakeRuntimeRepo) failNextUpdates(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateFailures = n
}

// failNextSeals makes the next n sealing transcript writes fail (streaming
// per-delta writes still succeed).
func (f *fakeRuntimeRepo) failNextSeals(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sealFailures = n
}

func (f *fakeRuntimeRepo) ListRunMessages(_ context.Context, runID int64, limit int) ([]agent.AgentMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]agent.AgentMessage, 0)
	for _, message := range f.messages {
		if message.RunID != nil && *message.RunID == runID {
			list = append(list, message)
		}
	}
	if limit > 0 && len(list) > limit {
		list = list[len(list)-limit:]
	}
	return list, nil
}

func (f *fakeRuntimeRepo) SaveContextPacket(_ context.Context, packet agent.ContextPacket) (agent.ContextPacket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	packet.ID = f.next()
	packet.CreatedAt = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	f.packets = append(f.packets, packet)
	f.packetIDs[packet.ID] = packet
	return packet, nil
}

func (f *fakeRuntimeRepo) GetContextPacket(_ context.Context, id int64) (agent.ContextPacket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	packet, ok := f.packetIDs[id]
	if !ok {
		return agent.ContextPacket{}, agent.ErrNotFound
	}
	return packet, nil
}

// LatestVersionContextPacket mirrors the production version-scoped read the
// stage-boundary trigger uses (H-E3). It answers from priorPacket ONLY: a fixture
// that does not seed a previous chain has no prior packet, exactly like a version
// whose first run is starting, so every existing assembly test keeps its
// behaviour. priorPacketErr exercises the read-failure branch.
func (f *fakeRuntimeRepo) LatestVersionContextPacket(_ context.Context, versionID int64) (agent.ContextPacket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.priorPacketReads = append(f.priorPacketReads, versionID)
	if f.priorPacketErr != nil {
		return agent.ContextPacket{}, f.priorPacketErr
	}
	if f.priorPacket == nil {
		return agent.ContextPacket{}, agent.ErrNotFound
	}
	return *f.priorPacket, nil
}

// UpdateContextPacketCacheStats mirrors the production token_counts rewrite:
// only the provider cache bookkeeping changes, the packet content is immutable.
func (f *fakeRuntimeRepo) UpdateContextPacketCacheStats(_ context.Context, id int64, cachedTokens int, cacheHit bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	packet, ok := f.packetIDs[id]
	if !ok {
		return agent.ErrNotFound
	}
	f.packetCacheUpdates++
	if packet.TokenCounts == nil {
		packet.TokenCounts = map[string]int{}
	}
	packet.TokenCounts["cached_tokens"] = cachedTokens
	packet.TokenCounts["cache_hit"] = cacheHitInt(cacheHit)
	f.packetIDs[id] = packet
	for index := range f.packets {
		if f.packets[index].ID == id {
			f.packets[index] = packet
		}
	}
	return nil
}

func cacheHitInt(cacheHit bool) int {
	if cacheHit {
		return 1
	}
	return 0
}

func (f *fakeRuntimeRepo) lastFinished(t interface{ Fatalf(string, ...any) }) agent.AgentRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.finished) == 0 {
		t.Fatalf("no finished run recorded")
	}
	return f.finished[len(f.finished)-1]
}

// scriptedChat is a deterministic ChatGateway: the Nth Chat call returns
// errors[N] if set, otherwise responses[min(N,last)]. Stream shares the same
// request/error scripting; streams[N] (when non-nil) scripts the Nth Stream
// call's delta sequence, otherwise the deltas are derived from responses[N] so
// Chat-scripted tests exercise the streaming consumption path unchanged.
type scriptedChat struct {
	mu        sync.Mutex
	responses []llmapp.ChatResponse
	errors    []error
	streams   [][]llmapp.Delta
	requests  []llmapp.ChatRequest
	chatCalls int
	models    []llmapp.ModelInfo
	modelErr  error
	resolves  []string
	// supportsVision mirrors the catalog capability gate: a run that asks for
	// Vision on a model without it must fail before Chat is called.
	supportsVision bool
	requirements   []llmapp.ModelRequirements
	onChat         func(index int, req llmapp.ChatRequest)
	// onDelta runs after each emitted delta of a Stream call (call index,
	// 0-based emitted count). It lets a test cancel or deadline the run context
	// mid-stream, exactly where a real SSE reader would notice.
	onDelta func(index int, emitted int)
	// onStreamCtx, when set, receives the context each Stream call runs under
	// (the runner's derived run context). It lets a fake-clock test wait
	// deterministically for a deadline to propagate to that context (H-E14).
	onStreamCtx func(ctx context.Context)
}

func (f *scriptedChat) Chat(_ context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, req)
	f.chatCalls++
	onChat := f.onChat
	var scriptedErr error
	if index < len(f.errors) {
		scriptedErr = f.errors[index]
	}
	var response llmapp.ChatResponse
	if len(f.responses) > 0 {
		if index < len(f.responses) {
			response = f.responses[index]
		} else {
			response = f.responses[len(f.responses)-1]
		}
	}
	f.mu.Unlock()
	if onChat != nil {
		onChat(index, req)
	}
	return response, scriptedErr
}

// Stream emits the scripted delta sequence of the Nth call and then returns
// errors[N]. A non-nil streams[N] entry is emitted verbatim (even when an error
// is scripted, so a partial-then-failed turn can be scripted); otherwise the
// deltas are derived from the scripted ChatResponse. A scripted error with no
// streams entry fails before any delta, mirroring a pre-first-delta rejection.
func (f *scriptedChat) Stream(ctx context.Context, req llmapp.ChatRequest, onDelta func(llmapp.Delta)) error {
	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, req)
	onChat := f.onChat
	deltaHook := f.onDelta
	streamCtxHook := f.onStreamCtx
	var scriptedErr error
	if index < len(f.errors) {
		scriptedErr = f.errors[index]
	}
	var deltas []llmapp.Delta
	if index < len(f.streams) && f.streams[index] != nil {
		deltas = f.streams[index]
	} else if scriptedErr == nil {
		var response llmapp.ChatResponse
		if len(f.responses) > 0 {
			if index < len(f.responses) {
				response = f.responses[index]
			} else {
				response = f.responses[len(f.responses)-1]
			}
		}
		deltas = chatResponseDeltas(response)
	}
	f.mu.Unlock()
	if onChat != nil {
		onChat(index, req)
	}
	if streamCtxHook != nil {
		streamCtxHook(ctx)
	}
	for emitted, delta := range deltas {
		if err := ctx.Err(); err != nil {
			return err
		}
		onDelta(delta)
		if deltaHook != nil {
			deltaHook(index, emitted)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return scriptedErr
}

// chatResponseDeltas renders one scripted ChatResponse as the delta sequence a
// protocol adapter would emit for it: reasoning text, per-index tool-call
// fragments (identity first, arguments split in two), content split in two,
// then the terminal finish/usage/reasoning-items delta.
func chatResponseDeltas(response llmapp.ChatResponse) []llmapp.Delta {
	var deltas []llmapp.Delta
	if response.ReasoningContent != "" {
		deltas = append(deltas, llmapp.Delta{ReasoningContent: response.ReasoningContent})
	}
	for index, call := range response.ToolCalls {
		deltas = append(deltas, llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{
			Index: index, ID: call.ID, Name: call.Name,
		}})
		mid := len(call.Arguments) / 2
		deltas = append(deltas,
			llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{Index: index, Arguments: call.Arguments[:mid]}},
			llmapp.Delta{ToolCall: &llmapp.ToolCallDelta{Index: index, Arguments: call.Arguments[mid:]}},
		)
	}
	if response.Content != "" {
		mid := len(response.Content) / 2
		deltas = append(deltas,
			llmapp.Delta{Content: response.Content[:mid]},
			llmapp.Delta{Content: response.Content[mid:]},
		)
	}
	deltas = append(deltas, llmapp.Delta{
		FinishReason:   response.FinishReason,
		Usage:          response.Usage,
		ReasoningItems: response.ReasoningItems,
	})
	return deltas
}

func (f *scriptedChat) chatCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chatCalls
}

func (f *scriptedChat) ResolveModel(_ context.Context, _ int64, model string, required ...llmapp.ModelRequirements) (llmapp.ModelInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves = append(f.resolves, model)
	f.requirements = append(f.requirements, required...)
	if f.modelErr != nil {
		return llmapp.ModelInfo{}, f.modelErr
	}
	for _, requirement := range required {
		if requirement.Vision && !f.supportsVision {
			return llmapp.ModelInfo{}, &platform.Error{
				Status:  400,
				Code:    llmapp.CodeLLMCapabilityMismatch,
				Message: "llm model does not support vision required by this role; choose a model with that capability",
				Details: map[string]any{"missing_capabilities": []string{"vision"}},
			}
		}
	}
	if len(f.models) == 0 {
		return llmapp.ModelInfo{ConfigID: 1, Model: "fake-llm", ContextWindow: 64000, MaxOutput: 8192}, nil
	}
	if model != "" {
		for _, info := range f.models {
			if info.Model == model {
				return info, nil
			}
		}
	}
	return f.models[0], nil
}

func (f *scriptedChat) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *scriptedChat) request(index int) llmapp.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[index]
}

// fakeJobCardRecord is one job_card message the runner projected into a
// conversation. The fake mirrors the version writer's one-card-per-job upsert:
// a repeated projection updates Data/History instead of appending a record.
type fakeJobCardRecord struct {
	VersionID      int64
	ConversationID int64
	MessageID      int64
	Data           map[string]any
	History        []map[string]any
}

type fakeConversationPort struct {
	kind          string
	modelOverride string
	snapshot      agentapp.ConversationSnapshot
	messages      map[int64]agentapp.ConversationMessage
	appended      []agentapp.AssistantMessageInput
	jobCards      []fakeJobCardRecord
	summaries     []string
	summaryVer    int
	snapshotErr   error
}

func (f *fakeConversationPort) Snapshot(_ context.Context, versionID, conversationID int64, _ int) (agentapp.ConversationSnapshot, error) {
	if f.snapshotErr != nil {
		return agentapp.ConversationSnapshot{}, f.snapshotErr
	}
	snapshot := f.snapshot
	snapshot.VersionID = versionID
	snapshot.ID = conversationID
	snapshot.Kind = f.kind
	return snapshot, nil
}

func (f *fakeConversationPort) ConversationKind(_ context.Context, _, _ int64) (string, error) {
	if f.snapshotErr != nil {
		return "", f.snapshotErr
	}
	if f.kind == "" {
		return "clarification", nil
	}
	return f.kind, nil
}

func (f *fakeConversationPort) ModelOverride(_ context.Context, _, _ int64) (string, error) {
	if f.snapshotErr != nil {
		return "", f.snapshotErr
	}
	return f.modelOverride, nil
}

func (f *fakeConversationPort) GetMessage(_ context.Context, versionID, conversationID, messageID int64) (agentapp.ConversationMessage, error) {
	if message, ok := f.messages[messageID]; ok {
		return message, nil
	}
	return agentapp.ConversationMessage{
		ID: messageID, VersionID: versionID, ConversationID: conversationID,
		Role: "user", Text: "用户消息",
	}, nil
}

func (f *fakeConversationPort) AppendAssistantMessage(_ context.Context, in agentapp.AssistantMessageInput) (agentapp.ConversationMessage, error) {
	f.appended = append(f.appended, in)
	return agentapp.ConversationMessage{
		ID:             int64(9000 + len(f.appended)),
		VersionID:      in.VersionID,
		ConversationID: in.ConversationID,
		Role:           "assistant",
		Parts:          in.Parts,
		AgentRunID:     &in.AgentRunID,
	}, nil
}

func (f *fakeConversationPort) UpdateSummary(_ context.Context, _, _ int64, summary string, _ []string) (agentapp.ConversationSummary, error) {
	f.summaries = append(f.summaries, summary)
	f.summaryVer++
	return agentapp.ConversationSummary{Version: f.summaryVer, Summary: summary}, nil
}

// UpsertJobCard mirrors the version writer's one-card-per-job contract: the
// first projection for a (conversation, job) appends the card message, later
// projections rewrite that same record and keep the update history.
func (f *fakeConversationPort) UpsertJobCard(_ context.Context, versionID, conversationID int64, part agentapp.ConversationPart) (agentapp.ConversationMessage, error) {
	jobID, _ := part.Data["job_id"].(string)
	for index := range f.jobCards {
		record := &f.jobCards[index]
		existingJobID, _ := record.Data["job_id"].(string)
		if record.VersionID != versionID || record.ConversationID != conversationID || existingJobID != jobID {
			continue
		}
		record.Data = copyPartData(part.Data)
		record.History = append(record.History, copyPartData(part.Data))
		return agentapp.ConversationMessage{
			ID:             record.MessageID,
			VersionID:      versionID,
			ConversationID: conversationID,
			Role:           "assistant",
			Parts:          []agentapp.ConversationPart{part},
		}, nil
	}
	record := fakeJobCardRecord{
		VersionID:      versionID,
		ConversationID: conversationID,
		MessageID:      int64(9200 + len(f.jobCards)),
		Data:           copyPartData(part.Data),
	}
	record.History = append(record.History, copyPartData(part.Data))
	f.jobCards = append(f.jobCards, record)
	return agentapp.ConversationMessage{
		ID:             record.MessageID,
		VersionID:      versionID,
		ConversationID: conversationID,
		Role:           "assistant",
		Parts:          []agentapp.ConversationPart{part},
	}, nil
}

func copyPartData(data map[string]any) map[string]any {
	copied := make(map[string]any, len(data))
	for key, value := range data {
		copied[key] = value
	}
	return copied
}

type fakeVersionReader struct {
	pack agentapp.VersionPack
	err  error
}

func (f *fakeVersionReader) VersionPack(_ context.Context, versionID int64) (agentapp.VersionPack, error) {
	if f.err != nil {
		return agentapp.VersionPack{}, f.err
	}
	pack := f.pack
	pack.VersionID = versionID
	return pack, nil
}

type fakeAttachmentReader struct {
	info agentapp.AttachmentInfo
	err  error
}

func (f *fakeAttachmentReader) GetAttachment(_ context.Context, versionID, attachmentID int64) (agentapp.AttachmentInfo, error) {
	if f.err != nil {
		return agentapp.AttachmentInfo{}, f.err
	}
	info := f.info
	info.ID = attachmentID
	return info, nil
}

// fakeImageLoader resolves image part data_refs for vision runs and records
// the refs the runner asked for.
type fakeImageLoader struct {
	images map[string]agentapp.ImageData
	err    error
	refs   []string
}

func (f *fakeImageLoader) LoadImage(_ context.Context, dataRef string) (agentapp.ImageData, error) {
	f.refs = append(f.refs, dataRef)
	if f.err != nil {
		return agentapp.ImageData{}, f.err
	}
	image, ok := f.images[dataRef]
	if !ok {
		return agentapp.ImageData{}, platform.NotFound("content")
	}
	return image, nil
}

type fakeTeamRoomPort struct {
	context      agentapp.TeamRoomContext
	contextErr   error
	contextTeams []string
	posts        []agent.TeamRoomPost
}

func (f *fakeTeamRoomPort) Context(_ context.Context, _ int64, team string, _ int) (agentapp.TeamRoomContext, error) {
	f.contextTeams = append(f.contextTeams, team)
	return f.context, f.contextErr
}

func (f *fakeTeamRoomPort) Post(_ context.Context, post agent.TeamRoomPost) (agent.TeamRoomPost, error) {
	post.ID = int64(7000 + len(f.posts))
	f.posts = append(f.posts, post)
	return post, nil
}

type fakeContentWriter struct {
	puts []string
	ref  agentapp.ContentRef
	err  error
}

func (f *fakeContentWriter) PutText(_ context.Context, owner string, data []byte) (agentapp.ContentRef, error) {
	if f.err != nil {
		return agentapp.ContentRef{}, f.err
	}
	f.puts = append(f.puts, owner)
	if f.ref.Ref == "" {
		return agentapp.ContentRef{SHA256: "sha256-fake", Ref: "content:fake"}, nil
	}
	return f.ref, nil
}

// fakeRequirementPort implements tools.RequirementPort.
type fakeRequirementPort struct {
	list        []tools.RequirementSummary
	detail      tools.RequirementDetail
	created     tools.RequirementSummary
	updated     tools.RequirementSummary
	createDraft tools.RequirementDraft
}

func (f *fakeRequirementPort) ListRequirements(context.Context, int64) ([]tools.RequirementSummary, error) {
	return f.list, nil
}

func (f *fakeRequirementPort) GetRequirement(_ context.Context, _, _ int64) (tools.RequirementDetail, error) {
	return f.detail, nil
}

func (f *fakeRequirementPort) CreateDraft(_ context.Context, _ int64, draft tools.RequirementDraft) (tools.RequirementSummary, error) {
	f.createDraft = draft
	return f.created, nil
}

func (f *fakeRequirementPort) UpdateDraft(context.Context, int64, int64, tools.RequirementDraft) (tools.RequirementSummary, error) {
	return f.updated, nil
}

func (f *fakeRequirementPort) ProposeRelation(context.Context, int64, int64, int64, string, string) error {
	return nil
}

// fakeDesignPort implements tools.DesignPort.
type fakeDesignPort struct {
	readCalls         int
	readVersionID     int64
	readRequirementID int64
	readCaseID        int64
	readResult        any
	readErr           error

	proposeCalls     int
	proposeVersionID int64
	proposeInput     tools.GraphProposal
	proposeActorID   int64
	proposeRunID     int64
	proposeResult    any
	proposeErr       error

	reviewCalls         int
	reviewVersionID     int64
	reviewRequirementID int64
	reviewRunID         int64
	reviewInput         tools.ReviewInput
	reviewResult        any
	reviewErr           error

	dataPlanCalls         int
	dataPlanVersionID     int64
	dataPlanCaseVersionID int64
	dataPlanRunID         int64
	dataPlanInput         tools.DataPlanInput
	dataPlanResult        any
	dataPlanErr           error

	judgmentCalls         int
	judgmentVersionID     int64
	judgmentRequirementID int64
	judgmentRunID         int64
	judgmentInput         tools.JudgmentInput
	judgmentResult        any
	judgmentErr           error
}

func (f *fakeDesignPort) ReadGraph(_ context.Context, versionID, requirementID, caseID int64) (any, error) {
	f.readCalls++
	f.readVersionID = versionID
	f.readRequirementID = requirementID
	f.readCaseID = caseID
	return f.readResult, f.readErr
}

func (f *fakeDesignPort) ProposeGraph(_ context.Context, versionID int64, in tools.GraphProposal, actorID, runID int64) (any, error) {
	f.proposeCalls++
	f.proposeVersionID = versionID
	f.proposeInput = in
	f.proposeActorID = actorID
	f.proposeRunID = runID
	return f.proposeResult, f.proposeErr
}

func (f *fakeDesignPort) SubmitReview(_ context.Context, versionID, requirementID int64, runID int64, in tools.ReviewInput) (any, error) {
	f.reviewCalls++
	f.reviewVersionID = versionID
	f.reviewRequirementID = requirementID
	f.reviewRunID = runID
	f.reviewInput = in
	return f.reviewResult, f.reviewErr
}

func (f *fakeDesignPort) SubmitDataPlan(_ context.Context, versionID, caseVersionID int64, runID int64, in tools.DataPlanInput) (any, error) {
	f.dataPlanCalls++
	f.dataPlanVersionID = versionID
	f.dataPlanCaseVersionID = caseVersionID
	f.dataPlanRunID = runID
	f.dataPlanInput = in
	return f.dataPlanResult, f.dataPlanErr
}

func (f *fakeDesignPort) SubmitJudgment(_ context.Context, versionID, requirementID, runID int64, in tools.JudgmentInput) (any, error) {
	f.judgmentCalls++
	f.judgmentVersionID = versionID
	f.judgmentRequirementID = requirementID
	f.judgmentRunID = runID
	f.judgmentInput = in
	return f.judgmentResult, f.judgmentErr
}

// fakeDesignRoundStarter records the design.cases stage-entry port calls.
type fakeDesignRoundStarter struct {
	calls          int
	versionID      int64
	requirementIDs []int64
	conversationID int64
	err            error
}

func (f *fakeDesignRoundStarter) StartDesignRound(_ context.Context, versionID int64, requirementIDs []int64) (int64, error) {
	f.calls++
	f.versionID = versionID
	f.requirementIDs = append([]int64(nil), requirementIDs...)
	return f.conversationID, f.err
}

// fakeBatchStarter records the execute.batch stage-entry port calls.
type fakeBatchStarter struct {
	calls     int
	versionID int64
	caseIDs   []int64
	err       error
}

func (f *fakeBatchStarter) StartBatch(_ context.Context, versionID int64, caseIDs []int64) error {
	f.calls++
	f.versionID = versionID
	f.caseIDs = append([]int64(nil), caseIDs...)
	return f.err
}

// fakeAttachmentPort implements tools.AttachmentPort.
type fakeAttachmentPort struct {
	attachments []tools.AttachmentMeta
	text        tools.AttachmentText
}

func (f *fakeAttachmentPort) ListAttachments(context.Context, int64, int64) ([]tools.AttachmentMeta, error) {
	return f.attachments, nil
}

func (f *fakeAttachmentPort) ReadAttachmentText(context.Context, int64, int64, int64, int, int) (tools.AttachmentText, error) {
	return f.text, nil
}

func (f *fakeAttachmentPort) AttachmentImageRef(context.Context, int64, int64, int64) (tools.AttachmentImage, error) {
	return tools.AttachmentImage{}, nil
}

func (f *fakeAttachmentPort) AttachmentImageRefByImage(context.Context, int64, int64, int64) (tools.AttachmentImage, error) {
	return tools.AttachmentImage{}, nil
}

// fakeResourcePort implements tools.ResourcePort.
type fakeResourcePort struct{}

func (f *fakeResourcePort) ListSUTs(context.Context, int64) ([]tools.SUTSummary, error) {
	return nil, nil
}

func (f *fakeResourcePort) ListPages(context.Context, int64, int64) ([]tools.ResourcePage, error) {
	return nil, nil
}

func fakeWorkItem(workType string, payload agentapp.WorkItemPayload) agent.WorkItem {
	raw, _ := json.Marshal(map[string]any{
		"conversation_id":        idStringOrZero(payload.ConversationID),
		"message_id":             idStringOrZero(payload.MessageID),
		"attachment_id":          idStringOrZero(payload.AttachmentID),
		"requirement_id":         idStringOrZero(payload.RequirementID),
		"requirement_version_id": idStringOrZero(payload.RequirementVersionID),
		"case_id":                idStringOrZero(payload.CaseID),
		"case_version_id":        idStringOrZero(payload.CaseVersionID),
		"round_id":               idStringOrZero(payload.RoundID),
		"intent_id":              idStringOrZero(payload.IntentID),
		"wave_id":                idStringOrZero(payload.WaveID),
		"case_run_id":            idStringOrZero(payload.CaseRunID),
		"dispatch_epoch":         idStringOrZero(payload.DispatchEpoch),
		"dispatch_token":         payload.DispatchToken,
		"trigger":                payload.Trigger,
		"issue_id":               idStringOrZero(payload.IssueID),
		"report_id":              idStringOrZero(payload.ReportID),
		"report_revision":        idStringOrZero(int64(payload.ReportRevision)),
		"repo_id":                idStringOrZero(payload.RepoID),
		"index_run_id":           idStringOrZero(payload.IndexRunID),
		"analysis_id":            idStringOrZero(payload.AnalysisID),
		"base_commit_sha":        payload.BaseCommitSHA,
		"head_commit_sha":        payload.HeadCommitSHA,
	})
	leaseEpoch := int64(3)
	claimedBy := "worker-1"
	return agent.WorkItem{
		ID:          1001,
		VersionID:   10,
		Team:        agent.TeamClarification,
		Type:        workType,
		Status:      agent.WorkItemRunning,
		DedupeKey:   "test",
		Payload:     raw,
		ClaimedBy:   &claimedBy,
		LeaseEpoch:  leaseEpoch,
		Attempts:    1,
		MaxAttempts: 3,
	}
}

func idStringOrZero(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

// manualClock is a manually advanced clock for soft-timeout tests.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
	// step, when positive, makes every read advance the clock by that amount. It
	// is how a boundary that runs entirely between two model calls (the prepare
	// assembly's stage boundary, H-E3) still has a MEASURED duration instead of
	// the frozen clock's legitimate zero. Zero keeps the clock frozen.
	step time.Duration
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.now
	if c.step > 0 {
		c.now = c.now.Add(c.step)
	}
	return current
}

func (c *manualClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

type seqIDGenerator struct {
	mu   sync.Mutex
	next int64
}

func (g *seqIDGenerator) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

type fakeAuditRecorder struct {
	mu     sync.Mutex
	events []auditapp.Event
}

func (f *fakeAuditRecorder) Record(_ context.Context, event auditapp.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}
