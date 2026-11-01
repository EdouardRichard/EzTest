package visualapp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

type fakeAssertionRepo struct {
	mu         sync.Mutex
	rows       map[int64]visual.Assertion
	updates    int
	supersedes []time.Time
}

func newFakeAssertionRepo() *fakeAssertionRepo {
	return &fakeAssertionRepo{rows: map[int64]visual.Assertion{}}
}

func (f *fakeAssertionRepo) Create(_ context.Context, assertion visual.Assertion) (visual.Assertion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.CaseAssertionID == assertion.CaseAssertionID {
			return visual.Assertion{}, visual.ErrDuplicate
		}
	}
	f.rows[assertion.ID] = assertion
	return assertion, nil
}

func (f *fakeAssertionRepo) UpdateWithRevision(_ context.Context, assertion visual.Assertion, supersededAt time.Time) (visual.Assertion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[assertion.ID] = assertion
	f.updates++
	f.supersedes = append(f.supersedes, supersededAt)
	return assertion, nil
}

func (f *fakeAssertionRepo) Get(_ context.Context, id int64) (visual.Assertion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return visual.Assertion{}, visual.ErrNotFound
	}
	return row, nil
}

func (f *fakeAssertionRepo) GetByCaseAssertion(_ context.Context, versionID, caseAssertionID int64) (visual.Assertion, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.VersionID == versionID && row.CaseAssertionID == caseAssertionID {
			return row, true, nil
		}
	}
	return visual.Assertion{}, false, nil
}

func (f *fakeAssertionRepo) List(_ context.Context, versionID int64, caseVersionID, versionSUTID *int64) ([]visual.Assertion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]visual.Assertion, 0)
	for _, row := range f.rows {
		if row.VersionID != versionID {
			continue
		}
		if caseVersionID != nil && row.CaseVersionID != *caseVersionID {
			continue
		}
		if versionSUTID != nil && row.VersionSUTID != *versionSUTID {
			continue
		}
		list = append(list, row)
	}
	return list, nil
}

type fakeBaselineRepo struct {
	mu         sync.Mutex
	rows       []visual.Baseline
	superseded int
}

func (f *fakeBaselineRepo) InsertApproved(_ context.Context, baseline visual.Baseline, supersededAt time.Time) (visual.Baseline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.rows {
		if f.rows[index].VisualAssertionID == baseline.VisualAssertionID && f.rows[index].Status == visual.BaselineApproved {
			at := supersededAt
			f.rows[index].Status = visual.BaselineSuperseded
			f.rows[index].SupersededAt = &at
			f.superseded++
		}
	}
	f.rows = append(f.rows, baseline)
	return baseline, nil
}

func (f *fakeBaselineRepo) List(_ context.Context, assertionID int64) ([]visual.Baseline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]visual.Baseline, 0)
	for _, row := range f.rows {
		if row.VisualAssertionID == assertionID {
			list = append(list, row)
		}
	}
	return list, nil
}

func (f *fakeBaselineRepo) Active(_ context.Context, assertionID int64) (visual.Baseline, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.VisualAssertionID == assertionID && row.Status == visual.BaselineApproved {
			return row, true, nil
		}
	}
	return visual.Baseline{}, false, nil
}

type fakeDiffRepo struct {
	mu       sync.Mutex
	rows     map[int64]visual.Diff
	byResult map[int64]int64
	overview visualapp.OverviewData
}

func newFakeDiffRepo() *fakeDiffRepo {
	return &fakeDiffRepo{rows: map[int64]visual.Diff{}, byResult: map[int64]int64{}}
}

func (f *fakeDiffRepo) Insert(_ context.Context, diff visual.Diff) (visual.Diff, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byResult[diff.AssertionResultID]; ok {
		return f.rows[id], false, nil
	}
	f.rows[diff.ID] = diff
	f.byResult[diff.AssertionResultID] = diff.ID
	return diff, true, nil
}

func (f *fakeDiffRepo) Get(_ context.Context, id int64) (visual.Diff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return visual.Diff{}, visual.ErrNotFound
	}
	return row, nil
}

func (f *fakeDiffRepo) List(_ context.Context, _ int64, _ visual.DiffFilter) ([]visual.Diff, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]visual.Diff, 0, len(f.rows))
	for _, row := range f.rows {
		list = append(list, row)
	}
	return list, false, nil
}

