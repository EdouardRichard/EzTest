package qualityapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

var (
	testNow   = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	testStart = testNow.Add(-2 * time.Minute)
	testEnd   = testNow
)

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

type seqIDs struct{ next int64 }

func (s *seqIDs) Next() int64 {
	s.next++
	return s.next
}

type fakeRepo struct {
	insertedRun     qualityapp.EvalRun
	insertedMetrics []quality.Metric
	insertCalls     int
	insertErr       error

	getRun qualityapp.EvalRun
	getErr error

	listRuns  []qualityapp.EvalRun
	listErr   error
	listLimit int

	latest    *qualityapp.EvalRun
	latestErr error
}

func (f *fakeRepo) Insert(_ context.Context, run qualityapp.EvalRun, metrics []quality.Metric) error {
	f.insertCalls++
	f.insertedRun = run
	f.insertedMetrics = metrics
	return f.insertErr
}

func (f *fakeRepo) Get(context.Context, int64) (qualityapp.EvalRun, error) {
	return f.getRun, f.getErr
}

func (f *fakeRepo) List(_ context.Context, limit int) ([]qualityapp.EvalRun, error) {
	f.listLimit = limit
	return f.listRuns, f.listErr
}

func (f *fakeRepo) Latest(context.Context) (*qualityapp.EvalRun, error) {
	return f.latest, f.latestErr
}

type fakeSource struct {
	snapshot quality.OperationalSnapshot
	version  *int64
	calls    int
	err      error
}

func (f *fakeSource) Snapshot(_ context.Context, versionID *int64) (quality.OperationalSnapshot, error) {
	f.calls++
	f.version = versionID
	return f.snapshot, f.err
}

type fakeSettings struct {
	value float64
	err   error
	key   string
}

func (f *fakeSettings) Float(_ context.Context, key string) (float64, error) {
	f.key = key
	return f.value, f.err
}

type fakeAudit struct {
	events []auditapp.Event
}

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

func passingSample() quality.Sample {
	return quality.Sample{
		ConfirmedRequirements:  10,
		CoveredRequirements:    9,
		ImpactExpected:         10,
		ImpactHit:              8,
		ImpactSelected:         10,
		InjectedFailures:       10,
		CorrectClassifications: 9,
		AIIssues:               10,
		TruePositiveIssues:     9,
		SupplementCases:        10,
		SupplementHits:         8,
	}
}

func newService(repo *fakeRepo, source *fakeSource, settings *fakeSettings, audit *fakeAudit) *qualityapp.Service {
	var operational qualityapp.OperationalSource
	if source != nil {
		operational = source
	}
	var settingsReader qualityapp.SettingsReader
	if settings != nil {
		settingsReader = settings
	}
	var auditRecorder auditapp.Recorder
	if audit != nil {
		auditRecorder = audit
	}
	return qualityapp.NewService(repo, &seqIDs{next: 100}, fixedClock{at: testNow}, operational, settingsReader, auditRecorder)
}

func inputWith(sample quality.Sample) qualityapp.RecordInput {
	return qualityapp.RecordInput{
		Mode:          quality.EvalModeRealLLM,
		CorpusVersion: "v02-2026-11-01.1",
		Model:         "deepseek-flash",
		Sample:        &sample,
		Baseline:      map[string]float64{},
		Summary:       map[string]any{"version_id": "10"},
		StartedAt:     &testStart,
		FinishedAt:    &testEnd,
	}
}

func apiError(t *testing.T, err error) *platform.Error {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *platform.Error", err)
	}
	return apiErr
}

