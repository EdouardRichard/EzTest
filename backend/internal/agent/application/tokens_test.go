package agentapp_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// TestEstimateTokens pins the v0.22 CJK-aware estimator (R-A5-14, A §6.10):
// every CJK rune is charged one token, every non-CJK rune a quarter token
// (rounded up so the estimate never under-counts), plus the fixed overhead.
func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"empty", "", 16},
		{"ascii short", "hello", 18},
		{"ascii long", strings.Repeat("a", 100), 41},
		{"chinese", "你好世界", 20},
		{"chinese long", strings.Repeat("中文", 50), 116},
		{"chinese punctuation", "需求澄清：，。！", 24},
		{"mixed", "登录 login", 20},
		{"mixed long", "需求澄清 abcdefgh", 23},
		{"newlines", "a\nb\nc", 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentapp.EstimateTokens(tc.input); got != tc.want {
				t.Fatalf("EstimateTokens(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

// TestEstimateTokensIsConservativeForChinese pins "宁高勿低" (A:431): a run of N
// CJK runes is charged at least N tokens, not the historical N/2.
func TestEstimateTokensIsConservativeForChinese(t *testing.T) {
	text := strings.Repeat("需求澄清", 100)
	runes := utf8.RuneCountInString(text)
	if got := agentapp.EstimateTokens(text); got < runes+16 {
		t.Fatalf("estimate %d is below the CJK floor %d", got, runes+16)
	}
}

// TestEstimateTokensIsConservativeForASCII pins the ceil rounding for the
// non-CJK quarter-token rate: three ASCII runes still cost one whole token.
func TestEstimateTokensIsConservativeForASCII(t *testing.T) {
	for runes := 0; runes <= 41; runes++ {
		text := strings.Repeat("a", runes)
		want := (runes+3)/4 + 16
		if got := agentapp.EstimateTokens(text); got != want {
			t.Fatalf("EstimateTokens(%d ascii runes) = %d, want ceil rate %d", runes, got, want)
		}
	}
}

// TestEstimateTokensIsMonotonic pins that appending text never lowers the
// estimate, so pressure compaction cannot be postponed by a longer prompt.
func TestEstimateTokensIsMonotonic(t *testing.T) {
	text := "需求澄清：登录与注册 login/register，验收标准见 §3.2。"
	previous := 0
	for index := 0; index < len(text); index++ {
		prefix := text[:index]
		// Only measure at rune boundaries; UTF-8 prefixes in the middle of a
		// multi-byte rune decode as U+FFFD (non-CJK) and are not meaningful.
		if !utf8.ValidString(prefix) {
			continue
		}
		got := agentapp.EstimateTokens(prefix)
		if got < previous {
			t.Fatalf("estimate dropped from %d to %d at prefix %q", previous, got, prefix)
		}
		previous = got
	}
}
