package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Query bounds of spec §5.1. The service itself only requires a positive limit;
// the HTTP contract adds the public 50–2000 / 1–500 windows. The discrepancy
// with the C5 fix note ("limit 上限 1000") is resolved in favour of the §5.1
// contract table.
const (
	defaultChainLimit = 300
	minChainLimit     = 50
	maxChainLimit     = 2000

	defaultRequirementLimit = 100
	minRequirementLimit     = 1
	maxRequirementLimit     = 500

	searchLimitRunes = 100
)

// handleExplorationChain serves GET /api/versions/{versionId}/exploration-chain.
func (h *Handlers) handleExplorationChain(w http.ResponseWriter, r *http.Request) {
	versionID, err := parseDecimalID("versionId", r.PathValue("versionId"))
	if err != nil {
		writeChainError(w, r, err)
		return
	}
	query, err := parseChainQuery(r)
	if err != nil {
		writeChainError(w, r, err)
		return
	}
	query.VersionID = versionID

	snapshot, err := h.builder.Build(r.Context(), query)
	if err != nil {
		writeChainError(w, r, err)
		return
	}

	// The read model is real-time data: no cross-request or browser caching
	// (spec §5.1).
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, snapshot)
}

// parseChainQuery validates every §5.1 query parameter at the HTTP boundary.
func parseChainQuery(r *http.Request) (chainapp.Query, error) {
	values := r.URL.Query()
	query := chainapp.Query{IncludeSupplement: true}

	batchRaw := strings.TrimSpace(values.Get("batchId"))
	switch {
	case batchRaw == "":
	case strings.EqualFold(batchRaw, "all"):
		query.BatchID = "all"
	default:
		batchID, err := parseDecimalID("batchId", batchRaw)
		if err != nil {
			return chainapp.Query{}, err
		}
		query.BatchID = batchID
	}

	requirementRaw := strings.TrimSpace(values.Get("requirementId"))
	if requirementRaw != "" {
		requirementID, err := parseDecimalID("requirementId", requirementRaw)
		if err != nil {
			return chainapp.Query{}, err
		}
		query.RequirementID = requirementID
	}

	resultStatus := strings.TrimSpace(values.Get("resultStatus"))
	if resultStatus != "" {
		if err := validateEnum("resultStatus", resultStatus,
			"passed", "failed", "blocked", "inconclusive", "skipped", "interrupted"); err != nil {
			return chainapp.Query{}, err
		}
	}
	query.ResultStatus = resultStatus

	issueStatus := strings.TrimSpace(values.Get("issueStatus"))
	if issueStatus != "" {
		if err := validateEnum("issueStatus", issueStatus, "open", "confirmed", "dismissed"); err != nil {
			return chainapp.Query{}, err
		}
	}
	query.IssueStatus = issueStatus

	issueSeverity := strings.TrimSpace(values.Get("issueSeverity"))
	if issueSeverity != "" {
		if err := validateEnum("issueSeverity", issueSeverity,
			"blocker", "major", "minor", "info"); err != nil {
			return chainapp.Query{}, err
		}
	}
	query.IssueSeverity = issueSeverity

	search := strings.TrimSpace(values.Get("q"))
	if utf8.RuneCountInString(search) > searchLimitRunes {
		return chainapp.Query{}, invalidArgument("q", 0, searchLimitRunes,
			"q must be at most 100 characters")
	}
	query.Search = search

	abnormalOnly, err := parseOptionalBool(values, "abnormalOnly", false)
	if err != nil {
		return chainapp.Query{}, err
	}
	query.AbnormalOnly = abnormalOnly

	includeSupplement, err := parseOptionalBool(values, "includeSupplement", true)
	if err != nil {
		return chainapp.Query{}, err
	}
	query.IncludeSupplement = includeSupplement

	revalidationOnly, err := parseOptionalBool(values, "revalidationOnly", false)
	if err != nil {
		return chainapp.Query{}, err
	}
	query.RevalidationOnly = revalidationOnly

	limit, err := parseBoundedInt(values, "limit", defaultChainLimit, minChainLimit, maxChainLimit)
	if err != nil {
		return chainapp.Query{}, err
	}
	query.Limit = limit

	requirementLimit, err := parseBoundedInt(values, "requirementLimit",
		defaultRequirementLimit, minRequirementLimit, maxRequirementLimit)
	if err != nil {
		return chainapp.Query{}, err
	}
	query.RequirementLimit = requirementLimit

	return query, nil
}

