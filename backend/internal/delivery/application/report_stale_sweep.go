package deliveryapp

// v0.2.4-G T11 (SC-G8, R-G10): the periodic COMPLETE stale sweep. It is the
// fallback behind the event-driven causal triggers: it scans EVERY version that
// has a published report — running AND completed — recomputes the conclusion-
// input fingerprint and marks changed reports stale. It replaces the old narrow
// polling that only looked at running versions (and a bounded case_run window),
// and it heals lost events (best-effort publish) plus the no-event terminal
// reclassification path (G does not modify the execution terminal write path).
//
// The sweep is bounded per page and idempotent: MarkStaleIfChanged only
// transitions a published report to stale when its fingerprint actually changed,
// so repeated sweeps never mint duplicate revisions or re-emit report.stale.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
)

// PublishedReportVersionLister lists the versions that own at least one report
// with a published current revision (status published OR stale — a stale report
// still has a published revision the display healer must reach, final fix wave
// #5), ascending by version_id, paginated by afterVersionID (exclusive). It is
// the sweep's scan source: ownership of "has a published report" lives in the
// delivery report table, so the lister is a delivery port (the version reconciler
// only knows running versions and must not be the scan source). limit is clamped
// by the adapter; the sweep pages until an EMPTY page.
type PublishedReportVersionLister interface {
	ListVersionsWithPublishedReports(ctx context.Context, afterVersionID int64, limit int) ([]int64, error)
}

// ReportStaleMarker recomputes the conclusion-input fingerprint of a version's
// published reports and marks the changed ones stale. *ReportService implements
// it (MarkStaleIfChanged).
type ReportStaleMarker interface {
	MarkStaleIfChanged(ctx context.Context, versionID int64) (bool, error)
}

// ReportDisplayHealer refreshes a version's published report when its DISPLAY-
// only state (judge projection) diverged from the stored display digest — the
// lost-judge-event healing path independent of the canonical fingerprint
// (v0.2.4-G T11 fix round 1, R-G10a). *ReportService implements it
// (RefreshDisplayIfChanged).
type ReportDisplayHealer interface {
	RefreshDisplayIfChanged(ctx context.Context, versionID int64) (bool, error)
}

// ReportVersionSweeper is the optional combined per-version sweep operation
// (H-G4 #27): it runs the canonical stale leg AND the display-heal leg sharing
// one version-snapshot read. *ReportService implements it (SweepVersionOnce).
// The sweeper uses it only when the SAME instance is wired as both marker and
// display healer; otherwise it falls back to the two independent port calls
// (identical behavior, no shared read).
type ReportVersionSweeper interface {
	SweepVersionOnce(ctx context.Context, versionID int64) (staled bool, refreshed bool, err error)
}

// sameSweepInstance reports whether a and b are the same comparable instance
// (the production wiring passes one *ReportService as both marker and healer).
// Non-comparable dynamic types never match, so the comparison cannot panic.
func sameSweepInstance(a, b any) bool {
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta == nil || tb == nil || ta != tb || !ta.Comparable() {
		return false
	}
	return a == b
}

// defaultReportSweepPageSize bounds how many versions one sweep page reads.
// COUPLING PIN (fix round 1 minor #3): the postgres adapter clamps LIMIT to 100
// (deliverypg effectiveLimit). The sweep therefore terminates on an EMPTY page,
// NOT a short page, so a future pageSize above the adapter clamp can never end
// the scan early (an incomplete sweep with no warning). Keep pageSize <= 100 to
// match the adapter; the empty-page termination is the safety net regardless.
const defaultReportSweepPageSize = 100

// ReportSweepStats reports one sweep pass: how many published-report versions
// were scanned, how many had a report marked stale (canonical), and how many had
// a display-only refresh published (judge display healing).
type ReportSweepStats struct {
	Scanned          int
	Staled           int
	DisplayRefreshed int
}

// ReportStaleSweeper runs the complete stale sweep over all published reports.
type ReportStaleSweeper struct {
	versions PublishedReportVersionLister
	marker   ReportStaleMarker
	display  ReportDisplayHealer
	pageSize int
	logger   *slog.Logger
}

