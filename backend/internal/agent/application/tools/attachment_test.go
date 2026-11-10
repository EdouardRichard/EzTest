package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakePagedAttachment is a rune-aware AttachmentPort: it slices the stored text
// exactly like the version attachment service (offset/maxChars in characters).
// Images are projected in full on the first page like the version layer, so the
// tool-side pagination contract is exercised for real.
type fakePagedAttachment struct {
	text       string
	err        error
	offset     int
	maxChars   int
	status     string
	totalChars int
	images     []AttachmentImageDetail
	imageCount int
	// image_ref fake state (R2-C-02): both id kinds are recorded so a test can
	// prove which branch the tool executed and with which conversation scope.
	imageRef     AttachmentImage
	imageRefErr  error
	imageRefID   int64
	imageRefConv int64

	imageByImage     AttachmentImage
	imageByImageErr  error
	imageByImageID   int64
	imageByImageVer  int64
	imageByImageConv int64
}

func (f *fakePagedAttachment) ListAttachments(_ context.Context, _, _ int64) ([]AttachmentMeta, error) {
	return nil, nil
}

func (f *fakePagedAttachment) ReadAttachmentText(_ context.Context, _, _, attachmentID int64, offset, maxChars int) (AttachmentText, error) {
	f.offset, f.maxChars = offset, maxChars
	if f.err != nil {
		return AttachmentText{}, f.err
	}
	runes := []rune(f.text)
	total := len(runes)
	if f.totalChars > 0 {
		total = f.totalChars
	}
	end := offset + maxChars
	if end > len(runes) {
		end = len(runes)
	}
	text := ""
	if offset < len(runes) {
		text = string(runes[offset:end])
	}
	status := f.status
	if status == "" {
		status = "done"
	}
	result := AttachmentText{
		AttachmentID: attachmentID,
		Name:         "需求.md",
		MIME:         "text/markdown",
		Status:       status,
		Offset:       offset,
		Text:         text,
		TotalChars:   total,
		Truncated:    end < total,
	}
	if offset == 0 {
		result.Images = f.images
		result.ImageCount = f.imageCount
		if result.ImageCount == 0 {
			result.ImageCount = len(f.images)
		}
	}
	return result, nil
}

func (f *fakePagedAttachment) AttachmentImageRef(_ context.Context, _, conversationID, attachmentID int64) (AttachmentImage, error) {
	f.imageRefID = attachmentID
	f.imageRefConv = conversationID
	return f.imageRef, f.imageRefErr
}

// AttachmentImageRefByImage is the R2-C-02 image-id branch: it records the
// resolved image id plus the version/conversation scope so tests can prove the
// parent lookup stays conversation-scoped.
func (f *fakePagedAttachment) AttachmentImageRefByImage(_ context.Context, versionID, conversationID, imageID int64) (AttachmentImage, error) {
	f.imageByImageID = imageID
	f.imageByImageVer = versionID
	f.imageByImageConv = conversationID
	return f.imageByImage, f.imageByImageErr
}

// fakeImages builds count persisted image rows with captions from caption.
func fakeImages(count int, caption func(index int) string) []AttachmentImageDetail {
	images := make([]AttachmentImageDetail, 0, count)
	for index := 0; index < count; index++ {
		images = append(images, AttachmentImageDetail{
			ID: int64(9201 + index), Seq: index + 1, SHA256: fmt.Sprintf("sha-%d", index+1),
			MIME: "image/png", Width: 8, Height: 6, Status: "done",
			Caption: caption(index),
		})
	}
	return images
}

func readTextPage(t *testing.T, port *fakePagedAttachment, params string) map[string]any {
	t.Helper()
	registry := NewP1Registry(Deps{Attachment: port})
	result, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentReadText, params)
	if err != nil {
		t.Fatalf("attachment.read_text %s: %v", params, err)
	}
	return resultMap(t, result)
}

