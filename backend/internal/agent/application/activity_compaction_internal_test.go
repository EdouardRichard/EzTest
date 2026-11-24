package agentapp

// v0.2.4-D T10 (SC-D5): the compaction fact hook sits on the ONE compaction
// emission funnel, so every trigger of the authority is recorded without a
// per-trigger branch. The trigger set is enumerated from
// agentcontext.CompactionTriggers() — a newly added trigger is covered by this
// test automatically (method discipline ⑧: a hand-written closed list cannot
// catch an addition).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// activityFactRepo answers ONLY the fact-ledger calls. Every other repository
// method panics through the embedded nil interface, so a hook that quietly
// reaches for another port fails loudly instead of being fed a zero value.
type activityFactRepo struct {
	agent.RuntimeRepository
	rows []agent.Activity
	err  error
}

func (r *activityFactRepo) InsertActivity(ctx context.Context, activity agent.Activity) error {
	// A real pgx write on a cancelled context fails. Honouring ctx here is what
	// makes the "the outcome fact survives cancellation" assertion discriminating.
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.err != nil {
		return r.err
	}
	if err := activity.Validate(); err != nil {
		return err
	}
	for index := range r.rows {
		if r.rows[index].RunID == activity.RunID && r.rows[index].Seq == activity.Seq {
			r.rows[index] = activity
			return nil
		}
	}
	activity.ID = int64(len(r.rows) + 1)
	r.rows = append(r.rows, activity)
	return nil
}

// activityEventPublisher answers ONLY Publish, recording the derived events.
type activityEventPublisher struct {
	eventingapp.Publisher
	events []eventingapp.Event
	err    error
}

func (p *activityEventPublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return p.err
}

