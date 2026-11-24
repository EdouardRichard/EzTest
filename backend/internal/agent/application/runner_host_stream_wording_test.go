package agentapp_test

// H-E13/H-E16 (D 交接 §4.1i #14/#17): the Host reply text trim parity and the
// salvage seal wording precision. Expectations come from the contracts — the
// batch loop returns strings.TrimSpace(finalText) (runner.go), so the incremental
// head must carry the same trimmed text (same reply, same first/last whitespace
// on both paths); and a salvage seal must claim content loss only when content
// was actually lost (constitution VII.6 honest attribution).

import (
	"context"
	"errors"
	"strings"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestHostStreamTextTrimParity pins H-E13: the same reply rendered through the
// incremental streaming head and through the batch fallback carries identical
// first/last whitespace. The batch loop trims the final text (TrimSpace); the
// streaming head must trim its live text snapshots the same way, or the two paths
// disagree about the reply's edges. Reverting the streaming trim makes the
// streaming leg fail (the mutation this test guards).
func TestHostStreamTextTrimParity(t *testing.T) {
	const padded = "  回复 正文  \n"
	const trimmed = "回复 正文"

	t.Run("streaming head trims like the batch final text", func(t *testing.T) {
		fixture := newHostStreamFixture(t)
		fixture.chat.streams = [][]llmapp.Delta{{
			{Content: "  回复"},
			{Content: " 正文  \n"},
			{FinishReason: "stop"},
		}}
		result, err := fixture.runner.Run(context.Background(), hostItem())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Status != agent.AgentRunDone {
			t.Fatalf("result = %#v, want done", result)
		}
		_, _, updates, finishes, interrupts := fixture.stream.snapshot()
		if len(finishes) != 1 || len(interrupts) != 0 {
			t.Fatalf("finish=%d interrupt=%d, want one complete seal", len(finishes), len(interrupts))
		}
		text := partByType(finishes[0].Parts, "text")
		if text == nil || text.Text != trimmed {
			t.Fatalf("streaming head text = %q, want the trimmed %q (batch parity)", text.Text, trimmed)
		}
		// The live snapshots are trimmed too, and still grow by prefix (R-D2).
		assertGrowingPrefixChain(t, "text", seqTexts(updates, 1), trimmed)
	})

	t.Run("batch fallback writes the same trimmed text", func(t *testing.T) {
		fixture := newHostStreamFixture(t)
		// The head cannot open, so the reply takes the batch append path.
		fixture.stream.beginErr = errors.New("head could not open")
		fixture.chat.responses = []llmapp.ChatResponse{{Content: padded, FinishReason: "stop"}}
		result, err := fixture.runner.Run(context.Background(), hostItem())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Status != agent.AgentRunDone {
			t.Fatalf("result = %#v, want done", result)
		}
		text := partByType(fixture.stream.written, "text")
		if text == nil || text.Text != trimmed {
			t.Fatalf("batch fallback text = %q, want the trimmed %q", text.Text, trimmed)
		}
	})
}

// TestHostStreamSalvageWordingMatchesActualLoss pins H-E16: when the complete
// seal is transiently refused but the head already stores the WHOLE reply, the
// salvage seal must not claim "其余内容未能写入" (there is no loss); when the stored
// prefix is genuinely incomplete (an oversized reply the store refused), the
// salvage seal must report the loss. Reverting to the unconditional loss wording
// makes the no-loss leg fail (the mutation this test guards).
func TestHostStreamSalvageWordingMatchesActualLoss(t *testing.T) {
	t.Run("stored holds the whole reply: no loss claimed", func(t *testing.T) {
		fixture := newHostStreamFixture(t)
		fixture.chat.responses = []llmapp.ChatResponse{{Content: "完整回复", FinishReason: "stop"}}
		// Force the complete seal to be refused; every live update succeeded, so
		// the stored prefix already holds the entire reply.
		fixture.stream.finishErr = errors.New("transient complete-seal refusal")
		if _, err := fixture.runner.Run(context.Background(), hostItem()); err == nil {
			t.Fatal("expected the refused complete seal to fail the run")
		}
		_, _, _, _, interrupts := fixture.stream.snapshot()
		if len(interrupts) != 1 {
			t.Fatalf("interrupt seals = %d, want exactly 1 salvage seal", len(interrupts))
		}
		if text := partByType(interrupts[0].Parts, "text"); text == nil || text.Text != "完整回复" {
			t.Fatalf("salvage dropped the fully stored reply: %#v", interrupts[0].Parts)
		}
		errorPart := partByType(interrupts[0].Parts, "error")
		if errorPart == nil {
			t.Fatalf("salvage has no error part: %#v", interrupts[0].Parts)
		}
		message, _ := errorPart.Data["message"].(string)
		if strings.Contains(message, "其余内容未能写入") {
			t.Fatalf("salvage claims loss when the stored prefix holds the whole reply: %q", message)
		}
		if strings.TrimSpace(message) == "" {
			t.Fatal("salvage error part has an empty message")
		}
	})

	t.Run("stored is a strict prefix: loss is reported", func(t *testing.T) {
		fixture := newHostStreamFixture(t)
		oversized := strings.Repeat("超", version.MessageTextMax+1)
		fixture.chat.streams = [][]llmapp.Delta{{
			{Content: "已经保存的前缀"},
			{Content: oversized},
			{FinishReason: "stop"},
		}}
		if _, err := fixture.runner.Run(context.Background(), hostItem()); err == nil {
			t.Fatal("expected the oversized reply to fail the run")
		}
		_, _, _, _, interrupts := fixture.stream.snapshot()
		if len(interrupts) == 0 {
			t.Fatal("the oversized reply left the head streaming (no salvage seal)")
		}
		sealed := interrupts[len(interrupts)-1]
		if text := partByType(sealed.Parts, "text"); text == nil || text.Text != "已经保存的前缀" {
			t.Fatalf("salvage dropped the stored prefix: %#v", sealed.Parts)
		}
		errorPart := partByType(sealed.Parts, "error")
		if errorPart == nil {
			t.Fatalf("salvage has no error part: %#v", sealed.Parts)
		}
		message, _ := errorPart.Data["message"].(string)
		if !strings.Contains(message, "其余内容未能写入") {
			t.Fatalf("salvage hides a real content loss: %q", message)
		}
	})
}
