// Package llm is the domain model for LLM gateway configurations and their
// model catalog. The package is dependency-free apart from the shared platform
// validation helpers.
package llm

import (
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Provider/protocol/status values frozen by the P0 schema (llm_0001_llm.sql).
const (
	ProviderOpenAI = "openai"

	// ProtocolOpenAI and ProtocolOpenAICompatible are the legacy P0 storage
	// values. They stay valid on read and write (llm_0003_protocol021.sql keeps
	// them in the CHECK) and are normalized to
	// ProtocolOpenAIChatCompletions by NormalizeProtocol, so no data migration
	// is needed for existing rows (spec §7.1).
	ProtocolOpenAI           = "openai"
	ProtocolOpenAICompatible = "openai_compatible"

	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Protocol is the upstream wire protocol spoken by one LLM configuration.
// The current values behave differently: chat completions posts to
// /chat/completions, Responses posts to /responses (spec §7.1).
type Protocol string

const (
	ProtocolOpenAIChatCompletions Protocol = "openai_chat_completions"
	ProtocolOpenAIResponses       Protocol = "openai_responses"
)

// Thinking modes and reasoning efforts frozen by the 0.2.1 schema
// (llm_0002_model021.sql), spec §7.2. The two fields are independent: the
// mode says whether the provider should think, the effort says how hard.
const (
	ThinkingTypeAuto     = "auto"
	ThinkingTypeEnabled  = "enabled"
	ThinkingTypeDisabled = "disabled"

	ReasoningEffortNone   = "none"
	ReasoningEffortLow    = "low"
	ReasoningEffortMedium = "medium"
	ReasoningEffortHigh   = "high"
	ReasoningEffortXHigh  = "xhigh"
	ReasoningEffortMax    = "max"

	// DefaultThinkingType and DefaultReasoningEffort match the migration
	// defaults; an empty stored value is read as these values for rows written
	// before the 0.2.1 columns existed.
	DefaultThinkingType    = ThinkingTypeAuto
	DefaultReasoningEffort = ReasoningEffortNone
)

// P0 defaults and validation limits. They mirror the migration check
// constraints so the application fails with a typed 400 before PostgreSQL
// rejects the row.
const (
	DefaultProvider = ProviderOpenAI
	// DefaultProtocol stays the legacy P0 storage value for configs created
	// without an explicit protocol. llm_0003_protocol021.sql now accepts the
	// canonical values too, while the adapter factory still reads this value as
	// ProtocolOpenAIChatCompletions.
	DefaultProtocol       = ProtocolOpenAI
	DefaultStatus         = StatusActive
	DefaultTimeoutSeconds = 60
	DefaultMaxRetries     = 2

	MinTimeoutSeconds = 1
	MaxTimeoutSeconds = 600
	MinMaxRetries     = 0
	MaxMaxRetries     = 10

	MaxNameLength      = 100
	MaxProviderLength  = 100
	MaxBaseURLLength   = 2048
	MaxAPIKeyLength    = 8192
	MaxHeaders         = 50
	MaxHeaderNameLen   = 256
	MaxHeaderValueLen  = 8192
	MaxModelNameLength = 200
	MaxContextWindow   = 10_000_000
	MaxMaxOutput       = 10_000_000
)

// Config is one LLM gateway configuration. APIKey is stored in plain text by
// design (P0-D3) and must never be serialized to an API response: the HTTP
// DTOs expose api_key_set instead.
type Config struct {
	ID             int64
	Name           string
	Provider       string
	Protocol       string
	BaseURL        string
	APIKey         string
	Headers        map[string]string
	TimeoutSeconds int
	MaxRetries     int
	IsDefault      bool
	Status         string
	// ThinkingType and ReasoningEffort are the 0.2.1 thinking settings
	// (spec §7.2). DefaultModelID is the explicit default model; specs forbid
	// falling back to models[0]. Zero means "not configured".
	ThinkingType    string
	ReasoningEffort string
	DefaultModelID  int64
	CreatedBy       *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// EffectiveThinkingType returns the stored thinking mode or the schema default
// when a row predates the 0.2.1 column.
func (c Config) EffectiveThinkingType() string {
	if c.ThinkingType == "" {
		return DefaultThinkingType
	}
	return c.ThinkingType
}

// EffectiveReasoningEffort returns the stored reasoning effort or the schema
// default when a row predates the 0.2.1 column.
func (c Config) EffectiveReasoningEffort() string {
	if c.ReasoningEffort == "" {
		return DefaultReasoningEffort
	}
	return c.ReasoningEffort
}

// ValidThinkingType reports whether value is part of the frozen enum.
func ValidThinkingType(value string) bool {
	switch value {
	case ThinkingTypeAuto, ThinkingTypeEnabled, ThinkingTypeDisabled:
		return true
	default:
		return false
	}
}

// ValidReasoningEffort reports whether value is part of the frozen enum.
func ValidReasoningEffort(value string) bool {
	switch value {
	case ReasoningEffortNone, ReasoningEffortLow, ReasoningEffortMedium,
		ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax:
		return true
	default:
		return false
	}
}

// APIKeySet reports whether a secret is configured.
func (c Config) APIKeySet() bool { return strings.TrimSpace(c.APIKey) != "" }

// Validate checks the invariants shared by create and update. The caller is
// expected to have applied defaults and trimmed the user-provided fields.
func (c Config) Validate() error {
	if err := validation.String("name", c.Name, 1, MaxNameLength); err != nil {
		return err
	}
	if err := validation.String("provider", c.Provider, 1, MaxProviderLength); err != nil {
		return err
	}
	if !ValidProtocol(c.Protocol) {
		return platform.Invalid("protocol", "must be one of openai_chat_completions, openai_responses (legacy openai/openai_compatible accepted)")
	}
	if err := validation.URL("base_url", c.BaseURL); err != nil {
		return err
	}
	if len(c.BaseURL) > MaxBaseURLLength {
		return platform.Invalid("base_url", "must be at most "+strconv.Itoa(MaxBaseURLLength)+" characters")
	}
	if len(c.APIKey) > MaxAPIKeyLength {
		return platform.Invalid("api_key", "must be at most "+strconv.Itoa(MaxAPIKeyLength)+" characters")
	}
	if err := ValidateHeaders(c.Headers); err != nil {
		return err
	}
	if c.TimeoutSeconds < MinTimeoutSeconds || c.TimeoutSeconds > MaxTimeoutSeconds {
		return platform.Invalid("timeout_seconds", "must be between "+strconv.Itoa(MinTimeoutSeconds)+" and "+strconv.Itoa(MaxTimeoutSeconds))
	}
	if c.MaxRetries < MinMaxRetries || c.MaxRetries > MaxMaxRetries {
		return platform.Invalid("max_retries", "must be between "+strconv.Itoa(MinMaxRetries)+" and "+strconv.Itoa(MaxMaxRetries))
	}
	if !ValidStatus(c.Status) {
		return platform.Invalid("status", "must be one of active, disabled")
	}
	if !ValidThinkingType(c.ThinkingType) {
		return platform.Invalid("thinking_type", "must be one of auto, enabled, disabled")
	}
	if !ValidReasoningEffort(c.ReasoningEffort) {
		return platform.Invalid("reasoning_effort", "must be one of none, low, medium, high, xhigh, max")
	}
	if c.DefaultModelID < 0 {
		return platform.Invalid("default_model_id", "must not be negative")
	}
	return nil
}

// ValidateHeaders rejects blank header names and reserved authentication
// headers, which would otherwise override the configured API key.
func ValidateHeaders(headers map[string]string) error {
	if len(headers) > MaxHeaders {
		return platform.Invalid("headers", "must contain at most "+strconv.Itoa(MaxHeaders)+" items")
	}
	for name, value := range headers {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return platform.Invalid("headers", "header names must not be empty")
		}
		if len(trimmed) > MaxHeaderNameLen {
			return platform.Invalid("headers", "header name is too long")
		}
		if len(value) > MaxHeaderValueLen {
			return platform.Invalid("headers", "header value is too long")
		}
		if strings.EqualFold(trimmed, "authorization") {
			return platform.Invalid("headers", "Authorization is managed by api_key and must not be set")
		}
	}
	return nil
}

// NormalizeHeaders returns a copy with trimmed names and a non-nil map.
func NormalizeHeaders(headers map[string]string) map[string]string {
	normalized := make(map[string]string, len(headers))
	for name, value := range headers {
		normalized[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return normalized
}

// ValidProtocol reports whether protocol is supported. Legacy P0 values stay
// accepted while stored rows still use them; the adapter factory normalizes
// both legacy values to chat completions.
func ValidProtocol(protocol string) bool {
	_, ok := NormalizeProtocol(protocol)
	return ok
}

// NormalizeProtocol maps a configured protocol value to the canonical enum.
// The legacy P0 values openai and openai_compatible read as
// openai_chat_completions, so existing rows keep working without a data
// migration (spec §7.1). A blank or unknown value reports ok=false.
func NormalizeProtocol(protocol string) (Protocol, bool) {
	switch Protocol(strings.TrimSpace(protocol)) {
	case ProtocolOpenAIChatCompletions, ProtocolOpenAI, ProtocolOpenAICompatible:
		return ProtocolOpenAIChatCompletions, true
	case ProtocolOpenAIResponses:
		return ProtocolOpenAIResponses, true
	default:
		return "", false
	}
}

// ValidStatus reports whether status is one of the frozen P0 values.
func ValidStatus(status string) bool {
	return status == StatusActive || status == StatusDisabled
}