func activityTestRunner(repo agent.RuntimeRepository, publisher eventingapp.Publisher) *Runner {
	return &Runner{
		repo:   repo,
		events: publisher,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func activityTestState() *runState {
	return &runState{
		run:     agent.AgentRun{ID: 777},
		item:    agent.WorkItem{ID: 12, VersionID: 34},
		payload: WorkItemPayload{ConversationID: 56},
	}
}

// TestCompactionFactCoversEveryTrigger drives the recording funnel with each
// trigger of the authority and asserts one compaction fact per trigger carrying
// the boundary's token counts.
func TestCompactionFactCoversEveryTrigger(t *testing.T) {
	triggers := agentcontext.CompactionTriggers()
	if len(triggers) == 0 {
		t.Fatalf("the compaction trigger authority is empty")
	}
	for _, trigger := range triggers {
		repo := &activityFactRepo{}
		publisher := &activityEventPublisher{}
		runner := activityTestRunner(repo, publisher)
		state := activityTestState()
		assembly := contextAssembly{
			Compacted:    true,
			BeforeTokens: 4000,
			AfterTokens:  900,
			Decision: &agentcontext.CompactionDecision{
				Trigger: trigger,
				Retain:  512,
			},
		}

		runner.recordContextCompaction(context.Background(), state, assembly, false)

		if len(repo.rows) != 1 {
			t.Fatalf("%s: facts = %#v, want exactly one compaction fact", trigger, repo.rows)
		}
		row := repo.rows[0]
		if row.Type != agent.ActivityTypeCompaction {
			t.Fatalf("%s: fact type = %q, want compaction", trigger, row.Type)
		}
		if row.RunID != state.run.ID || row.Seq != 1 {
			t.Fatalf("%s: fact key = (%d,%d), want (%d,1)", trigger, row.RunID, row.Seq, state.run.ID)
		}
		if got, _ := row.Payload["trigger"].(string); got != string(trigger) {
			t.Fatalf("%s: payload trigger = %#v", trigger, row.Payload["trigger"])
		}
		if before, _ := row.Payload["before_tokens"].(int); before != 4000 {
			t.Fatalf("%s: before_tokens = %#v, want 4000", trigger, row.Payload["before_tokens"])
		}
		if after, _ := row.Payload["after_tokens"].(int); after != 900 {
			t.Fatalf("%s: after_tokens = %#v, want 900", trigger, row.Payload["after_tokens"])
		}
		if len(publisher.events) != 1 || publisher.events[0].Type != agentcontext.EventContextCompacted {
			t.Fatalf("%s: derived events = %#v, want one context.compacted", trigger, publisher.events)
		}
	}
}

// TestCompactionFactSeqIsMonotonicPerRun pins that consecutive boundaries of one
// run take consecutive seqs (the same counter the tool facts use).
func TestCompactionFactSeqIsMonotonicPerRun(t *testing.T) {
	repo := &activityFactRepo{}
	runner := activityTestRunner(repo, &activityEventPublisher{})
	state := activityTestState()
	for index := 0; index < 3; index++ {
		runner.recordContextCompaction(context.Background(), state, contextAssembly{
			Compacted:    true,
			BeforeTokens: 100,
			AfterTokens:  50,
			Decision:     &agentcontext.CompactionDecision{Trigger: agentcontext.Pressure, Retain: 10},
		}, false)
	}
	if len(repo.rows) != 3 {
		t.Fatalf("facts = %d, want one per boundary", len(repo.rows))
	}
	for index, row := range repo.rows {
		if row.Seq != index+1 {
			t.Fatalf("fact %d seq = %d, want %d", index, row.Seq, index+1)
		}
	}
}

// TestCompactionEventIsSkippedWhenTheFactWriteFails is the compaction half of
// 五.7: no fact, no derived event.
func TestCompactionEventIsSkippedWhenTheFactWriteFails(t *testing.T) {
	repo := &activityFactRepo{err: errors.New("activity store is unavailable")}
	publisher := &activityEventPublisher{}
	runner := activityTestRunner(repo, publisher)
	state := activityTestState()

	runner.recordContextCompaction(context.Background(), state, contextAssembly{
		Compacted:    true,
		BeforeTokens: 100,
		AfterTokens:  50,
		Decision:     &agentcontext.CompactionDecision{Trigger: agentcontext.Overflow, Retain: 10},
	}, false)

	if len(publisher.events) != 0 {
		t.Fatalf("events = %#v, want none when the fact was never written", publisher.events)
	}
}

// TestCompactionFactSurvivesEventFailure is the independence half: the fact is
// committed even when the event transport fails.
func TestCompactionFactSurvivesEventFailure(t *testing.T) {
	repo := &activityFactRepo{}
	publisher := &activityEventPublisher{err: errors.New("event transport is down")}
	runner := activityTestRunner(repo, publisher)
	state := activityTestState()

	runner.recordContextCompaction(context.Background(), state, contextAssembly{
		Compacted:    true,
		BeforeTokens: 100,
		AfterTokens:  50,
		Decision:     &agentcontext.CompactionDecision{Trigger: agentcontext.StageBoundary, Retain: 10},
	}, false)

	if len(repo.rows) != 1 {
		t.Fatalf("facts = %#v, want the boundary fact despite the event failure", repo.rows)
	}
}

// TestCompactionStopFactIsRecordedByTheRealRecoverableBranch drives the REAL
// production path of a stopped compaction: another transaction already owns the
// conversation, so CompactionManager.RunDecision returns ErrCompactionBusy,
// IsRecoverableCompactionError accepts it and compactContext keeps the original
// context. The attempt must still be a fact — with the real cause, the real
// measured duration, and NO fabricated before/after token counts (T10 review
// #1: the previous version of this test hand-built an event no producer emits).
func TestCompactionStopFactIsRecordedByTheRealRecoverableBranch(t *testing.T) {
	repo := &activityFactRepo{}
	publisher := &activityEventPublisher{}
	runner := activityTestRunner(repo, publisher)
	step := 250 * time.Millisecond
	runner.clock = &steppingActivityClock{
		now:  time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		step: step,
	}
	manager := agentcontext.NewCompactionManager()
	runner.compactionManager = manager
	state := activityTestState()
	policy := agentcontext.NewCompactionPolicy(128000, 4096)

	// A live transaction owns this conversation: the next attempt is busy.
	if _, err := manager.Start(context.Background(), state.payload.ConversationID,
		agentcontext.Pressure, policy); err != nil {
		t.Fatalf("seed live compaction transaction: %v", err)
	}
	decision := agentcontext.CompactionDecision{Trigger: agentcontext.Pressure, Policy: policy, Retain: policy.Retain}

	if err := runner.compactContext(context.Background(), state, decision, -1); err != nil {
		t.Fatalf("compactContext: %v (a busy conversation is recoverable)", err)
	}

	if len(repo.rows) != 1 {
		t.Fatalf("facts = %#v, want the stopped attempt recorded once", repo.rows)
	}
	row := repo.rows[0]
	if row.Type != agent.ActivityTypeCompaction {
		t.Fatalf("fact type = %q, want compaction", row.Type)
	}
	if !strings.Contains(row.Error, agentcontext.ErrCompactionBusy.Error()) {
		t.Fatalf("fact error = %q, want the real recoverable cause %q", row.Error, agentcontext.ErrCompactionBusy)
	}
	if got, _ := row.Payload["trigger"].(string); got != string(agentcontext.Pressure) {
		t.Fatalf("fact trigger = %#v, want pressure", row.Payload["trigger"])
	}
	for _, key := range []string{"before_tokens", "after_tokens"} {
		if _, ok := row.Payload[key]; ok {
			t.Fatalf("a stopped attempt fabricated %s: %#v", key, row.Payload)
		}
	}
	duration, ok := row.Payload["duration_ms"].(int64)
	if !ok || duration != step.Milliseconds() {
		t.Fatalf("fact duration_ms = %#v, want the measured %d", row.Payload["duration_ms"], step.Milliseconds())
	}
	if row.EndedAt == nil || !row.StartedAt.Equal(row.EndedAt.Add(-time.Duration(duration)*time.Millisecond)) {
		t.Fatalf("fact start/end = %v / %#v, want start = end - duration", row.StartedAt, row.EndedAt)
	}
	// A stopped attempt derives no event: the existing contract keeps the event
	// stream and the summary ledger untouched.
	if len(publisher.events) != 0 {
		t.Fatalf("events = %#v, want none for a stopped attempt", publisher.events)
	}
}

// steppingActivityClock advances on every read, so a duration measured across
// two r.now() calls is real and non-zero.
type steppingActivityClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func (c *steppingActivityClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.now
	c.now = c.now.Add(c.step)
	return current
}

type fixedActivityClock struct{ now time.Time }

func (c fixedActivityClock) Now() time.Time { return c.now }

// TestToolCallOutcomeFactSurvivesCancellation pins the audit half of the tool
// fact: a call that ended because the run was cancelled still records its end.
// The repository fake honours ctx.Err(), so a write riding the dead run context
// would fail here.
func TestToolCallOutcomeFactSurvivesCancellation(t *testing.T) {
	repo := &activityFactRepo{}
	runner := activityTestRunner(repo, &activityEventPublisher{})
	state := activityTestState()
	started := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	runner.clock = fixedActivityClock{now: started.Add(time.Second)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call := llmapp.ToolCall{ID: "call_1", Name: "requirement.read", Arguments: `{}`}
	runner.recordToolCallEnd(ctx, state, call, 1, started, context.Canceled)

	if len(repo.rows) != 1 {
		t.Fatalf("facts = %#v, want the cancelled call's outcome recorded", repo.rows)
	}
	row := repo.rows[0]
	if row.EndedAt == nil || row.Error == "" {
		t.Fatalf("fact = %#v, want an end and the cancellation reason", row)
	}
	if row.Seq != 1 || row.Type != agent.ActivityTypeToolCall {
		t.Fatalf("fact key/kind = (%d,%q), want (1,tool_call)", row.Seq, row.Type)
	}
}

// TestActivityFactFailureIsLogged pins 七.6/七.7: a fact write that fails is an
// explicit, attributable log record (run/kind/seq/cause), never a silent drop.
func TestActivityFactFailureIsLogged(t *testing.T) {
	var logs bytes.Buffer
	repo := &activityFactRepo{err: errors.New("activity store is unavailable")}
	runner := activityTestRunner(repo, &activityEventPublisher{})
	runner.logger = slog.New(slog.NewTextHandler(&logs, nil))
	state := activityTestState()

	committed := runner.recordActivityFact(context.Background(), state, agent.Activity{
		RunID: state.run.ID, Seq: 3, Type: agent.ActivityTypeToolCall, Title: "requirement.read",
	})

	if committed {
		t.Fatalf("a failed write reported success")
	}
	text := logs.String()
	for _, want := range []string{
		"agent activity fact could not be recorded",
		"run_id=777", "activity_type=tool_call", "activity_seq=3",
		"activity store is unavailable",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("failure log missing %q: %s", want, text)
		}
	}
}
