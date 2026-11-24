package adapters

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeWaiverFacts serves the version waiver fact read port (T10). It records the
// kinds it was asked for and can inject a loud error to prove the read model
// fails closed rather than silently dropping the waiver context.
type fakeWaiverFacts struct {
	facts []version.Fact
	err   error
	kinds []string
}

func (f *fakeWaiverFacts) Current(_ context.Context, _ int64, kind string) ([]version.Fact, error) {
	f.kinds = append(f.kinds, kind)
	if f.err != nil {
		return nil, f.err
	}
	return f.facts, nil
}

func versionWaiverFact(reason, actor, waivedAt string) version.Fact {
	return version.Fact{
		VersionID: 7,
		Kind:      version.FactKindWaiver,
		Key:       "version",
		Payload: map[string]any{
			"scope":        "version",
			"version_id":   "7",
			"reason":       reason,
			"actor_id":     actor,
			"closure_kind": version.ClosureKindWaived,
			"unverified":   true,
			"waived_at":    waivedAt,
		},
	}
}

func requirementWaiverFact(requirementID int64, reason, actor, waivedAt string) version.Fact {
	return version.Fact{
		VersionID: 7,
		Kind:      version.FactKindWaiver,
		Key:       "requirement:" + strconv.FormatInt(requirementID, 10),
		Payload: map[string]any{
			"scope":          "requirement",
			"version_id":     "7",
			"requirement_id": strconv.FormatInt(requirementID, 10),
			"reason":         reason,
			"actor_id":       actor,
			"waived_at":      waivedAt,
		},
	}
}

// TestRequirementWaiverFactProjectedIndependentOfClosureKind pins final fix wave
// #1 (SC-G11): a requirement-level waiver is projected onto the requirement
// snapshot (reason/operator/time from the kind=waiver, key=requirement:<id> fact)
// INDEPENDENT of the version closure_kind. A requirement waived then normally
// closed (closure_kind=normal) still overrode completeness, so the report must
// disclose it — the version-level waiver fields stay empty (no version waive).
func TestRequirementWaiverFactProjectedIndependentOfClosureKind(t *testing.T) {
	deps := newEnrichmentFixture()
	// The version closed NORMALLY (not waive-closed); requirement 101 stays waived.
	view := deps.Versions.(*fakeVersions).views[7]
	view.ClosureKind = version.ClosureKindNormal
	view.Unverified = true // a normal close with a waived requirement is unverified (#1)
	deps.Versions.(*fakeVersions).views[7] = view
	deps.WaiverFacts = &fakeWaiverFacts{facts: []version.Fact{
		requirementWaiverFact(101, "AC-1 降级到下一版本", "9001", "2026-11-20T08:00:00Z"),
	}}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	var req *delivery.RequirementSnapshot
	for i := range snapshot.Requirements {
		if snapshot.Requirements[i].ID == 101 {
			req = &snapshot.Requirements[i]
		}
	}
	if req == nil || !req.Waived {
		t.Fatalf("requirement 101 missing or not waived: %+v", req)
	}
	if req.WaiverReason != "AC-1 降级到下一版本" || req.WaiverOperator != "9001" {
		t.Fatalf("requirement waiver reason/operator = %q/%q, want the fact values (independent of closure_kind)",
			req.WaiverReason, req.WaiverOperator)
	}
	if want := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC); !req.WaivedAt.Equal(want) {
		t.Fatalf("requirement waived_at = %v, want %v", req.WaivedAt, want)
	}
	// No version-level waive: the version waiver fields stay empty.
	if snapshot.WaiverReason != "" || snapshot.WaiverOperator != "" {
		t.Fatalf("version-level waiver populated for a normal close: %q/%q", snapshot.WaiverReason, snapshot.WaiverOperator)
	}

	// Mutation: no read port → the requirement waiver context is empty (the
	// renderer then discloses 豁免理由不可用, never a silent claim of no waiver).
	deps.WaiverFacts = nil
	empty, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot without port: %v", err)
	}
	for _, r := range empty.Requirements {
		if r.ID == 101 && (r.WaiverReason != "" || r.WaiverOperator != "") {
			t.Fatalf("requirement waiver context populated without a read port: %q/%q", r.WaiverReason, r.WaiverOperator)
		}
	}
}

