package storageapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// Content-ledger owner kinds and roles GC protects (spec §4.4). They mirror the
// constants owned by the delivery/version/execution adapters; the ledger itself
// treats owner_kind as an opaque string.
const (
	OwnerKindReportRevision        = "report_revision"
	OwnerKindRequirementAttachment = "requirement_attachment"
	OwnerKindEvidence              = "evidence"
	RoleReportHTML                 = "report_html"
	RoleReportPDF                  = "report_pdf"
	GCReportSampleLimit            = 200
	DefaultGCRunLimit              = 20
	MaxGCRunLimit                  = 100
	gcReportErrorLimit             = 20
	// gcFinishTimeout bounds the detached terminal-state write.
	gcFinishTimeout = 10 * time.Second
)

// Protected reasons recorded in the GC report.
const (
	GCProtectedReportContent    = "report_content"
	GCProtectedOwnerExists      = "owner_exists"
	GCProtectedUnknownOwnerKind = "unknown_owner_kind"
	GCProtectedGrace            = "grace"
	GCProtectedLiveRefs         = "live_refs"
	GCProtectedMissingRef       = "missing_ref"
)

// GCReportSettings records the retention values a pass used.
type GCReportSettings struct {
	TempHours               int64 `json:"temp_hours"`
	ReplacedExportDays      int64 `json:"replaced_export_days"`
	UnreferencedContentDays int64 `json:"unreferenced_content_days"`
	GraceHours              int64 `json:"grace_hours"`
	BatchSize               int   `json:"batch_size"`
}

// GCReportScanned records how many ledger rows/files the pass examined.
type GCReportScanned struct {
	Objects         int `json:"objects"`
	Refs            int `json:"refs"`
	Files           int `json:"files"`
	ExportArtifacts int `json:"export_artifacts"`
}

// GCReportActions counts planned or performed deletions.
type GCReportActions struct {
	Objects int   `json:"objects"`
	Refs    int   `json:"refs"`
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
}

// GCReportFileResults counts physical file deletions.
type GCReportFileResults struct {
	Deleted int `json:"deleted"`
	Missing int `json:"missing"`
	Failed  int `json:"failed"`
}

// GCCandidateSample is one candidate in the capped report sample.
type GCCandidateSample struct {
	Class            string `json:"class"`
	ObjectSHA        string `json:"object_sha"`
	Size             int64  `json:"size"`
	OwnerKind        string `json:"owner_kind,omitempty"`
	OwnerID          int64  `json:"owner_id,omitempty"`
	RefID            int64  `json:"ref_id,omitempty"`
	ExportArtifactID int64  `json:"export_artifact_id,omitempty"`
	AgeSeconds       int64  `json:"age_seconds"`
	DeleteObject     bool   `json:"delete_object"`
	DeleteRef        bool   `json:"delete_ref"`
}

// GCProtectedSample is one protected ledger row in the capped report sample.
type GCProtectedSample struct {
	ObjectSHA  string `json:"object_sha,omitempty"`
	OwnerKind  string `json:"owner_kind,omitempty"`
	OwnerID    int64  `json:"owner_id,omitempty"`
	RefID      int64  `json:"ref_id,omitempty"`
	Reason     string `json:"reason"`
	AgeSeconds int64  `json:"age_seconds"`
}

// GCReport is the JSONB report stored on gc_run. Counts are complete; samples
// are capped at GCReportSampleLimit entries.
type GCReport struct {
	Mode              string              `json:"mode"`
	DryRun            bool                `json:"dry_run"`
	Status            string              `json:"status"`
	StartedAt         time.Time           `json:"started_at"`
	FinishedAt        *time.Time          `json:"finished_at,omitempty"`
	Error             string              `json:"error,omitempty"`
	Settings          GCReportSettings    `json:"settings"`
	Scanned           GCReportScanned     `json:"scanned"`
	Candidates        map[string]int      `json:"candidates"`
	CandidateSamples  []GCCandidateSample `json:"candidate_samples"`
	Protected         map[string]int      `json:"protected"`
	ProtectedSamples  []GCProtectedSample `json:"protected_samples"`
	SamplesTruncated  bool                `json:"samples_truncated"`
	UnknownOwnerKinds []string            `json:"unknown_owner_kinds"`
	Planned           GCReportActions     `json:"planned"`
	Deleted           GCReportActions     `json:"deleted"`
	Files             GCReportFileResults `json:"files"`
	Tombstones        int                 `json:"tombstones"`
	Errors            []string            `json:"errors,omitempty"`

	selected     []gcCandidate
	unknownKinds map[string]struct{}
}

