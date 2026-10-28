package delivery

import (
	"context"
	"errors"
	"fmt"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// RequirementGapWriter is the delivery issue application surface the execution
// worker needs for the supplement requirement-gap escalations (spec §6.4.4).
// *delivery/application.IssueManager satisfies it.
type RequirementGapWriter interface {
	ApplyRequirementGaps(ctx context.Context, versionID, batchID, actor int64,
		escalations []deliveryapp.RequirementGapEscalation) ([]deliverydomain.Issue, error)
}

// RequirementGapIssues adapts the delivery issue service to the execution
// worker's DeliveryPort. It only projects the escalation payload; validation,
// hard dedupe, events and audit stay in delivery.
type RequirementGapIssues struct {
	Issues RequirementGapWriter
}

// NewRequirementGapIssues wires the execution-side requirement-gap adapter.
func NewRequirementGapIssues(issues RequirementGapWriter) *RequirementGapIssues {
	return &RequirementGapIssues{Issues: issues}
}

var _ executionapp.DeliveryPort = (*RequirementGapIssues)(nil)

// CreateRequirementGapIssues implements executionapp.DeliveryPort: it
// materializes the run's validated requirement gaps as delivery requirement_gap
// issues and reports how many issues the materialization returned.
func (p *RequirementGapIssues) CreateRequirementGapIssues(ctx context.Context, versionID, batchID int64,
	escalations []executionapp.RequirementGapEscalation, actor int64) (int, error) {
	if p == nil || p.Issues == nil {
		return 0, errors.New("execution delivery: issue service is not configured")
	}
	if len(escalations) == 0 {
		return 0, nil
	}
	gaps := make([]deliveryapp.RequirementGapEscalation, 0, len(escalations))
	for _, escalation := range escalations {
		gaps = append(gaps, deliveryapp.RequirementGapEscalation{
			Title:          escalation.Title,
			Detail:         escalation.Detail,
			RequirementIDs: append([]int64(nil), escalation.RequirementIDs...),
		})
	}
	issues, err := p.Issues.ApplyRequirementGaps(ctx, versionID, batchID, actor, gaps)
	if err != nil {
		return 0, fmt.Errorf("execution delivery: materialize requirement-gap issues of batch %d: %w", batchID, err)
	}
	return len(issues), nil
}
