package render

// v0.2.4-G final fix-wave tests (whole-branch independent review). These pin the
// cross-task seam fixes: the AC truncation note states the DISPLAYED count (#2),
// the requirement-level waiver disclosure renders independent of closure_kind
// (#1), the waived conclusion mentions waiver even with open defects (#1/minor),
// and a budget-truncated defect card discloses truncation instead of falsely
// claiming 无代表 run 证据 (#6).

import (
	"fmt"
	"strings"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// requirementWaivedSnapshot is a NORMALLY closed version (closure_kind=normal,
// unverified=true because a requirement closed via waiver) whose requirement 101
// was admin-waived with a recorded reason/operator/time. The version itself was
// NOT waive-closed.
func requirementWaivedSnapshot() delivery.ReportSnapshot {
	snapshot := v3SampleSnapshot()
	snapshot.ClosureKind = delivery.ReportClosureKindNormal
	snapshot.Unverified = true
	snapshot.Issues = nil
	snapshot.CaseRuns[0].Status = "passed"
	snapshot.Requirements[0].Waived = true
	snapshot.Requirements[0].WaiverReason = "AC-2 延期到下一版本验证"
	snapshot.Requirements[0].WaiverOperator = "9001"
	snapshot.Requirements[0].WaivedAt = v3RenderTime
	return snapshot
}

// TestRequirementWaiverDisclosedWithoutVersionWaive pins final fix wave #1
// (SC-G11): a requirement-level waiver is disclosed in the report INDEPENDENT of
// the version closure_kind. A normally closed version with a waived requirement
// renders the 豁免说明 section naming the waived requirement, its reason and its
// operator — the human can tell the admin overrode completeness even though the
// closure is labeled normal. The conclusion mentions the waiver too.
func TestRequirementWaiverDisclosedWithoutVersionWaive(t *testing.T) {
	text := string(renderV3(t, requirementWaivedSnapshot(), "", allSections()))
	if !strings.Contains(text, `data-section="waived"`) {
		t.Fatal("a normally closed version with a waived requirement rendered no 豁免说明 section")
	}
	for _, want := range []string{"需求一", "AC-2 延期到下一版本验证", "9001"} {
		if !strings.Contains(text, want) {
			t.Fatalf("requirement waiver disclosure missing %q", want)
		}
	}
	// The conclusion must mention the waiver (not read as a clean verified pass).
	conclusion := conclusionText(t, text)
	if !strings.Contains(conclusion, "豁免") {
		t.Fatalf("conclusion does not mention the requirement waiver: %q", conclusion)
	}
	if strings.Contains(conclusion, "一切正常") {
		t.Fatalf("a waived-requirement version concluded 一切正常: %q", conclusion)
	}

	// Control: a normal version with NO waived requirement renders no waived section.
	plain := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	if strings.Contains(plain, `data-section="waived"`) {
		t.Fatal("a normal version with no waiver rendered the waived section")
	}
}

// TestRequirementWaiverDisclosedWhileRunning pins that the disclosure does not
// depend on the version being closed at all: a RUNNING version (closure_kind
// empty) with a waived requirement still discloses it.
func TestRequirementWaiverDisclosedWhileRunning(t *testing.T) {
	snapshot := requirementWaivedSnapshot()
	snapshot.ClosureKind = ""
	snapshot.Unverified = false
	text := string(renderV3(t, snapshot, "", allSections()))
	if !strings.Contains(text, `data-section="waived"`) || !strings.Contains(text, "AC-2 延期到下一版本验证") {
		t.Fatal("a running version with a waived requirement did not disclose the waiver")
	}
}

// TestWaivedConclusionMentionsWaiverWithOpenDefects pins the conclusion reorder
// (final fix wave #1 + minor): a waived version that ALSO has open product defects
// must mention BOTH the waiver and the defects in the conclusion — the waived
// branch previously sat after defects>0, so the waiver was never named.
func TestWaivedConclusionMentionsWaiverWithOpenDefects(t *testing.T) {
	// Version-level waive + an open product defect (the sample's issue 1).
	versionWaived := v3SampleSnapshot()
	versionWaived.ClosureKind = delivery.ReportClosureKindWaived
	versionWaived.Unverified = true
	text := string(renderV3(t, versionWaived, "", allSections()))
	conclusion := conclusionText(t, text)
	if !strings.Contains(conclusion, "豁免") {
		t.Fatalf("waived version with open defects conclusion never mentions the waiver: %q", conclusion)
	}
	if !strings.Contains(conclusion, "1 个未关闭产品缺陷") {
		t.Fatalf("waived version with open defects conclusion never mentions the defect count: %q", conclusion)
	}

	// Requirement-level waiver (normal close) + an open product defect.
	reqWaived := v3SampleSnapshot()
	reqWaived.ClosureKind = delivery.ReportClosureKindNormal
	reqWaived.Unverified = true
	reqWaived.Requirements[0].Waived = true
	reqWaived.Requirements[0].WaiverReason = "AC-2 延期"
	reqWaived.Requirements[0].WaiverOperator = "9001"
	reqText := string(renderV3(t, reqWaived, "", allSections()))
	reqConclusion := conclusionText(t, reqText)
	if !strings.Contains(reqConclusion, "豁免") || !strings.Contains(reqConclusion, "1 个未关闭产品缺陷") {
		t.Fatalf("requirement-waived version with open defects conclusion must mention both waiver and defects: %q", reqConclusion)
	}
}

// conclusionText extracts the deterministic overview conclusion paragraph.
func conclusionText(t *testing.T, html string) string {
	t.Helper()
	const marker = `class="conclusion"`
	start := strings.Index(html, marker)
	if start < 0 {
		t.Fatal("no conclusion paragraph in the report")
	}
	seg := html[start:]
	gt := strings.Index(seg, ">")
	if gt < 0 {
		t.Fatal("malformed conclusion paragraph")
	}
	seg = seg[gt+1:]
	end := strings.Index(seg, "</p>")
	if end < 0 {
		t.Fatal("unterminated conclusion paragraph")
	}
	return seg[:end]
}

// TestACMatrixTruncationNoteShowsDisplayedCount pins fix #2 (七.6): the AC matrix
// truncation note states the ACTUAL displayed count vs the full pre-cap total.
// The snapshot is built THROUGH SummarizeACCoverage (the production derivation),
// not hand-set, so a 501-AC matrix capped at 500 renders "显示 500 条，截断前共
// 501 条" — never the self-contradictory "显示 501 条，截断前共 501 条" above a
// 500-row table.
func TestACMatrixTruncationNoteShowsDisplayedCount(t *testing.T) {
	total := delivery.MaxReportACs + 1
	criteria := make([]delivery.ACCriterionSnapshot, 0, total)
	for i := 0; i < total; i++ {
		criteria = append(criteria, delivery.ACCriterionSnapshot{
			ID: fmt.Sprintf("AC-%03d", i+1), Given: "g", When: "w", Then: "t",
		})
	}
	matrix := deliveryapp.BuildACVerificationMatrix(101, criteria, nil)
	coverage := deliveryapp.SummarizeACCoverage([]delivery.RequirementACCoverageSnapshot{
		{RequirementID: 101, ACs: matrix},
	})
	snapshot := v3SampleSnapshot()
	snapshot.Coverage.ACVerification = coverage

	text := string(renderV3(t, snapshot, "", allSections()))
	if !strings.Contains(text, `data-ac-truncated="true"`) {
		t.Fatal("the over-cap AC matrix did not render the truncation aside")
	}
	want := fmt.Sprintf("显示 %d 条，截断前共 %d 条", delivery.MaxReportACs, total)
	if !strings.Contains(text, want) {
		t.Fatalf("AC truncation note must state %q (displayed < total); got note area:\n%s",
			want, truncationAside(text, "data-ac-truncated"))
	}
	// The self-contradictory full-total-as-displayed wording must be gone.
	bad := fmt.Sprintf("显示 %d 条，截断前共 %d 条", total, total)
	if strings.Contains(text, bad) {
		t.Fatalf("AC truncation note still claims the full total as the displayed count (%q)", bad)
	}
}

// truncationAside extracts the aside element carrying the given marker attribute
// so a failure message shows the actual note text.
func truncationAside(text, marker string) string {
	start := strings.Index(text, marker)
	if start < 0 {
		return "(aside not found)"
	}
	seg := text[start:]
	if end := strings.Index(seg, "</aside>"); end >= 0 {
		return seg[:end]
	}
	return seg
}

// TestDefectCardEvidenceTruncatedDiscloses pins fix #6 (art.7.6): a card whose
// representative evidence was reduced by the aggregate card budget discloses
// 证据超出显示上限（已截断）。 — both when fully starved (no rows) and when partially
// shown — instead of falsely claiming 无代表 run 证据. A card with genuinely no
// evidence still renders 无代表 run 证据.
func TestDefectCardEvidenceTruncatedDiscloses(t *testing.T) {
	// Fully starved: evidence exists but the budget gave the card zero rows.
	starved := v3SampleSnapshot()
	starved.Issues[0].RepresentativeEvidence = nil
	starved.Issues[0].RepresentativeEvidenceTruncated = true
	text := string(renderV3(t, starved, "", allSections()))
	if strings.Contains(text, "无代表 run 证据") {
		t.Fatal("a budget-starved card falsely claimed 无代表 run 证据 while evidence exists")
	}
	if !strings.Contains(text, "证据超出显示上限（已截断）。") || !strings.Contains(text, `data-evidence-truncated="true"`) {
		t.Fatal("a budget-starved card did not disclose the evidence truncation")
	}

	// Partially shown: some rows displayed plus the truncation disclosure.
	partial := v3SampleSnapshot()
	partial.Issues[0].RepresentativeEvidenceTruncated = true
	partialText := string(renderV3(t, partial, "", allSections()))
	if !strings.Contains(partialText, `href="/api/versions/7/evidence/41/content"`) {
		t.Fatal("the partially-shown card lost its viewable evidence link")
	}
	if !strings.Contains(partialText, "证据超出显示上限（已截断）。") {
		t.Fatal("the partially-shown card did not disclose the evidence truncation")
	}

	// Genuinely no evidence: the honest empty statement stays.
	none := v3SampleSnapshot()
	none.Issues[0].RepresentativeEvidence = nil
	none.Issues[0].RepresentativeEvidenceTruncated = false
	noneText := string(renderV3(t, none, "", allSections()))
	if !strings.Contains(noneText, "无代表 run 证据") {
		t.Fatal("a card with genuinely no evidence must still state 无代表 run 证据")
	}
	if strings.Contains(noneText, "证据超出显示上限") {
		t.Fatal("a card with genuinely no evidence falsely disclosed a truncation")
	}
}