func (f *fakeDiffRepo) ByAssertionResult(_ context.Context, assertionResultID int64) (visual.Diff, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byResult[assertionResultID]
	if !ok {
		return visual.Diff{}, false, nil
	}
	return f.rows[id], true, nil
}

func (f *fakeDiffRepo) Overview(_ context.Context, versionID *int64) (visualapp.OverviewData, error) {
	return f.overview, nil
}

type fakeCaseReader struct {
	ref visualapp.CaseAssertionRef
	err error
}

func (f *fakeCaseReader) VisualAssertionContext(context.Context, int64, int64) (visualapp.CaseAssertionRef, error) {
	return f.ref, f.err
}

type fakeEvidenceReader struct {
	evidence visualapp.VisualEvidence
	err      error
}

func (f *fakeEvidenceReader) VisualEvidence(context.Context, int64, int64) (visualapp.VisualEvidence, error) {
	return f.evidence, f.err
}

type fakeSettings struct {
	floats map[string]float64
	ints   map[string]int64
}

func (f *fakeSettings) Float(_ context.Context, key string) (float64, error) {
	if value, ok := f.floats[key]; ok {
		return value, nil
	}
	return 0, errors.New("missing setting")
}

func (f *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	if value, ok := f.ints[key]; ok {
		return value, nil
	}
	return 0, errors.New("missing setting")
}

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time { return f.now }

type fakeIDs struct {
	mu   sync.Mutex
	next int64
}

func (f *fakeIDs) Next() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	return f.next
}

type fakeAudit struct {
	mu      sync.Mutex
	actions []string
}

func (f *fakeAudit) Record(_ context.Context, entry visualapp.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, entry.Action)
	return nil
}

type fakeEvents struct {
	mu     sync.Mutex
	events []visualapp.Event
}

func (f *fakeEvents) Publish(_ context.Context, event visualapp.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func platformCode(t *testing.T, err error) (int, string) {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a *platform.Error", err)
	}
	return apiErr.Status, apiErr.Code
}

func newConfigFixture(t *testing.T) (*visualapp.ConfigService, *fakeAssertionRepo, *fakeAudit) {
	t.Helper()
	repo := newFakeAssertionRepo()
	audit := &fakeAudit{}
	service := visualapp.NewConfigService(visualapp.ConfigServiceDeps{
		Repo: repo,
		Cases: &fakeCaseReader{ref: visualapp.CaseAssertionRef{
			CaseAssertionID: 9001, CaseVersionID: 8001, CaseID: 7001,
			StepSeq: 2, AssertionSeq: 1, Kind: "visual",
			TargetVersionSUTID: 5001, DefaultVersionSUTID: 5001,
			CaseVersionStatus: "confirmed",
		}},
		Settings: &fakeSettings{
			floats: map[string]float64{"visual.default_threshold": 0.005, "visual.default_device_scale_factor": 1},
			ints:   map[string]int64{"visual.default_viewport_width": 1280, "visual.default_viewport_height": 720, "visual.baseline_max_bytes": 5 << 20},
		},
		Clock: &fakeClock{now: time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)},
		IDs:   &fakeIDs{next: 100},
		Audit: audit,
	})
	return service, repo, audit
}

func TestConfigUpsertCreatesThenIncrementsRevision(t *testing.T) {
	service, repo, audit := newConfigFixture(t)
	ctx := context.Background()

	first, err := service.Upsert(ctx, 4001, 9001, 77, visual.ConfigInput{})
	if err != nil {
		t.Fatalf("Upsert create: %v", err)
	}
	if first.ID == 0 || first.ConfigRevision != 1 || first.Status != visual.AssertionActive {
		t.Fatalf("created assertion = %+v", first)
	}
	if first.VersionID != 4001 || first.CaseVersionID != 8001 || first.StepSeq != 2 || first.VersionSUTID != 5001 {
		t.Fatalf("identity = %+v", first)
	}
	if first.Threshold != 0.005 || first.ViewportWidth != 1280 || first.ViewportHeight != 720 {
		t.Fatalf("defaults = %+v", first)
	}

	selector := "#chart"
	second, err := service.Upsert(ctx, 4001, 9001, 77, visual.ConfigInput{Selector: &selector})
	if err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	if second.ConfigRevision != 2 || second.Selector != "#chart" {
		t.Fatalf("updated assertion = %+v", second)
	}
	if len(repo.supersedes) != 1 {
		t.Fatalf("supersede calls = %d", len(repo.supersedes))
	}

	third, err := service.Upsert(ctx, 4001, 9001, 77, visual.ConfigInput{Selector: &selector})
	if err != nil {
		t.Fatalf("Upsert unchanged: %v", err)
	}
	if third.ConfigRevision != 2 {
		t.Fatalf("unchanged upsert bumped revision: %+v", third)
	}
	if len(repo.supersedes) != 1 {
		t.Fatalf("unchanged upsert superseded again: %d", len(repo.supersedes))
	}
	if len(audit.actions) != 2 {
		t.Fatalf("audit actions = %v", audit.actions)
	}
}

