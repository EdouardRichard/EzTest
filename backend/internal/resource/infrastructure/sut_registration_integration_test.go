//go:build integration

package resourcepg_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	resourceapi "github.com/EdouardRichard/EzTest/backend/internal/resource/interfaces/httpapi"
)

// v0.2.4-B R-B4 integration gates: the admin creation path persists source/status
// and an append-only registration fact, the fact is readable back end to end
// through the real HTTP handler (not merely "a row exists"), the table is
// append-only (multiple registrations = multiple rows, old rows untouched), and the
// CHECK constraints reject illegal vocabulary at the database boundary.

// TestSUTRegistrationAdminCreateReadBackEndToEnd is the read/write closed loop the
// brief requires: an admin creates a SUT, then GET
// /api/resources/suts/{id}/registrations reads back the fact just written, through
// the real handler -> service -> repository -> PostgreSQL and back.
func TestSUTRegistrationAdminCreateReadBackEndToEnd(t *testing.T) {
	e := newEnv(t)
	environment := createEnvironment(t, e, "prod")
	sut := createSUTIn(t, e, int64Of(t, environment.ID), "Order Center")

	handler := resourceapi.New(e.svc)
	req := httptest.NewRequest(http.MethodGet, "/api/resources/suts/"+sut.ID+"/registrations", nil)
	req.SetPathValue("id", sut.ID)
	req = req.WithContext(platform.WithActor(req.Context(), platform.Actor{ID: 42, Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ListSUTRegistrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET registrations status = %d: %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data []struct {
			ID               string   `json:"id"`
			SUTID            string   `json:"sut_id"`
			Source           string   `json:"source"`
			EvidenceRefs     []string `json:"evidence_refs"`
			ResolutionMethod string   `json:"resolution_method"`
			GateResult       string   `json:"gate_result"`
			CreatedBy        string   `json:"created_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("GET read back %d facts, want the one just written by CreateSUT", len(envelope.Data))
	}
	fact := envelope.Data[0]
	if fact.SUTID != sut.ID {
		t.Fatalf("read-back fact sut_id = %q, want the created SUT %q", fact.SUTID, sut.ID)
	}
	if fact.Source != resource.SUTSourceAdmin || fact.GateResult != resource.SUTStatusApproved {
		t.Fatalf("read-back fact source/gate = %q/%q, want admin/approved", fact.Source, fact.GateResult)
	}
	if fact.ResolutionMethod != resource.SUTResolutionAdminCreate || fact.CreatedBy != "42" {
		t.Fatalf("read-back fact resolution/actor = %q/%q, want %s/42", fact.ResolutionMethod, fact.CreatedBy, resource.SUTResolutionAdminCreate)
	}
}

// TestSUTSourceStatusPersistedAndReadBack pins that the admin path's source/status
// survive the PostgreSQL round trip on the SUT row itself.
func TestSUTSourceStatusPersistedAndReadBack(t *testing.T) {
	e := newEnv(t)
	environment := createEnvironment(t, e, "prod")
	sut := createSUTIn(t, e, int64Of(t, environment.ID), "Order Center")
	if sut.Source != resource.SUTSourceAdmin || sut.Status != resource.SUTStatusApproved {
		t.Fatalf("created view source/status = %q/%q, want admin/approved", sut.Source, sut.Status)
	}
	stored, err := e.repo.GetSUT(context.Background(), int64Of(t, sut.ID))
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if stored.Source != resource.SUTSourceAdmin || stored.Status != resource.SUTStatusApproved {
		t.Fatalf("stored source/status = %q/%q, want admin/approved", stored.Source, stored.Status)
	}
}

// TestSUTRegistrationAppendOnlyMultipleRows pins the append-only contract against
// the real table: registering the same SUT twice yields two rows and never
// rewrites the first.
func TestSUTRegistrationAppendOnlyMultipleRows(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	environment := createEnvironment(t, e, "prod")
	sut := createSUTIn(t, e, int64Of(t, environment.ID), "Order Center")
	sutID := int64Of(t, sut.ID)

	first, err := e.repo.ListSUTRegistrations(ctx, sutID)
	if err != nil || len(first) != 1 {
		t.Fatalf("first read = %#v, err %v; want the one birth record", first, err)
	}

	second := resource.SUTRegistration{
		ID: 990001, SUTID: sutID, Source: resource.SUTSourceAI,
		EvidenceRefs: []string{"evidence:sha256:abc"}, ResolutionMethod: "parent_path", GateResult: resource.SUTStatusProposed,
		CreatedAt: testNow, CreatedBy: 42,
	}
	if _, err := e.repo.AppendSUTRegistration(ctx, second); err != nil {
		t.Fatalf("AppendSUTRegistration: %v", err)
	}

	all, err := e.repo.ListSUTRegistrations(ctx, sutID)
	if err != nil {
		t.Fatalf("ListSUTRegistrations: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("registrations = %d, want two rows (append-only, not an overwrite)", len(all))
	}
	if all[0].ID != first[0].ID || all[0].Source != resource.SUTSourceAdmin || all[0].GateResult != resource.SUTStatusApproved {
		t.Fatalf("the birth record was rewritten: %#v", all[0])
	}
	if all[1].ID != 990001 || all[1].Source != resource.SUTSourceAI || len(all[1].EvidenceRefs) != 1 {
		t.Fatalf("the appended fact = %#v, want the ai fact with its evidence", all[1])
	}
}

// TestSUTRegistrationCheckConstraintsRejectIllegalValues pins the database CHECK
// boundary: illegal source/status vocabulary is rejected even when it bypasses the
// domain validation (the repository does not re-validate).
func TestSUTRegistrationCheckConstraintsRejectIllegalValues(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	environment := createEnvironment(t, e, "prod")
	environmentID := int64Of(t, environment.ID)
	sut := createSUTIn(t, e, environmentID, "Order Center")
	sutID := int64Of(t, sut.ID)

	// Illegal SUT source.
	_, err := e.repo.CreateSUT(ctx, resource.SUT{
		ID: 990101, EnvironmentID: environmentID, Name: "bad-source", BaseURL: "https://bad.example.com",
		Source: "robot", Status: resource.SUTStatusApproved, CreatedBy: 42, CreatedAt: testNow, UpdatedAt: testNow,
	})
	if err == nil {
		t.Fatal("sut.source CHECK accepted an illegal value")
	}
	// Illegal SUT status (the shared lifecycle vocabulary must not be accepted).
	_, err = e.repo.CreateSUT(ctx, resource.SUT{
		ID: 990102, EnvironmentID: environmentID, Name: "bad-status", BaseURL: "https://bad.example.com",
		Source: resource.SUTSourceAdmin, Status: resource.StatusActive, CreatedBy: 42, CreatedAt: testNow, UpdatedAt: testNow,
	})
	if err == nil {
		t.Fatal("sut.status CHECK accepted the shared lifecycle value 'active'")
	}
	// Illegal registration source.
	_, err = e.repo.AppendSUTRegistration(ctx, resource.SUTRegistration{
		ID: 990103, SUTID: sutID, Source: "robot",
		ResolutionMethod: resource.SUTResolutionAdminCreate, GateResult: resource.SUTStatusApproved, CreatedAt: testNow, CreatedBy: 42,
	})
	if err == nil {
		t.Fatal("sut_registration.source CHECK accepted an illegal value")
	}
}

// TestEnvironmentCodePoolExcludesNonApprovedSUTRepositories pins the R-B4 trust
// boundary on the environment code pool for BOTH non-approved statuses (proposed
// and rejected): a non-approved SUT's enabled repository association must not enter
// the pool, so it can never become pool[0] and decide the code baseline the tests
// run against. The raw per-SUT view still shows the association so an admin can
// approve the SUT.
func TestEnvironmentCodePoolExcludesNonApprovedSUTRepositories(t *testing.T) {
	for _, status := range []string{resource.SUTStatusProposed, resource.SUTStatusRejected} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			environment := createEnvironment(t, e, "prod")
			environmentID := int64Of(t, environment.ID)

			// repoLow is created first so it has the LOWER repository ID: if a
			// non-approved SUT could contribute, repoLow would sort to pool[0] and
			// become the baseline.
			repoLow, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{Name: "low", Path: "https://git.example.com/low.git"}, 42)
			if err != nil {
				t.Fatalf("CreateRepository low: %v", err)
			}
			repoHigh, err := e.svc.CreateRepository(ctx, resourceapp.RepositoryInput{Name: "high", Path: "https://git.example.com/high.git"}, 42)
			if err != nil {
				t.Fatalf("CreateRepository high: %v", err)
			}
			if int64Of(t, repoLow.ID) >= int64Of(t, repoHigh.ID) {
				t.Fatalf("fixture must give the non-approved SUT the lower repo id: low=%s high=%s", repoLow.ID, repoHigh.ID)
			}

			// An approved SUT (the admin path) owning repoHigh.
			approved := createSUTIn(t, e, environmentID, "Approved")
			// A non-approved SUT — the shape subproject F's Agent path writes —
			// owning repoLow.
			nonApproved, err := e.repo.CreateSUT(ctx, resource.SUT{
				ID: 990201, EnvironmentID: environmentID, Name: "NonApproved", BaseURL: "https://nonapproved.example.com",
				Source: resource.SUTSourceAI, Status: status,
				CreatedBy: 42, CreatedAt: testNow, UpdatedAt: testNow,
			})
			if err != nil {
				t.Fatalf("CreateSUT non-approved: %v", err)
			}

			if _, err := e.repo.CreateSUTRepository(ctx, resource.SUTRepository{
				ID: 990202, SUTID: nonApproved.ID, RepoID: int64Of(t, repoLow.ID), Enabled: true, CreatedAt: testNow, UpdatedAt: testNow,
			}); err != nil {
				t.Fatalf("associate non-approved->low: %v", err)
			}
			if _, err := e.repo.CreateSUTRepository(ctx, resource.SUTRepository{
				ID: 990203, SUTID: int64Of(t, approved.ID), RepoID: int64Of(t, repoHigh.ID), Enabled: true, CreatedAt: testNow, UpdatedAt: testNow,
			}); err != nil {
				t.Fatalf("associate approved->high: %v", err)
			}

			pool, err := e.svc.ListEnvironmentRepositories(ctx, environmentID)
			if err != nil {
				t.Fatalf("ListEnvironmentRepositories: %v", err)
			}
			if len(pool) != 1 {
				t.Fatalf("pool = %d rows, want only the approved SUT's repository (a %s SUT must not contribute)", len(pool), status)
			}
			if pool[0].Repository.ID != repoHigh.ID {
				t.Fatalf("pool[0] = %q, want the approved SUT's %q; a %s SUT's repository must never decide the code baseline", pool[0].Repository.ID, repoHigh.ID, status)
			}

			// The raw per-SUT catalog view still shows the non-approved SUT's
			// association, so an admin can see and approve it.
			raw, err := e.svc.ListSUTRepositories(ctx, nonApproved.ID)
			if err != nil {
				t.Fatalf("ListSUTRepositories: %v", err)
			}
			if len(raw) != 1 || raw[0].Repository.ID != repoLow.ID {
				t.Fatalf("raw non-approved associations = %#v, want the repoLow association still visible", raw)
			}
		})
	}
}

// TestSUTAgentRegisterApprovedReadBackEndToEnd is the R-B4/R-F1 (v0.2.4-F T11)
// read/write closed loop for the Agent new-SUT path: a proven new SUT is registered
// source=ai/status=approved with the SAME append-only sut_registration fact table
// (gate_result=approved, resolution_method=agent_register, the proving evidence
// refs), readable back through the real handler -> service -> repository ->
// PostgreSQL. An unproven registration stays proposed (fail-closed).
func TestSUTAgentRegisterApprovedReadBackEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	environment := createEnvironment(t, e, "prod")
	environmentID := int64Of(t, environment.ID)
	refs := []string{"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}

	sut, err := e.svc.RegisterAISUT(ctx, environmentID, resourceapp.SUTInput{
		Name: "AI Center", Intro: "discovered", BaseURL: "https://ai.example.com",
	}, refs, true, 77, testNow)
	if err != nil {
		t.Fatalf("RegisterAISUT: %v", err)
	}
	if sut.Source != resource.SUTSourceAI || sut.Status != resource.SUTStatusApproved {
		t.Fatalf("ai proven view source/status = %q/%q, want ai/approved", sut.Source, sut.Status)
	}

	handler := resourceapi.New(e.svc)
	req := httptest.NewRequest(http.MethodGet, "/api/resources/suts/"+sut.ID+"/registrations", nil)
	req.SetPathValue("id", sut.ID)
	req = req.WithContext(platform.WithActor(req.Context(), platform.Actor{ID: 42, Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ListSUTRegistrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET registrations status = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data []struct {
			Source           string   `json:"source"`
			EvidenceRefs     []string `json:"evidence_refs"`
			ResolutionMethod string   `json:"resolution_method"`
			GateResult       string   `json:"gate_result"`
			CreatedBy        string   `json:"created_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("read back %d facts, want the one birth fact", len(envelope.Data))
	}
	fact := envelope.Data[0]
	if fact.Source != resource.SUTSourceAI || fact.GateResult != resource.SUTStatusApproved {
		t.Fatalf("fact source/gate = %q/%q, want ai/approved", fact.Source, fact.GateResult)
	}
	if fact.ResolutionMethod != resource.SUTResolutionAgentRegister || fact.CreatedBy != "77" {
		t.Fatalf("fact resolution/actor = %q/%q, want %s/77", fact.ResolutionMethod, fact.CreatedBy, resource.SUTResolutionAgentRegister)
	}
	if len(fact.EvidenceRefs) != 1 || fact.EvidenceRefs[0] != refs[0] {
		t.Fatalf("fact evidence_refs = %v, want the proving refs %v", fact.EvidenceRefs, refs)
	}

	// An unproven registration stays proposed (fail-closed: not in effective scope).
	proposed, err := e.svc.RegisterAISUT(ctx, environmentID, resourceapp.SUTInput{
		Name: "AI Proposed", BaseURL: "https://ai-proposed.example.com",
	}, nil, false, 77, testNow)
	if err != nil {
		t.Fatalf("RegisterAISUT proposed: %v", err)
	}
	if proposed.Status != resource.SUTStatusProposed {
		t.Fatalf("unproven view status = %q, want proposed", proposed.Status)
	}
}

// TestAdminRejectSUTEndToEnd pins the SC-F4 admin post-hoc audit/reject entry through
// the real HTTP handler: an admin rejects an auto-approved SUT (proposed/approved →
// rejected), the status persists, and a second reject is a 409 (terminal).
func TestAdminRejectSUTEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	environment := createEnvironment(t, e, "prod")
	sut := createSUTIn(t, e, int64Of(t, environment.ID), "Order Center")

	handler := resourceapi.New(e.svc)
	reject := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/resources/suts/"+sut.ID+"/reject", nil)
		req.SetPathValue("id", sut.ID)
		req = req.WithContext(platform.WithActor(req.Context(), platform.Actor{ID: 42, Role: "admin"}))
		rec := httptest.NewRecorder()
		handler.RejectSUT(rec, req)
		return rec
	}

	rec := reject()
	if rec.Code != http.StatusOK {
		t.Fatalf("reject status = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := e.repo.GetSUT(ctx, int64Of(t, sut.ID))
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if stored.Status != resource.SUTStatusRejected {
		t.Fatalf("stored status = %q, want rejected", stored.Status)
	}
	// The birth registration fact survives the reject (append-only, independent of
	// current status).
	regs, err := e.repo.ListSUTRegistrations(ctx, int64Of(t, sut.ID))
	if err != nil || len(regs) != 1 {
		t.Fatalf("registrations = %#v, err %v; want the birth fact preserved", regs, err)
	}
	if again := reject(); again.Code != http.StatusConflict {
		t.Fatalf("second reject status = %d, want 409: %s", again.Code, again.Body.String())
	}
}

// TestNewSUTRebirthGuardEndToEnd pins the P22 rebirth guard against the real DB:
// after an admin rejects a SUT, a rename with the same base_url citing pre-reject
// evidence (provenAt <= the rejection timestamp) downgrades to proposed; evidence
// newer than the rejection auto-approves. The rejected-match read goes through the
// real ListSUTsByEnvironment query.
func TestNewSUTRebirthGuardEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	environment := createEnvironment(t, e, "prod")
	environmentID := int64Of(t, environment.ID)
	sut := createSUTIn(t, e, environmentID, "Order Center")
	if _, err := e.svc.RejectSUT(ctx, int64Of(t, sut.ID), 99); err != nil {
		t.Fatalf("RejectSUT: %v", err)
	}
	refs := []string{"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}

	stale, err := e.svc.RegisterAISUT(ctx, environmentID, resourceapp.SUTInput{
		Name: "Order Center v2", BaseURL: sut.BaseURL,
	}, refs, true, 77, testNow.Add(-time.Hour))
	if err != nil {
		t.Fatalf("RegisterAISUT stale rebirth: %v", err)
	}
	if stale.Status != resource.SUTStatusProposed {
		t.Fatalf("stale rebirth status = %q, want proposed (old evidence must not overturn a human reject)", stale.Status)
	}

	fresh, err := e.svc.RegisterAISUT(ctx, environmentID, resourceapp.SUTInput{
		Name: "Order Center v3", BaseURL: sut.BaseURL,
	}, refs, true, 77, testNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("RegisterAISUT fresh rebirth: %v", err)
	}
	if fresh.Status != resource.SUTStatusApproved {
		t.Fatalf("fresh rebirth status = %q, want approved", fresh.Status)
	}
}

// TestCreateSUTWithRegistrationRollsBackOnAppendFailure pins the I-1 atomicity
// contract: if the registration append fails, the SUT INSERT is rolled back too, so
// a SUT can never exist without its birth record (a half-apply the read path would
// surface as a successful empty list with no repair path).
func TestCreateSUTWithRegistrationRollsBackOnAppendFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	environment := createEnvironment(t, e, "prod")
	environmentID := int64Of(t, environment.ID)

	sut := resource.SUT{
		ID: 990301, EnvironmentID: environmentID, Name: "Atomic", BaseURL: "https://atomic.example.com",
		Source: resource.SUTSourceAdmin, Status: resource.SUTStatusApproved,
		CreatedBy: 42, CreatedAt: testNow, UpdatedAt: testNow,
	}
	// A registration that FAILS the INSERT: an illegal source violates
	// chk_sut_registration_source, aborting the transaction after the SUT INSERT.
	badRegistration := resource.SUTRegistration{
		ID: 990302, SUTID: sut.ID, Source: "robot",
		ResolutionMethod: resource.SUTResolutionAdminCreate, GateResult: resource.SUTStatusApproved,
		CreatedAt: testNow, CreatedBy: 42,
	}
	if _, err := e.repo.CreateSUTWithRegistration(ctx, sut, badRegistration); err == nil {
		t.Fatal("CreateSUTWithRegistration accepted an illegal registration")
	}

	// The SUT must NOT exist — the whole transaction rolled back.
	if _, err := e.repo.GetSUT(ctx, sut.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("SUT was half-created despite the failed registration append: err=%v", err)
	}
	// And no registration row survives either.
	regs, err := e.repo.ListSUTRegistrations(ctx, sut.ID)
	if err != nil || len(regs) != 0 {
		t.Fatalf("registrations = %#v, err %v; want none after rollback", regs, err)
	}

	// The happy path still commits both together.
	goodRegistration := badRegistration
	goodRegistration.ID = 990303
	goodRegistration.Source = resource.SUTSourceAdmin
	if _, err := e.repo.CreateSUTWithRegistration(ctx, sut, goodRegistration); err != nil {
		t.Fatalf("CreateSUTWithRegistration happy path: %v", err)
	}
	if _, err := e.repo.GetSUT(ctx, sut.ID); err != nil {
		t.Fatalf("SUT missing after successful create: %v", err)
	}
	regs, err = e.repo.ListSUTRegistrations(ctx, sut.ID)
	if err != nil || len(regs) != 1 {
		t.Fatalf("registrations = %#v, err %v; want the one birth record", regs, err)
	}
}
