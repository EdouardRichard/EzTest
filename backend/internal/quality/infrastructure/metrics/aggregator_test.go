package metrics_test

import (
	"context"
	"errors"
	"math"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
	qualitymetrics "github.com/EdouardRichard/EzTest/backend/internal/quality/infrastructure/metrics"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// Task 22 wiring contract: the concrete cross-context query services must
// satisfy the narrow ports without adapters.
var (
	_ qualitymetrics.VersionPort   = (*versionapp.QueryService)(nil)
	_ qualitymetrics.DesignPort    = (*designapp.QueryService)(nil)
	_ qualitymetrics.ExecutionPort = (*executionapp.QueryService)(nil)
	_ qualitymetrics.DeliveryPort  = (*deliveryapp.QueryService)(nil)
	_ qualitymetrics.AgentPort     = (*agentapp.QueryService)(nil)
	_ qualitymetrics.EventsPort    = (*eventingapp.QueryService)(nil)
)

type fakeVersion struct {
	num, denom int
	err        error
	calls      int
	versionID  *int64
}

func (f *fakeVersion) CountRequirementClosure(_ context.Context, versionID *int64) (int, int, error) {
	f.calls++
	f.versionID = versionID
	return f.num, f.denom, f.err
}

type fakeDesign struct {
	num, denom int
	err        error
	calls      int
	versionID  *int64
}

func (f *fakeDesign) CountCaseConfirmation(_ context.Context, versionID *int64) (int, int, error) {
	f.calls++
	f.versionID = versionID
	return f.num, f.denom, f.err
}

type fakeExecution struct {
	passNum, passDenom int
	evNum, evDenom     int
	err                error
	passCalls          int
	evCalls            int
	versionID          *int64
}

func (f *fakeExecution) CountExecutionPass(_ context.Context, versionID *int64) (int, int, error) {
	f.passCalls++
	f.versionID = versionID
	return f.passNum, f.passDenom, f.err
}

func (f *fakeExecution) CountEvidenceCompleteness(_ context.Context, versionID *int64) (int, int, error) {
	f.evCalls++
	f.versionID = versionID
	return f.evNum, f.evDenom, f.err
}

type fakeDelivery struct {
	num, denom int
	err        error
	calls      int
	versionID  *int64
}

func (f *fakeDelivery) CountIssueValidity(_ context.Context, versionID *int64) (int, int, error) {
	f.calls++
	f.versionID = versionID
	return f.num, f.denom, f.err
}

type fakeAgent struct {
	num, denom int
	err        error
	calls      int
	versionID  *int64
}

func (f *fakeAgent) CountTaskRetries(_ context.Context, versionID *int64) (int, int, error) {
	f.calls++
	f.versionID = versionID
	return f.num, f.denom, f.err
}

type fakeEvents struct {
	count     int
	err       error
	calls     int
	eventType string
	channel   string
}

func (f *fakeEvents) CountEvents(_ context.Context, eventType, channel string) (int, error) {
	f.calls++
	f.eventType = eventType
	f.channel = channel
	return f.count, f.err
}

type fixture struct {
	version   *fakeVersion
	design    *fakeDesign
	execution *fakeExecution
	delivery  *fakeDelivery
	agent     *fakeAgent
	events    *fakeEvents
}

func newFixture() *fixture {
	return &fixture{
		version:   &fakeVersion{num: 3, denom: 5},
		design:    &fakeDesign{num: 4, denom: 8},
		execution: &fakeExecution{passNum: 6, passDenom: 10, evNum: 7, evDenom: 10},
		delivery:  &fakeDelivery{num: 2, denom: 4},
		agent:     &fakeAgent{num: 1, denom: 4},
		events:    &fakeEvents{count: 12},
	}
}

func (f *fixture) aggregator() *qualitymetrics.Aggregator {
	return qualitymetrics.NewAggregator(f.version, f.design, f.execution, f.delivery, f.agent, f.events)
}

func metricMap(t *testing.T, snapshot quality.OperationalSnapshot) map[string]quality.OperationalMetric {
	t.Helper()
	metrics := make(map[string]quality.OperationalMetric, len(snapshot.Metrics))
	for _, metric := range snapshot.Metrics {
		metrics[metric.Name] = metric
	}
	return metrics
}

func TestSnapshotReturnsSevenOperationalMetrics(t *testing.T) {
	fixture := newFixture()
	versionID := int64(10)
	snapshot, err := fixture.aggregator().Snapshot(context.Background(), &versionID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Metrics) != 7 {
		t.Fatalf("metrics = %d, want 7", len(snapshot.Metrics))
	}
	for index, name := range quality.OperationalMetricOrder {
		if snapshot.Metrics[index].Name != name {
			t.Fatalf("metric[%d] = %q, want %q", index, snapshot.Metrics[index].Name, name)
		}
	}

	metrics := metricMap(t, snapshot)
	cases := []struct {
		name  string
		num   int
		den   int
		value float64
	}{
		{quality.OperationalRequirementClosureRate, 3, 5, 0.6},
		{quality.OperationalCaseConfirmationRate, 4, 8, 0.5},
		{quality.OperationalExecutionPassRate, 6, 10, 0.6},
		{quality.OperationalIssueValidityRate, 2, 4, 0.5},
		{quality.OperationalEvidenceCompletenessRate, 7, 10, 0.7},
		{quality.OperationalTaskFailureRetryRate, 1, 4, 0.25},
	}
	for _, tc := range cases {
		metric := metrics[tc.name]
		if metric.Numerator != tc.num || metric.Denominator != tc.den {
			t.Fatalf("%s counts = %d/%d", tc.name, metric.Numerator, metric.Denominator)
		}
		if !metric.Available || metric.Value == nil || math.Abs(*metric.Value-tc.value) > 1e-9 {
			t.Fatalf("%s value = %+v", tc.name, metric.Value)
		}
		if metric.Definition == "" {
			t.Fatalf("%s has no definition", tc.name)
		}
	}

	// context_compaction_count is a count, not a ratio: denominator 1 keeps it
	// available even when the real count is zero.
	compaction := metrics[quality.OperationalContextCompactionCount]
	if !compaction.Available || compaction.Value == nil || *compaction.Value != 12 ||
		compaction.Numerator != 12 || compaction.Denominator != 1 {
		t.Fatalf("compaction metric = %+v", compaction)
	}

	// The version scope is forwarded to every ratio port and becomes the event
	// channel for the compaction count.
	if fixture.version.versionID == nil || *fixture.version.versionID != 10 ||
		fixture.design.versionID == nil || *fixture.design.versionID != 10 ||
		fixture.execution.versionID == nil || *fixture.execution.versionID != 10 ||
		fixture.delivery.versionID == nil || *fixture.delivery.versionID != 10 ||
		fixture.agent.versionID == nil || *fixture.agent.versionID != 10 {
		t.Fatalf("version filter was not forwarded")
	}
	if fixture.events.eventType != "context.compacted" || fixture.events.channel != "version:10" {
		t.Fatalf("event query = %q/%q", fixture.events.eventType, fixture.events.channel)
	}
	if fixture.execution.passCalls != 1 || fixture.execution.evCalls != 1 {
		t.Fatalf("execution calls = %d/%d", fixture.execution.passCalls, fixture.execution.evCalls)
	}
}

func TestSnapshotGlobalScopeUsesTheGlobalEventChannel(t *testing.T) {
	fixture := newFixture()
	snapshot, err := fixture.aggregator().Snapshot(context.Background(), nil)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Metrics) != 7 {
		t.Fatalf("metrics = %d", len(snapshot.Metrics))
	}
	if fixture.version.versionID != nil || fixture.design.versionID != nil ||
		fixture.execution.versionID != nil || fixture.delivery.versionID != nil || fixture.agent.versionID != nil {
		t.Fatalf("nil version scope was not forwarded")
	}
	if fixture.events.channel != "" {
		t.Fatalf("global event channel = %q, want empty", fixture.events.channel)
	}
}

