package resource

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// TestAccount is one credential set used by tests in a SUT environment.
type TestAccount struct {
	ID            int64
	SUTID         int64
	EnvironmentID int64
	Username      string
	Password      string
	Roles         []string
	Notes         string
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
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
	if err := validation.StringList("roles", a.Roles, 20); err != nil {
		return err
	}
	if err := validation.String("notes", a.Notes, 0, 2000); err != nil {
		return err
	}
	return validateStatus(a.Status)
}
