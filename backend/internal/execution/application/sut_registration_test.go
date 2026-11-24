package executionapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// --- fakes (R-B4 new-SUT evidence gate) ------------------------------------

// fakeNewSUTWriter records every new-SUT registration call. The gate decides the
// approved flag; the writer creates the SUT (source=ai) with status approved on
// proof, proposed otherwise, and the birth sut_registration fact (the resource
// service owns that write). The fake mirrors the status from the approved flag so
// the service result/event can be asserted at this layer.
type fakeNewSUTWriter struct {
	calls []newSUTCall
	id    int64
	err   error
}

type newSUTCall struct {
	versionID    int64
	in           tools.SUTRegisterInput
	evidenceRefs []string
	approved     bool
	actorID      int64
	provenAt     time.Time
}

func (f *fakeNewSUTWriter) RegisterNewSUT(_ context.Context, versionID int64, in tools.SUTRegisterInput, evidenceRefs []string, approved bool, actorID int64, provenAt time.Time) (NewSUTResult, error) {
	if f.err != nil {
		return NewSUTResult{}, f.err
	}
	f.calls = append(f.calls, newSUTCall{versionID: versionID, in: in, evidenceRefs: evidenceRefs, approved: approved, actorID: actorID, provenAt: provenAt})
	status := "proposed"
	if approved {
		status = "approved"
	}
	return NewSUTResult{
		SUTID:         f.id,
		EnvironmentID: 200,
		Name:          in.Name,
		BaseURL:       in.BaseURL,
		Source:        "ai",
		Status:        status,
	}, nil
}

func newSUTScope() tools.Scope {
	return tools.Scope{VersionID: 2001, CaseRunID: 5001, AgentRunID: 3001}
}

// --- tests -----------------------------------------------------------------

// TestNewSUTEvidenceGateAutoApprove pins SC-F4 / R-F1: a new SUT whose evidence
// EXISTS, whose sha256 is VALID (content integrity) and whose linked assertion
// PASSED (server verdict) is registered source=ai and AUTO-approved (no human
// approval; GD-01 evidence gate, not approval gate). The proving refs flow to the
// catalog writer (which records gate_result=approved in the sut_registration
// fact), and a sut.registered event is published. The evidence carries no SUT
// attribution (the SUT does not exist yet), so the gate must not require one.
func TestNewSUTEvidenceGateAutoApprove(t *testing.T) {
	proof := []byte("the agent reached the new resource and the assertion held")
	sha := shaOf(proof)
	step, assertion := 1, 1
	proofAt := time.Date(2026, 11, 19, 9, 30, 0, 0, time.UTC)
	evidence := &fakeProofEvidence{bySHA: map[string]execution.Evidence{
		sha: {
			ID: 8001, CaseRunID: 5001, SUTID: nil,
			StepSeq: &step, AssertionSeq: &assertion,
			Kind: execution.EvidenceScreenshot, SHA256: sha, CreatedAt: proofAt,
		},
	}}
	results := &fakeProofResults{assertions: []execution.AssertionResult{
		{CaseRunID: 5001, StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed},
	}}
	content := &fakeProofContent{bySHA: map[string][]byte{sha: proof}}
	writer := &fakeNewSUTWriter{id: 9101}
	pub := &fakeResolutionPublisher{}
	svc := NewSUTRegistrationService(SUTRegistrationDeps{
		Evidence: evidence, Results: results, Content: content,
		SUTs: writer, Events: pub,
	})

	in := tools.SUTRegisterInput{
		Name: "订单中心", Intro: "orders", BaseURL: "https://orders.example.com",
		EvidenceRefs: []string{sha},
	}
	result, err := svc.RegisterSUT(context.Background(), newSUTScope(), in)
	if err != nil {
		t.Fatalf("RegisterSUT: %v", err)
	}
	if len(writer.calls) != 1 {
		t.Fatalf("new-SUT registration calls = %d, want 1", len(writer.calls))
	}
	call := writer.calls[0]
	if !call.approved {
		t.Fatal("proven new SUT must be auto-approved (R-F1)")
	}
	if call.versionID != 2001 || call.actorID != 3001 {
		t.Fatalf("call scope = %+v, want version 2001 actor 3001", call)
	}
	if len(call.evidenceRefs) != 1 || call.evidenceRefs[0] != sha {
		t.Fatalf("call evidence_refs = %v, want the proving ref [%s]", call.evidenceRefs, sha)
	}
	// The shared gate's freshness anchor (newest proving-evidence created_at) flows
	// to the catalog writer — the resource rebirth guard measures it against a
	// rejected same-domain SUT's reject timestamp (P22).
	if !call.provenAt.Equal(proofAt) {
		t.Fatalf("call provenAt = %v, want the proving evidence created_at %v", call.provenAt, proofAt)
	}
	if len(pub.events) != 1 || pub.events[0].Type != EventSUTRegistered {
		t.Fatalf("events = %+v, want one sut.registered", pub.events)
	}
	if pub.events[0].Channel != "version:2001" {
		t.Fatalf("event channel = %q, want version:2001", pub.events[0].Channel)
	}
	out, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map[string]any", result)
	}
	if out["status"] != "approved" || out["source"] != "ai" || out["sut_id"] != "9101" || out["approved"] != true {
		t.Fatalf("result = %+v, want approved ai SUT 9101", out)
	}
	if out["gate_result"] != "approved" {
		t.Fatalf("result gate_result = %v, want approved", out["gate_result"])
	}
}

