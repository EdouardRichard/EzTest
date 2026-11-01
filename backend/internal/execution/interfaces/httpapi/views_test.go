package httpapi

import "testing"

// TestEvidenceKindsIncludeVisualKinds pins the EvidenceView.kind API enum
// (spec §4.2): the eight P3 kinds stay valid and the two P5 visual evidence
// kinds are added for GET /evidence filtering and evidence views.
func TestEvidenceKindsIncludeVisualKinds(t *testing.T) {
	kinds := evidenceKinds()
	if len(kinds) != 10 {
		t.Fatalf("evidence kinds = %d, want 10", len(kinds))
	}
	seen := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		if seen[kind] {
			t.Fatalf("duplicate evidence kind %q", kind)
		}
		seen[kind] = true
	}
	for _, want := range []string{
		"screenshot", "snapshot", "console", "network", "http", "db", "download", "text",
		"visual_actual", "visual_diff",
	} {
		if !seen[want] {
			t.Fatalf("evidence kind %q is missing from the API enum", want)
		}
	}
}
