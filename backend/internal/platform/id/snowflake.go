package id

import (
	"fmt"
	"sync"
	"time"
)

// Snowflake layout (D29): 41-bit milliseconds since the custom epoch, 10-bit
// node ID and 12-bit sequence. The top bit stays clear, so every generated ID
// is a positive int64.
const (
	// snowflakeEpochMs is 2024-01-01T00:00:00Z in Unix milliseconds.
	snowflakeEpochMs int64 = 1_704_067_200_000

	snowflakeNodeBits = 10
	snowflakeSeqBits  = 12

	snowflakeMaxNodeID int64 = 1<<snowflakeNodeBits - 1 // 1023
	snowflakeMaxSeq    int64 = 1<<snowflakeSeqBits - 1  // 4095

	snowflakeNodeShift = snowflakeSeqBits
	snowflakeTimeShift = snowflakeSeqBits + snowflakeNodeBits
)

// Snowflake generates unique, monotonically increasing 64-bit IDs in the
// process. It is safe for concurrent use.
type Snowflake struct {
	mu     sync.Mutex
	nodeID int64
	lastMs int64
	seq    int64
	now    func() time.Time
}

// NewSnowflake returns a generator for the given 10-bit node ID. nodeID must
// be in 0..1023 and must be unique per running instance.
func NewSnowflake(nodeID int64) (*Snowflake, error) {
	return newSnowflake(nodeID, time.Now)
}

// newSnowflake is NewSnowflake with an injectable clock for deterministic
// tests; production callers must use NewSnowflake.
func newSnowflake(nodeID int64, now func() time.Time) (*Snowflake, error) {
	if nodeID < 0 || nodeID > snowflakeMaxNodeID {
		return nil, fmt.Errorf("id: snowflake node ID must be between 0 and %d, got %d", snowflakeMaxNodeID, nodeID)
	}
	return &Snowflake{nodeID: nodeID, lastMs: -1, now: now}, nil
}

// Next returns the next ID. IDs are strictly increasing for the lifetime of
// the generator, including across clock rollback: when the wall clock does not
// advance, the sequence is bumped, and an exhausted sequence advances the
// logical millisecond instead of sleeping (which would deadlock a stalled
// clock). The 4096 IDs/ms/node budget is therefore a per logical millisecond
// budget.
func (s *Snowflake) Next() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	ms := s.now().UnixMilli() - snowflakeEpochMs
	if ms > s.lastMs {
		s.lastMs = ms
		s.seq = 0
	} else {
		s.seq++
		if s.seq > snowflakeMaxSeq {
			s.lastMs++
			s.seq = 0
		}
	}
	return s.lastMs<<snowflakeTimeShift | s.nodeID<<snowflakeNodeShift | s.seq
}
