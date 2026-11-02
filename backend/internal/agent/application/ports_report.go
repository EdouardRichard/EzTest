package agentapp

import "context"

// ReportFinalizer verifies that the report revision targeted by a
// report.wave/report.version run is published before the run is recorded as
// done. The delivery context implements it structurally (for example
// deliveryapp.ReportFinalizer over ReportService.RequirePublished); the runner
// maps a failure to a retryable task error so the reconciler can ensure the
// next revision after a failed render (spec §4.4/§6.5).
type ReportFinalizer interface {
	Finalize(ctx context.Context, reportID int64, revision int32) error
}