func TestConfigUpsertRejectsInvalidConfig(t *testing.T) {
	service, _, _ := newConfigFixture(t)
	threshold := 0.5
	_, err := service.Upsert(context.Background(), 4001, 9001, 77, visual.ConfigInput{Threshold: &threshold})
	status, code := platformCode(t, err)
	if status != 422 || code != "config_invalid" {
		t.Fatalf("status=%d code=%s", status, code)
	}
}

func TestConfigUpsertRejectsNonVisualAssertion(t *testing.T) {
	service := visualapp.NewConfigService(visualapp.ConfigServiceDeps{
		Repo: newFakeAssertionRepo(),
		Cases: &fakeCaseReader{ref: visualapp.CaseAssertionRef{
			CaseAssertionID: 9001, CaseVersionID: 8001, StepSeq: 1, AssertionSeq: 1, Kind: "ui",
			TargetVersionSUTID: 5001, CaseVersionStatus: "confirmed",
		}},
		Clock: &fakeClock{}, IDs: &fakeIDs{next: 100},
	})
	_, err := service.Upsert(context.Background(), 4001, 9001, 77, visual.ConfigInput{})
	status, code := platformCode(t, err)
	if status != 422 || code != "config_invalid" {
		t.Fatalf("status=%d code=%s", status, code)
	}
}

func TestBaselineApproveCreatesFingerprintedBaseline(t *testing.T) {
	assertion := visual.Assertion{
		ID: 6001, VersionID: 4001, CaseVersionID: 8001, CaseAssertionID: 9001,
		StepSeq: 2, AssertionSeq: 1, VersionSUTID: 5001,
		Region: json.RawMessage(`{}`), Threshold: 0.005,
		ViewportWidth: 100, ViewportHeight: 100, DeviceScaleFactor: 1,
		WaitPolicy: json.RawMessage(`{}`), ConfigRevision: 3, Status: visual.AssertionActive,
		CreatedAt: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC),
	}
	data := testPNG(t, 100, 100)
	sutID := int64(5001)
	evidence := visualapp.VisualEvidence{
		ID: 7001, CaseRunID: 3001, CaseVersionID: 8001, VersionSUTID: &sutID,
		Kind: "visual_actual", SHA256: shaHex(data), SizeBytes: int64(len(data)),
		Data: data, Width: 100, Height: 100, ConfigRevision: 3,
	}
	assertions := newFakeAssertionRepo()
	assertions.rows[assertion.ID] = assertion
	baselines := &fakeBaselineRepo{}
	events := &fakeEvents{}
	service := visualapp.NewBaselineService(visualapp.BaselineServiceDeps{
		Baselines: baselines, Assertions: assertions,
		Evidence: &fakeEvidenceReader{evidence: evidence},
		Settings: &fakeSettings{ints: map[string]int64{"visual.baseline_max_bytes": 5 << 20}},
		Clock:    &fakeClock{now: time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)},
		IDs:      &fakeIDs{next: 200},
		Audit:    &fakeAudit{},
		Events:   events,
	})
	baseline, err := service.Approve(context.Background(), 4001, assertion.ID, evidence.ID, 77)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if baseline.Status != visual.BaselineApproved || baseline.ConfigRevision != 3 || baseline.Width != 100 {
		t.Fatalf("baseline = %+v", baseline)
	}
	if baseline.EnvironmentFingerprint == "" || len(baseline.EnvironmentFingerprint) != 64 {
		t.Fatalf("fingerprint = %q", baseline.EnvironmentFingerprint)
	}
	if len(events.events) != 1 || events.events[0].Type != "visual.baseline_approved" {
		t.Fatalf("events = %+v", events.events)
	}

	// A second approval supersedes the first.
	second, err := service.Approve(context.Background(), 4001, assertion.ID, evidence.ID, 78)
	if err != nil {
		t.Fatalf("second Approve: %v", err)
	}
	active, ok, err := service.Active(context.Background(), assertion.ID)
	if err != nil || !ok || active.ID != second.ID {
		t.Fatalf("active = %+v ok=%v err=%v", active, ok, err)
	}
	if baselines.superseded != 1 {
		t.Fatalf("superseded = %d", baselines.superseded)
	}
}

