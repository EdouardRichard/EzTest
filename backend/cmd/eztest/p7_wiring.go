package main

// P7 composition root: multi-replica orchestration (instance registry, leader
// election, readiness), cross-instance event fan-out, the knowledge context,
// the content-ledger GC and the report PDF pipeline. Like the P4/P5/P6 wiring
// files this is the single place that knows the bounded contexts together; the
// contexts themselves stay independent.
//
// Boot ordering (S4 report / Task 19): settings are seeded first, then the
// orchestration registry allocates this replica's snowflake node, and only then
// is the real ID generator built for every other service. The bootstrap
// generator is used solely for the api_instance row; using it for business IDs
// would reintroduce the cross-replica collision the node allocation prevents.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/knowledgeadapter"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/renderclient"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledgepg "github.com/EdouardRichard/EzTest/backend/internal/knowledge/infrastructure/postgres"
	knowledgeapi "github.com/EdouardRichard/EzTest/backend/internal/knowledge/interfaces/httpapi"
	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	orchestrationinfra "github.com/EdouardRichard/EzTest/backend/internal/orchestration/infrastructure"
	orchestrationpg "github.com/EdouardRichard/EzTest/backend/internal/orchestration/infrastructure/postgres"
	orchestrationapi "github.com/EdouardRichard/EzTest/backend/internal/orchestration/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	settingsapp "github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	settingsdomain "github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	storageapi "github.com/EdouardRichard/EzTest/backend/internal/storage/interfaces/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// P7 settings keys consumed by the composition root.
const (
	p7SettingRenderURL      = "report.render_worker_url"
	p7SettingRenderToken    = "report.render_token"
	p7SettingRenderPDF      = "report.render_pdf"
	p7SettingRenderAttempts = "report.render_max_attempts"
	p7RenderClientTimeout   = 60 * time.Second
)

// p7SettingGetter is the read surface of the settings repository used before
// the settings application service exists (it needs the allocated node ID).
// *settingspg.Repository implements it.
type p7SettingGetter interface {
	Get(ctx context.Context, key string) (settingsdomain.Setting, error)
}

// p7RawSettings adapts the settings repository to the typed readers the
// orchestration registry and the readiness storage probe need during boot.
// Values are decoded from the persisted JSON (the same encoding the settings
// center validates); an unknown key is an error so the consumer's documented
// fallback applies.
type p7RawSettings struct {
	repo p7SettingGetter
}

func (s p7RawSettings) raw(ctx context.Context, key string) (settingsdomain.Setting, error) {
	if s.repo == nil {
		return settingsdomain.Setting{}, errors.New("p7 settings reader: settings repository is not configured")
	}
	return s.repo.Get(ctx, key)
}

// String implements orchestrationinfra.RootReader.
func (s p7RawSettings) String(ctx context.Context, key string) (string, error) {
	setting, err := s.raw(ctx, key)
	if err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(setting.Value, &value); err != nil {
		return "", fmt.Errorf("decode setting %s: %w", key, err)
	}
	return value, nil
}

// Int implements orchestrationapp.SettingsReader.
func (s p7RawSettings) Int(ctx context.Context, key string) (int64, error) {
	setting, err := s.raw(ctx, key)
	if err != nil {
		return 0, err
	}
	var value int64
	if err := json.Unmarshal(setting.Value, &value); err != nil {
		return 0, fmt.Errorf("decode setting %s: %w", key, err)
	}
	return value, nil
}

// Bool is the typed boolean reader (unused during boot today but kept so the
// adapter matches the settings center's three-type read surface).
func (s p7RawSettings) Bool(ctx context.Context, key string) (bool, error) {
	setting, err := s.raw(ctx, key)
	if err != nil {
		return false, err
	}
	var value bool
	if err := json.Unmarshal(setting.Value, &value); err != nil {
		return false, fmt.Errorf("decode setting %s: %w", key, err)
	}
	return value, nil
}

// p7LateAudit breaks the construction cycle between the orchestration registry
// (which must start before the real snowflake generator exists) and the audit
// service (which needs that generator). The registry only audits drain/activate
// calls, so the target is always attached before the HTTP server serves any
// request; calls before that are dropped.
type p7LateAudit struct {
	mu     sync.RWMutex
	target auditapp.Recorder
}

// Set attaches the real audit recorder.
func (a *p7LateAudit) Set(target auditapp.Recorder) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.target = target
	a.mu.Unlock()
}