// TestReadTextReturnsNextOffset pins R-A6-2 on a 9000-character CJK document:
// the default page is 8000 characters and the second page advances by the
// actual returned count.
func TestReadTextReturnsNextOffset(t *testing.T) {
	content := strings.Repeat("中", 9000)
	port := &fakePagedAttachment{text: content}

	first := readTextPage(t, port, `{"attachment_id":"9001"}`)
	if first["returned_chars"] != float64(8000) || first["next_offset"] != float64(8000) {
		t.Fatalf("first page returned/next = %#v/%#v, want 8000/8000", first["returned_chars"], first["next_offset"])
	}
	if port.maxChars != 8000 || port.offset != 0 {
		t.Fatalf("default request args = offset %d max_chars %d, want 0/8000", port.offset, port.maxChars)
	}
	firstText, _ := first["text"].(string)
	if !utf8.ValidString(firstText) || len([]rune(firstText)) != 8000 {
		t.Fatalf("first page text has %d runes, valid=%v", len([]rune(firstText)), utf8.ValidString(firstText))
	}

	second := readTextPage(t, port, `{"attachment_id":"9001","offset":8000}`)
	if second["returned_chars"] != float64(1000) || second["next_offset"] != float64(9000) {
		t.Fatalf("second page returned/next = %#v/%#v, want 1000/9000", second["returned_chars"], second["next_offset"])
	}
	secondText, _ := second["text"].(string)
	if firstText+secondText != content {
		t.Fatalf("pages do not cover the document: %d + %d runes, want %d",
			len([]rune(firstText)), len([]rune(secondText)), len([]rune(content)))
	}
}

// TestReadTextCapsLimit pins the 20000-character cap: a larger requested limit
// is clamped rather than rejected, and next_offset follows the returned count.
func TestReadTextCapsLimit(t *testing.T) {
	content := strings.Repeat("中", 25000)
	port := &fakePagedAttachment{text: content}

	first := readTextPage(t, port, `{"attachment_id":"9001","max_chars":30000}`)
	if port.maxChars != MaxToolResultReadChars {
		t.Fatalf("requested max_chars = %d, want clamped to %d", port.maxChars, MaxToolResultReadChars)
	}
	if first["returned_chars"] != float64(20000) || first["next_offset"] != float64(20000) {
		t.Fatalf("capped page returned/next = %#v/%#v, want 20000/20000", first["returned_chars"], first["next_offset"])
	}

	second := readTextPage(t, port, `{"attachment_id":"9001","offset":20000,"max_chars":30000}`)
	if second["returned_chars"] != float64(5000) || second["next_offset"] != float64(25000) {
		t.Fatalf("final page returned/next = %#v/%#v, want 5000/25000", second["returned_chars"], second["next_offset"])
	}
}

// TestReadTextNextOffsetUsesReturnedCount proves the model cannot jump by the
// requested page size when the document ends earlier.
func TestReadTextNextOffsetUsesReturnedCount(t *testing.T) {
	port := &fakePagedAttachment{text: strings.Repeat("a", 5000)}
	page := readTextPage(t, port, fmt.Sprintf(`{"attachment_id":"9001","max_chars":%d}`, DefaultToolResultReadChars))
	if page["returned_chars"] != float64(5000) || page["next_offset"] != float64(5000) {
		t.Fatalf("short document page = %#v, want returned/next = 5000/5000", page)
	}
}

