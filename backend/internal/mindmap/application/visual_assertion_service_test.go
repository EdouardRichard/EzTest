package application_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// visualFixture bundles the fakes of the visual assertion use cases.
type visualFixture struct {
	store     *fakeVisualStore
	evidence  *fakeVisualEvidence
	versions  *fakeVisualVersions
	baselines *fakeVisualBaselines
	events    *fakeVisualEvents
	now       time.Time
}

func newVisualFixture() *visualFixture {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	return &visualFixture{
		store: &fakeVisualStore{target: application.VisualAssertionTarget{
			NodeID: 12, NodeRevision: 3, Kind: domain.AssertionKindVisual,
			Visual: &domain.VisualAssertion{
				AssertionID: 77, Selector: "#total", Threshold: 0.005,
				ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1, Revision: 1,
			},
		}},
		evidence:  &fakeVisualEvidence{},
		versions:  &fakeVisualVersions{status: "designing"},
		baselines: &fakeVisualBaselines{},
		events:    &fakeVisualEvents{},
		now:       now,
	}
}

func (f *visualFixture) service() *application.VisualAssertionService {
	return application.NewVisualAssertionService(application.VisualAssertionDeps{
		Store:     f.store,
		Evidence:  f.evidence,
		Versions:  f.versions,
		Baselines: f.baselines,
		Events:    f.events,
		Clock:     fixedVisualClock(f.now),
	})
}

// pngBytes renders a plain w×h PNG.
func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func visualReason(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %v is not a domain error", err)
	}
	return domainErr.Reason
}

