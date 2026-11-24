package tools

import (
	"encoding/json"
	"reflect"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestRequirementRoleHintUnifiesWithDomainNormalizer closes U1f (v0.2.4-C T22,
// R-C6): the tool boundary and the version domain must produce the SAME result
// for the same account_roles_hint input. The domain normalizer
// (version.NormalizeAccountRolesHint) trims, drops empties and de-duplicates;
// the tool used to reject an empty-after-trim entry with 400 while the REST
// side silently dropped it — one input, two results (constitution art.5.1).
func TestRequirementRoleHintUnifiesWithDomainNormalizer(t *testing.T) {
	inputs := [][]string{
		{"admin", "  "},
		{"admin", "admin", "  "},
		{" 普通用户 ", "管理员", "普通用户"},
		{"  ", "\t"},
		nil,
	}
	for _, input := range inputs {
		domain := version.NormalizeAccountRolesHint(input)
		draft, toolErr := buildDraft(draftFields{
			Title:              "t",
			UserStory:          userStoryFields{AsA: "用户", IWant: "用手机号登录", SoThat: "不必记密码"},
			AcceptanceCriteria: []acceptanceCriterionFields{{Given: "g", When: "w", Then: "t"}},
			SutDescription:     "登录页",
			AccountRolesHint:   input,
		})
		if toolErr != nil {
			t.Fatalf("input %#v: tool rejected what the domain normalizer accepts: %v", input, toolErr)
		}
		got := draft.AccountRolesHint
		if len(got) == 0 && len(domain) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, domain) {
			t.Fatalf("input %#v: tool roles %#v != domain roles %#v", input, got, domain)
		}
	}
}

// TestRequirementRoleHintStillRejectsOverlongRole keeps the structural bound
// the domain also enforces (ValidateDraft: each role 1..RequirementAccountRoleMax):
// unification drops empties, it does not relax the length contract.
func TestRequirementRoleHintStillRejectsOverlongRole(t *testing.T) {
	long := make([]byte, version.RequirementAccountRoleMax+1)
	for i := range long {
		long[i] = 'a'
	}
	_, toolErr := buildDraft(draftFields{
		Title:              "t",
		UserStory:          userStoryFields{AsA: "a", IWant: "b", SoThat: "c"},
		AcceptanceCriteria: []acceptanceCriterionFields{{Given: "g", When: "w", Then: "t"}},
		SutDescription:     "s",
		AccountRolesHint:   []string{string(long)},
	})
	if toolErr == nil || toolErr.Code != CodeInvalidArgument {
		t.Fatalf("overlong role must stay invalid_argument, got %v", toolErr)
	}
}

// TestRequirementBoundsDeriveFromVersionAuthority closes U1g (T22): the tool
// boundary constants are DERIVED from the version domain authority, not a
// hand-copied second truth (constitution art.5.3). The pin compares values so a
// future domain change that the tool does not follow fails here.
func TestRequirementBoundsDeriveFromVersionAuthority(t *testing.T) {
	pins := []struct {
		name      string
		tool      int
		authority int
	}{
		{"title", maxRequirementTitle, version.RequirementTitleMax},
		{"user_story part", maxRequirementUserStoryPart, version.RequirementUserStoryPartMax},
		{"rules", maxRequirementRules, version.RequirementRulesMax},
		{"ac part", maxRequirementACPart, version.AcceptanceCriterionPartMax},
		{"ac count", maxRequirementACs, version.MaxAcceptanceCriteria},
		{"sut_description", maxRequirementSutDescription, version.RequirementSutDescriptionMax},
		{"scope_out", maxRequirementScopeOut, version.RequirementScopeOutMax},
		{"account role", maxRequirementAccountRole, version.RequirementAccountRoleMax},
		{"reopen reason", maxReopenReason, version.RequirementReasonMax},
	}
	for _, pin := range pins {
		if pin.tool != pin.authority {
			t.Fatalf("%s bound: tool %d != version authority %d (derive, do not copy)", pin.name, pin.tool, pin.authority)
		}
	}
	if defaultRequirementPriority != version.DefaultPriority {
		t.Fatalf("default priority %q != version.DefaultPriority %q", defaultRequirementPriority, version.DefaultPriority)
	}
}

// TestRequirementDetailACIDsCanonicalOrder closes U1b (T22): ac_ids (and the
// structured criteria array) are emitted in the canonical id-sequence order
// (version.SortAcceptanceCriteria, design §3.6 "按 id 排序"), not storage order.
// AC-2 must sort before AC-10 even when the port returns them unsorted.
func TestRequirementDetailACIDsCanonicalOrder(t *testing.T) {
	detail := RequirementDetail{
		RequirementSummary: RequirementSummary{ID: 501, Status: "clarified"},
		AcceptanceCriteria: []RequirementAcceptanceCriterion{
			{ID: "AC-10", Given: "g10", When: "w10", Then: "t10"},
			{ID: "AC-2", Given: "g2", When: "w2", Then: "t2"},
			{ID: "AC-1", Given: "g1", When: "w1", Then: "t1"},
		},
	}
	result := requirementDetailResult(detail)
	ids, ok := result["ac_ids"].([]string)
	if !ok {
		t.Fatalf("ac_ids type %T, want []string", result["ac_ids"])
	}
	want := []string{"AC-1", "AC-2", "AC-10"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ac_ids = %#v, want canonical id order %#v", ids, want)
	}
	criteria, ok := result["acceptance_criteria"].([]map[string]any)
	if !ok || len(criteria) != 3 {
		t.Fatalf("acceptance_criteria = %#v", result["acceptance_criteria"])
	}
	for i, id := range want {
		if criteria[i]["id"] != id {
			t.Fatalf("acceptance_criteria[%d].id = %v, want %s (same canonical order as ac_ids)", i, criteria[i]["id"], id)
		}
	}
	// The order matches the domain authority exactly (single source).
	domainCriteria := make([]version.AcceptanceCriterion, 0, len(detail.AcceptanceCriteria))
	for _, c := range detail.AcceptanceCriteria {
		domainCriteria = append(domainCriteria, version.AcceptanceCriterion{ID: c.ID, Given: c.Given, When: c.When, Then: c.Then})
	}
	if !reflect.DeepEqual(ids, version.ACIDs(domainCriteria)) {
		t.Fatalf("ac_ids diverge from version.ACIDs: %#v vs %#v", ids, version.ACIDs(domainCriteria))
	}
}

// TestRequirementReadRoleHintNormalized pins the read face to the same
// normalizer: a stored hint with blanks/duplicates reads back normalized.
func TestRequirementReadRoleHintNormalized(t *testing.T) {
	stored := []string{" admin ", "  ", "admin", "viewer"}
	detail := RequirementDetail{
		RequirementSummary: RequirementSummary{ID: 501, Status: "clarified"},
		AccountRolesHint:   stored,
	}
	result := requirementDetailResult(detail)
	roles, ok := result["account_roles_hint"].([]string)
	if !ok {
		t.Fatalf("account_roles_hint type %T", result["account_roles_hint"])
	}
	want := version.NormalizeAccountRolesHint(stored)
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("read roles %#v, want %#v", roles, want)
	}
}