// parseDecimalID validates a positive decimal id and returns its canonical
// spelling.
func parseDecimalID(field, raw string) (string, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return "", invalidArgument(field, 1, 0, field+" must be a positive decimal id")
	}
	return strconv.FormatInt(id, 10), nil
}

// parseBoundedInt parses an optional bounded integer query parameter. An empty
// value returns defaultValue; a non-integer or out-of-range value is a 400 with
// details{field,min,max,reason}.
func parseBoundedInt(values map[string][]string, field string, defaultValue, min, max int) (int, error) {
	raw := ""
	if rawValues, ok := values[field]; ok && len(rawValues) > 0 {
		raw = strings.TrimSpace(rawValues[0])
	}
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, invalidArgument(field, min, max, field+" must be an integer")
	}
	if value < min || value > max {
		return 0, invalidArgument(field, min, max,
			fmt.Sprintf("%s must be between %d and %d", field, min, max))
	}
	return value, nil
}

// parseOptionalBool parses an optional boolean query parameter, keeping the
// default when the parameter is absent.
func parseOptionalBool(values map[string][]string, field string, defaultValue bool) (bool, error) {
	rawValues, ok := values[field]
	if !ok || len(rawValues) == 0 {
		return defaultValue, nil
	}
	raw := strings.TrimSpace(rawValues[0])
	if raw == "" {
		return false, invalidArgument(field, 0, 0, field+" must be a boolean")
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, invalidArgument(field, 0, 0, field+" must be a boolean")
	}
	return value, nil
}

// validateEnum rejects a value outside the closed enum of a query parameter.
func validateEnum(field, value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return invalidArgument(field, 0, 0,
		field+" must be one of "+strings.Join(allowed, ", "))
}

func invalidArgument(field string, min, max int, message string) *chainapp.ErrInvalidArgument {
	return &chainapp.ErrInvalidArgument{
		Field:   field,
		Reason:  chainapp.ReasonInvalidArgument,
		Min:     min,
		Max:     max,
		Message: message,
	}
}

// ---------------------------------------------------------------------------
// Envelopes and error mapping
// ---------------------------------------------------------------------------

// dataEnvelope is the success shape of the chain endpoint: {"data": snapshot}.
type dataEnvelope struct {
	Data any `json:"data"`
}

// writeData writes {"data": data} with the shared JSON content type.
func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dataEnvelope{Data: data})
}

// writeChainError maps the chain service errors onto the shared error envelope:
// invalid queries and malformed parameters are 400 invalid_argument with
// details{field,reason,min,max}; unknown versions/batches are 404 not_found;
// everything else is a 500 internal that never leaks the provider message.
func writeChainError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal,
			"internal server error")
		return
	}

	var invalid *chainapp.ErrInvalidArgument
	if errors.As(err, &invalid) {
		details := map[string]any{"reason": chainapp.ReasonInvalidArgument}
		if invalid.Field != "" {
			details["field"] = invalid.Field
		}
		if invalid.Reason != "" {
			details["reason"] = invalid.Reason
		}
		if invalid.Min != 0 {
			details["min"] = invalid.Min
		}
		if invalid.Max != 0 {
			details["max"] = invalid.Max
		}
		message := invalid.Message
		if message == "" {
			message = "invalid argument"
		}
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
			message, platform.WithDetails(details))
		return
	}

	var notFound *chainapp.ErrNotFound
	if errors.As(err, &notFound) {
		message := notFound.Message
		if message == "" && notFound.Resource != "" {
			message = notFound.Resource + " not found"
		}
		if message == "" {
			message = "not found"
		}
		details := map[string]any{}
		if notFound.Resource != "" {
			details["reason"] = notFound.Resource + "_not_found"
		}
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound,
			message, platform.WithDetails(details))
		return
	}

	platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal,
		"internal server error")
}
