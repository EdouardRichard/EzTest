package storageapp

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// GC settings keys (spec §8) and the P7 retention values GC consumes.
const (
	GCSettingTempHours               = "retention.temp_hours"
	GCSettingReplacedExportDays      = "retention.replaced_export_days"
	GCSettingUnreferencedContentDays = "retention.unreferenced_content_days"
	GCSettingGraceHours              = "retention.gc_grace_hours"
	GCSettingBatchSize               = "retention.gc_batch_size"
	GCSettingDryRun                  = "retention.gc_dry_run"
	GCSettingIntervalMinutes         = "retention.gc_interval_minutes"

	// DefaultGCFallback* are used only when a settings read fails in the
	// periodic worker; an API-triggered run fails instead of guessing.
	DefaultGCTempHours               int64 = 24
	DefaultGCReplacedExportDays      int64 = 7
	DefaultGCUnreferencedContentDays int64 = 30
	DefaultGCGraceHours              int64 = 24
	DefaultGCBatchSize                     = 500
	MaxGCBatchSize                         = 5000
	DefaultGCInterval                      = 24 * time.Hour
	MinGCInterval                          = time.Minute
)

// GCSettingsReader reads the typed settings a GC pass needs. It is separate
// from SettingsReader (which only reads strings) so existing content-service
// fakes stay valid.
type GCSettingsReader interface {
	String(ctx context.Context, key string) (string, error)
	Int(ctx context.Context, key string) (int64, error)
	Bool(ctx context.Context, key string) (bool, error)
}

// OwnerLookup answers whether a content_ref owner entity still exists. GC never
// deletes a ref whose owner exists. An unknown owner_kind is not looked up at
// all: it is protected and reported (fail-safe, spec §4.4).
type OwnerLookup interface {
	// ReportRevisionExists reports a delivery_report_revision row. Report HTML
	// and PDF refs are protected regardless; this lookup covers other roles.
	ReportRevisionExists(ctx context.Context, revisionID int64) (bool, error)
	// AttachmentExists reports a requirement_attachment row whose version also
	// still exists (spec §4.4 protection rule).
	AttachmentExists(ctx context.Context, attachmentID int64) (bool, error)
	// EvidenceExists reports an evidence row.
	EvidenceExists(ctx context.Context, evidenceID int64) (bool, error)
	// ExportArtifactExists reports an export_artifact row.
	ExportArtifactExists(ctx context.Context, artifactID int64) (bool, error)
}

// FileInfo is one regular file found under {data_root}/objects.
type FileInfo struct {
	// SHA256 is the object key when RelativePath uses the ledger layout
	// (objects/aa/bb/<sha> or the ".<sha>.tmp-*" staging name); otherwise it is
	// empty and GC ignores the file.
	SHA256       string
	RelativePath string
	Size         int64
	ModTime      time.Time
}

// GCFileStore scans and removes immutable blobs for GC. The root is passed per
// call so it always reflects the current paths.data_root setting.
type GCFileStore interface {
	// ScanObjects walks {root}/objects and returns every regular file.
	ScanObjects(ctx context.Context, root string) ([]FileInfo, error)
	// DeleteObject removes one root-relative ledger path. A missing file is
	// reported as os.ErrNotExist (GC records it as "missing", not an error).
	DeleteObject(ctx context.Context, root, relativePath string) error
	// ProbeWritable writes and removes {root}/.probe-<hash(instanceKey)> to
	// prove the shared data root is writable (readiness, spec §4.5).
	ProbeWritable(ctx context.Context, root, instanceKey string) error
}

// GCRef is one live content_ref joined with its object metadata, as returned by
// the paged ref scan.
type GCRef struct {
	RefID           int64
	ObjectSHA       string
	Size            int64
	MIME            string
	RelativePath    string
	OwnerKind       string
	OwnerID         int64
	Role            string
	ObjectCreatedAt time.Time
	RefCreatedAt    time.Time
}

// SupersededExport is an export_artifact replaced by a newer revision of the
// same (version_id, owner_kind, owner_id, file_format) tuple, joined with the
// content_ref that points at its bytes.
type SupersededExport struct {
	ArtifactID        int64
	VersionID         int64
	OwnerKind         string
	OwnerID           int64
	Format            string
	Revision          int
	RefID             int64
	ObjectSHA         string
	Size              int64
	MIME              string
	RelativePath      string
	ArtifactCreatedAt time.Time
	ObjectCreatedAt   time.Time
}

// GCRepository persists gc_run/gc_tombstone rows and provides the paged ledger
// scans GC needs. Owner existence is deliberately NOT checked in SQL; the
// application calls OwnerLookup instead.
type GCRepository interface {
	// CreateRun inserts a running run. It returns storage.ErrGCRunning when the
	// single-running partial unique index is violated.
	CreateRun(ctx context.Context, run storage.GCRun) (storage.GCRun, error)
	// UpdateRun persists the terminal status/report/counters of a run.
	UpdateRun(ctx context.Context, run storage.GCRun) error
	// GetRun returns one run or storage.ErrNotFound.
	GetRun(ctx context.Context, id int64) (storage.GCRun, error)
	// ListRuns returns runs in ID-descending order.
	ListRuns(ctx context.Context, limit int) ([]storage.GCRun, error)
	// CreateTombstones appends object tombstone rows before their deletion.
	CreateTombstones(ctx context.Context, tombstones []storage.GCTombstone) error
	// ListTombstones returns one run's tombstones in ID order.
	ListTombstones(ctx context.Context, runID int64) ([]storage.GCTombstone, error)
	// ListRefsPage returns live refs with ID > afterID ordered by ID.
	ListRefsPage(ctx context.Context, afterID int64, limit int) ([]GCRef, error)
	// LiveRefCounts returns the number of live refs per object SHA.
	LiveRefCounts(ctx context.Context, shas []string) (map[string]int, error)
	// DeleteRefs soft-deletes the given refs; already-deleted/missing ids are
	// ignored so concurrent GC passes converge.
	DeleteRefs(ctx context.Context, ids []int64) error
	// ListUnreferencedObjects returns content objects created before olderThan
	// with no live ref, oldest first.
	ListUnreferencedObjects(ctx context.Context, olderThan time.Time, limit int) ([]storage.Object, error)
	// DeleteObjects removes the content_object rows whose SHA is given and for
	// which no live ref remains (hard-deleting their soft-deleted refs), and
	// returns the SHAs that were actually deleted.
	DeleteObjects(ctx context.Context, shas []string) ([]string, error)
	// ListSupersededExports returns replaced export artifacts older than
	// olderThan, including the content_ref that owns their bytes.
	ListSupersededExports(ctx context.Context, olderThan time.Time, limit int) ([]SupersededExport, error)
	// ExistingObjectSHAs returns the subset of shas present in content_object.
	ExistingObjectSHAs(ctx context.Context, shas []string) ([]string, error)
}