func TestBaselineApproveRejectsInvalidEvidence(t *testing.T) {
	assertion := visual.Assertion{
		ID: 6001, VersionID: 4001, CaseVersionID: 8001, CaseAssertionID: 9001,
		StepSeq: 1, AssertionSeq: 1, VersionSUTID: 5001,
		Region: json.RawMessage(`{}`), Threshold: 0.005,
		ViewportWidth: 100, ViewportHeight: 100, DeviceScaleFactor: 1,
		WaitPolicy: json.RawMessage(`{}`), ConfigRevision: 3, Status: visual.AssertionActive,
		UpdatedAt: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC),
	}
	data := testPNG(t, 100, 100)
	sutID := int64(5001)
	valid := visualapp.VisualEvidence{
		ID: 7001, CaseVersionID: 8001, VersionSUTID: &sutID, Kind: "visual_actual",
		SHA256: shaHex(data), SizeBytes: int64(len(data)), Data: data,
		Width: 100, Height: 100, ConfigRevision: 3,
	}
	cases := []struct {
		name     string
		mutate   func(*visualapp.VisualEvidence)
		wantCode string
	}{
		{"wrong kind", func(e *visualapp.VisualEvidence) { e.Kind = "screenshot" }, "baseline_evidence_invalid"},
		{"wrong case version", func(e *visualapp.VisualEvidence) { e.CaseVersionID = 9999 }, "baseline_evidence_invalid"},
		{"wrong sut", func(e *visualapp.VisualEvidence) { other := int64(1); e.VersionSUTID = &other }, "baseline_evidence_invalid"},
		{"sha mismatch", func(e *visualapp.VisualEvidence) { e.SHA256 = shaHex([]byte("other")) }, "baseline_evidence_invalid"},
		{"wrong dimensions", func(e *visualapp.VisualEvidence) { e.Width = 50; e.Height = 50 }, "baseline_evidence_invalid"},
		{"config revision changed", func(e *visualapp.VisualEvidence) { e.ConfigRevision = 2 }, "visual_config_changed"},
		{"capture before config change", func(e *visualapp.VisualEvidence) {
			e.ConfigRevision = 0
			e.CreatedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		}, "visual_config_changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := valid
			tc.mutate(&evidence)
			assertions := newFakeAssertionRepo()
			assertions.rows[assertion.ID] = assertion
			service := visualapp.NewBaselineService(visualapp.BaselineServiceDeps{
				Baselines: &fakeBaselineRepo{}, Assertions: assertions,
				Evidence: &fakeEvidenceReader{evidence: evidence},
				Settings: &fakeSettings{ints: map[string]int64{"visual.baseline_max_bytes": 5 << 20}},
				Clock:    &fakeClock{}, IDs: &fakeIDs{next: 200},
			})
			_, err := service.Approve(context.Background(), 4001, assertion.ID, evidence.ID, 77)
			status, code := platformCode(t, err)
			if status != 422 || code != tc.wantCode {
				t.Fatalf("status=%d code=%s want %s", status, code, tc.wantCode)
			}
		})
	}
}

func newDiffFixture(t *testing.T) (*visualapp.DiffService, *fakeDiffRepo, *fakeEvents, visual.Assertion) {
	t.Helper()
	assertion := visual.Assertion{ID: 6001, VersionID: 4001, CaseVersionID: 8001, CaseAssertionID: 9001}
	assertions := newFakeAssertionRepo()
	assertions.rows[assertion.ID] = assertion
	diffs := newFakeDiffRepo()
	events := &fakeEvents{}
	service := visualapp.NewDiffService(visualapp.DiffServiceDeps{
		Diffs: diffs, Assertions: assertions,
		Clock: &fakeClock{now: time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)},
		IDs:   &fakeIDs{next: 300}, Audit: &fakeAudit{}, Events: events,
	})
	return service, diffs, events, assertion
}

