package id

import (
	"sync"
	"testing"
	"time"
)

// snowflakeTestEpochMs is the 2024-01-01T00:00:00Z epoch in Unix
// milliseconds, pinned as a literal so the test fails if the implementation
// moves the epoch.
const snowflakeTestEpochMs int64 = 1_704_067_200_000

// fixedClock returns a clock function frozen at now, plus a setter the
// rollback test uses to move it backwards.
func fixedClock(now time.Time) (func() time.Time, func(time.Time)) {
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	set := func(v time.Time) {
		mu.Lock()
		defer mu.Unlock()
		now = v
	}
	return clock, set
}

// TestSnowflakeMonotonicAndUnique proves 10k IDs from one generator are
// strictly increasing and never repeat (INV-15: unique and monotonic in
// process).
func TestSnowflakeMonotonicAndUnique(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock, _ := fixedClock(base)
	sf, err := newSnowflake(1, clock)
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[int64]struct{}, 10_000)
	var prev int64
	for i := 0; i < 10_000; i++ {
		got := sf.Next()
		if _, ok := seen[got]; ok {
			t.Fatalf("Next() returned duplicate id %d at iteration %d", got, i)
		}
		seen[got] = struct{}{}
		if i > 0 && got <= prev {
			t.Fatalf("Next() id %d at iteration %d is not greater than previous %d", got, i, prev)
		}
		prev = got
	}
}

// TestSnowflakeEncodesEpochNodeAndSequence pins the D29 bit layout:
// 41-bit milliseconds since 2024-01-01 UTC, 10-bit node, 12-bit sequence.
func TestSnowflakeEncodesEpochNodeAndSequence(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock, _ := fixedClock(base)
	const nodeID int64 = 5
	sf, err := newSnowflake(nodeID, clock)
	if err != nil {
		t.Fatal(err)
	}

	got := sf.Next()
	if got <= 0 {
		t.Fatalf("Next()=%d, want a positive int64", got)
	}
	if wantMs := base.UnixMilli() - snowflakeTestEpochMs; got>>22 != wantMs {
		t.Fatalf("time field = %d, want %d", got>>22, wantMs)
	}
	if node := (got >> 12) & 0x3FF; node != nodeID {
		t.Fatalf("node field = %d, want %d", node, nodeID)
	}
	if seq := got & 0xFFF; seq != 0 {
		t.Fatalf("sequence field = %d, want 0 for the first id", seq)
	}
}

// TestSnowflakeRejectsInvalidNode proves only 0..1023 are valid node IDs.
func TestSnowflakeRejectsInvalidNode(t *testing.T) {
	clock, _ := fixedClock(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	for _, nodeID := range []int64{-1, 1024} {
		if _, err := NewSnowflake(nodeID); err == nil {
			t.Fatalf("NewSnowflake(%d) accepted an out-of-range node", nodeID)
		}
	}
	for _, nodeID := range []int64{0, 1023} {
		if _, err := NewSnowflake(nodeID); err != nil {
			t.Fatalf("NewSnowflake(%d) rejected a valid node: %v", nodeID, err)
		}
	}
	if sf, err := newSnowflake(0, clock); err != nil || sf == nil {
		t.Fatalf("newSnowflake(0) = %v, %v", sf, err)
	}
}

// TestSnowflakeClockRollback proves a backwards clock step never repeats or
// regresses an ID: the generator reuses the last millisecond with an
// incremented sequence instead of waiting for wall time to catch up.
func TestSnowflakeClockRollback(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock, setClock := fixedClock(base)
	sf, err := newSnowflake(1, clock)
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[int64]struct{}, 128)
	prev := sf.Next()
	seen[prev] = struct{}{}

	setClock(base.Add(-time.Hour))
	for i := 0; i < 100; i++ {
		got := sf.Next()
		if got <= prev {
			t.Fatalf("id %d after clock rollback is not greater than previous %d", got, prev)
		}
		if _, ok := seen[got]; ok {
			t.Fatalf("id %d repeated after clock rollback", got)
		}
		seen[got] = struct{}{}
		prev = got
	}
}

// TestSnowflakeConcurrentUnique proves 8 goroutines generating 1000 IDs each
// share one generator without duplicates.
func TestSnowflakeConcurrentUnique(t *testing.T) {
	const (
		goroutines   = 8
		perGoroutine = 1000
	)
	clock, _ := fixedClock(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	sf, err := newSnowflake(7, clock)
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[int64]struct{}, goroutines*perGoroutine)
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			local := make([]int64, 0, perGoroutine)
			for j := 0; j < perGoroutine; j++ {
				local = append(local, sf.Next())
			}
			mu.Lock()
			defer mu.Unlock()
			for _, v := range local {
				if _, ok := seen[v]; ok {
					t.Errorf("concurrent Next() returned duplicate id %d", v)
				}
				seen[v] = struct{}{}
			}
		}()
	}
	wg.Wait()

	if want := goroutines * perGoroutine; len(seen) != want {
		t.Fatalf("collected %d unique ids, want %d", len(seen), want)
	}
}
