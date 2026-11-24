package version_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func validRequirementVersion() version.RequirementVersion {
	return version.RequirementVersion{
		Title: "Checkout supports coupons",
		UserStory: version.UserStory{
			AsA:    "a signed-in shopper",
			IWant:  "to apply a coupon code at checkout",
			SoThat: "I pay less for my order",
		},
		Rules: "One coupon per order.",
		AcceptanceCriteria: []version.AcceptanceCriterion{
			{
				ID:    "AC-1",
				Given: "a signed-in shopper with a valid coupon",
				When:  "the coupon is applied at checkout",
				Then:  "the order total drops by the coupon discount",
			},
			{
				ID:    "AC-2",
				Given: "a signed-in shopper",
				When:  "an expired coupon is applied",
				Then:  "the checkout rejects it and says why",
			},
		},
		Priority:         "P1",
		Constraints:      json.RawMessage(`{"business_rules":["one coupon per order"],"preconditions":["signed in"]}`),
		SutDescription:   "The web storefront's checkout page and the pricing API behind it.",
		ScopeOut:         "Gift cards.",
		AccountRolesHint: []string{"shopper"},
	}
}

// longRunes builds a string of n runes for the length-bound cases.
func longRunes(n int) string {
	return string(make([]rune, n))
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

	longTitle := validRequirementVersion()
	longTitle.Title = longRunes(version.RequirementTitleMax + 1)
	longStoryPart := validRequirementVersion()
	longStoryPart.UserStory.IWant = longRunes(version.RequirementUserStoryPartMax + 1)
	missingStoryPart := validRequirementVersion()
	missingStoryPart.UserStory.SoThat = "   "
	longRules := validRequirementVersion()
	longRules.Rules = longRunes(version.RequirementRulesMax + 1)
	noCriteria := validRequirementVersion()
	noCriteria.AcceptanceCriteria = nil
	longACPart := validRequirementVersion()
	longACPart.AcceptanceCriteria[0].Then = longRunes(version.AcceptanceCriterionPartMax + 1)
	blankACPart := validRequirementVersion()
	blankACPart.AcceptanceCriteria[1].Given = ""
	badACID := validRequirementVersion()
	badACID.AcceptanceCriteria[0].ID = "1"
	duplicateACID := validRequirementVersion()
	duplicateACID.AcceptanceCriteria[1].ID = "AC-1"
	longSutDescription := validRequirementVersion()
	longSutDescription.SutDescription = longRunes(version.RequirementSutDescriptionMax + 1)
	missingSutDescription := validRequirementVersion()
	missingSutDescription.SutDescription = ""
	longScopeOut := validRequirementVersion()
	longScopeOut.ScopeOut = longRunes(version.RequirementScopeOutMax + 1)
	longRole := validRequirementVersion()
	longRole.AccountRolesHint = []string{longRunes(version.RequirementAccountRoleMax + 1)}
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
		{"long title", longTitle, "title"},
		{"long user story part", longStoryPart, "user_story.i_want"},
		{"missing user story part", missingStoryPart, "user_story.so_that"},
		{"long rules", longRules, "rules"},
		{"no acceptance criteria", noCriteria, "acceptance_criteria"},
		{"long acceptance part", longACPart, "acceptance_criteria[0].then"},
		{"blank acceptance part", blankACPart, "acceptance_criteria[1].given"},
		{"non canonical ac id", badACID, "acceptance_criteria[0].id"},
		{"duplicate ac id", duplicateACID, "acceptance_criteria[1].id"},
		{"long sut_description", longSutDescription, "sut_description"},
		{"missing sut_description", missingSutDescription, "sut_description"},
		{"long scope_out", longScopeOut, "scope_out"},
		{"long account role", longRole, "account_roles_hint[0]"},
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
}

// TestValidateDraftStaysLax pins the I-B1 boundary: the draft gate is structural
// only. The completeness/clarity increment -- the placeholder-term blacklist and
// the minimum-description-chars floor -- belongs to the confirmation gate that
// subproject E owns, so a draft whose sut_description is short but well-formed,
// or whose story still says "TBD", must be accepted here.
func TestValidateDraftStaysLax(t *testing.T) {
	shortButValid := validRequirementVersion()
	shortButValid.SutDescription = "Checkout."
	placeholder := validRequirementVersion()
	placeholder.UserStory.SoThat = "TBD"
	placeholder.Rules = "TODO: 待补充"
	placeholder.AcceptanceCriteria[0].Then = "待定"

	for _, content := range []version.RequirementVersion{shortButValid, placeholder} {
		if err := content.ValidateDraft(); err != nil {
			t.Fatalf("draft gate rejected content that only the confirmation gate may judge: %v", err)
		}
	}
}

