package versionpg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// versionAdvisoryLockSQL is the version-scoped serialization lock shared with
// the execution intent creation path. The exact key is pinned in both bounded
// contexts: pg_advisory_xact_lock(hashtext('eztest:version:' || $1)).
//
// Version close and requirement closure take it as their FIRST lock (before any
// row lock), and the intent insert takes it as its first lock too. That order
// makes the recheck-before-commit window of a close/closure visible to a
// concurrent intent creation (and vice versa), instead of allowing an intent to
// commit behind a recheck that already passed.
const versionAdvisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtext('eztest:version:' || ($1::bigint)::text))`

// lockVersionTx acquires the version advisory lock of the current transaction.
// Callers must not hold any row lock yet: advisory first keeps the lock order
// consistent with the intent creation transaction.
func lockVersionTx(ctx context.Context, tx pgx.Tx, versionID int64, op string) error {
	if _, err := tx.Exec(ctx, versionAdvisoryLockSQL, versionID); err != nil {
		return fmt.Errorf("%s: lock version advisory: %w", op, err)
	}
	return nil
}
