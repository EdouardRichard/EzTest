package resource

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// TestAccount is one credential set of the global test-account pool (v0.22
// R-C1-2/DB-15). Accounts are not owned by a SUT or environment; a version opts
// into a subset through version_account.
//
// SUTID is an optional AFFINITY hint added by v0.2.4-B (SC-B1): it records which
// SUT the credential was created for, and nil means "no affinity" (a shared
// account). v0.2.4-F (P3, SC-F6) wires the affinity behaviour: execution
// credential resolution (execution/infrastructure/adapters/credential_adapter.go)
// prefers accounts whose SUTID matches the resolved SUT and falls back to shared
// (nil-SUTID) accounts when no affinity match exists.
type TestAccount struct {
	ID        int64
	SUTID     *int64
	Username  string
	Password  string
	Roles     []string
	Notes     string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks account fields for creation; password is required.
func (a TestAccount) Validate() error {
	if err := a.validateFields(); err != nil {
		return err
	}
	return validation.String("password", a.Password, 1, 1000)
}

// ValidateUpdate checks account fields for an update where the password may be
// omitted (meaning "keep the stored password").
func (a TestAccount) ValidateUpdate() error {
	if err := a.validateFields(); err != nil {
		return err
	}
	return validation.String("password", a.Password, 0, 1000)
}

func (a TestAccount) validateFields() error {
	if err := validation.String("username", a.Username, 1, 100); err != nil {
		return err
	}
	// A present affinity must name a real row; nil means "no affinity".
	if a.SUTID != nil && *a.SUTID <= 0 {
		return platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	if err := validation.StringList("roles", a.Roles, 20); err != nil {
		return err
	}
	if err := validation.String("notes", a.Notes, 0, 2000); err != nil {
		return err
	}
	return validateStatus(a.Status)
}
