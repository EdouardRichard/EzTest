package version_test

import (
	"context"
	"errors"
	"testing"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmapversion "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/version"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeRequirementLister serves a fixed sequence of requirement pages and records
// the status filter and cursors the adapter asks for.
type fakeRequirementLister struct {
	pages   [][]versionapp.Requirement
	more    []bool
	err     error
	calls   int
	status  []string
	cursors []string
}

// List implements the version requirement page read.
func (f *fakeRequirementLister) List(_ context.Context, versionID int64, status string, page versiondomain.Page) ([]versionapp.Requirement, bool, error) {
	f.status = append(f.status, status)
	f.cursors = append(f.cursors, page.Cursor)
	if f.err != nil {
		return nil, false, f.err
	}
	index := f.calls
	f.calls++
	if index >= len(f.pages) {
		return nil, false, nil
	}
	return f.pages[index], f.more[index], nil
}

// fakeRelationLister returns the configured relations of one version.
type fakeRelationLister struct {
	relations []versionapp.Relation
	err       error
	versionID int64
}

// List implements the version relation read.
func (f *fakeRelationLister) List(_ context.Context, versionID int64) ([]versionapp.Relation, error) {
	f.versionID = versionID
	if f.err != nil {
		return nil, f.err
	}
	return f.relations, nil
}

// fakeAttachmentReader returns attachment metadata and text content per
// requirement so the adapter digest can be asserted.
type fakeAttachmentReader struct {
	byRequirement map[int64][]versionapp.Attachment
	texts         map[int64]string
	reads         []int64
	listCalls     []int64
}

// List implements the version attachment metadata read.
func (f *fakeAttachmentReader) List(_ context.Context, versionID, requirementID int64) ([]versionapp.Attachment, error) {
	f.listCalls = append(f.listCalls, requirementID)
	return f.byRequirement[requirementID], nil
}

// ReadText implements the version text attachment read.
func (f *fakeAttachmentReader) ReadText(_ context.Context, versionID, attachmentID int64, maxBytes int) (string, error) {
	f.reads = append(f.reads, attachmentID)
	return f.texts[attachmentID], nil
}

// TestRequirementReaderMapsConfirmedRequirements pins the requirement projection
// and the text-attachment digest: only clarified requirements are requested and
// image attachments are never read.
func TestRequirementReaderMapsConfirmedRequirements(t *testing.T) {
	ctx := context.Background()
	requirements := &fakeRequirementLister{
		pages: [][]versionapp.Requirement{{
			{
				ID:                 1,
				VersionID:          7,
				Title:              "Login",
				Description:        "user can log in",
				AcceptanceCriteria: "redirected to home",
				Priority:           versiondomain.PriorityMust,
				Status:             versiondomain.RequirementClarified,
			},
		}},
		more: []bool{false},
	}
	attachments := &fakeAttachmentReader{
		byRequirement: map[int64][]versionapp.Attachment{
			1: {
				{ID: 101, RequirementID: 1, Kind: versiondomain.AttachmentText, Filename: "spec.md"},
				{ID: 102, RequirementID: 1, Kind: versiondomain.AttachmentText, Filename: "notes.txt"},
				{ID: 103, RequirementID: 1, Kind: versiondomain.AttachmentImage, Filename: "mock.png"},
			},
		},
		texts: map[int64]string{101: "first", 102: "second"},
	}
	adapter := mindmapversion.NewRequirementReader(requirements, &fakeRelationLister{}, attachments)
	var _ mindmapapp.RequirementReader = adapter

	mapped, err := adapter.ListConfirmed(ctx, 7)
	if err != nil {
		t.Fatalf("ListConfirmed: %v", err)
	}
	if len(mapped) != 1 {
		t.Fatalf("mapped %d requirements, want 1", len(mapped))
	}
	got := mapped[0]
	if got.ID != 1 || got.VersionID != 7 || got.Title != "Login" || got.Description != "user can log in" ||
		got.AcceptanceCriteria != "redirected to home" || got.Priority != versiondomain.PriorityMust ||
		got.Status != versiondomain.RequirementClarified {
		t.Fatalf("requirement=%+v want the source fields", got)
	}
	if got.AttachmentSummary != "first\n\nsecond" {
		t.Fatalf("AttachmentSummary=%q want %q", got.AttachmentSummary, "first\n\nsecond")
	}
	for _, id := range attachments.reads {
		if id == 103 {
			t.Fatal("image attachment 103 was read; images are never injected")
		}
	}
	if len(requirements.status) != 1 || requirements.status[0] != versiondomain.RequirementClarified {
		t.Fatalf("status filters=%v want [clarified]", requirements.status)
	}
}

// TestRequirementReaderDrainsRequirementPages pins that the adapter follows the
// version cursor pages instead of truncating at the first page.
func TestRequirementReaderDrainsRequirementPages(t *testing.T) {
	ctx := context.Background()
	requirements := &fakeRequirementLister{
		pages: [][]versionapp.Requirement{
			{{ID: 5, VersionID: 7, Title: "first", Status: versiondomain.RequirementClarified}},
			{{ID: 9, VersionID: 7, Title: "second", Status: versiondomain.RequirementClarified}},
		},
		more: []bool{true, false},
	}
	adapter := mindmapversion.NewRequirementReader(requirements, &fakeRelationLister{}, nil)

	mapped, err := adapter.ListConfirmed(ctx, 7)
	if err != nil {
		t.Fatalf("ListConfirmed: %v", err)
	}
	if len(mapped) != 2 || mapped[0].ID != 5 || mapped[1].ID != 9 {
		t.Fatalf("mapped=%+v want requirement ids 5 and 9", mapped)
	}
	if len(requirements.cursors) != 2 {
		t.Fatalf("cursors=%v want two pages", requirements.cursors)
	}
	if want := platform.EncodeCursor(5); requirements.cursors[1] != want {
		t.Fatalf("second page cursor=%q want %q", requirements.cursors[1], want)
	}
	if mapped[0].AttachmentSummary != "" {
		t.Fatalf("AttachmentSummary=%q want empty without an attachment reader", mapped[0].AttachmentSummary)
	}
}

// TestRequirementReaderMapsRelations pins the requirement relation projection.
func TestRequirementReaderMapsRelations(t *testing.T) {
	ctx := context.Background()
	relations := &fakeRelationLister{relations: []versionapp.Relation{{
		ID:                3,
		VersionID:         7,
		FromRequirementID: 1,
		ToRequirementID:   2,
		RelationType:      versiondomain.RelationDependsOn,
		Description:       "needs the API",
	}}}
	adapter := mindmapversion.NewRequirementReader(&fakeRequirementLister{}, relations, nil)

	mapped, err := adapter.ListRelations(ctx, 7)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(mapped) != 1 {
		t.Fatalf("mapped %d relations, want 1", len(mapped))
	}
	got := mapped[0]
	if got.ID != 3 || got.VersionID != 7 || got.FromRequirementID != 1 || got.ToRequirementID != 2 ||
		got.RelationType != versiondomain.RelationDependsOn || got.Description != "needs the API" {
		t.Fatalf("relation=%+v want the source fields", got)
	}
	if relations.versionID != 7 {
		t.Fatalf("relation reader received version %d want 7", relations.versionID)
	}
}

// TestRequirementReaderMapsVersionErrors pins the version -> mindmap not_found
// translation on the requirement read path.
func TestRequirementReaderMapsVersionErrors(t *testing.T) {
	ctx := context.Background()
	adapter := mindmapversion.NewRequirementReader(
		&fakeRequirementLister{err: versiondomain.NewNotFound("version", 7)},
		&fakeRelationLister{err: versiondomain.NewNotFound("version", 7)},
		nil,
	)
	_, err := adapter.ListConfirmed(ctx, 7)
	var mapped *mindmapdomain.Error
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindNotFound || mapped.Reason != "not_found" {
		t.Fatalf("ListConfirmed error=%v want mindmap not_found", err)
	}
	_, err = adapter.ListRelations(ctx, 7)
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindNotFound {
		t.Fatalf("ListRelations error=%v want mindmap not_found", err)
	}
}
