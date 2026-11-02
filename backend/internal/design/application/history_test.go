package designapp

import (
	"context"
	"errors"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeHistoryRepo implements the historical case-version read of the design
// repository port. It returns rows only for the configured version, mimicking
// the version-scoped SQL of the real adapter.
type fakeHistoryRepo struct {
	design.Repository

	versionID int64
	rows      map[int64]design.CaseVersionSummary
	err       error

	calls        int
	gotVersionID int64
	gotIDs       []int64
}

func (f *fakeHistoryRepo) ReadCaseVersionsByIDs(_ context.Context, versionID int64, ids []int64) ([]design.CaseVersionSummary, error) {
	f.calls++
	f.gotVersionID = versionID
	f.gotIDs = append([]int64(nil), ids...)
	if f.err != nil {
		return nil, f.err
	}
	if versionID != f.versionID {
		return []design.CaseVersionSummary{}, nil
	}
	out := make([]design.CaseVersionSummary, 0, len(ids))
	for _, id := range ids {
		if row, ok := f.rows[id]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func newHistoryService(repo design.Repository) *GraphService {
	return NewGraphService(repo, &fakeIDs{}, newFixedClock(), nil, nil, nil)
}

func TestReadCaseVersionsByIDsReadsHistoricalRows(t *testing.T) {
	repo := &fakeHistoryRepo{
		versionID: 1001,
		rows: map[int64]design.CaseVersionSummary{
			5103: {ID: 5103, Revision: 2, Title: "Superseded checkout", Status: "superseded"},
			5101: {ID: 5101, Revision: 1, Title: "Checkout", Status: "confirmed"},
		},
	}
	service := newHistoryService(repo)

	views, err := service.ReadCaseVersionsByIDs(context.Background(), 1001, []int64{5103, 5101, 999999, -4, 0, 5103})
	if err != nil {
		t.Fatalf("ReadCaseVersionsByIDs: %v", err)
	}
	if repo.calls != 1 {
		t.Fatalf("repository calls = %d, want 1", repo.calls)
	}
	if repo.gotVersionID != 1001 {
		t.Errorf("repository version id = %d, want 1001", repo.gotVersionID)
	}
	if len(repo.gotIDs) != 3 || repo.gotIDs[0] != 5103 || repo.gotIDs[1] != 5101 || repo.gotIDs[2] != 999999 {
		t.Errorf("repository ids = %v, want [5103 5101 999999] (deduped, positive only)", repo.gotIDs)
	}
	if len(views) != 2 {
		t.Fatalf("views = %+v, want 2 rows (missing ids skipped)", views)
	}
	if views[0].ID != 5103 || views[0].Revision != 2 || views[0].Title != "Superseded checkout" || views[0].Status != "superseded" {
		t.Errorf("superseded view = %+v", views[0])
	}
	if views[1].ID != 5101 || views[1].Status != "confirmed" || views[1].Title == "" {
		t.Errorf("confirmed view = %+v", views[1])
	}
}

func TestReadCaseVersionsByIDsFiltersForeignVersion(t *testing.T) {
	repo := &fakeHistoryRepo{
		versionID: 1001,
		rows: map[int64]design.CaseVersionSummary{
			5103: {ID: 5103, Revision: 2, Title: "Superseded checkout", Status: "superseded"},
		},
	}
	service := newHistoryService(repo)

	// The row lives in version 1001; a request scoped to version 2002 must not
	// see it, even though the caller knows the case-version id.
	views, err := service.ReadCaseVersionsByIDs(context.Background(), 2002, []int64{5103})
	if err != nil {
		t.Fatalf("ReadCaseVersionsByIDs: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("views = %+v, want none for a foreign version", views)
	}
	if repo.gotVersionID != 2002 {
		t.Errorf("repository version id = %d, want 2002", repo.gotVersionID)
	}
}

func TestReadCaseVersionsByIDsEmptyInputSkipsRepository(t *testing.T) {
	repo := &fakeHistoryRepo{versionID: 1001}
	service := newHistoryService(repo)

	views, err := service.ReadCaseVersionsByIDs(context.Background(), 1001, nil)
	if err != nil {
		t.Fatalf("ReadCaseVersionsByIDs: %v", err)
	}
	if views == nil || len(views) != 0 {
		t.Fatalf("views = %#v, want an empty non-nil slice", views)
	}
	if repo.calls != 0 {
		t.Errorf("repository calls = %d, want 0 for an empty id list", repo.calls)
	}
}

func TestReadCaseVersionsByIDsRejectsInvalidVersion(t *testing.T) {
	repo := &fakeHistoryRepo{versionID: 1001}
	service := newHistoryService(repo)

	_, err := service.ReadCaseVersionsByIDs(context.Background(), 0, []int64{5103})
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
		t.Fatalf("error = %v, want invalid_argument", err)
	}
	if repo.calls != 0 {
		t.Errorf("repository calls = %d, want 0 before validation", repo.calls)
	}
}

func TestReadCaseVersionsByIDsPropagatesRepositoryError(t *testing.T) {
	repo := &fakeHistoryRepo{versionID: 1001, err: design.ErrNotFound}
	service := newHistoryService(repo)

	_, err := service.ReadCaseVersionsByIDs(context.Background(), 1001, []int64{5103})
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("error = %v, want not_found", err)
	}
}
