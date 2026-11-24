package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// quotasProjectionSettings answers exactly ONE settings key; every other key is
// absent. That makes the test prove WHICH key the projection reads, not just
// that it reads some key.
type quotasProjectionSettings struct {
	key   string
	value int64
	err   error
}

func (s quotasProjectionSettings) String(context.Context, string) (string, error) {
	return "", execution.ErrNotFound
}

func (s quotasProjectionSettings) Int(_ context.Context, key string) (int64, error) {
	if key != s.key {
		return 0, execution.ErrNotFound
	}
	return s.value, s.err
}

func (s quotasProjectionSettings) Bool(context.Context, string) (bool, error) {
	return false, execution.ErrNotFound
}

// mutableQuotaSettings answers the quota key with a value/error the test can
// flip between calls, to exercise the last-known-good retention path.
type mutableQuotaSettings struct {
	value int64
	err   error
}

func (s *mutableQuotaSettings) String(context.Context, string) (string, error) {
	return "", execution.ErrNotFound
}

func (s *mutableQuotaSettings) Int(_ context.Context, key string) (int64, error) {
	if key != executionapp.SettingMaxConcurrentCaseRuns {
		return 0, execution.ErrNotFound
	}
	return s.value, s.err
}

func (s *mutableQuotaSettings) Bool(context.Context, string) (bool, error) {
	return false, execution.ErrNotFound
}

// TestQuotasProjectionSingleGlobal pins the v0.2.4-F P5 REST contract (V-0015
// third copy): GET /execution/overview's quotas block is a SINGLE global
// projection of execution.max_concurrent_case_runs (0 = unlimited). This view
// struct is independent of the domain Quota -- removing the domain dimensions
// does not force it to change, so it is pinned explicitly here.
func TestQuotasProjectionSingleGlobal(t *testing.T) {
	ctx := context.Background()

	t.Run("reads the new single-global key verbatim", func(t *testing.T) {
		h := New(Deps{Settings: quotasProjectionSettings{
			key: executionapp.SettingMaxConcurrentCaseRuns, value: 5,
		}})
		if got := h.quotas(ctx); got.MaxConcurrentCaseRuns != 5 {
			t.Fatalf("quotas = %+v, want MaxConcurrentCaseRuns 5 from %s", got, executionapp.SettingMaxConcurrentCaseRuns)
		}
	})

	t.Run("zero is unlimited and must not fall back to a positive default", func(t *testing.T) {
		// settingInt's value<=0 fallback is WRONG for this key: 0 is a valid,
		// meaningful value (unlimited), not "unset".
		h := New(Deps{Settings: quotasProjectionSettings{
			key: executionapp.SettingMaxConcurrentCaseRuns, value: 0,
		}})
		if got := h.quotas(ctx); got.MaxConcurrentCaseRuns != 0 {
			t.Fatalf("quotas = %+v, want 0 (unlimited) preserved", got)
		}
	})

	t.Run("the retired three keys are no longer read", func(t *testing.T) {
		// A settings source that ONLY knows a retired key must yield 0: the
		// projection must not read execution.global_concurrency anymore.
		h := New(Deps{Settings: quotasProjectionSettings{
			key: "execution.global_concurrency", value: 3,
		}})
		if got := h.quotas(ctx); got.MaxConcurrentCaseRuns != 0 {
			t.Fatalf("quotas = %+v, want 0: the retired key must not feed the projection", got)
		}
	})

	t.Run("missing settings or read failure projects unlimited", func(t *testing.T) {
		h := New(Deps{})
		if got := h.quotas(ctx); got.MaxConcurrentCaseRuns != 0 {
			t.Fatalf("quotas with no settings = %+v, want 0", got)
		}
		h = New(Deps{Settings: quotasProjectionSettings{
			key: executionapp.SettingMaxConcurrentCaseRuns, err: execution.ErrNotFound,
		}})
		if got := h.quotas(ctx); got.MaxConcurrentCaseRuns != 0 {
			t.Fatalf("quotas on read failure = %+v, want 0", got)
		}
	})

	t.Run("read failure retains the last-known-good limit (ruling P12)", func(t *testing.T) {
		// Same retention semantics as the scheduler gate: a failed read must not
		// silently rewrite a configured finite limit to 0 (unlimited) in the
		// projection either -- gate and UI share one resolver.
		settings := &mutableQuotaSettings{value: 5}
		h := New(Deps{Settings: settings})
		if got := h.quotas(ctx); got.MaxConcurrentCaseRuns != 5 {
			t.Fatalf("quotas = %+v, want 5", got)
		}
		settings.err = execution.ErrNotFound
		if got := h.quotas(ctx); got.MaxConcurrentCaseRuns != 5 {
			t.Fatalf("quotas after a failed read = %+v, want the retained 5, not a silent "+
				"widening to 0", got)
		}
	})

	t.Run("JSON shape is exactly one global field", func(t *testing.T) {
		raw, err := json.Marshal(ExecutionQuotas{MaxConcurrentCaseRuns: 7})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(fields) != 1 {
			t.Fatalf("quotas JSON = %s, want exactly one field (the retired per_sut/cross_system/global trio is gone)", raw)
		}
		if value, ok := fields["max_concurrent_case_runs"]; !ok || value != float64(7) {
			t.Fatalf("quotas JSON = %s, want max_concurrent_case_runs 7", raw)
		}
	})
}
