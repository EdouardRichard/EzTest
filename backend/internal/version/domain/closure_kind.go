package version

// Closure kinds (v0.2.4-G SC-G11 / GD-06 / P23). ClosureKind classifies how a
// version closed: normal through the full closure predicate, waived through the
// admin waiver override (which also sets Unverified).
//
// This file is the SINGLE authority for the closure_kind enum, mirroring the
// stage.go (Stage / Stages) and version.go (status / Statuses) shape: the const
// block is the root, ClosureKinds() is the derived list, and every other copy
// (the frozen version.closure_kind CHECK, the migration SQL, the report
// fingerprint payload) is validated against it rather than hand-maintained
// (constitution 五.1 / V-0017). The enum lives in its own file so the status AST
// root guard (TestStatusEnumRootIsDerived), which scans version.go's untyped
// string constants as the status enum, is not confused by a second enum.
const (
	ClosureKindNormal = "normal"
	ClosureKindWaived = "waived"
)

// ClosureKinds returns every version closure kind in canonical order. It is the
// SINGLE authority for the closure_kind enum; the AST root guard
// (TestClosureKindEnumRootIsDerived), the migration single-source gate
// (db/closure_kind_enum_test.go) and the applied-constraint schema pin
// (db.TestGMigrationSchema) derive their expected set from it.
func ClosureKinds() []string {
	return []string{ClosureKindNormal, ClosureKindWaived}
}
