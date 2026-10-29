package application_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// recordingVersionAudit captures every VersionAuditEvent a service records.
type recordingVersionAudit struct {
	events []application.VersionAuditEvent
	err    error
}

// Record implements application.VersionAuditSink.
func (r *recordingVersionAudit) Record(_ context.Context, event application.VersionAuditEvent) error {
	r.events = append(r.events, event)
	return r.err
}

// TestRequirementWritesAreAudited pins the audit coverage of the requirement
// pipeline: every successful write records exactly one entry carrying the
// version and requirement ids plus the status/revision, and never the title,
// description or acceptance criteria (spec §10.4: ids/status only).
func TestRequirementWritesAreAudited(t *testing.T) {
	f := newRequirementFixture()
	audit := &recordingVersionAudit{}
	f.service = application.NewRequirementService(
		f.repo, f.versions, f.events,
		application.WithRequirementIDGenerator(f.ids),
		application.WithRequirementAuditSink(audit),
	)
	version := f.seedVersion(domain.VersionClarifying)

	created, err := f.service.Create(context.Background(), version.ID,
		application.CreateRequirementCommand{
			Title:              "Checkout flow SECRET-TITLE",
			Description:        "SECRET-DESCRIPTION",
			AcceptanceCriteria: "SECRET-CRITERIA",
		}, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := f.service.Update(context.Background(), version.ID, created.ID,
		application.UpdateRequirementCommand{
			Revision: created.Revision,
			Title:    ptr("Renamed SECRET-TITLE-2"),
		}, 42)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := f.service.Confirm(context.Background(), version.ID, created.ID, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := f.service.Reopen(context.Background(), version.ID, created.ID, 42); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := f.service.Delete(context.Background(), version.ID, created.ID, 42); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	wantActions := []string{
		"version.requirement_created",
		"version.requirement_updated",
		"version.requirement_confirmed",
		"version.requirement_reopened",
		"version.requirement_deleted",
	}
	wantTargetID := strconv.FormatInt(created.ID, 10)
	if len(audit.events) != len(wantActions) {
		t.Fatalf("audit events=%d want %d: %+v", len(audit.events), len(wantActions), audit.events)
	}
	for i, want := range wantActions {
		event := audit.events[i]
		if event.Action != want {
			t.Fatalf("events[%d].Action=%q want %q", i, event.Action, want)
		}
		// The version id must survive even after the delete: it is what lets the
		// audit sink trace the entry back to its version (sink metadata.version_id).
		if event.VersionID != version.ID {
			t.Fatalf("%s version=%d want %d", want, event.VersionID, version.ID)
		}
		if event.ActorID != 42 {
			t.Fatalf("%s actor=%d want 42", want, event.ActorID)
		}
		if event.TargetType != "requirement" || event.TargetID != wantTargetID {
			t.Fatalf("%s target=%q/%q want requirement/%s", want, event.TargetType, event.TargetID, wantTargetID)
		}
	}

	if got := audit.events[0].Metadata["status"]; got != domain.RequirementDraft {
		t.Fatalf("created metadata=%v", audit.events[0].Metadata)
	}
	if got := audit.events[1].Metadata["revision"]; got != updated.Revision {
		t.Fatalf("updated metadata=%v want revision=%d", audit.events[1].Metadata, updated.Revision)
	}
	if got := audit.events[2].Metadata["status"]; got != domain.RequirementClarified {
		t.Fatalf("confirm metadata=%v want status=%s", audit.events[2].Metadata, domain.RequirementClarified)
	}

	raw, err := json.Marshal(audit.events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	for _, secret := range []string{"SECRET-TITLE", "SECRET-DESCRIPTION", "SECRET-CRITERIA"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit events leak %q: %s", secret, raw)
		}
	}
}

// TestVersionCreateAndMetadataEditAreAudited pins the version-level audit
// coverage: create and metadata edit record ids, status and changed field names
// only, never the version text.
func TestVersionCreateAndMetadataEditAreAudited(t *testing.T) {
	f := newVersionFixture()
	audit := &recordingVersionAudit{}
	f.service = application.NewVersionService(
		f.repo, f.catalog, f.git, f.events,
		application.WithIDGenerator(f.ids),
		application.WithVersionAuditSink(audit),
	)

	created, err := f.service.Create(context.Background(), application.CreateVersionCommand{
		Name:        "Order v2 SECRET-NAME",
		Description: "SECRET-DESCRIPTION",
	}, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := f.service.UpdateMetadata(context.Background(), created.ID, created.Revision,
		"Order v3 SECRET-NAME-2", "", "SECRET-DESCRIPTION-2", 42)
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if len(audit.events) != 2 {
		t.Fatalf("audit events=%d want 2: %+v", len(audit.events), audit.events)
	}

	createEvent := audit.events[0]
	if createEvent.Action != "version.created" {
		t.Fatalf("action=%q want version.created", createEvent.Action)
	}
	if createEvent.TargetType != "version" || createEvent.TargetID == "" {
		t.Fatalf("target=%q/%q want version/<id>", createEvent.TargetType, createEvent.TargetID)
	}
	if createEvent.Metadata["status"] != domain.VersionDraft {
		t.Fatalf("created metadata=%v", createEvent.Metadata)
	}

	updateEvent := audit.events[1]
	if updateEvent.Action != "version.updated" {
		t.Fatalf("action=%q want version.updated", updateEvent.Action)
	}
	if updateEvent.Metadata["revision"] != updated.Revision {
		t.Fatalf("updated metadata=%v want revision=%d", updateEvent.Metadata, updated.Revision)
	}
	changed, ok := updateEvent.Metadata["changed"].([]string)
	if !ok || !containsString(changed, "name") {
		t.Fatalf("updated metadata changed=%v want name", updateEvent.Metadata["changed"])
	}

	raw, err := json.Marshal(audit.events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	for _, secret := range []string{"SECRET-NAME", "SECRET-DESCRIPTION"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit events leak %q: %s", secret, raw)
		}
	}
}

// containsString reports whether values contains want.
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
