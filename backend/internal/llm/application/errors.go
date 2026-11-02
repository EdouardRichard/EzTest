package llmapp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// 0.2.1 stable LLM error codes (spec §5.6/§7.4/§8.4). They extend the frozen P1
// set (ai_unavailable/context_overflow) so a capability or protocol rejection is
// distinguishable from a transient outage and is never retried automatically.
const (
	// CodeLLMProtocolError marks a non-retryable protocol/parameter rejection:
	// the request shape is wrong for this model or gateway.
	CodeLLMProtocolError = "llm_protocol_error"
	// CodeLLMAuthFailed marks a non-retryable upstream authentication or
	// authorization failure (HTTP 401/403); an operator must fix the config.
	CodeLLMAuthFailed = "llm_auth_failed"
	// CodeInvalidModel marks a non-retryable rejection of the selected model
	// (for example a model the gateway does not expose).
	CodeInvalidModel = "invalid_model"
)

// LLM error actions. User-visible errors must tell the user what to do next
// (spec §5.6/§7.4); these strings are appended to the message and exposed as
// details.action so the UI and the agent failure classifier can surface them.
const (
	LLMActionCheckModel       = "检查模型能力/协议"
	LLMActionCheckCredentials = "检查模型配置与凭据"
	LLMActionRetry            = "自动重试"
)

// ErrProtocol and ErrTransient are the application-level classification
// sentinels (R25). Infrastructure adapters tag their typed errors with them so
// the application can classify an upstream failure without importing
// infrastructure. Errors classify as exactly one of the two.
var (
	ErrProtocol  = errors.New("llm protocol error")
	ErrTransient = errors.New("llm transient error")
)

// LLMErrorKind is the contract §5.6 failure class of one LLM error.
type LLMErrorKind string

// LLM error kinds. The values are stable identifiers surfaced in
// platform.Error details.kind.
const (
	LLMKindCapability LLMErrorKind = "capability"
	LLMKindTransient  LLMErrorKind = "transient"
	LLMKindBudget     LLMErrorKind = "budget"
	LLMKindAuth       LLMErrorKind = "auth"
	LLMKindProtocol   LLMErrorKind = "protocol"
)

// LLMError is the application-level classified LLM failure: what kind of
// failure it is, the upstream HTTP status (0 when the failure never reached the
// wire), the redacted provider message and the actionable next step. An
// unclassified infrastructure error is never represented by this type.
type LLMError struct {
	Kind            LLMErrorKind
	StatusCode      int
	ProviderMessage string
	Action          string
	Err             error
}

// Error renders the classified failure without adding secrets.
func (e *LLMError) Error() string {
	if e == nil {
		return "llm request failed"
	}
	message := strings.TrimSpace(e.ProviderMessage)
	if message == "" && e.Err != nil {
		message = e.Err.Error()
	}
	if message == "" {
		return "llm " + string(e.Kind) + " error"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("llm %s error (HTTP %d): %s", e.Kind, e.StatusCode, message)
	}
	return fmt.Sprintf("llm %s error: %s", e.Kind, message)
}

// Unwrap exposes the provider/transport cause to errors.Is/errors.As.
func (e *LLMError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is maps the classified failure onto the application sentinels so callers can
// classify with errors.Is(err, ErrProtocol/ErrTransient).
func (e *LLMError) Is(target error) bool {
	if e == nil || target == nil {
		return false
	}
	switch e.Kind {
	case LLMKindTransient:
		return target == ErrTransient
	case LLMKindCapability, LLMKindAuth, LLMKindProtocol:
		return target == ErrProtocol
	default:
		return false
	}
}

// ClassifiedError is implemented by infrastructure adapters (ProtocolError /
// TransientError) so the application can read the classification through
// errors.As without importing the infrastructure package (R25).
type ClassifiedError interface {
	error
	LLMErrorKind() LLMErrorKind
	HTTPStatusCode() int
	UpstreamMessage() string
}

// IsProtocol reports whether err is a non-retryable protocol/capability/config
// failure. It accepts raw classified errors and the platform errors produced by
// mapChatError, so the agent runtime can classify a run failure without
// importing infrastructure.
func IsProtocol(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrProtocol) || errors.Is(err, ErrNotImplemented) {
		return true
	}
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		switch llmErr.Kind {
		case LLMKindTransient, LLMKindBudget:
			return false
		default:
			return true
		}
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case CodeLLMProtocolError, CodeInvalidModel, CodeLLMCapabilityMismatch,
			CodeLLMAuthFailed, CodeLLMConfigInvalid:
			return true
		}
	}
	return false
}

// IsTransient reports whether err is a retryable transient LLM failure
// (429/5xx/empty response/timeout). Generic ai_unavailable errors that were not
// classified as transient report false.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTransient) {
		return true
	}
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.Kind == LLMKindTransient
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		kind, _ := apiErr.Details["kind"].(string)
		return kind == string(LLMKindTransient)
	}
	return false
}

