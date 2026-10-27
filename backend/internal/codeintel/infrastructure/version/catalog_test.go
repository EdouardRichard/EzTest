package version_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	codedomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelversion "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/version"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeVersionReader is an in-memory P3 version reader.
type fakeVersionReader struct {
	version *versionapp.Version
	err     error
	calls   []int64
}

// Get returns the configured version or failure.
func (f *fakeVersionReader) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return nil, f.err
	}
	return f.version, nil
}

// fakeRequirementLister pages an in-memory requirement list using the same
// platform cursor the P3 repository decodes.
type fakeRequirementLister struct {
	requirements []versionapp.Requirement
	err          error
	pages        []versiondomain.Page
}

// List returns one cursor page ordered by ID ascending.
func (f *fakeRequirementLister) List(_ context.Context, versionID int64, status string, page versiondomain.Page) ([]versionapp.Requirement, bool, error) {
	f.pages = append(f.pages, page)
	if f.err != nil {
		return nil, false, f.err
	}
	start := 0
	if page.Cursor != "" {
		cursorID, err := platform.DecodeCursor(page.Cursor)
		if err != nil {
			return nil, false, err
		}
		for start < len(f.requirements) && f.requirements[start].ID != cursorID {
			start++
		}
		if start < len(f.requirements) {
			start++
		}
	}
	limit := page.Limit
	if limit <= 0 {
		limit = 20
	}
	end := start + limit
	if end > len(f.requirements) {
		end = len(f.requirements)
	}
	more := end < len(f.requirements)
	return f.requirements[start:end], more, nil
}

// fakeRelationLister is an in-memory P3 relation lister.
type fakeRelationLister struct {
	relations []versionapp.Relation
	err       error
	calls     []int64
}

// List returns the configured relations.
func (f *fakeRelationLister) List(_ context.Context, versionID int64) ([]versionapp.Relation, error) {
	f.calls = append(f.calls, versionID)
	if f.err != nil {
		return nil, f.err
	}
	return f.relations, nil
}

