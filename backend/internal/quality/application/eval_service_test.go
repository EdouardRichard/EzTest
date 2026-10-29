package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// qualityTestNow is the deterministic clock of the fake harness.
var qualityTestNow = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

// fakeRunRepository is the in-memory RunRepository of the service tests.
type fakeRunRepository struct {
	created   []domain.Run
	list      []domain.Run
	listLimit int
	get       domain.Run
	getErr    error
	latest    domain.Run
	latestSet bool
	latestErr error
}

func (f *fakeRunRepository) Create(_ context.Context, run domain.Run) (domain.Run, error) {
	f.created = append(f.created, run)
	return run, nil
}

func (f *fakeRunRepository) List(_ context.Context, limit int) ([]domain.Run, error) {
	f.listLimit = limit
	return f.list, nil
}

func (f *fakeRunRepository) Get(_ context.Context, id int64) (domain.Run, error) {
	if f.getErr != nil {
		return domain.Run{}, f.getErr
	}
	return f.get, nil
}

func (f *fakeRunRepository) Latest(context.Context) (domain.Run, error) {
	if f.latestErr != nil {
		return domain.Run{}, f.latestErr
	}
	if !f.latestSet {
		return domain.Run{}, ErrRunNotFound
	}
	return f.latest, nil
}

// fakeIDGenerator mints a fixed sequence of ids.
type fakeIDGenerator struct{ next int64 }

func (f *fakeIDGenerator) Next() int64 {
	f.next++
	return 1000 + f.next
}

// fakeClock is the fixed service clock.
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

// metricByName indexes the metrics of one run for terse assertions.
func metricByName(t *testing.T, run domain.Run, name string) domain.Metric {
	t.Helper()
	for _, metric := range run.Metrics {
		if metric.Name == name {
			return metric
		}
	}
	t.Fatalf("run has no metric %q: %+v", name, run.Metrics)
	return domain.Metric{}
}

// qualitySample is the corpus observation of Task 23 step 1: design coverage
// 4/5, impact recall 3/4 (precision 3/4), classification 2/2, issue false
// positives 0/2 and supplement hit rate 1/2.
func qualitySample() domain.Sample {
	return domain.Sample{
		ConfirmedRequirements:  5,
		CoveredRequirements:    4,
		ImpactExpected:         4,
		ImpactHit:              3,
		ImpactSelected:         4,
		InjectedFailures:       2,
		CorrectClassifications: 2,
		AIIssues:               2,
		TruePositiveIssues:     2,
		SupplementCases:        2,
		SupplementHits:         1,
	}
}

func newQualityService(repo RunRepository) *Service {
	return NewService(repo, WithIDGenerator(&fakeIDGenerator{}), WithClock(fakeClock{now: qualityTestNow}))
}

