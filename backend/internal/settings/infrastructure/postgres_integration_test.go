//go:build integration

package settingspg_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func TestRepositorySeedUpdateAndRevisions(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := settingspg.New(pool)
	ctx := context.Background()
	defs := settingsapp.Definitions("http://127.0.0.1:13336")

	if err := repo.Seed(ctx, defs); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if err := repo.Seed(ctx, defs); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != len(defs) {
		t.Fatalf("settings = %d, want %d", len(list), len(defs))
	}

	initial, err := repo.Get(ctx, "execution.global_concurrency")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(initial.Value) != "3" || initial.Version != 1 {
		t.Fatalf("initial = %#v", initial)
	}

	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	updated, err := repo.Update(ctx, "execution.global_concurrency", json.RawMessage("5"), 9001, 42, at)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if string(updated.Value) != "5" || updated.Version != 2 {
		t.Fatalf("updated = %#v", updated)
	}
	if updated.UpdatedBy == nil || *updated.UpdatedBy != 42 {
		t.Fatalf("updated_by = %v", updated.UpdatedBy)
	}

	revisions, err := repo.Revisions(ctx, "execution.global_concurrency", 10, nil)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("revisions = %#v", revisions)
	}
	if string(revisions[0].OldValue) != "3" || string(revisions[0].NewValue) != "5" {
		t.Fatalf("revision = %#v", revisions[0])
	}

	// Seeding after a user update must not overwrite the value.
	if err := repo.Seed(ctx, defs); err != nil {
		t.Fatalf("third Seed: %v", err)
	}
	after, err := repo.Get(ctx, "execution.global_concurrency")
	if err != nil {
		t.Fatalf("Get after seed: %v", err)
	}
	if string(after.Value) != "5" {
		t.Fatalf("seed overwrote value: %#v", after)
	}

	if _, err := repo.Get(ctx, "missing.key"); !errors.Is(err, settings.ErrNotFound) {
		t.Fatalf("missing get error = %v", err)
	}
}
