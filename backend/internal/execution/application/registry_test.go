package executionapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
