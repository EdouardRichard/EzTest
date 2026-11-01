package executionapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

type fakeExecutorRepo struct {
	mu        sync.Mutex
	rows      map[int64]execution.ExecutorInstance
	nextID    int64
	upserts   int
	lastError string
	statuses  []execution.ExecutorStatus
	getErr    error
}

func newFakeExecutorRepo() *fakeExecutorRepo {
	return &fakeExecutorRepo{rows: map[int64]execution.ExecutorInstance{}}
}

func (f *fakeExecutorRepo) UpsertHeartbeat(_ context.Context, instance execution.ExecutorInstance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts++
	f.lastError = instance.LastError
	if instance.ID == 0 {
		f.nextID++
		instance.ID = f.nextID
	}
	for id, existing := range f.rows {
		if existing.Endpoint == instance.Endpoint && id != instance.ID {
			instance.ID = id
			delete(f.rows, id)
		}
	}
	f.rows[instance.ID] = instance
	return nil
}

func (f *fakeExecutorRepo) GetByEndpoint(_ context.Context, endpoint string) (execution.ExecutorInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return execution.ExecutorInstance{}, f.getErr
	}
	for _, row := range f.rows {
		if row.Endpoint == endpoint {
			return row, nil
		}
	}
	return execution.ExecutorInstance{}, execution.ErrNotFound
}

func (f *fakeExecutorRepo) Get(_ context.Context, id int64) (execution.ExecutorInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return execution.ExecutorInstance{}, execution.ErrNotFound
	}
	return row, nil
}

func (f *fakeExecutorRepo) List(context.Context) ([]execution.ExecutorInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]execution.ExecutorInstance, 0, len(f.rows))
	for _, row := range f.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (f *fakeExecutorRepo) SetStatus(_ context.Context, id int64, status execution.ExecutorStatus) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return false, nil
	}
	row.Status = status
	f.rows[id] = row
	f.statuses = append(f.statuses, status)
	return true, nil
}

func (f *fakeExecutorRepo) only(t *testing.T) execution.ExecutorInstance {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rows) != 1 {
		t.Fatalf("executor rows = %d, want 1", len(f.rows))
	}
	for _, row := range f.rows {
		return row
	}
	return execution.ExecutorInstance{}
}

type fakeExecutorProbe struct {
	health    executionapp.ExecutorHealth
	healthErr error
	contexts  []executionapp.ExecutorContextRef
	listErr   error
	calls     int
}

func (f *fakeExecutorProbe) Health(context.Context) (executionapp.ExecutorHealth, error) {
	f.calls++
	return f.health, f.healthErr
}

func (f *fakeExecutorProbe) ListContexts(context.Context) ([]executionapp.ExecutorContextRef, error) {
	return f.contexts, f.listErr
}

type fakeRegistrySettings struct {
	strings map[string]string
	ints    map[string]int64
	err     error
}

func (f *fakeRegistrySettings) String(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	value, ok := f.strings[key]
	if !ok {
		return "", errors.New("setting not found: " + key)
	}
	return value, nil
}

func (f *fakeRegistrySettings) Int(_ context.Context, key string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	value, ok := f.ints[key]
	if !ok {
		return 0, errors.New("setting not found: " + key)
	}
	return value, nil
}

func (f *fakeRegistrySettings) Bool(_ context.Context, key string) (bool, error) {
	return false, nil
}

type registryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *registryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *registryClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

type registryIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *registryIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

const registryEndpoint = "http://127.0.0.1:13336"
const registryToken = "executor-secret-token"

func newRegistryFixture(t *testing.T) (*executionapp.Registry, *fakeExecutorRepo, *fakeExecutorProbe, *registryClock) {
	t.Helper()
	repo := newFakeExecutorRepo()
	probe := &fakeExecutorProbe{health: executionapp.ExecutorHealth{
		Status: "ok", Version: "0.2.0-p3", Contexts: 2, MaxContexts: 8,
	}}
	clock := &registryClock{now: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)}
	registry := executionapp.NewRegistry(executionapp.RegistryDeps{
		Repo:  repo,
		Probe: probe,
		Settings: &fakeRegistrySettings{
			strings: map[string]string{
				"execution.executor_url":   registryEndpoint,
				"execution.executor_token": registryToken,
			},
			ints: map[string]int64{
				"execution.executor_heartbeat_seconds": 10,
				"execution.executor_offline_seconds":   30,
			},
		},
		Clock: clock,
		IDs:   &registryIDs{next: 100},
	})
	return registry, repo, probe, clock
}

