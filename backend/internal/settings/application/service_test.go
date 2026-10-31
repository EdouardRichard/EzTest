package settingsapp

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
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
	if len(defs) < 40 {
		t.Fatalf("catalog has %d keys, want >= 40", len(defs))
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
