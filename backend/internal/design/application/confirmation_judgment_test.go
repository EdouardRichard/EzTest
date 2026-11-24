package designapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// alwaysPassJudgmentGate satisfies the confirmation judgment gate for tests
// whose subject is not the judge review (the pre-existing confirmation suite).
// It answers "pass" for every case version and refuses the fact questions a
// pass state never asks, so a test cannot silently receive an input the
// production path would have had to read.
type alwaysPassJudgmentGate struct{}

func (alwaysPassJudgmentGate) ReviewStateFor(context.Context, int64, int64, int64) (design.ReviewState, error) {
	return design.ReviewStatePass, nil
}

func (alwaysPassJudgmentGate) LatestOverride(context.Context, int64, int64) (*design.Override, error) {
	panic("always-pass judgment gate must not be asked for an override")
}

func (alwaysPassJudgmentGate) LatestJudgment(context.Context, int64) (*design.Judgment, error) {
	panic("always-pass judgment gate must not be asked for a judgment fact")
}

// confirmJudgmentFixture wires the confirmation service to the REAL judgment
// service (T6) over the in-memory judgment fact repository. The confirmation
// gate is a terminal-write decision, so its review-state input must come from
// the real producer, not from a stub that answers "pass" by fiat: only the
// persistence layer is replaced here (its SQL is pinned by the T6 integration
// test).
type confirmJudgmentFixture struct {
	service   *GraphService
	repo      *fakeRepo
	facts     *fakeJudgmentRepo
	judgments *JudgmentService
	events    *fakePublisher
	audit     *fakeAudit
}

func newConfirmJudgmentFixture(t *testing.T) *confirmJudgmentFixture {
	t.Helper()
	service, repo, publisher, audit := newConfirmFixture(t)
	facts := &fakeJudgmentRepo{exhausted: map[[2]int64]bool{}}
	judgments := NewJudgmentService(facts, repo, &fakeIDs{}, newFixedClock(), publisher,
		fakeJudgmentSettings{maxReworkRoundsKey: 1})
	// Replace the always-pass stub of newConfirmFixture with the real gate.
	service.SetJudgmentGate(NewJudgmentGate(judgments, facts))
	return &confirmJudgmentFixture{
		service: service, repo: repo, facts: facts, judgments: judgments,
		events: publisher, audit: audit,
	}
}

// currentVersionID returns the server-side current version id of a seeded case.
func (f *confirmJudgmentFixture) currentVersionID(t *testing.T, caseID int64) int64 {
	t.Helper()
	item, ok := f.repo.cases[caseID]
	if !ok || item.current == nil {
		t.Fatalf("case %d has no current version", caseID)
	}
	return item.current.ID
}

// contentHash returns the hash the server stored for the current version. The
// expectation is derived from the stored fact, never hand-written.
func (f *confirmJudgmentFixture) contentHash(t *testing.T, caseID int64) string {
	t.Helper()
	item, ok := f.repo.cases[caseID]
	if !ok || item.current == nil {
		t.Fatalf("case %d has no current version", caseID)
	}
	return item.current.ContentHash
}

func (f *confirmJudgmentFixture) seedJudgment(caseID, caseVersionID int64, round int, verdict string, findings ...design.JudgmentFinding) design.Judgment {
	judgment := design.Judgment{
		ID: int64(9000 + len(f.facts.judgments)), VersionID: 1001, RequirementID: 3001,
		CaseID: caseID, CaseVersionID: caseVersionID,
		ContentHash: f.repo.cases[caseID].current.ContentHash,
		Round:       round, Verdict: verdict, Findings: findings,
		CreatedAt: newFixedClock().Now(),
	}
	f.facts.judgments = append(f.facts.judgments, judgment)
	return judgment
}

func (f *confirmJudgmentFixture) seedOverride(caseID, caseVersionID int64, contentHash, condition string, at time.Time, judgmentID *int64) {
	f.facts.overrides = append(f.facts.overrides, design.Override{
		ID: int64(9500 + len(f.facts.overrides)), CaseID: caseID, CaseVersionID: caseVersionID,
		ContentHash: contentHash, JudgmentID: judgmentID, Condition: condition,
		ActorID: 42, Reason: "人工复核后接受该用例", CreatedAt: at,
	})
}