// NewReportStaleSweeper wires the sweep. A nil logger uses slog.Default.
func NewReportStaleSweeper(versions PublishedReportVersionLister, marker ReportStaleMarker, logger *slog.Logger) *ReportStaleSweeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReportStaleSweeper{
		versions: versions,
		marker:   marker,
		pageSize: defaultReportSweepPageSize,
		logger:   logger,
	}
}

// WithDisplayHealer attaches the optional display-only healing path (judge
// display digest comparison). A nil healer keeps the canonical-only sweep. It
// returns the sweeper so the composition root can chain it.
func (s *ReportStaleSweeper) WithDisplayHealer(display ReportDisplayHealer) *ReportStaleSweeper {
	if s != nil {
		s.display = display
	}
	return s
}

// Sweep scans every version with a published report and marks changed reports
// stale (canonical) plus refreshes diverged display-only state (judge). It pages
// through the full set (complete coverage per pass, terminating on an EMPTY
// page), isolates a per-version failure (one bad version never stops the rest)
// and aggregates the errors. It is idempotent: a second pass over unchanged
// inputs marks nothing and refreshes nothing.
func (s *ReportStaleSweeper) Sweep(ctx context.Context) (ReportSweepStats, error) {
	var stats ReportSweepStats
	if s == nil || s.versions == nil || s.marker == nil {
		return stats, reportServiceNotConfigured("report stale sweep")
	}
	pageSize := s.pageSize
	if pageSize <= 0 {
		pageSize = defaultReportSweepPageSize
	}
	var errs []error
	after := int64(0)
	for {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		ids, err := s.versions.ListVersionsWithPublishedReports(ctx, after, pageSize)
		if err != nil {
			errs = append(errs, fmt.Errorf("list published report versions: %w", err))
			break
		}
		// Terminate on an EMPTY page (not a short one): the adapter may clamp the
		// page below pageSize, and a short-page stop would silently end the sweep
		// early (fix round 1 minor #3).
		if len(ids) == 0 {
			break
		}
		for _, versionID := range ids {
			if versionID <= 0 {
				continue
			}
			if ctx.Err() != nil {
				errs = append(errs, ctx.Err())
				break
			}
			stats.Scanned++
			// Combined path (H-G4 #27): when one instance is both marker and
			// healer, run both legs sharing a single version-snapshot read.
			//
			// Stats counting nuance (review fix-round M4, disclosed not aligned):
			// SweepVersionOnce runs BOTH legs and joins their errors, returning the
			// flags for what each leg actually did. So a flag is counted here even
			// when err != nil (the error may come from the OTHER leg, or a leg may
			// have marked some reports before failing). The two-call fallback below
			// instead counts a flag only when THAT leg's own err == nil. The
			// difference is purely in the diagnostic stats counters under partial
			// failure — the stale marking / display refresh themselves are identical
			// and correct on both paths (each leg's writes are independent).
			if combined, ok := s.marker.(ReportVersionSweeper); ok && s.display != nil && sameSweepInstance(s.marker, s.display) {
				staled, refreshed, err := combined.SweepVersionOnce(ctx, versionID)
				if err != nil {
					errs = append(errs, err)
				}
				if staled {
					stats.Staled++
				}
				if refreshed {
					stats.DisplayRefreshed++
				}
				continue
			}
			changed, err := s.marker.MarkStaleIfChanged(ctx, versionID)
			if err != nil {
				errs = append(errs, fmt.Errorf("stale sweep version %d: %w", versionID, err))
			} else if changed {
				stats.Staled++
			}
			if s.display != nil {
				refreshed, err := s.display.RefreshDisplayIfChanged(ctx, versionID)
				if err != nil {
					errs = append(errs, fmt.Errorf("display sweep version %d: %w", versionID, err))
				} else if refreshed {
					stats.DisplayRefreshed++
				}
			}
		}
		after = ids[len(ids)-1]
	}
	return stats, errors.Join(errs...)
}
