// Package agentapp implements the agent-runtime use cases: the lease-based
// work queue, the version-scoped team room with deterministic digests, and the
// layered context builder. Runtime workers/reconcilers are wired by cmd/eztest
// and follow the P1 single-instance assumption.
package agentapp
