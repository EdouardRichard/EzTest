package render

// v3 human-useful report projections (v0.2.4-G T10, SC-G1/G2/G4/G11). The v3
// template renders a FIXED structure organized by defect: overview (with a
// deterministic conclusion and fixed Chinese term definitions) → five-element
// defect cards with natural-language context and viewable evidence → non-issue
// failure explanations → traceability chain → evidence appendix → AC verification
// matrix (with judge display and failed-run hints) → waived content. Every value
// is derived from the deterministic snapshot (single source of truth); the AI
// narrative stays an isolated candidate block guarded by the application layer.
//
// Only the v3 template consumes these projections; v1/v2 stay byte-identical.

import (
	"fmt"
	"sort"
	"strings"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// conclusionView is the deterministic overview conclusion (SC-G1 C1/M2). It is
// generated from snapshot facts, never from the narrative. Level is a stable
// machine marker (ok/unverified/defect/empty); Text is the fixed human-readable
// Chinese conclusion. A zero-defect version with blocked/inconclusive runs or
// unverified ACs is "unverified", never "ok" (it must not read 一切正常).
type conclusionView struct {
	Text  string
	Level string
}

// termDef is one fixed Chinese definition of the failure vocabulary (SC-G1 M3).
type termDef struct {
	Term       string
	Definition string
}

// evidenceLink is one viewable evidence row (SC-G2/G7 C3): the content endpoint
// link a human opens to see the actual screenshot/log/capture, plus the hash.
type evidenceLink struct {
	ID          int64
	CaseRunID   int64
	Kind        string
	ContentPath string
	SHA256      string
}

// defectCard is one five-element defect card (SC-G2). The five elements render in
// a FIXED order: 关联需求 → 执行步骤 → 验收标准 AC → 预期结果 → 实际结果. The
// natural-language context (requirement user story, case description/preconditions,
// step action/target, SUT name/description) lets a human understand the defect
// without looking up any ID. Expected/Actual come from the DEFINED representative
// run only (I4); the other linked runs are listed, never mixed in.
type defectCard struct {
	IssueID             int64
	Title               string
	Status              string
	Category            string
	Severity            string
	CaseTitle           string
	CaseDescription     string
	CasePreconditions   string
	SUTName             string
	SUTDescription      string
	RequirementID       int64
	RequirementTitle    string
	UserStory           string
	HasRequirement      bool
	Steps               []delivery.CaseStepSnapshot
	ACs                 []delivery.ACVerificationSnapshot
	Expected            string
	Actual              string
	Reason              string
	Evidence            []evidenceLink
	RepresentativeRunID int64
	OtherRunIDs         []int64
	// RepTruncated: the representative run is referenced but not in the displayed
	// run list (2 MiB halving / input cap). ContextTruncated: NO linked run is
	// displayed, so the requirement/AC join cannot be located. Both are explicit
	// truncation disclosures (art.7.6) — the card must never claim 无关联需求/无步骤
	// when the truth is "not in this display range". ContextRunID is the fallback
	// linked run used to locate the requirement/AC when the representative run is
	// truncated (requirement-level context only; expected/actual/steps stay
	// representative-scoped, never mixed across runs, I4).
	RepTruncated     bool
	ContextTruncated bool
	ContextRunID     int64
	// EvidenceTruncated: the card's representative evidence display list was
	// reduced below its full reverse-lookup count by the aggregate card budget
	// (final fix wave #6). The card discloses 证据超出显示上限（已截断） instead of
	// falsely claiming 无代表 run 证据 when the evidence exists but is capped.
	EvidenceTruncated bool
}

// traceRow is one link of the traceability chain (SC-G2):
// 需求 → AC → 用例 → case_run → 步骤 → 断言 → 证据.
type traceRow struct {
	RequirementID    int64
	RequirementTitle string
	ACIDs            []string
	CaseVersionID    int64
	CaseTitle        string
	CaseRunID        int64
	StepSeq          int
	AssertionSeq     int
	EvidenceIDs      []int64
}

// acMatrixRow is one AC verification row (SC-G4 I1) with its g/w/t text, its
// verification state and a failed-run hint (T9 #4): a verified AC (passed wins
// over failed) that also has a failed covering run still shows the failure so a
// human is not misled by the verified label.
type acMatrixRow struct {
	RequirementID          int64
	ACID                   string
	Given                  string
	When                   string
	Then                   string
	State                  string
	StateLabel             string
	CoveringCaseVersionIDs []int64
	FailedRunHint          string
	HasFailedHint          bool
}

// judgeRow is one design judge fact projection (SC-G4 I3, T11 carry): the derived
// review_state, the latest verdict and findings, plus the judge's CANDIDATE AC
// matrix (presentation only, never the verification truth) and its disclosed
// discard count.
type judgeRow struct {
	CaseID        int64
	CaseVersionID int64
	RequirementID int64
	ReviewState   string
	Verdict       string
	Findings      []delivery.JudgmentFindingSnapshot
	MatrixKeys    []string
	Matrix        map[string][]string
	DroppedCount  int
}

// waivedRequirement / waivedAC name the waived scope (SC-G11): the waived
// requirements and the unverified/failed/uncovered AC range a human must judge.
// Each waived requirement carries its OWN waiver reason/operator/time (final fix
// wave #1) so the disclosure names who overrode completeness and why, independent
// of the version closure_kind.
type waivedRequirement struct {
	ID       int64
	Title    string
	Reason   string
	Operator string
	WaivedAt string
}

type waivedAC struct {
	RequirementID int64
	ACID          string
	State         string
	StateLabel    string
}

// waivedView is the waived report content (SC-G11): the unverified badge plus the
// waived scope, the waiver reason and the operator. Rendered only when the version
// closure_kind is waived (gated on the column, never on waiver-fact presence).
type waivedView struct {
	Reason        string
	Operator      string
	WaivedAt      string
	Requirements  []waivedRequirement
	UnverifiedACs []waivedAC
}

// reportTerms is the fixed, deterministic Chinese failure vocabulary (SC-G1 M3).
// The order is part of the frozen template contract. The wording mirrors the
// domain FailureExplanationText so a human reads the same definition everywhere.
var reportTerms = []termDef{
	{"失败（failed）", "用例执行完成但断言未通过，可能为产品缺陷，需结合证据与平台分诊判断。"},
	{"受阻（blocked）", "执行被阻塞，用例未能完成（如执行器能力受限），不是产品缺陷。"},
	{"不确定（inconclusive）", "证据不足，无法判定用例通过或失败，需补充证据后复测。"},
	{"预期内失败（expected_failure）", "平台分诊判定为预期内失败（如预算耗尽或未观察到目标），需重跑后才能得出结论，不计入产品缺陷。"},
	{"环境问题（environment）", "执行环境问题导致失败，不是产品缺陷。"},
	{"自动化偶发（automation）", "平台分诊判定为自动化偶发失败（flaky），不是产品缺陷，建议重跑确认。"},
}

// acStateLabel maps an AC verification state to its fixed Chinese label.
func acStateLabel(state string) string {
	switch state {
	case delivery.ACVerificationVerified:
		return "已验证"
	case delivery.ACVerificationUnverified:
		return "未验证"
	case delivery.ACVerificationFailed:
		return "失败"
	case delivery.ACVerificationUncovered:
		return "未覆盖"
	default:
		return state
	}
}

// The following label functions are the report's fixed Chinese glossary (SC-G1
// M3): every enum a Chinese-reading human sees is rendered in Chinese, with the
// raw code kept in parentheses where it helps cross-reference the API. Unknown
// values fall back to the raw code (never silently dropped). EVERY switch key is
// a domain constant (single source, 五.1/五.4): the delivery display constants for
// delivery-owned enums, and the owning context's constants for the projected
// foreign vocabularies (design review_state/verdict, execution assertion kind).
// H T11e-1: the design judge keys used to be a hand-written presentation
// dictionary here on the stated premise that "the delivery context must not
// import the design domain" — that premise does not hold at the infrastructure
// layer (adapters/read_model.go already imports the design, execution and version
// domains in production), and a dictionary keyed by literals silently falls
// through to the raw code the moment a design constant changes value. Only the
// delivery DOMAIN keeps that rule (see delivery/domain/display.go).

func issueStatusLabel(status string) string {
	switch delivery.IssueStatus(status) {
	case delivery.IssueStatusOpen:
		return "未关闭（open）"
	case delivery.IssueStatusConfirmed:
		return "已确认（confirmed）"
	case delivery.IssueStatusDismissed:
		return "已驳回（dismissed）"
	case delivery.IssueStatusMerged:
		return "已合并（merged）"
	default:
		return status
	}
}

func issueCategoryLabel(category string) string {
	switch category {
	case execution.AssertionKindUI:
		return "界面（ui）"
	case execution.AssertionKindAPI:
		return "接口（api）"
	case execution.AssertionKindDB:
		return "数据库（db）"
	default:
		return category
	}
}

func severityLabel(severity string) string {
	switch severity {
	case delivery.IssueSeverityMajor:
		return "主要（major）"
	case delivery.IssueSeverityMinor:
		return "次要（minor）"
	case delivery.IssueSeverityTrivial:
		return "轻微（trivial）"
	default:
		return severity
	}
}

func closureKindLabel(kind string) string {
	switch kind {
	case delivery.ReportClosureKindWaived:
		return "豁免闭环（waived）"
	case delivery.ReportClosureKindNormal, "":
		return "正常闭环（normal）"
	default:
		return kind
	}
}

func runStatusLabel(status string) string {
	switch status {
	case delivery.ReportRunStatusPassed:
		return "通过（passed）"
	case delivery.ReportRunStatusFailed:
		return "失败（failed）"
	case delivery.ReportRunStatusBlocked:
		return "受阻（blocked）"
	case delivery.ReportRunStatusInconclusive:
		return "不确定（inconclusive）"
	case delivery.ReportRunStatusSkipped:
		return "跳过（skipped）"
	default:
		return status
	}
}

func failureClassLabel(class string) string {
	switch class {
	case delivery.ReportFailureClassApplication:
		return "应用缺陷（application）"
	case delivery.ReportFailureClassAutomation:
		return "自动化偶发（automation）"
	case delivery.ReportFailureClassExpectedFailure:
		return "预期内失败（expected_failure）"
	case delivery.ReportFailureClassBlocked:
		return "受阻（blocked）"
	case delivery.ReportFailureClassEnvironment:
		return "环境问题（environment）"
	case delivery.ReportFailureClassSession:
		return "会话问题（session）"
	case delivery.ReportFailureClassModel:
		return "模型异常（model）"
	case delivery.ReportFailureClassTimeout:
		return "超时（timeout）"
	case delivery.ReportFailureClassExecutorLost:
		return "执行器失联（executor_lost）"
	case delivery.ReportFailureClassUnknown:
		return "未分类（unknown）"
	case "":
		return "—"
	default:
		return class
	}
}

func reviewStateLabel(state string) string {
	switch design.ReviewState(state) {
	case design.ReviewStatePending:
		return "待评审（pending）"
	case design.ReviewStatePass:
		return "评审通过（pass）"
	case design.ReviewStateReworking:
		return "返工中（reworking）"
	case design.ReviewStateEscalatedFail:
		return "升级失败（escalated_fail）"
	case design.ReviewStateJudgeUnavailable:
		return "裁判不可用（judge_unavailable）"
	case "":
		return "未评审"
	default:
		return state
	}
}

func verdictLabel(verdict string) string {
	switch verdict {
	case design.VerdictPass:
		return "通过（pass）"
	case design.VerdictFail:
		return "不通过（fail）"
	case "":
		return "—"
	default:
		return verdict
	}
}

// initV3Stable populates the v3 projections that do not depend on the truncatable
// display lists: the raw judge/failure/AC/waiver carriers and the deterministic
// conclusion, term list, AC coverage rate and waived scope. It is called once from
// Render for the v3 template only (T19b #24); v1/v2 keep their frozen bytes.
func (data *renderData) initV3Stable(snapshot delivery.ReportSnapshot) {
	data.Judgments = append([]delivery.JudgmentSnapshot(nil), snapshot.Judgments...)
	sort.SliceStable(data.Judgments, func(i, j int) bool { return data.Judgments[i].CaseID < data.Judgments[j].CaseID })
	data.FailureExplanations = append([]delivery.FailureExplanationSnapshot(nil), snapshot.FailureExplanations...)
	sort.SliceStable(data.FailureExplanations, func(i, j int) bool {
		return data.FailureExplanations[i].CaseRunID < data.FailureExplanations[j].CaseRunID
	})
	data.ACVerification = snapshot.Coverage.ACVerification
	data.ClosureKind = snapshot.ClosureKind
	data.Unverified = snapshot.Unverified
	data.WaiverReason = snapshot.WaiverReason
	data.WaiverOperator = snapshot.WaiverOperator
	data.WaivedAt = formatTime(snapshot.WaivedAt)
	data.Terms = reportTerms
	data.Conclusion = deriveConclusion(snapshot)
	data.ACRate = acCoverageRate(data.ACVerification)
	// The evidence appendix states the FULL pre-cap total (SC-G7, art.7.6), never
	// the truncated display sum. The production read model populates
	// EvidenceTotalCount; a synthetic snapshot without one falls back to the
	// displayed evidence sum (equal to the full total when nothing was capped).
	data.EvidenceTotalCount = snapshot.EvidenceTotalCount
	if data.EvidenceTotalCount == 0 {
		data.EvidenceTotalCount = data.Summary.Evidence
	}
	data.HasWaived = snapshot.ClosureKind == delivery.ReportClosureKindWaived || anyRequirementWaived(snapshot)
	data.VersionWaived = snapshot.ClosureKind == delivery.ReportClosureKindWaived
	if data.HasWaived {
		data.Waived = buildWaivedView(snapshot)
	}
}

// anyRequirementWaived reports whether any requirement snapshot carries the
// admin requirement-level waiver flag (final fix wave #1). The waiver disclosure
// and conclusion gate on this INDEPENDENT of the version closure_kind: a
// requirement waived then normally closed still overrode closure completeness.
func anyRequirementWaived(snapshot delivery.ReportSnapshot) bool {
	for _, requirement := range snapshot.Requirements {
		if requirement.Waived {
			return true
		}
	}
	return false
}

// deriveConclusion is the deterministic overview conclusion (SC-G1 C1/M2/M3). It
// distinguishes a clean pass from a zero-defect-but-unverified version: ANY failed
// run (even one triaged flaky/expected_failure, which is not a product defect),
// blocked/inconclusive run, or unverified/failed/uncovered AC keeps the conclusion
// out of the ok branch. 全部通过/一切正常 is claimed ONLY when every displayed run
// passed (Passed==Total) and every AC is verified — a failed-but-non-product run
// alongside a passed-wins verified AC must never read as a clean pass (fix #2:
// the conclusion may not contradict the failed-run hint / failure explanations on
// the same page).
func deriveConclusion(snapshot delivery.ReportSnapshot) conclusionView {
	defects := 0
	for _, issue := range snapshot.Issues {
		if issue.OpenProductIssue() {
			defects++
		}
	}
	summary := summarize(snapshot)
	ac := snapshot.Coverage.ACVerification
	unverifiedACs := ac.Unverified + ac.Uncovered
	versionWaived := snapshot.ClosureKind == delivery.ReportClosureKindWaived
	requirementWaived := anyRequirementWaived(snapshot)
	hasWaiver := versionWaived || requirementWaived
	// The waiver note is scope-accurate: a version waive-close vs a requirement
	// waiver under a normal close (final fix wave #1).
	waiverNote := "存在需求级豁免：被豁免需求的验收完整性未经完整验证，剩余风险见豁免说明"
	if versionWaived {
		waiverNote = "管理员豁免关闭：存在未验证的需求/验收标准，剩余风险见豁免说明"
	}
	blockedSuffix := ""
	if summary.Blocked > 0 || summary.Inconclusive > 0 {
		blockedSuffix = fmt.Sprintf("另有受阻 %d、不确定 %d 个用例尚未得出结论。", summary.Blocked, summary.Inconclusive)
	}
	switch {
	case summary.Total == 0:
		return conclusionView{Level: "empty", Text: "尚无执行结果，无法得出验收结论。"}
	case hasWaiver && defects > 0:
		// The waiver is named FIRST (final fix wave #1/minor): a waived version
		// with open product defects must not let the defect branch hide the waiver.
		return conclusionView{Level: "defect", Text: waiverNote + fmt.Sprintf("；另发现 %d 个未关闭产品缺陷，需修复后复测。", defects) + blockedSuffix}
	case defects > 0:
		return conclusionView{Level: "defect", Text: fmt.Sprintf("发现 %d 个未关闭产品缺陷，需修复后复测。", defects) + blockedSuffix}
	case versionWaived:
		return conclusionView{Level: "unverified", Text: "管理员豁免关闭：存在未验证的需求/验收标准，剩余风险见豁免说明，不能视为验收通过。"}
	case requirementWaived:
		return conclusionView{Level: "unverified", Text: "存在需求级豁免：管理员豁免了部分需求的闭环完整性检查，被豁免需求的验收标准未经完整验证，剩余风险见豁免说明，不能视为验收通过。"}
	case summary.Failed > 0 || summary.Blocked > 0 || summary.Inconclusive > 0 || ac.Failed > 0 || unverifiedACs > 0 || summary.Passed != summary.Total:
		return conclusionView{
			Level: "unverified",
			Text: fmt.Sprintf("未发现产品缺陷，但验收尚未完成：失败 %d（经分诊非产品缺陷或未建单，需复核/重跑）、受阻 %d、不确定 %d、跳过 %d；未验证/未覆盖验收标准 %d、失败验收标准 %d。需人工复核后方可下验收结论。",
				summary.Failed, summary.Blocked, summary.Inconclusive, summary.Skipped, unverifiedACs, ac.Failed),
		}
	default:
		return conclusionView{Level: "ok", Text: "未发现缺陷，已执行用例全部通过，验收标准均已验证，一切正常。"}
	}
}

// acCoverageRate formats the AC-level verification coverage rate (SC-G4 I1): it
// separates verification (verified) from link coverage so 100% link coverage is
// never misread as acceptance-passed.
func acCoverageRate(ac delivery.ACCoverageSnapshot) string {
	if ac.Total == 0 {
		return "AC 验证覆盖率：无验收标准。"
	}
	return fmt.Sprintf("AC 验证覆盖率：已验证 %d / 共 %d（未验证 %d、失败 %d、未覆盖 %d；链接覆盖 %d）。",
		ac.Verified, ac.Total, ac.Unverified, ac.Failed, ac.Uncovered, ac.LinkCovered)
}

// buildWaivedView projects the waived scope (SC-G11): the waived requirements and
// every AC that is not verified (the unverified range a human must judge), plus
// the waiver reason/operator/time carried by the snapshot.
func buildWaivedView(snapshot delivery.ReportSnapshot) waivedView {
	waivedAt := "未记录（早期豁免事实无时间戳）"
	if !snapshot.WaivedAt.IsZero() {
		waivedAt = formatTime(snapshot.WaivedAt)
	}
	view := waivedView{
		Reason:   snapshot.WaiverReason,
		Operator: snapshot.WaiverOperator,
		WaivedAt: waivedAt,
	}
	for _, requirement := range snapshot.Requirements {
		if requirement.Waived {
			waivedAt := ""
			if !requirement.WaivedAt.IsZero() {
				waivedAt = formatTime(requirement.WaivedAt)
			}
			view.Requirements = append(view.Requirements, waivedRequirement{
				ID: requirement.ID, Title: requirement.Title,
				Reason: requirement.WaiverReason, Operator: requirement.WaiverOperator, WaivedAt: waivedAt,
			})
		}
	}
	sort.SliceStable(view.Requirements, func(i, j int) bool { return view.Requirements[i].ID < view.Requirements[j].ID })
	for _, group := range snapshot.Coverage.ACVerification.ByRequirement {
		for _, ac := range group.ACs {
			if ac.State != delivery.ACVerificationVerified {
				view.UnverifiedACs = append(view.UnverifiedACs, waivedAC{
					RequirementID: ac.RequirementID, ACID: ac.ACID, State: ac.State, StateLabel: acStateLabel(ac.State),
				})
			}
		}
	}
	sort.SliceStable(view.UnverifiedACs, func(i, j int) bool {
		if view.UnverifiedACs[i].RequirementID != view.UnverifiedACs[j].RequirementID {
			return view.UnverifiedACs[i].RequirementID < view.UnverifiedACs[j].RequirementID
		}
		return view.UnverifiedACs[i].ACID < view.UnverifiedACs[j].ACID
	})
	return view
}

// buildV3Display rebuilds the truncation-dependent v3 views on every render so
// they stay consistent with the (possibly halved) display lists. Cards/rows are
// sorted deterministically regardless of input order.
func (data *renderData) buildV3Display() {
	runs := make(map[int64]delivery.CaseRunSnapshot, len(data.CaseRuns))
	for _, run := range data.CaseRuns {
		runs[run.ID] = run
	}
	requirements := make(map[int64]delivery.RequirementSnapshot, len(data.Requirements))
	for _, requirement := range data.Requirements {
		requirements[requirement.ID] = requirement
	}
	suts := make(map[int64]delivery.SUTSnapshot, len(data.SUTs))
	for _, sut := range data.SUTs {
		suts[sut.ID] = sut
	}
	acByRequirement := make(map[int64][]delivery.ACVerificationSnapshot, len(data.ACVerification.ByRequirement))
	for _, group := range data.ACVerification.ByRequirement {
		acByRequirement[group.RequirementID] = group.ACs
	}

	data.DefectCards = buildDefectCards(data.Issues, runs, requirements, suts, acByRequirement)
	data.Traceability = buildTraceability(data.CaseRuns, requirements, acByRequirement)
	// Render-side AC matrix display cap (fix minor: the AC matrix derives from a
	// carrier the 2 MiB halving pass never shrinks, so a huge matrix could make the
	// flagship report unrenderable). Cap the displayed rows with an EXPLICIT
	// annotation (art.7.6); the summary counts still reflect the full set. The
	// read model already caps at MaxReportACs; this is the render-side backstop.
	acRows := buildACMatrix(data.ACVerification, data.CaseRuns)
	data.ACMatrixRenderTotal = len(acRows)
	data.ACMatrixRenderTruncated = len(acRows) > delivery.MaxReportACs
	if data.ACMatrixRenderTruncated {
		acRows = acRows[:delivery.MaxReportACs]
	}
	data.ACMatrix = acRows
	data.EvidenceLinks = buildEvidenceLinks(data.CaseRuns)
	data.JudgeRows = buildJudgeRows(data.Judgments)
}

// buildDefectCards joins each issue with its DEFINED representative run, the
// requirement natural-language context, the covering AC g/w/t and the viewable
// representative evidence. Expected/Actual come from the representative run only.
//
// Fix #5 (art.7.6): the requirement/AC join does NOT depend on the representative
// run being displayed. Requirements and the AC matrix are not truncatable, so when
// the representative run is halved out the card locates the requirement/AC through
// another displayed linked run (requirement-level context only) and DISCLOSES the
// representative-run truncation instead of falsely claiming 无关联需求/无步骤. When
// no linked run is displayed at all, the join is disclosed as unlocatable (never
// "无…"). Steps/case NL stay representative-scoped (I4: never mixed across runs).
func buildDefectCards(
	issues []delivery.IssueSnapshot,
	runs map[int64]delivery.CaseRunSnapshot,
	requirements map[int64]delivery.RequirementSnapshot,
	suts map[int64]delivery.SUTSnapshot,
	acByRequirement map[int64][]delivery.ACVerificationSnapshot,
) []defectCard {
	cards := make([]defectCard, 0, len(issues))
	for _, issue := range issues {
		card := defectCard{
			IssueID:             issue.ID,
			Title:               issue.Title,
			Status:              string(issue.Status),
			Category:            issue.Category,
			Severity:            issue.Severity,
			Expected:            issue.FailedExpected,
			Actual:              issue.FailedActual,
			Reason:              issue.FailedReason,
			RepresentativeRunID: issue.RepresentativeCaseRunID,
		}
		rep, hasRep := runs[issue.RepresentativeCaseRunID]
		card.RepTruncated = issue.RepresentativeCaseRunID != 0 && !hasRep
		// Locate the requirement/AC context: the representative run when displayed,
		// else the smallest-ID displayed linked run (deterministic fallback).
		contextRun, hasContext := rep, hasRep
		if !hasContext {
			linked := append([]int64(nil), issue.CaseRunIDs...)
			sort.Slice(linked, func(i, j int) bool { return linked[i] < linked[j] })
			for _, id := range linked {
				if run, ok := runs[id]; ok {
					contextRun, hasContext = run, true
					card.ContextRunID = id
					break
				}
			}
		}
		card.ContextTruncated = len(issue.CaseRunIDs) > 0 && !hasContext
		if hasContext {
			card.RequirementID = contextRun.RequirementID
			if requirement, ok := requirements[contextRun.RequirementID]; ok {
				card.HasRequirement = true
				card.RequirementTitle = requirement.Title
				card.UserStory = formatUserStory(requirement.UserStory)
				card.SUTDescription = requirement.SutDescription
			}
			for _, ac := range acByRequirement[contextRun.RequirementID] {
				if coversCaseVersion(ac.CoveringCaseVersionIDs, contextRun.CaseVersionID) {
					card.ACs = append(card.ACs, ac)
				}
			}
		}
		// Case-level NL and steps are representative-scoped only (I4): a fallback
		// run may be a different case version, so its NL/steps are never shown as
		// the representative's.
		if hasRep {
			card.CaseTitle = rep.CaseTitle
			card.CaseDescription = rep.CaseDescription
			card.CasePreconditions = rep.CasePreconditions
			if rep.SUTDescription != "" {
				card.SUTDescription = rep.SUTDescription
			}
			card.Steps = rep.Steps
			if rep.SUTID != nil {
				if sut, ok := suts[*rep.SUTID]; ok {
					card.SUTName = sut.SUTName
					if card.SUTDescription == "" {
						card.SUTDescription = sut.SUTDescription
					}
				}
			}
		}
		for _, evidence := range issue.RepresentativeEvidence {
			card.Evidence = append(card.Evidence, evidenceLink{
				ID: evidence.ID, CaseRunID: evidence.CaseRunID, Kind: evidence.Kind, ContentPath: evidence.ContentPath, SHA256: evidence.SHA256,
			})
		}
		card.EvidenceTruncated = issue.RepresentativeEvidenceTruncated
		for _, id := range issue.CaseRunIDs {
			if id != issue.RepresentativeCaseRunID {
				card.OtherRunIDs = append(card.OtherRunIDs, id)
			}
		}
		sort.SliceStable(card.OtherRunIDs, func(i, j int) bool { return card.OtherRunIDs[i] < card.OtherRunIDs[j] })
		cards = append(cards, card)
	}
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].IssueID < cards[j].IssueID })
	return cards
}

