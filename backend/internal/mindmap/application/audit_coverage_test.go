package application_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// graphInt64Text renders an id the way audit target ids are stored.
func graphInt64Text(id int64) string { return strconv.FormatInt(id, 10) }

// TestGraphWritesAreAudited pins the mindmap audit coverage (spec §10.4): every
// successful manual graph write records one entry naming the version and node
// ids, the kind/status/revision and the changed part only. Case titles, step
// actions, expectations and test data must never enter the audit log.
func TestGraphWritesAreAudited(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: 1, Status: "clarified"}}
	service := fixture.service()

	created, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
		Kind:           domain.NodeKindCase,
		Title:          "SECRET-NODE-TITLE",
		Description:    "SECRET-NODE-DESCRIPTION",
		RequirementIDs: []int64{7},
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	updated, err := service.UpdateNode(ctx, 1, created.ID, 42, application.UpdateNodeCommand{
		Revision: created.Revision,
		Title:    graphStringPtr("SECRET-NODE-TITLE-2"),
	})
	if err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if _, err := service.AddStep(ctx, 1, created.ID, 42, application.AddStepCommand{
		NodeRevision: updated.Revision,
		Action:       "SECRET-STEP-ACTION",
		Expected:     "SECRET-STEP-EXPECTED",
	}); err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	scenario := fixture.seedScenario(20, nil)
	if err := service.DeleteNode(ctx, 1, scenario.ID, 42, scenario.Revision); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	want := []struct {
		action     string
		changed    string
		targetType string
		targetID   string
		revision   int
	}{
		{action: "mindmap.case_created", targetType: "case_node", targetID: graphInt64Text(created.ID), revision: created.Revision},
		{action: "mindmap.case_updated", targetType: "case_node", targetID: graphInt64Text(updated.ID), revision: updated.Revision},
		// The child-collection write bumps the node revision inside the
		// transaction: the audit entry must name the post-write revision (3),
		// not the pre-write revision read into eventNode (2).
		{action: "mindmap.steps_updated", targetType: "case_node", targetID: graphInt64Text(created.ID), revision: updated.Revision + 1},
		{action: "mindmap.case_deleted", targetType: "case_node", targetID: graphInt64Text(scenario.ID), revision: scenario.Revision},
	}
	if len(fixture.audits) != len(want) {
		t.Fatalf("audit events=%d want %d: %+v", len(fixture.audits), len(want), fixture.audits)
	}
	for i, expected := range want {
		event := fixture.audits[i]
		if event.Action != expected.action {
			t.Fatalf("audits[%d].Action=%q want %q", i, event.Action, expected.action)
		}
		if event.TargetType != expected.targetType || event.TargetID != expected.targetID {
			t.Fatalf("%s target=%q/%q want %s/%s",
				expected.action, event.TargetType, event.TargetID, expected.targetType, expected.targetID)
		}
		if event.ActorID != 42 {
			t.Fatalf("%s actor=%d want 42", expected.action, event.ActorID)
		}
		if event.Metadata["version_id"] != int64(1) {
			t.Fatalf("%s metadata=%v want version_id=1", expected.action, event.Metadata)
		}
		if event.Metadata["revision"] != expected.revision {
			t.Fatalf("%s revision=%v want %d", expected.action, event.Metadata["revision"], expected.revision)
		}
	}
	if fixture.audits[0].Metadata["kind"] != domain.NodeKindCase {
		t.Fatalf("created audit metadata=%v", fixture.audits[0].Metadata)
	}
	if fixture.audits[0].Metadata["status"] != domain.NodeStatusDraft {
		t.Fatalf("created audit metadata=%v", fixture.audits[0].Metadata)
	}
	if fixture.audits[1].Metadata["revision"] == nil {
		t.Fatalf("updated audit metadata=%v want revision", fixture.audits[1].Metadata)
	}

	raw, err := json.Marshal(fixture.audits)
	if err != nil {
		t.Fatalf("marshal audits: %v", err)
	}
	for _, secret := range []string{
		"SECRET-NODE-TITLE",
		"SECRET-NODE-DESCRIPTION",
		"SECRET-STEP-ACTION",
		"SECRET-STEP-EXPECTED",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit events leak %q: %s", secret, raw)
		}
	}
}
