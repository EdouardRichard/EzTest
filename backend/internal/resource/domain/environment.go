package domain

import "time"

// Environment kinds, mirrored from the environments.kind CHECK constraint.
const (
	EnvironmentKindDev     = "dev"
	EnvironmentKindStaging = "staging"
	EnvironmentKindProd    = "prod"
	EnvironmentKindOther   = "other"
)

// Observed login_check outcomes recorded in a session snapshot.
const (
	ObservedAllowed = "allowed"
	ObservedDenied  = "denied"
	ObservedError   = "error"
)

// LoginCheck is the optional, read-only login verification configured on an
// environment (spec §6.3). It is stored as JSONB in environments.login_check;
// the JSON tags match that stored document.
type LoginCheck struct {
	URL          string `json:"url"`
	ExpectStatus []int  `json:"expect_status"`
	BodyContains string `json:"body_contains"`
}

// LoginCheckResult is the outcome of one login_check request. Degraded means
// no check could be performed (not configured); Reason is empty for a normal
// allowed/denied result.
type LoginCheckResult struct {
	Observed string
	Status   int
	Degraded bool
	Reason   string
}

// ValidateLoginCheck enforces the optional login_check bounds of spec §7.2:
// URL at most 2048 bytes, at most 10 expect_status values in 100-599, and
// body_contains at most 512 bytes. An empty URL means "not configured".
func ValidateLoginCheck(lc *LoginCheck) error {
	if lc == nil {
		return nil
	}
	if len(lc.URL) > 2048 {
		return NewInvalid("loginCheck.url", "invalid_login_check", "login_check url must be at most 2048 bytes")
	}
	if len(lc.ExpectStatus) > 10 {
		return NewInvalid("loginCheck.expectStatus", "invalid_login_check",
			"login_check expect_status accepts at most 10 statuses")
	}
	for _, status := range lc.ExpectStatus {
		if status < 100 || status > 599 {
			return NewInvalid("loginCheck.expectStatus", "invalid_login_check",
				"login_check expect_status values must be between 100 and 599")
		}
	}
	if len(lc.BodyContains) > 512 {
		return NewInvalid("loginCheck.bodyContains", "invalid_login_check",
			"login_check body_contains must be at most 512 bytes")
	}
	return nil
}

// Environment is one deployment of a System. Key is immutable after creation.
type Environment struct {
	ID              int64
	SystemID        int64
	Key             string
	Name            string
	BaseURL         string
	Kind            string
	IsProd          bool
	AllowedHosts    []string
	LoginCheck      *LoginCheck
	AllowAutoLogin  bool
	RequireApproval bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