func TestRecordDerivesMetricsAndIgnoresClientVerdict(t *testing.T) {
	repo := &fakeRepo{}
	audit := &fakeAudit{}
	service := newService(repo, nil, nil, audit)

	sample := passingSample()
	sample.CoveredRequirements = 5 // design_coverage 0.5 < 0.80
	input := inputWith(sample)
	input.Status = "passed" // client verdict must be ignored, not rejected

	run, err := service.Record(context.Background(), platform.Actor{ID: 77, Role: "admin"}, input)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if run.ID != 101 {
		t.Fatalf("run.ID = %d", run.ID)
	}
	if run.Status != quality.StatusFailed {
		t.Fatalf("status = %q, want failed", run.Status)
	}
	if repo.insertCalls != 1 {
		t.Fatalf("insert calls = %d", repo.insertCalls)
	}
	if repo.insertedRun.Status != quality.StatusFailed {
		t.Fatalf("persisted status = %q", repo.insertedRun.Status)
	}
	if len(run.Metrics) != 6 || len(repo.insertedMetrics) != 6 {
		t.Fatalf("metrics = %d/%d, want 6", len(run.Metrics), len(repo.insertedMetrics))
	}
	if run.Metrics[0].Name != quality.MetricDesignCoverage || run.Metrics[0].Passed {
		t.Fatalf("design metric = %+v", run.Metrics[0])
	}
	if run.Mode != quality.EvalModeRealLLM || run.CorpusVersion != "v02-2026-11-01.1" || run.Model != "deepseek-flash" {
		t.Fatalf("run header = %+v", run)
	}
	if !run.StartedAt.Equal(testStart) || run.FinishedAt == nil || !run.FinishedAt.Equal(testEnd) {
		t.Fatalf("times = %v / %v", run.StartedAt, run.FinishedAt)
	}
	if run.CreatedAt != testNow {
		t.Fatalf("created at = %v", run.CreatedAt)
	}

	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d", len(audit.events))
	}
	event := audit.events[0]
	if event.Action != "quality.eval_recorded" || event.TargetKind != "eval_run" || event.TargetID != "101" {
		t.Fatalf("audit event = %+v", event)
	}
	if event.ActorID == nil || *event.ActorID != 77 {
		t.Fatalf("audit actor = %v", event.ActorID)
	}
	if event.Metadata["run_id"] != "101" || event.Metadata["status"] != quality.StatusFailed {
		t.Fatalf("audit metadata = %+v", event.Metadata)
	}
	if event.Metadata["metrics_count"] != 6 {
		t.Fatalf("audit metrics_count = %#v", event.Metadata["metrics_count"])
	}
	for key := range event.Metadata {
		switch key {
		case "run_id", "status", "metrics_count":
		default:
			t.Fatalf("audit metadata must not carry %q: %+v", key, event.Metadata)
		}
	}
}

func TestRecordDerivesSevenMetricsWhenVisualCountersArePresent(t *testing.T) {
	repo := &fakeRepo{}
	service := newService(repo, nil, nil, nil)

	sample := passingSample()
	visualInjected, visualCorrect := 10, 9
	sample.VisualInjected = &visualInjected
	sample.VisualCorrect = &visualCorrect

	run, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, inputWith(sample))
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(run.Metrics) != 7 {
		t.Fatalf("metrics = %d, want 7", len(run.Metrics))
	}
	if run.Metrics[6].Name != quality.MetricVisualClassificationAccuracy {
		t.Fatalf("last metric = %q", run.Metrics[6].Name)
	}
	if run.Status != quality.StatusPassed {
		t.Fatalf("status = %q, want passed", run.Status)
	}
}

func TestRecordRejectsInvalidSample(t *testing.T) {
	repo := &fakeRepo{}
	service := newService(repo, nil, nil, nil)

	sample := passingSample()
	sample.CoveredRequirements = sample.ConfirmedRequirements + 1
	_, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, inputWith(sample))

	apiErr := apiError(t, err)
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("error = %+v", apiErr)
	}
	if apiErr.Details["field"] != "covered_requirements" {
		t.Fatalf("field = %v", apiErr.Details["field"])
	}
	if repo.insertCalls != 0 {
		t.Fatalf("invalid sample reached the repository")
	}
}

func TestRecordRejectsUnsupportedMode(t *testing.T) {
	for _, mode := range []string{"", "deterministic", "fake", "REAL_LLM"} {
		t.Run(mode, func(t *testing.T) {
			repo := &fakeRepo{}
			service := newService(repo, nil, nil, nil)
			input := inputWith(passingSample())
			input.Mode = mode
			_, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, input)
			apiErr := apiError(t, err)
			if apiErr.Details["field"] != "mode" {
				t.Fatalf("mode %q field = %v", mode, apiErr.Details["field"])
			}
			if repo.insertCalls != 0 {
				t.Fatalf("unsupported mode reached the repository")
			}
		})
	}
}

func TestRecordRejectsEmptyCorpusVersion(t *testing.T) {
	repo := &fakeRepo{}
	service := newService(repo, nil, nil, nil)
	input := inputWith(passingSample())
	input.CorpusVersion = "   "
	_, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, input)
	apiErr := apiError(t, err)
	if apiErr.Details["field"] != "corpus_version" {
		t.Fatalf("field = %v", apiErr.Details["field"])
	}
}

