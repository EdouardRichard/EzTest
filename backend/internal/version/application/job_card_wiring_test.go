package versionapp_test

import (
	"encoding/json"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Task 63: the job control service consumes this narrow port, so the deferred
// composition-root wiring is a compile-time guarantee here.
var _ agentapp.JobCardUpdater = (*versionapp.ConversationService)(nil)

// lastConversationMessageEvent returns the newest conversation.message event
// for one message id. The job-card upsert republishes the message as its
// snapshot changes, so the latest frame carries the current card.
func lastConversationMessageEvent(t *testing.T, env *conversationEnv, messageID int64) eventingapp.Event {
	t.Helper()
	want := idString(messageID)
	found := false
	var latest eventingapp.Event
	for _, event := range env.events.events {
		if event.Type != "conversation.message" {
			continue
		}
		if event.Payload["id"] == want {
			latest = event
			found = true
		}
	}
	if !found {
		t.Fatalf("conversation.message event for message %s missing: %#v", want, env.events.events)
	}
	return latest
}

// TestPartsJobCardUpsert pins the single-card contract of the conversation job
// card writer: the first snapshot appends one assistant message carrying only
// the job_card part (with its streaming row); every later progress/terminal
// snapshot rewrites that same message and part payload instead of appending a
// second card; a different job owns its own card message.
func TestPartsJobCardUpsert(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)
	cancelable := true
	start := version.MessagePart{
		Type: "job_card", JobID: 501, JobKind: "requirement.curate",
		Status: "running", Current: 0, Total: 0, Cancelable: &cancelable,
	}

	first, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, start)
	if err != nil {
		t.Fatalf("UpsertJobCard(start): %v", err)
	}
	if !created {
		t.Fatal("first upsert did not create the job card message")
	}
	if first.Role != version.MessageRoleAssistant {
		t.Fatalf("job card role = %q, want assistant", first.Role)
	}
	if len(first.Parts) != 1 || first.Parts[0].Type != version.MessagePartJobCard ||
		first.Parts[0].JobID != 501 || first.Parts[0].JobKind != "requirement.curate" ||
		first.Parts[0].Status != "running" {
		t.Fatalf("job card parts = %#v", first.Parts)
	}
	if got := len(env.repo.messages[conversation.ID]); got != 1 {
		t.Fatalf("conversation messages = %d, want the one card message", got)
	}
	rows, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, first.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	if len(rows) != 1 || rows[0].Seq != 1 || rows[0].Type != version.MessagePartJobCard {
		t.Fatalf("job card stream rows = %#v, want one seq=1 job_card row", rows)
	}
	event := lastConversationMessageEvent(t, env, first.ID)
	eventPayload := eventParts(t, event)
	if eventPayload[0]["job_id"] != "501" || eventPayload[0]["status"] != "running" {
		t.Fatalf("job card event part = %#v", eventPayload[0])
	}

	// Progress rewrites the SAME card message and its streaming row.
	progress := start
	progress.Current = 3
	second, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, progress)
	if err != nil {
		t.Fatalf("UpsertJobCard(progress): %v", err)
	}
	if created {
		t.Fatal("progress appended a second job card message")
	}
	if second.ID != first.ID {
		t.Fatalf("progress card message = %d, want the original %d", second.ID, first.ID)
	}
	if len(second.Parts) != 1 || second.Parts[0].Current != 3 {
		t.Fatalf("progress card parts = %#v, want current 3", second.Parts)
	}
	if got := len(env.repo.messages[conversation.ID]); got != 1 {
		t.Fatalf("conversation messages after progress = %d, want one", got)
	}
	rows, err = env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, first.ID)
	if err != nil {
		t.Fatalf("MessageParts after progress: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("decode job card payload: %v", err)
	}
	if payload["status"] != "running" || payload["current"] != float64(3) {
		t.Fatalf("job card row payload = %#v, want the progress snapshot", payload)
	}
	latest := eventParts(t, lastConversationMessageEvent(t, env, first.ID))
	if latest[0]["current"] != float64(3) {
		t.Fatalf("latest job card event = %#v, want current 3", latest[0])
	}

	// The terminal snapshot lands on the same card and drops cancelable.
	done := progress
	done.Status = "done"
	done.Current = 4
	done.Cancelable = nil
	third, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, done)
	if err != nil {
		t.Fatalf("UpsertJobCard(done): %v", err)
	}
	if created || third.ID != first.ID {
		t.Fatalf("terminal card = (%d, created=%v), want the same message %d", third.ID, created, first.ID)
	}
	if third.Parts[0].Status != "done" || third.Parts[0].Current != 4 {
		t.Fatalf("terminal card parts = %#v", third.Parts)
	}

	// A different job owns its own card message.
	other := done
	other.JobID = 502
	other.JobKind = "design.cases"
	fourth, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, other)
	if err != nil {
		t.Fatalf("UpsertJobCard(second job): %v", err)
	}
	if !created {
		t.Fatal("a second job did not create its own card message")
	}
	if fourth.ID == first.ID {
		t.Fatalf("second job reused message %d", first.ID)
	}
	if got := len(env.repo.messages[conversation.ID]); got != 2 {
		t.Fatalf("conversation messages = %d, want one card per job", got)
	}

	// Conversation work types are accepted as card kinds (the parenthetical
	// requirement: chat.turn/requirement.curate/design.round).
	chatTurn := start
	chatTurn.JobID = 503
	chatTurn.JobKind = "chat.turn"
	if _, _, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, chatTurn); err != nil {
		t.Fatalf("UpsertJobCard(chat.turn): %v", err)
	}

	// Invalid card payloads are rejected before any write.
	badKind := start
	badKind.JobID = 504
	badKind.JobKind = "design.requirement"
	badStatus := start
	badStatus.JobID = 505
	badStatus.Status = "bogus"
	noJob := start
	noJob.JobID = 0
	overflow := start
	overflow.JobID = 506
	overflow.Current = 5
	overflow.Total = 2
	doneCancelable := start
	doneCancelable.JobID = 507
	doneCancelable.Status = "done"
	doneCancelable.Cancelable = &cancelable
	for _, tc := range []struct {
		name string
		card version.MessagePart
	}{
		{"unknown kind", badKind},
		{"unknown status", badStatus},
		{"missing job id", noJob},
		{"progress overflow", overflow},
		{"terminal cancelable", doneCancelable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, tc.card); err == nil {
				t.Fatalf("UpsertJobCard(%s) succeeded, want a validation error", tc.name)
			}
		})
	}

	// The card lookup is version-scoped: a foreign version cannot address the
	// conversation.
	if _, _, err := env.svc.UpsertJobCard(requestContext(), env.versionID+1, conversation.ID, start); err == nil {
		t.Fatal("UpsertJobCard succeeded for a foreign version")
	}
}

