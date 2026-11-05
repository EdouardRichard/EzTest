package agentapp_test

// v0.22 W0 Task 36 runner wiring (RED-first).
//
// These tests pin the production composition the Lead must add to runner.go:
//   - Host replies are written through the single-writer lease path
//     (AcquireWriter -> AppendWriterAssistant -> ReleaseWriter), never through
//     the legacy writer-less AppendAssistantMessage;
//   - task.complete is registered for the Host profiles, its successful result
//     is translated into the run's completion signal exactly once (no duplicate
//     completion writes);
//   - plan.update / question.ask / clarification.search are exposed to the Host
//     tool surface when their production ports are wired.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/load"
)

// leaseConversationPort is the fake writer-lease conversation surface: the
// runner must type-assert the lease port and use it for Host replies.
type leaseConversationPort struct {
	*fakeConversationPort
	mu       sync.Mutex
	acquired int
	renewed  int
	released int
	written  []agentapp.ConversationPart
	lastTurn int64
	lastName string
}

func newLeaseConversationPort() *leaseConversationPort {
	return &leaseConversationPort{fakeConversationPort: &fakeConversationPort{
		messages: map[int64]agentapp.ConversationMessage{
			501: {ID: 501, Role: "user", Text: "我想做一个登录功能"},
		},
		snapshot: agentapp.ConversationSnapshot{
			Summary:  "此前讨论了登录。",
			Messages: []agentapp.ConversationMessage{{ID: 501, Role: "user", Text: "我想做一个登录功能"}},
		},
	}}
}

func (p *leaseConversationPort) AcquireWriter(_ context.Context, _ int64, agentName string, turnID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acquired++
	p.lastTurn = turnID
	p.lastName = agentName
	return nil
}

func (p *leaseConversationPort) RenewWriter(_ context.Context, _ int64, _ string, _ int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.renewed++
	return nil
}

func (p *leaseConversationPort) ReleaseWriter(_ context.Context, _ int64, _ string, _ int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released++
	return nil
}

func (p *leaseConversationPort) AppendWriterAssistant(_ context.Context, _ int64, _ string, _ int64, runID int64, replyTo *int64, parts []agentapp.ConversationPart) (agentapp.ConversationMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, parts...)
	return agentapp.ConversationMessage{
		ID:               9001,
		VersionID:        7,
		ConversationID:   20,
		Role:             "assistant",
		Parts:            parts,
		ReplyToMessageID: replyTo,
		AgentRunID:       &runID,
	}, nil
}

// completionWriterFake records task.complete commits.
type completionWriterFake struct {
	mu      sync.Mutex
	commits int
}

func (f *completionWriterFake) Commit(_ context.Context, commit tools.CompletionCommit) (tools.CompletionOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	return tools.CompletionOutcome{
		PlanItemID:       commit.PlanItemID,
		PlanStatus:       "done",
		Decision:         commit.Summary,
		PlanItemRevision: 2,
	}, nil
}

type completionValidatorFake struct{ planItemID int64 }

func (v completionValidatorFake) Validate(context.Context, tools.CompletionRequest) (tools.CompletionVerification, error) {
	if v.planItemID <= 0 {
		return tools.CompletionVerification{}, errors.New("no plan item target")
	}
	return tools.CompletionVerification{PlanItemID: v.planItemID}, nil
}

type planWriterFake struct {
	mu      sync.Mutex
	updates int
}

func (f *planWriterFake) Update(_ context.Context, _ int64, itemID int64, _ tools.PlanPatch, _ int) (tools.PlanItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	return tools.PlanItem{ID: itemID, Status: "done"}, nil
}

type questionAskerFake struct{ asked int }

func (f *questionAskerFake) AskQuestions(_ context.Context, _ tools.Scope, in tools.QuestionAskInput) (tools.QuestionAskResult, error) {
	f.asked++
	return tools.QuestionAskResult{ID: 1, Status: "pending"}, nil
}

type searcherFake struct{ searches int }

func (f *searcherFake) SearchClarification(_ context.Context, _ tools.ClarificationSearchQuery) ([]tools.ClarificationSearchHit, error) {
	f.searches++
	return nil, nil
}

type wiringFixture struct {
	*runnerFixture
	lease    *leaseConversationPort
	writer   *completionWriterFake
	plan     *planWriterFake
	asker    *questionAskerFake
	searcher *searcherFake
}

