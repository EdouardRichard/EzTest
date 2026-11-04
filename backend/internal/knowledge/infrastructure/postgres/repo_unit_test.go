package knowledgepg

import "testing"

// TestLikeContainsPatternEscapesLikeMetacharacters pins the literal "contains"
// pattern used by the retrieval top-K ILIKE branches: a free-text query is
// data, so % and _ must not turn it into a wildcard.
func TestLikeContainsPatternEscapesLikeMetacharacters(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "cjk text is unchanged", input: "登录", want: "%登录%"},
		{name: "percent is escaped", input: "100%", want: `%100\%%`},
		{name: "underscore is escaped", input: "a_b", want: `%a\_b%`},
		{name: "backslash is escaped first", input: `c:\temp`, want: `%c:\\temp%`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := likeContainsPattern(testCase.input); got != testCase.want {
				t.Fatalf("likeContainsPattern(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}
