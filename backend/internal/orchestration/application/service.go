package orchestrationapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/orchestration/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// Service is the per-process orchestration surface: it registers this replica,
// keeps its heartbeat alive, exposes drain/activate/roster operations and
// answers the readiness probe. It also owns the periodic stale-instance scan.
type Service struct {
	repo     Repository
	ids      platformid.Generator
	clock    clock.Clock
	settings SettingsReader
	storage  StorageProbe
	load     LoadTracker
	audit    auditapp.Recorder
	events   eventingapp.Publisher
	logger   *slog.Logger

	mu       sync.RWMutex
	instance orchestration.Instance
	started  bool
	draining bool
	hbCancel context.CancelFunc
	hbDone   chan struct{}
}

// New wires the orchestration application service. storage, audit, events and
// settings may be nil in tests: a nil probe makes readiness fail closed, a nil
// sink keeps the operation successful, and missing settings fall back to the
// catalog defaults.
func New(
	repo Repository,
	ids platformid.Generator,
	clk clock.Clock,
	settings SettingsReader,
	storage StorageProbe,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
) *Service {
	if clk == nil {
		clk = clock.System{}
	}
	return &Service{
		repo:     repo,
		ids:      ids,
		clock:    clk,
		settings: settings,
		storage:  storage,
		audit:    audit,
		events:   events,
		logger:   slog.Default(),
	}
}

// SetLogger overrides the structured logger used by the heartbeat loop.
func (s *Service) SetLogger(logger *slog.Logger) {
	if logger != nil {
		s.logger = logger
	}
}

// SetLoadTracker installs the process-local in-flight counters reported by the
// heartbeat. Wiring calls it once before Start; a nil tracker reports zero.
func (s *Service) SetLoadTracker(tracker LoadTracker) {
	s.mu.Lock()
	s.load = tracker
	s.mu.Unlock()
}

// instanceIDRetries bounds the fresh-ID retries of one registration. Two
// replicas that share the bootstrap snowflake node can mint the same
// api_instance.id when they start in the same millisecond; a short wait crosses
// the millisecond boundary so the next Next() is distinct.
const instanceIDRetries = 2

// instanceIDRetryDelay is a variable so tests can pin the retry path quickly.
var instanceIDRetryDelay = 2 * time.Millisecond

// insertInstance registers one node candidate, reallocating the instance ID and
// retrying when the primary key collides (bounded). ErrNodeTaken and
// ErrInstanceKeyTaken are returned untouched so Start can advance candidates.
func (s *Service) insertInstance(ctx context.Context, candidate orchestration.Instance) (orchestration.Instance, error) {
	for attempt := 0; ; attempt++ {
		err := s.repo.Insert(ctx, candidate)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, orchestration.ErrInstanceIDTaken) || attempt >= instanceIDRetries {
			return orchestration.Instance{}, err
		}
		time.Sleep(instanceIDRetryDelay)
		candidate.ID = s.ids.Next()
	}
}

