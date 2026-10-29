package application

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// version_change_service.go implements the P8 D58 read-time snapshot generation
// of GET /api/versions/{id}/code/changes: for every batch of a version bound to
// a code repository, the deterministic base..head diff is summarised into one
// version_change row (upserted by (version_id, batch_id)) and announced once via
// code.changed. The service never stores code content, diffs, prompts or model
// responses.

// VersionChangeEvent is the payload of the version_change code.changed event.
// It carries the documented §11.2 summary fields only: the batch id (decimal
// string at the JSON boundary), the commit pair, the file/impact counters and
// the D49 rewritten flag.
type VersionChangeEvent struct {
	BatchID          int64
	BaseCommit       string
	HeadCommit       string
	ChangedFiles     int
	ImpactedCases    int
	HistoryRewritten bool
}

// VersionChangeNotifier publishes the version_change code.changed event. The
// existing codeintel eventing Publisher implements both this port and
// ImpactNotifier, so the composition root injects the same adapter.
type VersionChangeNotifier interface {
	VersionChanged(ctx context.Context, versionID int64, e VersionChangeEvent) error
}

// VersionChangeServiceDeps are the ports of the version-change snapshot use
// case. Changes is the snapshot repository doubled as the read-only run_batch
// projection; Notifier may be nil (publishing disabled).
type VersionChangeServiceDeps struct {
	Git      GitReader
	Repos    RepoCatalog
	Versions VersionCatalog
	Entities domain.EntityRepository
	Anchors  domain.AnchorRepository
	Changes  domain.VersionChangeRepository
	Notifier VersionChangeNotifier
	IDs      IDGenerator
	Clock    Clock
}

// VersionChangeService derives, persists and reads per-batch change snapshots.
type VersionChangeService struct {
	deps VersionChangeServiceDeps
}

// NewVersionChangeService wires the version-change snapshot use case.
func NewVersionChangeService(deps VersionChangeServiceDeps) *VersionChangeService {
	return &VersionChangeService{deps: deps}
}

// VersionChangeTimelineEntry is one batchTimeline entry: the persisted snapshot
// plus the batch identity/status attached at read time (version_change does not
// duplicate run_batch columns).
type VersionChangeTimelineEntry struct {
	Change      domain.VersionChange
	BatchNo     int
	BatchStatus string
	BatchKind   string
}

// Timeline returns the version's batch snapshots ordered Batch 1→N. Missing
// snapshots are computed and upserted idempotently at read time (D58 fallback),
// so repeated reads never duplicate rows, re-run the same diff or re-publish the
// event. A version without a code repository yields an empty, non-nil list.
func (s *VersionChangeService) Timeline(ctx context.Context, versionID int64) ([]VersionChangeTimelineEntry, error) {
	if s == nil || s.deps.Versions == nil || s.deps.Changes == nil {
		return []VersionChangeTimelineEntry{}, nil
	}
	info, err := s.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if !info.HasRepo || info.RepoID <= 0 {
		return []VersionChangeTimelineEntry{}, nil
	}

	batches, err := s.deps.Changes.ListBatches(ctx, versionID)
	if err != nil {
		return nil, err
	}
	stored, err := s.deps.Changes.ListByVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	storedByBatch := make(map[int64]domain.VersionChange, len(stored))
	for _, change := range stored {
		storedByBatch[change.BatchID] = change
	}

	entries := make([]VersionChangeTimelineEntry, 0, len(batches))
	var repo RepoInfo
	repoLoaded := false
	for _, batch := range batches {
		if change, ok := storedByBatch[batch.ID]; ok {
			entries = append(entries, versionChangeEntry(change, batch))
			continue
		}
		head := strings.TrimSpace(batch.HeadCommit)
		if head == "" {
			// A batch that never resolved code history has no change range.
			continue
		}
		base := strings.TrimSpace(batch.CodeBaselineCommit)
		if base == "" {
			base = strings.TrimSpace(info.BaselineCommit)
		}
		if !commitHashPattern.MatchString(base) || !commitHashPattern.MatchString(head) {
			// Historical batches with an unusable commit pair are skipped rather
			// than failing the whole read (no code-change data exists for them).
			continue
		}
		if !repoLoaded {
			repo, err = s.deps.Repos.Get(ctx, info.RepoID)
			if err != nil {
				return nil, err
			}
			repoLoaded = true
		}
		diff, err := s.deps.Git.Diff(ctx, repo.LocalPath, base, head)
		if err != nil {
			return nil, err
		}
		change, err := s.buildSnapshot(ctx, versionID, repo.ID, batch, base, head, diff)
		if err != nil {
			return nil, err
		}
		change.VersionID = versionID
		change.BatchID = batch.ID
		change.ComputedAt = s.now()
		if s.deps.IDs != nil {
			change.ID = s.deps.IDs.Next()
		}
		storedChange, created, err := s.deps.Changes.Upsert(ctx, change)
		if err != nil {
			return nil, err
		}
		if created {
			s.publish(ctx, versionID, storedChange)
		}
		entries = append(entries, versionChangeEntry(storedChange, batch))
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].BatchNo != entries[j].BatchNo {
			return entries[i].BatchNo < entries[j].BatchNo
		}
		return entries[i].Change.BatchID < entries[j].Change.BatchID
	})
	return entries, nil
}

