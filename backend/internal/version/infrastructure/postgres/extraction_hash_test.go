package versionpg

import (
	"encoding/json"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestExtractionRevisionUsesTheDomainHash proves the extraction pipeline has no
// hash implementation of its own (constitution 五.1/五.3): the ContentHash it
// stores is byte-for-byte what version.RequirementVersion.ComputeContentHash
// returns for the same field population, including the v0.2.4-B mapping of the
// extracted prose description onto rules and of the flat statements onto
// structured criteria.
func TestExtractionRevisionUsesTheDomainHash(t *testing.T) {
	extracted := versionapp.ExtractionRequirement{
		ExternalID:         "FR-001",
		Title:              " 手机号登录 ",
		Description:        "  用户可以用手机号加验证码登录  ",
		AcceptanceCriteria: []string{"验证码 60 秒内有效", "验证码只能使用一次"},
		Priority:           "P1",
		ConstraintsJSON:    json.RawMessage(`{"business_rules":["one session per phone"]}`),
	}
	criteria := version.AssignACIDs(nil, extractionAcceptanceCriteria(nil, extracted.AcceptanceCriteria))

	got, err := extractionRevision(extracted, criteria)
	if err != nil {
		t.Fatalf("extractionRevision: %v", err)
	}

	// The same payload assembled by hand through the domain type, with no
	// reference to the extraction helper.
	want, err := version.RequirementVersion{
		Title:    "手机号登录",
		Rules:    "用户可以用手机号加验证码登录",
		Priority: "P1",
		AcceptanceCriteria: []version.AcceptanceCriterion{
			{ID: "AC-1", Then: "验证码 60 秒内有效"},
			{ID: "AC-2", Then: "验证码只能使用一次"},
		},
		Constraints: json.RawMessage(`{"business_rules":["one session per phone"]}`),
	}.ComputeContentHash()
	if err != nil {
		t.Fatalf("ComputeContentHash: %v", err)
	}

	if got.ContentHash != want {
		t.Fatalf("extraction hash = %q, want the domain hash %q", got.ContentHash, want)
	}
	if got.Title != "手机号登录" || got.Rules != "用户可以用手机号加验证码登录" {
		t.Fatalf("extraction revision = %#v: title/rules were not normalized onto the §5.1 fields", got)
	}
	if len(got.AcceptanceCriteria) != 2 || got.AcceptanceCriteria[0].ID != "AC-1" || got.AcceptanceCriteria[1].ID != "AC-2" {
		t.Fatalf("extraction criteria = %#v, want AC-1/AC-2 assigned from AC-1", got.AcceptanceCriteria)
	}
}

// TestExtractionRevisionReEmitKeepsIDsAndHash proves the idempotence the upsert
// relies on: re-emitting the same statements against the revision already stored
// inherits their ids, so the hash is unchanged and no new revision is written.
func TestExtractionRevisionReEmitKeepsIDsAndHash(t *testing.T) {
	statements := []string{"验证码 60 秒内有效", "验证码只能使用一次"}
	first := version.AssignACIDs(nil, extractionAcceptanceCriteria(nil, statements))
	firstRevision, err := extractionRevision(versionapp.ExtractionRequirement{
		Title: "手机号登录", Description: "登录", Priority: "P1",
	}, first)
	if err != nil {
		t.Fatalf("first revision: %v", err)
	}

	// The stored criteria come back out of the JSONB column and are flattened for
	// the merge policy, then rebuilt and re-assigned -- exactly the upsert path.
	roundTripped := flattenExtractionAcceptanceCriteria(first)
	if len(roundTripped) != 2 || roundTripped[0] != statements[0] || roundTripped[1] != statements[1] {
		t.Fatalf("flatten = %#v, want the original statements %v", roundTripped, statements)
	}
	second := version.AssignACIDs(first, extractionAcceptanceCriteria(first, roundTripped))
	secondRevision, err := extractionRevision(versionapp.ExtractionRequirement{
		Title: "手机号登录", Description: "登录", Priority: "P1",
	}, second)
	if err != nil {
		t.Fatalf("second revision: %v", err)
	}

	if secondRevision.ContentHash != firstRevision.ContentHash {
		t.Fatalf("re-emit changed the hash: %q vs %q", secondRevision.ContentHash, firstRevision.ContentHash)
	}
	if acIDList(second) != acIDList(first) {
		t.Fatalf("re-emit changed the ids: %q vs %q", acIDList(second), acIDList(first))
	}
}

// TestExtractionAcceptanceCriteriaPreservesClarifiedGherkin proves an extraction
// re-emit over a draft that clarification already completed does not flatten the
// Given/When away: the stored criterion is reused whole.
func TestExtractionAcceptanceCriteriaPreservesClarifiedGherkin(t *testing.T) {
	previous := []version.AcceptanceCriterion{
		{ID: "AC-1", Given: "已注册的手机号", When: "提交验证码", Then: "登录成功"},
	}
	statements := flattenExtractionAcceptanceCriteria(previous)
	if len(statements) != 1 || statements[0] != "Given: 已注册的手机号 | When: 提交验证码 | Then: 登录成功" {
		t.Fatalf("flatten = %#v, want the three-part rendering", statements)
	}

	rebuilt := extractionAcceptanceCriteria(previous, statements)
	if len(rebuilt) != 1 {
		t.Fatalf("rebuilt = %#v, want one criterion", rebuilt)
	}
	if rebuilt[0].Given != previous[0].Given || rebuilt[0].When != previous[0].When || rebuilt[0].Then != previous[0].Then {
		t.Fatalf("rebuilt = %#v, want the stored Given/When/Then preserved", rebuilt[0])
	}

	// A genuinely new statement still becomes a Then-only criterion.
	appended := extractionAcceptanceCriteria(previous, append(statements, "验证码只能使用一次"))
	if len(appended) != 2 {
		t.Fatalf("appended = %#v, want two criteria", appended)
	}
	if appended[1].Given != "" || appended[1].When != "" || appended[1].Then != "验证码只能使用一次" {
		t.Fatalf("appended[1] = %#v, want a Then-only criterion", appended[1])
	}
}

func acIDList(criteria []version.AcceptanceCriterion) string {
	out, err := json.Marshal(version.ACIDs(criteria))
	if err != nil {
		return ""
	}
	return string(out)
}
