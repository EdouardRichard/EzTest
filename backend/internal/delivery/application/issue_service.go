package deliveryapp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	platformclock "github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
)

// P4 issue event types (spec §4.3, events.md §13.1). Events are UI projections:
// a publish failure never rolls back the issue write.
const (
	issueEventCreated  = "issue.created"
	issueEventUpdated  = "issue.updated"
	issueEventReopened = "issue.reopened"
	issueEventMerged   = "issue.merged"
	// issueEventRecurred is the P7 recurrence event (spec §7): emitted on both a
	// successful redirect and a broken/cyclic chain fallback.
	issueEventRecurred = "issue.recurred"
	// v0.22 W2-5 retest events (spec EV-16 / R-W2-2). retest_started is emitted
	// when a retest wave is linked; completed/failed are emitted by the terminal
	// wave writeback. "Other" terminal statuses keep retesting (no event).
	issueEventRetestStarted   = "issue.retest_started"
	issueEventRetestCompleted = "issue.retest_completed"
	issueEventRetestFailed    = "issue.retest_failed"
)

// Audit actions of the issue lifecycle (D52). Audit is best effort.
const (
	issueAuditCreated   = "delivery.issue.created"
	issueAuditConfirmed = "delivery.issue.confirmed"
	issueAuditDismissed = "delivery.issue.dismissed"
	issueAuditReopened  = "delivery.issue.reopened"
	issueAuditMerged    = "delivery.issue.merged"
	// issueAuditRecurred records a recurrence redirect, including the fallback
	// that keeps the pre-P7 dedupe behavior.
	issueAuditRecurred = "delivery.issue.recurred"
	// issueAuditRetestStarted/Completed/Failed record the v0.22 retest lifecycle
	// (IDs only, never case/evidence content).
	issueAuditRetestStarted   = "delivery.issue.retest_started"
	issueAuditRetestCompleted = "delivery.issue.retest_completed"
	issueAuditRetestFailed    = "delivery.issue.retest_failed"
)

const (
	// issueReasonMax is the issue action reason bound of the API contract.
	issueReasonMax = 2000
	// issueActorKindUser is the actor kind persisted for human transitions.
	issueActorKindUser = "user"
	// issueActorKindSystem is the actor kind persisted for system transitions.
	issueActorKindSystem = "system"
	// issueReasonRecurrence is the status-history reason of a recurrence
	// redirect (spec §4.6); the status itself does not change.
	issueReasonRecurrence = "recurrence"
)

// CreateManualIssueInput is the v0.22 W2-4 manual create command. CaseRunID and
// the evidence/merge IDs are optional; empty strings mean absent.
type CreateManualIssueInput struct {
	VersionID   int64
	Title       string
	Summary     string
	Category    string
	Severity    string
	CaseRunID   int64
	EvidenceIDs []int64
	MergeIntoID int64
	Reason      string
}

// RetestResult is the outcome of StartRetest: the created retest wave and the
// issue row carrying retest_status='retesting' and the wave pointer.
type RetestResult struct {
	Wave  RetestWave
	Issue delivery.Issue
}

// UpdateManualIssueInput is the v0.22 W2-4 manual patch command. Empty string
// fields leave the stored column unchanged; ExpectedRevision is mandatory.
type UpdateManualIssueInput struct {
	Title            string
	Summary          string
	Category         string
	Severity         string
	CaseRunID        int64
	EvidenceIDs      []int64
	MergeIntoID      int64
	Reason           string
	ExpectedRevision int32
}

// CreateManual creates one source='manual' issue (v0.22 W2-4). A supplied
// case_run is resolved through the execution read port so its case_version,
// requirement and version SUT refs are persisted with the explicit evidence
// refs. The creation history, event and audit are projections of the committed
// write; a projection failure never rolls it back. When MergeIntoID is set the
// existing merge use case runs after the create, so merge validation, history
// and the issue.merged event stay unchanged.
func (s *IssueService) CreateManual(ctx context.Context, in CreateManualIssueInput, actorID int64) (delivery.Issue, error) {
	if in.VersionID <= 0 {
		return delivery.Issue{}, invalidArgumentf("version id must be a positive decimal ID")
	}
	if actorID <= 0 {
		return delivery.Issue{}, invalidArgumentf("actor id must be a positive decimal ID")
	}
	if in.CaseRunID < 0 {
		return delivery.Issue{}, invalidArgumentf("case_run_id must be a positive decimal ID")
	}
	if in.MergeIntoID < 0 {
		return delivery.Issue{}, invalidArgumentf("merge_into_id must be a positive decimal ID")
	}
	reason, err := normalizeIssueReason(in.Reason, false)
	if err != nil {
		return delivery.Issue{}, err
	}
	if err := s.requireIssueRepository(); err != nil {
		return delivery.Issue{}, err
	}
	if err := delivery.ValidateManualIssueFields(in.Title, in.Summary, in.Category, in.Severity); err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("validate manual issue", err)
	}
	if _, err := delivery.ValidateManualEvidenceIDs(in.EvidenceIDs); err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("validate manual issue evidence", err)
	}
	manual, err := s.resolveManualIssueInput(ctx, in.VersionID, in.Title, in.Summary,
		in.Category, in.Severity, in.CaseRunID, in.EvidenceIDs)
	if err != nil {
		return delivery.Issue{}, err
	}
	issue, refs, err := delivery.NewManualIssue(manual)
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("build manual issue", err)
	}
	now := s.nowUTC()
	issue.CreatedAt = now
	issue.UpdatedAt = now
	created, err := s.repo.CreateManual(ctx, IssueManualCreate{
		Issue: issue,
		Refs:  refs,
		History: delivery.IssueStatusChange{
			To:        delivery.IssueStatusOpen,
			Reason:    reason,
			ActorID:   &actorID,
			ActorKind: issueActorKindUser,
			CreatedAt: now,
			UpdatedAt: now,
		},
	})
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("create manual issue", err)
	}
	projected := created
	if projected.VersionID <= 0 {
		projected.VersionID = in.VersionID
	}
	payload := map[string]any{
		"issue_id": formatIssueID(projected.ID),
		"status":   string(projected.Status),
		"category": projected.Category,
		"severity": projected.Severity,
		"source":   delivery.IssueManualSource,
	}
	if in.CaseRunID > 0 {
		payload["case_run_id"] = formatIssueID(in.CaseRunID)
	}
	s.publishIssueEvent(ctx, issueEventCreated, projected.VersionID, payload)
	s.recordManualIssueCreatedAudit(ctx, actorID, projected)
	if in.MergeIntoID > 0 {
		return s.Merge(ctx, in.VersionID, projected.ID, in.MergeIntoID, actorID, reason, &projected.ExpectedRevision)
	}
	return created, nil
}