// TestCancelUpdatesPersistedJobCard pins the Task 63 persisted-side cancel: the
// same job_card message the runner projected is rewritten to the terminal
// cancelled snapshot (status cancelled, cancelable false) with no second card,
// and the usual conversation.message frame is republished so the live UI and a
// reload agree. Cancelling twice is idempotent.
func TestCancelUpdatesPersistedJobCard(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)
	cancelable := true
	start := version.MessagePart{
		Type: "job_card", JobID: 601, JobKind: "requirement.curate",
		Status: "running", Current: 2, Total: 5, Cancelable: &cancelable,
	}
	cardMessage, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, start)
	if err != nil {
		t.Fatalf("UpsertJobCard: %v", err)
	}
	if !created {
		t.Fatal("fixture did not create the job card message")
	}
	if got := len(env.repo.messages[conversation.ID]); got != 1 {
		t.Fatalf("messages before cancel = %d, want one card", got)
	}

	if err := env.svc.CancelJobCard(requestContext(), env.versionID, conversation.ID, 601); err != nil {
		t.Fatalf("CancelJobCard: %v", err)
	}

	// One card per job: the cancel rewrote the existing message, it did not
	// append a second card.
	if got := len(env.repo.messages[conversation.ID]); got != 1 {
		t.Fatalf("messages after cancel = %d, want the one existing card", got)
	}
	stored := env.repo.messages[conversation.ID][0]
	if stored.ID != cardMessage.ID {
		t.Fatalf("card message = %d, want the original %d", stored.ID, cardMessage.ID)
	}
	if len(stored.Parts) != 1 || stored.Parts[0].Type != version.MessagePartJobCard ||
		stored.Parts[0].JobID != 601 {
		t.Fatalf("stored card parts = %#v", stored.Parts)
	}
	card := stored.Parts[0]
	if card.Status != "cancelled" {
		t.Fatalf("stored card status = %q, want cancelled", card.Status)
	}
	if card.Cancelable == nil || *card.Cancelable {
		t.Fatalf("stored card cancelable = %v, want false on a terminal card", card.Cancelable)
	}
	if card.JobKind != start.JobKind || card.Current != start.Current || card.Total != start.Total {
		t.Fatalf("cancel rewrote the card snapshot beyond the terminal fields: %#v", card)
	}

	// The streaming row and the SSE frame carry the same terminal snapshot.
	rows, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, cardMessage.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	if len(rows) != 1 || rows[0].Seq != 1 || rows[0].Type != version.MessagePartJobCard {
		t.Fatalf("card stream rows = %#v, want the original one seq=1 row", rows)
	}
	payload := decodedStreamParts(t, rows)[0]
	if payload["status"] != "cancelled" || payload["cancelable"] != false {
		t.Fatalf("card row payload = %#v, want the cancelled terminal snapshot", payload)
	}
	event := eventParts(t, lastConversationMessageEvent(t, env, cardMessage.ID))
	if event[0]["status"] != "cancelled" || event[0]["job_id"] != "601" {
		t.Fatalf("republished card event = %#v, want cancelled job 601", event[0])
	}

	// A repeated cancel republishes the same terminal card, never a duplicate.
	if err := env.svc.CancelJobCard(requestContext(), env.versionID, conversation.ID, 601); err != nil {
		t.Fatalf("second CancelJobCard: %v", err)
	}
	if got := len(env.repo.messages[conversation.ID]); got != 1 {
		t.Fatalf("messages after repeated cancel = %d, want one", got)
	}
	if got := env.repo.messages[conversation.ID][0].Parts[0].Status; got != "cancelled" {
		t.Fatalf("repeated cancel card status = %q, want cancelled", got)
	}
}

