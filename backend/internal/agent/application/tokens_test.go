package agentapp_test

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"empty", "", 16},
		{"ascii short", "hello", 18},
		{"ascii long", strings.Repeat("a", 100), 66},
		{"chinese", "你好世界", 18},
		{"chinese long", strings.Repeat("中文", 50), 66},
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

func TestEstimateTokensIsConservativeForChinese(t *testing.T) {
	// A Chinese string of N runes is charged at least N/2 tokens; this is the
	// P1 conservative budget used by the context builder.
	text := strings.Repeat("需求澄清", 100)
	if got := agentapp.EstimateTokens(text); got < len([]rune(text))/2+16 {
		t.Fatalf("estimate %d is below the conservative floor", got)
	}
}
