package agent_test

import (
	"strings"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func promptCatalog() []agent.PromptVariable {
	return []agent.PromptVariable{
		{Name: "Now", Description: "当前时间", Example: "2026-11-01T12:00:00Z", Source: "runtime"},
		{Name: "VersionTitle", Description: "版本标题", Example: "订单系统 Q4", Source: "runtime"},
	}
}

func validPrompt() string {
	return "[profile:clarification_host]\n当前时间 {{.Now}}，版本 {{.VersionTitle}}。"
}

func TestValidatePromptAcceptsCatalogVariables(t *testing.T) {
	if err := agent.ValidatePrompt("clarification_host", validPrompt(), promptCatalog()); err != nil {
		t.Fatalf("ValidatePrompt: %v", err)
	}
	// A prompt without template actions stays valid.
	if err := agent.ValidatePrompt("clarification_host", "[profile:clarification_host] 你是澄清 Host。", promptCatalog()); err != nil {
		t.Fatalf("plain prompt: %v", err)
	}
}

func TestValidatePromptRejectsInvalidBodies(t *testing.T) {
	long := "[profile:clarification_host] " + strings.Repeat("长", agent.MaxPromptRunes)
	cases := []struct {
		name   string
		prompt string
		reason string
		field  string
	}{
		{name: "empty", prompt: "", field: "system_prompt"},
		{name: "whitespace", prompt: "   \n\t", field: "system_prompt"},
		{name: "too long", prompt: long, field: "system_prompt"},
		{name: "missing marker", prompt: "你是澄清 Host。", reason: "missing_profile_marker", field: "system_prompt"},
		{name: "wrong marker", prompt: "[profile:design_host] 你是澄清 Host。", reason: "missing_profile_marker", field: "system_prompt"},
		{name: "unknown variable", prompt: "[profile:clarification_host] {{.Bogus}}", reason: "unknown_variable", field: "system_prompt"},
		{name: "field chain", prompt: "[profile:clarification_host] {{.Version.Title}}", reason: "unknown_variable", field: "system_prompt"},
		{name: "function call", prompt: "[profile:clarification_host] {{printf \"x\"}}", reason: "invalid_template", field: "system_prompt"},
		{name: "syntax", prompt: "[profile:clarification_host] {{.Now", reason: "invalid_template", field: "system_prompt"},
		{name: "empty action", prompt: "[profile:clarification_host] {{}}", reason: "invalid_template", field: "system_prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := agent.ValidatePrompt("clarification_host", tc.prompt, promptCatalog())
			if err == nil {
				t.Fatal("expected validation error")
			}
			apiErr := platform.AsError(err)
			if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
				t.Fatalf("err = %#v", apiErr)
			}
			if apiErr.Details["field"] != tc.field {
				t.Fatalf("details = %#v", apiErr.Details)
			}
			if tc.reason != "" && apiErr.Details["reason"] != tc.reason {
				t.Fatalf("reason = %#v, want %q", apiErr.Details["reason"], tc.reason)
			}
		})
	}
}

func TestValidatePromptTemplateAllowsControlFlowOverVariables(t *testing.T) {
	tmpl := "[profile:clarification_host]{{if .VersionTitle}}版本 {{.VersionTitle}}{{else}}无版本{{end}}"
	if err := agent.ValidatePromptTemplate(tmpl, promptCatalog()); err != nil {
		t.Fatalf("control flow template: %v", err)
	}
}

func TestValidatePromptRejectsUnknownVariableInControlFlow(t *testing.T) {
	tmpl := "[profile:clarification_host]{{if .Missing}}x{{end}}"
	err := agent.ValidatePromptTemplate(tmpl, promptCatalog())
	if err == nil || platform.AsError(err).Details["reason"] != "unknown_variable" {
		t.Fatalf("err = %v", err)
	}
}

func TestPromptSourceValidation(t *testing.T) {
	for _, source := range []string{agent.PromptSourceSeed, agent.PromptSourceAdmin, agent.PromptSourceReset} {
		if err := agent.ValidatePromptSource(source); err != nil {
			t.Fatalf("source %q: %v", source, err)
		}
	}
	for _, source := range []string{"", "system", "ADMIN"} {
		if err := agent.ValidatePromptSource(source); err == nil {
			t.Fatalf("source %q accepted", source)
		}
	}
}

func TestPromptWriteValidate(t *testing.T) {
	valid := agent.PromptWrite{
		ProfileKey:   "clarification_host",
		SystemPrompt: validPrompt(),
		Source:       agent.PromptSourceAdmin,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid write: %v", err)
	}

	invalidSource := valid
	invalidSource.Source = "system"
	if err := invalidSource.Validate(); err == nil {
		t.Fatal("invalid source accepted")
	}

	emptyKey := valid
	emptyKey.ProfileKey = ""
	if err := emptyKey.Validate(); err == nil {
		t.Fatal("empty key accepted")
	}

	emptyPrompt := valid
	emptyPrompt.SystemPrompt = ""
	if err := emptyPrompt.Validate(); err == nil {
		t.Fatal("empty prompt accepted")
	}

	badExpected := valid
	badExpected.ExpectedVersion = promptIntPtr(0)
	if err := badExpected.Validate(); err == nil {
		t.Fatal("non-positive expected version accepted")
	}
}

func TestPromptRevisionValidate(t *testing.T) {
	revision := agent.PromptRevision{
		ID: 1, ProfileKey: "clarification_host", Version: 1,
		SystemPrompt: validPrompt(), Source: agent.PromptSourceSeed,
	}
	if err := revision.Validate(); err != nil {
		t.Fatalf("revision validate: %v", err)
	}
	revision.Version = 0
	if err := revision.Validate(); err == nil {
		t.Fatal("zero version accepted")
	}
}

func promptIntPtr(value int) *int { return &value }