// UpdateManual patches the editable fields of one issue under the
// expected_revision CAS (v0.22 W2-4). Empty string fields leave the stored
// column unchanged; evidence IDs are appended to the append-only refs. A stale
// revision is reported as the stable issue_revision_conflict subcode. When
// MergeIntoID is set the existing merge use case runs on the patched revision.
func (s *IssueService) UpdateManual(ctx context.Context, versionID, issueID int64, in UpdateManualIssueInput, actorID int64) (delivery.Issue, error) {
	if versionID <= 0 || issueID <= 0 {
		return delivery.Issue{}, invalidArgumentf("version and issue IDs must be positive decimal IDs")
	}
	if actorID <= 0 {
		return delivery.Issue{}, invalidArgumentf("actor id must be a positive decimal ID")
	}
	if in.ExpectedRevision <= 0 {
		return delivery.Issue{}, invalidArgumentf("expected revision must be a positive integer")
	}
	if in.CaseRunID < 0 {
		return delivery.Issue{}, invalidArgumentf("case_run_id must be a positive decimal ID")
	}
	if in.MergeIntoID < 0 {
		return delivery.Issue{}, invalidArgumentf("merge_into_id must be a positive decimal ID")
	}
	reason, err := normalizeIssueReason(in.Reason, false)
	if err != nil {
		return delivery.Issue{}, err
	}
	if err := s.requireIssueRepository(); err != nil {
		return delivery.Issue{}, err
	}
	current, err := s.repo.Get(ctx, versionID, issueID)
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("load issue for manual update", err)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = current.Title
	}
	summary := current.Summary
	if strings.TrimSpace(in.Summary) != "" {
		summary = in.Summary
	}
	category := strings.TrimSpace(in.Category)
	if category == "" {
		category = current.Category
	}
	severity := strings.TrimSpace(in.Severity)
	if severity == "" {
		severity = current.Severity
	}
	if err := delivery.ValidateManualIssueFields(title, summary, category, severity); err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("validate manual issue update", err)
	}
	if _, err := delivery.ValidateManualEvidenceIDs(in.EvidenceIDs); err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("validate manual issue update evidence", err)
	}
	manual, err := s.resolveManualIssueInput(ctx, versionID, title, summary,
		category, severity, in.CaseRunID, in.EvidenceIDs)
	if err != nil {
		return delivery.Issue{}, err
	}
	_, refs, err := delivery.NewManualIssue(manual)
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("validate manual issue update", err)
	}
	// The repository treats an empty summary as "unchanged"; a non-blank patch
	// value replaces it, preserving intentional leading/trailing whitespace.
	updatedSummary := ""
	if strings.TrimSpace(in.Summary) != "" {
		updatedSummary = in.Summary
	}
	now := s.nowUTC()
	from := current.Status
	updated, err := s.repo.UpdateManual(ctx, IssueManualUpdate{
		VersionID:        versionID,
		IssueID:          issueID,
		Title:            strings.TrimSpace(in.Title),
		Summary:          updatedSummary,
		Category:         strings.TrimSpace(in.Category),
		Severity:         strings.TrimSpace(in.Severity),
		ExpectedRevision: in.ExpectedRevision,
		Refs:             refs,
		History: delivery.IssueStatusChange{
			From:      &from,
			To:        current.Status,
			Reason:    reason,
			ActorID:   &actorID,
			ActorKind: issueActorKindUser,
			CreatedAt: now,
			UpdatedAt: now,
		},
	})
	return s.finishManualUpdate(ctx, versionID, issueID, current, in, reason, actorID, updated, err)
}

