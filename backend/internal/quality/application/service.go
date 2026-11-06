package qualityapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// Audit action recorded for every accepted evaluation run.
const ActionEvalRecorded = "quality.eval_recorded"

// Snapshot eval lifecycle event types (W2-14, EV-20). They are published on the
// version channel with {eval_run_id,version_id,mode,status,metrics,error}.
const (
	EventEvalStarted   = "eval.started"
	EventEvalCompleted = "eval.completed"
	EventEvalFailed    = "eval.failed"
)

// Service implements the quality application surface.
type Service struct {
	repo        Repository
	ids         platformid.Generator
	clock       clock.Clock
	operational OperationalSource
	settings    SettingsReader
	audit       auditapp.Recorder

	version   VersionSnapshotPort
	design    DesignSnapshotPort
	execution ExecutionSnapshotPort
	visual    VisualSnapshotPort
	events    EventPublisher
}

// NewService wires the quality application service. operational, settings and
// audit may be nil: an unwired aggregator yields seven unavailable metrics, a
// missing settings reader uses the documented 0.05 tolerance, and a missing
// audit sink keeps the operation successful.
func NewService(
	repo Repository,
	ids platformid.Generator,
	clk clock.Clock,
	operational OperationalSource,
	settings SettingsReader,
	audit auditapp.Recorder,
) *Service {
	return &Service{
		repo:        repo,
		ids:         ids,
		clock:       clk,
		operational: operational,
		settings:    settings,
		audit:       audit,
	}
}

// WithSnapshotPorts wires the read-only product-data ports used by
// TriggerSnapshot (W2-14). Any nil port makes a snapshot trigger unavailable
// instead of fabricating metric values.
func (s *Service) WithSnapshotPorts(
	execution ExecutionSnapshotPort,
	version VersionSnapshotPort,
	design DesignSnapshotPort,
	visual VisualSnapshotPort,
) *Service {
	s.execution = execution
	s.version = version
	s.design = design
	s.visual = visual
	return s
}

// WithEvents wires the eval.* lifecycle publisher. A nil publisher keeps the
// trigger successful without events (best-effort UI projection).
func (s *Service) WithEvents(events EventPublisher) *Service {
	s.events = events
	return s
}

// List returns the newest eval runs. A non-positive limit falls back to 20 and
// an oversized limit is capped at 50 (frozen contract).
func (s *Service) List(ctx context.Context, limit int) ([]EvalRun, error) {
	if s.repo == nil {
		return nil, platform.Unavailable("quality repository is not configured")
	}
	return s.repo.List(ctx, normalizeLimit(limit))
}

// Record validates the sample, derives the 6+1 metrics and status on the
// server, persists the run in one transaction and writes a best-effort audit
// entry without corpus or sample content.
func (s *Service) Record(ctx context.Context, actor platform.Actor, input RecordInput) (EvalRun, error) {
	if s.repo == nil {
		return EvalRun{}, platform.Unavailable("quality repository is not configured")
	}
	mode := strings.TrimSpace(input.Mode)
	if mode != quality.EvalModeRealLLM {
		return EvalRun{}, platform.Invalid("mode", "must be real_llm")
	}
	corpusVersion := strings.TrimSpace(input.CorpusVersion)
	if corpusVersion == "" {
		return EvalRun{}, platform.Invalid("corpus_version", "must not be empty")
	}

	declaredError := strings.TrimSpace(input.Status) == quality.StatusError
	metrics := make([]quality.Metric, 0)
	status := quality.StatusError
	if declaredError {
		// A declared error run records the scenario failure and never invents
		// metrics for it.
	} else {
		if input.Sample == nil {
			return EvalRun{}, platform.Invalid("sample", "is required unless status is error")
		}
		if err := quality.ValidateSample(*input.Sample); err != nil {
			return EvalRun{}, err
		}
		status, metrics = quality.Evaluate(*input.Sample, input.Baseline, s.regressionTolerance(ctx))
	}

	now := s.clock.Now().UTC()
	startedAt := now
	if input.StartedAt != nil {
		startedAt = input.StartedAt.UTC()
	}
	finishedAt := now
	if input.FinishedAt != nil {
		finishedAt = input.FinishedAt.UTC()
	}
	summary := input.Summary
	if summary == nil {
		summary = map[string]any{}
	}

	run := EvalRun{
		ID:            s.ids.Next(),
		Mode:          mode,
		CorpusVersion: corpusVersion,
		Model:         strings.TrimSpace(input.Model),
		Status:        status,
		Summary:       summary,
		StartedAt:     startedAt,
		FinishedAt:    &finishedAt,
		CreatedAt:     now,
		Metrics:       metrics,
	}
	if err := s.repo.Insert(ctx, run, metrics); err != nil {
		return EvalRun{}, err
	}
	s.recordAudit(ctx, actor, run)
	return run, nil
}

