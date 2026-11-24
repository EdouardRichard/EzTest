package version_test

// v0.2.4-E final fix wave: cross-language behaviour gate for the requirement
// confirmation precheck (spec SC-E1/SC-E10).
//
// SC-E10 says the frontend must not keep a second hand-maintained implementation
// of the matching logic. The client precheck necessarily re-expresses the rule in
// TypeScript (there is no Go->TS codegen pipeline in this worktree), so the
// single-source mechanism is the same one the enum unions use: a SHARED, checked
// case set. testdata/confirmation_precheck_gate.json is that set; this test
// evaluates the Go AUTHORITY against it and
// frontend/src/features/clarification/requirement-precheck.authority.test.ts
// evaluates the TypeScript mirror against the SAME file. A semantic drift on
// either side fails its own gate instead of silently diverging.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type precheckGateFixture struct {
	PlaceholderCases []struct {
		Name          string   `json:"name"`
		Value         string   `json:"value"`
		Terms         []string `json:"terms"`
		IsPlaceholder bool     `json:"is_placeholder"`
	} `json:"placeholder_cases"`
	MinLengthCases []struct {
		Name     string `json:"name"`
		Value    string `json:"value"`
		MinChars int    `json:"min_chars"`
		TooShort bool   `json:"too_short"`
	} `json:"min_length_cases"`
}

func loadPrecheckGateFixture(t *testing.T) precheckGateFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/confirmation_precheck_gate.json")
	if err != nil {
		t.Fatalf("read shared precheck gate fixture: %v", err)
	}
	var fixture precheckGateFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode shared precheck gate fixture: %v", err)
	}
	if len(fixture.PlaceholderCases) == 0 || len(fixture.MinLengthCases) == 0 {
		t.Fatal("shared precheck gate fixture is empty; the gate would pass vacuously")
	}
	return fixture
}

// TestConfirmationPrecheckGatePlaceholder evaluates the Go placeholder authority
// against the shared cases (whole-value match after trim+lowercase; a substring
// must never match).
func TestConfirmationPrecheckGatePlaceholder(t *testing.T) {
	for _, test := range loadPrecheckGateFixture(t).PlaceholderCases {
		t.Run(test.Name, func(t *testing.T) {
			if got := version.IsPlaceholder(test.Value, test.Terms); got != test.IsPlaceholder {
				t.Fatalf("IsPlaceholder(%q, %v) = %v, want %v (shared cross-language case)",
					test.Value, test.Terms, got, test.IsPlaceholder)
			}
		})
	}
}

// TestConfirmationPrecheckGateMinLength evaluates the Go sut_description floor
// against the shared cases: trimmed RUNE count, not bytes and not UTF-16 units.
// It is the same expression ValidateConfirmed applies
// (`len([]rune(strings.TrimSpace(v.SutDescription))) < minDescChars`).
func TestConfirmationPrecheckGateMinLength(t *testing.T) {
	for _, test := range loadPrecheckGateFixture(t).MinLengthCases {
		t.Run(test.Name, func(t *testing.T) {
			got := utf8.RuneCountInString(strings.TrimSpace(test.Value)) < test.MinChars
			if got != test.TooShort {
				t.Fatalf("rune floor(%q, %d) tooShort = %v, want %v (shared cross-language case)",
					test.Value, test.MinChars, got, test.TooShort)
			}
		})
	}
}

// TestConfirmationPrecheckGateAgreesWithValidateConfirmed ties the shared floor
// cases to the REAL gate entry point, so the fixture cannot describe a rule the
// production validator does not apply. Cases whose trimmed value is EMPTY are
// skipped here: ValidateDraft already requires a non-empty sut_description, so
// their outcome is structural rather than the confirmation increment (they are
// still evaluated against the increment expression by the test above).
func TestConfirmationPrecheckGateAgreesWithValidateConfirmed(t *testing.T) {
	for _, test := range loadPrecheckGateFixture(t).MinLengthCases {
		if strings.TrimSpace(test.Value) == "" {
			continue
		}
		t.Run(test.Name, func(t *testing.T) {
			v := validRequirementVersion()
			v.SutDescription = test.Value
			err := v.ValidateConfirmed(nil, test.MinChars)
			if test.TooShort {
				if err == nil {
					t.Fatalf("ValidateConfirmed accepted a below-floor description %q", test.Value)
				}
				if field := requirementAPIField(t, err); field != "sut_description" {
					t.Fatalf("field = %q, want sut_description", field)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateConfirmed rejected an above-floor description: %v", err)
			}
		})
	}
}