// gcCandidate is the internal, pre-report form of one selected candidate.
type gcCandidate struct {
	Object           storage.Object
	Class            string
	RefID            int64
	OwnerKind        string
	OwnerID          int64
	ExportArtifactID int64
	AgeSeconds       int64
	DeleteObject     bool
	DeletionReason   string
}

// gcSettings is the resolved retention configuration of one pass.
type gcSettings struct {
	DataRoot                string
	TempHours               int64
	ReplacedExportDays      int64
	UnreferencedContentDays int64
	GraceHours              int64
	BatchSize               int
}

// GCRunDetail is one run plus its tombstones (GET /api/admin/gc/runs/{id}).
type GCRunDetail struct {
	Run        storage.GCRun
	Tombstones []storage.GCTombstone
}

// GCService scans the content ledger for garbage and, in apply mode, deletes
// the reported candidates. It is safe for the leader-scheduled worker and the
// manual admin API to share one instance: the database's single-running index
// rejects concurrent runs.
type GCService struct {
	repo     GCRepository
	owners   OwnerLookup
	files    GCFileStore
	settings GCSettingsReader
	ids      platformid.Generator
	clock    clock.Clock
	audit    auditapp.Recorder
	events   eventingapp.Publisher
}

// NewGCService wires the content-ledger GC service. audit and events are
// best-effort sinks and may be nil in tests.
func NewGCService(
	repo GCRepository,
	owners OwnerLookup,
	files GCFileStore,
	settings GCSettingsReader,
	ids platformid.Generator,
	clk clock.Clock,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
) *GCService {
	if clk == nil {
		clk = clock.System{}
	}
	return &GCService{repo: repo, owners: owners, files: files, settings: settings, ids: ids, clock: clk, audit: audit, events: events}
}

// Run executes one GC pass. Dry-run writes the report only; apply deletes the
// selected candidates. Apply-mode confirmation is a transport concern: the HTTP
// handler requires confirm=true (spec §6.2).
func (s *GCService) Run(ctx context.Context, mode storage.GCMode, actorID int64) (storage.GCRun, error) {
	if !mode.Valid() {
		return storage.GCRun{}, platform.Invalid("mode", "mode must be dry_run or apply")
	}
	if s == nil || s.repo == nil || s.owners == nil || s.files == nil || s.settings == nil {
		return storage.GCRun{}, platform.NewError(http.StatusInternalServerError, "internal", "gc service is not configured")
	}
	if s.ids == nil {
		return storage.GCRun{}, platform.NewError(http.StatusInternalServerError, "internal", "id generator is not configured")
	}
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return storage.GCRun{}, err
	}

	now := s.clock.Now().UTC()
	leaseUntil := now.Add(GCLease)
	run := storage.GCRun{
		ID:        s.ids.Next(),
		Mode:      mode,
		Status:    storage.GCRunRunning,
		ActorKind: storage.GCActorSystem,
		StartedAt: now,
		// GCLease fences a running pass: a crashed pass leaves this lease
		// behind and the next CreateRun reclaims it after expiry.
		LeaseOwner: GCRunLeaseOwner,
		LeaseUntil: &leaseUntil,
		Report:     json.RawMessage(`{}`),
	}
	if actorID > 0 {
		actor := actorID
		run.ActorID = &actor
		run.ActorKind = storage.GCActorUser
	}
	if err := run.Validate(); err != nil {
		return storage.GCRun{}, err
	}
	created, err := s.repo.CreateRun(ctx, run)
	if err != nil {
		if errors.Is(err, storage.ErrGCRunning) {
			return storage.GCRun{}, platform.NewError(http.StatusConflict, "gc_running", "another GC run is already running")
		}
		return storage.GCRun{}, fmt.Errorf("create gc run: %w", err)
	}

	report := newGCReport(mode, settings, now)
	candidates, err := s.scan(ctx, settings, now, report)
	if err != nil {
		return s.finish(ctx, created, report, err)
	}
	if mode == storage.GCModeApply {
		if err := s.apply(ctx, created.ID, settings, now, candidates, report); err != nil {
			return s.finish(ctx, created, report, err)
		}
	}
	return s.finish(ctx, created, report, nil)
}

