package agent_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestValidateSkillName(t *testing.T) {
	valid := []string{"playwright-cli", "api-recon", "a", "skill_2", strings.Repeat("a", 64)}
	for _, name := range valid {
		if err := agent.ValidateSkillName(name); err != nil {
			t.Fatalf("ValidateSkillName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "Playwright", "1skill", "-lead", "has space", "has/slash", strings.Repeat("a", 65), "中文名"}
	for _, name := range invalid {
		err := agent.ValidateSkillName(name)
		if err == nil {
			t.Fatalf("ValidateSkillName(%q) = nil, want invalid", name)
		}
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 400 {
			t.Fatalf("ValidateSkillName(%q) error = %v, want a 400 platform error", name, err)
		}
	}
}

func TestValidateSkillBody(t *testing.T) {
	if err := agent.ValidateSkillBody("# Usage\n\nRun the CLI."); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
	for _, body := range []string{"", "   ", "\n\t"} {
		if err := agent.ValidateSkillBody(body); err == nil {
			t.Fatalf("ValidateSkillBody(%q) = nil, want invalid", body)
		}
	}
	if err := agent.ValidateSkillBody(strings.Repeat("x", agent.MaxSkillBodyRunes)); err != nil {
		t.Fatalf("body at the rune limit rejected: %v", err)
	}
	if err := agent.ValidateSkillBody(strings.Repeat("x", agent.MaxSkillBodyRunes+1)); err == nil {
		t.Fatal("body above the rune limit accepted")
	}
}

func TestValidateSkillDescription(t *testing.T) {
	if err := agent.ValidateSkillDescription(""); err != nil {
		t.Fatalf("empty description rejected (optional field): %v", err)
	}
	if err := agent.ValidateSkillDescription(strings.Repeat("d", agent.MaxSkillDescriptionRunes)); err != nil {
		t.Fatalf("description at the limit rejected: %v", err)
	}
	if err := agent.ValidateSkillDescription(strings.Repeat("d", agent.MaxSkillDescriptionRunes+1)); err == nil {
		t.Fatal("description above the limit accepted")
	}
}

func TestValidateSkillMetadata(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"tags":["browser"]}`)} {
		if err := agent.ValidateSkillMetadata(raw); err != nil {
			t.Fatalf("ValidateSkillMetadata(%s) = %v, want nil", raw, err)
		}
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`"x"`), json.RawMessage(`null`), json.RawMessage(`{invalid`)} {
		if err := agent.ValidateSkillMetadata(raw); err == nil {
			t.Fatalf("ValidateSkillMetadata(%s) = nil, want invalid", raw)
		}
	}
}

func TestSkillUpdateEmpty(t *testing.T) {
	if !(agent.SkillUpdate{}).Empty() {
		t.Fatal("zero SkillUpdate must be empty")
	}
	version := 3
	if !(agent.SkillUpdate{ExpectedVersion: &version}).Empty() {
		t.Fatal("expected_version alone is not a writable field")
	}
	desc := "d"
	body := "b"
	enabled := false
	for _, update := range []agent.SkillUpdate{
		{Description: &desc},
		{Body: &body},
		{Metadata: json.RawMessage(`{"a":1}`)},
		{Enabled: &enabled},
	} {
		if update.Empty() {
			t.Fatalf("update %#v reported empty", update)
		}
	}
}