// finishManualUpdate applies the patch projections (event, optional merge).
func (s *IssueService) finishManualUpdate(ctx context.Context, versionID, issueID int64,
	current delivery.Issue, in UpdateManualIssueInput, reason string, actorID int64,
	updated delivery.Issue, repoErr error) (delivery.Issue, error) {
	if repoErr != nil {
		if errors.Is(repoErr, delivery.ErrStateConflict) {
			return delivery.Issue{}, &IssueStateConflictError{Reason: IssueConflictRevisionMismatch}
		}
		return delivery.Issue{}, mapIssueRepositoryError("update manual issue", repoErr)
	}
	updatedID := updated.ID
	if updatedID <= 0 {
		updatedID = issueID
	}
	payload := map[string]any{
		"issue_id":    formatIssueID(updatedID),
		"from_status": string(current.Status),
		"to_status":   string(updated.Status),
		"source":      delivery.IssueManualSource,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	s.publishIssueEvent(ctx, issueEventUpdated, versionID, payload)
	if in.MergeIntoID > 0 {
		return s.Merge(ctx, versionID, updatedID, in.MergeIntoID, actorID, reason, &updated.ExpectedRevision)
	}
	return updated, nil
}

// StartRetest creates a retest wave for one issue (v0.22 W2-5 / R-W2-2). The
// optional caseRunIDs restrict the retest to a subset of the issue's linked
// case_runs; an empty selection retests every linked case_run. The retest
// intents reuse IntentService.CreateManual(PurposeRetest), the wave reuses the
// deterministic scheduler, and the issue/ref writeback is one transaction.
func (s *IssueService) StartRetest(ctx context.Context, versionID, issueID, actorID int64, caseRunIDs ...int64) (RetestResult, error) {
	if versionID <= 0 || issueID <= 0 {
		return RetestResult{}, invalidArgumentf("version and issue IDs must be positive decimal IDs")
	}
	if actorID <= 0 {
		return RetestResult{}, invalidArgumentf("actor id must be a positive decimal ID")
	}
	if err := s.requireIssueRepository(); err != nil {
		return RetestResult{}, err
	}
	if err := s.requireExecutionReader(); err != nil {
		return RetestResult{}, err
	}
	if s.retest == nil {
		return RetestResult{}, issueServiceNotConfigured("retest executor")
	}
	issue, err := s.repo.Get(ctx, versionID, issueID)
	if err != nil {
		return RetestResult{}, mapIssueRepositoryError("load issue for retest", err)
	}
	if issue.Status != delivery.IssueStatusOpen && issue.Status != delivery.IssueStatusConfirmed {
		return RetestResult{}, &IssueStateConflictError{Reason: IssueConflictNotRetestable}
	}
	// Any active wave blocks a new retest: the deterministic scheduler keeps one
	// active wave per version, so a concurrent retest/execute must be a typed
	// conflict instead of a half-created intent batch.
	if _, active, err := s.retest.ActiveWave(ctx, versionID); err != nil {
		return RetestResult{}, mapIssueRepositoryError("check active retest wave", err)
	} else if active {
		return RetestResult{}, &IssueStateConflictError{Reason: IssueConflictRetestActive}
	}

	refs, err := s.repo.ListRefs(ctx, issueID)
	if err != nil {
		return RetestResult{}, mapIssueRepositoryError("list issue refs for retest", err)
	}
	selected, err := selectRetestCaseRunRefs(refs, caseRunIDs)
	if err != nil {
		return RetestResult{}, err
	}
	if len(selected) == 0 {
		return RetestResult{}, &IssueStateConflictError{Reason: IssueConflictNotRetestable}
	}

	// Resolve the selected case_runs to their case versions. One retest intent is
	// created per distinct case version, preserving the issue ref order.
	type retestCaseVersion struct {
		caseVersionID int64
	}
	ordered := make([]retestCaseVersion, 0, len(selected))
	seen := make(map[int64]struct{}, len(selected))
	for _, ref := range selected {
		snapshot, err := s.exec.CaseRunSnapshot(ctx, ref.RefID)
		if err != nil {
			return RetestResult{}, mapIssueRepositoryError("load retest case run", err)
		}
		if snapshot.VersionID > 0 && snapshot.VersionID != versionID {
			return RetestResult{}, invalidArgumentf("case run %d belongs to version %d, not %d",
				ref.RefID, snapshot.VersionID, versionID)
		}
		if snapshot.CaseVersionID <= 0 {
			return RetestResult{}, invalidArgumentf("case run %d has no case version", ref.RefID)
		}
		if _, ok := seen[snapshot.CaseVersionID]; ok {
			continue
		}
		seen[snapshot.CaseVersionID] = struct{}{}
		ordered = append(ordered, retestCaseVersion{caseVersionID: snapshot.CaseVersionID})
	}
	if len(ordered) == 0 {
		return RetestResult{}, &IssueStateConflictError{Reason: IssueConflictNotRetestable}
	}

	intentIDs := make([]int64, 0, len(ordered))
	for _, item := range ordered {
		intentID, err := s.retest.CreateRetestIntent(ctx, versionID, item.caseVersionID, actorID)
		if err != nil {
			s.cancelRetestIntents(ctx, intentIDs)
			return RetestResult{}, err
		}
		intentIDs = append(intentIDs, intentID)
	}
	wave, err := s.retest.StartRetestWave(ctx, versionID)
	if err != nil {
		// The intents are now open but no wave was planned; cancel them so a
		// failed start does not leave a phantom retest queue behind.
		s.cancelRetestIntents(ctx, intentIDs)
		return RetestResult{}, mapIssueRepositoryError("start retest wave", err)
	}
	if wave.ID <= 0 {
		s.cancelRetestIntents(ctx, intentIDs)
		return RetestResult{}, errors.New("delivery: retest wave has no ID")
	}

	now := s.nowUTC()
	retestRefs := make([]delivery.IssueRef, 0, len(wave.CaseRuns)+1)
	retestRefs = append(retestRefs, delivery.IssueRef{
		Kind: delivery.IssueRefKindWave, RefID: wave.ID, Role: delivery.IssueRefRoleRetest,
	})
	linkedCaseRunIDs := make([]int64, 0, len(wave.CaseRuns))
	for _, run := range wave.CaseRuns {
		if _, ok := seen[run.CaseVersionID]; !ok {
			continue
		}
		linkedCaseRunIDs = append(linkedCaseRunIDs, run.ID)
		retestRefs = append(retestRefs, delivery.IssueRef{
			Kind: delivery.IssueRefKindCaseRun, RefID: run.ID, Role: delivery.IssueRefRoleRetest,
		})
	}
	updated, err := s.repo.StartRetest(ctx, IssueRetestStart{
		VersionID: versionID,
		IssueID:   issueID,
		WaveID:    wave.ID,
		Refs:      retestRefs,
		Now:       now,
	})
	if err != nil {
		return RetestResult{}, mapIssueRepositoryError("persist retest start", err)
	}
	payload := map[string]any{
		"issue_id":      formatIssueID(updated.ID),
		"version_id":    formatIssueID(versionID),
		"wave_id":       formatIssueID(wave.ID),
		"case_run_ids":  formatIssueIDs(linkedCaseRunIDs),
		"retest_status": delivery.IssueRetestStatusRetesting,
	}
	s.publishIssueEvent(ctx, issueEventRetestStarted, versionID, payload)
	s.recordIssueAudit(ctx, actorID, issueAuditRetestStarted, updated.ID, map[string]any{
		"version_id": formatIssueID(versionID),
		"wave_id":    formatIssueID(wave.ID),
		"status":     delivery.IssueRetestStatusRetesting,
	})
	return RetestResult{Wave: wave, Issue: updated}, nil
}

// cancelRetestIntents best-effort rollback of a partially created retest batch.
func (s *IssueService) cancelRetestIntents(ctx context.Context, intentIDs []int64) {
	for _, intentID := range intentIDs {
		if intentID <= 0 {
			continue
		}
		_ = s.retest.CancelRetestIntent(ctx, intentID)
	}
}

// selectRetestCaseRunRefs filters the issue's source case_run refs and applies
// the optional client selection. A selected ID that is not linked to the issue
// is invalid rather than silently dropped.
func selectRetestCaseRunRefs(refs []delivery.IssueRef, caseRunIDs []int64) ([]delivery.IssueRef, error) {
	source := make([]delivery.IssueRef, 0, len(refs))
	available := make(map[int64]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Kind != delivery.IssueRefKindCaseRun || ref.Role == delivery.IssueRefRoleRetest {
			continue
		}
		source = append(source, ref)
		available[ref.RefID] = struct{}{}
	}
	if len(caseRunIDs) == 0 {
		return source, nil
	}
	selected := make(map[int64]struct{}, len(caseRunIDs))
	for _, caseRunID := range caseRunIDs {
		if caseRunID <= 0 {
			return nil, invalidArgumentf("case_run_ids must contain positive decimal IDs")
		}
		if _, ok := available[caseRunID]; !ok {
			return nil, invalidArgumentf("case run %d is not linked to this issue", caseRunID)
		}
		selected[caseRunID] = struct{}{}
	}
	filtered := make([]delivery.IssueRef, 0, len(selected))
	for _, ref := range source {
		if _, ok := selected[ref.RefID]; ok {
			filtered = append(filtered, ref)
		}
	}
	return filtered, nil
}

// OnRetestTerminal writes back one terminal retest case_run result. passed and
// failed map to the same issue retest_status; any other terminal status keeps
// retesting and records the reason in the append-only status history. Repeated
// calls for the same retest wave are idempotent and emit no duplicate event.
func (s *IssueService) OnRetestTerminal(ctx context.Context, waveID, caseRunID int64, status string) (delivery.Issue, error) {
	if waveID <= 0 || caseRunID <= 0 {
		return delivery.Issue{}, invalidArgumentf("wave and case run IDs must be positive decimal IDs")
	}
	if err := s.requireIssueRepository(); err != nil {
		return delivery.Issue{}, err
	}
	issue, found, err := s.repo.FindByRetestWave(ctx, waveID)
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("find issue for retest writeback", err)
	}
	if !found {
		// A terminal wave that is not a retest wave has nothing to write back.
		return delivery.Issue{}, nil
	}
	target, reason := retestOutcome(status)
	if issue.RetestStatus == target && issue.LastRetestCaseRunID != nil && *issue.LastRetestCaseRunID == caseRunID {
		return issue, nil
	}
	updated, err := s.repo.ApplyRetestResult(ctx, IssueRetestResult{
		VersionID:    issue.VersionID,
		IssueID:      issue.ID,
		WaveID:       waveID,
		CaseRunID:    caseRunID,
		TargetStatus: target,
		Reason:       reason,
		Now:          s.nowUTC(),
	})
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("write issue retest result", err)
	}
	payload := map[string]any{
		"issue_id":        formatIssueID(updated.ID),
		"version_id":      formatIssueID(updated.VersionID),
		"wave_id":         formatIssueID(waveID),
		"case_run_ids":    formatIssueIDs([]int64{caseRunID}),
		"retest_status":   updated.RetestStatus,
		"case_run_status": strings.TrimSpace(status),
	}
	auditMetadata := map[string]any{
		"version_id": formatIssueID(updated.VersionID),
		"wave_id":    formatIssueID(waveID),
		"status":     updated.RetestStatus,
	}
	switch target {
	case delivery.IssueRetestStatusPassed:
		s.publishIssueEvent(ctx, issueEventRetestCompleted, updated.VersionID, payload)
		s.recordIssueAudit(ctx, 0, issueAuditRetestCompleted, updated.ID, auditMetadata)
	case delivery.IssueRetestStatusFailed:
		s.publishIssueEvent(ctx, issueEventRetestFailed, updated.VersionID, payload)
		s.recordIssueAudit(ctx, 0, issueAuditRetestFailed, updated.ID, auditMetadata)
	}
	return updated, nil
}