// loadSettings resolves the retention values of one pass. The settings catalog
// constrains these keys; the defensive clamps only guard against a broken
// settings row, never against a legitimate value.
func (s *GCService) loadSettings(ctx context.Context) (gcSettings, error) {
	root, err := s.settings.String(ctx, DataRootSettingKey)
	if err != nil {
		return gcSettings{}, fmt.Errorf("read %s: %w", DataRootSettingKey, err)
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return gcSettings{}, platform.NewError(http.StatusInternalServerError, "internal", "paths.data_root is not configured")
	}
	intSetting := func(key string, fallback int64, min, max int64) (int64, error) {
		value, err := s.settings.Int(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", key, err)
		}
		if value < min {
			value = fallback
		}
		if max > 0 && value > max {
			value = max
		}
		return value, nil
	}
	tempHours, err := intSetting(GCSettingTempHours, DefaultGCTempHours, 1, 0)
	if err != nil {
		return gcSettings{}, err
	}
	replacedDays, err := intSetting(GCSettingReplacedExportDays, DefaultGCReplacedExportDays, 1, 0)
	if err != nil {
		return gcSettings{}, err
	}
	unreferencedDays, err := intSetting(GCSettingUnreferencedContentDays, DefaultGCUnreferencedContentDays, 1, 0)
	if err != nil {
		return gcSettings{}, err
	}
	graceHours, err := intSetting(GCSettingGraceHours, DefaultGCGraceHours, 1, 0)
	if err != nil {
		return gcSettings{}, err
	}
	batchSize, err := intSetting(GCSettingBatchSize, DefaultGCBatchSize, 1, MaxGCBatchSize)
	if err != nil {
		return gcSettings{}, err
	}
	return gcSettings{
		DataRoot:                root,
		TempHours:               tempHours,
		ReplacedExportDays:      replacedDays,
		UnreferencedContentDays: unreferencedDays,
		GraceHours:              graceHours,
		BatchSize:               int(batchSize),
	}, nil
}

// scan classifies the four candidate classes and fills the report's counts,
// samples and protected reasons.
func (s *GCService) scan(ctx context.Context, settings gcSettings, now time.Time, report *GCReport) ([]gcCandidate, error) {
	selected := make([]gcCandidate, 0)
	perClass := map[string]int{}
	selectCandidates := func(found []gcCandidate) {
		for _, candidate := range found {
			if perClass[candidate.Class] >= settings.BatchSize {
				report.SamplesTruncated = true
				continue
			}
			perClass[candidate.Class]++
			selected = append(selected, candidate)
			report.Candidates[candidate.Class]++
		}
	}

	orphans, err := s.scanRefs(ctx, settings, now, report)
	if err != nil {
		return nil, err
	}
	superseded, err := s.scanSupersededExports(ctx, settings, now, report)
	if err != nil {
		return nil, err
	}
	unreferenced, err := s.scanUnreferencedObjects(ctx, settings, now, report)
	if err != nil {
		return nil, err
	}
	temp, err := s.scanTempFiles(ctx, settings, now, report)
	if err != nil {
		return nil, err
	}

	selectCandidates(orphans)
	selectCandidates(superseded)
	selectCandidates(unreferenced)
	selectCandidates(temp)

	// Decide which ref candidates can also collect their object. The object is
	// handled "as class 1" (spec §4.4): only when this is its only live ref and
	// it already satisfies the unreferenced-retention window.
	if err := s.assignObjectDeletion(ctx, settings, now, selected); err != nil {
		return nil, err
	}
	for _, candidate := range selected {
		report.plan(candidate)
	}
	report.selected = selected
	report.finalize()
	return selected, nil
}

// scanRefs pages through every live content_ref and classifies orphan refs.
// Owner existence is resolved through OwnerLookup; unknown owner kinds are
// protected and reported (never deleted).
func (s *GCService) scanRefs(ctx context.Context, settings gcSettings, now time.Time, report *GCReport) ([]gcCandidate, error) {
	found := make([]gcCandidate, 0)
	afterID := int64(0)
	for {
		refs, err := s.repo.ListRefsPage(ctx, afterID, settings.BatchSize)
		if err != nil {
			return nil, fmt.Errorf("list content refs: %w", err)
		}
		for _, ref := range refs {
			afterID = ref.RefID
			report.Scanned.Refs++
			age := nonNegativeAge(now, ref.ObjectCreatedAt)
			exists, known, err := s.ownerExists(ctx, ref.OwnerKind, ref.OwnerID)
			if err != nil {
				return nil, err
			}
			if !known {
				report.noteUnknownOwnerKind(ref.OwnerKind)
				report.protectRef(ref, GCProtectedUnknownOwnerKind, age)
				continue
			}
			if ref.OwnerKind == OwnerKindReportRevision && (ref.Role == RoleReportHTML || ref.Role == RoleReportPDF) {
				// Old report revisions never lose their HTML/PDF bytes (OD-4).
				report.protectRef(ref, GCProtectedReportContent, age)
				continue
			}
			if exists {
				report.protectRef(ref, GCProtectedOwnerExists, age)
				continue
			}
			if time.Duration(age)*time.Second < time.Duration(settings.GraceHours)*time.Hour {
				report.protectRef(ref, GCProtectedGrace, age)
				continue
			}
			found = append(found, gcCandidate{
				Object: storage.Object{
					SHA256:       ref.ObjectSHA,
					Size:         ref.Size,
					MIME:         ref.MIME,
					RelativePath: ref.RelativePath,
					CreatedAt:    ref.ObjectCreatedAt,
				},
				Class:          storage.GCCandidateOrphanFile,
				RefID:          ref.RefID,
				OwnerKind:      ref.OwnerKind,
				OwnerID:        ref.OwnerID,
				AgeSeconds:     age,
				DeletionReason: storage.GCCandidateOrphanFile,
			})
		}
		if len(refs) < settings.BatchSize {
			return found, nil
		}
	}
}

