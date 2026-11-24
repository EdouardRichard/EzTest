package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// v3RenderTime is the fixed as-of time of the v3 golden snapshot.
var v3RenderTime = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

// v3SampleSnapshot exercises every v3 section with fixed, deterministic data:
// a five-element defect card with natural-language context and a viewable
// evidence link, a non-issue failure explanation, an AC verification matrix
// (including a verified AC that also has a failed covering run), judge display
// facts and the overview conclusion. It is NOT waived (the waived content has
// its own snapshot below).
func v3SampleSnapshot() delivery.ReportSnapshot {
	sutID := int64(31)
	return delivery.ReportSnapshot{
		Kind:             delivery.ReportKindVersion,
		VersionID:        7,
		ReportID:         70,
		ReportRevision:   3,
		InputFingerprint: strings.Repeat("b", 64),
		Requirements: []delivery.RequirementSnapshot{
			{
				ID: 101, VersionID: 7, Title: "需求一", Status: "closed",
				UserStory:      delivery.UserStorySnapshot{AsA: "注册用户", IWant: "登录系统", SoThat: "访问个人主页"},
				SutDescription: "门户系统",
				UpdatedAt:      v3RenderTime,
			},
		},
		CaseRuns: []delivery.CaseRunSnapshot{
			{
				ID: 21, VersionID: 7, WaveID: 12, CaseVersionID: 201, RequirementID: 101,
				CaseTitle: "登录用例", CaseDescription: "验证用户登录流程", CasePreconditions: "用户已注册",
				SUTDescription: "门户系统", SUTID: &sutID, SUTIDs: []int64{31},
				Status: "failed", FailureClass: "application", Summary: "登录按钮无响应",
				StepCount: 2, StepFailedCount: 1, AssertionCount: 1, AssertionFailedCount: 1,
				FailedStepSeq: 2, FailedAssertionSeq: 1, Category: "ui", Severity: "major",
				Steps: []delivery.CaseStepSnapshot{
					{Seq: 1, Action: "输入", TargetDescription: "用户名输入框", Expected: "可输入"},
					{Seq: 2, Action: "点击", TargetDescription: "登录按钮", Expected: "跳转个人主页"},
				},
				Evidence: []delivery.EvidenceSnapshot{
					{ID: 41, CaseRunID: 21, SUTID: &sutID, Kind: "screenshot", SHA256: strings.Repeat("a", 64), SizeBytes: 100, ContentPath: "/api/versions/7/evidence/41/content"},
				},
				CreatedAt: v3RenderTime,
			},
			{ID: 20, VersionID: 7, WaveID: 12, CaseVersionID: 200, RequirementID: 101, CaseTitle: "登录成功用例", Status: "passed", CreatedAt: v3RenderTime},
		},
		Issues: []delivery.IssueSnapshot{
			{
				ID: 1, VersionID: 7, Title: "登录失败", Status: delivery.IssueStatusOpen, Category: "ui", Severity: "major", Source: "analysis",
				RepresentativeCaseRunID: 21,
				FailedExpected:          "跳转个人主页",
				FailedActual:            "停留在登录页",
				FailedReason:            "登录按钮点击无响应",
				CaseRunIDs:              []int64{21, 20},
				RepresentativeEvidence: []delivery.EvidenceSnapshot{
					{ID: 41, CaseRunID: 21, Kind: "screenshot", SHA256: strings.Repeat("a", 64), ContentPath: "/api/versions/7/evidence/41/content"},
				},
				CreatedAt: v3RenderTime, UpdatedAt: v3RenderTime,
			},
		},
		SUTs: []delivery.SUTSnapshot{
			{ID: 31, SUTID: 300, SUTName: "门户", EnvironmentID: 8, EnvironmentName: "staging", SUTDescription: "门户系统"},
		},
		Waves: []delivery.WaveSnapshot{
			{
				ID: 12, VersionID: 7, Status: "completed", Trigger: "manual",
				Counts:                 delivery.WaveCountsSnapshot{Total: 2, Passed: 1, Failed: 1},
				EnvironmentFingerprint: strings.Repeat("e", 64),
				CodeBaseline:           strings.Repeat("c", 40),
				EnvironmentVerifiable:  true,
				PlannedEnvironment: []delivery.WaveSUTEnvironmentSnapshot{
					{
						SUTID: 31, BaseURL: "https://portal.internal", PageIDs: []int64{501}, APIIDs: []int64{601}, DatabaseIDs: []int64{701},
						AccountRoles:        []string{"admin"},
						RepositoryBaselines: []delivery.WaveRepoBaselineSnapshot{{RepoID: 9, CommitSHA: strings.Repeat("d", 40)}},
					},
				},
				EnvironmentDivergences: []delivery.WaveEnvironmentDivergenceSnapshot{
					{
						CaseRunID: 21, CaseVersionID: 201,
						PlannedFingerprint: strings.Repeat("e", 64), ActualFingerprint: strings.Repeat("f", 64),
						PlannedCodeBaseline: strings.Repeat("c", 40), ActualCodeBaseline: strings.Repeat("c", 40),
						Differences: []string{"环境配置不同（计划 vs 实际执行）：SUT 31 计划 base_url=https://portal.internal；实际执行环境指纹不同。"},
					},
				},
			},
		},
		Coverage: delivery.CoverageSnapshot{
			RequirementsTotal: 1, RequirementsCovered: 1, CaseVersionsTotal: 2, CaseVersionsCovered: 1,
			ACVerification: delivery.ACCoverageSnapshot{
				ByRequirement: []delivery.RequirementACCoverageSnapshot{
					{
						RequirementID: 101,
						ACs: []delivery.ACVerificationSnapshot{
							{RequirementID: 101, ACID: "AC-1", Given: "用户已注册", When: "输入正确凭据并点击登录", Then: "跳转个人主页", State: delivery.ACVerificationVerified, CoveringCaseVersionIDs: []int64{200, 201}},
							{RequirementID: 101, ACID: "AC-2", Given: "用户已登录", When: "点击退出", Then: "返回登录页", State: delivery.ACVerificationUnverified, CoveringCaseVersionIDs: []int64{202}},
						},
					},
				},
				Total: 2, Verified: 1, Unverified: 1, Failed: 0, Uncovered: 0, LinkCovered: 2,
			},
		},
		FailureExplanations: []delivery.FailureExplanationSnapshot{
			{CaseRunID: 22, CaseVersionID: 202, RequirementID: 101, CaseTitle: "退出用例", Status: "blocked", FailureClass: "blocked", EffectiveFailureClass: "blocked", TerminalCause: "executor_capability", Explanation: "执行器能力受限，无法完成用例要求的操作；这不是产品缺陷。（执行器能力限制）"},
		},
		Judgments: []delivery.JudgmentSnapshot{
			{
				CaseID: 501, CaseVersionID: 201, RequirementID: 101, ReviewState: "pass", Verdict: "pass",
				Findings:         []delivery.JudgmentFindingSnapshot{{Dimension: "completeness", Severity: "minor", Message: "步骤覆盖充分", CaseRef: "AC-1"}},
				ACCoverageMatrix: map[string][]string{"AC-1": {"501"}},
			},
		},
		GeneratedAt: v3RenderTime,
	}
}

