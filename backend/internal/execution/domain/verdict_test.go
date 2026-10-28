package domain_test

import (
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func failureClassPtr(class domain.FailureClass) *domain.FailureClass {
	return &class
}

// finishSnapshot is the canonical two-step, two-assertion case used by the
// ValidateFinish matrix: step 1 carries a ui assertion, step 2 an api one.
func finishSnapshot() domain.CaseSnapshot {
	return domain.CaseSnapshot{
		Node: domain.CaseSnapshotNode{ID: 7, Title: "下单"},
		Steps: []domain.CaseSnapshotStep{
			{Seq: 1, Action: "打开列表", Expected: "列表可见"},
			{Seq: 2, Action: "提交订单", Expected: "创建成功"},
		},
		Assertions: []domain.CaseSnapshotAssertion{
			{ID: 9, StepSeq: 1, Kind: "ui", Target: "list", Expression: "visible", Expected: "true"},
			{ID: 10, StepSeq: 2, Kind: "api", Target: "/orders", Expression: "status", Expected: "201"},
		},
	}
}

// finishDBSnapshot adds an unsupported db assertion to the canonical case.
func finishDBSnapshot() domain.CaseSnapshot {
	snapshot := finishSnapshot()
	snapshot.Assertions = append(snapshot.Assertions, domain.CaseSnapshotAssertion{
		ID: 11, StepSeq: 2, Kind: "db", Target: "orders", Expression: "count", Expected: "1",
	})
	return snapshot
}

func passedSteps() []domain.StepResult {
	return []domain.StepResult{
		{Seq: 1, Status: domain.StepStatusPassed, EvidenceCount: 1},
		{Seq: 2, Status: domain.StepStatusPassed, EvidenceCount: 1},
	}
}

func failedSteps() []domain.StepResult {
	return []domain.StepResult{
		{Seq: 1, Status: domain.StepStatusPassed, EvidenceCount: 1},
		{Seq: 2, Status: domain.StepStatusFailed, EvidenceCount: 1},
	}
}

func passedAssertions() []domain.AssertionResult {
	return []domain.AssertionResult{
		{AssertionID: 9, Kind: "ui", Verdict: "passed"},
		{AssertionID: 10, Kind: "api", Verdict: "passed"},
	}
}

func failedAssertions() []domain.AssertionResult {
	return []domain.AssertionResult{
		{AssertionID: 9, Kind: "ui", Verdict: "passed"},
		{AssertionID: 10, Kind: "api", Verdict: "failed"},
	}
}

func withoutEvidence(steps []domain.StepResult) []domain.StepResult {
	for i := range steps {
		steps[i].EvidenceCount = 0
	}
	return steps
}

type finishCase struct {
	name       string
	snapshot   domain.CaseSnapshot
	steps      []domain.StepResult
	assertions []domain.AssertionResult
	decision   domain.FinishDecision
	wantReason string // empty means ValidateFinish must return nil
}

func passedFinish() domain.FinishDecision {
	return domain.FinishDecision{Verdict: domain.VerdictPassed}
}

func TestValidateFinishMatrix(t *testing.T) {
	tests := []finishCase{
		{
			name: "passed with evidence", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: passedAssertions(), decision: passedFinish(),
		},
		{
			name: "failed application with failed assertion and evidence", snapshot: finishSnapshot(),
			steps: failedSteps(), assertions: failedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictFailed, FailureClass: failureClassPtr(domain.FailureClassApplication),
			},
		},
		{
			name: "blocked environment needs no evidence", snapshot: finishSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictBlocked, FailureClass: failureClassPtr(domain.FailureClassEnvironment),
			},
		},
		{
			name: "blocked session needs no evidence", snapshot: finishSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictBlocked, FailureClass: failureClassPtr(domain.FailureClassSession),
			},
		},
		{
			name: "inconclusive model needs no evidence", snapshot: finishSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictInconclusive, FailureClass: failureClassPtr(domain.FailureClassModel),
			},
		},
		{
			name: "inconclusive case needs no evidence", snapshot: finishSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictInconclusive, FailureClass: failureClassPtr(domain.FailureClassCase),
			},
		},
		{
			name: "db assertion is inconclusive case", snapshot: finishDBSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictInconclusive, FailureClass: failureClassPtr(domain.FailureClassCase),
			},
		},
		{
			name: "passed without evidence", snapshot: finishSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(), decision: passedFinish(),
			wantReason: "evidence_missing",
		},
		{
			name: "failed without evidence", snapshot: finishSnapshot(),
			steps: withoutEvidence(failedSteps()), assertions: failedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictFailed, FailureClass: failureClassPtr(domain.FailureClassApplication),
			},
			wantReason: "evidence_missing",
		},
		{
			name: "passed cannot carry a failure class", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictPassed, FailureClass: failureClassPtr(domain.FailureClassCase),
			},
			wantReason: "invalid_failure_class",
		},
		{
			name: "failed requires a failure class", snapshot: finishSnapshot(),
			steps: failedSteps(), assertions: failedAssertions(),
			decision:   domain.FinishDecision{Verdict: domain.VerdictFailed},
			wantReason: "invalid_failure_class",
		},
		{
			name: "failed cannot be environment", snapshot: finishSnapshot(),
			steps: failedSteps(), assertions: failedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictFailed, FailureClass: failureClassPtr(domain.FailureClassEnvironment),
			},
			wantReason: "invalid_failure_class",
		},
		{
			name: "blocked cannot be application", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictBlocked, FailureClass: failureClassPtr(domain.FailureClassApplication),
			},
			wantReason: "invalid_failure_class",
		},
		{
			name: "blocked requires a failure class", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: passedAssertions(),
			decision:   domain.FinishDecision{Verdict: domain.VerdictBlocked},
			wantReason: "invalid_failure_class",
		},
		{
			name: "inconclusive cannot be session", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictInconclusive, FailureClass: failureClassPtr(domain.FailureClassSession),
			},
			wantReason: "invalid_failure_class",
		},
		{
			name: "skipped is not a finish verdict", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: passedAssertions(),
			decision:   domain.FinishDecision{Verdict: domain.Verdict("skipped")},
			wantReason: "invalid_finish_verdict",
		},
		{
			name: "passed requires every assertion passed", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: failedAssertions(), decision: passedFinish(),
			wantReason: "verdict_not_justified",
		},
		{
			name: "passed requires every step passed", snapshot: finishSnapshot(),
			steps: failedSteps(), assertions: passedAssertions(), decision: passedFinish(),
			wantReason: "verdict_not_justified",
		},
		{
			name: "failed requires a failed assertion", snapshot: finishSnapshot(),
			steps: failedSteps(), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictFailed, FailureClass: failureClassPtr(domain.FailureClassApplication),
			},
			wantReason: "verdict_not_justified",
		},
		{
			name: "budget exhaustion never fails", snapshot: finishSnapshot(),
			steps: failedSteps(), assertions: failedAssertions(),
			decision: domain.FinishDecision{
				Verdict:      domain.VerdictFailed,
				FailureClass: failureClassPtr(domain.FailureClassApplication),
				Reason:       "budget_exhausted",
			},
			wantReason: "budget_exhausted",
		},
		{
			name: "budget exhaustion is inconclusive model", snapshot: finishSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict:      domain.VerdictInconclusive,
				FailureClass: failureClassPtr(domain.FailureClassModel),
				Reason:       "budget_exhausted",
			},
		},
		{
			name: "budget exhaustion is not inconclusive case", snapshot: finishSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict:      domain.VerdictInconclusive,
				FailureClass: failureClassPtr(domain.FailureClassCase),
				Reason:       "budget_exhausted",
			},
			wantReason: "budget_exhausted",
		},
		{
			name: "missing step result", snapshot: finishSnapshot(),
			steps: passedSteps()[:1], assertions: passedAssertions(), decision: passedFinish(),
			wantReason: "invalid_step_results",
		},
		{
			name: "steps are not in seq order", snapshot: finishSnapshot(),
			steps: []domain.StepResult{
				{Seq: 2, Status: domain.StepStatusPassed, EvidenceCount: 1},
				{Seq: 1, Status: domain.StepStatusPassed, EvidenceCount: 1},
			},
			assertions: passedAssertions(), decision: passedFinish(),
			wantReason: "invalid_step_results",
		},
		{
			name: "duplicate step result", snapshot: finishSnapshot(),
			steps: []domain.StepResult{
				{Seq: 1, Status: domain.StepStatusPassed, EvidenceCount: 1},
				{Seq: 1, Status: domain.StepStatusPassed, EvidenceCount: 1},
			},
			assertions: passedAssertions(), decision: passedFinish(),
			wantReason: "invalid_step_results",
		},
		{
			name: "step outside the snapshot", snapshot: finishSnapshot(),
			steps: []domain.StepResult{
				{Seq: 1, Status: domain.StepStatusPassed, EvidenceCount: 1},
				{Seq: 3, Status: domain.StepStatusPassed, EvidenceCount: 1},
			},
			assertions: passedAssertions(), decision: passedFinish(),
			wantReason: "invalid_step_results",
		},
		{
			name: "missing non-db assertion submission", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: passedAssertions()[:1], decision: passedFinish(),
			wantReason: "invalid_assertion_results",
		},
		{
			name: "duplicate assertion submission", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: append(passedAssertions(), passedAssertions()[0]), decision: passedFinish(),
			wantReason: "invalid_assertion_results",
		},
		{
			name: "assertion outside the snapshot", snapshot: finishSnapshot(),
			steps: passedSteps(), assertions: append(passedAssertions(), domain.AssertionResult{
				AssertionID: 99, Kind: "ui", Verdict: "passed",
			}), decision: passedFinish(),
			wantReason: "invalid_assertion_results",
		},
		{
			name: "db assertion cannot finish failed", snapshot: finishDBSnapshot(),
			steps: failedSteps(), assertions: failedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictFailed, FailureClass: failureClassPtr(domain.FailureClassApplication),
			},
			wantReason: "unsupported_assertion_kind",
		},
		{
			name: "db assertion cannot finish model inconclusive", snapshot: finishDBSnapshot(),
			steps: withoutEvidence(passedSteps()), assertions: passedAssertions(),
			decision: domain.FinishDecision{
				Verdict: domain.VerdictInconclusive, FailureClass: failureClassPtr(domain.FailureClassModel),
			},
			wantReason: "unsupported_assertion_kind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := domain.ValidateFinish(tt.snapshot, tt.steps, tt.assertions, tt.decision)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("ValidateFinish returned %v, want nil", err)
				}
				return
			}
			requireFinishReason(t, err, tt.wantReason)
		})
	}
}

func requireFinishReason(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("ValidateFinish returned nil, want reason %q", want)
	}
	var reasonErr *domain.ReasonError
	if !errors.As(err, &reasonErr) {
		t.Fatalf("ValidateFinish error type = %T, want *domain.ReasonError", err)
	}
	if reasonErr.Reason != want {
		t.Fatalf("ValidateFinish reason = %q, want %q (%v)", reasonErr.Reason, want, err)
	}
}

func TestValidateFinishRequiresEvidenceForPassed(t *testing.T) {
	requireFinishReason(t, domain.ValidateFinish(
		finishSnapshot(), withoutEvidence(passedSteps()), passedAssertions(), passedFinish(),
	), "evidence_missing")
}

func TestValidateFinishDBAssertionIsUnsupported(t *testing.T) {
	requireFinishReason(t, domain.ValidateFinish(
		finishDBSnapshot(), passedSteps(), passedAssertions(),
		domain.FinishDecision{
			Verdict: domain.VerdictFailed, FailureClass: failureClassPtr(domain.FailureClassApplication),
		},
	), "unsupported_assertion_kind")
}
