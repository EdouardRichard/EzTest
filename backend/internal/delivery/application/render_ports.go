package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// P7 report PDF pipeline ports (spec §4.2). The delivery context owns these
// contracts; the composition root adapts the agent work queue and the
// eventing/audit services onto them. No agent-domain type (agent.WorkItem)
// crosses into this package: the render queue exposes a delivery-owned
// RenderJob so the delivery context never depends on the agent context.
const (
	// RenderTaskType is the frozen P7 work item type of one PDF render.
	RenderTaskType = "report.render_pdf"
	// RenderTaskTeam is the owning team of a PDF render work item. It is a
	// deterministic ops task, not an agent-model delivery task: the generic agent
	// worker claims only the five product teams, so "ops" keeps the job safe for
	// the dedicated RenderWorker (which claims by type).
	RenderTaskTeam = "ops"

	// DefaultRenderLease is used when report.render_lease_seconds is missing.
	DefaultRenderLease = 120 * time.Second
	// MinRenderLease/MaxRenderLease mirror the settings catalog bounds. A
	// corrupted settings row outside this range falls back to the default so a
	// heartbeat can never be scheduled at/after the lease expiry.
	MinRenderLease = 30 * time.Second
	MaxRenderLease = 1800 * time.Second
	// RenderHeartbeatFloor is the minimum heartbeat cadence. With the 30s lease
	// floor, lease/3 (>=10s) is always above it, so every configured lease keeps
	// at least two renewals before expiry.
	RenderHeartbeatFloor = 5 * time.Second
	// DefaultRenderMaxAttempts is used when report.render_max_attempts is
	// missing or unreadable.
	DefaultRenderMaxAttempts = 3
	// MinRenderAttempts/MaxRenderAttempts mirror the settings catalog bounds.
	MinRenderAttempts = 1
	MaxRenderAttempts = 10

	// renderEnsureLimit bounds how many pending revisions one reconciler pass
	// ensures (the same 100-row bound as the P4 rules).
	renderEnsureLimit = 100
	// renderMaxPDFError bounds the persisted pdf_error to the schema CHECK.
	renderMaxPDFError = 2000
)

// Renderer sentinels. The renderclient adapter wraps its own errors into these
// so the worker can decide retry vs terminal failure without importing
// infrastructure.
var (
	// ErrRenderUnavailable marks a transient renderer failure (connection
	// refused, timeout, 5xx, busy). The work item retries with backoff.
	ErrRenderUnavailable = errors.New("delivery: renderer is unavailable")
	// ErrRenderRejected marks a non-retryable renderer failure (4xx, invalid
	// response, non-PDF bytes). The revision is marked failed immediately.
	ErrRenderRejected = errors.New("delivery: renderer rejected the request")
)

// RenderJob is the delivery-owned view of one claimed report.render_pdf work
// item. The cmd adapter converts agent.WorkItem into this struct.
type RenderJob struct {
	ID          int64
	Epoch       int64
	VersionID   int64
	Attempts    int
	MaxAttempts int
	Payload     json.RawMessage
}

// RenderQueue claims and settles report.render_pdf work items. The P7 worker
// uses a specialized claim (ClaimForTypes) so it never steals P1-P4 agent work.
type RenderQueue interface {
	ClaimForTypes(ctx context.Context, claimant string, types []string, lease time.Duration) (RenderJob, bool, error)
	// Heartbeat renews the lease of a claimed job. lease must be the same
	// duration the job was claimed with (report.render_lease_seconds), never the
	// agent queue default: renewing shorter than the claim creates a lease gap
	// in which another replica can re-claim and re-render the revision.
	Heartbeat(ctx context.Context, jobID, epoch int64, lease time.Duration) error
	Complete(ctx context.Context, jobID, epoch int64) error
	Fail(ctx context.Context, jobID, epoch int64, cause error, retryable bool) error
}

// ReportContentRef is the content-ledger reference of one stored PDF.
type ReportContentRef struct {
	RefID  int64
	SHA256 string
	Size   int64
}

// RenderContent is the content-ledger surface of the PDF worker: it reads the
// authoritative frozen HTML and writes the rendered PDF under
// owner_kind='report_revision', role='report_pdf'.
type RenderContent interface {
	GetReportHTML(ctx context.Context, contentRefID int64) ([]byte, error)
	PutReportPDF(ctx context.Context, reportRevisionID int64, data []byte) (ReportContentRef, error)
}

// RenderPDFRenderer renders one frozen HTML document to PDF bytes
// (infrastructure/renderclient.Client implements it).
type RenderPDFRenderer interface {
	RenderPDF(ctx context.Context, html []byte) ([]byte, error)
}

