//go:build integration

package orchestrationpg_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	domain "github.com/EdouardRichard/EzTest/backend/internal/orchestration/domain"
	orchestrationpg "github.com/EdouardRichard/EzTest/backend/internal/orchestration/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

type fakeSettings struct {
	heartbeat int64
	offline   int64
	node      int64
}

func (f *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	switch key {
	case orchestrationapp.SettingHeartbeatSeconds:
		return f.heartbeat, nil
	case orchestrationapp.SettingOfflineSeconds:
		return f.offline, nil
	case orchestrationapp.SettingNodeID:
		return f.node, nil
	default:
		return 0, errors.New("unknown setting " + key)
	}
}

type recordingPublisher struct {
	mu     sync.Mutex
	events []eventingapp.Event
}

func (p *recordingPublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *recordingPublisher) ofType(eventType string) []eventingapp.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	var matched []eventingapp.Event
	for _, event := range p.events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

type recordingAudit struct{}

func (recordingAudit) Record(context.Context, auditapp.Event) error { return nil }

func startMeta(host string, pid, port int) orchestrationapp.StartMeta {
	return orchestrationapp.StartMeta{Hostname: host, PID: pid, APIPort: port, Version: "0.2.0"}
}

func newOrchestrationService(
	repo orchestrationapp.Repository,
	settings orchestrationapp.SettingsReader,
	events eventingapp.Publisher,
	ids platformid.Generator,
) *orchestrationapp.Service {
	return orchestrationapp.New(repo, ids, clock.System{}, settings, nil, nil, events)
}

func TestRegistrationsGetDistinctNodeIDsAndStoppedNodeIsReused(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := orchestrationpg.New(pool)
	ids, err := platformid.NewSnowflake(11)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	settings := &fakeSettings{heartbeat: 10, offline: 30, node: 5}
	events := &recordingPublisher{}
	ctx := context.Background()

	start := func(host string, pid, port int) (domain.Instance, *orchestrationapp.Service) {
		t.Helper()
		service := newOrchestrationService(repo, settings, events, ids)
		instance, err := service.Start(ctx, startMeta(host, pid, port))
		if err != nil {
			t.Fatalf("Start %s: %v", host, err)
		}
		return instance, service
	}

	first, firstService := start("host-a", 101, 18080)
	if first.NodeID != 5 {
		t.Fatalf("first node_id = %d, want preferred 5", first.NodeID)
	}
	second, secondService := start("host-b", 102, 18081)
	if second.NodeID != 0 {
		t.Fatalf("second node_id = %d, want smallest free 0", second.NodeID)
	}
	if first.NodeID == second.NodeID {
		t.Fatal("two live registrations share a node id")
	}

	instances, err := firstService.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(instances) != 2 {
		t.Fatalf("instances = %d, want 2", len(instances))
	}
	for _, instance := range instances {
		if instance.Status != domain.StatusActive {
			t.Fatalf("instance %d status = %q", instance.ID, instance.Status)
		}
	}

	// Graceful stop makes the node reclaimable; the next replica prefers it.
	if err := firstService.Stop(ctx); err != nil {
		t.Fatalf("Stop first: %v", err)
	}
	stopped, err := repo.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get stopped: %v", err)
	}
	if stopped.Status != domain.StatusStopped || stopped.StoppedAt == nil {
		t.Fatalf("stopped row = %+v", stopped)
	}

	third, thirdService := start("host-c", 103, 18082)
	if third.NodeID != 5 {
		t.Fatalf("third node_id = %d, want reclaimed preferred 5", third.NodeID)
	}
	if _, err := repo.Get(ctx, first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stopped row after reclaim error = %v, want ErrNotFound", err)
	}

	registered := events.ofType(orchestrationapp.EventRegistered)
	if len(registered) != 3 {
		t.Fatalf("registered events = %d, want 3", len(registered))
	}
	if registered[0].Payload["node_id"] != 5 || registered[0].Payload["instance_key"] != "host-a:101" {
		t.Fatalf("registered payload = %#v", registered[0].Payload)
	}

	_ = secondService.Stop(ctx)
	_ = thirdService.Stop(ctx)
}

