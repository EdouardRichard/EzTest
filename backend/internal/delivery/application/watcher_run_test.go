package deliveryapp

// v0.2.4-G T11: the background Run loop is the production path — causal wakes
// drain off the event publish path, and the periodic complete sweep fires on its
// own ticker (so a lost event with no wake is still healed). These tests drive
// the real loop with recording markers.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingMarker records every MarkStaleIfChanged version and signals a channel.
type recordingMarker struct {
	mu       sync.Mutex
	versions []int64
	signal   chan int64
}

func newRecordingMarker() *recordingMarker {
	return &recordingMarker{signal: make(chan int64, 64)}
}

func (m *recordingMarker) MarkStaleIfChanged(_ context.Context, versionID int64) (bool, error) {
	m.mu.Lock()
	m.versions = append(m.versions, versionID)
	m.mu.Unlock()
	select {
	case m.signal <- versionID:
	default:
	}
	return true, nil
}

func (m *recordingMarker) waitVersion(t *testing.T, want int64) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-m.signal:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for stale check of version %d", want)
		}
	}
}

// TestWatcherRunDrainsCausalWake: an event enqueues a version; the Run loop
// drains it off the publish path and re-fingerprints that version.
func TestWatcherRunDrainsCausalWake(t *testing.T) {
	marker := newRecordingMarker()
	watcher := NewReportStaleWatcher(marker, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = watcher.Run(ctx, nil, time.Hour) // sweep far away; only the causal wake fires
		close(done)
	}()

	fireStaleEvent(t, watcher, "issue.updated", 42)
	marker.waitVersion(t, 42)
	cancel()
	<-done
}

// TestWatcherRunPeriodicSweep: with no events at all, the periodic complete sweep
// still scans published-report versions (the lost-event fallback).
func TestWatcherRunPeriodicSweep(t *testing.T) {
	marker := newRecordingMarker()
	lister := &staticVersionLister{ids: []int64{7}}
	sweeper := NewReportStaleSweeper(lister, marker, nil)
	watcher := NewReportStaleWatcher(marker, nil, sweeper, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = watcher.Run(ctx, nil, 20*time.Millisecond) // fast sweep; no causal events
		close(done)
	}()

	marker.waitVersion(t, 7)
	cancel()
	<-done
}

// staticVersionLister returns a fixed version page once, then an empty page.
type staticVersionLister struct {
	mu   sync.Mutex
	ids  []int64
	done bool
}

func (l *staticVersionLister) ListVersionsWithPublishedReports(_ context.Context, _ int64, _ int) ([]int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return nil, nil
	}
	l.done = true
	return l.ids, nil
}
