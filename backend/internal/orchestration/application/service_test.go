package orchestrationapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/orchestration/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

/* --------------------------------- fakes ---------------------------------- */

type statusCall struct {
	id   int64
	from orchestration.Status
	to   orchestration.Status
}

type fakeRepo struct {
	used    []int
	usedErr error

	// insertFn may reject an instance; successful instances are recorded in
	// inserted and every attempt in attempts.
	insertFn func(orchestration.Instance) error
	attempts []orchestration.Instance
	inserted []orchestration.Instance

	getInstance orchestration.Instance
	getErr      error

	status    orchestration.Status
	statusErr error

	heartbeatFn    func(id int64, at time.Time) (orchestration.Status, bool, error)
	heartbeatCalls int

	setStatusFn    func(id int64, from, to orchestration.Status, at time.Time) (orchestration.Instance, bool, error)
	setStatusCalls []statusCall

	markStoppedCalls []int64
	markStoppedErr   error

	staleFn     func(cutoff time.Time) ([]orchestration.Instance, error)
	staleCalls  int
	staleCutoff time.Time

	pingErr error
	list    []orchestration.Instance
}

func (f *fakeRepo) UsedNodeIDs(context.Context) ([]int, error) {
	return append([]int(nil), f.used...), f.usedErr
}

func (f *fakeRepo) Insert(_ context.Context, instance orchestration.Instance) error {
	f.attempts = append(f.attempts, instance)
	if f.insertFn != nil {
		if err := f.insertFn(instance); err != nil {
			return err
		}
	}
	f.inserted = append(f.inserted, instance)
	if f.getInstance.ID == 0 && f.getErr == nil {
		f.getInstance = instance
	}
	return nil
}

func (f *fakeRepo) Get(_ context.Context, id int64) (orchestration.Instance, error) {
	if f.getErr != nil {
		return orchestration.Instance{}, f.getErr
	}
	if f.getInstance.ID == id {
		return f.getInstance, nil
	}
	for _, instance := range f.inserted {
		if instance.ID == id {
			return instance, nil
		}
	}
	return orchestration.Instance{}, orchestration.ErrNotFound
}

func (f *fakeRepo) List(context.Context) ([]orchestration.Instance, error) {
	return f.list, nil
}

func (f *fakeRepo) Heartbeat(_ context.Context, _ int64, _ time.Time) (orchestration.Status, bool, error) {
	f.heartbeatCalls++
	if f.heartbeatFn != nil {
		return f.heartbeatFn(0, time.Time{})
	}
	return orchestration.StatusActive, true, nil
}

func (f *fakeRepo) SetStatus(_ context.Context, id int64, from, to orchestration.Status, at time.Time) (orchestration.Instance, bool, error) {
	f.setStatusCalls = append(f.setStatusCalls, statusCall{id: id, from: from, to: to})
	if f.setStatusFn != nil {
		return f.setStatusFn(id, from, to, at)
	}
	instance := f.getInstance
	if instance.ID == 0 {
		instance = fixtureInstance(id, from)
	}
	instance.Status = to
	if to == orchestration.StatusDraining {
		instance.DrainingAt = &at
	}
	return instance, true, nil
}

func (f *fakeRepo) MarkStopped(_ context.Context, id int64, _ time.Time) (bool, error) {
	f.markStoppedCalls = append(f.markStoppedCalls, id)
	if f.markStoppedErr != nil {
		return false, f.markStoppedErr
	}
	return true, nil
}

func (f *fakeRepo) MarkOfflineStale(_ context.Context, cutoff time.Time) ([]orchestration.Instance, error) {
	f.staleCalls++
	f.staleCutoff = cutoff
	if f.staleFn != nil {
		return f.staleFn(cutoff)
	}
	return nil, nil
}

