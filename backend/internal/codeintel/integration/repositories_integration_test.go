//go:build integration

package integration

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// Valid hashes reused by the repository fixtures.
const (
	ciBlobHashA = "git-sha1:0123456789abcdef0123456789abcdef01234567"
	ciBlobHashB = "git-sha1:fedcba9876543210fedcba9876543210fedcba98"
	ciExcerptA  = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ciExcerptB  = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// TestChangeSetGetOrCreate pins the deterministic change-set reuse: the same
// (repo, base, head) returns the first stored row and created=false, and the
// stored JSONB documents round-trip.
func TestChangeSetGetOrCreate(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, _, _ := seedRepoVersionRequirement(t, pool)
	repo := postgres.NewChangeSetRepository(pool)

	want := domain.CodeChangeSet{
		ID:         7001,
		RepoID:     repoID,
		BaseCommit: seedBaseCommit,
		HeadCommit: seedHeadCommit,
		Stats:      []byte(`{"files":1,"additions":2,"deletions":1,"truncated":false}`),
		Files: []domain.ChangedFile{{
			Path:         "src/api/refund.ts",
			Status:       "M",
			OldPath:      ciPtr("src/api/refund-old.ts"),
			Additions:    2,
			Deletions:    1,
			Binary:       false,
			HeadBlobHash: ciBlobHashA,
		}},
		Truncated: false,
	}
	stored, wasCreated, err := repo.GetOrCreate(ctx, want)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if !wasCreated {
		t.Fatal("first GetOrCreate created = false, want true")
	}
	if stored.ID != want.ID {
		t.Fatalf("stored id = %d, want %d", stored.ID, want.ID)
	}
	if stored.CreatedAt.IsZero() {
		t.Fatal("stored created_at is zero")
	}
	if !reflect.DeepEqual(stored.Files, want.Files) {
		t.Fatalf("stored files = %+v, want %+v", stored.Files, want.Files)
	}
	if stored.Truncated != want.Truncated {
		t.Fatalf("stored truncated = %v, want %v", stored.Truncated, want.Truncated)
	}
	ciAssertJSONEqual(t, stored.Stats, string(want.Stats))

	// The changed_files JSONB is a readable document with the domain field
	// names, not the Go struct names.
	var storedPath string
	if err := pool.QueryRow(ctx,
		`SELECT changed_files -> 0 ->> 'path' FROM code_change_sets WHERE id = $1`, want.ID,
	).Scan(&storedPath); err != nil {
		t.Fatalf("read changed_files path: %v", err)
	}
	if storedPath != want.Files[0].Path {
		t.Fatalf("changed_files[0].path = %q, want %q", storedPath, want.Files[0].Path)
	}

	// A second call for the same trio keeps the original row: DO NOTHING must
	// not overwrite the deterministic change set.
	second, wasCreated, err := repo.GetOrCreate(ctx, domain.CodeChangeSet{
		ID:         7002,
		RepoID:     repoID,
		BaseCommit: seedBaseCommit,
		HeadCommit: seedHeadCommit,
		Stats:      []byte(`{"files":99}`),
		Files:      []domain.ChangedFile{{Path: "src/other.ts", Status: "A"}},
	})
	if err != nil {
		t.Fatalf("second GetOrCreate: %v", err)
	}
	if wasCreated {
		t.Fatal("second GetOrCreate created = true, want false")
	}
	if second.ID != want.ID {
		t.Fatalf("second id = %d, want original %d", second.ID, want.ID)
	}
	if second.Files[0].Path != want.Files[0].Path {
		t.Fatalf("second files = %+v, want the original change set", second.Files)
	}

	loaded, err := repo.GetByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if loaded.ID != want.ID || !reflect.DeepEqual(loaded.Files, want.Files) {
		t.Fatalf("loaded = %+v, want %+v", loaded, want)
	}
	if _, err := repo.GetByID(ctx, 9999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID missing error = %v, want ErrNotFound", err)
	}
}

// TestEntityUpsertAndFactVersion covers the T0 file upsert, the current-versus-
// history split of code_entities/code_fact_versions and the fact-body read of
// GetVersion.
func TestEntityUpsertAndFactVersion(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, _, _ := seedRepoVersionRequirement(t, pool)
	commit := seedHeadCommit
	entities := postgres.NewEntityRepository(pool)
	facts := postgres.NewFactVersionRepository(pool)

	factKey := "api:POST /api/refund"
	factPath := "backend/internal/api/refund.go"
	fact := domain.CodeEntity{
		ID:          7101,
		RepoID:      repoID,
		Commit:      commit,
		Kind:        domain.KindAPI,
		Path:        factPath,
		Name:        "POST /api/refund",
		StartLine:   ciPtr(10),
		EndLine:     ciPtr(12),
		Signature:   "func refund",
		Source:      domain.SourceAI,
		Confidence:  domain.ConfidenceHigh,
		Evidence:    []domain.Evidence{{File: factPath, StartLine: 10, EndLine: 12, ExcerptHash: ciExcerptA}},
		Meta:        []byte(`{"summary":"新增退款接口"}`),
		FactKey:     &factKey,
		FactVersion: ciPtr(1),
		AnalysisID:  ciPtr(int64(7201)),
	}
	if err := entities.Upsert(ctx, fact); err != nil {
		t.Fatalf("Upsert fact: %v", err)
	}
	stored, err := entities.GetByFactKey(ctx, repoID, commit, factKey)
	if err != nil {
		t.Fatalf("GetByFactKey: %v", err)
	}
	if stored.ID != fact.ID || stored.Kind != fact.Kind || stored.Path != factPath ||
		stored.Name != fact.Name || stored.Source != domain.SourceAI ||
		stored.Confidence != domain.ConfidenceHigh ||
		stored.FactKey == nil || *stored.FactKey != factKey ||
		stored.FactVersion == nil || *stored.FactVersion != 1 ||
		stored.AnalysisID == nil || *stored.AnalysisID != 7201 ||
		stored.StartLine == nil || *stored.StartLine != 10 ||
		stored.EndLine == nil || *stored.EndLine != 12 {
		t.Fatalf("stored fact = %+v, want %+v", stored, fact)
	}
	if !reflect.DeepEqual(stored.Evidence, fact.Evidence) {
		t.Fatalf("stored evidence = %+v, want %+v", stored.Evidence, fact.Evidence)
	}
	ciAssertJSONEqual(t, stored.Meta, string(fact.Meta))

	// A new body bumps the current version: the fact key stays unique and the
	// current entity row is updated in place.
	fact.FactVersion = ciPtr(2)
	fact.Confidence = domain.ConfidenceMedium
	fact.Evidence = append(fact.Evidence, domain.Evidence{File: factPath, StartLine: 20, EndLine: 21, ExcerptHash: ciExcerptB})
	if err := entities.Upsert(ctx, fact); err != nil {
		t.Fatalf("Upsert fact version 2: %v", err)
	}
	var factRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_entities WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3`,
		repoID, commit, factKey).Scan(&factRows); err != nil {
		t.Fatalf("count fact rows: %v", err)
	}
	if factRows != 1 {
		t.Fatalf("fact rows = %d, want 1 (fact_key must be unique per commit)", factRows)
	}
	updated, err := entities.GetByFactKey(ctx, repoID, commit, factKey)
	if err != nil {
		t.Fatalf("GetByFactKey after update: %v", err)
	}
	if updated.FactVersion == nil || *updated.FactVersion != 2 {
		t.Fatalf("current fact version = %v, want 2", updated.FactVersion)
	}
	if updated.Confidence != domain.ConfidenceMedium || len(updated.Evidence) != 2 {
		t.Fatalf("updated fact = %+v, want medium with 2 evidence entries", updated)
	}

	// The append-only body history: Current returns the entity plus the current
	// version, GetVersion reads one exact body, Supersede closes older versions.
	if err := facts.Create(ctx, repoID, commit, factKey, "deepseek-chat", "p4.1", 1,
		[]byte(`{"summary":"v1","kind":"api"}`), 7201); err != nil {
		t.Fatalf("Create fact version 1: %v", err)
	}
	if err := facts.Create(ctx, repoID, commit, factKey, "deepseek-chat", "p4.1", 2,
		[]byte(`{"summary":"v2","kind":"api"}`), 7202); err != nil {
		t.Fatalf("Create fact version 2: %v", err)
	}
	current, version, err := facts.Current(ctx, repoID, commit, factKey)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if version != 2 {
		t.Fatalf("Current version = %d, want 2", version)
	}
	if current.ID != fact.ID || current.FactVersion == nil || *current.FactVersion != 2 {
		t.Fatalf("Current entity = %+v, want the current row at version 2", current)
	}
	body, err := facts.GetVersion(ctx, repoID, commit, factKey, 1)
	if err != nil {
		t.Fatalf("GetVersion(1): %v", err)
	}
	ciAssertJSONEqual(t, body, `{"summary":"v1","kind":"api"}`)
	if _, err := facts.GetVersion(ctx, repoID, commit, factKey, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetVersion(99) error = %v, want ErrNotFound", err)
	}

	supersededAt := ciNow()
	if err := facts.Supersede(ctx, repoID, commit, factKey, 2, supersededAt); err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	var superseded int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int FROM code_fact_versions
		WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3
		  AND version < 2 AND superseded_at IS NOT NULL`,
		repoID, commit, factKey).Scan(&superseded); err != nil {
		t.Fatalf("count superseded versions: %v", err)
	}
	if superseded != 1 {
		t.Fatalf("superseded versions = %d, want 1", superseded)
	}
	var currentSuperseded bool
	if err := pool.QueryRow(ctx, `
		SELECT superseded_at IS NULL FROM code_fact_versions
		WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3 AND version = 2`,
		repoID, commit, factKey).Scan(&currentSuperseded); err != nil {
		t.Fatalf("read current version superseded_at: %v", err)
	}
	if !currentSuperseded {
		t.Fatal("current fact version must not be superseded")
	}
	if _, _, err := facts.Current(ctx, repoID, commit, "api:POST /api/missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Current missing error = %v, want ErrNotFound", err)
	}

	// T0 file upserts are keyed by (repo, commit, path): a re-index refreshes
	// the blob hash without creating a duplicate row.
	fileA := domain.CodeEntity{
		ID: 7110, RepoID: repoID, Commit: commit, Kind: domain.KindFile,
		Path: "src/a.ts", BlobHash: ciBlobHashA, Source: domain.SourceGit, Confidence: domain.ConfidenceHigh,
	}
	fileB := domain.CodeEntity{
		ID: 7111, RepoID: repoID, Commit: commit, Kind: domain.KindFile,
		Path: "src/b.go", BlobHash: ciBlobHashB, Source: domain.SourceGit, Confidence: domain.ConfidenceHigh,
	}
	if err := entities.UpsertFile(ctx, repoID, commit, []domain.CodeEntity{fileA, fileB}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	files, _, _, err := entities.List(ctx, repoID, domain.EntityFilter{Commit: commit, Kind: domain.KindFile, Limit: 10})
	if err != nil {
		t.Fatalf("List files: %v", err)
	}
	if len(files) != 2 || files[0].Path != "src/a.ts" || files[1].Path != "src/b.go" {
		t.Fatalf("files = %+v, want a.ts then b.go", files)
	}
	reIndexedA := fileA
	reIndexedA.ID = 7112
	reIndexedA.BlobHash = ciBlobHashB
	if err := entities.UpsertFile(ctx, repoID, commit, []domain.CodeEntity{reIndexedA}); err != nil {
		t.Fatalf("re-UpsertFile: %v", err)
	}
	aFiles, _, _, err := entities.List(ctx, repoID, domain.EntityFilter{Commit: commit, Kind: domain.KindFile, Path: "src/a.ts", Limit: 10})
	if err != nil {
		t.Fatalf("List src/a.ts: %v", err)
	}
	if len(aFiles) != 1 {
		t.Fatalf("src/a.ts rows = %d, want 1", len(aFiles))
	}
	if aFiles[0].ID != fileA.ID {
		t.Fatalf("src/a.ts id = %d, want the existing row %d", aFiles[0].ID, fileA.ID)
	}
	if aFiles[0].BlobHash != ciBlobHashB {
		t.Fatalf("src/a.ts blob_hash = %q, want refreshed %q", aFiles[0].BlobHash, ciBlobHashB)
	}

	// List filters and keyset pagination: path ordering, id cursor.
	apiOnly, _, _, err := entities.List(ctx, repoID, domain.EntityFilter{Commit: commit, Kind: domain.KindAPI, Limit: 10})
	if err != nil {
		t.Fatalf("List api: %v", err)
	}
	if len(apiOnly) != 1 || apiOnly[0].FactKey == nil || *apiOnly[0].FactKey != factKey {
		t.Fatalf("api entities = %+v, want the refund fact", apiOnly)
	}
	searched, _, _, err := entities.List(ctx, repoID, domain.EntityFilter{Commit: commit, Query: "REFUND", Limit: 10})
	if err != nil {
		t.Fatalf("List query: %v", err)
	}
	if len(searched) != 1 || searched[0].Kind != domain.KindAPI {
		t.Fatalf("search result = %+v, want the refund api fact", searched)
	}
	firstPage, nextCursor, hasMore, err := entities.List(ctx, repoID, domain.EntityFilter{Commit: commit, Kind: domain.KindFile, Limit: 1})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(firstPage) != 1 || !hasMore || nextCursor == "" {
		t.Fatalf("page 1 = %+v cursor=%q hasMore=%v, want one row and a cursor", firstPage, nextCursor, hasMore)
	}
	secondPage, lastCursor, hasMore, err := entities.List(ctx, repoID, domain.EntityFilter{Commit: commit, Kind: domain.KindFile, Limit: 1, Cursor: nextCursor})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(secondPage) != 1 || hasMore || lastCursor != "" {
		t.Fatalf("page 2 = %+v cursor=%q hasMore=%v, want the final row", secondPage, lastCursor, hasMore)
	}
	if secondPage[0].Path == firstPage[0].Path {
		t.Fatalf("page 2 repeated %q; keyset cursor did not advance", secondPage[0].Path)
	}

	byPaths, err := entities.ListByPaths(ctx, repoID, commit, []string{"src/a.ts", "src/b.go", "src/missing.ts"})
	if err != nil {
		t.Fatalf("ListByPaths: %v", err)
	}
	if len(byPaths) != 2 {
		t.Fatalf("ListByPaths rows = %d, want 2", len(byPaths))
	}

	if err := entities.DeleteFact(ctx, repoID, commit, factKey); err != nil {
		t.Fatalf("DeleteFact: %v", err)
	}
	if _, err := entities.GetByFactKey(ctx, repoID, commit, factKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByFactKey after delete error = %v, want ErrNotFound", err)
	}
	// Deleting a missing fact is idempotent.
	if err := entities.DeleteFact(ctx, repoID, commit, factKey); err != nil {
		t.Fatalf("second DeleteFact: %v", err)
	}
}

