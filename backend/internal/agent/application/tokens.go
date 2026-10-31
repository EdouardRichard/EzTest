package agentapp

import "unicode/utf8"

// tokenOverhead is the fixed per-section charge of the P1 estimator; it covers
// role markers and formatting the model does not see in the raw text.
const tokenOverhead = 16

// EstimateTokens is the conservative P1 token estimator: runes/2 + 16. Chinese
// text is charged roughly one token per two runes, which over-estimates real
// model tokens and keeps the context budget safe (spec §5.3, P1-D4/Q23).
func EstimateTokens(text string) int {
	return utf8.RuneCountInString(text)/2 + tokenOverhead
}

// truncateToTokens returns the longest prefix of text whose estimated token
// count fits maxTokens. If maxTokens cannot even cover the estimator overhead,
// it returns the empty string (whose estimate is tokenOverhead).
func truncateToTokens(text string, maxTokens int) string {
	if maxTokens <= tokenOverhead {
		return ""
	}
	maxRunes := (maxTokens - tokenOverhead) * 2
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes <= 0 {
		return ""
	}
	return string(runes[:maxRunes])
}
