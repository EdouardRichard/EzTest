package versionapp_test

// Task 37 fix round 1 composition contract: the production extraction pipeline
// must actually run end to end. POST /api/versions/{id}/requirements/extractions
// must not answer the degraded 503 once the attachment source is wired, and the
// worker must process the run to done, writing the plan milestone, the facts
// and the extracted requirements.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// pipelineExtractionSource is an in-memory ExtractionIngestSource
// + ExtractionSegmentsSource built from one deterministic parse result.
type pipelineExtractionSource struct {
	input    versionapp.ExtractionStartInput
	document versionapp.ExtractionDocument
}

func (s *pipelineExtractionSource) ExtractionInputs(_ context.Context, versionID, attachmentID int64) ([]versionapp.ExtractionStartInput, error) {
	if versionID != s.input.VersionID {
		return nil, nil
	}
	if attachmentID > 0 && attachmentID != s.input.AttachmentID {
		return nil, nil
	}
	return []versionapp.ExtractionStartInput{s.input}, nil
}

func (s *pipelineExtractionSource) ExtractionDocument(_ context.Context, _, _ int64, inputSHA256 string) (versionapp.ExtractionDocument, error) {
	if inputSHA256 != s.input.InputSHA256 {
		return versionapp.ExtractionDocument{}, platform.NotFound("requirement attachment document")
	}
	return s.document, nil
}

func (s *pipelineExtractionSource) ExtractionSegments(_ context.Context, _, _ int64, inputSHA256 string) ([]versionapp.Segment, error) {
	if inputSHA256 != s.input.InputSHA256 {
		return nil, platform.NotFound("requirement attachment document")
	}
	return s.input.Segments, nil
}

func pipelineDocumentText() string {
	return "# 功能需求\n\nFR-001 用户登录\nFR-002 用户下单\nFR-003 退款申请\n"
}

func newPipelineSource(t *testing.T) *pipelineExtractionSource {
	t.Helper()
	text := pipelineDocumentText()
	segments, err := versionapp.SegmentExtraction(versionapp.ExtractionResult{Text: text}, versionapp.SegmentOptions{SourcePath: "需求文档.md"})
	if err != nil {
		t.Fatalf("SegmentExtraction: %v", err)
	}
	if len(segments) == 0 {
		t.Fatal("SegmentExtraction returned no segments")
	}
	return &pipelineExtractionSource{
		input: versionapp.ExtractionStartInput{
			VersionID: 7, AttachmentID: 3, InputSHA256: testSHA("pipeline-document"),
			Segments: segments, DocumentText: text, AttachmentName: "需求文档.md",
		},
		document: versionapp.ExtractionDocument{
			Name: "需求文档.md", Text: text, SkippedImages: 2,
		},
	}
}

func newDeterministicPipelineEnv(t *testing.T) (*extractionEnv, *pipelineExtractionSource, *fakePlanRepo, *fakeFactRepo) {
	t.Helper()
	env := newExtractionEnv(t)
	planRepo := newFakePlanRepo()
	factRepo := newFakeFactRepo()
	env.svc.SetMilestoneStores(
		versionapp.NewPlanService(planRepo, &seqIDs{}, nil),
		versionapp.NewFactService(factRepo, &seqIDs{}, nil),
	)
	source := newPipelineSource(t)
	env.svc.SetSource(source)
	env.svc.SetProcessor(versionapp.NewDeterministicExtractionProcessor(source, env.svc))
	return env, source, planRepo, factRepo
}

