package resourceapp

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// v0.2.4-F T11 (R-B4 / R-F1, SC-F4): the Agent new-SUT registration path writes
// source=ai and the SAME append-only sut_registration fact table the admin path
// uses, with gate_result reflecting the evidence-gate outcome (approved on proof,
// proposed otherwise). The admin post-hoc audit/reject path turns a proposed or
// auto-approved SUT into rejected.

func TestRegisterAISUTApprovedWritesAIFact(t *testing.T) {
	h := newHarness()
	refs := []string{"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
	view, err := h.svc.RegisterAISUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "AI Center", Intro: "discovered", BaseURL: "https://ai.example.com",
	}, refs, true, 77, testNow)
	if err != nil {
		t.Fatalf("RegisterAISUT: %v", err)
	}
	if view.Source != resource.SUTSourceAI || view.Status != resource.SUTStatusApproved {
		t.Fatalf("ai proven path source/status = %q/%q, want ai/approved", view.Source, view.Status)
	}
	// The SUT and its birth fact commit through the ONE-transaction method (I-1).
	if h.repo.createSUTWithRegistrationCalls != 1 {
		t.Fatalf("CreateSUTWithRegistration calls = %d, want 1 (ai path must be atomic)", h.repo.createSUTWithRegistrationCalls)
	}
	registrations, err := h.svc.ListSUTRegistrations(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("ListSUTRegistrations: %v", err)
	}
	if len(registrations) != 1 {
		t.Fatalf("registrations = %d, want one birth fact", len(registrations))
	}
	fact := registrations[0]
	if fact.Source != resource.SUTSourceAI {
		t.Fatalf("fact source = %q, want ai", fact.Source)
	}
	if fact.ResolutionMethod != resource.SUTResolutionAgentRegister {
		t.Fatalf("fact resolution_method = %q, want %q", fact.ResolutionMethod, resource.SUTResolutionAgentRegister)
	}
	if fact.GateResult != resource.SUTStatusApproved {
		t.Fatalf("fact gate_result = %q, want approved", fact.GateResult)
	}
	if len(fact.EvidenceRefs) != 1 || fact.EvidenceRefs[0] != refs[0] {
		t.Fatalf("fact evidence_refs = %v, want the proving refs %v", fact.EvidenceRefs, refs)
	}
	if fact.CreatedBy != 77 {
		t.Fatalf("fact created_by = %d, want the actor 77", fact.CreatedBy)
	}
}

func TestRegisterAISUTUnprovenStaysProposed(t *testing.T) {
	h := newHarness()
	view, err := h.svc.RegisterAISUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "AI Center", BaseURL: "https://ai.example.com",
	}, nil, false, 77, time.Time{})
	if err != nil {
		t.Fatalf("RegisterAISUT: %v", err)
	}
	if view.Source != resource.SUTSourceAI || view.Status != resource.SUTStatusProposed {
		t.Fatalf("ai unproven path source/status = %q/%q, want ai/proposed (fail-closed)", view.Source, view.Status)
	}
	registrations, err := h.svc.ListSUTRegistrations(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("ListSUTRegistrations: %v", err)
	}
	if len(registrations) != 1 {
		t.Fatalf("registrations = %d, want one birth fact", len(registrations))
	}
	fact := registrations[0]
	if fact.GateResult != resource.SUTStatusProposed {
		t.Fatalf("fact gate_result = %q, want proposed", fact.GateResult)
	}
	if len(fact.EvidenceRefs) != 0 {
		t.Fatalf("unproven fact evidence_refs = %v, want empty", fact.EvidenceRefs)
	}
	// A proposed SUT contributes nothing to effective scope (R-B4 trust boundary).
	detail, err := h.svc.GetSUT(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if detail.IsApproved() {
		t.Fatal("a proposed SUT must not be approved for effective scope")
	}
	if scope := detail.EffectiveAssets(); len(scope.Pages) != 0 || len(scope.APIs) != 0 || len(scope.Databases) != 0 {
		t.Fatalf("proposed SUT effective scope = %+v, want empty", scope)
	}
}

