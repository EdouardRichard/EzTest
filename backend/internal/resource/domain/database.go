package resource

import (
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// TargetDatabase is one database the tests of a SUT environment may access.
type TargetDatabase struct {
	ID            int64
	SUTID         int64
	EnvironmentID int64
	Name          string
	Kind          string
	DSN           string
	Username      string
	Password      string
	ReadOnly      bool
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Validate checks target-database fields. Password is optional.
func (d TargetDatabase) Validate() error {
	if err := validation.String("name", d.Name, 1, 100); err != nil {
		return err
	}
	if err := validation.String("dsn", d.DSN, 1, 2000); err != nil {
		return err
	}
	if !strings.HasPrefix(d.DSN, "postgres://") && !strings.HasPrefix(d.DSN, "postgresql://") {
		return invalidEnum("dsn", "must start with postgres:// or postgresql://")
	}
	if d.Kind != "postgres" {
		return invalidEnum("kind", d.Kind)
	}
	if err := validation.String("username", d.Username, 1, 100); err != nil {
		return err
	}
	if err := validation.String("password", d.Password, 0, 1000); err != nil {
		return err
	}
	return validateStatus(d.Status)
}
