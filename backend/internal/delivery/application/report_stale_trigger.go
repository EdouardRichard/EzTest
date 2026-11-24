package deliveryapp

// v0.2.4-G T11 (SC-G8, R-G7/R-G10/R-G10a): the causal stale trigger vocabulary
// and the event-driven watcher. A conclusion-input change EVENT wakes the
// watcher, which re-fingerprints the affected version's published reports
// (MarkStaleIfChanged) regardless of the version's running/completed status.
// Judge state events additionally refresh the report display (R-G10a), because
// review_state/verdict are display-only projections, not canonical fingerprint
// members, so a fingerprint check alone would never refresh them.
//
// Single source: the trigger vocabulary is a declared routing contract (which
// events change a conclusion input). TestStaleTriggerProducersExist pins every
// member against a real backend producer, so a renamed/removed producer event
// fails the gate. It is a closed routing table by design: a NEW conclusion-input
// event added later is not auto-enrolled — the periodic complete sweep is the
// safety net that heals any uncovered or lost path (R-G10).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// DefaultReportStaleSweepInterval is the periodic complete-sweep cadence. It is
// a fallback (the event-driven watcher is primary), so it is deliberately much
// slower than the reconciler debounce; each pass re-fingerprints every published
// report.
const DefaultReportStaleSweepInterval = 60 * time.Second

// defaultReportStaleDebounce coalesces a burst of conclusion-input events into
// one drain, anchored at the first wake so a continuous stream cannot starve it.
const defaultReportStaleDebounce = 300 * time.Millisecond

// reportStaleTriggerEvents is the set of conclusion-input change events that
// drive the causal stale check (R-G10). Each member is produced by the owning
// context (pinned by TestStaleTriggerProducersExist):
//   - evidence settlement / drop: execution evidence.created / evidence.skipped
//   - case_run terminal: execution case_run.finished / case_run.blocked
//     (terminal RECLASSIFICATION emits no event today; the sweep heals it)
//   - issue FIELD changes status/severity/category/dedupe: delivery issue.* events
//     (issue.triage ANALYSIS completion writes analysis_run.output without an
//     issue field event — a triage-ONLY change is sweep-healed, not event-driven;
//     final fix wave #4 disclosure)
//   - AC coverage (case confirmation / covered_acs): design.case_confirmed /
//     case_version_superseded / case_version_stale
//   - requirement content (AC hash) / closure: requirement.* events
//   - waiver / version closure classification: version.waived /
//     requirement.waived / version.closed
//   - reopen: version.reopened / requirement.reopened
var reportStaleTriggerEvents = map[string]struct{}{
	"evidence.created":               {},
	"evidence.skipped":               {},
	"case_run.finished":              {},
	"case_run.blocked":               {},
	issueEventCreated:                {},
	issueEventUpdated:                {},
	issueEventReopened:               {},
	issueEventMerged:                 {},
	"design.case_confirmed":          {},
	"design.case_version_superseded": {},
	"design.case_version_stale":      {},
	"requirement.version_created":    {},
	"requirement.clarified":          {},
	"requirement.closed":             {},
	"requirement.reopened":           {},
	"requirement.obsoleted":          {},
	"requirement.merged":             {},
	"requirement.deduped":            {},
	"requirement.rolled_back":        {},
	"requirement.waived":             {},
	"version.waived":                 {},
	"version.closed":                 {},
	"version.reopened":               {},
}

// judgeDisplayRefreshEvents are the judge state events (R-G10a). They are a
// subset of the stale triggers AND additionally refresh the published version
// report display, because the report shows judge verdict/review_state which are
// display-only (not fingerprint members).
var judgeDisplayRefreshEvents = map[string]struct{}{
	"design.judgment_submitted":  {},
	"design.judgment_overridden": {},
	"design.rework_requested":    {},
	"design.judge_exhausted":     {},
}

func init() {
	for eventType := range judgeDisplayRefreshEvents {
		reportStaleTriggerEvents[eventType] = struct{}{}
	}
}

// IsReportStaleTrigger reports whether eventType is a conclusion-input change
// event that must drive the causal stale check.
func IsReportStaleTrigger(eventType string) bool {
	_, ok := reportStaleTriggerEvents[eventType]
	return ok
}

// IsJudgeDisplayRefreshEvent reports whether eventType is a judge state event
// that must also refresh the report display (R-G10a).
func IsJudgeDisplayRefreshEvent(eventType string) bool {
	_, ok := judgeDisplayRefreshEvents[eventType]
	return ok
}

// VersionReportDisplayRefresher regenerates and publishes the affected version's
// published report so display-only projections (judge state) stay current.
// *ReportService implements it (RefreshVersionReportDisplay).
type VersionReportDisplayRefresher interface {
	RefreshVersionReportDisplay(ctx context.Context, versionID int64) error
}

// ReportStaleWatcher is the event-driven causal stale controller. Observe is a
// non-blocking event observer (it only enqueues); Drain processes the coalesced
// pending versions off the publish path; Run is the leader-gated background loop
// that drains causal wakes and runs the periodic complete sweep.
type ReportStaleWatcher struct {
	marker  ReportStaleMarker
	refresh VersionReportDisplayRefresher
	sweeper *ReportStaleSweeper
	logger  *slog.Logger

	mu           sync.Mutex
	stalePending map[int64]struct{}
	judgePending map[int64]struct{}
	wake         chan struct{}
}