func TestDiffRecordIsIdempotent(t *testing.T) {
	service, _, events, _ := newDiffFixture(t)
	input := visual.RecordDiffInput{
		VersionID: 4001, VisualAssertionID: 6001, CaseRunID: 3001, AssertionResultID: 2001,
		ActualEvidenceID: 7001, DiffRatio: 0.02, Threshold: 0.005,
		Classification: visual.ClassificationFailed, Reason: "visual_mismatch",
		EnvironmentMatch: true, Width: 100, Height: 100,
		MismatchBoxes: json.RawMessage(`[{"x":0,"y":0,"width":32,"height":32}]`),
	}
	first, err := service.Record(context.Background(), input)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	second, err := service.Record(context.Background(), input)
	if err != nil {
		t.Fatalf("Record replay: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("replay created a second diff: %d vs %d", first.ID, second.ID)
	}
	if len(events.events) != 1 {
		t.Fatalf("events = %d", len(events.events))
	}
	if events.events[0].Type != "visual.diff_created" {
		t.Fatalf("event type = %s", events.events[0].Type)
	}
}

func TestDiffRecordRejectsEnvironmentFailure(t *testing.T) {
	service, _, _, _ := newDiffFixture(t)
	input := visual.RecordDiffInput{
		VersionID: 4001, VisualAssertionID: 6001, CaseRunID: 3001, AssertionResultID: 2001,
		ActualEvidenceID: 7001, Threshold: 0.005,
		Classification: visual.ClassificationFailed, EnvironmentMatch: false,
	}
	_, err := service.Record(context.Background(), input)
	status, code := platformCode(t, err)
	if status != 422 || code != "diff_invalid" {
		t.Fatalf("status=%d code=%s", status, code)
	}
}

func TestOverviewAggregatesCoveragePassRateAndPercentiles(t *testing.T) {
	diffs := newFakeDiffRepo()
	diffs.overview = visualapp.OverviewData{
		AssertionTotal:  4,
		BaselineCovered: 3,
		DiffCounts: map[visual.Classification]int{
			visual.ClassificationPassed: 6, visual.ClassificationFailed: 2,
			visual.ClassificationInconclusive: 1, visual.ClassificationBlocked: 1,
		},
		InconclusiveReasons: []visualapp.ReasonCount{{Reason: visual.ReasonMissingBaseline, Count: 1}},
		DiffRatios:          []float64{0, 0.001, 0.002, 0.003, 0.5},
	}
	service := visualapp.NewOverviewService(visualapp.OverviewServiceDeps{
		Diffs: diffs, Clock: &fakeClock{now: time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)},
	})
	overview, err := service.Version(context.Background(), 4001)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if overview.BaselineCoverageRatio == nil || *overview.BaselineCoverageRatio != 0.75 {
		t.Fatalf("coverage = %v", overview.BaselineCoverageRatio)
	}
	if overview.PassRatio == nil || *overview.PassRatio != 0.75 {
		t.Fatalf("pass ratio = %v", overview.PassRatio)
	}
	if overview.DiffRatioP50 == nil || *overview.DiffRatioP50 != 0.002 {
		t.Fatalf("p50 = %v", overview.DiffRatioP50)
	}
	if overview.DiffRatioP95 == nil {
		t.Fatal("p95 missing")
	}
	metrics, err := service.Global(context.Background(), nil)
	if err != nil {
		t.Fatalf("Global: %v", err)
	}
	if metrics.Diffs != 10 || metrics.Passed != 6 || metrics.Failed != 2 || metrics.Inconclusive != 1 || metrics.Blocked != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
	if metrics.GeneratedAt.IsZero() {
		t.Fatal("generated_at missing")
	}
}

func TestDiffListRejectsInvalidFilter(t *testing.T) {
	service, _, _, _ := newDiffFixture(t)
	_, _, err := service.List(context.Background(), 4001, visual.DiffFilter{Classification: "bogus"})
	status, code := platformCode(t, err)
	if status != 422 || code != "diff_invalid" {
		t.Fatalf("status=%d code=%s", status, code)
	}
}