// TestRequirementWaiverFactLatestWins pins the repeated-waiver case at the
// requirement scope: several key=requirement:<id> facts coexist; the most recent
// waived_at is the current accountability context.
func TestRequirementWaiverFactLatestWins(t *testing.T) {
	deps := newEnrichmentFixture()
	deps.WaiverFacts = &fakeWaiverFacts{facts: []version.Fact{
		requirementWaiverFact(101, "旧理由", "9001", "2026-11-20T08:00:00Z"),
		requirementWaiverFact(101, "新理由", "9002", "2026-11-21T08:00:00Z"),
	}}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	for _, r := range snapshot.Requirements {
		if r.ID == 101 {
			if r.WaiverReason != "新理由" || r.WaiverOperator != "9002" {
				t.Fatalf("latest requirement waiver not selected: %q/%q", r.WaiverReason, r.WaiverOperator)
			}
			return
		}
	}
	t.Fatal("requirement 101 missing")
}

// TestRequirementWaiverNotProjectedWhenNotWaived pins 五.9: the requirement waiver
// disclosure gates on the requirement's CURRENT Waived flag, not on fact presence.
// A reopened requirement (waived=false) shows no waiver context even though its
// append-only historical waiver fact survives.
func TestRequirementWaiverNotProjectedWhenNotWaived(t *testing.T) {
	deps := newEnrichmentFixture()
	requirements := deps.Requirements.(*fakeRequirements)
	view := requirements.views[7][0]
	view.Waived = false
	requirements.views[7][0] = view
	detail := requirements.details[101]
	detail.Waived = false
	requirements.details[101] = detail
	deps.WaiverFacts = &fakeWaiverFacts{facts: []version.Fact{
		requirementWaiverFact(101, "历史豁免", "9001", "2026-11-20T08:00:00Z"),
	}}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	for _, r := range snapshot.Requirements {
		if r.ID == 101 && (r.WaiverReason != "" || r.WaiverOperator != "") {
			t.Fatalf("a reopened (not waived) requirement projected a stale waiver: %q/%q", r.WaiverReason, r.WaiverOperator)
		}
	}
}

// TestWaiverFactProjectedIntoSnapshot pins the T10 read side: a waived version's
// report snapshot carries the waiver reason/operator/time from the kind=waiver,
// key=version fact. Removing the read port leaves them empty (read-side
// existence discrimination, SC-G11).
func TestWaiverFactProjectedIntoSnapshot(t *testing.T) {
	deps := newEnrichmentFixture()
	facts := &fakeWaiverFacts{facts: []version.Fact{versionWaiverFact("AC-2 延期验证", "9001", "2026-11-20T08:00:00Z")}}
	deps.WaiverFacts = facts
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if snapshot.WaiverReason != "AC-2 延期验证" || snapshot.WaiverOperator != "9001" {
		t.Fatalf("waiver reason/operator = %q/%q, want the fact values", snapshot.WaiverReason, snapshot.WaiverOperator)
	}
	if want := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC); !snapshot.WaivedAt.Equal(want) {
		t.Fatalf("waived_at = %v, want %v", snapshot.WaivedAt, want)
	}
	if len(facts.kinds) == 0 || facts.kinds[0] != version.FactKindWaiver {
		t.Fatalf("the read model did not read the waiver fact kind: %v", facts.kinds)
	}

	// Mutation: no read port → no waiver reason/operator (the badge still renders
	// from the closure_kind/unverified columns, which stay populated).
	deps.WaiverFacts = nil
	empty, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot without port: %v", err)
	}
	if empty.WaiverReason != "" || empty.WaiverOperator != "" || !empty.WaivedAt.IsZero() {
		t.Fatalf("waiver context populated without a read port: %q/%q/%v", empty.WaiverReason, empty.WaiverOperator, empty.WaivedAt)
	}
	if empty.ClosureKind != version.ClosureKindWaived || !empty.Unverified {
		t.Fatal("the closure_kind/unverified columns must still populate without the fact port")
	}
}

// TestWaiverFactGatedOnClosureKind pins 五.9: the waiver context is gated on the
// closure_kind COLUMN, not on fact presence. A reopened (normal) version shows no
// waiver even though its append-only historical waiver facts survive.
func TestWaiverFactGatedOnClosureKind(t *testing.T) {
	deps := newEnrichmentFixture()
	deps.Versions.(*fakeVersions).views[7] = func() versionapp.VersionView {
		view := deps.Versions.(*fakeVersions).views[7]
		view.ClosureKind = version.ClosureKindNormal
		view.Unverified = false
		return view
	}()
	deps.WaiverFacts = &fakeWaiverFacts{facts: []version.Fact{versionWaiverFact("历史豁免", "9001", "2026-11-20T08:00:00Z")}}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if snapshot.WaiverReason != "" || snapshot.WaiverOperator != "" {
		t.Fatalf("a reopened normal version projected a stale waiver: %q/%q", snapshot.WaiverReason, snapshot.WaiverOperator)
	}
}

