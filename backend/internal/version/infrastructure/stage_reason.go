package versionpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StageReasonReader is the fact-backed R49 stage-transition reader (V-0020). It
// answers the agent reconciler's "why was the current stage visit entered?"
// question from the durable stage_transition fact the stage machine appends in
// the same transaction as the stage row, NOT from the version.stage_changed
// event. A fact is never removed by event retention, so the reason survives
// retention.event_days deleting the event log — the defect that silently
// disabled the R49 stage-entry-job repair.
//
// It is read-only: it never writes the stage, the fact or the event log. The key
// match pins the fact to the stage visit the caller observed (same
// stage_entered_at), so an earlier visit to the same stage, or a resume/reject
// re-entry, never authorizes a repair.
type StageReasonReader struct {
	pool *pgxpool.Pool
}

// NewStageReasonReader wraps a pgx pool. A nil pool is reported on the read
// instead of panicking, so a degraded assembly fails the rule, not the process.
func NewStageReasonReader(pool *pgxpool.Pool) *StageReasonReader {
	return &StageReasonReader{pool: pool}
}

// LastStageReason returns the reason recorded on the stage_transition fact whose
// key (the visit's stage_entered_at, RFC3339 UTC) identifies the stage visit that
// started at enteredAt. It returns "" with a nil error when there is no such
// current fact: an unknown/legacy transition, a visit whose fact was not written
// yet, or a different visit. R49 treats "" as "do not repair", so a missing
// record can never authorize a dispatch. The newest matching fact wins (id
// descending), mirroring the event reader's seq-descending tie-break.
func (r *StageReasonReader) LastStageReason(ctx context.Context, versionID int64, enteredAt time.Time) (string, error) {
	if r == nil || r.pool == nil {
		return "", fmt.Errorf("stage reason reader is not configured")
	}
	if versionID <= 0 {
		return "", platform.Invalid("version_id", "must be a positive decimal ID")
	}
	var reason string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(payload->>'reason', '')
		FROM version_fact
		WHERE version_id = $1 AND kind = $2 AND key = $3 AND superseded_by IS NULL
		ORDER BY id DESC
		LIMIT 1
	`, versionID, version.FactKindStageTransition, enteredAt.UTC().Format(time.RFC3339)).Scan(&reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read last stage reason for version %d: %w", versionID, err)
	}
	return strings.TrimSpace(reason), nil
}