// OnRetestWaveTerminal is the wave-level writeback used by the execution
// scheduler's WaveTerminalRetestNotifier. It aggregates the retest wave's linked
// case_runs (any failed => failed, all passed => passed, otherwise retesting)
// and applies one idempotent writeback.
func (s *IssueService) OnRetestWaveTerminal(ctx context.Context, waveID int64) error {
	if waveID <= 0 {
		return invalidArgumentf("wave id must be a positive decimal ID")
	}
	if err := s.requireIssueRepository(); err != nil {
		return err
	}
	if err := s.requireExecutionReader(); err != nil {
		return err
	}
	issue, found, err := s.repo.FindByRetestWave(ctx, waveID)
	if err != nil {
		return mapIssueRepositoryError("find issue for retest wave", err)
	}
	if !found {
		return nil
	}
	snapshot, err := s.exec.WaveSnapshot(ctx, issue.VersionID, waveID)
	if err != nil {
		return mapIssueRepositoryError("load retest wave snapshot", err)
	}
	linked, err := s.retestCaseRunIDs(ctx, issue.ID)
	if err != nil {
		return err
	}
	runs := make([]CaseRunSnapshot, 0, len(snapshot.CaseRuns))
	for _, run := range snapshot.CaseRuns {
		if len(linked) > 0 {
			if _, ok := linked[run.ID]; !ok {
				continue
			}
		}
		runs = append(runs, run)
	}
	if len(runs) == 0 {
		// An empty retest wave has no case_run result to write; the issue keeps
		// the retesting status set by StartRetest.
		return nil
	}
	target, representative, reason := aggregateRetestRuns(runs)
	if target == delivery.IssueRetestStatusRetesting {
		reason = representative.Status
	}
	_, err = s.OnRetestTerminal(ctx, waveID, representative.ID, reason)
	return err
}

// retestCaseRunIDs returns the case_run IDs linked with role='retest'.
func (s *IssueService) retestCaseRunIDs(ctx context.Context, issueID int64) (map[int64]struct{}, error) {
	refs, err := s.repo.ListRefs(ctx, issueID)
	if err != nil {
		return nil, mapIssueRepositoryError("list retest refs", err)
	}
	linked := make(map[int64]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Kind == delivery.IssueRefKindCaseRun && ref.Role == delivery.IssueRefRoleRetest {
			linked[ref.RefID] = struct{}{}
		}
	}
	return linked, nil
}

// aggregateRetestRuns applies the R-W2-2 outcome rule. The representative run is
// the failed run when any failed, otherwise the last passed run, otherwise the
// last non-passed run (the reason source of the "keeps retesting" case).
func aggregateRetestRuns(runs []CaseRunSnapshot) (string, CaseRunSnapshot, string) {
	var lastPassed *CaseRunSnapshot
	var lastOther *CaseRunSnapshot
	for index := range runs {
		run := runs[index]
		switch run.Status {
		case "failed":
			return delivery.IssueRetestStatusFailed, run, "failed"
		case "passed":
			lastPassed = &run
		default:
			lastOther = &run
		}
	}
	if lastOther != nil {
		return delivery.IssueRetestStatusRetesting, *lastOther, lastOther.Status
	}
	if lastPassed != nil {
		return delivery.IssueRetestStatusPassed, *lastPassed, "passed"
	}
	return delivery.IssueRetestStatusRetesting, CaseRunSnapshot{}, "unknown"
}

// retestOutcome maps a terminal case_run status onto the issue retest status and
// the optional reason recorded for the "keeps retesting" case.
func retestOutcome(status string) (string, string) {
	trimmed := strings.TrimSpace(status)
	switch trimmed {
	case "passed":
		return delivery.IssueRetestStatusPassed, ""
	case "failed":
		return delivery.IssueRetestStatusFailed, ""
	default:
		if trimmed == "" {
			trimmed = "unknown"
		}
		return delivery.IssueRetestStatusRetesting, trimmed
	}
}

// IssueDetail is the read projection of one issue: its row plus the append-only
// refs and status history and the latest triage analysis run, when one is
// attached through IssueTriageReader. MergedChain is the read-time merged_into
// target chain of a merged issue (direct target first, terminal target last);
// it is empty for a non-merged issue and never persisted (spec §4.6).
type IssueDetail struct {
	Issue       delivery.Issue
	Refs        []delivery.IssueRef
	History     []delivery.IssueStatusChange
	Triage      *delivery.AnalysisRun
	MergedChain []int64
}

// IssueTriageReader is the optional triage projection of an issue detail. It is
// satisfied by AnalysisRepository; the assembly root attaches it with
// WithTriageReader so the issue service does not depend on the analysis
// workstream. A nil reader leaves IssueDetail.Triage nil.
type IssueTriageReader interface {
	LatestByIssue(ctx context.Context, issueID int64) (delivery.AnalysisRun, bool, error)
}

// IssueService is the single server-side truth for issue candidate ingestion,
// reconciliation and lifecycle transitions (spec §5.1-§5.3). It owns no SQL and
// calls no LLM: the repository and execution ports provide persistence and the
// deterministic candidate truth, while events and audit are best-effort
// projections of the committed write.
type IssueService struct {
	repo   IssueRepository
	exec   ExecutionReader
	events EventPublisher
	audit  AuditSink
	clock  Clock
	tasks  TaskEnsurer
	triage IssueTriageReader
	// retest is the v0.22 W2-5 execution write port (intent creation + wave
	// planning). It is optional so the P4 constructor and its tests keep
	// compiling; WithRetestExecutor attaches it in the composition root.
	retest RetestExecutor

	// P6 cross-version read ports. They stay optional so the P4 constructor
	// and its tests keep compiling; WithGlobalReader attaches them.
	global GlobalIssueReader
	titles VersionTitleReader
}

