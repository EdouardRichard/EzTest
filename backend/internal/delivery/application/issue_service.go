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
)

// Audit actions of the issue lifecycle (D52). Audit is best effort.
const (
	issueAuditCreated   = "delivery.issue.created"
	issueAuditConfirmed = "delivery.issue.confirmed"
	issueAuditDismissed = "delivery.issue.dismissed"
	issueAuditReopened  = "delivery.issue.reopened"
	issueAuditMerged    = "delivery.issue.merged"
)

const (
	// issueReasonMax is the issue action reason bound of the API contract.
	issueReasonMax = 2000
	// issueActorKindUser is the actor kind persisted for human transitions.
	issueActorKindUser = "user"
)

// IssueDetail is the read projection of one issue: its row plus the append-only
// refs and status history and the latest triage analysis run, when one is
// attached through IssueTriageReader.
type IssueDetail struct {
	Issue   delivery.Issue
	Refs    []delivery.IssueRef
	History []delivery.IssueStatusChange
	Triage  *delivery.AnalysisRun
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
