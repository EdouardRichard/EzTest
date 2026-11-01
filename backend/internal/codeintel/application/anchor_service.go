package codeintelapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// AnchorRepository persists code_anchor rows. Create returns
// codeintel.ErrConflict when the subject point already exists; Get returns
// codeintel.ErrNotFound for a missing row.
type AnchorRepository interface {
	Create(ctx context.Context, anchor codeintel.Anchor) (codeintel.Anchor, error)
	List(ctx context.Context, versionID int64, subjectKind string, subjectID int64) ([]codeintel.Anchor, error)
	Get(ctx context.Context, id int64) (codeintel.Anchor, error)
	UpdateStatus(ctx context.Context, id int64, status string, validatedAt time.Time) (codeintel.Anchor, error)
	Delete(ctx context.Context, id int64) error
}

// AnchorSubjectReader validates that an anchor subject belongs to a version
// (spec §6.1). The design and delivery contexts implement it; the empty subject
// reader keeps the pure-version subject usable in tests and read-only setups.
type AnchorSubjectReader interface {
	RequirementInVersion(ctx context.Context, versionID, requirementID int64) (bool, error)
	RequirementVersionInVersion(ctx context.Context, versionID, requirementVersionID int64) (bool, error)
	CaseVersionInVersion(ctx context.Context, versionID, caseVersionID int64) (bool, error)
	IssueInVersion(ctx context.Context, versionID, issueID int64) (bool, error)
}

// AnchorDeps is the anchor service wiring. Runs, Subjects and Settings are
// optional: without Runs the latest done index commit is skipped, without
// Subjects only the version subject can be validated, and without Settings the
// hard excerpt-line limit applies.
type AnchorDeps struct {
	Anchors   AnchorRepository
	Git       GitClient
	Versions  VersionReader
	Resources ResourceReader
	Runs      IndexRepository
	Subjects  AnchorSubjectReader
	Events    EventPublisher
	Audit     AuditRecorder
	Settings  SettingsReader
	Clock     Clock
	IDs       IDGenerator
}

// AnchorService owns the unified code anchors of one version (spec §6.1):
// create with server-side path/line/excerpt validation, list, re-validate
// (valid/stale/broken) and delete. Anchors are provenance, so validation never
// deletes a stale/broken row.
type AnchorService struct {
	anchors   AnchorRepository
	git       GitClient
	versions  VersionReader
	resources ResourceReader
	runs      IndexRepository
	subjects  AnchorSubjectReader
	events    EventPublisher
	audit     AuditRecorder
	settings  SettingsReader
	clock     Clock
	ids       IDGenerator
}

// NewAnchorService wires the anchor service with test-friendly defaults.
func NewAnchorService(deps AnchorDeps) *AnchorService {
	return &AnchorService{
		anchors:   deps.Anchors,
		git:       deps.Git,
		versions:  deps.Versions,
		resources: deps.Resources,
		runs:      deps.Runs,
		subjects:  deps.Subjects,
		events:    deps.Events,
		audit:     deps.Audit,
		settings:  deps.Settings,
		clock:     clockOrSystem(deps.Clock),
		ids:       idsOrFallback(deps.IDs),
	}
}

// CreateAnchorInput is the POST /code/anchors payload (spec §4.2 #19). Line
// numbers are optional but must be supplied together; CommitSHA is optional and
// defaults to the version baseline, the latest done index commit or the branch
// head in that order.
type CreateAnchorInput struct {
	VersionID     int64
	SubjectKind   codeintel.AnchorSubject
	SubjectID     int64
	RepoID        int64
	FilePath      string
	StartLine     *int
	EndLine       *int
	ExcerptSHA    string
	CommitSHA     string
	CreatedByKind string
	CreatedBy     *int64
}