// buildSnapshot summarises one deterministic change set into the per-batch
// snapshot. Items are path-ordered file summaries; coverage follows the P4
// INV-8 rule (any verified AI fact or code anchor covers a changed file).
func (s *VersionChangeService) buildSnapshot(ctx context.Context, versionID, repoID int64, batch domain.VersionBatch, base, head string, diff domain.ChangeSetData) (domain.VersionChange, error) {
	files := append([]domain.ChangedFile(nil), diff.Files...)
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.Path) != "" {
			paths = append(paths, file.Path)
		}
	}

	var entities []domain.CodeEntity
	if s.deps.Entities != nil && len(paths) > 0 {
		loaded, err := s.deps.Entities.ListByPaths(ctx, repoID, head, paths)
		if err != nil {
			return domain.VersionChange{}, err
		}
		entities = loaded
	}
	var anchors []domain.CodeAnchor
	if s.deps.Anchors != nil && len(paths) > 0 {
		loaded, err := s.deps.Anchors.ListByPaths(ctx, versionID, paths)
		if err != nil {
			return domain.VersionChange{}, err
		}
		anchors = loaded
	}

	covered := make(map[string]struct{}, len(paths))
	subjectsByPath := make(map[string][]domain.VersionChangeSubject)
	for _, entity := range entities {
		if entity.Path == "" {
			continue
		}
		// Coverage mirrors impactCoverageFacts: only a verified AI fact proves
		// the file was analysed (the T0 file entity never does).
		if isImpactFactKind(entity.Kind) && impactFactHasVerifiedEvidence(entity) {
			covered[entity.Path] = struct{}{}
			for _, evidence := range entity.Evidence {
				if evidence.File != "" {
					covered[evidence.File] = struct{}{}
				}
			}
		}
		if !isVersionChangeSubjectKind(entity.Kind) || !impactFactHasVerifiedEvidence(entity) {
			continue
		}
		subjectsByPath[entity.Path] = append(subjectsByPath[entity.Path], domain.VersionChangeSubject{
			Kind: entity.Kind,
			Name: entity.Name,
		})
	}

	caseIDsByPath := make(map[string]map[int64]struct{})
	for _, anchor := range anchors {
		if anchor.FilePath == "" {
			continue
		}
		covered[anchor.FilePath] = struct{}{}
		if anchor.SubjectKind != domain.SubjectCase || anchor.SubjectID <= 0 {
			continue
		}
		ids := caseIDsByPath[anchor.FilePath]
		if ids == nil {
			ids = make(map[int64]struct{})
			caseIDsByPath[anchor.FilePath] = ids
		}
		ids[anchor.SubjectID] = struct{}{}
	}

	items := make([]domain.VersionChangeItem, 0, len(files))
	impacted := make(map[int64]struct{})
	unmapped := 0
	for _, file := range files {
		if strings.TrimSpace(file.Path) == "" {
			continue
		}
		item := domain.VersionChangeItem{
			File:       file.Path,
			ChangeKind: domain.ChangeKindFromStatus(file.Status),
		}
		if subjects := subjectsByPath[file.Path]; len(subjects) > 0 {
			sort.SliceStable(subjects, func(i, j int) bool {
				if subjects[i].Kind != subjects[j].Kind {
					return subjects[i].Kind < subjects[j].Kind
				}
				return subjects[i].Name < subjects[j].Name
			})
			item.Subjects = append([]domain.VersionChangeSubject(nil), subjects...)
		} else {
			item.Subjects = []domain.VersionChangeSubject{}
		}
		if ids := caseIDsByPath[file.Path]; len(ids) > 0 {
			item.CaseIDs = formatSortedCaseIDs(ids)
			for id := range ids {
				impacted[id] = struct{}{}
			}
		} else {
			item.CaseIDs = []string{}
		}
		if _, ok := covered[file.Path]; !ok {
			item.Unmapped = true
			unmapped++
		}
		items = append(items, item)
	}

	return domain.VersionChange{
		BaseCommit:       base,
		HeadCommit:       head,
		HistoryRewritten: batch.BaseFallbackReason == domain.BaseFallbackHistoryRewritten,
		ChangedFiles:     len(items),
		ImpactedCases:    len(impacted),
		UnmappedCount:    unmapped,
		Items:            items,
	}, nil
}

