// Package deliveryadapter adapts the agent-facing consumer ports onto the delivery and
// work-queue application services inside the composition root boundary. It owns
// no business logic: every method delegates to an application service and only
// maps the port shapes (payloads/dedupe keys).
package deliveryadapter

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Task types and teams of the delivery task table (contracts/task-types.md §6).
// GD-17 (v0.2.4-G) removed requirement.close/version.close and the steward team:
// closure is a deterministic reconciler action, not an agent work item.
// T11e-4: compile-time aliases of the agent work-type authority
// (application/ports_agent.go WorkType*), not a second hand-written value table.
const (
	taskIssueTriage   = agentapp.WorkTypeIssueTriage
	taskReportWave    = agentapp.WorkTypeReportWave
	taskReportVersion = agentapp.WorkTypeReportVersion

	teamDelivery = string(agent.TeamDelivery)
)

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

// TaskEnsurer implements deliveryapp.TaskEnsurer over the agent work queue. One
// value is passed to the delivery issue and report services.
type TaskEnsurer struct {
	queue   Queue
	reports ReportReader
}

// NewTaskEnsurer wires the delivery task ensurer.
func NewTaskEnsurer(queue Queue, reports ReportReader) *TaskEnsurer {
	return &TaskEnsurer{queue: queue, reports: reports}
}

var _ deliveryapp.TaskEnsurer = (*TaskEnsurer)(nil)

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
		return errors.New("delivery task ensurer is missing its report reader")
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

// ensure delegates to the work queue, guarding a nil ensurer.
func (e *TaskEnsurer) ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	if e == nil || e.queue == nil {
		return agent.WorkItem{}, false, errors.New("delivery task ensurer is missing its work queue")
	}
	return e.queue.Ensure(ctx, in)
}

// idText formats one ID as the decimal string used on the task payload
// boundary.
func idText(id int64) string { return strconv.FormatInt(id, 10) }