// NewIssueService wires the issue use cases. Events, audit and the triage
// reader are optional; the clock defaults to the system clock.
func NewIssueService(repo IssueRepository, exec ExecutionReader, events EventPublisher, audit AuditSink, clock Clock, tasks TaskEnsurer) *IssueService {
	if clock == nil {
		clock = platformclock.System{}
	}
	return &IssueService{
		repo:   repo,
		exec:   exec,
		events: events,
		audit:  audit,
		clock:  clock,
		tasks:  tasks,
	}
}

// WithTriageReader attaches the optional triage projection returned by Get. It
// returns the service so the assembly root can chain it after construction.
func (s *IssueService) WithTriageReader(reader IssueTriageReader) *IssueService {
	if s != nil {
		s.triage = reader
	}
	return s
}

// WithRetestExecutor attaches the v0.22 W2-5 retest execution port. It returns
// the service so the assembly root can chain it after construction.
func (s *IssueService) WithRetestExecutor(executor RetestExecutor) *IssueService {
	if s != nil {
		s.retest = executor
	}
	return s
}

// ScanFailures is the reconciler hook of spec §5.1: it ingests every
// failed(application) case_run of one version that has no issue yet and
// returns how many issues were created. Per-case-run failures are collected
// and do not stop the scan; projection failures (events, audit) never fail it.
func (s *IssueService) ScanFailures(ctx context.Context, versionID int64) (int, error) {
	if versionID <= 0 {
		return 0, invalidArgumentf("version id must be a positive decimal ID")
	}
	if err := s.requireIssueRepository(); err != nil {
		return 0, err
	}
	if err := s.requireExecutionReader(); err != nil {
		return 0, err
	}
	snapshots, err := s.exec.ListFailedApplicationCaseRuns(ctx, versionID)
	if err != nil {
		return 0, mapIssueRepositoryError("scan failed application case runs", err)
	}
	createdCount := 0
	var failures []error
	for _, snapshot := range snapshots {
		// The execution read port already filters, but the reconciler stays
		// defensive: only application failures become issues (spec §5.1).
		if snapshot.FailureClass != "application" {
			continue
		}
		if snapshot.ID <= 0 {
			failures = append(failures, invalidArgumentf("case run snapshot has no ID"))
			continue
		}
		if snapshot.VersionID > 0 && snapshot.VersionID != versionID {
			failures = append(failures, invalidArgumentf(
				"case run %d belongs to version %d, not %d", snapshot.ID, snapshot.VersionID, versionID))
			continue
		}
		_, found, err := s.repo.FindByCaseRun(ctx, versionID, snapshot.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("find issue for case run %d: %w", snapshot.ID, err))
			continue
		}
		if found {
			continue
		}
		_, created, err := s.ingestSnapshot(ctx, snapshot)
		if created {
			createdCount++
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return createdCount, errors.Join(failures...)
}

// IngestCandidate builds and persists the deterministic issue candidate of one
// failed(application) case_run (spec §5.1). The bool reports whether a new issue
// row was created; a dedupe hit returns the existing issue with false and emits
// no event, audit entry or triage task.
func (s *IssueService) IngestCandidate(ctx context.Context, caseRunID int64) (delivery.Issue, bool, error) {
	if caseRunID <= 0 {
		return delivery.Issue{}, false, invalidArgumentf("case run id must be a positive decimal ID")
	}
	if err := s.requireIssueRepository(); err != nil {
		return delivery.Issue{}, false, err
	}
	if err := s.requireExecutionReader(); err != nil {
		return delivery.Issue{}, false, err
	}
	snapshot, err := s.exec.CaseRunSnapshot(ctx, caseRunID)
	if err != nil {
		return delivery.Issue{}, false, mapIssueRepositoryError("load case run snapshot", err)
	}
	if snapshot.ID <= 0 {
		snapshot.ID = caseRunID
	}
	return s.ingestSnapshot(ctx, snapshot)
}

// List returns one descending-ID issue page of a version.
func (s *IssueService) List(ctx context.Context, versionID int64, f IssueFilter) ([]delivery.Issue, error) {
	if versionID <= 0 {
		return nil, invalidArgumentf("version id must be a positive decimal ID")
	}
	if err := validateIssueFilter(f); err != nil {
		return nil, err
	}
	if err := s.requireIssueRepository(); err != nil {
		return nil, err
	}
	issues, err := s.repo.List(ctx, versionID, f)
	if err != nil {
		return nil, mapIssueRepositoryError("list issues", err)
	}
	return issues, nil
}

// Get returns one issue with its append-only refs/history and the latest triage
// analysis run when a triage reader is attached.
func (s *IssueService) Get(ctx context.Context, versionID, issueID int64) (IssueDetail, error) {
	if versionID <= 0 || issueID <= 0 {
		return IssueDetail{}, invalidArgumentf("version and issue IDs must be positive decimal IDs")
	}
	if err := s.requireIssueRepository(); err != nil {
		return IssueDetail{}, err
	}
	issue, err := s.repo.Get(ctx, versionID, issueID)
	if err != nil {
		return IssueDetail{}, mapIssueRepositoryError("get issue", err)
	}
	refs, err := s.repo.ListRefs(ctx, issueID)
	if err != nil {
		return IssueDetail{}, mapIssueRepositoryError("list issue refs", err)
	}
	history, err := s.repo.ListHistory(ctx, issueID)
	if err != nil {
		return IssueDetail{}, mapIssueRepositoryError("list issue history", err)
	}
	detail := IssueDetail{Issue: issue, Refs: refs, History: history}
	if issue.MergedIntoID != nil {
		chain, _, err := s.resolveMergeChain(ctx, issue)
		if err != nil {
			return IssueDetail{}, fmt.Errorf("resolve issue %d merge chain: %w", issueID, err)
		}
		detail.MergedChain = issueIDs(chain)
	}
	if s.triage != nil {
		run, found, err := s.triage.LatestByIssue(ctx, issueID)
		if err != nil {
			return IssueDetail{}, fmt.Errorf("load issue %d triage: %w", issueID, err)
		}
		if found {
			detail.Triage = &run
		}
	}
	return detail, nil
}

// Confirm moves an open issue to confirmed (spec §5.2).
func (s *IssueService) Confirm(ctx context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error) {
	return s.transition(ctx, issueTransition{
		versionID:      versionID,
		issueID:        issueID,
		actorID:        actorID,
		to:             delivery.IssueStatusConfirmed,
		reason:         reason,
		expected:       expected,
		conflictReason: IssueConflictNotOpen,
		eventType:      issueEventUpdated,
		auditAction:    issueAuditConfirmed,
	})
}

// Dismiss moves an open issue to dismissed (spec §5.2).
func (s *IssueService) Dismiss(ctx context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error) {
	return s.transition(ctx, issueTransition{
		versionID:      versionID,
		issueID:        issueID,
		actorID:        actorID,
		to:             delivery.IssueStatusDismissed,
		reason:         reason,
		expected:       expected,
		conflictReason: IssueConflictNotOpen,
		eventType:      issueEventUpdated,
		auditAction:    issueAuditDismissed,
	})
}

// Reopen moves a confirmed or dismissed issue back to open and increments
// reopen_count in the repository (spec §5.2). The reason is mandatory.
func (s *IssueService) Reopen(ctx context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error) {
	return s.transition(ctx, issueTransition{
		versionID:      versionID,
		issueID:        issueID,
		actorID:        actorID,
		to:             delivery.IssueStatusOpen,
		reason:         reason,
		expected:       expected,
		reasonRequired: true,
		conflictReason: IssueConflictNotReopenable,
		eventType:      issueEventReopened,
		auditAction:    issueAuditReopened,
	})
}

// Merge marks sourceID merged into a valid targetID of the same version
// (spec §5.2). The domain validator enforces self/cross-version/merged-target
// rejection and the bounded cycle walk; the repository re-checks the target
// inside its transaction. Source refs are not migrated in P4.
func (s *IssueService) Merge(ctx context.Context, versionID, sourceID, targetID, actorID int64, reason string, expected *int32) (delivery.Issue, error) {
	if versionID <= 0 || sourceID <= 0 || targetID <= 0 {
		return delivery.Issue{}, invalidArgumentf("version, source and target IDs must be positive decimal IDs")
	}
	if expected != nil && *expected <= 0 {
		return delivery.Issue{}, invalidArgumentf("expected revision must be a positive integer")
	}
	reason, err := normalizeIssueReason(reason, false)
	if err != nil {
		return delivery.Issue{}, err
	}
	if err := s.requireIssueRepository(); err != nil {
		return delivery.Issue{}, err
	}
	if sourceID == targetID {
		return delivery.Issue{}, &delivery.MergeError{Reason: delivery.MergeReasonTargetInvalid}
	}
	source, err := s.repo.Get(ctx, versionID, sourceID)
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("load merge source", err)
	}
	target, err := s.repo.Get(ctx, versionID, targetID)
	if err != nil {
		if errors.Is(err, delivery.ErrNotFound) {
			// An unknown or foreign target is a stable merge state conflict, not
			// a missing issue: the API contract reports merge_target_invalid.
			return delivery.Issue{}, &delivery.MergeError{Reason: delivery.MergeReasonTargetInvalid}
		}
		return delivery.Issue{}, mapIssueRepositoryError("load merge target", err)
	}
	lookup := func(id int64) (delivery.Issue, bool) {
		issue, err := s.repo.GetByID(ctx, id)
		if err != nil {
			return delivery.Issue{}, false
		}
		return issue, true
	}
	if err := delivery.ValidateMerge(source, target, lookup); err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("validate issue merge", err)
	}
	merged, err := s.repo.Merge(ctx, IssueMerge{
		VersionID:        versionID,
		SourceID:         sourceID,
		TargetID:         targetID,
		ActorID:          actorID,
		Reason:           reason,
		ExpectedRevision: expected,
	})
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("merge issue", err)
	}

	mergedID := merged.ID
	if mergedID <= 0 {
		mergedID = sourceID
	}
	payload := map[string]any{
		"issue_id":       formatIssueID(mergedID),
		"merged_into_id": formatIssueID(targetID),
	}
	if reason != "" {
		payload["reason"] = reason
	}
	s.publishIssueEvent(ctx, issueEventMerged, versionID, payload)
	metadata := map[string]any{"merged_into_id": formatIssueID(targetID)}
	if reason != "" {
		metadata["reason"] = reason
	}
	s.recordIssueAudit(ctx, actorID, issueAuditMerged, mergedID, metadata)
	return merged, nil
}

