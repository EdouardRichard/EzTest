package storageapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

var gcNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type gcCallLog struct {
	calls []string
}

func (l *gcCallLog) add(call string) { l.calls = append(l.calls, call) }

func (l *gcCallLog) indexOf(prefix string) int {
	for i, call := range l.calls {
		if strings.HasPrefix(call, prefix) {
			return i
		}
	}
	return -1
}

func (l *gcCallLog) count(prefix string) int {
	count := 0
	for _, call := range l.calls {
		if strings.HasPrefix(call, prefix) {
			count++
		}
	}
	return count
}

type gcFixedClock struct{ now time.Time }

func (c gcFixedClock) Now() time.Time { return c.now }

type gcSequentialIDs struct{ next int64 }

func (g *gcSequentialIDs) Next() int64 { g.next++; return g.next }

type gcFakeSettings struct {
	ints    map[string]int64
	bools   map[string]bool
	strings map[string]string
	err     error
}

func newGCFakeSettings(root string) *gcFakeSettings {
	return &gcFakeSettings{
		ints: map[string]int64{
			GCSettingTempHours:               24,
			GCSettingReplacedExportDays:      7,
			GCSettingUnreferencedContentDays: 30,
			GCSettingGraceHours:              24,
			GCSettingBatchSize:               500,
			GCSettingIntervalMinutes:         1440,
		},
		bools:   map[string]bool{GCSettingDryRun: true},
		strings: map[string]string{DataRootSettingKey: root},
	}
}

func (s *gcFakeSettings) String(_ context.Context, key string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.strings[key], nil
}

func (s *gcFakeSettings) Int(_ context.Context, key string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.ints[key], nil
}

func (s *gcFakeSettings) Bool(_ context.Context, key string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.bools[key], nil
}

type gcFakeOwners struct {
	reportRevisions map[int64]bool
	attachments     map[int64]bool
	evidence        map[int64]bool
	exports         map[int64]bool
	err             error
}

func (o *gcFakeOwners) ReportRevisionExists(_ context.Context, id int64) (bool, error) {
	if o.err != nil {
		return false, o.err
	}
	return o.reportRevisions[id], nil
}

func (o *gcFakeOwners) AttachmentExists(_ context.Context, id int64) (bool, error) {
	if o.err != nil {
		return false, o.err
	}
	return o.attachments[id], nil
}

func (o *gcFakeOwners) EvidenceExists(_ context.Context, id int64) (bool, error) {
	if o.err != nil {
		return false, o.err
	}
	return o.evidence[id], nil
}

func (o *gcFakeOwners) ExportArtifactExists(_ context.Context, id int64) (bool, error) {
	if o.err != nil {
		return false, o.err
	}
	return o.exports[id], nil
}

type gcFakeRepo struct {
	log *gcCallLog

	createErr     error
	updateErr     error
	createTombErr error
	deleteRefsErr error
	deleteObjErr  error
	refsPageErr   error
	unrefErr      error
	supersededErr error
	existingErr   error
	liveCountsErr error
	getErr        error
	listErr       error

	runs       map[int64]storage.GCRun
	runOrder   []int64
	tombstones []storage.GCTombstone
	tombErr    error

	refs         []GCRef
	unreferenced []storage.Object
	superseded   []SupersededExport
	existingSHAs []string
	liveCounts   map[string]int

	deletedRefIDs       []int64
	deletedSHAs         []string
	deleteObjectsResult []string

	refsPageLimits    []int
	unrefLimits       []int
	supersededLimits  []int
	listLimits        []int
	existingRequested [][]string
}

func newGCFakeRepo(log *gcCallLog) *gcFakeRepo {
	return &gcFakeRepo{log: log, runs: map[int64]storage.GCRun{}, liveCounts: map[string]int{}}
}

func (r *gcFakeRepo) CreateRun(_ context.Context, run storage.GCRun) (storage.GCRun, error) {
	r.log.add("create_run")
	if r.createErr != nil {
		return storage.GCRun{}, r.createErr
	}
	r.runs[run.ID] = run
	r.runOrder = append(r.runOrder, run.ID)
	return run, nil
}

func (r *gcFakeRepo) UpdateRun(_ context.Context, run storage.GCRun) error {
	r.log.add("update_run:" + string(run.Status))
	if r.updateErr != nil {
		return r.updateErr
	}
	r.runs[run.ID] = run
	return nil
}

