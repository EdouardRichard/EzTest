package chainapp

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeSettings is the hot settings cache stand-in: Int reads the current map on
// every call, so a test can change a value between two Read calls.
type fakeSettings struct {
	values map[string]int64
	err    error
	calls  []string
}

func (s *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	s.calls = append(s.calls, key)
	if s.err != nil {
		return 0, s.err
	}
	value, ok := s.values[key]
	if !ok {
		return 0, platform.NotFound("setting")
	}
	return value, nil
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{values: map[string]int64{
		"chain.max_nodes":          chain.DefaultMaxNodes,
		"chain.max_edges":          chain.DefaultMaxEdges,
		"chain.max_requirements":   chain.DefaultMaxRequirements,
		"chain.replay_event_limit": chain.DefaultReplayEventLimit,
	}}
}

func TestP6SettingsHotReload(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	fakes := completeFakes()
	settings := newFakeSettings()
	settings.values["chain.max_nodes"] = 3
	settings.values["chain.max_edges"] = 2
	settings.values["chain.max_requirements"] = 2
	service := newService(fakes, now).WithSettings(settings)

	first, err := service.Read(context.Background(), 42, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if first.Truncation.MaxNodes != 3 || first.Truncation.MaxEdges != 2 || first.Truncation.MaxRequirements != 2 {
		t.Errorf("first truncation = %+v, want the hot settings 3/2/2", first.Truncation)
	}
	if fakes.requirementLimit != 3 {
		t.Errorf("Requirements limit = %d, want max_requirements+1 = 3", fakes.requirementLimit)
	}

	// A PATCH settings hot-cache update must apply to the next request without
	// rebuilding the service.
	settings.values["chain.max_nodes"] = 5
	settings.values["chain.max_edges"] = 4
	settings.values["chain.max_requirements"] = 4
	second, err := service.Read(context.Background(), 42, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if second.Truncation.MaxNodes != 5 || second.Truncation.MaxEdges != 4 || second.Truncation.MaxRequirements != 4 {
		t.Errorf("second truncation = %+v, want the updated hot settings 5/4/4", second.Truncation)
	}

	// requirement_limit overrides chain.max_requirements for one request.
	overridden, err := service.Read(context.Background(), 42, chain.ChainFilter{RequirementLimit: 7})
	if err != nil {
		t.Fatalf("override Read: %v", err)
	}
	if overridden.Truncation.MaxRequirements != 7 {
		t.Errorf("override max_requirements = %d, want 7", overridden.Truncation.MaxRequirements)
	}
	if fakes.requirementLimit != 8 {
		t.Errorf("override Requirements limit = %d, want 8", fakes.requirementLimit)
	}

	// Missing keys and a failing settings cache fall back to the static budget.
	settings.err = errors.New("settings cache down")
	fallback, err := newServiceWithBudget(completeFakes(), now, chain.Budget{MaxNodes: 9}).WithSettings(settings).
		Read(context.Background(), 42, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("fallback Read: %v", err)
	}
	if fallback.Truncation.MaxNodes != 9 {
		t.Errorf("fallback max_nodes = %d, want the static budget 9", fallback.Truncation.MaxNodes)
	}
}

func TestP6DegradedLayers(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

	t.Run("one failed layer degrades and keeps healthy layers", func(t *testing.T) {
		fakes := completeFakes()
		fakes.errs["scenarios"] = errProviderBoom

		view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !slices.Equal(view.DegradedLayers, []string{chain.LayerDesign}) {
			t.Errorf("degraded_layers = %v, want [design]", view.DegradedLayers)
		}
		ids := map[string]bool{}
		for _, node := range view.Nodes {
			ids[node.ID] = true
		}
		if !ids["requirement:1"] || !ids["case_version:100"] || !ids["issue:300"] {
			t.Errorf("healthy layers missing after a design degradation: %v", view.Nodes)
		}
		if ids["scenario:10"] {
			t.Errorf("failed design layer still returned scenarios: %v", view.Nodes)
		}
	})

	t.Run("unavailable provider errors degrade instead of failing the request", func(t *testing.T) {
		fakes := completeFakes()
		fakes.errs["issues"] = platform.Unavailable("delivery read down")

		view, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !slices.Equal(view.DegradedLayers, []string{chain.LayerDelivery}) {
			t.Errorf("degraded_layers = %v, want [delivery]", view.DegradedLayers)
		}
	})

	t.Run("every layer failing is a 503 provider_unavailable", func(t *testing.T) {
		fakes := completeFakes()
		for _, operation := range []string{
			"requirements", "version_suts", "scenarios", "cases", "waves", "case_runs",
			"issues", "reports", "closures", "sut_names", "page_names", "cursor",
		} {
			fakes.errs[operation] = errProviderBoom
		}

		_, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{})
		if err == nil {
			t.Fatal("Read error = nil, want 503 provider_unavailable")
		}
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 503 || apiErr.Code != "provider_unavailable" {
			t.Fatalf("error = %v, want 503 provider_unavailable", err)
		}
	})

	t.Run("version 404 is not degraded", func(t *testing.T) {
		fakes := completeFakes()
		fakes.errs["requirements"] = platform.NotFound("version")

		_, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{})
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 404 {
			t.Fatalf("error = %v, want the version 404", err)
		}
	})
}