// newWiringFixture builds a runner whose Host conversation port supports the
// v0.22 writer lease and whose optional tool ports are wired.
func newWiringFixture(t *testing.T, options ...func(*agentapp.RunnerDeps)) *wiringFixture {
	t.Helper()
	base := newRunnerFixture(t)
	lease := newLeaseConversationPort()
	fixture := &wiringFixture{
		runnerFixture: base,
		lease:         lease,
		writer:        &completionWriterFake{},
		plan:          &planWriterFake{},
		asker:         &questionAskerFake{},
		searcher:      &searcherFake{},
	}
	deps := agentapp.RunnerDeps{
		Runtime:        base.repo,
		Chat:           base.chat,
		Settings:       base.settings,
		Clock:          base.clock,
		IDs:            &seqIDGenerator{},
		Events:         base.events,
		Audit:          base.audit,
		Conversation:   lease,
		Versions:       base.versions,
		Attachments:    base.attachments,
		Images:         base.images,
		TeamRoom:       base.team,
		Content:        base.content,
		Knowledge:      base.knowledge,
		KnowledgeUsage: base.knowledgeUsage,
		Briefing:       agentcontext.NewBriefingBuilder(base.briefing),
		Clarification:  base.clarification,
		DesignRounds:   base.designRounds,
		Batches:        base.batches,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Load:           &load.Counter{},
		ToolDeps: tools.Deps{
			Requirement: base.requirement,
			Attachment:  &fakeAttachmentPort{},
			Resource:    &fakeResourcePort{},
			Design:      base.design,
			P7:          tools.P7Deps{Knowledge: base.knowledgeWriter},
		},
		PlanWriter:          fixture.plan,
		QuestionAsker:       fixture.asker,
		ClarificationSearch: fixture.searcher,
		Completion: func(context.Context, agentapp.CompletionContext) (tools.CompletionDeps, error) {
			return tools.CompletionDeps{
				Validator: completionValidatorFake{planItemID: 77},
				Writer:    fixture.writer,
			}, nil
		},
		InstanceID: "instance-1",
	}
	for _, option := range options {
		option(&deps)
	}
	fixture.runner = agentapp.NewRunner(deps)
	return fixture
}

func runWiring(t *testing.T, fixture *wiringFixture, item agent.WorkItem) agentapp.RunResult {
	t.Helper()
	result, err := fixture.runner.Run(context.Background(), item)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result
}

func TestHostReplyUsesWriterLease(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式？"}}

	result := runWiring(t, fixture, hostItem())
	if result.Status != agent.AgentRunDone {
		t.Fatalf("status = %s", result.Status)
	}
	if fixture.lease.acquired != 1 || fixture.lease.released != 1 {
		t.Fatalf("lease acquire/release = %d/%d", fixture.lease.acquired, fixture.lease.released)
	}
	if fixture.lease.lastName != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("lease agent = %q", fixture.lease.lastName)
	}
	if fixture.lease.lastTurn != result.AgentRunID {
		t.Fatalf("lease turn = %d, want run %d", fixture.lease.lastTurn, result.AgentRunID)
	}
	if len(fixture.lease.written) != 1 || fixture.lease.written[0].Type != "text" {
		t.Fatalf("writer parts = %#v", fixture.lease.written)
	}
	if len(fixture.lease.fakeConversationPort.appended) != 0 {
		t.Fatalf("legacy AppendAssistantMessage path was used: %#v", fixture.lease.fakeConversationPort.appended)
	}
}

func TestTaskCompleteSignalsCompletionOnce(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolTaskComplete, `{"summary":"done","artifacts":["design:1"]}`),
		{Content: "should not be requested"},
	}
	result := runWiring(t, fixture, hostItem())
	if result.Status != agent.AgentRunDone {
		t.Fatalf("status = %s", result.Status)
	}
	if fixture.writer.commits != 1 {
		t.Fatalf("completion commits = %d", fixture.writer.commits)
	}
	if fixture.chat.requestCount() != 1 {
		t.Fatalf("model turns after completion = %d, want 1", fixture.chat.requestCount())
	}
}

func TestTaskCompleteCannotWriteTwiceInOneTurn(t *testing.T) {
	fixture := newWiringFixture(t)
	response := llmapp.ChatResponse{
		ToolCalls: []llmapp.ToolCall{
			{ID: "call_1", Name: tools.ToolTaskComplete, Arguments: `{"summary":"done","artifacts":["design:1"]}`},
			{ID: "call_2", Name: tools.ToolTaskComplete, Arguments: `{"summary":"again","artifacts":["design:2"]}`},
		},
		FinishReason: "tool_calls",
	}
	fixture.chat.responses = []llmapp.ChatResponse{response}
	result := runWiring(t, fixture, hostItem())
	if result.Status != agent.AgentRunDone {
		t.Fatalf("status = %s", result.Status)
	}
	if fixture.writer.commits != 1 {
		t.Fatalf("duplicate completion writes = %d", fixture.writer.commits)
	}
}

func TestHostToolSurfaceIncludesV022Tools(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "回复"}}
	_ = runWiring(t, fixture, hostItem())
	request := fixture.chat.request(0)
	names := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		names[tool.Name] = struct{}{}
	}
	for _, want := range []string{
		tools.ToolTaskComplete,
		tools.ToolPlanUpdate,
		tools.ToolQuestionAsk,
		tools.ToolClarificationSearch,
	} {
		if _, ok := names[want]; !ok {
			t.Fatalf("host tool definitions missing %q: %v", want, requestToolNames(request))
		}
	}
}

func requestToolNames(request llmapp.ChatRequest) []string {
	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// registerCompletionToolGroup mirrors the production composition root: the
// task.complete tool group is registered once and then evaluates the per-run
// Deps.Completion.
func registerCompletionToolGroup() {
	completionGroupOnce.Do(func() {
		tools.RegisterP4ToolGroup(tools.TaskCompleteTools)
	})
}

var completionGroupOnce sync.Once

func init() { registerCompletionToolGroup() }
