package eventingpg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StageTransitionReader is the read-only stage-transition projection the agent
// reconciler's R49 rule uses to decide whether the current version stage visit
// owns a stage-entry job. It reads the reason persisted on the version's
// version.stage_changed event, so it never writes the stage or the event log.
//
// The query also matches the event's stage_entered_at against the stage visit
// the caller observed. A stale record (an earlier visit to the same stage, or a
// transition whose event was not persisted yet) therefore never authorizes a
// repair, which keeps a resume/reject re-entry from being mistaken for the
// gate/wave transition that originally created the job.
type StageTransitionReader struct {
	pool *pgxpool.Pool
}

// NewStageTransitionReader wraps a pgx pool. A nil pool is reported on the read
// instead of panicking, so a degraded assembly fails the rule, not the process.
func NewStageTransitionReader(pool *pgxpool.Pool) *StageTransitionReader {
	return &StageTransitionReader{pool: pool}
}

// LastStageReason returns the reason recorded on the version.stage_changed event
// whose stage_entered_at identifies the stage visit that started at enteredAt.
// It returns "" with a nil error when there is no such record: an
// unknown/legacy transition, a record already removed by event retention, or a
// transition event that has not been persisted yet. R49 treats "" as "do not
// repair", so a missing record can never authorize a dispatch.
func (r *StageTransitionReader) LastStageReason(ctx context.Context, versionID int64, enteredAt time.Time) (string, error) {
	if r == nil || r.pool == nil {
		return "", fmt.Errorf("stage transition reader is not configured")
	}
	if versionID <= 0 {
		return "", platform.Invalid("version_id", "must be a positive decimal ID")
	}
	var reason string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(envelope->'payload'->>'reason', '')
		FROM event
		WHERE channel = $1
		  AND type = 'version.stage_changed'
		  AND envelope->'payload'->>'stage_entered_at' = $2
		ORDER BY seq DESC
		LIMIT 1
	`, versionChannel(versionID), enteredAt.UTC().Format(time.RFC3339)).Scan(&reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read last stage reason for version %d: %w", versionID, err)
	}
	return strings.TrimSpace(reason), nil
}

// versionChannel is the version-scoped event channel. The stage machine
// publishes version.stage_changed to both "version:{id}" and "system"; the
// version channel is the precise one and does not mix versions.
func versionChannel(versionID int64) string {
	return "version:" + strconv.FormatInt(versionID, 10)
}
