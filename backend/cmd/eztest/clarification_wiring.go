package main

// Clarification convergence composition. The agent runner observes that the
// clarification host asked the user to confirm the requirement drafts; the
// version stage machine owns the actual clarifying→requirement_review edge and
// the requirement service proves the gate is not empty. This adapter is the only
// production caller of StageMachine.ConvergeClarification.

import (
	"context"
	"errors"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// stageClarificationConverger adapts the version stage machine and requirement
// reader to the Runner's ClarificationConverger port. A version with no draft
// requirement is never advanced: the requirement gate would otherwise show an
// empty list.
type stageClarificationConverger struct {
	machine      *versionapp.StageMachine
	requirements p9RequirementReader
}

func (c stageClarificationConverger) Converge(ctx context.Context, versionID int64) error {
	if c.machine == nil {
		return errors.New("clarification converger: stage machine is not configured")
	}
	if c.requirements != nil {
		views, _, err := c.requirements.List(ctx, versionID, "draft", 1, nil)
		if err != nil {
			return err
		}
		if len(views) == 0 {
			return nil
		}
	}
	return c.machine.ConvergeClarification(ctx, versionID)
}

var _ agentapp.ClarificationConverger = stageClarificationConverger{}
