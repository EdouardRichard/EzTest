package httpapi

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// The DTO structures below are the wire contract of design spec §7.2/§7.3 and
// frontend/src/features/code-intel/types.ts (lead rulings R6/R7). Codeintel-owned
// ids are decimal strings; repoId/createdBy stay numbers; timestamps are ISO
// strings (null for the optional run timestamps).

// RepoSummaryDTO is a P2 code repository plus its latest index-run projection.
// Index is null when the repository has never been indexed.
type RepoSummaryDTO struct {
	ID            int64        `json:"id"`
	Key           string       `json:"key"`
	Name          string       `json:"name"`
	Status        string       `json:"status"`
	FixedBranch   string       `json:"fixedBranch"`
	LastCommit    string       `json:"lastCommit"`
	LastIndexedAt *time.Time   `json:"lastIndexedAt"`
	Index         *IndexRunDTO `json:"index"`
}

// IndexRunStatsDTO is the files/truncated projection of one index run.
type IndexRunStatsDTO struct {
	Files     int  `json:"files"`
	Truncated bool `json:"truncated"`
}

// IndexRunDTO is one T0 repository index run. runId is a decimal string;
// repoId/createdBy are numbers.
type IndexRunDTO struct {
	RunID      string             `json:"runId"`
	RepoID     int64              `json:"repoId"`
	Commit     string             `json:"commit"`
	Source     string             `json:"source"`
	Status     string             `json:"status"`
	Stats      *IndexRunStatsDTO  `json:"stats"`
	Errors     []AnalysisErrorDTO `json:"errors"`
	StartedAt  *time.Time         `json:"startedAt"`
	FinishedAt *time.Time         `json:"finishedAt"`
	CreatedBy  int64              `json:"createdBy"`
	CreatedAt  *time.Time         `json:"createdAt,omitempty"`
}

// IndexRunAcceptedDTO is the 202 body of POST /api/code/repos/{repoId}/index.
type IndexRunAcceptedDTO struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Commit string `json:"commit"`
}

// AnalysisErrorDTO is one non-fatal run error (code/path/message only; never
// code content).
type AnalysisErrorDTO struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// TreeEntryDTO is one entry of a commit file tree.
type TreeEntryDTO struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Size        int64  `json:"size"`
	GitObjectID string `json:"gitObjectId"`
	Mode        string `json:"mode"`
	Unsupported bool   `json:"unsupported"`
}

// TreePageDTO is one tree page's entry array; the shared page envelope supplies
// next_cursor/has_more (frontend apiGetPage reads data as the array).
type TreePageDTO []TreeEntryDTO

// FileWindowDTO is one 1-based line window of a commit-pinned file.
type FileWindowDTO struct {
	Commit     string `json:"commit"`
	Path       string `json:"path"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	TotalLines int    `json:"totalLines"`
	Truncated  bool   `json:"truncated"`
	Content    string `json:"content"`
}

// FileDiffDTO is the unified diff of one changed file. Binary/oversized files
// still return 200 with an empty patch (R18).
type FileDiffDTO struct {
	BaseCommit string `json:"baseCommit"`
	HeadCommit string `json:"headCommit"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Binary     bool   `json:"binary"`
	Truncated  bool   `json:"truncated"`
	Patch      string `json:"patch"`
}

// ChangeSetStatsDTO is the aggregate counter of a change list. The gitx stats
// document names the file count changedFiles; the wire contract is files.
type ChangeSetStatsDTO struct {
	Files     int  `json:"files"`
	Additions int  `json:"additions"`
	Deletions int  `json:"deletions"`
	Truncated bool `json:"truncated"`
}