// v3WaivedSnapshot is a waived version report snapshot: closure_kind=waived,
// unverified, a waived requirement, an unverified AC and the waiver reason /
// operator / time projected from the waiver fact.
func v3WaivedSnapshot() delivery.ReportSnapshot {
	snapshot := v3SampleSnapshot()
	snapshot.ClosureKind = "waived"
	snapshot.Unverified = true
	snapshot.WaiverReason = "验收标准 AC-2 延期到下一版本验证"
	snapshot.WaiverOperator = "9001"
	snapshot.WaivedAt = v3RenderTime
	snapshot.Requirements[0].Waived = true
	// The waived requirement carries its own waiver context (final fix wave #1):
	// the disclosure names the requirement-level reason/operator/time.
	snapshot.Requirements[0].WaiverReason = "AC-2 延期到下一版本验证"
	snapshot.Requirements[0].WaiverOperator = "9001"
	snapshot.Requirements[0].WaivedAt = v3RenderTime
	snapshot.Issues = nil
	snapshot.CaseRuns[0].Status = "passed"
	// Exercise the truncation asides in the waived golden too (evidence display cap
	// + AC matrix read cap), so those branches are byte-frozen as well. Displayed
	// matches the two AC rows actually carried (fix #2: the note states displayed).
	snapshot.TruncatedSections = []string{delivery.ReportTruncationEvidence}
	snapshot.Coverage.ACVerification.Truncated = true
	snapshot.Coverage.ACVerification.TotalBeforeCap = 3
	snapshot.Coverage.ACVerification.Displayed = 2
	return snapshot
}