// TestNewSUTNoEvidenceNotApproved pins the SC-F4 negative (Review Focus 4): a new
// SUT that is NOT proven (no evidence / sha256 mismatch / linked assertion not
// passed) is NEVER auto-approved — it stays proposed (fail-closed: a proposed SUT
// contributes nothing to effective scope), and the outcome is observable. Setting
// approved=true regardless of proof would approve these and fail this test
// (mutation of the evidence gate).
func TestNewSUTNoEvidenceNotApproved(t *testing.T) {
	proof := []byte("real proof bytes")
	sha := shaOf(proof)
	step, assertion := 1, 1
	passedAssertions := []execution.AssertionResult{
		{CaseRunID: 5001, StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed},
	}
	linkedEvidence := execution.Evidence{
		ID: 8001, CaseRunID: 5001, SUTID: nil,
		StepSeq: &step, AssertionSeq: &assertion,
		Kind: execution.EvidenceScreenshot, SHA256: sha,
	}
	badSHA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name     string
		evidence *fakeProofEvidence
		results  *fakeProofResults
		content  *fakeProofContent
		refs     []string
	}{
		{
			name:     "no evidence",
			evidence: &fakeProofEvidence{bySHA: map[string]execution.Evidence{}},
			results:  &fakeProofResults{assertions: passedAssertions},
			content:  &fakeProofContent{bySHA: map[string][]byte{sha: proof}},
			refs:     []string{sha},
		},
		{
			name: "sha256 mismatch",
			evidence: &fakeProofEvidence{bySHA: map[string]execution.Evidence{
				badSHA: {ID: 8002, CaseRunID: 5001, StepSeq: &step, AssertionSeq: &assertion, SHA256: badSHA},
			}},
			results: &fakeProofResults{assertions: passedAssertions},
			content: &fakeProofContent{bySHA: map[string][]byte{badSHA: []byte("tampered bytes whose hash differs")}},
			refs:    []string{badSHA},
		},
		{
			name:     "assertion not passed",
			evidence: &fakeProofEvidence{bySHA: map[string]execution.Evidence{sha: linkedEvidence}},
			results: &fakeProofResults{assertions: []execution.AssertionResult{
				{CaseRunID: 5001, StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionFailed},
			}},
			content: &fakeProofContent{bySHA: map[string][]byte{sha: proof}},
			refs:    []string{sha},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer := &fakeNewSUTWriter{id: 9101}
			pub := &fakeResolutionPublisher{}
			svc := NewSUTRegistrationService(SUTRegistrationDeps{
				Evidence: tc.evidence, Results: tc.results, Content: tc.content,
				SUTs: writer, Events: pub,
			})
			in := tools.SUTRegisterInput{
				Name: "订单中心", BaseURL: "https://orders.example.com", EvidenceRefs: tc.refs,
			}
			result, err := svc.RegisterSUT(context.Background(), newSUTScope(), in)
			if err != nil {
				t.Fatalf("RegisterSUT: %v", err)
			}
			if len(writer.calls) != 1 {
				t.Fatalf("registration calls = %d, want 1 (a proposed SUT is recorded for admin audit)", len(writer.calls))
			}
			if writer.calls[0].approved {
				t.Fatal("an unproven new SUT must NOT be auto-approved")
			}
			if len(writer.calls[0].evidenceRefs) != 0 {
				t.Fatalf("unproven registration cited refs %v, want none", writer.calls[0].evidenceRefs)
			}
			if !writer.calls[0].provenAt.IsZero() {
				t.Fatalf("unproven registration provenAt = %v, want zero (no proof anchor)", writer.calls[0].provenAt)
			}
			out, ok := result.(map[string]any)
			if !ok {
				t.Fatalf("result type %T, want map[string]any", result)
			}
			if out["status"] != "proposed" || out["approved"] != false {
				t.Fatalf("result = %+v, want proposed/not-approved (fail-closed)", out)
			}
		})
	}
}

// TestNewSUTRegisterRequiresProofRefs: an empty evidence_refs list is an invalid
// argument (the model must cite the proof), never a silent registration.
func TestNewSUTRegisterRequiresProofRefs(t *testing.T) {
	writer := &fakeNewSUTWriter{id: 9101}
	svc := NewSUTRegistrationService(SUTRegistrationDeps{
		Evidence: &fakeProofEvidence{}, Results: &fakeProofResults{}, Content: &fakeProofContent{},
		SUTs: writer,
	})
	_, err := svc.RegisterSUT(context.Background(), newSUTScope(), tools.SUTRegisterInput{
		Name: "x", BaseURL: "https://x.example.com",
	})
	if err == nil {
		t.Fatal("empty evidence_refs must be rejected")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("error = %v, want a 400 invalid argument", err)
	}
	if len(writer.calls) != 0 {
		t.Fatal("no registration may happen without proof refs")
	}
}

// TestNewSUTRegisterWriteErrorPropagates: a catalog/fact write failure after the
// gate ran is returned (observable, retryable), never a silent success.
func TestNewSUTRegisterWriteErrorPropagates(t *testing.T) {
	proof := []byte("proof")
	sha := shaOf(proof)
	step, assertion := 1, 1
	svc := NewSUTRegistrationService(SUTRegistrationDeps{
		Evidence: &fakeProofEvidence{bySHA: map[string]execution.Evidence{
			sha: {ID: 8001, CaseRunID: 5001, StepSeq: &step, AssertionSeq: &assertion, SHA256: sha},
		}},
		Results: &fakeProofResults{assertions: []execution.AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed}}},
		Content: &fakeProofContent{bySHA: map[string][]byte{sha: proof}},
		SUTs:    &fakeNewSUTWriter{id: 9101, err: errors.New("db down")},
	})
	_, err := svc.RegisterSUT(context.Background(), newSUTScope(), tools.SUTRegisterInput{
		Name: "x", BaseURL: "https://x.example.com", EvidenceRefs: []string{sha},
	})
	if err == nil {
		t.Fatal("registration write error must propagate")
	}
}
