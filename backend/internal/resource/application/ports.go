// Package application holds the resource context's use cases and the ports
// they consume from the outside world: the clock, the read-only login checker
// and the read-only local Git validator. The domain and application layers
// depend on these interfaces; adapters live in infrastructure.
package application

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Clock returns the current time. It is injectable so TTL and expiry rules can
// be tested without sleeping.
type Clock interface {
	Now() time.Time
}

// LoginChecker performs the environment's read-only login_check (spec §6.3).
// A non-nil error means the check could not be performed at all; a denied or
// unreachable result is reported in LoginCheckResult.
type LoginChecker interface {
	Check(ctx context.Context, env domain.Environment, state domain.StorageState) (domain.LoginCheckResult, error)
}

// GitValidator validates a local Git checkout read-only and returns the commit
// the fixed branch points at (spec §6.7).
type GitValidator interface {
	Validate(ctx context.Context, path, branch string) (domain.GitValidation, error)
}

// DefaultCompanyKey is the key of the single seeded company that owns every P2
// resource. Requests never carry a company id.
const DefaultCompanyKey = "default"