func TestRecordRequiresSampleUnlessTheRunIsError(t *testing.T) {
	repo := &fakeRepo{}
	service := newService(repo, nil, nil, nil)

	input := inputWith(passingSample())
	input.Sample = nil
	input.Status = ""
	_, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, input)
	if field := apiError(t, err).Details["field"]; field != "sample" {
		t.Fatalf("field = %v", field)
	}

	errorInput := input
	errorInput.Status = quality.StatusError
	run, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, errorInput)
	if err != nil {
		t.Fatalf("error run: %v", err)
	}
	if run.Status != quality.StatusError {
		t.Fatalf("status = %q", run.Status)
	}
	if len(run.Metrics) != 0 || len(repo.insertedMetrics) != 0 {
		t.Fatalf("error run invented metrics: %d", len(run.Metrics))
	}
	if repo.insertedRun.FinishedAt == nil {
		t.Fatalf("error run has no finished_at")
	}
}

func TestRecordErrorStatusSkipsMetricDerivation(t *testing.T) {
	repo := &fakeRepo{}
	service := newService(repo, nil, nil, nil)
	input := inputWith(passingSample())
	input.Status = quality.StatusError
	run, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, input)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if run.Status != quality.StatusError || len(run.Metrics) != 0 {
		t.Fatalf("run = %+v", run)
	}
}

func TestRecordDefaultsMissingTimesToNow(t *testing.T) {
	repo := &fakeRepo{}
	service := newService(repo, nil, nil, nil)
	input := inputWith(passingSample())
	input.StartedAt = nil
	input.FinishedAt = nil
	run, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, input)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !run.StartedAt.Equal(testNow) || run.FinishedAt == nil || !run.FinishedAt.Equal(testNow) {
		t.Fatalf("times = %v / %v", run.StartedAt, run.FinishedAt)
	}
}

func TestRecordUsesConfiguredRegressionTolerance(t *testing.T) {
	t.Run("configured tolerance", func(t *testing.T) {
		repo := &fakeRepo{}
		settings := &fakeSettings{value: 0.20}
		service := newService(repo, nil, settings, nil)
		sample := passingSample()
		sample.CoveredRequirements = 8 // 0.80 vs baseline 0.90 => 0.10 <= 0.20
		input := inputWith(sample)
		input.Baseline = map[string]float64{quality.MetricDesignCoverage: 0.90}
		run, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, input)
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		if settings.key != qualityapp.SettingEvalBaselineRegression {
			t.Fatalf("settings key = %q", settings.key)
		}
		metric := run.Metrics[0]
		if metric.Detail.Regressed || run.Status != quality.StatusPassed {
			t.Fatalf("metric = %+v status=%s", metric, run.Status)
		}
	})

	t.Run("settings error falls back to the default", func(t *testing.T) {
		repo := &fakeRepo{}
		settings := &fakeSettings{err: errors.New("settings unavailable")}
		service := newService(repo, nil, settings, nil)
		sample := passingSample()
		sample.CoveredRequirements = 8 // 0.80 vs baseline 0.90 => 0.10 > 0.05
		input := inputWith(sample)
		input.Baseline = map[string]float64{quality.MetricDesignCoverage: 0.90}
		run, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, input)
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		if !run.Metrics[0].Detail.Regressed || run.Status != quality.StatusFailed {
			t.Fatalf("metric = %+v status=%s", run.Metrics[0], run.Status)
		}
	})
}

func TestRecordPropagatesRepositoryError(t *testing.T) {
	repo := &fakeRepo{insertErr: errors.New("db down")}
	service := newService(repo, nil, nil, nil)
	_, err := service.Record(context.Background(), platform.Actor{ID: 1, Role: "admin"}, inputWith(passingSample()))
	if err == nil || err.Error() != "db down" {
		t.Fatalf("error = %v", err)
	}
}

func TestListNormalizesLimit(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{-5, qualityapp.DefaultListLimit},
		{0, qualityapp.DefaultListLimit},
		{1, 1},
		{50, 50},
		{51, qualityapp.MaxListLimit},
	}
	for _, tc := range cases {
		repo := &fakeRepo{listRuns: []qualityapp.EvalRun{{ID: 1}}}
		service := newService(repo, nil, nil, nil)
		_, err := service.List(context.Background(), tc.in)
		if err != nil {
			t.Fatalf("List(%d): %v", tc.in, err)
		}
		if repo.listLimit != tc.want {
			t.Fatalf("List(%d) repo limit = %d, want %d", tc.in, repo.listLimit, tc.want)
		}
	}
}

func TestGetValidatesIDAndMapsNotFound(t *testing.T) {
	repo := &fakeRepo{}
	service := newService(repo, nil, nil, nil)
	if _, err := service.Get(context.Background(), 0); apiError(t, err).Details["field"] != "id" {
		t.Fatalf("zero id was not rejected")
	}

	repo.getErr = quality.ErrNotFound
	_, err := service.Get(context.Background(), 42)
	apiErr := apiError(t, err)
	if apiErr.Status != 404 || apiErr.Code != "not_found" {
		t.Fatalf("error = %+v", apiErr)
	}
	if apiErr.Details["kind"] != "eval_run" {
		t.Fatalf("kind = %v", apiErr.Details["kind"])
	}

	repo.getRun = qualityapp.EvalRun{ID: 42}
	repo.getErr = nil
	run, err := service.Get(context.Background(), 42)
	if err != nil || run.ID != 42 {
		t.Fatalf("run = %+v err=%v", run, err)
	}
}

