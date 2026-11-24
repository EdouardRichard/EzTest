package deliveryadapter

import (
	"context"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// FailureScanner is the issue-service scan entry point
// (*deliveryapp.IssueService). The DeliveryEnsurer contract is error-only, so the
// adapter drops the ingested count.
type FailureScanner interface {
	ScanFailures(ctx context.Context, versionID int64) (int, error)
}

// ReportEnsurer ensures terminal-wave/version reports
// (*deliveryapp.ReportService).
type ReportEnsurer interface {
	EnsureReports(ctx context.Context, versionID int64) error
}

// ClosureEnsurer is the reconciler's deterministic requirement-close hook
// (*versionapp.ClosureService). GD-17: it evaluates the predicate and calls the
// terminal Close entry directly — it does not ensure a work item.
type ClosureEnsurer interface {
	EnsureClosures(ctx context.Context, versionID int64) error
}

// VersionCloseEnsurer is the reconciler's deterministic version-close hook
// (*versionapp.VersionCloseService). GD-17: it evaluates the predicate and calls
// the terminal Close entry directly — it does not ensure a work item.
type VersionCloseEnsurer interface {
	EnsureVersionClose(ctx context.Context, versionID int64) error
}

// Ensurer composes the four delivery reconciler rules behind the agent context's
// DeliveryEnsurer consumer port (spec §4.4/§7.4). The issue/report rules ensure work;
// the two closure rules (GD-17) perform the deterministic close directly.
type Ensurer struct {
	scanner      FailureScanner
	reports      ReportEnsurer
	closures     ClosureEnsurer
	versionClose VersionCloseEnsurer
}

// NewEnsurer wires the delivery reconciler ensurer.
func NewEnsurer(scanner FailureScanner, reports ReportEnsurer, closures ClosureEnsurer, versionClose VersionCloseEnsurer) *Ensurer {
	return &Ensurer{scanner: scanner, reports: reports, closures: closures, versionClose: versionClose}
}

var _ agentapp.DeliveryEnsurer = (*Ensurer)(nil)

// ScanFailures adapts the int-returning issue scan to the error-only
// DeliveryEnsurer contract: the reconciler does not consume the created count.
func (e *Ensurer) ScanFailures(ctx context.Context, versionID int64) error {
	if e == nil || e.scanner == nil {
		return nil
	}
	_, err := e.scanner.ScanFailures(ctx, versionID)
	return err
}

// EnsureReports implements agentapp.DeliveryEnsurer.
func (e *Ensurer) EnsureReports(ctx context.Context, versionID int64) error {
	if e == nil || e.reports == nil {
		return nil
	}
	return e.reports.EnsureReports(ctx, versionID)
}

// EnsureClosures implements agentapp.DeliveryEnsurer.
func (e *Ensurer) EnsureClosures(ctx context.Context, versionID int64) error {
	if e == nil || e.closures == nil {
		return nil
	}
	return e.closures.EnsureClosures(ctx, versionID)
}

// EnsureVersionClose implements agentapp.DeliveryEnsurer.
func (e *Ensurer) EnsureVersionClose(ctx context.Context, versionID int64) error {
	if e == nil || e.versionClose == nil {
		return nil
	}
	return e.versionClose.EnsureVersionClose(ctx, versionID)
}
