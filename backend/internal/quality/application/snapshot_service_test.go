package qualityapp_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// fakeSnapshotPorts implements the four read-only product-data ports wired by
// WithSnapshotPorts. A non-nil err makes every port fail so the failed-event
// path is observable.
type fakeSnapshotPorts struct {
	closureClosed int
	closureTotal  int

	caseConfirmed int
	caseTotal     int

	execPassed int
	execTotal  int

	visualPassed int
	visualFailed int

	err error

	versionID *int64
}

func (f *fakeSnapshotPorts) CountRequirementClosure(_ context.Context, versionID *int64) (int, int, error) {
	f.versionID = versionID
	return f.closureClosed, f.closureTotal, f.err
}

func (f *fakeSnapshotPorts) CountCaseConfirmation(_ context.Context, versionID *int64) (int, int, error) {
	f.versionID = versionID
	return f.caseConfirmed, f.caseTotal, f.err
}

func (f *fakeSnapshotPorts) CountExecutionPass(_ context.Context, versionID *int64) (int, int, error) {
	f.versionID = versionID
	return f.execPassed, f.execTotal, f.err
}

func (f *fakeSnapshotPorts) VisualClassification(_ context.Context, versionID *int64) (int, int, error) {
	f.versionID = versionID
	return f.visualPassed, f.visualFailed, f.err
}

// fakeVersionExists implements the narrow VersionExistsPort wired by
// WithVersionExistence. The version id is recorded so a test can prove the
// probe is scoped to the requested version.
type fakeVersionExists struct {
	exists    bool
	err       error
	versionID int64
	calls     int
}

func (f *fakeVersionExists) VersionExists(_ context.Context, versionID int64) (bool, error) {
	f.calls++
	f.versionID = versionID
	return f.exists, f.err
}

type fakeEvalEvents struct {
	types   []string
	version []int64
	payload []map[string]any
	err     error
}

func (f *fakeEvalEvents) Publish(_ context.Context, eventType string, versionID int64, payload map[string]any) error {
	f.types = append(f.types, eventType)
	f.version = append(f.version, versionID)
	f.payload = append(f.payload, payload)
	return f.err
}

func (f *fakeEvalEvents) count(eventType string) int {
	total := 0
	for _, item := range f.types {
		if item == eventType {
			total++
		}
	}
	return total
}

func snapshotPorts() *fakeSnapshotPorts {
	return &fakeSnapshotPorts{
		closureClosed: 9, closureTotal: 10,
		caseConfirmed: 8, caseTotal: 10,
		execPassed: 9, execTotal: 10,
		visualPassed: 9, visualFailed: 1,
	}
}

func snapshotService(repo *fakeRepo, ports *fakeSnapshotPorts, events *fakeEvalEvents) *qualityapp.Service {
	service := newService(repo, nil, nil, nil)
	service.WithSnapshotPorts(ports, ports, ports, ports)
	service.WithVersionExistence(&fakeVersionExists{exists: true})
	if events != nil {
		service.WithEvents(events)
	}
	return service
}

func snapshotMetric(t *testing.T, run qualityapp.EvalRun, name string) quality.Metric {
	t.Helper()
	for _, metric := range run.Metrics {
		if metric.Name == name {
			return metric
		}
	}
	t.Fatalf("missing metric %q in %+v", name, run.Metrics)
	return quality.Metric{}
}

// TestSnapshotTriggerMetricsStatusAndEvents is the W2-14 behavioural contract:
// one admin trigger derives a mode=snapshot run scoped to the version, persists
// four metrics and publishes eval.started before eval.completed.
func TestSnapshotTriggerMetricsStatusAndEvents(t *testing.T) {
	repo := &fakeRepo{}
	events := &fakeEvalEvents{}
	ports := snapshotPorts()
	service := snapshotService(repo, ports, events)

	run, err := service.TriggerSnapshot(context.Background(), platform.Actor{ID: 9, Role: "admin"}, 7)
	if err != nil {
		t.Fatalf("TriggerSnapshot: %v", err)
	}
	if run.Mode != quality.EvalModeSnapshot {
		t.Fatalf("mode = %q", run.Mode)
	}
	if run.VersionID == nil || *run.VersionID != 7 {
		t.Fatalf("version_id = %v", run.VersionID)
	}
	if run.Status != quality.StatusPassed {
		t.Fatalf("status = %q", run.Status)
	}
	if len(run.Metrics) != 4 || len(repo.insertedMetrics) != 4 {
		t.Fatalf("metrics = %d/%d", len(run.Metrics), len(repo.insertedMetrics))
	}
	for _, name := range []string{
		quality.MetricDesignCoverage,
		quality.MetricExecutionPassRate,
		quality.MetricCaseCoverage,
		quality.MetricVisualClassificationAccuracy,
	} {
		snapshotMetric(t, run, name)
	}
	if repo.insertCalls != 1 || repo.insertedRun.VersionID == nil || *repo.insertedRun.VersionID != 7 {
		t.Fatalf("inserted run = %+v calls=%d", repo.insertedRun, repo.insertCalls)
	}
	if ports.versionID == nil || *ports.versionID != 7 {
		t.Fatalf("ports scoped to %v", ports.versionID)
	}

	if len(events.types) != 2 || events.types[0] != qualityapp.EventEvalStarted || events.types[1] != qualityapp.EventEvalCompleted {
		t.Fatalf("events = %v", events.types)
	}
	for index, payload := range events.payload {
		if payload["eval_run_id"] != "101" || payload["version_id"] != "7" || payload["mode"] != quality.EvalModeSnapshot {
			t.Fatalf("event %d payload = %+v", index, payload)
		}
	}
	if events.payload[0]["status"] != quality.StatusRunning {
		t.Fatalf("started status = %v", events.payload[0]["status"])
	}
	if events.payload[1]["status"] != quality.StatusPassed {
		t.Fatalf("completed status = %v", events.payload[1]["status"])
	}
	if events.version[0] != 7 || events.version[1] != 7 {
		t.Fatalf("event versions = %v", events.version)
	}
}

