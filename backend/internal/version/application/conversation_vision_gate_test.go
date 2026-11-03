package versionapp_test

import (
	"net/http"
	"strings"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// visionConversationCatalog is the minimal vision-capable catalog image tests
// need now that PostUserMessage verifies the effective model before accepting an
// image draft. Production always wires the real LLM catalog at the composition
// root.
func visionConversationCatalog() *fakeConversationModelCatalog {
	return &fakeConversationModelCatalog{
		defaultModel: "vision-llm",
		selectable:   map[string]string{"vision-llm": "vision-llm"},
	}
}

// TestSendBlockedForNonVisionImage covers Task 75 / admin B1: an image draft
// pinned to a model without supports_vision must be rejected with the typed
// 400 before the message is stored or the chat.turn/requirement.curate jobs are
// enqueued. The admin trial accepted the message with 201 and then produced two
// opaque zero-step job failures, so the gate has to run before any of those side
// effects. A vision-capable model still accepts the same draft (no regression)
// and the conversation is never switched to another model behind the user.
func TestSendBlockedForNonVisionImage(t *testing.T) {
	env := newConversationEnv(t)
	catalog := &fakeConversationModelCatalog{
		defaultModel: "vision-llm",
		selectable: map[string]string{
			"vision-llm": "vision-llm",
			"text-only":  "text-only",
		},
		visionless: map[string]bool{"text-only": true},
	}
	env.svc.SetModelCatalog(catalog)

	textOnly := "text-only"
	conversation, err := env.svc.CreateWithModel(
		requestContext(), env.versionID, "clarification", "Chat", &textOnly, 42)
	if err != nil {
		t.Fatalf("CreateWithModel: %v", err)
	}
	tasksBefore := len(env.queue.tasks)
	image := version.MessagePart{
		Type: version.MessagePartImage, MIME: "image/png",
		DataRef: strings.Repeat("e", 64), Width: 800, Height: 600,
	}
	parts := []version.MessagePart{
		{Type: version.MessagePartText, Text: "这张截图说明了什么？"},
		image,
	}

	if _, err := env.svc.PostUserMessage(
		requestContext(), env.versionID, conversation.ID, parts, "image-nonvision", 42); err == nil {
		t.Fatal("image send with a non-vision model was accepted")
	} else {
		apiErr := apiError(t, err)
		if apiErr.Status != http.StatusBadRequest || apiErr.Code != "llm_capability_mismatch" || apiErr.Retryable {
			t.Fatalf("error = %#v, want non-retryable 400 llm_capability_mismatch", apiErr)
		}
		if apiErr.Details["model"] != "text-only" {
			t.Fatalf("details = %#v, want the effective model text-only", apiErr.Details)
		}
		missing, ok := apiErr.Details["missing_capabilities"].([]string)
		if !ok || len(missing) != 1 || missing[0] != "vision" {
			t.Fatalf("missing_capabilities = %#v, want [vision]", apiErr.Details["missing_capabilities"])
		}
	}
	// No 201 ghost message and no expected-to-fail background jobs.
	if got := len(env.repo.messages[conversation.ID]); got != 0 {
		t.Fatalf("rejected image send stored %d messages", got)
	}
	if got := len(env.queue.tasks); got != tasksBefore {
		t.Fatalf("rejected image send enqueued tasks: before=%d after=%d (%#v)",
			tasksBefore, got, env.queue.tasks)
	}

	// No silent model switch: the conversation stays pinned to text-only.
	if stored := env.repo.conversations[conversation.ID].ModelOverride; stored == nil || *stored != "text-only" {
		t.Fatalf("stored override = %#v, want text-only", stored)
	}

	// A vision-capable model still accepts the same image draft and enqueues the
	// normal reply/curate tasks.
	vision := "vision-llm"
	visionConversation, err := env.svc.CreateWithModel(
		requestContext(), env.versionID, "clarification", "Chat", &vision, 42)
	if err != nil {
		t.Fatalf("CreateWithModel(vision): %v", err)
	}
	message, err := env.svc.PostUserMessage(
		requestContext(), env.versionID, visionConversation.ID, parts, "image-vision", 42)
	if err != nil {
		t.Fatalf("image send with a vision model: %v", err)
	}
	if _, ok := env.queue.find("chat.turn", "reply:"+idString(message.ID)); !ok {
		t.Fatalf("vision send did not enqueue chat.turn: %#v", env.queue.tasks)
	}
	if _, ok := env.queue.find("requirement.curate", "curate:"+idString(message.ID)); !ok {
		t.Fatalf("vision send did not enqueue requirement.curate: %#v", env.queue.tasks)
	}
}

// TestSendBlockedForNonVisionUploadedImage covers the real UI path: the
// composer submits an uploaded image as an attachment part and the attachment
// service converts it into an image part during preparation. The vision gate
// runs on the converted parts, so that path is blocked too instead of only the
// direct-image API path.
func TestSendBlockedForNonVisionUploadedImage(t *testing.T) {
	env := newImageConversationEnv(t)
	env.svc.SetModelCatalog(&fakeConversationModelCatalog{
		defaultModel: "text-only",
		selectable:   map[string]string{"text-only": "text-only"},
		visionless:   map[string]bool{"text-only": true},
	})
	data := pngImageBytes(800, 600)
	sha := strings.Repeat("e", 64)
	env.attachments.store.put(sha, data, "image/png")
	parts := []version.MessagePart{
		{Type: version.MessagePartText, Text: "这张截图说明了什么？"},
		{Type: version.MessagePartAttachment, SHA256: sha, Name: "shot.png", MIME: "image/png", Size: int64(len(data))},
	}

	if _, err := env.svc.PostUserMessage(
		requestContext(), env.versionID, env.conversation.ID, parts, "image-upload-nonvision", 42); err == nil {
		t.Fatal("uploaded image send with a non-vision model was accepted")
	} else if apiErr := apiError(t, err); apiErr.Code != "llm_capability_mismatch" {
		t.Fatalf("error = %#v, want llm_capability_mismatch", apiErr)
	}
	if got := len(env.repo.messages[env.conversation.ID]); got != 0 {
		t.Fatalf("rejected uploaded image stored %d messages", got)
	}
}