func registryTokenHash() string {
	sum := sha256.Sum256([]byte(registryToken))
	return hex.EncodeToString(sum[:])
}

func TestRegistryPollRegistersHealthyExecutor(t *testing.T) {
	registry, repo, _, clock := newRegistryFixture(t)
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	row := repo.only(t)
	if row.ID == 0 {
		t.Fatalf("executor ID was not assigned")
	}
	if row.Endpoint != registryEndpoint || row.Status != execution.ExecutorActive {
		t.Fatalf("executor = %+v", row)
	}
	if row.Capacity != 8 || row.ActiveContexts != 2 || row.Version != "0.2.0-p3" {
		t.Fatalf("executor health projection = %+v", row)
	}
	if row.TokenHash != registryTokenHash() {
		t.Fatalf("token hash = %q, want %q", row.TokenHash, registryTokenHash())
	}
	if row.LastHeartbeat == nil || !row.LastHeartbeat.Equal(clock.Now()) {
		t.Fatalf("last heartbeat = %v", row.LastHeartbeat)
	}
	if row.LastError != "" {
		t.Fatalf("last error = %q", row.LastError)
	}
}

func TestRegistryPollDegradedStillHeartbeats(t *testing.T) {
	registry, repo, probe, _ := newRegistryFixture(t)
	probe.health = executionapp.ExecutorHealth{
		Status: "degraded", Version: "0.2.0-p3", Contexts: 0, MaxContexts: 8,
		Error: "browser unavailable",
	}
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	row := repo.only(t)
	if row.Status != execution.ExecutorActive {
		t.Fatalf("degraded executor should still be active for dispatch, got %q", row.Status)
	}
	if row.LastError != "browser unavailable" {
		t.Fatalf("last error = %q", row.LastError)
	}
}

func TestRegistryPollKeepsDrainingAndMarksOffline(t *testing.T) {
	registry, repo, probe, clock := newRegistryFixture(t)
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	row := repo.only(t)

	// Admin drain: a subsequent successful poll must keep draining.
	if ok, err := registry.Drain(context.Background(), row.ID); err != nil || !ok {
		t.Fatalf("Drain = %v, %v", ok, err)
	}
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got := repo.only(t); got.Status != execution.ExecutorDraining {
		t.Fatalf("status after healthy poll = %q, want draining", got.Status)
	}

	// Health failures within the offline window keep the row but record the
	// error; once the last heartbeat is older than offline_seconds the executor
	// is marked offline.
	probe.healthErr = errors.New("connection refused")
	probe.health = executionapp.ExecutorHealth{}
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got := repo.only(t); got.Status != execution.ExecutorDraining {
		t.Fatalf("status within offline window = %q", got.Status)
	}
	if got := repo.only(t); got.LastError == "" {
		t.Fatalf("expected a recorded health error")
	}

	clock.Advance(31 * time.Second)
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got := repo.only(t); got.Status != execution.ExecutorOffline {
		t.Fatalf("status after offline window = %q, want offline", got.Status)
	}

	// A recovered heartbeat reactivates an offline executor (but not a drained
	// one, as proven above).
	probe.healthErr = nil
	probe.health = executionapp.ExecutorHealth{Status: "ok", Version: "0.2.0-p3", Contexts: 1, MaxContexts: 8}
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got := repo.only(t); got.Status != execution.ExecutorActive {
		t.Fatalf("status after recovery = %q, want active", got.Status)
	}
}

func TestRegistryPollDoesNotRegisterUnknownExecutor(t *testing.T) {
	registry, repo, probe, _ := newRegistryFixture(t)
	probe.healthErr = errors.New("connection refused")
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.rows) != 0 {
		t.Fatalf("an unreachable executor must not be registered: %+v", repo.rows)
	}
}