// TestSnapshotTriggerFailurePublishesFailedEvent proves a read-port failure is
// observable: no run is persisted and eval.failed carries the error.
func TestSnapshotTriggerFailurePublishesFailedEvent(t *testing.T) {
	repo := &fakeRepo{}
	events := &fakeEvalEvents{}
	ports := snapshotPorts()
	ports.err = errors.New("version read failed")
	service := snapshotService(repo, ports, events)

	_, err := service.TriggerSnapshot(context.Background(), platform.Actor{ID: 9, Role: "admin"}, 7)
	if err == nil || !strings.Contains(err.Error(), "version read failed") {
		t.Fatalf("error = %v", err)
	}
	if repo.insertCalls != 0 {
		t.Fatalf("insert calls = %d", repo.insertCalls)
	}
	if events.count(qualityapp.EventEvalFailed) != 1 || events.count(qualityapp.EventEvalCompleted) != 0 {
		t.Fatalf("events = %v", events.types)
	}
	failed := events.payload[len(events.payload)-1]
	if failed["error"] == nil || failed["status"] != quality.StatusFailed {
		t.Fatalf("failed payload = %+v", failed)
	}
}

// TestSnapshotTriggerRequiresPorts: an unwired snapshot surface must fail
// honestly instead of fabricating metrics.
func TestSnapshotTriggerRequiresPorts(t *testing.T) {
	service := newService(&fakeRepo{}, nil, nil, nil)
	_, err := service.TriggerSnapshot(context.Background(), platform.Actor{ID: 9, Role: "admin"}, 7)
	apiErr := apiError(t, err)
	if apiErr.Status != 503 {
		t.Fatalf("status = %d", apiErr.Status)
	}
}

func TestSnapshotTriggerRejectsInvalidVersion(t *testing.T) {
	service := snapshotService(&fakeRepo{}, snapshotPorts(), nil)
	_, err := service.TriggerSnapshot(context.Background(), platform.Actor{ID: 9, Role: "admin"}, 0)
	apiErr := apiError(t, err)
	if apiErr.Status != 400 || apiErr.Details["field"] != "version_id" {
		t.Fatalf("error = %+v", apiErr)
	}
}

// TestSnapshotTriggerUnknownVersionIsNotFound proves the trigger probes the
// narrow version-existence port before publishing eval.started or inserting a
// run: an unknown version is a 404, not an all-zero failed snapshot.
func TestSnapshotTriggerUnknownVersionIsNotFound(t *testing.T) {
	repo := &fakeRepo{}
	events := &fakeEvalEvents{}
	existence := &fakeVersionExists{exists: false}
	service := snapshotService(repo, snapshotPorts(), events)
	service.WithVersionExistence(existence)

	_, err := service.TriggerSnapshot(context.Background(), platform.Actor{ID: 9, Role: "admin"}, 77)
	apiErr := apiError(t, err)
	if apiErr.Status != 404 || apiErr.Details["kind"] != "version" {
		t.Fatalf("error = %+v", apiErr)
	}
	if existence.versionID != 77 || existence.calls != 1 {
		t.Fatalf("existence probe = %+v", existence)
	}
	if repo.insertCalls != 0 {
		t.Fatalf("unknown version persisted %d run(s)", repo.insertCalls)
	}
	if len(events.types) != 0 {
		t.Fatalf("unknown version published events: %v", events.types)
	}
}

// TestSnapshotTriggerVersionProbeFailureIsReported proves a failing existence
// probe aborts before any side effect and surfaces the read failure.
func TestSnapshotTriggerVersionProbeFailureIsReported(t *testing.T) {
	boom := errors.New("version existence read failed")
	repo := &fakeRepo{}
	events := &fakeEvalEvents{}
	service := snapshotService(repo, snapshotPorts(), events)
	service.WithVersionExistence(&fakeVersionExists{err: boom})

	_, err := service.TriggerSnapshot(context.Background(), platform.Actor{ID: 9, Role: "admin"}, 7)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want wrapped probe failure", err)
	}
	if repo.insertCalls != 0 || len(events.types) != 0 {
		t.Fatalf("probe failure had side effects: inserts=%d events=%v", repo.insertCalls, events.types)
	}
}

