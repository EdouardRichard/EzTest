package versionapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Version close/reopen event types and reopen triggers (events.md §13,
// spec §7.5).
const (
	versionEventClosed   = "version.closed"
	versionEventReopened = "version.reopened"
)

// Version reopen triggers accepted by ReopenIfCompleted. The five values are
// frozen by the event contract.
const (
	VersionReopenTriggerRequirementCreated  = "requirement_created"
	VersionReopenTriggerRequirementReopened = "requirement_reopened"
	VersionReopenTriggerCaseConfirmed       = "case_confirmed"
	VersionReopenTriggerExecutionIntent     = "execution_intent"
	VersionReopenTriggerManual              = "manual"
)

// VersionCloseServiceDeps groups the version close service's consumer ports.
// The readers are implemented by the version/agent/execution/delivery
// application services and wired by the assembly root; audit, events, the
// report staler and the clock are optional (best effort / system default).
type VersionCloseServiceDeps struct {
	Versions      version.VersionCloseRepository
	WorkItems     VersionWorkItemReader
	Execution     VersionExecutionReader
	HumanRequests VersionHumanRequestReader
	Requirements  VersionRequirementReader
	Reports       ReportStatusReader
	ReportStaler  VersionReportStaler
	Tasks         VersionCloseTaskEnsurer
	Audit         auditapp.Recorder
	Events        eventingapp.Publisher
	Clock         clock.Clock
}

// VersionCloseService is the single server-side truth for version closure and
// reopening (spec §7.3-§7.5). Close recomputes the predicate inside the version
// row-lock transaction; EnsureVersionClose is the reconciler hook;
// ReopenIfCompleted is the shared reopen path used by requirement/design/intent
// hooks and the manual API.
type VersionCloseService struct {
	versions     version.VersionCloseRepository
	work         VersionWorkItemReader
	execution    VersionExecutionReader
	human        VersionHumanRequestReader
	requirements VersionRequirementReader
	reports      ReportStatusReader
	staler       VersionReportStaler
	tasks        VersionCloseTaskEnsurer
	audit        auditapp.Recorder
	events       eventingapp.Publisher
	clock        clock.Clock
}

// NewVersionCloseService wires the version close service. ReportStaler, audit
// and events may be nil in unit tests; the clock defaults to the system clock.
func NewVersionCloseService(deps VersionCloseServiceDeps) *VersionCloseService {
	clk := deps.Clock
	if clk == nil {
		clk = clock.System{}
	}
	return &VersionCloseService{
		versions:     deps.Versions,
		work:         deps.WorkItems,
		execution:    deps.Execution,
		human:        deps.HumanRequests,
		requirements: deps.Requirements,
		reports:      deps.Reports,
		staler:       deps.ReportStaler,
		tasks:        deps.Tasks,
		audit:        deps.Audit,
		events:       deps.Events,
		clock:        clk,
	}
}

// Evaluate recomputes the close predicate of one version without changing any
// state. It backs the version.close_read Steward tool.
func (s *VersionCloseService) Evaluate(ctx context.Context, versionID int64) (version.ClosePredicate, error) {
	input, err := s.loadInput(ctx, versionID)
	if err != nil {
		return version.ClosePredicate{}, err
	}
	return version.EvaluateVersionClose(input), nil
}

// EnsureVersionClose is the reconciler hook: when every close condition passes
// it idempotently ensures the version.close steward task for the current
// version report revision and predicate fingerprint. A failing predicate is not
// an error (no task is created); port failures propagate.
func (s *VersionCloseService) EnsureVersionClose(ctx context.Context, versionID int64) error {
	if versionID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if s.tasks == nil {
		return errors.New("version close service is missing its task ensurer")
	}
	input, err := s.loadInput(ctx, versionID)
	if err != nil {
		return err
	}
	if !version.EvaluateVersionClose(input).Passed() {
		return nil
	}
	return s.tasks.EnsureVersionClose(ctx, versionID, input.Report.RevisionID, version.VersionCloseFingerprint(input))
}

// Close recomputes the close predicate inside the version row-lock transaction
// and, when every condition passes, flips running -> completed, writes the close
// projection, publishes version.closed to version:{id} and system and records
// the audit entry. A rejected predicate returns
// *version.ErrVersionClosePredicateFailed and changes nothing; a version that is
// not running is a state conflict.
func (s *VersionCloseService) Close(ctx context.Context, versionID, runID int64, reason string) (version.Version, error) {
	if versionID <= 0 {
		return version.Version{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return version.Version{}, platform.Invalid("reason", "must not be empty")
	}
	if len([]rune(reason)) > version.VersionCloseReasonMax {
		return version.Version{}, platform.Invalid("reason", fmt.Sprintf(
			"must be at most %d characters", version.VersionCloseReasonMax))
	}
	if err := s.requireEvaluationDeps(); err != nil {
		return version.Version{}, err
	}

	now := s.clock.Now()
	var closeRunID *int64
	if runID > 0 {
		closeRunID = &runID
	}
	var evaluated version.VersionCloseInput
	closed, err := s.versions.Close(ctx, versionID, version.VersionCloseFields{
		ClosedAt:    now,
		CloseReason: reason,
		CloseRunID:  closeRunID,
	}, func(_ context.Context, locked version.Version) error {
		// Re-evaluate while the version row is locked. A concurrent intent,
		// wave, human request or requirement change committed before this
		// point is visible here and rejects the close.
		input, err := s.buildInput(ctx, locked)
		if err != nil {
			return err
		}
		if predicate := version.EvaluateVersionClose(input); !predicate.Passed() {
			return &version.ErrVersionClosePredicateFailed{Failed: version.VersionCloseFailureCounts(input)}
		}
		evaluated = input
		return nil
	})
	if err != nil {
		return version.Version{}, mapVersionCloseRepositoryError(err)
	}

	versionIDString := platformid.Format(closed.ID)
	payload := map[string]any{
		"version_id":         versionIDString,
		"report_revision_id": platformid.Format(evaluated.Report.RevisionID),
		"closed_at":          closed.ClosedAt.UTC().Format(closeTimeLayout),
		"close_reason":       closed.CloseReason,
	}
	if closed.CloseRunID != nil {
		payload["close_run_id"] = platformid.Format(*closed.CloseRunID)
	}
	s.publishVersionStatusEvent(ctx, closed.ID, versionEventClosed, payload)
	s.recordVersionAudit(ctx, versionEventClosed, closed.ID, map[string]any{
		"from_status":           version.StatusRunning,
		"to_status":             version.StatusCompleted,
		"report_revision_id":    platformid.Format(evaluated.Report.RevisionID),
		"closed_at":             closed.ClosedAt.UTC().Format(closeTimeLayout),
		"close_reason":          closed.CloseReason,
		"close_run_id":          payload["close_run_id"],
		"predicate_fingerprint": version.VersionCloseFingerprint(evaluated),
	})
	return closed, nil
}

// ReopenIfCompleted moves a completed version back to running, clears the close
// fields, marks the latest version report stale and publishes version.reopened
// to version:{id} and system. A version that is not completed is a no-op. The
// report stale projection and event delivery are best effort; the status
// transition is the truth and is never rolled back by them.
func (s *VersionCloseService) ReopenIfCompleted(ctx context.Context, versionID int64, trigger, reason string) error {
	if versionID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if !validVersionReopenTrigger(trigger) {
		return platform.Invalid("trigger", "must be one of requirement_created, requirement_reopened, case_confirmed, execution_intent, manual")
	}
	reason = strings.TrimSpace(reason)
	if trigger == VersionReopenTriggerManual && reason == "" {
		return platform.Invalid("reason", "must not be empty")
	}
	if len([]rune(reason)) > version.VersionReopenReasonMax {
		return platform.Invalid("reason", fmt.Sprintf(
			"must be at most %d characters", version.VersionReopenReasonMax))
	}
	if s.versions == nil {
		return errors.New("version close service is missing its version repository")
	}

	reopened, changed, err := s.versions.ReopenIfCompleted(ctx, versionID, s.clock.Now())
	if err != nil {
		return mapVersionCloseRepositoryError(err)
	}
	if !changed {
		return nil
	}

	if s.staler != nil {
		// The projection already committed; a stale-mark failure must not roll
		// the reopen back. The reconciler retries it.
		_ = s.staler.MarkVersionReportStale(ctx, versionID, reason)
	}
	versionIDString := platformid.Format(reopened.ID)
	payload := map[string]any{
		"version_id": versionIDString,
		"trigger":    trigger,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	s.publishVersionStatusEvent(ctx, reopened.ID, versionEventReopened, payload)
	s.recordVersionAudit(ctx, versionEventReopened, reopened.ID, map[string]any{
		"from_status": version.StatusCompleted,
		"to_status":   version.StatusRunning,
		"trigger":     trigger,
		"reason":      reason,
	})
	return nil
}

// loadInput loads the version and evaluates the close projection from the
// consumer ports.
func (s *VersionCloseService) loadInput(ctx context.Context, versionID int64) (version.VersionCloseInput, error) {
	if versionID <= 0 {
		return version.VersionCloseInput{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if err := s.requireEvaluationDeps(); err != nil {
		return version.VersionCloseInput{}, err
	}
	stored, _, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return version.VersionCloseInput{}, mapVersionCloseRepositoryError(err)
	}
	return s.buildInput(ctx, stored)
}

// buildInput recomputes the close projection of one already loaded version. The
// version.close task itself is subtracted from the blocking work item count
// here (self-task exclusion, spec §7.3 check 2).
func (s *VersionCloseService) buildInput(ctx context.Context, stored version.Version) (version.VersionCloseInput, error) {
	requirements, err := s.requirements.ListByVersion(ctx, stored.ID)
	if err != nil {
		return version.VersionCloseInput{}, fmt.Errorf("version close %d: load requirements: %w", stored.ID, err)
	}
	workCounts, err := s.work.BlockingWorkItems(ctx, stored.ID)
	if err != nil {
		return version.VersionCloseInput{}, fmt.Errorf("version close %d: load work items: %w", stored.ID, err)
	}
	activeIntents, err := s.execution.CountActiveIntents(ctx, stored.ID)
	if err != nil {
		return version.VersionCloseInput{}, fmt.Errorf("version close %d: count intents: %w", stored.ID, err)
	}
	openHumanRequests, err := s.human.CountOpenHumanRequests(ctx, stored.ID)
	if err != nil {
		return version.VersionCloseInput{}, fmt.Errorf("version close %d: count human requests: %w", stored.ID, err)
	}
	activeWaves, err := s.execution.CountActiveWaves(ctx, stored.ID)
	if err != nil {
		return version.VersionCloseInput{}, fmt.Errorf("version close %d: count active waves: %w", stored.ID, err)
	}
	inflightRuns, err := s.execution.CountInflightCaseRuns(ctx, stored.ID)
	if err != nil {
		return version.VersionCloseInput{}, fmt.Errorf("version close %d: count in-flight case runs: %w", stored.ID, err)
	}
	report, err := s.reports.VersionReportStatus(ctx, stored.ID)
	if err != nil {
		return version.VersionCloseInput{}, fmt.Errorf("version close %d: load version report: %w", stored.ID, err)
	}
	blockingWorkItems := workCounts.Total - workCounts.VersionClose
	if blockingWorkItems < 0 {
		blockingWorkItems = 0
	}
	return version.VersionCloseInput{
		Version:           stored,
		BlockingWorkItems: blockingWorkItems,
		ActiveIntents:     activeIntents,
		OpenHumanRequests: openHumanRequests,
		ActiveWaves:       activeWaves,
		InflightCaseRuns:  inflightRuns,
		Requirements:      requirements,
		Report:            report,
	}, nil
}

func (s *VersionCloseService) requireEvaluationDeps() error {
	switch {
	case s.versions == nil:
		return errors.New("version close service is missing its version repository")
	case s.work == nil:
		return errors.New("version close service is missing its work item reader")
	case s.execution == nil:
		return errors.New("version close service is missing its execution reader")
	case s.human == nil:
		return errors.New("version close service is missing its human request reader")
	case s.requirements == nil:
		return errors.New("version close service is missing its requirement reader")
	case s.reports == nil:
		return errors.New("version close service is missing its report status reader")
	default:
		return nil
	}
}

// closeTimeLayout is the event/audit timestamp wire format (UTC RFC3339).
const closeTimeLayout = "2006-01-02T15:04:05Z07:00"

// publishVersionStatusEvent publishes a version status event to the version
// channel and to system (P0 version-state convention). Delivery is best effort.
func (s *VersionCloseService) publishVersionStatusEvent(ctx context.Context, versionID int64, eventType string, payload map[string]any) {
	if s.events == nil {
		return
	}
	versionIDString := platformid.Format(versionID)
	for _, channel := range []string{"version:" + versionIDString, "system"} {
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   channel,
			Type:      eventType,
			Scope:     map[string]any{"version_id": versionIDString},
			Payload:   payload,
			RequestID: requestid.From(ctx),
		})
	}
}

// recordVersionAudit records the best-effort audit entry. The actor comes from
// the authenticated context when present (manual API); agent-run closes carry no
// HTTP actor and leave it nil.
func (s *VersionCloseService) recordVersionAudit(ctx context.Context, action string, versionID int64, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	var actor *int64
	if authenticated, ok := platform.ActorFrom(ctx); ok && authenticated.ID > 0 {
		actorID := authenticated.ID
		actor = &actorID
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actor,
		Action:     action,
		TargetKind: "version",
		TargetID:   platformid.Format(versionID),
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}

func validVersionReopenTrigger(trigger string) bool {
	switch trigger {
	case VersionReopenTriggerRequirementCreated,
		VersionReopenTriggerRequirementReopened,
		VersionReopenTriggerCaseConfirmed,
		VersionReopenTriggerExecutionIntent,
		VersionReopenTriggerManual:
		return true
	default:
		return false
	}
}

func mapVersionCloseRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrNotFound):
		return platform.NotFound("version")
	case errors.Is(err, version.ErrStateConflict):
		return platform.StateConflict("version is not running")
	default:
		return err
	}
}
