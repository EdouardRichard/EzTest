//go:build integration

package storagepg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func testArtifact(id, revision int64, format string) storage.ExportArtifact {
	createdBy := int64(1)
	hash := strings.Repeat("a", 64)
	if format == storage.FormatCSV {
		hash = strings.Repeat("b", 64)
	}
	return storage.ExportArtifact{
		ID: id, VersionID: 1001, OwnerID: 1001, OwnerKind: storage.OwnerKindCase,
		Revision: int(revision), Format: format, ContentObjectID: hash,
		Size: int64(revision), MIME: "text/plain; charset=utf-8",
		Filename: "case-export-" + format, Status: storage.ExportStatusReady,
		CreatedBy: &createdBy, CreatedAt: time.Now().UTC(),
	}
}

func TestExportRepositoryCRUD(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := storagepg.NewExportRepository(pool)
	ctx := context.Background()

	first, err := repo.Create(ctx, testArtifact(7001, 1, storage.FormatMarkdown))
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	if first.ID != 7001 || first.Revision != 1 {
		t.Fatalf("first = %+v", first)
	}
	second, err := repo.Create(ctx, testArtifact(7002, 2, storage.FormatMarkdown))
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if second.Revision != 2 {
		t.Fatalf("second revision = %d", second.Revision)
	}
	if _, err := repo.Create(ctx, testArtifact(7003, 1, storage.FormatCSV)); err != nil {
		t.Fatalf("Create csv: %v", err)
	}
	// Unique (version, owner_kind, owner_id, format, revision) is enforced.
	duplicate := testArtifact(7004, 1, storage.FormatMarkdown)
	if _, err := repo.Create(ctx, duplicate); !errors.Is(err, storage.ErrDuplicate) {
		t.Fatalf("duplicate create err = %v, want ErrDuplicate", err)
	}

	loaded, err := repo.Get(ctx, 7001)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.ContentObjectID != first.ContentObjectID || loaded.Filename != first.Filename {
		t.Fatalf("loaded = %+v", loaded)
	}
	if _, err := repo.Get(ctx, 999999); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing Get err = %v, want ErrNotFound", err)
	}

	maxRevision, err := repo.MaxRevision(ctx, 1001, storage.OwnerKindCase, 1001, storage.FormatMarkdown)
	if err != nil {
		t.Fatalf("MaxRevision: %v", err)
	}
	if maxRevision != 2 {
		t.Fatalf("max revision = %d", maxRevision)
	}

	all, err := repo.List(ctx, 1001, "", 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all = %d", len(all))
	}
	if all[0].ID != 7003 || all[2].ID != 7001 {
		t.Fatalf("list order = %+v", all)
	}
	markdown, err := repo.List(ctx, 1001, storage.FormatMarkdown, 10, nil)
	if err != nil {
		t.Fatalf("List format: %v", err)
	}
	if len(markdown) != 2 {
		t.Fatalf("markdown = %d", len(markdown))
	}
	before := all[1].ID
	page, err := repo.List(ctx, 1001, "", 10, &before)
	if err != nil {
		t.Fatalf("List cursor: %v", err)
	}
	if len(page) != 1 || page[0].ID != 7001 {
		t.Fatalf("cursor page = %+v", page)
	}
}