func (f *confirmJudgmentFixture) seedJudgeExhausted(caseID, caseVersionID int64) {
	f.facts.exhausted[[2]int64{caseID, caseVersionID}] = true
}

func requireJudgmentCode(t *testing.T, err error, wantCode string) *platform.Error {
	t.Helper()
	apiErr := platform.AsError(err)
	if err == nil {
		t.Fatalf("error = nil, want 409/%s", wantCode)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != wantCode {
		t.Fatalf("error = %d/%s (%v), want 409/%s", apiErr.Status, apiErr.Code, err, wantCode)
	}
	return apiErr
}

// TestConfirmJudgmentPredicate pins the review-state routing of the confirmation
// predicate (spec SC-E5): pass and a valid human override are confirmable,
// pending/reworking answer judgment_pending, and an escalated fail or an
// unavailable judge answer judgment_failed until a valid override exists.
func TestConfirmJudgmentPredicate(t *testing.T) {
	ctx := context.Background()

	t.Run("pass is confirmable", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7001, 5001, "登录成功", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7001)
		f.seedJudgment(7001, versionID, 1, design.VerdictPass)
		if err := f.service.validateJudgmentForConfirm(ctx, 1001, 7001, versionID); err != nil {
			t.Fatalf("pass blocked: %v", err)
		}
	})

	t.Run("no fact is pending", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7002, 5001, "未审查", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7002)
		err := f.service.validateJudgmentForConfirm(ctx, 1001, 7002, versionID)
		apiErr := requireJudgmentCode(t, err, "judgment_pending")
		if got, _ := apiErr.Details["review_state"].(string); got != string(design.ReviewStatePending) {
			t.Fatalf("review_state = %v, want pending", apiErr.Details["review_state"])
		}
	})

	t.Run("judgment on an older version is pending", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7001, 5001, "登录成功", design.CaseStatusDraft, design.CaseStatusDraft)
		current := f.currentVersionID(t, 7001)
		f.seedJudgment(7001, current-1, 1, design.VerdictPass)
		err := f.service.validateJudgmentForConfirm(ctx, 1001, 7001, current)
		requireJudgmentCode(t, err, "judgment_pending")
	})

	t.Run("fail within the rework budget is reworking", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7003, 5001, "首次失败", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7003)
		f.seedJudgment(7003, versionID, 1, design.VerdictFail)
		err := f.service.validateJudgmentForConfirm(ctx, 1001, 7003, versionID)
		apiErr := requireJudgmentCode(t, err, "judgment_pending")
		if got, _ := apiErr.Details["review_state"].(string); got != string(design.ReviewStateReworking) {
			t.Fatalf("review_state = %v, want reworking", apiErr.Details["review_state"])
		}
	})

	t.Run("exhausted fail names its findings", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7003)
		f.seedJudgment(7003, versionID, 2, design.VerdictFail, design.JudgmentFinding{
			Dimension: "coverage", Severity: design.SeverityBlocker,
			Message: "AC-2 未被任何断言覆盖", CaseRef: "7003",
		})
		err := f.service.validateJudgmentForConfirm(ctx, 1001, 7003, versionID)
		apiErr := requireJudgmentCode(t, err, "judgment_failed")
		if !strings.Contains(apiErr.Message, "AC-2") {
			t.Fatalf("message = %q, want the blocking finding quoted", apiErr.Message)
		}
		if got, _ := apiErr.Details["review_state"].(string); got != string(design.ReviewStateEscalatedFail) {
			t.Fatalf("review_state = %v, want escalated_fail", apiErr.Details["review_state"])
		}
		if next, _ := apiErr.Details["next_action"].(string); !strings.Contains(next, "override") {
			t.Fatalf("next_action = %v, want the human override route", apiErr.Details["next_action"])
		}
	})

	t.Run("valid override unblocks an exhausted fail", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7003)
		f.seedJudgment(7003, versionID, 2, design.VerdictFail)
		f.seedOverride(7003, versionID, f.contentHash(t, 7003), OverrideConditionFailExhausted,
			newFixedClock().Now().Add(time.Minute), nil)
		if err := f.service.validateJudgmentForConfirm(ctx, 1001, 7003, versionID); err != nil {
			t.Fatalf("override blocked: %v", err)
		}
	})

	t.Run("override referencing the overridden judgment is valid", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7003)
		judgment := f.seedJudgment(7003, versionID, 2, design.VerdictFail)
		// Same instant as the judgment: the explicit judgment reference, not the
		// clock, proves the override was made against that fact.
		f.seedOverride(7003, versionID, f.contentHash(t, 7003), OverrideConditionFailExhausted,
			judgment.CreatedAt, &judgment.ID)
		if err := f.service.validateJudgmentForConfirm(ctx, 1001, 7003, versionID); err != nil {
			t.Fatalf("referencing override blocked: %v", err)
		}
	})

	t.Run("override older than the latest judgment is invalid", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7003)
		f.seedJudgment(7003, versionID, 2, design.VerdictFail)
		f.seedOverride(7003, versionID, f.contentHash(t, 7003), OverrideConditionFailExhausted,
			newFixedClock().Now().Add(-time.Minute), nil)
		requireJudgmentCode(t, f.service.validateJudgmentForConfirm(ctx, 1001, 7003, versionID), "judgment_failed")
	})

	t.Run("override bound to an older version is invalid", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft, design.CaseStatusDraft)
		current := f.currentVersionID(t, 7003)
		f.seedJudgment(7003, current, 2, design.VerdictFail)
		f.seedOverride(7003, current-1, f.contentHash(t, 7003), OverrideConditionFailExhausted,
			newFixedClock().Now().Add(time.Minute), nil)
		requireJudgmentCode(t, f.service.validateJudgmentForConfirm(ctx, 1001, 7003, current), "judgment_failed")
	})

	t.Run("override with a stale content hash is invalid", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7003)
		f.seedJudgment(7003, versionID, 2, design.VerdictFail)
		f.seedOverride(7003, versionID, "hash-of-other-content", OverrideConditionFailExhausted,
			newFixedClock().Now().Add(time.Minute), nil)
		requireJudgmentCode(t, f.service.validateJudgmentForConfirm(ctx, 1001, 7003, versionID), "judgment_failed")
	})

	t.Run("judge unavailable escalates and names the reason", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7004, 5001, "裁判不可用", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7004)
		f.seedJudgeExhausted(7004, versionID)
		apiErr := requireJudgmentCode(t, f.service.validateJudgmentForConfirm(ctx, 1001, 7004, versionID), "judgment_failed")
		if got, _ := apiErr.Details["review_state"].(string); got != string(design.ReviewStateJudgeUnavailable) {
			t.Fatalf("review_state = %v, want judge_unavailable", apiErr.Details["review_state"])
		}
		if !strings.Contains(apiErr.Message, "judge") {
			t.Fatalf("message = %q, want the escalation reason", apiErr.Message)
		}
	})

	t.Run("judge unavailable with a valid override is confirmable", func(t *testing.T) {
		f := newConfirmJudgmentFixture(t)
		seedFakeCase(t, f.repo, 7004, 5001, "裁判不可用", design.CaseStatusDraft)
		versionID := f.currentVersionID(t, 7004)
		f.seedJudgeExhausted(7004, versionID)
		f.seedOverride(7004, versionID, f.contentHash(t, 7004), OverrideConditionJudgeExhausted,
			newFixedClock().Now().Add(time.Minute), nil)
		if err := f.service.validateJudgmentForConfirm(ctx, 1001, 7004, versionID); err != nil {
			t.Fatalf("judge-unavailable override blocked: %v", err)
		}
	})
}