// Record implements auditapp.Recorder.
func (a *p7LateAudit) Record(ctx context.Context, event auditapp.Event) error {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	target := a.target
	a.mu.RUnlock()
	if target == nil {
		return nil
	}
	return target.Record(ctx, event)
}

// p7KnowledgeAudit adapts the knowledge application audit port
// (Record(ctx, action, targetID, metadata)) onto the shared audit service. The
// knowledge service supplies the acting user ID as a decimal string in
// metadata["actor_id"]; the metadata itself never contains the item body.
type p7KnowledgeAudit struct {
	recorder auditapp.Recorder
}

// Record implements knowledgeapp.AuditRecorder.
func (a p7KnowledgeAudit) Record(ctx context.Context, action string, targetID int64, metadata map[string]any) error {
	if a.recorder == nil {
		return nil
	}
	event := auditapp.Event{
		Action:     action,
		TargetKind: "knowledge_item",
		TargetID:   platformid.Format(targetID),
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	}
	if raw, ok := metadata["actor_id"]; ok {
		if formatted, ok := raw.(string); ok {
			if actorID, err := platformid.Parse(formatted); err == nil && actorID > 0 {
				event.ActorID = &actorID
			}
		}
	}
	return a.recorder.Record(ctx, event)
}

// p7SourceVersions answers the knowledge read-time source_stale marker. It is a
// composition-root adapter because knowledge never reads the version context's
// tables directly.
type p7SourceVersions struct {
	pool *pgxpool.Pool
}

// SourceVersionExists implements knowledgeapp.SourceVersionReader.
func (r p7SourceVersions) SourceVersionExists(ctx context.Context, versionID int64) (bool, error) {
	if r.pool == nil {
		return false, errors.New("knowledge source version reader: database pool is not configured")
	}
	if versionID <= 0 {
		return false, nil
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM version WHERE id = $1)`, versionID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// p7VersionSystems resolves the SUT families bound to a version so system-scope
// knowledge can match them. The version/resource read models do not expose
// sut.family yet (resource migration #38 adds the column), so the adapter joins
// version_sut to sut in one read-only query.
type p7VersionSystems struct {
	pool *pgxpool.Pool
}

// VersionSystemKeys implements knowledgeadapter.VersionSystemReader.
func (r p7VersionSystems) VersionSystemKeys(ctx context.Context, versionID int64) ([]string, error) {
	if r.pool == nil {
		return nil, errors.New("knowledge version system reader: database pool is not configured")
	}
	if versionID <= 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT s.family
		FROM version_sut vs
		JOIN sut s ON s.id = vs.sut_id
		WHERE vs.version_id = $1 AND s.family IS NOT NULL
		ORDER BY s.family`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]string, 0, 4)
	for rows.Next() {
		var family string
		if err := rows.Scan(&family); err != nil {
			return nil, err
		}
		if family = strings.TrimSpace(family); family != "" {
			keys = append(keys, family)
		}
	}
	return keys, rows.Err()
}

// p7OwnerLookup answers the GC owner-existence probes across the delivery,
// version and execution tables. GC consults the owning entity before deleting
// an orphan ref; a lookup error makes the pass fail (fail-closed, never delete
// on uncertainty).
type p7OwnerLookup struct {
	pool *pgxpool.Pool
}