// TestRequirementDraftCreateAcceptsBlankRoleHint is the tool-surface regression
// for U1f: ["admin","  "] used to be a 400 at the tool boundary while the REST
// side dropped the blank and persisted ["admin"]. Both faces now agree.
func TestRequirementDraftCreateAcceptsBlankRoleHint(t *testing.T) {
	port := &fakeRequirementPort{created: RequirementSummary{ID: 501, CurrentVersionID: 700, Revision: 1, Status: "draft"}}
	registry := NewBaseRegistry(Deps{Requirement: port})
	_, err := execute(t, registry, Scope{VersionID: 10}, ToolRequirementDraftCreate, `{
		"title":"t",
		"user_story":{"as_a":"用户","i_want":"用手机号登录","so_that":"不必记密码"},
		"acceptance_criteria":[{"given":"g","when":"w","then":"t"}],
		"sut_description":"登录页",
		"account_roles_hint":["admin","  "]
	}`)
	if err != nil {
		t.Fatalf("blank role hint must be dropped like the domain normalizer, got %v", err)
	}
	if !reflect.DeepEqual(port.createIn.AccountRolesHint, []string{"admin"}) {
		t.Fatalf("persisted roles = %#v, want [admin]", port.createIn.AccountRolesHint)
	}
}

// TestRequirementSchemaACMaxItemsDerives pins the model-facing schema bound to
// the same authority (the schema is surface ① of the model contract).
func TestRequirementSchemaACMaxItemsDerives(t *testing.T) {
	tool := &requirementDraftCreate{}
	var schema struct {
		Properties map[string]struct {
			MaxItems int `json:"maxItems"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("schema decode: %v", err)
	}
	if got := schema.Properties["acceptance_criteria"].MaxItems; got != version.MaxAcceptanceCriteria {
		t.Fatalf("acceptance_criteria maxItems = %d, want version.MaxAcceptanceCriteria %d", got, version.MaxAcceptanceCriteria)
	}
}
