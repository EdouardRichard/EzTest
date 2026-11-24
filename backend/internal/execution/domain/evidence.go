package execution

import "time"

// EvidenceKind is the persisted evidence kind enum. The kind set is the single
// registry of what the evidence settlement path accepts: every execution
// channel declares its evidence kind here, and an artifact kind outside this
// registry fails closed (constitution art.4.5).
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
	// Recording-layer kinds (v0.2.4-C): command execution records, workspace
	// file snapshots and browser/execution traces. Producers are the executor
	// v2 recording layer; the settlement path accepts them through this
	// registry like every other kind.
	EvidenceCommand EvidenceKind = "command"
	EvidenceFile    EvidenceKind = "file"
	EvidenceTrace   EvidenceKind = "trace"
)

// EvidenceKinds is the authoritative evidence-kind registry. API enums, the
// artifact settlement gate and contract tests derive from it; do not maintain
// a second hand-written kind list.
func EvidenceKinds() []EvidenceKind {
	return []EvidenceKind{
		EvidenceScreenshot, EvidenceSnapshot, EvidenceConsole, EvidenceNetwork,
		EvidenceHTTP, EvidenceDB, EvidenceDownload, EvidenceText,
		EvidenceCommand, EvidenceFile, EvidenceTrace,
	}
}

// Valid reports whether kind is one of the persisted evidence kinds.
func (k EvidenceKind) Valid() bool {
	for _, kind := range EvidenceKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// MIME returns the default MIME type for a kind when the uploaded artifact does
// not carry one.
func (k EvidenceKind) MIME() string {
	switch k {
	case EvidenceScreenshot:
		return "image/png"
	case EvidenceSnapshot, EvidenceText, EvidenceDB:
		return "text/plain; charset=utf-8"
	case EvidenceConsole, EvidenceNetwork, EvidenceHTTP:
		return "application/json"
	case EvidenceDownload, EvidenceCommand, EvidenceFile, EvidenceTrace:
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
	SUTID          *int64
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
	CaseRunID *int64
	SUTID     *int64
	Kind      EvidenceKind
	BeforeID  *int64
	Limit     int
}

// EvidenceFilenameMax / EvidenceMIMEMax bound evidence metadata strings.
const (
	EvidenceFilenameMax = 255
	EvidenceMIMEMax     = 128
)
