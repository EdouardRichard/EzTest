// Package load provides the small process-local counters used by the P8
// drain/rolling-upgrade observability. They are intentionally not a metrics
// system: one counter per concern, incremented and decremented around the work
// it measures. The heartbeat copies the current values into api_instance so the
// admin instance roster and GET /api/health/ready can show whether a draining
// replica still has in-flight work.
package load

import "sync/atomic"

// Counter is a concurrency-safe monotonic gauge.
type Counter struct {
	value atomic.Int64
}

// Inc adds one to the counter.
func (c *Counter) Inc() {
	if c != nil {
		c.value.Add(1)
	}
}

// Dec subtracts one from the counter (never below zero in correct use).
func (c *Counter) Dec() {
	if c != nil {
		c.value.Add(-1)
	}
}

// Value returns the current count.
func (c *Counter) Value() int {
	if c == nil {
		return 0
	}
	return int(c.value.Load())
}
