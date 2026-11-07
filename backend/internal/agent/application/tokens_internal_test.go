package agentapp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateToTokensHonoursCJKEstimate pins that the prefix cutter uses the
// same v0.22 CJK-aware rate as EstimateTokens (R-A5-14). A CJK prefix charged at
// the historical two-runes-per-token rate would let a caller send twice the
// promised budget to the provider.
func TestTruncateToTokensHonoursCJKEstimate(t *testing.T) {
	t.Run("chinese", func(t *testing.T) {
		text := strings.Repeat("需求澄清", 100)
		got := truncateToTokens(text, 200)
		if estimate := EstimateTokens(got); estimate > 200 {
			t.Fatalf("truncated estimate %d exceeds max 200", estimate)
		}
		if runes := utf8.RuneCountInString(got); runes != 184 {
			t.Fatalf("truncated runes = %d, want the 184-rune CJK budget", runes)
		}
	})
	t.Run("ascii", func(t *testing.T) {
		text := strings.Repeat("a", 100)
		got := truncateToTokens(text, 20)
		if estimate := EstimateTokens(got); estimate > 20 {
			t.Fatalf("truncated estimate %d exceeds max 20", estimate)
		}
		if runes := utf8.RuneCountInString(got); runes != 16 {
			t.Fatalf("truncated runes = %d, want the 16-rune non-CJK budget", runes)
		}
	})
	t.Run("below overhead", func(t *testing.T) {
		if got := truncateToTokens("需求", tokenOverhead); got != "" {
			t.Fatalf("truncateToTokens at overhead = %q, want empty", got)
		}
	})
}