// ChangedFileDTO is one file-level change inside a change list.
type ChangedFileDTO struct {
	Path         string  `json:"path"`
	OldPath      *string `json:"oldPath"`
	Status       string  `json:"status"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	Binary       bool    `json:"binary"`
	HeadBlobHash string  `json:"headBlobHash"`
}

// ChangeListDTO is the change-list projection returned inside the data field by
// /api/code/repos/{repoId}/diff and (T15) /api/versions/{id}/code/changes
// (ruling R7: not wrapped in the generic page envelope). ChangeSetID is null for
// a read-only preview that never persisted the deterministic change set.
type ChangeListDTO struct {
	ChangeSetID *string           `json:"changeSetId"`
	BaseCommit  string            `json:"baseCommit"`
	HeadCommit  string            `json:"headCommit"`
	Stats       ChangeSetStatsDTO `json:"stats"`
	Files       []ChangedFileDTO  `json:"files"`
	NextCursor  *string           `json:"nextCursor"`
	HasMore     bool              `json:"hasMore"`
}

// CodeEntityDTO is one file/AI fact entity returned by the facts and search
// reads (R6). id/analysisId are decimal strings, repoId a number, evidence keeps
// the camelCase evidence range with the optional excerpt hash.
type CodeEntityDTO struct {
	ID          string            `json:"id"`
	RepoID      int64             `json:"repoId"`
	Commit      string            `json:"commit"`
	Kind        string            `json:"kind"`
	Path        string            `json:"path"`
	Name        string            `json:"name"`
	StartLine   *int              `json:"startLine"`
	EndLine     *int              `json:"endLine"`
	Signature   string            `json:"signature"`
	BlobHash    string            `json:"blobHash"`
	Source      string            `json:"source"`
	Confidence  string            `json:"confidence"`
	Evidence    []domain.Evidence `json:"evidence"`
	FactKey     *string           `json:"factKey"`
	FactVersion *int              `json:"factVersion"`
	AnalysisID  *string           `json:"analysisId"`
	CreatedAt   *time.Time        `json:"createdAt,omitempty"`
	UpdatedAt   *time.Time        `json:"updatedAt,omitempty"`
}

// FactsPageDTO is one facts/search page's entity array; the shared page envelope
// supplies next_cursor/has_more.
type FactsPageDTO []CodeEntityDTO

// CommitDTO is the read-only commit metadata of GET /commits/{sha}.
type CommitDTO struct {
	SHA         string    `json:"sha"`
	Message     string    `json:"message"`
	Author      string    `json:"author"`
	CommittedAt time.Time `json:"committedAt"`
}

// StartAnalysisDTO is the body of POST /api/versions/{id}/code/analyses (design
// spec §7.3): 202 with reused=false for a new run, 200 with reused=true for a
// reused one.
type StartAnalysisDTO struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	AnalysisVersion int    `json:"analysisVersion"`
	Reused          bool   `json:"reused"`
}

// AnalysisStatsDTO is the projected JSONB stats document of one analysis
// (frontend types.ts AnalysisStats). It is always an object, even when the run
// has not produced stats yet (R8).
type AnalysisStatsDTO struct {
	ChangedFiles       int  `json:"changedFiles"`
	AnalyzedFiles      int  `json:"analyzedFiles"`
	CacheHits          int  `json:"cacheHits"`
	Facts              int  `json:"facts"`
	Edges              int  `json:"edges"`
	Contracts          int  `json:"contracts"`
	EvidenceChecks     int  `json:"evidenceChecks"`
	EvidenceFailures   int  `json:"evidenceFailures"`
	Unmapped           int  `json:"unmapped"`
	InvalidFacts       int  `json:"invalidFacts"`
	DuplicateFacts     int  `json:"duplicateFacts"`
	UnmatchedContracts int  `json:"unmatchedContracts"`
	Truncated          bool `json:"truncated"`
	Skipped            int  `json:"skipped"`
}

// FactVersionDTO is one entry of an analysis' fact-version snapshot.
type FactVersionDTO struct {
	FactKey string `json:"factKey"`
	Version int    `json:"version"`
}

// UnmappedChangeDTO is one changed file no candidate covers (design spec §7.3).
type UnmappedChangeDTO struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Suggestion string `json:"suggestion"`
}

// CandidateDTO is one impact candidate (design spec §7.3). id and subjectId are
// decimal strings; selectionUpdatedBy stays a number and selectionUpdatedAt an
// ISO timestamp. Evidence is always an array: T1/AI evidence carries a verified
// line range plus excerpt hash, anchor evidence only file.
type CandidateDTO struct {
	ID                 string            `json:"id"`
	SubjectKind        string            `json:"subjectKind"`
	SubjectID          string            `json:"subjectId"`
	Reason             string            `json:"reason"`
	Confidence         string            `json:"confidence"`
	Source             string            `json:"source"`
	Evidence           []domain.Evidence `json:"evidence"`
	Selected           bool              `json:"selected"`
	SelectionUpdatedBy *int64            `json:"selectionUpdatedBy,omitempty"`
	SelectionUpdatedAt *time.Time        `json:"selectionUpdatedAt,omitempty"`
}

// AnalysisDTO is the aggregated analysis detail of GET latest/{analysisId}
// (design spec §7.3, frontend types.ts Analysis). The four aggregate arrays are
// always [] rather than null (R8); changeSummary is projected out of the stats
// document (R28); stats is always an object.
type AnalysisDTO struct {
	ID              string              `json:"id"`
	VersionID       string              `json:"versionId"`
	RepoID          int64               `json:"repoId"`
	BaseCommit      string              `json:"baseCommit"`
	HeadCommit      string              `json:"headCommit"`
	Status          string              `json:"status"`
	Degraded        bool                `json:"degraded"`
	AnalysisVersion int                 `json:"analysisVersion"`
	Model           string              `json:"model"`
	PromptVersion   string              `json:"promptVersion"`
	ChangeSetID     *string             `json:"changeSetId"`
	ChangeSummary   string              `json:"changeSummary"`
	Stats           AnalysisStatsDTO    `json:"stats"`
	FactVersions    []FactVersionDTO    `json:"factVersions"`
	Candidates      []CandidateDTO      `json:"candidates"`
	Unmapped        []UnmappedChangeDTO `json:"unmapped"`
	Errors          []AnalysisErrorDTO  `json:"errors"`
	CreatedBy       int64               `json:"createdBy"`
	CreatedAt       time.Time           `json:"createdAt"`
	StartedAt       *time.Time          `json:"startedAt"`
	FinishedAt      *time.Time          `json:"finishedAt"`
}

// AnchorDTO is one code anchor (design spec §7.3, frontend types.ts Anchor):
// anchor/version/subject ids are decimal strings, createdBy stays a number and
// the location fields are nullable.
type AnchorDTO struct {
	ID            string    `json:"id"`
	VersionID     string    `json:"versionId"`
	SubjectKind   string    `json:"subjectKind"`
	SubjectID     string    `json:"subjectId"`
	FilePath      string    `json:"filePath"`
	StartLine     *int      `json:"startLine"`
	EndLine       *int      `json:"endLine"`
	CommitSHA     *string   `json:"commitSha"`
	Confidence    string    `json:"confidence"`
	CreatedByKind string    `json:"createdByKind"`
	CreatedBy     int64     `json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
}

