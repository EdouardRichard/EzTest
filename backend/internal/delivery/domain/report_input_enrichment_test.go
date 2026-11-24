package delivery

import (
	"strings"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestRepresentativeRunIDRule pins the deterministic representative-run rule of
// a merged/deduplicated issue (SC-G2 I4): the newest FAILED linked run wins;
// without a failed run the newest linked run wins; no runs -> 0. The rule never
// mixes runs and never depends on ref order.
func TestRepresentativeRunIDRule(t *testing.T) {
	cases := []struct {
		name   string
		ids    []int64
		failed map[int64]bool
		want   int64
	}{
		{"newest failed wins over newer passed", []int64{5001, 5005, 5007}, map[int64]bool{5001: true, 5005: true}, 5005},
		{"single failed", []int64{5001}, map[int64]bool{5001: true}, 5001},
		{"no failed falls back to newest", []int64{5002, 5007}, map[int64]bool{}, 5007},
		{"empty", nil, nil, 0},
		{"order independent", []int64{5007, 5001, 5005}, map[int64]bool{5005: true, 5001: true}, 5005},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RepresentativeRunID(tc.ids, tc.failed); got != tc.want {
				t.Fatalf("RepresentativeRunID(%v, %v) = %d, want %d", tc.ids, tc.failed, got, tc.want)
			}
		})
	}
}

// TestFailureExplanationText pins the deterministic human-readable explanation
// of every report-visible non-defect failure class (SC-G2 C1/M3): a human reads
// WHY the case did not pass without knowing any internal code.
func TestFailureExplanationText(t *testing.T) {
	cases := []struct {
		status string
		class  string
		cause  string
	}{
		{string(execution.CaseRunBlocked), FailureClassBlocked, TerminalCauseExecutorCapability},
		{string(execution.CaseRunBlocked), FailureClassBlocked, ""},
		{string(execution.CaseRunInconclusive), string(execution.FailureClassUnknown), ""},
		{string(execution.CaseRunFailed), string(execution.FailureClassEnvironment), ""},
		{string(execution.CaseRunFailed), FailureClassAutomation, ""},
		{string(execution.CaseRunFailed), FailureClassExpectedFailure, ""},
		{string(execution.CaseRunFailed), string(execution.FailureClassSession), ""},
		{string(execution.CaseRunFailed), string(execution.FailureClassModel), ""},
		{string(execution.CaseRunFailed), string(execution.FailureClassTimeout), ""},
		{string(execution.CaseRunFailed), string(execution.FailureClassExecutorLost), ""},
		{string(execution.CaseRunFailed), string(execution.FailureClassApplication), ""},
		{string(execution.CaseRunFailed), string(execution.FailureClassUnknown), ""},
		{string(execution.CaseRunFailed), "", ""},
	}
	for _, tc := range cases {
		text := FailureExplanationText(tc.status, tc.class, tc.cause)
		if strings.TrimSpace(text) == "" {
			t.Fatalf("FailureExplanationText(%q, %q, %q) is empty; every class needs a human explanation", tc.status, tc.class, tc.cause)
		}
	}
	// The executor-capability block must say it is not a product defect.
	blocked := FailureExplanationText(string(execution.CaseRunBlocked), FailureClassBlocked, TerminalCauseExecutorCapability)
	if !strings.Contains(blocked, "不是产品缺陷") {
		t.Fatalf("executor capability explanation = %q, want it to state this is not a product defect", blocked)
	}
	// A budget-caused inconclusive run names the budget so the human knows the
	// next step (rerun with budget), never a generic shrug.
	budget := FailureExplanationText(string(execution.CaseRunInconclusive), string(execution.FailureClassUnknown), execution.TerminalCauseTurnBudget)
	if !strings.Contains(budget, "轮次") {
		t.Fatalf("turn budget explanation = %q, want it to name the turn budget", budget)
	}
	// An inconclusive run without a cause still says the evidence was insufficient.
	inconclusive := FailureExplanationText(string(execution.CaseRunInconclusive), "", "")
	if !strings.Contains(inconclusive, "证据不足") {
		t.Fatalf("inconclusive explanation = %q, want it to state insufficient evidence", inconclusive)
	}
}

