package agentapp

// v0.22 W0 fix round 1 (Task 36 review Important 10 / Task 19 handoff): the
// attachment task pack carries the extracted image references/captions and
// charges 1500 tokens per image against the extracted-text budget.

import (
	"strings"
	"testing"
)

func TestAttachmentUserTextRendersImagesAndCaptions(t *testing.T) {
	info := AttachmentInfo{
		Name:       "requirements.docx",
		MIME:       "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Status:     "done",
		Text:       "登录页面需要支持短信验证码登录。",
		TotalChars: 17,
		Images: []AttachmentImageInfo{
			{ID: 9001, Seq: 1, SHA256: strings.Repeat("a", 64), MIME: "image/png", Width: 640, Height: 480,
				SourceKind: "embedded", Status: "done", Caption: "登录页原型：手机号 + 验证码输入框"},
			{ID: 9002, Seq: 2, SHA256: strings.Repeat("b", 64), MIME: "image/png",
				SourceKind: "embedded", Status: "failed", Error: "vision_analysis_failed"},
		},
		ImageTokens: 2 * AttachmentImageTokenBudget,
	}
	text := attachmentUserText(info)
	for _, want := range []string{
		"requirements.docx", "登录页面需要支持短信验证码登录",
		"image_id=9001", "seq=1", "image/png", "640x480",
		"caption=登录页原型", "image_id=9002", "error=vision_analysis_failed",
		"image_tokens=3000",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("attachment pack missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "data_ref") || strings.Contains(text, "base64") {
		t.Fatalf("attachment pack leaked image bytes: %s", text)
	}
}

func TestAttachmentUserTextImageTokensShrinkTextBudget(t *testing.T) {
	longText := strings.Repeat("长", 4000)
	base := attachmentUserText(AttachmentInfo{Name: "a.docx", Status: "done", Text: longText, TotalChars: 4000})
	if !strings.Contains(base, longText) {
		t.Fatalf("no-image pack truncated the configured text budget")
	}
	budgeted := attachmentUserText(AttachmentInfo{
		Name: "a.docx", Status: "done", Text: longText, TotalChars: 4000,
		Images:      []AttachmentImageInfo{{ID: 1, Seq: 1, Status: "done"}},
		ImageTokens: AttachmentImageTokenBudget,
	})
	if strings.Contains(budgeted, strings.Repeat("长", 2000)) {
		t.Fatal("image token charge did not shrink the extracted-text budget")
	}
	if !strings.Contains(budgeted, "image_tokens=1500") {
		t.Fatalf("image token accounting missing: %s", budgeted)
	}
}
