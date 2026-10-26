// Package httpapi is the LLM gateway's HTTP interface: the admin-only
// configuration CRUD, model-catalog refresh and connectivity test endpoints. It
// depends only on the application ports; the server composition root mounts the
// routes behind the global bearer auth and the per-route admin guard.
package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	// maxLLMBodyBytes caps the configuration and test request bodies.
	maxLLMBodyBytes int64 = 16 << 10
	// defaultTestPrompt is the design's default prompt for the test endpoint.
	defaultTestPrompt = "请用一句话描述这张图片"
	// pixelFixturePath is the built-in 1x1 PNG used by image tests.
	pixelFixturePath = "testdata/pixel.png"
)

// ConfigService is the LLM configuration use case consumed by the admin routes.
// *application.ConfigService satisfies it.
type ConfigService interface {
	Create(ctx context.Context, params llm.CreateParams) (llm.Config, error)
	Update(ctx context.Context, id int64, params llm.UpdateParams) (llm.Config, error)
	Delete(ctx context.Context, id int64) error
	Get(ctx context.Context, id int64) (llm.Config, error)
	List(ctx context.Context) ([]llm.Config, error)
	GetDefault(ctx context.Context) (llm.Config, error)
}

// Handler serves the admin LLM endpoints. Every route is mounted behind the
// global bearer authentication and RequireAdmin; the handler never returns the
// API key or header values.
type Handler struct {
	svc      ConfigService
	factory  llmapp.ClientFactory
	recorder auditapp.Recorder
	// imagePath optionally overrides the built-in test image location.
	imagePath string
}

// NewHandler returns the admin LLM handlers. A nil recorder degrades to a no-op
// so a partially wired process cannot panic on the first test.
func NewHandler(svc ConfigService, factory llmapp.ClientFactory, recorder auditapp.Recorder) *Handler {
	if recorder == nil {
		recorder = auditapp.NopRecorder{}
	}
	return &Handler{svc: svc, factory: factory, recorder: recorder}
}

