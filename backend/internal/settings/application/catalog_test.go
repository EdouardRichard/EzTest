package settingsapp

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

// TestP7SettingsRemainFrozen pins the eleven P7 keys and the report template
// version default "2" for new databases. The catalog grew to 85 in P8 and to
// 98 in v0.22 but the P7 keys/defaults must not change.
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

// TestP8SettingsPinsCatalog pins the two P8 keys (spec §7: 83 -> 85) and the
// two P8 category counts. Both are hot-reloadable and non-secret; the P7
// defaults stay unchanged. The v0.22 additions grow the catalog to 98.
func TestP8SettingsPinsCatalog(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	if len(defs) != 98 {
		t.Fatalf("catalog has %d keys, want 98 (P8: 83 -> 85, v0.22: 85 -> 98)", len(defs))
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

// TestV022Settings pins the v0.22 settings catalog (spec §8.1 ST-1..ST-19):
// the raised context window default, the new compaction/vision/tool/question/gc
// keys, and the already-existing agent.tool_result_max_bytes unchanged at
// 32768. The catalog grows from 85 to 98 keys; the legacy keys stay in place
// until W1 disposes of the dead ones, and question.blocking must not exist.
func TestV022Settings(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	if len(defs) != 98 {
		t.Fatalf("catalog has %d keys, want 98 (v0.22: 85 -> 98)", len(defs))
	}

	byKey := make(map[string]settings.Definition, len(defs))
	for _, def := range defs {
		if _, ok := byKey[def.Key]; ok {
			t.Fatalf("duplicate key %q", def.Key)
		}
		byKey[def.Key] = def
	}

	cases := []struct {
		key      string
		category string
		typ      settings.Type
		def      string
		integer  bool
		min      *float64
		max      *float64
	}{
		{"context.window_tokens", "context", settings.TypeNumber, "128000", true, floatPtr(1024), nil},
		{"compaction.threshold_ratio", "compaction", settings.TypeNumber, "0.8", false, floatPtr(0.5), floatPtr(0.9)},
		{"compaction.retain_ratio", "compaction", settings.TypeNumber, "0.16", false, floatPtr(0.05), floatPtr(0.5)},
		{"compaction.summary_max_tokens", "compaction", settings.TypeNumber, "8192", true, floatPtr(256), floatPtr(8192)},
		{"compaction.overflow_retries", "compaction", settings.TypeNumber, "1", true, floatPtr(0), floatPtr(5)},
		{"agent.tool_result_max_bytes", "agent", settings.TypeNumber, "32768", true, floatPtr(1024), nil},
		{"vision.required", "vision", settings.TypeBool, "true", false, nil, nil},
		{"vision.max_images_per_attachment", "vision", settings.TypeNumber, "50", true, floatPtr(1), floatPtr(500)},
		{"vision.max_image_bytes", "vision", settings.TypeNumber, "10485760", true, floatPtr(1024), floatPtr(104857600)},
		{"tool.retry_max", "tool", settings.TypeNumber, "2", true, floatPtr(0), floatPtr(5)},
		{"question.timeout_seconds", "question", settings.TypeNumber, "86400", true, floatPtr(60), floatPtr(604800)},
		{"gc.retention_days", "gc", settings.TypeNumber, "30", true, floatPtr(1), floatPtr(3650)},
		{"gc.max_bytes", "gc", settings.TypeNumber, "0", true, floatPtr(0), nil},
		{"gc.dry_run", "gc", settings.TypeBool, "true", false, nil, nil},
		{"gc.grace_days", "gc", settings.TypeNumber, "7", true, floatPtr(0), floatPtr(365)},
	}
	for _, tc := range cases {
		def, ok := byKey[tc.key]
		if !ok {
			t.Fatalf("%s missing from catalog", tc.key)
		}
		if def.Category != tc.category || def.Type != tc.typ || string(def.Default) != tc.def || def.Integer != tc.integer {
			t.Fatalf("%s = %+v, want category=%s type=%s default=%s integer=%v", tc.key, def, tc.category, tc.typ, tc.def, tc.integer)
		}
		if def.Secret || def.RestartRequired {
			t.Fatalf("%s must be hot-reloadable and non-secret (got secret=%v restart=%v)", tc.key, def.Secret, def.RestartRequired)
		}
		if !floatPtrEqual(def.Min, tc.min) || !floatPtrEqual(def.Max, tc.max) {
			t.Fatalf("%s range = %v..%v, want %v..%v", tc.key, def.Min, def.Max, tc.min, tc.max)
		}
	}

	if _, ok := byKey["question.blocking"]; ok {
		t.Fatal("question.blocking must not exist (questions never block, C-2)")
	}
}