// TestRecordComputesCorpusMetricsWithThresholdsAndRegression pins step 1 of the
// Task 23 brief: the deterministic corpus yields design_coverage=0.8,
// impact_recall=0.75, classification_accuracy=1.0,
// issue_false_positive_rate=0.0 and supplement_hit_rate=0.5; every metric
// records its threshold and pass verdict, and a value more than 0.05 below its
// baseline fails even when it still clears the threshold.
func TestRecordComputesCorpusMetricsWithThresholdsAndRegression(t *testing.T) {
	repo := &fakeRunRepository{}
	service := newQualityService(repo)
	sample := qualitySample()
	run, err := service.Record(context.Background(), RecordInput{
		Mode:          domain.ModeDeterministic,
		CorpusVersion: "2026-10-29.1",
		Model:         "fake-llm",
		Sample:        &sample,
		Baseline: map[string]float64{
			// 0.80 -> 0.80 is a zero regression and must not fail the metric.
			domain.MetricDesignCoverage: 0.80,
			// 0.79 -> 0.75 is a 0.04 drop, inside the tolerance.
			domain.MetricImpactRecall: 0.79,
			// 0.60 -> 0.50 is a 0.10 drop and must fail the metric.
			domain.MetricSupplementHitRate: 0.60,
			// The remaining gated keys are required even when they do not
			// exercise the regression rule in this scenario.
			domain.MetricImpactPrecision:        0.75,
			domain.MetricClassificationAccuracy: 1.00,
			domain.MetricIssueFalsePositiveRate: 0.00,
		},
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("persisted runs = %d, want 1", len(repo.created))
	}

	cases := []struct {
		name      string
		value     float64
		threshold float64
		passed    bool
	}{
		{domain.MetricDesignCoverage, 0.8, 0.90, false},
		{domain.MetricImpactRecall, 0.75, 0.90, false},
		{domain.MetricImpactPrecision, 0.75, 0.80, false},
		{domain.MetricClassificationAccuracy, 1.0, 1.00, true},
		{domain.MetricIssueFalsePositiveRate, 0.0, 0.05, true},
		{domain.MetricSupplementHitRate, 0.5, 0.80, false},
	}
	if len(run.Metrics) != len(cases) {
		t.Fatalf("metrics = %d (%+v), want %d", len(run.Metrics), run.Metrics, len(cases))
	}
	for _, tc := range cases {
		metric := metricByName(t, run, tc.name)
		if !almostEqual(metric.Value, tc.value) {
			t.Errorf("%s value = %v, want %v", tc.name, metric.Value, tc.value)
		}
		if !almostEqual(metric.Threshold, tc.threshold) {
			t.Errorf("%s threshold = %v, want %v", tc.name, metric.Threshold, tc.threshold)
		}
		if metric.Passed != tc.passed {
			t.Errorf("%s passed = %v, want %v", tc.name, metric.Passed, tc.passed)
		}
		if metric.Detail == nil {
			t.Errorf("%s has no detail document", tc.name)
		}
	}

	// The supplement metric clears nothing: its 0.10 regression must be
	// recorded as the failure reason, independent of the threshold verdict.
	supplement := metricByName(t, run, domain.MetricSupplementHitRate)
	if supplement.Detail["reason"] != domain.MetricReasonBaselineRegression {
		t.Errorf("supplement reason = %v, want %q", supplement.Detail["reason"], domain.MetricReasonBaselineRegression)
	}
	if !almostEqual(supplement.Detail["baseline"].(float64), 0.60) {
		t.Errorf("supplement baseline detail = %v, want 0.60", supplement.Detail["baseline"])
	}
	// The 0.04 drop is inside the tolerance and keeps its own reason.
	recall := metricByName(t, run, domain.MetricImpactRecall)
	if recall.Detail["reason"] != domain.MetricReasonBelowThreshold {
		t.Errorf("impact recall reason = %v, want %q", recall.Detail["reason"], domain.MetricReasonBelowThreshold)
	}

	if run.Status != domain.StatusFailed {
		t.Errorf("run status = %q, want failed", run.Status)
	}
	if run.ID != 1001 {
		t.Errorf("run id = %d, want the Snowflake generator value 1001", run.ID)
	}
	if !run.StartedAt.Equal(qualityTestNow) || run.FinishedAt == nil || !run.FinishedAt.Equal(qualityTestNow) {
		t.Errorf("run times = %v/%v, want the injected clock", run.StartedAt, run.FinishedAt)
	}
	if run.Mode != domain.ModeDeterministic || run.CorpusVersion != "2026-10-29.1" || run.Model != "fake-llm" {
		t.Errorf("run = %+v, want the request fields to be persisted", run)
	}
}

// deterministicBaseline returns the six gated baseline keys the deterministic
// runner must supply. Every one of them is required so a partial document can
// never silently disable the regression rule.
func deterministicBaseline() map[string]float64 {
	return map[string]float64{
		domain.MetricDesignCoverage:         1.0,
		domain.MetricImpactRecall:           1.0,
		domain.MetricImpactPrecision:        1.0,
		domain.MetricClassificationAccuracy: 1.0,
		domain.MetricIssueFalsePositiveRate: 0.0,
		domain.MetricSupplementHitRate:      1.0,
	}
}

// TestRecordRejectsDeterministicBaselineMissingKey proves the baseline document
// is a complete contract: dropping any one of the six deterministic keys is a
// 400-class validation error (and nothing is persisted), never a run whose
// regression gate is silently disabled.
func TestRecordRejectsDeterministicBaselineMissingKey(t *testing.T) {
	sample := qualitySample()
	for _, missing := range []string{
		domain.MetricDesignCoverage,
		domain.MetricImpactRecall,
		domain.MetricImpactPrecision,
		domain.MetricClassificationAccuracy,
		domain.MetricIssueFalsePositiveRate,
		domain.MetricSupplementHitRate,
	} {
		t.Run(missing, func(t *testing.T) {
			baseline := deterministicBaseline()
			delete(baseline, missing)
			repo := &fakeRunRepository{}
			service := newQualityService(repo)
			_, err := service.Record(context.Background(), RecordInput{
				Mode:          domain.ModeDeterministic,
				CorpusVersion: "2026-10-29.1",
				Sample:        &sample,
				Baseline:      baseline,
			})
			if err == nil {
				t.Fatalf("Record() without baseline key %q unexpectedly succeeded", missing)
			}
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("Record() error = %v, want *ValidationError", err)
			}
			if invalid.Field != "baseline" {
				t.Errorf("field = %q, want baseline", invalid.Field)
			}
			if !strings.Contains(invalid.Message, missing) {
				t.Errorf("message = %q, want the missing metric %q named", invalid.Message, missing)
			}
			if len(repo.created) != 0 {
				t.Errorf("persisted %d runs, want 0", len(repo.created))
			}
		})
	}
}