// startAnalysisRequest is the POST /analyses body (design spec §7.3). Both
// fields are optional, so an empty object is a valid trigger.
type startAnalysisRequest struct {
	HeadCommit string `json:"headCommit"`
	Force      bool   `json:"force"`
}

// patchCandidateRequest is the PATCH candidate body (design spec §7.3): selected
// is required, so it is a pointer here and a missing value is a 400.
type patchCandidateRequest struct {
	Selected *bool `json:"selected"`
}

// createAnchorRequest is the POST /anchors body (design spec §7.3). subjectId is
// a decimal string on the wire; the handler parses it to the domain's int64.
type createAnchorRequest struct {
	SubjectKind string `json:"subjectKind"`
	SubjectID   string `json:"subjectId"`
	FilePath    string `json:"filePath"`
	StartLine   *int   `json:"startLine"`
	EndLine     *int   `json:"endLine"`
}

// indexRunStatsDocument is the stored JSONB stats document of an index run. Only
// the projected keys are read; unknown keys are ignored.
type indexRunStatsDocument struct {
	Files     int  `json:"files"`
	Truncated bool `json:"truncated"`
}

// changeSetStatsDocument is the stored JSONB stats document of a change set
// (gitx writes changedFiles; the API translates it to files).
type changeSetStatsDocument struct {
	ChangedFiles int  `json:"changedFiles"`
	Additions    int  `json:"additions"`
	Deletions    int  `json:"deletions"`
	Truncated    bool `json:"truncated"`
}