// TestWaiverFactLatestWins pins the repeated-waiver case: several key=version
// facts coexist (each waive appends its own waived_at-discriminated row); the
// most recent is the current accountability context.
func TestWaiverFactLatestWins(t *testing.T) {
	deps := newEnrichmentFixture()
	deps.WaiverFacts = &fakeWaiverFacts{facts: []version.Fact{
		versionWaiverFact("旧理由", "9001", "2026-11-20T08:00:00Z"),
		versionWaiverFact("新理由", "9002", "2026-11-21T08:00:00Z"),
	}}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if snapshot.WaiverReason != "新理由" || snapshot.WaiverOperator != "9002" {
		t.Fatalf("latest waiver not selected: %q/%q", snapshot.WaiverReason, snapshot.WaiverOperator)
	}
}

// TestWaiverFactErrorFailsClosed pins art.7.6: a wired fact port that errors fails
// the snapshot build closed instead of silently rendering a waived report without
// its accountability context.
func TestWaiverFactErrorFailsClosed(t *testing.T) {
	deps := newEnrichmentFixture()
	deps.WaiverFacts = &fakeWaiverFacts{err: errors.New("fact store unavailable")}
	if _, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7); err == nil {
		t.Fatal("a waiver fact read error was silently swallowed")
	}
}

// TestWaiverReasonDurableWhenFactLost pins fix #4 (五.1/五.4): the waiver reason
// comes from the durable version.CloseReason column, so a lost best-effort waiver
// fact (waiver_service tolerates RecordFact failure) still renders the reason.
func TestWaiverReasonDurableWhenFactLost(t *testing.T) {
	deps := newEnrichmentFixture()
	view := deps.Versions.(*fakeVersions).views[7]
	view.CloseReason = "持久豁免理由（durable column）"
	deps.Versions.(*fakeVersions).views[7] = view
	deps.WaiverFacts = nil // fact lost / port unwired
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if snapshot.WaiverReason != "持久豁免理由（durable column）" {
		t.Fatalf("waiver reason = %q, want the durable CloseReason even with a lost fact", snapshot.WaiverReason)
	}
	if snapshot.WaiverOperator != "" {
		t.Fatalf("operator = %q, want empty without a fact (template discloses 豁免上下文不可用)", snapshot.WaiverOperator)
	}
}

// TestWaiverReasonDurableColumnWins pins that the durable column is authoritative
// over the fact's reason copy; the fact supplies only operator/time.
func TestWaiverReasonDurableColumnWins(t *testing.T) {
	deps := newEnrichmentFixture()
	view := deps.Versions.(*fakeVersions).views[7]
	view.CloseReason = "持久理由"
	deps.Versions.(*fakeVersions).views[7] = view
	deps.WaiverFacts = &fakeWaiverFacts{facts: []version.Fact{versionWaiverFact("事实理由", "9001", "2026-11-20T08:00:00Z")}}
	snapshot, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if snapshot.WaiverReason != "持久理由" {
		t.Fatalf("waiver reason = %q, want the durable column to win over the fact copy", snapshot.WaiverReason)
	}
	if snapshot.WaiverOperator != "9001" {
		t.Fatalf("operator = %q, want the fact operator", snapshot.WaiverOperator)
	}
}

// TestWaiverFactMalformedWaivedAtFailsClosed pins art.7.6 (no ignored-error): a
// non-empty malformed waived_at is a data-integrity error, not a silent zero time.
func TestWaiverFactMalformedWaivedAtFailsClosed(t *testing.T) {
	deps := newEnrichmentFixture()
	deps.WaiverFacts = &fakeWaiverFacts{facts: []version.Fact{versionWaiverFact("理由", "9001", "not-a-timestamp")}}
	if _, err := NewReportDataReader(deps).VersionSnapshot(context.Background(), 7); err == nil {
		t.Fatal("a malformed waived_at was silently swallowed")
	}
}

// TestReportDisplayVocabularyMatchesAuthority pins 五.1: the delivery display
// closure-kind constants mirror the version domain authority (the delivery domain
// cannot import the version domain, so the equality is checked here at the
// adapters boundary), and the run-status constants derive from execution.
func TestReportDisplayVocabularyMatchesAuthority(t *testing.T) {
	if delivery.ReportClosureKindWaived != version.ClosureKindWaived {
		t.Fatalf("delivery waived display constant %q != version authority %q", delivery.ReportClosureKindWaived, version.ClosureKindWaived)
	}
	if delivery.ReportClosureKindNormal != version.ClosureKindNormal {
		t.Fatalf("delivery normal display constant %q != version authority %q", delivery.ReportClosureKindNormal, version.ClosureKindNormal)
	}
	if delivery.ReportRunStatusFailed != string(execution.CaseRunFailed) || delivery.ReportRunStatusPassed != string(execution.CaseRunPassed) {
		t.Fatal("delivery run-status display constants drifted from the execution authority")
	}
}
