package versionapp

// v0.22 A-7/A-8 plan-fact milestone (Task 18, R-A7-10 / R-A8-4 / A-15).
//
// The extraction run is a technical detail; the plan item is the user-visible
// milestone. Every attachment owns exactly one clarifying plan item
// ("解析需求文档 <name>") whose Decision starts with the deterministic marker
// "extraction_attachment:<attachmentID>:run:<runID>" and whose Status follows
// the latest run:
//
//   - Start (or replay) writes/keeps in_progress;
//   - a terminal run writes done or failed and, on completion, the two
//     append-only facts attachment_parse and requirement_source with the run id,
//     counts, input sha256 and skipped-image count.
//
// Idempotency: the plan item is found by the attachment part of its Decision
// marker (the run id link required by A §7.2 is refreshed on every write), so a
// replay or a re-extraction after a document change never duplicates the
// milestone. Every write is an optimistic revision-guarded PlanService.Update
// retried once on a stale revision. Facts use the version_fact
// (version_id, kind, key, content_hash) dedupe, so replaying an identical
// completed run appends nothing.
//
// Durability: plan/fact writes are best-effort after the run transition (the
// extraction row is the source of truth). They are deliberately not in the run
// transaction because PlanService and FactService own separate stores; the
// outbox/co-transaction requirement of A §7.2 stays an explicit Task 36/37
// composition-root concern (see the task report handoff).

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Extraction milestone contract constants.
const (
	// ExtractionPlanPhase is the plan phase of the document-parse milestone
	// (phase=clarifying, R-A7-10).
	ExtractionPlanPhase = "clarifying"
	// ExtractionPlanStatusRunning is the plan status while the run is open.
	ExtractionPlanStatusRunning = "in_progress"
	// ExtractionPlanStatusDone is the plan status of a completed run.
	ExtractionPlanStatusDone = "done"
	// ExtractionPlanStatusFailed is the plan status of a failed run.
	ExtractionPlanStatusFailed = "failed"
	// ExtractionPlanTitlePrefix is the user-visible milestone title prefix.
	ExtractionPlanTitlePrefix = "解析需求文档 "
	// ExtractionPlanDecisionMarkerPrefix starts the plan Decision marker that
	// links one attachment milestone to its current run.
	ExtractionPlanDecisionMarkerPrefix = "extraction_attachment:"
	// ExtractionFactKindAttachmentParse is the parse fact kind (R-A7-10).
	ExtractionFactKindAttachmentParse = "attachment_parse"
	// ExtractionFactKindRequirementSource is the requirement-provenance fact
	// kind (R-A7-10).
	ExtractionFactKindRequirementSource = "requirement_source"
	// ExtractionFactSource is version_fact.source for extraction facts.
	ExtractionFactSource = "requirement.extraction"
)

// ExtractionPlanStore is the narrow PlanService surface the milestone writer
// uses (*versionapp.PlanService implements it). Writes go through the
// revision-guarded Update path; Upsert is only used to create the missing item.
type ExtractionPlanStore interface {
	List(ctx context.Context, versionID int64, status string) ([]version.PlanItem, error)
	Upsert(ctx context.Context, item version.PlanItem) (version.PlanItem, error)
	Update(ctx context.Context, versionID, itemID int64, patch version.PlanPatch, revision int) (version.PlanItem, error)
}

// ExtractionFactStore is the narrow FactService surface the milestone writer
// uses (*versionapp.FactService implements it). RecordFact returns the
// content-hash dedupe outcome, so a replay never appends a duplicate fact.
type ExtractionFactStore interface {
	RecordFact(ctx context.Context, fact version.Fact) (FactRecordOutcome, error)
}

var (
	_ ExtractionPlanStore = (*PlanService)(nil)
	_ ExtractionFactStore = (*FactService)(nil)
)

