package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// --- fakes (asset evidence gate) -------------------------------------------

// fakeProofEvidence looks up a case_run's evidence by content sha256. A missing
// sha returns execution.ErrNotFound (the production store's no-rows mapping).
type fakeProofEvidence struct {
	bySHA map[string]execution.Evidence
	err   error
}

func (f *fakeProofEvidence) EvidenceByCaseRunSHA(_ context.Context, _ int64, sha string) (execution.Evidence, error) {
	if f.err != nil {
		return execution.Evidence{}, f.err
	}
	ev, ok := f.bySHA[sha]
	if !ok {
		return execution.Evidence{}, execution.ErrNotFound
	}
	return ev, nil
}

// fakeProofResults returns the case_run's assertion results (server verdict).
type fakeProofResults struct {
	assertions []execution.AssertionResult
	err        error
}

func (f *fakeProofResults) ListAssertions(context.Context, int64) ([]execution.AssertionResult, error) {
	return f.assertions, f.err
}

// fakeProofContent returns the stored bytes for a sha256 so the gate can verify
// content integrity (recompute sha256 == the evidence row's sha256).
type fakeProofContent struct {
	bySHA map[string][]byte
	err   error
}

func (f *fakeProofContent) Open(_ context.Context, sha string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.bySHA[sha]
	if !ok {
		return nil, execution.ErrNotFound
	}
	return data, nil
}

// fakeProvenAssets records every catalog registration call (candidate + the
// server-derived freshness anchor). The gate must never reach it for an unproven
// discovery (fail-closed). A zero result falls back to an ai/approved page row.
type fakeProvenAssets struct {
	calls    []tools.AssetRegisterInput
	provenAt []time.Time
	result   RegisteredAsset
	err      error
}

func (f *fakeProvenAssets) RegisterProvenAsset(_ context.Context, in tools.AssetRegisterInput, _ int64, provenAt time.Time) (RegisteredAsset, error) {
	if f.err != nil {
		return RegisteredAsset{}, f.err
	}
	f.calls = append(f.calls, in)
	f.provenAt = append(f.provenAt, provenAt)
	if f.result.ID != 0 {
		return f.result, nil
	}
	return RegisteredAsset{ID: 7001, Source: "ai", Status: "approved"}, nil
}

// fakeAssetRegStore is the append-only asset_registration fact store stub.
type fakeAssetRegStore struct {
	appended  []execution.AssetRegistration
	appendErr error
}

func (f *fakeAssetRegStore) AppendAssetRegistration(_ context.Context, r execution.AssetRegistration) (execution.AssetRegistration, error) {
	if f.appendErr != nil {
		return execution.AssetRegistration{}, f.appendErr
	}
	r.ID = int64(len(f.appended) + 1)
	f.appended = append(f.appended, r)
	return r, nil
}

func (f *fakeAssetRegStore) ListAssetRegistrations(context.Context, int64) ([]execution.AssetRegistration, error) {
	return f.appended, nil
}

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func assetScope() tools.Scope {
	return tools.Scope{VersionID: 2001, CaseRunID: 5001, AgentRunID: 3001}
}

var assetProofTime = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

// --- tests -----------------------------------------------------------------

