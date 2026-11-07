package settingsapp

import (
	"strings"
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
// defaults stay unchanged. The v0.22 additions grow the catalog to 98, W1-2
// removes seven dead keys (91), and W2-15 removes the four gc.* aliases (87).
func TestP8SettingsPinsCatalog(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	if len(defs) != 87 {
		t.Fatalf("catalog has %d keys, want 87 (P8: 83 -> 85, v0.22: 85 -> 98, W1-2 removes 7 dead keys, W2-15 removes 4 gc.* aliases)", len(defs))
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
		"security":  2, // audit_retention_days + rate_limit_per_minute (W1-2 removed mask_secrets)
		"instance":  2,
	}
	for category, want := range wantCounts {
		if counts[category] != want {
			t.Fatalf("%s settings = %d, want %d", category, counts[category], want)
		}
	}
}

// TestV022Settings pins the v0.22 settings catalog (spec §8.1 ST-1..ST-19):
// the raised context window default, the new compaction/vision/tool/question
// keys, and the already-existing agent.tool_result_max_bytes unchanged at
// 32768. The v0.22 additions grew the catalog from 85 to 98 keys; W1-2 removes
// seven dead keys (91), W2-15 removes the four gc.* aliases (87), and
// question.blocking must not exist.
func TestV022Settings(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	if len(defs) != 87 {
		t.Fatalf("catalog has %d keys, want 87 (v0.22: 85 -> 98, W1-2 removes 7 dead keys, W2-15 removes 4 gc.* aliases)", len(defs))
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
		{"vision.max_images_per_attachment", "vision", settings.TypeNumber, "50", true, floatPtr(1), floatPtr(500)},
		{"vision.max_image_bytes", "vision", settings.TypeNumber, "10485760", true, floatPtr(1024), floatPtr(104857600)},
		{"tool.retry_max", "tool", settings.TypeNumber, "2", true, floatPtr(0), floatPtr(5)},
		{"question.timeout_seconds", "question", settings.TypeNumber, "86400", true, floatPtr(60), floatPtr(604800)},
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

// TestDeadSettingsDisposed pins the W1-2 disposition of the 13 settings that
// had no production reader: seven dead keys are removed from the catalog and
// six are kept because their readers are wired by W1-2. W2-15 removes the four
// gc.* aliases so retention.* is the only GC settings family.
func TestDeadSettingsDisposed(t *testing.T) {
	byKey := make(map[string]bool, 87)
	for _, def := range Definitions("http://127.0.0.1:13336") {
		byKey[def.Key] = true
	}
	for _, removed := range []string{
		"paths.attachment_dir", "paths.evidence_dir", "paths.report_dir", "paths.export_dir",
		"security.mask_secrets", "feature.experimental", "vision.required",
		"gc.retention_days", "gc.max_bytes", "gc.dry_run", "gc.grace_days",
	} {
		if byKey[removed] {
			t.Fatalf("%s must be removed from the catalog", removed)
		}
	}
	for _, wired := range []string{
		"vision.max_images_per_attachment", "vision.max_image_bytes", "tool.retry_max",
		"compaction.overflow_retries", "search.max_results_per_type", "todos.max_items",
	} {
		if !byKey[wired] {
			t.Fatalf("%s must stay in the catalog (wired)", wired)
		}
	}
}

// TestW2GCKeysRemovedAndRetentionAuthoritative is the W2-15 catalog contract
// (spec R-W2-7, W2-C4, ST-16..ST-19): the four gc.* aliases are gone, there is
// no "gc" category at all, and the retention family carries the frozen GC
// defaults (unreferenced 30d, grace 168h, dry-run true, batch/interval kept).
func TestW2GCKeysRemovedAndRetentionAuthoritative(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	byKey := make(map[string]settings.Definition, len(defs))
	for _, def := range defs {
		if strings.HasPrefix(def.Key, "gc.") || def.Category == "gc" {
			t.Fatalf("%s must not remain in the catalog (retention.* is the only GC settings family)", def.Key)
		}
		byKey[def.Key] = def
	}
	for _, removed := range []string{"gc.retention_days", "gc.max_bytes", "gc.dry_run", "gc.grace_days"} {
		if _, ok := byKey[removed]; ok {
			t.Fatalf("%s must be removed from the catalog", removed)
		}
	}

	want := map[string]string{
		"retention.unreferenced_content_days": "30",
		"retention.gc_grace_hours":            "168",
		"retention.gc_dry_run":                "true",
		"retention.temp_hours":                "24",
		"retention.replaced_export_days":      "7",
		"retention.gc_batch_size":             "500",
		"retention.gc_interval_minutes":       "1440",
		"retention.event_days":                "90",
	}
	for key, defaultValue := range want {
		def, ok := byKey[key]
		if !ok {
			t.Fatalf("%s missing from the catalog", key)
		}
		if string(def.Default) != defaultValue {
			t.Fatalf("%s default = %s, want %s", key, def.Default, defaultValue)
		}
		if def.Category != "retention" {
			t.Fatalf("%s category = %q, want retention", key, def.Category)
		}
	}
}