func formatUserStory(story delivery.UserStorySnapshot) string {
	if story.AsA == "" && story.IWant == "" && story.SoThat == "" {
		return ""
	}
	return fmt.Sprintf("作为%s，我希望%s，以便%s。", story.AsA, story.IWant, story.SoThat)
}

func coversCaseVersion(ids []int64, caseVersionID int64) bool {
	for _, id := range ids {
		if id == caseVersionID {
			return true
		}
	}
	return false
}

// buildTraceability projects one chain row per case run: requirement → covering
// ACs → case version → case_run → failed step/assertion → evidence ids.
func buildTraceability(
	caseRuns []delivery.CaseRunSnapshot,
	requirements map[int64]delivery.RequirementSnapshot,
	acByRequirement map[int64][]delivery.ACVerificationSnapshot,
) []traceRow {
	rows := make([]traceRow, 0, len(caseRuns))
	for _, run := range caseRuns {
		row := traceRow{
			RequirementID: run.RequirementID,
			CaseVersionID: run.CaseVersionID,
			CaseTitle:     run.CaseTitle,
			CaseRunID:     run.ID,
			StepSeq:       run.FailedStepSeq,
			AssertionSeq:  run.FailedAssertionSeq,
		}
		if requirement, ok := requirements[run.RequirementID]; ok {
			row.RequirementTitle = requirement.Title
		}
		for _, ac := range acByRequirement[run.RequirementID] {
			if coversCaseVersion(ac.CoveringCaseVersionIDs, run.CaseVersionID) {
				row.ACIDs = append(row.ACIDs, ac.ACID)
			}
		}
		for _, evidence := range run.Evidence {
			row.EvidenceIDs = append(row.EvidenceIDs, evidence.ID)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CaseRunID < rows[j].CaseRunID })
	return rows
}