// TestAssetRegisterEvidenceGate pins SC-F3: a discovery whose evidence EXISTS,
// whose sha256 is VALID (content integrity) and whose linked assertion PASSED
// (server verdict) is registered into the catalog, an append-only
// asset_registration fact with gate_result=approved is written, and a
// sut.asset_registered event is published. The writer receives the freshness
// anchor (the proving evidence's created_at), and the result/event report the
// ACTUAL catalog row's source/status.
func TestAssetRegisterEvidenceGate(t *testing.T) {
	proof := []byte("the step used the asset and the assertion held")
	sha := shaOf(proof)
	step, assertion := 1, 1
	sutID := int64(9001)
	evidence := &fakeProofEvidence{bySHA: map[string]execution.Evidence{
		sha: {
			ID: 8001, CaseRunID: 5001, SUTID: &sutID,
			StepSeq: &step, AssertionSeq: &assertion,
			Kind: execution.EvidenceScreenshot, SHA256: sha, CreatedAt: assetProofTime,
		},
	}}
	results := &fakeProofResults{assertions: []execution.AssertionResult{
		{CaseRunID: 5001, StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed},
	}}
	content := &fakeProofContent{bySHA: map[string][]byte{sha: proof}}
	assets := &fakeProvenAssets{}
	store := &fakeAssetRegStore{}
	pub := &fakeResolutionPublisher{}
	svc := NewAssetRegistrationService(AssetRegistrationDeps{
		Evidence: evidence, Results: results, Content: content,
		Assets: assets, Registrations: store, Events: pub,
		Clock: fakeResolutionClock{at: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)},
	})

	in := tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: execution.AssetKindPage,
		Name: "订单页", URL: "https://orders.example.com/orders",
		EvidenceRefs: []string{sha},
	}
	result, err := svc.RegisterAsset(context.Background(), assetScope(), in)
	if err != nil {
		t.Fatalf("RegisterAsset: %v", err)
	}
	if len(assets.calls) != 1 {
		t.Fatalf("catalog registration calls = %d, want 1", len(assets.calls))
	}
	if assets.calls[0].AssetKind != execution.AssetKindPage || assets.calls[0].SUTID != 9001 {
		t.Fatalf("registered asset = %+v, want page on SUT 9001", assets.calls[0])
	}
	if len(assets.provenAt) != 1 || !assets.provenAt[0].Equal(assetProofTime) {
		t.Fatalf("freshness anchor = %v, want the proving evidence created_at %v", assets.provenAt, assetProofTime)
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended %d facts, want 1", len(store.appended))
	}
	fact := store.appended[0]
	if fact.GateResult != AssetGateApproved {
		t.Fatalf("gate_result = %q, want approved", fact.GateResult)
	}
	if fact.Source != execution.AssetSourceAI {
		t.Fatalf("source = %q, want ai (the registration path)", fact.Source)
	}
	if fact.AssetID != 7001 || fact.SUTID != 9001 || fact.AssetKind != execution.AssetKindPage {
		t.Fatalf("fact = %+v, want asset 7001 page on SUT 9001", fact)
	}
	if len(fact.EvidenceRefs) != 1 || fact.EvidenceRefs[0] != sha {
		t.Fatalf("fact evidence_refs = %v, want the proving ref [%s]", fact.EvidenceRefs, sha)
	}
	if len(pub.events) != 1 || pub.events[0].Type != EventSUTAssetRegistered {
		t.Fatalf("events = %+v, want one sut.asset_registered", pub.events)
	}
	if pub.events[0].Channel != "version:2001" {
		t.Fatalf("event channel = %q, want version:2001", pub.events[0].Channel)
	}
	if pub.events[0].Payload["status"] != "approved" || pub.events[0].Payload["source"] != "ai" {
		t.Fatalf("event payload source/status = %v/%v, want the actual row ai/approved",
			pub.events[0].Payload["source"], pub.events[0].Payload["status"])
	}
	out, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map[string]any", result)
	}
	if out["status"] != "approved" || out["source"] != "ai" || out["asset_id"] != "7001" {
		t.Fatalf("result = %+v, want the actual row ai/approved asset 7001", out)
	}
}

