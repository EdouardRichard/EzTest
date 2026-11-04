package execution

// Task 112: the failure analyst writes a free-form summary that becomes an
// issue's summary. That text must not assert a severity different from the
// server-derived issue severity (the FR-106-02 trap said "severity 由 minor
// 上调为 major" while the issue record was minor). The summary is evidence
// narrative, not a second classification source; a deterministic normalizer
// drops severity statements while preserving the observed behavior evidence.

import (
	"regexp"
	"strings"
	"unicode"
)

// severityClaim matches a free-text severity statement: a change of severity
// ("上调/升级/upgraded"), or an explicit severity assignment ("severity: major").
// It is intentionally narrow so normal narrative that merely mentions the word
// "severity" (for example "severity 字段由服务端派生") is preserved.
var severityClaim = regexp.MustCompile(`(?i)(?:severity|严重度|严重级|严重性)[^\n。！？!?;；]{0,80}?` +
	`(?:上调|下调|提升|降低|升级|降级|调整|改为|改成|修改为|` +
	`upgrad|downgrad|rais|lower|chang|adjust|` +
	`[:：=为][[:space:]]*(?:major|minor|trivial|blocker|info))`)

// sentenceTerminators end one narrative sentence; the terminator is preserved
// with the sentence so joining the kept sentences keeps the original prose. The
// ASCII dot is handled separately: it terminates only before whitespace/end so
// decimals and URLs stay intact.
const sentenceTerminators = "。！？!?;；\n"

// SanitizeFailureSummary removes sentences that would contradict the canonical
// issue severity, leaving every other sentence untouched. A summary that was
// only a severity claim normalizes to an empty string so the caller can fall
// back to the recorded step/assertion evidence.
func SanitizeFailureSummary(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return ""
	}
	var kept strings.Builder
	var sentence strings.Builder
	flush := func() {
		if sentence.Len() == 0 {
			return
		}
		if !severityClaim.MatchString(sentence.String()) {
			kept.WriteString(sentence.String())
		}
		sentence.Reset()
	}
	runes := []rune(summary)
	for index, character := range runes {
		sentence.WriteRune(character)
		switch {
		case character == '.':
			if index+1 == len(runes) || unicode.IsSpace(runes[index+1]) {
				flush()
			}
		case strings.ContainsRune(sentenceTerminators, character):
			flush()
		}
	}
	flush()
	return strings.TrimSpace(kept.String())
}
