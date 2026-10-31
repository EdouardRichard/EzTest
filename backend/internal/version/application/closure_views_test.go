package versionapp_test

import (
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
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
