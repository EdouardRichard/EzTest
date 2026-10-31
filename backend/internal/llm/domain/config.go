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

	ProtocolOpenAI           = "openai"
	ProtocolOpenAICompatible = "openai_compatible"

	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// P0 defaults and validation limits. They mirror the migration check
// constraints so the application fails with a typed 400 before PostgreSQL
// rejects the row.
const (
	DefaultProvider       = ProviderOpenAI
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
	CreatedBy      *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
		return platform.Invalid("protocol", "must be one of openai, openai_compatible")
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

// ValidProtocol reports whether protocol is one of the frozen P0 values.
func ValidProtocol(protocol string) bool {
	return protocol == ProtocolOpenAI || protocol == ProtocolOpenAICompatible
}

// ValidStatus reports whether status is one of the frozen P0 values.
func ValidStatus(status string) bool {
	return status == StatusActive || status == StatusDisabled
}
