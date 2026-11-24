package version_test

// V-0017 closure_kind enum authority ROOT guard (v0.2.4-G T2). Mirrors
// TestStatusEnumRootIsDerived / TestStageEnumRootIsDerived: the const block in
// closure_kind.go is the single authority root and ClosureKinds() is the derived
// list every other copy (DB CHECK, migration SQL, report fingerprint) is validated
// against. assertEnumRootDerived (enum_ast_authority_test.go) parses the file with
// go/ast and asserts name-set + value-set equality, so a closure kind added to (or
// removed from) either side fails immediately rather than at the first runtime
// write. Reusing the shared helper keeps this guard in the same AST-derived family.

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestClosureKindEnumRootIsDerived(t *testing.T) {
	assertEnumRootDerived(t, "closure_kind.go", "", "ClosureKinds", version.ClosureKinds())
}