func TestUpdateVisualAssertionPersistsValidatedParams(t *testing.T) {
	fixture := newVisualFixture()
	region := &domain.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3}

	stored, err := fixture.service().Update(context.Background(), 5, 77, 42, application.UpdateVisualAssertionCommand{
		NodeRevision: 3, Selector: "#chart", Region: region, Threshold: 0.02,
		ViewportWidth: 1440, ViewportHeight: 900, DeviceScaleFactor: 2,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fixture.store.saveCalls != 1 {
		t.Fatalf("save calls = %d, want 1", fixture.store.saveCalls)
	}
	saved := fixture.store.savedParams
	if saved.Selector != "#chart" || saved.Threshold != 0.02 ||
		saved.ViewportWidth != 1440 || saved.ViewportHeight != 900 || saved.DeviceScaleFactor != 2 {
		t.Fatalf("saved params = %+v", saved)
	}
	if saved.Region == nil || *saved.Region != *region {
		t.Fatalf("saved region = %+v, want %+v", saved.Region, region)
	}
	if stored.AssertionID != 77 || stored.Revision != 2 || stored.Threshold != 0.02 {
		t.Fatalf("stored visual = %+v", stored)
	}
}

func TestUpdateVisualAssertionAppliesDefaults(t *testing.T) {
	fixture := newVisualFixture()
	_, err := fixture.service().Update(context.Background(), 5, 77, 42, application.UpdateVisualAssertionCommand{
		NodeRevision: 3, Threshold: 0.005,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	saved := fixture.store.savedParams
	if saved.ViewportWidth != domain.VisualViewportDefaultWidth ||
		saved.ViewportHeight != domain.VisualViewportDefaultHeight ||
		saved.DeviceScaleFactor != domain.VisualDeviceScaleFactorDefault {
		t.Fatalf("defaults = %+v", saved)
	}
}

func TestUpdateVisualAssertionRejectsNonVisualAssertion(t *testing.T) {
	fixture := newVisualFixture()
	fixture.store.target.Kind = domain.AssertionKindUI
	_, err := fixture.service().Update(context.Background(), 5, 77, 42, application.UpdateVisualAssertionCommand{
		NodeRevision: 3, Threshold: 0.005,
	})
	if reason := visualReason(t, err); reason != "invalid_assertion_kind" {
		t.Fatalf("reason = %q, want invalid_assertion_kind", reason)
	}
	if fixture.store.saveCalls != 0 {
		t.Fatalf("a non-visual assertion was saved: %d", fixture.store.saveCalls)
	}
}

func TestUpdateVisualAssertionRejectsInvalidParams(t *testing.T) {
	cases := []struct {
		name string
		cmd  application.UpdateVisualAssertionCommand
	}{
		{"threshold above max", application.UpdateVisualAssertionCommand{NodeRevision: 3, Threshold: 0.5}},
		{"negative threshold", application.UpdateVisualAssertionCommand{NodeRevision: 3, Threshold: -0.1}},
		{"region outside unit square", application.UpdateVisualAssertionCommand{
			NodeRevision: 3, Threshold: 0.01, Region: &domain.VisualRegion{X: 0.8, Y: 0, Width: 0.5, Height: 0.5},
		}},
		{"negative viewport", application.UpdateVisualAssertionCommand{NodeRevision: 3, Threshold: 0.01, ViewportWidth: -1, ViewportHeight: 720}},
		{"device scale factor", application.UpdateVisualAssertionCommand{NodeRevision: 3, Threshold: 0.01, DeviceScaleFactor: 8}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newVisualFixture()
			_, err := fixture.service().Update(context.Background(), 5, 77, 42, testCase.cmd)
			if err == nil {
				t.Fatalf("expected a validation error")
			}
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindInvalid {
				t.Fatalf("error = %v, want invalid", err)
			}
			if fixture.store.saveCalls != 0 {
				t.Fatalf("invalid params reached the store")
			}
		})
	}
}

func TestUpdateVisualAssertionRejectsFrozenVersion(t *testing.T) {
	fixture := newVisualFixture()
	fixture.baselines.baseline = &domain.CaseBaseline{ID: 1, VersionID: 5}
	_, err := fixture.service().Update(context.Background(), 5, 77, 42, application.UpdateVisualAssertionCommand{
		NodeRevision: 3, Threshold: 0.005,
	})
	if reason := visualReason(t, err); reason != "baseline_frozen" {
		t.Fatalf("reason = %q, want baseline_frozen", reason)
	}
}

func TestUpdateVisualAssertionRejectsStaleRevision(t *testing.T) {
	fixture := newVisualFixture()
	_, err := fixture.service().Update(context.Background(), 5, 77, 42, application.UpdateVisualAssertionCommand{
		NodeRevision: 2, Threshold: 0.005,
	})
	if reason := visualReason(t, err); reason != "case_revision_conflict" {
		t.Fatalf("reason = %q, want case_revision_conflict", reason)
	}
}

func TestApproveVisualBaselinePersistsAndPublishesIdentifiersOnly(t *testing.T) {
	fixture := newVisualFixture()
	data := pngBytes(t, 1280, 720)
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "screenshot", Mime: "image/png",
		Data: data, SizeBytes: int64(len(data)),
	}

	stored, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if err != nil {
		t.Fatalf("ApproveBaseline: %v", err)
	}
	if fixture.store.approvedEvidenceID != 501 || fixture.store.approvedActor != 1 {
		t.Fatalf("approved evidence/actor = %d/%d", fixture.store.approvedEvidenceID, fixture.store.approvedActor)
	}
	if !stored.HasBaseline() || *stored.BaselineEvidenceID != 501 {
		t.Fatalf("stored baseline = %+v", stored)
	}
	if len(fixture.events.events) != 1 {
		t.Fatalf("events = %+v, want one", fixture.events.events)
	}
	event := fixture.events.events[0]
	if event.Type != "case.visual_baseline_approved" {
		t.Fatalf("event type = %q", event.Type)
	}
	if event.VersionID != 5 || len(event.RefCaseIDs) != 1 || event.RefCaseIDs[0] != 12 {
		t.Fatalf("event version/refs = %+v", event)
	}
	wantKeys := map[string]bool{
		"caseNodeId": true, "assertionId": true, "baselineEvidenceId": true,
		"approvedBy": true, "status": true,
	}
	if len(event.Detail) != len(wantKeys) {
		t.Fatalf("event detail = %+v, want only ids/status", event.Detail)
	}
	for key, value := range event.Detail {
		if !wantKeys[key] {
			t.Fatalf("event detail carries unexpected key %q", key)
		}
		if key == "status" {
			if value != "approved" {
				t.Fatalf("status = %v", value)
			}
			continue
		}
		if _, ok := value.(int64); !ok {
			t.Fatalf("detail %q = %T, want an id", key, value)
		}
	}
}

// TestApproveVisualBaselineAfterExecutionFreeze pins the only admissible
// baseline flow of design spec §9.3: the candidate screenshot comes from an
// execution run, so the version is already executing with a frozen case
// baseline when the administrator approves it. Update stays closed after the
// freeze (baseline_frozen); approval must not.
func TestApproveVisualBaselineAfterExecutionFreeze(t *testing.T) {
	fixture := newVisualFixture()
	fixture.versions.status = "executing"
	fixture.baselines.baseline = &domain.CaseBaseline{ID: 9, VersionID: 5}
	data := pngBytes(t, 1280, 720)
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "screenshot", Mime: "image/png",
		Data: data, SizeBytes: int64(len(data)),
	}

	stored, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if err != nil {
		t.Fatalf("ApproveBaseline after the execution freeze: %v", err)
	}
	if stored.BaselineEvidenceID == nil || *stored.BaselineEvidenceID != 501 {
		t.Fatalf("stored baseline = %+v, want evidence 501", stored)
	}

	// Editing the comparison parameters remains forbidden once the baseline is
	// frozen; only the baseline pointer is mutable through the approval.
	_, err = fixture.service().Update(context.Background(), 5, 77, 42, application.UpdateVisualAssertionCommand{
		NodeRevision: 3, Threshold: 0.02,
	})
	if reason := visualReason(t, err); reason != "baseline_frozen" {
		t.Fatalf("Update reason = %q, want baseline_frozen", reason)
	}
	if fixture.store.saveCalls != 0 {
		t.Fatalf("Update reached the store after the baseline freeze")
	}
}

