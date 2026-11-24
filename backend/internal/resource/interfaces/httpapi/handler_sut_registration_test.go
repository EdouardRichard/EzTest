package resourceapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// v0.2.4-B R-B4: the admin read path GET /api/resources/suts/{id}/registrations
// must return the SUT's append-only registration facts (the production reader that
// keeps sut_registration from being a write-only table, violations.md V-0019), and
// the SUT DTO must expose the new source/status registration vocabulary.

func TestListSUTRegistrationsReturnsFacts(t *testing.T) {
	handler, svc := newHandler()
	svc.registrations = []resourceapp.SUTRegistrationView{
		{
			ID: "1002", SUTID: "1002", Source: resource.SUTSourceAdmin,
			EvidenceRefs: []string{}, ResolutionMethod: resource.SUTResolutionAdminCreate, GateResult: resource.SUTStatusApproved,
			CreatedAt: testNow, CreatedBy: 42,
		},
	}
	req := request(http.MethodGet, "/api/resources/suts/1002/registrations", "", admin)
	req.SetPathValue("id", "1002")
	rec := httptest.NewRecorder()
	handler.ListSUTRegistrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastID != 1002 {
		t.Fatalf("service called with sut id %d, want 1002", svc.lastID)
	}
	var dtos []sutRegistrationDTO
	decodeData(t, rec, &dtos)
	if len(dtos) != 1 {
		t.Fatalf("registrations = %#v, want one fact", dtos)
	}
	dto := dtos[0]
	if dto.ID != "1002" || dto.SUTID != "1002" || dto.Source != resource.SUTSourceAdmin {
		t.Fatalf("dto identity = %#v", dto)
	}
	if dto.ResolutionMethod != resource.SUTResolutionAdminCreate || dto.GateResult != resource.SUTStatusApproved {
		t.Fatalf("dto resolution/gate = %#v", dto)
	}
	if dto.CreatedBy != "42" {
		t.Fatalf("dto created_by = %q, want 42", dto.CreatedBy)
	}
	if dto.EvidenceRefs == nil || len(dto.EvidenceRefs) != 0 {
		t.Fatalf("dto evidence_refs = %#v, want a non-nil empty list", dto.EvidenceRefs)
	}
}

func TestListSUTRegistrationsEmptyListIsNotNull(t *testing.T) {
	handler, _ := newHandler()
	req := request(http.MethodGet, "/api/resources/suts/1002/registrations", "", admin)
	req.SetPathValue("id", "1002")
	rec := httptest.NewRecorder()
	handler.ListSUTRegistrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var dtos []sutRegistrationDTO
	decodeData(t, rec, &dtos)
	if dtos == nil || len(dtos) != 0 {
		t.Fatalf("registrations = %#v, want a non-nil empty list", dtos)
	}
}

func TestListSUTRegistrationsPropagatesServiceError(t *testing.T) {
	handler, svc := newHandler()
	svc.err = platform.NotFound("sut")
	req := request(http.MethodGet, "/api/resources/suts/9999/registrations", "", admin)
	req.SetPathValue("id", "9999")
	rec := httptest.NewRecorder()
	handler.ListSUTRegistrations(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestRejectSUTAdminEndpoint pins SC-F4: POST /api/resources/suts/{id}/reject is the
// admin post-hoc audit/reject entry. An admin caller reaches the service and gets the
// rejected SUT DTO; a non-admin caller is forbidden (defense-in-depth alongside the
// admin route middleware); a service conflict propagates.
func TestRejectSUTAdminEndpoint(t *testing.T) {
	t.Run("admin rejects", func(t *testing.T) {
		handler, svc := newHandler()
		svc.sut.Status = resource.SUTStatusRejected
		req := request(http.MethodPost, "/api/resources/suts/1002/reject", "", admin)
		req.SetPathValue("id", "1002")
		rec := httptest.NewRecorder()
		handler.RejectSUT(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if svc.lastID != 1002 || svc.lastActor != 42 {
			t.Fatalf("service called with id/actor = %d/%d, want 1002/42", svc.lastID, svc.lastActor)
		}
		var dto sutDTO
		decodeData(t, rec, &dto)
		if dto.Status != resource.SUTStatusRejected {
			t.Fatalf("dto status = %q, want rejected", dto.Status)
		}
	})

	t.Run("non-admin forbidden", func(t *testing.T) {
		handler, _ := newHandler()
		req := request(http.MethodPost, "/api/resources/suts/1002/reject", "", plainUser)
		req.SetPathValue("id", "1002")
		rec := httptest.NewRecorder()
		handler.RejectSUT(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("conflict propagates", func(t *testing.T) {
		handler, svc := newHandler()
		svc.err = platform.Conflict("sut is already rejected")
		req := request(http.MethodPost, "/api/resources/suts/1002/reject", "", admin)
		req.SetPathValue("id", "1002")
		rec := httptest.NewRecorder()
		handler.RejectSUT(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestSUTDTOExposesSourceAndStatus(t *testing.T) {
	handler, svc := newHandler()
	svc.sut.Source = resource.SUTSourceAdmin
	svc.sut.Status = resource.SUTStatusApproved
	req := request(http.MethodGet, "/api/resources/suts/1002", "", plainUser)
	req.SetPathValue("id", "1002")
	rec := httptest.NewRecorder()
	handler.GetSUT(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var dto sutDTO
	decodeData(t, rec, &dto)
	if dto.Source != resource.SUTSourceAdmin || dto.Status != resource.SUTStatusApproved {
		t.Fatalf("sut dto source/status = %q/%q, want admin/approved", dto.Source, dto.Status)
	}
}