// gateFault wraps the real gate and fails one read with an infrastructure error.
// Only the failing method is replaced; every other answer still comes from the
// real judgment service.
type gateFault struct {
	JudgmentGate
	reviewErr   error
	overrideErr error
	judgmentErr error
}

func (g gateFault) ReviewStateFor(ctx context.Context, versionID, caseID, currentVersionID int64) (design.ReviewState, error) {
	if g.reviewErr != nil {
		return "", g.reviewErr
	}
	return g.JudgmentGate.ReviewStateFor(ctx, versionID, caseID, currentVersionID)
}

func (g gateFault) LatestOverride(ctx context.Context, caseID, caseVersionID int64) (*design.Override, error) {
	if g.overrideErr != nil {
		return nil, g.overrideErr
	}
	return g.JudgmentGate.LatestOverride(ctx, caseID, caseVersionID)
}

func (g gateFault) LatestJudgment(ctx context.Context, caseID int64) (*design.Judgment, error) {
	if g.judgmentErr != nil {
		return nil, g.judgmentErr
	}
	return g.JudgmentGate.LatestJudgment(ctx, caseID)
}

// TestConfirmJudgmentInfraErrorIsNotMisattributed pins that a judgment-store
// read failure propagates as itself: it must never be reported as a field error
// or as a judgment verdict about the case.
func TestConfirmJudgmentInfraErrorIsNotMisattributed(t *testing.T) {
	boom := errors.New("judgment store connection reset")
	for _, tc := range []struct {
		name  string
		fault gateFault
	}{
		{name: "review state read", fault: gateFault{reviewErr: boom}},
		{name: "override read", fault: gateFault{overrideErr: boom}},
		{name: "judgment read", fault: gateFault{judgmentErr: boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfirmJudgmentFixture(t)
			seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft)
			versionID := f.currentVersionID(t, 7003)
			f.seedJudgment(7003, versionID, 2, design.VerdictFail)
			fault := tc.fault
			fault.JudgmentGate = NewJudgmentGate(f.judgments, f.facts)
			f.service.SetJudgmentGate(fault)

			err := f.service.validateJudgmentForConfirm(context.Background(), 1001, 7003, versionID)
			if !errors.Is(err, boom) {
				t.Fatalf("error = %v, want the infrastructure error propagated", err)
			}
			code := platform.AsError(err).Code
			if code == "judgment_pending" || code == "judgment_failed" || code == "invalid_argument" {
				t.Fatalf("infrastructure error misattributed as %q", code)
			}
			if len(f.repo.confirmations) != 0 {
				t.Fatalf("confirmation rows = %d, want 0", len(f.repo.confirmations))
			}
		})
	}
}