// TestCancelWithoutCardNoop pins the no-card cancellation paths: a job whose
// card was never projected, or whose conversation is not owned by the addressed
// version, has nothing to rewrite, so CancelJobCard is a silent no-op and the
// authoritative cancel state is never put at risk.
func TestCancelWithoutCardNoop(t *testing.T) {
	t.Run("conversation without a card", func(t *testing.T) {
		env := newConversationEnv(t)
		conversation := newChatConversation(t, env)
		eventsBefore := len(env.events.events)

		if err := env.svc.CancelJobCard(requestContext(), env.versionID, conversation.ID, 601); err != nil {
			t.Fatalf("CancelJobCard without a card: %v", err)
		}
		if got := len(env.repo.messages[conversation.ID]); got != 0 {
			t.Fatalf("messages = %d, want no card appended", got)
		}
		if got := len(env.events.events); got != eventsBefore {
			t.Fatalf("events = %d, want no republish for a missing card", got-eventsBefore)
		}
	})

	t.Run("foreign version", func(t *testing.T) {
		env := newConversationEnv(t)
		conversation := newChatConversation(t, env)
		eventsBefore := len(env.events.events)

		if err := env.svc.CancelJobCard(requestContext(), env.versionID+1, conversation.ID, 603); err != nil {
			t.Fatalf("CancelJobCard for a foreign version: %v", err)
		}
		if got := len(env.repo.messages[conversation.ID]); got != 0 {
			t.Fatalf("messages = %d, want no cross-version card write", got)
		}
		if got := len(env.events.events); got != eventsBefore {
			t.Fatalf("events = %d, want no cross-version republish", got-eventsBefore)
		}
	})
}

// TestCancelLegacyNoCard pins the legacy thread path: a conversation written
// before the 0.2.1 job-card projection has no job_card part at all, so a cancel
// must rewrite nothing and append nothing.
func TestCancelLegacyNoCard(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)
	if _, err := env.svc.AppendAssistantMessage(requestContext(), conversation.ID,
		[]version.MessagePart{{Type: "text", Text: "legacy reply"}}, 0); err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}
	eventsBefore := len(env.events.events)

	if err := env.svc.CancelJobCard(requestContext(), env.versionID, conversation.ID, 602); err != nil {
		t.Fatalf("CancelJobCard on a legacy thread: %v", err)
	}
	if got := len(env.repo.messages[conversation.ID]); got != 1 {
		t.Fatalf("messages = %d, want the legacy message and no card", got)
	}
	if got := len(env.events.events); got != eventsBefore {
		t.Fatalf("events = %d, want no republish for a legacy thread", got-eventsBefore)
	}
}