// TestVersionCatalogMapsBindingAndRelations pins the whole VersionCatalog
// projection: bindings, requirements and the two requirement ids of every
// relation.
func TestVersionCatalogMapsBindingAndRelations(t *testing.T) {
	systemID := int64(7)
	repoID := int64(42)
	branch := "release/1.2"
	baseline := strings.Repeat("a", 40)
	version := &versionapp.Version{
		ID:                 int64(5),
		SystemID:           &systemID,
		CodeRepositoryID:   &repoID,
		CodeBranch:         &branch,
		CodeBaselineCommit: &baseline,
	}
	requirements := []versionapp.Requirement{
		{ID: 101, Title: "Refund", Description: "Refund an order", Status: "clarified"},
		{ID: 102, Title: "Orders", Description: "List orders", Status: "draft"},
	}
	relations := []versionapp.Relation{
		{ID: 1, FromRequirementID: 101, ToRequirementID: 102, RelationType: "depends_on"},
		{ID: 2, FromRequirementID: 102, ToRequirementID: 101, RelationType: "overlaps"},
	}
	catalog := codeintelversion.NewCatalog(
		&fakeVersionReader{version: version},
		&fakeRequirementLister{requirements: requirements},
		&fakeRelationLister{relations: relations},
	)

	info, err := catalog.Get(context.Background(), 5)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.ID != 5 || !info.HasSystem || info.SystemID != 7 {
		t.Fatalf("system binding=%+v", info)
	}
	if !info.HasRepo || info.RepoID != 42 {
		t.Fatalf("repo binding=%+v", info)
	}
	if info.Branch != "release/1.2" || info.BaselineCommit != baseline {
		t.Fatalf("branch/baseline=%q/%q", info.Branch, info.BaselineCommit)
	}

	gotRequirements, err := catalog.ListRequirements(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListRequirements: %v", err)
	}
	if len(gotRequirements) != 2 {
		t.Fatalf("requirements=%+v want 2", gotRequirements)
	}
	if gotRequirements[0].ID != 101 || gotRequirements[0].Title != "Refund" ||
		gotRequirements[0].Description != "Refund an order" || gotRequirements[0].Status != "clarified" {
		t.Fatalf("requirement[0]=%+v", gotRequirements[0])
	}

	gotRelations, err := catalog.ListRelations(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(gotRelations) != 2 {
		t.Fatalf("relations=%+v want 2", gotRelations)
	}
	if gotRelations[0].FromID != 101 || gotRelations[0].ToID != 102 || gotRelations[0].Type != "depends_on" {
		t.Fatalf("relation[0]=%+v", gotRelations[0])
	}
	if gotRelations[1].FromID != 102 || gotRelations[1].ToID != 101 || gotRelations[1].Type != "overlaps" {
		t.Fatalf("relation[1]=%+v", gotRelations[1])
	}
}

// TestVersionCatalogUnboundVersion pins the zero-binding projection: a version
// without a system or repository reports the Has flags false and empty fields.
func TestVersionCatalogUnboundVersion(t *testing.T) {
	catalog := codeintelversion.NewCatalog(
		&fakeVersionReader{version: &versionapp.Version{ID: 9}},
		&fakeRequirementLister{},
		&fakeRelationLister{},
	)

	info, err := catalog.Get(context.Background(), 9)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.ID != 9 || info.HasSystem || info.HasRepo {
		t.Fatalf("info=%+v want no bindings", info)
	}
	if info.SystemID != 0 || info.RepoID != 0 || info.Branch != "" || info.BaselineCommit != "" {
		t.Fatalf("info=%+v want zero binding fields", info)
	}
}

// TestVersionCatalogPagesRequirements proves the adapter follows the P3
// requirement cursor instead of returning only the first page.
func TestVersionCatalogPagesRequirements(t *testing.T) {
	requirements := make([]versionapp.Requirement, 0, 150)
	for i := 1; i <= 150; i++ {
		requirements = append(requirements, versionapp.Requirement{ID: int64(i), Title: "R", Status: "draft"})
	}
	lister := &fakeRequirementLister{requirements: requirements}
	catalog := codeintelversion.NewCatalog(
		&fakeVersionReader{version: &versionapp.Version{ID: 5}},
		lister,
		&fakeRelationLister{},
	)

	got, err := catalog.ListRequirements(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListRequirements: %v", err)
	}
	if len(got) != 150 {
		t.Fatalf("requirements=%d want 150", len(got))
	}
	if len(lister.pages) != 2 {
		t.Fatalf("pages=%d want 2", len(lister.pages))
	}
	lastID, err := platform.DecodeCursor(lister.pages[1].Cursor)
	if err != nil || lastID != 100 {
		t.Fatalf("second cursor=%q (lastID=%d, err=%v) want 100", lister.pages[1].Cursor, lastID, err)
	}
}

// TestVersionCatalogMapsMissingVersion pins the codeintel-facing error: a
// missing P3 version is a codeintel not-found so the HTTP layer can return 404.
func TestVersionCatalogMapsMissingVersion(t *testing.T) {
	catalog := codeintelversion.NewCatalog(
		&fakeVersionReader{err: versiondomain.NewNotFound("version", 5)},
		&fakeRequirementLister{},
		&fakeRelationLister{},
	)

	_, err := catalog.Get(context.Background(), 5)
	if err == nil {
		t.Fatal("Get(missing)=nil want a domain error")
	}
	var domainErr *codedomain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %v (%T) is not a *codedomain.Error", err, err)
	}
	if !errors.Is(err, codedomain.ErrNotFound) || domainErr.Reason != "not_found" {
		t.Fatalf("kind/reason=%v/%q want ErrNotFound/not_found", domainErr.Kind, domainErr.Reason)
	}
}