func renderV3(t *testing.T, snapshot delivery.ReportSnapshot, narrative string, opts delivery.SectionOptions) []byte {
	t.Helper()
	output, err := newVersionedRenderer(t, TemplateVersionV3).Render(context.Background(), snapshot, narrative, opts)
	if err != nil {
		t.Fatalf("Render v3: %v", err)
	}
	if len(output) == 0 {
		t.Fatal("Render v3 returned no HTML")
	}
	return output
}

// v3GoldenSHA256 pins the v3 template output byte-for-byte (SC-G1 I3 format
// freeze). Like v1, the hash is taken over LF-normalized bytes so the guard is
// platform independent. ANY change to the frozen v3 template or its fixed field
// order is a regression even if the visible content looks equivalent.
const v3GoldenSHA256 = "8769459a161ecb5211254424e90922a00a0f0f7d5df88ad0feb09f91ebdd89c5"

func TestTemplateV3Golden(t *testing.T) {
	output := renderV3(t, v3SampleSnapshot(), "固定叙述\n第二行", allSections())
	if !strings.Contains(string(output), `data-template-version="3"`) {
		t.Fatal("v3 output does not record template version 3")
	}
	normalized := bytes.ReplaceAll(output, []byte("\r\n"), []byte("\n"))
	sum := sha256.Sum256(normalized)
	if got := hex.EncodeToString(sum[:]); got != v3GoldenSHA256 {
		t.Fatalf("v3 golden hash changed: got %s (%d bytes)", got, len(output))
	}
}

// TestFiveElementCardFixedOrder pins the defect card's five elements in their
// fixed order (SC-G2): 关联需求 → 执行步骤 → 验收标准 → 预期结果 → 实际结果.
func TestFiveElementCardFixedOrder(t *testing.T) {
	text := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	if !strings.Contains(text, `data-issue-id="1"`) {
		t.Fatal("v3 has no defect card for issue 1")
	}
	order := []string{`data-element="requirement"`, `data-element="steps"`, `data-element="ac"`, `data-element="expected"`, `data-element="actual"`}
	prev := -1
	for _, marker := range order {
		index := strings.Index(text, marker)
		if index < 0 {
			t.Fatalf("defect card is missing fixed element %q", marker)
		}
		if index <= prev {
			t.Fatalf("defect element %q is out of fixed order (index %d, previous %d)", marker, index, prev)
		}
		prev = index
	}
	// The card carries the natural-language context and the five-element values.
	for _, want := range []string{"需求一", "注册用户", "登录用例", "验证用户登录流程", "用户已注册", "登录按钮", "跳转个人主页", "停留在登录页", "用户已注册", "输入正确凭据并点击登录"} {
		if !strings.Contains(text, want) {
			t.Fatalf("defect card is missing natural-language / five-element value %q", want)
		}
	}
}