// TestConfirmWithoutJudgmentGateFailsClosed pins the unwired-gate policy: a
// process without the judgment gate cannot confirm anything (constitution
// VII.6, no silent degradation).
func TestConfirmWithoutJudgmentGateFailsClosed(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	f.service.SetJudgmentGate(nil)
	seedFakeCase(t, f.repo, 7001, 5001, "登录成功", design.CaseStatusDraft)

	_, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	if err == nil {
		t.Fatal("Confirm succeeded without a judgment gate")
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("error = %d/%s, want 503 fail-closed", apiErr.Status, apiErr.Code)
	}
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("confirmation rows = %d, want 0", len(f.repo.confirmations))
	}
	if status := f.repo.cases[7001].current.Status; status != design.CaseStatusDraft {
		t.Fatalf("case status = %q, want draft", status)
	}
}

// TestConfirmCaseScopeBlockedByPending pins the single-case path: an unreviewed
// draft is a typed 409 and nothing is frozen, published or audited.
func TestConfirmCaseScopeBlockedByPending(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "未审查", design.CaseStatusDraft)

	_, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	requireJudgmentCode(t, err, "judgment_pending")
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("confirmation rows = %d, want 0", len(f.repo.confirmations))
	}
	if status := f.repo.cases[7001].current.Status; status != design.CaseStatusDraft {
		t.Fatalf("case status = %q, want draft", status)
	}
	if len(f.events.events) != 0 || len(f.audit.events) != 0 {
		t.Fatalf("blocked confirm emitted events=%v audit=%v", f.events.types(), f.audit.events)
	}
}

