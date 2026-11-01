package settingsapp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

type seqIDs struct{ next int64 }

func (s *seqIDs) Next() int64 { s.next++; return s.next }

type fakeRepo struct {
	items     map[string]settings.Setting
	revisions []settings.Revision
	seedCalls int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{items: map[string]settings.Setting{}}
}

func (f *fakeRepo) List(context.Context) ([]settings.Setting, error) {
	list := make([]settings.Setting, 0, len(f.items))
	for _, item := range f.items {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return list, nil
}

func (f *fakeRepo) Get(_ context.Context, key string) (settings.Setting, error) {
	item, ok := f.items[key]
	if !ok {
		return settings.Setting{}, settings.ErrNotFound
	}
	return item, nil
}

func (f *fakeRepo) Seed(_ context.Context, definitions []settings.Definition) error {
	f.seedCalls++
	for _, def := range definitions {
		if _, ok := f.items[def.Key]; ok {
			continue
		}
		f.items[def.Key] = settings.Setting{
			Key: def.Key, Value: def.Default, Category: def.Category, Type: def.Type,
			Secret: def.Secret, RestartRequired: def.RestartRequired,
			Description: def.Description, Version: 1, UpdatedAt: time.Unix(0, 0).UTC(),
		}
	}
	return nil
}

func (f *fakeRepo) Update(_ context.Context, key string, value json.RawMessage, revisionID, actorID int64, at time.Time) (settings.Setting, error) {
	item, ok := f.items[key]
	if !ok {
		return settings.Setting{}, settings.ErrNotFound
	}
	f.revisions = append(f.revisions, settings.Revision{
		ID: revisionID, Key: key, OldValue: item.Value, NewValue: value,
		ActorID: &actorID, CreatedAt: at,
	})
	item.Value = value
	item.Version++
	item.UpdatedBy = &actorID
	item.UpdatedAt = at
	f.items[key] = item
	return item, nil
}

func (f *fakeRepo) Revisions(_ context.Context, key string, limit int, beforeID *int64) ([]settings.Revision, error) {
	result := make([]settings.Revision, 0)
	for i := len(f.revisions) - 1; i >= 0; i-- {
		revision := f.revisions[i]
		if revision.Key != key {
			continue
		}
		if beforeID != nil && revision.ID >= *beforeID {
			continue
		}
		result = append(result, revision)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

type fakeAudit struct{ events []auditapp.Event }

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

type fakePublisher struct{ events []eventingapp.Event }

func (f *fakePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

func newTestService(t *testing.T, repo *fakeRepo) (*Service, *fakeAudit, *fakePublisher) {
	t.Helper()
	audit := &fakeAudit{}
	publisher := &fakePublisher{}
	svc := New(
		repo,
		Definitions("http://127.0.0.1:13336"),
		&seqIDs{},
		fixedClock{at: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)},
		audit,
		publisher,
	)
	return svc, audit, publisher
}

func TestDefinitionsAreUniqueAndWellFormed(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	if len(defs) != 72 {
		t.Fatalf("catalog has %d keys, want 72 (P6: 69 -> 72)", len(defs))
	}
	seen := map[string]bool{}
	for _, def := range defs {
		if seen[def.Key] {
			t.Fatalf("duplicate key %q", def.Key)
		}
		seen[def.Key] = true
		if def.Category == "" || def.Description == "" {
			t.Fatalf("definition %q is missing category/description", def.Key)
		}
		switch def.Type {
		case settings.TypeString, settings.TypeNumber, settings.TypeBool, settings.TypeJSON:
		default:
			t.Fatalf("definition %q has invalid type %q", def.Key, def.Type)
		}
		if len(def.Default) == 0 {
			t.Fatalf("definition %q has no default", def.Key)
		}
	}
	if !seen["execution.executor_url"] || !seen["runtime.snowflake_node_id"] {
		t.Fatal("catalog is missing required keys")
	}
	if got := string(defs[indexOf(t, defs, "execution.executor_url")].Default); got != `"http://127.0.0.1:13336"` {
		t.Fatalf("executor default = %s", got)
	}
}

// TestP3ExecutionSettings pins the five P3 keys (spec §11.2, P3-D-19): types,
// defaults, ranges and the first secret-semantics setting.
func TestP3ExecutionSettings(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	cases := []struct {
		key     string
		typ     settings.Type
		def     string
		secret  bool
		min     *float64
		max     *float64
		integer bool
	}{
		{"execution.executor_token", settings.TypeString, `"eztest-dev-executor-token"`, true, nil, nil, false},
		{"execution.executor_heartbeat_seconds", settings.TypeNumber, "10", false, floatPtr(1), floatPtr(60), true},
		{"execution.executor_offline_seconds", settings.TypeNumber, "30", false, floatPtr(5), floatPtr(300), true},
		{"execution.executor_context_ttl_seconds", settings.TypeNumber, "300", false, floatPtr(30), floatPtr(3600), true},
		{"execution.human_request_timeout_seconds", settings.TypeNumber, "1800", false, floatPtr(60), floatPtr(86400), true},
	}
	for _, tc := range cases {
		def := defs[indexOf(t, defs, tc.key)]
		if def.Type != tc.typ || string(def.Default) != tc.def || def.Secret != tc.secret || def.Integer != tc.integer {
			t.Fatalf("%s = %+v, want type=%s default=%s secret=%v integer=%v", tc.key, def, tc.typ, tc.def, tc.secret, tc.integer)
		}
		if def.Category != "execution" {
			t.Fatalf("%s category = %q", tc.key, def.Category)
		}
		if !floatPtrEqual(def.Min, tc.min) || !floatPtrEqual(def.Max, tc.max) {
			t.Fatalf("%s range = %v..%v, want %v..%v", tc.key, def.Min, def.Max, tc.min, tc.max)
		}
	}

	executionKeys := 0
	for _, def := range defs {
		if def.Category == "execution" {
			executionKeys++
		}
	}
	if executionKeys != 11 {
		t.Fatalf("execution settings = %d, want 11 (6 frozen + 5 P3)", executionKeys)
	}
}

// TestP4ChainSettings pins the four P4 chain keys (spec §4.5/settings.md
// §4d): number, non-secret, integer and the configured range/default.
func TestP4ChainSettings(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	cases := []struct {
		key string
		def string
		min *float64
		max *float64
	}{
		{"chain.max_nodes", "300", floatPtr(1), floatPtr(1000)},
		{"chain.max_edges", "600", floatPtr(1), floatPtr(2000)},
		{"chain.max_requirements", "100", floatPtr(1), floatPtr(500)},
		{"chain.replay_event_limit", "500", floatPtr(1), floatPtr(2000)},
	}
	for _, tc := range cases {
		def := defs[indexOf(t, defs, tc.key)]
		if def.Type != settings.TypeNumber || def.Category != "chain" || def.Secret || !def.Integer {
			t.Fatalf("%s = %+v, want number/chain/non-secret/integer", tc.key, def)
		}
		if string(def.Default) != tc.def || !floatPtrEqual(def.Min, tc.min) || !floatPtrEqual(def.Max, tc.max) {
			t.Fatalf("%s = default %s range %v..%v, want %s %v..%v", tc.key, def.Default, def.Min, def.Max, tc.def, tc.min, tc.max)
		}
	}
	chainKeys := 0
	for _, def := range defs {
		if def.Category == "chain" {
			chainKeys++
		}
	}
	if chainKeys != 4 {
		t.Fatalf("chain settings = %d, want 4", chainKeys)
	}
}

// TestP5CodeintelVisualSettings pins the 16 P5 keys (spec §4.5): nine
// codeintel.* and seven visual.* settings, plus the JSON-array exclude globs.
// Every key is non-secret and only lowers the hard domain limits.
func TestP5CodeintelVisualSettings(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	cases := []struct {
		key     string
		typ     settings.Type
		def     string
		integer bool
		min     *float64
		max     *float64
	}{
		{"codeintel.max_index_files", settings.TypeNumber, "5000", true, floatPtr(1), floatPtr(50000)},
		{"codeintel.max_changed_files", settings.TypeNumber, "20", true, floatPtr(1), floatPtr(200)},
		{"codeintel.max_file_bytes", settings.TypeNumber, "65536", true, floatPtr(1024), floatPtr(1048576)},
		{"codeintel.max_diff_bytes", settings.TypeNumber, "262144", true, floatPtr(4096), floatPtr(1048576)},
		{"codeintel.max_excerpt_lines", settings.TypeNumber, "40", true, floatPtr(1), floatPtr(200)},
		{"codeintel.analysis_timeout_seconds", settings.TypeNumber, "180", true, floatPtr(10), floatPtr(1800)},
		{"codeintel.max_concurrent_analyses", settings.TypeNumber, "1", true, floatPtr(1), floatPtr(4)},
		{"codeintel.prompt_version", settings.TypeString, `"p5.1"`, false, nil, nil},
		{"visual.default_threshold", settings.TypeNumber, "0.005", false, floatPtr(0), floatPtr(0.1)},
		{"visual.baseline_max_bytes", settings.TypeNumber, "5242880", true, floatPtr(65536), floatPtr(20971520)},
		{"visual.default_viewport_width", settings.TypeNumber, "1280", true, floatPtr(1), floatPtr(10000)},
		{"visual.default_viewport_height", settings.TypeNumber, "720", true, floatPtr(1), floatPtr(10000)},
		{"visual.default_device_scale_factor", settings.TypeNumber, "1", false, floatPtr(0.5), floatPtr(4)},
		{"visual.compare_timeout_seconds", settings.TypeNumber, "60", true, floatPtr(5), floatPtr(300)},
		{"visual.wait_timeout_seconds", settings.TypeNumber, "15", true, floatPtr(1), floatPtr(120)},
	}
	for _, tc := range cases {
		def := defs[indexOf(t, defs, tc.key)]
		wantCategory := "codeintel"
		if strings.HasPrefix(tc.key, "visual.") {
			wantCategory = "visual"
		}
		if def.Type != tc.typ || string(def.Default) != tc.def || def.Secret || def.Integer != tc.integer {
			t.Fatalf("%s = %+v, want type=%s default=%s non-secret integer=%v", tc.key, def, tc.typ, tc.def, tc.integer)
		}
		if def.Category != wantCategory {
			t.Fatalf("%s category = %q, want %q", tc.key, def.Category, wantCategory)
		}
		if !floatPtrEqual(def.Min, tc.min) || !floatPtrEqual(def.Max, tc.max) {
			t.Fatalf("%s range = %v..%v, want %v..%v", tc.key, def.Min, def.Max, tc.min, tc.max)
		}
	}

	globs := defs[indexOf(t, defs, "codeintel.exclude_globs")]
	if globs.Type != settings.TypeJSON || globs.Category != "codeintel" || globs.Secret {
		t.Fatalf("codeintel.exclude_globs = %+v, want non-secret json in codeintel", globs)
	}
	var patterns []string
	if err := json.Unmarshal(globs.Default, &patterns); err != nil {
		t.Fatalf("codeintel.exclude_globs default is not a JSON string array: %v", err)
	}
	wantPatterns := []string{"**/.git/**", "**/node_modules/**", "**/dist/**", "**/build/**", "**/vendor/**"}
	if !slices.Equal(patterns, wantPatterns) {
		t.Fatalf("codeintel.exclude_globs = %v, want %v", patterns, wantPatterns)
	}

	counts := map[string]int{}
	for _, def := range defs {
		counts[def.Category]++
	}
	if counts["codeintel"] != 9 {
		t.Fatalf("codeintel settings = %d, want 9", counts["codeintel"])
	}
	if counts["visual"] != 7 {
		t.Fatalf("visual settings = %d, want 7", counts["visual"])
	}
}

// TestP6SearchTodosQualitySettings pins the three P6 keys (spec §4.5):
// number, non-secret, hot-reloadable (no restart) and their defaults/ranges.
func TestP6SearchTodosQualitySettings(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	cases := []struct {
		key      string
		category string
		def      string
		integer  bool
		min      *float64
		max      *float64
	}{
		{"search.max_results_per_type", "search", "5", true, floatPtr(1), floatPtr(20)},
		{"todos.max_items", "workspace", "50", true, floatPtr(1), floatPtr(200)},
		{"quality.eval_baseline_regression", "quality", "0.05", false, floatPtr(0), floatPtr(1)},
	}
	for _, tc := range cases {
		def := defs[indexOf(t, defs, tc.key)]
		if def.Type != settings.TypeNumber || def.Category != tc.category || def.Secret || def.RestartRequired || def.Integer != tc.integer {
			t.Fatalf("%s = %+v, want number/%s/non-secret/hot-reload/integer=%v", tc.key, def, tc.category, tc.integer)
		}
		if string(def.Default) != tc.def || !floatPtrEqual(def.Min, tc.min) || !floatPtrEqual(def.Max, tc.max) {
			t.Fatalf("%s = default %s range %v..%v, want %s %v..%v", tc.key, def.Default, def.Min, def.Max, tc.def, tc.min, tc.max)
		}
	}
}

func floatPtr(value float64) *float64 { return &value }

func floatPtrEqual(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func indexOf(t *testing.T, defs []settings.Definition, key string) int {
	t.Helper()
	for i, def := range defs {
		if def.Key == key {
			return i
		}
	}
	t.Fatalf("key %s not found", key)
	return -1
}

func TestSeedDefaultsIsIdempotentAndPreservesValues(t *testing.T) {
	repo := newFakeRepo()
	svc, _, _ := newTestService(t, repo)
	ctx := context.Background()

	if err := svc.SeedDefaults(ctx); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	if repo.seedCalls != 1 {
		t.Fatalf("seed calls = %d", repo.seedCalls)
	}
	first, err := svc.Int(ctx, "execution.global_concurrency")
	if err != nil || first != 3 {
		t.Fatalf("concurrency = %d, %v", first, err)
	}

	// A prior user value must survive a restart seed.
	repo.items["execution.global_concurrency"] = settings.Setting{
		Key: "execution.global_concurrency", Value: json.RawMessage("7"),
		Category: "execution", Type: settings.TypeNumber,
	}
	if err := svc.SeedDefaults(ctx); err != nil {
		t.Fatalf("second SeedDefaults: %v", err)
	}
	second, err := svc.Int(ctx, "execution.global_concurrency")
	if err != nil || second != 7 {
		t.Fatalf("second concurrency = %d, %v", second, err)
	}
}

func TestUpdateWritesRevisionAndHotReloads(t *testing.T) {
	repo := newFakeRepo()
	svc, audit, publisher := newTestService(t, repo)
	ctx := context.Background()
	if err := svc.SeedDefaults(ctx); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	updated, err := svc.Update(ctx, "execution.global_concurrency", 5, 42)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Version != 2 || updated.UpdatedBy == nil || *updated.UpdatedBy != 42 {
		t.Fatalf("updated = %#v", updated)
	}
	value, err := svc.Int(ctx, "execution.global_concurrency")
	if err != nil || value != 5 {
		t.Fatalf("hot value = %d, %v", value, err)
	}
	revisions, err := svc.Revisions(ctx, "execution.global_concurrency", 10, nil)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("revisions = %#v, %v", revisions, err)
	}
	if string(revisions[0].OldValue) != "3" || string(revisions[0].NewValue) != "5" {
		t.Fatalf("revision = %#v", revisions[0])
	}
	if len(audit.events) != 1 || audit.events[0].Action != "settings.updated" {
		t.Fatalf("audit = %#v", audit.events)
	}
	if len(publisher.events) != 1 || publisher.events[0].Channel != "system" || publisher.events[0].Type != "settings.updated" {
		t.Fatalf("events = %#v", publisher.events)
	}
	if _, ok := publisher.events[0].Payload["value"]; ok {
		t.Fatal("settings event leaked the value")
	}
}

func TestUpdateValidatesTypeAndRange(t *testing.T) {
	repo := newFakeRepo()
	svc, _, _ := newTestService(t, repo)
	ctx := context.Background()
	if err := svc.SeedDefaults(ctx); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	cases := []struct {
		key   string
		value any
	}{
		{"execution.global_concurrency", "abc"},
		{"execution.global_concurrency", 0},
		{"execution.global_concurrency", 33},
		{"execution.global_concurrency", 1.5},
		{"retention.gc_dry_run", "yes"},
		{"context.compact_threshold", 0.95},
		{"context.compact_threshold", 0.2},
		{"execution.executor_heartbeat_seconds", 0},
		{"execution.executor_heartbeat_seconds", 61},
		{"execution.executor_offline_seconds", 4},
		{"execution.executor_context_ttl_seconds", 3601},
		{"execution.human_request_timeout_seconds", 59},
		{"execution.executor_token", 123},
		{"codeintel.max_changed_files", 0},
		{"codeintel.max_changed_files", 201},
		{"codeintel.max_concurrent_analyses", 5},
		{"codeintel.prompt_version", 1},
		{"visual.default_threshold", 0.11},
		{"visual.default_device_scale_factor", 0.4},
		{"visual.compare_timeout_seconds", 4},
	}
	for _, tc := range cases {
		if _, err := svc.Update(ctx, tc.key, tc.value, 1); err == nil {
			t.Fatalf("Update(%s, %v) accepted", tc.key, tc.value)
		}
	}

	if _, err := svc.Update(ctx, "does.not.exist", 1, 1); !isNotFound(err) {
		t.Fatalf("unknown key error = %v, want not found", err)
	}
	if _, err := svc.Update(ctx, "context.compact_threshold", 0.75, 1); err != nil {
		t.Fatalf("valid float rejected: %v", err)
	}
	token, err := svc.Update(ctx, "execution.executor_token", "rotated-token", 1)
	if err != nil {
		t.Fatalf("valid secret token rejected: %v", err)
	}
	if !token.Secret {
		t.Fatal("executor token must stay a secret setting")
	}
	if _, err := svc.Update(ctx, "codeintel.exclude_globs", []string{"**/vendor/**"}, 1); err != nil {
		t.Fatalf("JSON array exclude_globs rejected: %v", err)
	}
}

func isNotFound(err error) bool {
	var apiErr *platform.Error
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

func TestTypedGetters(t *testing.T) {
	repo := newFakeRepo()
	svc, _, _ := newTestService(t, repo)
	ctx := context.Background()
	if err := svc.SeedDefaults(ctx); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	path, err := svc.String(ctx, "paths.data_root")
	if err != nil || path != "./.data" {
		t.Fatalf("path = %q, %v", path, err)
	}
	dryRun, err := svc.Bool(ctx, "retention.gc_dry_run")
	if err != nil || !dryRun {
		t.Fatalf("dry run = %v, %v", dryRun, err)
	}
	threshold, err := svc.Float(ctx, "context.compact_threshold")
	if err != nil || threshold != 0.7 {
		t.Fatalf("threshold = %v, %v", threshold, err)
	}
	concurrency, err := svc.Float(ctx, "execution.global_concurrency")
	if err != nil || concurrency != 3 {
		t.Fatalf("widened integer = %v, %v", concurrency, err)
	}
	if _, err := svc.String(ctx, "execution.global_concurrency"); err == nil {
		t.Fatal("String() accepted a number setting")
	}
	if _, err := svc.Float(ctx, "paths.data_root"); err == nil {
		t.Fatal("Float() accepted a string setting")
	}
}

func TestRevisionsRejectsUnknownKey(t *testing.T) {
	repo := newFakeRepo()
	svc, _, _ := newTestService(t, repo)
	if _, err := svc.Revisions(context.Background(), "nope", 10, nil); !isNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
}
