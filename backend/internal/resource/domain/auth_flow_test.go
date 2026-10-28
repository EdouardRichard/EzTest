package domain

import (
	"errors"
	"testing"
)

// validFormDefinition returns a minimal complete login recipe. Tests mutate one
// field to prove a single rule at a time.
func validFormDefinition() FormDefinition {
	return FormDefinition{
		LoginURL:         "https://app.test/login",
		UsernameSelector: "#username",
		PasswordSelector: "#password",
		SubmitSelector:   "button[type=submit]",
		Success:          SuccessRule{URLContains: "/home"},
		TimeoutMS:        20000,
	}
}

func TestFormDefinitionValidatesRequiredSelectors(t *testing.T) {
	d := FormDefinition{LoginURL: "https://x.test/login", UsernameSelector: "#u"}
	if err := d.Validate(); err == nil {
		t.Fatal("missing password/submit/success must fail")
	}
}

// TestFormDefinitionRequiresEachSelector proves every selector is mandatory and
// a whitespace-only value does not count.
func TestFormDefinitionRequiresEachSelector(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*FormDefinition)
		field  string
	}{
		{"username", func(d *FormDefinition) { d.UsernameSelector = "" }, "definition.username_selector"},
		{"password", func(d *FormDefinition) { d.PasswordSelector = " " }, "definition.password_selector"},
		{"submit", func(d *FormDefinition) { d.SubmitSelector = "" }, "definition.submit_selector"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validFormDefinition()
			tc.mutate(&d)
			assertAuthFlowInvalid(t, d.Validate(), tc.field)
		})
	}
}

// TestFormDefinitionRejectsInvalidLoginURL covers the http(s) URL rule.
func TestFormDefinitionRejectsInvalidLoginURL(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"relative":   "/login",
		"no scheme":  "app.test/login",
		"ftp":        "ftp://app.test/login",
		"no host":    "https:///login",
		"javascript": "javascript:alert(1)",
	}
	for name, rawURL := range cases {
		t.Run(name, func(t *testing.T) {
			d := validFormDefinition()
			d.LoginURL = rawURL
			assertAuthFlowInvalid(t, d.Validate(), "definition.login_url")
		})
	}
}

// TestFormDefinitionRequiresExactlyOneSuccessRule covers the url_contains XOR
// selector success rule.
func TestFormDefinitionRequiresExactlyOneSuccessRule(t *testing.T) {
	cases := []struct {
		name    string
		success SuccessRule
	}{
		{"neither", SuccessRule{}},
		{"both", SuccessRule{URLContains: "/home", Selector: "#welcome"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validFormDefinition()
			d.Success = tc.success
			assertAuthFlowInvalid(t, d.Validate(), "definition.success")
		})
	}

	d := validFormDefinition()
	d.Success = SuccessRule{Selector: "#welcome"}
	if err := d.Validate(); err != nil {
		t.Fatalf("selector-only success rule rejected: %v", err)
	}
}

// TestFormDefinitionAcceptsMinimalRecipe proves the documented minimal recipe
// from spec §3.4 is valid.
func TestFormDefinitionAcceptsMinimalRecipe(t *testing.T) {
	d := FormDefinition{
		LoginURL:         "http://127.0.0.1:13335/p6/login",
		UsernameSelector: "#username",
		PasswordSelector: "#password",
		SubmitSelector:   "button[type=submit]",
		Success:          SuccessRule{URLContains: "/p6/home"},
		Failure: FailureSelectors{
			MFASelector:     "#mfa",
			CaptchaSelector: "#captcha",
			InvalidSelector: ".login-error",
		},
		TimeoutMS: 20000,
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("minimal recipe rejected: %v", err)
	}
}

// TestFormDefinitionTimeoutBounds covers the inclusive 5000-60000 ms range.
func TestFormDefinitionTimeoutBounds(t *testing.T) {
	cases := []struct {
		name      string
		timeoutMS int
		wantErr   bool
	}{
		{"zero", 0, true},
		{"below minimum", 4999, true},
		{"minimum", 5000, false},
		{"maximum", 60000, false},
		{"above maximum", 60001, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validFormDefinition()
			d.TimeoutMS = tc.timeoutMS
			err := d.Validate()
			if tc.wantErr {
				assertAuthFlowInvalid(t, err, "definition.timeout_ms")
				return
			}
			if err != nil {
				t.Fatalf("timeout %d rejected: %v", tc.timeoutMS, err)
			}
		})
	}
}

// assertAuthFlowInvalid asserts the shared 422 auth_flow_invalid domain error
// carries the expected field.
func assertAuthFlowInvalid(t *testing.T, err error, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an auth_flow_invalid error")
	}
	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %v is not a *domain.Error", err)
	}
	if domainErr.Kind != KindInvalid || domainErr.Reason != "auth_flow_invalid" {
		t.Fatalf("error kind/reason=%q/%q want invalid/auth_flow_invalid", domainErr.Kind, domainErr.Reason)
	}
	if domainErr.Field != wantField {
		t.Fatalf("error field=%q want %q", domainErr.Field, wantField)
	}
}