// ownerExists maps an owner_kind to its OwnerLookup probe. known=false marks an
// unknown owner_kind, which is protected fail-safe.
func (s *GCService) ownerExists(ctx context.Context, ownerKind string, ownerID int64) (exists, known bool, err error) {
	switch ownerKind {
	case OwnerKindReportRevision:
		exists, err = s.owners.ReportRevisionExists(ctx, ownerID)
	case OwnerKindRequirementAttachment:
		exists, err = s.owners.AttachmentExists(ctx, ownerID)
	case OwnerKindEvidence:
		exists, err = s.owners.EvidenceExists(ctx, ownerID)
	case storage.OwnerKindExport:
		exists, err = s.owners.ExportArtifactExists(ctx, ownerID)
	default:
		return false, false, nil
	}
	if err != nil {
		return false, true, fmt.Errorf("look up %s owner %d: %w", ownerKind, ownerID, err)
	}
	return exists, true, nil
}

// scanSupersededExports classifies export artifacts replaced by a newer
// revision of the same (version, owner_kind, owner_id, file_format) tuple.
func (s *GCService) scanSupersededExports(ctx context.Context, settings gcSettings, now time.Time, report *GCReport) ([]gcCandidate, error) {
	olderThan := now.Add(-durationAtLeast(
		time.Duration(settings.ReplacedExportDays)*24*time.Hour,
		time.Duration(settings.GraceHours)*time.Hour,
	))
	exports, err := s.repo.ListSupersededExports(ctx, olderThan, settings.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("list superseded exports: %w", err)
	}
	report.Scanned.ExportArtifacts = len(exports)
	report.Scanned.Objects += len(exports)
	found := make([]gcCandidate, 0, len(exports))
	for _, artifact := range exports {
		age := nonNegativeAge(now, artifact.ObjectCreatedAt)
		exists, err := s.owners.ExportArtifactExists(ctx, artifact.ArtifactID)
		if err != nil {
			return nil, fmt.Errorf("look up export artifact %d: %w", artifact.ArtifactID, err)
		}
		if !exists {
			report.protect(GCProtectedOwnerExists, "", storage.OwnerKindExport, artifact.ArtifactID, artifact.RefID, age)
			continue
		}
		if artifact.RefID <= 0 {
			report.protect(GCProtectedMissingRef, "", storage.OwnerKindExport, artifact.ArtifactID, 0, age)
			continue
		}
		if time.Duration(age)*time.Second < time.Duration(settings.GraceHours)*time.Hour {
			report.protect(GCProtectedGrace, "", storage.OwnerKindExport, artifact.ArtifactID, artifact.RefID, age)
			continue
		}
		found = append(found, gcCandidate{
			Object: storage.Object{
				SHA256:       artifact.ObjectSHA,
				Size:         artifact.Size,
				MIME:         artifact.MIME,
				RelativePath: artifact.RelativePath,
				CreatedAt:    artifact.ObjectCreatedAt,
			},
			Class:            storage.GCCandidateSupersededExport,
			RefID:            artifact.RefID,
			OwnerKind:        storage.OwnerKindExport,
			OwnerID:          artifact.ArtifactID,
			ExportArtifactID: artifact.ArtifactID,
			AgeSeconds:       age,
			DeletionReason:   storage.GCCandidateSupersededExport,
		})
	}
	return found, nil
}

