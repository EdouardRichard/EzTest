//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepostgres "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestMetadataRepositoryInsertIsInsertOnly proves the storage_object
// repository inserts once per sha256, reports whether a row was newly created,
// and never updates an existing row.
func TestMetadataRepositoryInsertIsInsertOnly(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := storagepostgres.NewMetadataRepository(pool)

	sha := strings.Repeat("a", 64)
	first := storageapp.ObjectMetadata{
		SHA256:       sha,
		Size:         42,
		ContentType:  "text/plain",
		RelativePath: "aa/aa/" + sha,
	}
	created, err := repo.Insert(ctx, first)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !created {
		t.Fatal("first insert did not report a new row")
	}

	// A conflicting insert is a no-op: it must return created=false and leave
	// the original row untouched.
	created, err = repo.Insert(ctx, storageapp.ObjectMetadata{
		SHA256:       sha,
		Size:         999,
		ContentType:  "application/octet-stream",
		RelativePath: "bogus/path",
	})
	if err != nil {
		t.Fatalf("conflicting insert: %v", err)
	}
	if created {
		t.Fatal("conflicting insert reported a new row")
	}

	stored, ok, err := repo.Find(ctx, sha)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok {
		t.Fatal("row not found after insert")
	}
	if stored.SHA256 != first.SHA256 || stored.Size != first.Size ||
		stored.ContentType != first.ContentType || stored.RelativePath != first.RelativePath {
		t.Fatalf("stored row=%+v want %+v", stored, first)
	}
	if stored.CreatedAt.IsZero() {
		t.Fatal("created_at was not set")
	}

	// An unknown hash is reported as missing, not as an error.
	missing, ok, err := repo.Find(ctx, strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("find missing: %v", err)
	}
	if ok || missing != (storageapp.ObjectMetadata{}) {
		t.Fatalf("missing find=%+v ok=%v", missing, ok)
	}
}
