//go:build integration

package storagepg_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// gcIntegrationSettings serves the retention settings from a fixed map.
type gcIntegrationSettings struct {
	root string
	ints map[string]int64
}

func (s gcIntegrationSettings) String(context.Context, string) (string, error) { return s.root, nil }

func (s gcIntegrationSettings) Int(_ context.Context, key string) (int64, error) {
	return s.ints[key], nil
}

func (s gcIntegrationSettings) Bool(context.Context, string) (bool, error) { return true, nil }

// dbOwnerLookup resolves content_ref owners against the real P1/P3/P4 tables.
// Task 19 wires the production adapters; this is the integration-test double
// with the same semantics.
type dbOwnerLookup struct{ pool *pgxpool.Pool }

func (l dbOwnerLookup) ReportRevisionExists(ctx context.Context, revisionID int64) (bool, error) {
	var exists bool
	err := l.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM delivery_report_revision WHERE id = $1)`, revisionID).Scan(&exists)
	return exists, err
}

func (l dbOwnerLookup) AttachmentExists(ctx context.Context, attachmentID int64) (bool, error) {
	var exists bool
	err := l.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM requirement_attachment a
			JOIN version v ON v.id = a.version_id
			WHERE a.id = $1
		)`, attachmentID).Scan(&exists)
	return exists, err
}

func (l dbOwnerLookup) EvidenceExists(ctx context.Context, evidenceID int64) (bool, error) {
	var exists bool
	err := l.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM evidence WHERE id = $1)`, evidenceID).Scan(&exists)
	return exists, err
}

func (l dbOwnerLookup) ExportArtifactExists(ctx context.Context, artifactID int64) (bool, error) {
	var exists bool
	err := l.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM export_artifact WHERE id = $1)`, artifactID).Scan(&exists)
	return exists, err
}

type gcIntegrationAudit struct{ actions []string }

func (a *gcIntegrationAudit) Record(_ context.Context, event auditapp.Event) error {
	a.actions = append(a.actions, event.Action)
	return nil
}

type gcIntegrationEvents struct{ events []eventingapp.Event }

func (e *gcIntegrationEvents) Publish(_ context.Context, event eventingapp.Event) error {
	e.events = append(e.events, event)
	return nil
}

type gcIntegrationFixture struct {
	pool     *pgxpool.Pool
	root     string
	store    *storagepg.FileStore
	repo     *storagepg.GCRepository
	svc      *storageapp.GCService
	audit    *gcIntegrationAudit
	events   *gcIntegrationEvents
	settings gcIntegrationSettings
	ctx      context.Context
}

func newGCIntegrationFixture(t *testing.T) *gcIntegrationFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	root := t.TempDir()
	ids, err := id.NewSnowflake(3)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	settings := gcIntegrationSettings{
		root: root,
		ints: map[string]int64{
			storageapp.GCSettingTempHours:               24,
			storageapp.GCSettingReplacedExportDays:      7,
			storageapp.GCSettingUnreferencedContentDays: 30,
			storageapp.GCSettingGraceHours:              24,
			storageapp.GCSettingBatchSize:               500,
			storageapp.GCSettingIntervalMinutes:         1440,
		},
	}
	repo := storagepg.NewGCRepository(pool)
	audit := &gcIntegrationAudit{}
	events := &gcIntegrationEvents{}
	svc := storageapp.NewGCService(repo, dbOwnerLookup{pool: pool}, storagepg.NewFileStore(), settings, ids, nil, audit, events)
	return &gcIntegrationFixture{
		pool: pool, root: root, store: storagepg.NewFileStore(), repo: repo, svc: svc,
		audit: audit, events: events, settings: settings, ctx: context.Background(),
	}
}

// gcWriteBlob stores bytes with the real filestore and sets mtime.
func (f *gcIntegrationFixture) gcWriteBlob(t *testing.T, data []byte, modified time.Time) (sha, relativePath, fullPath string) {
	t.Helper()
	sha = shaOf(data)
	relativePath, err := f.store.Put(f.ctx, f.root, sha, data)
	if err != nil {
		t.Fatalf("store blob: %v", err)
	}
	fullPath = filepath.Join(f.root, filepath.FromSlash(relativePath))
	if err := os.Chtimes(fullPath, modified, modified); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return sha, relativePath, fullPath
}

