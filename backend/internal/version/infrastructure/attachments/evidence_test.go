package attachments_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteCoverageEvidence writes one coverage JSON per format when
// EZTEST_EVIDENCE_DIR is set. It is skipped in normal runs; the W0 acceptance
// evidence is produced with:
//
//	EZTEST_EVIDENCE_DIR=docs/version-V0.22/acceptance/evidence/w0/task-15 \
//	  go test ./internal/version/infrastructure/attachments -run TestWriteCoverageEvidence -count=1
func TestWriteCoverageEvidence(t *testing.T) {
	directory := os.Getenv("EZTEST_EVIDENCE_DIR")
	if directory == "" {
		t.Skip("EZTEST_EVIDENCE_DIR is not set")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create evidence dir: %v", err)
	}
	cases := []struct {
		name string
		file string
		mime string
		data []byte
	}{
		{"docx", "requirements.docx", "", docxFullyFixture()},
		{"docx-skipped-image", "requirements.docx", "", docxSkippedImageFixture()},
		{"pptx", "deck.pptx", "", pptxFullyFixture()},
		{"xlsx", "matrix.xlsx", "", xlsxFullyFixture()},
		{"html", "requirements.html", "text/html", []byte(htmlFullyFixture)},
		{"pdf", "report.pdf", "application/pdf", pdfFullyFixture()},
		{"pdf-skipped-image", "report.pdf", "application/pdf", pdfSkippedImageFixture()},
	}
	for _, tc := range cases {
		result, err := extractResult(t, tc.file, tc.mime, tc.data)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		payload := map[string]any{
			"format":       tc.name,
			"file":         tc.file,
			"text_chars":   len([]rune(result.Text)),
			"elements":     len(result.Elements),
			"images":       len(result.Images),
			"coverage":     result.Coverage,
			"skipped":      result.Skipped,
			"element_list": result.Elements,
		}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		path := filepath.Join(directory, tc.name+".json")
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("%s: write %s: %v", tc.name, path, err)
		}
	}
}