// classifyLLMError normalizes one upstream error into the application taxonomy.
// Unknown errors report ok=false so the caller keeps the legacy generic
// ai_unavailable mapping.
func classifyLLMError(err error) (*LLMError, bool) {
	if err == nil {
		return nil, false
	}
	var classified ClassifiedError
	if errors.As(err, &classified) {
		return &LLMError{
			Kind:            classified.LLMErrorKind(),
			StatusCode:      classified.HTTPStatusCode(),
			ProviderMessage: strings.TrimSpace(classified.UpstreamMessage()),
			Err:             err,
		}, true
	}
	// ErrNotImplemented is a protocol/adapter capability gap: fail closed, never
	// retry and never report it as a transient outage (spec §7.1).
	if errors.Is(err, ErrNotImplemented) {
		return &LLMError{
			Kind:            LLMKindProtocol,
			ProviderMessage: strings.TrimSpace(err.Error()),
			Action:          LLMActionCheckModel,
			Err:             err,
		}, true
	}
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr, true
	}
	if errors.Is(err, ErrProtocol) {
		return &LLMError{Kind: LLMKindProtocol, ProviderMessage: strings.TrimSpace(err.Error()), Err: err}, true
	}
	if errors.Is(err, ErrTransient) {
		return &LLMError{Kind: LLMKindTransient, ProviderMessage: strings.TrimSpace(err.Error()), Err: err}, true
	}
	return nil, false
}

// classifiedPlatformError converts the taxonomy into the P0 HTTP error envelope.
// Every non-transient 4xx-of-the-upstream becomes a non-retryable 400 whose
// message names the next action; transient failures stay a retryable 503
// ai_unavailable (spec §5.6/§7.4).
func classifiedPlatformError(classified *LLMError, secret string) error {
	if classified == nil {
		return aiUnavailable("llm request failed")
	}
	message := redact(strings.TrimSpace(classified.ProviderMessage), secret)
	kind := normalizeLLMKind(classified)
	action := classified.Action
	details := map[string]any{
		"kind":        string(kind),
		"status_code": classified.StatusCode,
	}
	switch kind {
	case LLMKindTransient:
		if action == "" {
			action = LLMActionRetry
		}
		details["action"] = action
		if message == "" {
			message = "llm provider is temporarily unavailable"
		}
		return &platform.Error{
			Status:    http.StatusServiceUnavailable,
			Code:      CodeAIUnavailable,
			Message:   message + "；建议：" + action,
			Retryable: true,
			Details:   details,
		}
	case LLMKindAuth:
		if action == "" {
			action = LLMActionCheckCredentials
		}
		details["action"] = action
		if message == "" {
			message = "llm provider rejected the configured credentials"
		}
		return &platform.Error{
			Status:  http.StatusBadRequest,
			Code:    CodeLLMAuthFailed,
			Message: "LLM 认证/权限失败：" + message + "；建议：" + action,
			Details: details,
		}
	case LLMKindCapability:
		if action == "" {
			action = LLMActionCheckModel
		}
		details["action"] = action
		if message == "" {
			message = "llm model rejected the request capabilities"
		}
		return &platform.Error{
			Status:  http.StatusBadRequest,
			Code:    CodeLLMCapabilityMismatch,
			Message: "LLM 模型能力不匹配：" + message + "；建议：" + action,
			Details: details,
		}
	default:
		if action == "" {
			action = LLMActionCheckModel
		}
		details["action"] = action
		if message == "" {
			message = "llm provider rejected the request"
		}
		if isModelNotFound(classified.StatusCode, message) {
			details["model"] = modelNotFoundName(message)
			return &platform.Error{
				Status:  http.StatusBadRequest,
				Code:    CodeInvalidModel,
				Message: "LLM 模型不可用：" + message + "；建议：" + action,
				Details: details,
			}
		}
		return &platform.Error{
			Status:  http.StatusBadRequest,
			Code:    CodeLLMProtocolError,
			Message: message + "；建议：" + action,
			Details: details,
		}
	}
}

// normalizeLLMKind refines an adapter-level protocol rejection into the
// capability/auth/model classes using the upstream status and message. Adapters
// only know "retryable or not"; the application owns this taxonomy.
func normalizeLLMKind(classified *LLMError) LLMErrorKind {
	switch classified.Kind {
	case LLMKindTransient, LLMKindBudget, LLMKindCapability, LLMKindAuth:
		return classified.Kind
	}
	if classified.StatusCode == http.StatusUnauthorized || classified.StatusCode == http.StatusForbidden {
		return LLMKindAuth
	}
	if isCapabilityMessage(classified.ProviderMessage) {
		return LLMKindCapability
	}
	return LLMKindProtocol
}

// isCapabilityMessage detects an upstream parameter/capability rejection. The
// provider vocabulary is not standardized, so the well-known capability words
// are checked instead of matching a single provider string.
func isCapabilityMessage(message string) bool {
	lower := strings.ToLower(message)
	if lower == "" {
		return false
	}
	for _, hint := range []string{
		"capabilit", "not support", "unsupported", "does not support",
		"tool", "vision", "image", "reasoning",
	} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// isModelNotFound reports whether the upstream rejected the selected model.
func isModelNotFound(status int, message string) bool {
	if status == http.StatusNotFound {
		return true
	}
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "model") {
		return false
	}
	for _, hint := range []string{
		"not found", "does not exist", "no such model", "unknown model",
		"invalid model", "model not available", "not available",
	} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// modelNotFoundName best-effort extracts the rejected model name for the error
// details. It returns the raw message when no quoted name is present.
func modelNotFoundName(message string) string {
	for _, quote := range []string{"\"", "'", "`"} {
		parts := strings.Split(message, quote)
		if len(parts) >= 3 && strings.TrimSpace(parts[1]) != "" {
			return strings.TrimSpace(parts[1])
		}
	}
	return strings.TrimSpace(message)
}
