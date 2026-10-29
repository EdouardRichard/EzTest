//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// knowledgeTestCreator and knowledgeTestReviewer are the user ids of the seeded
// knowledge rows.
const (
	knowledgeTestCreator  = int64(7)
	knowledgeTestReviewer = int64(9)
)

// openKnowledgeTestDB opens the migrated test database and the knowledge and
// version repositories on it.
func openKnowledgeTestDB(t *testing.T) (*pgxpool.Pool, *postgres.KnowledgeRepository, *postgres.VersionRepository) {
	t.Helper()
	pool := testutil.OpenTestDB(t)
	return pool, postgres.NewKnowledgeRepository(pool), postgres.NewVersionRepository(pool)
}

// insertKnowledge creates one knowledge entry through the repository and returns
// the stored row. The caller supplies the explicit Snowflake id.
func insertKnowledge(t *testing.T, repo *postgres.KnowledgeRepository, entry domain.KnowledgeEntry) domain.KnowledgeEntry {
	t.Helper()
	if entry.Status == "" {
		entry.Status = domain.KnowledgeDraft
	}
	if entry.Confidence == "" {
		entry.Confidence = domain.KnowledgeConfidenceMedium
	}
	if entry.Revision == 0 {
		entry.Revision = 1
	}
	if entry.Tags == nil {
		entry.Tags = []string{}
	}
	if entry.SourceRefs == nil {
		entry.SourceRefs = []domain.KnowledgeSourceRef{}
	}
	if entry.Status != domain.KnowledgeDraft && entry.ReviewedBy == nil && entry.ReviewNote == "" {
		reviewer := knowledgeTestReviewer
		entry.ReviewedBy = &reviewer
	}
	if err := repo.Create(context.Background(), &entry); err != nil {
		t.Fatalf("create knowledge entry %d: %v", entry.ID, err)
	}
	return entry
}

// assertKnowledgeKind asserts err is a *domain.Error with kind and reason.
func assertKnowledgeKind(t *testing.T, err error, kind domain.ErrorKind, reason string) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want *domain.Error kind=%s reason=%s, got nil", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *domain.Error, got %T (%v)", err, err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("error kind/reason = %s/%s, want %s/%s (%v)",
			domainErr.Kind, domainErr.Reason, kind, reason, err)
	}
	return domainErr
}

// knowledgeIDs projects knowledge ids for readable failures.
func knowledgeIDs(entries []domain.KnowledgeEntry) []int64 {
	ids := make([]int64, len(entries))
	for index, entry := range entries {
		ids[index] = entry.ID
	}
	return ids
}

