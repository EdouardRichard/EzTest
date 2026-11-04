package main

// Event-driven execution intent scan (P2-T5 fix round; revised by Task 78 and
// Task 82).
//
// Execution intents are only refreshed by the explicit execute.batch stage job
// at executing. The wake remains for the leader/startup repair scan; case
// confirmations no longer wake it, because a case confirmed before the case hard
// gate must not auto-create an intent or wave. The repair scan itself is
// selection-scoped (D23): it reads the case_ids persisted on the execute.batch
// stage-entry work item and treats a missing selection as a typed skip.

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// intentEnsurer is the execution intent repair-scan surface
// (*executionapp.IntentService).
type intentEnsurer interface {
	EnsureInitialIntents(ctx context.Context, versionID int64) (int, error)
}

// versionSummaryLister is the version listing surface (*versionapp.Service)
// used by the startup and compatibility full scans.
type versionSummaryLister interface {
	List(ctx context.Context, limit int, beforeID *int64) ([]versionapp.VersionSummary, bool, error)
}

// executionIntentWake holds the one leader/startup repair request. It is a
// coalescing flag rather than a per-version queue: a single full scan covers
// every version, and the leader-gated scheduler performs the database work so a
// slow scan never blocks an event observer. scanAll stays set until a leader
// actually executes the scan.
type executionIntentWake struct {
	mu      sync.Mutex
	scanAll bool
	signal  chan struct{}
}

func newExecutionIntentWake() *executionIntentWake {
	return &executionIntentWake{
		signal: make(chan struct{}, 1),
	}
}

// NotifyScanAll asks for one full repair scan (startup and post-failover). The
// request survives follower ticks until a leader executes it.
func (w *executionIntentWake) NotifyScanAll() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.scanAll = true
	w.mu.Unlock()
	w.wake()
}

// takeScanAll drains the pending full-scan request. It must only be called
// while the caller holds leadership, otherwise the request would be lost.
func (w *executionIntentWake) takeScanAll() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	scanAll := w.scanAll
	w.scanAll = false
	return scanAll
}

// events exposes the coalescing signal; a nil wake yields a nil channel so the
// scheduler select simply never wakes on it.
func (w *executionIntentWake) events() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.signal
}

func (w *executionIntentWake) wake() {
	select {
	case w.signal <- struct{}{}:
	default:
		// A scan is already pending for this burst.
	}
}

// intentScanIntervalFromEnv is the P2-T5 compatibility switch. Unset/0/off (in
// both the bare and duration spellings) keeps the intent scan fully
// event-driven; a positive duration restores the pre-0.2.1 periodic scan until
// P10 acceptance. An invalid value logs and stays event-driven.
func intentScanIntervalFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("EZTEST_INTENT_SCAN_INTERVAL"))
	if raw == "" || raw == "0" || strings.EqualFold(raw, "off") {
		return 0
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval <= 0 {
		if err != nil {
			log.Printf("eztest: invalid EZTEST_INTENT_SCAN_INTERVAL %q; keeping the event-driven intent scan", raw)
		}
		return 0
	}
	return interval
}
