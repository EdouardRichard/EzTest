package execution

// Task 112: the analyst-written failure summary is free text, but it must never
// contradict the deterministic issue severity. The normalizer drops severity
// change claims (the FR-106-02 trap said "severity 由 minor 上调为 major" while
// the issue record is minor) while preserving the observed behavior evidence.

import (
	"strings"
	"testing"
)

const fr10602Summary = "FR-106 定时发布能力在 SUT 上整体缺失，属应用功能未实现而非测试数据问题，重跑无法修复。" +
	"observed 证据（4 个 step 全部 failed）：编辑页没有「发布时间/预约发布」控件，无法设置未来时间 T。" +
	"说明：用例指定的读路径问题不影响上述结论；失败本体是应用行为与 AC 期望不一致。" +
	"severity 由 minor 上调为 major：定时发布为整条 P2 能力（控制面+接口+调度）完全缺失，非边缘缺陷。" +
	"另 FR-106 当前仍为 draft，建议与 PO 复核确认。"

func TestSanitizeFailureSummaryRemovesSeverityUpgradeClaim(t *testing.T) {
	got := SanitizeFailureSummary(fr10602Summary)
	if strings.Contains(got, "上调") || strings.Contains(strings.ToLower(got), "severity") {
		t.Fatalf("severity claim survived sanitization: %q", got)
	}
	for _, want := range []string{"定时发布能力在 SUT 上整体缺失", "没有「发布时间/预约发布」控件", "建议与 PO 复核确认"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized summary lost evidence %q: %q", want, got)
		}
	}
}

func TestSanitizeFailureSummaryRemovesEnglishSeverityClaim(t *testing.T) {
	got := SanitizeFailureSummary("Scheduled publish is missing. Severity was upgraded from minor to major. Observed: no publish control.")
	if strings.Contains(strings.ToLower(got), "upgraded") || strings.Contains(strings.ToLower(got), "severity") {
		t.Fatalf("English severity claim survived sanitization: %q", got)
	}
	if !strings.Contains(got, "Scheduled publish is missing.") || !strings.Contains(got, "Observed: no publish control.") {
		t.Fatalf("sanitized English summary lost evidence: %q", got)
	}
}

func TestSanitizeFailureSummaryKeepsNeutralText(t *testing.T) {
	neutral := "失败摘要：预约发布时间未被落库，publishedAt 恒为 null。severity 字段由服务端派生。"
	got := SanitizeFailureSummary(neutral)
	if got != neutral {
		t.Fatalf("neutral summary changed: %q -> %q", neutral, got)
	}
}

func TestSanitizeFailureSummaryDropsClaimOnlySummary(t *testing.T) {
	if got := SanitizeFailureSummary("severity 由 minor 上调为 major。"); got != "" {
		t.Fatalf("claim-only summary = %q, want empty", got)
	}
}
