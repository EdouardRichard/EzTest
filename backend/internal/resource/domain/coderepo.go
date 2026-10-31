package resource

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// CodeRepository is a source repository attached to a version SUT binding.
type CodeRepository struct {
	ID        int64
	Name      string
	Path      string
	Branch    string
	Include   []string
	Exclude   []string
	CreatedBy int64
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks code-repository fields.
func (r CodeRepository) Validate() error {
	if err := validation.String("name", r.Name, 1, 100); err != nil {
		return err
	}
	if err := validation.String("path", r.Path, 1, 1000); err != nil {
		return err
	}
	if err := validation.String("branch", r.Branch, 0, 200); err != nil {
		return err
	}
	if err := validation.StringList("include", r.Include, 100); err != nil {
		return err
	}
	if err := validation.StringList("exclude", r.Exclude, 100); err != nil {
		return err
	}
	return validateStatus(r.Status)
}