// NewReportStaleWatcher wires the watcher. refresh and sweeper are optional:
// a nil refresh disables judge display refresh; a nil sweeper disables the
// periodic fallback in Run (causal draining still works).
func NewReportStaleWatcher(marker ReportStaleMarker, refresh VersionReportDisplayRefresher, sweeper *ReportStaleSweeper, logger *slog.Logger) *ReportStaleWatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReportStaleWatcher{
		marker:       marker,
		refresh:      refresh,
		sweeper:      sweeper,
		logger:       logger,
		stalePending: make(map[int64]struct{}),
		judgePending: make(map[int64]struct{}),
		wake:         make(chan struct{}, 1),
	}
}

// Observe implements the eventing observer contract. It never blocks and never
// fails the publish: it only records the affected version in the coalescing
// pending set and signals one wake. Non-trigger events and events without a
// version scope are ignored.
func (w *ReportStaleWatcher) Observe(_ context.Context, event eventingdomain.Event) error {
	if w == nil || !IsReportStaleTrigger(event.Type) {
		return nil
	}
	versionID := staleEventVersionID(event)
	if versionID <= 0 {
		return nil
	}
	w.mu.Lock()
	w.stalePending[versionID] = struct{}{}
	if IsJudgeDisplayRefreshEvent(event.Type) {
		w.judgePending[versionID] = struct{}{}
	}
	w.mu.Unlock()
	w.signal()
	return nil
}

func (w *ReportStaleWatcher) signal() {
	select {
	case w.wake <- struct{}{}:
	default: // a drain is already pending
	}
}

// Drain processes the coalesced pending versions: re-fingerprint every version
// touched by a conclusion-input event, then refresh the display of every version
// touched by a judge event. A per-version failure is aggregated, never stopping
// the rest. It runs off the event publish path (background loop / test call).
func (w *ReportStaleWatcher) Drain(ctx context.Context) error {
	if w == nil || w.marker == nil {
		return nil
	}
	w.mu.Lock()
	stale := w.stalePending
	judge := w.judgePending
	w.stalePending = make(map[int64]struct{})
	w.judgePending = make(map[int64]struct{})
	w.mu.Unlock()

	var errs []error
	for versionID := range stale {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if _, err := w.marker.MarkStaleIfChanged(ctx, versionID); err != nil {
			errs = append(errs, fmt.Errorf("causal stale version %d: %w", versionID, err))
		}
	}
	if w.refresh != nil {
		for versionID := range judge {
			if ctx.Err() != nil {
				errs = append(errs, ctx.Err())
				break
			}
			if err := w.refresh.RefreshVersionReportDisplay(ctx, versionID); err != nil {
				errs = append(errs, fmt.Errorf("judge display refresh version %d: %w", versionID, err))
				// Re-queue so a transient failure retries instead of being silently
				// dropped (fix round 1, art.7.6). No immediate signal: the periodic
				// complete sweep's display-digest comparison and the next causal wake
				// both retry it, so a persistent failure cannot busy-spin.
				w.mu.Lock()
				w.judgePending[versionID] = struct{}{}
				w.mu.Unlock()
			}
		}
	}
	return errors.Join(errs...)
}

// Run is the leader-gated background loop: it drains causal wakes (debounced)
// and runs the periodic complete sweep. shouldRun gates each pass (nil runs
// always); a gate-closed causal wake is re-armed, never dropped, so it runs once
// leadership is acquired. It returns nil on context cancellation.
func (w *ReportStaleWatcher) Run(ctx context.Context, shouldRun func() bool, sweepInterval time.Duration) error {
	if w == nil {
		return nil
	}
	if sweepInterval <= 0 {
		sweepInterval = DefaultReportStaleSweepInterval
	}
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.wake:
			if !w.waitDebounce(ctx) {
				return nil
			}
			if !reportStaleGateOpen(shouldRun) {
				w.signal() // keep the pending drain armed for when leadership opens
				continue
			}
			if err := w.Drain(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.Warn("report stale causal drain failed", "error", err)
			}
		case <-ticker.C:
			if !reportStaleGateOpen(shouldRun) || w.sweeper == nil {
				continue
			}
			if _, err := w.sweeper.Sweep(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.Warn("report stale complete sweep failed", "error", err)
			}
		}
	}
}

func reportStaleGateOpen(shouldRun func() bool) bool {
	return shouldRun == nil || shouldRun()
}

// waitDebounce coalesces a wake burst, anchored at the first notification. It
// returns false when ctx is cancelled.
func (w *ReportStaleWatcher) waitDebounce(ctx context.Context) bool {
	timer := time.NewTimer(defaultReportStaleDebounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-w.wake:
			// another event in the same burst: keep waiting for the window
		case <-timer.C:
			return true
		}
	}
}

// staleEventVersionID extracts the version scope of a published event (decimal
// string version_id, with the version:{id} channel as fallback). It mirrors the
// reconciler wake extraction so both routing paths agree on the affected version.
func staleEventVersionID(event eventingdomain.Event) int64 {
	if raw, ok := event.Scope["version_id"]; ok {
		switch value := raw.(type) {
		case string:
			if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && parsed > 0 {
				return parsed
			}
		case float64:
			if value > 0 {
				return int64(value)
			}
		case int64:
			if value > 0 {
				return value
			}
		case json.Number:
			if parsed, err := value.Int64(); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	const prefix = "version:"
	if strings.HasPrefix(event.Channel, prefix) {
		if parsed, err := strconv.ParseInt(strings.TrimPrefix(event.Channel, prefix), 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}
