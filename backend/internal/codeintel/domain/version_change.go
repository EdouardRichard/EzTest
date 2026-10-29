package domain

import "time"

// Version change snapshot rules (design spec §4.2/§7.2, D58): one row per
// (version_id, batch_id), summary items only. A snapshot never stores code
// content, diffs, prompts or model responses.

// VersionChangeSubject kinds persisted inside version_change.items (design spec
// §4.2). They mirror the AI fact kinds that describe a changed surface.
const (
	VersionChangeSubjectRoute     = "route"
	VersionChangeSubjectAPI       = "api"
	VersionChangeSubjectComponent = "component"
)

// VersionChangeKind is the semantic family of one changed file inside a batch
// snapshot. It is derived from the deterministic git status letter so the stored
// summary does not depend on git's rename/copy score suffixes.
const (
	VersionChangeAdded       = "added"
	VersionChangeModified    = "modified"
	VersionChangeDeleted     = "deleted"
	VersionChangeRenamed     = "renamed"
	VersionChangeCopied      = "copied"
	VersionChangeTypeChanged = "type_changed"
)

// BaseFallbackHistoryRewritten is the run_batch.base_fallback_reason value of an
// incremental batch whose parent head could not be used as the base (D49). The
// value is declared again in this context so the codeintel snapshot does not
// import another context's domain.
const BaseFallbackHistoryRewritten = "history_rewritten"

// VersionChangeSubject is one changed entity summary inside a snapshot item.
type VersionChangeSubject struct {
	Kind string
	Name string
}

// VersionChangeItem is the stored summary of one changed file: the file, its
// change kind, the entities that map to it and the affected case anchors. It
// deliberately carries no code excerpt, patch or prompt.
type VersionChangeItem struct {
	File       string
	ChangeKind string
	Subjects   []VersionChangeSubject
	CaseIDs    []string
	// Unmapped reports that no AI fact or code anchor covers the file (the P4
	// INV-8 unmapped rule). It is a boolean summary, not content.
	Unmapped bool
}

// VersionChange is one persisted per-batch code-change snapshot.
type VersionChange struct {
	ID               int64
	VersionID        int64
	BatchID          int64
	BaseCommit       string
	HeadCommit       string
	HistoryRewritten bool
	ChangedFiles     int
	ImpactedCases    int
	UnmappedCount    int
	Items            []VersionChangeItem
	ComputedAt       time.Time
	CreatedAt        time.Time
}

// VersionBatch is the read-only run_batch projection the snapshot generation
// needs: the batch identity, its commit range and its D49 base-fallback marker.
// The codeintel context never writes execution rows.
type VersionBatch struct {
	ID                 int64
	VersionID          int64
	BatchNo            int
	Status             string
	Kind               string
	ParentBatchID      *int64
	BaseFallbackReason string
	CodeBaselineCommit string
	HeadCommit         string
	CreatedAt          time.Time
	// HeadAnalysed reports that a terminal (done) codeintel analysis of the same
	// version targets this batch's head commit. The read-time snapshot
	// generation only persists a batch once its head is analysed (or it has no
	// changed files); a read before that returns a provisional, recomputable
	// snapshot instead of freezing an all-unmapped pre-analysis row.
	HeadAnalysed bool
}

// ChangeKindFromStatus maps one deterministic git status (A, M, D, R100, C75,
// T…) to the semantic change kind stored in a snapshot item. An unknown or empty
// status degrades to modified so the summary always has a value.
func ChangeKindFromStatus(status string) string {
	if status == "" {
		return VersionChangeModified
	}
	switch status[0] {
	case 'A':
		return VersionChangeAdded
	case 'D':
		return VersionChangeDeleted
	case 'R':
		return VersionChangeRenamed
	case 'C':
		return VersionChangeCopied
	case 'T':
		return VersionChangeTypeChanged
	default:
		return VersionChangeModified
	}
}
