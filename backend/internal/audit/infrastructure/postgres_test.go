package auditpg

import "testing"

func TestLikePrefixPatternEscapesWildcards(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"auth.login", `auth.login%`},
		{"100%", `100\%%`},
		{"a_b", `a\_b%`},
		{`back\slash`, `back\\slash%`},
		{"", "%"},
	}
	for _, tc := range cases {
		if got := likePrefixPattern(tc.in); got != tc.want {
			t.Errorf("likePrefixPattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
