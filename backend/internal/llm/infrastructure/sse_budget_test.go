package llminfra

// v0.2.4-D fix wave (I-2): the streamed response body must not inherit the
// non-streaming 4 MiB cap, and a stream cut by the CLIENT at its own budget must
// be attributed to that local limit — never to the provider.
//
// Expectations come from the contracts, not from observed output:
//   - llmapp.MaxRequestOutputTokens is the request-side output bound (the longest
//     legitimate turn the client can ask for);
//   - OpenAI chat-completions SSE frames every fragment in a full
//     chat.completion.chunk envelope, so the wire size is the fragment count
//     times the envelope, not the decoded reply length;
//   - the error taxonomy (llmapp.LLMKindBudget vs LLMKindTransient) decides who
//     the failure is blamed on.

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// maxTurnFragment is one realistic chat.completions chunk carrying a single
// output token. The envelope fields are the ones a real OpenAI-compatible
// gateway sends (id/object/created/model/system_fingerprint/service_tier plus
// the choices array); reasoning_content is present because providers that
// interleave reasoning stream it on the same chunk shape.
func maxTurnFragment(index int) string {
	return fmt.Sprintf(
		`data: {"id":"chatcmpl-9pQxK2mZr7Yb4Tn1Vc0Ld8Hs","object":"chat.completion.chunk",`+
			`"created":1763376000,"model":"qwen3-coder-plus-2026-07-11","system_fingerprint":"fp_8f21c4b9d0",`+
			`"service_tier":"default","choices":[{"index":0,"delta":{"role":"assistant","content":"字",`+
			`"reasoning_content":""},"finish_reason":null,"logprobs":null}],"fragment":%d}`+"\n\n",
		index)
}

const maxTurnTerminalFragment = `data: {"id":"chatcmpl-9pQxK2mZr7Yb4Tn1Vc0Ld8Hs","object":"chat.completion.chunk",` +
	`"created":1763376000,"model":"qwen3-coder-plus-2026-07-11","choices":[{"index":0,"delta":{},` +
	`"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":16000,"total_tokens":16011}}` +
	"\n\ndata: [DONE]\n\n"

// TestStreamBudgetIsUnreachableForAMaxLengthTurn drives a full-length legitimate
// turn (one fragment per allowed output token) through the real chat-completions
// stream consumer. It must complete, and its wire size must exceed the
// NON-streaming cap — that inequality is the regression this fix closes: the old
// shared cap cut this turn mid-reply and reported a provider truncation.
func TestStreamBudgetIsUnreachableForAMaxLengthTurn(t *testing.T) {
	fragment := maxTurnFragment(0)
	turnBytes := int64(len(fragment))*llmapp.MaxRequestOutputTokens + int64(len(maxTurnTerminalFragment))
	if turnBytes <= maxResponseBytes {
		t.Fatalf("a max-length turn is %d bytes, which no longer exceeds the non-streaming cap %d; "+
			"the fixture must keep modelling the framed wire size that made the shared cap reachable",
			turnBytes, maxResponseBytes)
	}
	if turnBytes >= maxStreamResponseBytes {
		t.Fatalf("streaming budget %d does not admit a legitimate max-length turn of %d bytes",
			maxStreamResponseBytes, turnBytes)
	}

	var body strings.Builder
	for index := int64(0); index < llmapp.MaxRequestOutputTokens; index++ {
		body.WriteString(maxTurnFragment(int(index)))
	}
	body.WriteString(maxTurnTerminalFragment)

	var text, finishReason string
	var usage map[string]int
	err := consumeChatStream(strings.NewReader(body.String()), nil, func(delta llmapp.Delta) {
		text += delta.Content
		if delta.FinishReason != "" {
			finishReason = delta.FinishReason
		}
		if delta.Usage != nil {
			usage = delta.Usage
		}
	}, int(llmapp.MaxRequestOutputTokens))
	if err != nil {
		t.Fatalf("a legitimate max-length turn failed: %v", err)
	}
	if got := int64(len([]rune(text))); got != llmapp.MaxRequestOutputTokens {
		t.Fatalf("decoded %d tokens, want the whole %d-token reply", got, llmapp.MaxRequestOutputTokens)
	}
	if finishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", finishReason)
	}
	if usage["completion_tokens"] != int(llmapp.MaxRequestOutputTokens) {
		t.Fatalf("usage = %#v, want the provider snapshot of the full turn", usage)
	}
}