// TestConfirmOverrideAllowsConfirm pins the human decision route end to end:
// an escalated fail with a valid override is confirmed by the human call.
func TestConfirmOverrideAllowsConfirm(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft)
	versionID := f.currentVersionID(t, 7003)
	f.seedJudgment(7003, versionID, 2, design.VerdictFail)

	_, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7003,
	}, 42)
	requireJudgmentCode(t, err, "judgment_failed")

	f.seedOverride(7003, versionID, f.contentHash(t, 7003), OverrideConditionFailExhausted,
		newFixedClock().Now().Add(time.Minute), nil)
	result, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7003,
	}, 42)
	if err != nil {
		t.Fatalf("Confirm after override: %v", err)
	}
	if len(result.Confirmed) != 1 || result.Confirmed[0].CaseVersionID != versionID {
		t.Fatalf("confirmed = %+v, want version %d", result.Confirmed, versionID)
	}
	if len(f.repo.confirmations) != 1 {
		t.Fatalf("confirmation rows = %d, want 1", len(f.repo.confirmations))
	}
}

// TestBulkConfirmAtomicOnPending pins the explicit-selection atomicity: one
// unreviewed case blocks the whole selection before any sibling is frozen.
func TestBulkConfirmAtomicOnPending(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "已审查", design.CaseStatusDraft)
	seedFakeCase(t, f.repo, 7002, 5001, "未审查", design.CaseStatusDraft)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)

	err := f.service.ConfirmCasesByIDs(context.Background(), 1001, []int64{7001, 7002})
	apiErr := requireJudgmentCode(t, err, "judgment_pending")
	if got, _ := apiErr.Details["case_id"].(string); got != "7002" {
		t.Fatalf("blocking case_id = %v, want 7002", apiErr.Details["case_id"])
	}
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("partial confirm leaked %d row(s)", len(f.repo.confirmations))
	}
	for _, caseID := range []int64{7001, 7002} {
		if status := f.repo.cases[caseID].current.Status; status != design.CaseStatusDraft {
			t.Fatalf("case %d status = %q, want draft", caseID, status)
		}
	}
	if len(f.events.events) != 0 || len(f.audit.events) != 0 {
		t.Fatalf("blocked bulk confirm emitted events=%v audit=%v", f.events.types(), f.audit.events)
	}
}

// TestBulkConfirmAtomicOnJudgmentFailed pins the same atomicity for an
// escalated fail without an override.
func TestBulkConfirmAtomicOnJudgmentFailed(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "已审查", design.CaseStatusDraft)
	seedFakeCase(t, f.repo, 7002, 5001, "二次失败", design.CaseStatusDraft)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)
	f.seedJudgment(7002, f.currentVersionID(t, 7002), 2, design.VerdictFail)

	err := f.service.ConfirmCasesByIDs(context.Background(), 1001, []int64{7001, 7002})
	requireJudgmentCode(t, err, "judgment_failed")
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("partial confirm leaked %d row(s)", len(f.repo.confirmations))
	}
	for _, caseID := range []int64{7001, 7002} {
		if status := f.repo.cases[caseID].current.Status; status != design.CaseStatusDraft {
			t.Fatalf("case %d status = %q, want draft", caseID, status)
		}
	}
}

