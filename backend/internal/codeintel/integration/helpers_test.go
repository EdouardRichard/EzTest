//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// seedBaseCommit and seedHeadCommit are the valid lowercase 40-hex commit ids
// the codeintel fixtures use. The codeintel DDL checks every commit column
// against ^[0-9a-f]{40}$.
const (
	seedBaseCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	seedHeadCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// seedIDGenerator is the process-wide Snowflake generator of the seeded
// fixtures. It uses node 3 so its ids cannot collide with the application's
// node-configured generator (node 1) or the literal ids the repository fixtures
// supply.
var seedIDGenerator = sync.OnceValue(func() *id.Snowflake {
	generator, err := id.NewSnowflake(3)
	if err != nil {
		panic(fmt.Sprintf("integration: seed snowflake generator: %v", err))
	}
	return generator
})

// nextSeedID returns the next fixture id.
func nextSeedID() int64 { return seedIDGenerator().Next() }

// seedRepoVersionRequirement inserts the minimal P2/P3 rows later codeintel
// integration tests build on:
//
//   - one active P2 code repository (BIGSERIAL id);
//   - one P3 version bound to that repository at seedBaseCommit;
//   - one P3 requirement of that version.
//
// It returns the inserted ids. The repository key and the Snowflake ids are
// unique per call, so a test may call it more than once (for example to seed a
// second version) without violating the P2/P3 unique constraints.
func seedRepoVersionRequirement(t *testing.T, db *pgxpool.Pool) (repoID, versionID, requirementID int64) {
	t.Helper()
	ctx := context.Background()

	repoKey := fmt.Sprintf("p4-seed-%d", nextSeedID())
	if err := db.QueryRow(ctx,
		`INSERT INTO code_repository (key, name, local_path) VALUES ($1, $2, $3) RETURNING id`,
		repoKey, "P4 seed repository", "/tmp/"+repoKey,
	).Scan(&repoID); err != nil {
		t.Fatalf("seed code repository: %v", err)
	}

	versionID = nextSeedID()
	if _, err := db.Exec(ctx,
		`INSERT INTO version
			(id, name, status, code_repository_id, code_branch, code_baseline_commit, created_by, updated_by)
		 VALUES ($1, $2, 'draft', $3, 'main', $4, 1, 1)`,
		versionID, "P4 seed version", repoID, seedBaseCommit,
	); err != nil {
		t.Fatalf("seed version: %v", err)
	}

	requirementID = nextSeedID()
	if _, err := db.Exec(ctx,
		`INSERT INTO requirement (id, version_id, title, created_by, updated_by)
		 VALUES ($1, $2, $3, 1, 1)`,
		requirementID, versionID, "P4 seed requirement",
	); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	return repoID, versionID, requirementID
}

// ciPtr returns a pointer to value, for the nullable domain fields.
func ciPtr[T any](value T) *T { return &value }

// ciNow returns the current UTC time truncated to PostgreSQL's microsecond
// resolution, so values written to and read from TIMESTAMPTZ compare equal.
func ciNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// ciAssertJSONEqual compares two JSON documents semantically: JSONB does not
// preserve key order or whitespace, so raw byte equality is not meaningful.
func ciAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode stored JSON %q: %v", string(got), err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("stored JSON = %s, want %s", string(got), want)
	}
}

// ciHasAnalysisError reports whether errs contains an entry with code.
func ciHasAnalysisError(errs []domain.AnalysisError, code string) bool {
	for _, entry := range errs {
		if entry.Code == code {
			return true
		}
	}
	return false
}
