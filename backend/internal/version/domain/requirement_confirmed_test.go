package version_test

import (
	"testing"
)

// validRequirementVersion() is the shared legal, non-placeholder fixture
// already defined in requirement_test.go (same version_test package); these
// tests reuse it rather than redeclaring a second copy (single source of
// truth, AGENTS.md §2.2).

func TestValidateConfirmedPlaceholderWholeValue(t *testing.T) {
	v := validRequirementVersion() // helper: 所有字段合法非占位
	v.SutDescription = "这是一个足够长的被测系统自然语言描述用于通过最小长度"
	terms := []string{"TBD", "待定"}
	if err := v.ValidateConfirmed(terms, 20); err != nil {
		t.Fatalf("valid content rejected: %v", err)
	}
	v.SutDescription = "待定" // 整值占位
	if err := v.ValidateConfirmed(terms, 20); err == nil {
		t.Fatal("whole-value placeholder accepted")
	}
	v.SutDescription = "这个描述里包含待定两个字但是完整叙述不应被误伤足够长"
	if err := v.ValidateConfirmed(terms, 20); err != nil {
		t.Fatalf("substring false positive: %v", err)
	}
}

func TestValidateConfirmedMinLength(t *testing.T) {
	v := validRequirementVersion()
	v.SutDescription = "短描述" // <20
	if err := v.ValidateConfirmed(nil, 20); err == nil {
		t.Fatal("short sut_description accepted")
	}
	v.SutDescription = "这是一个恰好超过二十个字符的被测系统描述文本"
	if err := v.ValidateConfirmed(nil, 20); err != nil {
		t.Fatalf(">=20 rejected: %v", err)
	}
}

func TestValidateDraftUnaffected(t *testing.T) {
	v := validRequirementVersion()
	v.SutDescription = "待定"
	if err := v.ValidateDraft(); err != nil {
		t.Fatalf("draft must stay lax: %v", err)
	}
}

// TestValidateConfirmedPlaceholderACPart pins the 400 status and field path of
// a placeholder inside an acceptance-criterion part (T3 review carry: the
// placeholder rejection previously only covered sut_description).
func TestValidateConfirmedPlaceholderACPart(t *testing.T) {
	terms := []string{"TBD", "待定"}
	v := validRequirementVersion()
	v.AcceptanceCriteria[0].Given = "TBD"
	err := v.ValidateConfirmed(terms, 20)
	if err == nil {
		t.Fatal("placeholder acceptance-criterion given accepted")
	}
	if field := requirementAPIField(t, err); field != "acceptance_criteria[0].given" {
		t.Fatalf("field = %q, want acceptance_criteria[0].given", field)
	}
	v = validRequirementVersion()
	v.AcceptanceCriteria[1].Then = "待定"
	err = v.ValidateConfirmed(terms, 20)
	if err == nil {
		t.Fatal("placeholder acceptance-criterion then accepted")
	}
	if field := requirementAPIField(t, err); field != "acceptance_criteria[1].then" {
		t.Fatalf("field = %q, want acceptance_criteria[1].then", field)
	}
}
