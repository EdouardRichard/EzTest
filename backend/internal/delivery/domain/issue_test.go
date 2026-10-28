package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// issueTestDedupeKey returns a deterministic 64-hex dedupe key.
func issueTestDedupeKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// issueTestValidDraft is a complete AI defect draft every test can mutate.
func issueTestValidDraft() IssueDraft {
	caseNodeID := int64(9001)
	caseRunID := int64(777)
	return IssueDraft{
		DedupeKey:    issueTestDedupeKey("defect"),
		Title:        "登录按钮无响应",
		Description:  "点击登录后页面停留在原处",
		ReproSteps:   "1. 打开登录页\n2. 点击登录",
		Expected:     "进入首页",
		Actual:       "页面无变化",
		Severity:     SeverityMajor,
		Confidence:   ConfidenceHigh,
		Category:     CategoryDefect,
		FailureClass: FailureClassApplication,
		CaseRunID:    &caseRunID,
		CaseNodeID:   &caseNodeID,
		EvidenceIDs:  []int64{31},
	}
}

// issueTestRequirementGapDraft is a complete AI requirement-gap draft without a
// case (spec §5.4: a requirement gap must reference a requirement, a case is
// optional).
func issueTestRequirementGapDraft() IssueDraft {
	requirementID := int64(55)
	return IssueDraft{
		DedupeKey:      issueTestDedupeKey("requirement-gap"),
		Title:          "缺少密码找回需求",
		Severity:       SeverityMajor,
		Confidence:     ConfidenceMedium,
		Category:       CategoryRequirementGap,
		RequirementIDs: []int64{requirementID},
	}
}

// issueTestInvalid asserts err is an invalid domain error with reason.
func issueTestInvalid(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if domainErr.Kind != KindInvalid {
		t.Fatalf("error kind = %q, want %q (%v)", domainErr.Kind, KindInvalid, err)
	}
	if domainErr.Reason != reason {
		t.Fatalf("error reason = %q, want %q (%v)", domainErr.Reason, reason, err)
	}
}

