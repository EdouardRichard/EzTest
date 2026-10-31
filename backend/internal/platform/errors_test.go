package platform

import (
	"errors"
	"net/http"
	"testing"
)

func TestErrorHelpers(t *testing.T) {
	cases := []struct {
		name   string
		err    *Error
		status int
		code   string
	}{
		{"invalid", Invalid("title", "required"), http.StatusBadRequest, "invalid_argument"},
		{"unauthenticated", Unauthenticated(), http.StatusUnauthorized, "unauthenticated"},
		{"forbidden", Forbidden(), http.StatusForbidden, "forbidden"},
		{"not found", NotFound("sut"), http.StatusNotFound, "not_found"},
		{"conflict", Conflict("already exists"), http.StatusConflict, "conflict"},
		{"state conflict", StateConflict("invalid transition"), http.StatusConflict, "state_conflict"},
		{"lease lost", LeaseLost(), http.StatusConflict, "lease_lost"},
		{"ai unavailable", AIUnavailable("upstream down"), http.StatusServiceUnavailable, "ai_unavailable"},
		{"context overflow", ContextOverflow("too large"), http.StatusServiceUnavailable, "context_overflow"},
		{"attachment failed", AttachmentError("extraction_failed", "bad pdf"), http.StatusUnprocessableEntity, "extraction_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Status != tc.status || tc.err.Code != tc.code {
				t.Fatalf("error = %#v, want status %d code %s", tc.err, tc.status, tc.code)
			}
		})
	}
	if !AIUnavailable("x").Retryable || !ContextOverflow("x").Retryable {
		t.Fatal("transient LLM errors must be retryable")
	}
}

func TestInvalidCarriesFieldDetail(t *testing.T) {
	err := Invalid("title", "required")
	if err.Details["field"] != "title" {
		t.Fatalf("details = %#v", err.Details)
	}
}

func TestAsErrorPreservesPlatformError(t *testing.T) {
	original := Conflict("duplicate")
	got := AsError(original)
	if got != original {
		t.Fatalf("AsError returned a different error: %#v", got)
	}
}

func TestAsErrorWrapsUnknown(t *testing.T) {
	got := AsError(errors.New("boom"))
	if got.Status != http.StatusInternalServerError || got.Code != "internal" {
		t.Fatalf("AsError = %#v", got)
	}
	if got.Message == "" {
		t.Fatal("internal error must have a message")
	}
}

func TestAsErrorWrapsJoin(t *testing.T) {
	base := Invalid("title", "required")
	got := AsError(errors.Join(errors.New("outer"), base))
	if got != base {
		t.Fatalf("AsError did not unwrap the platform error: %#v", got)
	}
}
