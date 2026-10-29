package application

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeResourceAudit captures the resource write-path audit events.
type fakeResourceAudit struct {
	events []AuditEvent
	err    error
}

func (a *fakeResourceAudit) RecordResourceAudit(_ context.Context, event AuditEvent) error {
	a.events = append(a.events, event)
	return a.err
}

// TestSessionWritesAreAudited pins the Session audit coverage (spec §10.4):
// import, confirm, invalidate, revoke and the manual-takeover lifecycle each
// record one entry naming the session (and takeover) id and the state. The
// stored storage state, cookies, headers and note must never enter the audit
// log.
func TestSessionWritesAreAudited(t *testing.T) {
	svc, f := newSessionServiceWithFakes(t)
	f.checker.result = domain.LoginCheckResult{Observed: domain.ObservedAllowed, Status: 200}
	actor := int64(7)

	input := validStorageStateInput()
	input.ActorID = &actor
	imported, _, err := svc.Import(context.Background(), f.accessID, input)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, err := svc.Revoke(context.Background(), imported.ID, "fixture cleanup"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	needsManual := f.seedSession(domain.Session{State: domain.StateNeedsManual})
	if _, _, err := svc.Confirm(context.Background(), needsManual.ID, "confirmed by admin", &actor); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	valid := f.seedSession(domain.Session{State: domain.StateValid, ExpiresAt: timePtr(sessionTestNow.Add(time.Hour))})
	if _, err := svc.Invalidate(context.Background(), valid.ID, "fixture invalidate"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	takeover, _, err := svc.RequestTakeover(context.Background(), valid.ID,
		domain.TakeoverReasonApproval, json.RawMessage(`{"instructions":"SECRET-TAKEOVER-NOTE"}`),
		0, &actor)
	if err != nil {
		t.Fatalf("RequestTakeover: %v", err)
	}
	if _, err := svc.CancelTakeover(context.Background(), takeover.ID); err != nil {
		t.Fatalf("CancelTakeover: %v", err)
	}

	wantActions := []string{
		ActionSessionImported,
		ActionSessionRevoked,
		ActionSessionConfirmed,
		ActionSessionInvalidated,
		ActionTakeoverRequested,
		ActionTakeoverCancelled,
	}
	if len(f.audit.events) != len(wantActions) {
		t.Fatalf("audit events=%d want %d: %+v", len(f.audit.events), len(wantActions), f.audit.events)
	}
	for i, want := range wantActions {
		event := f.audit.events[i]
		if event.Action != want {
			t.Fatalf("events[%d].Action=%q want %q", i, event.Action, want)
		}
		if event.TargetID == "" {
			t.Fatalf("%s target id is empty", want)
		}
		if event.Metadata["state"] == nil {
			t.Fatalf("%s metadata=%v want state", want, event.Metadata)
		}
	}
	if f.audit.events[0].ActorID != actor {
		t.Fatalf("import actor=%d want %d", f.audit.events[0].ActorID, actor)
	}
	if f.audit.events[0].TargetID != formatInt(imported.ID) {
		t.Fatalf("import target=%q want %d", f.audit.events[0].TargetID, imported.ID)
	}
	if f.audit.events[0].Metadata["state"] != domain.StateValid {
		t.Fatalf("import metadata=%v", f.audit.events[0].Metadata)
	}
	if f.audit.events[4].Metadata["takeover_id"] != takeover.ID {
		t.Fatalf("takeover metadata=%v want takeover_id=%d", f.audit.events[4].Metadata, takeover.ID)
	}

	raw, err := json.Marshal(f.audit.events)
	if err != nil {
		t.Fatalf("marshal audit events: %v", err)
	}
	for _, secret := range []string{
		"fixture-secret-cookie",
		"fixture-token",
		"Authorization",
		"storage_state",
		"SECRET-TAKEOVER-NOTE",
		"confirmed by admin",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit events leak %q: %s", secret, raw)
		}
	}
	if strings.Contains(string(raw), "localStorage") {
		t.Fatalf("audit events leak storage state: %s", raw)
	}
}

// timePtr returns a pointer to value.
func timePtr(value time.Time) *time.Time { return &value }

// formatInt renders id the way audit target ids are stored.
func formatInt(id int64) string { return strconv.FormatInt(id, 10) }
