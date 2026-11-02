package pagination

import "testing"

func TestLimit(t *testing.T) {
	cases := []struct {
		raw  string
		def  int
		max  int
		want int
	}{
		{"", 20, 100, 20},
		{"5", 20, 100, 5},
		{"0", 20, 100, 20},
		{"-3", 20, 100, 20},
		{"abc", 20, 100, 20},
		{"500", 20, 100, 100},
	}
	for _, tc := range cases {
		if got := Limit(tc.raw, tc.def, tc.max); got != tc.want {
			t.Fatalf("Limit(%q, %d, %d) = %d, want %d", tc.raw, tc.def, tc.max, got, tc.want)
		}
	}
}

func TestCursor(t *testing.T) {
	if got, err := Cursor(""); err != nil || got != 0 {
		t.Fatalf("Cursor(\"\") = %d, %v", got, err)
	}
	if got, err := Cursor("123"); err != nil || got != 123 {
		t.Fatalf("Cursor(123) = %d, %v", got, err)
	}
	for _, raw := range []string{"abc", "-1", "1.5"} {
		if _, err := Cursor(raw); err == nil {
			t.Fatalf("Cursor(%q) accepted", raw)
		}
	}
}

func TestNextCursor(t *testing.T) {
	if got := NextCursor(false, 99); got != "" {
		t.Fatalf("NextCursor(false) = %q", got)
	}
	if got := NextCursor(true, 99); got != "99" {
		t.Fatalf("NextCursor(true) = %q", got)
	}
}