// TestExtractionStartEndpointRunsToDone is the composition test: the real
// extraction service+handler are wired with an attachment source/processor, the
// POST start answers 200 (previously the typed 503), and ProcessNext drives the
// run to done with the plan milestone, the two facts and the emitted FR ids.
func TestExtractionStartEndpointRunsToDone(t *testing.T) {
	env, _, planRepo, factRepo := newDeterministicPipelineEnv(t)
	ctx := context.Background()

	handler := versionapi.NewExtractionHandler(env.svc)
	request := httptest.NewRequest(http.MethodPost, "/api/versions/7/requirements/extractions", nil)
	request.SetPathValue("id", "7")
	request = request.WithContext(platform.WithActor(request.Context(), platform.Actor{ID: 42, Role: "user"}))
	recorder := httptest.NewRecorder()
	handler.Start(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST start = %d body=%s (the production pipeline must not answer 503)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data struct {
			Runs []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"runs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if len(body.Data.Runs) != 1 || body.Data.Runs[0].ID == "" {
		t.Fatalf("start runs = %#v", body.Data.Runs)
	}
	if body.Data.Runs[0].Status != "pending" {
		t.Fatalf("started run status = %q, want pending", body.Data.Runs[0].Status)
	}

	processed, err := env.svc.ProcessNext(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed runs = %d, want 1", processed)
	}
	runs, err := env.svc.ListRuns(ctx, 7)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %#v/%v", runs, err)
	}
	if runs[0].Status != versionapp.ExtractionRunDone {
		t.Fatalf("run status = %q, want done (segments=%#v)", runs[0].Status, env.repo.segments[runs[0].ID])
	}

	items := milestonePlanItems(t, planRepo, 7)
	if len(items) != 1 || items[0].Status != "done" {
		t.Fatalf("plan milestone = %#v, want one done item", items)
	}
	if !strings.HasPrefix(items[0].Decision, versionapp.ExtractionPlanDecisionMarker(3, runs[0].ID)) {
		t.Fatalf("plan decision = %q", items[0].Decision)
	}
	facts := currentFactsByKind(t, factRepo, 7)
	if len(facts) != 2 {
		t.Fatalf("facts = %#v, want attachment_parse + requirement_source", facts)
	}
	if facts["attachment_parse"].Payload["skipped_images"] != 2 {
		t.Fatalf("parse fact skipped_images = %#v, want 2", facts["attachment_parse"].Payload["skipped_images"])
	}

	if len(env.repo.requirements) != 3 {
		t.Fatalf("extracted requirements = %#v, want FR-001..003", env.repo.requirements)
	}
	for _, externalID := range []string{"FR-001", "FR-002", "FR-003"} {
		requirement, ok := env.repo.requirements[fakeRequirementKey(7, externalID)]
		if !ok || requirement.RunID != runs[0].ID {
			t.Fatalf("requirement %s = %#v, want run %d", externalID, requirement, runs[0].ID)
		}
	}
	if !strings.Contains(env.repo.requirements[fakeRequirementKey(7, "FR-001")].SourceQuote, "FR-001") {
		t.Fatalf("source quote = %q", env.repo.requirements[fakeRequirementKey(7, "FR-001")].SourceQuote)
	}
}

// TestDeterministicProcessorSkipsImageSegments pins the C5 processor contract:
// image-only segments are skipped (never emitted) and a text segment without FR
// ids is done with zero requirements.
func TestDeterministicProcessorSkipsImageSegments(t *testing.T) {
	source := newPipelineSource(t)
	segments, err := versionapp.SegmentExtraction(versionapp.ExtractionResult{
		Images: []versionapp.ExtractionImage{{Seq: 1, Name: "login.png", Source: "word/media/image1.png", MIME: "image/png"}},
	}, versionapp.SegmentOptions{SourcePath: "需求文档.md"})
	if err != nil || len(segments) != 1 {
		t.Fatalf("image segmentation = %#v/%v", segments, err)
	}
	source.input.Segments = segments
	processor := versionapp.NewDeterministicExtractionProcessor(source, nil)
	// The sink is only needed for emitted requirements; a skipped segment must
	// not touch it, so a nil sink proves the branch does not emit.
	run := versionapp.ExtractionRun{ID: 11, VersionID: 7, AttachmentID: 3, InputSHA256: source.input.InputSHA256}
	result, err := processor.ProcessSegment(context.Background(), run, versionapp.ExtractionSegment{Seq: 1, TextSHA256: segments[0].TextSHA256})
	if err != nil {
		t.Fatalf("ProcessSegment(image): %v", err)
	}
	if !result.Skipped || result.ExtractedCount != 0 {
		t.Fatalf("image segment result = %#v, want skipped", result)
	}
}