func TestApproveVisualBaselineRejectsNonPNGEvidence(t *testing.T) {
	fixture := newVisualFixture()
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "screenshot", Mime: "text/plain", Data: []byte("not a png"), SizeBytes: 9,
	}
	_, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if reason := visualReason(t, err); reason != "visual_baseline_invalid" {
		t.Fatalf("reason = %q, want visual_baseline_invalid", reason)
	}
	if fixture.store.approveCalls != 0 {
		t.Fatalf("invalid evidence reached the store")
	}
}

// TestApproveVisualBaselineRejectsForeignEvidenceKind pins the §9.3 approval
// vocabulary: only a screenshot or a dedicated visual_baseline artifact may be
// promoted to baseline. A download/console PNG (or any other kind) is a 422
// invalid_argument, never an approved baseline.
func TestApproveVisualBaselineRejectsForeignEvidenceKind(t *testing.T) {
	for _, kind := range []string{"download", "http_exchange", "db_result", ""} {
		fixture := newVisualFixture()
		data := pngBytes(t, 1280, 720)
		fixture.evidence.evidence = application.VisualEvidence{
			ID: 501, Kind: kind, Mime: "image/png", Data: data, SizeBytes: int64(len(data)),
		}
		_, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindInvalid {
			t.Fatalf("kind %q: err = %v (%T), want a KindInvalid domain error", kind, err, err)
		}
		if reason := visualReason(t, err); reason != "visual_baseline_invalid" {
			t.Fatalf("kind %q: reason = %q, want visual_baseline_invalid", kind, reason)
		}
		if fixture.store.approveCalls != 0 {
			t.Fatalf("kind %q reached the store", kind)
		}
	}
}

// TestApproveVisualBaselineAcceptsDedicatedBaselineKind proves the second
// allowed kind, visual_baseline, is approved like a screenshot.
func TestApproveVisualBaselineAcceptsDedicatedBaselineKind(t *testing.T) {
	fixture := newVisualFixture()
	data := pngBytes(t, 1280, 720)
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "visual_baseline", Mime: "image/png", Data: data, SizeBytes: int64(len(data)),
	}
	if _, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501); err != nil {
		t.Fatalf("ApproveBaseline: %v", err)
	}
	if fixture.store.approvedEvidenceID != 501 {
		t.Fatalf("approved evidence = %d, want 501", fixture.store.approvedEvidenceID)
	}
}

func TestApproveVisualBaselineRejectsDimensionMismatch(t *testing.T) {
	fixture := newVisualFixture()
	// A full-page assertion compares the whole viewport, so the baseline must be
	// exactly viewport×DPR.
	fixture.store.target.Visual.Selector = ""
	data := pngBytes(t, 640, 360)
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "screenshot", Mime: "image/png", Data: data, SizeBytes: int64(len(data)),
	}
	_, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if reason := visualReason(t, err); reason != "visual_baseline_invalid" {
		t.Fatalf("reason = %q, want visual_baseline_invalid", reason)
	}
}