// endlessChunks is a provider that never stops streaming, so the scanner's own
// budget is the only thing that can end the read.
type endlessChunks struct {
	chunk string
	left  int
}

func (r *endlessChunks) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if r.left == 0 {
			r.left = len(r.chunk)
		}
		copied := copy(p[n:], r.chunk[len(r.chunk)-r.left:])
		r.left -= copied
		n += copied
	}
	return n, nil
}

var _ io.Reader = (*endlessChunks)(nil)

// TestStreamOverBudgetIsALocalCutNotAProviderTruncation pins the attribution:
// exceeding the streaming budget is a *StreamBudgetError classified as a local
// budget, not the transient "provider stream ended before completion" the old
// shared LimitReader produced.
func TestStreamOverBudgetIsALocalCutNotAProviderTruncation(t *testing.T) {
	body := &endlessChunks{chunk: maxTurnFragment(0)}
	err := consumeSSE(body, func(string) (bool, error) { return false, nil }, "read llm chat stream failed", int(llmapp.MaxRequestOutputTokens))
	if err == nil {
		t.Fatal("an over-budget stream returned success (silent truncation)")
	}
	var budgetErr *StreamBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error = %v (%T), want *StreamBudgetError", err, err)
	}
	if budgetErr.Limit != maxStreamResponseBytes {
		t.Fatalf("reported limit = %d, want the streaming budget %d", budgetErr.Limit, maxStreamResponseBytes)
	}
	if budgetErr.Read < maxStreamResponseBytes {
		t.Fatalf("reported read = %d, want at least the budget %d", budgetErr.Read, maxStreamResponseBytes)
	}
	if budgetErr.LLMErrorKind() != llmapp.LLMKindBudget {
		t.Fatalf("kind = %q, want the local budget class", budgetErr.LLMErrorKind())
	}
	if budgetErr.HTTPStatusCode() != 0 {
		t.Fatalf("status = %d, want 0 (the failure never came from the wire)", budgetErr.HTTPStatusCode())
	}
	if strings.Contains(err.Error(), "provider stream ended before completion") {
		t.Fatalf("error blames the provider for a local cut: %q", err)
	}
	if !strings.Contains(err.Error(), "local client response budget") {
		t.Fatalf("error does not name the local budget: %q", err)
	}
	if errors.Is(err, llmapp.ErrTransient) {
		t.Fatal("a local budget cut is not an upstream transient outage")
	}
	var transient *TransientError
	if errors.As(err, &transient) {
		t.Fatal("a local budget cut must not be a TransientError")
	}
}

// TestStreamBudgetExceedsTheNonStreamingCap keeps the two budgets distinct: a
// future edit that re-points the scanner at maxResponseBytes fails here even if
// no long-turn fixture is run.
func TestStreamBudgetExceedsTheNonStreamingCap(t *testing.T) {
	if maxStreamResponseBytes <= maxResponseBytes {
		t.Fatalf("streaming budget %d must exceed the non-streaming cap %d (SSE framing overhead)",
			maxStreamResponseBytes, maxResponseBytes)
	}
	want := int64(llmapp.MaxRequestOutputTokens) * streamFragmentEnvelopeBytes * streamBudgetHeadroom
	if maxStreamResponseBytes != want {
		t.Fatalf("streaming budget = %d, want the contract-derived %d", maxStreamResponseBytes, want)
	}
}

// countedTurnReader generates a legitimate max-length turn for a configured
// output limit: one framed fragment per output token, then the terminal chunk,
// then EOF. It streams the fragments instead of materializing the whole body so
// a 110k-token turn (tens of MB of framed wire bytes) stays cheap to drive.
type countedTurnReader struct {
	chunk     string
	terminal  string
	remaining int
	chunkOff  int
	termOff   int
}

func (r *countedTurnReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		switch {
		case r.remaining > 0:
			copied := copy(p[n:], r.chunk[r.chunkOff:])
			r.chunkOff += copied
			n += copied
			if r.chunkOff == len(r.chunk) {
				r.chunkOff = 0
				r.remaining--
			}
		case r.termOff < len(r.terminal):
			copied := copy(p[n:], r.terminal[r.termOff:])
			r.termOff += copied
			n += copied
		default:
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		}
	}
	return n, nil
}

