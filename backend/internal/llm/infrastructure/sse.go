package llminfra

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// Streaming byte budget (v0.2.4-D fix wave, I-2; H-E12 scales it with the
// configured output limit). The non-streaming cap (maxResponseBytes, 4 MiB of
// decoded JSON) is NOT a valid bound for a streamed reply: SSE frames every
// fragment in a full chat.completion.chunk / response.* envelope, so the wire
// size is tens of times the decoded reply. A legitimate max-length turn (one
// envelope per fragment) already reaches several MB of framed bytes, so
// inheriting the non-streaming cap cut valid replies mid-turn and blamed the
// provider for a local reader limit (constitution VII.7 attribution).
//
// The budget is DERIVED from the request-side output contract, never guessed:
// tokens x per-fragment envelope allowance x headroom for providers that emit
// more than one fragment per token. It is unreachable for a legitimate turn.
const (
	// streamFragmentEnvelopeBytes is the generous per-fragment wire allowance
	// (id/object/created/model/system_fingerprint/choices + delta payload).
	streamFragmentEnvelopeBytes = 512
	// streamBudgetHeadroom covers finer-than-one-fragment-per-token chunking.
	streamBudgetHeadroom = 4
	// maxStreamResponseBytes is the DEFAULT streaming budget, derived from the
	// request-side default output bound (llmapp.MaxRequestOutputTokens). It is the
	// floor streamResponseBudget applies when the request's output limit is
	// unknown or below that default; a configured larger limit scales the budget
	// up (H-E12). It is NOT a fixed ceiling: the runner forwards a model's
	// configured max_output verbatim (never clamped — constitution II.7 / SC-H8),
	// so a long turn above the default must not be starved by this floor.
	maxStreamResponseBytes = int64(llmapp.MaxRequestOutputTokens) * streamFragmentEnvelopeBytes * streamBudgetHeadroom
)

// streamResponseBudget derives the byte budget for one streamed response from the
// request's output-token limit (H-E12). The runner forwards the model's configured
// max_output verbatim (llm/application.prepareChat only defaults a non-positive
// limit; it never clamps a positive one), so a model configured for, say, 110k
// output tokens can legitimately stream a turn whose framed wire size far exceeds
// the default 16000-based budget. Scaling the budget with the forwarded limit keeps
// such a turn reachable instead of cutting it locally and misattributing the cut to
// the provider (constitution VII.7). The floor keeps the historical default budget
// for an unknown or smaller limit, so no default-limit stream shrinks below the
// pre-fix budget (zero capability reduction, SC-H8).
func streamResponseBudget(maxOutputTokens int) int64 {
	tokens := int64(maxOutputTokens)
	if tokens < llmapp.MaxRequestOutputTokens {
		tokens = llmapp.MaxRequestOutputTokens
	}
	return tokens * streamFragmentEnvelopeBytes * streamBudgetHeadroom
}

// StreamBudgetError marks a stream the CLIENT stopped reading at its own byte
// budget — not a provider truncation and not an upstream outage. It carries no
// HTTP status (the failure never came from the wire) and classifies as
// llmapp.LLMKindBudget so the failure is attributable to the local limit
// (constitution VII.7) instead of being reported as "the provider ended the
// stream before completion".
type StreamBudgetError struct {
	Limit int64
	Read  int64
}

var _ llmapp.ClassifiedError = (*StreamBudgetError)(nil)

func (e *StreamBudgetError) Error() string {
	if e == nil {
		return "llm stream exceeded the local response budget"
	}
	return fmt.Sprintf(
		"llm stream exceeded the local client response budget (%d bytes read, limit %d); the client stopped reading, the provider did not end the reply",
		e.Read, e.Limit)
}

// LLMErrorKind classifies the cut as a local budget exhaustion.
func (e *StreamBudgetError) LLMErrorKind() llmapp.LLMErrorKind { return llmapp.LLMKindBudget }

// HTTPStatusCode is 0: no upstream status produced this failure.
func (e *StreamBudgetError) HTTPStatusCode() int { return 0 }

// UpstreamMessage carries the local cause verbatim. There is no provider
// message for this failure; surfacing the local text keeps the operator-facing
// envelope honest instead of substituting a generic provider rejection.
func (e *StreamBudgetError) UpstreamMessage() string { return e.Error() }

// countingReader records how many bytes the SSE scanner actually consumed so an
// exhausted budget can be told apart from a provider that closed the stream.
type countingReader struct {
	src   io.Reader
	total int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	r.total += int64(n)
	return n, err
}

// streamCallRef is the stable identity of one streamed function call, shared
// by both protocol stream states so the id/name announced for a call index is
// re-emitted on every later argument fragment.
type streamCallRef struct {
	id   string
	name string
}

// consumeSSE is the single SSE framing scanner shared by the chat-completions
// and Responses stream consumers (single source, constitution V.3/V.4). It
// reads data: lines (accumulating multi-line data payloads), dispatches each
// complete event to handle on the blank-line boundary, and stops when handle
// reports the terminal event. handle returns (done, err): done marks the
// terminal event, err aborts the stream with the handler's typed failure.
//
// maxOutputTokens is the request's forwarded output limit; the byte budget is
// derived from it (streamResponseBudget, H-E12) so a configured long turn is not
// starved by the default floor.
//
// Fail-closed contract (identical for both protocols): an empty body is an
// "empty stream" transient failure; EOF before the terminal event is "ended
// before completion"; a read failure is a transient failure carrying
// readFailMsg. The body is bounded by the derived streaming budget — NOT the
// non-streaming maxResponseBytes. Reaching that budget without a terminal event
// is reported as the LOCAL cut (*StreamBudgetError), never as a provider
// truncation, and never silently truncated into a successful turn.
// bufio.ReadString is used instead of bufio.Scanner so a single long data line
// is not silently dropped at the Scanner's 64KiB token cap.
func consumeSSE(body io.Reader, handle func(data string) (bool, error), readFailMsg string, maxOutputTokens int) error {
	budget := streamResponseBudget(maxOutputTokens)
	counted := &countingReader{src: body}
	reader := bufio.NewReader(io.LimitReader(counted, budget))
	var data strings.Builder
	sawData := false
	terminal := false

	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		sawData = true
		done, err := handle(data.String())
		data.Reset()
		if err != nil {
			return err
		}
		if done {
			terminal = true
		}
		return nil
	}

	for !terminal {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if err := dispatch(); err != nil {
					return err
				}
			case strings.HasPrefix(line, "data:"):
				value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return &TransientError{Err: errors.New(readFailMsg)}
		}
	}
	if !terminal {
		// Tolerate a final event that is not followed by a blank line.
		if err := dispatch(); err != nil {
			return err
		}
	}
	switch {
	case terminal:
		return nil
	case counted.total >= budget:
		// The client stopped reading at its own budget: attribute the cut to
		// this process, not to the provider (constitution VII.7).
		return &StreamBudgetError{Limit: budget, Read: counted.total}
	case !sawData:
		return &TransientError{Err: errors.New("llm provider returned an empty stream")}
	default:
		return &TransientError{Err: errors.New("llm provider stream ended before completion")}
	}
}