// formatID renders one codeintel id as the decimal string of the wire contract.
func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// formatOptionalID renders an optional codeintel id as a nullable decimal
// string.
func formatOptionalID(id *int64) *string {
	if id == nil {
		return nil
	}
	formatted := formatID(*id)
	return &formatted
}

// newRepoSummaryDTO maps the P2 repository projection; the caller attaches the
// latest index run and the last indexed commit.
func newRepoSummaryDTO(info application.RepoInfo) RepoSummaryDTO {
	return RepoSummaryDTO{
		ID:            info.ID,
		Key:           info.Key,
		Name:          info.Name,
		Status:        info.Status,
		FixedBranch:   info.FixedBranch,
		LastIndexedAt: info.LastIndexedAt,
	}
}

// newIndexRunDTO maps one index run to its wire shape. A malformed stored stats
// document is ignored rather than failing the read (the frontend renders an
// absent stats block); the DTO never carries repository paths or code content.
func newIndexRunDTO(run domain.IndexRun) IndexRunDTO {
	dto := IndexRunDTO{
		RunID:      formatID(run.ID),
		RepoID:     run.RepoID,
		Commit:     run.CommitSHA,
		Source:     run.Source,
		Status:     run.Status,
		Errors:     newAnalysisErrorDTOs(run.Errors),
		StartedAt:  run.StartedAt,
		FinishedAt: run.FinishedAt,
		CreatedBy:  run.CreatedBy,
	}
	if !run.CreatedAt.IsZero() {
		createdAt := run.CreatedAt
		dto.CreatedAt = &createdAt
	}
	if len(run.Stats) > 0 {
		var document indexRunStatsDocument
		if err := json.Unmarshal(run.Stats, &document); err == nil {
			dto.Stats = &IndexRunStatsDTO{Files: document.Files, Truncated: document.Truncated}
		}
	}
	return dto
}

// newAnalysisErrorDTOs maps run errors, always returning a non-nil slice so the
// JSON is [] rather than null.
func newAnalysisErrorDTOs(items []domain.AnalysisError) []AnalysisErrorDTO {
	dtos := make([]AnalysisErrorDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, AnalysisErrorDTO{Code: item.Code, Path: item.Path, Message: item.Message})
	}
	return dtos
}

// newTreePageDTO maps one tree page's entries, always returning a non-nil slice.
func newTreePageDTO(page domain.TreePage) TreePageDTO {
	entries := make(TreePageDTO, 0, len(page.Entries))
	for _, entry := range page.Entries {
		entries = append(entries, TreeEntryDTO{
			Path:        entry.Path,
			Type:        entry.Type,
			Size:        entry.Size,
			GitObjectID: entry.GitObjectID,
			Mode:        entry.Mode,
			Unsupported: entry.Unsupported,
		})
	}
	return entries
}

// newFileWindowDTO maps one file line window.
func newFileWindowDTO(window domain.FileWindow) FileWindowDTO {
	return FileWindowDTO{
		Commit:     window.Commit,
		Path:       window.Path,
		StartLine:  window.StartLine,
		EndLine:    window.EndLine,
		TotalLines: window.TotalLines,
		Truncated:  window.Truncated,
		Content:    window.Content,
	}
}

// newFileDiffDTO maps one unified file diff.
func newFileDiffDTO(diff domain.FileDiff) FileDiffDTO {
	return FileDiffDTO{
		BaseCommit: diff.BaseCommit,
		HeadCommit: diff.HeadCommit,
		Path:       diff.Path,
		Status:     diff.Status,
		Binary:     diff.Binary,
		Truncated:  diff.Truncated,
		Patch:      diff.Patch,
	}
}