func (r *gcFakeRepo) GetRun(_ context.Context, id int64) (storage.GCRun, error) {
	if r.getErr != nil {
		return storage.GCRun{}, r.getErr
	}
	run, ok := r.runs[id]
	if !ok {
		return storage.GCRun{}, storage.ErrNotFound
	}
	return run, nil
}

func (r *gcFakeRepo) ListRuns(_ context.Context, limit int) ([]storage.GCRun, error) {
	r.listLimits = append(r.listLimits, limit)
	if r.listErr != nil {
		return nil, r.listErr
	}
	runs := make([]storage.GCRun, 0, len(r.runOrder))
	for _, id := range r.runOrder {
		runs = append(runs, r.runs[id])
	}
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (r *gcFakeRepo) CreateTombstones(_ context.Context, tombstones []storage.GCTombstone) error {
	reasons := make([]string, 0, len(tombstones))
	for _, tombstone := range tombstones {
		reasons = append(reasons, tombstone.Reason)
	}
	r.log.add("create_tombstones:" + strings.Join(reasons, ","))
	if r.createTombErr != nil {
		return r.createTombErr
	}
	r.tombstones = append(r.tombstones, tombstones...)
	return nil
}

func (r *gcFakeRepo) ListTombstones(_ context.Context, runID int64) ([]storage.GCTombstone, error) {
	if r.tombErr != nil {
		return nil, r.tombErr
	}
	result := make([]storage.GCTombstone, 0)
	for _, tombstone := range r.tombstones {
		if tombstone.RunID == runID {
			result = append(result, tombstone)
		}
	}
	return result, nil
}

func (r *gcFakeRepo) ListRefsPage(_ context.Context, afterID int64, limit int) ([]GCRef, error) {
	r.refsPageLimits = append(r.refsPageLimits, limit)
	if r.refsPageErr != nil {
		return nil, r.refsPageErr
	}
	result := make([]GCRef, 0, limit)
	for _, ref := range r.refs {
		if ref.RefID <= afterID {
			continue
		}
		result = append(result, ref)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (r *gcFakeRepo) LiveRefCounts(_ context.Context, shas []string) (map[string]int, error) {
	r.log.add(fmt.Sprintf("live_counts:%d", len(shas)))
	if r.liveCountsErr != nil {
		return nil, r.liveCountsErr
	}
	result := make(map[string]int, len(shas))
	for _, sha := range shas {
		result[sha] = r.liveCounts[sha]
	}
	return result, nil
}

func (r *gcFakeRepo) DeleteRefs(_ context.Context, ids []int64) error {
	r.log.add(fmt.Sprintf("delete_refs:%d", len(ids)))
	if r.deleteRefsErr != nil {
		return r.deleteRefsErr
	}
	r.deletedRefIDs = append(r.deletedRefIDs, ids...)
	return nil
}

func (r *gcFakeRepo) ListUnreferencedObjects(_ context.Context, _ time.Time, limit int) ([]storage.Object, error) {
	r.unrefLimits = append(r.unrefLimits, limit)
	if r.unrefErr != nil {
		return nil, r.unrefErr
	}
	if len(r.unreferenced) > limit {
		return r.unreferenced[:limit], nil
	}
	return r.unreferenced, nil
}

func (r *gcFakeRepo) DeleteObjects(_ context.Context, shas []string) ([]string, error) {
	r.log.add(fmt.Sprintf("delete_objects:%d", len(shas)))
	if r.deleteObjErr != nil {
		return nil, r.deleteObjErr
	}
	if r.deleteObjectsResult != nil {
		return r.deleteObjectsResult, nil
	}
	r.deletedSHAs = append(r.deletedSHAs, shas...)
	return shas, nil
}

func (r *gcFakeRepo) ListSupersededExports(_ context.Context, _ time.Time, limit int) ([]SupersededExport, error) {
	r.supersededLimits = append(r.supersededLimits, limit)
	if r.supersededErr != nil {
		return nil, r.supersededErr
	}
	if len(r.superseded) > limit {
		return r.superseded[:limit], nil
	}
	return r.superseded, nil
}

func (r *gcFakeRepo) ExistingObjectSHAs(_ context.Context, shas []string) ([]string, error) {
	r.existingRequested = append(r.existingRequested, shas)
	if r.existingErr != nil {
		return nil, r.existingErr
	}
	existing := make(map[string]bool, len(r.existingSHAs))
	for _, sha := range r.existingSHAs {
		existing[sha] = true
	}
	result := make([]string, 0, len(shas))
	for _, sha := range shas {
		if existing[sha] {
			result = append(result, sha)
		}
	}
	return result, nil
}

type gcFakeFiles struct {
	log *gcCallLog

	files      []FileInfo
	scanErr    error
	deleteErrs map[string]error
	probeErr   error

	deletedPaths []string
	probeCalls   []string
}

func newGCFakeFiles(log *gcCallLog) *gcFakeFiles {
	return &gcFakeFiles{log: log, deleteErrs: map[string]error{}}
}

func (f *gcFakeFiles) ScanObjects(_ context.Context, _ string) ([]FileInfo, error) {
	f.log.add("scan_files")
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	return f.files, nil
}

func (f *gcFakeFiles) DeleteObject(_ context.Context, _ string, relativePath string) error {
	f.log.add("delete_file:" + relativePath)
	if err := f.deleteErrs[relativePath]; err != nil {
		return err
	}
	f.deletedPaths = append(f.deletedPaths, relativePath)
	return nil
}

func (f *gcFakeFiles) ProbeWritable(_ context.Context, _ string, instanceKey string) error {
	f.probeCalls = append(f.probeCalls, instanceKey)
	return f.probeErr
}

type gcFakeAudit struct {
	events []auditapp.Event
	err    error
}

func (a *gcFakeAudit) Record(_ context.Context, event auditapp.Event) error {
	a.events = append(a.events, event)
	return a.err
}

type gcFakeEvents struct {
	events []eventingapp.Event
	err    error
}

func (e *gcFakeEvents) Publish(_ context.Context, event eventingapp.Event) error {
	e.events = append(e.events, event)
	return e.err
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func gcSHA(seed string) string {
	return strings.Repeat(string(seed[0]), 64)
}

func gcObject(sha string, size int64, age time.Duration) storage.Object {
	return storage.Object{
		SHA256:       sha,
		Size:         size,
		MIME:         "text/plain; charset=utf-8",
		RelativePath: "objects/" + sha[0:2] + "/" + sha[2:4] + "/" + sha,
		CreatedAt:    gcNow.Add(-age),
	}
}

func gcRef(refID int64, object storage.Object, ownerKind string, ownerID int64, role string) GCRef {
	return GCRef{
		RefID:           refID,
		ObjectSHA:       object.SHA256,
		Size:            object.Size,
		MIME:            object.MIME,
		RelativePath:    object.RelativePath,
		OwnerKind:       ownerKind,
		OwnerID:         ownerID,
		Role:            role,
		ObjectCreatedAt: object.CreatedAt,
		RefCreatedAt:    object.CreatedAt,
	}
}

func supersededExport(artifactID int64, refID int64, object storage.Object) SupersededExport {
	return SupersededExport{
		ArtifactID:        artifactID,
		VersionID:         10,
		OwnerKind:         storage.OwnerKindCase,
		OwnerID:           20,
		Format:            storage.FormatHTML,
		Revision:          1,
		RefID:             refID,
		ObjectSHA:         object.SHA256,
		Size:              object.Size,
		MIME:              object.MIME,
		RelativePath:      object.RelativePath,
		ArtifactCreatedAt: object.CreatedAt,
		ObjectCreatedAt:   object.CreatedAt,
	}
}

type gcFixture struct {
	svc      *GCService
	repo     *gcFakeRepo
	owners   *gcFakeOwners
	files    *gcFakeFiles
	settings *gcFakeSettings
	audit    *gcFakeAudit
	events   *gcFakeEvents
	log      *gcCallLog
}

func newGCFixture(t *testing.T) *gcFixture {
	t.Helper()
	log := &gcCallLog{}
	repo := newGCFakeRepo(log)
	owners := &gcFakeOwners{
		reportRevisions: map[int64]bool{},
		attachments:     map[int64]bool{},
		evidence:        map[int64]bool{},
		exports:         map[int64]bool{},
	}
	files := newGCFakeFiles(log)
	settings := newGCFakeSettings(t.TempDir())
	audit := &gcFakeAudit{}
	events := &gcFakeEvents{}
	svc := NewGCService(repo, owners, files, settings, &gcSequentialIDs{next: 1000}, gcFixedClock{now: gcNow}, audit, events)
	return &gcFixture{svc: svc, repo: repo, owners: owners, files: files, settings: settings, audit: audit, events: events, log: log}
}

func decodeGCReport(t *testing.T, run storage.GCRun) GCReport {
	t.Helper()
	var report GCReport
	if err := json.Unmarshal(run.Report, &report); err != nil {
		t.Fatalf("decode report: %v (raw=%s)", err, string(run.Report))
	}
	return report
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestGCRunDryRunClassifiesFourCandidateClassesAndProtects(t *testing.T) {
	fixture := newGCFixture(t)
	old := 40 * 24 * time.Hour
	young := time.Hour

	orphanObject := gcObject(gcSHA("a"), 100, old)
	reportHTML := gcObject(gcSHA("b"), 200, old)
	reportOrphan := gcObject(gcSHA("c"), 210, old)
	unknownObject := gcObject(gcSHA("d"), 300, old)
	attachmentObject := gcObject(gcSHA("e"), 400, old)
	exportObject := gcObject(gcSHA("f"), 500, old)
	unreferenced := gcObject(gcSHA("1"), 600, old)
	tempFile := gcObject(gcSHA("2"), 700, 48*time.Hour)
	youngTemp := gcObject(gcSHA("3"), 800, young)
	youngOrphan := gcObject(gcSHA("4"), 900, young)

	fixture.repo.refs = []GCRef{
		gcRef(101, orphanObject, OwnerKindEvidence, 501, "text"),
		gcRef(102, reportHTML, OwnerKindReportRevision, 502, RoleReportHTML),
		gcRef(103, reportOrphan, OwnerKindReportRevision, 503, RoleReportPDF),
		gcRef(104, unknownObject, "mystery_owner", 504, "content"),
		gcRef(105, attachmentObject, OwnerKindRequirementAttachment, 505, "attachment"),
		gcRef(106, youngOrphan, OwnerKindEvidence, 506, "text"),
	}
	fixture.owners.reportRevisions[502] = true
	fixture.owners.attachments[505] = true
	fixture.owners.exports[600] = true
	fixture.repo.liveCounts = map[string]int{
		orphanObject.SHA256:     1,
		reportHTML.SHA256:       1,
		reportOrphan.SHA256:     1,
		unknownObject.SHA256:    1,
		attachmentObject.SHA256: 1,
		youngOrphan.SHA256:      1,
	}
	fixture.repo.superseded = []SupersededExport{supersededExport(600, 201, exportObject)}
	fixture.repo.liveCounts[exportObject.SHA256] = 1
	fixture.repo.unreferenced = []storage.Object{unreferenced}
	fixture.files.files = []FileInfo{
		{SHA256: tempFile.SHA256, RelativePath: tempFile.RelativePath, Size: tempFile.Size, ModTime: tempFile.CreatedAt},
		{SHA256: youngTemp.SHA256, RelativePath: youngTemp.RelativePath, Size: youngTemp.Size, ModTime: youngTemp.CreatedAt},
		{RelativePath: "objects/notes.txt", Size: 5, ModTime: gcNow.Add(-48 * time.Hour)},
	}

	run, err := fixture.svc.Run(context.Background(), storage.GCModeDryRun, 77)
	if err != nil {
		t.Fatalf("Run dry_run: %v", err)
	}
	if run.Status != storage.GCRunCompleted || run.Mode != storage.GCModeDryRun || run.ActorID == nil || *run.ActorID != 77 {
		t.Fatalf("run = %#v", run)
	}
	if run.ActorKind != storage.GCActorUser {
		t.Fatalf("actor kind = %q", run.ActorKind)
	}

	report := decodeGCReport(t, run)
	wantCandidates := map[string]int{
		storage.GCCandidateUnreferenced:     1,
		storage.GCCandidateTemp:             1,
		storage.GCCandidateSupersededExport: 1,
		storage.GCCandidateOrphanFile:       1,
	}
	for class, want := range wantCandidates {
		if got := report.Candidates[class]; got != want {
			t.Fatalf("candidate class %q = %d, want %d (report=%#v)", class, got, want, report)
		}
	}
	wantProtected := map[string]int{
		GCProtectedReportContent:    2,
		GCProtectedUnknownOwnerKind: 1,
		GCProtectedOwnerExists:      1,
		GCProtectedGrace:            2,
	}
	for reason, want := range wantProtected {
		if got := report.Protected[reason]; got != want {
			t.Fatalf("protected reason %q = %d, want %d (report=%#v)", reason, got, want, report)
		}
	}
	if len(report.UnknownOwnerKinds) != 1 || report.UnknownOwnerKinds[0] != "mystery_owner" {
		t.Fatalf("unknown owner kinds = %#v", report.UnknownOwnerKinds)
	}
	if report.Planned.Refs != 2 || report.Planned.Objects != 3 || report.Planned.Bytes != 1900 {
		t.Fatalf("planned = %#v", report.Planned)
	}
	if report.Planned.Files != 4 {
		t.Fatalf("planned files = %d", report.Planned.Files)
	}
	if report.SamplesTruncated {
		t.Fatal("samples should not be truncated")
	}

	// A dry run must never write tombstones, delete refs/objects or touch files.
	if len(fixture.repo.deletedRefIDs) != 0 || len(fixture.repo.deletedSHAs) != 0 || len(fixture.repo.tombstones) != 0 {
		t.Fatalf("dry run deleted rows: refs=%v objects=%v tombstones=%v", fixture.repo.deletedRefIDs, fixture.repo.deletedSHAs, fixture.repo.tombstones)
	}
	if len(fixture.files.deletedPaths) != 0 {
		t.Fatalf("dry run deleted files: %v", fixture.files.deletedPaths)
	}
	if run.DeletedObjects != 0 || run.DeletedRefs != 0 || run.DeletedBytes != 0 {
		t.Fatalf("dry run run counters = %#v", run)
	}
	if len(fixture.events.events) != 1 {
		t.Fatalf("events = %#v", fixture.events.events)
	}
	event := fixture.events.events[0]
	if event.Type != "gc.completed" || event.Channel != "system" {
		t.Fatalf("event = %#v", event)
	}
	if event.Payload["run_status"] != string(storage.GCRunCompleted) || event.Payload["mode"] != string(storage.GCModeDryRun) {
		t.Fatalf("event payload = %#v", event.Payload)
	}
	if len(fixture.audit.events) != 1 || fixture.audit.events[0].Action != "storage.gc.completed" {
		t.Fatalf("audit = %#v", fixture.audit.events)
	}
	if fixture.audit.events[0].ActorID == nil || *fixture.audit.events[0].ActorID != 77 {
		t.Fatalf("audit actor = %#v", fixture.audit.events[0].ActorID)
	}
	if fixture.repo.refsPageLimits[0] != 500 || fixture.repo.unrefLimits[0] != 500 {
		t.Fatalf("batch limits = %v/%v", fixture.repo.refsPageLimits, fixture.repo.unrefLimits)
	}
}

func TestGCApplyTombstonesFirstThenRefsObjectsAndFiles(t *testing.T) {
	fixture := newGCFixture(t)
	old := 40 * 24 * time.Hour
	orphanObject := gcObject(gcSHA("a"), 100, old)
	unreferenced := gcObject(gcSHA("b"), 200, old)
	tempFile := gcObject(gcSHA("c"), 300, 48*time.Hour)

	fixture.repo.refs = []GCRef{gcRef(101, orphanObject, OwnerKindEvidence, 501, "text")}
	fixture.repo.liveCounts = map[string]int{orphanObject.SHA256: 1}
	fixture.repo.unreferenced = []storage.Object{unreferenced}
	fixture.files.files = []FileInfo{{SHA256: tempFile.SHA256, RelativePath: tempFile.RelativePath, Size: tempFile.Size, ModTime: tempFile.CreatedAt}}
	// The orphan object file was already removed from disk: applying must record
	// the missing file and still delete the content_object row.
	fixture.files.deleteErrs[orphanObject.RelativePath] = os.ErrNotExist

	run, err := fixture.svc.Run(context.Background(), storage.GCModeApply, 0)
	if err != nil {
		t.Fatalf("Run apply: %v", err)
	}
	if run.Status != storage.GCRunCompleted {
		t.Fatalf("status = %q", run.Status)
	}
	if run.DeletedObjects != 2 || run.DeletedRefs != 1 || run.DeletedBytes != 600 {
		t.Fatalf("counters = %#v", run)
	}

	if tombstoneIdx := fixture.log.indexOf("create_tombstones"); tombstoneIdx < 0 {
		t.Fatalf("no tombstone written: %v", fixture.log.calls)
	}
	if fixture.log.indexOf("create_tombstones") > fixture.log.indexOf("delete_refs") ||
		fixture.log.indexOf("delete_refs") > fixture.log.indexOf("delete_objects") ||
		fixture.log.indexOf("delete_objects") > fixture.log.indexOf("delete_file") {
		t.Fatalf("deletion order wrong: %v", fixture.log.calls)
	}
	if len(fixture.repo.tombstones) != 3 {
		t.Fatalf("tombstones = %#v", fixture.repo.tombstones)
	}
	reasons := map[string]int{}
	for _, tombstone := range fixture.repo.tombstones {
		reasons[tombstone.Reason]++
		if tombstone.RunID != run.ID {
			t.Fatalf("tombstone run = %d, want %d", tombstone.RunID, run.ID)
		}
	}
	wantReasons := map[string]int{
		storage.GCCandidateOrphanFile:   1,
		storage.GCCandidateUnreferenced: 1,
		storage.GCCandidateTemp:         1,
	}
	for reason, want := range wantReasons {
		if reasons[reason] != want {
			t.Fatalf("tombstone reasons = %#v", reasons)
		}
	}

	report := decodeGCReport(t, run)
	if report.Deleted.Objects != 2 || report.Deleted.Refs != 1 || report.Deleted.Bytes != 600 {
		t.Fatalf("report deleted = %#v", report.Deleted)
	}
	if report.Files.Missing != 1 || report.Files.Deleted != 2 || report.Files.Failed != 0 {
		t.Fatalf("report files = %#v", report.Files)
	}
	if report.Tombstones != 3 {
		t.Fatalf("report tombstones = %d", report.Tombstones)
	}
	if run.ActorKind != storage.GCActorSystem || run.ActorID != nil {
		t.Fatalf("system run actor = %#v", run)
	}
}

func TestGCApplyKeepsObjectsWithOtherLiveRefs(t *testing.T) {
	fixture := newGCFixture(t)
	old := 40 * 24 * time.Hour
	shared := gcObject(gcSHA("a"), 100, old)
	currentExport := gcObject(gcSHA("b"), 200, old)

	fixture.repo.refs = []GCRef{
		gcRef(101, shared, OwnerKindEvidence, 501, "text"),
		gcRef(102, shared, OwnerKindEvidence, 502, "text"),
		gcRef(103, currentExport, storage.OwnerKindExport, 700, "content"),
	}
	fixture.owners.evidence[502] = true
	fixture.owners.exports[700] = true
	fixture.repo.liveCounts = map[string]int{
		shared.SHA256:        2,
		currentExport.SHA256: 1,
	}

	run, err := fixture.svc.Run(context.Background(), storage.GCModeApply, 1)
	if err != nil {
		t.Fatalf("Run apply: %v", err)
	}
	if run.DeletedRefs != 1 || run.DeletedObjects != 0 {
		t.Fatalf("counters = %#v", run)
	}
	if len(fixture.repo.deletedRefIDs) != 1 || fixture.repo.deletedRefIDs[0] != 101 {
		t.Fatalf("deleted refs = %v", fixture.repo.deletedRefIDs)
	}
	if len(fixture.repo.deletedSHAs) != 0 {
		t.Fatalf("deleted objects = %v", fixture.repo.deletedSHAs)
	}
	if len(fixture.repo.tombstones) != 0 {
		t.Fatalf("tombstones = %#v", fixture.repo.tombstones)
	}
	report := decodeGCReport(t, run)
	if report.Protected[GCProtectedOwnerExists] != 2 {
		t.Fatalf("protected = %#v", report.Protected)
	}
}

func TestGCRunConflictsWithRunningRun(t *testing.T) {
	fixture := newGCFixture(t)
	fixture.repo.createErr = storage.ErrGCRunning

	_, err := fixture.svc.Run(context.Background(), storage.GCModeDryRun, 1)
	if err == nil {
		t.Fatal("expected a conflict")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "gc_running" {
		t.Fatalf("err = %#v", err)
	}
	if len(fixture.repo.refsPageLimits) != 0 || len(fixture.repo.unrefLimits) != 0 {
		t.Fatalf("run scanned after conflict: %#v", fixture.log.calls)
	}
}

func TestGCRunRejectsInvalidMode(t *testing.T) {
	fixture := newGCFixture(t)
	_, err := fixture.svc.Run(context.Background(), storage.GCMode("APPLY"), 1)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("err = %#v", err)
	}
	if len(fixture.log.calls) != 0 {
		t.Fatalf("repo calls = %v", fixture.log.calls)
	}
}

func TestGCRunSettingsFailureDoesNotCreateRun(t *testing.T) {
	fixture := newGCFixture(t)
	fixture.settings.err = errors.New("settings store down")
	if _, err := fixture.svc.Run(context.Background(), storage.GCModeDryRun, 1); err == nil {
		t.Fatal("expected a settings error")
	}
	if len(fixture.log.calls) != 0 {
		t.Fatalf("repo calls = %v", fixture.log.calls)
	}
}

func TestGCBatchSizeTruncatesCandidates(t *testing.T) {
	fixture := newGCFixture(t)
	fixture.settings.ints[GCSettingBatchSize] = 1
	old := 40 * 24 * time.Hour
	first := gcObject(gcSHA("a"), 10, old)
	second := gcObject(gcSHA("b"), 10, old)
	fixture.repo.refs = []GCRef{
		gcRef(101, first, OwnerKindEvidence, 501, "text"),
		gcRef(102, second, OwnerKindEvidence, 502, "text"),
	}
	fixture.repo.liveCounts = map[string]int{first.SHA256: 1, second.SHA256: 1}
	fixture.repo.unreferenced = []storage.Object{gcObject(gcSHA("c"), 10, old)}

	run, err := fixture.svc.Run(context.Background(), storage.GCModeDryRun, 1)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	report := decodeGCReport(t, run)
	if report.Candidates[storage.GCCandidateOrphanFile] != 1 {
		t.Fatalf("orphan candidates = %d, want 1", report.Candidates[storage.GCCandidateOrphanFile])
	}
	if !report.SamplesTruncated {
		t.Fatal("report should be truncated")
	}
	if fixture.repo.refsPageLimits[0] != 1 || fixture.repo.unrefLimits[0] != 1 {
		t.Fatalf("limits = %v/%v", fixture.repo.refsPageLimits, fixture.repo.unrefLimits)
	}
}

func TestGCListAndGetRuns(t *testing.T) {
	fixture := newGCFixture(t)
	run := storage.GCRun{
		ID:        50,
		Mode:      storage.GCModeApply,
		Status:    storage.GCRunCompleted,
		ActorKind: storage.GCActorSystem,
		StartedAt: gcNow,
		Report:    json.RawMessage(`{"candidates":{}}`),
	}
	fixture.repo.runs[50] = run
	fixture.repo.runOrder = []int64{50}
	fixture.repo.tombstones = []storage.GCTombstone{
		{ID: 1, RunID: 50, ObjectSHA: gcSHA("a"), Size: 1, MIME: "text/plain", RelativePath: "objects/aa/aa/" + gcSHA("a"), Reason: storage.GCCandidateUnreferenced, DeletedAt: gcNow},
	}

	runs, err := fixture.svc.ListRuns(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != 50 {
		t.Fatalf("runs = %#v", runs)
	}
	if fixture.repo.listLimits[0] != DefaultGCRunLimit {
		t.Fatalf("limit = %d", fixture.repo.listLimits[0])
	}
	if _, err := fixture.svc.ListRuns(context.Background(), 1000); err != nil {
		t.Fatalf("ListRuns large: %v", err)
	}
	if fixture.repo.listLimits[1] != MaxGCRunLimit {
		t.Fatalf("clamped limit = %d", fixture.repo.listLimits[1])
	}

	detail, err := fixture.svc.GetRun(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if detail.Run.ID != 50 || len(detail.Tombstones) != 1 {
		t.Fatalf("detail = %#v", detail)
	}

	_, err = fixture.svc.GetRun(context.Background(), 999)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Code != "not_found" {
		t.Fatalf("missing run err = %#v", err)
	}
	if _, err := fixture.svc.GetRun(context.Background(), 0); err == nil {
		t.Fatal("zero id should fail")
	}
}
