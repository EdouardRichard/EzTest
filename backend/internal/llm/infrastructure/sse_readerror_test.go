package llminfra

// H-E4 (D 交接 §4.1i #2): the shared consumeSSE scanner's NON-EOF read-error
// branch had no test. Both protocol stream consumers (chat-completions and
// Responses) route a transport read failure through this one branch, so a
// regression here silently changes the failure taxonomy of both protocols.
//
// Expectations come from the consumeSSE fail-closed contract (sse.go): a read
// failure that is NOT io.EOF is a transient failure carrying readFailMsg — not
// the "ended before completion" path (which is reserved for a clean io.EOF
// before the terminal event), not a local budget cut, and not a success.

import (
	"errors"
	"io"
	"strings"
	"testing"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// brokenStreamReader yields a fixed prefix and then a non-EOF read error,
// modelling a connection reset mid-stream after some framed bytes arrived.
type brokenStreamReader struct {
	prefix string
	err    error
	off    int
}

func (r *brokenStreamReader) Read(p []byte) (int, error) {
	if r.off < len(r.prefix) {
		n := copy(p, r.prefix[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.err
}

var _ io.Reader = (*brokenStreamReader)(nil)

// TestConsumeSSENonEOFReadError pins the non-EOF read-error branch for BOTH
// protocol readFailMsg values: a transport read failure is a TransientError
// carrying the protocol's readFailMsg, classified transient with no upstream
// HTTP status (the failure never came from a status line).
func TestConsumeSSENonEOFReadError(t *testing.T) {
	// One complete content event arrived, then the connection reset before the
	// terminal event: the scanner must report the read failure, not silently
	// treat the partial stream as a finished turn.
	prefix := `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n"
	readErr := errors.New("connection reset by peer")
	for _, readFailMsg := range []string{"read llm chat stream failed", "read llm responses stream failed"} {
		t.Run(readFailMsg, func(t *testing.T) {
			body := &brokenStreamReader{prefix: prefix, err: readErr}
			err := consumeSSE(body, func(string) (bool, error) { return false, nil }, readFailMsg, int(llmapp.MaxRequestOutputTokens))
			if err == nil {
				t.Fatal("a non-EOF read failure returned success (silent truncation)")
			}
			var transient *TransientError
			if !errors.As(err, &transient) {
				t.Fatalf("error = %v (%T), want *TransientError", err, err)
			}
			if !errors.Is(err, llmapp.ErrTransient) {
				t.Fatalf("error = %v, want it to map onto llmapp.ErrTransient", err)
			}
			if transient.LLMErrorKind() != llmapp.LLMKindTransient {
				t.Fatalf("kind = %q, want transient", transient.LLMErrorKind())
			}
			if transient.HTTPStatusCode() != 0 {
				t.Fatalf("status = %d, want 0 (a read failure has no upstream status)", transient.HTTPStatusCode())
			}
			if !strings.Contains(err.Error(), readFailMsg) {
				t.Fatalf("error = %q, want it to carry the protocol readFailMsg %q", err, readFailMsg)
			}
			// It must NOT be confused with the clean-EOF "ended before completion"
			// path nor with a local budget cut.
			if strings.Contains(err.Error(), "ended before completion") {
				t.Fatalf("a read failure was reported as a clean EOF truncation: %q", err)
			}
			var budgetErr *StreamBudgetError
			if errors.As(err, &budgetErr) {
				t.Fatalf("a read failure was reported as a local budget cut: %v", err)
			}
		})
	}
}

// TestConsumeSSEEOFIsNotAReadError keeps the two branches distinct: a clean
// io.EOF before the terminal event is the "ended before completion" transient
// failure, NOT the readFailMsg branch. A future edit that collapses io.EOF into
// the read-error branch fails here.
func TestConsumeSSEEOFIsNotAReadError(t *testing.T) {
	prefix := `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n"
	body := &brokenStreamReader{prefix: prefix, err: io.EOF}
	err := consumeSSE(body, func(string) (bool, error) { return false, nil }, "read llm chat stream failed", int(llmapp.MaxRequestOutputTokens))
	if err == nil {
		t.Fatal("a stream that ended before completion returned success")
	}
	if !strings.Contains(err.Error(), "ended before completion") {
		t.Fatalf("error = %q, want the clean-EOF \"ended before completion\" path", err)
	}
	if strings.Contains(err.Error(), "read llm chat stream failed") {
		t.Fatalf("a clean EOF was reported through the read-error branch: %q", err)
	}
}
