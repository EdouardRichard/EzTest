// Package codeintelapi exposes the P5 code-intelligence HTTP API (spec §4.2).
// The server mounts these handlers behind the bearer-auth guard; handlers only
// parse DTOs, call the application services and map errors.
package codeintelapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// codeIndexRunDTO is CodeIndexRunView.
type codeIndexRunDTO struct {
	ID           string           `json:"id"`
	VersionID    *string          `json:"version_id"`
	RepoID       string           `json:"repo_id"`
	CommitSHA    string           `json:"commit_sha"`
	Branch       string           `json:"branch"`
	Source       string           `json:"source"`
	Status       string           `json:"status"`
	FilesTotal   int              `json:"files_total"`
	FilesIndexed int              `json:"files_indexed"`
	Truncated    bool             `json:"truncated"`
	Stats        map[string]any   `json:"stats"`
	Errors       []map[string]any `json:"errors"`
	StartedAt    *time.Time       `json:"started_at"`
	FinishedAt   *time.Time       `json:"finished_at"`
	CreatedBy    *string          `json:"created_by"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

func toIndexRunDTO(run codeintel.IndexRun) codeIndexRunDTO {
	view := codeIndexRunDTO{
		ID:           platformid.Format(run.ID),
		RepoID:       platformid.Format(run.RepoID),
		CommitSHA:    run.CommitSHA,
		Branch:       run.Branch,
		Source:       "git",
		Status:       run.Status,
		FilesTotal:   run.FilesTotal,
		FilesIndexed: run.FilesIndexed,
		Truncated:    run.Truncated,
		Stats:        decodeObject(run.Stats),
		Errors:       decodeObjectArray(run.Errors),
		StartedAt:    run.StartedAt,
		FinishedAt:   run.FinishedAt,
		CreatedAt:    run.CreatedAt,
		UpdatedAt:    run.UpdatedAt,
	}
	if run.VersionID > 0 {
		view.VersionID = stringPtr(platformid.Format(run.VersionID))
	}
	if run.CreatedBy != nil {
		view.CreatedBy = stringPtr(platformid.Format(*run.CreatedBy))
	}
	return view
}

// codeRepositoryDTO is the CodeRepository view nested in CodeRepoStatusView.
type codeRepositoryDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`
	Include   []string  `json:"include"`
	Exclude   []string  `json:"exclude"`
	CreatedBy string    `json:"created_by"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// codeRepoStatusDTO is CodeRepoStatusView.
type codeRepoStatusDTO struct {
	RepoID          string            `json:"repo_id"`
	Repository      codeRepositoryDTO `json:"repository"`
	LatestIndex     *codeIndexRunDTO  `json:"latest_index"`
	BaselineCommit  *string           `json:"baseline_commit"`
	IndexInProgress bool              `json:"index_in_progress"`
}

func toRepoStatusDTO(status codeintelapp.RepoStatus) codeRepoStatusDTO {
	view := codeRepoStatusDTO{
		RepoID:          platformid.Format(status.RepoID),
		Repository:      toRepositoryDTO(status.Repository),
		IndexInProgress: status.IndexInProgress,
	}
	if status.Latest != nil {
		latest := toIndexRunDTO(*status.Latest)
		view.LatestIndex = &latest
	}
	if status.BaselineCommit != "" {
		view.BaselineCommit = stringPtr(status.BaselineCommit)
	}
	return view
}

func toRepositoryDTO(repository codeintelapp.RepoRef) codeRepositoryDTO {
	include := repository.Include
	if include == nil {
		include = []string{}
	}
	exclude := repository.Exclude
	if exclude == nil {
		exclude = []string{}
	}
	return codeRepositoryDTO{
		ID:        platformid.Format(repository.ID),
		Name:      repository.Name,
		Path:      repository.Path,
		Branch:    repository.Branch,
		Include:   include,
		Exclude:   exclude,
		CreatedBy: platformid.Format(repository.CreatedBy),
		Status:    repository.Status,
		CreatedAt: repository.CreatedAt,
		UpdatedAt: repository.UpdatedAt,
	}
}

// codeTreeEntryDTO is CodeTreeEntryView. T0 materializes regular files only,
// so kind is always "file".
type codeTreeEntryDTO struct {
	Path      string  `json:"path"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	SizeBytes *int64  `json:"size_bytes"`
	BlobHash  *string `json:"blob_hash"`
}

func toTreeEntryDTO(entity codeintel.Entity) codeTreeEntryDTO {
	view := codeTreeEntryDTO{Path: entity.Path, Name: entity.Name, Kind: "file"}
	meta := decodeObject(entity.Meta)
	if rawSize, ok := meta["size_bytes"].(float64); ok && rawSize >= 0 {
		size := int64(rawSize)
		view.SizeBytes = &size
	}
	if entity.BlobHash != "" {
		view.BlobHash = stringPtr(entity.BlobHash)
	}
	return view
}

