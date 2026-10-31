package versionapp_test

import (
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestClosureServiceLatestClosureProjectsSummary(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClosed)
	env.closures.closures[requirement.ID] = []version.RequirementClosure{{
		ID: 3001, RequirementID: requirement.ID, RequirementVersionID: 1002,
		CloseRound: 2, ClosedAt: testTime, ReopenCount: 3,
	}}

	got, found, err := env.svc.LatestClosure(requestContext(), requirement.ID)
	if err != nil {
		t.Fatalf("LatestClosure: %v", err)
	}
	if !found {
		t.Fatal("LatestClosure found = false")
	}
	if got.ID != 3001 || got.CloseRound != 2 || got.ReopenCount != 3 || !got.ClosedAt.Equal(testTime) {
		t.Fatalf("closure view = %#v", got)
	}
}

func TestClosureServiceLatestClosureMissingAndInvalid(t *testing.T) {
	env := newClosureEnv(t)

	if _, found, err := env.svc.LatestClosure(requestContext(), 9999); err != nil || found {
		t.Fatalf("missing closure = %v/%v", found, err)
	}

	_, _, err := env.svc.LatestClosure(requestContext(), 0)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("invalid id error = %v", err)
	}

	env.closures.latestErr = errors.New("closure store down")
	if _, _, err := env.svc.LatestClosure(requestContext(), 1001); err == nil {
		t.Fatal("LatestClosure dropped the storage error")
	}
}

func TestClosureServiceSummariesByRequirementIDs(t *testing.T) {
	env := newClosureEnv(t)
	closed := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClosed)
	reopened := seedClosureRequirement(env, 1003, 701, 1004, version.RequirementDraft)
	reclosed := seedClosureRequirement(env, 1005, 701, 1006, version.RequirementClosed)
	neverClosed := seedClosureRequirement(env, 1007, 701, 1008, version.RequirementClarified)
	foreign := seedClosureRequirement(env, 1009, 702, 1010, version.RequirementClosed)
	env.closures.closures[closed.ID] = []version.RequirementClosure{{
		ID: 3001, RequirementID: closed.ID, RequirementVersionID: 1002,
		CloseRound: 2, ClosedAt: testTime,
	}}
	env.closures.closures[reopened.ID] = []version.RequirementClosure{{
		ID: 3002, RequirementID: reopened.ID, RequirementVersionID: 1004,
		CloseRound: 1, ClosedAt: testTime, ReopenCount: 1,
	}}
	env.closures.closures[reclosed.ID] = []version.RequirementClosure{{
		ID: 3003, RequirementID: reclosed.ID, RequirementVersionID: 1006,
		CloseRound: 1, ClosedAt: testTime, ReopenCount: 1,
	}}
	env.closures.closures[foreign.ID] = []version.RequirementClosure{{
		ID: 3004, RequirementID: foreign.ID, RequirementVersionID: 1010,
		CloseRound: 1, ClosedAt: testTime,
	}}

	got, err := env.svc.SummariesByRequirementIDs(requestContext(), 701, []int64{
		closed.ID, reopened.ID, reclosed.ID, neverClosed.ID, foreign.ID, 0, closed.ID, 9999,
	})
	if err != nil {
		t.Fatalf("SummariesByRequirementIDs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("summaries = %#v, want 3 (closed/reopened/reclosed)", got)
	}
	if summary := got[closed.ID]; summary.ID != 3001 || summary.CloseRound != 2 ||
		summary.Status != versionapp.ClosureSummaryStatusClosed || summary.ReopenCount != 0 ||
		!summary.ClosedAt.Equal(testTime) {
		t.Errorf("closed summary = %#v", summary)
	}
	if summary := got[reopened.ID]; summary.ID != 3002 || summary.CloseRound != 1 ||
		summary.Status != versionapp.ClosureSummaryStatusReopened || summary.ReopenCount != 1 {
		t.Errorf("reopened summary = %#v", summary)
	}
	if summary := got[reclosed.ID]; summary.Status != versionapp.ClosureSummaryStatusReopened {
		t.Errorf("re-closed summary = %#v, want reopened while the latest round has reopen_count > 0", summary)
	}
	if _, ok := got[neverClosed.ID]; ok {
		t.Errorf("summary for a never-closed requirement = %#v, want absent", got[neverClosed.ID])
	}
	if _, ok := got[foreign.ID]; ok {
		t.Errorf("summary leaked a foreign-version requirement: %#v", got[foreign.ID])
	}
}

func TestClosureServiceSummariesByRequirementIDsValidatesAndPropagates(t *testing.T) {
	env := newClosureEnv(t)
	seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClosed)

	_, err := env.svc.SummariesByRequirementIDs(requestContext(), 0, []int64{1001})
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("invalid version error = %v", err)
	}

	env.closures.latestErr = errors.New("closure store down")
	if _, err := env.svc.SummariesByRequirementIDs(requestContext(), 701, []int64{1001}); err == nil {
		t.Fatal("SummariesByRequirementIDs dropped the storage error")
	}
}
