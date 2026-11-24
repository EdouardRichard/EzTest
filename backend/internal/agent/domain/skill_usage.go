package agent

import "time"

// SkillUsage is one append-only skill_usage fact row: one skill.read attempt
// in one agent run (v0.2.4-C T19, SC-C12). Found records whether the runtime
// skill library served the body (true) or the package name was unknown
// (false) — misses are auditable facts, not silent no-ops. Seq is the per-run
// read ordinal; (run_id, seq) is the idempotency key of the ledger.
type SkillUsage struct {
	ID        int64
	RunID     int64
	Seq       int
	SkillName string
	Found     bool
	CreatedAt time.Time
}
