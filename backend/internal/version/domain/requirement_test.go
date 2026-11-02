package version_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func validRequirementVersion() version.RequirementVersion {
	return version.RequirementVersion{
		Title:              "Checkout supports coupons",
		Description:        "Users can apply a coupon code at checkout.",
		AcceptanceCriteria: "A valid coupon reduces the total by its discount.",
		Priority:           "P1",
		Constraints:        json.RawMessage(`{"business_rules":["one coupon per order"],"preconditions":["signed in"]}`),
		TargetVersionSUTs:  []string{"5001"},
		TargetPages:        []string{"6001", "6002"},
		TargetAPIs:         []string{"7001"},
	}
}

func requirementAPIField(t *testing.T, err error) string {
	t.Helper()
	apiErr, ok := err.(*platform.Error)
	if !ok {
		t.Fatalf("error %v is not a platform error", err)
	}
	if apiErr.Status != 400 {
		t.Fatalf("status = %d, want 400", apiErr.Status)
	}
	field, _ := apiErr.Details["field"].(string)
	return field
}

func TestRequirementStatusTransitions(t *testing.T) {
	allowed := []struct {
		from, to version.RequirementStatus
	}{
		{version.RequirementDraft, version.RequirementClarified},
		{version.RequirementClarified, version.RequirementDraft},
		{version.RequirementClosed, version.RequirementDraft},
	}
	for _, tc := range allowed {
		if !version.CanTransitionRequirement(tc.from, tc.to) {
			t.Fatalf("%s -> %s should be allowed", tc.from, tc.to)
		}
	}

	rejected := []struct {
		from, to version.RequirementStatus
	}{
		{version.RequirementDraft, version.RequirementClosed},
		{version.RequirementDraft, version.RequirementObsolete},
		{version.RequirementClarified, version.RequirementClosed},
		{version.RequirementObsolete, version.RequirementClarified},
		{version.RequirementDraft, version.RequirementDraft},
		{version.RequirementClarified, version.RequirementClarified},
		{version.RequirementClosed, version.RequirementClarified},
	}
	for _, tc := range rejected {
		if version.CanTransitionRequirement(tc.from, tc.to) {
			t.Fatalf("%s -> %s should be rejected", tc.from, tc.to)
		}
	}
}

