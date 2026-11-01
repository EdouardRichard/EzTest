package execution

import "time"

// EvidenceKind is the frozen evidence kind enum (P3-D-21, extended by P5 §3.6).
type EvidenceKind string

const (
	EvidenceScreenshot EvidenceKind = "screenshot"
	EvidenceSnapshot   EvidenceKind = "snapshot"
	EvidenceConsole    EvidenceKind = "console"
	EvidenceNetwork    EvidenceKind = "network"
	EvidenceHTTP       EvidenceKind = "http"
	EvidenceDB         EvidenceKind = "db"
	EvidenceDownload   EvidenceKind = "download"
	EvidenceText       EvidenceKind = "text"
	// P5 visual evidence kinds: the live capture and the pixel-diff image of
	// one visual assertion comparison (executor visual.compare).
	EvidenceVisualActual EvidenceKind = "visual_actual"
	EvidenceVisualDiff   EvidenceKind = "visual_diff"
)

// EvidenceMaxPerCaseRun caps the evidence rows of one case_run. Overflow is
// dropped and counted, never failed.
const EvidenceMaxPerCaseRun = 100

// Valid reports whether kind is one of the ten persisted evidence kinds.
func (k EvidenceKind) Valid() bool {
	switch k {
	case EvidenceScreenshot, EvidenceSnapshot, EvidenceConsole, EvidenceNetwork,
		EvidenceHTTP, EvidenceDB, EvidenceDownload, EvidenceText,
		EvidenceVisualActual, EvidenceVisualDiff:
		return true
	default:
		return false
	}
}

// MIME returns the default MIME type for a kind when the uploaded artifact does
// not carry one.
func (k EvidenceKind) MIME() string {
	switch k {
	case EvidenceScreenshot, EvidenceVisualActual, EvidenceVisualDiff:
		return "image/png"
	case EvidenceSnapshot, EvidenceText, EvidenceDB:
		return "text/plain; charset=utf-8"
	case EvidenceConsole, EvidenceNetwork, EvidenceHTTP:
		return "application/json"
	case EvidenceDownload:
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}

// Evidence is one immutable evidence row. Bytes live in the P0 content ledger
// (content_ref_id); the execution context stores only metadata (P3-D-20).
type Evidence struct {
	ID             int64
	CaseRunID      int64
	VersionSUTID   *int64
	StepSeq        *int
	AssertionSeq   *int
	Kind           EvidenceKind
	ContentRefID   int64
	SHA256         string
	SizeBytes      int64
	MIME           string
	Filename       string
	CreatedByRunID *int64
	CreatedAt      time.Time
}

// Evidence owner kinds of evidence_ref.
const (
	EvidenceOwnerCaseRun         = "case_run"
	EvidenceOwnerStepResult      = "step_result"
	EvidenceOwnerAssertionResult = "assertion_result"
	EvidenceOwnerHumanRequest    = "human_request"
)

// ValidEvidenceOwnerKind reports whether ownerKind is a persisted evidence_ref
// owner kind.
func ValidEvidenceOwnerKind(ownerKind string) bool {
	switch ownerKind {
	case EvidenceOwnerCaseRun, EvidenceOwnerStepResult, EvidenceOwnerAssertionResult, EvidenceOwnerHumanRequest:
		return true
	default:
		return false
	}
}

// EvidenceRef links an evidence row to an owning result row.
type EvidenceRef struct {
	ID         int64
	EvidenceID int64
	OwnerKind  string
	OwnerID    int64
	Role       string
	CreatedAt  time.Time
}

// EvidenceFilter narrows one evidence page. Limit and BeforeID implement the
// shared descending-ID cursor convention.
type EvidenceFilter struct {
	CaseRunID    *int64
	VersionSUTID *int64
	Kind         EvidenceKind
	BeforeID     *int64
	Limit        int
}

// EvidenceFilenameMax / EvidenceMIMEMax bound evidence metadata strings.
const (
	EvidenceFilenameMax = 255
	EvidenceMIMEMax     = 128
)