// buildACMatrix projects the AC verification rows in requirement/AC order and
// attaches a failed-run hint to a verified AC that also has a failed covering run
// (T9 #4: passed wins over failed in the state, but the human still sees the
// failure). The hint names the failed covering case version and case run.
func buildACMatrix(ac delivery.ACCoverageSnapshot, caseRuns []delivery.CaseRunSnapshot) []acMatrixRow {
	failedByCaseVersion := make(map[int64][]int64)
	for _, run := range caseRuns {
		if run.Status == delivery.ReportRunStatusFailed {
			failedByCaseVersion[run.CaseVersionID] = append(failedByCaseVersion[run.CaseVersionID], run.ID)
		}
	}
	rows := make([]acMatrixRow, 0)
	for _, group := range ac.ByRequirement {
		for _, criterion := range group.ACs {
			row := acMatrixRow{
				RequirementID:          criterion.RequirementID,
				ACID:                   criterion.ACID,
				Given:                  criterion.Given,
				When:                   criterion.When,
				Then:                   criterion.Then,
				State:                  criterion.State,
				StateLabel:             acStateLabel(criterion.State),
				CoveringCaseVersionIDs: criterion.CoveringCaseVersionIDs,
			}
			if criterion.State == delivery.ACVerificationVerified {
				for _, caseVersionID := range criterion.CoveringCaseVersionIDs {
					if runIDs := failedByCaseVersion[caseVersionID]; len(runIDs) > 0 {
						// Fix #3: name the NEWEST failed run (max ID — IDs are
						// allocated ascending, so the max is the latest), not the
						// oldest, and state the failed-run count. The earlier
						// runIDs[0] was the earliest failed run while the text said
						// 最新, pointing humans at the wrong case_run.
						newest := runIDs[0]
						for _, id := range runIDs {
							if id > newest {
								newest = id
							}
						}
						row.HasFailedHint = true
						row.FailedRunHint = fmt.Sprintf("注意：该验收标准虽有通过用例，但用例版本 %d 有 %d 个失败执行（最新 case_run %d），请复核失败证据。",
							caseVersionID, len(runIDs), newest)
						break
					}
				}
			}
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].RequirementID != rows[j].RequirementID {
			return rows[i].RequirementID < rows[j].RequirementID
		}
		return rows[i].ACID < rows[j].ACID
	})
	return rows
}