// resolveManualIssueInput validates the manual payload's case_run link and
// maps the execution projection onto the domain manual-issue input. A supplied
// case_run must resolve to the request's version.
func (s *IssueService) resolveManualIssueInput(ctx context.Context, versionID int64,
	title, summary, category, severity string, caseRunID int64, evidenceIDs []int64) (delivery.ManualIssueInput, error) {
	in := delivery.ManualIssueInput{
		VersionID:   versionID,
		Title:       title,
		Summary:     summary,
		Category:    category,
		Severity:    severity,
		CaseRunID:   caseRunID,
		EvidenceIDs: evidenceIDs,
	}
	if caseRunID <= 0 {
		return in, nil
	}
	if s.exec == nil {
		return in, invalidArgumentf("case_run_id requires an execution reader")
	}
	snapshot, err := s.exec.CaseRunSnapshot(ctx, caseRunID)
	if err != nil {
		return in, mapIssueRepositoryError("load manual issue case run", err)
	}
	if snapshot.ID <= 0 {
		snapshot.ID = caseRunID
	}
	if snapshot.VersionID > 0 && snapshot.VersionID != versionID {
		return in, invalidArgumentf("case run %d belongs to version %d, not %d",
			caseRunID, snapshot.VersionID, versionID)
	}
	in.CaseRunID = snapshot.ID
	in.CaseVersionID = snapshot.CaseVersionID
	in.RequirementID = snapshot.RequirementID
	in.VersionSUTID = snapshot.VersionSUTID
	return in, nil
}

// recordManualIssueCreatedAudit records the user creation audit with the same
// delivery.issue.created action as the candidate path (D52: IDs and
// classification only, never body/evidence content).
func (s *IssueService) recordManualIssueCreatedAudit(ctx context.Context, actorID int64, issue delivery.Issue) {
	metadata := map[string]any{
		"version_id": formatIssueID(issue.VersionID),
		"status":     string(issue.Status),
		"category":   issue.Category,
		"severity":   issue.Severity,
		"source":     delivery.IssueManualSource,
	}
	s.recordIssueAudit(ctx, actorID, issueAuditCreated, issue.ID, metadata)
}

// issueTransition is one status-machine action of the issue service.
type issueTransition struct {
	versionID      int64
	issueID        int64
	actorID        int64
	to             delivery.IssueStatus
	reason         string
	expected       *int32
	reasonRequired bool
	conflictReason string
	eventType      string
	auditAction    string
}

