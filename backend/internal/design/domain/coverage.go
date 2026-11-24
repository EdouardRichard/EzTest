package design

import "sort"

// CoverageUnion returns the sorted, deduplicated union of covered AC ids from
// confirmed cases and current draft cases. Coverage accumulates across proposal
// batches: a single partial batch is incomplete, but confirmed ∪ drafts can be
// complete (spec SC-E2). AC ids are canonicalized via NormalizeACIDSet.
func CoverageUnion(confirmedACs, draftACs [][]string) []string {
	set := map[string]struct{}{}
	for _, group := range [][][]string{confirmedACs, draftACs} {
		for _, acs := range group {
			for _, id := range NormalizeACIDSet(acs) {
				set[id] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// MissingACs returns the normalized requirement AC ids that are not present in
// union, in the LEXICOGRAPHIC order NormalizeACIDSet produces — not numeric id
// order ("AC-10" sorts before "AC-2"). Callers that render or compare the list
// must not assume numeric ordering.
//
// union must already be normalized: pass the CoverageUnion output (or another
// NormalizeACIDSet result). Raw covered_acs rows are NOT normalized, so a raw
// union would report false gaps for ids that differ only in surrounding
// whitespace or appear more than once.
func MissingACs(union, requirementACIDs []string) []string {
	have := map[string]struct{}{}
	for _, id := range union {
		have[id] = struct{}{}
	}
	var missing []string
	for _, id := range NormalizeACIDSet(requirementACIDs) {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// CoverageComplete reports whether union covers every requirement AC id.
func CoverageComplete(union, requirementACIDs []string) bool {
	return len(MissingACs(union, requirementACIDs)) == 0
}
