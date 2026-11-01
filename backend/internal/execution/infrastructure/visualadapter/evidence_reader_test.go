package visualadapter_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/visualadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeEvidence struct {
	row execution.Evidence
	err error
}

func (f *fakeEvidence) Get(context.Context, int64) (execution.Evidence, error) {
	return f.row, f.err
}

type fakeCaseRuns struct{ row execution.CaseRun }

func (f *fakeCaseRuns) Get(context.Context, int64) (execution.CaseRun, error) {
	return f.row, nil
}

type fakeIntents struct{ row execution.ExecutionIntent }

func (f *fakeIntents) Get(context.Context, int64) (execution.ExecutionIntent, error) {
	return f.row, nil
}

type fakeContent struct{ data []byte }

func (f *fakeContent) Open(context.Context, string) ([]byte, error) {
	return f.data, nil
}

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 1, G: 2, B: 3, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func TestVisualEvidenceReturnsBytesAndDimensions(t *testing.T) {
	data := pngBytes(t, 64, 40)
	sutID := int64(7)
	reader := visualadapter.NewEvidenceReader(visualadapter.EvidenceReaderDeps{
		Evidence: &fakeEvidence{row: execution.Evidence{
			ID: 7001, CaseRunID: 3001, VersionSUTID: &sutID, Kind: execution.EvidenceVisualActual,
			SHA256: "abc", SizeBytes: int64(len(data)), MIME: "image/png",
		}},
		CaseRuns: &fakeCaseRuns{row: execution.CaseRun{ID: 3001, IntentID: 1001, CaseVersionID: 41}},
		Intents:  &fakeIntents{row: execution.ExecutionIntent{ID: 1001, VersionID: 10}},
		Content:  &fakeContent{data: data},
	})
	evidence, err := reader.VisualEvidence(context.Background(), 10, 7001)
	if err != nil {
		t.Fatalf("VisualEvidence: %v", err)
	}
	if evidence.Kind != "visual_actual" || evidence.CaseVersionID != 41 || notNil(evidence.VersionSUTID) != 7 {
		t.Fatalf("evidence = %+v", evidence)
	}
	if evidence.Width != 64 || evidence.Height != 40 {
		t.Fatalf("dimensions = %dx%d", evidence.Width, evidence.Height)
	}
	if !bytes.Equal(evidence.Data, data) {
		t.Fatal("evidence bytes were not returned")
	}
	if evidence.ConfigRevision != 0 {
		t.Fatalf("config revision = %d, want 0 (timestamp fallback)", evidence.ConfigRevision)
	}
}

func TestVisualEvidenceRejectsAnotherVersion(t *testing.T) {
	reader := visualadapter.NewEvidenceReader(visualadapter.EvidenceReaderDeps{
		Evidence: &fakeEvidence{row: execution.Evidence{ID: 7001, CaseRunID: 3001, Kind: execution.EvidenceVisualActual}},
		CaseRuns: &fakeCaseRuns{row: execution.CaseRun{ID: 3001, IntentID: 1001, CaseVersionID: 41}},
		Intents:  &fakeIntents{row: execution.ExecutionIntent{ID: 1001, VersionID: 10}},
	})
	if _, err := reader.VisualEvidence(context.Background(), 11, 7001); err == nil {
		t.Fatal("cross-version evidence must be not_found")
	}
}

func TestVisualEvidenceMapsMissingEvidence(t *testing.T) {
	reader := visualadapter.NewEvidenceReader(visualadapter.EvidenceReaderDeps{
		Evidence: &fakeEvidence{err: execution.ErrNotFound},
		CaseRuns: &fakeCaseRuns{},
		Intents:  &fakeIntents{},
	})
	_, err := reader.VisualEvidence(context.Background(), 10, 7001)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("err = %v", err)
	}
}

func notNil(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
