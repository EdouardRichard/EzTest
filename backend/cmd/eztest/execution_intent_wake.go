package main

// Event-driven execution intent scan (P2-T5 fix round).
//
// Before this file the execution scheduler polled EnsureInitialIntents every
// five 1s ticks, because confirmed cases can appear without an execution event.
// Case confirmation does publish design.case_confirmed, so the scan is now
// woken by that event through the same eventing observer seam as the
// reconciler. EZTEST_INTENT_SCAN_INTERVAL remains the compatibility poll until
// P10 acceptance.

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// designCaseConfirmedEvent is the design event that can make new case versions
// executable. It is the event-driven replacement for the old 5s intent scan.
const designCaseConfirmedEvent = "design.case_confirmed"

// intentEnsurer is the execution intent scan surface (*executionapp.IntentService).
type intentEnsurer interface {
	EnsureInitialIntents(ctx context.Context, versionID int64) (int, error)
}

// versionSummaryLister is the version listing surface (*versionapp.Service)
// used by the startup and compatibility full scans.
type versionSummaryLister interface {
	List(ctx context.Context, limit int, beforeID *int64) ([]versionapp.VersionSummary, bool, error)
}

// executionIntentWake coalesces case-confirmed events into pending version IDs.
// The observer only records the affected version; the leader-gated scheduler
// loop performs the database work, so a slow scan never blocks the publishing
// request. scanAll is the startup/leader fallback: it stays set until a leader
// actually executes the full scan.
type executionIntentWake struct {
	mu      sync.Mutex
	pending map[int64]struct{}
	scanAll bool
	signal  chan struct{}
}

func newExecutionIntentWake() *executionIntentWake {
	return &executionIntentWake{
		pending: make(map[int64]struct{}),
		signal:  make(chan struct{}, 1),
	}
}

// Notify records one version whose case confirmation may need an intent scan.
// An unknown/zero version is ignored: the full scan covers it.
func (w *executionIntentWake) Notify(versionID int64) {
	if w == nil || versionID <= 0 {
		return
	}
	w.mu.Lock()
	w.pending[versionID] = struct{}{}
	w.mu.Unlock()
	w.wake()
}

// NotifyScanAll asks for one full intent scan (startup and post-failover). The
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

// take drains the pending wake state. It must only be called while the caller
// holds leadership, otherwise the requests would be lost.
func (w *executionIntentWake) take() ([]int64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := make([]int64, 0, len(w.pending))
	for versionID := range w.pending {
		ids = append(ids, versionID)
	}
	w.pending = make(map[int64]struct{})
	scanAll := w.scanAll
	w.scanAll = false
	return ids, scanAll
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