// Start registers this API replica, allocates a snowflake node ID and starts
// the heartbeat loop. Preferred is runtime.snowflake_node_id; when it is
// already owned by a live replica the smallest free node 0..1023 is used and
// unique-conflict retries move to the next candidate. A second Start on the
// same service is rejected.
func (s *Service) Start(ctx context.Context, meta StartMeta) (orchestration.Instance, error) {
	if err := validateStartMeta(meta); err != nil {
		return orchestration.Instance{}, err
	}
	if s.repo == nil {
		return orchestration.Instance{}, platform.Unavailable("orchestration repository is not configured")
	}
	if s.ids == nil {
		return orchestration.Instance{}, platform.Unavailable("id generator is not configured")
	}

	s.mu.RLock()
	alreadyStarted := s.started
	s.mu.RUnlock()
	if alreadyStarted {
		return orchestration.Instance{}, platform.Conflict("orchestration instance is already started")
	}

	used, err := s.repo.UsedNodeIDs(ctx)
	if err != nil {
		return orchestration.Instance{}, err
	}
	preferred := s.preferredNodeID(ctx)
	candidates := candidateNodeIDs(preferred, used)
	if len(candidates) == 0 {
		return orchestration.Instance{}, platform.Conflict("no free snowflake node id is available")
	}

	now := s.clock.Now().UTC()
	base := orchestration.Instance{
		ID:          s.ids.Next(),
		InstanceKey: fmt.Sprintf("%s:%d", strings.TrimSpace(meta.Hostname), meta.PID),
		Hostname:    strings.TrimSpace(meta.Hostname),
		PID:         meta.PID,
		APIPort:     meta.APIPort,
		Status:      orchestration.StatusActive,
		Version:     meta.Version,
		StartedAt:   now,
		HeartbeatAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	var registered *orchestration.Instance
	for _, nodeID := range candidates {
		candidate := base
		candidate.NodeID = nodeID
		inserted, err := s.insertInstance(ctx, candidate)
		switch {
		case err == nil:
			registered = &inserted
		case errors.Is(err, orchestration.ErrNodeTaken):
			continue
		case errors.Is(err, orchestration.ErrInstanceKeyTaken):
			return orchestration.Instance{}, platform.Conflict("instance key is already registered")
		case errors.Is(err, orchestration.ErrInstanceIDTaken):
			return orchestration.Instance{}, platform.Conflict("api_instance id collision retry budget exhausted")
		default:
			return orchestration.Instance{}, err
		}
		if registered != nil {
			break
		}
	}
	if registered == nil {
		return orchestration.Instance{}, platform.Conflict("no free snowflake node id is available")
	}

	heartbeat := time.Duration(s.intSetting(ctx, SettingHeartbeatSeconds, DefaultHeartbeatSeconds, 1, 60)) * time.Second
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	heartbeatDone := make(chan struct{})

	s.mu.Lock()
	s.instance = *registered
	s.started = true
	s.draining = false
	s.hbCancel = cancelHeartbeat
	s.hbDone = heartbeatDone
	s.mu.Unlock()

	go s.runHeartbeat(heartbeatCtx, heartbeatDone, heartbeat)

	s.publishEvent(ctx, eventingapp.Event{
		Channel: "system",
		Type:    EventRegistered,
		Scope:   map[string]any{},
		Payload: map[string]any{
			"instance_id":  platformid.Format(registered.ID),
			"node_id":      registered.NodeID,
			"instance_key": registered.InstanceKey,
		},
	})
	return *registered, nil
}

// Stop terminates the heartbeat loop and marks this instance stopped. The row
// keeps its node_id but becomes reclaimable by the next registration.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	instance := s.instance
	cancel := s.hbCancel
	done := s.hbDone
	s.started = false
	s.draining = false
	s.hbCancel = nil
	s.hbDone = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	at := s.clock.Now().UTC()
	if _, err := s.repo.MarkStopped(ctx, instance.ID, at); err != nil {
		return err
	}
	s.mu.Lock()
	s.instance.Status = orchestration.StatusStopped
	s.instance.StoppedAt = &at
	s.instance.UpdatedAt = at
	s.mu.Unlock()
	return nil
}

// Drain moves an instance from active to draining, emits
// instance.drained + orchestration.instance.drained and is idempotent while the
// instance is already draining. When the target is this replica, IsDraining()
// flips immediately.
func (s *Service) Drain(ctx context.Context, id int64, actor platform.Actor) (orchestration.Instance, error) {
	return s.transition(ctx, id, actor, orchestration.StatusDraining, EventDrained, ActionDrained)
}

// Activate moves a draining instance back to active, emits
// instance.activated + orchestration.instance.activated and is idempotent while
// the instance is already active.
func (s *Service) Activate(ctx context.Context, id int64, actor platform.Actor) (orchestration.Instance, error) {
	return s.transition(ctx, id, actor, orchestration.StatusActive, EventActivated, ActionActivated)
}

// List returns the registered instances.
func (s *Service) List(ctx context.Context) ([]orchestration.Instance, error) {
	if s.repo == nil {
		return nil, platform.Unavailable("orchestration repository is not configured")
	}
	return s.repo.List(ctx)
}

