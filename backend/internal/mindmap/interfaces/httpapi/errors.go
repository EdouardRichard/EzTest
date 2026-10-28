package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// conflictReasons are the spec §7.4 reasons the HTTP layer maps to 409 even when
// the domain classified them as invalid (spec §7.4 note): they describe a graph
// state or business conflict, not a malformed request field.
var conflictReasons = map[string]struct{}{
	"version_state_conflict":           {},
	"baseline_frozen":                  {},
	"case_revision_conflict":           {},
	"invalid_case_tree":                {},
	"no_cases":                         {},
	"unconfirmed_requirements":         {},
	"requirement_not_clarified":        {},
	"blocker_findings_open":            {},
	"run_in_progress":                  {},
	"run_state_conflict":               {},
	"graph_too_large":                  {},
	"duplicate_case_source":            {},
	"duplicate_key":                    {},
	"export_conflict":                  {},
	"environment_not_allowed":          {},
	"exploration_requires_environment": {},
	"environment_unreachable":          {},
	"ai_unavailable":                   {},
	"code_repo_unavailable":            {},
}

// notFoundReason reports whether a reason is one of the spec §7.4
// "<resource>_not_found" values.
func notFoundReason(reason string) bool {
	return reason == "not_found" || strings.HasSuffix(reason, "_not_found")
}

// detailCarrier is implemented by application errors that enrich the shared
// envelope with structured details. The approve gate (BlockerFindingsError) is
// the P5 case: it carries the open blocker finding ids next to its domain error.
type detailCarrier interface {
	Details() map[string]any
}

// requestError is a transport-level request failure (a malformed path id, an
// invalid query parameter or a body that is not a JSON object). Spec §7.1 makes
// those 400s: they are not domain field validations.
type requestError struct {
	status  int
	code    string
	message string
	details map[string]any
}

// Error implements error.
func (e *requestError) Error() string { return e.message }

// newBadRequest builds a 400 invalid_argument failure with its field/reason.
func newBadRequest(field, reason, message string) *requestError {
	details := map[string]any{"message": message}
	if field != "" {
		details["field"] = field
	}
	if reason != "" {
		details["reason"] = reason
	}
	return &requestError{
		status:  http.StatusBadRequest,
		code:    platform.CodeInvalidArgument,
		message: message,
		details: details,
	}
}

// newForbidden builds the 403 forbidden failure of the admin-only exploration
// run and the anchor-delete ownership rule (spec §7.2 endpoints 16/17).
func newForbidden(reason, message string) *requestError {
	return &requestError{
		status:  http.StatusForbidden,
		code:    platform.CodeForbidden,
		message: message,
		details: map[string]any{"message": message, "reason": reason},
	}
}

// writeServiceError renders any handler failure through the shared error
// envelope. Mapping (spec §7.4):
//
//	requestError        -> its own 400/403 status
//	*_not_found         -> 404 not_found
//	forbidden reason    -> 403 forbidden
//	export_corrupted    -> 500 internal (details.reason preserved)
//	the 409 reason list -> 409 conflict (even for domain-invalid reasons)
//	invalid_*           -> 422 invalid_argument
//	anything else       -> 500 internal
//
// The stable reason always travels in details.reason; internal failures never
// leak their message.
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var requestErr *requestError
	if errors.As(err, &requestErr) && requestErr != nil {
		platform.WriteError(w, r, requestErr.status, requestErr.code, requestErr.message,
			platform.WithDetails(requestErr.details))
		return
	}

	// The approve gate keeps the domain error shape and adds findingIds.
	var detailed detailCarrier
	if errors.As(err, &detailed) && detailed != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr != nil {
			writeDomainError(w, r, domainErr, detailed.Details())
			return
		}
	}

	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr == nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}
	writeDomainError(w, r, domainErr, nil)
}

// writeDomainError applies the reason/kind mapping and renders the envelope. An
// extra details map (when present) is merged over the standard fields, so a
// blocker error keeps both its reason and its finding ids.
func writeDomainError(w http.ResponseWriter, r *http.Request, domainErr *domain.Error, extra map[string]any) {
	status, code := domainErrorStatus(domainErr)
	message := domainErr.Message
	if status == http.StatusInternalServerError {
		// An internal failure never echoes its message to the client.
		message = "internal server error"
	}
	if message == "" {
		message = http.StatusText(status)
	}
	details := map[string]any{"message": message}
	if domainErr.Field != "" {
		details["field"] = domainErr.Field
	}
	if domainErr.Reason != "" {
		details["reason"] = domainErr.Reason
	}
	for key, value := range extra {
		details[key] = value
	}
	platform.WriteError(w, r, status, code, message, platform.WithDetails(details))
}

// domainErrorStatus maps one domain error onto the shared status/code pair.
func domainErrorStatus(domainErr *domain.Error) (int, string) {
	reason := domainErr.Reason
	switch {
	case reason == "forbidden":
		return http.StatusForbidden, platform.CodeForbidden
	case notFoundReason(reason), domainErr.Kind == domain.KindNotFound:
		return http.StatusNotFound, platform.CodeNotFound
	case reason == "export_corrupted":
		// A sha256 mismatch is a server fault, not a client error (spec §7.2
		// endpoint 27); the reason stays in details for the operator.
		return http.StatusInternalServerError, platform.CodeInternal
	case inConflictReasons(reason):
		return http.StatusConflict, platform.CodeConflict
	case domainErr.Kind == domain.KindInvalid:
		return http.StatusUnprocessableEntity, platform.CodeInvalidArgument
	case domainErr.Kind == domain.KindConflict || domainErr.Kind == domain.KindStateConflict:
		return http.StatusConflict, platform.CodeConflict
	default:
		return http.StatusInternalServerError, platform.CodeInternal
	}
}

// inConflictReasons reports whether the reason belongs to the spec §7.4 409 set.
func inConflictReasons(reason string) bool {
	_, ok := conflictReasons[reason]
	return ok
}
