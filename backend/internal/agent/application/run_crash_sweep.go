package agentapp

// v0.2.4-H T12 (H-E2): the run-level crash sweep.
//
// The work queue recovers WORK ITEMS, not runs. RecoverExpired reopens an expired
// item while attempts remain and settles it dead at the attempt cap; the 0.2.1
// claim transaction does the same with failed. Neither touches agent_run or its
// transcript, so a process that dies mid-run leaves:
//
//   - agent_run.status = 'running' forever (the Agent Center shows a run that
//     never ends, and its lease_epoch no longer matches the item's);
//   - the run's agent_message head at stream_state = 'streaming' (the admin
//     transcript renders an unfinished row no later turn will ever seal);
//   - for a Host profile, the conversation_message head streaming too — the
//     lease-fenced orphan sweep only runs when a LATER turn takes the lease over,
//     which never happens for an exhausted item in a conversation that ends there.
//
// This sweeper closes all three. It writes terminal state ONLY through the
// existing server-side entries (constitution 三.6, no second terminal write path):
// RuntimeRepository.UpdateMessage seals a transcript row, the conversation port
// seals the run's Host heads, and RuntimeRepository.FinishRun writes the run
// terminal under its stored lease_epoch fence.
//
// Selection is a lease fact, never a guess about "how long has it been running":
// a run is orphaned exactly when its work item is no longer leased by it — the
// item is settled/reopened (open/dead/done/failed) or its lease_epoch moved past
// the run's. A live attempt is therefore never touched, and waiting_human (a
// parked run awaiting a human) is deliberately NOT a trigger.
//
// Everything is best effort per run and never silent: a seal failure is logged
// and counted, and the run still reaches its terminal — the same order and the
// same reasoning as Runner.finish, whose head seal also precedes the row write.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// DefaultCrashSweepLimit bounds one sweep pass. The sweep runs on the worker's
// recovery tick, so a backlog is drained over successive ticks instead of one
// unbounded read (constitution 七.6: no hidden truncation — the bound is explicit
// and the remaining rows are still orphaned, hence still selected next tick).
const DefaultCrashSweepLimit = 100

// CrashedRun is one agent_run whose attempt no longer owns its work item. It
// carries the identity and the counters the terminal write must preserve: the
// sweep never invents usage the run did not record.
type CrashedRun struct {
	ID             int64
	LeaseEpoch     int64
	WorkItemID     int64
	VersionID      int64
	ConversationID int64
	ProfileKey     string
	Turns          int
	Usage          map[string]int
	Tokens         map[string]int
	Cache          map[string]int
}

// CrashSweepStore is the runtime repository surface the sweep needs. The read is
// deliberately separate from ListRunMessages: that read is a bounded page (and
// its limit<=0 form means "the whole run" only for the admin projection), while
// the sweep must see EVERY streaming row of the run without a hidden cap.
type CrashSweepStore interface {
	ListCrashedRuns(ctx context.Context, limit int) ([]CrashedRun, error)
	ListStreamingRunMessages(ctx context.Context, runID int64) ([]agent.AgentMessage, error)
	UpdateMessage(ctx context.Context, message agent.AgentMessage) error
	FinishRun(ctx context.Context, run agent.AgentRun) error
}

// CrashSweepConversations seals the Host conversation heads of one terminal run.
// The version conversation service satisfies it through the composition-root
// adapter; nil disables the Host leg (a deployment without the conversation
// context still recovers its transcripts and run terminals).
type CrashSweepConversations interface {
	InterruptRunOrphanStreams(ctx context.Context, conversationID, runID int64, errorPart ConversationPart) (int, error)
}

// CrashSweepReport is the observable outcome of one pass. Failed counts runs whose
// terminal write did not land (they stay orphaned and are retried next tick);
// SealFailures counts heads that could not be sealed even though their run did
// reach its terminal.
type CrashSweepReport struct {
	Runs              int
	TranscriptHeads   int
	ConversationHeads int
	SealFailures      int
	Failed            int
}

// RunCrashSweeper recovers runs a dead attempt left behind. It is safe to call
// repeatedly (every step is idempotent) and holds no state between passes.
type RunCrashSweeper struct {
	store         CrashSweepStore
	conversations CrashSweepConversations
	events        eventingapp.Publisher
	logger        *slog.Logger
	clock         clock.Clock
	limit         int
}

// NewRunCrashSweeper wires the sweep. conversations and events may be nil; a nil
// store disables the sweep entirely (half-assembled process).
func NewRunCrashSweeper(store CrashSweepStore, conversations CrashSweepConversations, events eventingapp.Publisher, logger *slog.Logger, clk clock.Clock) *RunCrashSweeper {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.System{}
	}
	return &RunCrashSweeper{
		store:         store,
		conversations: conversations,
		events:        events,
		logger:        logger,
		clock:         clk,
		limit:         DefaultCrashSweepLimit,
	}
}