// TestUnprovenDiscoveryNotRegistered pins the SC-F3 negative: a discovery that is
// NOT successfully proven (no evidence / sha256 mismatch / linked assertion not
// passed) is NEVER registered — fail-closed and observable (a typed error), with
// no catalog asset and no asset_registration fact. Removing the evidence gate
// would register these unproven assets and fail this test (mutation).
func TestUnprovenDiscoveryNotRegistered(t *testing.T) {
	proof := []byte("real proof bytes")
	sha := shaOf(proof)
	step, assertion := 1, 1
	sutID := int64(9001)
	passedAssertions := []execution.AssertionResult{
		{CaseRunID: 5001, StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed},
	}
	linkedEvidence := execution.Evidence{
		ID: 8001, CaseRunID: 5001, SUTID: &sutID,
		StepSeq: &step, AssertionSeq: &assertion,
		Kind: execution.EvidenceScreenshot, SHA256: sha, CreatedAt: assetProofTime,
	}
	badSHA := strings.Repeat("a", 64)

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
				badSHA: {ID: 8002, CaseRunID: 5001, SUTID: &sutID, StepSeq: &step, AssertionSeq: &assertion, SHA256: badSHA, CreatedAt: assetProofTime},
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
			assets := &fakeProvenAssets{}
			store := &fakeAssetRegStore{}
			pub := &fakeResolutionPublisher{}
			svc := NewAssetRegistrationService(AssetRegistrationDeps{
				Evidence: tc.evidence, Results: tc.results, Content: tc.content,
				Assets: assets, Registrations: store, Events: pub,
			})
			in := tools.AssetRegisterInput{
				SUTID: 9001, AssetKind: execution.AssetKindPage,
				Name: "订单页", URL: "https://orders.example.com/orders",
				EvidenceRefs: tc.refs,
			}
			_, err := svc.RegisterAsset(context.Background(), assetScope(), in)
			if err == nil {
				t.Fatal("an unproven discovery must fail closed, not register")
			}
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) || apiErr.Status != 409 {
				t.Fatalf("error = %v, want an observable 409 state conflict", err)
			}
			if len(assets.calls) != 0 {
				t.Fatalf("unproven discovery registered %d catalog assets, want 0", len(assets.calls))
			}
			if len(store.appended) != 0 {
				t.Fatalf("unproven discovery appended %d facts, want 0", len(store.appended))
			}
			if len(pub.events) != 0 {
				t.Fatalf("unproven discovery published %d events, want 0", len(pub.events))
			}
		})
	}
}

// TestAssetRegisterRequiresProofRefs: an empty evidence_refs list is an invalid
// argument (the model must cite the proof), never a silent registration.
func TestAssetRegisterRequiresProofRefs(t *testing.T) {
	assets := &fakeProvenAssets{}
	store := &fakeAssetRegStore{}
	svc := NewAssetRegistrationService(AssetRegistrationDeps{
		Evidence: &fakeProofEvidence{}, Results: &fakeProofResults{}, Content: &fakeProofContent{},
		Assets: assets, Registrations: store,
	})
	_, err := svc.RegisterAsset(context.Background(), assetScope(), tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: execution.AssetKindPage, Name: "x", URL: "https://x.example.com",
	})
	if err == nil {
		t.Fatal("empty evidence_refs must be rejected")
	}
	if len(assets.calls) != 0 || len(store.appended) != 0 {
		t.Fatal("no registration may happen without proof refs")
	}
}

// TestAssetRegisterFactWriteErrorPropagates: a fact-write failure AFTER the gate
// passed and the catalog write happened is returned (observable, retryable) —
// the catalog call did occur exactly once, and the error is the infra failure,
// not the evidence-gate conflict.
func TestAssetRegisterFactWriteErrorPropagates(t *testing.T) {
	proof := []byte("proof")
	sha := shaOf(proof)
	step, assertion := 1, 1
	sutID := int64(9001)
	assets := &fakeProvenAssets{}
	svc := NewAssetRegistrationService(AssetRegistrationDeps{
		Evidence: &fakeProofEvidence{bySHA: map[string]execution.Evidence{
			sha: {ID: 8001, CaseRunID: 5001, SUTID: &sutID, StepSeq: &step, AssertionSeq: &assertion, SHA256: sha, CreatedAt: assetProofTime},
		}},
		Results:       &fakeProofResults{assertions: []execution.AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed}}},
		Content:       &fakeProofContent{bySHA: map[string][]byte{sha: proof}},
		Assets:        assets,
		Registrations: &fakeAssetRegStore{appendErr: errors.New("db down")},
	})
	_, err := svc.RegisterAsset(context.Background(), assetScope(), tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: execution.AssetKindPage, Name: "x", URL: "https://x.example.com", EvidenceRefs: []string{sha},
	})
	if err == nil {
		t.Fatal("fact write error must propagate")
	}
	if len(assets.calls) != 1 {
		t.Fatalf("catalog calls = %d, want 1 (the gate passed before the fact write)", len(assets.calls))
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && apiErr.Code == "evidence_gate_failed" {
		t.Fatalf("a post-gate infra failure must not masquerade as the evidence gate: %v", err)
	}
}