var _ io.Reader = (*countedTurnReader)(nil)

// TestStreamBudgetScalesWithConfiguredMaxOutput pins H-E12 (SC-H8 zero capability
// reduction): the runner forwards a model's configured max_output verbatim (it is
// never clamped to MaxRequestOutputTokens — clamping would starve a long turn,
// constitution II.7), so the streaming byte budget must SCALE with that configured
// limit. A model configured for 110k output tokens produces a legitimate turn whose
// framed wire size exceeds the old fixed 16000-based budget; that turn must complete,
// not be cut by the local reader and misattributed as a provider truncation.
//
// Expectations come from the contract: the budget is tokens x per-fragment envelope
// x headroom, derived from the request's output limit, with MaxRequestOutputTokens
// as the floor for an unknown/default limit. Reverting the derivation to the fixed
// 16000-based budget makes this leg fail (the mutation this test guards).
func TestStreamBudgetScalesWithConfiguredMaxOutput(t *testing.T) {
	const configuredMaxOutput = 110_000
	fragment := maxTurnFragment(0)
	terminal := `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":110000,"total_tokens":110011}}` +
		"\n\ndata: [DONE]\n\n"
	turnBytes := int64(len(fragment))*configuredMaxOutput + int64(len(terminal))
	// The fixture must keep modelling a turn that the OLD fixed budget cut: its
	// framed size exceeds the default (16000-based) budget...
	if turnBytes <= maxStreamResponseBytes {
		t.Fatalf("a %d-token turn is %d bytes, which no longer exceeds the default budget %d; "+
			"the fixture must keep modelling the long turn the fixed budget starved",
			configuredMaxOutput, turnBytes, maxStreamResponseBytes)
	}
	// ...and fits the budget derived from the configured limit.
	wantBudget := int64(configuredMaxOutput) * streamFragmentEnvelopeBytes * streamBudgetHeadroom
	if turnBytes >= wantBudget {
		t.Fatalf("configured budget %d does not admit a legitimate %d-token turn of %d bytes",
			wantBudget, configuredMaxOutput, turnBytes)
	}

	body := &countedTurnReader{chunk: fragment, terminal: terminal, remaining: configuredMaxOutput}
	var tokens int
	var finishReason string
	var usage map[string]int
	err := consumeChatStream(body, nil, func(delta llmapp.Delta) {
		tokens += len([]rune(delta.Content))
		if delta.FinishReason != "" {
			finishReason = delta.FinishReason
		}
		if delta.Usage != nil {
			usage = delta.Usage
		}
	}, configuredMaxOutput)
	if err != nil {
		t.Fatalf("a legitimate %d-token turn failed (budget starved the configured output): %v",
			configuredMaxOutput, err)
	}
	if int64(tokens) != configuredMaxOutput {
		t.Fatalf("decoded %d tokens, want the whole %d-token reply", tokens, configuredMaxOutput)
	}
	if finishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", finishReason)
	}
	if usage["completion_tokens"] != configuredMaxOutput {
		t.Fatalf("usage = %#v, want the provider snapshot of the full turn", usage)
	}
}

// TestStreamBudgetDerivation pins the budget function itself: it scales with a
// configured limit above the request-side default and floors at the historical
// default budget for an unknown or smaller limit (so no existing default-limit
// stream shrinks below the pre-fix budget — zero capability reduction).
func TestStreamBudgetDerivation(t *testing.T) {
	cases := []struct {
		name            string
		maxOutputTokens int
		want            int64
	}{
		{name: "unknown limit floors at the default", maxOutputTokens: 0, want: maxStreamResponseBytes},
		{name: "negative limit floors at the default", maxOutputTokens: -1, want: maxStreamResponseBytes},
		{name: "small limit floors at the default", maxOutputTokens: 4096, want: maxStreamResponseBytes},
		{name: "default limit is the default budget", maxOutputTokens: int(llmapp.MaxRequestOutputTokens), want: maxStreamResponseBytes},
		{name: "configured long turn scales up", maxOutputTokens: 110_000, want: int64(110_000) * streamFragmentEnvelopeBytes * streamBudgetHeadroom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamResponseBudget(tc.maxOutputTokens); got != tc.want {
				t.Fatalf("streamResponseBudget(%d) = %d, want %d", tc.maxOutputTokens, got, tc.want)
			}
		})
	}
}

