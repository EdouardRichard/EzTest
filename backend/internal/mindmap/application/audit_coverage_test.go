package application_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

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
	}{
		{action: "mindmap.case_created", targetType: "case_node"},
		{action: "mindmap.case_updated", targetType: "case_node"},
		{action: "mindmap.steps_updated", targetType: "case_node"},
		{action: "mindmap.case_deleted", targetType: "case_node"},
	}
	if len(fixture.audits) != len(want) {
		t.Fatalf("audit events=%d want %d: %+v", len(fixture.audits), len(want), fixture.audits)
	}
	for i, expected := range want {
		event := fixture.audits[i]
		if event.Action != expected.action {
			t.Fatalf("audits[%d].Action=%q want %q", i, event.Action, expected.action)
		}
		if event.TargetType != expected.targetType || event.TargetID == "" {
			t.Fatalf("%s target=%q/%q want %s/<id>", expected.action, event.TargetType, event.TargetID, expected.targetType)
		}
		if event.ActorID != 42 {
			t.Fatalf("%s actor=%d want 42", expected.action, event.ActorID)
		}
		if event.Metadata["version_id"] != int64(1) {
			t.Fatalf("%s metadata=%v want version_id=1", expected.action, event.Metadata)
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