// codeFileWindowDTO is CodeFileWindowView.
type codeFileWindowDTO struct {
	RepoID     string `json:"repo_id"`
	CommitSHA  string `json:"commit_sha"`
	Path       string `json:"path"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
	Binary     bool   `json:"binary"`
	Content    string `json:"content"`
	SHA256     string `json:"sha256"`
}

func newFileWindowDTO(repoID int64, commit, path string, window codeintel.FileWindow, binary bool, content string) codeFileWindowDTO {
	view := codeFileWindowDTO{
		RepoID:     platformid.Format(repoID),
		CommitSHA:  commit,
		Path:       path,
		StartLine:  window.StartLine,
		EndLine:    window.EndLine,
		TotalLines: window.TotalLines,
		Truncated:  window.Truncated,
		Binary:     binary,
		Content:    content,
	}
	if view.StartLine < 1 {
		view.StartLine = 1
	}
	if view.EndLine < 1 {
		view.EndLine = view.StartLine
	}
	hash := sha256.Sum256([]byte(content))
	view.SHA256 = hex.EncodeToString(hash[:])
	return view
}

// codeChangedFileDTO is CodeChangedFileView.
type codeChangedFileDTO struct {
	Path         string  `json:"path"`
	OldPath      *string `json:"old_path"`
	Status       string  `json:"status"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	Binary       bool    `json:"binary"`
	HeadBlobHash *string `json:"head_blob_hash"`
}

func toChangedFileDTO(file codeintel.ChangedFile) codeChangedFileDTO {
	view := codeChangedFileDTO{
		Path:      file.Path,
		Status:    file.Status,
		Additions: file.Additions,
		Deletions: file.Deletions,
		Binary:    file.Binary,
	}
	if file.OldPath != "" {
		view.OldPath = stringPtr(file.OldPath)
	}
	if file.HeadBlobSHA != nil && *file.HeadBlobSHA != "" {
		view.HeadBlobHash = stringPtr(*file.HeadBlobSHA)
	}
	return view
}

// codeFileDiffDTO is CodeFileDiffView.
type codeFileDiffDTO struct {
	Path      string  `json:"path"`
	OldPath   *string `json:"old_path"`
	Status    string  `json:"status"`
	Additions int     `json:"additions"`
	Deletions int     `json:"deletions"`
	Binary    bool    `json:"binary"`
	Large     bool    `json:"large"`
	Deleted   bool    `json:"deleted"`
	Patch     *string `json:"patch"`
}

func toFileDiffDTO(fileDiff codeintel.FileDiff, changed codeintel.ChangedFile) codeFileDiffDTO {
	view := codeFileDiffDTO{
		Path:      fileDiff.Path,
		Status:    changed.Status,
		Additions: changed.Additions,
		Deletions: changed.Deletions,
		Binary:    fileDiff.Binary || changed.Binary,
		Large:     fileDiff.Truncated,
		Deleted:   fileDiff.Deleted || changed.Status == "deleted",
	}
	if changed.OldPath != "" {
		view.OldPath = stringPtr(changed.OldPath)
	}
	if !view.Binary && !view.Deleted && !view.Large {
		view.Patch = stringPtr(fileDiff.Patch)
	}
	return view
}

// codeCommitDTO is CodeCommitView.
type codeCommitDTO struct {
	SHA         string    `json:"sha"`
	Branch      *string   `json:"branch"`
	Message     string    `json:"message"`
	AuthorName  *string   `json:"author_name"`
	AuthoredAt  time.Time `json:"authored_at"`
	CommittedAt time.Time `json:"committed_at"`
	Parents     []string  `json:"parents"`
	IsHead      bool      `json:"is_head"`
}

func toCommitDTO(view codeintelapp.CommitView) codeCommitDTO {
	parents := view.Parents
	if parents == nil {
		parents = []string{}
	}
	commit := codeCommitDTO{
		SHA:         view.SHA,
		Message:     view.Message,
		AuthoredAt:  view.AuthoredAt,
		CommittedAt: view.CommittedAt,
		Parents:     parents,
		IsHead:      view.IsHead,
	}
	if view.Branch != "" {
		commit.Branch = stringPtr(view.Branch)
	}
	if view.AuthorName != "" {
		commit.AuthorName = stringPtr(view.AuthorName)
	}
	return commit
}