func TestRegistrySelectActiveMatchingEndpoint(t *testing.T) {
	registry, repo, _, _ := newRegistryFixture(t)
	clocked := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	repo.rows = map[int64]execution.ExecutorInstance{
		1: {ID: 1, Endpoint: registryEndpoint, Status: execution.ExecutorActive, Capacity: 8},
		2: {ID: 2, Endpoint: "http://other:13336", Status: execution.ExecutorActive, Capacity: 8},
		3: {ID: 3, Endpoint: registryEndpoint, Status: execution.ExecutorDraining, Capacity: 8},
		4: {ID: 4, Endpoint: registryEndpoint, Status: execution.ExecutorOffline, Capacity: 8, LastHeartbeat: &clocked},
	}
	selected, err := registry.Select(context.Background())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selected == nil || selected.ID != 1 {
		t.Fatalf("selected = %+v", selected)
	}

	registry2, repo2, _, _ := newRegistryFixture(t)
	repo2.rows = map[int64]execution.ExecutorInstance{
		1: {ID: 1, Endpoint: registryEndpoint, Status: execution.ExecutorDraining},
		2: {ID: 2, Endpoint: "http://other:13336", Status: execution.ExecutorActive},
	}
	selected, err = registry2.Select(context.Background())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selected != nil {
		t.Fatalf("selected = %+v, want nil", selected)
	}
}

func TestRegistryActivateReleasesDrain(t *testing.T) {
	registry, repo, probe, _ := newRegistryFixture(t)
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	row := repo.only(t)
	if ok, err := registry.Drain(context.Background(), row.ID); err != nil || !ok {
		t.Fatalf("Drain = %v, %v", ok, err)
	}
	if ok, err := registry.Activate(context.Background(), row.ID); err != nil || !ok {
		t.Fatalf("Activate = %v, %v", ok, err)
	}
	if ok, err := registry.Activate(context.Background(), 9999); err != nil || ok {
		t.Fatalf("Activate(unknown) = %v, %v", ok, err)
	}
	if got := repo.only(t); got.Status != execution.ExecutorActive {
		t.Fatalf("status = %q", got.Status)
	}
	probe.health = executionapp.ExecutorHealth{Status: "ok"}
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
}

/* ---------------------- P7 multi-endpoint registry ------------------------ */

// fakeProbeFactory builds one probe per additional endpoint and records which
// endpoints were actually polled.
type fakeProbeFactory struct {
	mu     sync.Mutex
	probes map[string]*fakeExecutorProbe
	calls  []string
}

func (f *fakeProbeFactory) Probe(endpoint string) executionapp.ExecutorProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, endpoint)
	if probe, ok := f.probes[endpoint]; ok {
		return probe
	}
	return &fakeExecutorProbe{healthErr: errors.New("unexpected probe for " + endpoint)}
}

func (f *fakeProbeFactory) polled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// newMultiRegistryFixture builds a registry whose primary endpoint must have a
// probe in probes and whose additional endpoints are built by the factory.
func newMultiRegistryFixture(t *testing.T, primary, extras string, probes map[string]*fakeExecutorProbe) (*executionapp.Registry, *fakeExecutorRepo, *fakeProbeFactory, *registryClock) {
	t.Helper()
	repo := newFakeExecutorRepo()
	clock := &registryClock{now: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)}
	factory := &fakeProbeFactory{probes: map[string]*fakeExecutorProbe{}}
	for endpoint, probe := range probes {
		if normalizeTestEndpoint(endpoint) == normalizeTestEndpoint(primary) {
			continue
		}
		factory.probes[normalizeTestEndpoint(endpoint)] = probe
	}
	primaryProbe := probes[primary]
	if primaryProbe == nil {
		primaryProbe = &fakeExecutorProbe{healthErr: errors.New("unexpected primary probe")}
	}
	registry := executionapp.NewRegistry(executionapp.RegistryDeps{
		Repo:  repo,
		Probe: primaryProbe,
		ProbeFactory: func(endpoint string) executionapp.ExecutorProbe {
			return factory.Probe(endpoint)
		},
		Settings: &fakeRegistrySettings{
			strings: map[string]string{
				"execution.executor_url":   primary,
				"execution.executor_urls":  extras,
				"execution.executor_token": registryToken,
			},
			ints: map[string]int64{
				"execution.executor_heartbeat_seconds": 10,
				"execution.executor_offline_seconds":   30,
			},
		},
		Clock: clock,
		IDs:   &registryIDs{next: 100},
	})
	return registry, repo, factory, clock
}

func normalizeTestEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}

func rowsByEndpoint(t *testing.T, repo *fakeExecutorRepo) map[string]execution.ExecutorInstance {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	rows := make(map[string]execution.ExecutorInstance, len(repo.rows))
	for _, row := range repo.rows {
		rows[row.Endpoint] = row
	}
	return rows
}

