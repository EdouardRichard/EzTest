package domain_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// proposedClass converts the table's string proposal into the optional class
// value consumed by Classify. An empty value means the caller has no proposal.
func proposedClass(value string) domain.FailureClass {
	return domain.FailureClass(value)
}

func TestClassifyToolCodeMatrix(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		proposed    string
		evidence    int
		wantVerdict domain.Verdict
		wantClass   domain.FailureClass
		wantReason  string
	}{
		{
			name: "host block is environment", code: "host_not_allowed",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassEnvironment,
			wantReason: "host_not_allowed",
		},
		{
			name: "dns failure is environment", code: "dns_failed",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassEnvironment,
			wantReason: "dns_failed",
		},
		{
			name: "executor unavailable is environment", code: "executor_unavailable",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassEnvironment,
			wantReason: "executor_unavailable",
		},
		{
			name: "session unauthorized is session", code: "session_unauthorized",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassSession,
			wantReason: "session_unauthorized",
		},
		{
			name: "budget exhausted is model", code: "budget_exhausted",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassModel,
			wantReason: "budget_exhausted",
		},
		{
			name: "db assertion kind is case", code: "unsupported_assertion_kind",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "unsupported_assertion_kind",
		},
		{
			name: "internal tool error is model", code: "internal",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassModel,
			wantReason: "internal",
		},
		{
			name: "invalid arguments is case", code: "invalid_arguments",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "invalid_arguments",
		},
		{
			name: "target not found with evidence is application", code: "target_not_found", evidence: 1,
			wantVerdict: domain.VerdictFailed, wantClass: domain.FailureClassApplication,
			wantReason: "target_not_found",
		},
		{
			name: "target not found without evidence degrades", code: "target_not_found",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "evidence_missing",
		},
		{
			name: "timeout with evidence is application", code: "timeout", evidence: 2,
			wantVerdict: domain.VerdictFailed, wantClass: domain.FailureClassApplication,
			wantReason: "timeout",
		},
		{
			name: "timeout without evidence defaults to environment", code: "timeout",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassEnvironment,
			wantReason: "timeout",
		},
		{
			name: "assertion mismatch with evidence is application", code: "assertion_mismatch", evidence: 1,
			wantVerdict: domain.VerdictFailed, wantClass: domain.FailureClassApplication,
			wantReason: "assertion_mismatch",
		},
		{
			name: "assertion mismatch without evidence degrades", code: "assertion_mismatch",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "evidence_missing",
		},
		{
			name: "tool code wins over proposed class", code: "host_not_allowed",
			proposed: "application", evidence: 5,
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassEnvironment,
			wantReason: "host_not_allowed",
		},
		{
			name: "proposed environment normalizes to blocked", proposed: "environment",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassEnvironment,
			wantReason: "environment",
		},
		{
			name: "proposed session normalizes to blocked", proposed: "session",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassSession,
			wantReason: "session",
		},
		{
			name: "proposed model is inconclusive", proposed: "model",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassModel,
			wantReason: "model",
		},
		{
			name: "proposed case is inconclusive", proposed: "case",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "case",
		},
		{
			name: "proposed application with evidence is failed", proposed: "application", evidence: 1,
			wantVerdict: domain.VerdictFailed, wantClass: domain.FailureClassApplication,
			wantReason: "application",
		},
		{
			name: "proposed application without evidence degrades", proposed: "application",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "evidence_missing",
		},
		{
			name: "unknown code never becomes application", code: "mystery_error",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "mystery_error",
		},
		{
			name: "no code and no proposal is unclassified", wantVerdict: domain.VerdictInconclusive,
			wantClass: domain.FailureClassCase, wantReason: "unclassified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, class, reason := domain.Classify(tt.code, proposedClass(tt.proposed), tt.evidence)
			if verdict != tt.wantVerdict || class != tt.wantClass || reason != tt.wantReason {
				t.Fatalf("Classify(%q, %q, %d) = %s/%s/%q, want %s/%s/%q",
					tt.code, tt.proposed, tt.evidence, verdict, class, reason,
					tt.wantVerdict, tt.wantClass, tt.wantReason)
			}
		})
	}
}

func TestClassifyNeverReturnsApplicationForEnvOrSession(t *testing.T) {
	for _, code := range []string{
		"host_not_allowed", "dns_failed", "executor_unavailable",
		"session_unauthorized", "session_expired", "no_session",
	} {
		// Even a proposed application failure with plenty of evidence must not
		// turn an environment/session tool error into an application verdict.
		verdict, class, _ := domain.Classify(code, proposedClass("application"), 9)
		if class == domain.FailureClassApplication || verdict == domain.VerdictFailed {
			t.Fatalf("Classify(%q) = %s/%s, must not be application/failed", code, verdict, class)
		}
	}

	for _, proposal := range []domain.FailureClass{
		domain.FailureClassEnvironment, domain.FailureClassSession,
	} {
		verdict, class, _ := domain.Classify("", proposal, 9)
		if class == domain.FailureClassApplication || verdict == domain.VerdictFailed {
			t.Fatalf("proposed %q = %s/%s, must not be application/failed", proposal, verdict, class)
		}
	}
}

func TestClassifyModelAndBudgetNeverFailed(t *testing.T) {
	for _, code := range []string{"budget_exhausted", "context_overflow", "model_error", "internal"} {
		verdict, class, _ := domain.Classify(code, proposedClass("application"), 4)
		if verdict != domain.VerdictInconclusive || class != domain.FailureClassModel {
			t.Fatalf("Classify(%q) = %s/%s, want inconclusive/model", code, verdict, class)
		}
	}
}
