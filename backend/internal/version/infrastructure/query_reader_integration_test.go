//go:build integration

package versionpg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p6PendingDesignFake is the design application port stub used by the version
// reader tests: it records the batch it received and returns canned counts.
type p6PendingDesignFake struct {
	ids    []int64
	counts map[int64]int
	err    error
}

func (f *p6PendingDesignFake) PendingDesignCounts(_ context.Context, versionIDs []int64) (map[int64]int, error) {
	f.ids = append([]int64(nil), versionIDs...)
	return f.counts, f.err
}

func p6VersionID(value int64) *int64 { return &value }

func p6SeedVersion(t *testing.T, pool *pgxpool.Pool, id int64, title, status string, updatedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, title, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, 1, $4, $4)
	`, id, title, status, updatedAt); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

func p6SeedRequirement(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID int64, status, title string, createdAt, updatedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, current_version_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, requirementID, versionID, requirementVersionID, status, createdAt, updatedAt); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash, created_at)
		VALUES ($1, $2, 1, $3, $4, $5)
	`, requirementVersionID, requirementID, title, strings.Repeat("a", 64), createdAt); err != nil {
		t.Fatalf("seed requirement version: %v", err)
	}
}

func TestP6VersionQueryReaderCountRequirementClosure(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)
	p6SeedVersion(t, pool, 1002, "Checkout SUT", "running", base.Add(time.Hour))

	p6SeedRequirement(t, pool, 1001, 2001, 3001, "draft", "draft req", base, base)
	p6SeedRequirement(t, pool, 1001, 2002, 3002, "clarified", "clarified req", base, base)
	p6SeedRequirement(t, pool, 1001, 2003, 3003, "closed", "closed req", base, base)
	p6SeedRequirement(t, pool, 1001, 2004, 3004, "obsolete", "obsolete req", base, base)
	p6SeedRequirement(t, pool, 1002, 2011, 3011, "closed", "other version closed", base, base)
	p6SeedRequirement(t, pool, 1002, 2012, 3012, "draft", "other version draft", base, base)

	num, denom, err := reader.CountRequirementClosure(ctx, nil)
	if err != nil {
		t.Fatalf("CountRequirementClosure global: %v", err)
	}
	if num != 2 || denom != 5 {
		t.Fatalf("global counts = %d/%d, want 2/5 (obsolete excluded)", num, denom)
	}

	num, denom, err = reader.CountRequirementClosure(ctx, p6VersionID(1001))
	if err != nil {
		t.Fatalf("CountRequirementClosure version: %v", err)
	}
	if num != 1 || denom != 3 {
		t.Fatalf("version counts = %d/%d, want 1/3", num, denom)
	}

	num, denom, err = reader.CountRequirementClosure(ctx, p6VersionID(1003))
	if err != nil {
		t.Fatalf("CountRequirementClosure empty version: %v", err)
	}
	if num != 0 || denom != 0 {
		t.Fatalf("empty version counts = %d/%d, want 0/0", num, denom)
	}
}

func TestP6VersionQueryReaderSearchVersions(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base.Add(time.Hour))
	p6SeedVersion(t, pool, 1002, "Login API", "running", base.Add(2*time.Hour))
	p6SeedVersion(t, pool, 1003, "Checkout SUT", "completed", base.Add(3*time.Hour))

	rows, err := reader.SearchVersions(ctx, "login", 2)
	if err != nil {
		t.Fatalf("SearchVersions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (limit)", len(rows))
	}
	if rows[0].ID != 1002 || rows[1].ID != 1001 {
		t.Fatalf("ids = %d,%d, want 1002,1001 (updated_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].Title != "Login API" || rows[0].Status != "running" || !rows[0].UpdatedAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("row = %#v", rows[0])
	}

	rows, err = reader.SearchVersions(ctx, "does-not-exist", 5)
	if err != nil {
		t.Fatalf("SearchVersions empty: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty non-nil slice", rows)
	}
}

