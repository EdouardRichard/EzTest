package executionapp

import (
	"context"
	"errors"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// offlineWindowSettings is a minimal SettingsReader for the offline-window
// single-source tests.
type offlineWindowSettings struct {
	seconds int64
	err     error
}

func (s *offlineWindowSettings) String(context.Context, string) (string, error) {
	return "", nil
}

func (s *offlineWindowSettings) Int(context.Context, string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.seconds, nil
}

func (s *offlineWindowSettings) Bool(context.Context, string) (bool, error) {
	return false, nil
}

func defaultOfflineWindow() time.Duration {
	return time.Duration(execution.DefaultExecutorOfflineSeconds) * time.Second
}

// onlineExecutorRecovery builds a recovery sweep whose only executor is online
// with a heartbeat at heartbeatAge before now.
func onlineExecutorRecovery(now time.Time, heartbeatAge time.Duration, window func(context.Context) time.Duration) (*Recovery, execution.CaseRun) {
	repo := newFakeExecutorStore()
	heartbeat := now.Add(-heartbeatAge)
	repo.byID[1] = execution.ExecutorInstance{
		ID: 1, Name: "executor", Endpoint: "http://x",
		Status: execution.ExecutorActive, LastHeartbeat: &heartbeat,
	}
	recovery := NewRecovery(RecoveryDeps{
		Executors:     repo,
		Clock:         &coreClock{now: now},
		OfflineWindow: window,
	})
	run := execution.CaseRun{ID: 500, OwnerExecutorID: int64PtrValue(1)}
	return recovery, run
}

// TestRecoveryOfflineWindowUsesConfiguredWindow is the 019B red test: with a
// 90s window a reachable executor whose last heartbeat was 60s ago is still
// online. The old constant-30s code classified it as unreachable.
func TestRecoveryOfflineWindowUsesConfiguredWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	recovery, run := onlineExecutorRecovery(now, 60*time.Second, func(context.Context) time.Duration {
		return 90 * time.Second
	})
	if recovered := recovery.executorUnreachable(ctx, run); recovered {
		t.Fatal("executor with a 60s-old heartbeat must stay reachable under a 90s window")
	}
}

// TestRecoveryOfflineWindowUnchangedDefault keeps the legacy classification
// when the configured window is the default 30s.
func TestRecoveryOfflineWindowUnchangedDefault(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	recovery, run := onlineExecutorRecovery(now, 60*time.Second, func(context.Context) time.Duration {
		return 30 * time.Second
	})
	if !recovery.executorUnreachable(ctx, run) {
		t.Fatal("a 60s-old heartbeat must be unreachable under a 30s window")
	}
}

// TestRecoveryOfflineWindowDefaultsWhenUnwired verifies the nil
// OfflineWindow fallback: 30s boundaries stay identical to the old constant.
func TestRecoveryOfflineWindowDefaultsWhenUnwired(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)

	recovery, run := onlineExecutorRecovery(now, 29*time.Second, nil)
	if recovery.executorUnreachable(ctx, run) {
		t.Fatal("a 29s-old heartbeat must stay reachable with the default window")
	}

	recovery, run = onlineExecutorRecovery(now, 31*time.Second, nil)
	if !recovery.executorUnreachable(ctx, run) {
		t.Fatal("a 31s-old heartbeat must be unreachable with the default window")
	}
}

// TestRegistryOfflineWindowUsesSettings proves the registry and recovery share
// one settings-backed window, with the default on missing/zero/error values.
func TestRegistryOfflineWindowUsesSettings(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		settings SettingsReader
		want     time.Duration
	}{
		{name: "configured", settings: &offlineWindowSettings{seconds: 90}, want: 90 * time.Second},
		{name: "nil settings", settings: nil, want: defaultOfflineWindow()},
		{name: "zero", settings: &offlineWindowSettings{seconds: 0}, want: defaultOfflineWindow()},
		{name: "negative", settings: &offlineWindowSettings{seconds: -5}, want: defaultOfflineWindow()},
		{name: "read error", settings: &offlineWindowSettings{err: errors.New("settings unavailable")}, want: defaultOfflineWindow()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry(RegistryDeps{Settings: tc.settings})
			if got := registry.OfflineWindow(ctx); got != tc.want {
				t.Fatalf("OfflineWindow() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecutionOfflineWindowNilSettings(t *testing.T) {
	if got := executionOfflineWindow(context.Background(), nil); got != defaultOfflineWindow() {
		t.Fatalf("executionOfflineWindow(nil) = %v, want %v", got, defaultOfflineWindow())
	}
}
