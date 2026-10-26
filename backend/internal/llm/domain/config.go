// Package llm is the LLM gateway context's domain model: provider
// configurations, their retry/timeout policy, the cached model catalog and the
// validation rules shared by the repository port and the application layer.
package llm

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// Protocol selects the upstream API dialect used by an LLM configuration.
type Protocol string

const (
	// ProtocolChat is the OpenAI-compatible chat completions protocol.
	ProtocolChat Protocol = "chat_completions"
	// ProtocolResponses is the OpenAI-compatible responses protocol.
	ProtocolResponses Protocol = "responses"
)

// Valid reports whether p is a protocol the service accepts.
func (p Protocol) Valid() bool {
	return p == ProtocolChat || p == ProtocolResponses
}

// ModelInfo is one entry of the cached upstream model catalog.
type ModelInfo struct {
	ID              string   `json:"id"`
	ContextWindow   *int64   `json:"context_window,omitempty"`
	MaxOutputTokens *int64   `json:"max_output_tokens,omitempty"`
	InputModalities []string `json:"input_modalities,omitempty"`
}

// Config is one stored LLM gateway configuration. APIKey and Headers are
// secrets: the repository encrypts them at rest and decrypts them when a
// configuration is loaded for internal use. They are never returned by an API
// as plaintext.
type Config struct {
	ID       int64
	Name     string
	Protocol Protocol
	BaseURL  string
	Model    string
	APIKey   string
	// KeyVersion is the keyring version that encrypted APIKey and Headers.
	KeyVersion        int
	Headers           map[string]string
	RetryCount        int
	RetryIntervalMS   int
	TimeoutMS         int
	StreamEnabled     bool
	MultimodalEnabled bool
	IsDefault         bool
	ModelCatalog      []ModelInfo
	CatalogFetchedAt  *time.Time
}

// Retry and timeout bounds from the design. Values outside them are rejected
// before they reach the database CHECK constraints.
const (
	MinRetryCount      = 0
	MaxRetryCount      = 5
	MinRetryIntervalMS = 100
	MaxRetryIntervalMS = 30000
	MinTimeoutMS       = 1000
	MaxTimeoutMS       = 300000
)

// Defaults used when a create request omits a timing field. Zero is outside
// both valid ranges, so it unambiguously means "not provided"; these mirror the
// database column defaults. Retry count accepts an explicit zero, so callers
// that omit it must resolve DefaultRetryCount themselves (the HTTP layer uses
// application.DefaultCreateParams).
const (
	DefaultRetryCount      = 2
	DefaultRetryIntervalMS = 500
	DefaultTimeoutMS       = 60000
)

// Settings applied when a configuration is seeded from the environment.
const (
	SeedConfigName        = "default"
	SeedRetryCount        = 2
	SeedStreamEnabled     = true
	SeedMultimodalEnabled = true
)

// CreateParams describes a new configuration. The repository encrypts the API
// key and headers before inserting the row.
type CreateParams struct {
	Name              string
	Protocol          Protocol
	BaseURL           string
	Model             string
	APIKey            string
	Headers           map[string]string
	RetryCount        int
	RetryIntervalMS   int
	TimeoutMS         int
	StreamEnabled     bool
	MultimodalEnabled bool
	IsDefault         bool
}

// UpdateParams describes a partial update: nil fields keep their stored value.
// Headers and APIKey are re-encrypted with the active key version whenever any
// part of the configuration is saved.
type UpdateParams struct {
	Name              *string
	Protocol          *Protocol
	BaseURL           *string
	Model             *string
	APIKey            *string
	Headers           *map[string]string
	RetryCount        *int
	RetryIntervalMS   *int
	TimeoutMS         *int
	StreamEnabled     *bool
	MultimodalEnabled *bool
	IsDefault         *bool
	ModelCatalog      *[]ModelInfo
	CatalogFetchedAt  *time.Time
}