// TestBulkConfirmNamesEveryBlockingCase pins the batch report: the user sees
// every blocking case of the selection, not only the first one.
func TestBulkConfirmNamesEveryBlockingCase(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "已审查", design.CaseStatusDraft)
	seedFakeCase(t, f.repo, 7002, 5001, "未审查", design.CaseStatusDraft)
	seedFakeCase(t, f.repo, 7003, 5001, "二次失败", design.CaseStatusDraft)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)
	f.seedJudgment(7003, f.currentVersionID(t, 7003), 2, design.VerdictFail)

	err := f.service.ConfirmCasesByIDs(context.Background(), 1001, []int64{7001, 7002, 7003})
	apiErr := platform.AsError(err)
	if apiErr.Status != http.StatusConflict {
		t.Fatalf("error = %d/%s, want 409", apiErr.Status, apiErr.Code)
	}
	blocking, ok := apiErr.Details["blocking_cases"].([]map[string]any)
	if !ok || len(blocking) != 2 {
		t.Fatalf("blocking_cases = %#v, want two entries", apiErr.Details["blocking_cases"])
	}
	byID := map[string]string{}
	for _, entry := range blocking {
		id, _ := entry["case_id"].(string)
		code, _ := entry["code"].(string)
		byID[id] = code
	}
	if byID["7002"] != "judgment_pending" || byID["7003"] != "judgment_failed" {
		t.Fatalf("blocking codes = %#v, want 7002 pending and 7003 failed", byID)
	}
	if !strings.Contains(apiErr.Message, "7002") || !strings.Contains(apiErr.Message, "7003") {
		t.Fatalf("message = %q, want both blocking cases named", apiErr.Message)
	}
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("partial confirm leaked %d row(s)", len(f.repo.confirmations))
	}
}

// TestConfirmRequirementScopeIsAtomicOnPending pins the in-transaction bulk
// path (scenario/requirement scope): the predicate runs on every draft of the
// scope before the first write, not only on the explicit-selection precheck.
func TestConfirmRequirementScopeIsAtomicOnPending(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "已审查", design.CaseStatusDraft)
	seedFakeCase(t, f.repo, 7002, 5001, "未审查", design.CaseStatusDraft)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)

	_, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: 3001,
	}, 42)
	requireJudgmentCode(t, err, "judgment_pending")
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("partial confirm leaked %d row(s)", len(f.repo.confirmations))
	}
	for _, caseID := range []int64{7001, 7002} {
		if status := f.repo.cases[caseID].current.Status; status != design.CaseStatusDraft {
			t.Fatalf("case %d status = %q, want draft", caseID, status)
		}
	}
}

// TestConfirmScopeAllIsAtomicOnPending pins the "all" selection: it reuses the
// requirement scope, so one unreviewed draft blocks every case of the version.
func TestConfirmScopeAllIsAtomicOnPending(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "已审查", design.CaseStatusDraft)
	seedFakeCase(t, f.repo, 7002, 5001, "未审查", design.CaseStatusDraft)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)

	err := f.service.ConfirmCasesByIDs(context.Background(), 1001, nil)
	requireJudgmentCode(t, err, "judgment_pending")
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("partial confirm leaked %d row(s)", len(f.repo.confirmations))
	}
}

// TestConfirmSkipsJudgmentForNonDrafts pins that the gate only guards drafts:
// already-confirmed and stale cases keep their existing skip semantics even
// when no judgment fact exists for them.
func TestConfirmSkipsJudgmentForNonDrafts(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "已审查草案", design.CaseStatusDraft)
	seedFakeCase(t, f.repo, 7002, 5001, "早已确认", design.CaseStatusConfirmed)
	seedFakeCase(t, f.repo, 7003, 5001, "已过期", design.CaseStatusStale)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)

	result, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeScenario, TargetID: 5001,
	}, 42)
	if err != nil {
		t.Fatalf("Confirm scenario: %v", err)
	}
	if len(result.Confirmed) != 1 || len(result.Skipped) != 2 {
		t.Fatalf("confirmed=%d skipped=%d, want 1/2", len(result.Confirmed), len(result.Skipped))
	}
}

// TestIdempotentConfirmNeedsNoJudgment pins that a repeated confirmation of an
// already-confirmed version stays idempotent even after its judgment facts are
// no longer the latest facts of the case.
func TestIdempotentConfirmNeedsNoJudgment(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "登录成功", design.CaseStatusDraft)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)
	if _, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	f.facts.judgments = nil

	result, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	if err != nil {
		t.Fatalf("idempotent Confirm: %v", err)
	}
	if len(result.Confirmed) != 0 || len(result.Skipped) != 1 {
		t.Fatalf("idempotent result = %+v", result)
	}
}