// ExtractionMilestoneLocker is the optional repository capability that
// serializes the plan projection of one (version, attachment) across
// cooperative processes. The PostgreSQL adapter implements it with a session
// advisory lock; an in-memory adapter serializes with a keyed mutex. It is
// optional so a degraded/test repository without it still projects milestones
// (best effort, single process).
type ExtractionMilestoneLocker interface {
	// LockMilestone blocks until the caller owns the milestone lock of one
	// attachment and returns the release function (never nil on success).
	LockMilestone(ctx context.Context, versionID, attachmentID int64) (unlock func(), err error)
}

// SetMilestoneStores wires the plan and fact writers (Task 36/37 composition
// root). Either may be nil: a degraded assembly still runs extraction, it just
// does not project it into the plan/findings.
func (s *ExtractionService) SetMilestoneStores(plan ExtractionPlanStore, facts ExtractionFactStore) {
	if s == nil {
		return
	}
	s.plan = plan
	s.facts = facts
}

// ExtractionPlanDecisionMarker is the deterministic plan-item marker of one run
// of one attachment: Decision is "<marker> · <human detail>" (or the bare marker
// when the detail is empty). The item is found by the attachment part of the
// marker, so re-extraction after a document change updates the existing
// milestone instead of creating a second "解析需求文档 <name>" item. It is
// exported so Task 36/37 and tests share the link.
func ExtractionPlanDecisionMarker(attachmentID, runID int64) string {
	return extractionPlanItemMarkerPrefix(attachmentID) + "run:" + platformid.Format(runID)
}

// extractionPlanItemMarkerPrefix is the attachment part of the plan Decision
// marker (the stable identity of the milestone across runs).
func extractionPlanItemMarkerPrefix(attachmentID int64) string {
	return ExtractionPlanDecisionMarkerPrefix + platformid.Format(attachmentID) + ":"
}

// syncStartMilestone projects a Start/replay outcome into the plan item. A
// replay of a terminal run finalizes the milestone instead of reopening it.
func (s *ExtractionService) syncStartMilestone(ctx context.Context, run ExtractionRun, input ExtractionStartInput) ExtractionRun {
	if s == nil {
		return run
	}
	if run.Status == ExtractionRunDone || run.Status == ExtractionRunFailed {
		// Start deliberately selected this run (including a revival of an older
		// sha whose newer generation already settled), so the terminal
		// projection may take over the milestone.
		return s.syncTerminalMilestone(ctx, run, true)
	}
	if s.plan == nil && s.facts == nil {
		return run
	}
	name := input.AttachmentName
	if strings.TrimSpace(name) == "" {
		name = s.attachmentDocumentName(ctx, run)
	}
	s.persistExtractionPlan(ctx, run, ExtractionPlanStatusRunning, name,
		extractionRunningDetail(run))
	return run
}

// syncTransitionMilestone projects an explicit run transition (Resume/Retry)
// into the milestone. Resume can make a paused run terminal (the worker already
// finished the claimed batch while paused), and a retry moves a failed run back
// to pending: both outcomes are user-visible in the plan, so it follows the run.
func (s *ExtractionService) syncTransitionMilestone(ctx context.Context, run ExtractionRun) ExtractionRun {
	if s == nil {
		return run
	}
	switch run.Status {
	case ExtractionRunDone, ExtractionRunFailed:
		return s.syncTerminalMilestone(ctx, run, false)
	default:
		if s.plan == nil && s.facts == nil {
			return run
		}
		s.persistExtractionPlan(ctx, run, ExtractionPlanStatusRunning, "",
			extractionRunningDetail(run))
		return run
	}
}

