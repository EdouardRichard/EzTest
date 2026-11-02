package settingsapp

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

// TestP7SettingsRemainFrozen pins the eleven P7 keys and the report template
// version default "2" for new databases. The catalog grew to 85 in P8 but the
// P7 keys/defaults must not change.
func TestP7SettingsRemainFrozen(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")

	cases := []struct {
		key      string
		category string
		typ      settings.Type
		def      string
		secret   bool
		integer  bool
		min      *float64
		max      *float64
	}{
		{"report.section_appendix", "report", settings.TypeBool, "true", false, false, nil, nil},
		{"report.render_max_attempts", "report", settings.TypeNumber, "3", false, true, floatPtr(1), floatPtr(10)},
		{"report.render_lease_seconds", "report", settings.TypeNumber, "120", false, true, floatPtr(30), floatPtr(1800)},
		{"report.render_worker_url", "report", settings.TypeString, `"http://127.0.0.1:13337"`, false, false, nil, nil},
		{"report.render_token", "report", settings.TypeString, `"eztest-dev-render-token"`, true, false, nil, nil},
		{"knowledge.injection_max_items", "knowledge", settings.TypeNumber, "5", false, true, floatPtr(0), floatPtr(50)},
		{"retention.gc_interval_minutes", "retention", settings.TypeNumber, "1440", false, true, floatPtr(5), floatPtr(10080)},
		{"retention.gc_batch_size", "retention", settings.TypeNumber, "500", false, true, floatPtr(1), floatPtr(5000)},
		{"instance.heartbeat_seconds", "instance", settings.TypeNumber, "10", false, true, floatPtr(1), floatPtr(60)},
		{"instance.offline_seconds", "instance", settings.TypeNumber, "30", false, true, floatPtr(5), floatPtr(300)},
		{"execution.executor_urls", "execution", settings.TypeString, `""`, false, false, nil, nil},
	}
	for _, tc := range cases {
		def := defs[indexOf(t, defs, tc.key)]
		if def.Type != tc.typ || string(def.Default) != tc.def || def.Secret != tc.secret || def.Integer != tc.integer {
			t.Fatalf("%s = %+v, want type=%s default=%s secret=%v integer=%v", tc.key, def, tc.typ, tc.def, tc.secret, tc.integer)
		}
		if def.Category != tc.category {
			t.Fatalf("%s category = %q, want %q", tc.key, def.Category, tc.category)
		}
		if def.RestartRequired {
			t.Fatalf("%s must be hot-reloadable (got RestartRequired)", tc.key)
		}
		if !floatPtrEqual(def.Min, tc.min) || !floatPtrEqual(def.Max, tc.max) {
			t.Fatalf("%s range = %v..%v, want %v..%v", tc.key, def.Min, def.Max, tc.min, tc.max)
		}
	}

	templateVersion := defs[indexOf(t, defs, "report.template_version")]
	if string(templateVersion.Default) != `"2"` {
		t.Fatalf("report.template_version default = %s, want \"2\"", templateVersion.Default)
	}
}

// TestP8SettingsPinsCatalog pins the two P8 keys (spec §7: 83 -> 85).
// Both are hot-reloadable and non-secret; the P7 defaults stay unchanged.
func TestP8SettingsPinsCatalog(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	if len(defs) != 85 {
		t.Fatalf("catalog has %d keys, want 85 (P8: 83 -> 85)", len(defs))
	}

	cases := []struct {
		key      string
		category string
		typ      settings.Type
		def      string
		secret   bool
		integer  bool
		min      *float64
		max      *float64
	}{
		{"security.rate_limit_per_minute", "security", settings.TypeNumber, "0", false, true, floatPtr(0), floatPtr(60000)},
		{"retention.event_days", "retention", settings.TypeNumber, "90", false, true, floatPtr(1), floatPtr(3650)},
	}
	for _, tc := range cases {
		def := defs[indexOf(t, defs, tc.key)]
		if def.Type != tc.typ || string(def.Default) != tc.def || def.Secret != tc.secret || def.Integer != tc.integer {
			t.Fatalf("%s = %+v, want type=%s default=%s secret=%v integer=%v", tc.key, def, tc.typ, tc.def, tc.secret, tc.integer)
		}
		if def.Category != tc.category {
			t.Fatalf("%s category = %q, want %q", tc.key, def.Category, tc.category)
		}
		if def.RestartRequired {
			t.Fatalf("%s must be hot-reloadable (got RestartRequired)", tc.key)
		}
		if !floatPtrEqual(def.Min, tc.min) || !floatPtrEqual(def.Max, tc.max) {
			t.Fatalf("%s range = %v..%v, want %v..%v", tc.key, def.Min, def.Max, tc.min, tc.max)
		}
	}

	// P7 keys and defaults remain untouched.
	if def := defs[indexOf(t, defs, "security.audit_retention_days")]; string(def.Default) != "90" {
		t.Fatalf("security.audit_retention_days = %s, want 90", def.Default)
	}
	if def := defs[indexOf(t, defs, "report.template_version")]; string(def.Default) != `"2"` {
		t.Fatalf("report.template_version = %s, want \"2\"", def.Default)
	}

	counts := map[string]int{}
	for _, def := range defs {
		counts[def.Category]++
	}
	wantCounts := map[string]int{
		"report":    12,
		"execution": 12,
		"knowledge": 3,
		"retention": 8, // 7 P7 keys + retention.event_days
		"security":  3, // 2 P0 keys + security.rate_limit_per_minute
		"instance":  2,
	}
	for category, want := range wantCounts {
		if counts[category] != want {
			t.Fatalf("%s settings = %d, want %d", category, counts[category], want)
		}
	}
}
