package application

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// TestHashExcerptCRLFIsNormalised pins design spec §6.3: lines are split on
// "\n", a trailing "\r" is stripped (CRLF -> LF) and the excerpt is joined with
// "\n" and hashed as "sha256:<lowercase hex>".
func TestHashExcerptCRLFIsNormalised(t *testing.T) {
	h1, err := HashExcerpt([]byte("a\r\nb\r\nc\n"), 2, 3)
	if err != nil {
		t.Fatalf("HashExcerpt(CRLF) error = %v", err)
	}
	h2, err := HashExcerpt([]byte("a\nb\nc\n"), 2, 3)
	if err != nil {
		t.Fatalf("HashExcerpt(LF) error = %v", err)
	}
	if h1 != h2 {
		t.Fatalf("CRLF hash %q != LF hash %q", h1, h2)
	}
	if !strings.HasPrefix(h1, domain.ExcerptHashPrefix) {
		t.Fatalf("hash %q is missing the %q prefix", h1, domain.ExcerptHashPrefix)
	}
	sum := sha256.Sum256([]byte("b\nc"))
	want := domain.ExcerptHashPrefix + hex.EncodeToString(sum[:])
	if h1 != want {
		t.Fatalf("hash = %q, want %q", h1, want)
	}
}

// TestHashExcerptRejectsInvalidRanges covers the range rules of design spec
// §6.3 step 2. The content has three lines; the trailing newline does not count
// as a fourth line.
func TestHashExcerptRejectsInvalidRanges(t *testing.T) {
	content := []byte("a\nb\nc\n")
	tests := []struct {
		name       string
		start, end int
	}{
		{"zero start", 0, 1},
		{"negative start", -1, 2},
		{"end before start", 3, 2},
		{"end past file", 2, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := HashExcerpt(content, tt.start, tt.end); err == nil {
				t.Fatalf("HashExcerpt(%d, %d) error = nil, want error", tt.start, tt.end)
			}
		})
	}
}

// TestHashExcerptRejectsOutlineContent proves non-UTF-8 and binary content can
// never carry a verifiable excerpt (design spec §6.3 step 1).
func TestHashExcerptRejectsOutlineContent(t *testing.T) {
	if _, err := HashExcerpt([]byte{0xff, 0xfe, 0xfd}, 1, 1); err == nil {
		t.Fatal("HashExcerpt(invalid UTF-8) error = nil, want error")
	}
	if _, err := HashExcerpt([]byte("a\x00b"), 1, 1); err == nil {
		t.Fatal("HashExcerpt(binary) error = nil, want error")
	}
}

// TestValidateEvidenceAddsHash proves Server-side hash recomputation: the
// returned evidence carries the excerpt hash of the referenced lines.
func TestValidateEvidenceAddsHash(t *testing.T) {
	content := []byte("one\ntwo\nthree\n")
	ev := domain.Evidence{File: "src/a.ts", StartLine: 2, EndLine: 3}

	got, err := ValidateEvidence(content, ev, 10)
	if err != nil {
		t.Fatalf("ValidateEvidence() error = %v", err)
	}
	if got.File != ev.File || got.StartLine != 2 || got.EndLine != 3 {
		t.Fatalf("ValidateEvidence() = %+v, want file/range preserved", got)
	}
	wantHash, err := HashExcerpt(content, 2, 3)
	if err != nil {
		t.Fatalf("HashExcerpt() error = %v", err)
	}
	if got.ExcerptHash != wantHash {
		t.Fatalf("ExcerptHash = %q, want %q", got.ExcerptHash, wantHash)
	}
}

// TestValidateEvidenceRejectsOutOfRangeAndOversized covers design spec §6.3
// step 2: 1 <= startLine <= endLine <= totalLines and span <= maxLines.
func TestValidateEvidenceRejectsOutOfRangeAndOversized(t *testing.T) {
	content := []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n")
	tests := []struct {
		name       string
		start, end int
		maxLines   int
	}{
		{"start line zero", 0, 1, 10},
		{"negative start line", -1, 2, 10},
		{"end before start", 4, 3, 10},
		{"end past total lines", 9, 11, 10},
		{"span over maxLines", 1, 6, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := domain.Evidence{File: "src/a.ts", StartLine: tt.start, EndLine: tt.end}
			if _, err := ValidateEvidence(content, ev, tt.maxLines); err == nil {
				t.Fatalf("ValidateEvidence(%d..%d, max=%d) error = nil, want error", tt.start, tt.end, tt.maxLines)
			}
		})
	}
}

// TestValidateEvidenceRejectsTrailingNewlineAsLine proves the implicit extra
// line produced by a trailing "\n" is not addressable.
func TestValidateEvidenceRejectsTrailingNewlineAsLine(t *testing.T) {
	ev := domain.Evidence{File: "a.ts", StartLine: 4, EndLine: 4}
	if _, err := ValidateEvidence([]byte("a\nb\nc\n"), ev, 10); err == nil {
		t.Fatal("ValidateEvidence(line 4 of 3) error = nil, want error")
	}
}

// TestValidateEvidenceRejectsBinaryContent proves binary/non-UTF-8 content
// cannot be validated (design spec §6.3 step 1 and §7.1 unsupported_content).
func TestValidateEvidenceRejectsBinaryContent(t *testing.T) {
	ev := domain.Evidence{File: "a.ts", StartLine: 1, EndLine: 1}

	_, err := ValidateEvidence([]byte{0xff, 0xfe}, ev, 10)
	if err == nil {
		t.Fatal("ValidateEvidence(invalid UTF-8) error = nil, want error")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("errors.Is(err, domain.ErrInvalid) = false (err = %v)", err)
	}

	_, err = ValidateEvidence([]byte("a\x00b\nc"), ev, 10)
	if err == nil {
		t.Fatal("ValidateEvidence(binary) error = nil, want error")
	}
}

// TestValidateEvidenceDefaultsSpanLimit proves a non-positive maxLines falls
// back to the hard domain limit instead of disabling the span check.
func TestValidateEvidenceDefaultsSpanLimit(t *testing.T) {
	lines := make([]string, 0, domain.MaxExcerptLines+1)
	for i := 0; i < domain.MaxExcerptLines+1; i++ {
		lines = append(lines, "line")
	}
	content := []byte(strings.Join(lines, "\n"))

	ev := domain.Evidence{File: "a.ts", StartLine: 1, EndLine: domain.MaxExcerptLines + 1}
	if _, err := ValidateEvidence(content, ev, 0); err == nil {
		t.Fatal("ValidateEvidence(span over hard limit) error = nil, want error")
	}
}

// TestValidateEvidenceRejectsEmptyFile pins the zero-byte rule (review finding
// 3): empty content has zero lines, so no evidence range can be verified
// against it and a fabricated 1..1 fact must not validate.
func TestValidateEvidenceRejectsEmptyFile(t *testing.T) {
	ev := domain.Evidence{File: "src/empty.ts", StartLine: 1, EndLine: 1}
	for _, content := range [][]byte{nil, {}} {
		if _, err := HashExcerpt(content, 1, 1); err == nil {
			t.Fatalf("HashExcerpt(empty content) error = nil, want rejection")
		} else if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("HashExcerpt(empty content) error = %v, want ErrInvalid", err)
		}
		if _, err := ValidateEvidence(content, ev, 10); err == nil {
			t.Fatalf("ValidateEvidence(empty content) error = nil, want rejection")
		} else if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("ValidateEvidence(empty content) error = %v, want ErrInvalid", err)
		}
	}
}
