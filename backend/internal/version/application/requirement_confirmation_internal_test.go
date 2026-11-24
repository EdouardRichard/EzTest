package versionapp

import (
	"context"
	"encoding/json"
	"testing"
)

// stubConfirmationSettings serves fixed raw values so the resolver's
// sanitization and validation can be pinned directly (an empty term is
// unobservable through Confirm: ValidateDraft rejects empty fields first).
type stubConfirmationSettings struct {
	terms json.RawMessage
	min   int64
}

func (s stubConfirmationSettings) Int(context.Context, string) (int64, error) {
	return s.min, nil
}

func (s stubConfirmationSettings) Raw(context.Context, string) (json.RawMessage, error) {
	return s.terms, nil
}

// TestConfirmationRulesSanitizesTerms pins the T3-review carry: terms that
// normalize to "" (empty or whitespace-only) would whole-value-match any empty
// optional field via IsPlaceholder, so the resolver drops them before the
// domain gate sees the list.
func TestConfirmationRulesSanitizesTerms(t *testing.T) {
	svc := &RequirementService{settings: stubConfirmationSettings{
		terms: json.RawMessage(`["TBD","  ","","\t","待定"]`),
		min:   20,
	}}
	terms, minDescChars, err := svc.confirmationRules(context.Background())
	if err != nil {
		t.Fatalf("confirmationRules: %v", err)
	}
	if minDescChars != 20 {
		t.Fatalf("minDescChars = %d, want 20", minDescChars)
	}
	want := []string{"TBD", "待定"}
	if len(terms) != len(want) {
		t.Fatalf("terms = %q, want %q (empty/whitespace terms dropped)", terms, want)
	}
	for i := range want {
		if terms[i] != want[i] {
			t.Fatalf("terms = %q, want %q", terms, want)
		}
	}
}

// TestConfirmationRulesRejectsInvalidValues pins fail-closed reads: a
// non-string-array terms value or a floor below the catalog minimum (1) is an
// explicit error, never a silently relaxed gate.
func TestConfirmationRulesRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		terms json.RawMessage
		min   int64
	}{
		{"terms not an array", json.RawMessage(`{"a":1}`), 20},
		{"terms not strings", json.RawMessage(`[1,2]`), 20},
		{"floor below catalog minimum", json.RawMessage(`[]`), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &RequirementService{settings: stubConfirmationSettings{terms: tc.terms, min: tc.min}}
			if _, _, err := svc.confirmationRules(context.Background()); err == nil {
				t.Fatal("invalid settings value accepted")
			}
		})
	}
}