// TestEvidenceDigestUsesFullCarrier pins that the fingerprint digest consumes
// the FULL evidence sha carrier, never the capped display list (SC-G7 R-G2
// causal completeness): a run whose display is capped to 1 row but whose
// carrier holds 3 shas digests all 3.
func TestEvidenceDigestUsesFullCarrier(t *testing.T) {
	snapshot := ReportSnapshot{
		Kind: ReportKindVersion, VersionID: 7,
		CaseRuns: []CaseRunSnapshot{{
			ID: 5001, VersionID: 7, WaveID: 11, CaseVersionID: 3001, Status: "failed",
			Evidence: []EvidenceSnapshot{
				{ID: 7001, CaseRunID: 5001, SHA256: strings.Repeat("a", 64)},
			},
			EvidenceSHA256s: []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)},
		}},
	}
	baseline := Fingerprint(snapshot)
	if baseline == "" {
		t.Fatal("empty fingerprint")
	}
	// Changing a carrier sha that is NOT in the display list must move the
	// fingerprint (the digest covers the full set).
	snapshot.CaseRuns[0].EvidenceSHA256s[2] = strings.Repeat("9", 64)
	if Fingerprint(snapshot) == baseline {
		t.Fatal("changing a beyond-display carrier sha did not change the fingerprint")
	}
	// Changing only the display list (carrier untouched) must NOT move it.
	snapshot.CaseRuns[0].EvidenceSHA256s[2] = strings.Repeat("c", 64)
	snapshot.CaseRuns[0].Evidence[0].SHA256 = strings.Repeat("8", 64)
	if Fingerprint(snapshot) != baseline {
		t.Fatal("the capped display list leaked into the fingerprint digest")
	}
	// The carrier count (not the display count) is the digest count.
	snapshot.CaseRuns[0].EvidenceSHA256s = append(snapshot.CaseRuns[0].EvidenceSHA256s, strings.Repeat("c", 64))
	if Fingerprint(snapshot) == baseline {
		t.Fatal("adding a carrier row (multiplicity) did not change the fingerprint count")
	}
}

// TestEvidenceTotalCountAndTruncated pins the display total/truncation derived
// from the full carrier: total counts every evidence row, truncation is explicit.
func TestEvidenceTotalCountAndTruncated(t *testing.T) {
	run := CaseRunSnapshot{
		Evidence:        []EvidenceSnapshot{{ID: 1, SHA256: "a"}},
		EvidenceSHA256s: []string{"a", "b", "c"},
	}
	if run.EvidenceTotalCount() != 3 || !run.EvidenceTruncated() {
		t.Fatalf("total/truncated = %d/%v, want 3/true", run.EvidenceTotalCount(), run.EvidenceTruncated())
	}
	// A synthetic snapshot without a carrier derives the total from the list.
	legacy := CaseRunSnapshot{Evidence: []EvidenceSnapshot{{ID: 1, SHA256: "a"}, {ID: 2, SHA256: "b"}}}
	if legacy.EvidenceTotalCount() != 2 || legacy.EvidenceTruncated() {
		t.Fatalf("legacy total/truncated = %d/%v, want 2/false", legacy.EvidenceTotalCount(), legacy.EvidenceTruncated())
	}
}

// TestTruncationSectionsIncludeEvidence pins the canonical evidence truncation
// marker order (issues, waves, case_runs, evidence).
func TestTruncationSectionsIncludeEvidence(t *testing.T) {
	snapshot := ReportSnapshot{
		TruncatedSections: []string{ReportTruncationEvidence, ReportTruncationIssues},
	}
	capped := CapReportSnapshot(snapshot)
	want := []string{ReportTruncationIssues, ReportTruncationEvidence}
	if len(capped.TruncatedSections) != len(want) {
		t.Fatalf("sections = %v, want %v", capped.TruncatedSections, want)
	}
	for index := range want {
		if capped.TruncatedSections[index] != want[index] {
			t.Fatalf("sections = %v, want %v", capped.TruncatedSections, want)
		}
	}
}

