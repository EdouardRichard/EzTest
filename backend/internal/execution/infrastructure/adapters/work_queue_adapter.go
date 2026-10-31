package adapters

import (
	"context"
	"fmt"
	"strconv"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// WorkItemEnsurer is the agent work-queue surface consumed by the execution
// adapters (*agentapp.WorkQueue satisfies it).
type WorkItemEnsurer interface {
	Ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error)
	MarkWaitingHuman(ctx context.Context, id, epoch int64) error
	Finish(ctx context.Context, id int64, reason string) error
}

// WorkQueueAdapter implements the execution WorkQueue port over the P1 agent
// work queue. Payload IDs stay decimal strings; Ensure is idempotent on
// (version_id, type, dedupe_key).
type WorkQueueAdapter struct {
	queue WorkItemEnsurer
}

// NewWorkQueueAdapter wires the execution→agent work queue adapter.
func NewWorkQueueAdapter(queue WorkItemEnsurer) *WorkQueueAdapter {
	return &WorkQueueAdapter{queue: queue}
}

var _ executionapp.WorkQueue = (*WorkQueueAdapter)(nil)

// Ensure ensures one execution task exists and returns its work item ID.
func (a *WorkQueueAdapter) Ensure(ctx context.Context, in executionapp.WorkItemInput) (int64, bool, error) {
	if a.queue == nil {
		return 0, false, errNotConfigured("agent work queue")
	}
	item, created, err := a.queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: in.VersionID,
		Team:      in.Team,
		Type:      in.Type,
		Priority:  in.Priority,
		DedupeKey: in.DedupeKey,
		Payload:   in.Payload,
	})
	if err != nil {
		return 0, false, err
	}
	return item.ID, created, nil
}

// MarkWaitingHuman parks the running execute.case item for a human takeover.
func (a *WorkQueueAdapter) MarkWaitingHuman(ctx context.Context, workItemID, epoch int64) error {
	if a.queue == nil {
		return errNotConfigured("agent work queue")
	}
	return a.queue.MarkWaitingHuman(ctx, workItemID, epoch)
}

// Finish closes the waiting_human item after the takeover is resolved.
func (a *WorkQueueAdapter) Finish(ctx context.Context, workItemID int64, reason string) error {
	if a.queue == nil {
		return errNotConfigured("agent work queue")
	}
	return a.queue.Finish(ctx, workItemID, reason)
}

// RequirementDetailReader is the version requirement read surface used to
// resolve the P2 gap dedupe suffix.
type RequirementDetailReader interface {
	Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
}

// GapEnsurerAdapter ensures a P2 design.gap work item for execution coverage
// gaps with the same dedupe convention as the P2 planner
// (gap:{requirement_id}:{case_version_id|requirement_version_id}).
type GapEnsurerAdapter struct {
	queue        WorkItemEnsurer
	requirements RequirementDetailReader
}

// NewGapEnsurer wires the execution coverage-gap adapter.
func NewGapEnsurer(queue WorkItemEnsurer, requirements RequirementDetailReader) *GapEnsurerAdapter {
	return &GapEnsurerAdapter{queue: queue, requirements: requirements}
}

var _ executionapp.GapEnsurer = (*GapEnsurerAdapter)(nil)

// EnsureGap ensures the design.gap work item without creating duplicates.
func (a *GapEnsurerAdapter) EnsureGap(ctx context.Context, versionID, requirementID, caseVersionID int64, reason string) error {
	if a.queue == nil {
		return errNotConfigured("agent work queue")
	}
	suffix := int64(0)
	payload := map[string]any{
		"requirement_id": strconv.FormatInt(requirementID, 10),
		"reason":         reason,
	}
	if caseVersionID > 0 {
		suffix = caseVersionID
		payload["case_version_id"] = strconv.FormatInt(caseVersionID, 10)
	} else if a.requirements != nil {
		detail, err := a.requirements.Get(ctx, versionID, requirementID)
		if err != nil {
			return err
		}
		suffix = detail.CurrentVersionID
		payload["requirement_version_id"] = strconv.FormatInt(detail.CurrentVersionID, 10)
	}
	if suffix <= 0 {
		return fmt.Errorf("could not resolve the requirement version of requirement %d", requirementID)
	}
	_, _, err := a.queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: versionID,
		Team:      string(agent.TeamDesign),
		Type:      agentapp.WorkTypeDesignGap,
		DedupeKey: fmt.Sprintf("gap:%d:%d", requirementID, suffix),
		Payload:   payload,
	})
	return err
}

// TeamRoomPoster is the agent team-room write surface (*agentapp.TeamRoom
// satisfies it).
type TeamRoomPoster interface {
	Post(ctx context.Context, post agent.TeamRoomPost) (agent.TeamRoomPost, error)
}

// TeamRoomAdapter implements the execution TeamRoom port over the agent
// team-room service. Posts never carry evidence bytes or credentials.
type TeamRoomAdapter struct {
	room TeamRoomPoster
}

// NewTeamRoomAdapter wires the execution team-room adapter.
func NewTeamRoomAdapter(room TeamRoomPoster) *TeamRoomAdapter {
	return &TeamRoomAdapter{room: room}
}

var _ executionapp.TeamRoom = (*TeamRoomAdapter)(nil)

// Post writes one execution finding/blocker post.
func (a *TeamRoomAdapter) Post(ctx context.Context, post executionapp.TeamPost) error {
	if a.room == nil {
		return errNotConfigured("agent team room")
	}
	refs := make([]agent.PostRef, 0, len(post.Refs))
	for _, ref := range post.Refs {
		refs = append(refs, agent.PostRef{Kind: ref.Kind, ID: ref.ID})
	}
	_, err := a.room.Post(ctx, agent.TeamRoomPost{
		VersionID: post.VersionID,
		Team:      agent.Team(post.Team),
		Kind:      agent.PostKind(post.Kind),
		Content:   post.Content,
		Refs:      refs,
		CreatedBy: post.CreatedBy,
	})
	return err
}
