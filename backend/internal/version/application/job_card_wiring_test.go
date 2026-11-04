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

// TestJobCardFailureReasonPersisted covers Task 75: a failed job card's typed
// failure code and user-facing reason are part of the immutable single-card
// snapshot, the append-only streaming row and the SSE frame, so a reloaded
// thread can explain the failure instead of showing only 失败/重试作业.
func TestJobCardFailureReasonPersisted(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)
	failed := version.MessagePart{
		Type: "job_card", JobID: 801, JobKind: "chat.turn", Status: "failed",
		Code:   "llm_capability_mismatch",
		Reason: `llm model "text-only" does not support images; choose a model with vision`,
	}

	view, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, failed)
	if err != nil {
		t.Fatalf("UpsertJobCard(failed): %v", err)
	}
	if !created {
		t.Fatal("failed job card did not create its message")
	}
	card := view.Parts[0]
	if card.Code != failed.Code || card.Reason != failed.Reason || card.Status != "failed" {
		t.Fatalf("stored failed card = %#v, want the typed code/reason", card)
	}

	rows, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, view.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stream rows = %#v, want one job_card row", rows)
	}
	payload := decodedStreamParts(t, rows)[0]
	if payload["code"] != failed.Code || payload["reason"] != failed.Reason {
		t.Fatalf("stream row payload = %#v, want the typed failure code/reason", payload)
	}
	eventPart := eventParts(t, lastConversationMessageEvent(t, env, view.ID))[0]
	if eventPart["code"] != failed.Code || eventPart["reason"] != failed.Reason {
		t.Fatalf("republished card event = %#v, want the typed failure code/reason", eventPart)
	}
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

// newCancelledJobCard projects jobID's running card at the given progress and
// cancels it, returning the stored card message and the event count after the
// cancel — the baseline a late worker projection must not change.
func newCancelledJobCard(t *testing.T, env *conversationEnv, conversationID, jobID int64, current int) (versionapp.MessageView, int) {
	t.Helper()
	cancelable := true
	start := version.MessagePart{
		Type: "job_card", JobID: jobID, JobKind: "requirement.curate",
		Status: "running", Current: current, Total: 5, Cancelable: &cancelable,
	}
	cardMessage, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversationID, start)
	if err != nil {
		t.Fatalf("UpsertJobCard(start): %v", err)
	}
	if !created {
		t.Fatal("fixture did not create the job card message")
	}
	if err := env.svc.CancelJobCard(requestContext(), env.versionID, conversationID, jobID); err != nil {
		t.Fatalf("CancelJobCard: %v", err)
	}
	return cardMessage, len(env.events.events)
}

