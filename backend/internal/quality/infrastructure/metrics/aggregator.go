// Package metrics aggregates the seven operational quality KPIs of spec §5.2.
// It consumes only the other contexts' read-only application query ports (Task
// 3b); it never runs a cross-context SQL join and never imports another
// context's infrastructure.
package metrics

import (
	"context"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// EventTypeContextCompacted is the persisted event counted by the
// context_compaction_count metric (contracts/events.md).
const EventTypeContextCompacted = "context.compacted"

// VersionPort is the version context's requirement-closure counter
// (versionapp.QueryService satisfies it).
type VersionPort interface {
	CountRequirementClosure(ctx context.Context, versionID *int64) (int, int, error)
}

// DesignPort is the design context's case-confirmation counter
// (designapp.QueryService satisfies it).
type DesignPort interface {
	CountCaseConfirmation(ctx context.Context, versionID *int64) (int, int, error)
}

// ExecutionPort is the execution context's pass-rate and evidence-completeness
// counter (executionapp.QueryService satisfies it).
type ExecutionPort interface {
	CountExecutionPass(ctx context.Context, versionID *int64) (int, int, error)
	CountEvidenceCompleteness(ctx context.Context, versionID *int64) (int, int, error)
}

// DeliveryPort is the delivery context's issue-validity counter
// (deliveryapp.QueryService satisfies it).
type DeliveryPort interface {
	CountIssueValidity(ctx context.Context, versionID *int64) (int, int, error)
}

// AgentPort is the agent context's task retry counter (agentapp.QueryService
// satisfies it).
type AgentPort interface {
	CountTaskRetries(ctx context.Context, versionID *int64) (int, int, error)
}

// EventsPort is the eventing context's typed event counter
// (eventingapp.QueryService satisfies it).
type EventsPort interface {
	CountEvents(ctx context.Context, eventType, channel string) (int, error)
}

// Aggregator builds the operational snapshot from the six read-only ports.
type Aggregator struct {
	Version   VersionPort
	Design    DesignPort
	Execution ExecutionPort
	Delivery  DeliveryPort
	Agent     AgentPort
	Events    EventsPort
}

// NewAggregator wires the operational metrics aggregator.
func NewAggregator(
	version VersionPort,
	design DesignPort,
	execution ExecutionPort,
	delivery DeliveryPort,
	agent AgentPort,
	events EventsPort,
) *Aggregator {
	return &Aggregator{
		Version:   version,
		Design:    design,
		Execution: execution,
		Delivery:  delivery,
		Agent:     agent,
		Events:    events,
	}
}

// Snapshot returns the seven metrics in the contract's stable order. A zero
// denominator yields available=false with a nil value; context_compaction_count
// is a count (denominator 1) and stays available for a real zero. Any port
// error is returned to the caller unchanged (wrapped) so the overview endpoint
// can fail honestly instead of showing fabricated numbers.
func (a *Aggregator) Snapshot(ctx context.Context, versionID *int64) (quality.OperationalSnapshot, error) {
	if versionID != nil && *versionID <= 0 {
		return quality.OperationalSnapshot{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if a == nil || a.Version == nil || a.Design == nil || a.Execution == nil ||
		a.Delivery == nil || a.Agent == nil || a.Events == nil {
		return quality.OperationalSnapshot{}, platform.Unavailable("quality operational metric ports are not configured")
	}

	closureNum, closureDenom, err := a.Version.CountRequirementClosure(ctx, versionID)
	if err != nil {
		return quality.OperationalSnapshot{}, fmt.Errorf("requirement closure metric: %w", err)
	}
	confirmationNum, confirmationDenom, err := a.Design.CountCaseConfirmation(ctx, versionID)
	if err != nil {
		return quality.OperationalSnapshot{}, fmt.Errorf("case confirmation metric: %w", err)
	}
	passNum, passDenom, err := a.Execution.CountExecutionPass(ctx, versionID)
	if err != nil {
		return quality.OperationalSnapshot{}, fmt.Errorf("execution pass metric: %w", err)
	}
	validityNum, validityDenom, err := a.Delivery.CountIssueValidity(ctx, versionID)
	if err != nil {
		return quality.OperationalSnapshot{}, fmt.Errorf("issue validity metric: %w", err)
	}
	evidenceNum, evidenceDenom, err := a.Execution.CountEvidenceCompleteness(ctx, versionID)
	if err != nil {
		return quality.OperationalSnapshot{}, fmt.Errorf("evidence completeness metric: %w", err)
	}
	channel := ""
	if versionID != nil {
		channel = fmt.Sprintf("version:%d", *versionID)
	}
	compactionCount, err := a.Events.CountEvents(ctx, EventTypeContextCompacted, channel)
	if err != nil {
		return quality.OperationalSnapshot{}, fmt.Errorf("context compaction metric: %w", err)
	}
	retryNum, retryDenom, err := a.Agent.CountTaskRetries(ctx, versionID)
	if err != nil {
		return quality.OperationalSnapshot{}, fmt.Errorf("task retry metric: %w", err)
	}

	return quality.OperationalSnapshot{Metrics: []quality.OperationalMetric{
		quality.NewOperationalMetric(quality.OperationalRequirementClosureRate, closureNum, closureDenom),
		quality.NewOperationalMetric(quality.OperationalCaseConfirmationRate, confirmationNum, confirmationDenom),
		quality.NewOperationalMetric(quality.OperationalExecutionPassRate, passNum, passDenom),
		quality.NewOperationalMetric(quality.OperationalIssueValidityRate, validityNum, validityDenom),
		quality.NewOperationalMetric(quality.OperationalEvidenceCompletenessRate, evidenceNum, evidenceDenom),
		quality.NewOperationalCount(quality.OperationalContextCompactionCount, compactionCount),
		quality.NewOperationalMetric(quality.OperationalTaskFailureRetryRate, retryNum, retryDenom),
	}}, nil
}

var _ qualityapp.OperationalSource = (*Aggregator)(nil)
