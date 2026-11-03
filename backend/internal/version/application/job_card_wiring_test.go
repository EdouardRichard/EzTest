package versionapp_test

import (
	"encoding/json"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

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