// TestApproveVisualBaselineAcceptsFullPageViewportSize proves the strict
// viewport×DPR check still accepts a correctly sized full-page baseline.
func TestApproveVisualBaselineAcceptsFullPageViewportSize(t *testing.T) {
	fixture := newVisualFixture()
	fixture.store.target.Visual.Selector = ""
	data := pngBytes(t, 1280, 720)
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "screenshot", Mime: "image/png", Data: data, SizeBytes: int64(len(data)),
	}
	if _, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501); err != nil {
		t.Fatalf("ApproveBaseline(full page): %v", err)
	}
}

// TestApproveVisualBaselineAcceptsSelectorElementSize pins the selector
// baseline-size contract: an element screenshot is naturally smaller than
// viewport×DPR, so approval validates only PNG/size/positive dimensions for a
// selector assertion. The executor compares the element screenshot against it
// later; a size difference there is an environment mismatch, not a failure.
func TestApproveVisualBaselineAcceptsSelectorElementSize(t *testing.T) {
	fixture := newVisualFixture()
	if fixture.store.target.Visual.Selector == "" {
		t.Fatal("fixture must configure a selector assertion")
	}
	data := pngBytes(t, 640, 360)
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "screenshot", Mime: "image/png", Data: data, SizeBytes: int64(len(data)),
	}
	stored, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if err != nil {
		t.Fatalf("ApproveBaseline(selector element): %v", err)
	}
	if fixture.store.approvedEvidenceID != 501 || !stored.HasBaseline() {
		t.Fatalf("selector baseline not approved: store=%d stored=%+v", fixture.store.approvedEvidenceID, stored)
	}
}

// TestApproveVisualBaselineRejectsCorruptSelectorBaseline keeps the PNG proof
// for selector assertions even though the viewport dimension check is relaxed.
func TestApproveVisualBaselineRejectsCorruptSelectorBaseline(t *testing.T) {
	fixture := newVisualFixture()
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "screenshot", Mime: "image/png",
		Data: []byte("not a png"), SizeBytes: int64(len("not a png")),
	}
	_, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if reason := visualReason(t, err); reason != "visual_baseline_invalid" {
		t.Fatalf("reason = %q, want visual_baseline_invalid", reason)
	}
	if fixture.store.approveCalls != 0 {
		t.Fatalf("corrupt selector baseline reached the store")
	}
}

func TestApproveVisualBaselineRejectsMissingEvidence(t *testing.T) {
	fixture := newVisualFixture()
	fixture.evidence.err = domain.NewNotFound("evidence", 501)
	_, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if reason := visualReason(t, err); reason != "evidence_not_found" {
		t.Fatalf("reason = %q, want evidence_not_found", reason)
	}
}

func TestApproveVisualBaselineRejectsOversizedEvidence(t *testing.T) {
	fixture := newVisualFixture()
	fixture.evidence.evidence = application.VisualEvidence{
		ID: 501, Kind: "screenshot", Mime: "image/png",
		SizeBytes: (5 << 20) + 1, Data: make([]byte, (5<<20)+1),
	}
	_, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if reason := visualReason(t, err); reason != "visual_baseline_too_large" {
		t.Fatalf("reason = %q, want visual_baseline_too_large", reason)
	}
}

func TestApproveVisualBaselineRequiresVisualAssertion(t *testing.T) {
	fixture := newVisualFixture()
	fixture.store.target.Kind = domain.AssertionKindText
	_, err := fixture.service().ApproveBaseline(context.Background(), 5, 77, 1, 501)
	if reason := visualReason(t, err); reason != "invalid_assertion_kind" {
		t.Fatalf("reason = %q, want invalid_assertion_kind", reason)
	}
}

func TestVisualAssertionExpectedImageSize(t *testing.T) {
	visual := domain.VisualAssertion{ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1.5}
	width, height := visual.ExpectedImageSize()
	if width != 1920 || height != 1080 {
		t.Fatalf("expected size = %dx%d, want 1920x1080", width, height)
	}
}

// fakeVisualStore is an in-memory VisualAssertionStore.
type fakeVisualStore struct {
	target             application.VisualAssertionTarget
	getErr             error
	saveCalls          int
	approveCalls       int
	savedParams        domain.VisualAssertionParams
	approvedEvidenceID int64
	approvedActor      int64
}

func (f *fakeVisualStore) Get(_ context.Context, _, _ int64) (application.VisualAssertionTarget, error) {
	if f.getErr != nil {
		return application.VisualAssertionTarget{}, f.getErr
	}
	return f.target, nil
}