// codeFactDTO is CodeFactView.
type codeFactDTO struct {
	ID            string           `json:"id"`
	RepoID        string           `json:"repo_id"`
	CommitSHA     string           `json:"commit_sha"`
	FactKey       string           `json:"fact_key"`
	Version       int              `json:"version"`
	Kind          string           `json:"kind"`
	Path          string           `json:"path"`
	Name          string           `json:"name"`
	Signature     string           `json:"signature"`
	StartLine     *int             `json:"start_line"`
	EndLine       *int             `json:"end_line"`
	Body          map[string]any   `json:"body"`
	Evidence      []map[string]any `json:"evidence"`
	Source        string           `json:"source"`
	Confidence    string           `json:"confidence"`
	Model         string           `json:"model"`
	PromptVersion string           `json:"prompt_version"`
	AnalysisID    *string          `json:"analysis_id"`
	SupersededAt  *time.Time       `json:"superseded_at"`
	CreatedAt     time.Time        `json:"created_at"`
}

func toFactDTO(fact codeintel.Fact) codeFactDTO {
	view := codeFactDTO{
		ID:            platformid.Format(fact.ID),
		RepoID:        platformid.Format(fact.RepoID),
		CommitSHA:     fact.CommitSHA,
		FactKey:       fact.FactKey,
		Version:       fact.Version,
		Kind:          string(fact.Kind),
		Path:          fact.Path,
		Name:          fact.Name,
		Signature:     fact.Signature,
		StartLine:     fact.StartLine,
		EndLine:       fact.EndLine,
		Body:          decodeObject(fact.Body),
		Evidence:      decodeObjectArray(fact.Evidence),
		Source:        fact.Source,
		Confidence:    string(fact.Confidence),
		Model:         fact.Model,
		PromptVersion: fact.PromptVersion,
		SupersededAt:  fact.SupersededAt,
		CreatedAt:     fact.CreatedAt,
	}
	if fact.AnalysisID != nil {
		view.AnalysisID = stringPtr(platformid.Format(*fact.AnalysisID))
	}
	return view
}