func TestP6WaveNotInVersionIs404(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	fakes := completeFakes()

	_, err := newService(fakes, now).Read(context.Background(), 42, chain.ChainFilter{WaveID: ptr(999)})
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("error = %v, want a wave 404", err)
	}
}

func TestP6ReplayEventLimit(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	fakes := completeFakes()
	fakes.cursor = 10
	fakes.eventTimes[5] = now.Add(-time.Hour)
	fakes.eventTimes[9] = now.Add(time.Hour)
	settings := newFakeSettings()
	settings.values["chain.replay_event_limit"] = 3
	service := newService(fakes, now).WithSettings(settings)

	view, err := service.Read(context.Background(), 42, chain.ChainFilter{EventFrom: ptr(5), EventTo: ptr(9)})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if view.Replay.EventLimit != 3 {
		t.Errorf("replay.event_limit = %d, want 3", view.Replay.EventLimit)
	}
	if !view.Replay.Truncated {
		t.Error("replay.truncated = false, want true for a 5-event range with limit 3")
	}

	settings.values["chain.replay_event_limit"] = 20
	next, err := service.Read(context.Background(), 42, chain.ChainFilter{EventFrom: ptr(5), EventTo: ptr(9)})
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if next.Replay.EventLimit != 20 || next.Replay.Truncated {
		t.Errorf("replay = %+v, want limit 20/truncated false", next.Replay)
	}

	live, err := service.Read(context.Background(), 42, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("live Read: %v", err)
	}
	if live.Replay.EventLimit != 20 || live.Replay.Truncated {
		t.Errorf("live replay = %+v, want the configured limit and no truncation", live.Replay)
	}
}

// queryTracer is the unit-level SQL tracer: each chain provider round trip is
// recorded as one bounded read. The P6 budget is ten SQL reads per request; the
// production adapters implement DesignSnapshot/ExecutionSnapshot/ResourceSnapshot
// so the service issues one read per aggregated layer instead of per node type.
type queryTracer struct{ reads int }

func (t *queryTracer) add() { t.reads++ }

type queryBudgetProviders struct {
	*fakeProviders
	tracer *queryTracer
}

func (p *queryBudgetProviders) Requirements(ctx context.Context, versionID int64, limit int) ([]chain.Node, error) {
	p.tracer.add()
	return p.fakeProviders.Requirements(ctx, versionID, limit)
}

func (p *queryBudgetProviders) VersionSUTs(ctx context.Context, versionID int64) ([]chain.Node, error) {
	p.tracer.add()
	return p.fakeProviders.VersionSUTs(ctx, versionID)
}

func (p *queryBudgetProviders) Issues(ctx context.Context, versionID int64) ([]chain.Node, error) {
	p.tracer.add()
	return p.fakeProviders.Issues(ctx, versionID)
}

func (p *queryBudgetProviders) Reports(ctx context.Context, versionID int64) ([]chain.Node, error) {
	p.tracer.add()
	return p.fakeProviders.Reports(ctx, versionID)
}

func (p *queryBudgetProviders) Closures(ctx context.Context, versionID int64) ([]chain.Node, error) {
	p.tracer.add()
	return p.fakeProviders.Closures(ctx, versionID)
}

func (p *queryBudgetProviders) LatestCursor(ctx context.Context, channel string) (int64, error) {
	p.tracer.add()
	return p.fakeProviders.LatestCursor(ctx, channel)
}

func (p *queryBudgetProviders) DesignSnapshot(ctx context.Context, versionID int64) ([]chain.Node, []chain.Node, error) {
	p.tracer.add()
	return p.fakeProviders.scenarios, p.fakeProviders.cases, nil
}

func (p *queryBudgetProviders) ExecutionSnapshot(ctx context.Context, versionID int64) ([]chain.Node, []chain.Node, error) {
	p.tracer.add()
	return p.fakeProviders.waves, p.fakeProviders.caseRuns, nil
}

func (p *queryBudgetProviders) ResourceSnapshot(ctx context.Context, versionID int64) (map[int64]string, map[int64]string, error) {
	p.tracer.add()
	return p.fakeProviders.sutNames, p.fakeProviders.pageNames, nil
}

func TestP6QueryBudget(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	fakes := completeFakes()
	tracer := &queryTracer{}
	providers := &queryBudgetProviders{fakeProviders: fakes, tracer: tracer}
	service := NewChainService(providers, providers, providers, providers, providers, providers, providers, fakeClock{now: now})

	view, err := service.Read(context.Background(), 42, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(view.Nodes) == 0 {
		t.Fatal("budget read returned no nodes")
	}
	if tracer.reads > 10 {
		t.Fatalf("query budget = %d reads, want <= 10", tracer.reads)
	}
}