// carrierSnapshot is a report snapshot whose evidence sha carrier IS populated
// (the production path), used to exercise the fingerprint sensitivity matrix
// through the carrier branch rather than the nil-carrier display fallback (#6).
func carrierSnapshot() ReportSnapshot {
	shas := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	return ReportSnapshot{
		Kind: ReportKindVersion, VersionID: 7,
		Requirements: []RequirementSnapshot{{
			ID: 101, VersionID: 7, Title: "R", Status: "closed",
			UpdatedAt: reportFingerprintTime, ACHash: strings.Repeat("c", 64),
			UserStory:      UserStorySnapshot{AsA: "运营", IWant: "导出", SoThat: "对账"},
			SutDescription: "订单系统",
		}},
		CaseRuns: []CaseRunSnapshot{{
			ID: 5001, VersionID: 7, WaveID: 11, CaseVersionID: 3001, Status: "failed",
			FailureClass: "application", TerminalCause: "assertion_failed",
			CaseTitle: "导出订单", Severity: "major", Category: "api",
			Evidence:        []EvidenceSnapshot{{ID: 7001, CaseRunID: 5001, SHA256: shas[0]}},
			EvidenceSHA256s: shas,
			CaseDescription: "验证导出", CasePreconditions: "已登录", SUTDescription: "订单系统 Web",
			Steps: []CaseStepSnapshot{{Seq: 1, Action: "点击导出", TargetDescription: "导出按钮"}},
		}},
		Issues: []IssueSnapshot{{
			ID: 8001, VersionID: 7, Title: "I", Status: IssueStatusOpen, Category: "api", Severity: "major",
			DedupeKey: strings.Repeat("f", 32), CaseRunIDs: []int64{5001},
		}},
		SUTs: []SUTSnapshot{{ID: 700, SUTID: 700, SUTName: "订单系统", SUTDescription: "内部订单管理系统"}},
	}
}

// TestFingerprintSensitivityCarrierPopulated (#6): the sensitivity matrix through
// the PRODUCTION carrier branch — conclusion inputs move the fingerprint,
// updated_at does not, and the digest consumes the carrier (not the display list).
func TestFingerprintSensitivityCarrierPopulated(t *testing.T) {
	base := carrierSnapshot()
	baseline := Fingerprint(base)
	if baseline == "" {
		t.Fatal("empty fingerprint")
	}
	mutations := []struct {
		name    string
		mutate  func(*ReportSnapshot)
		changed bool
	}{
		{"verdict", func(s *ReportSnapshot) { s.CaseRuns[0].Status = "blocked" }, true},
		{"terminal_cause", func(s *ReportSnapshot) { s.CaseRuns[0].TerminalCause = "turn_budget" }, true},
		{"carrier sha (digest content)", func(s *ReportSnapshot) { s.CaseRuns[0].EvidenceSHA256s[1] = strings.Repeat("9", 64) }, true},
		{"carrier count", func(s *ReportSnapshot) {
			s.CaseRuns[0].EvidenceSHA256s = append(s.CaseRuns[0].EvidenceSHA256s, strings.Repeat("b", 64))
		}, true},
		{"ac_hash", func(s *ReportSnapshot) { s.Requirements[0].ACHash = strings.Repeat("9", 64) }, true},
		{"issue dedupe_key", func(s *ReportSnapshot) { s.Issues[0].DedupeKey = strings.Repeat("9", 32) }, true},
		{"requirement updated_at alone", func(s *ReportSnapshot) { s.Requirements[0].UpdatedAt = reportFingerprintTime.Add(time.Hour) }, false},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			snapshot := carrierSnapshot()
			m.mutate(&snapshot)
			got := Fingerprint(snapshot)
			if m.changed && got == baseline {
				t.Fatalf("%s changed a conclusion input but the fingerprint did not move", m.name)
			}
			if !m.changed && got != baseline {
				t.Fatalf("%s is not a conclusion input but the fingerprint moved", m.name)
			}
		})
	}
}

// TestNLContextFingerprintNeutral (#13): the card natural-language context is
// display metadata — changing it must NOT move the fingerprint (the frozen
// case/revision IDs already cover content changes).
func TestNLContextFingerprintNeutral(t *testing.T) {
	base := carrierSnapshot()
	baseline := Fingerprint(base)
	snapshot := carrierSnapshot()
	snapshot.CaseRuns[0].CaseDescription = "完全不同的描述"
	snapshot.CaseRuns[0].CasePreconditions = "完全不同的前置"
	snapshot.CaseRuns[0].SUTDescription = "完全不同的系统"
	snapshot.CaseRuns[0].Steps = []CaseStepSnapshot{{Seq: 9, Action: "别的动作", TargetDescription: "别的目标"}}
	snapshot.Requirements[0].UserStory = UserStorySnapshot{AsA: "别的角色", IWant: "别的愿望", SoThat: "别的理由"}
	snapshot.Requirements[0].SutDescription = "别的系统描述"
	snapshot.SUTs[0].SUTDescription = "别的 SUT 介绍"
	if got := Fingerprint(snapshot); got != baseline {
		t.Fatalf("NL context changed the fingerprint: %s != %s (NL must be display-only)", got, baseline)
	}
}
