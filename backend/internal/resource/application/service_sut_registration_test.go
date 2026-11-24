package resourceapp

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// v0.2.4-B R-B4: the admin SUT-creation path must write source=admin /
// status=approved AND an append-only sut_registration fact, and that fact must be
// readable back through ListSUTRegistrations (a write-only fact table would repeat
// violations.md V-0019).

func TestCreateSUTWritesAdminSourceStatusAndRegistrationFact(t *testing.T) {
	h := newHarness()
	view, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "Order Center", Intro: "orders", BaseURL: "https://order.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	// I-1 wiring pin: the admin path must create the SUT and its birth fact through
	// the ONE-transaction method. A rewire to sequential CreateSUT + AppendSUTRegistration
	// leaves this counter at 0 and fails here (the observable fake state would otherwise
	// be identical).
	if h.repo.createSUTWithRegistrationCalls != 1 {
		t.Fatalf("CreateSUTWithRegistration calls = %d, want 1 (admin path must be atomic)", h.repo.createSUTWithRegistrationCalls)
	}
	if view.Source != resource.SUTSourceAdmin || view.Status != resource.SUTStatusApproved {
		t.Fatalf("admin path source/status = %q/%q, want admin/approved", view.Source, view.Status)
	}

	// The registration fact was written and is readable back (end-to-end at the
	// service layer, not merely "a row exists").
	registrations, err := h.svc.ListSUTRegistrations(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("ListSUTRegistrations: %v", err)
	}
	if len(registrations) != 1 {
		t.Fatalf("registrations = %d, want exactly one for a freshly created SUT", len(registrations))
	}
	fact := registrations[0]
	if fact.SUTID != view.ID {
		t.Fatalf("fact SUTID = %q, want the created SUT %q", fact.SUTID, view.ID)
	}
	if fact.Source != resource.SUTSourceAdmin {
		t.Fatalf("fact source = %q, want admin", fact.Source)
	}
	if fact.ResolutionMethod != resource.SUTResolutionAdminCreate {
		t.Fatalf("fact resolution_method = %q, want %q", fact.ResolutionMethod, resource.SUTResolutionAdminCreate)
	}
	if fact.GateResult != resource.SUTStatusApproved {
		t.Fatalf("fact gate_result = %q, want approved", fact.GateResult)
	}
	if fact.CreatedBy != 42 {
		t.Fatalf("fact created_by = %d, want the actor 42", fact.CreatedBy)
	}
	if len(fact.EvidenceRefs) != 0 {
		t.Fatalf("admin path fact evidence_refs = %#v, want empty (no AI evidence)", fact.EvidenceRefs)
	}
	if !fact.CreatedAt.Equal(testNow) {
		t.Fatalf("fact created_at = %v, want %v", fact.CreatedAt, testNow)
	}
}

func TestListSUTRegistrationsRejectsMissingSUT(t *testing.T) {
	h := newHarness()
	_, err := h.svc.ListSUTRegistrations(context.Background(), 9999)
	assertAPIError(t, err, 404, "not_found")
}

func TestListSUTRegistrationsReturnsEmptyForASUTWithoutFacts(t *testing.T) {
	h := newHarness()
	// Seed a SUT directly without a registration fact (the read path must return
	// an empty list, not an error, and must not fabricate facts).
	h.repo.suts = append(h.repo.suts, resource.SUT{
		ID: 7777, EnvironmentID: testEnvironmentID, Name: "factless",
		BaseURL: "https://factless.example.com", Source: resource.SUTSourceAdmin, Status: resource.SUTStatusApproved,
	})
	registrations, err := h.svc.ListSUTRegistrations(context.Background(), 7777)
	if err != nil {
		t.Fatalf("ListSUTRegistrations: %v", err)
	}
	if len(registrations) != 0 {
		t.Fatalf("registrations = %#v, want empty for a SUT without facts", registrations)
	}
}

// TestSUTRegistrationAppendOnlyMultipleRows pins the append-only contract at the
// repository surface: registering the same SUT again appends a second row and
// never rewrites the first. The admin creation writes the birth record; a later
// registration (the shape subproject F's Agent path will use) appends beside it.
func TestSUTRegistrationAppendOnlyMultipleRows(t *testing.T) {
	h := newHarness()
	view, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "Order Center", BaseURL: "https://order.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	first, err := h.svc.ListSUTRegistrations(context.Background(), testSUTID)
	if err != nil || len(first) != 1 {
		t.Fatalf("first read = %#v, err %v; want one birth record", first, err)
	}

	// Append a second registration of the SAME SUT with a different ID (the
	// Agent-proposal shape: source=ai, evidence attached, gate proposed).
	second := resource.SUTRegistration{
		ID: 8888, SUTID: testSUTID, Source: resource.SUTSourceAI,
		EvidenceRefs: []string{"evidence:sha256:abc"}, ResolutionMethod: "parent_path", GateResult: resource.SUTStatusProposed,
		CreatedAt: testNow, CreatedBy: 42,
	}
	if _, err := h.repo.AppendSUTRegistration(context.Background(), second); err != nil {
		t.Fatalf("AppendSUTRegistration: %v", err)
	}

	all, err := h.svc.ListSUTRegistrations(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("ListSUTRegistrations: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("registrations = %d, want two rows (append-only, not an overwrite)", len(all))
	}
	// The birth record is unchanged.
	if all[0].ID != first[0].ID || all[0].Source != resource.SUTSourceAdmin || all[0].GateResult != resource.SUTStatusApproved {
		t.Fatalf("the first registration was rewritten: %#v", all[0])
	}
	if all[1].ID != "8888" || all[1].Source != resource.SUTSourceAI {
		t.Fatalf("the appended registration = %#v, want the ai fact 8888", all[1])
	}
	// With independent IDs the meaningful invariant is that the birth record points
	// at the SUT that was created (not that the ids are equal).
	if view.ID != first[0].SUTID {
		t.Fatalf("created SUT id %q != birth record sut_id %q", view.ID, first[0].SUTID)
	}
}