func (l p7OwnerLookup) exists(ctx context.Context, query string, id int64) (bool, error) {
	if l.pool == nil {
		return false, errors.New("gc owner lookup: database pool is not configured")
	}
	if id <= 0 {
		return false, nil
	}
	var exists bool
	if err := l.pool.QueryRow(ctx, query, id).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// ReportRevisionExists implements storageapp.OwnerLookup.
func (l p7OwnerLookup) ReportRevisionExists(ctx context.Context, revisionID int64) (bool, error) {
	return l.exists(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_report_revision WHERE id = $1)`, revisionID)
}

// AttachmentExists implements storageapp.OwnerLookup. The spec protection rule
// requires both the attachment row and its version row to exist.
func (l p7OwnerLookup) AttachmentExists(ctx context.Context, attachmentID int64) (bool, error) {
	return l.exists(ctx, `SELECT EXISTS(
		SELECT 1 FROM requirement_attachment a JOIN version v ON v.id = a.version_id
		WHERE a.id = $1)`, attachmentID)
}

// EvidenceExists implements storageapp.OwnerLookup.
func (l p7OwnerLookup) EvidenceExists(ctx context.Context, evidenceID int64) (bool, error) {
	return l.exists(ctx, `SELECT EXISTS(SELECT 1 FROM evidence WHERE id = $1)`, evidenceID)
}

// ExportArtifactExists implements storageapp.OwnerLookup.
func (l p7OwnerLookup) ExportArtifactExists(ctx context.Context, artifactID int64) (bool, error) {
	return l.exists(ctx, `SELECT EXISTS(SELECT 1 FROM export_artifact WHERE id = $1)`, artifactID)
}

// renderQueueAdapter adapts the agent work queue to the delivery context's
// RenderQueue port, converting agent.WorkItem into the delivery-owned RenderJob
// so no agent-domain type crosses into delivery (Task 19, S1 report §5.3).
// P7 drain semantics: once this replica is draining, the worker stops claiming
// new PDF jobs (in-flight jobs finish under their lease).
type renderQueueAdapter struct {
	queue    *agentapp.WorkQueue
	draining func() bool
}

// ClaimForTypes implements deliveryapp.RenderQueue.
func (q renderQueueAdapter) ClaimForTypes(ctx context.Context, claimant string, types []string, lease time.Duration) (deliveryapp.RenderJob, bool, error) {
	if q.draining != nil && q.draining() {
		return deliveryapp.RenderJob{}, false, nil
	}
	if q.queue == nil {
		return deliveryapp.RenderJob{}, false, errors.New("report render queue: work queue is not configured")
	}
	item, ok, err := q.queue.ClaimForTypes(ctx, claimant, types, lease)
	if err != nil || !ok {
		return deliveryapp.RenderJob{}, ok, err
	}
	return deliveryapp.RenderJob{
		ID:          item.ID,
		Epoch:       item.LeaseEpoch,
		VersionID:   item.VersionID,
		Attempts:    item.Attempts,
		MaxAttempts: item.MaxAttempts,
		Payload:     item.Payload,
	}, true, nil
}

// Heartbeat implements deliveryapp.RenderQueue. It renews with the exact lease
// the job was claimed with (report.render_lease_seconds), never the agent queue
// default, so the lease cannot lapse between renewals.
func (q renderQueueAdapter) Heartbeat(ctx context.Context, jobID, epoch int64, lease time.Duration) error {
	if q.queue == nil {
		return errors.New("report render queue: work queue is not configured")
	}
	return q.queue.Heartbeat(ctx, jobID, epoch, lease)
}

// Complete implements deliveryapp.RenderQueue.
func (q renderQueueAdapter) Complete(ctx context.Context, jobID, epoch int64) error {
	if q.queue == nil {
		return errors.New("report render queue: work queue is not configured")
	}
	return q.queue.Complete(ctx, jobID, epoch)
}

// Fail implements deliveryapp.RenderQueue.
func (q renderQueueAdapter) Fail(ctx context.Context, jobID, epoch int64, cause error, retryable bool) error {
	if q.queue == nil {
		return errors.New("report render queue: work queue is not configured")
	}
	return q.queue.Fail(ctx, jobID, epoch, cause, retryable)
}

// p7RenderSettings resolves the two PDF settings the report service needs at
// construction. A settings read failure keeps PDF enabled with the frozen
// default attempt count; the reconciler ensurer re-reads the live value.
func p7RenderSettings(ctx context.Context, settings interface {
	Int(ctx context.Context, key string) (int64, error)
	Bool(ctx context.Context, key string) (bool, error)
}) (bool, int) {
	enabled := true
	if settings != nil {
		if value, err := settings.Bool(ctx, p7SettingRenderPDF); err == nil {
			enabled = value
		}
	}
	attempts := deliveryapp.DefaultRenderMaxAttempts
	if settings != nil {
		if value, err := settings.Int(ctx, p7SettingRenderAttempts); err == nil &&
			value >= int64(deliveryapp.MinRenderAttempts) && value <= int64(deliveryapp.MaxRenderAttempts) {
			attempts = int(value)
		}
	}
	return enabled, attempts
}

// p7GCInterval resolves retention.gc_interval_minutes for the leader-gated GC
// loop with the same defaults/clamp as the storage GC worker.
func p7GCInterval(ctx context.Context, settings interface {
	Int(ctx context.Context, key string) (int64, error)
}) time.Duration {
	if settings != nil {
		if minutes, err := settings.Int(ctx, storageapp.GCSettingIntervalMinutes); err == nil && minutes > 0 {
			interval := time.Duration(minutes) * time.Minute
			if interval < storageapp.MinGCInterval {
				return storageapp.MinGCInterval
			}
			return interval
		}
	}
	return storageapp.DefaultGCInterval
}

// p7RunGated executes one leader-gated tick. Every long-running P7 scheduler
// calls it per tick: a follower never starts a duplicate pass, and a leadership
// change is picked up on the next tick without restarting the loop.
func p7RunGated(gate orchestrationapp.LeaderGate, fn func()) bool {
	return orchestrationapp.RunIfLeader(gate, fn)
}

// p7InstanceKey is the frozen api_instance identity: hostname:pid (spec §4.5).
// It is deliberately distinct from instanceID (hostname-pid) used by workers.
func p7InstanceKey() string {
	return fmt.Sprintf("%s:%d", hostName(), os.Getpid())
}

// p7ExecutorLister is the roster read the dispatch restriction needs.
type p7ExecutorLister interface {
	List(ctx context.Context) ([]execution.ExecutorInstance, error)
}

// primaryExecutorRegistry restricts case-run dispatch to the primary
// execution.executor_url endpoint.
//
// Task 19 restriction (S5 report §3): the P7 registry discovers/registers/
// heartbeats/list every endpoint in execution.executor_urls, but the
// CaseRunner/Browser adapter in this composition root is bound to a single
// executor client. Dispatching a case to a secondary or push-registered
// endpoint would therefore silently drive the primary executor. Until a
// per-executor browser client cache exists (P8), Select only returns a
// dispatchable row whose endpoint normalizes to the primary endpoint; the extra
// endpoints keep being polled, heartbeated, registered and admin-listed.
type primaryExecutorRegistry struct {
	registry p7ExecutorLister
	primary  string
}

// Select implements executionapp.ExecutorRegistry.
func (p primaryExecutorRegistry) Select(ctx context.Context) (*execution.ExecutorInstance, error) {
	if p.registry == nil {
		return nil, nil
	}
	instances, err := p.registry.List(ctx)
	if err != nil {
		return nil, err
	}
	target := p7NormalizeEndpoint(p.primary)
	for i := range instances {
		row := instances[i]
		if !row.Status.Dispatchable() {
			continue
		}
		if target != "" && p7NormalizeEndpoint(row.Endpoint) != target {
			continue
		}
		return &row, nil
	}
	return nil, nil
}

// p7NormalizeEndpoint mirrors the execution registry's endpoint normalization.
func p7NormalizeEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}

// p7Orchestration is the boot-time registry surface. Service is started before
// the ID generator is rebuilt; Leader is started with the other background
// loops.
type p7Orchestration struct {
	Service   *orchestrationapp.Service
	Leader    *orchestrationpg.Leader
	LateAudit *p7LateAudit
}

// p7OrchestrationDeps are the boot-time dependencies of the instance registry.
type p7OrchestrationDeps struct {
	Pool         *pgxpool.Pool
	SettingsRepo p7SettingGetter
	BootstrapIDs platformid.Generator
	Clock        clock.Clock
	Events       eventingapp.Publisher
	Hostname     string
	PID          int
	APIPort      int
	Version      string
	InstanceKey  string
}

// startP7Orchestration registers this API replica and allocates its snowflake
// node ID. It runs before every other P7 service because the allocated node
// seeds the real ID generator (S4 report concern #1).
func startP7Orchestration(ctx context.Context, deps p7OrchestrationDeps) (*p7Orchestration, error) {
	if deps.Pool == nil {
		return nil, errors.New("p7 orchestration: nil database pool")
	}
	if deps.SettingsRepo == nil {
		return nil, errors.New("p7 orchestration: nil settings repository")
	}
	if deps.BootstrapIDs == nil {
		return nil, errors.New("p7 orchestration: nil bootstrap id generator")
	}
	if strings.TrimSpace(deps.InstanceKey) == "" {
		return nil, errors.New("p7 orchestration: empty instance key")
	}
	lateAudit := &p7LateAudit{}
	settings := p7RawSettings{repo: deps.SettingsRepo}
	probe := orchestrationinfra.NewStorageProbe(settings, deps.InstanceKey)
	service := orchestrationapp.New(
		orchestrationpg.New(deps.Pool),
		deps.BootstrapIDs,
		deps.Clock,
		settings,
		probe,
		lateAudit,
		deps.Events,
	)
	if _, err := service.Start(ctx, orchestrationapp.StartMeta{
		Hostname: deps.Hostname,
		PID:      deps.PID,
		APIPort:  deps.APIPort,
		Version:  deps.Version,
	}); err != nil {
		return nil, fmt.Errorf("register api instance: %w", err)
	}
	return &p7Orchestration{
		Service:   service,
		Leader:    orchestrationpg.NewLeader(deps.Pool),
		LateAudit: lateAudit,
	}, nil
}

// p7RuntimeDeps are the already-constructed P0-P6 services the P7 composition
// consumes.
type p7RuntimeDeps struct {
	Pool           *pgxpool.Pool
	Settings       *settingsapp.Service
	IDs            platformid.Generator
	Clock          clock.Clock
	Audit          auditapp.Recorder
	Events         eventingapp.Publisher
	Hub            *eventingapp.Hub
	Queue          *agentapp.WorkQueue
	ReportRepo     *deliverypg.ReportRepository
	ReportContent  deliveryapp.RenderContent
	DeliveryEvents deliveryapp.EventPublisher
	DeliveryAudit  deliveryapp.AuditSink
	Orchestrator   *orchestrationapp.Service
	Leader         *orchestrationpg.Leader
	InstanceKey    string
	InstanceID     string
	Logger         *slog.Logger
	RenderTimeout  time.Duration
}

// p7Runtime is the constructed P7 service/handler surface plus the background
// workers main starts.
type p7Runtime struct {
	Knowledge        *knowledgeapi.Handler
	KnowledgeAdapter *knowledgeadapter.Adapter
	Orchestration    *orchestrationapi.Handler
	GC               *storageapi.GCHandler
	RenderEnsurer    *deliveryapp.RenderJobEnsurer
	RenderWorker     *deliveryapp.RenderWorker
	GCWorker         *storageapp.GCWorker
	Listener         *eventingpg.Listener

	orchestrator *orchestrationapp.Service
	leader       *orchestrationpg.Leader
}

// newP7Runtime builds the knowledge, GC and report PDF pipeline over the
// already-constructed P0-P6 services. Construction fails closed on a missing
// required collaborator rather than producing a handler that panics at request
// time.
func newP7Runtime(ctx context.Context, deps p7RuntimeDeps) (*p7Runtime, error) {
	if deps.Pool == nil {
		return nil, errors.New("p7 runtime: nil database pool")
	}
	if deps.Settings == nil {
		return nil, errors.New("p7 runtime: nil settings service")
	}
	if deps.IDs == nil {
		return nil, errors.New("p7 runtime: nil id generator")
	}
	if deps.Hub == nil {
		return nil, errors.New("p7 runtime: nil event hub")
	}
	if deps.Queue == nil {
		return nil, errors.New("p7 runtime: nil work queue")
	}
	if deps.ReportRepo == nil {
		return nil, errors.New("p7 runtime: nil report repository")
	}
	if deps.ReportContent == nil {
		return nil, errors.New("p7 runtime: nil report content store")
	}
	if deps.Orchestrator == nil {
		return nil, errors.New("p7 runtime: nil orchestration service")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderTimeout := deps.RenderTimeout
	if renderTimeout <= 0 {
		renderTimeout = p7RenderClientTimeout
	}

	// Knowledge context: repository, usage repository, service, cross-context
	// adapters and the agent/tool bridge.
	knowledgeService := knowledgeapp.NewService(
		knowledgepg.NewRepository(deps.Pool),
		knowledgepg.NewUsageRepository(deps.Pool),
		deps.Settings,
		p7KnowledgeAudit{recorder: deps.Audit},
		deps.Events,
		p7SourceVersions{pool: deps.Pool},
		deps.Clock,
		deps.IDs,
	)
	knowledgeAdapter := knowledgeadapter.New(knowledgeService, p7VersionSystems{pool: deps.Pool})

	// Storage GC: repository, owner lookup, filesystem ledger adapter, service
	// and the leader-gated periodic worker.
	gcService := storageapp.NewGCService(
		storagepg.NewGCRepository(deps.Pool),
		p7OwnerLookup{pool: deps.Pool},
		storagepg.NewFileStore(),
		deps.Settings,
		deps.IDs,
		deps.Clock,
		deps.Audit,
		deps.Events,
	)

	// Report PDF: renderer client, work-queue adapter (with the drain gate) and
	// the per-instance worker.
	renderURL, _ := deps.Settings.String(ctx, p7SettingRenderURL)
	renderToken, _ := deps.Settings.String(ctx, p7SettingRenderToken)
	renderClient := renderclient.New(renderURL, renderToken, renderTimeout)
	renderWorker := deliveryapp.NewRenderWorker(deliveryapp.RenderWorkerDeps{
		Queue:      renderQueueAdapter{queue: deps.Queue, draining: deps.Orchestrator.IsDraining},
		Jobs:       deps.ReportRepo,
		Content:    deps.ReportContent,
		Client:     renderClient,
		Settings:   deps.Settings,
		Events:     deps.DeliveryEvents,
		Audit:      deps.DeliveryAudit,
		Clock:      deps.Clock,
		InstanceID: deps.InstanceID,
		Logger:     logger,
	})

	// Cross-instance SSE fan-out: every replica runs one LISTEN connection and
	// replays the persisted event into its local hub. P8 also reloads the
	// settings cache when another replica publishes settings.updated, so hot
	// updates apply to every replica within one heartbeat.
	listener := eventingpg.NewListener(
		deps.Pool,
		eventingpg.New(deps.Pool),
		deps.Hub,
		eventingpg.WithListenerLogger(logger),
		eventingpg.WithListenerObserver(func(ctx context.Context, event eventing.Event) error {
			if event.Type != "settings.updated" {
				return nil
			}
			return deps.Settings.Reload(ctx)
		}),
	)

	return &p7Runtime{
		Knowledge:        knowledgeapi.New(knowledgeService),
		KnowledgeAdapter: knowledgeAdapter,
		Orchestration:    orchestrationapi.New(deps.Orchestrator),
		GC:               storageapi.NewGC(gcService),
		RenderEnsurer:    deliveryapp.NewRenderJobEnsurer(deps.ReportRepo, deps.Settings),
		RenderWorker:     renderWorker,
		GCWorker:         storageapp.NewGCWorker(gcService, deps.Settings, logger),
		Listener:         listener,
		orchestrator:     deps.Orchestrator,
		leader:           deps.Leader,
	}, nil
}

// StartWorkers starts every P7 background loop. The leader loop comes first so
// the gated schedulers observe leadership as soon as it is acquired; everything
// except the leader-gated GC worker also runs on followers (render worker,
// listener, instance maintenance).
func (r *p7Runtime) StartWorkers(ctx context.Context, background *sync.WaitGroup, settings *settingsapp.Service) {
	if r == nil || background == nil {
		return
	}
	start := func(name string, run func() error) {
		background.Add(1)
		go func() {
			defer background.Done()
			if err := run(); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("eztest: %s stopped: %v", name, err)
			}
		}()
	}
	if r.leader != nil {
		start("orchestration leader", func() error { return r.leader.Run(ctx) })
	}
	if r.Listener != nil {
		start("event listener", func() error { return r.Listener.Run(ctx) })
	}
	if r.orchestrator != nil {
		start("instance maintenance", func() error { return r.orchestrator.RunMaintenance(ctx) })
	}
	if r.RenderWorker != nil {
		start("report pdf worker", func() error { return r.RenderWorker.Run(ctx) })
	}
	if r.GCWorker != nil {
		background.Add(1)
		go func() {
			defer background.Done()
			runP7GCWorker(ctx, r.leader, r.GCWorker, func(ctx context.Context) time.Duration {
				return p7GCInterval(ctx, settings)
			}, func(format string, args ...any) { log.Printf("eztest: "+format, args...) })
		}()
	}
}

// runP7GCWorker runs the storage GC pass on every interval, but only on the
// leader. RunIfLeader is evaluated per tick so leadership transfer starts/stops
// GC without restarting the loop; in-flight runs are protected by the single
// running gc_run index.
func runP7GCWorker(
	ctx context.Context,
	gate orchestrationapp.LeaderGate,
	worker *storageapp.GCWorker,
	interval func(context.Context) time.Duration,
	logf func(string, ...any),
) {
	if worker == nil {
		return
	}
	for {
		wait := storageapp.DefaultGCInterval
		if interval != nil {
			if resolved := interval(ctx); resolved > 0 {
				wait = resolved
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		p7RunGated(gate, func() {
			if _, err := worker.RunOnce(ctx); err != nil && ctx.Err() == nil && logf != nil {
				logf("gc worker: run failed: %v", err)
			}
		})
	}
}

// p7ServerHandlers projects the runtime onto the server's optional P7 group.
func (r *p7Runtime) p7ServerHandlers() *server.P7Handlers {
	if r == nil {
		return nil
	}
	return &server.P7Handlers{
		Knowledge:     r.Knowledge,
		Orchestration: r.Orchestration,
		GC:            r.GC,
	}
}

// p7StopTimeout bounds the registry Stop call on shutdown.
const p7StopTimeout = 5 * time.Second