// TestOverviewConclusionDistinguishesNoDefectVsUnverified pins C1/M2: a
// zero-defect version with blocked/inconclusive runs must NOT conclude "一切正常".
func TestOverviewConclusionDistinguishesNoDefectVsUnverified(t *testing.T) {
	clean := delivery.ReportSnapshot{
		Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: v3RenderTime,
		CaseRuns: []delivery.CaseRunSnapshot{{ID: 1, VersionID: 7, CaseVersionID: 200, Status: "passed", CreatedAt: v3RenderTime}},
		Coverage: delivery.CoverageSnapshot{ACVerification: delivery.ACCoverageSnapshot{Total: 1, Verified: 1}},
	}
	cleanText := string(renderV3(t, clean, "", allSections()))
	if !strings.Contains(cleanText, "一切正常") {
		t.Fatalf("a fully passing zero-defect version should conclude 一切正常; overview missing it")
	}

	blocked := delivery.ReportSnapshot{
		Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: v3RenderTime,
		CaseRuns: []delivery.CaseRunSnapshot{
			{ID: 1, VersionID: 7, CaseVersionID: 200, Status: "passed", CreatedAt: v3RenderTime},
			{ID: 2, VersionID: 7, CaseVersionID: 201, Status: "blocked", CreatedAt: v3RenderTime},
		},
		Coverage: delivery.CoverageSnapshot{ACVerification: delivery.ACCoverageSnapshot{Total: 1, Verified: 1}},
	}
	blockedText := string(renderV3(t, blocked, "", allSections()))
	if strings.Contains(blockedText, "一切正常") {
		t.Fatal("a zero-defect version with a blocked run must NOT conclude 一切正常")
	}
	if !strings.Contains(blockedText, "受阻") && !strings.Contains(blockedText, "未验证") {
		t.Fatal("the blocked-version conclusion must name the unverified/blocked state")
	}
}

// TestTermsHumanReadable pins M3: the template carries fixed Chinese definitions
// of the failure vocabulary so a human understands WHY a case did not pass.
func TestTermsHumanReadable(t *testing.T) {
	text := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	for _, want := range []string{
		"失败（failed）", "受阻（blocked）", "不确定（inconclusive）",
		"预期内失败（expected_failure）", "环境问题（environment）", "自动化偶发（automation）",
		"断言未通过", "证据不足", "不是产品缺陷",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("overview terms are missing human-readable definition %q", want)
		}
	}
}

// TestEvidenceViewableInReport pins C3: the card and the evidence appendix link
// the viewable content endpoint, not just a sha256 hash.
func TestEvidenceViewableInReport(t *testing.T) {
	text := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	if !strings.Contains(text, `href="/api/versions/7/evidence/41/content"`) {
		t.Fatal("v3 report does not render a viewable evidence content link")
	}
	if strings.Count(text, `href="/api/versions/7/evidence/41/content"`) < 2 {
		t.Fatal("the viewable evidence link must appear in both the defect card and the evidence appendix")
	}
}

// TestWaivedReportContent pins SC-G11: a waived report lists the unverified
// badge, the waived requirement / unverified AC range, the waiver reason and the
// operator so a human can judge the residual risk.
func TestWaivedReportContent(t *testing.T) {
	text := string(renderV3(t, v3WaivedSnapshot(), "", allSections()))
	if !strings.Contains(text, `data-section="waived"`) {
		t.Fatal("waived report has no waived section")
	}
	for _, want := range []string{
		"未验证", "验收标准 AC-2 延期到下一版本验证", "9001", "需求一", "AC-2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("waived report is missing waived content %q", want)
		}
	}
	// A non-waived report must NOT render the waived section.
	plain := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	if strings.Contains(plain, `data-section="waived"`) {
		t.Fatal("a non-waived report rendered the waived section")
	}
}

