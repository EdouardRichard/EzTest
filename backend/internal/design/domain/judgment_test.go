package design

import "testing"

func TestDeriveReviewStatePriority(t *testing.T) {
	j := &Judgment{CaseVersionID: 5, Verdict: VerdictFail}
	// judge_exhausted 优先于 pending
	if got := DeriveReviewState(nil, true, 5, false); got != ReviewStateJudgeUnavailable {
		t.Fatalf("exhausted priority = %q", got)
	}
	if got := DeriveReviewState(j, false, 5, true); got != ReviewStateEscalatedFail {
		t.Fatalf("fail exhausted = %q", got)
	}
	if got := DeriveReviewState(j, false, 5, false); got != ReviewStateReworking {
		t.Fatalf("fail reworking = %q", got)
	}
	j.Verdict = VerdictPass
	if got := DeriveReviewState(j, false, 5, false); got != ReviewStatePass {
		t.Fatalf("pass = %q", got)
	}
	// 分支序：pass 先于 reworkExhausted 判定——打回额度已耗尽但当前版本已判 pass
	// （例如 Host 编辑后新一代裁判通过）必须是 pass，不得回落 escalated_fail。
	if got := DeriveReviewState(j, false, 5, true); got != ReviewStatePass {
		t.Fatalf("pass with rework budget spent = %q, want pass (pass beats escalated_fail)", got)
	}
	// judge_exhausted 仍优先于 pass（同版本升级事实在案）。
	if got := DeriveReviewState(j, true, 5, true); got != ReviewStateJudgeUnavailable {
		t.Fatalf("judge_exhausted over pass = %q, want judge_unavailable", got)
	}
	// 版本不匹配 → pending（无 exhausted）
	if got := DeriveReviewState(j, false, 9, false); got != ReviewStatePending {
		t.Fatalf("version mismatch = %q", got)
	}
	if got := DeriveReviewState(nil, false, 5, false); got != ReviewStatePending {
		t.Fatalf("no fact = %q", got)
	}
}

func TestReworkAllowedBoundary(t *testing.T) {
	if !ReworkAllowed(0, 1) {
		t.Fatal("count 0 < 1 must allow")
	}
	if ReworkAllowed(1, 1) {
		t.Fatal("count 1 < 1 must deny")
	}
}

func TestValidJudgmentEnums(t *testing.T) {
	for _, verdict := range []string{VerdictPass, VerdictFail} {
		if !ValidVerdict(verdict) {
			t.Fatalf("verdict %q must be valid", verdict)
		}
	}
	for _, invalid := range []string{"", "passed", "PASS", "inconclusive"} {
		if ValidVerdict(invalid) {
			t.Fatalf("verdict %q must be invalid", invalid)
		}
	}
	for _, severity := range []string{SeverityBlocker, SeverityImportant, SeverityMinor} {
		if !ValidSeverity(severity) {
			t.Fatalf("severity %q must be valid", severity)
		}
	}
	for _, invalid := range []string{"", "critical", "BLOCKER"} {
		if ValidSeverity(invalid) {
			t.Fatalf("severity %q must be invalid", invalid)
		}
	}
	for _, state := range []ReviewState{
		ReviewStatePending, ReviewStatePass, ReviewStateReworking,
		ReviewStateEscalatedFail, ReviewStateJudgeUnavailable,
	} {
		if !ValidReviewState(state) {
			t.Fatalf("review state %q must be valid", state)
		}
	}
	for _, invalid := range []ReviewState{"", "failed", "PENDING"} {
		if ValidReviewState(invalid) {
			t.Fatalf("review state %q must be invalid", invalid)
		}
	}
}