// TestAdminSUTReject pins SC-F4: the admin post-hoc audit/reject entry turns a
// proposed OR an auto-approved SUT into rejected (the agent auto-approves on
// proof; the admin can still reject afterwards). A second reject is a 409.
func TestAdminSUTReject(t *testing.T) {
	t.Run("reject auto-approved SUT", func(t *testing.T) {
		h := newHarness()
		created := createTestSUT(t, h, "Order Center") // admin/approved
		if created.Status != resource.SUTStatusApproved {
			t.Fatalf("seed status = %q, want approved", created.Status)
		}
		view, err := h.svc.RejectSUT(context.Background(), testSUTID, 99)
		if err != nil {
			t.Fatalf("RejectSUT: %v", err)
		}
		if view.Status != resource.SUTStatusRejected {
			t.Fatalf("rejected status = %q, want rejected", view.Status)
		}
		stored, err := h.repo.GetSUT(context.Background(), testSUTID)
		if err != nil {
			t.Fatalf("GetSUT: %v", err)
		}
		if stored.Status != resource.SUTStatusRejected {
			t.Fatalf("stored status = %q, want rejected", stored.Status)
		}
		if got := h.audit.entries[len(h.audit.entries)-1].Action; got != "resource.sut.rejected" {
			t.Fatalf("audit action = %q, want resource.sut.rejected", got)
		}
		// A rejected SUT contributes nothing to effective scope.
		detail, err := h.svc.GetSUT(context.Background(), testSUTID)
		if err != nil {
			t.Fatalf("GetSUT: %v", err)
		}
		if detail.IsApproved() {
			t.Fatal("a rejected SUT must not be approved for effective scope")
		}
		// Rejecting again is a state conflict (terminal).
		if _, err := h.svc.RejectSUT(context.Background(), testSUTID, 99); err == nil {
			t.Fatal("rejecting an already-rejected SUT must conflict")
		}
	})

	t.Run("reject proposed SUT", func(t *testing.T) {
		h := newHarness()
		if _, err := h.svc.RegisterAISUT(context.Background(), testEnvironmentID, SUTInput{
			Name: "AI Center", BaseURL: "https://ai.example.com",
		}, nil, false, 77, time.Time{}); err != nil {
			t.Fatalf("RegisterAISUT: %v", err)
		}
		view, err := h.svc.RejectSUT(context.Background(), testSUTID, 99)
		if err != nil {
			t.Fatalf("RejectSUT: %v", err)
		}
		if view.Status != resource.SUTStatusRejected {
			t.Fatalf("rejected status = %q, want rejected", view.Status)
		}
	})

	t.Run("unknown SUT is not found", func(t *testing.T) {
		h := newHarness()
		_, err := h.svc.RejectSUT(context.Background(), 9999, 99)
		assertAPIError(t, err, 404, "not_found")
	})
}

// rejectedSUTHarness seeds one admin SUT and rejects it at testNow (the harness
// fixed clock), returning its base_url. The reject timestamp is the row's
// UpdatedAt (= testNow), the anchor the rebirth freshness rule measures against.
func rejectedSUTHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness()
	created := createTestSUT(t, h, "Order Center")
	if _, err := h.svc.RejectSUT(context.Background(), testSUTID, 99); err != nil {
		t.Fatalf("RejectSUT: %v", err)
	}
	return h, created.BaseURL
}