// scanUnreferencedObjects classifies content objects with no live ref that are
// older than max(unreferenced_content_days, gc_grace_hours).
func (s *GCService) scanUnreferencedObjects(ctx context.Context, settings gcSettings, now time.Time, report *GCReport) ([]gcCandidate, error) {
	retention := durationAtLeast(
		time.Duration(settings.UnreferencedContentDays)*24*time.Hour,
		time.Duration(settings.GraceHours)*time.Hour,
	)
	objects, err := s.repo.ListUnreferencedObjects(ctx, now.Add(-retention), settings.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("list unreferenced objects: %w", err)
	}
	report.Scanned.Objects += len(objects)
	shas := make([]string, 0, len(objects))
	for _, object := range objects {
		shas = append(shas, object.SHA256)
	}
	counts := map[string]int{}
	if len(shas) > 0 {
		counts, err = s.repo.LiveRefCounts(ctx, shas)
		if err != nil {
			return nil, fmt.Errorf("read live ref counts: %w", err)
		}
	}
	found := make([]gcCandidate, 0, len(objects))
	for _, object := range objects {
		age := nonNegativeAge(now, object.CreatedAt)
		if counts[object.SHA256] > 0 {
			// A ref appeared between the scan and the count: protect the object.
			report.protect(GCProtectedLiveRefs, object.SHA256, "", 0, 0, age)
			continue
		}
		found = append(found, gcCandidate{
			Object:         object,
			Class:          storage.GCCandidateUnreferenced,
			AgeSeconds:     age,
			DeleteObject:   true,
			DeletionReason: storage.GCCandidateUnreferenced,
		})
	}
	return found, nil
}

// scanTempFiles classifies orphan files under objects/ with no content_object
// row and an mtime older than max(temp_hours, gc_grace_hours).
func (s *GCService) scanTempFiles(ctx context.Context, settings gcSettings, now time.Time, report *GCReport) ([]gcCandidate, error) {
	files, err := s.files.ScanObjects(ctx, settings.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("scan content files: %w", err)
	}
	report.Scanned.Files = len(files)
	shas := make([]string, 0, len(files))
	bySHA := make(map[string]FileInfo, len(files))
	for _, file := range files {
		if file.SHA256 == "" || !storage.ValidSHA256(file.SHA256) {
			continue
		}
		shas = append(shas, file.SHA256)
		bySHA[file.SHA256] = file
	}
	existing := map[string]struct{}{}
	if len(shas) > 0 {
		known, err := s.repo.ExistingObjectSHAs(ctx, shas)
		if err != nil {
			return nil, fmt.Errorf("read existing object shas: %w", err)
		}
		for _, sha := range known {
			existing[sha] = struct{}{}
		}
	}
	retention := durationAtLeast(
		time.Duration(settings.TempHours)*time.Hour,
		time.Duration(settings.GraceHours)*time.Hour,
	)
	found := make([]gcCandidate, 0)
	for _, sha := range shas {
		file := bySHA[sha]
		age := nonNegativeAge(now, file.ModTime)
		if _, ok := existing[sha]; ok {
			continue
		}
		if time.Duration(age)*time.Second < retention {
			report.protect(GCProtectedGrace, sha, "", 0, 0, age)
			continue
		}
		found = append(found, gcCandidate{
			Object: storage.Object{
				SHA256:       sha,
				Size:         file.Size,
				MIME:         "application/octet-stream",
				RelativePath: file.RelativePath,
				CreatedAt:    file.ModTime,
			},
			Class:          storage.GCCandidateTemp,
			AgeSeconds:     age,
			DeleteObject:   true,
			DeletionReason: storage.GCCandidateTemp,
		})
	}
	return found, nil
}

// assignObjectDeletion decides, for every selected ref candidate, whether the
// object can also be collected in this pass.
func (s *GCService) assignObjectDeletion(ctx context.Context, settings gcSettings, now time.Time, candidates []gcCandidate) error {
	shas := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate.RefID == 0 {
			continue
		}
		if _, ok := seen[candidate.Object.SHA256]; ok {
			continue
		}
		seen[candidate.Object.SHA256] = struct{}{}
		shas = append(shas, candidate.Object.SHA256)
	}
	if len(shas) == 0 {
		return nil
	}
	counts, err := s.repo.LiveRefCounts(ctx, shas)
	if err != nil {
		return fmt.Errorf("read live ref counts: %w", err)
	}
	retention := durationAtLeast(
		time.Duration(settings.UnreferencedContentDays)*24*time.Hour,
		time.Duration(settings.GraceHours)*time.Hour,
	)
	cutoff := now.Add(-retention)
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.RefID == 0 {
			continue
		}
		if counts[candidate.Object.SHA256] == 1 && candidate.Object.CreatedAt.Before(cutoff) {
			candidate.DeleteObject = true
		}
	}
	return nil
}