func TestP6VersionQueryReaderSearchRequirements(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)
	p6SeedVersion(t, pool, 1002, "Login API", "running", base)
	p6SeedRequirement(t, pool, 1001, 2001, 3001, "clarified", "Email login", base, base.Add(time.Hour))

	// Requirement 2002 has a historical revision that matches q and a current
	// revision that also matches. Only the current revision is searched.
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, current_version_id, status, created_at, updated_at)
		VALUES (2002, 1002, 3002, 'draft', $1, $2)
	`, base, base.Add(3*time.Hour)); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash)
		VALUES (3021, 2002, 1, 'Login retry old', $1)
	`, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed old requirement version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, content_hash)
		VALUES (3002, 2002, 2, 'Login retry', $1)
	`, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed current requirement version: %v", err)
	}

	rows, err := reader.SearchRequirements(ctx, "login", 5)
	if err != nil {
		t.Fatalf("SearchRequirements: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].ID != 2002 || rows[1].ID != 2001 {
		t.Fatalf("ids = %d,%d, want 2002,2001 (updated_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].Title != "Login retry" || rows[0].Status != "draft" {
		t.Fatalf("row = %#v", rows[0])
	}
	if rows[0].VersionID != 1002 || rows[0].VersionTitle != "Login API" {
		t.Fatalf("version fields = %#v", rows[0])
	}
	if rows[1].VersionTitle != "Login SUT" {
		t.Fatalf("version title = %q, want Login SUT", rows[1].VersionTitle)
	}

	limited, err := reader.SearchRequirements(ctx, "login", 1)
	if err != nil {
		t.Fatalf("SearchRequirements limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 2002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6VersionQueryReaderVersionTitles(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)
	p6SeedVersion(t, pool, 1002, "Checkout SUT", "initial", base)

	titles, err := reader.VersionTitles(ctx, []int64{1001, 1002, 9999})
	if err != nil {
		t.Fatalf("VersionTitles: %v", err)
	}
	if len(titles) != 2 || titles[1001] != "Login SUT" || titles[1002] != "Checkout SUT" {
		t.Fatalf("titles = %#v", titles)
	}

	empty, err := reader.VersionTitles(ctx, nil)
	if err != nil {
		t.Fatalf("VersionTitles empty: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("titles = %#v, want empty non-nil map", empty)
	}
}

func TestP6VersionQueryReaderListPendingDesignVersions(t *testing.T) {
	pool := testutil.NewDB(t)
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base.Add(time.Hour))
	p6SeedVersion(t, pool, 1002, "Checkout SUT", "initial", base.Add(2*time.Hour))

	pending := &p6PendingDesignFake{counts: map[int64]int{1001: 2, 1002: 0}}
	reader, err := NewQueryReader(pool, pending)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()

	rows, err := reader.ListPendingDesignVersions(ctx)
	if err != nil {
		t.Fatalf("ListPendingDesignVersions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].VersionID != 1001 || rows[0].VersionTitle != "Login SUT" || rows[0].PendingCount != 2 {
		t.Fatalf("row = %#v", rows[0])
	}
	if len(pending.ids) != 2 || pending.ids[0] != 1002 || pending.ids[1] != 1001 {
		t.Fatalf("port ids = %v, want [1002 1001] (version order)", pending.ids)
	}

	failing, err := NewQueryReader(pool, &p6PendingDesignFake{err: errors.New("boom")})
	if err != nil {
		t.Fatalf("NewQueryReader failing: %v", err)
	}
	if _, err := failing.ListPendingDesignVersions(ctx); err == nil {
		t.Fatal("expected the design port error to propagate")
	}
}

func TestP6VersionQueryReaderListDraftRequirements(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)

	p6SeedRequirement(t, pool, 1001, 2001, 3001, "draft", "Older draft", base, base)
	p6SeedRequirement(t, pool, 1001, 2002, 3002, "draft", "Newer draft", base.Add(time.Hour), base.Add(time.Hour))
	p6SeedRequirement(t, pool, 1001, 2003, 3003, "clarified", "Clarified", base.Add(2*time.Hour), base.Add(2*time.Hour))

	rows, err := reader.ListDraftRequirements(ctx, 10)
	if err != nil {
		t.Fatalf("ListDraftRequirements: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (draft only)", len(rows))
	}
	if rows[0].ID != 2002 || rows[1].ID != 2001 {
		t.Fatalf("ids = %d,%d, want 2002,2001 (created_at DESC)", rows[0].ID, rows[1].ID)
	}
	if rows[0].Title != "Newer draft" || rows[0].VersionID != 1001 || rows[0].VersionTitle != "Login SUT" {
		t.Fatalf("row = %#v", rows[0])
	}

	limited, err := reader.ListDraftRequirements(ctx, 1)
	if err != nil {
		t.Fatalf("ListDraftRequirements limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 2002 {
		t.Fatalf("limited rows = %#v", limited)
	}
}

func TestP6VersionQueryReaderRequiresPoolAndDesignPort(t *testing.T) {
	pool := testutil.NewDB(t)
	if _, err := NewQueryReader(nil, &p6PendingDesignFake{}); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
	if _, err := NewQueryReader(pool, nil); err == nil {
		t.Fatal("expected an error for a missing pending design port")
	}
}

/* ------------------- Task 62 fix round 1: read SQL coverage ------------------ */

// p62FactSeed is one version_fact fixture row. The content hash is derived from
// the row id so the (version, kind, key, content_hash) unique index never
// collides across fixture rows.
type p62FactSeed struct {
	ID           int64
	VersionID    int64
	Kind         string
	Key          string
	Payload      string
	Confidence   *float64
	EvidenceIDs  []int64
	SupersededBy *int64
	CreatedAt    time.Time
}

func p62SeedFact(t *testing.T, pool *pgxpool.Pool, seed p62FactSeed) {
	t.Helper()
	payload := seed.Payload
	if payload == "" {
		payload = "{}"
	}
	evidence := seed.EvidenceIDs
	if evidence == nil {
		evidence = []int64{}
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version_fact (id, version_id, kind, key, payload, confidence,
			source, source_ref, evidence_refs, content_hash, superseded_by, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, 'llm', '', $7, $8, $9, $10)
	`, seed.ID, seed.VersionID, seed.Kind, seed.Key, payload, seed.Confidence,
		evidence, fmt.Sprintf("%064d", seed.ID), seed.SupersededBy, seed.CreatedAt); err != nil {
		t.Fatalf("seed version fact %d: %v", seed.ID, err)
	}
}

