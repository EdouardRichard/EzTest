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
			name:        "tool call budget exhaustion is model",
			code:        "tool_call_budget_exhausted",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassModel,
			wantReason: "tool_call_budget_exhausted",
		},
		{
			name: "executor timeout is model", code: "executor_timeout",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassModel,
			wantReason: "executor_timeout",
		},
		{
			name: "db assertion kind is case", code: "unsupported_assertion_kind",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "unsupported_assertion_kind",
		},
		{
			name: "db connection failure is environment", code: "db_connection_failed",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassEnvironment,
			wantReason: "db_connection_failed",
		},
		{
			name: "db target missing is environment", code: "db_target_missing",
			wantVerdict: domain.VerdictBlocked, wantClass: domain.FailureClassEnvironment,
			wantReason: "db_target_missing",
		},
		{
			name: "db query failure is case", code: "db_query_failed",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "db_query_failed",
		},
		{
			name: "db readonly violation is case", code: "db_readonly_violation",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "db_readonly_violation",
		},
		{
			name: "db result truncated is case", code: "db_result_truncated",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "db_result_truncated",
		},
		{
			name: "db expected invalid is case", code: "db_expected_invalid",
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "db_expected_invalid",
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
		"db_connection_failed", "db_target_missing",
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

// TestClassifyDBAssertionMismatchIsApplication pins that a mismatched db
// assertion is the one db failure that blames the application (spec §6.6.2):
// with evidence it is failed(application), without evidence it degrades.
func TestClassifyDBAssertionMismatchIsApplication(t *testing.T) {
	for _, code := range []string{"assertion_mismatch", "assertion_failed"} {
		verdict, class, reason := domain.Classify(code, "", 1)
		if verdict != domain.VerdictFailed || class != domain.FailureClassApplication || reason != code {
			t.Fatalf("Classify(%q, evidence) = %s/%s/%q, want failed/application/%q",
				code, verdict, class, reason, code)
		}
		verdict, class, reason = domain.Classify(code, "", 0)
		if verdict != domain.VerdictInconclusive || class != domain.FailureClassCase || reason != "evidence_missing" {
			t.Fatalf("Classify(%q, no evidence) = %s/%s/%q, want inconclusive/case/evidence_missing",
				code, verdict, class, reason)
		}
	}
}
