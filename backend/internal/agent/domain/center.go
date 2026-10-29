package domain

import (
	"context"
	"time"
)

// ProfileRunStats is one profile's aggregated run history for the P8 Agent
// centre catalogue (spec §8.1): total runs, succeeded runs and failed runs. The
// repository computes it in one query so the read endpoint never walks a
// session page to fabricate a statistic.
type ProfileRunStats struct {
	ProfileID      int64
	ProfileKey     string
	RunCount       int64
	SucceededCount int64
	FailedCount    int64
}

// CenterSessionFilter is the read-only run-observation filter (spec §8.1):
// role is the logical role/profile key, status is the derived session status
// (idle/running/done/failed/aborted/interrupted) and limit bounds the page.
// Empty RoleKey/Status mean "all".
type CenterSessionFilter struct {
	RoleKey string
	Status  string
	Limit   int
}

// CenterSessionRow is one row of the center's run observation. Status is derived
// from the session's active or newest terminal run; a session without runs is
// "idle". Title is the stored session title (possibly empty).
type CenterSessionRow struct {
	ID             int64
	ProfileKey     string
	Title          string
	Status         string
	Running        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActivityAt time.Time
}

// CenterReadRepository is the additive P8 read-model port over the Agent
// persistence. It is separate from SessionRepository so P1–P7 repositories and
// fakes stay valid; the postgres SessionRepository/RunRepository pair does not
// have to grow aggregation methods. Implementations must never expose messages,
// prompts or credentials.
type CenterReadRepository interface {
	// ProfileRunStats returns one row per stored profile (zero counts included).
	ProfileRunStats(ctx context.Context) ([]ProfileRunStats, error)
	// CenterSessions returns the newest sessions matching filter, ordered by
	// last activity descending.
	CenterSessions(ctx context.Context, filter CenterSessionFilter) ([]CenterSessionRow, error)
}