// transition validates the state machine before touching the repository, then
// persists the transition (the repository writes history and the revision bump
// atomically) and publishes the best-effort projections.
func (s *IssueService) transition(ctx context.Context, spec issueTransition) (delivery.Issue, error) {
	if spec.versionID <= 0 || spec.issueID <= 0 {
		return delivery.Issue{}, invalidArgumentf("version and issue IDs must be positive decimal IDs")
	}
	if spec.expected != nil && *spec.expected <= 0 {
		return delivery.Issue{}, invalidArgumentf("expected revision must be a positive integer")
	}
	reason, err := normalizeIssueReason(spec.reason, spec.reasonRequired)
	if err != nil {
		return delivery.Issue{}, err
	}
	if err := s.requireIssueRepository(); err != nil {
		return delivery.Issue{}, err
	}
	issue, err := s.repo.Get(ctx, spec.versionID, spec.issueID)
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("load issue", err)
	}
	if !delivery.CanTransitionIssue(issue.Status, spec.to) {
		return delivery.Issue{}, &IssueStateConflictError{Reason: spec.conflictReason}
	}
	updated, err := s.repo.UpdateStatus(ctx, IssueStatusUpdate{
		VersionID:        spec.versionID,
		IssueID:          spec.issueID,
		To:               spec.to,
		Reason:           reason,
		ActorID:          spec.actorID,
		ActorKind:        issueActorKindUser,
		ExpectedRevision: spec.expected,
	})
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("update issue status", err)
	}

	updatedID := updated.ID
	if updatedID <= 0 {
		updatedID = spec.issueID
	}
	if spec.eventType == issueEventReopened {
		s.publishIssueEvent(ctx, issueEventReopened, spec.versionID, map[string]any{
			"issue_id":     formatIssueID(updatedID),
			"reopen_count": updated.ReopenCount,
			"reason":       reason,
		})
	} else {
		payload := map[string]any{
			"issue_id":    formatIssueID(updatedID),
			"from_status": string(issue.Status),
			"to_status":   string(spec.to),
		}
		if reason != "" {
			payload["reason"] = reason
		}
		s.publishIssueEvent(ctx, spec.eventType, spec.versionID, payload)
	}
	metadata := map[string]any{
		"from_status": string(issue.Status),
		"to_status":   string(spec.to),
	}
	if reason != "" {
		metadata["reason"] = reason
	}
	if spec.eventType == issueEventReopened {
		metadata["reopen_count"] = updated.ReopenCount
	}
	s.recordIssueAudit(ctx, spec.actorID, spec.auditAction, updatedID, metadata)
	return updated, nil
}

// ingestSnapshot builds and persists the candidate of one already loaded
// case_run snapshot. It is shared by the reconciler scan and manual ingestion.
func (s *IssueService) ingestSnapshot(ctx context.Context, snapshot CaseRunSnapshot) (delivery.Issue, bool, error) {
	issue, refs, err := delivery.NewIssueCandidate(issueCandidateInput(snapshot))
	if err != nil {
		return delivery.Issue{}, false, fmt.Errorf("build issue candidate for case run %d: %w", snapshot.ID, err)
	}
	now := s.nowUTC()
	issue.CreatedAt = now
	issue.UpdatedAt = now
	stored, created, err := s.repo.CreateCandidate(ctx, issue, refs)
	if err != nil {
		return delivery.Issue{}, false, mapIssueRepositoryError("create issue candidate", err)
	}
	if !created {
		if stored.Status == delivery.IssueStatusMerged {
			// A merged source means the candidate recurs (spec §4.6): redirect
			// the new refs/count/event/audit to the merge target instead of
			// writing the source. The repository deliberately did not append
			// the candidate refs on this dedupe hit.
			redirected, err := s.redirectRecurrence(ctx, stored, issue, refs)
			return redirected, false, err
		}
		return stored, false, nil
	}
	// The adapter returns the persisted row; fall back to the snapshot version
	// only if a custom repository omitted it so projections never target 0.
	projected := stored
	if projected.VersionID <= 0 {
		projected.VersionID = snapshot.VersionID
	}
	s.publishIssueCreated(ctx, projected, snapshot)
	s.recordIssueCreatedAudit(ctx, projected)
	if s.tasks == nil {
		return stored, true, issueServiceNotConfigured("task ensurer")
	}
	if err := s.tasks.EnsureIssueTriage(ctx, projected.ID, projected.VersionID); err != nil {
		return stored, true, fmt.Errorf("ensure issue.triage for %d: %w", projected.ID, err)
	}
	return stored, true, nil
}

// issueCandidateInput maps the execution projection onto the deterministic
// candidate input of spec §5.1.
func issueCandidateInput(snapshot CaseRunSnapshot) delivery.CandidateInput {
	var waveID *int64
	if snapshot.WaveID > 0 {
		wave := snapshot.WaveID
		waveID = &wave
	}
	return delivery.CandidateInput{
		VersionID:                snapshot.VersionID,
		CaseRunID:                snapshot.ID,
		CaseVersionID:            snapshot.CaseVersionID,
		RequirementID:            snapshot.RequirementID,
		CaseTitle:                snapshot.CaseTitle,
		CaseRunSummary:           snapshot.Summary,
		StepSeq:                  snapshot.FailedStepSeq,
		AssertionSeq:             snapshot.FailedAssertionSeq,
		FailedStepResultID:       snapshot.FailedStepResultID,
		FailedAssertionResultIDs: snapshot.FailedAssertionResultIDs,
		FailureClass:             snapshot.FailureClass,
		Category:                 snapshot.Category,
		Severity:                 snapshot.Severity,
		FailedExpected:           snapshot.FailedExpected,
		FailedActual:             snapshot.FailedActual,
		FailedReason:             snapshot.FailedReason,
		DedupeHint:               snapshot.DedupeHint,
		VersionSUTID:             snapshot.VersionSUTID,
		WaveID:                   waveID,
		EvidenceIDs:              snapshot.EvidenceIDs,
	}
}

// redirectRecurrence resolves the merge target of a merged dedupe hit and
// applies the recurrence there (spec §4.6). The source issue keeps its refs and
// history untouched. A broken, cyclic, foreign-version or over-budget chain —
// or a target that changed concurrently — falls back to the pre-P7 dedupe
// behavior (append the refs to the source) and reports redirect_failed.
func (s *IssueService) redirectRecurrence(ctx context.Context, stored delivery.Issue, candidate delivery.Issue, refs []delivery.IssueRef) (delivery.Issue, error) {
	targets, ok, err := s.resolveMergeChain(ctx, stored)
	if err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("resolve issue recurrence chain", err)
	}
	if !ok || len(targets) == 0 {
		return s.fallbackRecurrence(ctx, stored, candidate, refs)
	}
	target := targets[len(targets)-1]
	recurrenceRefs := make([]delivery.IssueRef, 0, len(refs))
	for _, ref := range refs {
		ref.Role = delivery.IssueRefRoleRecurrence
		recurrenceRefs = append(recurrenceRefs, ref)
	}
	updated, err := s.repo.ApplyRecurrence(ctx, IssueRecurrence{
		VersionID: stored.VersionID,
		SourceID:  stored.ID,
		TargetID:  target.ID,
		Refs:      recurrenceRefs,
	})
	if err != nil {
		if errors.Is(err, delivery.ErrStateConflict) || errors.Is(err, delivery.ErrNotFound) {
			// The target was merged/deleted between the chain walk and the
			// write. Preserve the candidate data on the source; the event/audit
			// records the failed redirect.
			return s.fallbackRecurrence(ctx, stored, candidate, refs)
		}
		return delivery.Issue{}, mapIssueRepositoryError("apply issue recurrence", err)
	}
	s.recordIssueRecurrence(ctx, updated.VersionID, updated.ID, stored.ID, candidate.DedupeKey, updated.RecurrenceCount, false)
	return updated, nil
}

