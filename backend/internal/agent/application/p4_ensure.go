package agentapp

import (
	"context"
	"errors"
	"fmt"
)

// P4Ensurer is the P4 delivery/steward control-plane surface run by the agent
// reconciler on every pass for every running version (spec §4.4/§7.3-§7.5).
//
// Each method only ensures work (issue.triage, report.wave/report.version,
// requirement.close, version.close); it never performs a business action. The
// implementations are idempotent, so the reconciler may call them on every
// tick: delivery.IssueService.ScanFailures, delivery.ReportService.EnsureReports,
// version.ClosureService.EnsureClosures and
// version.VersionCloseService.EnsureVersionClose (wired by Task 23). The
// interface keeps this package independent of the unfinished delivery/version
// implementations; ScanFailures returns only the error because the reconciler
// does not consume the ingested count.
type P4Ensurer interface {
	ScanFailures(ctx context.Context, versionID int64) error
	EnsureReports(ctx context.Context, versionID int64) error
	EnsureClosures(ctx context.Context, versionID int64) error
	EnsureVersionClose(ctx context.Context, versionID int64) error
}

// P4VersionLister lists the IDs of the running versions the P4 rules reconcile.
// The version context implements it (versionapp.Service.ListRunningVersions).
// Completed versions are never returned: their only P4 rule is the reopen scan,
// which the requirement/design/execution hooks own (spec §7.5), so the
// reconciler must not run an ensure pass for them.
type P4VersionLister interface {
	ListRunningVersions(ctx context.Context, limit int) ([]int64, error)
}

// reconcileP4 runs the P4 delivery/steward ensure rules for one pass. It is the
// last reconciler rule so a P4 failure can never prevent the P1-P3 rules from
// running; Reconcile aggregates its error. Every rule is independent: a failing
// ensure for one version does not stop the remaining rules or versions.
func (r *Reconciler) reconcileP4(ctx context.Context) error {
	if r.p4 == nil || r.runningVersions == nil {
		return nil
	}
	versionIDs, err := r.runningVersions.ListRunningVersions(ctx, r.limit)
	if err != nil {
		return fmt.Errorf("list running versions: %w", err)
	}
	var errs []error
	for _, versionID := range versionIDs {
		if versionID <= 0 {
			continue
		}
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := r.p4.ScanFailures(ctx, versionID); err != nil {
			errs = append(errs, fmt.Errorf("p4 scan failures for version %d: %w", versionID, err))
		}
		if err := r.p4.EnsureReports(ctx, versionID); err != nil {
			errs = append(errs, fmt.Errorf("p4 ensure reports for version %d: %w", versionID, err))
		}
		if err := r.p4.EnsureClosures(ctx, versionID); err != nil {
			errs = append(errs, fmt.Errorf("p4 ensure closures for version %d: %w", versionID, err))
		}
		if err := r.p4.EnsureVersionClose(ctx, versionID); err != nil {
			errs = append(errs, fmt.Errorf("p4 ensure version close for version %d: %w", versionID, err))
		}
	}
	return errors.Join(errs...)
}
