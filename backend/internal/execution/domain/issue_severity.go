package execution

// Task 112: the deterministic issue severity has exactly one source of truth —
// the case priority (P3-D-12: P0/P1 → major, P2 → minor, P3/unknown → trivial).
// The execution analysis, the attribution draft and the P4 issue projection all
// use this helper so an Issue record, the run narrative and the report agree.

// IssueSeverity maps a case priority onto the delivery issue severity enum.
// An unknown or empty priority is the safe deterministic fallback (trivial):
// it can never overstate a defect.
func IssueSeverity(priority string) string {
	switch priority {
	case "P0", "P1":
		return "major"
	case "P2":
		return "minor"
	default:
		return "trivial"
	}
}