// TestSnapshotTriggerPreviousSnapshotIsWarnOnlyBaseline: the prior snapshot of
// the same version is the baseline; a regression marks metrics and the run
// failed but the trigger still succeeds and persists the full series.
func TestSnapshotTriggerPreviousSnapshotIsWarnOnlyBaseline(t *testing.T) {
	repo := &fakeRepo{}
	previousBaseline := 0.99
	repo.latest = &qualityapp.EvalRun{
		ID:     55,
		Mode:   quality.EvalModeSnapshot,
		Status: quality.StatusPassed,
		Metrics: []quality.Metric{
			{Name: quality.MetricDesignCoverage, Value: previousBaseline},
			{Name: quality.MetricExecutionPassRate, Value: previousBaseline},
			{Name: quality.MetricCaseCoverage, Value: previousBaseline},
			{Name: quality.MetricVisualClassificationAccuracy, Value: previousBaseline},
		},
	}
	service := snapshotService(repo, snapshotPorts(), nil)

	run, err := service.TriggerSnapshot(context.Background(), platform.Actor{ID: 9, Role: "admin"}, 7)
	if err != nil {
		t.Fatalf("TriggerSnapshot: %v", err)
	}
	if repo.insertCalls != 1 {
		t.Fatalf("regression blocked the trigger (inserts=%d)", repo.insertCalls)
	}
	if run.Status != quality.StatusFailed {
		t.Fatalf("status = %q, want failed", run.Status)
	}
	design := snapshotMetric(t, run, quality.MetricDesignCoverage)
	if !design.Detail.Regressed || design.Detail.Baseline == nil ||
		math.Abs(*design.Detail.Baseline-previousBaseline) > 1e-9 {
		t.Fatalf("design metric = %+v", design)
	}
}

// TestServiceTrendsScopesVersionAndMetric: the service validates the scope and
// forwards the exact version/metric/bounds to the repository.
func TestServiceTrendsScopesVersionAndMetric(t *testing.T) {
	repo := &fakeRepo{trends: []qualityapp.TrendPoint{
		{RunID: 101, Metric: quality.MetricExecutionPassRate, Value: 0.9},
		{RunID: 102, Metric: quality.MetricExecutionPassRate, Value: 0.5},
	}}
	service := newService(repo, nil, nil, nil)
	service.WithVersionExistence(&fakeVersionExists{exists: true})
	from := testNow.Add(-time.Hour)
	to := testNow

	points, err := service.Trends(context.Background(), 7, quality.MetricExecutionPassRate, from, to)
	if err != nil {
		t.Fatalf("Trends: %v", err)
	}
	if len(points) != 2 || points[0].RunID != 101 {
		t.Fatalf("points = %+v", points)
	}
}

func TestServiceTrendsRejectsUnknownMetricAndVersion(t *testing.T) {
	service := newService(&fakeRepo{}, nil, nil, nil)
	_, err := service.Trends(context.Background(), 7, "nope", time.Time{}, time.Time{})
	if apiErr := apiError(t, err); apiErr.Status != 400 || apiErr.Details["field"] != "metric" {
		t.Fatalf("metric error = %+v", apiErr)
	}
	_, err = service.Trends(context.Background(), 0, quality.MetricDesignCoverage, time.Time{}, time.Time{})
	if apiErr := apiError(t, err); apiErr.Status != 400 || apiErr.Details["field"] != "version_id" {
		t.Fatalf("version error = %+v", apiErr)
	}
}

func TestServiceTrendsReportsRepositoryFailure(t *testing.T) {
	boom := errors.New("trends read failed")
	service := newService(&fakeRepo{trendsErr: boom}, nil, nil, nil)
	service.WithVersionExistence(&fakeVersionExists{exists: true})
	_, err := service.Trends(context.Background(), 7, quality.MetricDesignCoverage, time.Time{}, time.Time{})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
}

// TestServiceTrendsUnknownVersionIsNotFound proves the trend read validates the
// version through the narrow existence port before querying the repository, so
// an unknown version is a 404 instead of an empty 200 series.
func TestServiceTrendsUnknownVersionIsNotFound(t *testing.T) {
	existence := &fakeVersionExists{exists: false}
	service := newService(&fakeRepo{}, nil, nil, nil)
	service.WithVersionExistence(existence)

	_, err := service.Trends(context.Background(), 77, quality.MetricDesignCoverage, time.Time{}, time.Time{})
	apiErr := apiError(t, err)
	if apiErr.Status != 404 || apiErr.Details["kind"] != "version" {
		t.Fatalf("error = %+v", apiErr)
	}
	if existence.versionID != 77 || existence.calls != 1 {
		t.Fatalf("existence probe = %+v", existence)
	}
}