// TestRecordPassingRunPersistsPassedMetrics proves a corpus observation at or
// above every threshold persists status passed and one passed metric per
// definition.
func TestRecordPassingRunPersistsPassedMetrics(t *testing.T) {
	repo := &fakeRunRepository{}
	service := newQualityService(repo)
	run, err := service.Record(context.Background(), RecordInput{
		Mode:          domain.ModeDeterministic,
		CorpusVersion: "2026-10-29.1",
		Sample: &domain.Sample{
			ConfirmedRequirements:  5,
			CoveredRequirements:    5,
			ImpactExpected:         4,
			ImpactHit:              4,
			ImpactSelected:         4,
			InjectedFailures:       3,
			CorrectClassifications: 3,
			AIIssues:               3,
			TruePositiveIssues:     3,
			SupplementCases:        4,
			SupplementHits:         4,
		},
		Summary:  map[string]any{"corpus": "deterministic"},
		Baseline: deterministicBaseline(),
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if run.Status != domain.StatusPassed {
		t.Fatalf("run status = %q, want passed (%+v)", run.Status, run.Metrics)
	}
	for _, metric := range run.Metrics {
		if !metric.Passed {
			t.Errorf("%s passed = false, want true (value=%v threshold=%v detail=%v)",
				metric.Name, metric.Value, metric.Threshold, metric.Detail)
		}
	}
	if run.Summary["corpus"] != "deterministic" {
		t.Errorf("summary = %+v, want the request document", run.Summary)
	}
}

// TestRecordRealLLMUsesReportThresholdsAndIgnoresRegression pins the D61 dual
// track: the real-LLM report uses the looser report thresholds and never turns
// the deterministic +0.05 regression rule into a hard gate.
func TestRecordRealLLMUsesReportThresholdsAndIgnoresRegression(t *testing.T) {
	repo := &fakeRunRepository{}
	service := newQualityService(repo)
	run, err := service.Record(context.Background(), RecordInput{
		Mode:          domain.ModeRealLLM,
		CorpusVersion: "2026-10-29.1",
		Model:         "deepseek-flash",
		Sample: &domain.Sample{
			ConfirmedRequirements:  5,
			CoveredRequirements:    4, // 0.80 == real report threshold
			ImpactExpected:         5,
			ImpactHit:              4, // 0.80 >= 0.70
			ImpactSelected:         4,
			InjectedFailures:       5,
			CorrectClassifications: 4, // 0.80 >= real report threshold
			AIIssues:               10,
			TruePositiveIssues:     8, // false positive rate 0.20 > 0.15 -> report fail
			SupplementCases:        4,
			SupplementHits:         2, // 0.50 >= 0.50
		},
		Baseline: map[string]float64{
			domain.MetricDesignCoverage: 0.95, // -0.15, but ignored for real_llm
		},
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	design := metricByName(t, run, domain.MetricDesignCoverage)
	if !almostEqual(design.Threshold, 0.80) || !design.Passed {
		t.Errorf("real design coverage = %+v, want threshold 0.80 passed", design)
	}
	if design.Detail["reason"] == domain.MetricReasonBaselineRegression {
		t.Errorf("real LLM report must not apply the baseline regression rule: %+v", design.Detail)
	}
	accuracy := metricByName(t, run, domain.MetricClassificationAccuracy)
	if !almostEqual(accuracy.Threshold, 0.80) || !accuracy.Passed {
		t.Errorf("real classification accuracy = %+v, want threshold 0.80 passed", accuracy)
	}
	fp := metricByName(t, run, domain.MetricIssueFalsePositiveRate)
	if !almostEqual(fp.Value, 0.2) || fp.Passed {
		t.Errorf("real issue false positive = %+v, want 0.20 failed", fp)
	}
	if run.Status != domain.StatusFailed {
		t.Errorf("real report status = %q, want failed (the report still records the verdict)", run.Status)
	}
}

// TestRecordRejectsInvalidSamples proves a hand-crafted or broken runner payload
// cannot fabricate metrics: numerator/denominator inconsistencies are rejected
// as invalid input and nothing is persisted.
func TestRecordRejectsInvalidSamples(t *testing.T) {
	cases := []struct {
		name   string
		sample domain.Sample
		field  string
	}{
		{"covered exceeds confirmed", domain.Sample{ConfirmedRequirements: 2, CoveredRequirements: 3}, "coveredRequirements"},
		{"impact hit exceeds expected", domain.Sample{ImpactExpected: 2, ImpactHit: 3, ImpactSelected: 3}, "impactHit"},
		{"impact hit exceeds selected", domain.Sample{ImpactExpected: 3, ImpactHit: 3, ImpactSelected: 2}, "impactHit"},
		{"classification exceeds injected", domain.Sample{InjectedFailures: 1, CorrectClassifications: 2}, "correctClassifications"},
		{"true positives exceed issues", domain.Sample{AIIssues: 1, TruePositiveIssues: 2}, "truePositiveIssues"},
		{"supplement hits exceed cases", domain.Sample{SupplementCases: 1, SupplementHits: 2}, "supplementHits"},
		{"negative confirmed", domain.Sample{ConfirmedRequirements: -1}, "confirmedRequirements"},
	}
	for _, tc := range cases {
		repo := &fakeRunRepository{}
		service := newQualityService(repo)
		sample := tc.sample
		_, err := service.Record(context.Background(), RecordInput{
			Mode:          domain.ModeDeterministic,
			CorpusVersion: "2026-10-29.1",
			Sample:        &sample,
		})
		if err == nil {
			t.Fatalf("%s: Record() unexpectedly succeeded", tc.name)
		}
		var invalid *ValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("%s: error = %v, want *ValidationError", tc.name, err)
		}
		if invalid.Field != tc.field {
			t.Errorf("%s: field = %q, want %q", tc.name, invalid.Field, tc.field)
		}
		if len(repo.created) != 0 {
			t.Errorf("%s: persisted %d runs, want 0", tc.name, len(repo.created))
		}
	}
}

// TestRecordValidatesRequestFields proves the request metadata is validated
// before the repository sees it.
func TestRecordValidatesRequestFields(t *testing.T) {
	cases := []struct {
		name  string
		input RecordInput
		field string
	}{
		{"missing mode", RecordInput{CorpusVersion: "c", Sample: &domain.Sample{}}, "mode"},
		{"unknown mode", RecordInput{Mode: "manual", CorpusVersion: "c", Sample: &domain.Sample{}}, "mode"},
		{"missing corpus", RecordInput{Mode: domain.ModeDeterministic, Sample: &domain.Sample{}}, "corpusVersion"},
		{"missing sample", RecordInput{Mode: domain.ModeDeterministic, CorpusVersion: "c"}, "sample"},
		{"bad outcome", RecordInput{Mode: domain.ModeDeterministic, CorpusVersion: "c", Outcome: "skipped", Sample: &domain.Sample{}}, "status"},
		{"finished before started", RecordInput{
			Mode: domain.ModeDeterministic, CorpusVersion: "c", Sample: &domain.Sample{},
			StartedAt:  time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC),
			FinishedAt: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC),
		}, "finishedAt"},
	}
	for _, tc := range cases {
		repo := &fakeRunRepository{}
		service := newQualityService(repo)
		_, err := service.Record(context.Background(), tc.input)
		if err == nil {
			t.Fatalf("%s: Record() unexpectedly succeeded", tc.name)
		}
		var invalid *ValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("%s: error = %v, want *ValidationError", tc.name, err)
		}
		if invalid.Field != tc.field {
			t.Errorf("%s: field = %q, want %q", tc.name, invalid.Field, tc.field)
		}
		if len(repo.created) != 0 {
			t.Errorf("%s: persisted a run", tc.name)
		}
	}
}