// apply performs the deletions. Per candidate the order is tombstone → ref →
// content_object row → file (spec §4.4); a missing file is recorded, not fatal.
// Failures are recorded in the report and do not stop the rest of the pass,
// except for context cancellation.
func (s *GCService) apply(ctx context.Context, runID int64, settings gcSettings, now time.Time, candidates []gcCandidate, report *GCReport) error {
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch candidate.Class {
		case storage.GCCandidateTemp:
			if err := s.writeTombstone(ctx, runID, candidate, now, report); err != nil {
				report.recordError(err)
				continue
			}
			if s.deleteFile(ctx, settings.DataRoot, candidate, report) {
				report.Deleted.Files++
				report.Deleted.Bytes += candidate.Object.Size
			}
		case storage.GCCandidateUnreferenced:
			counts, err := s.repo.LiveRefCounts(ctx, []string{candidate.Object.SHA256})
			if err != nil {
				report.recordError(err)
				continue
			}
			if counts[candidate.Object.SHA256] > 0 {
				report.protect(GCProtectedLiveRefs, candidate.Object.SHA256, "", 0, 0, candidate.AgeSeconds)
				continue
			}
			if err := s.writeTombstone(ctx, runID, candidate, now, report); err != nil {
				report.recordError(err)
				continue
			}
			deleted, err := s.repo.DeleteObjects(ctx, []string{candidate.Object.SHA256})
			if err != nil {
				report.recordError(err)
				continue
			}
			if len(deleted) != 1 {
				report.protect(GCProtectedLiveRefs, candidate.Object.SHA256, "", 0, 0, candidate.AgeSeconds)
				continue
			}
			report.Deleted.Objects++
			report.Deleted.Bytes += candidate.Object.Size
			if s.deleteFile(ctx, settings.DataRoot, candidate, report) {
				report.Deleted.Files++
			}
		case storage.GCCandidateSupersededExport, storage.GCCandidateOrphanFile:
			counts, err := s.repo.LiveRefCounts(ctx, []string{candidate.Object.SHA256})
			if err != nil {
				report.recordError(err)
				continue
			}
			deleteObject := candidate.DeleteObject && counts[candidate.Object.SHA256] == 1
			if deleteObject {
				if err := s.writeTombstone(ctx, runID, candidate, now, report); err != nil {
					report.recordError(err)
					continue
				}
			}
			if err := s.repo.DeleteRefs(ctx, []int64{candidate.RefID}); err != nil {
				report.recordError(err)
				continue
			}
			report.Deleted.Refs++
			if !deleteObject {
				continue
			}
			deleted, err := s.repo.DeleteObjects(ctx, []string{candidate.Object.SHA256})
			if err != nil {
				report.recordError(err)
				continue
			}
			if len(deleted) != 1 {
				report.protect(GCProtectedLiveRefs, candidate.Object.SHA256, candidate.OwnerKind, candidate.OwnerID, candidate.RefID, candidate.AgeSeconds)
				continue
			}
			report.Deleted.Objects++
			report.Deleted.Bytes += candidate.Object.Size
			if s.deleteFile(ctx, settings.DataRoot, candidate, report) {
				report.Deleted.Files++
			}
		}
	}
	return nil
}

// writeTombstone records one object tombstone before its rows/bytes disappear.
func (s *GCService) writeTombstone(ctx context.Context, runID int64, candidate gcCandidate, now time.Time, report *GCReport) error {
	tombstone := storage.GCTombstone{
		ID:           s.ids.Next(),
		RunID:        runID,
		ObjectSHA:    candidate.Object.SHA256,
		Size:         candidate.Object.Size,
		MIME:         candidate.Object.MIME,
		RelativePath: candidate.Object.RelativePath,
		Reason:       candidate.DeletionReason,
		DeletedAt:    now,
	}
	if err := tombstone.Validate(); err != nil {
		return err
	}
	if err := s.repo.CreateTombstones(ctx, []storage.GCTombstone{tombstone}); err != nil {
		return fmt.Errorf("write gc tombstone: %w", err)
	}
	report.Tombstones++
	return nil
}

// deleteFile removes one blob best-effort. It returns true when the file was
// deleted and records missing/failed results otherwise.
func (s *GCService) deleteFile(ctx context.Context, root string, candidate gcCandidate, report *GCReport) bool {
	err := s.files.DeleteObject(ctx, root, candidate.Object.RelativePath)
	if err == nil {
		report.Files.Deleted++
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		report.Files.Missing++
		return false
	}
	report.Files.Failed++
	report.recordError(fmt.Errorf("delete object file %s: %w", candidate.Object.SHA256, err))
	return false
}

