package domain

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// AuthFlowKindForm is the only login recipe kind P6 supports (D41).
const AuthFlowKindForm = "form"

// IDGenerator mints snowflake ids for new auth flows. *id.Snowflake implements
// it, and tests inject a deterministic counter.
type IDGenerator interface {
	Next() int64
}

// FormDefinition timeout bounds from spec §3.4.
const (
	minAuthFlowTimeoutMS = 5000
	maxAuthFlowTimeoutMS = 60000
)

// AuthFlow is one login recipe of a system (spec §3.4). Definition is the
// minimal form recipe; kind is fixed to "form". The stored definition is not a
// secret, but it is never copied into events or logs.
type AuthFlow struct {
	ID         int64
	SystemID   int64
	Name       string
	Kind       string
	Definition FormDefinition
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// FormDefinition is the minimal form login recipe (spec §3.4). Success is
// either a URL fragment or a selector, never both; Failure selectors are
// optional and drive the manual-takeover path.
type FormDefinition struct {
	LoginURL         string           `json:"login_url"`
	UsernameSelector string           `json:"username_selector"`
	PasswordSelector string           `json:"password_selector"`
	SubmitSelector   string           `json:"submit_selector"`
	Success          SuccessRule      `json:"success"`
	Failure          FailureSelectors `json:"failure"`
	TimeoutMS        int              `json:"timeout_ms"`
}

// SuccessRule decides whether a login succeeded. Exactly one of URLContains and
// Selector must be set.
type SuccessRule struct {
	URLContains string `json:"url_contains,omitempty"`
	Selector    string `json:"selector,omitempty"`
}

// FailureSelectors identify the states that need a human: MFA, captcha and a
// rejected credential. All are optional.
type FailureSelectors struct {
	MFASelector     string `json:"mfa_selector,omitempty"`
	CaptchaSelector string `json:"captcha_selector,omitempty"`
	InvalidSelector string `json:"invalid_selector,omitempty"`
}

// Validate enforces spec §3.4: an absolute http(s) login URL, non-empty
// selectors, exactly one success rule and a 5000-60000 ms timeout. Every
// failure is the shared 422 auth_flow_invalid error.
func (d FormDefinition) Validate() error {
	if err := validateAuthFlowLoginURL(d.LoginURL); err != nil {
		return err
	}
	selectors := []struct{ field, value string }{
		{"definition.username_selector", d.UsernameSelector},
		{"definition.password_selector", d.PasswordSelector},
		{"definition.submit_selector", d.SubmitSelector},
	}
	for _, selector := range selectors {
		if strings.TrimSpace(selector.value) == "" {
			return authFlowInvalid(selector.field, "selector must not be empty")
		}
	}

	hasURLSuccess := strings.TrimSpace(d.Success.URLContains) != ""
	hasSelectorSuccess := strings.TrimSpace(d.Success.Selector) != ""
	if hasURLSuccess == hasSelectorSuccess {
		return authFlowInvalid("definition.success",
			"success must set exactly one of url_contains or selector")
	}

	if d.TimeoutMS < minAuthFlowTimeoutMS || d.TimeoutMS > maxAuthFlowTimeoutMS {
		return authFlowInvalid("definition.timeout_ms",
			fmt.Sprintf("timeout_ms must be between %d and %d", minAuthFlowTimeoutMS, maxAuthFlowTimeoutMS))
	}
	return nil
}

// ValidateAuthFlowKind enforces the auth_flow.kind CHECK constraint.
func ValidateAuthFlowKind(kind string) error {
	if kind != AuthFlowKindForm {
		return authFlowInvalid("kind", `kind must be "form"`)
	}
	return nil
}

// validateAuthFlowLoginURL checks that raw is an absolute http(s) URL with a
// host.
func validateAuthFlowLoginURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return authFlowInvalid("definition.login_url", "login_url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return authFlowInvalid("definition.login_url", "login_url must be an absolute http(s) URL")
	}
	return nil
}

// authFlowInvalid returns the shared 422 auth_flow_invalid error.
func authFlowInvalid(field, message string) *Error {
	return NewInvalid(field, "auth_flow_invalid", message)
}
