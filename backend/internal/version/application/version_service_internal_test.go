package application

import "testing"

// internalIDs is an IDGenerator stub used to prove that WithIDGenerator still
// wins over the package default.
type internalIDs struct {
	next int64
}

// Next returns the next scripted id.
func (g *internalIDs) Next() int64 {
	id := g.next
	g.next++
	return id
}

// TestDefaultIDGeneratorIsShared pins that every VersionService constructed
// without WithIDGenerator binds to one process-wide Snowflake generator. A
// per-service fallback lets two services started in the same millisecond mint
// identical ids from the same node-1 state.
func TestDefaultIDGeneratorIsShared(t *testing.T) {
	first := DefaultIDGenerator()
	second := DefaultIDGenerator()
	if first != second {
		t.Fatal("DefaultIDGenerator returned distinct generator instances")
	}

	// The shared generator never repeats an id within a burst: Snowflake is
	// strictly increasing, including across sequence rollover and clock
	// rollback.
	seen := make(map[int64]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		id := first.Next()
		if id <= 0 {
			t.Fatalf("generated non-positive id %d at position %d", id, i)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("generated duplicate id %d at position %d", id, i)
		}
		seen[id] = struct{}{}
	}

	// Default constructions must share the generator; explicit injection must
	// still override it.
	serviceA := NewVersionService(nil, nil, nil, nil)
	serviceB := NewVersionService(nil, nil, nil, nil)
	if serviceA.ids != first || serviceB.ids != first {
		t.Fatal("NewVersionService without WithIDGenerator did not use the shared default generator")
	}
	custom := &internalIDs{next: 100}
	injected := NewVersionService(nil, nil, nil, nil, WithIDGenerator(custom))
	if injected.ids != custom {
		t.Fatal("WithIDGenerator did not override the shared default generator")
	}
}
