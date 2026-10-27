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

// Test-local copies of the D29 field layout, so a shift or mask change in the
// implementation breaks these assertions instead of silently moving them.
const (
	snowflakeTestTimeShift = 22
	snowflakeTestNodeShift = 12
	snowflakeTestNodeMask  = 0x3FF
	snowflakeTestSeqMask   = 0xFFF
)

func snowflakeTimeField(id int64) int64 { return id >> snowflakeTestTimeShift }

func snowflakeNodeField(id int64) int64 {
	return id >> snowflakeTestNodeShift & snowflakeTestNodeMask
}

func snowflakeSeqField(id int64) int64 { return id & snowflakeTestSeqMask }

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

// TestSnowflakeSequenceOverflowAdvancesLogicalMillisecond pins the accepted
// policy for exhausting the 12-bit sequence: Next never blocks, it advances
// the logical millisecond instead. With a frozen clock the 4097th ID must
// carry time field +1 and sequence 0; once the wall clock catches up to or
// passes that logical millisecond, later IDs must stay strictly increasing and
// never repeat.
func TestSnowflakeSequenceOverflowAdvancesLogicalMillisecond(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock, setClock := fixedClock(base)
	const nodeID int64 = 3
	sf, err := newSnowflake(nodeID, clock)
	if err != nil {
		t.Fatal(err)
	}

	baseMs := base.UnixMilli() - snowflakeTestEpochMs

	seen := make(map[int64]struct{}, 4097+8)
	var prev int64
	record := func(phase string, idx int, id int64) {
		t.Helper()
		if _, ok := seen[id]; ok {
			t.Fatalf("%s id #%d: duplicate id %d", phase, idx, id)
		}
		seen[id] = struct{}{}
		if prev != 0 && id <= prev {
			t.Fatalf("%s id #%d: id %d is not greater than previous %d", phase, idx, id, prev)
		}
		prev = id
	}

	// The first 4096 IDs fit in the frozen millisecond: one per sequence slot.
	var lastFrozen int64
	for i := 0; i < 4096; i++ {
		lastFrozen = sf.Next()
		record("frozen", i, lastFrozen)
		if got := snowflakeTimeField(lastFrozen); got != baseMs {
			t.Fatalf("frozen id #%d time field = %d, want %d", i, got, baseMs)
		}
		if got := snowflakeSeqField(lastFrozen); got != int64(i) {
			t.Fatalf("frozen id #%d seq = %d, want %d", i, got, i)
		}
	}
	if got := snowflakeSeqField(lastFrozen); got != 4095 {
		t.Fatalf("last frozen id seq = %d, want 4095", got)
	}

	// The 4097th ID exhausts the sequence, so the generator advances the
	// logical millisecond rather than waiting on the frozen wall clock.
	overflow := sf.Next()
	record("overflow", 4096, overflow)
	if got := snowflakeTimeField(overflow); got != baseMs+1 {
		t.Fatalf("overflow id time field = %d, want %d (base+1)", got, baseMs+1)
	}
	if got := snowflakeSeqField(overflow); got != 0 {
		t.Fatalf("overflow id seq = %d, want 0", got)
	}
	if got := snowflakeNodeField(overflow); got != nodeID {
		t.Fatalf("overflow id node field = %d, want %d", got, nodeID)
	}

	// Catch the wall clock up to the logical millisecond: IDs may share that
	// millisecond, but must keep increasing without repeating.
	setClock(base.Add(time.Millisecond))
	for i := 0; i < 4; i++ {
		id := sf.Next()
		record("catch-up", i, id)
		if got := snowflakeTimeField(id); got != baseMs+1 {
			t.Fatalf("catch-up id #%d time field = %d, want %d", i, got, baseMs+1)
		}
	}

	// Once wall time passes the logical millisecond, it is authoritative again.
	setClock(base.Add(5 * time.Millisecond))
	for i := 0; i < 4; i++ {
		id := sf.Next()
		record("post-catch-up", i, id)
		if got := snowflakeTimeField(id); got != baseMs+5 {
			t.Fatalf("post-catch-up id #%d time field = %d, want %d", i, got, baseMs+5)
		}
	}

	if want := 4097 + 8; len(seen) != want {
		t.Fatalf("collected %d unique ids, want %d", len(seen), want)
	}
}

// TestSnowflakeClockAdvances proves that when wall time moves past the last
// millisecond, the time field advances by that many milliseconds, the sequence
// restarts at 0, and the ID is still strictly greater. Several IDs are taken
// before the advance so a missing sequence reset is observable: deleting the
// "s.seq = 0" line in Next's clock-advance branch leaves seq > 0 here.
func TestSnowflakeClockAdvances(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock, setClock := fixedClock(base)
	const nodeID int64 = 2
	sf, err := newSnowflake(nodeID, clock)
	if err != nil {
		t.Fatal(err)
	}

	baseMs := base.UnixMilli() - snowflakeTestEpochMs
	first := sf.Next()
	if got := snowflakeTimeField(first); got != baseMs {
		t.Fatalf("first id time field = %d, want %d", got, baseMs)
	}
	if got := snowflakeSeqField(first); got != 0 {
		t.Fatalf("first id seq = %d, want 0", got)
	}

	prev := first
	for i := 0; i < 3; i++ {
		prev = sf.Next()
	}
	if got := snowflakeSeqField(prev); got != 3 {
		t.Fatalf("seq before clock advance = %d, want 3", got)
	}

	const advanceMs = 7
	setClock(base.Add(advanceMs * time.Millisecond))
	got := sf.Next()

	if want := baseMs + advanceMs; snowflakeTimeField(got) != want {
		t.Fatalf("time field after advancing %dms = %d, want %d", advanceMs, snowflakeTimeField(got), want)
	}
	if seq := snowflakeSeqField(got); seq != 0 {
		t.Fatalf("sequence after clock advance = %d, want 0 (reset)", seq)
	}
	if node := snowflakeNodeField(got); node != nodeID {
		t.Fatalf("node field after clock advance = %d, want %d", node, nodeID)
	}
	if got <= prev {
		t.Fatalf("id %d after clock advance is not greater than previous %d", got, prev)
	}
}