func TestOverviewWithoutSourceReportsSevenUnavailableMetrics(t *testing.T) {
	repo := &fakeRepo{}
	service := newService(repo, nil, nil, nil)
	overview, err := service.Overview(context.Background(), nil)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(overview.Operational) != 7 {
		t.Fatalf("operational = %d, want 7", len(overview.Operational))
	}
	for _, metric := range overview.Operational {
		if metric.Available || metric.Value != nil {
			t.Fatalf("%s available with no source: %+v", metric.Name, metric)
		}
		if metric.Definition == "" {
			t.Fatalf("%s has no definition", metric.Name)
		}
	}
	if overview.LatestEval != nil {
		t.Fatalf("latest eval = %+v", overview.LatestEval)
	}
	if !overview.GeneratedAt.Equal(testNow) || overview.ScopeVersionID != nil {
		t.Fatalf("overview = %+v", overview)
	}
}

func TestOverviewUsesSourceAndLatestEvalSummary(t *testing.T) {
	versionID := int64(10)
	repo := &fakeRepo{latest: &qualityapp.EvalRun{
		ID: 900, Mode: quality.EvalModeRealLLM, Status: quality.StatusFailed,
		CreatedAt: testNow,
		Metrics: []quality.Metric{
			{Name: "a", Passed: true, Detail: quality.MetricDetail{}},
			{Name: "b", Passed: true, Detail: quality.MetricDetail{}},
			{Name: "c", Passed: false, Detail: quality.MetricDetail{}},
		},
	}}
	source := &fakeSource{snapshot: quality.OperationalSnapshot{Metrics: []quality.OperationalMetric{
		quality.NewOperationalMetric(quality.OperationalRequirementClosureRate, 2, 4),
	}}}
	service := newService(repo, source, nil, nil)

	overview, err := service.Overview(context.Background(), &versionID)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if source.calls != 1 || source.version == nil || *source.version != versionID {
		t.Fatalf("source call = %+v", source)
	}
	if len(overview.Operational) != 1 {
		t.Fatalf("operational = %+v", overview.Operational)
	}
	metric := overview.Operational[0]
	if !metric.Available || metric.Value == nil || *metric.Value != 0.5 {
		t.Fatalf("requirement closure = %+v", metric)
	}
	if overview.LatestEval == nil {
		t.Fatal("latest eval is nil")
	}
	if overview.LatestEval.ID != 900 || overview.LatestEval.Mode != quality.EvalModeRealLLM ||
		overview.LatestEval.Status != quality.StatusFailed {
		t.Fatalf("summary = %+v", overview.LatestEval)
	}
	if overview.LatestEval.MetricsCount != 3 || overview.LatestEval.MetricsPassed != 2 {
		t.Fatalf("summary counts = %+v", overview.LatestEval)
	}
	if overview.ScopeVersionID == nil || *overview.ScopeVersionID != versionID {
		t.Fatalf("scope = %v", overview.ScopeVersionID)
	}
}

func TestOverviewWithNoLatestEvalHasNilSummary(t *testing.T) {
	repo := &fakeRepo{latestErr: quality.ErrNotFound}
	service := newService(repo, nil, nil, nil)
	overview, err := service.Overview(context.Background(), nil)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if overview.LatestEval != nil {
		t.Fatalf("latest eval = %+v", overview.LatestEval)
	}
}

func TestOverviewRejectsNonPositiveVersionID(t *testing.T) {
	service := newService(&fakeRepo{}, nil, nil, nil)
	zero := int64(0)
	_, err := service.Overview(context.Background(), &zero)
	if field := apiError(t, err).Details["field"]; field != "version_id" {
		t.Fatalf("field = %v", field)
	}
}

func TestOverviewPropagatesSourceAndRepositoryErrors(t *testing.T) {
	source := &fakeSource{err: errors.New("provider down")}
	service := newService(&fakeRepo{}, source, nil, nil)
	if _, err := service.Overview(context.Background(), nil); err == nil {
		t.Fatal("source error was swallowed")
	}

	repo := &fakeRepo{latestErr: errors.New("db down")}
	service = newService(repo, nil, nil, nil)
	if _, err := service.Overview(context.Background(), nil); err == nil {
		t.Fatal("latest error was swallowed")
	}
}