// Create validates the subject ownership, repository binding, file/line range
// and excerpt hash, then stores one valid anchor. A repeated subject point maps
// to 409 anchor_duplicate.
func (s *AnchorService) Create(ctx context.Context, in CreateAnchorInput) (codeintel.Anchor, error) {
	subjectKind := codeintel.AnchorSubject(strings.TrimSpace(string(in.SubjectKind)))
	if !subjectKind.Valid() || in.SubjectID <= 0 {
		return codeintel.Anchor{}, unprocessable("invalid_subject", "subject kind or id is not valid")
	}
	if in.VersionID <= 0 {
		return codeintel.Anchor{}, platform.NotFound("version")
	}
	bindings, err := s.versions.VersionSUTBindings(ctx, in.VersionID)
	if err != nil {
		return codeintel.Anchor{}, err
	}
	binding, bound := bindingForRepo(bindings, in.RepoID)
	if !bound {
		return codeintel.Anchor{}, unprocessable("repo_not_bound", "repository is not bound to this version")
	}
	if err := s.checkSubject(ctx, in.VersionID, subjectKind, in.SubjectID); err != nil {
		return codeintel.Anchor{}, err
	}
	repository, err := s.resources.CodeRepository(ctx, in.RepoID)
	if err != nil {
		return codeintel.Anchor{}, mapDomainError(err, "code repository")
	}
	filePath, err := codeintel.NormalizeRepoPath(in.FilePath)
	if err != nil {
		return codeintel.Anchor{}, unprocessable("path_escape", "path must be a normalized repo-relative path")
	}

	commit, err := s.targetCommit(ctx, binding, repository, in.CommitSHA)
	if err != nil {
		return codeintel.Anchor{}, err
	}
	anchor := codeintel.Anchor{
		ID:            s.ids.Next(),
		VersionID:     in.VersionID,
		RepoID:        in.RepoID,
		SubjectKind:   subjectKind,
		SubjectID:     in.SubjectID,
		FilePath:      filePath,
		CommitSHA:     commit,
		Confidence:    string(codeintel.ConfidenceHigh),
		CreatedByKind: "user",
		CreatedBy:     in.CreatedBy,
	}
	if in.CreatedByKind != "" {
		anchor.CreatedByKind = in.CreatedByKind
	}
	if anchor.CreatedBy == nil {
		if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
			actorID := actor.ID
			anchor.CreatedBy = &actorID
		}
	}

	window, err := s.readWindow(ctx, repository.Path, commit, filePath, in.StartLine, in.EndLine)
	if err != nil {
		return codeintel.Anchor{}, err
	}
	if in.StartLine != nil || in.EndLine != nil {
		if in.StartLine == nil || in.EndLine == nil {
			return codeintel.Anchor{}, unprocessable("invalid_range", "startLine and endLine must be supplied together")
		}
		if err := validateAnchorRange(*in.StartLine, *in.EndLine, window.TotalLines); err != nil {
			return codeintel.Anchor{}, err
		}
		maxLines := readIntSetting(ctx, s.settings, SettingMaxExcerptLines, defaultMaxExcerptLines, codeintel.MaxExcerptLines)
		if *in.EndLine-*in.StartLine+1 > maxLines {
			return codeintel.Anchor{}, unprocessable("invalid_range", "excerpt exceeds the configured line limit")
		}
		lines, linesErr := excerptLines(window, *in.StartLine, *in.EndLine)
		if linesErr != nil {
			return codeintel.Anchor{}, linesErr
		}
		computed := codeintel.ExcerptHash(lines)
		if provided := strings.TrimSpace(in.ExcerptSHA); provided != "" {
			if !validExcerptSHA(provided) || provided != computed {
				return codeintel.Anchor{}, unprocessable("invalid_excerpt", "excerpt hash does not match the commit content")
			}
		}
		anchor.StartLine = in.StartLine
		anchor.EndLine = in.EndLine
		anchor.ExcerptSHA = computed
	} else if strings.TrimSpace(in.ExcerptSHA) != "" {
		return codeintel.Anchor{}, unprocessable("invalid_excerpt", "an excerpt hash requires a line range")
	}

	now := s.clock.Now()
	anchor.Status = string(codeintel.AnchorValid)
	anchor.LastValidatedAt = &now
	anchor.CreatedAt = now
	anchor.UpdatedAt = now
	created, err := s.anchors.Create(ctx, anchor)
	if err != nil {
		if errors.Is(err, codeintel.ErrConflict) {
			return codeintel.Anchor{}, duplicateAnchorError()
		}
		return codeintel.Anchor{}, mapDomainError(err, "code anchor")
	}
	s.recordAudit(ctx, "code.anchor.created", platformid.Format(created.ID), map[string]any{
		"version_id":   platformid.Format(created.VersionID),
		"repo_id":      platformid.Format(created.RepoID),
		"subject_kind": string(created.SubjectKind),
		"subject_id":   platformid.Format(created.SubjectID),
		"file_path":    created.FilePath,
		"start_line":   lineValue(created.StartLine),
		"end_line":     lineValue(created.EndLine),
	})
	return created, nil
}