func (f *fakeRepo) Status(context.Context, int64) (orchestration.Status, error) {
	if f.statusErr != nil {
		return "", f.statusErr
	}
	if f.status == "" {
		return orchestration.StatusActive, nil
	}
	return f.status, nil
}

func (f *fakeRepo) Ping(context.Context) error { return f.pingErr }

type fakeSettings struct {
	ints   map[string]int64
	errs   map[string]error
	intErr error
}

func (f *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	if f.errs != nil {
		if err, ok := f.errs[key]; ok {
			return 0, err
		}
	}
	if f.intErr != nil {
		return 0, f.intErr
	}
	if value, ok := f.ints[key]; ok {
		return value, nil
	}
	return 0, errors.New("missing setting " + key)
}

type fakeClock struct{ at time.Time }

func (f *fakeClock) Now() time.Time { return f.at }

type seqIDs struct{ next int64 }

func (f *seqIDs) Next() int64 { f.next++; return f.next }

type fakePublisher struct{ events []eventingapp.Event }

func (f *fakePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

type fakeAudit struct{ events []auditapp.Event }

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

type fakeProbe struct{ err error }

func (f *fakeProbe) Probe(context.Context) error { return f.err }

var fixedNow = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func fixtureInstance(id int64, status orchestration.Status) orchestration.Instance {
	return orchestration.Instance{
		ID:          id,
		InstanceKey: "host-a:42",
		Hostname:    "host-a",
		PID:         42,
		APIPort:     8080,
		NodeID:      3,
		Status:      status,
		Version:     "0.2.0",
		StartedAt:   fixedNow,
		HeartbeatAt: fixedNow,
		CreatedAt:   fixedNow,
		UpdatedAt:   fixedNow,
	}
}

type harness struct {
	repo     *fakeRepo
	settings *fakeSettings
	clock    *fakeClock
	ids      *seqIDs
	events   *fakePublisher
	audit    *fakeAudit
	probe    *fakeProbe
	service  *Service
}

func newHarness() *harness {
	repo := &fakeRepo{}
	settings := &fakeSettings{ints: map[string]int64{
		SettingHeartbeatSeconds: 10,
		SettingOfflineSeconds:   30,
		SettingNodeID:           7,
	}}
	h := &harness{
		repo:     repo,
		settings: settings,
		clock:    &fakeClock{at: fixedNow},
		ids:      &seqIDs{next: 1000},
		events:   &fakePublisher{},
		audit:    &fakeAudit{},
		probe:    &fakeProbe{},
	}
	h.service = New(repo, h.ids, h.clock, settings, h.probe, h.audit, h.events)
	h.service.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h
}

func (h *harness) start(t *testing.T) orchestration.Instance {
	t.Helper()
	instance, err := h.service.Start(context.Background(), StartMeta{
		Hostname: "host-a", PID: 42, APIPort: 8080, Version: "0.2.0",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.service.Stop(context.Background()) })
	return instance
}

func eventTypes(events []eventingapp.Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

/* -------------------------------- allocation ------------------------------ */

func TestStartUsesPreferredNodeWhenFree(t *testing.T) {
	h := newHarness()
	h.repo.used = []int{0, 1, 2}
	instance := h.start(t)

	if instance.NodeID != 7 {
		t.Fatalf("node_id = %d, want preferred 7", instance.NodeID)
	}
	if instance.InstanceKey != "host-a:42" || instance.Status != orchestration.StatusActive {
		t.Fatalf("instance = %+v", instance)
	}
	if !instance.StartedAt.Equal(fixedNow) || !instance.HeartbeatAt.Equal(fixedNow) {
		t.Fatalf("timestamps = %v / %v", instance.StartedAt, instance.HeartbeatAt)
	}
	if h.service.NodeID() != 7 || h.service.InstanceID() != instance.ID {
		t.Fatalf("service node/instance = %d/%d", h.service.NodeID(), h.service.InstanceID())
	}
	if len(h.events.events) != 1 || h.events.events[0].Type != EventRegistered || h.events.events[0].Channel != "system" {
		t.Fatalf("events = %#v", h.events.events)
	}
	payload := h.events.events[0].Payload
	if payload["instance_id"] != platformid.Format(instance.ID) || payload["node_id"] != 7 || payload["instance_key"] != "host-a:42" {
		t.Fatalf("registered payload = %#v", payload)
	}
}

func TestStartFallsBackToSmallestFreeNode(t *testing.T) {
	h := newHarness()
	h.repo.used = []int{7, 1}
	instance := h.start(t)
	if instance.NodeID != 0 {
		t.Fatalf("node_id = %d, want smallest free 0", instance.NodeID)
	}
}

func TestStartRetriesNextCandidateOnUniqueConflict(t *testing.T) {
	h := newHarness()
	h.repo.insertFn = func(instance orchestration.Instance) error {
		if instance.NodeID == 7 {
			return orchestration.ErrNodeTaken
		}
		return nil
	}
	instance := h.start(t)
	if instance.NodeID != 0 {
		t.Fatalf("node_id = %d, want fallback 0", instance.NodeID)
	}
	if len(h.repo.attempts) != 2 || h.repo.attempts[0].NodeID != 7 || h.repo.attempts[1].NodeID != 0 {
		t.Fatalf("attempts = %#v", h.repo.attempts)
	}
}

func TestStartFailsWhenNoNodeIsFree(t *testing.T) {
	h := newHarness()
	for node := 0; node <= MaxNodeID; node++ {
		h.repo.used = append(h.repo.used, node)
	}
	_, err := h.service.Start(context.Background(), StartMeta{Hostname: "host-a", PID: 42, APIPort: 8080})
	if err == nil {
		t.Fatal("Start succeeded with every node allocated")
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != 409 {
		t.Fatalf("status = %d, want 409", apiErr.Status)
	}
	if len(h.repo.attempts) != 0 {
		t.Fatalf("inserted %d times with no candidate", len(h.repo.attempts))
	}
}

func TestStartIgnoresOutOfRangePreferredNode(t *testing.T) {
	h := newHarness()
	h.settings.ints[SettingNodeID] = 2000
	h.repo.used = []int{0}
	instance := h.start(t)
	if instance.NodeID != 1 {
		t.Fatalf("node_id = %d, want first free 1", instance.NodeID)
	}
}

func TestStartRejectsInvalidMeta(t *testing.T) {
	cases := []StartMeta{
		{Hostname: " ", PID: 42, APIPort: 8080},
		{Hostname: "host-a", PID: 0, APIPort: 8080},
		{Hostname: "host-a", PID: 42, APIPort: 0},
		{Hostname: "host-a", PID: 42, APIPort: 70000},
	}
	for _, meta := range cases {
		h := newHarness()
		if _, err := h.service.Start(context.Background(), meta); err == nil {
			t.Fatalf("Start(%+v) succeeded", meta)
		} else if code := platform.AsError(err).Code; code != "invalid_argument" {
			t.Fatalf("Start(%+v) code = %q", meta, code)
		}
		if len(h.repo.attempts) != 0 {
			t.Fatalf("invalid meta reached the repository")
		}
	}
}

func TestStartRejectsSecondRegistration(t *testing.T) {
	h := newHarness()
	h.start(t)
	if _, err := h.service.Start(context.Background(), StartMeta{Hostname: "host-a", PID: 43, APIPort: 8081}); err == nil {
		t.Fatal("second Start succeeded")
	}
	if len(h.repo.attempts) != 1 {
		t.Fatalf("attempts = %d", len(h.repo.attempts))
	}
}

func TestStartRejectsTakenInstanceKey(t *testing.T) {
	h := newHarness()
	h.repo.insertFn = func(orchestration.Instance) error { return orchestration.ErrInstanceKeyTaken }
	if _, err := h.service.Start(context.Background(), StartMeta{Hostname: "host-a", PID: 42, APIPort: 8080}); err == nil {
		t.Fatal("Start succeeded for a duplicate instance key")
	} else if platform.AsError(err).Status != 409 {
		t.Fatalf("status = %d, want 409", platform.AsError(err).Status)
	}
}

// TestStartRetriesFreshIDOnInstanceIDConflict pins the simultaneous-start fix:
// two replicas that share the bootstrap snowflake generator can mint the same
// api_instance.id in the same millisecond; Start must allocate a fresh ID for
// the same node and retry instead of failing the whole registration.
func TestStartRetriesFreshIDOnInstanceIDConflict(t *testing.T) {
	h := newHarness()
	h.repo.used = []int{7}
	inserts := 0
	h.repo.insertFn = func(orchestration.Instance) error {
		inserts++
		if inserts <= 2 {
			return orchestration.ErrInstanceIDTaken
		}
		return nil
	}
	instance := h.start(t)

	if len(h.repo.attempts) != 3 {
		t.Fatalf("insert attempts = %d, want 3", len(h.repo.attempts))
	}
	firstID := h.repo.attempts[0].ID
	for index, attempt := range h.repo.attempts {
		if index > 0 && attempt.ID == firstID {
			t.Fatalf("attempt %d reused the colliding id %d", index, attempt.ID)
		}
		if attempt.NodeID != 0 {
			t.Fatalf("attempt %d moved to node %d, want the same candidate 0", index, attempt.NodeID)
		}
	}
	if instance.ID != h.repo.attempts[2].ID || h.service.InstanceID() != instance.ID {
		t.Fatalf("instance id = %d, want the third allocated id %d", instance.ID, h.repo.attempts[2].ID)
	}
}

// TestStartFailsAfterInstanceIDRetryBudget pins the bounded retry: a persistent
// api_instance.id conflict becomes a stable 409 instead of an infinite loop.
func TestStartFailsAfterInstanceIDRetryBudget(t *testing.T) {
	h := newHarness()
	h.repo.insertFn = func(orchestration.Instance) error { return orchestration.ErrInstanceIDTaken }
	_, err := h.service.Start(context.Background(), StartMeta{Hostname: "host-a", PID: 42, APIPort: 8080})
	if platform.AsError(err).Status != 409 {
		t.Fatalf("Start error = %v, want 409", err)
	}
	if len(h.repo.attempts) != 3 {
		t.Fatalf("insert attempts = %d, want the bounded 3", len(h.repo.attempts))
	}
}

/* ------------------------------- transitions ------------------------------ */

func TestDrainSelfTransitionsToDraining(t *testing.T) {
	h := newHarness()
	instance := h.start(t)
	h.repo.getInstance = instance
	h.repo.status = orchestration.StatusDraining

	actor := platform.Actor{ID: 9, Role: "admin"}
	updated, err := h.service.Drain(context.Background(), instance.ID, actor)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if updated.Status != orchestration.StatusDraining {
		t.Fatalf("drained status = %q", updated.Status)
	}
	if !h.service.IsDraining() {
		t.Fatal("self drain did not flip IsDraining")
	}
	if len(h.repo.setStatusCalls) != 1 ||
		h.repo.setStatusCalls[0].from != orchestration.StatusActive ||
		h.repo.setStatusCalls[0].to != orchestration.StatusDraining {
		t.Fatalf("set status calls = %#v", h.repo.setStatusCalls)
	}

	var drainedEvents int
	for _, event := range h.events.events {
		if event.Type == EventDrained {
			drainedEvents++
			if event.Channel != "system" || event.Payload["instance_id"] != platformid.Format(instance.ID) ||
				event.Payload["status"] != string(orchestration.StatusDraining) {
				t.Fatalf("drained event = %#v", event)
			}
			if event.ActorID == nil || *event.ActorID != 9 {
				t.Fatalf("event actor = %#v", event.ActorID)
			}
		}
	}
	if drainedEvents != 1 {
		t.Fatalf("drained events = %d, want 1", drainedEvents)
	}
	if len(h.audit.events) != 1 || h.audit.events[0].Action != ActionDrained ||
		h.audit.events[0].ActorID == nil || *h.audit.events[0].ActorID != 9 {
		t.Fatalf("audit = %#v", h.audit.events)
	}

	// Draining twice is idempotent and must not duplicate event/audit rows.
	h.repo.getInstance = updated
	if again, err := h.service.Drain(context.Background(), instance.ID, actor); err != nil || again.Status != orchestration.StatusDraining {
		t.Fatalf("second Drain = %+v, err = %v", again, err)
	}
	if len(h.repo.setStatusCalls) != 1 || len(h.audit.events) != 1 {
		t.Fatalf("duplicate drain calls = %d, audits = %d", len(h.repo.setStatusCalls), len(h.audit.events))
	}
}

func TestDrainRejectsDeadInstances(t *testing.T) {
	for _, status := range []orchestration.Status{orchestration.StatusOffline, orchestration.StatusStopped} {
		h := newHarness()
		h.repo.getInstance = fixtureInstance(42, status)
		_, err := h.service.Drain(context.Background(), 42, platform.Actor{ID: 9})
		if err == nil {
			t.Fatalf("Drain(%q) succeeded", status)
		}
		if platform.AsError(err).Code != "state_conflict" {
			t.Fatalf("Drain(%q) code = %q", status, platform.AsError(err).Code)
		}
		if len(h.repo.setStatusCalls) != 0 || len(h.events.events) != 0 || len(h.audit.events) != 0 {
			t.Fatalf("Drain(%q) had side effects", status)
		}
	}
}

func TestDrainUnknownInstanceIsNotFound(t *testing.T) {
	h := newHarness()
	_, err := h.service.Drain(context.Background(), 999, platform.Actor{ID: 9})
	if err == nil || platform.AsError(err).Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestActivateTransitionsBackToActive(t *testing.T) {
	h := newHarness()
	instance := h.start(t)
	h.repo.getInstance = fixtureInstance(instance.ID, orchestration.StatusDraining)
	h.repo.status = orchestration.StatusActive
	h.service.draining = true

	actor := platform.Actor{ID: 9, Role: "admin"}
	updated, err := h.service.Activate(context.Background(), instance.ID, actor)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if updated.Status != orchestration.StatusActive {
		t.Fatalf("activated status = %q", updated.Status)
	}
	if h.service.IsDraining() {
		t.Fatal("activate did not clear IsDraining")
	}
	if len(h.repo.setStatusCalls) != 1 ||
		h.repo.setStatusCalls[0].from != orchestration.StatusDraining ||
		h.repo.setStatusCalls[0].to != orchestration.StatusActive {
		t.Fatalf("set status calls = %#v", h.repo.setStatusCalls)
	}
	if len(h.audit.events) != 1 || h.audit.events[0].Action != ActionActivated {
		t.Fatalf("audit = %#v", h.audit.events)
	}
	var activated int
	for _, event := range h.events.events {
		if event.Type == EventActivated {
			activated++
			if event.Payload["status"] != string(orchestration.StatusActive) {
				t.Fatalf("activated payload = %#v", event.Payload)
			}
		}
	}
	if activated != 1 {
		t.Fatalf("activated events = %d", activated)
	}
}

func TestActivateIsIdempotentAndRejectsDeadInstances(t *testing.T) {
	h := newHarness()
	h.repo.getInstance = fixtureInstance(42, orchestration.StatusActive)
	if instance, err := h.service.Activate(context.Background(), 42, platform.Actor{ID: 9}); err != nil || instance.Status != orchestration.StatusActive {
		t.Fatalf("idempotent Activate = %+v, err = %v", instance, err)
	}
	if len(h.repo.setStatusCalls) != 0 || len(h.audit.events) != 0 {
		t.Fatal("idempotent Activate had side effects")
	}

	for _, status := range []orchestration.Status{orchestration.StatusOffline, orchestration.StatusStopped} {
		dead := newHarness()
		dead.repo.getInstance = fixtureInstance(43, status)
		if _, err := dead.service.Activate(context.Background(), 43, platform.Actor{ID: 9}); err == nil {
			t.Fatalf("Activate(%q) succeeded", status)
		}
	}
}

/* -------------------------------- readiness ------------------------------- */

func TestReadinessRequiresDatabaseStorageAndActiveStatus(t *testing.T) {
	cases := []struct {
		name      string
		pingErr   error
		probeErr  error
		status    orchestration.Status
		statusErr error
		nilProbe  bool
		want      Readiness
	}{
		{
			name: "ready",
			want: Readiness{Database: true, Storage: true},
		},
		{
			name:    "database down",
			pingErr: errors.New("db down"),
			want:    Readiness{},
		},
		{
			name:     "storage unavailable",
			probeErr: errors.New("read-only filesystem"),
			want:     Readiness{Database: true},
		},
		{
			name:   "draining",
			status: orchestration.StatusDraining,
			want:   Readiness{Database: true, Storage: true, Draining: true},
		},
		{
			name:      "status lookup failure",
			statusErr: errors.New("db gone"),
			want:      Readiness{},
		},
		{
			name:     "nil storage probe",
			nilProbe: true,
			want:     Readiness{Database: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			instance := h.start(t)
			h.repo.pingErr = tc.pingErr
			h.repo.status = tc.status
			h.repo.statusErr = tc.statusErr
			if tc.probeErr != nil {
				h.probe.err = tc.probeErr
			}
			var probe StorageProbe = h.probe
			if tc.nilProbe {
				probe = nil
			}
			service := New(h.repo, h.ids, h.clock, h.settings, probe, h.audit, h.events)
			service.instance = instance

			got := service.Readiness(context.Background())
			if got.InstanceID != instance.ID {
				t.Fatalf("instance_id = %d, want %d", got.InstanceID, instance.ID)
			}
			want := tc.want
			want.InstanceID = instance.ID
			if got != want {
				t.Fatalf("readiness = %+v, want %+v", got, want)
			}
			if got.Ready() != (tc.pingErr == nil && tc.probeErr == nil && !tc.nilProbe && tc.statusErr == nil && tc.status != orchestration.StatusDraining) {
				t.Fatalf("Ready() = %v", got.Ready())
			}
		})
	}
}

/* ------------------------------ stale scanning ---------------------------- */

func TestMarkOfflineStaleMarksAndPublishesTransitions(t *testing.T) {
	h := newHarness()
	stale := []orchestration.Instance{
		fixtureInstance(1, orchestration.StatusOffline),
		fixtureInstance(2, orchestration.StatusOffline),
	}
	h.repo.staleFn = func(time.Time) ([]orchestration.Instance, error) { return stale, nil }

	marked, err := h.service.MarkOfflineStale(context.Background())
	if err != nil {
		t.Fatalf("MarkOfflineStale: %v", err)
	}
	if len(marked) != 2 {
		t.Fatalf("marked = %d", len(marked))
	}
	wantCutoff := fixedNow.Add(-30 * time.Second)
	if !h.repo.staleCutoff.Equal(wantCutoff) {
		t.Fatalf("cutoff = %v, want %v", h.repo.staleCutoff, wantCutoff)
	}
	if len(h.events.events) != 2 {
		t.Fatalf("offline events = %d", len(h.events.events))
	}
	for index, event := range h.events.events {
		if event.Type != EventOffline || event.Channel != "system" ||
			event.Payload["instance_id"] != platformid.Format(stale[index].ID) ||
			event.Payload["status"] != string(orchestration.StatusOffline) {
			t.Fatalf("offline event %d = %#v", index, event)
		}
	}
	if len(h.audit.events) != 0 {
		t.Fatalf("offline scan wrote audit rows: %#v", h.audit.events)
	}
}

func TestMarkOfflineStaleFallsBackToDefaultWindow(t *testing.T) {
	h := newHarness()
	h.settings.ints[SettingOfflineSeconds] = 0
	h.settings.errs = map[string]error{SettingOfflineSeconds: errors.New("missing")}
	if _, err := h.service.MarkOfflineStale(context.Background()); err != nil {
		t.Fatalf("MarkOfflineStale: %v", err)
	}
	if want := fixedNow.Add(-30 * time.Second); !h.repo.staleCutoff.Equal(want) {
		t.Fatalf("cutoff = %v, want default %v", h.repo.staleCutoff, want)
	}
}

func TestRunMaintenanceScansImmediatelyAndStops(t *testing.T) {
	h := newHarness()
	scanned := make(chan struct{}, 1)
	h.repo.staleFn = func(time.Time) ([]orchestration.Instance, error) {
		select {
		case scanned <- struct{}{}:
		default:
		}
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.service.RunMaintenance(ctx) }()
	select {
	case <-scanned:
	case <-time.After(2 * time.Second):
		t.Fatal("maintenance did not scan immediately")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunMaintenance = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunMaintenance did not stop after cancel")
	}
}

/* -------------------------------- heartbeat ------------------------------- */

func TestHeartbeatMirrorsRemoteDrainingStatus(t *testing.T) {
	h := newHarness()
	h.service.instance = fixtureInstance(42, orchestration.StatusActive)
	h.service.started = true
	h.repo.heartbeatFn = func(int64, time.Time) (orchestration.Status, bool, error) {
		return orchestration.StatusDraining, true, nil
	}
	h.service.heartbeatOnce(context.Background())
	if !h.service.IsDraining() {
		t.Fatal("heartbeat did not adopt the DB draining status")
	}

	h.repo.heartbeatFn = func(int64, time.Time) (orchestration.Status, bool, error) {
		return orchestration.StatusActive, true, nil
	}
	h.service.heartbeatOnce(context.Background())
	if h.service.IsDraining() {
		t.Fatal("heartbeat did not clear the local draining mirror")
	}

	// A failed heartbeat must not flip the local state or panic.
	h.repo.heartbeatFn = func(int64, time.Time) (orchestration.Status, bool, error) {
		return "", false, errors.New("db down")
	}
	h.service.heartbeatOnce(context.Background())
	if h.service.IsDraining() {
		t.Fatal("failed heartbeat changed state")
	}
}

func TestStopMarksStoppedOnce(t *testing.T) {
	h := newHarness()
	instance := h.start(t)
	if err := h.service.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(h.repo.markStoppedCalls) != 1 || h.repo.markStoppedCalls[0] != instance.ID {
		t.Fatalf("markStopped calls = %#v", h.repo.markStoppedCalls)
	}
	if err := h.service.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if len(h.repo.markStoppedCalls) != 1 {
		t.Fatalf("second Stop re-marked: %d calls", len(h.repo.markStoppedCalls))
	}
}

func TestStopReturnsRepositoryError(t *testing.T) {
	h := newHarness()
	h.start(t)
	h.repo.markStoppedErr = errors.New("db down")
	if err := h.service.Stop(context.Background()); !errors.Is(err, h.repo.markStoppedErr) {
		t.Fatalf("Stop error = %v", err)
	}
}

/* --------------------------------- roster --------------------------------- */

func TestListDelegates(t *testing.T) {
	h := newHarness()
	h.repo.list = []orchestration.Instance{fixtureInstance(1, orchestration.StatusActive)}
	instances, err := h.service.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != 1 {
		t.Fatalf("instances = %#v", instances)
	}
}
