package id

import (
	"encoding/json"
	"testing"
)

func TestNewSnowflakeRejectsInvalidNode(t *testing.T) {
	for _, node := range []int64{-1, 1024} {
		if _, err := NewSnowflake(node); err == nil {
			t.Fatalf("NewSnowflake(%d) accepted", node)
		}
	}
}

func TestSnowflakeIsUniqueAndMonotonic(t *testing.T) {
	gen, err := NewSnowflake(7)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	var previous int64
	seen := make(map[int64]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		next := gen.Next()
		if next <= previous {
			t.Fatalf("id %d not greater than previous %d", next, previous)
		}
		if _, ok := seen[next]; ok {
			t.Fatalf("duplicate id %d", next)
		}
		seen[next] = struct{}{}
		previous = next
	}
}

func TestSnowflakeEncodesNode(t *testing.T) {
	gen, err := NewSnowflake(513)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	value := gen.Next()
	if got := (value >> 12) & 0x3FF; got != 513 {
		t.Fatalf("node bits = %d, want 513", got)
	}
}

func TestFormatParseRoundTrip(t *testing.T) {
	value := int64(72340172838076673)
	if got := ParseMust(t, Format(value)); got != value {
		t.Fatalf("round trip = %d, want %d", got, value)
	}
	if _, err := Parse(""); err == nil {
		t.Fatal("Parse(\"\") accepted")
	}
	if _, err := Parse("0"); err == nil {
		t.Fatal("Parse(0) accepted")
	}
	if _, err := Parse("abc"); err == nil {
		t.Fatal("Parse(abc) accepted")
	}
}

func TestJSONUsesString(t *testing.T) {
	payload, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: Format(72340172838076673)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(payload) != `{"id":"72340172838076673"}` {
		t.Fatalf("payload = %s", payload)
	}
}

func ParseMust(t *testing.T, raw string) int64 {
	t.Helper()
	value, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return value
}

func TestSnowflakeWaitsForNextMillisecondWhenSequenceExhausted(t *testing.T) {
	gen, err := NewSnowflake(1)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	const base int64 = 1_800_000_000_000
	var calls int64
	gen.now = func() int64 {
		calls++
		if calls > 5000 {
			return base + 1
		}
		return base
	}
	seen := map[int64]struct{}{}
	var previous int64
	for i := 0; i < 5000; i++ {
		next := gen.Next()
		if next <= previous {
			t.Fatalf("id %d not greater than %d", next, previous)
		}
		if _, duplicate := seen[next]; duplicate {
			t.Fatalf("duplicate id %d", next)
		}
		seen[next] = struct{}{}
		previous = next
	}
}
