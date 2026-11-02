package versionapp_test

import (
	"context"
	"errors"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// fakeCompletedVersionReopener records the best-effort reopen projection calls
// made by the requirement service.
type fakeCompletedVersionReopener struct {
	calls []struct {
		versionID int64
		trigger   string
		reason    string
	}
	err error
}

func (f *fakeCompletedVersionReopener) ReopenIfCompleted(_ context.Context, versionID int64, trigger, reason string) error {
	f.calls = append(f.calls, struct {
		versionID int64
		trigger   string
		reason    string
	}{versionID: versionID, trigger: trigger, reason: reason})
	return f.err
}

func TestRequirementCreateReopensCompletedVersion(t *testing.T) {
	env := newRequirementEnv(t)
	reopener := &fakeCompletedVersionReopener{}
	env.svc.SetVersionReopener(reopener)

	createRequirement(t, env, "Reopening requirement")

	if len(reopener.calls) != 1 {
		t.Fatalf("reopen calls = %+v, want one", reopener.calls)
	}
	call := reopener.calls[0]
	if call.versionID != env.versionID || call.trigger != versionapp.VersionReopenTriggerRequirementCreated {
		t.Fatalf("reopen call = %+v, want version %d trigger requirement_created", call, env.versionID)
	}
}

func TestRequirementUpdateReopensCompletedVersion(t *testing.T) {
	env := newRequirementEnv(t)
	reopener := &fakeCompletedVersionReopener{}
	env.svc.SetVersionReopener(reopener)

	detail := createRequirement(t, env, "Edited requirement")
	reopener.calls = nil
	draft := validRequirementDraft(env)
	draft.Title = "Edited requirement v2"
	if _, err := env.svc.Update(requestContext(), env.versionID, detail.ID, draft, "rewording", 42); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(reopener.calls) != 1 {
		t.Fatalf("reopen calls = %+v, want one", reopener.calls)
	}
	if reopener.calls[0].trigger != versionapp.VersionReopenTriggerRequirementReopened {
		t.Fatalf("trigger = %q, want requirement_reopened", reopener.calls[0].trigger)
	}
}

func TestRequirementReopenProjectionIsBestEffort(t *testing.T) {
	env := newRequirementEnv(t)
	env.svc.SetVersionReopener(&fakeCompletedVersionReopener{err: errors.New("reopen projection unavailable")})

	// The reopen hook runs after the requirement write; its failure must never
	// fail creation (the reconciler is the fallback).
	if detail := createRequirement(t, env, "Best effort"); detail.ID <= 0 {
		t.Fatalf("created requirement = %+v", detail)
	}
}

func TestRequirementWithoutReopenerDoesNotPanic(t *testing.T) {
	env := newRequirementEnv(t)
	if detail := createRequirement(t, env, "No hook"); detail.ID <= 0 {
		t.Fatalf("created requirement = %+v", detail)
	}
}