func TestRegistryPollUpsertsEveryConfiguredEndpoint(t *testing.T) {
	primary := "http://127.0.0.1:13336"
	extras := " http://127.0.0.1:13337/ , http://127.0.0.1:13338 , http://127.0.0.1:13337 , , not-a-url, ftp://bad-host "
	probes := map[string]*fakeExecutorProbe{
		primary:                  {health: executionapp.ExecutorHealth{Status: "ok", Version: "p7", Contexts: 2, MaxContexts: 8}},
		"http://127.0.0.1:13337": {health: executionapp.ExecutorHealth{Status: "ok", Version: "p7", Contexts: 0, MaxContexts: 4}},
		"http://127.0.0.1:13338": {health: executionapp.ExecutorHealth{Status: "ok", Version: "p7", Contexts: 3, MaxContexts: 16}},
	}
	registry, repo, factory, clock := newMultiRegistryFixture(t, primary, extras, probes)

	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	rows := rowsByEndpoint(t, repo)
	if len(rows) != 3 {
		t.Fatalf("executor rows = %d (%v), want 3", len(rows), rows)
	}
	for endpoint, wantCapacity := range map[string]int{
		primary: 8, "http://127.0.0.1:13337": 4, "http://127.0.0.1:13338": 16,
	} {
		row, ok := rows[endpoint]
		if !ok {
			t.Fatalf("endpoint %q was not registered", endpoint)
		}
		if row.Capacity != wantCapacity || row.Status != execution.ExecutorActive {
			t.Fatalf("row %q = %+v", endpoint, row)
		}
		if row.LastHeartbeat == nil || !row.LastHeartbeat.Equal(clock.Now()) {
			t.Fatalf("row %q last heartbeat = %v", endpoint, row.LastHeartbeat)
		}
	}
	// The primary endpoint reuses the legacy probe; the factory only sees the
	// deduplicated, valid additional endpoints.
	polled := factory.polled()
	if len(polled) != 2 || polled[0] != "http://127.0.0.1:13337" || polled[1] != "http://127.0.0.1:13338" {
		t.Fatalf("factory polled = %v", polled)
	}
}

func TestRegistryPollSingleEndpointKeepsLegacyShape(t *testing.T) {
	registry, repo, probe, _ := newRegistryFixture(t)
	probe.health = executionapp.ExecutorHealth{Status: "ok", Version: "p3", Contexts: 1, MaxContexts: 8}
	if err := registry.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	row := repo.only(t)
	if row.Endpoint != strings.TrimRight(registryEndpoint, "/") {
		t.Fatalf("endpoint = %q", row.Endpoint)
	}
}

func TestRegistrySelectMultiEndpointCapacityOrder(t *testing.T) {
	primary := "http://127.0.0.1:13336"
	extras := "http://127.0.0.1:13337,http://127.0.0.1:13338"
	registry, repo, _, _ := newMultiRegistryFixture(t, primary, extras, map[string]*fakeExecutorProbe{})
	t1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(30 * time.Second)
	rows := map[int64]execution.ExecutorInstance{
		1: {ID: 1, Endpoint: primary, Status: execution.ExecutorActive, Capacity: 8, LastHeartbeat: &t1},
		2: {ID: 2, Endpoint: primary, Status: execution.ExecutorActive, Capacity: 16, LastHeartbeat: &t1},
		3: {ID: 3, Endpoint: primary, Status: execution.ExecutorActive, Capacity: 16, LastHeartbeat: &t2},
		4: {ID: 4, Endpoint: "http://127.0.0.1:13337", Status: execution.ExecutorDraining, Capacity: 32, LastHeartbeat: &t2},
		5: {ID: 5, Endpoint: "http://127.0.0.1:13337", Status: execution.ExecutorActive, Capacity: 32, ActiveContexts: 32, LastHeartbeat: &t2},
		6: {ID: 6, Endpoint: "http://127.0.0.1:13338", Status: execution.ExecutorActive, Capacity: 16, LastHeartbeat: &t2},
		7: {ID: 7, Endpoint: primary, Status: execution.ExecutorActive, Capacity: 8, ActiveContexts: 8, LastHeartbeat: &t2},
	}
	repo.rows = rows
	selected, err := registry.Select(context.Background())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selected == nil || selected.ID != 3 {
		t.Fatalf("selected = %+v, want id 3 (capacity 16, newest heartbeat, lowest id)", selected)
	}

	// Same capacity and heartbeat: the stable lower id wins.
	row2 := rows[2]
	row2.LastHeartbeat = &t2
	rows[2] = row2
	delete(rows, 3)
	selected, err = registry.Select(context.Background())
	if err != nil || selected == nil || selected.ID != 2 {
		t.Fatalf("selected = %+v err=%v, want id 2", selected, err)
	}

	// Only a smaller free executor remains.
	row2 = rows[2]
	row2.ActiveContexts = 16
	rows[2] = row2
	delete(rows, 6)
	selected, err = registry.Select(context.Background())
	if err != nil || selected == nil || selected.ID != 1 {
		t.Fatalf("selected = %+v err=%v, want id 1", selected, err)
	}

	// No free active executor -> nil (draining/offline/zero-free are excluded).
	row1 := rows[1]
	row1.ActiveContexts = 8
	rows[1] = row1
	row5 := rows[5]
	row5.Status = execution.ExecutorOffline
	rows[5] = row5
	selected, err = registry.Select(context.Background())
	if err != nil || selected != nil {
		t.Fatalf("selected = %+v err=%v, want nil", selected, err)
	}
}