// p62PlanItemSeed is one version_plan_item fixture row.
type p62PlanItemSeed struct {
	ID         int64
	VersionID  int64
	Phase      string
	Title      string
	Status     string
	DependsOn  []int64
	WorkItemID *int64
	Decision   string
	Order      int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func p62SeedPlanItem(t *testing.T, pool *pgxpool.Pool, seed p62PlanItemSeed) {
	t.Helper()
	dependsOn := seed.DependsOn
	if dependsOn == nil {
		dependsOn = []int64{}
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version_plan_item (id, version_id, phase, title, status,
			depends_on, work_item_id, decision, sort_order, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, seed.ID, seed.VersionID, seed.Phase, seed.Title, seed.Status, dependsOn,
		seed.WorkItemID, seed.Decision, seed.Order, seed.CreatedAt, seed.UpdatedAt); err != nil {
		t.Fatalf("seed version plan item %d: %v", seed.ID, err)
	}
}

func p62FactIDs(rows []versionapp.FactReadRow) []int64 {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func p62PlanItemIDs(rows []versionapp.PlanItemReadRow) []int64 {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func p62ReadFacts(t *testing.T, reader *QueryReader, query versionapp.FactReadQuery) ([]versionapp.FactReadRow, bool) {
	t.Helper()
	rows, hasMore, err := reader.ListFacts(context.Background(), query)
	if err != nil {
		t.Fatalf("ListFacts(%+v): %v", query, err)
	}
	return rows, hasMore
}

func p62ReadPlanItems(t *testing.T, reader *QueryReader, query versionapp.PlanItemReadQuery) ([]versionapp.PlanItemReadRow, bool) {
	t.Helper()
	rows, hasMore, err := reader.ListPlanItems(context.Background(), query)
	if err != nil {
		t.Fatalf("ListPlanItems(%+v): %v", query, err)
	}
	return rows, hasMore
}

// TestVersionReadFactsSQL executes ListFacts against a migrated schema: current
// rows by default, include_superseded history, kind filter, id-DESC ordering,
// cursor pagination/has_more and the empty page all have to come from the SQL,
// not from the application projection.
func TestVersionReadFactsSQL(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)
	p6SeedVersion(t, pool, 1002, "Checkout SUT", "running", base)

	confidence := 0.9
	supersededBy := int64(5002)
	p62SeedFact(t, pool, p62FactSeed{
		ID: 5001, VersionID: 1001, Kind: "requirement", Key: "login",
		Payload: `{"summary":"email login"}`, SupersededBy: &supersededBy,
		CreatedAt: base,
	})
	p62SeedFact(t, pool, p62FactSeed{
		ID: 5002, VersionID: 1001, Kind: "requirement", Key: "login",
		Payload: `{"summary":"email login v2"}`, Confidence: &confidence,
		EvidenceIDs: []int64{9001, 9002}, CreatedAt: base.Add(time.Hour),
	})
	p62SeedFact(t, pool, p62FactSeed{
		ID: 5003, VersionID: 1001, Kind: "design", Key: "cases",
		Payload: `{"count":3}`, CreatedAt: base.Add(2 * time.Hour),
	})
	p62SeedFact(t, pool, p62FactSeed{
		ID: 5004, VersionID: 1001, Kind: "requirement", Key: "other",
		CreatedAt: base.Add(3 * time.Hour),
	})
	p62SeedFact(t, pool, p62FactSeed{
		ID: 5011, VersionID: 1002, Kind: "requirement", Key: "login",
		CreatedAt: base.Add(4 * time.Hour),
	})

	// Default page: current rows only, id DESC, version scoped.
	rows, hasMore := p62ReadFacts(t, reader, versionapp.FactReadQuery{VersionID: 1001, Limit: 10})
	if ids := p62FactIDs(rows); !slices.Equal(ids, []int64{5004, 5003, 5002}) {
		t.Fatalf("default fact ids = %v, want [5004 5003 5002] (current, id DESC)", ids)
	}
	if hasMore {
		t.Fatal("default has_more = true, want false")
	}
	current := rows[2]
	if current.Payload["summary"] != "email login v2" {
		t.Fatalf("payload = %#v", current.Payload)
	}
	if current.Confidence == nil || *current.Confidence != confidence {
		t.Fatalf("confidence = %#v, want %v", current.Confidence, confidence)
	}
	if !slices.Equal(current.EvidenceIDs, []int64{9001, 9002}) {
		t.Fatalf("evidence = %v, want [9001 9002]", current.EvidenceIDs)
	}
	if current.Source != "llm" || current.SourceRef != "" {
		t.Fatalf("source = %q/%q", current.Source, current.SourceRef)
	}
	if current.ContentHash != fmt.Sprintf("%064d", 5002) {
		t.Fatalf("content hash = %q", current.ContentHash)
	}
	if current.SupersededBy != nil {
		t.Fatalf("current superseded_by = %v, want nil", current.SupersededBy)
	}
	if !current.CreatedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("created_at = %v", current.CreatedAt)
	}
	if rows[1].Payload["count"] != float64(3) {
		t.Fatalf("design payload = %#v", rows[1].Payload)
	}
	if rows[0].Payload == nil || len(rows[0].Payload) != 0 || rows[0].EvidenceIDs == nil {
		t.Fatalf("defaulted payload/evidence = %#v/%#v", rows[0].Payload, rows[0].EvidenceIDs)
	}

	// include_superseded returns the append-only history including the closed row.
	history, _ := p62ReadFacts(t, reader, versionapp.FactReadQuery{VersionID: 1001, IncludeSuperseded: true, Limit: 10})
	if ids := p62FactIDs(history); !slices.Equal(ids, []int64{5004, 5003, 5002, 5001}) {
		t.Fatalf("history ids = %v, want [5004 5003 5002 5001]", ids)
	}
	superseded := history[3]
	if superseded.SupersededBy == nil || *superseded.SupersededBy != 5002 {
		t.Fatalf("superseded_by = %#v, want 5002", superseded.SupersededBy)
	}
	if superseded.Payload["summary"] != "email login" {
		t.Fatalf("superseded payload = %#v", superseded.Payload)
	}

	// kind filter, both with and without the history.
	design, _ := p62ReadFacts(t, reader, versionapp.FactReadQuery{VersionID: 1001, Kind: "design", Limit: 10})
	if ids := p62FactIDs(design); !slices.Equal(ids, []int64{5003}) {
		t.Fatalf("design ids = %v, want [5003]", ids)
	}
	reqHistory, _ := p62ReadFacts(t, reader, versionapp.FactReadQuery{
		VersionID: 1001, Kind: "requirement", IncludeSuperseded: true, Limit: 10,
	})
	if ids := p62FactIDs(reqHistory); !slices.Equal(ids, []int64{5004, 5002, 5001}) {
		t.Fatalf("requirement history ids = %v, want [5004 5002 5001]", ids)
	}

	// Cursor pagination: limit+1 read reports has_more and the cursor resumes.
	page, more := p62ReadFacts(t, reader, versionapp.FactReadQuery{
		VersionID: 1001, IncludeSuperseded: true, Limit: 2,
	})
	if ids := p62FactIDs(page); !slices.Equal(ids, []int64{5004, 5003}) || !more {
		t.Fatalf("first page = %v has_more=%v, want [5004 5003] true", ids, more)
	}
	cursor := page[len(page)-1].ID
	page, more = p62ReadFacts(t, reader, versionapp.FactReadQuery{
		VersionID: 1001, IncludeSuperseded: true, BeforeID: &cursor, Limit: 2,
	})
	if ids := p62FactIDs(page); !slices.Equal(ids, []int64{5002, 5001}) || more {
		t.Fatalf("second page = %v has_more=%v, want [5002 5001] false", ids, more)
	}
	// A page that fills the limit exactly must not report has_more.
	exact, more := p62ReadFacts(t, reader, versionapp.FactReadQuery{
		VersionID: 1001, IncludeSuperseded: true, Limit: 4,
	})
	if len(exact) != 4 || more {
		t.Fatalf("exact page rows = %d has_more=%v, want 4 false", len(exact), more)
	}

	// Empty version: empty non-nil page, no has_more.
	empty, more := p62ReadFacts(t, reader, versionapp.FactReadQuery{VersionID: 1003, Limit: 10})
	if empty == nil || len(empty) != 0 || more {
		t.Fatalf("empty page = %#v has_more=%v, want empty false", empty, more)
	}
}

// TestVersionReadPlanItemsSQL executes ListPlanItems: status filter, depends_on
// and work_item_id projection, the frozen order field (sort_order), id-DESC
// ordering, cursor pagination and the empty page.
func TestVersionReadPlanItemsSQL(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)
	p6SeedVersion(t, pool, 1002, "Checkout SUT", "initial", base)

	workItemID := int64(7001)
	p62SeedPlanItem(t, pool, p62PlanItemSeed{
		ID: 6001, VersionID: 1001, Phase: "design", Title: "Plan cases",
		Status: "pending", Order: 1, CreatedAt: base, UpdatedAt: base.Add(30 * time.Minute),
	})
	p62SeedPlanItem(t, pool, p62PlanItemSeed{
		ID: 6002, VersionID: 1001, Phase: "execute", Title: "Run batch",
		Status: "done", DependsOn: []int64{6001}, WorkItemID: &workItemID,
		Decision: "approved", Order: 2, CreatedAt: base.Add(time.Hour),
		UpdatedAt: base.Add(time.Hour),
	})
	p62SeedPlanItem(t, pool, p62PlanItemSeed{
		ID: 6003, VersionID: 1001, Phase: "execute", Title: "Analyze",
		Status: "done", DependsOn: []int64{6001, 6002}, Order: 3,
		CreatedAt: base.Add(2 * time.Hour), UpdatedAt: base.Add(2 * time.Hour),
	})
	p62SeedPlanItem(t, pool, p62PlanItemSeed{
		ID: 6011, VersionID: 1002, Phase: "design", Title: "Other version",
		Status: "pending", Order: 1, CreatedAt: base.Add(3 * time.Hour),
		UpdatedAt: base.Add(3 * time.Hour),
	})

	rows, hasMore := p62ReadPlanItems(t, reader, versionapp.PlanItemReadQuery{VersionID: 1001, Limit: 10})
	if ids := p62PlanItemIDs(rows); !slices.Equal(ids, []int64{6003, 6002, 6001}) {
		t.Fatalf("plan item ids = %v, want [6003 6002 6001] (id DESC)", ids)
	}
	if hasMore {
		t.Fatal("has_more = true, want false")
	}
	first := rows[2]
	if first.Phase != "design" || first.Title != "Plan cases" || first.Status != "pending" ||
		first.Order != 1 || first.WorkItemID != nil || first.Decision != "" {
		t.Fatalf("plan item = %#v", first)
	}
	if first.DependsOn == nil || len(first.DependsOn) != 0 {
		t.Fatalf("empty depends_on = %#v, want non-nil empty", first.DependsOn)
	}
	if !first.CreatedAt.Equal(base) || !first.UpdatedAt.Equal(base.Add(30*time.Minute)) {
		t.Fatalf("timestamps = %v/%v", first.CreatedAt, first.UpdatedAt)
	}
	second := rows[1]
	if !slices.Equal(second.DependsOn, []int64{6001}) {
		t.Fatalf("depends_on = %v, want [6001]", second.DependsOn)
	}
	if second.WorkItemID == nil || *second.WorkItemID != workItemID || second.Decision != "approved" || second.Order != 2 {
		t.Fatalf("done item = %#v", second)
	}
	if !slices.Equal(rows[0].DependsOn, []int64{6001, 6002}) {
		t.Fatalf("analyze depends_on = %v", rows[0].DependsOn)
	}

	done, _ := p62ReadPlanItems(t, reader, versionapp.PlanItemReadQuery{VersionID: 1001, Status: "done", Limit: 10})
	if ids := p62PlanItemIDs(done); !slices.Equal(ids, []int64{6003, 6002}) {
		t.Fatalf("done ids = %v, want [6003 6002]", ids)
	}
	pending, _ := p62ReadPlanItems(t, reader, versionapp.PlanItemReadQuery{VersionID: 1001, Status: "pending", Limit: 10})
	if ids := p62PlanItemIDs(pending); !slices.Equal(ids, []int64{6001}) {
		t.Fatalf("pending ids = %v, want [6001]", ids)
	}

	page, more := p62ReadPlanItems(t, reader, versionapp.PlanItemReadQuery{VersionID: 1001, Limit: 2})
	if ids := p62PlanItemIDs(page); !slices.Equal(ids, []int64{6003, 6002}) || !more {
		t.Fatalf("first page = %v has_more=%v, want [6003 6002] true", ids, more)
	}
	cursor := page[len(page)-1].ID
	page, more = p62ReadPlanItems(t, reader, versionapp.PlanItemReadQuery{
		VersionID: 1001, BeforeID: &cursor, Limit: 2,
	})
	if ids := p62PlanItemIDs(page); !slices.Equal(ids, []int64{6001}) || more {
		t.Fatalf("second page = %v has_more=%v, want [6001] false", ids, more)
	}

	empty, more := p62ReadPlanItems(t, reader, versionapp.PlanItemReadQuery{VersionID: 1003, Limit: 10})
	if empty == nil || len(empty) != 0 || more {
		t.Fatalf("empty page = %#v has_more=%v, want empty false", empty, more)
	}
}

// TestVersionReadVersionExistsSQL executes the version existence probe used to
// turn a missing/foreign version into a 404 before any fact/plan read.
func TestVersionReadVersionExistsSQL(t *testing.T) {
	pool := testutil.NewDB(t)
	reader, err := NewQueryReader(pool, &p6PendingDesignFake{})
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	p6SeedVersion(t, pool, 1001, "Login SUT", "initial", base)

	exists, err := reader.VersionExists(ctx, 1001)
	if err != nil || !exists {
		t.Fatalf("VersionExists(1001) = %v/%v, want true", exists, err)
	}
	exists, err = reader.VersionExists(ctx, 9999)
	if err != nil || exists {
		t.Fatalf("VersionExists(9999) = %v/%v, want false", exists, err)
	}
	if _, err := reader.VersionExists(ctx, 0); err == nil {
		t.Fatal("expected an error for a non-positive version id")
	}
}