// fallbackRecurrence keeps the pre-P7 dedupe behavior when the merge chain
// cannot be redirected: the candidate refs are appended to the source (the
// repository skipped them on the merged dedupe hit) and the failure is
// projected as issue.recurred redirect_failed + audit (spec §4.6).
func (s *IssueService) fallbackRecurrence(ctx context.Context, stored delivery.Issue, candidate delivery.Issue, refs []delivery.IssueRef) (delivery.Issue, error) {
	if err := s.repo.AppendRefs(ctx, stored.ID, refs); err != nil {
		return delivery.Issue{}, mapIssueRepositoryError("append recurrent issue refs", err)
	}
	s.recordIssueRecurrence(ctx, stored.VersionID, stored.ID, stored.ID, candidate.DedupeKey, stored.RecurrenceCount, true)
	return stored, nil
}

// resolveMergeChain walks the merged_into chain of one issue with the bounded
// domain walk. A missing link is a data condition (ok=false), while an
// unexpected repository failure is returned as an error.
func (s *IssueService) resolveMergeChain(ctx context.Context, issue delivery.Issue) ([]delivery.Issue, bool, error) {
	if issue.MergedIntoID == nil {
		return nil, true, nil
	}
	var lookupErr error
	chain, ok := delivery.MergeChain(issue, func(id int64) (delivery.Issue, bool) {
		next, err := s.repo.GetByID(ctx, id)
		if err != nil {
			if !errors.Is(err, delivery.ErrNotFound) {
				lookupErr = err
			}
			return delivery.Issue{}, false
		}
		return next, true
	})
	if lookupErr != nil {
		return nil, false, lookupErr
	}
	return chain, ok, nil
}

// recordIssueRecurrence publishes issue.recurred on the version channel and
// writes the delivery.issue.recurred audit entry. Both are best effort: the
// business write already committed. On a failed redirect the source issue is
// reported (there is no target) and redirect_failed is set.
func (s *IssueService) recordIssueRecurrence(ctx context.Context, versionID, issueID, sourceID int64, dedupeKey string, recurrenceCount int, redirectFailed bool) {
	payload := map[string]any{
		"issue_id":         formatIssueID(issueID),
		"source_issue_id":  formatIssueID(sourceID),
		"dedupe_key":       dedupeKey,
		"recurrence_count": recurrenceCount,
	}
	if redirectFailed {
		payload["redirect_failed"] = true
	}
	s.publishIssueEvent(ctx, issueEventRecurred, versionID, payload)

	metadata := map[string]any{
		"issue_id":         formatIssueID(issueID),
		"source_issue_id":  formatIssueID(sourceID),
		"dedupe_key":       dedupeKey,
		"recurrence_count": recurrenceCount,
	}
	if redirectFailed {
		metadata["redirect_failed"] = true
	}
	s.recordIssueAudit(ctx, 0, issueAuditRecurred, issueID, metadata)
}

// issueIDs maps issue rows onto their IDs, preserving walk order.
func issueIDs(issues []delivery.Issue) []int64 {
	if len(issues) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

// publishIssueCreated publishes issue.created with only IDs/status/classification
// (events.md §13.2).
func (s *IssueService) publishIssueCreated(ctx context.Context, issue delivery.Issue, snapshot CaseRunSnapshot) {
	payload := map[string]any{
		"issue_id": formatIssueID(issue.ID),
		"status":   string(issue.Status),
		"category": issue.Category,
		"severity": issue.Severity,
	}
	if snapshot.WaveID > 0 {
		payload["wave_id"] = formatIssueID(snapshot.WaveID)
	}
	if snapshot.ID > 0 {
		payload["case_run_id"] = formatIssueID(snapshot.ID)
	}
	s.publishIssueEvent(ctx, issueEventCreated, issue.VersionID, payload)
}

// publishIssueEvent is best effort: the business write already committed.
func (s *IssueService) publishIssueEvent(ctx context.Context, eventType string, versionID int64, payload map[string]any) {
	if s.events == nil {
		return
	}
	_ = s.events.Publish(ctx, eventType, versionID, payload)
}

// recordIssueCreatedAudit records the system creation audit (D52: only
// id/status/classification/source, never body or evidence content).
func (s *IssueService) recordIssueCreatedAudit(ctx context.Context, issue delivery.Issue) {
	metadata := map[string]any{
		"version_id": formatIssueID(issue.VersionID),
		"status":     string(issue.Status),
		"category":   issue.Category,
		"severity":   issue.Severity,
		"source":     issue.Source,
	}
	s.recordIssueAudit(ctx, 0, issueAuditCreated, issue.ID, metadata)
}

// recordIssueAudit writes one best-effort audit entry; a nil sink or a sink
// failure never rolls the transition back.
func (s *IssueService) recordIssueAudit(ctx context.Context, actorID int64, action string, issueID int64, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Write(ctx, actorID, action, "issue", formatIssueID(issueID), metadata)
}

// normalizeIssueReason trims and bounds a transition reason. Reopen requires a
// non-empty reason; the other transitions accept an empty one (spec §5.2).
func normalizeIssueReason(reason string, required bool) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		if required {
			return "", invalidArgumentf("reason must not be empty")
		}
		return "", nil
	}
	if len([]rune(reason)) > issueReasonMax {
		return "", invalidArgumentf("reason must be at most %d characters", issueReasonMax)
	}
	return reason, nil
}

// validateIssueFilter rejects malformed list filters before they reach SQL.
func validateIssueFilter(f IssueFilter) error {
	if f.Status != "" && !delivery.IssueStatus(f.Status).Valid() {
		return invalidArgumentf("status must be one of open, confirmed, dismissed, merged")
	}
	if f.Category != "" && !validIssueFilterCategory(f.Category) {
		return invalidArgumentf("category must be one of ui, api, db")
	}
	if f.Severity != "" && !validIssueFilterSeverity(f.Severity) {
		return invalidArgumentf("severity must be one of major, minor, trivial")
	}
	if f.VersionSUTID != nil && *f.VersionSUTID <= 0 {
		return invalidArgumentf("version SUT id must be a positive decimal ID")
	}
	if f.Before < 0 {
		return invalidArgumentf("before must not be negative")
	}
	if f.Limit < 0 {
		return invalidArgumentf("limit must not be negative")
	}
	return nil
}

func validIssueFilterCategory(category string) bool {
	switch category {
	case "ui", "api", "db":
		return true
	default:
		return false
	}
}

func validIssueFilterSeverity(severity string) bool {
	switch severity {
	case "major", "minor", "trivial":
		return true
	default:
		return false
	}
}

func (s *IssueService) requireIssueRepository() error {
	if s.repo == nil {
		return issueServiceNotConfigured("issue repository")
	}
	return nil
}

func (s *IssueService) requireExecutionReader() error {
	if s.exec == nil {
		return issueServiceNotConfigured("execution reader")
	}
	return nil
}

func (s *IssueService) nowUTC() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now().UTC()
}

// formatIssueID renders a 64-bit ID as the decimal string required on the
// event/audit wire (D29).
func formatIssueID(id int64) string { return strconv.FormatInt(id, 10) }
