package application

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// HashExcerpt returns the design spec §6.3 excerpt hash of the 1-based
// inclusive line range [startLine, endLine] of content:
//
//  1. content is the raw commit-pinned blob (never the working tree); it must
//     be valid UTF-8 and must not be binary;
//  2. lines are split on "\n" and a trailing "\r" is stripped from each line
//     (CRLF -> LF), so a CRLF and an LF checkout hash identically;
//  3. the selected lines are joined with "\n" (no trailing newline) and
//     hashed as "sha256:" + lowercase hex SHA-256.
//
// startLine < 1, endLine < startLine and endLine past the file's last line are
// rejected. The hash is computed server-side only: a hash supplied by the
// model is never trusted.
func HashExcerpt(content []byte, startLine, endLine int) (string, error) {
	lines, err := commitContentLines(content)
	if err != nil {
		return "", err
	}
	if err := domain.ValidateEvidenceRange(startLine, endLine, len(lines), 0); err != nil {
		return "", err
	}
	return hashExcerptLines(lines, startLine, endLine), nil
}

// ValidateEvidence checks design spec §6.3 evidence against commit content and
// returns it with the server-recomputed excerpt hash. All of the following must
// hold, otherwise a domain invalid error is returned:
//
//   - content is valid UTF-8 and not binary;
//   - 1 <= startLine <= endLine <= totalLines of content;
//   - endLine-startLine+1 <= maxLines (EZTEST_CODEINTEL_MAX_EXCERPT_LINES). A
//     non-positive maxLines falls back to the hard domain limit
//     domain.MaxExcerptLines so a caller cannot silently disable the check.
//
// The incoming ExcerptHash is ignored: the server always recomputes it from the
// commit bytes and stores only the verified value. The file's existence in the
// commit tree is the caller's responsibility (it provides the bytes).
func ValidateEvidence(content []byte, ev domain.Evidence, maxLines int) (domain.Evidence, error) {
	if maxLines <= 0 {
		maxLines = domain.MaxExcerptLines
	}

	lines, err := commitContentLines(content)
	if err != nil {
		return domain.Evidence{}, err
	}
	if err := domain.ValidateEvidenceRange(ev.StartLine, ev.EndLine, len(lines), maxLines); err != nil {
		return domain.Evidence{}, err
	}

	ev.ExcerptHash = hashExcerptLines(lines, ev.StartLine, ev.EndLine)
	return ev, nil
}

// commitContentLines splits raw commit content into lines following design spec
// §6.3: split on "\n", strip each line's trailing "\r" and do not count the
// implicit empty line produced by a trailing newline. Non-UTF-8 and binary
// content is rejected because no reliable excerpt can be derived from it.
func commitContentLines(content []byte) ([]string, error) {
	if !utf8.Valid(content) {
		return nil, unsupportedContentError("file content is not valid UTF-8")
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, unsupportedContentError("file content is binary")
	}

	text := string(content)
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" && strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines, nil
}

// hashExcerptLines hashes the 1-based inclusive line range of an already
// normalised line slice.
func hashExcerptLines(lines []string, startLine, endLine int) string {
	excerpt := strings.Join(lines[startLine-1:endLine], "\n")
	sum := sha256.Sum256([]byte(excerpt))
	return domain.ExcerptHashPrefix + hex.EncodeToString(sum[:])
}

// unsupportedContentError reports binary or non-UTF-8 content with the stable
// unsupported_content reason of design spec §7.1. It never embeds the bytes.
func unsupportedContentError(detail string) error {
	return domain.NewInvalid("file", "unsupported_content", "codeintel: "+detail)
}