// TestNewSUTRenameRebirthOldEvidenceStaysProposed pins the P22 rebirth guard
// (negative side): within the same case_run an admin rejects SUT X, then the Agent
// renames it (new name, same base_url/domain) and cites PRE-reject evidence. The
// gate must NOT auto-approve the rebirth — the registration downgrades to proposed
// so the admin adjudicates (the human reject authority is preserved, constitution
// 一.3). Removing the rejected-match/freshness check would approve the rebirth and
// fail this test (mutation).
func TestNewSUTRenameRebirthOldEvidenceStaysProposed(t *testing.T) {
	refs := []string{"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
	cases := []struct {
		name     string
		baseURL  func(rejected string) string
		provenAt time.Time
	}{
		{
			name:     "rename same base_url, pre-reject evidence",
			baseURL:  func(rejected string) string { return rejected },
			provenAt: testNow.Add(-time.Hour),
		},
		{
			name:     "rename same domain different path, pre-reject evidence",
			baseURL:  func(rejected string) string { return rejected + "/v2" },
			provenAt: testNow.Add(-time.Hour),
		},
		{
			name:     "evidence exactly at reject time is not fresh",
			baseURL:  func(rejected string) string { return rejected },
			provenAt: testNow,
		},
		{
			name:     "zero provenAt is old evidence (fail-closed)",
			baseURL:  func(rejected string) string { return rejected },
			provenAt: time.Time{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, rejectedURL := rejectedSUTHarness(t)
			view, err := h.svc.RegisterAISUT(context.Background(), testEnvironmentID, SUTInput{
				Name: "Order Center v2", BaseURL: tc.baseURL(rejectedURL),
			}, refs, true, 77, tc.provenAt)
			if err != nil {
				t.Fatalf("RegisterAISUT: %v", err)
			}
			if view.Status != resource.SUTStatusProposed {
				t.Fatalf("rebirth status = %q, want proposed (old evidence must not overturn a human reject)", view.Status)
			}
			id, err := strconv.ParseInt(view.ID, 10, 64)
			if err != nil {
				t.Fatalf("parse sut id %q: %v", view.ID, err)
			}
			facts, err := h.svc.ListSUTRegistrations(context.Background(), id)
			if err != nil || len(facts) != 1 {
				t.Fatalf("facts = %#v, err %v; want one birth fact", facts, err)
			}
			if facts[0].GateResult != resource.SUTStatusProposed {
				t.Fatalf("fact gate_result = %q, want proposed", facts[0].GateResult)
			}
			// The downgraded rebirth contributes nothing to effective scope.
			detail, err := h.svc.GetSUT(context.Background(), id)
			if err != nil {
				t.Fatalf("GetSUT: %v", err)
			}
			if detail.IsApproved() {
				t.Fatal("a downgraded rebirth must not enter effective scope")
			}
		})
	}
}

// TestNewSUTRenameRebirthFreshEvidenceApproved pins the positive side: evidence
// created AFTER the rejection legitimately re-proves the system, so the rebirth
// auto-approves (the guard blocks stale rebirths, not fresh discovery).
func TestNewSUTRenameRebirthFreshEvidenceApproved(t *testing.T) {
	h, rejectedURL := rejectedSUTHarness(t)
	refs := []string{"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
	view, err := h.svc.RegisterAISUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "Order Center v2", BaseURL: rejectedURL,
	}, refs, true, 77, testNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("RegisterAISUT: %v", err)
	}
	if view.Status != resource.SUTStatusApproved {
		t.Fatalf("fresh-proof rebirth status = %q, want approved", view.Status)
	}
}

// TestNewSUTUnrelatedDomainNotBlockedByReject pins that the guard is scoped to the
// rejected SUT's base_url/domain: a genuinely different system with old-but-valid
// proof still auto-approves (the guard must not over-block the main flow).
func TestNewSUTUnrelatedDomainNotBlockedByReject(t *testing.T) {
	h, _ := rejectedSUTHarness(t)
	refs := []string{"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
	view, err := h.svc.RegisterAISUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "Payments", BaseURL: "https://payments.example.com",
	}, refs, true, 77, testNow.Add(-time.Hour))
	if err != nil {
		t.Fatalf("RegisterAISUT: %v", err)
	}
	if view.Status != resource.SUTStatusApproved {
		t.Fatalf("unrelated-domain status = %q, want approved (guard must not over-block)", view.Status)
	}
}