func TestConcurrentRegistrationsGetDistinctNodeIDs(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := orchestrationpg.New(pool)
	ids, err := platformid.NewSnowflake(12)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	settings := &fakeSettings{heartbeat: 10, offline: 30, node: 0}
	events := &recordingPublisher{}
	ctx := context.Background()

	services := []*orchestrationapp.Service{
		newOrchestrationService(repo, settings, events, ids),
		newOrchestrationService(repo, settings, events, ids),
	}
	var wg sync.WaitGroup
	instances := make([]domain.Instance, len(services))
	errs := make([]error, len(services))
	start := make(chan struct{})
	for index := range services {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			instances[index], errs[index] = services[index].Start(ctx, startMeta(
				"concurrent-"+string(rune('a'+index)), 200+index, 19080+index))
		}(index)
	}
	close(start)
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("Start %d: %v", index, err)
		}
	}
	if instances[0].NodeID == instances[1].NodeID {
		t.Fatalf("concurrent registrations share node %d", instances[0].NodeID)
	}
	nodes := map[int]bool{instances[0].NodeID: true, instances[1].NodeID: true}
	if !nodes[0] || !nodes[1] {
		t.Fatalf("node ids = %v, want 0 and 1", []int{instances[0].NodeID, instances[1].NodeID})
	}
	for _, service := range services {
		_ = service.Stop(ctx)
	}
}