// RegisterRoutes mounts the admin LLM endpoints on mux. guard wraps every route
// with the caller's admin middleware; the server passes RequireAdmin and never
// installs a second authentication middleware here.
func RegisterRoutes(mux *http.ServeMux, h *Handler, guard func(http.Handler) http.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}
	mux.Handle("GET /api/admin/llm/configs", guard(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/admin/llm/configs", guard(http.HandlerFunc(h.Create)))
	mux.Handle("PATCH /api/admin/llm/configs/{id}", guard(http.HandlerFunc(h.Update)))
	mux.Handle("DELETE /api/admin/llm/configs/{id}", guard(http.HandlerFunc(h.Delete)))
	mux.Handle("POST /api/admin/llm/configs/{id}/catalog/refresh", guard(http.HandlerFunc(h.RefreshCatalog)))
	mux.Handle("POST /api/admin/llm/configs/{id}/test", guard(http.HandlerFunc(h.Test)))
}

// configPayload is the public configuration representation. It deliberately has
// no API key, no headers map and no ciphertext: only whether a key exists, the
// key version and the header names are exposed.
type configPayload struct {
	ID                int64           `json:"id"`
	Name              string          `json:"name"`
	Protocol          string          `json:"protocol"`
	BaseURL           string          `json:"base_url"`
	Model             string          `json:"model"`
	HasAPIKey         bool            `json:"has_api_key"`
	KeyVersion        int             `json:"key_version"`
	HeaderNames       []string        `json:"header_names"`
	RetryCount        int             `json:"retry_count"`
	RetryIntervalMS   int             `json:"retry_interval_ms"`
	TimeoutMS         int             `json:"timeout_ms"`
	StreamEnabled     bool            `json:"stream_enabled"`
	MultimodalEnabled bool            `json:"multimodal_enabled"`
	IsDefault         bool            `json:"is_default"`
	ModelCatalog      []llm.ModelInfo `json:"model_catalog"`
	CatalogFetchedAt  *time.Time      `json:"catalog_fetched_at"`
}

// newConfigPayload maps a domain configuration to the masked API shape.
func newConfigPayload(cfg llm.Config) configPayload {
	headerNames := make([]string, 0, len(cfg.Headers))
	for name := range cfg.Headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	catalog := cfg.ModelCatalog
	if catalog == nil {
		catalog = []llm.ModelInfo{}
	}
	return configPayload{
		ID:                cfg.ID,
		Name:              cfg.Name,
		Protocol:          string(cfg.Protocol),
		BaseURL:           cfg.BaseURL,
		Model:             cfg.Model,
		HasAPIKey:         cfg.APIKey != "",
		KeyVersion:        cfg.KeyVersion,
		HeaderNames:       headerNames,
		RetryCount:        cfg.RetryCount,
		RetryIntervalMS:   cfg.RetryIntervalMS,
		TimeoutMS:         cfg.TimeoutMS,
		StreamEnabled:     cfg.StreamEnabled,
		MultimodalEnabled: cfg.MultimodalEnabled,
		IsDefault:         cfg.IsDefault,
		ModelCatalog:      catalog,
		CatalogFetchedAt:  cfg.CatalogFetchedAt,
	}
}

// createConfigRequest is the POST /api/admin/llm/configs body. Pointer fields
// distinguish an omitted field (default policy) from an explicit zero.
type createConfigRequest struct {
	Name              string            `json:"name"`
	Protocol          llm.Protocol      `json:"protocol"`
	BaseURL           string            `json:"base_url"`
	Model             string            `json:"model"`
	APIKey            string            `json:"api_key"`
	Headers           map[string]string `json:"headers"`
	RetryCount        *int              `json:"retry_count"`
	RetryIntervalMS   *int              `json:"retry_interval_ms"`
	TimeoutMS         *int              `json:"timeout_ms"`
	StreamEnabled     *bool             `json:"stream_enabled"`
	MultimodalEnabled *bool             `json:"multimodal_enabled"`
	IsDefault         *bool             `json:"is_default"`
}

// updateConfigRequest is the PATCH body. Every field is optional; api_key and
// headers omitted keep their stored value, provided values replace them.
type updateConfigRequest struct {
	Name              *string            `json:"name"`
	Protocol          *llm.Protocol      `json:"protocol"`
	BaseURL           *string            `json:"base_url"`
	Model             *string            `json:"model"`
	APIKey            *string            `json:"api_key"`
	Headers           *map[string]string `json:"headers"`
	RetryCount        *int               `json:"retry_count"`
	RetryIntervalMS   *int               `json:"retry_interval_ms"`
	TimeoutMS         *int               `json:"timeout_ms"`
	StreamEnabled     *bool              `json:"stream_enabled"`
	MultimodalEnabled *bool              `json:"multimodal_enabled"`
	IsDefault         *bool              `json:"is_default"`
}

// testConfigRequest is the POST .../test body.
type testConfigRequest struct {
	Stream bool   `json:"stream"`
	Prompt string `json:"prompt"`
	Image  bool   `json:"image"`
}

// List returns every configuration with secrets masked.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	configs, err := h.svc.List(r.Context())
	if err != nil {
		writeConfigError(w, r, err)
		return
	}
	items := make([]configPayload, len(configs))
	for i, cfg := range configs {
		items[i] = newConfigPayload(cfg)
	}
	writeJSON(w, http.StatusOK, struct {
		Data []configPayload `json:"data"`
	}{Data: items})
}

// Create stores a new configuration. Omitted retry/feature fields use the
// documented create defaults; an explicitly provided zero is preserved.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req createConfigRequest
	if err := platform.DecodeJSON(r, &req, maxLLMBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	params := llmapp.DefaultCreateParams()
	params.Name = req.Name
	params.Protocol = req.Protocol
	params.BaseURL = req.BaseURL
	params.Model = req.Model
	params.APIKey = req.APIKey
	params.Headers = req.Headers
	if req.RetryCount != nil {
		params.RetryCount = *req.RetryCount
	}
	if req.RetryIntervalMS != nil {
		params.RetryIntervalMS = *req.RetryIntervalMS
	}
	if req.TimeoutMS != nil {
		params.TimeoutMS = *req.TimeoutMS
	}
	if req.StreamEnabled != nil {
		params.StreamEnabled = *req.StreamEnabled
	}
	if req.MultimodalEnabled != nil {
		params.MultimodalEnabled = *req.MultimodalEnabled
	}
	if req.IsDefault != nil {
		params.IsDefault = *req.IsDefault
	}
	cfg, err := h.svc.Create(r.Context(), params)
	if err != nil {
		writeConfigError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Data configPayload `json:"data"`
	}{Data: newConfigPayload(cfg)})
}