func (f *gcIntegrationFixture) gcInsertObject(t *testing.T, sha string, size int64, mime, relativePath string, createdAt time.Time) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO content_object (sha256, size, mime, relative_path, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, sha, size, mime, relativePath, createdAt)
	if err != nil {
		t.Fatalf("insert content_object: %v", err)
	}
}

func (f *gcIntegrationFixture) gcInsertRef(t *testing.T, refID int64, sha, ownerKind string, ownerID int64, role string, createdAt time.Time) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO content_ref (id, object_id, owner_kind, owner_id, role, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, refID, sha, ownerKind, ownerID, role, createdAt)
	if err != nil {
		t.Fatalf("insert content_ref: %v", err)
	}
}

func (f *gcIntegrationFixture) gcInsertExportArtifact(t *testing.T, artifactID, versionID int64, ownerKind string, ownerID int64, revision int, sha string, size int64, createdAt time.Time) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO export_artifact (
			id, version_id, owner_kind, owner_id, revision, file_format,
			content_object_id, size_bytes, mime, filename, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'html', $6, $7, 'text/html; charset=utf-8', $8, 'ready', $9, $9)
	`, artifactID, versionID, ownerKind, ownerID, revision, sha, size, "export-"+sha[:8]+".html", createdAt)
	if err != nil {
		t.Fatalf("insert export_artifact: %v", err)
	}
}

func (f *gcIntegrationFixture) gcObjectExists(t *testing.T, sha string) bool {
	t.Helper()
	var exists bool
	if err := f.pool.QueryRow(f.ctx, `SELECT EXISTS (SELECT 1 FROM content_object WHERE sha256 = $1)`, sha).Scan(&exists); err != nil {
		t.Fatalf("object exists: %v", err)
	}
	return exists
}

func (f *gcIntegrationFixture) gcRefLive(t *testing.T, refID int64) bool {
	t.Helper()
	var deleted *time.Time
	err := f.pool.QueryRow(f.ctx, `SELECT deleted_at FROM content_ref WHERE id = $1`, refID).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		// DeleteObjects hard-deletes the soft-deleted refs of a collected object
		// (the content_ref FK requires it); a missing row is not live.
		return false
	}
	if err != nil {
		t.Fatalf("read content_ref %d: %v", refID, err)
	}
	return deleted == nil
}

func (f *gcIntegrationFixture) gcCount(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return count
}

func decodeIntegrationGCReport(t *testing.T, run storage.GCRun) storageapp.GCReport {
	t.Helper()
	var report storageapp.GCReport
	if err := json.Unmarshal(run.Report, &report); err != nil {
		t.Fatalf("decode report: %v (raw=%s)", err, string(run.Report))
	}
	return report
}

func TestGCRepositoryScansPagesAndDeletesSafely(t *testing.T) {
	fixture := newGCIntegrationFixture(t)
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)

	// Three refs for paging; two share object "shared".
	shared := shaOf([]byte("shared"))
	sharedRel, _ := fixture.store.Put(fixture.ctx, fixture.root, shared, []byte("shared"))
	fixture.gcInsertObject(t, shared, int64(len("shared")), "text/plain", sharedRel, old)
	only := shaOf([]byte("only-ref"))
	onlyRel, _ := fixture.store.Put(fixture.ctx, fixture.root, only, []byte("only-ref"))
	fixture.gcInsertObject(t, only, int64(len("only-ref")), "text/plain", onlyRel, old)
	fixture.gcInsertRef(t, 101, shared, "evidence", 9001, "text", old)
	fixture.gcInsertRef(t, 102, shared, "evidence", 9002, "text", old)
	fixture.gcInsertRef(t, 103, only, "evidence", 9003, "text", old)

	firstPage, err := fixture.repo.ListRefsPage(fixture.ctx, 0, 2)
	if err != nil {
		t.Fatalf("ListRefsPage first: %v", err)
	}
	if len(firstPage) != 2 || firstPage[0].RefID != 101 || firstPage[1].RefID != 102 {
		t.Fatalf("first page = %#v", firstPage)
	}
	if firstPage[0].ObjectSHA != shared || firstPage[0].Size != int64(len("shared")) || firstPage[0].ObjectCreatedAt.IsZero() {
		t.Fatalf("first page object metadata = %#v", firstPage[0])
	}
	secondPage, err := fixture.repo.ListRefsPage(fixture.ctx, firstPage[len(firstPage)-1].RefID, 2)
	if err != nil {
		t.Fatalf("ListRefsPage second: %v", err)
	}
	if len(secondPage) != 1 || secondPage[0].RefID != 103 {
		t.Fatalf("second page = %#v", secondPage)
	}

	counts, err := fixture.repo.LiveRefCounts(fixture.ctx, []string{shared, only})
	if err != nil {
		t.Fatalf("LiveRefCounts: %v", err)
	}
	if counts[shared] != 2 || counts[only] != 1 {
		t.Fatalf("counts = %#v", counts)
	}

	// Unreferenced scan only returns objects without live refs and before the cutoff.
	unreferencedSHA := shaOf([]byte("unreferenced"))
	unreferencedRel, _ := fixture.store.Put(fixture.ctx, fixture.root, unreferencedSHA, []byte("unreferenced"))
	fixture.gcInsertObject(t, unreferencedSHA, int64(len("unreferenced")), "text/plain", unreferencedRel, old)
	youngSHA := shaOf([]byte("young"))
	youngRel, _ := fixture.store.Put(fixture.ctx, fixture.root, youngSHA, []byte("young"))
	fixture.gcInsertObject(t, youngSHA, int64(len("young")), "text/plain", youngRel, now.Add(-time.Hour))
	unreferenced, err := fixture.repo.ListUnreferencedObjects(fixture.ctx, now.Add(-30*24*time.Hour), 10)
	if err != nil {
		t.Fatalf("ListUnreferencedObjects: %v", err)
	}
	if len(unreferenced) != 1 || unreferenced[0].SHA256 != unreferencedSHA {
		t.Fatalf("unreferenced = %#v", unreferenced)
	}

	existing, err := fixture.repo.ExistingObjectSHAs(fixture.ctx, []string{shared, unreferencedSHA, shaOf([]byte("missing"))})
	if err != nil {
		t.Fatalf("ExistingObjectSHAs: %v", err)
	}
	if len(existing) != 2 {
		t.Fatalf("existing = %#v", existing)
	}

	// DeleteObjects must keep the live-referenced object and remove the other.
	softDeleted := shaOf([]byte("soft-ref-object"))
	softRel, _ := fixture.store.Put(fixture.ctx, fixture.root, softDeleted, []byte("soft-ref-object"))
	fixture.gcInsertObject(t, softDeleted, int64(len("soft-ref-object")), "text/plain", softRel, old)
	fixture.gcInsertRef(t, 201, softDeleted, "evidence", 9101, "text", old)
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE content_ref SET deleted_at = now() WHERE id = 201`); err != nil {
		t.Fatalf("soft delete ref: %v", err)
	}
	deleted, err := fixture.repo.DeleteObjects(fixture.ctx, []string{shared, softDeleted})
	if err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != softDeleted {
		t.Fatalf("deleted = %#v", deleted)
	}
	if !fixture.gcObjectExists(t, shared) {
		t.Fatal("live-referenced object was deleted")
	}
	if fixture.gcObjectExists(t, softDeleted) {
		t.Fatal("soft-referenced object was not deleted")
	}
	if got := fixture.gcCount(t, `SELECT count(*) FROM content_ref WHERE id = 201`); got != 0 {
		t.Fatalf("soft-deleted ref rows = %d, want 0", got)
	}

	// Tombstones round-trip with run IDs and reasons.
	runID := int64(777)
	run := storage.GCRun{ID: runID, Mode: storage.GCModeApply, Status: storage.GCRunCompleted, ActorKind: storage.GCActorSystem, StartedAt: now, Report: json.RawMessage(`{}`)}
	if _, err := fixture.repo.CreateRun(fixture.ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	tombstone := storage.GCTombstone{
		ID: 1, RunID: runID, ObjectSHA: unreferencedSHA, Size: int64(len("unreferenced")),
		MIME: "text/plain", RelativePath: unreferencedRel, Reason: storage.GCCandidateUnreferenced, DeletedAt: now,
	}
	if err := fixture.repo.CreateTombstones(fixture.ctx, []storage.GCTombstone{tombstone}); err != nil {
		t.Fatalf("CreateTombstones: %v", err)
	}
	tombstones, err := fixture.repo.ListTombstones(fixture.ctx, runID)
	if err != nil {
		t.Fatalf("ListTombstones: %v", err)
	}
	if len(tombstones) != 1 || tombstones[0].ID != 1 || tombstones[0].Reason != storage.GCCandidateUnreferenced {
		t.Fatalf("tombstones = %#v", tombstones)
	}
}

