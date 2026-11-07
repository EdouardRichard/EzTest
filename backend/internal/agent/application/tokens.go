package agentapp

// tokenOverhead is the fixed per-section charge of the P1 estimator; it covers
// role markers and formatting the model does not see in the raw text.
const tokenOverhead = 16

// cjkTokenUnits is the cost of one CJK rune expressed in quarter-token units:
// a CJK rune costs one whole token (4 units) while a non-CJK rune costs a
// quarter token (1 unit). Estimating in integer units keeps the estimator
// deterministic and lets truncateToTokens cut at a rune boundary.
const cjkTokenUnits = 4

// EstimateTokens is the v0.22 conservative token estimator (R-A5-14, A §6.10):
//
//	CJK runes × 1.0 + ceil(non-CJK runes / 4) + 16
//
// The previous runes/2 + 16 formula under-estimated Chinese roughly twofold
// (its comment claimed the opposite), so pressure compaction triggered late.
// Charging every CJK rune a full token and rounding the non-CJK quarter-token
// rate up keeps the estimate above the real provider token count ("宁高勿低") and
// makes it monotonic: appending text never lowers the estimate. The fixed
// overhead covers role markers and section formatting.
//
// This is a heuristic, not a tokenizer; v0.22 anchors new content on the
// provider's previous prompt_tokens (R-A5-14) and uses this estimate only for
// the delta.
func EstimateTokens(text string) int {
	cjk, other := 0, 0
	for _, r := range text {
		if isCJKTokenRune(r) {
			cjk++
		} else {
			other++
		}
	}
	// Ceil the non-CJK quarter-token rate so a short ASCII tail is never free.
	return cjk + (other+3)/4 + tokenOverhead
}

// isCJKTokenRune reports whether r belongs to a writing system the estimator
// charges a full token per rune: Chinese (incl. extensions, radicals and CJK
// punctuation/fullwidth forms), Japanese kana and Korean Hangul. Everything
// else is charged the non-CJK quarter-token rate.
func isCJKTokenRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x11FF: // Hangul Jamo
		return true
	case r >= 0x2E80 && r <= 0x2FDF: // CJK radicals (supplement + Kangxi)
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK symbols and punctuation
		return true
	case r >= 0x3040 && r <= 0x30FF: // Hiragana + Katakana
		return true
	case r >= 0x3100 && r <= 0x318F: // Bopomofo + Hangul compatibility Jamo
		return true
	case r >= 0x3200 && r <= 0x33FF: // enclosed CJK letters/months + compatibility
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK unified ideographs extension A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK unified ideographs
		return true
	case r >= 0xA960 && r <= 0xA97F: // Hangul Jamo extended-A
		return true
	case r >= 0xAC00 && r <= 0xD7FF: // Hangul syllables + Jamo extended-B
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK compatibility ideographs
		return true
	case r >= 0xFE30 && r <= 0xFE4F: // CJK compatibility forms
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // halfwidth/fullwidth forms (CJK punctuation)
		return true
	case r >= 0x1F200 && r <= 0x1F2FF: // enclosed ideographic supplement
		return true
	case r >= 0x20000 && r <= 0x2FA1F: // CJK extensions B–F + compatibility supplement
		return true
	case r >= 0x30000 && r <= 0x3134F: // CJK extension G
		return true
	default:
		return false
	}
}

// truncateToTokens returns the longest prefix of text whose estimated token
// count fits maxTokens. If maxTokens cannot even cover the estimator overhead,
// it returns the empty string (whose estimate is tokenOverhead).
//
// The cut uses the same CJK-aware rate as EstimateTokens. Because the unit
// budget is (maxTokens-overhead)*4 and both rune rates are integral multiples
// of one unit, the returned prefix's estimate is always <= maxTokens.
func truncateToTokens(text string, maxTokens int) string {
	if maxTokens <= tokenOverhead {
		return ""
	}
	unitBudget := (maxTokens - tokenOverhead) * cjkTokenUnits
	units := 0
	for index, r := range text {
		cost := 1
		if isCJKTokenRune(r) {
			cost = cjkTokenUnits
		}
		if units+cost > unitBudget {
			return text[:index]
		}
		units += cost
	}
	return text
}