// newChangeListDTO maps one change-set page. It translates the gitx
// changedFiles stats key to files and derives the cursor/hasMore from the page
// contract (R7): hasMore is len(files)==limit, not the change-set truncation
// flag; the cursor is the last path of the page when another page may follow.
func newChangeListDTO(data domain.ChangeSetData, base, head string, limit int) (ChangeListDTO, error) {
	var document changeSetStatsDocument
	if len(data.Stats) > 0 {
		if err := json.Unmarshal(data.Stats, &document); err != nil {
			return ChangeListDTO{}, fmt.Errorf("codeintel: decode change set stats: %w", err)
		}
	}
	files := make([]ChangedFileDTO, 0, len(data.Files))
	for _, file := range data.Files {
		files = append(files, ChangedFileDTO{
			Path:         file.Path,
			OldPath:      file.OldPath,
			Status:       file.Status,
			Additions:    file.Additions,
			Deletions:    file.Deletions,
			Binary:       file.Binary,
			HeadBlobHash: file.HeadBlobHash,
		})
	}
	fileCount := document.ChangedFiles
	if len(data.Stats) == 0 {
		// A change set is always produced with stats; keep the projection
		// sensible for a partially wired/fake service instead of reporting 0.
		fileCount = len(files)
	}
	dto := ChangeListDTO{
		ChangeSetID: formatOptionalID(data.ChangeSetID),
		BaseCommit:  base,
		HeadCommit:  head,
		Stats: ChangeSetStatsDTO{
			Files:     fileCount,
			Additions: document.Additions,
			Deletions: document.Deletions,
			Truncated: document.Truncated,
		},
		Files: files,
	}
	if limit > 0 && len(files) == limit {
		dto.HasMore = true
		cursor := files[len(files)-1].Path
		dto.NextCursor = &cursor
	}
	return dto, nil
}

// newCodeEntityDTO maps one entity. Evidence is always a non-nil array and the
// optional timestamps stay absent when zero.
func newCodeEntityDTO(entity domain.CodeEntity) CodeEntityDTO {
	evidence := entity.Evidence
	if evidence == nil {
		evidence = []domain.Evidence{}
	}
	dto := CodeEntityDTO{
		ID:          formatID(entity.ID),
		RepoID:      entity.RepoID,
		Commit:      entity.Commit,
		Kind:        entity.Kind,
		Path:        entity.Path,
		Name:        entity.Name,
		StartLine:   entity.StartLine,
		EndLine:     entity.EndLine,
		Signature:   entity.Signature,
		BlobHash:    entity.BlobHash,
		Source:      entity.Source,
		Confidence:  entity.Confidence,
		Evidence:    evidence,
		FactKey:     entity.FactKey,
		FactVersion: entity.FactVersion,
		AnalysisID:  formatOptionalID(entity.AnalysisID),
	}
	if !entity.CreatedAt.IsZero() {
		createdAt := entity.CreatedAt
		dto.CreatedAt = &createdAt
	}
	if !entity.UpdatedAt.IsZero() {
		updatedAt := entity.UpdatedAt
		dto.UpdatedAt = &updatedAt
	}
	return dto
}

// newFactsPageDTO maps an entity page, always returning a non-nil slice.
func newFactsPageDTO(entities []domain.CodeEntity) FactsPageDTO {
	dtos := make(FactsPageDTO, 0, len(entities))
	for _, entity := range entities {
		dtos = append(dtos, newCodeEntityDTO(entity))
	}
	return dtos
}

// newCommitDTO maps read-only commit metadata.
func newCommitDTO(info domain.CommitInfo) CommitDTO {
	return CommitDTO{
		SHA:         info.SHA,
		Message:     info.Message,
		Author:      info.Author,
		CommittedAt: info.CommittedAt,
	}
}

// analysisStatsDocument is the stored JSONB stats document of one analysis. The
// wire-projected counters are decoded through the embedded DTO; changeSummary
// lives in the same document (R28) and is projected separately.
type analysisStatsDocument struct {
	AnalysisStatsDTO
	ChangeSummary string `json:"changeSummary"`
}

