package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	"github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// maxEvalRequestBodyBytes bounds one POST body. The runner posts counters and a
// small summary document; a megabyte is far above the documented payload and
// keeps a broken client from buffering an unbounded body.
const maxEvalRequestBodyBytes = 1 << 20

// handleRecord serves POST /api/quality/eval-runs.
func (h *Handlers) handleRecord(w http.ResponseWriter, r *http.Request) {
	var body recordRunRequest
	if err := decodeEvalJSON(w, r, &body); err != nil {
		writeInvalidArgument(w, r, "body", "request body must be a JSON object")
		return
	}
	run, err := h.runs.Record(r.Context(), qualityapp.RecordInput{
		Mode:          domain.Mode(body.Mode),
		CorpusVersion: body.CorpusVersion,
		Model:         body.Model,
		Outcome:       domain.Status(body.Status),
		Sample:        body.Sample,
		Baseline:      body.Baseline,
		Summary:       body.Summary,
		StartedAt:     evalTime(body.StartedAt),
		FinishedAt:    evalTime(body.FinishedAt),
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, newEvalRunDTO(run))
}

// handleList serves GET /api/quality/eval-runs?limit=20.
func (h *Handlers) handleList(w http.ResponseWriter, r *http.Request) {
	limit, err := parseEvalLimit(r)
	if err != nil {
		writeInvalidArgument(w, r, "limit",
			"limit must be between 1 and "+strconv.Itoa(qualityapp.MaxListLimit))
		return
	}
	runs, err := h.runs.List(r.Context(), limit)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	items := make([]evalRunDTO, 0, len(runs))
	for _, run := range runs {
		items = append(items, newEvalRunDTO(run))
	}
	writeData(w, http.StatusOK, items)
}

// handleGet serves GET /api/quality/eval-runs/{id}.
func (h *Handlers) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := parseEvalPathID(r.PathValue("id"))
	if err != nil {
		writeInvalidArgument(w, r, "id", "id must be a positive decimal id")
		return
	}
	run, err := h.runs.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newEvalRunDTO(run))
}

// recordRunRequest is the POST /api/quality/eval-runs body. Times are RFC 3339
// and optional (the service falls back to its clock). Status is only meaningful
// as "error": a terminal passed/failed verdict is derived from the metrics, so
// a runner cannot claim a pass.
type recordRunRequest struct {
	Mode          string             `json:"mode"`
	CorpusVersion string             `json:"corpusVersion"`
	Model         string             `json:"model"`
	Status        string             `json:"status"`
	Sample        *domain.Sample     `json:"sample"`
	Baseline      map[string]float64 `json:"baseline"`
	Summary       map[string]any     `json:"summary"`
	StartedAt     *time.Time         `json:"startedAt"`
	FinishedAt    *time.Time         `json:"finishedAt"`
}

// evalRunDTO is the wire shape of one evaluation run. The id is a decimal string
// (D29) so a JavaScript client never loses precision; metric values stay JSON
// numbers so the dashboard can compare them directly.
type evalRunDTO struct {
	ID            string          `json:"id"`
	Mode          string          `json:"mode"`
	CorpusVersion string          `json:"corpusVersion"`
	Model         string          `json:"model"`
	Status        string          `json:"status"`
	Summary       map[string]any  `json:"summary"`
	Metrics       []evalMetricDTO `json:"metrics"`
	StartedAt     time.Time       `json:"startedAt"`
	FinishedAt    *time.Time      `json:"finishedAt"`
}

// evalMetricDTO is one KPI of a run.
type evalMetricDTO struct {
	Name      string         `json:"name"`
	Value     float64        `json:"value"`
	Threshold float64        `json:"threshold"`
	Passed    bool           `json:"passed"`
	Detail    map[string]any `json:"detail"`
}

// newEvalRunDTO projects the domain run onto the wire shape. A nil summary or
// metric slice becomes an empty document/array, never JSON null.
func newEvalRunDTO(run domain.Run) evalRunDTO {
	metrics := make([]evalMetricDTO, 0, len(run.Metrics))
	for _, metric := range run.Metrics {
		detail := metric.Detail
		if detail == nil {
			detail = map[string]any{}
		}
		metrics = append(metrics, evalMetricDTO{
			Name:      metric.Name,
			Value:     metric.Value,
			Threshold: metric.Threshold,
			Passed:    metric.Passed,
			Detail:    detail,
		})
	}
	summary := run.Summary
	if summary == nil {
		summary = map[string]any{}
	}
	return evalRunDTO{
		ID:            strconv.FormatInt(run.ID, 10),
		Mode:          string(run.Mode),
		CorpusVersion: run.CorpusVersion,
		Model:         run.Model,
		Status:        string(run.Status),
		Summary:       summary,
		Metrics:       metrics,
		StartedAt:     run.StartedAt.UTC(),
		FinishedAt:    run.FinishedAt,
	}
}

// decodeEvalJSON decodes one strict JSON object with the shared body limit and
// rejects trailing content, so a typo or a concatenated body is a 400 instead of
// a silently ignored field.
func decodeEvalJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if r.Body == nil {
		return errors.New("missing body")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEvalRequestBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain one JSON object")
	}
	return nil
}

// parseEvalLimit validates the optional list limit.
func parseEvalLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return qualityapp.DefaultListLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > qualityapp.MaxListLimit {
		return 0, errors.New("invalid limit")
	}
	return limit, nil
}

// parseEvalPathID parses one positive decimal id.
func parseEvalPathID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

// evalTime converts the optional wire timestamp.
func evalTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// writeServiceError maps the application verdicts onto the shared P0 envelope.
// The service message never carries database details, and an unknown failure is
// rendered as the generic internal error.
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *qualityapp.ValidationError
	if errors.As(err, &invalid) {
		writeInvalidArgument(w, r, invalid.Field, invalid.Message)
		return
	}
	if errors.Is(err, qualityapp.ErrRunNotFound) {
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "evaluation run not found")
		return
	}
	platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
}

// writeInvalidArgument renders the shared 400 envelope with the rejected field.
func writeInvalidArgument(w http.ResponseWriter, r *http.Request, field, message string) {
	platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, message,
		platform.WithDetails(map[string]any{"field": field}))
}

// dataEnvelope is the success shape of the quality endpoints: {"data": ...}.
type dataEnvelope struct {
	Data any `json:"data"`
}

// writeData writes {"data": data} with the shared JSON content type.
func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dataEnvelope{Data: data})
}
