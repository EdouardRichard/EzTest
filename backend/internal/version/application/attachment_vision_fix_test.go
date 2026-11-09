package versionapp_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// imageExtractionResult builds the parser outcome of a document with one
// embedded image.
func imageExtractionResult() versionapp.ExtractionResult {
	return versionapp.ExtractionResult{
		Text:   "登录界面示意",
		Images: []versionapp.ExtractionImage{fakeExtractedImage()},
		Coverage: versionapp.ExtractionCoverage{
			Found: 2, Parsed: 2, ImagesFound: 1, ImagesParsed: 1,
		},
	}
}

// twoImageExtractionResult builds a parser outcome with two embedded images so
// one image can succeed while the other fails.
func twoImageExtractionResult() versionapp.ExtractionResult {
	first := fakeExtractedImage()
	second := fakeExtractedImage()
	second.Seq, second.Name, second.Source = 2, "image2.png", "word/media/image2.png"
	return versionapp.ExtractionResult{
		Text:   "两张图片",
		Images: []versionapp.ExtractionImage{first, second},
		Coverage: versionapp.ExtractionCoverage{
			Found: 3, Parsed: 3, ImagesFound: 2, ImagesParsed: 2,
		},
	}
}

// payloadOf returns the payload of the first event of one type.
func payloadOf(events *fakePublisher, eventType string) (map[string]any, bool) {
	for _, event := range events.events {
		if event.Type == eventType {
			return event.Payload, true
		}
	}
	return nil, false
}

// failedSkippedImageAttachment registers one image-bearing document and runs an
// extraction without a vision model, leaving the attachment failed with a
// skipped image row (the state the UI shows before a reanalyze).
func failedSkippedImageAttachment(t *testing.T, env *attachmentEnv) versionapp.AttachmentView {
	t.Helper()
	env.vision.available = false
	data := []byte("docx")
	sha := strings.Repeat("c", 64)
	view := registerAttachment(t, env, sha, "with-image.docx", docxMIME, data)
	env.extractor.result = imageExtractionResult()
	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Attachment.ExtractionStatus != version.AttachmentFailed {
		t.Fatalf("extraction = %#v, want the no-vision failure", result)
	}
	return view
}