// TestJudgeDisplayRendered pins the T11 carry: the report renders the design
// judge review_state / verdict / findings (and the candidate AC matrix marked as
// candidate, not verification truth).
func TestJudgeDisplayRendered(t *testing.T) {
	text := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	if !strings.Contains(text, `data-section="judge"`) {
		t.Fatal("v3 report has no judge display section")
	}
	for _, want := range []string{"评审通过", "pass", "步骤覆盖充分", "completeness", "候选"} {
		if !strings.Contains(text, want) {
			t.Fatalf("judge display is missing %q", want)
		}
	}
	// Removing the judge facts removes the judge rows (read-side discrimination).
	empty := v3SampleSnapshot()
	empty.Judgments = nil
	emptyText := string(renderV3(t, empty, "", allSections()))
	if strings.Contains(emptyText, "步骤覆盖充分") {
		t.Fatal("judge findings rendered without any judge facts")
	}
}

// TestACFailedRunHint pins the T9 #4 carry: an AC marked verified (passed wins
// over failed) that ALSO has a failed covering run shows a failed-run hint so the
// human sees the failure even though the AC is verified.
func TestACFailedRunHint(t *testing.T) {
	text := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	if !strings.Contains(text, `data-ac-failed-hint="true"`) {
		t.Fatal("a verified AC with a failed covering run shows no failed-run hint")
	}
	if !strings.Contains(text, "201") {
		t.Fatal("the failed-run hint must name the failed covering case version / run")
	}
	// An AC with no failed covering run shows no hint.
	noFailed := v3SampleSnapshot()
	noFailed.CaseRuns[0].Status = "passed"
	noFailedText := string(renderV3(t, noFailed, "", allSections()))
	if strings.Contains(noFailedText, `data-ac-failed-hint="true"`) {
		t.Fatal("a failed-run hint was rendered when no covering run failed")
	}
}

// TestEvidenceTotalUsesFullCount pins SC-G7/art.7.6: the evidence appendix states
// the FULL pre-cap total, not the truncated display sum, so a capped report never
// silently understates how much evidence exists.
func TestEvidenceTotalUsesFullCount(t *testing.T) {
	snapshot := v3SampleSnapshot()
	snapshot.EvidenceTotalCount = 5 // full total; only 1 evidence row is displayed
	text := string(renderV3(t, snapshot, "", allSections()))
	if !strings.Contains(text, "证据总数（截断前）：5") {
		t.Fatal("the evidence appendix did not state the full pre-cap evidence total")
	}
}

// v3WaivedGoldenSHA256 pins the waived v3 shape byte-for-byte (fix minor: the
// first golden only covered the non-waived shape, so waived markup / truncation
// asides / empty branches could drift without failing the freeze). Updated in the
// final fix wave: the waived section now carries per-requirement waiver
// reason/operator/time (#1) and the AC truncation note states the displayed count
// (#2).
const v3WaivedGoldenSHA256 = "430dc6a50a74c11da0b39b801ad05b2f883a9ec6912d22d25aa0f12c4e0891d6"

func TestTemplateV3WaivedGolden(t *testing.T) {
	output := renderV3(t, v3WaivedSnapshot(), "", allSections())
	normalized := bytes.ReplaceAll(output, []byte("\r\n"), []byte("\n"))
	sum := sha256.Sum256(normalized)
	if got := hex.EncodeToString(sum[:]); got != v3WaivedGoldenSHA256 {
		t.Fatalf("v3 waived golden hash changed: got %s (%d bytes)", got, len(output))
	}
}

// TestConclusionFailedNonProductNotAllPassed pins fix #2 (七.6): a failed run
// triaged flaky (not a product defect) alongside a passed-wins verified AC must
// NOT let the conclusion claim 全部通过/一切正常 while the same page shows the
// failed-run hint and a 需重跑 explanation.
func TestConclusionFailedNonProductNotAllPassed(t *testing.T) {
	snapshot := delivery.ReportSnapshot{
		Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: v3RenderTime,
		CaseRuns: []delivery.CaseRunSnapshot{
			{ID: 1, VersionID: 7, CaseVersionID: 200, Status: "passed", CreatedAt: v3RenderTime},
			{ID: 2, VersionID: 7, CaseVersionID: 201, Status: "failed", FailureClass: "application", TriageClassification: delivery.TriageClassificationFlaky, CreatedAt: v3RenderTime},
		},
		Coverage: delivery.CoverageSnapshot{ACVerification: delivery.ACCoverageSnapshot{Total: 1, Verified: 1}},
	}
	text := string(renderV3(t, snapshot, "", allSections()))
	if strings.Contains(text, "全部通过") || strings.Contains(text, "一切正常") {
		t.Fatal("a failed-but-non-product run must not let the conclusion claim 全部通过/一切正常")
	}
	if !strings.Contains(text, "失败 1") {
		t.Fatal("the conclusion must name the failed-but-non-product run count")
	}
}