// Sweep recovers one bounded batch of orphaned runs. A per-run failure never
// aborts the pass; only the selection read failing is returned as an error, so
// the caller can log it and try again on the next tick.
func (s *RunCrashSweeper) Sweep(ctx context.Context) (CrashSweepReport, error) {
	report := CrashSweepReport{}
	if s == nil || s.store == nil {
		return report, nil
	}
	runs, err := s.store.ListCrashedRuns(ctx, s.limit)
	if err != nil {
		return report, fmt.Errorf("agent crash sweep: list crashed runs: %w", err)
	}
	for _, run := range runs {
		if ctx.Err() != nil {
			s.logger.Warn("agent crash sweep stopped before the batch was drained",
				"remaining", len(runs)-report.Runs-report.Failed, "error", ctx.Err())
			break
		}
		transcript, conversation, sealFailures, err := s.recoverRun(ctx, run)
		report.TranscriptHeads += transcript
		report.ConversationHeads += conversation
		report.SealFailures += sealFailures
		if err != nil {
			report.Failed++
			s.logger.Warn("agent crash sweep could not finish a crashed run",
				"run_id", run.ID, "work_item_id", run.WorkItemID, "lease_epoch", run.LeaseEpoch, "error", err)
			continue
		}
		report.Runs++
		s.logger.Info("agent crash sweep recovered a crashed run",
			"run_id", run.ID, "work_item_id", run.WorkItemID, "profile", run.ProfileKey,
			"transcript_heads", transcript, "conversation_heads", conversation, "seal_failures", sealFailures)
	}
	return report, nil
}

// recoverRun seals one crashed run's heads and then writes its terminal. The
// order mirrors Runner.finish: the visible content is sealed first so a failed
// row write can never leave a streaming head behind, and a failed seal never
// keeps the run from reaching its terminal.
func (s *RunCrashSweeper) recoverRun(ctx context.Context, run CrashedRun) (int, int, int, error) {
	transcript, sealFailures := s.sealTranscriptHeads(ctx, run)
	conversation := 0
	if run.ConversationID > 0 && s.conversations != nil {
		sealed, err := s.conversations.InterruptRunOrphanStreams(ctx, run.ConversationID, run.ID,
			streamInterruptedPart(streamInterruptedRecoveryMessage))
		conversation = sealed
		if err != nil {
			sealFailures++
			s.logger.Warn("agent crash sweep could not seal every conversation head of a crashed run",
				"run_id", run.ID, "conversation_id", run.ConversationID, "sealed", sealed, "error", err)
		}
	}

	finishedAt := s.clock.Now()
	reason := string(FailureKindInterrupted)
	terminal := agent.AgentRun{
		ID:             run.ID,
		LeaseEpoch:     run.LeaseEpoch,
		Status:         agent.AgentRunInterrupted,
		TerminalReason: &reason,
		Turns:          run.Turns,
		Usage:          run.Usage,
		Tokens:         run.Tokens,
		Cache:          run.Cache,
		FinishedAt:     &finishedAt,
	}
	if err := s.store.FinishRun(ctx, terminal); err != nil {
		return transcript, conversation, sealFailures, err
	}
	return transcript, conversation, sealFailures, nil
}

// sealTranscriptHeads seals every streaming transcript row of the run through the
// repository's own in-place seal entry and republishes its terminal frame, so a
// live admin transcript stops rendering an unfinished row.
func (s *RunCrashSweeper) sealTranscriptHeads(ctx context.Context, run CrashedRun) (int, int) {
	rows, err := s.store.ListStreamingRunMessages(ctx, run.ID)
	if err != nil {
		s.logger.Warn("agent crash sweep could not read the streaming transcript of a crashed run",
			"run_id", run.ID, "error", err)
		return 0, 1
	}
	sealed, failures := 0, 0
	for _, row := range rows {
		// A terminal row is frozen; the guard keeps the sweep from even attempting
		// a write the repository would refuse.
		if row.StreamState != agent.MessageStreamStateStreaming {
			continue
		}
		row.StreamState = agent.MessageStreamStateInterrupted
		if err := s.store.UpdateMessage(ctx, row); err != nil {
			failures++
			s.logger.Warn("agent crash sweep could not seal a transcript row",
				"run_id", run.ID, "message_id", row.ID, "error", err)
			continue
		}
		sealed++
		s.publishTranscriptSeal(ctx, run, row)
	}
	return sealed, failures
}

// publishTranscriptSeal republishes one sealed row as a full snapshot on the run's
// own channel. It uses the SAME wire producer and payload schema as the live
// stream (NewTranscriptMessage / TranscriptDelta), so a replayed frame and a live
// frame are indistinguishable to the client.
func (s *RunCrashSweeper) publishTranscriptSeal(ctx context.Context, run CrashedRun, message agent.AgentMessage) {
	if s.events == nil {
		return
	}
	view := NewTranscriptMessage(message)
	delta := TranscriptDelta{
		RunID:       platformid.Format(run.ID),
		MessageID:   view.ID,
		StreamState: view.StreamState,
		Message:     &view,
	}
	// TranscriptDelta holds only strings, ints and already-valid JSON-bearing
	// structs, so the round-trip cannot fail; the same reasoning as the live
	// publisher applies here.
	payload := map[string]any{}
	if raw, err := json.Marshal(delta); err == nil {
		_ = json.Unmarshal(raw, &payload)
	}
	scope := map[string]any{
		"version_id":   platformid.Format(run.VersionID),
		"agent_run_id": platformid.Format(run.ID),
	}
	_ = s.events.Publish(context.Background(), eventingapp.Event{
		Channel:   eventingapp.RunChannel(run.ID),
		Type:      TranscriptDeltaEventType,
		Scope:     scope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}