// TestReadTextImagePagingContract pins R2-C-01's tool-side pagination: images
// are paged by image_offset/image_limit, the page reports how many rows were
// returned and where the next page starts, and images_truncated means "more
// image rows remain" (not "a caption was cut").
func TestReadTextImagePagingContract(t *testing.T) {
	port := &fakePagedAttachment{
		text:   "page",
		images: fakeImages(12, func(index int) string { return fmt.Sprintf("图-%d", index+1) }),
	}

	first := readTextPage(t, port, `{"attachment_id":"9001","image_offset":10,"image_limit":5}`)
	images, ok := first["images"].([]any)
	if !ok || len(images) != 2 {
		t.Fatalf("images = %#v, want the last 2 rows of the 12-image document", first["images"])
	}
	if first["image_offset"] != float64(10) || first["image_returned"] != float64(2) {
		t.Fatalf("image paging = offset %#v / returned %#v, want 10/2",
			first["image_offset"], first["image_returned"])
	}
	if first["images_truncated"] != false || first["next_image_offset"] != float64(12) {
		t.Fatalf("image paging = truncated %#v / next %#v, want false/12",
			first["images_truncated"], first["next_image_offset"])
	}
	last := images[1].(map[string]any)
	if last["image_id"] != "9212" || last["caption"] != "图-12" {
		t.Fatalf("last image = %#v, want image_id 9212 / caption 图-12", last)
	}

	second := readTextPage(t, port, `{"attachment_id":"9001","image_offset":0,"image_limit":10}`)
	images, ok = second["images"].([]any)
	if !ok || len(images) != 10 {
		t.Fatalf("first image page = %#v, want 10 rows", second["images"])
	}
	if second["images_truncated"] != true || second["next_image_offset"] != float64(10) {
		t.Fatalf("first image page = truncated %#v / next %#v, want true/10",
			second["images_truncated"], second["next_image_offset"])
	}
	if first := images[0].(map[string]any); first["image_id"] != "9201" {
		t.Fatalf("first image = %#v, want image_id 9201", first)
	}
}

// TestReadTextKeepsImageCaptionsWhole pins the caption budget semantics: a
// caption that would cross the page budget moves whole to the next page (never
// cut in half); only a page whose first caption alone exceeds the budget is
// truncated and marked caption_truncated.
func TestReadTextKeepsImageCaptionsWhole(t *testing.T) {
	port := &fakePagedAttachment{
		text:   "page",
		images: fakeImages(12, func(int) string { return strings.Repeat("图", 500) }),
	}
	body := readTextPage(t, port, `{"attachment_id":"9001","image_offset":0,"image_limit":20}`)
	images, ok := body["images"].([]any)
	if !ok || len(images) != 8 {
		t.Fatalf("page = %d images, want 8 whole 500-rune captions (4000-rune page budget)",
			len(images))
	}
	for index, raw := range images {
		image := raw.(map[string]any)
		if got := len([]rune(image["caption"].(string))); got != 500 {
			t.Fatalf("image %d caption split: %d runes, want 500", index+1, got)
		}
		if _, truncated := image["caption_truncated"]; truncated {
			t.Fatalf("image %d carries caption_truncated; the whole caption should move to the next page", index+1)
		}
	}
	if body["images_truncated"] != true || body["next_image_offset"] != float64(8) {
		t.Fatalf("page = truncated %#v / next %#v, want true/8",
			body["images_truncated"], body["next_image_offset"])
	}

	// Degenerate path: the first image of the page alone exceeds the budget.
	// It is truncated to the budget, flagged, and the cursor still advances so
	// the model cannot loop on it forever.
	port.images[0].Caption = strings.Repeat("图", MaxAttachmentReadImageCaptionChars+500)
	oversized := readTextPage(t, port, `{"attachment_id":"9001","image_offset":0,"image_limit":20}`)
	images, ok = oversized["images"].([]any)
	if !ok || len(images) != 1 {
		t.Fatalf("oversized page = %d images, want 1", len(images))
	}
	image := images[0].(map[string]any)
	if got := len([]rune(image["caption"].(string))); got != MaxAttachmentReadImageCaptionChars {
		t.Fatalf("oversized caption runes = %d, want the %d-rune page budget", got, MaxAttachmentReadImageCaptionChars)
	}
	if image["caption_truncated"] != true {
		t.Fatalf("oversized image = %#v, want caption_truncated=true", image)
	}
	if oversized["next_image_offset"] != float64(1) {
		t.Fatalf("oversized page next_image_offset = %#v, want 1 (the cursor must advance)",
			oversized["next_image_offset"])
	}
}