// finish writes the terminal run row, report, audit entry and event. The
// terminal write runs on a detached context with a short timeout: a canceled
// request or shutdown must not leave a 'running' row behind (it would wedge
// every later GC until the lease reclaim). The pass error is still returned.
func (s *GCService) finish(ctx context.Context, run storage.GCRun, report *GCReport, runErr error) (storage.GCRun, error) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gcFinishTimeout)
	defer cancel()
	finishedAt := s.clock.Now().UTC()
	report.FinishedAt = &finishedAt
	if runErr != nil {
		report.Status = string(storage.GCRunFailed)
		report.Error = truncateError(runErr.Error())
		run.Status = storage.GCRunFailed
		run.Error = report.Error
	} else {
		report.Status = string(storage.GCRunCompleted)
		run.Status = storage.GCRunCompleted
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return run, fmt.Errorf("encode gc report: %w", err)
	}
	run.FinishedAt = &finishedAt
	run.Report = raw
	run.ScannedObjects = report.Scanned.Objects
	run.ScannedRefs = report.Scanned.Refs
	run.DeletedObjects = report.Deleted.Objects
	run.DeletedRefs = report.Deleted.Refs
	run.DeletedBytes = report.Deleted.Bytes
	if err := s.repo.UpdateRun(finishCtx, run); err != nil {
		return run, fmt.Errorf("finish gc run: %w", err)
	}
	s.recordCompletion(finishCtx, run, report)
	return run, runErr
}

// recordCompletion emits the system gc.completed event and the storage audit
// entry; both are best-effort and never fail the pass.
func (s *GCService) recordCompletion(ctx context.Context, run storage.GCRun, report *GCReport) {
	payload := map[string]any{
		"gc_run_id":       platformid.Format(run.ID),
		"mode":            string(run.Mode),
		"deleted_objects": report.Deleted.Objects,
		"deleted_bytes":   report.Deleted.Bytes,
		"run_status":      string(run.Status),
	}
	requestID := requestid.From(ctx)
	if s.events != nil {
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   "system",
			Type:      "gc.completed",
			Scope:     map[string]any{},
			Payload:   payload,
			ActorID:   run.ActorID,
			RequestID: requestID,
		})
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    run.ActorID,
			Action:     "storage.gc.completed",
			TargetKind: "gc_run",
			TargetID:   platformid.Format(run.ID),
			RequestID:  requestID,
			Metadata: map[string]any{
				"mode":            string(run.Mode),
				"status":          string(run.Status),
				"scanned_objects": report.Scanned.Objects,
				"scanned_refs":    report.Scanned.Refs,
				"deleted_objects": report.Deleted.Objects,
				"deleted_refs":    report.Deleted.Refs,
				"deleted_bytes":   report.Deleted.Bytes,
				"tombstones":      report.Tombstones,
			},
		})
	}
}

// ListRuns returns the newest runs, capped at MaxGCRunLimit.
func (s *GCService) ListRuns(ctx context.Context, limit int) ([]storage.GCRun, error) {
	if s == nil || s.repo == nil {
		return nil, platform.NewError(http.StatusInternalServerError, "internal", "gc service is not configured")
	}
	if limit <= 0 {
		limit = DefaultGCRunLimit
	}
	if limit > MaxGCRunLimit {
		limit = MaxGCRunLimit
	}
	runs, err := s.repo.ListRuns(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list gc runs: %w", err)
	}
	return runs, nil
}

// GetRun returns one run with its tombstones.
func (s *GCService) GetRun(ctx context.Context, runID int64) (GCRunDetail, error) {
	if runID <= 0 {
		return GCRunDetail{}, platform.Invalid("gc_run_id", "gc_run_id must be a positive decimal ID")
	}
	if s == nil || s.repo == nil {
		return GCRunDetail{}, platform.NewError(http.StatusInternalServerError, "internal", "gc service is not configured")
	}
	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return GCRunDetail{}, platform.NotFound("gc_run")
		}
		return GCRunDetail{}, fmt.Errorf("read gc run: %w", err)
	}
	tombstones, err := s.repo.ListTombstones(ctx, runID)
	if err != nil {
		return GCRunDetail{}, fmt.Errorf("list gc tombstones: %w", err)
	}
	return GCRunDetail{Run: run, Tombstones: tombstones}, nil
}