// TestConfirmCompletenessGateStillRunsFirst pins the existing Task 95 ordering:
// an incomplete version is still refused as incomplete_case_version, so the
// judgment gate does not mask the repair request.
// multiRequirementGraphRepo serves a version design graph with more than one
// requirement node. fakeRepo.ReadGraph always rebuilds a single-requirement
// graph from its own requirement/scenario fields, so only that one read is
// overridden here: targets, locks, case reads and confirmation writes still
// come from the shared fake, and the code under test (confirmAllCases and the
// judgment gate) is the real production implementation.
type multiRequirementGraphRepo struct {
	*fakeRepo
	extra []design.RequirementGraph
}

func (r multiRequirementGraphRepo) ReadGraph(ctx context.Context, versionID int64) (design.GraphData, error) {
	graph, err := r.fakeRepo.ReadGraph(ctx, versionID)
	if err != nil {
		return design.GraphData{}, err
	}
	graph.Requirements = append(graph.Requirements, r.extra...)
	return graph, nil
}

// requirementGraphNode builds one graph node for a requirement whose scenarios
// live outside the shared fake's single-requirement projection.
func requirementGraphNode(repo *fakeRepo, requirement design.RequirementRef, scenario design.Scenario, caseIDs ...int64) design.RequirementGraph {
	node := design.RequirementGraph{
		Requirement: requirement,
		Scenarios:   []design.ScenarioGraph{{Scenario: scenario}},
	}
	for _, caseID := range caseIDs {
		item := repo.cases[caseID]
		node.Scenarios[0].Cases = append(node.Scenarios[0].Cases, design.CaseGraph{
			Case: item.entity, Current: *item.current,
		})
	}
	return node
}

// withExtraRequirements re-wires the fixture's graph service onto a repository
// that also serves the given requirement nodes, keeping the real judgment gate.
func (f *confirmJudgmentFixture) withExtraRequirements(extra ...design.RequirementGraph) *GraphService {
	return NewGraphService(multiRequirementGraphRepo{fakeRepo: f.repo, extra: extra},
		&fakeIDs{}, newFixedClock(), f.audit, f.events, &fakeTeamRoom{}).
		SetJudgmentGate(NewJudgmentGate(f.judgments, f.facts))
}

// secondRequirement is the requirement B node used by the cross-requirement
// "all" tests. Its scenario is deliberately absent from the shared fake's
// scenario list so the fake keeps projecting requirement A only.
func secondRequirement() (design.RequirementRef, design.Scenario) {
	return design.RequirementRef{
			ID: 3002, VersionID: 1001, CurrentVersionID: 3004, Revision: 1,
			Title: "需求乙", Status: "clarified",
		}, design.Scenario{
			ID: 5002, VersionID: 1001, RequirementID: 3002, RequirementVersionID: 3004,
			Name: "需求乙场景",
		}
}

// TestConfirmAllIsAtomicAcrossRequirements pins the "all" selection across
// requirement boundaries. Each requirement is confirmed in its own transaction,
// so without a version-wide judgment precheck requirement A would already be
// frozen when requirement B is blocked — a partially confirmed version behind a
// failed gate (spec SC-E5: any selected draft failing means NO partial confirm).
func TestConfirmAllIsAtomicAcrossRequirements(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "需求甲已审查", design.CaseStatusDraft)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)
	seedFakeCase(t, f.repo, 7002, 5002, "需求乙未审查", design.CaseStatusDraft)
	requirementB, scenarioB := secondRequirement()
	service := f.withExtraRequirements(requirementGraphNode(f.repo, requirementB, scenarioB, 7002))

	err := service.ConfirmCasesByIDs(context.Background(), 1001, nil)
	// The invariant first: a blocked "all" selection must not have frozen any
	// requirement, including the ones whose own review passed.
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("cross-requirement partial confirm leaked %d row(s)", len(f.repo.confirmations))
	}
	for _, caseID := range []int64{7001, 7002} {
		if status := f.repo.cases[caseID].current.Status; status != design.CaseStatusDraft {
			t.Fatalf("case %d status = %q after blocked all-confirm, want draft", caseID, status)
		}
	}
	apiErr := requireJudgmentCode(t, err, "judgment_pending")
	if got, _ := apiErr.Details["case_id"].(string); got != "7002" {
		t.Fatalf("blocking case_id = %v, want 7002 (requirement B)", apiErr.Details["case_id"])
	}
	if len(f.events.events) != 0 || len(f.audit.events) != 0 {
		t.Fatalf("blocked all-confirm emitted events=%v audit=%v", f.events.types(), f.audit.events)
	}
}