/* --------------------------- P7 push registration ------------------------- */

func TestRegistryRegisterPushUpsertsActiveInstance(t *testing.T) {
	registry, repo, _, clock := newRegistryFixture(t)
	pushEndpoint := "http://127.0.0.1:14000/"
	instance, err := registry.RegisterPush(context.Background(), registryToken, executionapp.ExecutorRegistration{
		Endpoint:       pushEndpoint,
		Name:           "executor-a",
		Capacity:       6,
		ActiveContexts: 2,
		Version:        "0.2.0-p7",
	})
	if err != nil {
		t.Fatalf("RegisterPush: %v", err)
	}
	if instance.Endpoint != "http://127.0.0.1:14000" {
		t.Fatalf("endpoint = %q, want normalized", instance.Endpoint)
	}
	if instance.ID == 0 || instance.Status != execution.ExecutorActive {
		t.Fatalf("instance = %+v", instance)
	}
	if instance.Capacity != 6 || instance.ActiveContexts != 2 || instance.Version != "0.2.0-p7" {
		t.Fatalf("instance = %+v", instance)
	}
	if instance.Name != "executor-a" {
		t.Fatalf("name = %q", instance.Name)
	}
	if instance.TokenHash != registryTokenHash() {
		t.Fatalf("token hash = %q", instance.TokenHash)
	}
	if instance.LastHeartbeat == nil || !instance.LastHeartbeat.Equal(clock.Now()) {
		t.Fatalf("last heartbeat = %v", instance.LastHeartbeat)
	}
	// A repeated register is idempotent and refreshes the mutable fields.
	instance, err = registry.RegisterPush(context.Background(), registryToken, executionapp.ExecutorRegistration{
		Endpoint: "http://127.0.0.1:14000", Name: "executor-a2", Capacity: 10, Version: "0.2.0-p7.1",
	})
	if err != nil {
		t.Fatalf("second RegisterPush: %v", err)
	}
	rows := rowsByEndpoint(t, repo)
	if len(rows) != 1 {
		t.Fatalf("executor rows = %d, want 1", len(rows))
	}
	row := rows[instance.Endpoint]
	if row.Capacity != 10 || row.Name != "executor-a2" || row.Version != "0.2.0-p7.1" {
		t.Fatalf("row = %+v", row)
	}
}

func TestRegistryRegisterPushPreservesDraining(t *testing.T) {
	registry, _, _, _ := newRegistryFixture(t)
	endpoint := "http://127.0.0.1:14001"
	instance, err := registry.RegisterPush(context.Background(), registryToken, executionapp.ExecutorRegistration{
		Endpoint: endpoint, Name: "executor-b", Capacity: 4, Version: "0.2.0-p7",
	})
	if err != nil {
		t.Fatalf("RegisterPush: %v", err)
	}
	if _, err := registry.Drain(context.Background(), instance.ID); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	instance, err = registry.RegisterPush(context.Background(), registryToken, executionapp.ExecutorRegistration{
		Endpoint: endpoint, Name: "executor-b", Capacity: 4, Version: "0.2.0-p7",
	})
	if err != nil {
		t.Fatalf("RegisterPush after drain: %v", err)
	}
	if instance.Status != execution.ExecutorDraining {
		t.Fatalf("status = %q, want draining", instance.Status)
	}
}