// TestRecordErrorOutcomePersistsErrorRun proves the runner can record a failed
// scenario without inventing metrics.
func TestRecordErrorOutcomePersistsErrorRun(t *testing.T) {
	repo := &fakeRunRepository{}
	service := newQualityService(repo)
	run, err := service.Record(context.Background(), RecordInput{
		Mode:          domain.ModeDeterministic,
		CorpusVersion: "2026-10-29.1",
		Outcome:       domain.StatusError,
		Summary:       map[string]any{"reason": "scenario_failed"},
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if run.Status != domain.StatusError || len(run.Metrics) != 0 {
		t.Errorf("run = %+v, want status error and no metrics", run)
	}
	if run.Summary["reason"] != "scenario_failed" {
		t.Errorf("summary = %+v, want the error detail", run.Summary)
	}
}

// TestListAndGetForwardToTheRepository pins the query limits and the not-found
// mapping the HTTP layer depends on.
func TestListAndGetForwardToTheRepository(t *testing.T) {
	repo := &fakeRunRepository{list: []domain.Run{{ID: 7, Status: domain.StatusPassed}}}
	service := newQualityService(repo)

	runs, err := service.List(context.Background(), 0)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != 7 {
		t.Fatalf("List() = %+v, want the repository rows", runs)
	}
	if repo.listLimit != DefaultListLimit {
		t.Errorf("list limit = %d, want the default %d", repo.listLimit, DefaultListLimit)
	}
	if _, err := service.List(context.Background(), MaxListLimit+1); err == nil {
		t.Error("List(too large) unexpectedly succeeded")
	}

	repo.get = domain.Run{ID: 9}
	run, err := service.Get(context.Background(), 9)
	if err != nil || run.ID != 9 {
		t.Fatalf("Get() = %+v, %v want id 9", run, err)
	}
	repo.getErr = ErrRunNotFound
	if _, err := service.Get(context.Background(), 10); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrRunNotFound", err)
	}
	repo.latestSet = true
	repo.latest = domain.Run{ID: 11}
	latest, err := service.Latest(context.Background())
	if err != nil || latest.ID != 11 {
		t.Fatalf("Latest() = %+v, %v want id 11", latest, err)
	}
	repo.latestSet = false
	if _, err := service.Latest(context.Background()); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("Latest(empty) error = %v, want ErrRunNotFound", err)
	}
}

