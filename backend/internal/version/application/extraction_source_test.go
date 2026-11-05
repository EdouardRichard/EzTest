package versionapp_test

// Task 37 fix round 1: the production attachment-side ExtractionIngestSource.
//
// The source must re-parse the attachment's immutable original content, key the
// run by the original object sha, emit the deterministic segment plan, and
// honor input_sha256 when the terminal projection asks for the run's document
// instead of the attachment's current one.

import (
	"context"
	"strings"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

func extractionSourceDocText() string {
	return "FR-001 用户登录\nFR-002 用户下单\nFR-003 退款申请"
}

func extractionSourceResult() versionapp.ExtractionResult {
	return versionapp.ExtractionResult{
		Text: extractionSourceDocText(),
		Elements: []versionapp.ExtractionElement{
			{Kind: "heading", Text: "功能需求", HeadingPath: "功能需求", Source: "word/document.xml"},
			{Kind: "paragraph", Text: "FR-001 用户登录", HeadingPath: "功能需求", Source: "word/document.xml"},
			{Kind: "paragraph", Text: "FR-002 用户下单", HeadingPath: "功能需求", Source: "word/document.xml"},
			{Kind: "paragraph", Text: "FR-003 退款申请", HeadingPath: "功能需求", Source: "word/document.xml"},
		},
		Coverage: versionapp.ExtractionCoverage{
			Found: 4, Parsed: 4, ImagesFound: 3, ImagesParsed: 2,
		},
	}
}

// TestAttachmentExtractionSourceResolvesReadyInputs pins the production
// ExtractionIngestSource contract: only done attachments are input candidates,
// the input sha is the original content object sha, and the document resolves
// only for that sha (honoring input_sha256).
func TestAttachmentExtractionSourceResolvesReadyInputs(t *testing.T) {
	env := newAttachmentEnv(t)
	sha := strings.Repeat("a", 64)
	env.extractor.result = extractionSourceResult()
	done := registerAttachment(t, env, sha, "需求文档.md", "text/markdown", []byte("# 需求"))
	if _, err := env.svc.Extract(requestContext(), done.ID); err != nil {
		t.Fatalf("Extract done attachment: %v", err)
	}
	// A second attachment stays pending: it is not a ready extraction input.
	pending := registerAttachment(t, env, strings.Repeat("b", 64), "pending.md", "text/markdown", []byte("# pending"))

	inputs, err := env.svc.ExtractionInputs(context.Background(), env.versionID, 0)
	if err != nil {
		t.Fatalf("ExtractionInputs: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want only the done attachment: %#v", len(inputs), inputs)
	}
	input := inputs[0]
	if input.VersionID != env.versionID || input.AttachmentID != done.ID {
		t.Fatalf("input identity = %d/%d, want %d/%d", input.VersionID, input.AttachmentID, env.versionID, done.ID)
	}
	if input.InputSHA256 != sha {
		t.Fatalf("input sha = %q, want the original content sha %q", input.InputSHA256, sha)
	}
	if input.AttachmentName != "需求文档.md" || !strings.Contains(input.DocumentText, "FR-001") {
		t.Fatalf("input document metadata = %#v", input)
	}
	if len(input.Segments) == 0 || input.Segments[0].Seq != 1 {
		t.Fatalf("input segments = %#v", input.Segments)
	}

	document, err := env.svc.ExtractionDocument(context.Background(), env.versionID, done.ID, sha)
	if err != nil {
		t.Fatalf("ExtractionDocument: %v", err)
	}
	if document.Name != "需求文档.md" || !strings.Contains(document.Text, "FR-002") {
		t.Fatalf("document = %#v", document)
	}
	if document.SkippedImages != 1 {
		t.Fatalf("skipped images = %d, want ImagesFound-ImagesParsed = 1", document.SkippedImages)
	}

	segments, err := env.svc.ExtractionSegments(context.Background(), env.versionID, done.ID, sha)
	if err != nil {
		t.Fatalf("ExtractionSegments: %v", err)
	}
	if len(segments) != len(input.Segments) || segments[0].TextSHA256 != input.Segments[0].TextSHA256 {
		t.Fatalf("segment plan is not deterministic: %#v vs %#v", segments, input.Segments)
	}

	// input_sha256 is honored: the requested document is reproduced from the
	// original content, so a foreign sha must not return the current parse.
	if _, err := env.svc.ExtractionDocument(context.Background(), env.versionID, done.ID, strings.Repeat("f", 64)); err == nil {
		t.Fatal("ExtractionDocument accepted a foreign input_sha256")
	}
	if _, err := env.svc.ExtractionSegments(context.Background(), env.versionID, done.ID, strings.Repeat("f", 64)); err == nil {
		t.Fatal("ExtractionSegments accepted a foreign input_sha256")
	}

	// An unknown/unready attachment yields no input (the service maps this to
	// its typed 404), not a repository error.
	unready, err := env.svc.ExtractionInputs(context.Background(), env.versionID, pending.ID)
	if err != nil || len(unready) != 0 {
		t.Fatalf("unready attachment inputs = %#v/%v, want empty/nil", unready, err)
	}
	unknown, err := env.svc.ExtractionInputs(context.Background(), env.versionID, 999999)
	if err != nil || len(unknown) != 0 {
		t.Fatalf("unknown attachment inputs = %#v/%v, want empty/nil", unknown, err)
	}
}
