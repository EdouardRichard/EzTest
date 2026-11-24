package agentapp_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// promptIncrementSurfaces resolves one profile seed prompt and one role prompt
// so every T15 increment is pinned on BOTH twins (profiles.go seed + roles.go
// catalog role): they reach the same model on the same job, so a one-sided
// increment means the model is told two different things (the drift
// TestDesignRolePromptsAgreeWithTheDesignProfilePrompts already guards for the
// B contract terms).
func promptIncrementSurfaces(t *testing.T, profileKey string, roleKey agentapp.RoleKey) map[string]string {
	t.Helper()
	profile, ok := agentapp.Profile(profileKey)
	if !ok {
		t.Fatalf("profile %q missing", profileKey)
	}
	role, ok := agentapp.Role(roleKey)
	if !ok {
		t.Fatalf("role %q missing", roleKey)
	}
	return map[string]string{
		"profile " + profileKey:   profile.SystemPrompt,
		"role " + string(roleKey): role.SystemPrompt,
	}
}

func requirePromptTerms(t *testing.T, surfaces map[string]string, terms []string) {
	t.Helper()
	for surface, prompt := range surfaces {
		for _, term := range terms {
			if !strings.Contains(prompt, term) {
				t.Fatalf("%s prompt lacks %q:\n%s", surface, term, prompt)
			}
		}
	}
}

// TestDesignAuthorPromptHasCodeObligation pins the GD-03 code-awareness
// obligation (design v1.3.1 §5.2, E spec SC-E9): when the version environment
// has an indexed code repository the author MUST query code before designing,
// and code-derived cases must carry a kind=code source with anchor validation.
// Both author twins (design_author seed + case_author role) must teach it.
func TestDesignAuthorPromptHasCodeObligation(t *testing.T) {
	surfaces := promptIncrementSurfaces(t, agentapp.ProfileKeyDesignAuthor, agentapp.RoleCaseAuthor)
	requirePromptTerms(t, surfaces, []string{
		// The obligation trigger: an indexed repository in the version environment.
		"已索引", "设计前",
		// The concrete read-only code tools the author actually has (P5 increment).
		"code.facts", "code.routes", "code.apis",
		// The provenance obligation: kind=code source + anchor validation.
		"kind=code", "锚点", "code.anchors",
		// No-repo fallback stays requirement-only; no fabricated code facts.
		"不编造代码事实",
	})
}

// TestDesignAuthorPromptJudgeAwareness pins the judge-awareness increment
// (SC-E9): the author knows an independent judge reviews coverage /
// executability / related systems per case and that blocker/important findings
// send the case back for rework.
func TestDesignAuthorPromptJudgeAwareness(t *testing.T) {
	surfaces := promptIncrementSurfaces(t, agentapp.ProfileKeyDesignAuthor, agentapp.RoleCaseAuthor)
	requirePromptTerms(t, surfaces, []string{
		"裁判", "独立", "覆盖率", "可执行", "关联系统",
		"blocker", "important", "打回",
	})
}

// TestDesignAuthorPromptGherkinMapping pins the I-B7 mapping semantics
// (design v1.3.1 §5.2, review-response I-B7): preconditions establish the AC
// given premises; step expected = step-level observation while assertions =
// assertion-level Then (no redundancy); the scenario's terminal Then is carried
// by the last step's assertions.
func TestDesignAuthorPromptGherkinMapping(t *testing.T) {
	surfaces := promptIncrementSurfaces(t, agentapp.ProfileKeyDesignAuthor, agentapp.RoleCaseAuthor)
	requirePromptTerms(t, surfaces, []string{
		"preconditions", "given",
		"步骤级观察", "断言级 Then",
		"step_seq", "末步",
	})
}