func TestRequirementVersionValidatesConfirmation(t *testing.T) {
	content := validRequirementVersion()
	if err := content.ValidateConfirmed(nil, 0); err != nil {
		t.Fatalf("valid confirmation content rejected: %v", err)
	}

	// ValidateConfirmed(nil, 0) applies no completeness/clarity increment (no
	// placeholder terms, no min-length floor), so for these structural failures
	// it still agrees with ValidateDraft field for field. The application layer
	// reads the real settings-backed terms/minDescChars at the confirmation
	// call sites (T11).
	cases := map[string]version.RequirementVersion{}
	blankTitle := validRequirementVersion()
	blankTitle.Title = "   "
	cases["blank title"] = blankTitle
	missingSutDescription := validRequirementVersion()
	missingSutDescription.SutDescription = ""
	cases["missing sut_description"] = missingSutDescription
	noCriteria := validRequirementVersion()
	noCriteria.AcceptanceCriteria = nil
	cases["no acceptance criteria"] = noCriteria

	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			confirmedErr := invalid.ValidateConfirmed(nil, 0)
			draftErr := invalid.ValidateDraft()
			if confirmedErr == nil {
				t.Fatal("incomplete requirement was confirmable")
			}
			if confirmedErr.Error() != draftErr.Error() {
				t.Fatalf("ValidateConfirmed = %v, want the ValidateDraft result %v", confirmedErr, draftErr)
			}
		})
	}
}