// syncTerminalMilestone persists the done/failed plan item and the
// attachment_parse / requirement_source facts of a terminal run. Reconciliation
// runs whenever a source is wired - even with no plan/fact store - because the
// progress API reads the persisted report. The plan/fact writes are idempotent
// and best effort: a plan/fact failure never turns a completed extraction into
// an API failure.
//
// startSelected marks a run Start deliberately nominated as the attachment's
// milestone generation (including reviving an older sha); every other older
// run is fenced by extractionRunSuperseded.
func (s *ExtractionService) syncTerminalMilestone(ctx context.Context, run ExtractionRun, startSelected bool) ExtractionRun {
	if s == nil {
		return run
	}
	report := run.Report
	skipped := 0
	if report != nil {
		skipped = report.SkippedImages
	}
	name := ""
	if s.source != nil {
		// Resolve the run's own document by its input sha: after a changed-sha
		// re-extraction, a revived older run must reconcile the document it
		// actually parsed instead of the attachment's current one.
		document, err := s.source.ExtractionDocument(ctx, run.VersionID, run.AttachmentID, run.InputSHA256)
		if err == nil {
			name = document.Name
			skipped = document.SkippedImages
			if strings.TrimSpace(document.Text) != "" {
				if reconciled, reconcileErr := s.Reconcile(ctx, run.ID, document.Text, skipped); reconcileErr == nil {
					report = &reconciled
					if refreshed, getErr := s.repo.GetRun(ctx, run.ID); getErr == nil {
						run = refreshed
					}
					run.Report = report
				}
			}
		}
	}
	if s.plan == nil && s.facts == nil {
		return run
	}
	// A run superseded by a newer generation must not overwrite the live
	// milestone (archive/supersede fence), unless Start deliberately selected it.
	if !startSelected && s.extractionRunSuperseded(ctx, run) {
		return run
	}
	status := ExtractionPlanStatusRunning
	switch run.Status {
	case ExtractionRunDone:
		status = ExtractionPlanStatusDone
	case ExtractionRunFailed:
		status = ExtractionPlanStatusFailed
	}
	s.persistExtractionPlan(ctx, run, status, name, extractionTerminalDetail(run, report))
	s.recordExtractionFacts(ctx, run, report, skipped)
	return run
}

// persistExtractionPlan finds the attachment's milestone item by its Decision
// marker and writes status/title/detail with the current revision. The
// version+attachment milestone lock serializes the first write (two concurrent
// starts must not insert two plan items) and a stale revision (another plan
// writer between the list and the update) re-lists and retries once.
func (s *ExtractionService) persistExtractionPlan(ctx context.Context, run ExtractionRun, status, name, detail string) {
	if s.plan == nil || run.VersionID <= 0 || run.ID <= 0 || run.AttachmentID <= 0 {
		return
	}
	unlock := s.lockMilestone(ctx, run.VersionID, run.AttachmentID)
	defer unlock()
	decision := extractionPlanDecision(run.AttachmentID, run.ID, detail)
	title := ""
	if strings.TrimSpace(name) != "" {
		title = extractionPlanTitle(name, run.AttachmentID)
	}
	for attempt := 0; attempt < 2; attempt++ {
		items, err := s.plan.List(ctx, run.VersionID, "")
		if err != nil {
			return
		}
		item, found := findExtractionPlanItem(items, run.AttachmentID)
		if !found {
			_, _ = s.plan.Upsert(ctx, version.PlanItem{
				VersionID: run.VersionID,
				Phase:     ExtractionPlanPhase,
				Title:     extractionPlanTitle(name, run.AttachmentID),
				Status:    status,
				Decision:  decision,
				Order:     nextExtractionPlanOrder(items),
			})
			return
		}
		patch := version.PlanPatch{
			Status:   status,
			Decision: decision,
		}
		if title != "" {
			patch.Title = title
		}
		_, err = s.plan.Update(ctx, run.VersionID, item.ID, patch, item.Revision)
		if err == nil {
			return
		}
		var conflict *ConflictError
		if !errors.As(err, &conflict) {
			return
		}
	}
}

// lockMilestone serializes one attachment's milestone projection. The
// repository lock is used when available (a PostgreSQL session advisory lock or
// an in-memory keyed mutex); a repository without the capability keeps the
// projection best-effort in a single process.
func (s *ExtractionService) lockMilestone(ctx context.Context, versionID, attachmentID int64) func() {
	if s == nil || s.repo == nil {
		return func() {}
	}
	locker, ok := s.repo.(ExtractionMilestoneLocker)
	if !ok {
		return func() {}
	}
	unlock, err := locker.LockMilestone(ctx, versionID, attachmentID)
	if err != nil || unlock == nil {
		return func() {}
	}
	return unlock
}