// assertCancelledJobCardStored pins the sticky terminal snapshot: one card
// message, status cancelled, cancelable false, kind/current/total unchanged,
// and the streaming row carrying the same snapshot.
func assertCancelledJobCardStored(t *testing.T, env *conversationEnv, conversationID, messageID, jobID int64, current int) {
	t.Helper()
	messages := env.repo.messages[conversationID]
	if len(messages) != 1 {
		t.Fatalf("conversation messages = %d, want the one card", len(messages))
	}
	stored := messages[0]
	if stored.ID != messageID || len(stored.Parts) != 1 {
		t.Fatalf("stored card message = %#v, want the original card %d", stored, messageID)
	}
	card := stored.Parts[0]
	if card.Type != version.MessagePartJobCard || card.JobID != jobID || card.Status != "cancelled" {
		t.Fatalf("stored card = %#v, want the cancelled card of job %d", card, jobID)
	}
	if card.Cancelable == nil || *card.Cancelable {
		t.Fatalf("stored card cancelable = %v, want false", card.Cancelable)
	}
	if card.JobKind != "requirement.curate" || card.Current != current || card.Total != 5 {
		t.Fatalf("stored card = %#v, want the pre-cancel snapshot", card)
	}
	rows, err := env.svc.MessageParts(requestContext(), env.versionID, conversationID, messageID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	if len(rows) != 1 || rows[0].Seq != 1 || rows[0].Type != version.MessagePartJobCard {
		t.Fatalf("card stream rows = %#v, want the original one seq=1 row", rows)
	}
	payload := decodedStreamParts(t, rows)[0]
	if payload["status"] != "cancelled" || payload["cancelable"] != false ||
		payload["current"] != float64(current) {
		t.Fatalf("card row payload = %#v, want the cancelled snapshot", payload)
	}
}

// TestLateRunningAfterCancelNoop pins the cancel card's stickiness against the
// worker's ~10s heartbeat delay: the in-flight turn may still report progress
// after the cancel committed, and that late running projection must be ignored
// instead of overwriting the persisted cancelled card. No new message and no
// conversation.message republish either.
func TestLateRunningAfterCancelNoop(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)
	cardMessage, eventsAfterCancel := newCancelledJobCard(t, env, conversation.ID, 701, 2)

	late := version.MessagePart{
		Type: "job_card", JobID: 701, JobKind: "requirement.curate",
		Status: "running", Current: 4, Total: 5, Cancelable: boolPtr(true),
	}
	view, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, late)
	if err != nil {
		t.Fatalf("late running UpsertJobCard: %v", err)
	}
	if created {
		t.Fatal("late running projection appended a second card")
	}
	if view.ID != cardMessage.ID || len(view.Parts) != 1 || view.Parts[0].Status != "cancelled" {
		t.Fatalf("late running projection result = %#v, want the stored cancelled card %d", view, cardMessage.ID)
	}
	assertCancelledJobCardStored(t, env, conversation.ID, cardMessage.ID, 701, 2)

	if got := len(env.events.events); got != eventsAfterCancel {
		t.Fatalf("events after late running projection = %d, want the pre-projection %d (no republish)",
			got, eventsAfterCancel)
	}
}

// TestLateDoneAfterCancelNoop pins the same stickiness for the runner's late
// terminal outcome and its retryable failure reopen: once the persisted card is
// cancelled, done/failed/open projections are ignored. It also models the row
// lock race: a cancel committing after this projection's lookup is a silent
// no-op, not an error that would surface out of the best-effort write-back.
func TestLateDoneAfterCancelNoop(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		status     string
		cancelable *bool
	}{
		{"done", "done", nil},
		{"failed", "failed", nil},
		{"open", "open", boolPtr(true)},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			env := newConversationEnv(t)
			conversation := newChatConversation(t, env)
			cardMessage, eventsAfterCancel := newCancelledJobCard(t, env, conversation.ID, 702, 1)

			late := version.MessagePart{
				Type: "job_card", JobID: 702, JobKind: "requirement.curate",
				Status: testCase.status, Current: 3, Total: 5, Cancelable: testCase.cancelable,
			}
			view, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, late)
			if err != nil {
				t.Fatalf("late %s UpsertJobCard: %v", testCase.status, err)
			}
			if created || view.ID != cardMessage.ID {
				t.Fatalf("late %s projection = (%d, created=%v), want the stored card %d",
					testCase.status, view.ID, created, cardMessage.ID)
			}
			assertCancelledJobCardStored(t, env, conversation.ID, cardMessage.ID, 702, 1)
			if got := len(env.events.events); got != eventsAfterCancel {
				t.Fatalf("events after late %s projection = %d, want %d (no republish)",
					testCase.status, got, eventsAfterCancel)
			}
		})
	}

	t.Run("a lost row-lock race is a silent no-op", func(t *testing.T) {
		env := newConversationEnv(t)
		conversation := newChatConversation(t, env)
		cardMessage, eventsAfterCancel := newCancelledJobCard(t, env, conversation.ID, 705, 0)
		// The cancel commits after this projection's card lookup: the
		// repository's FOR UPDATE re-check sees the cancelled card and rejects
		// the rewrite. The app must treat that as an ignored write-back.
		env.repo.jobCardFindOverride = &version.Message{
			ID: cardMessage.ID, ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
			Parts: []version.MessagePart{{
				Type: "job_card", JobID: 705, JobKind: "requirement.curate",
				Status: "running", Current: 0, Total: 5, Cancelable: boolPtr(true),
			}},
		}
		env.repo.jobCardUpdateErr = version.ErrJobCardCancelled

		late := version.MessagePart{
			Type: "job_card", JobID: 705, JobKind: "requirement.curate",
			Status: "done", Current: 2, Total: 5,
		}
		view, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, late)
		if err != nil {
			t.Fatalf("late projection surfaced the concurrent cancel: %v", err)
		}
		if created || view.ID != 0 {
			t.Fatalf("late projection after a lost race = (%d, created=%v), want no write",
				view.ID, created)
		}
		assertCancelledJobCardStored(t, env, conversation.ID, cardMessage.ID, 705, 0)
		if got := len(env.events.events); got != eventsAfterCancel {
			t.Fatalf("events after lost-race projection = %d, want %d", got, eventsAfterCancel)
		}
	})
}

