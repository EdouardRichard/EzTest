package codeintel

import "testing"

// TestLimits pins the hard domain limits of spec §4.5. Settings may lower
// these values but can never raise them.
func TestLimits(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"MaxFactsPerFile", MaxFactsPerFile, 200},
		{"MaxEdgesPerFile", MaxEdgesPerFile, 200},
		{"MaxEvidencePerFact", MaxEvidencePerFact, 8},
		{"MaxExcerptLines", MaxExcerptLines, 200},
		{"MaxFactKeyChars", MaxFactKeyChars, 512},
		{"MaxReasonChars", MaxReasonChars, 2000},
		{"MaxPathChars", MaxPathChars, 4096},
		{"MaxTreeEntriesPerPage", MaxTreeEntriesPerPage, 200},
		{"MaxReadLines", MaxReadLines, 400},
		{"MaxFileReadBytes", MaxFileReadBytes, 64 << 10},
		{"MaxRequirementsInPrompt", MaxRequirementsInPrompt, 100},
		{"MaxAnalysisErrors", MaxAnalysisErrors, 20},
		{"MaxFactVersionsSnapshot", MaxFactVersionsSnapshot, 500},
		{"MaxUnmappedEntries", MaxUnmappedEntries, 200},
		{"MaxChangedFilesStored", MaxChangedFilesStored, 2000},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}
