package agentapp

// stallGate is the P6 thinking-only spinning guard (design §4.5 防失控). A model
// can burn turns emitting reasoning ("thinking") without ever calling a tool or
// producing substantive assistant content. The natural-completion rule (a reply
// without tool calls ends the run) would otherwise end such a run on the first
// empty reply with no explanation; instead the loop gives the model a chance to
// recover and counts consecutive no-progress rounds. After the threshold the
// gate trips and the loop forces the settlement winding turn, so the run ends
// with a recorded partial outcome rather than spinning to the defensive floor.
//
// The gate is a pure counter: any round with a tool call or substantive content
// resets the streak, so it only catches uninterrupted spinning. It is a safety
// mechanism, not a configurable budget restriction (P6 is default-open).
type stallGate struct {
	threshold   int
	consecutive int
}

// newStallGate builds a gate with the given consecutive-round threshold. A
// non-positive threshold falls back to defaultStallRounds (2).
func newStallGate(threshold int) *stallGate {
	if threshold < 1 {
		threshold = defaultStallRounds
	}
	return &stallGate{threshold: threshold}
}

// record updates the streak for one completed model round and reports whether
// the gate has tripped (the caller should force settlement winding). A round
// with a tool call or substantive assistant content resets the streak; a
// thinking-only round (no tool call, no substantive content) advances it.
func (g *stallGate) record(hasToolCalls, hasSubstantiveContent bool) bool {
	if hasToolCalls || hasSubstantiveContent {
		g.consecutive = 0
		return false
	}
	g.consecutive++
	return g.consecutive >= g.threshold
}
