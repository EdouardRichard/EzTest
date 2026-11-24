package agent

import (
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ActivityType is the coarse kind of one agent_activity fact row (v0.2.4-D T10,
// SC-D5 / design §2.3 Q04=A-lite). The ledger records the two boundaries that
// decide what a run actually did to its context and to the system under test:
// a tool call and a context compaction. It is deliberately NOT the transcript
// (agent_message, read by the admin Agent Center transcript) and NOT the
// per-turn "thinking" heartbeat, which is an immediate projection that decides
// nothing (constitution 五.11).
type ActivityType string

// The activity fact kinds. This slice-backed pair is the ONE authority of the
// vocabulary: Valid and every writer derive from it, so a new kind cannot be
// spelled by hand in a second place (constitution 五.1/五.3/五.4).
const (
	// ActivityTypeToolCall is one tool invocation of a run, from its start to
	// its outcome.
	ActivityTypeToolCall ActivityType = "tool_call"
	// ActivityTypeCompaction is one context compaction boundary of a run
	// (pressure / overflow / stage_boundary, carried in the payload).
	ActivityTypeCompaction ActivityType = "compaction"
)

// ActivityTypes returns the persisted activity type vocabulary in stable order.
func ActivityTypes() []string {
	return []string{string(ActivityTypeToolCall), string(ActivityTypeCompaction)}
}

// Valid reports whether the type belongs to the authority above.
func (t ActivityType) Valid() bool {
	for _, value := range ActivityTypes() {
		if string(t) == value {
			return true
		}
	}
	return false
}

// Activity is one agent_activity fact row: a coarse, idempotent record of a tool
// call or a compaction boundary, keyed (run_id, seq).
//
// Seq is the per-run monotonic ordinal assigned by the runtime; (run_id, seq) is
// the idempotency key, so replaying a boundary folds into the same row instead of
// duplicating it. A tool call writes its row twice — once at the start, once with
// its outcome — under the SAME seq.
//
// The row is a FACT, not current state (constitution 五.7/五.9): it is written
// before any event derived from it, and losing the event never loses the row.
type Activity struct {
	ID        int64
	RunID     int64
	Seq       int
	Type      ActivityType
	Title     string
	StartedAt time.Time
	// EndedAt is nil while the activity is still in flight.
	EndedAt *time.Time
	// Payload carries the coarse facts (tool name, compaction trigger and
	// before/after token counts, duration). It is always a JSON object.
	Payload map[string]any
	// Error is the failure reason of a rejected call or a failed boundary;
	// empty means the activity did not fail.
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks the idempotency key, the fact kind and the time ordering.
func (a Activity) Validate() error {
	if a.RunID <= 0 {
		return platform.Invalid("run_id", "must be a positive decimal ID")
	}
	if a.Seq < 1 {
		return platform.Invalid("seq", "must be a positive per-run ordinal")
	}
	if !a.Type.Valid() {
		// Derived from the authority, never a second hand-written vocabulary:
		// a third kind must not silently drift out of this message
		// (constitution 五.3/五.4).
		return platform.Invalid("type", "must be one of "+strings.Join(ActivityTypes(), ", "))
	}
	if a.EndedAt != nil && !a.StartedAt.IsZero() && a.EndedAt.Before(a.StartedAt) {
		return platform.Invalid("ended_at", "must not be before started_at")
	}
	return nil
}
