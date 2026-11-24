package execution

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// SessionFixture is one append-only fact recording a login storage-state the
// execution team saved for reuse across case_runs (v0.2.4-F P18, SC-F5). Rows
// are only ever inserted, never rewritten or deleted; saving the same reuse key
// again appends a second row (latest wins, constitution 五.7).
//
// The reuse key is (VersionID, SUTID, EnvironmentID, Role) — the cross-case
// reuse layer above today's per-run storage_state. StorageStateRef is the
// content-addressed reference to the saved state in the content ledger; the
// bytes never live here. VersionID/SUTID/EnvironmentID are logical BIGINT
// references into the version/resource contexts (no cross-context FK).
type SessionFixture struct {
	ID              int64
	VersionID       int64
	SUTID           int64
	EnvironmentID   int64
	Role            string
	StorageStateRef string
	CreatedAt       time.Time
	CreatedBy       int64
}

// MaxFixtureRoleRunes caps the account role label; MaxStorageStateRefRunes caps
// the content-addressed storage-state reference.
const (
	MaxFixtureRoleRunes     = 64
	MaxStorageStateRefRunes = 255
)

// Validate checks one fixture fact before it is appended.
func (f SessionFixture) Validate() error {
	if f.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if f.SUTID <= 0 {
		return platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	if f.EnvironmentID <= 0 {
		return platform.Invalid("environment_id", "must be a positive decimal ID")
	}
	if err := validation.String("role", f.Role, 1, MaxFixtureRoleRunes); err != nil {
		return err
	}
	if err := validation.String("storage_state_ref", f.StorageStateRef, 1, MaxStorageStateRefRunes); err != nil {
		return err
	}
	return nil
}

// SessionFixtureRepository persists the append-only session_fixture fact store.
// It exposes only Append/List — never Update/Delete (append-only fact,
// constitution 五.7). ListSessionFixtures returns one reuse key's
// (version,sut,environment,role) facts in append order (id ascending); the caller
// takes the latest as the effective fixture.
type SessionFixtureRepository interface {
	AppendSessionFixture(ctx context.Context, fixture SessionFixture) (SessionFixture, error)
	ListSessionFixtures(ctx context.Context, versionID, sutID, environmentID int64, role string) ([]SessionFixture, error)
}