// List returns the anchors of a version, optionally filtered by subject.
func (s *AnchorService) List(ctx context.Context, versionID int64, subjectKind string, subjectID int64) ([]codeintel.Anchor, error) {
	kind := strings.TrimSpace(subjectKind)
	if kind != "" && !codeintel.AnchorSubject(kind).Valid() {
		return nil, platform.Invalid("subjectKind", "must be a supported anchor subject kind")
	}
	anchors, err := s.anchors.List(ctx, versionID, kind, subjectID)
	if err != nil {
		return nil, mapDomainError(err, "code anchor")
	}
	return anchors, nil
}

// Validate re-checks one anchor against the latest done index commit, the
// current branch head or the anchor commit (first available) and persists
// valid/stale/broken. It never deletes an anchor.
func (s *AnchorService) Validate(ctx context.Context, versionID, anchorID int64) (codeintel.Anchor, error) {
	anchor, err := s.load(ctx, versionID, anchorID)
	if err != nil {
		return codeintel.Anchor{}, err
	}
	repository, err := s.resources.CodeRepository(ctx, anchor.RepoID)
	if err != nil {
		return codeintel.Anchor{}, mapDomainError(err, "code repository")
	}
	target := s.validationCommit(ctx, anchor, repository)
	status := string(codeintel.AnchorBroken)
	if target != "" {
		status = s.classifyAnchor(ctx, anchor, repository, target)
	}
	now := s.clock.Now()
	updated, err := s.anchors.UpdateStatus(ctx, anchor.ID, status, now)
	if err != nil {
		return codeintel.Anchor{}, mapDomainError(err, "code anchor")
	}
	if updated.VersionID == 0 {
		updated = anchor
		updated.Status = status
		updated.LastValidatedAt = &now
	}
	s.publishAnchorEvent(ctx, updated)
	s.recordAudit(ctx, "code.anchor.validated", platformid.Format(updated.ID), map[string]any{
		"version_id":   platformid.Format(updated.VersionID),
		"subject_kind": string(updated.SubjectKind),
		"subject_id":   platformid.Format(updated.SubjectID),
		"status":       updated.Status,
	})
	return updated, nil
}

// Delete removes one anchor of the version. Deleting a missing or foreign
// anchor maps to 404.
func (s *AnchorService) Delete(ctx context.Context, versionID, anchorID int64) error {
	anchor, err := s.load(ctx, versionID, anchorID)
	if err != nil {
		return err
	}
	if err := s.anchors.Delete(ctx, anchor.ID); err != nil {
		return mapDomainError(err, "code anchor")
	}
	s.recordAudit(ctx, "code.anchor.deleted", platformid.Format(anchor.ID), map[string]any{
		"version_id":   platformid.Format(anchor.VersionID),
		"subject_kind": string(anchor.SubjectKind),
		"subject_id":   platformid.Format(anchor.SubjectID),
	})
	return nil
}