func TestHeartbeatRefreshesAndStaleScanMarksOfflineOnce(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := orchestrationpg.New(pool)
	ids, err := platformid.NewSnowflake(13)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	settings := &fakeSettings{heartbeat: 1, offline: 5, node: 3}
	events := &recordingPublisher{}
	ctx := context.Background()

	service := newOrchestrationService(repo, settings, events, ids)
	instance, err := service.Start(ctx, startMeta("host-heartbeat", 301, 20080))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for {
		loaded, err := repo.Get(ctx, instance.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if loaded.HeartbeatAt.After(instance.HeartbeatAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat_at did not advance: start=%v now=%v", instance.HeartbeatAt, loaded.HeartbeatAt)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Stop the heartbeat loop, then simulate a replica that registered but went
	// silent: the scan must flip exactly that active row to offline.
	if err := service.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE api_instance
		SET status = 'active', stopped_at = NULL, heartbeat_at = now() - interval '10 seconds'
		WHERE id = $1
	`, instance.ID); err != nil {
		t.Fatalf("age heartbeat: %v", err)
	}

	stale, err := service.MarkOfflineStale(ctx)
	if err != nil {
		t.Fatalf("MarkOfflineStale: %v", err)
	}
	if len(stale) != 1 || stale[0].ID != instance.ID || stale[0].Status != domain.StatusOffline {
		t.Fatalf("stale = %#v", stale)
	}
	offlineEvents := events.ofType(orchestrationapp.EventOffline)
	if len(offlineEvents) != 1 {
		t.Fatalf("offline events = %d, want 1", len(offlineEvents))
	}
	if offlineEvents[0].Payload["status"] != string(domain.StatusOffline) {
		t.Fatalf("offline payload = %#v", offlineEvents[0].Payload)
	}

	again, err := service.MarkOfflineStale(ctx)
	if err != nil {
		t.Fatalf("second MarkOfflineStale: %v", err)
	}
	if len(again) != 0 || len(events.ofType(orchestrationapp.EventOffline)) != 1 {
		t.Fatalf("second scan re-published: stale=%d events=%d", len(again), len(events.ofType(orchestrationapp.EventOffline)))
	}
	loaded, err := repo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get offline: %v", err)
	}
	if loaded.Status != domain.StatusOffline {
		t.Fatalf("status = %q, want offline", loaded.Status)
	}
}

// TestDrainingStaleInstanceGoesOfflineAndReleasesNode pins the P7 node-leak fix:
// a replica that was draining when it crashed must be marked offline by the
// stale scan (not skipped as active-only), and its node ID must be reclaimable
// by the next registration.
func TestDrainingStaleInstanceGoesOfflineAndReleasesNode(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := orchestrationpg.New(pool)
	ids, err := platformid.NewSnowflake(15)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	settings := &fakeSettings{heartbeat: 10, offline: 5, node: 4}
	events := &recordingPublisher{}
	ctx := context.Background()

	deadService := newOrchestrationService(repo, settings, events, ids)
	drained, err := deadService.Start(ctx, startMeta("host-crashed-drain", 501, 22080))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if drained.NodeID != 4 {
		t.Fatalf("node_id = %d, want preferred 4", drained.NodeID)
	}
	// Stop the heartbeat loop, then simulate a crash while draining: the row
	// stays 'draining' with a stale heartbeat and no graceful Stop.
	if err := deadService.Stop(ctx); err != nil {
		t.Fatalf("Stop heartbeat: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE api_instance
		SET status = 'draining', stopped_at = NULL, draining_at = now(),
		    heartbeat_at = now() - interval '10 seconds'
		WHERE id = $1
	`, drained.ID); err != nil {
		t.Fatalf("simulate crashed draining row: %v", err)
	}

	stale, err := deadService.MarkOfflineStale(ctx)
	if err != nil {
		t.Fatalf("MarkOfflineStale: %v", err)
	}
	if len(stale) != 1 || stale[0].ID != drained.ID || stale[0].Status != domain.StatusOffline {
		t.Fatalf("stale = %#v, want the draining instance marked offline", stale)
	}
	if got := events.ofType(orchestrationapp.EventOffline); len(got) != 1 {
		t.Fatalf("offline events = %d, want 1", len(got))
	}

	// The offline row's node is now reclaimable by the next replica, and the
	// stale row is deleted (the same reclaim contract as a graceful Stop).
	nextService := newOrchestrationService(repo, settings, events, ids)
	replacement, err := nextService.Start(ctx, startMeta("host-replacement", 502, 22081))
	if err != nil {
		t.Fatalf("replacement Start: %v", err)
	}
	if replacement.NodeID != 4 {
		t.Fatalf("replacement node_id = %d, want reclaimed 4", replacement.NodeID)
	}
	if _, err := repo.Get(ctx, drained.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("offline row after reclaim error = %v, want ErrNotFound", err)
	}
	if err := nextService.Stop(ctx); err != nil {
		t.Fatalf("Stop replacement: %v", err)
	}
}

func TestDrainActivateAndReadinessPersistThroughRepository(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := orchestrationpg.New(pool)
	ids, err := platformid.NewSnowflake(14)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	settings := &fakeSettings{heartbeat: 10, offline: 30, node: 2}
	events := &recordingPublisher{}
	ctx := context.Background()

	service := newOrchestrationService(repo, settings, events, ids)
	instance, err := service.Start(ctx, startMeta("host-drain", 401, 21080))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })

	// The service was built without a storage probe, so readiness reports the
	// database condition but fails closed on storage.
	readiness := service.Readiness(ctx)
	if !readiness.Database || readiness.Storage || readiness.Draining || readiness.InstanceID != instance.ID {
		t.Fatalf("initial readiness = %+v", readiness)
	}

	actor := platform.Actor{ID: 9, Role: "admin"}
	drained, err := service.Drain(ctx, instance.ID, actor)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if drained.Status != domain.StatusDraining || drained.DrainingAt == nil {
		t.Fatalf("drained = %+v", drained)
	}
	loaded, err := repo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get draining: %v", err)
	}
	if loaded.Status != domain.StatusDraining {
		t.Fatalf("persisted status = %q, want draining", loaded.Status)
	}

	// The heartbeat keeps working while draining and reports the persisted
	// status so the process can mirror a remote drain.
	status, updated, err := repo.Heartbeat(ctx, instance.ID, time.Now(), 5, 4)
	if err != nil || !updated || status != domain.StatusDraining {
		t.Fatalf("draining heartbeat = %q/%v/%v", status, updated, err)
	}
	loaded, err = repo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get after heartbeat: %v", err)
	}
	if loaded.ActiveRequests != 5 || loaded.ActiveWork != 4 {
		t.Fatalf("persisted inflight = %d/%d, want 5/4", loaded.ActiveRequests, loaded.ActiveWork)
	}
	if readiness := service.Readiness(ctx); !readiness.Draining {
		t.Fatalf("readiness after drain = %+v", readiness)
	}

	activated, err := service.Activate(ctx, instance.ID, actor)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if activated.Status != domain.StatusActive {
		t.Fatalf("activated = %+v", activated)
	}
	loaded, err = repo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get active: %v", err)
	}
	if loaded.Status != domain.StatusActive {
		t.Fatalf("persisted status = %q, want active", loaded.Status)
	}
	if readiness := service.Readiness(ctx); readiness.Draining {
		t.Fatalf("readiness after activate = %+v", readiness)
	}

	if len(events.ofType(orchestrationapp.EventDrained)) != 1 ||
		len(events.ofType(orchestrationapp.EventActivated)) != 1 {
		t.Fatalf("transition events = drained:%d activated:%d",
			len(events.ofType(orchestrationapp.EventDrained)),
			len(events.ofType(orchestrationapp.EventActivated)))
	}
}
