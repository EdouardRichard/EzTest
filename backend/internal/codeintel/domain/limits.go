package codeintel

// Hard domain limits (spec §4.5/§5). Settings may lower a limit but can never
// raise it above these constants.
const (
	MaxFactsPerFile         = 200
	MaxEdgesPerFile         = 200
	MaxEvidencePerFact      = 8
	MaxExcerptLines         = 200
	MaxFactKeyChars         = 512
	MaxReasonChars          = 2000
	MaxPathChars            = 4096
	MaxTreeEntriesPerPage   = 200
	MaxReadLines            = 400
	MaxFileReadBytes        = 64 << 10
	MaxRequirementsInPrompt = 100
	MaxAnalysisErrors       = 20
	MaxFactVersionsSnapshot = 500
	MaxUnmappedEntries      = 200
	MaxChangedFilesStored   = 2000
)
