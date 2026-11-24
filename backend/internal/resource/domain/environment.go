package resource

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// MaxEnvironmentDescriptionRunes caps the optional environment description.
const MaxEnvironmentDescriptionRunes = 2000

// Environment is a top-level deployment environment (v0.2.4-B SC-B1). The
// hierarchy is environment -> SUT -> assets: an environment owns SUTs, never the
// other way round, and base_url lives on the SUT that actually serves it.
type Environment struct {
	ID          int64
	Name        string
	Description string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate checks the editable environment fields. Name uniqueness is enforced
// by the repository transaction and the uk_environment_name unique index.
func (e Environment) Validate() error {
	if err := validation.String("name", e.Name, 1, 100); err != nil {
		return err
	}
	if err := validation.String("description", e.Description, 0, MaxEnvironmentDescriptionRunes); err != nil {
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
