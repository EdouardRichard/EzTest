package settingsapp

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

// TestP7SettingsPinsCatalog pins the eleven P7 keys (spec §8: 72 -> 83) and
// the report template version default "2" for new databases. Every key is
// hot-reloadable; only the renderer Bearer token is secret.
func TestP7SettingsPinsCatalog(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	if len(defs) != 83 {
		t.Fatalf("catalog has %d keys, want 83 (P7: 72 -> 83)", len(defs))
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

	// P7-D-03: new databases default to the built-in template v2 (v1 stays
	// available to existing databases that keep their stored value).
	templateVersion := defs[indexOf(t, defs, "report.template_version")]
	if string(templateVersion.Default) != `"2"` {
		t.Fatalf("report.template_version default = %s, want \"2\"", templateVersion.Default)
	}

	counts := map[string]int{}
	for _, def := range defs {
		counts[def.Category]++
	}
	wantCounts := map[string]int{
		"report":    12, // 7 P4 keys + appendix + 4 renderer keys
		"execution": 12, // 11 P0-P3 keys + execution.executor_urls
		"knowledge": 3,  // 2 P0 keys + knowledge.injection_max_items
		"retention": 7,  // 5 P0 keys + gc_interval_minutes/gc_batch_size
		"instance":  2,  // heartbeat/offline
	}
	for category, want := range wantCounts {
		if counts[category] != want {
			t.Fatalf("%s settings = %d, want %d", category, counts[category], want)
		}
	}
}
