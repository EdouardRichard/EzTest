// Package resource is the resource-catalog domain: SUTs, environments, pages,
// APIs, target databases, test accounts and code repositories.
package resource

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Lifecycle statuses shared by environments, target databases, test accounts
// and code repositories.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// MaxFamilyRunes caps the optional SUT family slug.
const MaxFamilyRunes = 64

// SUT is a system under test.
type SUT struct {
	ID    int64
	Name  string
	Intro string
	// Family is the optional SUT family (P7-D-01). Knowledge items with
	// scope=system are visible to versions bound to a SUT with the matching
	// family used as system_key. nil means the SUT has no family.
	Family    *string
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
	if _, err := NormalizeFamily(s.Family); err != nil {
		return err
	}
	return nil
}

// NormalizeFamily trims and validates the optional SUT family. nil means the
// SUT has no family. A present family must be lowercase, 1..64 runes and free
// of whitespace so it can be used as a knowledge system_key.
func NormalizeFamily(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil, platform.Invalid("family", "must not be empty")
	}
	if utf8.RuneCountInString(value) > MaxFamilyRunes {
		return nil, platform.Invalid("family", "must be at most 64 characters")
	}
	if value != strings.ToLower(value) {
		return nil, platform.Invalid("family", "must be lowercase")
	}
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return nil, platform.Invalid("family", "must not contain whitespace")
	}
	return &value, nil
}