// Update applies a partial configuration update.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := configIDFromPath(w, r)
	if !ok {
		return
	}
	var req updateConfigRequest
	if err := platform.DecodeJSON(r, &req, maxLLMBodyBytes); err != nil {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
		return
	}
	params := llm.UpdateParams{
		Name:              req.Name,
		Protocol:          req.Protocol,
		BaseURL:           req.BaseURL,
		Model:             req.Model,
		APIKey:            req.APIKey,
		Headers:           req.Headers,
		RetryCount:        req.RetryCount,
		RetryIntervalMS:   req.RetryIntervalMS,
		TimeoutMS:         req.TimeoutMS,
		StreamEnabled:     req.StreamEnabled,
		MultimodalEnabled: req.MultimodalEnabled,
		IsDefault:         req.IsDefault,
	}
	cfg, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		writeConfigError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data configPayload `json:"data"`
	}{Data: newConfigPayload(cfg)})
}

// Delete removes a configuration.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := configIDFromPath(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeConfigError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RefreshCatalog fetches the upstream model catalog, stores it and returns the
// updated configuration. A failed refresh leaves the previous cache intact
// because Update is only called on success.
func (h *Handler) RefreshCatalog(w http.ResponseWriter, r *http.Request) {
	id, ok := configIDFromPath(w, r)
	if !ok {
		return
	}
	cfg, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeConfigError(w, r, err)
		return
	}
	client, err := h.clientFor(cfg)
	if err != nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "llm client is not available")
		return
	}
	models, err := client.ListModels(r.Context())
	if err != nil {
		writeLLMError(w, r, err)
		return
	}
	if models == nil {
		models = []llm.ModelInfo{}
	}
	updated, err := h.svc.Update(r.Context(), id, llm.UpdateParams{ModelCatalog: &models})
	if err != nil {
		writeConfigError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data configPayload `json:"data"`
	}{Data: newConfigPayload(updated)})
}

// Test performs a connectivity test against the saved configuration. image=true
// attaches the built-in 1x1 PNG fixture and stream=true returns normalized
// llm.delta/llm.done/llm.error SSE frames. Success and failure are audited.
func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	id, ok := configIDFromPath(w, r)
	if !ok {
		return
	}
	cfg, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeConfigError(w, r, err)
		return
	}

	var req testConfigRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := platform.DecodeJSON(r, &req, maxLLMBodyBytes); err != nil {
			platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid JSON body")
			return
		}
	}
	if req.Stream && !cfg.StreamEnabled {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
			"streaming is disabled for this LLM configuration")
		return
	}
	if req.Image && !cfg.MultimodalEnabled {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
			"image input is disabled for this LLM configuration")
		return
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		writeLLMError(w, r, llmapp.NotConfiguredError())
		return
	}

	parts := []llmapp.Part{{Type: "text", Text: strings.TrimSpace(req.Prompt)}}
	if parts[0].Text == "" {
		parts[0].Text = defaultTestPrompt
	}
	if req.Image {
		png, err := h.pixelImage()
		if err != nil {
			platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "test image is not available")
			return
		}
		parts = append(parts, llmapp.Part{
			Type:     "image_url",
			ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		})
	}

	client, err := h.clientFor(cfg)
	if err != nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "llm client is not available")
		return
	}
	chatRequest := llmapp.ChatRequest{
		Model:    cfg.Model,
		Messages: []llmapp.Message{{Role: "user", Content: parts}},
		Stream:   req.Stream,
	}
	if req.Stream {
		h.streamTest(w, r, cfg, req, client, chatRequest)
		return
	}
	h.syncTest(w, r, cfg, req, client, chatRequest)
}

// syncTestPayload is the non-streaming test result.
type syncTestPayload struct {
	Text      string       `json:"text"`
	Usage     llmapp.Usage `json:"usage"`
	LatencyMS int64        `json:"latency_ms"`
}

// deltaPayload is one llm.delta SSE frame.
type deltaPayload struct {
	Delta string `json:"delta"`
}

// donePayload is the terminal llm.done SSE frame.
type donePayload struct {
	Usage     llmapp.Usage `json:"usage"`
	LatencyMS int64        `json:"latency_ms"`
}