// TestAssetRegisterProvenAtIsNewestProof: with several proving refs, the
// freshness anchor forwarded to the catalog writer is the NEWEST proving
// evidence created_at (fresh proof exists iff the newest is newer than a
// rejection).
func TestAssetRegisterProvenAtIsNewestProof(t *testing.T) {
	oldProof, newProof := []byte("older proof"), []byte("newer proof")
	oldSHA, newSHA := shaOf(oldProof), shaOf(newProof)
	step, assertion := 1, 1
	sutID := int64(9001)
	older := assetProofTime.Add(-2 * time.Hour)
	newer := assetProofTime.Add(time.Hour)
	assets := &fakeProvenAssets{}
	svc := NewAssetRegistrationService(AssetRegistrationDeps{
		Evidence: &fakeProofEvidence{bySHA: map[string]execution.Evidence{
			oldSHA: {ID: 8001, CaseRunID: 5001, SUTID: &sutID, StepSeq: &step, AssertionSeq: &assertion, SHA256: oldSHA, CreatedAt: older},
			newSHA: {ID: 8002, CaseRunID: 5001, SUTID: &sutID, StepSeq: &step, AssertionSeq: &assertion, SHA256: newSHA, CreatedAt: newer},
		}},
		Results:       &fakeProofResults{assertions: []execution.AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed}}},
		Content:       &fakeProofContent{bySHA: map[string][]byte{oldSHA: oldProof, newSHA: newProof}},
		Assets:        assets,
		Registrations: &fakeAssetRegStore{},
	})
	_, err := svc.RegisterAsset(context.Background(), assetScope(), tools.AssetRegisterInput{
		SUTID: 9001, AssetKind: execution.AssetKindPage, Name: "x", URL: "https://x.example.com",
		EvidenceRefs: []string{oldSHA, newSHA},
	})
	if err != nil {
		t.Fatalf("RegisterAsset: %v", err)
	}
	if len(assets.provenAt) != 1 || !assets.provenAt[0].Equal(newer) {
		t.Fatalf("freshness anchor = %v, want the newest proving created_at %v", assets.provenAt, newer)
	}
}