// TestEdgeReplaceAndList pins the transactional fact-edge replacement and the
// inbound/outbound and relation listings.
func TestEdgeReplaceAndList(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, _, _ := seedRepoVersionRequirement(t, pool)
	commit := seedHeadCommit
	entities := postgres.NewEntityRepository(pool)
	edges := postgres.NewEdgeRepository(pool)

	if err := entities.UpsertFile(ctx, repoID, commit, []domain.CodeEntity{
		{ID: 7301, Path: "src/client.ts", BlobHash: ciBlobHashA},
		{ID: 7302, Path: "src/api.ts", BlobHash: ciBlobHashB},
	}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	factKey := "client:POST /api/refund"
	edge := domain.CodeEdge{
		ID:          7401,
		SrcEntityID: 7301,
		DstEntityID: 7302,
		Rel:         domain.RelCalls,
		Source:      domain.SourceAI,
		Confidence:  domain.ConfidenceHigh,
		Evidence:    []domain.Evidence{{File: "src/client.ts", StartLine: 3, EndLine: 5, ExcerptHash: ciExcerptA}},
		Meta:        []byte(`{"method":"POST"}`),
		AnalysisID:  ciPtr(int64(7300)),
	}
	if err := edges.ReplaceFactEdges(ctx, repoID, commit, factKey, 1, []domain.CodeEdge{edge}); err != nil {
		t.Fatalf("ReplaceFactEdges: %v", err)
	}
	bySrc, err := edges.ListByEntity(ctx, repoID, commit, 7301)
	if err != nil {
		t.Fatalf("ListByEntity src: %v", err)
	}
	if len(bySrc) != 1 {
		t.Fatalf("outbound edges = %d, want 1", len(bySrc))
	}
	if bySrc[0].Rel != domain.RelCalls || bySrc[0].FactKey == nil || *bySrc[0].FactKey != factKey ||
		bySrc[0].FactVersion == nil || *bySrc[0].FactVersion != 1 {
		t.Fatalf("outbound edge = %+v, want calls edge of the fact version", bySrc[0])
	}
	if !reflect.DeepEqual(bySrc[0].Evidence, edge.Evidence) {
		t.Fatalf("edge evidence = %+v, want %+v", bySrc[0].Evidence, edge.Evidence)
	}
	ciAssertJSONEqual(t, bySrc[0].Meta, string(edge.Meta))
	byDst, err := edges.ListByEntity(ctx, repoID, commit, 7302)
	if err != nil {
		t.Fatalf("ListByEntity dst: %v", err)
	}
	if len(byDst) != 1 {
		t.Fatalf("inbound edges = %d, want 1", len(byDst))
	}
	byRel, err := edges.ListByRel(ctx, repoID, commit, domain.RelCalls)
	if err != nil {
		t.Fatalf("ListByRel: %v", err)
	}
	if len(byRel) != 1 {
		t.Fatalf("calls edges = %d, want 1", len(byRel))
	}
	if empty, err := edges.ListByRel(ctx, repoID, commit, domain.RelImports); err != nil || len(empty) != 0 {
		t.Fatalf("imports edges = %+v (err %v), want none", empty, err)
	}

	// Replacement deletes the previous edge set of the fact version first.
	replacement := edge
	replacement.ID = 7402
	replacement.Rel = domain.RelRenders
	if err := edges.ReplaceFactEdges(ctx, repoID, commit, factKey, 1, []domain.CodeEdge{replacement}); err != nil {
		t.Fatalf("replace fact edges: %v", err)
	}
	if calls, err := edges.ListByRel(ctx, repoID, commit, domain.RelCalls); err != nil || len(calls) != 0 {
		t.Fatalf("calls edges after replace = %+v (err %v), want none", calls, err)
	}
	if renders, err := edges.ListByRel(ctx, repoID, commit, domain.RelRenders); err != nil || len(renders) != 1 {
		t.Fatalf("renders edges = %+v (err %v), want one", renders, err)
	}

	// An empty replacement just clears the fact version's edges.
	if err := edges.ReplaceFactEdges(ctx, repoID, commit, factKey, 1, nil); err != nil {
		t.Fatalf("clear fact edges: %v", err)
	}
	if all, err := edges.ListByEntity(ctx, repoID, commit, 7301); err != nil || len(all) != 0 {
		t.Fatalf("edges after clear = %+v (err %v), want none", all, err)
	}
}

// TestCacheHitAndUpsert pins the exact six-part cache key, the hit timestamp
// bump and the ON CONFLICT update of facts/stats/last_used_at.
func TestCacheHitAndUpsert(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, _, _ := seedRepoVersionRequirement(t, pool)
	cache := postgres.NewCacheRepository(pool)
	commit := seedHeadCommit

	if hit, ok, err := cache.Get(ctx, repoID, commit, "src/a.ts", ciBlobHashA, "deepseek-chat", "p4.1"); err != nil || ok || hit != nil {
		t.Fatalf("miss = (%+v, %v, %v), want (nil, false, nil)", hit, ok, err)
	}

	entry := domain.CachedFile{
		RepoID:        repoID,
		Commit:        commit,
		Path:          "src/a.ts",
		FileHash:      ciBlobHashA,
		Model:         "deepseek-chat",
		PromptVersion: "p4.1",
		Facts:         []byte(`[{"kind":"api","name":"POST /api/refund"}]`),
		Stats:         []byte(`{"facts":1}`),
	}
	if err := cache.Upsert(ctx, entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hit, ok, err := cache.Get(ctx, repoID, commit, "src/a.ts", ciBlobHashA, "deepseek-chat", "p4.1")
	if err != nil {
		t.Fatalf("Get hit: %v", err)
	}
	if !ok || hit == nil {
		t.Fatal("Get after Upsert missed")
	}
	ciAssertJSONEqual(t, hit.Facts, string(entry.Facts))
	ciAssertJSONEqual(t, hit.Stats, string(entry.Stats))
	if hit.LastUsedAt.IsZero() || hit.CreatedAt.IsZero() {
		t.Fatalf("cache timestamps = %v/%v, want populated", hit.CreatedAt, hit.LastUsedAt)
	}

	// Any part of the key changing is a miss.
	if _, ok, err := cache.Get(ctx, repoID, commit, "src/a.ts", ciBlobHashA, "deepseek-chat", "p4.2"); err != nil || ok {
		t.Fatalf("prompt-version miss = (ok %v, err %v), want a miss", ok, err)
	}
	if _, ok, err := cache.Get(ctx, repoID, commit, "src/a.ts", ciBlobHashB, "deepseek-chat", "p4.1"); err != nil || ok {
		t.Fatalf("file-hash miss = (ok %v, err %v), want a miss", ok, err)
	}

	// A hit refreshes last_used_at (design spec §6.4).
	old := ciNow().Add(-2 * time.Hour)
	if _, err := pool.Exec(ctx,
		`UPDATE code_analysis_cache SET last_used_at = $1 WHERE repo_id = $2 AND path = 'src/a.ts'`,
		old, repoID); err != nil {
		t.Fatalf("age last_used_at: %v", err)
	}
	hit, ok, err = cache.Get(ctx, repoID, commit, "src/a.ts", ciBlobHashA, "deepseek-chat", "p4.1")
	if err != nil || !ok {
		t.Fatalf("Get after aging = (ok %v, err %v)", ok, err)
	}
	if !hit.LastUsedAt.After(old) {
		t.Fatalf("last_used_at = %v, want later than %v after a cache hit", hit.LastUsedAt, old)
	}

	// Re-analysing the same file with the same key updates the row instead of
	// duplicating it and refreshes last_used_at again.
	updated := entry
	updated.Facts = []byte(`[{"kind":"route","name":"/order"}]`)
	updated.Stats = []byte(`{"facts":2}`)
	if err := cache.Upsert(ctx, updated); err != nil {
		t.Fatalf("Upsert updated: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_analysis_cache WHERE repo_id = $1 AND commit_sha = $2 AND path = 'src/a.ts'`,
		repoID, commit).Scan(&rows); err != nil {
		t.Fatalf("count cache rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("cache rows = %d, want 1", rows)
	}
	var storedFacts, storedStats []byte
	var lastUsed time.Time
	if err := pool.QueryRow(ctx, `
		SELECT facts, stats, last_used_at FROM code_analysis_cache
		WHERE repo_id = $1 AND commit_sha = $2 AND path = 'src/a.ts'`,
		repoID, commit).Scan(&storedFacts, &storedStats, &lastUsed); err != nil {
		t.Fatalf("read updated cache row: %v", err)
	}
	ciAssertJSONEqual(t, storedFacts, string(updated.Facts))
	ciAssertJSONEqual(t, storedStats, string(updated.Stats))
	if !lastUsed.After(old) {
		t.Fatalf("upsert last_used_at = %v, want later than %v", lastUsed, old)
	}
}

// TestAnalysisReuseAndNextVersion pins the analysis version lifecycle: monkey-
// increasing analysis_version, queued/running active detection, done-only
// reuse, latest lookup and the (version, base, head, analysis_version) unique
// key.
func TestAnalysisReuseAndNextVersion(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, versionID, _ := seedRepoVersionRequirement(t, pool)
	analyses := postgres.NewAnalysisRepository(pool)
	base, head := seedBaseCommit, seedHeadCommit

	next, err := analyses.NextAnalysisVersion(ctx, versionID, base, head)
	if err != nil {
		t.Fatalf("NextAnalysisVersion empty: %v", err)
	}
	if next != 1 {
		t.Fatalf("NextAnalysisVersion empty = %d, want 1", next)
	}

	a1 := domain.CodeAnalysis{
		ID:              7501,
		VersionID:       versionID,
		RepoID:          repoID,
		BaseCommit:      base,
		HeadCommit:      head,
		Status:          domain.RunQueued,
		Model:           "deepseek-chat",
		PromptVersion:   "p4.1",
		AnalysisVersion: 1,
		FactVersions:    []domain.FactVersionRef{{FactKey: "api:POST /api/refund", Version: 1}},
		Stats:           []byte(`{"changedFiles":1}`),
		Errors:          []domain.AnalysisError{},
		Unmapped:        []domain.UnmappedChange{},
		CreatedBy:       9,
	}
	if err := analyses.Create(ctx, a1); err != nil {
		t.Fatalf("Create analysis: %v", err)
	}
	if next, err = analyses.NextAnalysisVersion(ctx, versionID, base, head); err != nil || next != 2 {
		t.Fatalf("NextAnalysisVersion after create = %d (err %v), want 2", next, err)
	}
	if active, err := analyses.HasActive(ctx, versionID); err != nil || !active {
		t.Fatalf("HasActive queued = %v (err %v), want true", active, err)
	}

	started, finished := ciNow(), ciNow().Add(time.Minute)
	a1.Status = domain.RunDone
	a1.Degraded = true
	a1.StartedAt = ciPtr(started)
	a1.FinishedAt = ciPtr(finished)
	a1.Stats = []byte(`{"changedFiles":1,"analyzedFiles":1}`)
	a1.Errors = []domain.AnalysisError{{Code: "evidence_failed", Path: "src/a.ts", Message: "excerpt hash mismatch"}}
	a1.Unmapped = []domain.UnmappedChange{{Path: "src/z.ts", Status: "M", Reason: domain.UnmappedNoFact, Suggestion: "manual"}}
	a1.FactVersions = append(a1.FactVersions, domain.FactVersionRef{FactKey: "route:/order", Version: 2})
	if err := analyses.Update(ctx, a1); err != nil {
		t.Fatalf("Update analysis: %v", err)
	}
	if active, err := analyses.HasActive(ctx, versionID); err != nil || active {
		t.Fatalf("HasActive after done = %v (err %v), want false", active, err)
	}

	loaded, err := analyses.GetByID(ctx, versionID, a1.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if loaded.Status != domain.RunDone || !loaded.Degraded {
		t.Fatalf("loaded status/degraded = %s/%v, want done/true", loaded.Status, loaded.Degraded)
	}
	if loaded.StartedAt == nil || !loaded.StartedAt.Equal(started) {
		t.Fatalf("started_at = %v, want %v", loaded.StartedAt, started)
	}
	if loaded.FinishedAt == nil || !loaded.FinishedAt.Equal(finished) {
		t.Fatalf("finished_at = %v, want %v", loaded.FinishedAt, finished)
	}
	ciAssertJSONEqual(t, loaded.Stats, string(a1.Stats))
	if !reflect.DeepEqual(loaded.Errors, a1.Errors) {
		t.Fatalf("errors = %+v, want %+v", loaded.Errors, a1.Errors)
	}
	if !reflect.DeepEqual(loaded.Unmapped, a1.Unmapped) {
		t.Fatalf("unmapped = %+v, want %+v", loaded.Unmapped, a1.Unmapped)
	}
	if !reflect.DeepEqual(loaded.FactVersions, a1.FactVersions) {
		t.Fatalf("fact versions = %+v, want %+v", loaded.FactVersions, a1.FactVersions)
	}
	if loaded.Model != "deepseek-chat" || loaded.PromptVersion != "p4.1" || loaded.AnalysisVersion != 1 {
		t.Fatalf("loaded identity = %+v, want the created analysis", loaded)
	}

	reusable, err := analyses.FindReusable(ctx, versionID, repoID, base, head, "deepseek-chat", "p4.1")
	if err != nil {
		t.Fatalf("FindReusable: %v", err)
	}
	if reusable == nil || reusable.ID != a1.ID {
		t.Fatalf("FindReusable = %+v, want analysis %d", reusable, a1.ID)
	}
	if reusable, err := analyses.FindReusable(ctx, versionID, repoID, base, head, "other-model", "p4.1"); err != nil || reusable != nil {
		t.Fatalf("FindReusable other model = (%+v, %v), want nil", reusable, err)
	}
	if reusable, err := analyses.FindReusable(ctx, versionID, repoID, base, head, "deepseek-chat", "p4.2"); err != nil || reusable != nil {
		t.Fatalf("FindReusable other prompt = (%+v, %v), want nil", reusable, err)
	}

	latest, err := analyses.GetLatest(ctx, versionID)
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if latest.ID != a1.ID {
		t.Fatalf("GetLatest = %d, want %d", latest.ID, a1.ID)
	}

	// A forced re-run creates the next analysis version; GetLatest follows it.
	a2 := a1
	a2.ID = 7502
	a2.AnalysisVersion = 2
	a2.CreatedAt = time.Time{}
	if err := analyses.Create(ctx, a2); err != nil {
		t.Fatalf("Create analysis version 2: %v", err)
	}
	latest, err = analyses.GetLatest(ctx, versionID)
	if err != nil {
		t.Fatalf("GetLatest after v2: %v", err)
	}
	if latest.ID != a2.ID || latest.AnalysisVersion != 2 {
		t.Fatalf("latest = %d (v%d), want %d (v2)", latest.ID, latest.AnalysisVersion, a2.ID)
	}

	// The unique key rejects a duplicate analysis version.
	duplicate := a2
	duplicate.ID = 7503
	if err := analyses.Create(ctx, duplicate); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate analysis version error = %v, want ErrConflict", err)
	}
	if _, err := analyses.GetByID(ctx, versionID+1, a1.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID wrong version error = %v, want ErrNotFound", err)
	}
	missing := a1
	missing.ID = 9999
	if err := analyses.Update(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Update missing error = %v, want ErrNotFound", err)
	}
}

// TestCandidateSelection covers the transactional candidate replacement, the
// deterministic ordering, the detail lookup and the human selection update.
func TestCandidateSelection(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, versionID, requirementID := seedRepoVersionRequirement(t, pool)
	analyses := postgres.NewAnalysisRepository(pool)
	candidates := postgres.NewCandidateRepository(pool)
	analysisID := int64(7601)
	if err := analyses.Create(ctx, domain.CodeAnalysis{
		ID:              analysisID,
		VersionID:       versionID,
		RepoID:          repoID,
		BaseCommit:      seedBaseCommit,
		HeadCommit:      seedHeadCommit,
		Status:          domain.RunDone,
		Model:           "deepseek-chat",
		PromptVersion:   "p4.1",
		AnalysisVersion: 1,
		CreatedBy:       1,
	}); err != nil {
		t.Fatalf("Create analysis: %v", err)
	}

	direct := domain.ImpactCandidate{
		ID:          7701,
		AnalysisID:  analysisID,
		SubjectKind: domain.CandidateRequirement,
		SubjectID:   requirementID,
		Reason:      "changed file src/api/refund.ts",
		Confidence:  domain.ConfidenceHigh,
		Source:      domain.CandidateDirect,
		Evidence:    []domain.Evidence{{File: "src/api/refund.ts"}},
		Selected:    true,
	}
	ai := domain.ImpactCandidate{
		ID:          7702,
		AnalysisID:  analysisID,
		SubjectKind: domain.CandidateRequirement,
		SubjectID:   requirementID + 1,
		Reason:      "AI inferred impact",
		Confidence:  domain.ConfidenceLow,
		Source:      domain.CandidateAI,
		Evidence:    []domain.Evidence{{File: "src/api/refund.ts", StartLine: 4, EndLine: 6, ExcerptHash: ciExcerptA}},
		Selected:    false,
	}
	// Insert in reverse priority order so List must apply the deterministic
	// source/confidence ordering itself.
	if err := candidates.Replace(ctx, analysisID, []domain.ImpactCandidate{ai, direct}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	listed, err := candidates.List(ctx, analysisID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("candidates = %d, want 2", len(listed))
	}
	if listed[0].ID != direct.ID || listed[1].ID != ai.ID {
		t.Fatalf("candidate order = [%d %d], want [%d %d] (direct before ai)", listed[0].ID, listed[1].ID, direct.ID, ai.ID)
	}
	if !reflect.DeepEqual(listed[0].Evidence, direct.Evidence) || !reflect.DeepEqual(listed[1].Evidence, ai.Evidence) {
		t.Fatalf("candidate evidence = %+v / %+v, want %+v / %+v", listed[0].Evidence, listed[1].Evidence, direct.Evidence, ai.Evidence)
	}

	got, err := candidates.Get(ctx, analysisID, direct.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SubjectID != requirementID || got.Source != domain.CandidateDirect || !got.Selected {
		t.Fatalf("candidate = %+v, want the direct requirement candidate", got)
	}
	if _, err := candidates.Get(ctx, analysisID, 9999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get missing error = %v, want ErrNotFound", err)
	}

	// Replacement is a full delete+insert of the analysis' candidates.
	if err := candidates.Replace(ctx, analysisID, []domain.ImpactCandidate{direct}); err != nil {
		t.Fatalf("Replace subset: %v", err)
	}
	listed, err = candidates.List(ctx, analysisID)
	if err != nil {
		t.Fatalf("List subset: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != direct.ID {
		t.Fatalf("candidates after replace = %+v, want only the direct candidate", listed)
	}

	// Human selection writes selected/updated_by/updated_at.
	if err := analyses.UpdateCandidateSelection(ctx, analysisID, direct.ID, 42, false); err != nil {
		t.Fatalf("UpdateCandidateSelection: %v", err)
	}
	got, err = candidates.Get(ctx, analysisID, direct.ID)
	if err != nil {
		t.Fatalf("Get after selection: %v", err)
	}
	if got.Selected {
		t.Fatal("candidate still selected after deselect")
	}
	if got.SelectionUpdatedBy == nil || *got.SelectionUpdatedBy != 42 {
		t.Fatalf("selection_updated_by = %v, want 42", got.SelectionUpdatedBy)
	}
	if got.SelectionUpdatedAt == nil || got.SelectionUpdatedAt.IsZero() {
		t.Fatal("selection_updated_at not written")
	}
	if err := analyses.UpdateCandidateSelection(ctx, analysisID, 9999, 42, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateCandidateSelection missing error = %v, want ErrNotFound", err)
	}

	// An empty replacement clears the candidate set.
	if err := candidates.Replace(ctx, analysisID, nil); err != nil {
		t.Fatalf("Replace empty: %v", err)
	}
	if listed, err := candidates.List(ctx, analysisID); err != nil || len(listed) != 0 {
		t.Fatalf("candidates after clear = %d (err %v), want 0", len(listed), err)
	}
}

// TestAnchorDuplicate pins the anchor uniqueness rule (409 anchor_duplicate on
// the same version/subject/file/line range) and the get/list/delete queries.
func TestAnchorDuplicate(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, versionID, requirementID := seedRepoVersionRequirement(t, pool)
	anchors := postgres.NewAnchorRepository(pool)

	anchor := domain.CodeAnchor{
		ID:          7801,
		VersionID:   versionID,
		RepoID:      repoID,
		SubjectKind: domain.SubjectRequirement,
		SubjectID:   requirementID,
		FilePath:    "src/api/refund.ts",
		StartLine:   ciPtr(10),
		EndLine:     ciPtr(20),
		CommitSHA:   ciPtr(seedHeadCommit),
		CreatedBy:   5,
	}
	if err := anchors.Create(ctx, anchor); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, err := anchors.Get(ctx, versionID, anchor.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.FilePath != anchor.FilePath || stored.SubjectID != requirementID ||
		stored.StartLine == nil || *stored.StartLine != 10 ||
		stored.EndLine == nil || *stored.EndLine != 20 ||
		stored.CommitSHA == nil || *stored.CommitSHA != seedHeadCommit {
		t.Fatalf("stored anchor = %+v, want %+v", stored, anchor)
	}
	// The DDL defaults are applied by the repository as well.
	if stored.Confidence != domain.ConfidenceHigh {
		t.Fatalf("confidence = %q, want high", stored.Confidence)
	}
	if stored.CreatedByKind != "user" {
		t.Fatalf("created_by_kind = %q, want user", stored.CreatedByKind)
	}

	duplicate := anchor
	duplicate.ID = 7802
	duplicateErr := anchors.Create(ctx, duplicate)
	if !errors.Is(duplicateErr, domain.ErrConflict) {
		t.Fatalf("duplicate anchor error = %v, want ErrConflict", duplicateErr)
	}
	if reason := domain.Reason(duplicateErr); reason != "anchor_duplicate" {
		t.Fatalf("duplicate anchor reason = %q, want anchor_duplicate", reason)
	}

	// A different line range and a different file are distinct anchors.
	otherRange := anchor
	otherRange.ID = 7803
	otherRange.StartLine = ciPtr(30)
	otherRange.EndLine = ciPtr(31)
	if err := anchors.Create(ctx, otherRange); err != nil {
		t.Fatalf("Create different range: %v", err)
	}
	// Anchors without a line range are deduplicated on COALESCE(start,0).
	noLines := anchor
	noLines.ID = 7804
	noLines.StartLine = nil
	noLines.EndLine = nil
	noLines.FilePath = "src/order.ts"
	if err := anchors.Create(ctx, noLines); err != nil {
		t.Fatalf("Create line-less anchor: %v", err)
	}
	duplicateNoLines := noLines
	duplicateNoLines.ID = 7805
	if err := anchors.Create(ctx, duplicateNoLines); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate line-less anchor error = %v, want ErrConflict", err)
	}

	all, err := anchors.List(ctx, versionID, domain.AnchorFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("anchors = %d, want 3", len(all))
	}
	subjectAnchors, err := anchors.List(ctx, versionID, domain.AnchorFilter{SubjectKind: domain.SubjectRequirement, SubjectID: ciPtr(requirementID)})
	if err != nil {
		t.Fatalf("List by subject: %v", err)
	}
	if len(subjectAnchors) != 3 {
		t.Fatalf("subject anchors = %d, want 3", len(subjectAnchors))
	}
	noAppModels, err := anchors.List(ctx, versionID, domain.AnchorFilter{SubjectKind: domain.SubjectAppModel})
	if err != nil {
		t.Fatalf("List app models: %v", err)
	}
	if len(noAppModels) != 0 {
		t.Fatalf("app model anchors = %d, want 0", len(noAppModels))
	}
	byPaths, err := anchors.ListByPaths(ctx, versionID, []string{"src/api/refund.ts"})
	if err != nil {
		t.Fatalf("ListByPaths: %v", err)
	}
	if len(byPaths) != 2 {
		t.Fatalf("anchors for refund.ts = %d, want 2", len(byPaths))
	}

	if err := anchors.Delete(ctx, versionID, anchor.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := anchors.Get(ctx, versionID, anchor.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get deleted error = %v, want ErrNotFound", err)
	}
	if err := anchors.Delete(ctx, versionID, anchor.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second Delete error = %v, want ErrNotFound", err)
	}
	if _, err := anchors.Get(ctx, versionID+1, otherRange.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get wrong version error = %v, want ErrNotFound", err)
	}
}

// TestIndexRunLifecycle covers the run CRUD, the JSONB errors/stats round-trip,
// GetLatest and the application-generated-id failure modes.
func TestIndexRunLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, _, _ := seedRepoVersionRequirement(t, pool)
	runs := postgres.NewIndexRunRepository(pool)

	run := domain.IndexRun{
		ID:        7901,
		RepoID:    repoID,
		CommitSHA: seedHeadCommit,
		Source:    "t0",
		Status:    domain.RunQueued,
		Stats:     []byte(`{"files":10}`),
		CreatedBy: 1,
	}
	if err := runs.Create(ctx, run); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, err := runs.GetByID(ctx, repoID, run.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != domain.RunQueued || stored.Source != "t0" || stored.CommitSHA != seedHeadCommit {
		t.Fatalf("stored run = %+v, want queued t0 run", stored)
	}
	ciAssertJSONEqual(t, stored.Stats, string(run.Stats))
	if stored.StartedAt != nil || stored.FinishedAt != nil {
		t.Fatalf("new run timestamps = %v/%v, want nil", stored.StartedAt, stored.FinishedAt)
	}

	// Empty source/status fall back to the DDL defaults.
	defaulted := domain.IndexRun{ID: 7902, RepoID: repoID, CommitSHA: seedHeadCommit, CreatedBy: 1}
	if err := runs.Create(ctx, defaulted); err != nil {
		t.Fatalf("Create defaulted: %v", err)
	}
	defaultedStored, err := runs.GetByID(ctx, repoID, defaulted.ID)
	if err != nil {
		t.Fatalf("GetByID defaulted: %v", err)
	}
	if defaultedStored.Source != "t0" || defaultedStored.Status != domain.RunQueued {
		t.Fatalf("defaulted run = %s/%s, want t0/queued", defaultedStored.Source, defaultedStored.Status)
	}
	if latest, err := runs.GetLatest(ctx, repoID); err != nil || latest.ID != defaulted.ID {
		t.Fatalf("GetLatest = %+v (err %v), want %d", latest, err, defaulted.ID)
	}

	// Running transition writes started_at; a failed finish stores the error
	// entries as JSONB.
	started, finished := ciNow(), ciNow().Add(time.Minute)
	run.Status = domain.RunRunning
	run.StartedAt = ciPtr(started)
	if err := runs.Update(ctx, run); err != nil {
		t.Fatalf("Update running: %v", err)
	}
	run.Status = domain.RunFailed
	run.FinishedAt = ciPtr(finished)
	run.Errors = []domain.AnalysisError{{Code: "git_unavailable", Message: "git command timed out"}}
	run.Stats = []byte(`{"files":0}`)
	if err := runs.Update(ctx, run); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	stored, err = runs.GetByID(ctx, repoID, run.ID)
	if err != nil {
		t.Fatalf("GetByID updated: %v", err)
	}
	if stored.Status != domain.RunFailed || stored.StartedAt == nil || !stored.StartedAt.Equal(started) ||
		stored.FinishedAt == nil || !stored.FinishedAt.Equal(finished) {
		t.Fatalf("stored updated run = %+v, want failed with both timestamps", stored)
	}
	if !reflect.DeepEqual(stored.Errors, run.Errors) {
		t.Fatalf("stored errors = %+v, want %+v", stored.Errors, run.Errors)
	}
	ciAssertJSONEqual(t, stored.Stats, string(run.Stats))

	if _, err := runs.GetByID(ctx, repoID, 9999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID missing error = %v, want ErrNotFound", err)
	}
	if _, err := runs.GetByID(ctx, repoID+1, run.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID wrong repo error = %v, want ErrNotFound", err)
	}
	missing := run
	missing.ID = 9999
	if err := runs.Update(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Update missing error = %v, want ErrNotFound", err)
	}
}

// TestRecoverStaleRuns pins the startup recovery: queued/running runs older
// than the cutoff become failed with an interrupted error entry, while fresh
// runs are untouched.
func TestRecoverStaleRuns(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, _, _ := seedRepoVersionRequirement(t, pool)
	runs := postgres.NewIndexRunRepository(pool)

	staleQueued := domain.IndexRun{ID: 7911, RepoID: repoID, CommitSHA: seedHeadCommit, Status: domain.RunQueued, CreatedBy: 1}
	staleRunning := domain.IndexRun{ID: 7912, RepoID: repoID, CommitSHA: seedHeadCommit, Status: domain.RunRunning, StartedAt: ciPtr(ciNow().Add(-time.Hour)), CreatedBy: 1}
	freshRunning := domain.IndexRun{ID: 7913, RepoID: repoID, CommitSHA: seedHeadCommit, Status: domain.RunRunning, StartedAt: ciPtr(ciNow()), CreatedBy: 1}
	freshQueued := domain.IndexRun{ID: 7914, RepoID: repoID, CommitSHA: seedHeadCommit, Status: domain.RunQueued, CreatedBy: 1}
	for _, run := range []domain.IndexRun{staleQueued, staleRunning, freshRunning, freshQueued} {
		if err := runs.Create(ctx, run); err != nil {
			t.Fatalf("Create run %d: %v", run.ID, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`UPDATE code_index_runs SET created_at = now() - interval '1 hour' WHERE id = $1`,
		staleQueued.ID); err != nil {
		t.Fatalf("age stale queued run: %v", err)
	}

	recovered, err := runs.RecoverStale(ctx, ciNow().Add(-10*time.Minute))
	if err != nil {
		t.Fatalf("RecoverStale: %v", err)
	}
	if recovered != 2 {
		t.Fatalf("RecoverStale recovered %d runs, want 2", recovered)
	}
	for _, id := range []int64{staleQueued.ID, staleRunning.ID} {
		stored, err := runs.GetByID(ctx, repoID, id)
		if err != nil {
			t.Fatalf("GetByID recovered %d: %v", id, err)
		}
		if stored.Status != domain.RunFailed {
			t.Fatalf("recovered run %d status = %s, want failed", id, stored.Status)
		}
		if stored.FinishedAt == nil || stored.FinishedAt.IsZero() {
			t.Fatalf("recovered run %d has no finished_at", id)
		}
		if !ciHasAnalysisError(stored.Errors, "interrupted") {
			t.Fatalf("recovered run %d errors = %+v, want an interrupted entry", id, stored.Errors)
		}
	}
	for _, id := range []int64{freshRunning.ID, freshQueued.ID} {
		stored, err := runs.GetByID(ctx, repoID, id)
		if err != nil {
			t.Fatalf("GetByID fresh %d: %v", id, err)
		}
		if stored.Status == domain.RunFailed {
			t.Fatalf("fresh run %d was recovered", id)
		}
	}
	if again, err := runs.RecoverStale(ctx, ciNow().Add(-10*time.Minute)); err != nil || again != 0 {
		t.Fatalf("second RecoverStale = %d (err %v), want 0", again, err)
	}
}

// TestAnalysisRecoverStale covers the same startup recovery for analyses.
func TestAnalysisRecoverStale(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repoID, versionID, _ := seedRepoVersionRequirement(t, pool)
	analyses := postgres.NewAnalysisRepository(pool)

	makeAnalysis := func(id int64, version int, status string, started *time.Time) domain.CodeAnalysis {
		return domain.CodeAnalysis{
			ID:              id,
			VersionID:       versionID,
			RepoID:          repoID,
			BaseCommit:      seedBaseCommit,
			HeadCommit:      seedHeadCommit,
			Status:          status,
			Model:           "deepseek-chat",
			PromptVersion:   "p4.1",
			AnalysisVersion: version,
			StartedAt:       started,
			CreatedBy:       1,
		}
	}
	staleQueued := makeAnalysis(7951, 1, domain.RunQueued, nil)
	staleRunning := makeAnalysis(7952, 2, domain.RunRunning, ciPtr(ciNow().Add(-time.Hour)))
	freshRunning := makeAnalysis(7953, 3, domain.RunRunning, ciPtr(ciNow()))
	for _, analysis := range []domain.CodeAnalysis{staleQueued, staleRunning, freshRunning} {
		if err := analyses.Create(ctx, analysis); err != nil {
			t.Fatalf("Create analysis %d: %v", analysis.ID, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`UPDATE code_analyses SET created_at = now() - interval '1 hour' WHERE id = $1`,
		staleQueued.ID); err != nil {
		t.Fatalf("age stale queued analysis: %v", err)
	}

	recovered, err := analyses.RecoverStale(ctx, ciNow().Add(-10*time.Minute))
	if err != nil {
		t.Fatalf("RecoverStale: %v", err)
	}
	if recovered != 2 {
		t.Fatalf("RecoverStale recovered %d analyses, want 2", recovered)
	}
	for _, id := range []int64{staleQueued.ID, staleRunning.ID} {
		stored, err := analyses.GetByID(ctx, versionID, id)
		if err != nil {
			t.Fatalf("GetByID recovered %d: %v", id, err)
		}
		if stored.Status != domain.RunFailed {
			t.Fatalf("recovered analysis %d status = %s, want failed", id, stored.Status)
		}
		if stored.FinishedAt == nil || stored.FinishedAt.IsZero() {
			t.Fatalf("recovered analysis %d has no finished_at", id)
		}
		if !ciHasAnalysisError(stored.Errors, "interrupted") {
			t.Fatalf("recovered analysis %d errors = %+v, want an interrupted entry", id, stored.Errors)
		}
	}
	fresh, err := analyses.GetByID(ctx, versionID, freshRunning.ID)
	if err != nil {
		t.Fatalf("GetByID fresh: %v", err)
	}
	if fresh.Status != domain.RunRunning {
		t.Fatalf("fresh analysis status = %s, want running", fresh.Status)
	}
}