// recordExtractionFacts appends the parse and requirement-source facts through
// the FactService content-hash dedupe. A failed run only records
// attachment_parse (there are no extracted requirements to attribute).
func (s *ExtractionService) recordExtractionFacts(ctx context.Context, run ExtractionRun, report *ReconciliationReport, skipped int) {
	if s.facts == nil || run.VersionID <= 0 || run.ID <= 0 {
		return
	}
	payload := extractionFactPayload(run, report, skipped)
	kinds := []string{ExtractionFactKindAttachmentParse}
	if run.Status == ExtractionRunDone {
		kinds = append(kinds, ExtractionFactKindRequirementSource)
	}
	for _, kind := range kinds {
		key := extractionFactKey(kind, run.ID)
		_, _ = s.facts.RecordFact(ctx, version.Fact{
			VersionID: run.VersionID,
			Kind:      kind,
			Key:       key,
			Payload:   payload,
			Source:    ExtractionFactSource,
			SourceRef: key,
		})
	}
}

func extractionFactPayload(run ExtractionRun, report *ReconciliationReport, skipped int) map[string]any {
	extracted := run.ExtractedCount
	if report != nil {
		// The report is the audit count over declared requirements; the run
		// column also counts extracted requirements outside the document.
		extracted = report.Extracted
	}
	payload := map[string]any{
		"run_id":          platformid.Format(run.ID),
		"attachment_id":   platformid.Format(run.AttachmentID),
		"status":          string(run.Status),
		"input_sha256":    run.InputSHA256,
		"total_segments":  run.TotalSegments,
		"done_segments":   run.DoneSegments,
		"failed_segments": run.FailedSegments,
		"extracted":       extracted,
		"skipped_images":  skipped,
	}
	if run.ExpectedCount != nil {
		payload["expected"] = *run.ExpectedCount
	}
	if report != nil {
		payload["expected"] = report.Expected
		payload["missing"] = report.Missing
		payload["low_confidence"] = report.LowConfidence
		payload["planned_count"] = report.PlannedCount
	}
	if strings.TrimSpace(run.Error) != "" {
		payload["error"] = run.Error
	}
	return payload
}

func extractionFactKey(kind string, runID int64) string {
	return kind + ":run:" + platformid.Format(runID)
}

func extractionPlanDecision(attachmentID, runID int64, detail string) string {
	marker := ExtractionPlanDecisionMarker(attachmentID, runID)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return marker
	}
	return marker + " · " + detail
}

func extractionPlanTitle(name string, attachmentID int64) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "#" + platformid.Format(attachmentID)
	}
	return ExtractionPlanTitlePrefix + name
}

// findExtractionPlanItem finds the attachment's single milestone item, whatever
// run it currently points at (the decision marker's run part is refreshed by
// every write).
func findExtractionPlanItem(items []version.PlanItem, attachmentID int64) (version.PlanItem, bool) {
	prefix := extractionPlanItemMarkerPrefix(attachmentID)
	for _, item := range items {
		if strings.HasPrefix(item.Decision, prefix) {
			return item, true
		}
	}
	return version.PlanItem{}, false
}

// extractionRunSuperseded reports whether a newer generation of the same
// attachment already owns the milestone. Any older run is fenced - including
// when the newer run is already terminal - unless Start deliberately selected
// the run as the attachment's generation (a superseded-sha revival) or the plan
// item's Decision marker already points at it.
func (s *ExtractionService) extractionRunSuperseded(ctx context.Context, run ExtractionRun) bool {
	if s == nil || s.repo == nil || run.VersionID <= 0 || run.AttachmentID <= 0 {
		return false
	}
	// The plan item's Decision marker is the durable selection: Start refreshes
	// it synchronously, so it orders generations across processes. When the
	// marker exists and is valid it is authoritative.
	if s.plan != nil {
		if items, err := s.plan.List(ctx, run.VersionID, ""); err == nil {
			if item, found := findExtractionPlanItem(items, run.AttachmentID); found {
				if markerRunID, ok := extractionPlanMarkerRunID(item.Decision, run.AttachmentID); ok {
					return markerRunID != run.ID
				}
			}
		}
	}
	// No usable plan marker (facts-only assembly or a failed milestone write):
	// fall back to the process-local Start selection, then to the newest run.
	if selected, ok := s.selectedMilestoneRun(run); ok {
		return selected != run.ID
	}
	latest, err := s.repo.GetLatestRun(ctx, run.VersionID, run.AttachmentID)
	if err != nil {
		return false
	}
	return latest.ID != run.ID
}