func (f *fakeVisualStore) Save(_ context.Context, _, assertionID int64, expectedNodeRevision int, params domain.VisualAssertionParams, _ int64, now time.Time) (domain.VisualAssertion, error) {
	f.saveCalls++
	if f.target.NodeRevision != expectedNodeRevision {
		return domain.VisualAssertion{}, domain.NewStateConflict("case_revision_conflict", "stale revision")
	}
	revision := 1
	var baseline *int64
	if f.target.Visual != nil {
		revision = f.target.Visual.Revision + 1
		baseline = f.target.Visual.BaselineEvidenceID
	}
	stored := domain.VisualAssertion{
		AssertionID: assertionID, Selector: params.Selector, Region: params.Region,
		Threshold: params.Threshold, BaselineEvidenceID: baseline,
		ViewportWidth: params.ViewportWidth, ViewportHeight: params.ViewportHeight,
		DeviceScaleFactor: params.DeviceScaleFactor, Revision: revision,
		CreatedAt: now, UpdatedAt: now,
	}
	f.savedParams = params
	f.target.Visual = &stored
	f.target.NodeRevision++
	return stored, nil
}

func (f *fakeVisualStore) ApproveBaseline(_ context.Context, _, assertionID, evidenceID, actor int64, now time.Time) (domain.VisualAssertion, error) {
	f.approveCalls++
	f.approvedEvidenceID = evidenceID
	f.approvedActor = actor
	visual := domain.VisualAssertion{AssertionID: assertionID, Threshold: 0.005, ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1}
	if f.target.Visual != nil {
		visual = *f.target.Visual
	}
	value := evidenceID
	approver := actor
	visual.BaselineEvidenceID = &value
	visual.ApprovedBy = &approver
	visual.ApprovedAt = &now
	visual.UpdatedAt = now
	f.target.Visual = &visual
	return visual, nil
}

// fakeVisualEvidence is an in-memory VisualBaselineEvidenceReader.
type fakeVisualEvidence struct {
	evidence      application.VisualEvidence
	err           error
	gotVersionID  int64
	gotCaseNodeID int64
	gotEvidenceID int64
}

func (f *fakeVisualEvidence) ReadForBaseline(_ context.Context, versionID, caseNodeID, evidenceID int64) (application.VisualEvidence, error) {
	f.gotVersionID, f.gotCaseNodeID, f.gotEvidenceID = versionID, caseNodeID, evidenceID
	if f.err != nil {
		return application.VisualEvidence{}, f.err
	}
	return f.evidence, nil
}

// fakeVisualVersions is a VersionLifecycle with a fixed status.
type fakeVisualVersions struct {
	status string
}

func (f *fakeVisualVersions) GetVersion(_ context.Context, versionID int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{ID: versionID, Status: f.status}, nil
}

func (f *fakeVisualVersions) EnterReview(context.Context, int64, int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, errors.New("not supported")
}

func (f *fakeVisualVersions) RejectReview(context.Context, int64, int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, errors.New("not supported")
}

func (f *fakeVisualVersions) ApproveBaseline(context.Context, int64, int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, errors.New("not supported")
}

// fakeVisualBaselines optionally reports a frozen baseline.
type fakeVisualBaselines struct {
	baseline *domain.CaseBaseline
}

func (f *fakeVisualBaselines) Create(context.Context, domain.CaseBaseline) (domain.CaseBaseline, error) {
	return domain.CaseBaseline{}, errors.New("not supported")
}

func (f *fakeVisualBaselines) GetByVersion(_ context.Context, versionID int64) (domain.CaseBaseline, error) {
	if f.baseline != nil {
		return *f.baseline, nil
	}
	return domain.CaseBaseline{}, domain.NewNotFound("baseline", versionID)
}

func (f *fakeVisualBaselines) DeleteByVersion(context.Context, int64) error {
	return errors.New("not supported")
}

// fakeVisualEvents records published events.
type fakeVisualEvents struct {
	events []application.Event
}

func (f *fakeVisualEvents) Publish(_ context.Context, event application.Event) error {
	f.events = append(f.events, event)
	return nil
}

// fixedVisualClock is a Clock returning a fixed instant.
type fixedVisualClock time.Time

func (c fixedVisualClock) Now() time.Time { return time.Time(c) }
