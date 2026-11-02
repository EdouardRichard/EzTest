package eventingapp

// 0.2.1 job progress events (contracts/events.md §18.1/§18.2 #4). They are the
// UI projection of a 0.2.1 work item ("job"): job_id is work_item.id, the
// lifecycle status uses the spec §5.4 work_item values, and the events go to
// the version channel so the unified SSE stream carries them without a second
// endpoint (contracts/events.md §18).
//
// The publishers here are the single place that enforces the payload shape:
// non-negative current/total, total=0 meaning "unknown" (never a fabricated
// percentage), a valid lifecycle status, and job.finished only for the terminal
// statuses done/failed/cancelled. Emission call sites (the job runtime and the
// worker write-back path) publish through PublishJobProgress/PublishJobFinished.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// The two 0.2.1 job event types.
const (
	EventJobProgress = "job.progress"
	EventJobFinished = "job.finished"
)

// Work item lifecycle statuses from spec §5.4. They are the only valid values
// of the event payload's status field (events.md §18.2 #4).
const (
	JobStatusOpen         = "open"
	JobStatusClaimed      = "claimed"
	JobStatusRunning      = "running"
	JobStatusWaitingHuman = "waiting_human"
	JobStatusDone         = "done"
	JobStatusFailed       = "failed"
	JobStatusCancelled    = "cancelled"
)

// ValidJobStatus reports whether status is one of the spec §5.4 lifecycle
// values. Unknown values are rejected rather than silently emitted.
func ValidJobStatus(status string) bool {
	switch status {
	case JobStatusOpen, JobStatusClaimed, JobStatusRunning, JobStatusWaitingHuman,
		JobStatusDone, JobStatusFailed, JobStatusCancelled:
		return true
	default:
		return false
	}
}

// JobProgress is the payload of one job.progress event. Message and UpdatedAt
// are optional (events.md §18.1 optional fields).
type JobProgress struct {
	JobID     int64
	Kind      string
	Current   int
	Total     int
	Status    string
	Message   string
	UpdatedAt string
}

// Validate checks the contract constraints (required fields, non-negative
// counters, known lifecycle status).
func (p JobProgress) Validate() error {
	if p.JobID <= 0 {
		return platform.Invalid("job_id", "must be a positive decimal work item ID")
	}
	if strings.TrimSpace(p.Kind) == "" {
		return platform.Invalid("kind", "must not be empty")
	}
	if p.Current < 0 {
		return platform.Invalid("current", "must be non-negative")
	}
	if p.Total < 0 {
		return platform.Invalid("total", "must be non-negative (zero means unknown)")
	}
	if !ValidJobStatus(p.Status) {
		return platform.Invalid("status", "must be one of open, claimed, running, waiting_human, done, failed, cancelled")
	}
	return nil
}

// Event renders the version-channel job.progress event. The payload uses the
// contract field names; IDs are decimal strings like every other business ID.
func (p JobProgress) Event(versionID int64) (Event, error) {
	if versionID <= 0 {
		return Event{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if err := p.Validate(); err != nil {
		return Event{}, err
	}
	version := strconv.FormatInt(versionID, 10)
	job := strconv.FormatInt(p.JobID, 10)
	payload := map[string]any{
		"job_id":  job,
		"kind":    strings.TrimSpace(p.Kind),
		"current": p.Current,
		"total":   p.Total,
		"status":  p.Status,
	}
	if message := strings.TrimSpace(p.Message); message != "" {
		payload["message"] = message
	}
	if updatedAt := strings.TrimSpace(p.UpdatedAt); updatedAt != "" {
		payload["updated_at"] = updatedAt
	}
	return Event{
		Channel: "version:" + version,
		Type:    EventJobProgress,
		Scope:   map[string]any{"version_id": version, "job_id": job},
		Payload: payload,
	}, nil
}

// JobFinished is the payload of the one-time terminal job.finished event.
// Error and Attempt are optional (events.md §18.1 optional fields).
type JobFinished struct {
	JobID     int64
	Kind      string
	Status    string
	ResultRef string
	Error     string
	Attempt   int
}

// Validate checks the terminal-event constraints: a positive job, a kind, one
// of the three terminal statuses and a non-negative attempt.
func (f JobFinished) Validate() error {
	if f.JobID <= 0 {
		return platform.Invalid("job_id", "must be a positive decimal work item ID")
	}
	if strings.TrimSpace(f.Kind) == "" {
		return platform.Invalid("kind", "must not be empty")
	}
	switch f.Status {
	case JobStatusDone, JobStatusFailed, JobStatusCancelled:
	default:
		return platform.Invalid("status", "must be one of the terminal statuses done, failed, cancelled")
	}
	if f.Attempt < 0 {
		return platform.Invalid("attempt", "must be non-negative")
	}
	return nil
}

// Event renders the version-channel job.finished event. result_ref is always
// present (it may be empty for failed/cancelled jobs) because the contract
// lists it as a required payload field.
func (f JobFinished) Event(versionID int64) (Event, error) {
	if versionID <= 0 {
		return Event{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if err := f.Validate(); err != nil {
		return Event{}, err
	}
	version := strconv.FormatInt(versionID, 10)
	job := strconv.FormatInt(f.JobID, 10)
	payload := map[string]any{
		"job_id":     job,
		"kind":       strings.TrimSpace(f.Kind),
		"status":     f.Status,
		"result_ref": f.ResultRef,
	}
	if message := strings.TrimSpace(f.Error); message != "" {
		payload["error"] = message
	}
	if f.Attempt > 0 {
		payload["attempt"] = f.Attempt
	}
	return Event{
		Channel: "version:" + version,
		Type:    EventJobFinished,
		Scope:   map[string]any{"version_id": version, "job_id": job},
		Payload: payload,
	}, nil
}

// PublishJobProgress validates and publishes one job.progress event through the
// shared publisher. A validation error is returned to the emitter; publish
// errors follow the eventing contract (best effort, the DB remains the truth).
func PublishJobProgress(ctx context.Context, publisher Publisher, versionID int64, progress JobProgress) error {
	if publisher == nil {
		return fmt.Errorf("publish %s: publisher is not configured", EventJobProgress)
	}
	event, err := progress.Event(versionID)
	if err != nil {
		return err
	}
	return publisher.Publish(ctx, event)
}

// PublishJobFinished validates and publishes one terminal job.finished event.
func PublishJobFinished(ctx context.Context, publisher Publisher, versionID int64, finished JobFinished) error {
	if publisher == nil {
		return fmt.Errorf("publish %s: publisher is not configured", EventJobFinished)
	}
	event, err := finished.Event(versionID)
	if err != nil {
		return err
	}
	return publisher.Publish(ctx, event)
}