// responsesTurnFragment is one realistic Responses output_text.delta event carrying
// a single output token. The envelope fields (item_id/output_index/content_index/
// sequence_number/logprobs) are the ones a real OpenAI-compatible gateway sends; it
// is sized so a 110k-token turn's framed wire size exceeds the default budget.
const responsesTurnFragment = "event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","item_id":"msg_9pQxK2mZr7Yb4Tn1Vc0Ld8HsABCDEFGHJKLMNPQRSTWXYZ0123456789abcdefghjkmnpqrstwxyzABCDEFGHJKLMNPQRSTWXYZ0123456789","output_index":0,"content_index":0,"delta":"字","sequence_number":18446744073709551615,"logprobs":[]}` + "\n\n"

const responsesTurnTerminal = "event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":11,"output_tokens":110000,"total_tokens":110011}}}` + "\n\n"

// TestStreamBudgetScalesForResponsesProtocol pins H-E12 review Minor 4: the
// Responses stream consumer shares consumeSSE and must scale its byte budget with
// the forwarded output limit exactly like chat-completions. An endless Responses
// stream cut at the local budget must report the SCALED limit (not the default),
// so reverting the derivation to the fixed 16000-based budget fails here.
func TestStreamBudgetScalesForResponsesProtocol(t *testing.T) {
	const configuredMaxOutput = 110_000
	want := streamResponseBudget(configuredMaxOutput)
	if want <= maxStreamResponseBytes {
		t.Fatalf("scaled budget %d does not exceed the default %d; the fixture cannot discriminate the mutation",
			want, maxStreamResponseBytes)
	}
	body := &endlessChunks{chunk: responsesTurnFragment}
	err := consumeResponsesStream(body, nil, func(llmapp.Delta) {}, configuredMaxOutput)
	var budgetErr *StreamBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error = %v (%T), want *StreamBudgetError", err, err)
	}
	if budgetErr.Limit != want {
		t.Fatalf("responses budget limit = %d, want the scaled %d (not the default %d)",
			budgetErr.Limit, want, maxStreamResponseBytes)
	}
}

// TestResponsesLongTurnCompletes is the Responses counterpart of the chat long-turn
// leg: a legitimate configured long turn whose framed wire size exceeds the default
// budget completes through the real Responses consumer (not starved). Reverting the
// budget derivation to the fixed default makes this fail.
func TestResponsesLongTurnCompletes(t *testing.T) {
	const configuredMaxOutput = 110_000
	turnBytes := int64(len(responsesTurnFragment))*configuredMaxOutput + int64(len(responsesTurnTerminal))
	if turnBytes <= maxStreamResponseBytes {
		t.Fatalf("a %d-token Responses turn is %d bytes, which no longer exceeds the default budget %d; "+
			"enlarge responsesTurnFragment so the fixture keeps modelling the starved long turn",
			configuredMaxOutput, turnBytes, maxStreamResponseBytes)
	}
	if turnBytes >= streamResponseBudget(configuredMaxOutput) {
		t.Fatalf("configured budget does not admit a legitimate %d-token Responses turn of %d bytes",
			configuredMaxOutput, turnBytes)
	}
	body := &countedTurnReader{chunk: responsesTurnFragment, terminal: responsesTurnTerminal, remaining: configuredMaxOutput}
	var tokens int
	var finishReason string
	err := consumeResponsesStream(body, nil, func(delta llmapp.Delta) {
		tokens += len([]rune(delta.Content))
		if delta.FinishReason != "" {
			finishReason = delta.FinishReason
		}
	}, configuredMaxOutput)
	if err != nil {
		t.Fatalf("a legitimate %d-token Responses turn failed (budget starved the configured output): %v",
			configuredMaxOutput, err)
	}
	if int64(tokens) != configuredMaxOutput {
		t.Fatalf("decoded %d tokens, want the whole %d-token reply", tokens, configuredMaxOutput)
	}
	if finishReason == "" {
		t.Fatal("no terminal finish reason was emitted")
	}
}
