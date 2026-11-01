package agentapp_test

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
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

func (f *fakeRuntimeRepo) AppendMessage(_ context.Context, message agent.AgentMessage) (agent.AgentMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	message.ID = f.next()
	message.CreatedAt = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	f.messages = append(f.messages, message)
	return message, nil
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

func (f *fakeRuntimeRepo) lastFinished(t interface{ Fatalf(string, ...any) }) agent.AgentRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.finished) == 0 {
		t.Fatalf("no finished run recorded")
	}
	return f.finished[len(f.finished)-1]
}

// scriptedChat is a deterministic ChatGateway: the Nth Chat call returns
// errors[N] if set, otherwise responses[min(N,last)].
type scriptedChat struct {
	mu        sync.Mutex
	responses []llmapp.ChatResponse
	errors    []error
	requests  []llmapp.ChatRequest
	models    []llmapp.ModelInfo
	modelErr  error
	resolves  []string
	onChat    func(index int, req llmapp.ChatRequest)
}

func (f *scriptedChat) Chat(_ context.Context, req llmapp.ChatRequest) (llmapp.ChatResponse, error) {
	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, req)
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

func (f *scriptedChat) ResolveModel(_ context.Context, model string) (llmapp.ModelInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves = append(f.resolves, model)
	if f.modelErr != nil {
		return llmapp.ModelInfo{}, f.modelErr
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

type fakeConversationPort struct {
	kind        string
	snapshot    agentapp.ConversationSnapshot
	messages    map[int64]agentapp.ConversationMessage
	appended    []agentapp.AssistantMessageInput
	summaries   []string
	summaryVer  int
	snapshotErr error
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
		"close_round":            idStringOrZero(int64(payload.CloseRound)),
		"report_revision_id":     idStringOrZero(payload.ReportRevisionID),
		"predicate_fingerprint":  payload.PredicateFingerprint,
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
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
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