// Domain errors shared by the repository port, the configuration service and
// the HTTP layer. Callers use errors.Is rather than comparing messages.
var (
	// ErrConfigNotFound means no configuration matches the requested identifier.
	ErrConfigNotFound = errors.New("llm: configuration not found")
	// ErrNameTaken means another configuration already uses the name.
	ErrNameTaken = errors.New("llm: configuration name already taken")
	// ErrDefaultConflict means a concurrent writer created a default
	// configuration first; the partial unique index allows only one.
	ErrDefaultConflict = errors.New("llm: another configuration became the default")
	// ErrInvalidName means the configuration name is empty.
	ErrInvalidName = errors.New("llm: configuration name is required")
	// ErrInvalidModel means the model name is empty.
	ErrInvalidModel = errors.New("llm: model is required")
	// ErrInvalidProtocol means the protocol is not one of the supported dialects.
	ErrInvalidProtocol = errors.New("llm: protocol must be chat_completions or responses")
	// ErrInvalidBaseURL means base_url is not an absolute http or https URL.
	ErrInvalidBaseURL = errors.New("llm: base_url must be an http or https URL")
	// ErrInvalidRetryCount means retry_count is outside 0-5.
	ErrInvalidRetryCount = errors.New("llm: retry_count must be between 0 and 5")
	// ErrInvalidRetryInterval means retry_interval_ms is outside 100-30000.
	ErrInvalidRetryInterval = errors.New("llm: retry_interval_ms must be between 100 and 30000")
	// ErrInvalidTimeout means timeout_ms is outside 1000-300000.
	ErrInvalidTimeout = errors.New("llm: timeout_ms must be between 1000 and 300000")
	// ErrReservedHeader means a custom header would override a transport header
	// that the client sets itself.
	ErrReservedHeader = errors.New("llm: headers must not override Authorization or Content-Type")
	// ErrInvalidHeaderName means a custom header has an empty name.
	ErrInvalidHeaderName = errors.New("llm: header names must not be empty")
	// ErrSecretsUnavailable means the stored API key or headers cannot be
	// decrypted, for example because their key version is not configured.
	// The HTTP layer maps it to storage_corrupted.
	ErrSecretsUnavailable = errors.New("llm: stored credentials cannot be decrypted")
)

// ValidateName enforces a non-blank configuration name.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrInvalidName
	}
	return nil
}

// ValidateModel enforces a non-blank model name.
func ValidateModel(model string) error {
	if strings.TrimSpace(model) == "" {
		return ErrInvalidModel
	}
	return nil
}

// ValidateProtocol enforces the protocol enum.
func ValidateProtocol(protocol Protocol) error {
	if !protocol.Valid() {
		return ErrInvalidProtocol
	}
	return nil
}

// ValidateBaseURL enforces an absolute http or https URL with a host.
func ValidateBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ErrInvalidBaseURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ErrInvalidBaseURL
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return ErrInvalidBaseURL
	}
	return nil
}

// ValidateRetryCount enforces the documented retry-count range.
func ValidateRetryCount(count int) error {
	if count < MinRetryCount || count > MaxRetryCount {
		return ErrInvalidRetryCount
	}
	return nil
}

// ValidateRetryIntervalMS enforces the documented retry-interval range.
func ValidateRetryIntervalMS(intervalMS int) error {
	if intervalMS < MinRetryIntervalMS || intervalMS > MaxRetryIntervalMS {
		return ErrInvalidRetryInterval
	}
	return nil
}

// ValidateTimeoutMS enforces the documented timeout range.
func ValidateTimeoutMS(timeoutMS int) error {
	if timeoutMS < MinTimeoutMS || timeoutMS > MaxTimeoutMS {
		return ErrInvalidTimeout
	}
	return nil
}

// ValidateHeaders rejects empty names and the transport headers the client
// sets itself. The comparison is case-insensitive because HTTP header names are.
func ValidateHeaders(headers map[string]string) error {
	for name := range headers {
		if strings.TrimSpace(name) == "" {
			return ErrInvalidHeaderName
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authorization", "content-type":
			return ErrReservedHeader
		}
	}
	return nil
}

// Validate checks every field of a create request.
func (p CreateParams) Validate() error {
	if err := ValidateName(p.Name); err != nil {
		return err
	}
	if err := ValidateProtocol(p.Protocol); err != nil {
		return err
	}
	if err := ValidateBaseURL(p.BaseURL); err != nil {
		return err
	}
	if err := ValidateModel(p.Model); err != nil {
		return err
	}
	if err := ValidateRetryCount(p.RetryCount); err != nil {
		return err
	}
	if err := ValidateRetryIntervalMS(p.RetryIntervalMS); err != nil {
		return err
	}
	if err := ValidateTimeoutMS(p.TimeoutMS); err != nil {
		return err
	}
	return ValidateHeaders(p.Headers)
}

// Validate checks the fields an update request explicitly sets.
func (p UpdateParams) Validate() error {
	if p.Name != nil {
		if err := ValidateName(*p.Name); err != nil {
			return err
		}
	}
	if p.Protocol != nil {
		if err := ValidateProtocol(*p.Protocol); err != nil {
			return err
		}
	}
	if p.BaseURL != nil {
		if err := ValidateBaseURL(*p.BaseURL); err != nil {
			return err
		}
	}
	if p.Model != nil {
		if err := ValidateModel(*p.Model); err != nil {
			return err
		}
	}
	if p.RetryCount != nil {
		if err := ValidateRetryCount(*p.RetryCount); err != nil {
			return err
		}
	}
	if p.RetryIntervalMS != nil {
		if err := ValidateRetryIntervalMS(*p.RetryIntervalMS); err != nil {
			return err
		}
	}
	if p.TimeoutMS != nil {
		if err := ValidateTimeoutMS(*p.TimeoutMS); err != nil {
			return err
		}
	}
	if p.Headers != nil {
		if err := ValidateHeaders(*p.Headers); err != nil {
			return err
		}
	}
	return nil
}
