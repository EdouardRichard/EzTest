package resource

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Environment is one deployable environment of a SUT.
type Environment struct {
	ID        int64
	SUTID     int64
	Name      string
	BaseURL   string
	IsDefault bool
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks environment fields; default uniqueness is enforced by the
// repository transaction and partial unique index.
func (e Environment) Validate() error {
	if err := validation.String("name", e.Name, 1, 100); err != nil {
		return err
	}
	if err := validation.URL("base_url", e.BaseURL); err != nil {
		return err
	}
	return validateStatus(e.Status)
}

func validateStatus(status string) error {
	switch status {
	case StatusActive, StatusDisabled:
		return nil
	default:
		return invalidEnum("status", status)
	}
}
