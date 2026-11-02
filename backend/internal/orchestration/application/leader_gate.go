package orchestrationapp

// LeaderGate is the leadership signal consumed by leader-gated schedulers
// (reconciler, execution scheduler/recovery, executor poller, GC scheduler).
// infrastructure/postgres.Leader implements it.
type LeaderGate interface {
	IsLeader() bool
}

// RunIfLeader executes fn only when gate currently reports leadership, and
// reports whether fn ran. Callers invoke it per periodic tick: long-running
// work stays gated round by round and a follower never starts a duplicate
// scheduler.
func RunIfLeader(gate LeaderGate, fn func()) bool {
	if gate == nil || !gate.IsLeader() {
		return false
	}
	fn()
	return true
}
