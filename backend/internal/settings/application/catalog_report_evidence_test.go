package settingsapp

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

// TestGReportEvidenceCapSettingsRegistered pins the v0.2.4-G SC-G7 report
// evidence projection cap keys (R-G2/R-C I-4): a per-run and an aggregate
// display cap, both hot-reloadable integers defaulting to the bounded read-model
// values, 0 = unlimited. They constrain the report DISPLAY projection only —
// the execution write cap (execution.evidence_max_per_case_run, default 0)
// stays independent (C R-C5).
func TestGReportEvidenceCapSettingsRegistered(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	byKey := make(map[string]settings.Definition, len(defs))
	for _, def := range defs {
		byKey[def.Key] = def
	}
	cases := []struct {
		key string
		def string
	}{
		{"report.evidence_max_per_case_run", "100"},
		{"report.evidence_max_total", "2000"},
	}
	for _, tc := range cases {
		def, ok := byKey[tc.key]
		if !ok {
			t.Fatalf("settings key %q not registered", tc.key)
		}
		if def.Category != "report" || def.Type != settings.TypeNumber || !def.Integer {
			t.Fatalf("%s = %+v, want a report-category integer number", tc.key, def)
		}
		if string(def.Default) != tc.def {
			t.Fatalf("%s default = %s, want %s", tc.key, def.Default, tc.def)
		}
		if def.Min == nil || *def.Min != 0 {
			t.Fatalf("%s min = %v, want 0 (0 = unlimited)", tc.key, def.Min)
		}
		if def.Secret || def.RestartRequired {
			t.Fatalf("%s must be hot-reloadable and non-secret", tc.key)
		}
	}
}
