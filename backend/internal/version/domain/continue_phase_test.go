package domain_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestContinuePhaseEnum pins the P7 continue-phase enum of spec §4.2.5/§7.2:
// the empty string means "no phase", the three values mirror the CHECK
// constraint, and anything else is rejected with the stable reason.
func TestContinuePhaseEnum(t *testing.T) {
	valid := []string{
		"",
		domain.ContinuePhaseClarifying,
		domain.ContinuePhaseDesigning,
		domain.ContinuePhaseExecuting,
	}
	for _, phase := range valid {
		t.Run("valid "+phase, func(t *testing.T) {
			if err := domain.ValidateContinuePhase(phase); err != nil {
				t.Fatalf("ValidateContinuePhase(%q): %v", phase, err)
			}
		})
	}

	for _, phase := range []string{"running", "CLARIFYING", "clarify", "executing "} {
		t.Run("invalid "+phase, func(t *testing.T) {
			err := domain.ValidateContinuePhase(phase)
			if err == nil {
				t.Fatalf("ValidateContinuePhase(%q) accepted an unknown phase", phase)
			}
			domainErr, ok := err.(*domain.Error)
			if !ok {
				t.Fatalf("error type = %T, want *domain.Error", err)
			}
			if domainErr.Kind != domain.KindInvalid || domainErr.Reason != "invalid_continue_phase" {
				t.Fatalf("error = %s/%s, want invalid/invalid_continue_phase", domainErr.Kind, domainErr.Reason)
			}
		})
	}
}

// TestVersionValidateContinuePhase pins that version validation accepts the
// enum (and the empty string) and rejects an unknown phase.
func TestVersionValidateContinuePhase(t *testing.T) {
	base := domain.Version{Name: "Order v2", Status: domain.VersionContinuing}

	valid := base
	valid.ContinuePhase = domain.ContinuePhaseClarifying
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid continue phase rejected: %v", err)
	}

	invalid := base
	invalid.ContinuePhase = "running"
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid continue phase accepted by Version.Validate")
	}
}

// TestVersionChangeKinds pins the D50 /changes kinds; the HTTP and application
// layers switch on these exact strings.
func TestVersionChangeKinds(t *testing.T) {
	kinds := map[string]string{
		"requirement_added": domain.VersionChangeRequirementAdded,
		"batch_finished":    domain.VersionChangeBatchFinished,
		"report_published":  domain.VersionChangeReportPublished,
		"issue_created":     domain.VersionChangeIssueCreated,
		"code_changed":      domain.VersionChangeCodeChanged,
	}
	for want, got := range kinds {
		if got != want {
			t.Fatalf("change kind = %q, want %q", got, want)
		}
	}
}
