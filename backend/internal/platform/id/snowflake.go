// Package id implements 64-bit snowflake IDs and their decimal string form.
package id

import (
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const (
	epochMillis int64 = 1704067200000 // 2024-01-01T00:00:00Z
	nodeBits    int64 = 10
	seqBits     int64 = 12
	maxNode     int64 = 1<<nodeBits - 1
	maxSeq      int64 = 1<<seqBits - 1
)

// Generator hands out monotonically increasing int64 IDs.
type Generator interface {
	Next() int64
}

// Snowflake generates IDs from milliseconds, node and sequence.
type Snowflake struct {
	mu        sync.Mutex
	node      int64
	lastMilli int64
	sequence  int64
	now       func() int64
}

// NewSnowflake validates the node (0..1023) and returns a generator.
func NewSnowflake(node int64) (*Snowflake, error) {
	if node < 0 || node > maxNode {
		return nil, fmt.Errorf("snowflake node must be between 0 and %d, got %d", maxNode, node)
	}
	return &Snowflake{node: node, now: func() int64 { return time.Now().UnixMilli() }}, nil
}

// Next returns a new ID. It never goes backwards even if the wall clock does.
func (s *Snowflake) Next() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if now < s.lastMilli {
		now = s.lastMilli
	}
	if now == s.lastMilli {
		s.sequence = (s.sequence + 1) & maxSeq
		if s.sequence == 0 {
			// The 12-bit sequence is exhausted inside one millisecond. Wait for
			// the wall clock to advance instead of logically moving lastMilli
			// into the future: a future timestamp would leak into logs and make
			// every subsequent ID look newer than the actual clock.
			for now <= s.lastMilli {
				runtime.Gosched()
				time.Sleep(50 * time.Microsecond)
				now = s.now()
			}
		}
	} else {
		s.sequence = 0
	}
	s.lastMilli = now

	delta := now - epochMillis
	if delta < 0 {
		delta = 0
	}
	return delta<<(nodeBits+seqBits) | s.node<<seqBits | s.sequence
}

// Format renders an ID as the JSON/URL decimal string form.
func Format(value int64) string {
	return strconv.FormatInt(value, 10)
}

// Parse reads the decimal string form of an ID.
func Parse(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fmt.Errorf("id must be positive")
	}
	return value, nil
}