// TestKnowledgeRepositoryCreate covers the INSERT path against the real schema:
// the returning row carries the owning version name, every JSONB/audit column
// round-trips, and the SQL backstops map to the domain errors.
func TestKnowledgeRepositoryCreate(t *testing.T) {
	ctx := context.Background()
	_, repo, versions := openKnowledgeTestDB(t)
	version := insertVersion(t, versions, domain.Version{
		ID: 1001, Name: "Order v2", CreatedBy: knowledgeTestCreator, UpdatedBy: knowledgeTestCreator,
	})
	created := insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6001, VersionID: version.ID, Kind: domain.KnowledgeKindPitfall,
		Title: "Payment timeout", Body: "Use the fake clock for the sandbox gateway.",
		Tags:       []string{"payment", "flaky"},
		SourceRefs: []domain.KnowledgeSourceRef{{Kind: domain.KnowledgeRefRequirement, ID: 200}},
		Status:     domain.KnowledgeDraft, Confidence: domain.KnowledgeConfidenceHigh,
		CreatedBy: knowledgeTestCreator, Revision: 1,
	})
	if created.VersionName != version.Name {
		t.Fatalf("versionName = %q, want %q", created.VersionName, version.Name)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created timestamps not populated: %+v", created)
	}
	if len(created.Tags) != 2 || created.Tags[0] != "payment" ||
		len(created.SourceRefs) != 1 || created.SourceRefs[0].Kind != domain.KnowledgeRefRequirement ||
		created.SourceRefs[0].ID != 200 {
		t.Fatalf("jsonb columns did not round-trip: %+v", created)
	}
	if created.ReviewedBy != nil || created.ReviewNote != "" || created.Revision != 1 {
		t.Fatalf("draft audit fields = %+v", created)
	}

	loaded, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.ID != created.ID || loaded.VersionID != created.VersionID ||
		loaded.VersionName != version.Name || loaded.Kind != created.Kind ||
		loaded.Title != created.Title || loaded.Body != created.Body ||
		loaded.Status != created.Status || loaded.Confidence != created.Confidence ||
		loaded.CreatedBy != created.CreatedBy || loaded.Revision != created.Revision {
		t.Fatalf("loaded entry = %+v\nwant %+v", *loaded, created)
	}
	if len(loaded.Tags) != 2 || len(loaded.SourceRefs) != 1 {
		t.Fatalf("loaded jsonb columns = %+v", *loaded)
	}

	// Missing rows are not-found reads.
	_, err = repo.Get(ctx, 999999)
	assertKnowledgeKind(t, err, domain.KindNotFound, "not_found")

	// Duplicate explicit Snowflake ids are a uniqueness conflict.
	duplicate := &domain.KnowledgeEntry{
		ID: 6001, VersionID: version.ID, Kind: domain.KnowledgeKindPattern,
		Title: "Duplicate", Body: "duplicate body", Status: domain.KnowledgeDraft,
		Confidence: domain.KnowledgeConfidenceMedium, CreatedBy: knowledgeTestCreator, Revision: 1,
	}
	assertKnowledgeKind(t, repo.Create(ctx, duplicate), domain.KindConflict, "duplicate_key")

	// The database CHECK backstops are invalid requests (the service validates
	// first, the repository maps the constraint failure).
	oversized := &domain.KnowledgeEntry{
		ID: 6002, VersionID: version.ID, Kind: domain.KnowledgeKindPattern,
		Title: "Oversized", Body: strings.Repeat("x", domain.MaxKnowledgeBodyBytes+1),
		Status: domain.KnowledgeDraft, Confidence: domain.KnowledgeConfidenceMedium,
		CreatedBy: knowledgeTestCreator, Revision: 1,
	}
	assertKnowledgeKind(t, repo.Create(ctx, oversized), domain.KindInvalid, "invalid_knowledge")
}

// TestKnowledgeRepositoryUpdateContentCAS covers the draft content edit under
// the revision compare-and-swap: a successful write bumps the revision by one,
// the stale revision never writes, a reviewed row is a state conflict and a
// missing row is a not-found.
func TestKnowledgeRepositoryUpdateContentCAS(t *testing.T) {
	ctx := context.Background()
	_, repo, versions := openKnowledgeTestDB(t)
	version := insertVersion(t, versions, domain.Version{
		ID: 1001, Name: "Order v2", CreatedBy: knowledgeTestCreator, UpdatedBy: knowledgeTestCreator,
	})
	created := insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6010, VersionID: version.ID, Kind: domain.KnowledgeKindPattern,
		Title: "Retry policy", Body: "Retry twice.", Tags: []string{"payment"},
		Status:     domain.KnowledgeDraft,
		Confidence: domain.KnowledgeConfidenceMedium, CreatedBy: knowledgeTestCreator, Revision: 1,
	})

	// One successful content edit returns the stored row with revision+1.
	updated := created
	updated.Title = "Retry policy v2"
	updated.Body = "Retry three times before failing the case."
	updated.Kind = domain.KnowledgeKindPitfall
	updated.Tags = []string{"payment", "retry"}
	updated.SourceRefs = []domain.KnowledgeSourceRef{{Kind: domain.KnowledgeRefCase, ID: 300}}
	updated.Confidence = domain.KnowledgeConfidenceLow
	if err := repo.UpdateContent(ctx, &updated, created.Revision); err != nil {
		t.Fatalf("UpdateContent: %v", err)
	}
	if updated.Revision != created.Revision+1 {
		t.Fatalf("revised revision = %d, want %d", updated.Revision, created.Revision+1)
	}
	if updated.Title != "Retry policy v2" || updated.Kind != domain.KnowledgeKindPitfall ||
		updated.Confidence != domain.KnowledgeConfidenceLow ||
		len(updated.Tags) != 2 || len(updated.SourceRefs) != 1 ||
		updated.VersionName != version.Name {
		t.Fatalf("updated entry = %+v", updated)
	}
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("updatedAt moved backwards: %v before %v", updated.UpdatedAt, created.UpdatedAt)
	}

	stored, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if stored.Title != updated.Title || stored.Revision != updated.Revision ||
		stored.Kind != updated.Kind || stored.Confidence != updated.Confidence {
		t.Fatalf("stored entry = %+v, want %+v", *stored, updated)
	}

	// The same expected revision is now stale: the second write must conflict
	// and leave the stored row untouched.
	stale := *stored
	stale.Title = "Stale write"
	assertKnowledgeKind(t, repo.UpdateContent(ctx, &stale, created.Revision),
		domain.KindStateConflict, "knowledge_revision_conflict")
	afterStale, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after stale update: %v", err)
	}
	if afterStale.Title != "Retry policy v2" || afterStale.Revision != created.Revision+1 {
		t.Fatalf("stale update wrote the row: %+v", afterStale)
	}

	// A reviewed row is no longer editable.
	approved := insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6011, VersionID: version.ID, Kind: domain.KnowledgeKindDecision,
		Title: "Approved", Body: "Approved body.", Status: domain.KnowledgeApproved,
		Confidence: domain.KnowledgeConfidenceHigh, CreatedBy: knowledgeTestCreator, Revision: 3,
	})
	readOnly := approved
	readOnly.Title = "Edited after review"
	assertKnowledgeKind(t, repo.UpdateContent(ctx, &readOnly, approved.Revision),
		domain.KindStateConflict, "knowledge_state_conflict")

	// A missing row is not-found.
	missing := created
	missing.ID = 999999
	assertKnowledgeKind(t, repo.UpdateContent(ctx, &missing, 1), domain.KindNotFound, "not_found")
}