// TestReadTextAttachmentSummaryReportsCompleteness pins R2-C-03: every result
// carries an `attachment` completeness summary whose total_chars is the
// attachment length (not the tool result envelope length).
func TestReadTextAttachmentSummaryReportsCompleteness(t *testing.T) {
	port := &fakePagedAttachment{
		text:       strings.Repeat("中", 8000),
		totalChars: 101118,
		images:     fakeImages(12, func(index int) string { return fmt.Sprintf("图-%d", index+1) }),
	}
	body := readTextPage(t, port, `{"attachment_id":"9001"}`)
	summary, ok := body["attachment"].(map[string]any)
	if !ok {
		t.Fatalf("attachment summary = %#v, want an object", body["attachment"])
	}
	if summary["status"] != "done" || summary["extraction_done"] != true {
		t.Fatalf("summary status = %#v/%#v, want done/true", summary["status"], summary["extraction_done"])
	}
	if summary["total_chars"] != float64(101118) {
		t.Fatalf("summary total_chars = %#v, want the attachment length 101118", summary["total_chars"])
	}
	if summary["returned_offset"] != float64(0) || summary["returned_chars"] != float64(8000) || summary["next_offset"] != float64(8000) {
		t.Fatalf("summary offset/returned/next = %#v/%#v/%#v, want 0/8000/8000",
			summary["returned_offset"], summary["returned_chars"], summary["next_offset"])
	}
	if summary["text_truncated"] != true || summary["text_complete"] != false {
		t.Fatalf("summary truncation = %#v/%#v, want true/false",
			summary["text_truncated"], summary["text_complete"])
	}
	if summary["image_count"] != float64(12) || summary["images_included"] != true {
		t.Fatalf("summary images = %#v/%#v, want 12/true", summary["image_count"], summary["images_included"])
	}
	paging, ok := summary["image_paging"].(map[string]any)
	if !ok {
		t.Fatalf("summary image_paging = %#v", summary["image_paging"])
	}
	if paging["offset"] != float64(0) || paging["limit"] != float64(20) || paging["returned"] != float64(12) ||
		paging["total"] != float64(12) || paging["truncated"] != false || paging["next_offset"] != float64(12) {
		t.Fatalf("image_paging = %#v", paging)
	}
	note, _ := summary["note"].(string)
	for _, want := range []string{"不要用工具结果外层信封", "total_chars", "text_complete"} {
		if !strings.Contains(note, want) {
			t.Fatalf("summary note lacks %q: %s", want, note)
		}
	}
	if !strings.Contains(note, "还有 93118 字符未读") || !strings.Contains(note, "offset=8000") {
		t.Fatalf("summary note lacks the text continuation hint: %s", note)
	}

	// Last page: the same summary reports text_complete=true.
	port.text = strings.Repeat("中", 101118)
	port.totalChars = 0
	last := readTextPage(t, port, `{"attachment_id":"9001","offset":101000}`)
	summary = last["attachment"].(map[string]any)
	if summary["total_chars"] != float64(101118) || summary["returned_offset"] != float64(101000) ||
		summary["returned_chars"] != float64(118) || summary["next_offset"] != float64(101118) {
		t.Fatalf("last page summary = %#v, want total/offset/returned/next = 101118/101000/118/101118", summary)
	}
	if summary["text_truncated"] != false || summary["text_complete"] != true {
		t.Fatalf("last page truncation = %#v/%#v, want false/true",
			summary["text_truncated"], summary["text_complete"])
	}
	if summary["images_included"] != false {
		t.Fatalf("last page images_included = %#v, want false (images are first-page only)", summary["images_included"])
	}
}

