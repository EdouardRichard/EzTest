package orchestrationapp

import "testing"

type fakeLeaderGate struct{ leader bool }

func (f *fakeLeaderGate) IsLeader() bool { return f.leader }

func TestRunIfLeader(t *testing.T) {
	calls := 0
	action := func() { calls++ }

	if RunIfLeader(nil, action) {
		t.Fatal("nil gate reported leadership")
	}
	gate := &fakeLeaderGate{}
	if RunIfLeader(gate, action) {
		t.Fatal("follower ran leader-only work")
	}
	if calls != 0 {
		t.Fatalf("follower executed the action %d times", calls)
	}

	gate.leader = true
	if !RunIfLeader(gate, action) {
		t.Fatal("leader did not run leader-only work")
	}
	if calls != 1 {
		t.Fatalf("action calls = %d, want 1", calls)
	}

	// Leadership can be lost between ticks; the next gate check must skip.
	gate.leader = false
	if RunIfLeader(gate, action) || calls != 1 {
		t.Fatalf("lost leadership still ran the action: calls=%d", calls)
	}
}
