package domain

import "time"

// Account kinds, mirrored from the accounts.kind CHECK constraint.
const (
	AccountKindHuman   = "human"
	AccountKindService = "service"
)

// Account is an identity on the systems under test. It is deliberately
// different from a platform user (users table): credentials for a target
// system live on its Access rows.
type Account struct {
	ID        int64
	CompanyID int64
	Key       string
	Label     string
	Username  string
	Kind      string
	Status    string
	Notes     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AccountFilter selects a page of accounts ordered by ID ascending.
type AccountFilter struct {
	Status string
	Kind   string
	Page   Page
}
