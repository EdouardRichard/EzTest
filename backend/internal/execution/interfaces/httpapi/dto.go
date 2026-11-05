package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// startWaveRequest is POST /api/versions/{id}/waves (API-42). case_ids is the
// optional wave scope; an absent or empty array keeps every current+confirmed
// case (the handler calls the unselected intent scan then).
type startWaveRequest struct {
	CaseIDs []string `json:"case_ids"`
}

// parse converts the decimal-string case IDs; a nil/empty list means "no
// selection". A non-decimal or non-positive ID is a 400.
func (r startWaveRequest) parse() ([]int64, error) {
	if len(r.CaseIDs) == 0 {
		return nil, nil
	}
	caseIDs := make([]int64, 0, len(r.CaseIDs))
	for _, raw := range r.CaseIDs {
		caseID, err := parsePositiveIDField("case_ids", raw)
		if err != nil {
			return nil, err
		}
		caseIDs = append(caseIDs, caseID)
	}
	return caseIDs, nil
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

// createQuestionRequest is POST /versions/{id}/conversations/{cid}/questions.
// version/conversation/agent-run identity is never part of the body.
type createQuestionRequest struct {
	Questions []questionItemInput `json:"questions"`
	BatchID   string              `json:"batch_id"`
	ExpiresAt *string             `json:"expires_at"`
}

type questionItemInput struct {
	ID            string                `json:"id"`
	Text          string                `json:"text"`
	Options       []questionOptionInput `json:"options"`
	Multiple      bool                  `json:"multiple"`
	AllowFreeText bool                  `json:"allow_free_text"`
	Required      bool                  `json:"required"`
	SourceRefs    []string              `json:"source_refs"`
}

type questionOptionInput struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// parse converts the transport shape into the question payload; structural
// validation (1-5, unique ids, required/free-text) stays in the domain service.
func (r createQuestionRequest) parse() ([]execution.HumanQuestion, *time.Time, error) {
	questions := make([]execution.HumanQuestion, 0, len(r.Questions))
	for _, item := range r.Questions {
		options := make([]execution.HumanQuestionOption, 0, len(item.Options))
		for _, option := range item.Options {
			options = append(options, execution.HumanQuestionOption{ID: option.ID, Label: option.Label})
		}
		questions = append(questions, execution.HumanQuestion{
			ID:            item.ID,
			Text:          item.Text,
			Options:       options,
			Multiple:      item.Multiple,
			AllowFreeText: item.AllowFreeText,
			Required:      item.Required,
			SourceRefs:    append([]string{}, item.SourceRefs...),
		})
	}
	var expiresAt *time.Time
	if r.ExpiresAt != nil && strings.TrimSpace(*r.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*r.ExpiresAt))
		if err != nil {
			return nil, nil, platform.Invalid("expires_at", "must be an RFC3339 timestamp")
		}
		expiresAt = &parsed
	}
	return questions, expiresAt, nil
}

// answerQuestionRequest is POST /versions/{id}/questions/{qid}/answer.
type answerQuestionRequest struct {
	Answers []questionAnswerInput `json:"answers"`
}

type questionAnswerInput struct {
	QuestionID string   `json:"question_id"`
	Selected   []string `json:"selected"`
	Text       string   `json:"text"`
}

// parse normalizes the submitted answers. Answer semantics (required, option
// membership, multiple) are validated by the execution domain.
func (r answerQuestionRequest) parse() ([]execution.HumanQuestionAnswer, error) {
	if r.Answers == nil {
		return nil, platform.Invalid("answers", "answers is required")
	}
	answers := make([]execution.HumanQuestionAnswer, 0, len(r.Answers))
	for _, answer := range r.Answers {
		answers = append(answers, execution.HumanQuestionAnswer{
			QuestionID: answer.QuestionID,
			Selected:   append([]string{}, answer.Selected...),
			Text:       answer.Text,
		})
	}
	return answers, nil
}

// skipQuestionRequest is POST /versions/{id}/questions/{qid}/skip.
type skipQuestionRequest struct {
	Reason string `json:"reason"`
}

// questionOwnerID reads the owning Host agent run identity of a question write.
// Host-internal endpoint contract; Task 37 mounts these routes behind the Host
// run context, which injects this header.
func questionOwnerID(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("X-Agent-Run-Id"))
	if raw == "" {
		return 0, platform.Invalid("agent_run_id", "the X-Agent-Run-Id host run header is required")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, platform.Invalid("agent_run_id", "must be a positive decimal ID")
	}
	return value, nil
}