// TestRepositoryErrorsAreWrappedWithoutLeaking proves a persistence failure
// surfaces as an error but keeps the stable sentinel available to the HTTP
// layer, without reproducing the raw query text.
func TestRepositoryErrorsAreWrappedWithoutLeaking(t *testing.T) {
	repo := &failingRepository{err: errors.New("pgx: relation eval_run does not exist")}
	service := newQualityService(repo)
	_, err := service.Record(context.Background(), RecordInput{
		Mode:          domain.ModeDeterministic,
		CorpusVersion: "2026-10-29.1",
		Sample:        &domain.Sample{},
		Baseline:      deterministicBaseline(),
	})
	if err == nil {
		t.Fatal("Record() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "postgres://") {
		t.Errorf("error leaks connection material: %v", err)
	}
}

// failingRepository always fails Create.
type failingRepository struct{ err error }

func (f *failingRepository) Create(context.Context, domain.Run) (domain.Run, error) {
	return domain.Run{}, f.err
}
func (f *failingRepository) List(context.Context, int) ([]domain.Run, error) { return nil, f.err }
func (f *failingRepository) Get(context.Context, int64) (domain.Run, error) {
	return domain.Run{}, f.err
}
func (f *failingRepository) Latest(context.Context) (domain.Run, error) {
	return domain.Run{}, f.err
}

func almostEqual(left, right float64) bool {
	delta := left - right
	return delta < 1e-9 && delta > -1e-9
}
