package versionapp_test

// v0.2.4-H T12 (H-E2): the run-scoped crash recovery service path.
//
// A dead work item whose conversation gets no further turn has no lease holder to
// run the lease-fenced orphan sweep, so its head would stay streaming forever.
// InterruptRunOrphanStreams seals the heads of ONE terminal run without a lease,
// publishes their terminal frames, and never touches another run's head.
//
// The partial-recovery contract is pinned here too: when the repository reports a
// head it could not seal, the frames of the heads it DID seal are still published
// and the report is passed through untouched — it must not be relabelled as a
// 403/409 writer rejection, and it must not be swallowed (constitution 七.6).

import (
	"errors"
	"testing"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestInterruptRunOrphanStreamsSealsWithoutALease(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	dead := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, dead.Agent, dead.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	orphanID := beginStream(t, env, conversation.ID, dead, 101)
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, orphanID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "崩溃前已显示的内容"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Another run's head in the same conversation must survive the recovery.
	env.advanceClock(version.ConversationWriterLeaseDuration + time.Second)
	other := versionapp.Writer{TurnID: 202, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, other.Agent, other.TurnID); err != nil {
		t.Fatalf("takeover Acquire: %v", err)
	}
	otherID := beginStream(t, env, conversation.ID, other, 202)

	// The lease expires and nobody takes it over: no live writer exists.
	env.advanceClock(version.ConversationWriterLeaseDuration + time.Second)
	sealed, err := env.svc.InterruptRunOrphanStreams(requestContext(), conversation.ID, 101, recoveryErrorPart())
	if err != nil || sealed != 1 {
		t.Fatalf("InterruptRunOrphanStreams = %d (%v), want the dead run's one head", sealed, err)
	}
	frame := fullFrameFor(t, env, orphanID)
	if frame.Payload["stream_state"] != version.MessageStreamStateInterrupted {
		t.Fatalf("terminal frame stream_state = %#v, want interrupted", frame.Payload["stream_state"])
	}
	for index := range env.repo.messages[conversation.ID] {
		message := env.repo.messages[conversation.ID][index]
		if message.ID != otherID {
			continue
		}
		if message.StreamState != version.MessageStreamStateStreaming {
			t.Fatalf("another run's head stream_state = %q, want streaming", message.StreamState)
		}
	}

	// Idempotent, and an unknown run is an empty recovery.
	again, err := env.svc.InterruptRunOrphanStreams(requestContext(), conversation.ID, 101, recoveryErrorPart())
	if err != nil || again != 0 {
		t.Fatalf("repeated recovery = %d (%v), want 0", again, err)
	}
	unknown, err := env.svc.InterruptRunOrphanStreams(requestContext(), conversation.ID, 999, recoveryErrorPart())
	if err != nil || unknown != 0 {
		t.Fatalf("unknown run recovery = %d (%v), want 0", unknown, err)
	}
}

func TestInterruptRunOrphanStreamsValidatesInput(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	if _, err := env.svc.InterruptRunOrphanStreams(requestContext(), 0, 101, recoveryErrorPart()); err == nil {
		t.Fatal("a non-positive conversation id was accepted")
	}
	if _, err := env.svc.InterruptRunOrphanStreams(requestContext(), conversation.ID, 0, recoveryErrorPart()); err == nil {
		t.Fatal("a non-positive run id was accepted")
	}
	if _, err := env.svc.InterruptRunOrphanStreams(requestContext(), conversation.ID, 101,
		version.MessagePart{Type: version.MessagePartText, Text: "伪造成功"}); err == nil {
		t.Fatal("a non-error recovery part was accepted")
	}
}

// TestOrphanSweepPublishesSealedHeadsAndReportsTheRest pins the partial-recovery
// outcome on BOTH service entries: the sealed head's frame is published, the
// count reports it, and the unsealable head is returned as *version.OrphanSealError
// (never mapped to a writer rejection, never dropped).
func TestOrphanSweepPublishesSealedHeadsAndReportsTheRest(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	dead := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, dead.Agent, dead.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	badID := beginStream(t, env, conversation.ID, dead, 101)
	// The at-limit head: the producer cap keeps this unreachable, so it is
	// injected into the fake store exactly as the integration test injects it
	// into the real row.
	atLimit := make([]version.MessagePart, 0, version.MaxMessageStreamParts)
	for index := 0; index < version.MaxMessageStreamParts; index++ {
		atLimit = append(atLimit, version.MessagePart{Type: version.MessagePartText, Text: "段"})
	}
	for index := range env.repo.messages[conversation.ID] {
		if env.repo.messages[conversation.ID][index].ID == badID {
			env.repo.messages[conversation.ID][index].Parts = atLimit
		}
	}
	env.advanceClock(version.ConversationWriterLeaseDuration + time.Second)
	good := versionapp.Writer{TurnID: 202, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, good.Agent, good.TurnID); err != nil {
		t.Fatalf("takeover Acquire: %v", err)
	}
	goodID := beginStream(t, env, conversation.ID, good, 202)
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, goodID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "第二段"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	env.advanceClock(version.ConversationWriterLeaseDuration + time.Second)
	next := versionapp.Writer{TurnID: 303, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, next.Agent, next.TurnID); err != nil {
		t.Fatalf("second takeover Acquire: %v", err)
	}

	sealed, err := env.svc.InterruptOrphanStreams(requestContext(), conversation.ID, next, recoveryErrorPart())
	if sealed != 1 {
		t.Fatalf("sealed heads = %d, want the good head reported alongside the failure", sealed)
	}
	var partial *version.OrphanSealError
	if !errors.As(err, &partial) || len(partial.Failures) != 1 || partial.Failures[0].MessageID != badID {
		t.Fatalf("sweep error = %v, want an OrphanSealError naming head %d", err, badID)
	}
	if frame := fullFrameFor(t, env, goodID); frame.Payload["stream_state"] != version.MessageStreamStateInterrupted {
		t.Fatalf("the sealed head's terminal frame was not published: %#v", frame.Payload)
	}

	// The run-scoped entry reports the same way.
	sealedRun, err := env.svc.InterruptRunOrphanStreams(requestContext(), conversation.ID, 101, recoveryErrorPart())
	if sealedRun != 0 {
		t.Fatalf("run-scoped recovery sealed %d heads, want 0 (only the bad head belongs to run 101)", sealedRun)
	}
	if !errors.As(err, &partial) || len(partial.Failures) != 1 || partial.Failures[0].MessageID != badID {
		t.Fatalf("run-scoped error = %v, want the same OrphanSealError", err)
	}
}
