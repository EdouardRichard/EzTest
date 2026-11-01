package executionpg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// versionAdvisoryLockSQL is the version-scoped serialization lock shared with
// the version close and requirement closure transactions. The exact key is
// pinned in both bounded contexts:
// pg_advisory_xact_lock(hashtext('eztest:version:' || $1)).
//
// The intent insert takes it as the first lock of its transaction, before the
// version/requirement guards and the insert. Close/closure take the same key
// before their row lock and recheck, so whichever side commits first is visible
// to the other: a completed version or a closed requirement can never end up
// with an active intent.
const versionAdvisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtext('eztest:version:' || ($1::bigint)::text))`

// lockVersionTx acquires the version advisory lock of the current transaction.
// Callers must not hold any row lock yet: advisory first keeps the lock order
// consistent with the close/closure transactions.
func lockVersionTx(ctx context.Context, tx pgx.Tx, versionID int64, op string) error {
	if _, err := tx.Exec(ctx, versionAdvisoryLockSQL, versionID); err != nil {
		return fmt.Errorf("%s: lock version advisory: %w", op, err)
	}
	return nil
}