// --- internals -------------------------------------------------------------------

func (s *AnchorService) load(ctx context.Context, versionID, anchorID int64) (codeintel.Anchor, error) {
	anchor, err := s.anchors.Get(ctx, anchorID)
	if err != nil {
		return codeintel.Anchor{}, mapDomainError(err, "code anchor")
	}
	if anchor.VersionID != versionID {
		return codeintel.Anchor{}, platform.NotFound("code anchor")
	}
	return anchor, nil
}

// checkSubject validates the version ownership rules of spec §6.1.
func (s *AnchorService) checkSubject(ctx context.Context, versionID int64, kind codeintel.AnchorSubject, subjectID int64) error {
	if kind == codeintel.AnchorSubjectVersion {
		if subjectID != versionID {
			return unprocessable("invalid_subject", "the version subject must be the anchor version")
		}
		return nil
	}
	if s.subjects == nil {
		return platform.Unavailable("anchor subject reader is not configured")
	}
	var (
		ok  bool
		err error
	)
	switch kind {
	case codeintel.AnchorSubjectRequirement:
		ok, err = s.subjects.RequirementInVersion(ctx, versionID, subjectID)
	case codeintel.AnchorSubjectRequirementVersion:
		ok, err = s.subjects.RequirementVersionInVersion(ctx, versionID, subjectID)
	case codeintel.AnchorSubjectCaseVersion:
		ok, err = s.subjects.CaseVersionInVersion(ctx, versionID, subjectID)
	case codeintel.AnchorSubjectIssue:
		ok, err = s.subjects.IssueInVersion(ctx, versionID, subjectID)
	}
	if err != nil {
		return err
	}
	if !ok {
		return unprocessable("invalid_subject", "subject does not belong to this version")
	}
	return nil
}

// targetCommit resolves the create-time commit: explicit 40-hex, then the
// version baseline, then the latest done index commit, then the branch head.
func (s *AnchorService) targetCommit(ctx context.Context, binding VersionSUTBinding, repository RepoRef, explicit string) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		if !ValidCommitSHA(value) {
			return "", unprocessable("invalid_commit", "commit must be a 40-character lowercase hex commit SHA")
		}
		if _, err := s.git.ResolveCommit(ctx, repository.Path, value); err != nil {
			return "", mapGitFileError(err)
		}
		return value, nil
	}
	if ValidCommitSHA(binding.CodeBaselineCommit) {
		return binding.CodeBaselineCommit, nil
	}
	if commit, ok := s.latestDoneCommit(ctx, repository.ID); ok {
		return commit, nil
	}
	head, err := s.git.HeadCommit(ctx, repository.Path, repository.Branch)
	if err != nil {
		return "", mapGitFileError(err)
	}
	return head, nil
}

// validationCommit returns the newest provable commit, then the branch head,
// then the anchor commit (spec §6.1).
func (s *AnchorService) validationCommit(ctx context.Context, anchor codeintel.Anchor, repository RepoRef) string {
	if commit, ok := s.latestDoneCommit(ctx, anchor.RepoID); ok {
		return commit
	}
	head, err := s.git.HeadCommit(ctx, repository.Path, repository.Branch)
	if err == nil && ValidCommitSHA(head) {
		return head
	}
	if ValidCommitSHA(anchor.CommitSHA) {
		return anchor.CommitSHA
	}
	return ""
}

// latestDoneCommit returns the commit of the newest done index run.
func (s *AnchorService) latestDoneCommit(ctx context.Context, repoID int64) (string, bool) {
	if s.runs == nil {
		return "", false
	}
	run, err := s.runs.LatestRun(ctx, repoID)
	if err != nil || run.Status != string(codeintel.IndexDone) || !ValidCommitSHA(run.CommitSHA) {
		return "", false
	}
	return run.CommitSHA, true
}