// TestKnowledgeRepositoryReviewCAS covers the terminal review transition under
// the revision compare-and-swap, including the stale-revision and double-review
// conflicts.
func TestKnowledgeRepositoryReviewCAS(t *testing.T) {
	ctx := context.Background()
	_, repo, versions := openKnowledgeTestDB(t)
	version := insertVersion(t, versions, domain.Version{
		ID: 1001, Name: "Order v2", CreatedBy: knowledgeTestCreator, UpdatedBy: knowledgeTestCreator,
	})
	created := insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6020, VersionID: version.ID, Kind: domain.KnowledgeKindPattern,
		Title: "Review me", Body: "Review body.", Status: domain.KnowledgeDraft,
		Confidence: domain.KnowledgeConfidenceMedium, CreatedBy: knowledgeTestCreator, Revision: 1,
	})

	approved := created
	approved.Status = domain.KnowledgeApproved
	approved.ReviewedBy = knowledgeInt64Ptr(knowledgeTestReviewer)
	approved.ReviewNote = "matches the sandbox"
	if err := repo.Review(ctx, &approved, created.Revision); err != nil {
		t.Fatalf("Review approve: %v", err)
	}
	if approved.Revision != created.Revision+1 || approved.Status != domain.KnowledgeApproved ||
		approved.ReviewedBy == nil || *approved.ReviewedBy != knowledgeTestReviewer ||
		approved.ReviewNote != "matches the sandbox" {
		t.Fatalf("approved entry = %+v", approved)
	}
	if approved.VersionName != version.Name {
		t.Fatalf("review versionName = %q, want %q", approved.VersionName, version.Name)
	}

	stored, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after review: %v", err)
	}
	if stored.Status != domain.KnowledgeApproved || stored.Revision != 2 ||
		stored.ReviewedBy == nil || *stored.ReviewedBy != knowledgeTestReviewer {
		t.Fatalf("stored entry after review = %+v", *stored)
	}

	// The original revision is stale and the row is already reviewed: the CAS
	// reports the terminal state and never overwrites the decision.
	again := *stored
	again.Status = domain.KnowledgeRejected
	assertKnowledgeKind(t, repo.Review(ctx, &again, created.Revision),
		domain.KindStateConflict, "knowledge_state_conflict")
	afterAgain, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after blocked review: %v", err)
	}
	if afterAgain.Status != domain.KnowledgeApproved || afterAgain.ReviewNote != "matches the sandbox" {
		t.Fatalf("second review overwrote the decision: %+v", *afterAgain)
	}

	// A stale revision on a still-draft row reports the revision conflict.
	draft := insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6021, VersionID: version.ID, Kind: domain.KnowledgeKindPitfall,
		Title: "Stale draft", Body: "Stale body.", Status: domain.KnowledgeDraft,
		Confidence: domain.KnowledgeConfidenceMedium, CreatedBy: knowledgeTestCreator, Revision: 1,
	})
	revised := draft
	revised.Title = "Stale draft v2"
	if err := repo.UpdateContent(ctx, &revised, draft.Revision); err != nil {
		t.Fatalf("UpdateContent before review: %v", err)
	}
	staleReview := revised
	staleReview.Status = domain.KnowledgeRejected
	staleReview.ReviewedBy = knowledgeInt64Ptr(knowledgeTestReviewer)
	staleReview.ReviewNote = "too late"
	assertKnowledgeKind(t, repo.Review(ctx, &staleReview, draft.Revision),
		domain.KindStateConflict, "knowledge_revision_conflict")
	afterStale, err := repo.Get(ctx, draft.ID)
	if err != nil {
		t.Fatalf("Get after stale review: %v", err)
	}
	if afterStale.Status != domain.KnowledgeDraft || afterStale.ReviewedBy != nil ||
		afterStale.Revision != revised.Revision {
		t.Fatalf("stale review wrote the row: %+v", *afterStale)
	}

	// Rejecting stores the reason and bumps the revision as well.
	rejectedEntry := revised
	rejectedEntry.Status = domain.KnowledgeRejected
	rejectedEntry.ReviewedBy = knowledgeInt64Ptr(knowledgeTestReviewer)
	rejectedEntry.ReviewNote = "duplicates decision D12"
	if err := repo.Review(ctx, &rejectedEntry, revised.Revision); err != nil {
		t.Fatalf("Review reject: %v", err)
	}
	if rejectedEntry.Status != domain.KnowledgeRejected ||
		rejectedEntry.Revision != revised.Revision+1 ||
		rejectedEntry.ReviewNote != "duplicates decision D12" {
		t.Fatalf("rejected entry = %+v", rejectedEntry)
	}

	// A missing row is not-found.
	missing := created
	missing.ID = 999999
	assertKnowledgeKind(t, repo.Review(ctx, &missing, 1), domain.KindNotFound, "not_found")
}