// TestReadTextAttachmentSummarySurvivesSpillHead pins the spill-head guarantee:
// Go marshals map keys in lexical order, so the `attachment` summary is the
// first key and survives inside the 2000-rune head even when the full result is
// spilled. Before R2-C-03 the attachment total_chars/truncated only appeared in
// the tail, which made the Host believe the document was 27k characters.
func TestReadTextAttachmentSummarySurvivesSpillHead(t *testing.T) {
	port := &fakePagedAttachment{
		text:       strings.Repeat("中", 8000),
		totalChars: 101118,
		images:     fakeImages(12, func(int) string { return strings.Repeat("图", 500) }),
	}
	body := readTextPage(t, port, `{"attachment_id":"9001"}`)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal read_text result: %v", err)
	}
	spilled, err := SpillToolResult(context.Background(), newFakeContentStore(), string(encoded),
		ToolResultPolicy{MaxBytes: DefaultToolResultMaxBytes})
	if err != nil {
		t.Fatalf("SpillToolResult: %v", err)
	}
	if !spilled.Truncated {
		t.Fatalf("test payload (%d bytes) did not spill", len(encoded))
	}
	for _, want := range []string{`"attachment":{`, `"total_chars":101118`, `"text_complete":false`} {
		if !strings.Contains(spilled.Head, want) {
			t.Fatalf("spill head lacks %q:\n%s", want, spilled.Head)
		}
	}
}

// TestAttachmentImageRefAcceptsImageID pins R2-C-02's primary exit: passing the
// images[].image_id returned by attachment.read_text resolves the parent
// attachment without the Host guessing or reusing the top-level attachment id.
// The schema and description must make the two id kinds self-describing.
func TestAttachmentImageRefAcceptsImageID(t *testing.T) {
	port := &fakePagedAttachment{
		imageByImage: AttachmentImage{
			AttachmentID: 9002, SHA256: "abc", URL: "/api/content/abc", MIME: "image/png",
			Images: []AttachmentImageDetail{{
				ID: 901, Seq: 1, SHA256: "abc", MIME: "image/png", Width: 8, Height: 6,
				Status: "done", Caption: "登录界面截图",
			}},
		},
	}
	registry := NewP1Registry(Deps{Attachment: port})
	result, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentImageRef, `{"image_id":"901"}`)
	if err != nil {
		t.Fatalf("attachment.image_ref by image_id: %v", err)
	}
	if port.imageByImageID != 901 || port.imageByImageVer != 10 || port.imageByImageConv != 20 {
		t.Fatalf("image lookup = image %d version %d conversation %d, want 901/10/20",
			port.imageByImageID, port.imageByImageVer, port.imageByImageConv)
	}
	if port.imageRefID != 0 {
		t.Fatalf("attachment_id branch also ran with %d", port.imageRefID)
	}
	body := resultMap(t, result)
	if body["attachment_id"] != "9002" {
		t.Fatalf("attachment_id = %#v, want the resolved parent 9002", body["attachment_id"])
	}
	images, ok := body["images"].([]any)
	if !ok || len(images) != 1 || images[0].(map[string]any)["caption"] != "登录界面截图" {
		t.Fatalf("images = %#v, want the parent's persisted caption", body["images"])
	}

	tool, ok := registry.Get(ToolAttachmentImageRef)
	if !ok {
		t.Fatal("attachment.image_ref is not registered")
	}
	var schema struct {
		Type        string                    `json:"type"`
		Description string                    `json:"description"`
		Required    []string                  `json:"required"`
		Properties  map[string]map[string]any `json:"properties"`
		OneOf       []struct {
			Required []string `json:"required"`
		} `json:"oneOf"`
		AnyOf json.RawMessage `json:"anyOf"`
	}
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("Parameters() is not valid JSON: %v", err)
	}
	if schema.Type != "object" || len(schema.Required) != 0 {
		t.Fatalf("schema = %#v, want an object whose ids are alternatives", schema)
	}
	if !strings.Contains(schema.Description, "二选一") {
		t.Fatalf("schema description = %q, want the exactly-one rule", schema.Description)
	}
	imageProperty, ok := schema.Properties["image_id"]
	if !ok {
		t.Fatalf("schema properties = %#v, want image_id", schema.Properties)
	}
	if description, _ := imageProperty["description"].(string); !strings.Contains(description, "images[].image_id") {
		t.Fatalf("image_id schema description = %q, want the read_text image id", description)
	}
	if len(schema.AnyOf) != 0 {
		t.Fatalf("schema anyOf = %s, want oneOf (anyOf allows both ids at once)", schema.AnyOf)
	}
	if len(schema.OneOf) != 2 {
		t.Fatalf("oneOf branches = %#v, want attachment_id/image_id alternatives", schema.OneOf)
	}
	branches := map[string]bool{}
	for _, branch := range schema.OneOf {
		if len(branch.Required) == 1 {
			branches[branch.Required[0]] = true
		}
	}
	if !branches["attachment_id"] || !branches["image_id"] {
		t.Fatalf("oneOf required = %#v, want one branch per id", schema.OneOf)
	}
	description := tool.Description()
	for _, want := range []string{
		"attachment_id 与 image_id 二选一", "images[].image_id", "顶层",
		"不会重新运行视觉分析", "docx", "note",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("attachment.image_ref description lacks %q: %s", want, description)
		}
	}
}