// TestRetryReopensCancelledCard pins the explicit retry reopen: the one write
// allowed to replace a cancelled card. The same message is reused (never a
// second card), the card returns to the non-terminal open snapshot, and the new
// attempt's running projection is accepted again.
func TestRetryReopensCancelledCard(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)
	cardMessage, _ := newCancelledJobCard(t, env, conversation.ID, 703, 3)

	if err := env.svc.ReopenJobCard(requestContext(), env.versionID, conversation.ID, 703); err != nil {
		t.Fatalf("ReopenJobCard: %v", err)
	}

	messages := env.repo.messages[conversation.ID]
	if len(messages) != 1 || messages[0].ID != cardMessage.ID {
		t.Fatalf("messages after reopen = %#v, want the original card %d", messages, cardMessage.ID)
	}
	card := messages[0].Parts[0]
	if card.Status != "open" {
		t.Fatalf("reopened card status = %q, want open", card.Status)
	}
	if card.Cancelable == nil || !*card.Cancelable {
		t.Fatalf("reopened card cancelable = %v, want true", card.Cancelable)
	}
	if card.JobKind != "requirement.curate" || card.Current != 3 || card.Total != 5 {
		t.Fatalf("reopen rewrote the card beyond the status/cancelable fields: %#v", card)
	}
	rows, err := env.svc.MessageParts(requestContext(), env.versionID, conversation.ID, cardMessage.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	payload := decodedStreamParts(t, rows)[0]
	if payload["status"] != "open" || payload["cancelable"] != true {
		t.Fatalf("reopened card row payload = %#v, want the open snapshot", payload)
	}
	event := eventParts(t, lastConversationMessageEvent(t, env, cardMessage.ID))
	if event[0]["status"] != "open" || event[0]["job_id"] != "703" {
		t.Fatalf("republished reopen event = %#v, want open job 703", event[0])
	}

	// The sticky guard is lifted for the new attempt: the runner's start
	// projection rewrites the same card to running.
	next := version.MessagePart{
		Type: "job_card", JobID: 703, JobKind: "requirement.curate",
		Status: "running", Current: 0, Total: 5, Cancelable: boolPtr(true),
	}
	view, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, next)
	if err != nil {
		t.Fatalf("UpsertJobCard after reopen: %v", err)
	}
	if created || view.ID != cardMessage.ID || view.Parts[0].Status != "running" {
		t.Fatalf("post-reopen projection = (%#v, created=%v), want the same card running", view, created)
	}
	if got := env.repo.messages[conversation.ID][0].Parts[0].Status; got != "running" {
		t.Fatalf("card status after the new attempt started = %q, want running", got)
	}
	if got := len(env.repo.messages[conversation.ID]); got != 1 {
		t.Fatalf("conversation messages = %d, want the one reused card", got)
	}
}

// TestJobCardReopenMissingNoop pins the no-card retry path: a job that never
// projected a card (legacy thread / non-conversation job) has nothing to
// reopen, so the best-effort call is a silent no-op.
func TestJobCardReopenMissingNoop(t *testing.T) {
	env := newConversationEnv(t)
	conversation := newChatConversation(t, env)
	eventsBefore := len(env.events.events)

	if err := env.svc.ReopenJobCard(requestContext(), env.versionID, conversation.ID, 704); err != nil {
		t.Fatalf("ReopenJobCard without a card: %v", err)
	}
	if got := len(env.repo.messages[conversation.ID]); got != 0 {
		t.Fatalf("messages = %d, want no card appended", got)
	}
	if got := len(env.events.events); got != eventsBefore {
		t.Fatalf("events = %d, want no republish for a missing card", got-eventsBefore)
	}
}