// selectMilestoneRun records the attachment generation Start nominated. It is
// process-local; the durable counterpart is the plan item's Decision marker.
func (s *ExtractionService) selectMilestoneRun(run ExtractionRun) {
	if s == nil || run.ID <= 0 || run.VersionID <= 0 || run.AttachmentID <= 0 {
		return
	}
	s.selectedMu.Lock()
	defer s.selectedMu.Unlock()
	if s.selectedRuns == nil {
		s.selectedRuns = map[string]int64{}
	}
	s.selectedRuns[extractionMilestoneRunKey(run.VersionID, run.AttachmentID)] = run.ID
}

// selectedMilestoneRun returns the run Start nominated as the attachment's
// milestone generation. It is process-local; the durable counterpart is the
// plan item's Decision marker.
func (s *ExtractionService) selectedMilestoneRun(run ExtractionRun) (int64, bool) {
	if s == nil {
		return 0, false
	}
	s.selectedMu.RLock()
	defer s.selectedMu.RUnlock()
	selected, ok := s.selectedRuns[extractionMilestoneRunKey(run.VersionID, run.AttachmentID)]
	return selected, ok
}

func extractionMilestoneRunKey(versionID, attachmentID int64) string {
	return platformid.Format(versionID) + ":" + platformid.Format(attachmentID)
}

// extractionPlanMarkerRunID parses the run id of one attachment's Decision
// marker ("extraction_attachment:<aid>:run:<rid>[ · detail]").
func extractionPlanMarkerRunID(decision string, attachmentID int64) (int64, bool) {
	prefix := extractionPlanItemMarkerPrefix(attachmentID) + "run:"
	if !strings.HasPrefix(decision, prefix) {
		return 0, false
	}
	rest := decision[len(prefix):]
	if end := strings.Index(rest, " · "); end >= 0 {
		rest = rest[:end]
	}
	runID, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if err != nil || runID <= 0 {
		return 0, false
	}
	return runID, true
}

func nextExtractionPlanOrder(items []version.PlanItem) int {
	maxOrder := 0
	for _, item := range items {
		if item.Order > maxOrder {
			maxOrder = item.Order
		}
	}
	return maxOrder + 1
}

func extractionRunningDetail(run ExtractionRun) string {
	if run.TotalSegments <= 0 {
		return "解析中"
	}
	return fmt.Sprintf("解析中：%d/%d 段", run.DoneSegments, run.TotalSegments)
}

func extractionTerminalDetail(run ExtractionRun, report *ReconciliationReport) string {
	switch run.Status {
	case ExtractionRunDone:
		expected := 0
		if run.ExpectedCount != nil {
			expected = *run.ExpectedCount
		}
		extracted := run.ExtractedCount
		if report != nil {
			expected = report.Expected
			extracted = report.Extracted
		}
		if expected > 0 {
			detail := fmt.Sprintf("完成：已抽取 %d/%d 条", extracted, expected)
			if report != nil && report.Missing > 0 {
				detail += fmt.Sprintf("，缺失 %d 条", report.Missing)
			}
			if report != nil && report.PlannedCount > 0 {
				detail += fmt.Sprintf("，V2 规划 %d 条", report.PlannedCount)
			}
			return detail
		}
		return fmt.Sprintf("完成：已抽取 %d 条", extracted)
	case ExtractionRunFailed:
		message := truncateExtractionDecision(strings.TrimSpace(run.Error), 200)
		if message == "" {
			message = "未知错误"
		}
		return "失败：" + message
	default:
		return ""
	}
}

func truncateExtractionDecision(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