// TestAttachmentImageRefRejectsBothIDs pins the exactly-one contract: two ids are
// ambiguous, so the tool rejects the call instead of silently choosing one.
func TestAttachmentImageRefRejectsBothIDs(t *testing.T) {
	port := &fakePagedAttachment{}
	registry := NewP1Registry(Deps{Attachment: port})
	_, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentImageRef,
		`{"attachment_id":"9002","image_id":"901"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument", toolErr.Code)
	}
	if toolErr.Field != "attachment_id" {
		t.Fatalf("field = %q, want attachment_id", toolErr.Field)
	}
	const wantMessage = "provide either attachment_id or image_id, not both"
	if toolErr.Message != wantMessage {
		t.Fatalf("message = %q, want the English %q", toolErr.Message, wantMessage)
	}
	if !toolErr.Retryable {
		t.Fatalf("retryable = false, want true for a fixable call")
	}
	for _, want := range []string{"二选一", "attachment_id", "image_id"} {
		if !strings.Contains(toolErr.Hint, want) {
			t.Fatalf("hint = %q, want the actionable Chinese copy containing %q", toolErr.Hint, want)
		}
	}
	if detailsHint, _ := toolErr.Details["hint"].(string); detailsHint != toolErr.Hint {
		t.Fatalf("details.hint = %q, want %q", detailsHint, toolErr.Hint)
	}
	if port.imageRefID != 0 || port.imageByImageID != 0 {
		t.Fatalf("ambiguous call reached the port: attachment=%d image=%d", port.imageRefID, port.imageByImageID)
	}
}

// TestAttachmentImageRefRejectsNeitherID pins the other side of the exactly-one
// contract: an empty call names both accepted parameters and is retryable.
func TestAttachmentImageRefRejectsNeitherID(t *testing.T) {
	port := &fakePagedAttachment{}
	registry := NewP1Registry(Deps{Attachment: port})
	_, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentImageRef, `{}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument", toolErr.Code)
	}
	if toolErr.Field != "attachment_id" {
		t.Fatalf("field = %q, want attachment_id", toolErr.Field)
	}
	const wantMessage = "exactly one of attachment_id or image_id is required"
	if toolErr.Message != wantMessage {
		t.Fatalf("message = %q, want the English %q", toolErr.Message, wantMessage)
	}
	for _, want := range []string{"二选一", "attachment_id", "image_id"} {
		if !strings.Contains(toolErr.Hint, want) {
			t.Fatalf("hint = %q, want the actionable Chinese copy containing %q", toolErr.Hint, want)
		}
	}
	if detailsHint, _ := toolErr.Details["hint"].(string); detailsHint != toolErr.Hint {
		t.Fatalf("details.hint = %q, want %q", detailsHint, toolErr.Hint)
	}
	if !toolErr.Retryable {
		t.Fatalf("retryable = false, want true for a fixable call")
	}
	if port.imageRefID != 0 || port.imageByImageID != 0 {
		t.Fatalf("empty call reached the port: attachment=%d image=%d", port.imageRefID, port.imageByImageID)
	}
}

