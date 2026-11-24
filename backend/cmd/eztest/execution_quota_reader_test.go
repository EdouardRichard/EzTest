package main

import (
	"context"
	"errors"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// This file pins the PRODUCTION usage-side producer of the concurrency quota.
//
// executionQuotaReader is what the composition root actually wires into
// SchedulerDeps.QuotaReader (see main.go), so it -- not the scheduler's
// ListRunning fallback -- is what runs in production.
//
// v0.2.4-F (design P5, V-0015): the quota is a SINGLE global concurrent-case-run
// limit (execution.max_concurrent_case_runs, default 0 = unlimited). The retired
// per-SUT/cross-system dimensions are gone, and with them the B-era trap this
// file used to pin: quota.go asserted "spans systems" via
// `candidate.CrossSystem || len(candidate.SUTIDs) > 1` while every usage
// producer hardcoded CrossSystem false, so wiring a real usage-side signal
// alone collapsed multi-SUT concurrency to 1. The reader no longer resolves a
// per-run SUT set at all -- a running run is one global slot, nothing else.

// stubQuotaCaseRuns is the CaseRunRepository half of the reader's inputs. Only
// ListRunning is exercised; every other method panics so an accidental future use
// is loud rather than silently zero-valued.
type stubQuotaCaseRuns struct {
	running []execution.CaseRun
	listErr error
}

var _ executionapp.CaseRunRepository = (*stubQuotaCaseRuns)(nil)

func (s *stubQuotaCaseRuns) ListRunning(context.Context) ([]execution.CaseRun, error) {
	return s.running, s.listErr
}

func (s *stubQuotaCaseRuns) Get(context.Context, int64) (execution.CaseRun, error) {
	panic("stubQuotaCaseRuns.Get: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) ListByWave(context.Context, int64, string, int, *int64) ([]execution.CaseRun, bool, error) {
	panic("stubQuotaCaseRuns.ListByWave: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) ListAllByWave(context.Context, int64) ([]execution.CaseRun, error) {
	panic("stubQuotaCaseRuns.ListAllByWave: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) Create(context.Context, execution.CaseRun) error {
	panic("stubQuotaCaseRuns.Create: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) CreateAndTransition(context.Context, execution.CaseRun, []execution.CaseRunStatus, execution.CaseRunStatus, int64, string) error {
	panic("stubQuotaCaseRuns.CreateAndTransition: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) UpdateCAS(context.Context, int64, int64, string, func(*execution.CaseRun) error) (bool, error) {
	panic("stubQuotaCaseRuns.UpdateCAS: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) ReclassifyTerminal(context.Context, int64, int64, string, func(*execution.CaseRun) error) (bool, error) {
	panic("stubQuotaCaseRuns.ReclassifyTerminal: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) Update(context.Context, execution.CaseRun) (bool, error) {
	panic("stubQuotaCaseRuns.Update: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) Transition(context.Context, int64, []execution.CaseRunStatus, execution.CaseRunStatus, int64, string) (bool, error) {
	panic("stubQuotaCaseRuns.Transition: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) ClaimLease(context.Context, int64, int64, time.Time) (bool, error) {
	panic("stubQuotaCaseRuns.ClaimLease: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) ListRunningByExecutor(context.Context, int64) ([]execution.CaseRun, error) {
	panic("stubQuotaCaseRuns.ListRunningByExecutor: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) AddEvidenceDrop(context.Context, int64, int64) (int64, error) {
	panic("stubQuotaCaseRuns.AddEvidenceDrop: not used by executionQuotaReader")
}

func (s *stubQuotaCaseRuns) LatestByCaseVersions(context.Context, int64, []int64) (map[int64]execution.CaseRun, error) {
	panic("stubQuotaCaseRuns.LatestByCaseVersions: not used by executionQuotaReader")
}

// TestExecutionQuotaReaderCountsEveryRunningRun pins the shipped reader's
// accounting shape: one entry per running case_run, identified by its run ID.
func TestExecutionQuotaReaderCountsEveryRunningRun(t *testing.T) {
	ctx := context.Background()
	reader := executionQuotaReader{
		caseRuns: &stubQuotaCaseRuns{running: []execution.CaseRun{
			{ID: 9001, IntentID: 1001, Status: execution.CaseRunRunning},
			{ID: 9002, IntentID: 1002, Status: execution.CaseRunRunning},
		}},
	}

	usage, err := reader.RunningCases(ctx)
	if err != nil {
		t.Fatalf("RunningCases: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("usage = %d entries, want 2", len(usage))
	}
	seen := map[int64]bool{}
	for _, item := range usage {
		seen[item.CaseRunID] = true
	}
	if !seen[9001] || !seen[9002] {
		t.Fatalf("usage = %+v, want both running runs counted", usage)
	}
}

// TestExecutionQuotaReaderDoesNotCollapseMultiSUTConcurrency pins the
// CONSEQUENCE at the production wiring: feeding this reader's output into the
// real quota rule under the production default (single global limit, 0 =
// unlimited) must admit further runs of a multi-SUT version even with several
// runs already in flight -- no fake stands in for either the producer or the
// domain rule.
//
// Mutation contract (narrowed to what this test proves, fix round 1 Minor 1):
// restoring a FINITE GLOBAL DEFAULT (global 3 refuses the 4th run) or a
// CROSS-SYSTEM-STYLE THROTTLE COUNTING THE RUNNING SET (refuses the 2nd) fails
// the first assertion. A per-SUT reintroduction is structurally blocked
// upstream: RunningCase carries no SUT set, so there is no per-SUT input to
// restore without a type change. The second assertion proves the global gate
// is live, so the first is not a vacuous pass.
func TestExecutionQuotaReaderDoesNotCollapseMultiSUTConcurrency(t *testing.T) {
	ctx := context.Background()
	reader := executionQuotaReader{
		caseRuns: &stubQuotaCaseRuns{running: []execution.CaseRun{
			{ID: 9001, IntentID: 1001, Status: execution.CaseRunRunning},
			{ID: 9002, IntentID: 1002, Status: execution.CaseRunRunning},
			{ID: 9003, IntentID: 1003, Status: execution.CaseRunRunning},
		}},
	}
	usage, err := reader.RunningCases(ctx)
	if err != nil {
		t.Fatalf("RunningCases: %v", err)
	}

	// The production default: main.go wires the settings service and the
	// scheduler reads execution.max_concurrent_case_runs (default 0) per tick.
	quota := execution.QuotaFromSettings(0)
	ok, reason := execution.CanDispatch(quota, usage, 8, 0, 1)
	if !ok {
		t.Fatalf("CanDispatch = false (%s): a further run of a multi-SUT version was "+
			"refused under the default unlimited quota. A restored finite global default or "+
			"a cross-system-style count of the running set is the design-decision regression this test guards", reason)
	}

	// Sanity check that the rule is really live and this is not a vacuous pass:
	// a configured global limit of 3 refuses the 4th run.
	limited := execution.QuotaFromSettings(3)
	if ok, reason := execution.CanDispatch(limited, usage, 8, 0, 1); ok ||
		reason != execution.ReasonGlobalConcurrency {
		t.Fatalf("CanDispatch at a configured global limit = (%v, %q), want (false, %q): "+
			"the quota rule must be live for the assertion above to mean anything",
			ok, reason, execution.ReasonGlobalConcurrency)
	}
}

// TestExecutionQuotaReaderPropagatesListRunningError exercises the reader's error
// path (review round 7, Minor 2). It matters because RunningCases feeds the
// dispatch gate: a swallowed error would silently report an empty running set,
// and an empty set means the global limit looks free.
func TestExecutionQuotaReaderPropagatesListRunningError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("list running case_runs: connection refused")
	reader := executionQuotaReader{
		caseRuns: &stubQuotaCaseRuns{
			running: []execution.CaseRun{{ID: 9001, IntentID: 1001, Status: execution.CaseRunRunning}},
			listErr: sentinel,
		},
	}
	usage, err := reader.RunningCases(ctx)
	if err == nil {
		t.Fatalf("RunningCases = %+v, nil; want the ListRunning error propagated -- a swallowed "+
			"error would report an empty running set, and an empty set makes the global "+
			"limit look free", usage)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
	if usage != nil {
		t.Fatalf("usage = %+v, want nil alongside the error", usage)
	}
}