// TestClarificationPromptGateAwareness pins the confirmation-gate awareness
// increment (SC-E9, T1/T11 gate): the clarification Host knows the gate checks
// whole-value placeholder terms and a minimum sut_description length, so it
// drafts concrete content instead of learning the rule at confirm-rejection.
// The gate semantics taught must match version/domain ValidateConfirmed:
// whole-value exact match (substring containment does not trip it) and the
// length floor applies to sut_description only.
func TestClarificationPromptGateAwareness(t *testing.T) {
	profile, ok := agentapp.Profile(agentapp.ProfileKeyClarificationHost)
	if !ok {
		t.Fatal("clarification_host profile missing")
	}
	for _, term := range []string{
		"确认门禁", "占位", "整值", "最小长度", "sut_description",
	} {
		if !strings.Contains(profile.SystemPrompt, term) {
			t.Fatalf("clarification prompt lacks gate awareness %q:\n%s", term, profile.SystemPrompt)
		}
	}
	// The Host boundary is unchanged: it still must not confirm requirements.
	if !strings.Contains(profile.SystemPrompt, "确认只能由用户完成") {
		t.Fatalf("clarification prompt lost the user-only confirmation boundary:\n%s", profile.SystemPrompt)
	}
}

// TestDesignCriticPromptJudgeAwareness pins the critic-side increment (SC-E9):
// the internal self-audit complements but does not replace the independent
// judge gate. Both critic twins (design_critic seed + case_reviewer role) must
// teach it, and the existing "internal audit is not a human gate" boundary
// stays.
func TestDesignCriticPromptJudgeAwareness(t *testing.T) {
	surfaces := promptIncrementSurfaces(t, agentapp.ProfileKeyDesignCritic, agentapp.RoleCaseReviewer)
	requirePromptTerms(t, surfaces, []string{
		"裁判", "独立", "覆盖率", "可执行", "关联系统",
		"blocker", "important", "打回", "不替代",
	})
	for surface, prompt := range surfaces {
		if !strings.Contains(prompt, "不构成人工关卡") {
			t.Fatalf("%s prompt lost the not-a-human-gate boundary:\n%s", surface, prompt)
		}
	}
}

// TestJudgePromptRubric pins the T12-owned designJudgePrompt (T15 must NOT
// redefine it): the three rubric dimensions, the per-case verdict schema and
// the no-confirm boundary. Complements T12's
// TestDesignJudgePromptTeachesRubricAndBoundary with the SC-E9 acceptance
// wording (覆盖 / 可执行 / 关联系统 / 不确认).
func TestJudgePromptRubric(t *testing.T) {
	profile, ok := agentapp.Profile(agentapp.ProfileKeyDesignJudge)
	if !ok {
		t.Fatal("design_judge profile missing")
	}
	for _, dim := range []string{"覆盖", "可执行", "关联系统"} {
		if !strings.Contains(profile.SystemPrompt, dim) {
			t.Fatalf("judge prompt missing rubric dimension %q:\n%s", dim, profile.SystemPrompt)
		}
	}
	for _, term := range []string{
		"design.judgment_submit", "pass|fail", "findings",
		"dimension", "severity", "blocker", "important", "minor",
	} {
		if !strings.Contains(profile.SystemPrompt, term) {
			t.Fatalf("judge prompt missing verdict schema term %q:\n%s", term, profile.SystemPrompt)
		}
	}
	if !strings.Contains(profile.SystemPrompt, "不确认") {
		t.Fatalf("judge prompt must forbid confirming:\n%s", profile.SystemPrompt)
	}
}

// TestPromptSeedUntouched pins the byte-frozen historical fixtures (B warning:
// prompt_seed.go / prompt_seed_legacy_test.go are never modified). The pin is
// the sha256 of each file with CR stripped (the working copy is CRLF; content
// identity is what is frozen, not the line-ending style). Any edit to either
// fixture fails here even if the legacy self-consistency test still passes.
func TestPromptSeedUntouched(t *testing.T) {
	pins := map[string]string{
		"prompt_seed.go":             "1aea0d0db66f167d5b24840ea3cfe71ad0f41207f77de67e8b6cb24c8e7053cd",
		"prompt_seed_legacy_test.go": "0412cdfb7ac8447f7f2343e120eb3aa796c751585db7ce47aaa22a270e79a479",
	}
	for name, want := range pins {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read frozen fixture %s: %v", name, err)
		}
		sum := sha256.Sum256([]byte(strings.ReplaceAll(string(raw), "\r", "")))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("%s changed (CR-stripped sha256 %s, want %s) -- the historical fixture is byte-frozen and must never be edited", name, got, want)
		}
	}
}