// MarkOfflineStale transitions every active instance whose last heartbeat is
// older than instance.offline_seconds to offline and emits one
// instance.offline event per transition. Repeated scans publish nothing
// because only the first update matches status='active'.
func (s *Service) MarkOfflineStale(ctx context.Context) ([]orchestration.Instance, error) {
	if s.repo == nil {
		return nil, platform.Unavailable("orchestration repository is not configured")
	}
	seconds := s.intSetting(ctx, SettingOfflineSeconds, DefaultOfflineSeconds, 5, 300)
	cutoff := s.clock.Now().UTC().Add(-time.Duration(seconds) * time.Second)
	stale, err := s.repo.MarkOfflineStale(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	for _, instance := range stale {
		s.publishEvent(ctx, eventingapp.Event{
			Channel: "system",
			Type:    EventOffline,
			Scope:   map[string]any{},
			Payload: map[string]any{
				"instance_id": platformid.Format(instance.ID),
				"status":      string(orchestration.StatusOffline),
			},
		})
	}
	return stale, nil
}

// RunMaintenance scans for stale instances immediately and then every
// heartbeat interval until ctx is cancelled. Every replica may run it: the
// UPDATE ... WHERE status='active' RETURNING makes each transition publish
// exactly once. It returns nil on cancellation.
func (s *Service) RunMaintenance(ctx context.Context) error {
	if s.repo == nil {
		return platform.Unavailable("orchestration repository is not configured")
	}
	interval := time.Duration(s.intSetting(ctx, SettingHeartbeatSeconds, DefaultHeartbeatSeconds, 1, 60)) * time.Second
	scan := func() {
		if _, err := s.MarkOfflineStale(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("orchestration stale-instance scan failed", "error", err)
		}
	}
	scan()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			scan()
		}
	}
}

// Readiness evaluates the three P7 readiness conditions. A database or status
// lookup failure fails the DB condition; a missing/failing storage probe fails
// the storage condition; a remotely requested drain is observed in the DB.
func (s *Service) Readiness(ctx context.Context) Readiness {
	readiness := Readiness{}
	s.mu.RLock()
	readiness.InstanceID = s.instance.ID
	localDraining := s.draining
	load := s.load
	s.mu.RUnlock()
	if load != nil {
		readiness.InflightRequests = load.InflightRequests()
		readiness.InflightWork = load.InflightWork()
	}

	if s.repo == nil {
		return readiness
	}
	if err := s.repo.Ping(ctx); err != nil {
		return readiness
	}
	readiness.Database = true

	if readiness.InstanceID > 0 {
		status, err := s.repo.Status(ctx, readiness.InstanceID)
		if err != nil {
			// The database answered the ping but cannot describe this instance
			// (e.g. its row was reclaimed): fail closed.
			readiness.Database = false
			return readiness
		}
		readiness.Draining = localDraining || status == orchestration.StatusDraining
	} else {
		readiness.Draining = localDraining
	}

	if s.storage != nil && s.storage.Probe(ctx) == nil {
		readiness.Storage = true
	}
	return readiness
}

// IsDraining reports the local view of this replica's drain state. It is
// updated synchronously by Drain/Activate on this replica and mirrored from
// the DB by the heartbeat loop (so a drain requested through another replica is
// observed within one heartbeat interval). Workers use it to stop claiming.
func (s *Service) IsDraining() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.draining
}

// InstanceID is this replica's api_instance.id (0 before Start).
func (s *Service) InstanceID() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.instance.ID
}

// NodeID is the allocated snowflake node ID (0 before Start).
func (s *Service) NodeID() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(s.instance.NodeID)
}

/* --------------------------------- internals ------------------------------ */

func (s *Service) transition(
	ctx context.Context,
	id int64,
	actor platform.Actor,
	to orchestration.Status,
	eventType, action string,
) (orchestration.Instance, error) {
	if id <= 0 {
		return orchestration.Instance{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if s.repo == nil {
		return orchestration.Instance{}, platform.Unavailable("orchestration repository is not configured")
	}
	current, err := s.repo.Get(ctx, id)
	if errors.Is(err, orchestration.ErrNotFound) {
		return orchestration.Instance{}, platform.NotFound("instance")
	}
	if err != nil {
		return orchestration.Instance{}, err
	}
	if current.Status == to {
		// Idempotent replay: no transition, therefore no event/audit row.
		s.mirrorOwn(current)
		return current, nil
	}
	if !orchestration.CanTransition(current.Status, to) {
		return orchestration.Instance{}, platform.StateConflict(
			fmt.Sprintf("instance cannot move from %s to %s", current.Status, to))
	}

	updated, changed, err := s.repo.SetStatus(ctx, id, current.Status, to, s.clock.Now().UTC())
	if err != nil {
		return orchestration.Instance{}, err
	}
	if !changed {
		return orchestration.Instance{}, platform.StateConflict("instance status changed concurrently")
	}
	s.mirrorOwn(updated)

	actorID := actorPointer(actor)
	requestID := requestid.From(ctx)
	s.publishEvent(ctx, eventingapp.Event{
		Channel:   "system",
		Type:      eventType,
		Scope:     map[string]any{},
		Payload:   map[string]any{"instance_id": platformid.Format(updated.ID), "status": string(to)},
		ActorID:   actorID,
		RequestID: requestID,
	})
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    actorID,
			Action:     action,
			TargetKind: "api_instance",
			TargetID:   platformid.Format(updated.ID),
			RequestID:  requestID,
			Metadata: map[string]any{
				"instance_id": platformid.Format(updated.ID),
				"from_status": string(current.Status),
				"to_status":   string(to),
			},
		})
	}
	return updated, nil
}

