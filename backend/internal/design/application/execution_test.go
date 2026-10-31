package designapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func executableCaseFixture() design.ExecutableCase {
	versionSUT := int64(9001)
	pageID := int64(6001)
	return design.ExecutableCase{
		CaseID:               7001,
		RequirementID:        3001,
		RequirementVersionID: 3002,
		Version: design.CaseVersion{
			ID:                  7002,
			CaseID:              7001,
			Revision:            1,
			Title:               "跨系统退款",
			Description:         "desc",
			Preconditions:       "pre",
			Priority:            design.PriorityP1,
			DefaultVersionSUTID: &versionSUT,
			VersionSUTIDs:       []int64{9001, 9002},
			Status:              design.CaseStatusConfirmed,
			Steps: []design.CaseStep{
				{ID: 1, Seq: 1, Action: "open", Expected: "visible", TargetVersionSUTID: &versionSUT, TargetPageID: &pageID, AccountRole: "admin", Data: json.RawMessage(`{}`)},
			},
			Assertions: []design.CaseAssertion{
				{ID: 10, StepSeq: 1, AssertionSeq: 1, Kind: design.AssertionUI, Expected: "ok", TargetVersionSUTID: &versionSUT},
				{ID: 11, StepSeq: 1, AssertionSeq: 2, Kind: design.AssertionDB, Expected: "paid", DatabaseID: &pageID},
			},
		},
	}
}

func TestGetCaseVersionMapsExecutableCase(t *testing.T) {
	repo := newFakeRepo()
	repo.executableCaseErr = nil
	repo.executableCases = map[int64]design.ExecutableCase{
		7002: executableCaseFixture(),
	}
	service := NewGraphService(repo, &fakeIDs{}, newFixedClock(), nil, nil, &fakeTeamRoom{})

	executable, err := service.GetCaseVersion(context.Background(), 1001, 7002)
	if err != nil {
		t.Fatalf("GetCaseVersion: %v", err)
	}
	if executable.CaseID != 7001 || executable.CaseVersionID != 7002 || executable.RequirementID != 3001 ||
		executable.RequirementVersionID != 3002 || executable.DefaultVersionSUTID != 9001 {
		t.Fatalf("executable case = %+v", executable)
	}
	if len(executable.Steps) != 1 || executable.Steps[0].Action != "open" || executable.Steps[0].TargetVersionSUTID != 9001 {
		t.Fatalf("steps = %+v", executable.Steps)
	}
	if len(executable.Assertions) != 2 || executable.Assertions[1].Kind != design.AssertionDB ||
		executable.Assertions[1].AssertionSeq != 2 {
		t.Fatalf("assertions = %+v", executable.Assertions)
	}
}

func TestGetCaseVersionRejectsInvalidIDs(t *testing.T) {
	service := NewGraphService(newFakeRepo(), &fakeIDs{}, newFixedClock(), nil, nil, &fakeTeamRoom{})
	if _, err := service.GetCaseVersion(context.Background(), 0, 7002); err == nil {
		t.Fatal("missing version id must be rejected")
	}
	if _, err := service.GetCaseVersion(context.Background(), 1001, 0); err == nil {
		t.Fatal("missing case version id must be rejected")
	}
}

func TestGetCaseVersionMapsNotFound(t *testing.T) {
	repo := newFakeRepo()
	repo.executableCaseErr = design.ErrNotFound
	service := NewGraphService(repo, &fakeIDs{}, newFixedClock(), nil, nil, &fakeTeamRoom{})
	_, err := service.GetCaseVersion(context.Background(), 1001, 7002)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestListExecutableOnlyReturnsCurrentConfirmed(t *testing.T) {
	repo := newFakeRepo()
	repo.executableList = []design.ExecutableCase{executableCaseFixture()}
	service := NewGraphService(repo, &fakeIDs{}, newFixedClock(), nil, nil, &fakeTeamRoom{})

	cases, err := service.ListExecutable(context.Background(), 1001)
	if err != nil {
		t.Fatalf("ListExecutable: %v", err)
	}
	if len(cases) != 1 || cases[0].CaseVersionID != 7002 || cases[0].Title != "跨系统退款" {
		t.Fatalf("cases = %+v", cases)
	}
}

func TestListExecutableRejectsInvalidVersion(t *testing.T) {
	service := NewGraphService(newFakeRepo(), &fakeIDs{}, newFixedClock(), nil, nil, &fakeTeamRoom{})
	if _, err := service.ListExecutable(context.Background(), 0); err == nil {
		t.Fatal("missing version id must be rejected")
	}
}
