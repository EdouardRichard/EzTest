package agentapp_test

// v0.2.4-D fix wave (#26): sealTranscriptAtTerminal is the ONLY backstop that
// keeps a terminal run from leaving a transcript row streaming forever. The turn
// paths normally seal their own row; these tests drive the two reachable paths
// where they do not, and assert the run-terminal guarantee instead:
// the row ends interrupted and the content already written is intact.
//
// Expectations come from the T8 contract (a terminal run never leaves a
// transcript row streaming; a cut turn is interrupted, never complete; content
// already produced is never dropped) — not from observed runner output.

import (
	"context"
	"errors"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// TestTerminalSealsTheTranscriptWhenTheTurnSealWasLost fails the turn's own seal
// write (a transient store outage at exactly that write) while every per-delta
// write succeeded. The row must still end interrupted with the whole reply, and
// the terminal frame must carry it — the turn's complete seal never landed.
func TestTerminalSealsTheTranscriptWhenTheTurnSealWasLost(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{Content: "已经写完的"},
		{Content: "整段回复"},
		{FinishReason: "stop"},
	}}
	fixture.repo.failNextSeals(1)

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err == nil {
		t.Fatal("a lost seal write must fail the run, not report success")
	}
	if result.Status == agent.AgentRunDone {
		t.Fatalf("result = %#v, want a failed run", result)
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want the one streaming head", len(rows))
	}
	if rows[0].StreamState != agent.MessageStreamStateInterrupted {
		t.Fatalf("stream_state = %q, want interrupted (a terminal run never leaves a row streaming)",
			rows[0].StreamState)
	}
	if len(rows[0].Parts) != 1 || rows[0].Parts[0].Text != "已经写完的整段回复" {
		t.Fatalf("parts = %#v, want the whole reply preserved", rows[0].Parts)
	}
	for _, update := range fixture.repo.transcriptUpdates() {
		if update.StreamState == agent.MessageStreamStateComplete {
			t.Fatal("the turn's complete seal landed; this test no longer drives the terminal backstop")
		}
	}
	deltas := transcriptDeltasFor(t, fixture, result.AgentRunID, rows[0].ID)
	if len(deltas) == 0 {
		t.Fatal("no transcript frame was published for the head")
	}
	last := deltas[len(deltas)-1]
	if last.Message == nil || last.StreamState != agent.MessageStreamStateInterrupted {
		t.Fatalf("terminal frame = %#v, want an interrupted full snapshot", last)
	}
	if len(last.Message.Parts) != 1 || last.Message.Parts[0].Text != "已经写完的整段回复" {
		t.Fatalf("terminal frame parts = %#v, want the preserved reply", last.Message.Parts)
	}
}

// TestTerminalSealsTheTranscriptWhenTheTurnStoppedBeforeItsSeal cuts the turn
// after a reasoning-only first delta: the head is open, no content was written,
// and the partial-turn path has nothing to persist, so the terminal funnel is
// the only writer left. The row must end interrupted and must not fabricate text.
func TestTerminalSealsTheTranscriptWhenTheTurnStoppedBeforeItsSeal(t *testing.T) {
	fixture := newWiringFixture(t)
	fixture.chat.streams = [][]llmapp.Delta{{
		{ReasoningContent: "先想一下"},
	}}
	fixture.chat.errors = []error{errors.New("upstream closed")}

	result, err := fixture.runner.Run(context.Background(), designRequirementItem())
	if err == nil {
		t.Fatal("a post-first-delta stream failure must fail the run")
	}
	rows := assistantRows(fixture, result.AgentRunID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want the opened head", len(rows))
	}
	if rows[0].StreamState != agent.MessageStreamStateInterrupted {
		t.Fatalf("stream_state = %q, want interrupted", rows[0].StreamState)
	}
	if len(rows[0].Parts) != 0 {
		t.Fatalf("parts = %#v, want no fabricated content for a turn that produced none", rows[0].Parts)
	}
	deltas := transcriptDeltasFor(t, fixture, result.AgentRunID, rows[0].ID)
	if len(deltas) == 0 {
		t.Fatal("no transcript frame was published for the head")
	}
	last := deltas[len(deltas)-1]
	if last.Message == nil || last.StreamState != agent.MessageStreamStateInterrupted {
		t.Fatalf("terminal frame = %#v, want an interrupted full snapshot", last)
	}
}
