package domain

import "time"

// Company is the tenant that owns resources. P2 is single-company: every
// system, account and environment belongs to the seeded company with key
// "default". Status is "active" or "disabled".
type Company struct {
	ID        int64
	Key       string
	Name      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}