// newGCReport initializes a report with every class/reason key present so the
// JSONB always carries the full zero-filled shape.
func newGCReport(mode storage.GCMode, settings gcSettings, now time.Time) *GCReport {
	report := &GCReport{
		Mode:      string(mode),
		DryRun:    mode == storage.GCModeDryRun,
		Status:    string(storage.GCRunRunning),
		StartedAt: now,
		Settings: GCReportSettings{
			TempHours:               settings.TempHours,
			ReplacedExportDays:      settings.ReplacedExportDays,
			UnreferencedContentDays: settings.UnreferencedContentDays,
			GraceHours:              settings.GraceHours,
			BatchSize:               settings.BatchSize,
		},
		Candidates: map[string]int{
			storage.GCCandidateUnreferenced:     0,
			storage.GCCandidateTemp:             0,
			storage.GCCandidateSupersededExport: 0,
			storage.GCCandidateOrphanFile:       0,
		},
		CandidateSamples: []GCCandidateSample{},
		Protected: map[string]int{
			GCProtectedReportContent:    0,
			GCProtectedOwnerExists:      0,
			GCProtectedUnknownOwnerKind: 0,
			GCProtectedGrace:            0,
			GCProtectedLiveRefs:         0,
			GCProtectedMissingRef:       0,
		},
		ProtectedSamples:  []GCProtectedSample{},
		UnknownOwnerKinds: []string{},
		unknownKinds:      map[string]struct{}{},
	}
	return report
}

// plan accumulates the deletions the selected candidate set implies.
func (r *GCReport) plan(candidate gcCandidate) {
	switch candidate.Class {
	case storage.GCCandidateTemp:
		r.Planned.Files++
		r.Planned.Bytes += candidate.Object.Size
	case storage.GCCandidateUnreferenced:
		r.Planned.Objects++
		r.Planned.Files++
		r.Planned.Bytes += candidate.Object.Size
	default:
		r.Planned.Refs++
		if candidate.DeleteObject {
			r.Planned.Objects++
			r.Planned.Files++
			r.Planned.Bytes += candidate.Object.Size
		}
	}
}

// protectRef records a protected live ref.
func (r *GCReport) protectRef(ref GCRef, reason string, ageSeconds int64) {
	r.protect(reason, ref.ObjectSHA, ref.OwnerKind, ref.OwnerID, ref.RefID, ageSeconds)
}

// protect records a protected object/ref/file.
func (r *GCReport) protect(reason, objectSHA, ownerKind string, ownerID, refID int64, ageSeconds int64) {
	r.Protected[reason]++
	if len(r.ProtectedSamples) >= GCReportSampleLimit {
		r.SamplesTruncated = true
		return
	}
	r.ProtectedSamples = append(r.ProtectedSamples, GCProtectedSample{
		ObjectSHA:  objectSHA,
		OwnerKind:  ownerKind,
		OwnerID:    ownerID,
		RefID:      refID,
		Reason:     reason,
		AgeSeconds: ageSeconds,
	})
}

// noteUnknownOwnerKind records one unknown owner_kind value for the report.
func (r *GCReport) noteUnknownOwnerKind(kind string) {
	if r.unknownKinds == nil {
		r.unknownKinds = map[string]struct{}{}
	}
	r.unknownKinds[kind] = struct{}{}
}

// recordError stores a bounded list of deletion failures in the report.
func (r *GCReport) recordError(err error) {
	if err == nil || len(r.Errors) >= gcReportErrorLimit {
		return
	}
	r.Errors = append(r.Errors, truncateError(err.Error()))
}

// finalize builds the capped candidate sample and the sorted unknown-kind list.
func (r *GCReport) finalize() {
	for _, candidate := range r.selected {
		if len(r.CandidateSamples) >= GCReportSampleLimit {
			r.SamplesTruncated = true
			break
		}
		r.CandidateSamples = append(r.CandidateSamples, GCCandidateSample{
			Class:            candidate.Class,
			ObjectSHA:        candidate.Object.SHA256,
			Size:             candidate.Object.Size,
			OwnerKind:        candidate.OwnerKind,
			OwnerID:          candidate.OwnerID,
			RefID:            candidate.RefID,
			ExportArtifactID: candidate.ExportArtifactID,
			AgeSeconds:       candidate.AgeSeconds,
			DeleteObject:     candidate.DeleteObject,
			DeleteRef:        candidate.RefID > 0,
		})
	}
	r.UnknownOwnerKinds = make([]string, 0, len(r.unknownKinds))
	for kind := range r.unknownKinds {
		r.UnknownOwnerKinds = append(r.UnknownOwnerKinds, kind)
	}
	sort.Strings(r.UnknownOwnerKinds)
}

func nonNegativeAge(now, at time.Time) int64 {
	age := now.Sub(at)
	if age < 0 {
		return 0
	}
	return int64(age / time.Second)
}

func durationAtLeast(d, floor time.Duration) time.Duration {
	if d < floor {
		return floor
	}
	return d
}

func truncateError(message string) string {
	if len(message) <= 4000 {
		return message
	}
	return message[:4000]
}
