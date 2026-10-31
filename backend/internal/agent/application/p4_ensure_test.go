package agentapp_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// p4Call is one P4 ensure invocation recorded by the fake ensurer.
type p4Call struct {
	Rule      string
	VersionID int64
}

// fakeP4Ensurer records every P4 reconciler call and fails the configured
// rules, so a test can prove one failing rule neither stops the others nor the
// pre-existing P1/P2 rules.
type fakeP4Ensurer struct {
	mu       sync.Mutex
	calls    []p4Call
	failRule map[string]error
}

func (e *fakeP4Ensurer) record(rule string, versionID int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, p4Call{Rule: rule, VersionID: versionID})
	return e.failRule[rule]
}

func (e *fakeP4Ensurer) ScanFailures(_ context.Context, versionID int64) error {
	return e.record("scan_failures", versionID)
}

func (e *fakeP4Ensurer) EnsureReports(_ context.Context, versionID int64) error {
	return e.record("ensure_reports", versionID)
}

func (e *fakeP4Ensurer) EnsureClosures(_ context.Context, versionID int64) error {
	return e.record("ensure_closures", versionID)
}

func (e *fakeP4Ensurer) EnsureVersionClose(_ context.Context, versionID int64) error {
	return e.record("ensure_version_close", versionID)
}

func (e *fakeP4Ensurer) recorded() []p4Call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]p4Call(nil), e.calls...)
}

// fakeRunningVersions is the P4 version reader. It returns the same running
// version IDs the version context exposes; completed versions are never part of
// the result (the version context filters them).
type fakeRunningVersions struct {
	mu    sync.Mutex
	ids   []int64
	calls int
	err   error
}

func (r *fakeRunningVersions) ListRunningVersions(_ context.Context, limit int) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	ids := r.ids
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return append([]int64(nil), ids...), nil
}

func (r *fakeRunningVersions) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type p4ReconcilerFixture struct {
	reconciler *agentapp.Reconciler
	projector  *fakeProjector
	design     *fakeDesignReconciler
	queue      *fakeEnsurer
	p4         *fakeP4Ensurer
	versions   *fakeRunningVersions
}

func newP4ReconcilerFixture(t *testing.T) *p4ReconcilerFixture {
	t.Helper()
	fixture := &p4ReconcilerFixture{
		projector: &fakeProjector{},
		design:    &fakeDesignReconciler{},
		queue:     &fakeEnsurer{},
		p4:        &fakeP4Ensurer{failRule: map[string]error{}},
		versions:  &fakeRunningVersions{},
	}
	// The fixture intentionally wires no conversations/attachments: those
	// rules are covered by reconciler_test.go, and their absence keeps the
	// P4 call assertions focused.
	fixture.reconciler = agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions:        fixture.projector,
		Design:          fixture.design,
		Queue:           fixture.queue,
		P4:              fixture.p4,
		RunningVersions: fixture.versions,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return fixture
}

func p4Rules() []string {
	return []string{"scan_failures", "ensure_reports", "ensure_closures", "ensure_version_close"}
}

func assertP4Calls(t *testing.T, calls []p4Call, versionID int64, rules []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, call := range calls {
		if call.VersionID != versionID {
			continue
		}
		seen[call.Rule] = true
	}
	for _, rule := range rules {
		if !seen[rule] {
			t.Fatalf("p4 rule %q was not called for version %d (calls=%v)", rule, versionID, calls)
		}
	}
}

func TestP4EnsureRunningVersionsReceiveEveryRule(t *testing.T) {
	fixture := newP4ReconcilerFixture(t)
	fixture.versions.ids = []int64{10, 11}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	calls := fixture.p4.recorded()
	if len(calls) != 8 {
		t.Fatalf("p4 calls = %v, want four rules for each of two running versions", calls)
	}
	assertP4Calls(t, calls, 10, p4Rules())
	assertP4Calls(t, calls, 11, p4Rules())
	if fixture.versions.callCount() != 1 {
		t.Fatalf("running version scan count = %d, want one per pass", fixture.versions.callCount())
	}
}

func TestP4EnsureWithoutRunningVersionsDoesNoP4Work(t *testing.T) {
	fixture := newP4ReconcilerFixture(t)
	// Only completed versions exist: the version context returns no running
	// IDs, so P4 must not ensure anything (completed versions only get the
	// reopen scan owned by the P4 event hooks).
	fixture.versions.ids = nil

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if calls := fixture.p4.recorded(); len(calls) != 0 {
		t.Fatalf("p4 calls = %v, want none without running versions", calls)
	}
	if fixture.design.callCount() != 1 {
		t.Fatalf("design rule calls = %d, want the existing rules to run", fixture.design.callCount())
	}
}

func TestP4EnsureErrorsDoNotStopExistingRulesOrOtherVersions(t *testing.T) {
	fixture := newP4ReconcilerFixture(t)
	fixture.versions.ids = []int64{10, 11}
	fixture.p4.failRule["ensure_reports"] = errors.New("report scan failed")

	err := fixture.reconciler.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile returned nil despite a failing P4 rule")
	}
	if fixture.design.callCount() != 1 {
		t.Fatalf("design rule calls = %d, want the existing rules to run despite P4 errors", fixture.design.callCount())
	}
	calls := fixture.p4.recorded()
	assertP4Calls(t, calls, 10, p4Rules())
	assertP4Calls(t, calls, 11, p4Rules())
}

func TestP4EnsureVersionReaderFailureIsAggregated(t *testing.T) {
	fixture := newP4ReconcilerFixture(t)
	fixture.versions.err = errors.New("version scan failed")

	err := fixture.reconciler.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile returned nil despite a failing running-version scan")
	}
	if fixture.design.callCount() != 1 {
		t.Fatalf("design rule calls = %d, want the existing rules to run", fixture.design.callCount())
	}
	if calls := fixture.p4.recorded(); len(calls) != 0 {
		t.Fatalf("p4 calls = %v, want none when the version scan fails", calls)
	}
}

func TestReconcilerP4NilSafety(t *testing.T) {
	t.Run("nil ensurer", func(t *testing.T) {
		design := &fakeDesignReconciler{}
		versions := &fakeRunningVersions{ids: []int64{10}}
		reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
			Versions:        &fakeProjector{},
			Design:          design,
			RunningVersions: versions,
			Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if design.callCount() != 1 {
			t.Fatalf("design rule calls = %d", design.callCount())
		}
	})
	t.Run("nil version reader", func(t *testing.T) {
		design := &fakeDesignReconciler{}
		p4 := &fakeP4Ensurer{failRule: map[string]error{}}
		reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
			Versions: &fakeProjector{},
			Design:   design,
			P4:       p4,
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(p4.recorded()) != 0 {
			t.Fatalf("p4 calls = %v, want none", p4.recorded())
		}
		if design.callCount() != 1 {
			t.Fatalf("design rule calls = %d", design.callCount())
		}
	})
}
