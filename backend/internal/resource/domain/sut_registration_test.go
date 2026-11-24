package resource

import (
	"testing"
	"time"
)

// v0.2.4-B R-B4: the SUT registration vocabulary (source admin/ai/import, status
// proposed/approved/rejected) is the SUT domain's OWN constants. It is deliberately
// NOT the shared StatusActive/StatusDisabled lifecycle vocabulary that environments,
// target databases, accounts and repositories use — those two constants must stay
// untouched. These tests pin the new vocabulary and its validation.

func TestSUTSourceStatusConstantsAreDistinctFromLifecycle(t *testing.T) {
	// The SUT registration vocabularies must be DISJOINT from the shared lifecycle
	// vocabulary by construction — every pair, not a hand-picked subset.
	lifecycle := []string{StatusActive, StatusDisabled}
	for _, status := range []string{SUTStatusProposed, SUTStatusApproved, SUTStatusRejected} {
		for _, shared := range lifecycle {
			if status == shared {
				t.Fatalf("SUT registration status %q aliases the shared lifecycle constant %q", status, shared)
			}
		}
	}
	for _, source := range []string{SUTSourceAdmin, SUTSourceAI, SUTSourceImport} {
		for _, shared := range lifecycle {
			if source == shared {
				t.Fatalf("SUT registration source %q aliases the shared lifecycle constant %q", source, shared)
			}
		}
	}
	if SUTStatusProposed != "proposed" || SUTStatusApproved != "approved" || SUTStatusRejected != "rejected" {
		t.Fatalf("status vocabulary = %q/%q/%q", SUTStatusProposed, SUTStatusApproved, SUTStatusRejected)
	}
	if SUTSourceAdmin != "admin" || SUTSourceAI != "ai" || SUTSourceImport != "import" {
		t.Fatalf("source vocabulary = %q/%q/%q", SUTSourceAdmin, SUTSourceAI, SUTSourceImport)
	}
}

func TestSUTValidateSourceStatus(t *testing.T) {
	base := SUT{EnvironmentID: 7, Name: "Order Center", BaseURL: "https://order.example.com"}
	cases := []struct {
		name    string
		source  string
		status  string
		wantErr bool
		field   string
	}{
		{name: "admin approved", source: SUTSourceAdmin, status: SUTStatusApproved},
		{name: "ai proposed", source: SUTSourceAI, status: SUTStatusProposed},
		{name: "import rejected", source: SUTSourceImport, status: SUTStatusRejected},
		{name: "bad source", source: "robot", status: SUTStatusApproved, wantErr: true, field: "source"},
		{name: "bad status", source: SUTSourceAdmin, status: "active", wantErr: true, field: "status"},
		{name: "empty source", source: "", status: SUTStatusApproved, wantErr: true, field: "source"},
		{name: "empty status", source: SUTSourceAdmin, status: "", wantErr: true, field: "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sut := base
			sut.Source = tc.source
			sut.Status = tc.status
			err := sut.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}

func TestSUTRegistrationValidate(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	valid := SUTRegistration{
		ID: 1, SUTID: 7, Source: SUTSourceAdmin,
		EvidenceRefs: []string{}, ResolutionMethod: SUTResolutionAdminCreate, GateResult: "approved",
		CreatedAt: at, CreatedBy: 42,
	}
	cases := []struct {
		name    string
		mutate  func(*SUTRegistration)
		wantErr bool
		field   string
	}{
		{name: "valid admin fact", mutate: func(*SUTRegistration) {}},
		{name: "ai fact with evidence", mutate: func(r *SUTRegistration) {
			r.Source = SUTSourceAI
			r.EvidenceRefs = []string{"evidence:sha256:abc"}
			r.ResolutionMethod = "parent_path"
			r.GateResult = "proposed"
		}},
		{name: "missing sut", mutate: func(r *SUTRegistration) { r.SUTID = 0 }, wantErr: true, field: "sut_id"},
		{name: "bad source", mutate: func(r *SUTRegistration) { r.Source = "robot" }, wantErr: true, field: "source"},
		{name: "empty resolution method", mutate: func(r *SUTRegistration) { r.ResolutionMethod = "" }, wantErr: true, field: "resolution_method"},
		{name: "empty gate result", mutate: func(r *SUTRegistration) { r.GateResult = "" }, wantErr: true, field: "gate_result"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := valid
			tc.mutate(&reg)
			err := reg.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}