// mirrorOwn copies a transition affecting this replica into the local view so
// IsDraining() and NodeID()/InstanceID() stay accurate without a DB round trip.
func (s *Service) mirrorOwn(instance orchestration.Instance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instance.ID == 0 || s.instance.ID != instance.ID {
		return
	}
	s.instance = instance
	s.draining = instance.Status == orchestration.StatusDraining
}

func (s *Service) runHeartbeat(ctx context.Context, done chan<- struct{}, interval time.Duration) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.heartbeatOnce(ctx)
		}
	}
}

// heartbeatOnce persists one heartbeat and mirrors the persisted status. The
// mirror is how a drain requested through another replica reaches this
// process's workers within one heartbeat interval.
func (s *Service) heartbeatOnce(ctx context.Context) {
	s.mu.RLock()
	id := s.instance.ID
	started := s.started
	load := s.load
	s.mu.RUnlock()
	if !started || id == 0 {
		return
	}
	activeRequests, activeWork := 0, 0
	if load != nil {
		activeRequests = load.InflightRequests()
		activeWork = load.InflightWork()
	}
	status, updated, err := s.repo.Heartbeat(ctx, id, s.clock.Now().UTC(), activeRequests, activeWork)
	if err != nil {
		s.logger.Warn("orchestration heartbeat failed", "instance_id", id, "error", err)
		return
	}
	if !updated {
		s.logger.Warn("orchestration heartbeat ignored: instance is not active", "instance_id", id)
		return
	}
	if !status.Valid() {
		return
	}
	s.mu.Lock()
	s.draining = status == orchestration.StatusDraining
	s.mu.Unlock()
}

func (s *Service) publishEvent(ctx context.Context, event eventingapp.Event) {
	if s.events == nil {
		return
	}
	_ = s.events.Publish(ctx, event)
}

func (s *Service) intSetting(ctx context.Context, key string, fallback, min, max int64) int64 {
	if s.settings == nil {
		return fallback
	}
	value, err := s.settings.Int(ctx, key)
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

// preferredNodeID reads runtime.snowflake_node_id. Errors and out-of-range
// values mean "no preference" (-1); the allocator then starts at node 0.
func (s *Service) preferredNodeID(ctx context.Context) int {
	if s.settings == nil {
		return -1
	}
	value, err := s.settings.Int(ctx, SettingNodeID)
	if err != nil || value < MinNodeID || value > MaxNodeID {
		return -1
	}
	return int(value)
}

func validateStartMeta(meta StartMeta) error {
	if strings.TrimSpace(meta.Hostname) == "" {
		return platform.Invalid("hostname", "hostname is required")
	}
	if meta.PID <= 0 {
		return platform.Invalid("pid", "pid must be positive")
	}
	if meta.APIPort < 1 || meta.APIPort > 65535 {
		return platform.Invalid("api_port", "api_port must be between 1 and 65535")
	}
	return nil
}

// candidateNodeIDs returns the allocation order: the preferred node first when
// it is valid and free, then the smallest free node IDs 0..1023 ascending.
func candidateNodeIDs(preferred int, used []int) []int {
	var taken [MaxNodeID + 1]bool
	for _, nodeID := range used {
		if nodeID >= MinNodeID && nodeID <= MaxNodeID {
			taken[nodeID] = true
		}
	}
	order := make([]int, 0, MaxNodeID+1)
	if preferred >= MinNodeID && preferred <= MaxNodeID && !taken[preferred] {
		order = append(order, preferred)
		taken[preferred] = true
	}
	for nodeID := MinNodeID; nodeID <= MaxNodeID; nodeID++ {
		if !taken[nodeID] {
			order = append(order, nodeID)
		}
	}
	return order
}

func actorPointer(actor platform.Actor) *int64 {
	if actor.ID <= 0 {
		return nil
	}
	id := actor.ID
	return &id
}
