package version_test

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestSanitizeAttachmentName(t *testing.T) {
	cases := map[string]string{
		"spec.md":               "spec.md",
		"  spec.md  ":           "spec.md",
		"../../etc/passwd":      "passwd",
		"..\\..\\windows\\a.md": "a.md",
		"C:/tmp/report.docx":    "report.docx",
		"a\x00b.md":             "ab.md",
		"tab\tname.txt":         "tabname.txt",
	}
	for input, want := range cases {
		got, err := version.SanitizeAttachmentName(input)
		if err != nil {
			t.Fatalf("SanitizeAttachmentName(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("SanitizeAttachmentName(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "   ", ".", "..", "a/..", "../../"} {
		if _, err := version.SanitizeAttachmentName(input); err == nil {
			t.Fatalf("SanitizeAttachmentName(%q) accepted an empty name", input)
		}
	}
	longName := ""
	for i := 0; i < 300; i++ {
		longName += "a"
	}
	if _, err := version.SanitizeAttachmentName(longName); err == nil {
		t.Fatal("oversized attachment name accepted")
	}
}

func TestDetectAttachmentKind(t *testing.T) {
	cases := []struct {
		name string
		mime string
		kind version.AttachmentKind
	}{
		{"notes.md", "text/markdown", version.AttachmentMarkdown},
		{"notes.markdown", "text/markdown", version.AttachmentMarkdown},
		{"notes.txt", "text/plain", version.AttachmentText},
		{"requirements.docx", "application/octet-stream", version.AttachmentDOCX},
		{"slides.pptx", "application/octet-stream", version.AttachmentPPTX},
		{"report.pdf", "application/pdf", version.AttachmentPDF},
		{"screenshot.png", "image/png", version.AttachmentImage},
		{"photo.JPG", "image/jpeg", version.AttachmentImage},
		{"photo.webp", "image/webp", version.AttachmentImage},
		{"legacy.doc", "application/msword", version.AttachmentUnsupported},
		{"legacy.ppt", "application/vnd.ms-powerpoint", version.AttachmentUnsupported},
		{"archive.zip", "application/zip", version.AttachmentUnsupported},
		{"no-extension", "text/plain", version.AttachmentText},
		{"no-extension", "image/png", version.AttachmentImage},
		{"no-extension", "application/pdf", version.AttachmentPDF},
		{"no-extension", "application/octet-stream", version.AttachmentUnsupported},
	}
	for _, tc := range cases {
		if got := version.DetectAttachmentKind(tc.name, tc.mime); got != tc.kind {
			t.Fatalf("DetectAttachmentKind(%q, %q) = %q, want %q", tc.name, tc.mime, got, tc.kind)
		}
	}
}

func TestAttachmentKindSupportsExtraction(t *testing.T) {
	for _, kind := range []version.AttachmentKind{
		version.AttachmentMarkdown, version.AttachmentText, version.AttachmentDOCX,
		version.AttachmentPPTX, version.AttachmentPDF,
	} {
		if !kind.SupportsExtraction() {
			t.Fatalf("%q should support extraction", kind)
		}
	}
	for _, kind := range []version.AttachmentKind{
		version.AttachmentImage, version.AttachmentUnsupported,
	} {
		if kind.SupportsExtraction() {
			t.Fatalf("%q should not support extraction", kind)
		}
	}
	if !version.AttachmentImage.IsImage() || version.AttachmentDOCX.IsImage() {
		t.Fatal("IsImage misclassified a kind")
	}
}