// TestKnowledgeRepositoryListReads covers the version list with its filters and
// keyset pages, the approved-only injection read and the global read (default
// approved, version/kind filters, keyset page and version-name join).
func TestKnowledgeRepositoryListReads(t *testing.T) {
	ctx := context.Background()
	_, repo, versions := openKnowledgeTestDB(t)
	first := insertVersion(t, versions, domain.Version{
		ID: 1001, Name: "Order v2", CreatedBy: knowledgeTestCreator, UpdatedBy: knowledgeTestCreator,
	})
	second := insertVersion(t, versions, domain.Version{
		ID: 1002, Name: "Shop v3", CreatedBy: knowledgeTestCreator, UpdatedBy: knowledgeTestCreator,
	})
	insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6101, VersionID: first.ID, Kind: domain.KnowledgeKindPattern,
		Title: "Approved A", Body: "Approved body A.", Status: domain.KnowledgeApproved,
		Confidence: domain.KnowledgeConfidenceHigh, CreatedBy: knowledgeTestCreator, Revision: 2,
	})
	insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6102, VersionID: first.ID, Kind: domain.KnowledgeKindPitfall,
		Title: "Draft A", Body: "Draft body A.", Status: domain.KnowledgeDraft,
		Confidence: domain.KnowledgeConfidenceMedium, CreatedBy: knowledgeTestCreator, Revision: 1,
	})
	insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6103, VersionID: first.ID, Kind: domain.KnowledgeKindDecision,
		Title: "Rejected A", Body: "Rejected body A.", Status: domain.KnowledgeRejected,
		Confidence: domain.KnowledgeConfidenceLow, CreatedBy: 8, Revision: 2,
	})
	insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6104, VersionID: second.ID, Kind: domain.KnowledgeKindCaseIdea,
		Title: "Approved B", Body: "Approved body B.", Status: domain.KnowledgeApproved,
		Confidence: domain.KnowledgeConfidenceMedium, CreatedBy: 8, Revision: 2,
	})
	insertKnowledge(t, repo, domain.KnowledgeEntry{
		ID: 6105, VersionID: first.ID, Kind: domain.KnowledgeKindPitfall,
		Title: "Approved pitfall", Body: "Approved pitfall body.", Status: domain.KnowledgeApproved,
		Confidence: domain.KnowledgeConfidenceHigh, CreatedBy: 8, Revision: 2,
	})

	// The injection read is approved-only, ordered by id and capped.
	approved, err := repo.ListApproved(ctx, first.ID, 10)
	if err != nil {
		t.Fatalf("ListApproved: %v", err)
	}
	if got := knowledgeIDs(approved); len(got) != 2 || got[0] != 6101 || got[1] != 6105 {
		t.Fatalf("approved ids = %v, want [6101 6105]", knowledgeIDs(approved))
	}
	for _, entry := range approved {
		if entry.Status != domain.KnowledgeApproved || entry.VersionName != first.Name {
			t.Fatalf("approved entry = %+v", entry)
		}
	}
	capped, err := repo.ListApproved(ctx, first.ID, 1)
	if err != nil {
		t.Fatalf("ListApproved capped: %v", err)
	}
	if got := knowledgeIDs(capped); len(got) != 1 || got[0] != 6101 {
		t.Fatalf("capped approved ids = %v, want [6101]", knowledgeIDs(capped))
	}
	if none, err := repo.ListApproved(ctx, first.ID, 0); err != nil || len(none) != 0 {
		t.Fatalf("ListApproved limit 0 = %v/%v", none, err)
	}

	// The version list filters by status and kind and pages by id.
	statusPage, more, err := repo.List(ctx, domain.KnowledgeFilter{
		VersionID: first.ID, Status: domain.KnowledgeApproved, Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("List approved: %v", err)
	}
	if got := knowledgeIDs(statusPage); len(got) != 2 || got[0] != 6101 || got[1] != 6105 || more {
		t.Fatalf("version approved page = %v more=%v", knowledgeIDs(statusPage), more)
	}
	kindPage, more, err := repo.List(ctx, domain.KnowledgeFilter{
		VersionID: first.ID, Status: domain.KnowledgeApproved, Kind: domain.KnowledgeKindPitfall,
		Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("List kind: %v", err)
	}
	if got := knowledgeIDs(kindPage); len(got) != 1 || got[0] != 6105 || more {
		t.Fatalf("version kind page = %v more=%v", knowledgeIDs(kindPage), more)
	}
	firstPage, more, err := repo.List(ctx, domain.KnowledgeFilter{
		VersionID: first.ID, Page: domain.Page{Limit: 2},
	})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if got := knowledgeIDs(firstPage); len(got) != 2 || got[0] != 6101 || got[1] != 6102 || !more {
		t.Fatalf("version first page = %v more=%v", knowledgeIDs(firstPage), more)
	}
	secondPage, more, err := repo.List(ctx, domain.KnowledgeFilter{
		VersionID: first.ID,
		Page:      domain.Page{Limit: 2, Cursor: platform.EncodeCursor(firstPage[len(firstPage)-1].ID)},
	})
	if err != nil {
		t.Fatalf("List second page: %v", err)
	}
	if got := knowledgeIDs(secondPage); len(got) != 2 || got[0] != 6103 || got[1] != 6105 || more {
		t.Fatalf("version second page = %v more=%v", knowledgeIDs(secondPage), more)
	}

	// A non-admin viewer sees approved entries plus only their own
	// drafts/rejections; nil is the admin/server read of every creator.
	viewer7 := int64(knowledgeTestCreator)
	restricted, more, err := repo.List(ctx, domain.KnowledgeFilter{
		VersionID: first.ID, ViewerID: &viewer7, Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("List non-admin creator 7: %v", err)
	}
	if got := knowledgeIDs(restricted); len(got) != 3 || got[0] != 6101 || got[1] != 6102 || got[2] != 6105 || more {
		t.Fatalf("creator 7 list = %v more=%v", knowledgeIDs(restricted), more)
	}
	viewer8 := int64(8)
	restrictedOther, more, err := repo.List(ctx, domain.KnowledgeFilter{
		VersionID: first.ID, ViewerID: &viewer8, Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("List non-admin creator 8: %v", err)
	}
	if got := knowledgeIDs(restrictedOther); len(got) != 3 || got[0] != 6101 || got[1] != 6103 || got[2] != 6105 || more {
		t.Fatalf("creator 8 list = %v more=%v", knowledgeIDs(restrictedOther), more)
	}
	restrictedDrafts, more, err := repo.List(ctx, domain.KnowledgeFilter{
		VersionID: first.ID, Status: domain.KnowledgeDraft, ViewerID: &viewer8, Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("List non-admin drafts: %v", err)
	}
	if got := knowledgeIDs(restrictedDrafts); len(got) != 0 || more {
		t.Fatalf("creator 8 drafts = %v more=%v", knowledgeIDs(restrictedDrafts), more)
	}

	// The global read defaults to approved, filters by version and kind, keeps
	// the owning version name and pages by id.
	global, more, err := repo.ListGlobal(ctx, domain.GlobalKnowledgeFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatalf("ListGlobal: %v", err)
	}
	if got := knowledgeIDs(global); len(got) != 3 || got[0] != 6101 || got[1] != 6104 || got[2] != 6105 || more {
		t.Fatalf("global approved = %v more=%v", knowledgeIDs(global), more)
	}
	if global[1].VersionName != second.Name || global[0].VersionName != first.Name {
		t.Fatalf("global version names = %q/%q", global[0].VersionName, global[1].VersionName)
	}
	globalVersion, more, err := repo.ListGlobal(ctx, domain.GlobalKnowledgeFilter{
		VersionID: &first.ID, Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListGlobal version: %v", err)
	}
	if got := knowledgeIDs(globalVersion); len(got) != 2 || got[0] != 6101 || got[1] != 6105 || more {
		t.Fatalf("global version filter = %v more=%v", knowledgeIDs(globalVersion), more)
	}
	globalKind, more, err := repo.ListGlobal(ctx, domain.GlobalKnowledgeFilter{
		Kind: domain.KnowledgeKindPitfall, Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListGlobal kind: %v", err)
	}
	if got := knowledgeIDs(globalKind); len(got) != 1 || got[0] != 6105 || more {
		t.Fatalf("global kind filter = %v more=%v", knowledgeIDs(globalKind), more)
	}
	globalFirst, more, err := repo.ListGlobal(ctx, domain.GlobalKnowledgeFilter{Page: domain.Page{Limit: 2}})
	if err != nil {
		t.Fatalf("ListGlobal first page: %v", err)
	}
	if got := knowledgeIDs(globalFirst); len(got) != 2 || got[0] != 6101 || got[1] != 6104 || !more {
		t.Fatalf("global first page = %v more=%v", knowledgeIDs(globalFirst), more)
	}
	globalNext, more, err := repo.ListGlobal(ctx, domain.GlobalKnowledgeFilter{
		Page: domain.Page{Limit: 2, Cursor: platform.EncodeCursor(globalFirst[len(globalFirst)-1].ID)},
	})
	if err != nil {
		t.Fatalf("ListGlobal second page: %v", err)
	}
	if got := knowledgeIDs(globalNext); len(got) != 1 || got[0] != 6105 || more {
		t.Fatalf("global second page = %v more=%v", knowledgeIDs(globalNext), more)
	}

	// The admin queue can list a non-approved status per version.
	draftQueue, more, err := repo.ListGlobal(ctx, domain.GlobalKnowledgeFilter{
		Status: domain.KnowledgeDraft, Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListGlobal draft: %v", err)
	}
	if got := knowledgeIDs(draftQueue); len(got) != 1 || got[0] != 6102 || more {
		t.Fatalf("global draft queue = %v more=%v", knowledgeIDs(draftQueue), more)
	}
}

// knowledgeInt64Ptr returns a pointer to id.
func knowledgeInt64Ptr(id int64) *int64 { return &id }