// TestACFailedRunHintNamesNewest pins fix #3: with several failed runs covering a
// verified AC, the hint names the NEWEST (max ID) and states the count — not the
// oldest run mislabeled 最新.
func TestACFailedRunHintNamesNewest(t *testing.T) {
	snapshot := v3SampleSnapshot()
	snapshot.CaseRuns = append(snapshot.CaseRuns, delivery.CaseRunSnapshot{
		ID: 30, VersionID: 7, WaveID: 12, CaseVersionID: 201, RequirementID: 101, CaseTitle: "登录用例（重跑）", Status: "failed", FailureClass: "application", CreatedAt: v3RenderTime,
	})
	text := string(renderV3(t, snapshot, "", allSections()))
	if !strings.Contains(text, "case_run 30") {
		t.Fatal("the failed-run hint must name the newest failed run (30), not the oldest")
	}
	if !strings.Contains(text, "2 个失败执行") {
		t.Fatal("the failed-run hint must state the failed-run count")
	}
	if strings.Contains(text, "最新 case_run 21") {
		t.Fatal("the hint named the oldest failed run as 最新")
	}
}

// TestDefectCardRepresentativeTruncated pins fix #5 (art.7.6): when the
// representative run is truncated out, the card still locates the requirement/AC
// through a displayed linked run and DISCLOSES the truncation — it never falsely
// claims 无关联需求/无步骤. When no linked run is displayed, the join is disclosed
// as unlocatable.
func TestDefectCardRepresentativeTruncated(t *testing.T) {
	// Representative run 21 halved out; linked run 20 still displayed.
	partial := v3SampleSnapshot()
	partial.CaseRuns = []delivery.CaseRunSnapshot{partial.CaseRuns[1]}
	text := string(renderV3(t, partial, "", allSections()))
	if strings.Contains(text, "无关联需求") {
		t.Fatal("the card falsely claimed 无关联需求 while the requirement is locatable via a linked run")
	}
	if !strings.Contains(text, "需求一") || !strings.Contains(text, "用户已注册") {
		t.Fatal("the card must still show the requirement and covering AC via the fallback linked run")
	}
	if !strings.Contains(text, "不在本次显示范围") {
		t.Fatal("the card must disclose the representative-run truncation")
	}

	// No linked run displayed at all → unlocatable disclosure, not 无关联需求.
	none := v3SampleSnapshot()
	none.CaseRuns = nil
	noneText := string(renderV3(t, none, "", allSections()))
	if strings.Contains(noneText, "无关联需求") {
		t.Fatal("the card falsely claimed 无关联需求 when the join is merely truncated")
	}
	if !strings.Contains(noneText, "无法定位关联需求") {
		t.Fatal("the card must disclose the requirement join as unlocatable under truncation")
	}
}

// TestEnvironmentDisclosure pins fix #6 (SC-G6): a legacy wave renders the
// per-wave 环境不可核验 annotation, and a carrier divergence renders the readable
// planned-vs-actual difference (T9 projection now has a render surface).
func TestEnvironmentDisclosure(t *testing.T) {
	// The sample carries a verifiable wave with a readable divergence.
	text := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	if !strings.Contains(text, `data-section="environment"`) {
		t.Fatal("v3 has no environment section")
	}
	if !strings.Contains(text, "环境配置不同") {
		t.Fatal("the carrier divergence readable difference is not rendered")
	}
	// A legacy (unverifiable) wave renders the annotation + reason.
	legacy := v3SampleSnapshot()
	legacy.Waves[0].EnvironmentVerifiable = false
	legacy.Waves[0].EnvironmentUnverifiableReason = "legacy 波次快照无环境指纹载体"
	legacy.Waves[0].EnvironmentDivergences = nil
	legacyText := string(renderV3(t, legacy, "", allSections()))
	if !strings.Contains(legacyText, "环境不可核验") || !strings.Contains(legacyText, "legacy 波次快照无环境指纹载体") {
		t.Fatal("a legacy wave must render the per-wave 环境不可核验 annotation with its reason")
	}
}