func TestSnapshotZeroDenominatorIsUnavailable(t *testing.T) {
	fixture := newFixture()
	fixture.version.num, fixture.version.denom = 0, 0
	fixture.design.num, fixture.design.denom = 0, 0
	fixture.execution.passNum, fixture.execution.passDenom = 0, 0
	fixture.execution.evNum, fixture.execution.evDenom = 0, 0
	fixture.delivery.num, fixture.delivery.denom = 0, 0
	fixture.agent.num, fixture.agent.denom = 0, 0
	fixture.events.count = 0

	snapshot, err := fixture.aggregator().Snapshot(context.Background(), nil)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, metric := range snapshot.Metrics {
		if metric.Name == quality.OperationalContextCompactionCount {
			continue // a real zero count is data, not a missing denominator
		}
		if metric.Available || metric.Value != nil {
			t.Fatalf("%s must be unavailable with a zero denominator: %+v", metric.Name, metric)
		}
		if metric.Numerator != 0 || metric.Denominator != 0 {
			t.Fatalf("%s counts = %d/%d", metric.Name, metric.Numerator, metric.Denominator)
		}
	}
	compaction := metricMap(t, snapshot)[quality.OperationalContextCompactionCount]
	if !compaction.Available || compaction.Value == nil || *compaction.Value != 0 {
		t.Fatalf("zero compaction count = %+v", compaction)
	}
}

func TestSnapshotPropagatesPortErrors(t *testing.T) {
	boom := errors.New("provider down")
	cases := []struct {
		name    string
		breakFn func(*fixture)
	}{
		{"version", func(f *fixture) { f.version.err = boom }},
		{"design", func(f *fixture) { f.design.err = boom }},
		{"execution pass", func(f *fixture) { f.execution.err = boom }},
		{"delivery", func(f *fixture) { f.delivery.err = boom }},
		{"agent", func(f *fixture) { f.agent.err = boom }},
		{"events", func(f *fixture) { f.events.err = boom }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture()
			tc.breakFn(fixture)
			if _, err := fixture.aggregator().Snapshot(context.Background(), nil); !errors.Is(err, boom) {
				t.Fatalf("error = %v, want wrapped boom", err)
			}
		})
	}
}

func TestSnapshotRejectsInvalidVersionScope(t *testing.T) {
	fixture := newFixture()
	zero := int64(0)
	_, err := fixture.aggregator().Snapshot(context.Background(), &zero)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("error = %v", err)
	}
	if apiErr.Details["field"] != "version_id" {
		t.Fatalf("field = %v", apiErr.Details["field"])
	}
	if fixture.version.calls != 0 {
		t.Fatalf("invalid scope reached the provider")
	}
}

func TestSnapshotRejectsNilPorts(t *testing.T) {
	fixture := newFixture()
	aggregator := qualitymetrics.NewAggregator(nil, fixture.design, fixture.execution, fixture.delivery, fixture.agent, fixture.events)
	_, err := aggregator.Snapshot(context.Background(), nil)
	if err == nil {
		t.Fatal("nil version port was accepted")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unavailable" {
		t.Fatalf("error = %v", err)
	}
}
