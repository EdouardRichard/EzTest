// Package p4 adapts the P4 agent-facing consumer ports onto the delivery,
// version and work-queue application services inside the composition root
// boundary. It owns no business logic: every method delegates to an
// application service and only maps the port shapes (payloads/dedupe keys).
package p4

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// Task types and teams of the P4 task table (contracts/task-types.md §6).
const (
	taskIssueTriage      = "issue.triage"
	taskReportWave       = "report.wave"
	taskReportVersion    = "report.version"
	taskRequirementClose = "requirement.close"
	taskVersionClose     = "version.close"

	teamDelivery = "delivery"
	teamSteward  = "steward"
)

// requirementCloseMaxAttempts keeps the Steward's requirement.close task alive
// while sibling work (design.gap/analyze.failure/issue.triage) drains. A close
// attempt rejected with no_open_work is transient; with the default 3 attempts
// the task can die before those siblings finish and no newer close task is
// ensured for the same close round.
const requirementCloseMaxAttempts = 30

// Queue ensures an idempotent work item (agentapp.WorkQueue satisfies it).
type Queue interface {
	Ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error)
}

// ReportReader resolves the report kind/scope of a report task.
// (*deliverypg.ReportRepository satisfies it through the delivery application
// port.)
type ReportReader interface {
	GetReportByID(ctx context.Context, reportID int64) (delivery.Report, error)
}

// RequirementReader resolves the owning version of a requirement so the
// requirement.close work item is version-scoped. The version requirement
// repository satisfies it through a composition-root adapter.
type RequirementReader interface {
	GetRequirementVersionID(ctx context.Context, requirementID int64) (int64, error)
}

// TaskEnsurer implements deliveryapp.TaskEnsurer plus the version closure/close
// task-ensurer ports over the agent work queue. One value is passed to the
// delivery report service, the closure service and the version close service.
type TaskEnsurer struct {
	queue        Queue
	reports      ReportReader
	requirements RequirementReader
}

// NewTaskEnsurer wires the P4 task ensurer.
func NewTaskEnsurer(queue Queue, reports ReportReader, requirements RequirementReader) *TaskEnsurer {
	return &TaskEnsurer{queue: queue, reports: reports, requirements: requirements}
}

var (
	_ deliveryapp.TaskEnsurer            = (*TaskEnsurer)(nil)
	_ versionapp.ClosureTaskEnsurer      = (*TaskEnsurer)(nil)
	_ versionapp.VersionCloseTaskEnsurer = (*TaskEnsurer)(nil)
)

// EnsureIssueTriage ensures issue.triage with the frozen triage:{issue_id}
// dedupe key.
func (e *TaskEnsurer) EnsureIssueTriage(ctx context.Context, issueID, versionID int64) error {
	if issueID <= 0 || versionID <= 0 {
		return platform.Invalid("issue_id", "must be positive decimal IDs")
	}
	_, _, err := e.ensure(ctx, agent.NewWorkItem{
		VersionID: versionID,
		Team:      teamDelivery,
		Type:      taskIssueTriage,
		DedupeKey: "triage:" + idText(issueID),
		Payload: map[string]any{
			"issue_id":   idText(issueID),
			"version_id": idText(versionID),
		},
	})
	return err
}

// EnsureReportTask ensures report.wave/report.version for one report revision
// with the frozen report:{report_id}:r{revision} dedupe key.
func (e *TaskEnsurer) EnsureReportTask(ctx context.Context, reportID int64, revision int32) error {
	if reportID <= 0 || revision <= 0 {
		return platform.Invalid("report_id", "must be a positive decimal ID and revision a positive integer")
	}
	if e == nil || e.reports == nil {
		return errors.New("p4 task ensurer is missing its report reader")
	}
	report, err := e.reports.GetReportByID(ctx, reportID)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"report_id": idText(report.ID),
		"revision":  revision,
	}
	workType := ""
	switch report.Kind {
	case delivery.ReportKindWave:
		if report.WaveID == nil || *report.WaveID <= 0 {
			return fmt.Errorf("report %d has no wave", report.ID)
		}
		workType = taskReportWave
		payload["wave_id"] = idText(*report.WaveID)
	case delivery.ReportKindVersion:
		workType = taskReportVersion
		payload["version_id"] = idText(report.VersionID)
	default:
		return fmt.Errorf("report %d has an unknown kind %q", report.ID, report.Kind)
	}
	_, _, err = e.ensure(ctx, agent.NewWorkItem{
		VersionID: report.VersionID,
		Team:      teamDelivery,
		Type:      workType,
		DedupeKey: "report:" + idText(report.ID) + ":r" + strconv.FormatInt(int64(revision), 10),
		Payload:   payload,
	})
	return err
}

// EnsureRequirementClose ensures requirement.close for one requirement version
// and close round.
func (e *TaskEnsurer) EnsureRequirementClose(ctx context.Context, requirementID, requirementVersionID int64, closeRound int32) error {
	if requirementID <= 0 || requirementVersionID <= 0 || closeRound <= 0 {
		return platform.Invalid("requirement_id", "must be positive decimal IDs and a positive close round")
	}
	if e == nil || e.requirements == nil {
		return errors.New("p4 task ensurer is missing its requirement reader")
	}
	versionID, err := e.requirements.GetRequirementVersionID(ctx, requirementID)
	if err != nil {
		return err
	}
	_, _, err = e.ensure(ctx, agent.NewWorkItem{
		VersionID:   versionID,
		Team:        teamSteward,
		Type:        taskRequirementClose,
		MaxAttempts: requirementCloseMaxAttempts,
		DedupeKey:   "req-close:" + idText(requirementID) + ":" + idText(requirementVersionID) + ":" + idText(int64(closeRound)),
		Payload: map[string]any{
			"requirement_id":         idText(requirementID),
			"requirement_version_id": idText(requirementVersionID),
			"close_round":            closeRound,
		},
	})
	return err
}

// EnsureVersionClose ensures version.close for the current predicate
// fingerprint.
func (e *TaskEnsurer) EnsureVersionClose(ctx context.Context, versionID, reportRevisionID int64, predicateFingerprint string) error {
	if versionID <= 0 || reportRevisionID <= 0 || predicateFingerprint == "" {
		return platform.Invalid("version_id", "must be a positive decimal ID and the predicate fingerprint must not be empty")
	}
	_, _, err := e.ensure(ctx, agent.NewWorkItem{
		VersionID: versionID,
		Team:      teamSteward,
		Type:      taskVersionClose,
		DedupeKey: "ver-close:" + idText(versionID) + ":" + idText(reportRevisionID) + ":" + predicateFingerprint,
		Payload: map[string]any{
			"version_id":            idText(versionID),
			"report_revision_id":    idText(reportRevisionID),
			"predicate_fingerprint": predicateFingerprint,
		},
	})
	return err
}

// ensure delegates to the work queue, guarding a nil ensurer.
func (e *TaskEnsurer) ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	if e == nil || e.queue == nil {
		return agent.WorkItem{}, false, errors.New("p4 task ensurer is missing its work queue")
	}
	return e.queue.Ensure(ctx, in)
}

// idText formats one ID as the decimal string used on the task payload
// boundary.
func idText(id int64) string { return strconv.FormatInt(id, 10) }