func TestIssueStateTransitions(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{StatusOpen, StatusConfirmed, true},
		{StatusOpen, StatusDismissed, true},
		{StatusOpen, StatusOpen, false},
		{StatusConfirmed, StatusDismissed, false},
		{StatusConfirmed, StatusOpen, false},
		{StatusDismissed, StatusConfirmed, false},
		{StatusDismissed, StatusOpen, false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
	for _, status := range []string{StatusOpen, StatusConfirmed, StatusDismissed} {
		want := status != StatusOpen
		if got := IsTerminalStatus(status); got != want {
			t.Errorf("IsTerminalStatus(%q) = %v, want %v", status, got, want)
		}
	}
	if IsTerminalStatus("bogus") {
		t.Error("IsTerminalStatus(bogus) = true, want false")
	}
}

func TestConfidenceRank(t *testing.T) {
	if ConfidenceRank(ConfidenceHigh) <= ConfidenceRank(ConfidenceMedium) {
		t.Fatal("high confidence must outrank medium")
	}
	if ConfidenceRank(ConfidenceMedium) <= ConfidenceRank(ConfidenceLow) {
		t.Fatal("medium confidence must outrank low")
	}
	if ConfidenceRank("bogus") != 0 {
		t.Fatalf("ConfidenceRank(bogus) = %d, want 0", ConfidenceRank("bogus"))
	}
}

func TestValidateIssueRef(t *testing.T) {
	valid := []struct {
		kind, id string
	}{
		{RefKindCaseRun, "777"},
		{RefKindCaseNode, "9001"},
		{RefKindStepResult, "12"},
		{RefKindEvidence, "31"},
		{RefKindRequirement, "55"},
		{RefKindBatch, "700"},
		{RefKindCodeAnchor, "123"},
		{RefKindCommit, strings.Repeat("a", 40)},
	}
	for _, tc := range valid {
		if err := ValidateIssueRef(tc.kind, tc.id); err != nil {
			t.Errorf("ValidateIssueRef(%q, %q) = %v, want nil", tc.kind, tc.id, err)
		}
	}

	invalid := []struct {
		kind, id string
	}{
		{"bogus", "1"},
		{RefKindCaseRun, ""},
		{RefKindCaseRun, "0"},
		{RefKindCaseRun, "-3"},
		{RefKindCaseRun, "abc"},
		{RefKindEvidence, "12.5"},
		{RefKindCommit, strings.Repeat("A", 40)},
		{RefKindCommit, strings.Repeat("a", 39)},
		{RefKindCommit, "z" + strings.Repeat("a", 39)},
	}
	for _, tc := range invalid {
		issueTestInvalid(t, ValidateIssueRef(tc.kind, tc.id), ReasonIssueRefInvalid)
	}
}

func TestNormalizeIssueRefsSortsAndDeduplicates(t *testing.T) {
	refs := []IssueRef{
		{RefKind: RefKindEvidence, RefID: "31"},
		{RefKind: RefKindCaseRun, RefID: "777"},
		{RefKind: RefKindEvidence, RefID: "31"},
		{RefKind: RefKindCaseNode, RefID: "9001"},
		{RefKind: RefKindCaseRun, RefID: "777"},
	}
	got := NormalizeIssueRefs(refs)
	want := []IssueRef{
		{RefKind: RefKindCaseNode, RefID: "9001"},
		{RefKind: RefKindCaseRun, RefID: "777"},
		{RefKind: RefKindEvidence, RefID: "31"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeIssueRefs = %+v, want %+v", got, want)
	}
	if len(NormalizeIssueRefs(nil)) != 0 {
		t.Fatal("NormalizeIssueRefs(nil) must be empty")
	}
}

func TestValidateIssueDraft(t *testing.T) {
	if err := ValidateIssueDraft(issueTestValidDraft()); err != nil {
		t.Fatalf("valid defect draft rejected: %v", err)
	}
	if err := ValidateIssueDraft(issueTestRequirementGapDraft()); err != nil {
		t.Fatalf("valid requirement-gap draft rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*IssueDraft)
		field  string
	}{
		{"missing dedupe", func(d *IssueDraft) { d.DedupeKey = "" }, "dedupeKey"},
		{"uppercase dedupe", func(d *IssueDraft) { d.DedupeKey = strings.ToUpper(strings.Repeat("a", 64)) }, "dedupeKey"},
		{"short dedupe", func(d *IssueDraft) { d.DedupeKey = "abc" }, "dedupeKey"},
		{"missing title", func(d *IssueDraft) { d.Title = "  " }, "title"},
		{"long title", func(d *IssueDraft) { d.Title = strings.Repeat("x", 201) }, "title"},
		{"missing severity", func(d *IssueDraft) { d.Severity = "" }, "severity"},
		{"bad severity", func(d *IssueDraft) { d.Severity = "urgent" }, "severity"},
		{"missing confidence", func(d *IssueDraft) { d.Confidence = "" }, "confidence"},
		{"bad confidence", func(d *IssueDraft) { d.Confidence = "certain" }, "confidence"},
		{"missing failure class", func(d *IssueDraft) { d.FailureClass = "" }, "failureClass"},
		{"non-application failure class", func(d *IssueDraft) { d.FailureClass = FailureClassEnvironment }, "failureClass"},
		{"missing case node", func(d *IssueDraft) { d.CaseNodeID = nil }, "caseNodeId"},
		{"zero case node", func(d *IssueDraft) { zero := int64(0); d.CaseNodeID = &zero }, "caseNodeId"},
		{"zero case run", func(d *IssueDraft) { zero := int64(0); d.CaseRunID = &zero }, "caseRunId"},
		{"zero evidence", func(d *IssueDraft) { d.EvidenceIDs = []int64{0} }, "evidenceIds"},
		{"zero merge target", func(d *IssueDraft) { zero := int64(0); d.MergeIntoIssueID = &zero }, "mergeIntoIssueId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft := issueTestValidDraft()
			tc.mutate(&draft)
			err := ValidateIssueDraft(draft)
			issueTestInvalid(t, err, ReasonInvalidArgument)
			var domainErr *Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("error type = %T", err)
			}
			if domainErr.Field != tc.field {
				t.Fatalf("field = %q, want %q (%v)", domainErr.Field, tc.field, err)
			}
		})
	}

	t.Run("requirement gap without requirement", func(t *testing.T) {
		draft := issueTestRequirementGapDraft()
		draft.RequirementIDs = nil
		issueTestInvalid(t, ValidateIssueDraft(draft), ReasonInvalidArgument)
	})
	t.Run("defect without any case reference", func(t *testing.T) {
		draft := issueTestValidDraft()
		draft.CaseRunID = nil
		draft.CaseNodeID = nil
		issueTestInvalid(t, ValidateIssueDraft(draft), ReasonInvalidArgument)
	})
}

// TestValidateIssueDraftRequirementGapFailureClass pins spec §5.4/DDL: a
// requirement gap may leave failure_class empty but, when present, it only has to
// be a valid failure class (the defect-only "application" rule must not leak).
func TestValidateIssueDraftRequirementGapFailureClass(t *testing.T) {
	draft := issueTestRequirementGapDraft()
	draft.FailureClass = FailureClassEnvironment
	if err := ValidateIssueDraft(draft); err != nil {
		t.Fatalf("requirement-gap draft with failure class %q rejected: %v", draft.FailureClass, err)
	}

	draft.FailureClass = "bogus"
	issueTestInvalid(t, ValidateIssueDraft(draft), ReasonInvalidArgument)
}

func TestMergeOccurrence(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	base := Issue{
		ID:              500,
		VersionID:       1,
		Status:          StatusOpen,
		Confidence:      ConfidenceLow,
		Actual:          "旧现象",
		RecurrenceCount: 0,
		Revision:        3,
		DismissedReason: "",
	}

	merged := MergeOccurrence(base, ConfidenceHigh, "新现象", 44, now)
	if merged.RecurrenceCount != 1 {
		t.Errorf("recurrence_count = %d, want 1", merged.RecurrenceCount)
	}
	if merged.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %q, want high", merged.Confidence)
	}
	if merged.Actual != "新现象" {
		t.Errorf("actual = %q, want the new actual", merged.Actual)
	}
	if merged.Revision != 4 {
		t.Errorf("revision = %d, want 4", merged.Revision)
	}
	if merged.Status != StatusOpen {
		t.Errorf("status = %q, want open", merged.Status)
	}
	if merged.UpdatedBy != 44 || !merged.UpdatedAt.Equal(now) {
		t.Errorf("updated = (%d, %s), want (44, %s)", merged.UpdatedBy, merged.UpdatedAt, now)
	}

	downgraded := MergeOccurrence(merged, ConfidenceLow, "", 45, now)
	if downgraded.Confidence != ConfidenceHigh {
		t.Errorf("confidence was downgraded to %q", downgraded.Confidence)
	}
	if downgraded.Actual != "新现象" {
		t.Errorf("empty actual overwrote the stored actual: %q", downgraded.Actual)
	}
	if downgraded.RecurrenceCount != 2 {
		t.Errorf("recurrence_count = %d, want 2", downgraded.RecurrenceCount)
	}

	terminal := base
	terminal.Status = StatusConfirmed
	confirmed := MergeOccurrence(terminal, ConfidenceMedium, "", 44, now)
	if confirmed.Status != StatusConfirmed {
		t.Errorf("terminal status changed to %q", confirmed.Status)
	}
	if confirmed.RecurrenceCount != 1 || confirmed.Revision != 4 {
		t.Errorf("terminal merge = recurrence %d revision %d, want 1/4",
			confirmed.RecurrenceCount, confirmed.Revision)
	}

	dismissed := base
	dismissed.Status = StatusDismissed
	dismissed.DismissedReason = "环境问题"
	recurring := MergeOccurrence(dismissed, ConfidenceMedium, "again", 44, now)
	if recurring.Status != StatusDismissed || recurring.DismissedReason != "环境问题" {
		t.Errorf("dismissed issue reopened: %+v", recurring)
	}
}

