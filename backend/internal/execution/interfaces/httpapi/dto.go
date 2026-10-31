package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
)

const maxRequestBody = 1 << 20

// createIntentRequest is POST /execution/intents (contract CreateIntentRequest).
type createIntentRequest struct {
	CaseVersionID string `json:"case_version_id"`
	Purpose       string `json:"purpose"`
}

func (r createIntentRequest) parse() (int64, execution.Purpose, error) {
	caseVersionID, err := parsePositiveIDField("case_version_id", r.CaseVersionID)
	if err != nil {
		return 0, "", err
	}
	purpose := execution.Purpose(strings.TrimSpace(r.Purpose))
	if purpose != execution.PurposeRetest && purpose != execution.PurposeManual {
		return 0, "", platform.Invalid("purpose", "must be retest or manual")
	}
	return caseVersionID, purpose, nil
}

// pauseWaveRequest is POST /waves/{waveId}/pause (contract PauseWaveRequest).
type pauseWaveRequest struct {
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

func (r pauseWaveRequest) parse() (string, string, error) {
	mode := strings.TrimSpace(r.Mode)
	if mode == "" {
		mode = execution.PauseModeGraceful
	}
	if mode != execution.PauseModeGraceful && mode != execution.PauseModeImmediate {
		return "", "", platform.Invalid("mode", "must be graceful or immediate")
	}
	if len(r.Reason) > 200 {
		return "", "", platform.Invalid("reason", "must be at most 200 characters")
	}
	return mode, strings.TrimSpace(r.Reason), nil
}

// pauseCaseRunRequest is POST /case-runs/{caseRunId}/pause. P3 freezes the
// case-level pause mode to immediate (spec §5.7 / contract).
type pauseCaseRunRequest struct {
	Mode string `json:"mode"`
}

func (r pauseCaseRunRequest) parse() (string, error) {
	mode := strings.TrimSpace(r.Mode)
	if mode != execution.PauseModeImmediate {
		return "", platform.Invalid("mode", "case-level pause only supports immediate")
	}
	return execution.PauseModeImmediate, nil
}

// resolveHumanRequestRequest is POST /human-requests/{id}/resolve.
type resolveHumanRequestRequest struct {
	Response *humanResponseInput `json:"response"`
}

type humanResponseInput struct {
	Code *string `json:"code"`
	Note *string `json:"note"`
}

// parse normalizes the one-time response. Unknown keys are rejected by the
// strict decoder; code/note are trimmed and bounded.
func (r resolveHumanRequestRequest) parse() (map[string]any, error) {
	if r.Response == nil {
		return nil, platform.Invalid("response", "response is required")
	}
	response := map[string]any{}
	if r.Response.Code != nil {
		code := strings.TrimSpace(*r.Response.Code)
		if len(code) > 64 {
			return nil, platform.Invalid("response.code", "must be at most 64 characters")
		}
		response[execution.HumanResponseCode] = code
	}
	if r.Response.Note != nil {
		note := strings.TrimSpace(*r.Response.Note)
		if len(note) > 2000 {
			return nil, platform.Invalid("response.note", "must be at most 2000 characters")
		}
		response[execution.HumanResponseNote] = note
	}
	return response, nil
}

// pageParams parses the shared before/limit pagination (spec §9.1).
func pageParams(r *http.Request) (int, *int64, error) {
	query := r.URL.Query()
	limit := pagination.Limit(query.Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
	raw := strings.TrimSpace(query.Get("before"))
	if raw == "" {
		return limit, nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, nil, platform.Invalid("before", "must be a positive decimal ID")
	}
	return limit, &value, nil
}

// optionalQueryID parses an optional positive decimal query ID (filters).
func optionalQueryID(r *http.Request, field string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(field))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return nil, platform.Invalid(field, "must be a positive decimal ID")
	}
	return &value, nil
}

// parsePositiveIDField parses a required positive decimal body field.
func parsePositiveIDField(field, raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, platform.Invalid(field, "must be a positive decimal ID")
	}
	return value, nil
}

// requireEnum validates an optional enum query filter.
func requireEnum(field, raw string, allowed ...string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", platform.Invalid(field, "must be one of "+strings.Join(allowed, ", "))
}

// decodeBody decodes one JSON request body with the shared size cap.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := decoder.Decode(dst); err != nil {
		return platform.Invalid("body", "request body must be a valid JSON object")
	}
	return nil
}

// decodeStrictBody decodes one JSON request body and rejects unknown fields
// (contract resolve response has additionalProperties: false).
func decodeStrictBody(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return platform.Invalid("body", "request body must be a valid JSON object")
	}
	return nil
}
