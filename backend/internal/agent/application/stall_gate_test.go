package agentapp

import "testing"

// TestStallGateDefaultThreshold pins the P6 thinking-only stall gate default of 2
// consecutive rounds (design §4.5 防失控).
func TestStallGateDefaultThreshold(t *testing.T) {
	if defaultStallRounds != 2 {
		t.Fatalf("defaultStallRounds = %d, want 2", defaultStallRounds)
	}
	if g := newStallGate(0); g.threshold != 2 {
		t.Fatalf("newStallGate(0).threshold = %d, want the default 2", g.threshold)
	}
	if g := newStallGate(3); g.threshold != 3 {
		t.Fatalf("newStallGate(3).threshold = %d, want 3", g.threshold)
	}
}

// TestStallGateTripsAfterTwoThinkingOnlyRounds pins that two consecutive rounds
// with no tool call and no substantive content trip the gate (settlement winding).
func TestStallGateTripsAfterTwoThinkingOnlyRounds(t *testing.T) {
	g := newStallGate(2)
	if g.record(false, false) {
		t.Fatal("first thinking-only round tripped the gate, want it to wait for the second")
	}
	if !g.record(false, false) {
		t.Fatal("second consecutive thinking-only round did not trip the gate")
	}
}

// TestStallGateResetsOnProgress pins that a tool call or substantive content
// resets the consecutive counter: the gate only catches uninterrupted spinning.
func TestStallGateResetsOnProgress(t *testing.T) {
	g := newStallGate(2)
	if g.record(false, false) {
		t.Fatal("first thinking-only round tripped early")
	}
	// A tool call breaks the streak.
	if g.record(true, false) {
		t.Fatal("a tool-call round tripped the gate")
	}
	if g.record(false, false) {
		t.Fatal("the streak reset, so this single thinking-only round must not trip")
	}
	if !g.record(false, false) {
		t.Fatal("two thinking-only rounds after the reset must trip")
	}

	// Substantive assistant content also breaks the streak.
	g2 := newStallGate(2)
	g2.record(false, false)
	if g2.record(false, true) {
		t.Fatal("a substantive-content round tripped the gate")
	}
	if g2.record(false, false) {
		t.Fatal("the streak reset on content, so one thinking-only round must not trip")
	}
}