// TestReanalyzeWithoutVisionModelRejected covers the reanalyze precondition: no
// vision model is a typed 400 vision_required (not a 200 with a silent no-op)
// and the unavailable event carries the concrete reason.
func TestReanalyzeWithoutVisionModelRejected(t *testing.T) {
	env := newAttachmentEnv(t)
	view := failedSkippedImageAttachment(t, env)

	if _, err := env.svc.Reanalyze(requestContext(), view.ID); !errors.Is(err, version.ErrVisionRequired) {
		t.Fatalf("Reanalyze err = %v, want ErrVisionRequired", err)
	} else {
		apiErr := apiError(t, err)
		if apiErr.Status != http.StatusBadRequest || apiErr.Code != version.AttachmentErrorVisionRequired || apiErr.Retryable {
			t.Fatalf("error = %#v, want non-retryable 400 vision_required", apiErr)
		}
	}
	rows, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageSkipped {
		t.Fatalf("rows = %#v, want the skipped row untouched", rows)
	}

	payload, ok := payloadOf(env.events, "model.vision.unavailable")
	if !ok {
		t.Fatalf("events = %#v, want model.vision.unavailable", eventTypes(env.events))
	}
	reason, _ := payload["reason"].(string)
	if strings.TrimSpace(reason) == "" {
		t.Fatalf("unavailable payload = %#v, want a concrete reason", payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, reason) {
		t.Fatalf("unavailable message = %q, want it to carry the reason %q", message, reason)
	}
}

// TestReanalyzeFailureMarksAttachment covers the retry failure path: a vision
// error during reanalyze marks the image and the whole attachment failed with
// the stable vision_analysis_failed code rather than returning a fake success.
func TestReanalyzeFailureMarksAttachment(t *testing.T) {
	env := newAttachmentEnv(t)
	view := failedSkippedImageAttachment(t, env)
	factsBefore := len(env.facts.facts)

	env.vision.available = true
	env.vision.err = errors.New("vision upstream returned HTTP 500")
	rows, err := env.svc.Reanalyze(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Reanalyze: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageFailed ||
		!strings.Contains(rows[0].Error, "vision upstream returned HTTP 500") {
		t.Fatalf("rows = %#v", rows)
	}
	stored := env.repo.attachments[view.ID]
	if stored.ExtractionStatus != version.AttachmentFailed ||
		stored.ExtractionErrorCode != version.AttachmentErrorVisionAnalysis ||
		!strings.Contains(stored.ExtractionErrorText, "vision upstream returned HTTP 500") {
		t.Fatalf("attachment after failed reanalyze = %#v", stored)
	}
	if len(env.facts.facts) != factsBefore {
		t.Fatalf("failed reanalyze wrote a fact: %#v", env.facts.facts)
	}
	if !containsString(eventTypes(env.events), "attachment.image.failed") {
		t.Fatalf("events = %#v, want attachment.image.failed", eventTypes(env.events))
	}
}

// TestMultiImagePartialFailure covers the per-image aggregate decision: one
// image succeeds (caption + fact + analyzed event) while another fails (failed
// row + failed event) and the attachment as a whole fails.
func TestMultiImagePartialFailure(t *testing.T) {
	env := newAttachmentEnv(t)
	data := []byte("docx")
	sha := strings.Repeat("d", 64)
	view := registerAttachment(t, env, sha, "two-images.docx", docxMIME, data)
	env.extractor.result = twoImageExtractionResult()
	env.vision.callErrs = []error{nil, errors.New("second image was rejected by the vision model")}

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Attachment.ExtractionStatus != version.AttachmentFailed ||
		result.ErrorCode != version.AttachmentErrorVisionAnalysis {
		t.Fatalf("extraction = %#v, want the partial-failure outcome", result)
	}
	if !strings.Contains(result.Attachment.ExtractionErrorText, "1 of 2") {
		t.Fatalf("error text = %q, want the per-image failure count", result.Attachment.ExtractionErrorText)
	}
	rows, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Status != version.AttachmentImageDone || rows[0].Caption == "" {
		t.Fatalf("first image = %#v, want done with a caption", rows[0])
	}
	if rows[1].Status != version.AttachmentImageFailed || !strings.Contains(rows[1].Error, "second image") {
		t.Fatalf("second image = %#v, want failed with its reason", rows[1])
	}
	if len(env.facts.facts) != 1 || env.facts.facts[0].Payload["seq"] != 1 {
		t.Fatalf("facts = %#v, want only the successful image's caption", env.facts.facts)
	}
	for _, want := range []string{"attachment.image.analyzed", "attachment.image.failed"} {
		if !containsString(eventTypes(env.events), want) {
			t.Fatalf("events = %#v, want %s", eventTypes(env.events), want)
		}
	}
}

// TestReanalyzeRecoversExtractedRowsAfterCrash pins recovery after a crash
// between the reanalyze reset and the vision calls: rows already sitting in the
// claimable extracted state must still be analyzed and finish the attachment.
func TestReanalyzeRecoversExtractedRowsAfterCrash(t *testing.T) {
	env := newAttachmentEnv(t)
	view := failedSkippedImageAttachment(t, env)
	// The crash happened after ResetImagesForReanalysis flipped the row back to
	// extracted, before any vision call.
	if _, err := env.repo.ResetImagesForReanalysis(requestContext(), view.ID, testTime); err != nil {
		t.Fatalf("simulate reset: %v", err)
	}
	before, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(before) != 1 || before[0].Status != version.AttachmentImageExtracted {
		t.Fatalf("simulated crash state = %#v", before)
	}

	env.vision.available = true
	env.vision.caption = "崩溃恢复后的截图说明"
	rows, err := env.svc.Reanalyze(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Reanalyze: %v", err)
	}
	if env.vision.calls != 1 {
		t.Fatalf("vision calls = %d, want the extracted row analyzed", env.vision.calls)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageDone || rows[0].Caption == "" {
		t.Fatalf("rows = %#v, want the recovered row done", rows)
	}
	if stored := env.repo.attachments[view.ID]; stored.ExtractionStatus != version.AttachmentDone {
		t.Fatalf("attachment = %#v, want done after recovery", stored)
	}
}

// TestVisionAvailabilityErrorIsRetryable covers the distinction between a
// missing vision model (typed 400 vision_required) and a transient availability
// failure (a retryable Go error that never persists skipped rows or fails the
// attachment as if the configuration were wrong).
func TestVisionAvailabilityErrorIsRetryable(t *testing.T) {
	t.Run("extract", func(t *testing.T) {
		env := newAttachmentEnv(t)
		data := []byte("docx")
		sha := strings.Repeat("e", 64)
		view := registerAttachment(t, env, sha, "with-image.docx", docxMIME, data)
		env.extractor.result = imageExtractionResult()
		env.vision.availableErr = errors.New("temporary vision catalog outage")

		if _, err := env.svc.Extract(requestContext(), view.ID); err == nil {
			t.Fatal("transient availability error was swallowed")
		} else if errors.Is(err, version.ErrVisionRequired) {
			t.Fatalf("transient availability error became vision_required: %v", err)
		} else {
			apiErr := &platform.Error{}
			if errors.As(err, &apiErr) && apiErr.Code == version.AttachmentErrorVisionRequired {
				t.Fatalf("transient availability error became vision_required: %#v", apiErr)
			}
		}
		// The availability check runs before the row is claimed, so a transient
		// failure leaves the attachment claimable for the ingest retry.
		if stored := env.repo.attachments[view.ID]; stored.ExtractionStatus != version.AttachmentPending {
			t.Fatalf("attachment = %#v, want it left retryable (pending)", stored)
		}
		rows, err := env.svc.ListImages(requestContext(), view.ID)
		if err != nil {
			t.Fatalf("ListImages: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("transient error persisted image rows: %#v", rows)
		}
	})

	t.Run("send", func(t *testing.T) {
		env := newAttachmentEnv(t)
		failedSkippedImageAttachment(t, env)
		env.vision.availableErr = errors.New("temporary vision catalog outage")
		sha := strings.Repeat("c", 64)
		part := attachmentPart(sha, "with-image.docx", docxMIME, int64(len(env.store.data[sha])))
		_, _, err := env.svc.PrepareUserMessageParts(requestContext(), env.versionID, 6001,
			[]version.MessagePart{{Type: version.MessagePartText, Text: "看下这个文档"}, part}, 42)
		if err == nil {
			t.Fatal("transient availability error let the image document through")
		}
		if errors.Is(err, version.ErrVisionRequired) {
			t.Fatalf("transient availability error became vision_required: %v", err)
		}
		apiErr := &platform.Error{}
		if errors.As(err, &apiErr) && apiErr.Code == version.AttachmentErrorVisionRequired {
			t.Fatalf("transient availability error became vision_required: %#v", apiErr)
		}
	})

	t.Run("reanalyze", func(t *testing.T) {
		env := newAttachmentEnv(t)
		view := failedSkippedImageAttachment(t, env)
		env.vision.availableErr = errors.New("temporary vision catalog outage")
		if _, err := env.svc.Reanalyze(requestContext(), view.ID); err == nil {
			t.Fatal("transient availability error was swallowed by reanalyze")
		} else if errors.Is(err, version.ErrVisionRequired) {
			t.Fatalf("transient availability error became vision_required: %v", err)
		}
		rows, err := env.svc.ListImages(requestContext(), view.ID)
		if err != nil {
			t.Fatalf("ListImages: %v", err)
		}
		if len(rows) != 1 || rows[0].Status != version.AttachmentImageSkipped {
			t.Fatalf("transient error reset the rows: %#v", rows)
		}
	})
}

// TestVisionRequiredErrorCarriesTypedDiagnosis pins the M-07 error contract:
// the typed 400 vision_required identifies the deterministic cause and carries
// the config/model identity, the missing capabilities and a Chinese next action
// so the UI can explain which setting is broken instead of showing a generic
// "no vision model" message.
func TestVisionRequiredErrorCarriesTypedDiagnosis(t *testing.T) {
	env := newAttachmentEnv(t)
	view := failedSkippedImageAttachment(t, env)
	env.vision.availability = versionapp.ImageAvailability{
		ReasonCode: versionapp.VisionReasonDefaultModelNotVision,
		ConfigID:   "7", ConfigName: "主配置", ModelName: "gpt-4",
		Missing: []string{"vision"},
	}

	_, err := env.svc.Reanalyze(requestContext(), view.ID)
	if !errors.Is(err, version.ErrVisionRequired) {
		t.Fatalf("Reanalyze err = %v, want ErrVisionRequired", err)
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != version.AttachmentErrorVisionRequired || apiErr.Retryable {
		t.Fatalf("error = %#v, want non-retryable 400 vision_required", apiErr)
	}
	if apiErr.Details["reason_code"] != versionapp.VisionReasonDefaultModelNotVision {
		t.Fatalf("reason_code = %#v", apiErr.Details["reason_code"])
	}
	if apiErr.Details["config_id"] != "7" || apiErr.Details["config_name"] != "主配置" {
		t.Fatalf("config details = %#v", apiErr.Details)
	}
	if apiErr.Details["model"] != "gpt-4" {
		t.Fatalf("model = %#v", apiErr.Details["model"])
	}
	missing, _ := apiErr.Details["missing_capabilities"].([]string)
	if len(missing) != 1 || missing[0] != "vision" {
		t.Fatalf("missing_capabilities = %#v", apiErr.Details["missing_capabilities"])
	}
	if apiErr.Details["version_id"] != idString(env.versionID) || apiErr.Details["attachment_id"] != idString(view.ID) {
		t.Fatalf("attachment details = %#v", apiErr.Details)
	}
	nextAction, _ := apiErr.Details["next_action"].(string)
	if !strings.Contains(nextAction, "视觉") || !strings.Contains(nextAction, "LLM") {
		t.Fatalf("next_action = %q, want a Chinese configuration hint", nextAction)
	}
	if !strings.Contains(apiErr.Message, "视觉") {
		t.Fatalf("message = %q, want a Chinese cause-specific message", apiErr.Message)
	}
}

// TestVisionAvailabilityTransientReturns503 pins the other half of the M-07
// split: a transient gateway problem must stay a retryable 503 ai_unavailable,
// never a 400 vision_required that tells the user to fix a configuration.
func TestVisionAvailabilityTransientReturns503(t *testing.T) {
	env := newAttachmentEnv(t)
	view := failedSkippedImageAttachment(t, env)
	env.vision.availability = versionapp.ImageAvailability{
		ReasonCode: versionapp.VisionReasonGatewayUnreachable, Retryable: true,
	}

	_, err := env.svc.Reanalyze(requestContext(), view.ID)
	if err == nil {
		t.Fatal("transient availability failure was swallowed")
	}
	if errors.Is(err, version.ErrVisionRequired) {
		t.Fatalf("transient failure became vision_required: %v", err)
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != "ai_unavailable" || !apiErr.Retryable {
		t.Fatalf("error = %#v, want retryable 503 ai_unavailable", apiErr)
	}
	if apiErr.Details["reason_code"] != versionapp.VisionReasonGatewayUnreachable {
		t.Fatalf("reason_code = %#v", apiErr.Details["reason_code"])
	}
	rows, listErr := env.svc.ListImages(requestContext(), view.ID)
	if listErr != nil {
		t.Fatalf("ListImages: %v", listErr)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageSkipped {
		t.Fatalf("transient error reset the rows: %#v", rows)
	}
}

// TestCaptionFactPersistedBeforeAnalyzedEvent pins the success ordering: the
// caption fact is stored before attachment.image.analyzed is published, so a
// fact-store failure fails the image (clearing its caption) and never emits an
// analyzed event for a row that has no fact.
func TestCaptionFactPersistedBeforeAnalyzedEvent(t *testing.T) {
	env := newAttachmentEnv(t)
	env.facts.err = errors.New("fact store unavailable")
	data := []byte("docx")
	sha := strings.Repeat("f", 64)
	view := registerAttachment(t, env, sha, "with-image.docx", docxMIME, data)
	env.extractor.result = imageExtractionResult()

	result, err := env.svc.Extract(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Attachment.ExtractionStatus != version.AttachmentFailed ||
		result.ErrorCode != version.AttachmentErrorVisionAnalysis {
		t.Fatalf("extraction = %#v, want the fact-store failure", result)
	}
	rows, err := env.svc.ListImages(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != version.AttachmentImageFailed {
		t.Fatalf("rows = %#v, want the image failed", rows)
	}
	if rows[0].Caption != "" {
		t.Fatalf("failed image exposes a caption: %#v", rows[0])
	}
	if containsString(eventTypes(env.events), "attachment.image.analyzed") {
		t.Fatalf("events = %#v, analyzed was published before the fact was stored", eventTypes(env.events))
	}
	if !containsString(eventTypes(env.events), "attachment.image.failed") {
		t.Fatalf("events = %#v, want attachment.image.failed", eventTypes(env.events))
	}
}
