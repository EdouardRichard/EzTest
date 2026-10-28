package domain

import (
	"regexp"
	"time"
)

// resourceKeyRE is the documented resource key format (spec 搂7.2): 2-64
// characters, lowercase letters/digits first and then letters, digits, '_' or
// '-'.
var resourceKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// ValidateResourceKey enforces the key format shared by systems, accounts and
// code repositories. Keys are immutable after creation.
func ValidateResourceKey(key string) error {
	if !resourceKeyRE.MatchString(key) {
		return NewInvalid("key", "invalid_key",
			"key must be 2-64 characters of lowercase letters, digits, '_' or '-' and start with a letter or digit")
	}
	return nil
}

// System kinds, mirrored from the system.kind CHECK constraint.
const (
	SystemKindWeb = "web"
	SystemKindAPI = "api"
)

// Status values shared by the soft-deletable resources, mirrored from their
// status CHECK constraints.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// System is a target application under test. Its key is immutable after
// creation; DefaultSessionTTLSeconds bounds how long an imported Session lives
// when the Access does not override the TTL.
type System struct {
	ID                       int64
	CompanyID                int64
	Key                      string
	Name                     string
	Kind                     string
	BaseURL                  string
	DefaultSessionTTLSeconds int
	Notes                    string
	Status                   string
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// SystemFilter selects a page of systems ordered by ID ascending.
type SystemFilter struct {
	Status string
	Kind   string
	Page   Page
}