func TestRegistryHeartbeatPushUpdatesAndRequiresRegistration(t *testing.T) {
	registry, _, _, clock := newRegistryFixture(t)
	ctx := context.Background()
	if _, err := registry.HeartbeatPush(ctx, executionapp.ExecutorHeartbeat{
		Endpoint: "http://127.0.0.1:14999", ActiveContexts: 1,
	}); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("unknown heartbeat error = %v, want ErrNotFound", err)
	}

	endpoint := "http://127.0.0.1:14002/"
	registered, err := registry.RegisterPush(ctx, registryToken, executionapp.ExecutorRegistration{
		Endpoint: endpoint, Name: "executor-c", Capacity: 8, Version: "0.2.0-p7",
	})
	if err != nil {
		t.Fatalf("RegisterPush: %v", err)
	}
	clock.Advance(10 * time.Second)
	heartbeat, err := registry.HeartbeatPush(ctx, executionapp.ExecutorHeartbeat{
		Endpoint: endpoint, ActiveContexts: 5, Capacity: 12, Version: "0.2.0-p7.1", Status: "degraded",
	})
	if err != nil {
		t.Fatalf("HeartbeatPush: %v", err)
	}
	if heartbeat.ID != registered.ID {
		t.Fatalf("heartbeat id = %d, want %d", heartbeat.ID, registered.ID)
	}
	if heartbeat.Status != execution.ExecutorActive {
		t.Fatalf("status = %q, want active", heartbeat.Status)
	}
	if heartbeat.ActiveContexts != 5 || heartbeat.Capacity != 12 || heartbeat.Version != "0.2.0-p7.1" {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}
	if heartbeat.LastError != "degraded" {
		t.Fatalf("last error = %q, want degraded", heartbeat.LastError)
	}
	if heartbeat.LastHeartbeat == nil || !heartbeat.LastHeartbeat.Equal(clock.Now()) {
		t.Fatalf("last heartbeat = %v", heartbeat.LastHeartbeat)
	}

	// An explicit active heartbeat clears the degraded marker.
	heartbeat, err = registry.HeartbeatPush(ctx, executionapp.ExecutorHeartbeat{
		Endpoint: endpoint, ActiveContexts: 1, Status: "active",
	})
	if err != nil {
		t.Fatalf("HeartbeatPush active: %v", err)
	}
	if heartbeat.LastError != "" || heartbeat.Capacity != 12 {
		t.Fatalf("heartbeat = %+v (capacity must be preserved when omitted)", heartbeat)
	}

	// Draining is preserved by heartbeats and offline is recovered.
	if _, err := registry.Drain(ctx, registered.ID); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	heartbeat, err = registry.HeartbeatPush(ctx, executionapp.ExecutorHeartbeat{
		Endpoint: endpoint, ActiveContexts: 0, Status: "active",
	})
	if err != nil {
		t.Fatalf("HeartbeatPush while draining: %v", err)
	}
	if heartbeat.Status != execution.ExecutorDraining {
		t.Fatalf("status = %q, want draining", heartbeat.Status)
	}
}

func TestRegistryPushValidation(t *testing.T) {
	registry, _, _, _ := newRegistryFixture(t)
	ctx := context.Background()
	cases := []struct {
		name string
		in   executionapp.ExecutorRegistration
	}{
		{"endpoint required", executionapp.ExecutorRegistration{Endpoint: "", Name: "a", Capacity: 1, Version: "p7"}},
		{"endpoint scheme", executionapp.ExecutorRegistration{Endpoint: "ftp://host:1", Name: "a", Capacity: 1, Version: "p7"}},
		{"name required", executionapp.ExecutorRegistration{Endpoint: "http://host:1", Name: "", Capacity: 1, Version: "p7"}},
		{"capacity", executionapp.ExecutorRegistration{Endpoint: "http://host:1", Name: "a", Capacity: 0, Version: "p7"}},
		{"version required", executionapp.ExecutorRegistration{Endpoint: "http://host:1", Name: "a", Capacity: 1, Version: ""}},
	}
	for _, tc := range cases {
		if _, err := registry.RegisterPush(ctx, registryToken, tc.in); err == nil {
			t.Fatalf("%s: expected a validation error", tc.name)
		}
	}
	if _, err := registry.HeartbeatPush(ctx, executionapp.ExecutorHeartbeat{
		Endpoint: "http://host:1", ActiveContexts: -1,
	}); err == nil {
		t.Fatal("negative active_contexts must be rejected")
	}
	if _, err := registry.HeartbeatPush(ctx, executionapp.ExecutorHeartbeat{
		Endpoint: "http://host:1", ActiveContexts: 0, Status: "offline",
	}); err == nil {
		t.Fatal("unsupported heartbeat status must be rejected")
	}
}