// classifyAnchor computes the valid/stale/broken status of one anchor against a
// target commit.
func (s *AnchorService) classifyAnchor(ctx context.Context, anchor codeintel.Anchor, repository RepoRef, target string) string {
	window, err := s.readWindow(ctx, repository.Path, target, anchor.FilePath, anchor.StartLine, anchor.EndLine)
	if err != nil {
		return string(codeintel.AnchorBroken)
	}
	if anchor.StartLine != nil && anchor.EndLine != nil {
		if *anchor.StartLine < 1 || *anchor.EndLine < *anchor.StartLine || *anchor.EndLine > window.TotalLines {
			return string(codeintel.AnchorBroken)
		}
		if anchor.ExcerptSHA != "" {
			lines, linesErr := excerptLines(window, *anchor.StartLine, *anchor.EndLine)
			if linesErr != nil {
				return string(codeintel.AnchorBroken)
			}
			if codeintel.ExcerptHash(lines) != anchor.ExcerptSHA {
				return string(codeintel.AnchorStale)
			}
			return string(codeintel.AnchorValid)
		}
	}
	if anchor.ExcerptSHA == "" && anchor.CommitSHA != "" && anchor.CommitSHA != target {
		return string(codeintel.AnchorStale)
	}
	return string(codeintel.AnchorValid)
}

// readWindow reads the anchor file at commit. With a line range it only reads
// that excerpt; otherwise it reads the first line to prove existence and learn
// the total line count.
func (s *AnchorService) readWindow(ctx context.Context, repoPath, commit, filePath string, start, end *int) (codeintel.FileWindow, error) {
	from, to := 1, 1
	if start != nil && end != nil {
		from, to = *start, *end
	}
	window, err := s.git.ReadFile(ctx, repoPath, commit, filePath, from, to, codeintel.MaxFileReadBytes)
	if err != nil {
		return codeintel.FileWindow{}, mapGitFileError(err)
	}
	return window, nil
}

// excerptLines returns exactly the [start,end] lines from a read window.
func excerptLines(window codeintel.FileWindow, start, end int) ([]string, error) {
	if start < 1 || end < start {
		return nil, unprocessable("invalid_range", "line range is not valid")
	}
	offset := start - window.StartLine
	count := end - start + 1
	if window.StartLine < 1 || offset < 0 || offset+count > len(window.Lines) {
		return nil, unprocessable("invalid_range", "line range is outside the file")
	}
	return window.Lines[offset : offset+count], nil
}

func validateAnchorRange(start, end, total int) error {
	if start < 1 || end < start {
		return unprocessable("invalid_range", "line range is not valid")
	}
	if total <= 0 || end > total {
		return unprocessable("invalid_range", "line range is outside the file")
	}
	return nil
}

func validExcerptSHA(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// mapGitFileError turns git path/range errors into 422 anchor validation
// failures while keeping the stable git error code and never exposing a path.
func mapGitFileError(err error) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		if apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusUnprocessableEntity {
			return unprocessable(apiErr.Code, apiErr.Message)
		}
	}
	return err
}

func duplicateAnchorError() error {
	return platform.NewError(http.StatusConflict, "anchor_duplicate", "an anchor already exists for this subject point")
}

func lineValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func (s *AnchorService) publishAnchorEvent(ctx context.Context, anchor codeintel.Anchor) {
	publishEvent(ctx, s.events, "code.anchor_validated", anchor.VersionID, map[string]any{
		"anchor_id":    platformid.Format(anchor.ID),
		"subject_kind": string(anchor.SubjectKind),
		"subject_id":   platformid.Format(anchor.SubjectID),
		"status":       anchor.Status,
	})
}

func (s *AnchorService) recordAudit(ctx context.Context, action, targetID string, metadata map[string]any) {
	recordAudit(ctx, s.audit, action, "code_anchor", targetID, metadata)
}
