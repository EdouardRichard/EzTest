package adapters

// W1-14A: the production adapter maps *versionapp.DiscoveryDelta onto the
// scheduler's narrow DiscoveryDeltaEvaluator port. ErrDeltaBusy means another
// pass owns the (version,wave) lock; that is a skipped pass, not a wave
// failure, so the adapter degrades it to nil and logs at debug.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type fakeVersionDiscoveryDelta struct {
	versionID int64
	waveID    int64
	calls     int
	result    versionapp.DeltaResult
	err       error
}

func (f *fakeVersionDiscoveryDelta) Evaluate(_ context.Context, versionID, waveID int64) (versionapp.DeltaResult, error) {
	f.calls++
	f.versionID = versionID
	f.waveID = waveID
	return f.result, f.err
}

func TestDiscoveryDeltaEvaluatorMapsBusyToSkipped(t *testing.T) {
	delta := &fakeVersionDiscoveryDelta{err: versionapp.ErrDeltaBusy}
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	adapter := NewDiscoveryDeltaEvaluator(delta, logger)

	if err := adapter.Evaluate(context.Background(), 7, 7001); err != nil {
		t.Fatalf("busy delta = %v, want nil (a skipped pass)", err)
	}
	if delta.calls != 1 || delta.versionID != 7 || delta.waveID != 7001 {
		t.Fatalf("delta call = %d (%d,%d), want one (7,7001)", delta.calls, delta.versionID, delta.waveID)
	}
	if logged := buffer.String(); !strings.Contains(logged, "busy") {
		t.Fatalf("busy skip was not logged: %q", logged)
	}
}

func TestDiscoveryDeltaEvaluatorPassesRealFailureThrough(t *testing.T) {
	delta := &fakeVersionDiscoveryDelta{err: errors.New("catalog unavailable")}
	adapter := NewDiscoveryDeltaEvaluator(delta, nil)

	err := adapter.Evaluate(context.Background(), 7, 7001)
	if err == nil || !strings.Contains(err.Error(), "catalog unavailable") {
		t.Fatalf("delta failure = %v, want the underlying error", err)
	}
}

func TestDiscoveryDeltaEvaluatorSuccess(t *testing.T) {
	delta := &fakeVersionDiscoveryDelta{result: versionapp.DeltaResult{}}
	adapter := NewDiscoveryDeltaEvaluator(delta, nil)

	if err := adapter.Evaluate(context.Background(), 7, 7001); err != nil {
		t.Fatalf("successful delta = %v, want nil", err)
	}
}

var _ executionapp.DiscoveryDeltaEvaluator = (*DiscoveryDeltaEvaluator)(nil)
