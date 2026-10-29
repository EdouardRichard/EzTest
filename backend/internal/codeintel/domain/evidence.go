package domain

import "fmt"

// ExcerptHashPrefix prefixes every verified evidence excerpt hash (design spec
// §6.3: "sha256:" + lowercase hex SHA-256).
const ExcerptHashPrefix = "sha256:"

// Evidence locates a fact inside a commit. AI-produced evidence carries the
// verified excerpt hash; anchor-derived evidence carries only File.
type Evidence struct {
	File        string `json:"file"`
	StartLine   int    `json:"startLine,omitempty"`
	EndLine     int    `json:"endLine,omitempty"`
	ExcerptHash string `json:"excerptHash,omitempty"`
}

// ValidateEvidenceRange applies the line-range rules of design spec §6.3 step
// 2: 1 ≤ startLine ≤ endLine, optionally bounded by the file's totalLines and
// by maxLines (the effective EZTEST_CODEINTEL_MAX_EXCERPT_LINES value). A
// non-positive totalLines or maxLines disables that individual check.
func ValidateEvidenceRange(startLine, endLine, totalLines, maxLines int) error {
	if startLine < 1 {
		return NewInvalid("evidence", "invalid_evidence",
			fmt.Sprintf("codeintel: evidence startLine %d must be >= 1", startLine))
	}
	if endLine < startLine {
		return NewInvalid("evidence", "invalid_evidence",
			fmt.Sprintf("codeintel: evidence endLine %d must be >= startLine %d", endLine, startLine))
	}
	if maxLines > 0 && endLine-startLine+1 > maxLines {
		return NewInvalid("evidence", "invalid_evidence",
			fmt.Sprintf("codeintel: evidence spans %d lines, limit is %d", endLine-startLine+1, maxLines))
	}
	if totalLines > 0 && endLine > totalLines {
		return NewInvalid("evidence", "invalid_evidence",
			fmt.Sprintf("codeintel: evidence endLine %d exceeds file length %d", endLine, totalLines))
	}
	return nil
}
