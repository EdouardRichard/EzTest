// Package resource is the resource-catalog domain: SUTs, environments, pages,
// APIs, target databases, test accounts and code repositories.
package resource

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Lifecycle statuses shared by environments, target databases, test accounts
// and code repositories.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// SUT is a system under test.
type SUT struct {
	ID        int64
	Name      string
	Intro     string
	CreatedBy int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks the editable SUT fields.
func (s SUT) Validate() error {
	if err := validation.String("name", s.Name, 1, 100); err != nil {
		return err
	}
	if err := validation.String("intro", s.Intro, 0, 2000); err != nil {
		return err
	}
	return nil
}