func TestRequirementPriorityNormalization(t *testing.T) {
	cases := map[string]string{"P0": "P0", "P1": "P1", "P2": "P2", "P3": "P3", "": "P2", "  ": "P2"}
	for raw, want := range cases {
		got, err := version.NormalizePriority(raw)
		if err != nil {
			t.Fatalf("NormalizePriority(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("NormalizePriority(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{"P4", "p0", "urgent"} {
		if _, err := version.NormalizePriority(raw); err == nil {
			t.Fatalf("NormalizePriority(%q) accepted an invalid priority", raw)
		} else if field := requirementAPIField(t, err); field != "priority" {
			t.Fatalf("field = %q, want priority", field)
		}
	}
}

func TestRequirementVersionValidatesDraft(t *testing.T) {
	if err := validRequirementVersion().ValidateDraft(); err != nil {
		t.Fatalf("valid draft rejected: %v", err)
	}

	tooLongTitle := validRequirementVersion()
	tooLongTitle.Title = string(make([]rune, version.RequirementTitleMax+1))
	tooLongDescription := validRequirementVersion()
	tooLongDescription.Description = string(make([]rune, version.RequirementDescriptionMax+1))
	tooLongAcceptance := validRequirementVersion()
	tooLongAcceptance.AcceptanceCriteria = string(make([]rune, version.RequirementAcceptanceMax+1))
	badTarget := validRequirementVersion()
	badTarget.TargetPages = []string{"not-an-id"}
	zeroTarget := validRequirementVersion()
	zeroTarget.TargetVersionSUTs = []string{"0"}
	badConstraints := validRequirementVersion()
	badConstraints.Constraints = json.RawMessage(`[1,2,3]`)
	badJSON := validRequirementVersion()
	badJSON.Constraints = json.RawMessage(`{"oops"`)

	cases := []struct {
		name      string
		content   version.RequirementVersion
		wantField string
	}{
		{"blank title", version.RequirementVersion{Title: "  ", Priority: "P1"}, "title"},
		{"long title", tooLongTitle, "title"},
		{"long description", tooLongDescription, "description"},
		{"long acceptance", tooLongAcceptance, "acceptance_criteria"},
		{"bad target", badTarget, "target_pages"},
		{"zero target", zeroTarget, "target_version_suts"},
		{"bad constraints", badConstraints, "constraints"},
		{"bad constraints json", badJSON, "constraints"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.content.ValidateDraft()
			if err == nil {
				t.Fatal("invalid draft accepted")
			}
			if field := requirementAPIField(t, err); field != tc.wantField {
				t.Fatalf("field = %q, want %q", field, tc.wantField)
			}
		})
	}

	// The OpenAPI Create/Update request requires every content field, so a draft
	// is already complete before confirmation.
	missingDescription := validRequirementVersion()
	missingDescription.Description = ""
	if err := missingDescription.ValidateDraft(); err == nil {
		t.Fatal("draft without description accepted")
	} else if field := requirementAPIField(t, err); field != "description" {
		t.Fatalf("field = %q, want description", field)
	}
}

func TestRequirementVersionValidatesConfirmation(t *testing.T) {
	content := validRequirementVersion()
	if err := content.ValidateConfirmed(); err != nil {
		t.Fatalf("valid confirmation content rejected: %v", err)
	}

	missingDescription := validRequirementVersion()
	missingDescription.Description = ""
	missingAcceptance := validRequirementVersion()
	missingAcceptance.AcceptanceCriteria = ""
	blankTitle := validRequirementVersion()
	blankTitle.Title = "   "

	cases := []struct {
		name      string
		content   version.RequirementVersion
		wantField string
	}{
		{"missing description", missingDescription, "description"},
		{"missing acceptance", missingAcceptance, "acceptance_criteria"},
		{"blank title", blankTitle, "title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.content.ValidateConfirmed()
			if err == nil {
				t.Fatal("incomplete requirement was confirmable")
			}
			if field := requirementAPIField(t, err); field != tc.wantField {
				t.Fatalf("field = %q, want %q", field, tc.wantField)
			}
		})
	}
}

func TestContentHashIsStableAndNormalized(t *testing.T) {
	base := validRequirementVersion()
	hash, err := base.ComputeContentHash()
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
		t.Fatalf("hash %q is not sha256 hex", hash)
	}
	again, err := base.ComputeContentHash()
	if err != nil || again != hash {
		t.Fatalf("hash is not deterministic: %q/%q (%v)", hash, again, err)
	}

	reordered := validRequirementVersion()
	reordered.TargetPages = []string{"6002", "6001", "6002"}
	reordered.Constraints = json.RawMessage(`{"preconditions":["signed in"],"business_rules":["one coupon per order"]}`)
	reorderedHash, err := reordered.ComputeContentHash()
	if err != nil || reorderedHash != hash {
		t.Fatalf("equivalent content produced a different hash: %q vs %q (%v)", hash, reorderedHash, err)
	}

	changed := validRequirementVersion()
	changed.Title = "Checkout supports gift cards"
	changedHash, err := changed.ComputeContentHash()
	if err != nil || changedHash == hash {
		t.Fatalf("different content produced the same hash: %q", changedHash)
	}
}

func TestValidRelationKind(t *testing.T) {
	for _, kind := range []string{"depends_on", "conflicts_with", "overlaps", "shares_scope"} {
		if !version.ValidRelationKind(kind) {
			t.Fatalf("%q should be a valid relation kind", kind)
		}
	}
	for _, kind := range []string{"", "blocks", "DEPENDS_ON"} {
		if version.ValidRelationKind(kind) {
			t.Fatalf("%q should be invalid", kind)
		}
	}
}
