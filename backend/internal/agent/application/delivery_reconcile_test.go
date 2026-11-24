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

// deliveryCall is one P4 ensure invocation recorded by the fake ensurer.
type deliveryCall struct {
	Rule      string
	VersionID int64
}

// fakeDeliveryEnsurer records every P4 reconciler call and fails the configured
// rules, so a test can prove one failing rule neither stops the others nor the
// pre-existing P1/P2 rules.
type fakeDeliveryEnsurer struct {
	mu       sync.Mutex
	calls    []deliveryCall
	failRule map[string]error
}

func (e *fakeDeliveryEnsurer) record(rule string, versionID int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, deliveryCall{Rule: rule, VersionID: versionID})
	return e.failRule[rule]
}

func (e *fakeDeliveryEnsurer) ScanFailures(_ context.Context, versionID int64) error {
	return e.record("scan_failures", versionID)
}

func (e *fakeDeliveryEnsurer) EnsureReports(_ context.Context, versionID int64) error {
	return e.record("ensure_reports", versionID)
}

func (e *fakeDeliveryEnsurer) EnsureClosures(_ context.Context, versionID int64) error {
	return e.record("ensure_closures", versionID)
}

func (e *fakeDeliveryEnsurer) EnsureVersionClose(_ context.Context, versionID int64) error {
	return e.record("ensure_version_close", versionID)
}

func (e *fakeDeliveryEnsurer) recorded() []deliveryCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]deliveryCall(nil), e.calls...)
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

type deliveryReconcilerFixture struct {
	reconciler *agentapp.Reconciler
	projector  *fakeProjector
	design     *fakeDesignReconciler
	queue      *fakeEnsurer
	delivery   *fakeDeliveryEnsurer
	versions   *fakeRunningVersions
}

func newDeliveryReconcilerFixture(t *testing.T) *deliveryReconcilerFixture {
	t.Helper()
	fixture := &deliveryReconcilerFixture{
		projector: &fakeProjector{},
		design:    &fakeDesignReconciler{},
		queue:     &fakeEnsurer{},
		delivery:  &fakeDeliveryEnsurer{failRule: map[string]error{}},
		versions:  &fakeRunningVersions{},
	}
	// The fixture intentionally wires no conversations/attachments: those
	// rules are covered by reconciler_test.go, and their absence keeps the
	// P4 call assertions focused.
	fixture.reconciler = agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions:        fixture.projector,
		Design:          fixture.design,
		Queue:           fixture.queue,
		Delivery:        fixture.delivery,
		RunningVersions: fixture.versions,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return fixture
}

func deliveryRules() []string {
	return []string{"scan_failures", "ensure_reports", "ensure_closures", "ensure_version_close"}
}

func assertDeliveryCalls(t *testing.T, calls []deliveryCall, versionID int64, rules []string) {
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
			t.Fatalf("delivery rule %q was not called for version %d (calls=%v)", rule, versionID, calls)
		}
	}
}

func TestEnsureRunningVersionsReceiveEveryRule(t *testing.T) {
	fixture := newDeliveryReconcilerFixture(t)
	fixture.versions.ids = []int64{10, 11}

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	calls := fixture.delivery.recorded()
	if len(calls) != 8 {
		t.Fatalf("delivery calls = %v, want four rules for each of two running versions", calls)
	}
	assertDeliveryCalls(t, calls, 10, deliveryRules())
	assertDeliveryCalls(t, calls, 11, deliveryRules())
	if fixture.versions.callCount() != 1 {
		t.Fatalf("running version scan count = %d, want one per pass", fixture.versions.callCount())
	}
}

func TestEnsureWithoutRunningVersionsDoesNoDeliveryWork(t *testing.T) {
	fixture := newDeliveryReconcilerFixture(t)
	// Only completed versions exist: the version context returns no running
	// IDs, so P4 must not ensure anything (completed versions only get the
	// reopen scan owned by the P4 event hooks).
	fixture.versions.ids = nil

	if err := fixture.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if calls := fixture.delivery.recorded(); len(calls) != 0 {
		t.Fatalf("delivery calls = %v, want none without running versions", calls)
	}
	if fixture.design.callCount() != 1 {
		t.Fatalf("design rule calls = %d, want the existing rules to run", fixture.design.callCount())
	}
}

func TestEnsureErrorsDoNotStopExistingRulesOrOtherVersions(t *testing.T) {
	fixture := newDeliveryReconcilerFixture(t)
	fixture.versions.ids = []int64{10, 11}
	fixture.delivery.failRule["ensure_reports"] = errors.New("report scan failed")

	err := fixture.reconciler.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile returned nil despite a failing delivery rule")
	}
	if fixture.design.callCount() != 1 {
		t.Fatalf("design rule calls = %d, want the existing rules to run despite delivery errors", fixture.design.callCount())
	}
	calls := fixture.delivery.recorded()
	assertDeliveryCalls(t, calls, 10, deliveryRules())
	assertDeliveryCalls(t, calls, 11, deliveryRules())
}

func TestEnsureVersionReaderFailureIsAggregated(t *testing.T) {
	fixture := newDeliveryReconcilerFixture(t)
	fixture.versions.err = errors.New("version scan failed")

	err := fixture.reconciler.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile returned nil despite a failing running-version scan")
	}
	if fixture.design.callCount() != 1 {
		t.Fatalf("design rule calls = %d, want the existing rules to run", fixture.design.callCount())
	}
	if calls := fixture.delivery.recorded(); len(calls) != 0 {
		t.Fatalf("delivery calls = %v, want none when the version scan fails", calls)
	}
}

func TestReconcilerDeliveryNilSafety(t *testing.T) {
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
		delivery := &fakeDeliveryEnsurer{failRule: map[string]error{}}
		reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
			Versions: &fakeProjector{},
			Design:   design,
			Delivery: delivery,
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(delivery.recorded()) != 0 {
			t.Fatalf("delivery calls = %v, want none", delivery.recorded())
		}
		if design.callCount() != 1 {
			t.Fatalf("design rule calls = %d", design.callCount())
		}
	})
}
