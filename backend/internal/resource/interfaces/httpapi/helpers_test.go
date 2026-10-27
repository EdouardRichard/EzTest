package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// decodeResourceAPIError decodes the shared error envelope from a recorder.
func decodeResourceAPIError(t *testing.T, rr *httptest.ResponseRecorder) platform.APIError {
	t.Helper()
	var body struct {
		Error platform.APIError `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
	}
	return body.Error
}

func TestWriteServiceErrorMapsInvalid(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/resources/systems", nil)
	WriteServiceError(rr, req, domain.NewInvalid("key", "invalid_key", "bad key"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestPathIDRejectsNonNumeric(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/resources/systems/abc", nil)
	req.SetPathValue("id", "abc")
	if _, err := PathID(req); err == nil {
		t.Fatal("want error")
	}
}

func TestPathIDAcceptsPositiveID(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/resources/systems/42", nil)
	req.SetPathValue("id", "42")
	id, err := PathID(req)
	if err != nil || id != 42 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	for _, raw := range []string{"0", "-1", "", " 1", "1.5"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetPathValue("id", raw)
		if _, err := PathID(req); err == nil {
			t.Fatalf("id=%q: want error", raw)
		}
	}
}

func TestWriteServiceErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		status     int
		code       string
		reason     string
		messageNot string
	}{
		{"invalid", domain.NewInvalid("key", "invalid_key", "bad key"), http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_key", ""},
		{"not found", domain.NewNotFound("system", 7), http.StatusNotFound, platform.CodeNotFound, "not_found", ""},
		{"conflict", domain.NewConflict("duplicate_key", "duplicate"), http.StatusConflict, platform.CodeConflict, "duplicate_key", ""},
		{"dependency", domain.NewDependency("has_dependencies", "in use"), http.StatusConflict, platform.CodeConflict, "has_dependencies", ""},
		{"state conflict", domain.NewStateConflict("bad state"), http.StatusConflict, platform.CodeConflict, "session_state_conflict", ""},
		{"lease conflict", domain.NewLeaseConflict("busy"), http.StatusConflict, platform.CodeConflict, "lease_conflict", ""},
		{"unknown", errors.New("database password leaked"), http.StatusInternalServerError, platform.CodeInternal, "", "password"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/", nil)
			WriteServiceError(rr, req, tc.err)
			if rr.Code != tc.status {
				t.Fatalf("status=%d, want %d", rr.Code, tc.status)
			}
			apiErr := decodeResourceAPIError(t, rr)
			if apiErr.Code != tc.code {
				t.Fatalf("code=%q, want %q", apiErr.Code, tc.code)
			}
			if tc.reason != "" {
				if got := apiErr.Details["reason"]; got != tc.reason {
					t.Fatalf("details.reason=%v, want %q", got, tc.reason)
				}
			}
			if tc.messageNot != "" && strings.Contains(apiErr.Message, tc.messageNot) {
				t.Fatalf("message %q leaks an internal error", apiErr.Message)
			}
		})
	}
}

func TestWriteServiceErrorIncludesField(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)
	WriteServiceError(rr, req, domain.NewInvalid("key", "invalid_key", "bad key"))
	apiErr := decodeResourceAPIError(t, rr)
	if apiErr.Details["field"] != "key" || apiErr.Details["reason"] != "invalid_key" {
		t.Fatalf("details=%v", apiErr.Details)
	}
	if apiErr.Message != "bad key" {
		t.Fatalf("message=%q", apiErr.Message)
	}
}

func TestWriteData(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteData(rr, http.StatusCreated, map[string]any{"key": "shop"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d", rr.Code)
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["key"] != "shop" {
		t.Fatalf("body=%v", body.Data)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
}

func TestWritePage(t *testing.T) {
	rr := httptest.NewRecorder()
	WritePage(rr, []map[string]any{{"id": 1}}, "cursor-1", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var body struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.NextCursor != "cursor-1" || !body.HasMore {
		t.Fatalf("body=%+v", body)
	}
}

func TestParsePage(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	page, err := ParsePage(req)
	if err != nil {
		t.Fatal(err)
	}
	if page.Limit != platform.DefaultLimit || page.AfterID != 0 {
		t.Fatalf("page=%+v", page)
	}

	req = httptest.NewRequest("GET", "/?limit=5&cursor="+platform.EncodeCursor(99), nil)
	page, err = ParsePage(req)
	if err != nil {
		t.Fatal(err)
	}
	if page.Limit != 5 || page.AfterID != 99 {
		t.Fatalf("page=%+v", page)
	}

	for _, raw := range []string{"?limit=0", "?limit=101", "?limit=x", "?cursor=not-base64!"} {
		req := httptest.NewRequest("GET", "/"+raw, nil)
		_, err := ParsePage(req)
		var de *domain.Error
		if !errors.As(err, &de) || de.Kind != domain.KindInvalid {
			t.Fatalf("%s: err=%v, want a domain invalid error", raw, err)
		}
	}
}

func TestDecodeJSON(t *testing.T) {
	var dst struct {
		Key string `json:"key"`
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"key":"shop"}`))
	if err := DecodeJSON(httptest.NewRecorder(), req, &dst, 1<<10); err != nil {
		t.Fatal(err)
	}
	if dst.Key != "shop" {
		t.Fatalf("dst=%+v", dst)
	}

	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"key":"shop","extra":1}`))
	if err := DecodeJSON(httptest.NewRecorder(), req, &dst, 1<<10); err == nil {
		t.Fatal("want error for an unknown field")
	}

	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"key":"`+strings.Repeat("x", 100)+`"}`))
	err := DecodeJSON(httptest.NewRecorder(), req, &dst, 16)
	var de *domain.Error
	if !errors.As(err, &de) || de.Kind != domain.KindInvalid {
		t.Fatalf("err=%v, want a domain invalid error", err)
	}
}

type helpersTestAuthenticator struct {
	user identity.User
}

func (f helpersTestAuthenticator) Authenticate(context.Context, string) (identity.User, error) {
	return f.user, nil
}

func TestActorIDFromAuthenticatedUser(t *testing.T) {
	if got := ActorID(context.Background()); got != nil {
		t.Fatalf("got=%v, want nil without an authenticated user", *got)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	var got *int64
	handler := identityhttp.AuthMiddleware(helpersTestAuthenticator{user: identity.User{
		ID: 7, Role: identity.RoleAdmin, Status: identity.StatusActive,
	}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = ActorID(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if got == nil || *got != 7 {
		t.Fatalf("got=%v, want 7", got)
	}
}
