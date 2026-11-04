package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakePagedAttachment is a rune-aware AttachmentPort: it slices the stored text
// exactly like the version attachment service (offset/maxChars in characters).
type fakePagedAttachment struct {
	text     string
	err      error
	offset   int
	maxChars int
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
	end := offset + maxChars
	if end > len(runes) {
		end = len(runes)
	}
	text := ""
	if offset < len(runes) {
		text = string(runes[offset:end])
	}
	return AttachmentText{
		AttachmentID: attachmentID,
		Name:         "需求.md",
		MIME:         "text/markdown",
		Status:       "done",
		Offset:       offset,
		Text:         text,
		TotalChars:   len(runes),
		Truncated:    end < len(runes),
	}, nil
}

func (f *fakePagedAttachment) AttachmentImageRef(_ context.Context, _, _, attachmentID int64) (AttachmentImage, error) {
	return AttachmentImage{AttachmentID: attachmentID}, nil
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