// syncTest runs the non-streaming test and returns text, usage and latency.
func (h *Handler) syncTest(w http.ResponseWriter, r *http.Request, cfg llm.Config, req testConfigRequest, client llmapp.Client, chatRequest llmapp.ChatRequest) {
	start := time.Now()
	res, err := client.Chat(r.Context(), chatRequest)
	latency := time.Since(start)
	if err != nil {
		h.recordTestAudit(r.Context(), cfg, req, latency, false, err)
		writeLLMError(w, r, err)
		return
	}
	h.recordTestAudit(r.Context(), cfg, req, latency, true, nil)
	writeJSON(w, http.StatusOK, struct {
		Data syncTestPayload `json:"data"`
	}{Data: syncTestPayload{Text: res.Text, Usage: res.Usage, LatencyMS: latency.Milliseconds()}})
}

// streamTest normalizes the upstream stream into llm.delta, llm.done and
// llm.error SSE frames. Errors after the stream started cannot change the HTTP
// status anymore, so they are reported as an llm.error frame.
func (h *Handler) streamTest(w http.ResponseWriter, r *http.Request, cfg llm.Config, req testConfigRequest, client llmapp.Client, chatRequest llmapp.ChatRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "streaming is not supported")
		return
	}
	start := time.Now()
	events, err := client.ChatStream(r.Context(), chatRequest)
	if err != nil {
		h.recordTestAudit(r.Context(), cfg, req, time.Since(start), false, err)
		writeLLMError(w, r, err)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-events:
			if !open {
				err := llmapp.NewLLMError(platform.CodeLLMInvalidResponse, "llm stream ended unexpectedly", false, 0, nil)
				h.recordTestAudit(r.Context(), cfg, req, time.Since(start), false, err)
				_ = writeLLMFrame(w, flusher, "llm.error", llmErrorPayload{Code: err.Code, Message: err.Message, Retryable: err.Retryable})
				return
			}
			switch {
			case event.Err != nil:
				h.recordTestAudit(r.Context(), cfg, req, time.Since(start), false, event.Err)
				code, message, retryable := llmErrorFields(event.Err)
				_ = writeLLMFrame(w, flusher, "llm.error", llmErrorPayload{Code: code, Message: message, Retryable: retryable})
				return
			case event.Done:
				h.recordTestAudit(r.Context(), cfg, req, time.Since(start), true, nil)
				_ = writeLLMFrame(w, flusher, "llm.done", donePayload{Usage: event.Usage, LatencyMS: time.Since(start).Milliseconds()})
				return
			case event.Delta != "":
				if err := writeLLMFrame(w, flusher, "llm.delta", deltaPayload{Delta: event.Delta}); err != nil {
					return
				}
			}
		}
	}
}

// recordTestAudit writes llm.test_succeeded or llm.test_failed with metadata that
// never contains the key, header values, prompt or image bytes.
func (h *Handler) recordTestAudit(ctx context.Context, cfg llm.Config, req testConfigRequest, latency time.Duration, success bool, failure error) {
	action := auditapp.ActionLLMTestSucceeded
	metadata := map[string]any{
		"config_id":   cfg.ID,
		"config_name": cfg.Name,
		"model":       cfg.Model,
		"protocol":    string(cfg.Protocol),
		"stream":      req.Stream,
		"image":       req.Image,
		"latency_ms":  latency.Milliseconds(),
	}
	if !success {
		action = auditapp.ActionLLMTestFailed
		metadata["error_code"] = platform.CodeInternal
		if llmErr, ok := llmapp.AsLLMError(failure); ok {
			metadata["error_code"] = llmErr.Code
		}
	}
	entry := auditdomain.Entry{
		Action:     action,
		TargetType: "llm_config",
		TargetID:   strconv.FormatInt(cfg.ID, 10),
		Metadata:   metadata,
	}
	meta := auditapp.MetaFromContext(ctx)
	entry.RequestID = meta.RequestID
	entry.IP = meta.IP
	entry.UserAgent = meta.UserAgent
	if actorID, ok := auditapp.ActorIDFromContext(ctx); ok {
		entry.ActorUserID = &actorID
	}
	if err := h.recorder.Record(ctx, entry); err != nil {
		// Auditing is best-effort: the test result is still returned, but the
		// failure is logged without any entry content.
		slog.ErrorContext(ctx, "audit: record llm test entry", "action", entry.Action, "error", err)
	}
}