// SettingsReader is the delivery context's read port into the settings center
// for the P7 PDF pipeline keys. Values are validated/clamped by the consumer.
type SettingsReader interface {
	Int(ctx context.Context, key string) (int64, error)
	Bool(ctx context.Context, key string) (bool, error)
}

// RenderJobRepository persists the P7 PDF state and the render work items. All
// mutations are fenced: pdf metadata may only move forward from the expected
// state, so a stale worker can never overwrite a completed/failed revision.
// The HTML, as_of, input_fingerprint and template_version columns are never
// touched here (spec §5 #36).
type RenderJobRepository interface {
	// ListRenderableRevisions returns published+rendered revisions whose PDF is
	// pending or failed with attempts left, and still has no pdf_ref, oldest
	// revision first. maxAttempts is the report.render_max_attempts bound: failed
	// revisions at or above it are excluded in SQL so they can never consume the
	// bounded page and starve newer work.
	ListRenderableRevisions(ctx context.Context, limit, maxAttempts int) ([]delivery.RenderableRevision, error)
	// MarkPDFRunning moves pending/failed -> running and increments
	// pdf_attempts. Anything else is a state conflict.
	MarkPDFRunning(ctx context.Context, revisionID int64) error
	// MarkPDFRendered moves running -> done with the ledger ref and timestamp;
	// it is fenced on the running state, an empty pdf_ref and an attempt no
	// older than the current pdf_attempts, so a stale worker cannot settle a
	// newer attempt.
	MarkPDFRendered(ctx context.Context, revisionID int64, ref ReportContentRef, attempts int32, at time.Time) error
	// MarkPDFFailed moves pending/failed/running -> failed with the bounded
	// error message. It is fenced on the worker's attempt (the current
	// pdf_attempts must not be newer) and a done revision is untouched.
	MarkPDFFailed(ctx context.Context, revisionID int64, message string, attempts int32, at time.Time) error
	// MarkPDFDisabled moves a pending revision to disabled (report.render_pdf
	// is off). It is idempotent for an already-disabled revision.
	MarkPDFDisabled(ctx context.Context, revisionID int64) error
	// GetRevisionForRender loads one revision row for the worker. A missing row
	// is ErrReportNotFound.
	GetRevisionForRender(ctx context.Context, revisionID int64) (delivery.ReportRevision, error)
	// EnsureRenderJob inserts the idempotent work item for one revision. The
	// dedupe key is report:{reportID}:rev:{revision}; a repeated ensure returns
	// without touching an existing row.
	EnsureRenderJob(ctx context.Context, revisionID, reportID, versionID int64, dedupeKey string, maxAttempts int) error
}

// renderMaxAttempts resolves report.render_max_attempts with the frozen default
// and clamps it to the settings catalog bounds.
func renderMaxAttempts(ctx context.Context, settings SettingsReader) int {
	if settings == nil {
		return DefaultRenderMaxAttempts
	}
	value, err := settings.Int(ctx, "report.render_max_attempts")
	if err != nil || value < MinRenderAttempts || value > MaxRenderAttempts {
		return DefaultRenderMaxAttempts
	}
	return int(value)
}

// renderLease resolves report.render_lease_seconds with the frozen default and
// clamps it to the settings catalog bounds.
func renderLease(ctx context.Context, settings SettingsReader) time.Duration {
	if settings == nil {
		return DefaultRenderLease
	}
	value, err := settings.Int(ctx, "report.render_lease_seconds")
	if err != nil || value <= 0 {
		return DefaultRenderLease
	}
	lease := time.Duration(value) * time.Second
	if lease < MinRenderLease {
		return MinRenderLease
	}
	if lease > MaxRenderLease {
		return MaxRenderLease
	}
	return lease
}

// renderHeartbeatInterval is the renewal cadence for one lease: lease/3 with a
// 5s floor. Callers pass the same lease they claimed the job with, so every
// renewal happens well before the previous lease expires.
func renderHeartbeatInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < RenderHeartbeatFloor {
		interval = RenderHeartbeatFloor
	}
	return interval
}

// renderPDFEnabled resolves report.render_pdf; an unreadable setting keeps the
// feature enabled so a settings outage cannot silently drop PDF generation.
func renderPDFEnabled(ctx context.Context, settings SettingsReader) bool {
	if settings == nil {
		return true
	}
	enabled, err := settings.Bool(ctx, "report.render_pdf")
	if err != nil {
		return true
	}
	return enabled
}
