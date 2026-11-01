// Package codeintelpg implements the codeintel repositories over PostgreSQL.
// Rows map 1:1 to spec §3.2–§3.4; cross-context IDs stay logical (no FK) and
// same-context FKs are explicit in the migrations.
package codeintelpg

import (
	"encoding/json"
	"errors"
	"fmt"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/jackc/pgx/v5"
)

// rowScanner is the shared pgx row/rows scan surface.
type rowScanner interface {
	Scan(dest ...any) error
}

// wrapDB maps pgx.ErrNoRows to the domain not-found sentinel and keeps other
// database failures opaque to callers (no SQL/DSN leakage upward).
func wrapDB(action string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return codeintel.ErrNotFound
	}
	return fmt.Errorf("%s: %w", action, err)
}

// jsonText renders a JSONB parameter, falling back to a valid document.
func jsonText(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}

// nullableInt64 keeps a NULL-able BIGINT parameter explicit.
func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

const indexRunColumns = `id, version_id, repo_id, commit_sha, branch, status, files_total,
	files_indexed, truncated, stats, errors, started_at, finished_at, created_by, created_at, updated_at`

func scanIndexRun(row rowScanner) (codeintel.IndexRun, error) {
	var run codeintel.IndexRun
	var versionID *int64
	var stats, errorsRaw []byte
	if err := row.Scan(&run.ID, &versionID, &run.RepoID, &run.CommitSHA, &run.Branch, &run.Status,
		&run.FilesTotal, &run.FilesIndexed, &run.Truncated, &stats, &errorsRaw,
		&run.StartedAt, &run.FinishedAt, &run.CreatedBy, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return codeintel.IndexRun{}, err
	}
	if versionID != nil {
		run.VersionID = *versionID
	}
	run.Stats = json.RawMessage(stats)
	run.Errors = json.RawMessage(errorsRaw)
	return run, nil
}
