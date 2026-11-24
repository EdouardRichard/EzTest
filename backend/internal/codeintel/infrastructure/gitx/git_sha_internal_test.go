package gitx

import "testing"

// TestValidCommitSHANonEmpty pins the non-empty guard on the single SHA authority
// (H-B5). gitx delegates the 40-hex shape to platform/validation.GitCommitSHA,
// which accepts the empty string ("not resolved yet", the nullable-column
// semantics). A resolved commit must be present, so validCommitSHA rejects ""
// before delegating. Mutation: dropping the `value != ""` guard lets "" pass
// (GitCommitSHA("")==nil), and this test fails — the empty-string short-circuit
// trap the second hand-written regexp used to hide.
func TestValidCommitSHANonEmpty(t *testing.T) {
	if validCommitSHA("") {
		t.Fatal("empty SHA must be rejected: GitCommitSHA short-circuits nil on empty, so the non-empty guard is load-bearing")
	}
}

// TestValidCommitSHAShape pins that the delegated authority still accepts a
// well-formed 40-hex SHA and rejects malformed ones, so removing the second
// regexp did not loosen the shape rule.
func TestValidCommitSHAShape(t *testing.T) {
	const valid = "0123456789abcdef0123456789abcdef01234567" // 40 lowercase hex
	if !validCommitSHA(valid) {
		t.Fatalf("valid 40-hex SHA rejected: %q", valid)
	}
	for _, bad := range []string{
		"0123456789ABCDEF0123456789ABCDEF01234567",  // uppercase hex
		"0123456789abcdef0123456789abcdef0123456",   // 39 chars
		"0123456789abcdef0123456789abcdef012345678", // 41 chars
		"zzzz456789abcdef0123456789abcdef01234567",  // non-hex
		" 0123456789abcdef0123456789abcdef01234567", // leading space
		"0123456789abcdef0123456789abcdef01234567 ", // trailing space
	} {
		if validCommitSHA(bad) {
			t.Fatalf("invalid SHA accepted: %q", bad)
		}
	}
}