// TriggerSnapshot derives and persists one mode=snapshot eval run for a version
// from the real product data read ports (W2-14). It publishes eval.started
// before doing the work and eval.completed/eval.failed afterwards. The baseline
// is the previous run of the same version and only marks regressions: it never
// blocks the trigger.
func (s *Service) TriggerSnapshot(ctx context.Context, actor platform.Actor, versionID int64) (EvalRun, error) {
	if versionID <= 0 {
		return EvalRun{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if s.repo == nil {
		return EvalRun{}, platform.Unavailable("quality repository is not configured")
	}
	if s.execution == nil || s.version == nil || s.design == nil || s.visual == nil {
		return EvalRun{}, platform.Unavailable("quality snapshot ports are not configured")
	}

	runID := s.ids.Next()
	version := versionID
	now := s.clock.Now().UTC()
	s.publishSnapshotEvent(ctx, EventEvalStarted, runID, version, quality.StatusRunning, nil, "")

	closed, requirements, err := s.version.CountRequirementClosure(ctx, &version)
	if err != nil {
		return EvalRun{}, s.snapshotFailure(ctx, runID, version, fmt.Errorf("snapshot requirement closure: %w", err))
	}
	confirmedCases, _, err := s.design.CountCaseConfirmation(ctx, &version)
	if err != nil {
		return EvalRun{}, s.snapshotFailure(ctx, runID, version, fmt.Errorf("snapshot case confirmation: %w", err))
	}
	executionPassed, executionFinished, err := s.execution.CountExecutionPass(ctx, &version)
	if err != nil {
		return EvalRun{}, s.snapshotFailure(ctx, runID, version, fmt.Errorf("snapshot execution pass: %w", err))
	}
	visualPassed, visualFailed, err := s.visual.VisualClassification(ctx, &version)
	if err != nil {
		return EvalRun{}, s.snapshotFailure(ctx, runID, version, fmt.Errorf("snapshot visual classification: %w", err))
	}

	sample := quality.SnapshotSample{
		ConfirmedRequirements: requirements,
		CoveredRequirements:   closed,
		ConfirmedCases:        confirmedCases,
		ExecutionPassed:       executionPassed,
		ExecutionFinished:     executionFinished,
		VisualPassed:          visualPassed,
		VisualEvaluated:       visualPassed + visualFailed,
	}
	if err := quality.ValidateSnapshot(sample); err != nil {
		return EvalRun{}, s.snapshotFailure(ctx, runID, version, err)
	}

	status, metrics := quality.EvaluateSnapshot(sample, s.snapshotBaseline(ctx, version), s.regressionTolerance(ctx))
	summary := map[string]any{
		"version_id":             platformid.Format(version),
		"source":                 quality.EvalModeSnapshot,
		"confirmed_requirements": sample.ConfirmedRequirements,
		"covered_requirements":   sample.CoveredRequirements,
		"confirmed_cases":        sample.ConfirmedCases,
		"execution_passed":       sample.ExecutionPassed,
		"execution_finished":     sample.ExecutionFinished,
		"visual_passed":          sample.VisualPassed,
		"visual_evaluated":       sample.VisualEvaluated,
	}
	run := EvalRun{
		ID:            runID,
		Mode:          quality.EvalModeSnapshot,
		CorpusVersion: "snapshot:" + platformid.Format(version),
		Status:        status,
		Summary:       summary,
		VersionID:     &version,
		StartedAt:     now,
		FinishedAt:    &now,
		CreatedAt:     now,
		Metrics:       metrics,
	}
	if err := s.repo.Insert(ctx, run, metrics); err != nil {
		return EvalRun{}, s.snapshotFailure(ctx, runID, version, err)
	}
	s.recordAudit(ctx, actor, run)
	s.publishSnapshotEvent(ctx, EventEvalCompleted, runID, version, status, metrics, "")
	return run, nil
}

// Trends returns the version/metric trend series ordered oldest first. from/to
// are optional bounds (zero = unbounded).
func (s *Service) Trends(ctx context.Context, versionID int64, metric string, from, to time.Time) ([]TrendPoint, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	metric = strings.TrimSpace(metric)
	if metric == "" {
		return nil, platform.Invalid("metric", "is required")
	}
	if !quality.IsKnownMetric(metric) {
		return nil, platform.Invalid("metric", "is not a known eval metric")
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return nil, platform.Invalid("from", "must not be after to")
	}
	if s.repo == nil {
		return nil, platform.Unavailable("quality repository is not configured")
	}
	return s.repo.Trends(ctx, versionID, metric, from, to)
}

// snapshotBaseline reads the previous run of the version and projects its metric
// values into the baseline map. A read failure degrades to no baseline: the
// baseline is a report-only signal and must never block a snapshot trigger.
func (s *Service) snapshotBaseline(ctx context.Context, versionID int64) map[string]float64 {
	previous, err := s.repo.LatestForVersion(ctx, versionID)
	if err != nil || previous == nil || len(previous.Metrics) == 0 {
		return nil
	}
	baseline := make(map[string]float64, len(previous.Metrics))
	for _, metric := range previous.Metrics {
		baseline[metric.Name] = metric.Value
	}
	return baseline
}

// snapshotFailure publishes eval.failed with the concrete error and returns the
// original error so the HTTP layer can map it.
func (s *Service) snapshotFailure(ctx context.Context, runID, versionID int64, err error) error {
	s.publishSnapshotEvent(ctx, EventEvalFailed, runID, versionID, quality.StatusFailed, nil, err.Error())
	return err
}

// publishSnapshotEvent is best effort: a projection failure never changes the
// persisted eval result.
func (s *Service) publishSnapshotEvent(
	ctx context.Context,
	eventType string,
	runID, versionID int64,
	status string,
	metrics []quality.Metric,
	failure string,
) {
	if s.events == nil {
		return
	}
	values := make(map[string]float64, len(metrics))
	for _, metric := range metrics {
		values[metric.Name] = metric.Value
	}
	payload := map[string]any{
		"eval_run_id": platformid.Format(runID),
		"version_id":  platformid.Format(versionID),
		"mode":        quality.EvalModeSnapshot,
		"status":      status,
		"metrics":     values,
		"error":       failure,
	}
	_ = s.events.Publish(ctx, eventType, versionID, payload)
}

// Get returns one eval run or the shared 404 envelope.
func (s *Service) Get(ctx context.Context, id int64) (EvalRun, error) {
	if id <= 0 {
		return EvalRun{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if s.repo == nil {
		return EvalRun{}, platform.Unavailable("quality repository is not configured")
	}
	run, err := s.repo.Get(ctx, id)
	if errors.Is(err, quality.ErrNotFound) {
		return EvalRun{}, platform.NotFound("eval_run")
	}
	if err != nil {
		return EvalRun{}, err
	}
	return run, nil
}

// Overview returns the seven operational metrics for the requested scope plus
// the latest eval summary. A nil aggregator reports seven unavailable metrics
// instead of fabricated zeros.
func (s *Service) Overview(ctx context.Context, versionID *int64) (Overview, error) {
	if versionID != nil && *versionID <= 0 {
		return Overview{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	operational := quality.UnavailableOperationalMetrics()
	if s.operational != nil {
		snapshot, err := s.operational.Snapshot(ctx, versionID)
		if err != nil {
			return Overview{}, err
		}
		if snapshot.Metrics != nil {
			operational = snapshot.Metrics
		}
	}

	var latest *EvalSummary
	if s.repo != nil {
		run, err := s.repo.Latest(ctx)
		switch {
		case errors.Is(err, quality.ErrNotFound):
			// No evaluation has run yet.
		case err != nil:
			return Overview{}, err
		case run != nil:
			latest = summarize(*run)
		}
	}

	return Overview{
		ScopeVersionID: versionID,
		GeneratedAt:    s.clock.Now().UTC(),
		Operational:    operational,
		LatestEval:     latest,
	}, nil
}

func (s *Service) regressionTolerance(ctx context.Context) float64 {
	if s.settings == nil {
		return quality.DefaultRegressionTolerance
	}
	value, err := s.settings.Float(ctx, SettingEvalBaselineRegression)
	if err != nil {
		// The tolerance is a report-only signal; a settings hiccup must not
		// reject an otherwise valid real-LLM evaluation run.
		return quality.DefaultRegressionTolerance
	}
	return value
}

func (s *Service) recordAudit(ctx context.Context, actor platform.Actor, run EvalRun) {
	if s.audit == nil {
		return
	}
	var actorID *int64
	if actor.ID > 0 {
		actorID = &actor.ID
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actorID,
		Action:     ActionEvalRecorded,
		TargetKind: "eval_run",
		TargetID:   platformid.Format(run.ID),
		Metadata: map[string]any{
			"run_id":        platformid.Format(run.ID),
			"status":        run.Status,
			"metrics_count": len(run.Metrics),
		},
	})
}

func summarize(run EvalRun) *EvalSummary {
	passed := 0
	for _, metric := range run.Metrics {
		if metric.Passed {
			passed++
		}
	}
	return &EvalSummary{
		ID:            run.ID,
		Mode:          run.Mode,
		Status:        run.Status,
		CreatedAt:     run.CreatedAt,
		MetricsCount:  len(run.Metrics),
		MetricsPassed: passed,
	}
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultListLimit
	}
	if limit > MaxListLimit {
		return MaxListLimit
	}
	return limit
}
