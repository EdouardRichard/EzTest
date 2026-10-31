package p4

import (
	"context"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// FailureScanner is the issue-service scan entry point
// (*deliveryapp.IssueService). The P4Ensurer contract is error-only, so the
// adapter drops the ingested count.
type FailureScanner interface {
	ScanFailures(ctx context.Context, versionID int64) (int, error)
}

// ReportEnsurer ensures terminal-wave/version reports
// (*deliveryapp.ReportService).
type ReportEnsurer interface {
	EnsureReports(ctx context.Context, versionID int64) error
}

// ClosureEnsurer ensures requirement.close tasks
// (*versionapp.ClosureService).
type ClosureEnsurer interface {
	EnsureClosures(ctx context.Context, versionID int64) error
}

// VersionCloseEnsurer ensures version.close tasks
// (*versionapp.VersionCloseService).
type VersionCloseEnsurer interface {
	EnsureVersionClose(ctx context.Context, versionID int64) error
}

// Ensurer composes the four P4 reconciler rules behind the agent context's
// P4Ensurer consumer port (spec §4.4/§7.4). The reconciler only ensures work;
// no business action happens here.
type Ensurer struct {
	scanner      FailureScanner
	reports      ReportEnsurer
	closures     ClosureEnsurer
	versionClose VersionCloseEnsurer
}

// NewEnsurer wires the P4 reconciler ensurer.
func NewEnsurer(scanner FailureScanner, reports ReportEnsurer, closures ClosureEnsurer, versionClose VersionCloseEnsurer) *Ensurer {
	return &Ensurer{scanner: scanner, reports: reports, closures: closures, versionClose: versionClose}
}

var _ agentapp.P4Ensurer = (*Ensurer)(nil)

// ScanFailures adapts the int-returning issue scan to the error-only
// P4Ensurer contract: the reconciler does not consume the created count.
func (e *Ensurer) ScanFailures(ctx context.Context, versionID int64) error {
	if e == nil || e.scanner == nil {
		return nil
	}
	_, err := e.scanner.ScanFailures(ctx, versionID)
	return err
}

// EnsureReports implements agentapp.P4Ensurer.
func (e *Ensurer) EnsureReports(ctx context.Context, versionID int64) error {
	if e == nil || e.reports == nil {
		return nil
	}
	return e.reports.EnsureReports(ctx, versionID)
}

// EnsureClosures implements agentapp.P4Ensurer.
func (e *Ensurer) EnsureClosures(ctx context.Context, versionID int64) error {
	if e == nil || e.closures == nil {
		return nil
	}
	return e.closures.EnsureClosures(ctx, versionID)
}

// EnsureVersionClose implements agentapp.P4Ensurer.
func (e *Ensurer) EnsureVersionClose(ctx context.Context, versionID int64) error {
	if e == nil || e.versionClose == nil {
		return nil
	}
	return e.versionClose.EnsureVersionClose(ctx, versionID)
}