// TestConfirmAllNamesBlockingCasesAcrossRequirements pins that the version-wide
// precheck reports every blocking draft of the version, not only the first
// requirement's.
func TestConfirmAllNamesBlockingCasesAcrossRequirements(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "需求甲未审查", design.CaseStatusDraft)
	seedFakeCase(t, f.repo, 7002, 5002, "需求乙二次失败", design.CaseStatusDraft)
	f.seedJudgment(7002, f.currentVersionID(t, 7002), 2, design.VerdictFail)
	requirementB, scenarioB := secondRequirement()
	service := f.withExtraRequirements(requirementGraphNode(f.repo, requirementB, scenarioB, 7002))

	err := service.ConfirmCasesByIDs(context.Background(), 1001, nil)
	apiErr := platform.AsError(err)
	if apiErr.Status != http.StatusConflict {
		t.Fatalf("error = %d/%s, want 409", apiErr.Status, apiErr.Code)
	}
	blocking, ok := apiErr.Details["blocking_cases"].([]map[string]any)
	if !ok || len(blocking) != 2 {
		t.Fatalf("blocking_cases = %#v, want one entry per requirement", apiErr.Details["blocking_cases"])
	}
	byID := map[string]string{}
	for _, entry := range blocking {
		id, _ := entry["case_id"].(string)
		code, _ := entry["code"].(string)
		byID[id] = code
	}
	if byID["7001"] != "judgment_pending" || byID["7002"] != "judgment_failed" {
		t.Fatalf("blocking codes = %#v, want 7001 pending and 7002 failed", byID)
	}
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("cross-requirement partial confirm leaked %d row(s)", len(f.repo.confirmations))
	}
}

// TestConfirmAllPrecheckIgnoresNonDraftsOfOtherRequirements guards against
// over-blocking: a second requirement whose cases are already confirmed carries
// no draft, so it must not block the first requirement's confirmation.
func TestConfirmAllPrecheckIgnoresNonDraftsOfOtherRequirements(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCase(t, f.repo, 7001, 5001, "需求甲已审查", design.CaseStatusDraft)
	f.seedJudgment(7001, f.currentVersionID(t, 7001), 1, design.VerdictPass)
	seedFakeCase(t, f.repo, 7002, 5002, "需求乙早已确认", design.CaseStatusConfirmed)
	requirementB, scenarioB := secondRequirement()
	service := f.withExtraRequirements(requirementGraphNode(f.repo, requirementB, scenarioB, 7002))

	if err := service.ConfirmCasesByIDs(context.Background(), 1001, nil); err != nil {
		t.Fatalf("ConfirmCasesByIDs(all): %v", err)
	}
	if status := f.repo.cases[7001].current.Status; status != design.CaseStatusConfirmed {
		t.Fatalf("case 7001 status = %q, want confirmed", status)
	}
	if len(f.repo.confirmations) != 1 {
		t.Fatalf("confirmation rows = %d, want 1", len(f.repo.confirmations))
	}
}

func TestConfirmCompletenessGateStillRunsFirst(t *testing.T) {
	f := newConfirmJudgmentFixture(t)
	seedFakeCaseVersions(t, f.repo, 7001, 5001, "不完整用例",
		fakeCaseVersionSpec{steps: 1, assertions: 0, status: design.CaseStatusDraft},
	)

	_, err := f.service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	apiErr := platform.AsError(err)
	if apiErr.Code != "incomplete_case_version" {
		t.Fatalf("error = %d/%s, want incomplete_case_version", apiErr.Status, apiErr.Code)
	}
	if len(f.repo.confirmations) != 0 {
		t.Fatalf("confirmation rows = %d, want 0", len(f.repo.confirmations))
	}
}