// llmErrorPayload is one normalized llm.error SSE frame.
type llmErrorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// writeLLMFrame writes one SSE frame and flushes it.
func writeLLMFrame(w io.Writer, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// clientFor builds the protocol client for a stored configuration.
func (h *Handler) clientFor(cfg llm.Config) (llmapp.Client, error) {
	if h.factory == nil {
		return nil, errors.New("llm: client factory is not configured")
	}
	client := h.factory(cfg)
	if client == nil {
		return nil, errors.New("llm: client factory returned nil")
	}
	return client, nil
}

// pixelImage reads the built-in 1x1 PNG fixture. The explicit path wins; the
// remaining candidates keep the fixture discoverable both from the backend
// working directory and from a test package's directory.
func (h *Handler) pixelImage() ([]byte, error) {
	candidates := make([]string, 0, 3)
	if h.imagePath != "" {
		candidates = append(candidates, h.imagePath)
	}
	candidates = append(candidates, pixelFixturePath)
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "testdata", "pixel.png"))
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("llm: pixel fixture %q is not readable", pixelFixturePath)
}

// configIDFromPath parses the positive integer {id} path value.
func configIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid llm config id")
		return 0, false
	}
	return id, true
}

// writeConfigError maps the LLM domain errors to the shared error envelope.
func writeConfigError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, llm.ErrConfigNotFound):
		platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "llm configuration not found")
	case errors.Is(err, llm.ErrNameTaken):
		platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict, "llm configuration name already taken")
	case errors.Is(err, llm.ErrDefaultConflict):
		platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict, "another llm configuration became the default")
	case errors.Is(err, llm.ErrInvalidName):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm configuration name is required")
	case errors.Is(err, llm.ErrInvalidModel):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm model is required")
	case errors.Is(err, llm.ErrInvalidProtocol):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm protocol must be chat_completions or responses")
	case errors.Is(err, llm.ErrInvalidBaseURL):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm base_url must be an http or https URL")
	case errors.Is(err, llm.ErrInvalidRetryCount):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm retry_count must be between 0 and 5")
	case errors.Is(err, llm.ErrInvalidRetryInterval):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm retry_interval_ms must be between 100 and 30000")
	case errors.Is(err, llm.ErrInvalidTimeout):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm timeout_ms must be between 1000 and 300000")
	case errors.Is(err, llm.ErrReservedHeader):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm headers must not override Authorization or Content-Type")
	case errors.Is(err, llm.ErrInvalidHeaderName):
		platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument, "llm header names must not be empty")
	case errors.Is(err, llm.ErrSecretsUnavailable):
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeStorageCorrupted, "stored llm credentials cannot be decrypted")
	default:
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
	}
}

// writeLLMError maps a typed upstream failure to the shared error envelope with
// the llm_* code, retryability and upstream_status detail.
func writeLLMError(w http.ResponseWriter, r *http.Request, err error) {
	code, message, retryable := llmErrorFields(err)
	details := map[string]any{}
	if llmErr, ok := llmapp.AsLLMError(err); ok && llmErr.UpstreamStatus != 0 {
		details["upstream_status"] = llmErr.UpstreamStatus
	}
	platform.WriteError(w, r, statusForLLMCode(code), code, message,
		platform.WithRetryable(retryable), platform.WithDetails(details))
}

// llmErrorFields extracts the public code, message and retryability.
func llmErrorFields(err error) (code, message string, retryable bool) {
	if llmErr, ok := llmapp.AsLLMError(err); ok {
		return llmErr.Code, llmErr.Message, llmErr.Retryable
	}
	return platform.CodeInternal, "internal server error", false
}

// statusForLLMCode maps an llm_* code to the HTTP status.
func statusForLLMCode(code string) int {
	switch code {
	case platform.CodeLLMRateLimited:
		return http.StatusTooManyRequests
	case platform.CodeLLMTimeout:
		return http.StatusGatewayTimeout
	case platform.CodeLLMBadRequest:
		return http.StatusBadRequest
	case platform.CodeLLMNotConfigured:
		return http.StatusServiceUnavailable
	case platform.CodeLLMAuthFailed, platform.CodeLLMUpstreamError, platform.CodeLLMInvalidResponse:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// writeJSON writes a success response in the shared `{"data": ...}` style.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
