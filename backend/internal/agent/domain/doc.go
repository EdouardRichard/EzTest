// Package agent is the agent-runtime bounded context domain. It contains the
// execution task board (work_item/attempt), the version-scoped team room and
// the agent runtime records (profile/run/message/context packet).
//
// The package is dependency-free apart from the shared platform kernel; it must
// never import pgx, HTTP or the LLM SDK (Agent.md §6.2).
//
// P1 runs a single API instance: leases, epoch fencing and the reconciler are
// implemented but multi-instance coordination/leader election belongs to P7.
package agent