// buildEvidenceLinks flattens the displayed runs' evidence into viewable links
// (the evidence appendix), ordered by evidence ID.
func buildEvidenceLinks(caseRuns []delivery.CaseRunSnapshot) []evidenceLink {
	links := make([]evidenceLink, 0)
	for _, run := range caseRuns {
		for _, evidence := range run.Evidence {
			links = append(links, evidenceLink{
				ID: evidence.ID, CaseRunID: evidence.CaseRunID, Kind: evidence.Kind, ContentPath: evidence.ContentPath, SHA256: evidence.SHA256,
			})
		}
	}
	sort.SliceStable(links, func(i, j int) bool { return links[i].ID < links[j].ID })
	return links
}

// buildJudgeRows projects the judge facts for display, iterating the candidate AC
// matrix in sorted key order so the presentation is deterministic.
func buildJudgeRows(judgments []delivery.JudgmentSnapshot) []judgeRow {
	rows := make([]judgeRow, 0, len(judgments))
	for _, judgment := range judgments {
		row := judgeRow{
			CaseID:        judgment.CaseID,
			CaseVersionID: judgment.CaseVersionID,
			RequirementID: judgment.RequirementID,
			ReviewState:   judgment.ReviewState,
			Verdict:       judgment.Verdict,
			Findings:      judgment.Findings,
			Matrix:        judgment.ACCoverageMatrix,
			DroppedCount:  judgment.ACCoverageMatrixDroppedCount,
		}
		for acID := range judgment.ACCoverageMatrix {
			row.MatrixKeys = append(row.MatrixKeys, acID)
		}
		sort.Strings(row.MatrixKeys)
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CaseID < rows[j].CaseID })
	return rows
}

// joinInt64s renders an int64 list as a comma-separated string in ASCENDING order
// (a sorted copy — the input is never mutated), so the display is deterministic
// regardless of carrier order (empty list → "—").
func joinInt64s(values []int64) string {
	if len(values) == 0 {
		return "—"
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, 0, len(sorted))
	for _, value := range sorted {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, ", ")
}

// joinStrings renders a string list as a comma-separated string (empty → "—").
func joinStrings(values []string) string {
	if len(values) == 0 {
		return "—"
	}
	return strings.Join(values, ", ")
}
