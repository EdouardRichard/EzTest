package domain

import "strings"

// Failure-class reason identifiers returned by Classify. The tool code is
// preferred because it is the concrete, auditable cause; a missing application
// evidence downgrade uses the spec §5.8 evidence_missing reason.
const (
	reasonEvidenceMissing = "evidence_missing"
	reasonUnclassified    = "unclassified"
)

// toolCodeClass groups tool error codes into the failure classes of spec §5.8.
// The grouping is deliberately explicit instead of prefix matching so an
// unknown tool code can never accidentally become an application failure.
type toolCodeClass int

const (
	toolCodeUnknown toolCodeClass = iota
	toolCodeEnvironment
	toolCodeSession
	toolCodeModel
	toolCodeCase
	toolCodeApplication
)

// classifyToolCode maps a concrete tool error code to its failure-class group.
// host/network/executor errors and database connection/target problems are
// environment, authentication errors are session, model/budget errors are model,
// malformed submissions plus SQL/read-only/truncation/expected-value db
// problems are case, and only target/assertion evidence problems may be
// application failures.
func classifyToolCode(code string) toolCodeClass {
	switch strings.ToLower(code) {
	case "host_not_allowed", "dns_failed", "dns_error", "connection_refused",
		"connection_reset", "network_error", "tls_error", "env_unreachable",
		"executor_unavailable", "executor_unreachable",
		"db_connection_failed", "db_target_missing":
		return toolCodeEnvironment
	case "session_unauthorized", "session_expired", "session_invalid", "no_session":
		return toolCodeSession
	case "budget_exhausted", "budget_exceeded", "context_overflow", "max_turns",
		"model_error", "llm_error", "gateway_error", "internal":
		return toolCodeModel
	case "unsupported_assertion_kind", "invalid_arguments", "invalid_selector",
		"invalid_finish", "target_ambiguous", "case_invalid", "invalid_case",
		"agent_output_invalid", "db_query_failed", "db_readonly_violation",
		"db_result_truncated", "db_expected_invalid":
		return toolCodeCase
	case "target_not_found", "timeout", "assertion_mismatch", "assertion_failed",
		"step_failed":
		return toolCodeApplication
	default:
		return toolCodeUnknown
	}
}

// Classify derives the terminal verdict and failure class for a case run from a
// tool error code, the agent's proposed failure class and the evidence count.
//
// The tool code wins: an environment, session, model or case code never yields
// an application failure, even when the agent proposes one. A database
// connection/target problem therefore blocks as environment, while a SQL,
// read-only, truncation or invalid-expected problem stays inconclusive(case).
// A database assertion mismatch is the db failure that may blame the
// application (existing assertion_mismatch/assertion_failed codes).
// Application failures require at least one piece of evidence; without evidence
// they are downgraded to inconclusive(case, evidence_missing) as required by
// spec §5.8. timeout without evidence defaults to blocked(environment) because an
// unevidenced timeout is most likely an environment problem. A proposal is
// adopted only when no tool code is present. Unknown codes are isolated as
// inconclusive(case) instead of being blamed on the application.
//
// The third return value is the audit reason: the tool code when one was
// supplied, evidence_missing on an evidence downgrade, the adopted class for a
// proposal-only classification, or unclassified when neither input is present.
func Classify(toolCode string, proposed FailureClass, evidenceCount int) (Verdict, FailureClass, string) {
	code := strings.TrimSpace(toolCode)
	if code != "" {
		switch classifyToolCode(code) {
		case toolCodeEnvironment:
			return VerdictBlocked, FailureClassEnvironment, code
		case toolCodeSession:
			return VerdictBlocked, FailureClassSession, code
		case toolCodeModel:
			return VerdictInconclusive, FailureClassModel, code
		case toolCodeCase:
			return VerdictInconclusive, FailureClassCase, code
		case toolCodeApplication:
			if evidenceCount > 0 {
				return VerdictFailed, FailureClassApplication, code
			}
			if strings.EqualFold(code, "timeout") {
				// Unevidenced timeouts are treated as an environment block by
				// default; they must not claim an application defect.
				return VerdictBlocked, FailureClassEnvironment, code
			}
			return VerdictInconclusive, FailureClassCase, reasonEvidenceMissing
		default:
			return VerdictInconclusive, FailureClassCase, code
		}
	}

	switch proposed {
	case FailureClassEnvironment:
		return VerdictBlocked, FailureClassEnvironment, string(proposed)
	case FailureClassSession:
		return VerdictBlocked, FailureClassSession, string(proposed)
	case FailureClassModel:
		return VerdictInconclusive, FailureClassModel, string(proposed)
	case FailureClassCase:
		return VerdictInconclusive, FailureClassCase, string(proposed)
	case FailureClassApplication:
		if evidenceCount > 0 {
			return VerdictFailed, FailureClassApplication, string(proposed)
		}
		return VerdictInconclusive, FailureClassCase, reasonEvidenceMissing
	}
	return VerdictInconclusive, FailureClassCase, reasonUnclassified
}
