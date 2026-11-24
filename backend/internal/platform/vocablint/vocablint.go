// Package vocablint is the shared machinery of the repository's status-vocabulary
// gates: the occurrence-scoped exemption algorithm, the literal scanners, the
// vocabulary-derivation wrapper over platform/govocab, the elision-aware const
// walk and the scan-surface derivation helpers.
//
// It is TEST SUPPORT: only _test.go files import it. It lives in the platform
// shared kernel because a gate in ANY context (and a repository-level gate in
// cmd/eztest) must be able to reuse ONE algorithm. Everything here is a pure
// function over source text / AST / derived values — no entity state, no I/O
// beyond reading source files, no side effects — which is what makes platform the
// right home for it. It imports no bounded context.
//
// # Why it exists (T11e-0)
//
// The gates were built incrementally (T2 design, T3/T4 execution + agent, H-B1
// handoff, T11b/T11c H-A17). Each kept its own copy of the same algorithm inside
// its _test.go file, where Go makes it unimportable from anywhere else: five
// independent copies of occurrenceSanction and four local AST value walks
// (constitution 五.1/五.3 存量违例). This package is the single authority they all
// call now. The SCOPE of a gate (which files it guards, which files define its
// vocabulary) and its adjudicated exemptions stay at the gate — those are
// legitimately per-surface. The ALGORITHM does not.
//
// # What a gate keeps
//
//   - its scan surface (derived, never a hand-written file list);
//   - its vocabulary authorities (files / declared types / authority functions);
//   - its exemption table: fragment-scoped, occurrence-scoped, with a non-empty
//     reason, and non-vacuous (a fragment that sanctions nothing is stale and
//     must be removed in the same change set).
//
// Every helper that can detect a problem RETURNS it rather than calling
// *testing.T, so this package stays a pure production package (no testing import,
// no flag registration, no output). The gate decides how to report.
package vocablint

import (
	"sort"
	"strings"
)

// Exemption sanctions the guarded-token occurrences carried by Fragment
// (occurrence-scoped). Reason documents why those occurrences are not a
// vocabulary read that must go through a domain constant.
//
// SINGLE-LINE CONSTRAINT: Fragment MUST be a single source line.
// OccurrenceSanction evaluates one whitespace-normalized line at a time, so a
// fragment spanning two lines can never match and would silently sanction
// nothing — the non-vacuous check then fails it as stale.
type Exemption struct {
	Fragment string
	Reason   string
}

// OccurrenceSanction reports how many occurrences of token on line are NOT
// sanctioned by an exemption fragment, and which exemption INDICES sanctioned at
// least one occurrence (for the non-vacuous check; index-keyed so two exemptions
// with identical fragment text never share a count).
//
// Whitespace is normalized so gofmt alignment does not affect matching; the
// guarded tokens ('value' / "value") carry no internal whitespace, so their
// positions are stable under normalization.
//
// Sanctioning is by POSITION COVERAGE, not by counting: every match interval of a
// fragment marks the token occurrences it spans. An occurrence covered by several
// (possibly overlapping/sub-string) fragments is sanctioned once (boolean dedup),
// and a fragment that legitimately appears twice on a line sanctions both
// occurrences. This is why no clamp is needed: a rogue occurrence sharing the
// line with a sanctioned one is never hidden by fragment overlap, and a sanctioned
// fragment repeated on a line never produces a false positive.
func OccurrenceSanction(line, token string, exemptions []Exemption) (int, []int) {
	covered, perExemption := OccurrenceCoverage(line, token, exemptions)
	remaining := 0
	for _, c := range covered {
		if !c {
			remaining++
		}
	}
	var hits []int
	for i := range perExemption {
		hits = append(hits, i)
	}
	sort.Ints(hits)
	return remaining, hits
}

// OccurrenceCoverage is the coverage algorithm OccurrenceSanction reports from:
// for each token occurrence on the line (in source order) whether some fragment
// spans it, and for each exemption INDEX the occurrence indices it spans.
//
// The per-occurrence attribution is what makes an exemption registry 1:1 with the
// occurrences it adjudicates (T11e-6 review Minor 3): a fragment whose text spans
// TWO occurrences on one line -- a frozen `<section id="issues"
// data-section="issues">` carries the value twice -- adjudicates two occurrences,
// and keying sites by LINE alone let one fragment stand for both, so repaying
// either went undetected.
func OccurrenceCoverage(line, token string, exemptions []Exemption) ([]bool, map[int][]int) {
	norm := strings.Join(strings.Fields(line), " ")
	tokenStarts := AllIndices(norm, token)
	covered := make([]bool, len(tokenStarts))
	perExemption := map[int][]int{}
	if len(tokenStarts) == 0 {
		return covered, perExemption
	}
	for i, ex := range exemptions {
		frag := strings.Join(strings.Fields(ex.Fragment), " ")
		if frag == "" {
			continue
		}
		// A fragment text can match the line at several overlapping intervals
		// (AllIndices advances one byte, so overlaps are all found). Deduplicate
		// the occurrence indices so a fragment that spans one occurrence via two
		// intervals reports it ONCE -- otherwise FragmentHits would inflate
		// (review round-2 nit). Coverage itself is boolean and unaffected.
		seen := map[int]bool{}
		for _, fs := range AllIndices(norm, frag) {
			fe := fs + len(frag)
			for j, ts := range tokenStarts {
				if ts >= fs && ts+len(token) <= fe {
					covered[j] = true
					if !seen[j] {
						seen[j] = true
						perExemption[i] = append(perExemption[i], j)
					}
				}
			}
		}
	}
	return covered, perExemption
}

// AllIndices returns every start index of sub in s (advancing one byte past each
// match so overlapping matches are all found). An empty sub returns nil.
func AllIndices(s, sub string) []int {
	var idx []int
	if sub == "" {
		return nil
	}
	for off := 0; off <= len(s)-len(sub); {
		i := strings.Index(s[off:], sub)
		if i < 0 {
			break
		}
		idx = append(idx, off+i)
		off += i + 1
	}
	return idx
}

// Exemptions converts a fragment/reason table into the slice OccurrenceSanction
// takes, preserving order so the returned hit indices address the same entries.
func Exemptions(fragments []string, reason string) []Exemption {
	out := make([]Exemption, 0, len(fragments))
	for _, fragment := range fragments {
		out = append(out, Exemption{Fragment: fragment, Reason: reason})
	}
	return out
}