// TestContentHashSelfNormalizesTextFields proves the content address is
// single-source regardless of caller (constitution 四.6/五.1): the hash trims
// title/user_story/rules/sut_description internally, so a third caller that has
// not normalized its input cannot compute a different hash for the same content.
// It also pins the exact canonical bytes of an already-trimmed (stored) revision
// against an independently assembled payload, so a change that moved the hash of
// stored content would fail here rather than silently re-address every revision.
func TestContentHashSelfNormalizesTextFields(t *testing.T) {
	stored := validRequirementVersion()
	storedHash, err := stored.ComputeContentHash()
	if err != nil {
		t.Fatalf("ComputeContentHash: %v", err)
	}

	// 1. A third caller passes untrimmed text: same content must mean same hash.
	untrimmed := validRequirementVersion()
	untrimmed.Title = "  " + stored.Title + "\t"
	untrimmed.UserStory = version.UserStory{
		AsA:    " " + stored.UserStory.AsA + " ",
		IWant:  "\n" + stored.UserStory.IWant + "\t",
		SoThat: "  " + stored.UserStory.SoThat + "  ",
	}
	untrimmed.Rules = "\t" + stored.Rules + " \n"
	untrimmed.SutDescription = " " + stored.SutDescription + " "
	if got, err := untrimmed.ComputeContentHash(); err != nil || got != storedHash {
		t.Fatalf("untrimmed input hashed differently: %q vs stored %q (%v) -- the hash must self-normalize text fields", got, storedHash, err)
	}

	// 2. The stored (already trimmed) hash equals the canonical payload assembled
	// by hand from the documented contract, independent of ComputeContentHash.
	// This pins the exact bytes so the self-normalization fix cannot have moved a
	// previously-stored hash. Field order and normalization mirror the doc comment
	// on ComputeContentHash; the expected value comes from that contract, not from
	// observing the function's output.
	type acHash struct {
		ID    string `json:"id"`
		Given string `json:"given"`
		When  string `json:"when"`
		Then  string `json:"then"`
	}
	type storyHash struct {
		AsA    string `json:"as_a"`
		IWant  string `json:"i_want"`
		SoThat string `json:"so_that"`
	}
	canonical := struct {
		Title              string          `json:"title"`
		UserStory          storyHash       `json:"user_story"`
		Rules              string          `json:"rules"`
		AcceptanceCriteria []acHash        `json:"acceptance_criteria"`
		Priority           string          `json:"priority"`
		Constraints        json.RawMessage `json:"constraints"`
		SutDescription     string          `json:"sut_description"`
		ScopeOut           string          `json:"scope_out"`
		AccountRolesHint   []string        `json:"account_roles_hint"`
	}{
		Title:     "Checkout supports coupons",
		UserStory: storyHash{AsA: "a signed-in shopper", IWant: "to apply a coupon code at checkout", SoThat: "I pay less for my order"},
		Rules:     "One coupon per order.",
		AcceptanceCriteria: []acHash{
			{ID: "AC-1", Given: "a signed-in shopper with a valid coupon", When: "the coupon is applied at checkout", Then: "the order total drops by the coupon discount"},
			{ID: "AC-2", Given: "a signed-in shopper", When: "an expired coupon is applied", Then: "the checkout rejects it and says why"},
		},
		Priority:         "P1",
		Constraints:      json.RawMessage(`{"business_rules":["one coupon per order"],"preconditions":["signed in"]}`),
		SutDescription:   "The web storefront's checkout page and the pricing API behind it.",
		ScopeOut:         "Gift cards.",
		AccountRolesHint: []string{"shopper"},
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal canonical payload: %v", err)
	}
	sum := sha256.Sum256(raw)
	if want := hex.EncodeToString(sum[:]); storedHash != want {
		t.Fatalf("stored revision hash = %q, want the canonical contract hash %q -- a stored hash must not move", storedHash, want)
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

	// Reordering the criteria cannot change the hash: the payload sorts by id.
	reordered := validRequirementVersion()
	reordered.AcceptanceCriteria = []version.AcceptanceCriterion{
		base.AcceptanceCriteria[1], base.AcceptanceCriteria[0],
	}
	reordered.Constraints = json.RawMessage(`{"preconditions":["signed in"],"business_rules":["one coupon per order"]}`)
	if got, err := reordered.ComputeContentHash(); err != nil || got != hash {
		t.Fatalf("reordered criteria / reordered constraints changed the hash: %q vs %q (%v)", hash, got, err)
	}

	// Whitespace inside given/when/then is normalized away.
	rewrapped := validRequirementVersion()
	rewrapped.AcceptanceCriteria[0].Given = "  a signed-in shopper\n\twith a   valid coupon "
	if got, err := rewrapped.ComputeContentHash(); err != nil || got != hash {
		t.Fatalf("whitespace-only difference changed the hash: %q vs %q (%v)", hash, got, err)
	}

	// Every hashed field must actually reach the payload.
	changed := []struct {
		name   string
		mutate func(*version.RequirementVersion)
	}{
		{"title", func(v *version.RequirementVersion) { v.Title = "Checkout supports gift cards" }},
		{"user_story.as_a", func(v *version.RequirementVersion) { v.UserStory.AsA = "a guest shopper" }},
		{"user_story.i_want", func(v *version.RequirementVersion) { v.UserStory.IWant = "to pay in full" }},
		{"user_story.so_that", func(v *version.RequirementVersion) { v.UserStory.SoThat = "I keep my receipts" }},
		{"rules", func(v *version.RequirementVersion) { v.Rules = "Two coupons per order." }},
		{"acceptance given", func(v *version.RequirementVersion) { v.AcceptanceCriteria[0].Given = "an anonymous shopper" }},
		{"acceptance when", func(v *version.RequirementVersion) { v.AcceptanceCriteria[0].When = "the coupon is ignored" }},
		{"acceptance then", func(v *version.RequirementVersion) { v.AcceptanceCriteria[0].Then = "nothing changes" }},
		{"acceptance id", func(v *version.RequirementVersion) { v.AcceptanceCriteria[0].ID = "AC-7" }},
		{"priority", func(v *version.RequirementVersion) { v.Priority = "P3" }},
		{"constraints", func(v *version.RequirementVersion) {
			v.Constraints = json.RawMessage(`{"business_rules":["two coupons per order"]}`)
		}},
		{"sut_description", func(v *version.RequirementVersion) { v.SutDescription = "The mobile app's checkout." }},
	}
	for _, tc := range changed {
		t.Run(tc.name, func(t *testing.T) {
			mutated := validRequirementVersion()
			tc.mutate(&mutated)
			got, err := mutated.ComputeContentHash()
			if err != nil {
				t.Fatalf("ContentHash: %v", err)
			}
			if got == hash {
				t.Fatalf("changing %s did not change the hash", tc.name)
			}
		})
	}

	// scope_out and account_roles_hint ARE part of the canonical payload, after
	// the seven fields §3.6 lists: constitution 四.6 requires every input a
	// conclusion depends on to be inside the fingerprint, the §5.3 judge rubric
	// reads scope_out for its over-coverage check, and account_roles_hint feeds
	// execution account resolution. If either could change without moving the
	// hash, confirmed cases would not go stale and the judge/executor would act
	// on content the confirmation freeze never captured.
	scopeChanged := validRequirementVersion()
	scopeChanged.ScopeOut = "Gift cards, loyalty points and refunds."
	scopeHash, err := scopeChanged.ComputeContentHash()
	if err != nil {
		t.Fatalf("ComputeContentHash: %v", err)
	}
	if scopeHash == hash {
		t.Fatal("changing only scope_out did not change the hash")
	}

	rolesChanged := validRequirementVersion()
	rolesChanged.AccountRolesHint = []string{"shopper", "support"}
	rolesHash, err := rolesChanged.ComputeContentHash()
	if err != nil {
		t.Fatalf("ComputeContentHash: %v", err)
	}
	if rolesHash == hash {
		t.Fatal("changing only account_roles_hint did not change the hash")
	}
	if rolesHash == scopeHash {
		t.Fatal("scope_out and account_roles_hint hashed to the same value; the payload fields are colliding")
	}
}

// TestContentHashCanonicalizesScopeOutAndRoles pins the normalization of the two
// appended payload fields: scope_out is trimmed, and account_roles_hint is
// sorted-unique with nil treated as the empty array -- the same convention the
// payload already uses for arrays, so the order a caller supplies and the
// nil-vs-empty distinction are both irrelevant to the hash.
func TestContentHashCanonicalizesScopeOutAndRoles(t *testing.T) {
	base := validRequirementVersion()
	base.ScopeOut = "Gift cards."
	base.AccountRolesHint = []string{"support", "shopper"}
	want, err := base.ComputeContentHash()
	if err != nil {
		t.Fatalf("ComputeContentHash: %v", err)
	}

	sameHash := func(t *testing.T, name string, mutate func(*version.RequirementVersion)) {
		t.Helper()
		variant := validRequirementVersion()
		variant.ScopeOut = "Gift cards."
		variant.AccountRolesHint = []string{"support", "shopper"}
		mutate(&variant)
		got, err := variant.ComputeContentHash()
		if err != nil {
			t.Fatalf("%s: ComputeContentHash: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s changed the hash: %q vs %q", name, got, want)
		}
	}

	// Reordering the hints is not a content change.
	sameHash(t, "reordered account_roles_hint", func(v *version.RequirementVersion) {
		v.AccountRolesHint = []string{"shopper", "support"}
	})
	// Duplicates collapse, so a repeated role is not a content change either.
	sameHash(t, "duplicated account_roles_hint", func(v *version.RequirementVersion) {
		v.AccountRolesHint = []string{"support", "shopper", "support"}
	})
	// Surrounding whitespace is trimmed on both fields.
	sameHash(t, "untrimmed scope_out and roles", func(v *version.RequirementVersion) {
		v.ScopeOut = "  Gift cards.  "
		v.AccountRolesHint = []string{" support ", "\tshopper\n"}
	})
	// Blank roles are dropped, so a list of blanks equals no list at all.
	sameHash(t, "blank account_roles_hint entries", func(v *version.RequirementVersion) {
		v.AccountRolesHint = []string{"support", "  ", "shopper", ""}
	})

	// nil and [] must hash identically: both are "no roles", and the payload
	// marshals an empty array rather than null in both cases.
	nilRoles := validRequirementVersion()
	nilRoles.ScopeOut = ""
	nilRoles.AccountRolesHint = nil
	emptyRoles := validRequirementVersion()
	emptyRoles.ScopeOut = ""
	emptyRoles.AccountRolesHint = []string{}
	nilHash, err := nilRoles.ComputeContentHash()
	if err != nil {
		t.Fatalf("ComputeContentHash(nil): %v", err)
	}
	emptyHash, err := emptyRoles.ComputeContentHash()
	if err != nil {
		t.Fatalf("ComputeContentHash(empty): %v", err)
	}
	if nilHash != emptyHash {
		t.Fatalf("nil account_roles_hint = %q but [] = %q, want identical", nilHash, emptyHash)
	}
	// And an empty scope_out is the trimmed-empty string, not an omission.
	blankScope := validRequirementVersion()
	blankScope.ScopeOut = "   "
	blankScope.AccountRolesHint = nil
	if got, err := blankScope.ComputeContentHash(); err != nil || got != nilHash {
		t.Fatalf("blank scope_out = %q, want the empty-scope_out hash %q (%v)", got, nilHash, err)
	}
}

func TestACIDsProjectsCanonicalOrder(t *testing.T) {
	criteria := []version.AcceptanceCriterion{
		{ID: "AC-10", Given: "g", When: "w", Then: "t"},
		{ID: "AC-2", Given: "g", When: "w", Then: "t2"},
		{ID: "AC-1", Given: "g", When: "w", Then: "t3"},
	}
	got := version.ACIDs(criteria)
	want := []string{"AC-1", "AC-2", "AC-10"}
	if len(got) != len(want) {
		t.Fatalf("ACIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ACIDs = %v, want %v (numeric order, not lexicographic)", got, want)
		}
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
