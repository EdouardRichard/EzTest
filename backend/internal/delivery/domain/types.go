// Package delivery defines the P4 delivery domain types shared by the issue,
// report and analysis use cases (spec §3, §5, §6). It depends on the Go
// standard library only; API and database projections are mapped outside this
// package.
package delivery

import (
	"encoding/json"
	"time"
)

// IssueStatus is the persisted delivery issue lifecycle state (spec §5.2).
// merged is terminal in P4.
type IssueStatus string

// Issue statuses.
const (
	IssueStatusOpen      IssueStatus = "open"
	IssueStatusConfirmed IssueStatus = "confirmed"
	IssueStatusDismissed IssueStatus = "dismissed"
	IssueStatusMerged    IssueStatus = "merged"
)

// Valid reports whether status is a persisted issue status.
func (s IssueStatus) Valid() bool {
	switch s {
	case IssueStatusOpen, IssueStatusConfirmed, IssueStatusDismissed, IssueStatusMerged:
		return true
	default:
		return false
	}
}

// ReportKind is the delivery report scope (spec §3.3): one wave or the whole
// version.
type ReportKind string

// Report kinds.
const (
	ReportKindWave    ReportKind = "wave"
	ReportKindVersion ReportKind = "version"
)

// Valid reports whether kind is a persisted report kind.
func (k ReportKind) Valid() bool {
	switch k {
	case ReportKindWave, ReportKindVersion:
		return true
	default:
		return false
	}
}

// ReportStatus is the delivery report lifecycle state (spec §3.3).
type ReportStatus string

// Report statuses.
const (
	ReportStatusDraft     ReportStatus = "draft"
	ReportStatusPublished ReportStatus = "published"
	ReportStatusStale     ReportStatus = "stale"
)

// Valid reports whether status is a persisted report status.
func (s ReportStatus) Valid() bool {
	switch s {
	case ReportStatusDraft, ReportStatusPublished, ReportStatusStale:
		return true
	default:
		return false
	}
}

// PDFStatus is the P7 PDF render state of one report revision (spec §5 #36).
// HTML is authoritative: PDF is rendered asynchronously and never blocks
// publishing. done/failed/disabled are terminal for the automatic pipeline.
type PDFStatus string

// PDF render states.
const (
	PDFStatusPending  PDFStatus = "pending"
	PDFStatusRunning  PDFStatus = "running"
	PDFStatusDone     PDFStatus = "done"
	PDFStatusFailed   PDFStatus = "failed"
	PDFStatusDisabled PDFStatus = "disabled"
)

// Valid reports whether status is a persisted pdf_status value.
func (s PDFStatus) Valid() bool {
	switch s {
	case PDFStatusPending, PDFStatusRunning, PDFStatusDone, PDFStatusFailed, PDFStatusDisabled:
		return true
	default:
		return false
	}
}

// AnalysisStatus is the delivery analysis_run lifecycle state (spec §3.4).
type AnalysisStatus string

// Analysis statuses.
const (
	AnalysisStatusQueued   AnalysisStatus = "queued"
	AnalysisStatusRunning  AnalysisStatus = "running"
	AnalysisStatusDone     AnalysisStatus = "done"
	AnalysisStatusFailed   AnalysisStatus = "failed"
	AnalysisStatusDegraded AnalysisStatus = "degraded"
)

// Valid reports whether status is a persisted analysis status.
func (s AnalysisStatus) Valid() bool {
	switch s {
	case AnalysisStatusQueued, AnalysisStatusRunning, AnalysisStatusDone,
		AnalysisStatusFailed, AnalysisStatusDegraded:
		return true
	default:
		return false
	}
}

// Issue is one deduplicated delivery issue of a version (spec §3.2/§5).
type Issue struct {
	ID               int64
	VersionID        int64
	Title            string
	Summary          string
	Status           IssueStatus
	Category         string
	Severity         string
	DedupeKey        string
	Fingerprint      string
	Source           string
	ExpectedRevision int32
	MergedIntoID     *int64
	MergedAt         *time.Time
	MergedBy         *int64
	ReopenCount      int
	// RecurrenceCount counts the failures redirect to a merge target (spec
	// §4.6); LastRecurredAt is the latest redirect time. Neither changes the
	// source issue's refs or history.
	RecurrenceCount int
	LastRecurredAt  *time.Time
	CreatedByRunID  *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// IssueRef is one append-only logical reference of an issue (spec §3.2). Kind
// names the referenced entity; RefID is its intra-domain ID.
type IssueRef struct {
	ID           int64
	IssueID      int64
	Kind         string
	RefID        int64
	VersionSUTID *int64
	Role         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IssueStatusChange is one append-only issue status history row (spec §3.2).
// From is nil for the creation entry.
type IssueStatusChange struct {
	ID        int64
	IssueID   int64
	From      *IssueStatus
	To        IssueStatus
	Reason    string
	ActorID   *int64
	ActorKind string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Report is one version or wave report header (spec §3.3).
type Report struct {
	ID                int64
	VersionID         int64
	WaveID            *int64
	Kind              ReportKind
	CurrentRevisionID *int64
	Status            ReportStatus
	InputFingerprint  string
	CreatedByRunID    *int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ReportRevision is one append-only report content revision (spec §3.3). HTMLRef
// and PDFRef point at content_ref IDs. After publish only the PDF metadata
// (PDFStatus/PDFError/PDFAttempts/PDFRenderedAt) may change; as_of/html_ref/
// input_fingerprint/template_version are immutable (spec §5 #36).
type ReportRevision struct {
	ID               int64
	ReportID         int64
	Revision         int32
	Status           ReportStatus
	HTMLRef          *int64
	PDFRef           *int64
	InputFingerprint string
	AsOf             time.Time
	TemplateVersion  string
	RenderStatus     string
	PDFStatus        PDFStatus
	PDFError         string
	PDFAttempts      int32
	PDFRenderedAt    *time.Time
	CreatedByRunID   *int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RenderableRevision is one published revision whose PDF is still pending or
// failed, as scanned by the P7 reconciler before ensuring a render job.
type RenderableRevision struct {
	RevisionID  int64
	ReportID    int64
	VersionID   int64
	Revision    int32
	PDFStatus   PDFStatus
	PDFAttempts int32
}

// ReportRef is one append-only logical reference of a report revision (spec
// §3.3).
type ReportRef struct {
	ID               int64
	ReportID         int64
	ReportRevisionID int64
	Kind             string
	RefID            int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// AnalysisRun is one deterministic or agent-assisted analysis row (spec §3.4).
// P4 writes only kind "triage".
type AnalysisRun struct {
	ID         int64
	VersionID  int64
	Kind       string
	Status     AnalysisStatus
	DedupeKey  string
	InputRef   json.RawMessage
	Output     json.RawMessage
	AgentRunID *int64
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