// TestAssetRegisterResultReflectsActualRow: the tool result reports the ACTUAL
// catalog row's source/status, never hardcoded constants — an idempotent hit on
// an admin-created approved page reports admin/approved, and a database reports
// its real lifecycle status (active). The fact still records the registration
// path (source=ai, gate_result=approved).
func TestAssetRegisterResultReflectsActualRow(t *testing.T) {
	proof := []byte("proof")
	sha := shaOf(proof)
	step, assertion := 1, 1
	sutID := int64(9001)
	cases := []struct {
		name       string
		kind       string
		row        RegisteredAsset
		wantSource string
		wantStatus string
	}{
		{"admin approved page hit", execution.AssetKindPage, RegisteredAsset{ID: 7001, Source: "admin", Status: "approved"}, "admin", "approved"},
		{"active database", execution.AssetKindDB, RegisteredAsset{ID: 1501, Source: "ai", Status: "active"}, "ai", "active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeAssetRegStore{}
			svc := NewAssetRegistrationService(AssetRegistrationDeps{
				Evidence: &fakeProofEvidence{bySHA: map[string]execution.Evidence{
					sha: {ID: 8001, CaseRunID: 5001, SUTID: &sutID, StepSeq: &step, AssertionSeq: &assertion, SHA256: sha, CreatedAt: assetProofTime},
				}},
				Results:       &fakeProofResults{assertions: []execution.AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed}}},
				Content:       &fakeProofContent{bySHA: map[string][]byte{sha: proof}},
				Assets:        &fakeProvenAssets{result: tc.row},
				Registrations: store,
			})
			result, err := svc.RegisterAsset(context.Background(), assetScope(), tools.AssetRegisterInput{
				SUTID: 9001, AssetKind: tc.kind, Name: "x", URL: "https://x.example.com",
				DSN: "postgres://db.example.com:5432/x", Username: "u",
				EvidenceRefs: []string{sha},
			})
			if err != nil {
				t.Fatalf("RegisterAsset: %v", err)
			}
			out := result.(map[string]any)
			if out["source"] != tc.wantSource || out["status"] != tc.wantStatus {
				t.Fatalf("result source/status = %v/%v, want the actual row %s/%s", out["source"], out["status"], tc.wantSource, tc.wantStatus)
			}
			if len(store.appended) != 1 || store.appended[0].Source != execution.AssetSourceAI || store.appended[0].GateResult != AssetGateApproved {
				t.Fatalf("fact must record the registration path ai/approved: %+v", store.appended)
			}
		})
	}
}

// TestAssetRegisterInfraErrorsPropagate: an evidence/assertion/content read
// failure propagates (observable, retryable) — never a silent registration and
// never the fail-closed gate conflict.
func TestAssetRegisterInfraErrorsPropagate(t *testing.T) {
	proof := []byte("proof")
	sha := shaOf(proof)
	step, assertion := 1, 1
	sutID := int64(9001)
	goodEvidence := &fakeProofEvidence{bySHA: map[string]execution.Evidence{
		sha: {ID: 8001, CaseRunID: 5001, SUTID: &sutID, StepSeq: &step, AssertionSeq: &assertion, SHA256: sha, CreatedAt: assetProofTime},
	}}
	goodResults := &fakeProofResults{assertions: []execution.AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: execution.AssertionPassed}}}
	goodContent := &fakeProofContent{bySHA: map[string][]byte{sha: proof}}
	cases := []struct {
		name     string
		evidence *fakeProofEvidence
		results  *fakeProofResults
		content  *fakeProofContent
	}{
		{"evidence read", &fakeProofEvidence{err: errors.New("evidence store down")}, goodResults, goodContent},
		{"assertion read", goodEvidence, &fakeProofResults{err: errors.New("result store down")}, goodContent},
		{"content read", goodEvidence, goodResults, &fakeProofContent{err: errors.New("content store down")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assets := &fakeProvenAssets{}
			store := &fakeAssetRegStore{}
			svc := NewAssetRegistrationService(AssetRegistrationDeps{
				Evidence: tc.evidence, Results: tc.results, Content: tc.content,
				Assets: assets, Registrations: store,
			})
			_, err := svc.RegisterAsset(context.Background(), assetScope(), tools.AssetRegisterInput{
				SUTID: 9001, AssetKind: execution.AssetKindPage, Name: "x", URL: "https://x.example.com", EvidenceRefs: []string{sha},
			})
			if err == nil {
				t.Fatal("infra read error must propagate")
			}
			var apiErr *platform.Error
			if errors.As(err, &apiErr) && apiErr.Code == "evidence_gate_failed" {
				t.Fatalf("infra failure must not masquerade as the evidence gate: %v", err)
			}
			if len(assets.calls) != 0 || len(store.appended) != 0 {
				t.Fatal("no registration may happen when a proof read failed")
			}
		})
	}
}