func TestIssueFieldValidators(t *testing.T) {
	title, err := ValidateTitle("  登录失败  ")
	if err != nil {
		t.Fatalf("ValidateTitle(valid) = %v", err)
	}
	if title != "登录失败" {
		t.Errorf("title = %q, want trimmed", title)
	}
	if _, err := ValidateTitle("   "); err != nil {
		issueTestInvalid(t, err, ReasonInvalidArgument)
	} else {
		t.Fatal("empty title was accepted")
	}
	if _, err := ValidateTitle(strings.Repeat("x", 201)); err != nil {
		issueTestInvalid(t, err, ReasonInvalidArgument)
	} else {
		t.Fatal("over-long title was accepted")
	}

	for _, severity := range []string{SeverityBlocker, SeverityMajor, SeverityMinor, SeverityInfo} {
		if err := ValidateSeverity(severity); err != nil {
			t.Errorf("ValidateSeverity(%q) = %v", severity, err)
		}
	}
	issueTestInvalid(t, ValidateSeverity(""), ReasonInvalidArgument)
	issueTestInvalid(t, ValidateSeverity("urgent"), ReasonInvalidArgument)

	for _, category := range []string{CategoryDefect, CategoryRequirementGap, CategoryOther} {
		if err := ValidateCategory(category); err != nil {
			t.Errorf("ValidateCategory(%q) = %v", category, err)
		}
	}
	issueTestInvalid(t, ValidateCategory("bug"), ReasonInvalidArgument)

	for _, confidence := range []string{ConfidenceHigh, ConfidenceMedium, ConfidenceLow} {
		if err := ValidateConfidence(confidence); err != nil {
			t.Errorf("ValidateConfidence(%q) = %v", confidence, err)
		}
	}
	issueTestInvalid(t, ValidateConfidence("certain"), ReasonInvalidArgument)
}
