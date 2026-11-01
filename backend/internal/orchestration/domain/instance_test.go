package orchestration

import (
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	allowed := map[Status][]Status{
		StatusActive:   {StatusDraining, StatusOffline, StatusStopped},
		StatusDraining: {StatusActive, StatusOffline, StatusStopped},
		StatusOffline:  {StatusStopped},
		StatusStopped:  nil,
	}
	all := []Status{StatusActive, StatusDraining, StatusOffline, StatusStopped}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, candidate := range allowed[from] {
				if candidate == to {
					want = true
				}
			}
			if got := CanTransition(from, to); got != want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
	if CanTransition("bogus", StatusActive) {
		t.Fatal("unknown status must not transition")
	}
}

func TestStatusValid(t *testing.T) {
	for _, status := range []Status{StatusActive, StatusDraining, StatusOffline, StatusStopped} {
		if !status.Valid() {
			t.Fatalf("%q should be valid", status)
		}
	}
	for _, status := range []Status{"", "running", "Active"} {
		if status.Valid() {
			t.Fatalf("%q should be invalid", status)
		}
	}
}

func TestInstanceIsReclaimable(t *testing.T) {
	cases := map[Status]bool{
		StatusActive:   false,
		StatusDraining: false,
		StatusOffline:  true,
		StatusStopped:  true,
	}
	for status, want := range cases {
		if got := (Instance{Status: status}).Reclaimable(); got != want {
			t.Fatalf("Reclaimable(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestSentinelErrorsAreDistinct(t *testing.T) {
	if errors.Is(ErrNotFound, ErrNodeTaken) || errors.Is(ErrNodeTaken, ErrInstanceKeyTaken) || errors.Is(ErrNotFound, ErrInstanceKeyTaken) {
		t.Fatal("repository sentinels must be distinct")
	}
}
