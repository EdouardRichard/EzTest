package main

// The version.created event is the production trigger of the automatic
// created→clarifying edge (spec §5.3). The composition root owns that wiring, so
// it is pinned here: only a version.created event with a version scope calls the
// stage machine, and a stage error never breaks event delivery.

import (
	"context"
	"errors"
	"testing"

	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

type fakeClarificationStarter struct {
	ids []int64
	err error
}

func (f *fakeClarificationStarter) StartClarification(_ context.Context, versionID int64) error {
	f.ids = append(f.ids, versionID)
	return f.err
}

func TestAdvanceCreatedVersionStartsClarification(t *testing.T) {
	starter := &fakeClarificationStarter{}
	advanceCreatedVersionToClarifying(context.Background(), starter, eventingdomain.Event{
		Type:    "version.created",
		Channel: "version:375820000000000001",
		Scope:   map[string]any{"version_id": "375820000000000001"},
	})
	if len(starter.ids) != 1 || starter.ids[0] != 375820000000000001 {
		t.Fatalf("started versions = %v, want the created version", starter.ids)
	}
}

func TestAdvanceCreatedVersionIgnoresOtherEvents(t *testing.T) {
	starter := &fakeClarificationStarter{}
	advanceCreatedVersionToClarifying(context.Background(), starter, eventingdomain.Event{
		Type:    "conversation.message_created",
		Channel: "version:375820000000000001",
		Scope:   map[string]any{"version_id": "375820000000000001"},
	})
	if len(starter.ids) != 0 {
		t.Fatalf("non-created event started clarification: %v", starter.ids)
	}

	advanceCreatedVersionToClarifying(context.Background(), starter, eventingdomain.Event{Type: "version.created"})
	if len(starter.ids) != 0 {
		t.Fatalf("unscoped created event started clarification: %v", starter.ids)
	}
}

func TestAdvanceCreatedVersionSwallowsStageErrors(t *testing.T) {
	starter := &fakeClarificationStarter{err: errors.New("stage write failed")}
	// Must not panic or abort event delivery.
	advanceCreatedVersionToClarifying(context.Background(), starter, eventingdomain.Event{
		Type:    "version.created",
		Channel: "version:375820000000000002",
		Scope:   map[string]any{"version_id": "375820000000000002"},
	})
	advanceCreatedVersionToClarifying(context.Background(), nil, eventingdomain.Event{
		Type:  "version.created",
		Scope: map[string]any{"version_id": "375820000000000002"},
	})
}
