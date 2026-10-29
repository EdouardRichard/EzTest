package codeintel_test

import (
	"context"
	"errors"
	"testing"

	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmapcodeintel "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/codeintel"
)

// fakeAnchorService records the codeintel anchor calls.
type fakeAnchorService struct {
	createVersionID int64
	createActorID   int64
	createParams    codeinteldomain.CreateAnchorParams
	created         codeinteldomain.CodeAnchor
	createErr       error

	listVersionID int64
	listFilter    codeinteldomain.AnchorFilter
	anchors       []codeinteldomain.CodeAnchor
	listErr       error

	deletedVersionID int64
	deletedAnchorID  int64
	deleteErr        error
}

// Create implements the codeintel anchor creation.
func (f *fakeAnchorService) Create(_ context.Context, versionID, actorID int64, params codeinteldomain.CreateAnchorParams) (codeinteldomain.CodeAnchor, error) {
	f.createVersionID, f.createActorID, f.createParams = versionID, actorID, params
	if f.createErr != nil {
		return codeinteldomain.CodeAnchor{}, f.createErr
	}
	return f.created, nil
}

// List implements the codeintel anchor read.
func (f *fakeAnchorService) List(_ context.Context, versionID int64, filter codeinteldomain.AnchorFilter) ([]codeinteldomain.CodeAnchor, error) {
	f.listVersionID, f.listFilter = versionID, filter
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.anchors, nil
}

// Delete implements the codeintel anchor deletion.
func (f *fakeAnchorService) Delete(_ context.Context, versionID, anchorID int64) error {
	f.deletedVersionID, f.deletedAnchorID = versionID, anchorID
	return f.deleteErr
}

// TestAnchorAdapterCreatesCaseAnchors pins the hardcoded subject_kind='case' and
// the anchor projection.
func TestAnchorAdapterCreatesCaseAnchors(t *testing.T) {
	ctx := context.Background()
	startLine, endLine := 10, 20
	commit := "0123456789abcdef0123456789abcdef01234567"
	service := &fakeAnchorService{created: codeinteldomain.CodeAnchor{
		ID:            77,
		VersionID:     7,
		RepoID:        9,
		SubjectKind:   codeinteldomain.SubjectCase,
		SubjectID:     55,
		FilePath:      "src/login.ts",
		StartLine:     &startLine,
		EndLine:       &endLine,
		CommitSHA:     &commit,
		Confidence:    codeinteldomain.ConfidenceHigh,
		CreatedByKind: "user",
		CreatedBy:     42,
	}}
	adapter := mindmapcodeintel.NewAnchorAdapter(service)
	var _ mindmapapp.CodeAnchorGateway = adapter

	got, err := adapter.Create(ctx, 7, 42, mindmapapp.AnchorParams{
		SubjectID:  55,
		FilePath:   "src/login.ts",
		StartLine:  &startLine,
		EndLine:    &endLine,
		Confidence: "medium",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if service.createVersionID != 7 || service.createActorID != 42 {
		t.Fatalf("create version/actor=%d/%d want 7/42", service.createVersionID, service.createActorID)
	}
	if service.createParams.SubjectKind != codeinteldomain.SubjectCase {
		t.Fatalf("subject kind=%q want case", service.createParams.SubjectKind)
	}
	if service.createParams.SubjectID != 55 || service.createParams.FilePath != "src/login.ts" {
		t.Fatalf("create params=%+v", service.createParams)
	}
	if service.createParams.StartLine == nil || *service.createParams.StartLine != startLine ||
		service.createParams.EndLine == nil || *service.createParams.EndLine != endLine {
		t.Fatalf("line range=%v-%v want %d-%d", service.createParams.StartLine, service.createParams.EndLine, startLine, endLine)
	}
	if got.ID != 77 || got.VersionID != 7 || got.RepoID != 9 || got.SubjectID != 55 ||
		got.FilePath != "src/login.ts" || got.CommitSHA == nil || *got.CommitSHA != commit ||
		got.Confidence != codeinteldomain.ConfidenceHigh || got.CreatedBy != 42 {
		t.Fatalf("anchor=%+v want the codeintel projection", got)
	}
}

// TestAnchorAdapterListsAndDeletesCaseAnchors pins the case filter, the mapping
// and the version-scoped deletion.
func TestAnchorAdapterListsAndDeletesCaseAnchors(t *testing.T) {
	ctx := context.Background()
	service := &fakeAnchorService{anchors: []codeinteldomain.CodeAnchor{{
		ID:          77,
		VersionID:   7,
		RepoID:      9,
		SubjectKind: codeinteldomain.SubjectCase,
		SubjectID:   55,
		FilePath:    "src/login.ts",
	}}}
	adapter := mindmapcodeintel.NewAnchorAdapter(service)

	anchors, err := adapter.List(ctx, 7, 55)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(anchors) != 1 || anchors[0].ID != 77 || anchors[0].FilePath != "src/login.ts" {
		t.Fatalf("anchors=%+v want anchor 77", anchors)
	}
	if service.listVersionID != 7 {
		t.Fatalf("list version=%d want 7", service.listVersionID)
	}
	if service.listFilter.SubjectKind != codeinteldomain.SubjectCase ||
		service.listFilter.SubjectID == nil || *service.listFilter.SubjectID != 55 {
		t.Fatalf("list filter=%+v want case subject 55", service.listFilter)
	}

	if err := adapter.Delete(ctx, 7, 77); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if service.deletedVersionID != 7 || service.deletedAnchorID != 77 {
		t.Fatalf("delete version/id=%d/%d want 7/77", service.deletedVersionID, service.deletedAnchorID)
	}
}

// TestAnchorAdapterMapsCodeIntelErrors pins the conflict/not-found/invalid
// translations.
func TestAnchorAdapterMapsCodeIntelErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		err        error
		wantKind   string
		wantReason string
	}{
		{
			name:       "duplicate",
			err:        codeinteldomain.NewConflict("anchor_duplicate", "an anchor already exists"),
			wantKind:   mindmapdomain.KindConflict,
			wantReason: "anchor_duplicate",
		},
		{
			name:       "not found",
			err:        codeinteldomain.NewNotFound("anchor_not_found", "anchor 77 not found"),
			wantKind:   mindmapdomain.KindNotFound,
			wantReason: "anchor_not_found",
		},
		{
			name:       "path not found",
			err:        codeinteldomain.NewInvalid("filePath", "path_not_found", "unknown file"),
			wantKind:   mindmapdomain.KindInvalid,
			wantReason: "path_not_found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeAnchorService{createErr: tc.err, deleteErr: tc.err}
			adapter := mindmapcodeintel.NewAnchorAdapter(service)

			_, err := adapter.Create(ctx, 7, 42, mindmapapp.AnchorParams{SubjectID: 55, FilePath: "a.go"})
			var mapped *mindmapdomain.Error
			if !errors.As(err, &mapped) || mapped.Kind != tc.wantKind || mapped.Reason != tc.wantReason {
				t.Fatalf("Create error=%v want kind/reason %q/%q", err, tc.wantKind, tc.wantReason)
			}

			err = adapter.Delete(ctx, 7, 77)
			if !errors.As(err, &mapped) || mapped.Kind != tc.wantKind || mapped.Reason != tc.wantReason {
				t.Fatalf("Delete error=%v want kind/reason %q/%q", err, tc.wantKind, tc.wantReason)
			}
		})
	}
}