func TestGCServiceDryRunThenApplyOnRealLedger(t *testing.T) {
	fixture := newGCIntegrationFixture(t)
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	young := now.Add(-1 * time.Hour)

	// --- unprotected objects (one per candidate class) -----------------------
	unreferencedData := []byte("unreferenced object")
	unreferencedSHA, unreferencedRel, unreferencedPath := fixture.gcWriteBlob(t, unreferencedData, old)
	fixture.gcInsertObject(t, unreferencedSHA, int64(len(unreferencedData)), "text/plain; charset=utf-8", unreferencedRel, old)

	orphanData := []byte("orphan ref object")
	orphanSHA, orphanRel, orphanPath := fixture.gcWriteBlob(t, orphanData, old)
	fixture.gcInsertObject(t, orphanSHA, int64(len(orphanData)), "text/plain; charset=utf-8", orphanRel, old)
	fixture.gcInsertRef(t, 301, orphanSHA, "evidence", 880001, "text", old)

	missingData := []byte("missing file object")
	missingSHA, missingRel, missingPath := fixture.gcWriteBlob(t, missingData, old)
	fixture.gcInsertObject(t, missingSHA, int64(len(missingData)), "text/plain; charset=utf-8", missingRel, old)
	fixture.gcInsertRef(t, 302, missingSHA, "evidence", 880002, "text", old)
	if err := os.Remove(missingPath); err != nil {
		t.Fatalf("remove missing fixture file: %v", err)
	}

	tempData := []byte("temp orphan file")
	_, _, tempPath := fixture.gcWriteBlob(t, tempData, old)

	youngTempData := []byte("young temp file")
	_, _, youngTempPath := fixture.gcWriteBlob(t, youngTempData, young)

	// --- superseded export (object old enough to be collected too) ----------
	supersededData := []byte("superseded export")
	supersededSHA, supersededRel, supersededPath := fixture.gcWriteBlob(t, supersededData, old)
	fixture.gcInsertObject(t, supersededSHA, int64(len(supersededData)), "text/html; charset=utf-8", supersededRel, old)
	fixture.gcInsertRef(t, 303, supersededSHA, storage.OwnerKindExport, 601, "content", old)
	fixture.gcInsertExportArtifact(t, 601, 10, storage.OwnerKindCase, 20, 1, supersededSHA, int64(len(supersededData)), old)

	currentData := []byte("current export")
	currentSHA, currentRel, currentPath := fixture.gcWriteBlob(t, currentData, old)
	fixture.gcInsertObject(t, currentSHA, int64(len(currentData)), "text/html; charset=utf-8", currentRel, old)
	fixture.gcInsertRef(t, 304, currentSHA, storage.OwnerKindExport, 602, "content", now)
	fixture.gcInsertExportArtifact(t, 602, 10, storage.OwnerKindCase, 20, 2, currentSHA, int64(len(currentData)), now)

	// --- protected ledger owners --------------------------------------------
	// report revision (report_html) is protected even if old.
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO delivery_report (id, version_id, kind, status) VALUES (4001, 5001, 'version', 'published')`); err != nil {
		t.Fatalf("insert report: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO delivery_report_revision (id, report_id, revision, status, input_fingerprint, as_of)
		VALUES (4101, 4001, 1, 'published', $1, $2)
	`, strings.Repeat("ab", 32), now); err != nil {
		t.Fatalf("insert report revision: %v", err)
	}
	reportData := []byte("report html bytes")
	reportSHA, reportRel, reportPath := fixture.gcWriteBlob(t, reportData, old)
	fixture.gcInsertObject(t, reportSHA, int64(len(reportData)), "text/html; charset=utf-8", reportRel, old)
	fixture.gcInsertRef(t, 305, reportSHA, storageapp.OwnerKindReportRevision, 4101, storageapp.RoleReportHTML, old)

	// requirement attachment whose version still exists.
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO version (id, title, created_by) VALUES (5001, 'GC integration', 1)`); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	attachmentData := []byte("attachment bytes")
	attachmentSHA, attachmentRel, attachmentPath := fixture.gcWriteBlob(t, attachmentData, old)
	fixture.gcInsertObject(t, attachmentSHA, int64(len(attachmentData)), "text/plain; charset=utf-8", attachmentRel, old)
	fixture.gcInsertRef(t, 306, attachmentSHA, storageapp.OwnerKindRequirementAttachment, 5101, "attachment", old)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO requirement_attachment (id, version_id, content_ref_id, name, mime, size)
		VALUES (5101, 5001, 306, 'spec.txt', 'text/plain', $1)
	`, int64(len(attachmentData))); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}

	// evidence row exists -> protected.
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, environment_fingerprint, dedupe_key)
		VALUES (6001, 5001, 7001, $1, 'gc-integration')
	`, strings.Repeat("cd", 32)); err != nil {
		t.Fatalf("insert execution intent: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO run_wave (id, version_id, status) VALUES (6101, 5001, 'done')`); err != nil {
		t.Fatalf("insert run wave: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status)
		VALUES (6201, 6101, 6001, 7001, 'passed')
	`); err != nil {
		t.Fatalf("insert case run: %v", err)
	}
	evidenceData := []byte("evidence bytes")
	evidenceSHA, evidenceRel, evidencePath := fixture.gcWriteBlob(t, evidenceData, old)
	fixture.gcInsertObject(t, evidenceSHA, int64(len(evidenceData)), "text/plain; charset=utf-8", evidenceRel, old)
	fixture.gcInsertRef(t, 307, evidenceSHA, storageapp.OwnerKindEvidence, 6301, "text", old)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO evidence (id, case_run_id, kind, content_ref_id, sha256, size_bytes)
		VALUES (6301, 6201, 'text', 307, $1, $2)
	`, evidenceSHA, int64(len(evidenceData))); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}

	// young orphan ref: within the grace window, protected.
	youngOrphanData := []byte("young orphan ref")
	youngOrphanSHA, youngOrphanRel, _ := fixture.gcWriteBlob(t, youngOrphanData, young)
	fixture.gcInsertObject(t, youngOrphanSHA, int64(len(youngOrphanData)), "text/plain; charset=utf-8", youngOrphanRel, young)
	fixture.gcInsertRef(t, 308, youngOrphanSHA, "evidence", 880003, "text", young)

	// --- dry run -------------------------------------------------------------
	dryRun, err := fixture.svc.Run(fixture.ctx, storage.GCModeDryRun, 77)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dryRun.Status != storage.GCRunCompleted || dryRun.Mode != storage.GCModeDryRun || dryRun.ActorID == nil || *dryRun.ActorID != 77 {
		t.Fatalf("dry run = %#v", dryRun)
	}
	dryReport := decodeIntegrationGCReport(t, dryRun)
	wantCandidates := map[string]int{
		storage.GCCandidateUnreferenced:     1,
		storage.GCCandidateTemp:             1,
		storage.GCCandidateSupersededExport: 1,
		storage.GCCandidateOrphanFile:       2,
	}
	for class, want := range wantCandidates {
		if got := dryReport.Candidates[class]; got != want {
			t.Fatalf("dry candidate %q = %d, want %d (report=%#v)", class, got, want, dryReport.Candidates)
		}
	}
	wantProtected := map[string]int{
		storageapp.GCProtectedReportContent: 1,
		// attachment + current export + evidence + the superseded export ref
		// (the orphan-ref scan sees its artifact row and protects it too; the
		// superseded-export scan classifies it as a candidate).
		storageapp.GCProtectedOwnerExists: 4,
		storageapp.GCProtectedGrace:       2,
	}
	for reason, want := range wantProtected {
		if got := dryReport.Protected[reason]; got != want {
			t.Fatalf("dry protected %q = %d, want %d (report=%#v)", reason, got, want, dryReport.Protected)
		}
	}
	if dryReport.Planned.Refs != 3 || dryReport.Planned.Objects != 4 || dryReport.Planned.Files != 5 {
		t.Fatalf("dry planned = %#v", dryReport.Planned)
	}
	if got := fixture.gcCount(t, `SELECT count(*) FROM gc_tombstone`); got != 0 {
		t.Fatalf("dry run wrote %d tombstones", got)
	}
	for _, path := range []string{unreferencedPath, orphanPath, supersededPath, tempPath, reportPath, attachmentPath, evidencePath, currentPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry run removed file %s: %v", path, err)
		}
	}
	if !fixture.gcRefLive(t, 301) || !fixture.gcRefLive(t, 303) {
		t.Fatal("dry run soft-deleted refs")
	}

	// --- apply ---------------------------------------------------------------
	expectedBytes := int64(len(unreferencedData) + len(orphanData) + len(missingData) + len(supersededData) + len(tempData))
	applyRun, err := fixture.svc.Run(fixture.ctx, storage.GCModeApply, 77)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if applyRun.Status != storage.GCRunCompleted {
		t.Fatalf("apply run = %#v", applyRun)
	}
	applyReport := decodeIntegrationGCReport(t, applyRun)
	if applyRun.DeletedObjects != 4 || applyRun.DeletedRefs != 3 || applyRun.DeletedBytes != expectedBytes {
		t.Fatalf("apply counters = objects:%d refs:%d bytes:%d, want 4/3/%d", applyRun.DeletedObjects, applyRun.DeletedRefs, applyRun.DeletedBytes, expectedBytes)
	}
	if applyReport.Files.Missing != 1 || applyReport.Files.Failed != 0 {
		t.Fatalf("file results = %#v", applyReport.Files)
	}

	for _, sha := range []string{unreferencedSHA, orphanSHA, missingSHA, supersededSHA} {
		if fixture.gcObjectExists(t, sha) {
			t.Fatalf("object %s still exists after apply", sha)
		}
	}
	for _, path := range []string{unreferencedPath, orphanPath, supersededPath, tempPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("file %s still exists (err=%v)", path, err)
		}
	}
	if _, err := os.Stat(missingPath); !os.IsNotExist(err) {
		t.Fatalf("missing file still exists: %v", err)
	}

	// Protected owners/bytes survive, including the current export revision.
	for _, sha := range []string{reportSHA, attachmentSHA, evidenceSHA, currentSHA, youngOrphanSHA} {
		if !fixture.gcObjectExists(t, sha) {
			t.Fatalf("protected object %s was deleted", sha)
		}
	}
	for _, path := range []string{reportPath, attachmentPath, evidencePath, currentPath, youngTempPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("protected file %s was removed: %v", path, err)
		}
	}
	if !fixture.gcRefLive(t, 305) || !fixture.gcRefLive(t, 306) || !fixture.gcRefLive(t, 307) || !fixture.gcRefLive(t, 304) {
		t.Fatal("protected refs were soft-deleted")
	}
	if fixture.gcRefLive(t, 301) || fixture.gcRefLive(t, 302) || fixture.gcRefLive(t, 303) {
		t.Fatal("candidate refs were not soft-deleted")
	}
	if got := fixture.gcCount(t, `SELECT count(*) FROM export_artifact`); got != 2 {
		t.Fatalf("export artifacts = %d", got)
	}

	// Tombstones: one per deleted object with the candidate's reason.
	detail, err := fixture.svc.GetRun(fixture.ctx, applyRun.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(detail.Tombstones) != 5 {
		t.Fatalf("tombstones = %#v", detail.Tombstones)
	}
	reasons := map[string]int{}
	relativePaths := map[string]bool{}
	for _, tombstone := range detail.Tombstones {
		reasons[tombstone.Reason]++
		relativePaths[tombstone.RelativePath] = true
		if tombstone.RunID != applyRun.ID || tombstone.DeletedAt.IsZero() || tombstone.ObjectSHA == "" {
			t.Fatalf("tombstone = %#v", tombstone)
		}
	}
	wantReasons := map[string]int{
		storage.GCCandidateUnreferenced:     1,
		storage.GCCandidateTemp:             1,
		storage.GCCandidateSupersededExport: 1,
		storage.GCCandidateOrphanFile:       2,
	}
	for reason, want := range wantReasons {
		if reasons[reason] != want {
			t.Fatalf("tombstone reasons = %#v", reasons)
		}
	}
	if len(relativePaths) != 5 {
		t.Fatalf("tombstone paths = %#v", relativePaths)
	}

	// gc_run rows: the dry run is completed too and the apply report is JSONB.
	var reportType string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT jsonb_typeof(report) FROM gc_run WHERE id = $1`, applyRun.ID).Scan(&reportType); err != nil {
		t.Fatalf("read gc_run report: %v", err)
	}
	if reportType != "object" {
		t.Fatalf("report type = %q", reportType)
	}
	if got := fixture.gcCount(t, `SELECT count(*) FROM gc_run WHERE status = 'completed'`); got != 2 {
		t.Fatalf("completed runs = %d", got)
	}
	if got := fixture.gcCount(t, `SELECT count(*) FROM gc_run WHERE status = 'running'`); got != 0 {
		t.Fatalf("running runs = %d", got)
	}

	if len(fixture.events.events) != 2 {
		t.Fatalf("events = %#v", fixture.events.events)
	}
	for _, event := range fixture.events.events {
		if event.Type != "gc.completed" || event.Channel != "system" {
			t.Fatalf("event = %#v", event)
		}
	}
	if len(fixture.audit.actions) != 2 || fixture.audit.actions[0] != "storage.gc.completed" {
		t.Fatalf("audit actions = %#v", fixture.audit.actions)
	}
}

// TestGCReclaimsStaleRunningRun pins fix 3: a crashed GC pass left a 'running'
// row whose lease expired; the next pass must fail that row and proceed instead
// of returning 409 gc_running for ever (the partial unique index).
func TestGCReclaimsStaleRunningRun(t *testing.T) {
	fixture := newGCIntegrationFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO gc_run (id, mode, status, actor_kind, started_at, lease_owner, lease_until)
		VALUES (950, 'apply', 'running', 'system', now() - interval '1 hour', 'crashed', now() - interval '1 minute')
	`); err != nil {
		t.Fatalf("insert stale running run: %v", err)
	}

	run, err := fixture.svc.Run(fixture.ctx, storage.GCModeDryRun, 1)
	if err != nil {
		t.Fatalf("Run after a stale running row: %v", err)
	}
	if run.Status != storage.GCRunCompleted {
		t.Fatalf("new run = %#v", run)
	}

	var status, errorText string
	var leaseOwner *string
	var leaseUntil, finishedAt *time.Time
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT status, coalesce(error, ''), lease_owner, lease_until, finished_at
		FROM gc_run WHERE id = 950
	`).Scan(&status, &errorText, &leaseOwner, &leaseUntil, &finishedAt); err != nil {
		t.Fatalf("read reclaimed run: %v", err)
	}
	if status != string(storage.GCRunFailed) {
		t.Fatalf("stale row status = %q, want failed", status)
	}
	if !strings.Contains(errorText, "lease") {
		t.Fatalf("stale row error = %q, want a lease-expired explanation", errorText)
	}
	if leaseOwner != nil || leaseUntil != nil {
		t.Fatalf("stale row lease not cleared: owner=%v until=%v", leaseOwner, leaseUntil)
	}
	if finishedAt == nil {
		t.Fatal("stale row finished_at was not stamped")
	}

	// A legacy running row with no lease_until is also reclaimable once it is
	// older than the GC lease window.
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO gc_run (id, mode, status, actor_kind, started_at, lease_owner, lease_until)
		VALUES (951, 'dry_run', 'running', 'system', now() - interval '1 hour', NULL, NULL)
	`); err != nil {
		t.Fatalf("insert legacy stale running run: %v", err)
	}
	if _, err := fixture.svc.Run(fixture.ctx, storage.GCModeDryRun, 1); err != nil {
		t.Fatalf("Run after a legacy stale running row: %v", err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT status FROM gc_run WHERE id = 951`).Scan(&status); err != nil {
		t.Fatalf("read legacy reclaimed run: %v", err)
	}
	if status != string(storage.GCRunFailed) {
		t.Fatalf("legacy stale row status = %q, want failed", status)
	}
}

func TestGCSingleRunningRunConflictOnRealDB(t *testing.T) {
	fixture := newGCIntegrationFixture(t)
	now := time.Now().UTC()
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO gc_run (id, mode, status, actor_kind, started_at)
		VALUES (900, 'dry_run', 'running', 'system', now())
	`); err != nil {
		t.Fatalf("insert running run: %v", err)
	}

	_, err := fixture.repo.CreateRun(fixture.ctx, storage.GCRun{
		ID: 901, Mode: storage.GCModeDryRun, Status: storage.GCRunRunning,
		ActorKind: storage.GCActorSystem, StartedAt: now, Report: json.RawMessage(`{}`),
	})
	if !errors.Is(err, storage.ErrGCRunning) {
		t.Fatalf("repo CreateRun err = %v", err)
	}

	_, err = fixture.svc.Run(fixture.ctx, storage.GCModeDryRun, 1)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "gc_running" {
		t.Fatalf("service err = %#v", err)
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM gc_run WHERE id = 900`); err != nil {
		t.Fatalf("delete running run: %v", err)
	}
	if run, err := fixture.svc.Run(fixture.ctx, storage.GCModeDryRun, 1); err != nil || run.Status != storage.GCRunCompleted {
		t.Fatalf("run after releasing lock = %#v, err=%v", run, err)
	}
}