// codeEntityDTO is CodeEntityView.
type codeEntityDTO struct {
	ID         string `json:"id"`
	RepoID     string `json:"repo_id"`
	CommitSHA  string `json:"commit_sha"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Signature  string `json:"signature"`
	StartLine  *int   `json:"start_line"`
	EndLine    *int   `json:"end_line"`
	Source     string `json:"source"`
	Confidence string `json:"confidence"`
}

func toEntityDTO(entity codeintel.Entity) codeEntityDTO {
	return codeEntityDTO{
		ID:         platformid.Format(entity.ID),
		RepoID:     platformid.Format(entity.RepoID),
		CommitSHA:  entity.CommitSHA,
		Kind:       string(entity.Kind),
		Path:       entity.Path,
		Name:       entity.Name,
		Signature:  entity.Signature,
		StartLine:  entity.StartLine,
		EndLine:    entity.EndLine,
		Source:     entity.Source,
		Confidence: string(entity.Confidence),
	}
}

// codeChangeSetDTO is CodeChangeSetView.
type codeChangeSetDTO struct {
	RepoID        string               `json:"repo_id"`
	BaseCommitSHA string               `json:"base_commit_sha"`
	HeadCommitSHA string               `json:"head_commit_sha"`
	Files         []codeChangedFileDTO `json:"files"`
	Stats         map[string]any       `json:"stats"`
	Truncated     bool                 `json:"truncated"`
}

func toChangeSetDTO(changeSet codeintelapp.ChangeSet) codeChangeSetDTO {
	files := make([]codeChangedFileDTO, 0, len(changeSet.Files))
	for _, file := range changeSet.Files {
		files = append(files, toChangedFileDTO(file))
	}
	return codeChangeSetDTO{
		RepoID:        platformid.Format(changeSet.RepoID),
		BaseCommitSHA: changeSet.BaseCommitSHA,
		HeadCommitSHA: changeSet.HeadCommitSHA,
		Files:         files,
		Stats:         map[string]any{"changedFiles": len(files), "truncated": changeSet.Truncated},
		Truncated:     changeSet.Truncated,
	}
}

// codeAnalysisDTO is CodeAnalysisView.
type codeAnalysisDTO struct {
	ID                string         `json:"id"`
	VersionID         string         `json:"version_id"`
	RepoID            string         `json:"repo_id"`
	BaseCommitSHA     string         `json:"base_commit_sha"`
	HeadCommitSHA     string         `json:"head_commit_sha"`
	Status            string         `json:"status"`
	Degraded          bool           `json:"degraded"`
	Model             string         `json:"model"`
	PromptVersion     string         `json:"prompt_version"`
	AnalysisVersion   int            `json:"analysis_version"`
	Stats             map[string]any `json:"stats"`
	ChangedFilesCount int            `json:"changed_files_count"`
	CandidatesCount   int            `json:"candidates_count"`
	SelectedCount     int            `json:"selected_count"`
	UnmappedCount     int            `json:"unmapped_count"`
	ErrorsCount       int            `json:"errors_count"`
	Error             *string        `json:"error"`
	CreatedAt         time.Time      `json:"created_at"`
	StartedAt         *time.Time     `json:"started_at"`
	FinishedAt        *time.Time     `json:"finished_at"`
}

func toAnalysisDTO(analysis codeintel.Analysis) codeAnalysisDTO {
	errorsList := decodeObjectArray(analysis.Errors)
	view := codeAnalysisDTO{
		ID:                platformid.Format(analysis.ID),
		VersionID:         platformid.Format(analysis.VersionID),
		RepoID:            platformid.Format(analysis.RepoID),
		BaseCommitSHA:     analysis.BaseCommitSHA,
		HeadCommitSHA:     analysis.HeadCommitSHA,
		Status:            string(analysis.Status),
		Degraded:          analysis.Degraded,
		Model:             analysis.Model,
		PromptVersion:     analysis.PromptVersion,
		AnalysisVersion:   analysis.AnalysisVersion,
		Stats:             decodeObject(analysis.Stats),
		ChangedFilesCount: len(decodeChangedFiles(analysis.ChangedFiles)),
		UnmappedCount:     len(decodeUnmapped(analysis.Unmapped)),
		ErrorsCount:       len(errorsList),
		CreatedAt:         analysis.CreatedAt,
		StartedAt:         analysis.StartedAt,
		FinishedAt:        analysis.FinishedAt,
	}
	if len(errorsList) > 0 {
		if message, ok := errorsList[0]["message"].(string); ok {
			view.Error = stringPtr(message)
		}
	}
	return view
}

// codeUnmappedDTO is one analysis unmapped entry.
type codeUnmappedDTO struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// codeCandidateDTO is CodeImpactCandidateView. Task 10 fills it; Task 8 emits an
// empty array so the detail shape stays stable.
type codeCandidateDTO struct {
	ID                 string           `json:"id"`
	AnalysisID         string           `json:"analysis_id"`
	RepoID             *string          `json:"repo_id"`
	VersionSUTID       *string          `json:"version_sut_id"`
	SubjectKind        string           `json:"subject_kind"`
	SubjectID          string           `json:"subject_id"`
	Reason             string           `json:"reason"`
	Confidence         string           `json:"confidence"`
	Source             string           `json:"source"`
	Evidence           []map[string]any `json:"evidence"`
	Selected           bool             `json:"selected"`
	SelectionUpdatedBy *string          `json:"selection_updated_by"`
	SelectionUpdatedAt *time.Time       `json:"selection_updated_at"`
	CreatedAt          time.Time        `json:"created_at"`
}

// codeAnalysisDetailDTO is CodeAnalysisDetailView.
type codeAnalysisDetailDTO struct {
	Analysis     codeAnalysisDTO      `json:"analysis"`
	ChangedFiles []codeChangedFileDTO `json:"changed_files"`
	Unmapped     []codeUnmappedDTO    `json:"unmapped"`
	Candidates   []codeCandidateDTO   `json:"candidates"`
	FactVersions map[string]int       `json:"fact_versions"`
	Errors       []map[string]any     `json:"errors"`
}

func toAnalysisDetailDTO(analysis codeintel.Analysis) codeAnalysisDetailDTO {
	return codeAnalysisDetailDTO{
		Analysis:     toAnalysisDTO(analysis),
		ChangedFiles: decodeChangedFiles(analysis.ChangedFiles),
		Unmapped:     decodeUnmapped(analysis.Unmapped),
		Candidates:   []codeCandidateDTO{},
		FactVersions: decodeFactVersions(analysis.FactVersions),
		Errors:       decodeObjectArray(analysis.Errors),
	}
}

// --- JSONB decoding helpers ------------------------------------------------------

func decodeObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return map[string]any{}
	}
	return value
}

func decodeObjectArray(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return []map[string]any{}
	}
	var value []map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return []map[string]any{}
	}
	return value
}

func decodeChangedFiles(raw json.RawMessage) []codeChangedFileDTO {
	if len(raw) == 0 {
		return []codeChangedFileDTO{}
	}
	var value []codeChangedFileDTO
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return []codeChangedFileDTO{}
	}
	return value
}

func decodeUnmapped(raw json.RawMessage) []codeUnmappedDTO {
	if len(raw) == 0 {
		return []codeUnmappedDTO{}
	}
	var value []codeUnmappedDTO
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return []codeUnmappedDTO{}
	}
	return value
}

func decodeFactVersions(raw json.RawMessage) map[string]int {
	if len(raw) == 0 {
		return map[string]int{}
	}
	var value map[string]int
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return map[string]int{}
	}
	return value
}

func stringPtr(value string) *string { return &value }
