package domain_test

import (
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestReasonErrorCarriesReason(t *testing.T) {
	details := map[string]any{"batchId": int64(12)}
	err := domain.NewReasonError("batch_active", "batch_active",
		"an active batch already exists", false, details)

	var re *domain.ReasonError
	if !errors.As(err, &re) {
		t.Fatalf("expected *domain.ReasonError, got %T", err)
	}
	if re.Reason != "batch_active" {
		t.Fatalf("reason not preserved: %q", re.Reason)
	}
	if re.Code != "batch_active" {
		t.Fatalf("code not preserved: %q", re.Code)
	}
	if re.Message != "an active batch already exists" {
		t.Fatalf("message not preserved: %q", re.Message)
	}
	if re.Retryable {
		t.Fatal("retryable must be preserved as false")
	}
	if got := re.Details["batchId"]; got != int64(12) {
		t.Fatalf("details not preserved: %v", got)
	}
	if got, want := err.Error(), "batch_active: an active batch already exists"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
