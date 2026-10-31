// Package clock provides an injectable time source.
package clock

import "time"

// Clock is the minimal time source used by application services.
type Clock interface {
	Now() time.Time
}

// System is the real clock.
type System struct{}

// Now returns the current time.
func (System) Now() time.Time { return time.Now() }
