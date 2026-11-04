package main

import (
	"context"
	"errors"
	"testing"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// fakeVisualAssertionRepo is the repository subset consumed by ConfigService
// List: it mimics the version/case-version filtered SQL of the postgres adapter.
type fakeVisualAssertionRepo struct {
	rows []visual.Assertion
}

func (f *fakeVisualAssertionRepo) Create(context.Context, visual.Assertion) (visual.Assertion, error) {
	return visual.Assertion{}, errors.New("not implemented")
}

func (f *fakeVisualAssertionRepo) UpdateWithRevision(context.Context, visual.Assertion, time.Time) (visual.Assertion, error) {
	return visual.Assertion{}, errors.New("not implemented")
}

func (f *fakeVisualAssertionRepo) Get(_ context.Context, id int64) (visual.Assertion, error) {
	for _, row := range f.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return visual.Assertion{}, visual.ErrNotFound
}

func (f *fakeVisualAssertionRepo) GetByCaseAssertion(context.Context, int64, int64) (visual.Assertion, bool, error) {
	return visual.Assertion{}, false, nil
}

func (f *fakeVisualAssertionRepo) List(_ context.Context, versionID int64, caseVersionID, versionSUTID *int64) ([]visual.Assertion, error) {
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

// TestDesignVisualAssertionsResolverFiltersActiveCaseConfigurations pins the
// composition-root adapter used by design confirmation (Task 115): only active
// configurations of the requested version and case version resolve, so an
// archived or foreign-case configuration can never satisfy the gate.
func TestDesignVisualAssertionsResolverFiltersActiveCaseConfigurations(t *testing.T) {
	repo := &fakeVisualAssertionRepo{rows: []visual.Assertion{
		{ID: 6001, VersionID: 10, CaseVersionID: 41, CaseAssertionID: 9001, Status: visual.AssertionActive},
		{ID: 6002, VersionID: 10, CaseVersionID: 41, CaseAssertionID: 9002, Status: visual.AssertionArchived},
		{ID: 6003, VersionID: 10, CaseVersionID: 42, CaseAssertionID: 9003, Status: visual.AssertionActive},
		{ID: 6004, VersionID: 11, CaseVersionID: 41, CaseAssertionID: 9004, Status: visual.AssertionActive},
	}}
	resolver := designVisualAssertions{configs: visualapp.NewConfigService(visualapp.ConfigServiceDeps{Repo: repo})}

	refs, err := resolver.ListActiveVisualAssertions(context.Background(), 10, 41)
	if err != nil {
		t.Fatalf("ListActiveVisualAssertions: %v", err)
	}
	if len(refs) != 1 || refs[0] != (designapp.VisualAssertionRef{ID: 6001, CaseAssertionID: 9001}) {
		t.Fatalf("resolved refs = %+v, want only the active case-41 configuration", refs)
	}

	if _, err := (designVisualAssertions{}).ListActiveVisualAssertions(context.Background(), 10, 41); err == nil {
		t.Fatal("a resolver without a configuration service must fail closed")
	}
}
