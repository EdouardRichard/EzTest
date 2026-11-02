package versionapp_test

import (
	"reflect"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestP4ReconcilerListRunningVersions(t *testing.T) {
	env := newTestEnv(t)
	for id, status := range map[int64]string{
		700: version.StatusCompleted,
		701: version.StatusRunning,
		702: version.StatusInitial,
		703: version.StatusRunning,
	} {
		env.repo.versions[id] = version.Version{ID: id, Title: "v", Status: status}
	}

	ids, err := env.svc.ListRunningVersions(requestContext(), 100)
	if err != nil {
		t.Fatalf("ListRunningVersions: %v", err)
	}
	// Newest first; completed and initial versions never reach the P4 ensure
	// rules (their reopen scan is owned by the P4 event hooks).
	if want := []int64{703, 701}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ListRunningVersions = %v, want %v", ids, want)
	}

	limited, err := env.svc.ListRunningVersions(requestContext(), 1)
	if err != nil {
		t.Fatalf("ListRunningVersions(limit=1): %v", err)
	}
	if want := []int64{703}; !reflect.DeepEqual(limited, want) {
		t.Fatalf("ListRunningVersions(limit=1) = %v, want %v", limited, want)
	}

	// A version list without running versions yields an empty result, so the
	// reconciler performs no P4 ensure calls at all.
	env.repo.versions = map[int64]version.Version{
		704: {ID: 704, Status: version.StatusCompleted},
	}
	none, err := env.svc.ListRunningVersions(requestContext(), 10)
	if err != nil {
		t.Fatalf("ListRunningVersions(no running): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListRunningVersions(no running) = %v, want empty", none)
	}
}
