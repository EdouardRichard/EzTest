package domain

import (
	"strings"
	"testing"
)

func TestValidateLoginCheck(t *testing.T) {
	if err := ValidateLoginCheck(nil); err != nil {
		t.Fatalf("nil login check: %v", err)
	}
	if err := ValidateLoginCheck(&LoginCheck{}); err != nil {
		t.Fatalf("empty login check: %v", err)
	}
	valid := &LoginCheck{URL: "/account", ExpectStatus: []int{200, 204}, BodyContains: "Fixture Account"}
	if err := ValidateLoginCheck(valid); err != nil {
		t.Fatalf("valid login check: %v", err)
	}
	tooManyStatuses := &LoginCheck{ExpectStatus: []int{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110}}
	cases := []*LoginCheck{
		{URL: "/" + strings.Repeat("a", 2048)},
		{ExpectStatus: []int{99}},
		{ExpectStatus: []int{600}},
		tooManyStatuses,
		{BodyContains: strings.Repeat("a", 513)},
	}
	for i, lc := range cases {
		if err := ValidateLoginCheck(lc); err == nil {
			t.Fatalf("case %d: want error", i)
		}
	}
}