// newAnalysisDTO maps one analysis plus its persisted candidates to the wire
// shape. A malformed or absent stored stats document is rendered as an empty
// object instead of failing the read (it can only be produced by a bug, and R8
// requires stats to stay an object); the four aggregate arrays are always
// non-nil so the JSON is [] rather than null.
func newAnalysisDTO(analysis domain.CodeAnalysis, candidates []domain.ImpactCandidate) AnalysisDTO {
	var document analysisStatsDocument
	if len(analysis.Stats) > 0 {
		_ = json.Unmarshal(analysis.Stats, &document)
	}
	return AnalysisDTO{
		ID:              formatID(analysis.ID),
		VersionID:       formatID(analysis.VersionID),
		RepoID:          analysis.RepoID,
		BaseCommit:      analysis.BaseCommit,
		HeadCommit:      analysis.HeadCommit,
		Status:          analysis.Status,
		Degraded:        analysis.Degraded,
		AnalysisVersion: analysis.AnalysisVersion,
		Model:           analysis.Model,
		PromptVersion:   analysis.PromptVersion,
		ChangeSetID:     formatOptionalID(analysis.ChangeSetID),
		ChangeSummary:   document.ChangeSummary,
		Stats:           document.AnalysisStatsDTO,
		FactVersions:    newFactVersionDTOs(analysis.FactVersions),
		Candidates:      newCandidateDTOs(candidates),
		Unmapped:        newUnmappedChangeDTOs(analysis.Unmapped),
		Errors:          newAnalysisErrorDTOs(analysis.Errors),
		CreatedBy:       analysis.CreatedBy,
		CreatedAt:       analysis.CreatedAt,
		StartedAt:       analysis.StartedAt,
		FinishedAt:      analysis.FinishedAt,
	}
}

// newFactVersionDTOs maps the fact-version snapshot, always returning a non-nil
// slice.
func newFactVersionDTOs(items []domain.FactVersionRef) []FactVersionDTO {
	dtos := make([]FactVersionDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, FactVersionDTO{FactKey: item.FactKey, Version: item.Version})
	}
	return dtos
}

// newUnmappedChangeDTOs maps the unmapped changes, always returning a non-nil
// slice.
func newUnmappedChangeDTOs(items []domain.UnmappedChange) []UnmappedChangeDTO {
	dtos := make([]UnmappedChangeDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, UnmappedChangeDTO{
			Path:       item.Path,
			Status:     item.Status,
			Reason:     item.Reason,
			Suggestion: item.Suggestion,
		})
	}
	return dtos
}

// newCandidateDTOs maps the candidates, always returning a non-nil slice.
func newCandidateDTOs(items []domain.ImpactCandidate) []CandidateDTO {
	dtos := make([]CandidateDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, newCandidateDTO(item))
	}
	return dtos
}

// newCandidateDTO maps one candidate. Evidence keeps the domain JSON shape
// (file plus the optional verified range/hash) and is never null.
func newCandidateDTO(candidate domain.ImpactCandidate) CandidateDTO {
	evidence := candidate.Evidence
	if evidence == nil {
		evidence = []domain.Evidence{}
	}
	return CandidateDTO{
		ID:                 formatID(candidate.ID),
		SubjectKind:        candidate.SubjectKind,
		SubjectID:          formatID(candidate.SubjectID),
		Reason:             candidate.Reason,
		Confidence:         candidate.Confidence,
		Source:             candidate.Source,
		Evidence:           evidence,
		Selected:           candidate.Selected,
		SelectionUpdatedBy: candidate.SelectionUpdatedBy,
		SelectionUpdatedAt: candidate.SelectionUpdatedAt,
	}
}

// newAnchorDTO maps one anchor to its wire shape (decimal-string ids).
func newAnchorDTO(anchor domain.CodeAnchor) AnchorDTO {
	return AnchorDTO{
		ID:            formatID(anchor.ID),
		VersionID:     formatID(anchor.VersionID),
		SubjectKind:   anchor.SubjectKind,
		SubjectID:     formatID(anchor.SubjectID),
		FilePath:      anchor.FilePath,
		StartLine:     anchor.StartLine,
		EndLine:       anchor.EndLine,
		CommitSHA:     anchor.CommitSHA,
		Confidence:    anchor.Confidence,
		CreatedByKind: anchor.CreatedByKind,
		CreatedBy:     anchor.CreatedBy,
		CreatedAt:     anchor.CreatedAt,
	}
}

// newAnchorDTOs maps the anchor list, always returning a non-nil slice.
func newAnchorDTOs(items []domain.CodeAnchor) []AnchorDTO {
	dtos := make([]AnchorDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, newAnchorDTO(item))
	}
	return dtos
}