// TestAttachmentImageRefLocalizesUnknownAttachment pins R2-C-02's error path:
// a not_found keeps the English message envelope (the frontend localizes generic
// errors) but carries the actionable field/hint details that explain the id
// difference. Retryable is false: the same unknown id cannot succeed on a retry.
func TestAttachmentImageRefLocalizesUnknownAttachment(t *testing.T) {
	port := &fakePagedAttachment{
		imageRefErr:     platform.NotFound("attachment"),
		imageByImageErr: platform.NotFound("attachment image"),
	}
	registry := NewP1Registry(Deps{Attachment: port})

	_, err := execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentImageRef, `{"attachment_id":"9002"}`)
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeNotFound {
		t.Fatalf("code = %q, want not_found", toolErr.Code)
	}
	if toolErr.Retryable {
		t.Fatalf("retryable = true, want false for an unknown id")
	}
	if toolErr.Field != "attachment_id" {
		t.Fatalf("field = %q, want attachment_id", toolErr.Field)
	}
	if strings.Contains(toolErr.Message, "未找到") || !strings.Contains(toolErr.Message, "not found") {
		t.Fatalf("message = %q, want the English not_found envelope", toolErr.Message)
	}
	for _, want := range []string{"images[].image_id", "顶层", "不是"} {
		if !strings.Contains(toolErr.Hint, want) {
			t.Fatalf("hint = %q, want %q", toolErr.Hint, want)
		}
	}
	if !strings.Contains(toolErr.Hint, "9002") {
		t.Fatalf("hint = %q, want the attempted attachment id", toolErr.Hint)
	}
	payload := toolErr.Payload()
	errBody, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v", payload)
	}
	details, ok := errBody["details"].(map[string]any)
	if !ok {
		t.Fatalf("error payload = %#v, want details", errBody)
	}
	if details["field"] != "attachment_id" {
		t.Fatalf("details.field = %#v, want attachment_id", details["field"])
	}
	if hint, _ := details["hint"].(string); !strings.Contains(hint, "images[].image_id") {
		t.Fatalf("details.hint = %#v, want the id-difference hint", details["hint"])
	}
	if retryable, _ := errBody["retryable"].(bool); retryable {
		t.Fatalf("payload retryable = %#v, want false", errBody["retryable"])
	}

	// The image_id branch carries the same actionable details, naming the image
	// row id it could not resolve inside the current conversation.
	_, err = execute(t, registry, Scope{VersionID: 10, ConversationID: 20}, ToolAttachmentImageRef, `{"image_id":"901"}`)
	toolErr = asToolError(t, err)
	if toolErr.Code != CodeNotFound || toolErr.Retryable {
		t.Fatalf("image not_found = %#v, want a non-retryable not_found", toolErr)
	}
	if toolErr.Field != "image_id" {
		t.Fatalf("field = %q, want image_id", toolErr.Field)
	}
	if strings.Contains(toolErr.Message, "未找到") || !strings.Contains(toolErr.Message, "not found") {
		t.Fatalf("message = %q, want the English not_found envelope", toolErr.Message)
	}
	for _, want := range []string{"images[].image_id", "attachment_id", "不是", "901"} {
		if !strings.Contains(toolErr.Hint, want) {
			t.Fatalf("image hint = %q, want %q", toolErr.Hint, want)
		}
	}
}

// TestReadTextDescriptionDocumentsCompletenessAndImagePaging pins the contract
// the model reads: the tool description must document image paging (with its
// continuation call) and where attachment completeness is authoritative.
func TestReadTextDescriptionDocumentsCompletenessAndImagePaging(t *testing.T) {
	tool, ok := NewP1Registry(Deps{}).Get(ToolAttachmentReadText)
	if !ok {
		t.Fatal("attachment.read_text is not registered")
	}
	description := tool.Description()
	for _, want := range []string{
		"image_offset", "image_limit", "整条 caption 不拆分", "image_offset=next_image_offset",
		"offset=0", "text_complete", "total_chars", "tool_result.read", "外层",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("attachment.read_text description lacks %q: %s", want, description)
		}
	}
}