// publish emits the summary-only code.changed event for one created snapshot.
// Publishing failures never fail the read: the persisted row stays authoritative
// and the next reader reuses it.
func (s *VersionChangeService) publish(ctx context.Context, versionID int64, change domain.VersionChange) {
	if s.deps.Notifier == nil {
		return
	}
	_ = s.deps.Notifier.VersionChanged(ctx, versionID, VersionChangeEvent{
		BatchID:          change.BatchID,
		BaseCommit:       change.BaseCommit,
		HeadCommit:       change.HeadCommit,
		ChangedFiles:     change.ChangedFiles,
		ImpactedCases:    change.ImpactedCases,
		HistoryRewritten: change.HistoryRewritten,
	})
}

// now returns the injected clock reading, or the zero time when no clock is
// wired (partially wired tests use the repository default instead).
func (s *VersionChangeService) now() time.Time {
	if s.deps.Clock == nil {
		return time.Time{}
	}
	return s.deps.Clock.Now()
}

// versionChangeEntry attaches the run_batch identity to one stored snapshot.
func versionChangeEntry(change domain.VersionChange, batch domain.VersionBatch) VersionChangeTimelineEntry {
	return VersionChangeTimelineEntry{
		Change:      change,
		BatchNo:     batch.BatchNo,
		BatchStatus: batch.Status,
		BatchKind:   batch.Kind,
	}
}

// isVersionChangeSubjectKind keeps only the §4.2 documented item subject kinds;
// the remaining AI fact kinds (page/client_call/module/symbol) still cover a
// changed file but are not summarised as item subjects.
func isVersionChangeSubjectKind(kind string) bool {
	switch kind {
	case domain.VersionChangeSubjectRoute, domain.VersionChangeSubjectAPI, domain.VersionChangeSubjectComponent:
		return true
	default:
		return false
	}
}

// formatSortedCaseIDs renders the case anchor ids as ascending decimal strings
// (D29: ids stay strings at the JSON boundary).
func formatSortedCaseIDs(ids map[int64]struct{}) []string {
	ordered := make([]int64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	formatted := make([]string, 0, len(ordered))
	for _, id := range ordered {
		formatted = append(formatted, strconv.FormatInt(id, 10))
	}
	return formatted
}
