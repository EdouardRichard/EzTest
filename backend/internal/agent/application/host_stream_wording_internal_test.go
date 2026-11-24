package agentapp

// H-E17 (D 交接 §4.1i #19): sealHostStreamAtTerminal's near-unreachable fallback
// (a terminal interrupted seal with no recorded hostStreamCause) used to append
// the RAW taxonomy reason code to the user-facing message ("回复未完成…（model_error）").
// The conversation thread is user-facing; a development taxonomy label is runtime
// pollution (P31). The fallback wording must stay in the user's register and never
// leak a raw reason code. Expectations come from the P31 runtime-hygiene contract,
// not from observed output.

import (
	"strings"
	"testing"
)

// TestStreamUnfinishedFallbackMessageIsRuntimeClean pins that the fallback
// explanation never contains a raw taxonomy reason code, for every terminal
// reason the funnel can pass. Reverting the fallback to append "（"+reason+"）"
// makes this fail (the mutation this test guards).
func TestStreamUnfinishedFallbackMessageIsRuntimeClean(t *testing.T) {
	rawCodes := []string{
		"model_error", "tool_failure", "budget",
		"context_overflow_unrecoverable", "aborted", "interrupted",
	}
	reasons := append(append([]string(nil), rawCodes...), "", "some_future_reason")
	for _, reason := range reasons {
		message := streamUnfinishedFallbackMessage(reason)
		if strings.TrimSpace(message) == "" {
			t.Fatalf("fallback message for reason %q is empty", reason)
		}
		for _, code := range rawCodes {
			if strings.Contains(message, code) {
				t.Fatalf("fallback message for reason %q leaks raw taxonomy code %q: %q", reason, code, message)
			}
		}
	}
	// The two context reasons reuse their specific user-facing sentences (single
	// source with hostStreamFailureMessage), not the generic unfinished text.
	if got := streamUnfinishedFallbackMessage("aborted"); got != streamAbortedMessage {
		t.Fatalf("aborted fallback = %q, want streamAbortedMessage %q", got, streamAbortedMessage)
	}
	if got := streamUnfinishedFallbackMessage("interrupted"); got != streamDeadlineMessage {
		t.Fatalf("interrupted fallback = %q, want streamDeadlineMessage %q", got, streamDeadlineMessage)
	}
}

// TestSalvageCoversContentKeepsRealErrorCards pins H-E16 review Minor 1: the
// salvage coverage comparison strips only META error parts (the interruption
// explanation and the trace-truncation signal) by code, NOT every error part by
// type. A real content error card (e.g. a tool_failure result) that was refused and
// never stored must stay in the comparison and be reported as a loss — stripping it
// would mask the loss under a "内容已完整保存" claim. Reverting to strip-by-Type
// makes the first leg fail (the mutation this test guards).
func TestSalvageCoversContentKeepsRealErrorCards(t *testing.T) {
	text := ConversationPart{Type: "text", Text: "回复"}
	realErr := ConversationPart{Type: "error", Data: map[string]any{"code": "tool_failure", "message": "工具失败"}}
	metaErr := ConversationPart{Type: "error", Data: map[string]any{"code": streamInterruptedCode}}
	truncErr := ConversationPart{Type: "error", Data: map[string]any{"code": streamPartsTruncatedCode}}

	// A trailing real error card missing from stored is a LOSS (must not be stripped).
	if salvageCoversContent([]ConversationPart{text}, []ConversationPart{text, realErr, metaErr}) {
		t.Fatal("a trailing real error card missing from stored was masked as no-loss")
	}
	// Meta-only trailing errors are excluded: stored text covers the content → no loss.
	if !salvageCoversContent([]ConversationPart{text}, []ConversationPart{text, metaErr}) {
		t.Fatal("the interruption meta error should be excluded from coverage")
	}
	if !salvageCoversContent([]ConversationPart{text}, []ConversationPart{text, truncErr, metaErr}) {
		t.Fatal("truncation + interruption meta errors should be excluded from coverage")
	}
	// Stored covering the real error card too → no loss.
	if !salvageCoversContent([]ConversationPart{text, realErr}, []ConversationPart{text, realErr, metaErr}) {
		t.Fatal("stored covering the real error card should be no-loss")
	}
	// A non-error trailing part is never stripped: missing it is a loss.
	if salvageCoversContent([]ConversationPart{text}, []ConversationPart{text, {Type: "text", Text: "另一段"}}) {
		t.Fatal("a missing trailing text part was masked as no-loss")
	}
}
