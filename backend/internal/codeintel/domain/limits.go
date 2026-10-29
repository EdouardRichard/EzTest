package domain

// P4 limits from design spec §4.2. They are code constants, not configuration;
// configuration values may lower but never raise them.
const (
	// MaxFactsPerFile bounds the AI facts accepted for one file.
	MaxFactsPerFile = 200
	// MaxEdgesPerFile bounds the AI edges accepted for one file.
	MaxEdgesPerFile = 200
	// MaxEvidencePerFact bounds the evidence entries of one fact.
	MaxEvidencePerFact = 8
	// MaxExcerptLines is the hard upper bound of an evidence excerpt.
	MaxExcerptLines = 200
	// MaxFactKeyChars bounds a fact_key.
	MaxFactKeyChars = 512
	// MaxReasonChars bounds a candidate or unmapped reason.
	MaxReasonChars = 2000
	// MaxPathChars bounds a repository-relative file path.
	MaxPathChars = 4096
	// MaxUnmappedEntries bounds the unmapped entries of one analysis.
	MaxUnmappedEntries = 200
	// MaxChangedFilesStored bounds the changed files of one change set.
	MaxChangedFilesStored = 2000
	// MaxTreeEntriesPerPage bounds one tree page.
	MaxTreeEntriesPerPage = 200
	// MaxReadLines bounds one file-window read.
	MaxReadLines = 400
	// MaxRequirementsInPrompt bounds the requirement list of one AI impact
	// prompt.
	MaxRequirementsInPrompt = 100
	// MaxAnalysisErrors bounds the error entries of one analysis.
	MaxAnalysisErrors = 20
	// MaxFactVersionsSnapshot bounds the fact-version snapshot of one analysis.
	MaxFactVersionsSnapshot = 500
	// MaxFileReadBytes bounds on-demand file content reads (aligned with P3
	// gitread).
	MaxFileReadBytes = 64 << 10
)