// TestWaivedContextUnavailable pins fix #4 (七.6): a waived version whose waiver
// fact is lost renders an explicit 豁免上下文不可用 disclosure for the operator,
// never a silent "—".
func TestWaivedContextUnavailable(t *testing.T) {
	snapshot := v3WaivedSnapshot()
	snapshot.WaiverOperator = ""
	snapshot.WaiverReason = ""
	snapshot.WaivedAt = time.Time{}
	text := string(renderV3(t, snapshot, "", allSections()))
	if !strings.Contains(text, "豁免上下文不可用") {
		t.Fatal("a lost waiver fact must disclose 豁免上下文不可用, not a silent —")
	}
	if !strings.Contains(text, "豁免理由不可用") {
		t.Fatal("a missing waiver reason must disclose 豁免理由不可用")
	}
}

// TestACMatrixRenderCap pins the render-side AC display cap (fix minor): a matrix
// larger than the cap is explicitly truncated with an annotation, so a large
// version's flagship report stays renderable.
func TestACMatrixRenderCap(t *testing.T) {
	snapshot := v3SampleSnapshot()
	acs := make([]delivery.ACVerificationSnapshot, 0, 600)
	for i := 0; i < 600; i++ {
		acs = append(acs, delivery.ACVerificationSnapshot{RequirementID: 101, ACID: fmt.Sprintf("AC-%03d", i), Given: "g", When: "w", Then: "t", State: delivery.ACVerificationUnverified})
	}
	snapshot.Coverage.ACVerification = delivery.ACCoverageSnapshot{
		ByRequirement: []delivery.RequirementACCoverageSnapshot{{RequirementID: 101, ACs: acs}},
		Total:         600, Unverified: 600,
	}
	text := string(renderV3(t, snapshot, "", allSections()))
	if !strings.Contains(text, `data-ac-render-truncated="true"`) {
		t.Fatal("an over-cap AC matrix was not explicitly render-truncated")
	}
	if !strings.Contains(text, "共 600 条") {
		t.Fatal("the render-cap annotation must state the full AC count")
	}
}

// TestFailureExplanationsTruncated pins the v3 truncation pass (fix minor): the
// failure-explanation carrier is halved under the 2 MiB limit with an explicit
// note, so a large version renders instead of failing outright.
func TestFailureExplanationsTruncated(t *testing.T) {
	snapshot := delivery.ReportSnapshot{Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: v3RenderTime}
	for id := int64(1); id <= 3000; id++ {
		snapshot.FailureExplanations = append(snapshot.FailureExplanations, delivery.FailureExplanationSnapshot{
			CaseRunID: id, CaseTitle: "用例", Status: "blocked", EffectiveFailureClass: "blocked",
			Explanation: strings.Repeat("x", 900),
		})
	}
	output, err := newVersionedRenderer(t, TemplateVersionV3).Render(context.Background(), snapshot, "", allSections())
	if err != nil {
		t.Fatalf("Render with oversized failure explanations: %v", err)
	}
	if len(output) > MaxHTMLBytes {
		t.Fatalf("truncated output = %d bytes, want <= %d", len(output), MaxHTMLBytes)
	}
	text := string(output)
	if !strings.Contains(text, `data-truncated="true"`) || !strings.Contains(text, "失败说明") {
		t.Fatal("the failure-explanation truncation is not disclosed in the note")
	}
}
