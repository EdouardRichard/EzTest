package qualityapp

import (
	"context"
	"errors"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// Audit action recorded for every accepted evaluation run.
const ActionEvalRecorded = "quality.eval_recorded"

// Service implements the quality application surface.
type Service struct {
	repo        Repository
	ids         platformid.Generator
	clock       clock.Clock
	operational OperationalSource
	settings    SettingsReader
	audit       auditapp.Recorder
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
