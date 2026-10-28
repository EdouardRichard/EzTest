//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// testTime is the fixed application clock reading the repositories receive in
// their fully-populated entities. It is deliberately in UTC and free of
// sub-second precision so a TIMESTAMPTZ round trip is lossless.
var testTime = time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)

const (
	testVersion = int64(1)
	testActor   = int64(9001)
)

// newCaseNode builds a fully populated draft case node.
func newCaseNode(id, versionID int64, parentID *int64, kind, title string) domain.CaseNode {
	return domain.CaseNode{
		ID:            id,
		VersionID:     versionID,
		ParentID:      parentID,
		Kind:          kind,
		Title:         title,
		Description:   "description",
		Priority:      domain.PriorityP2,
		Preconditions: "signed in",
		TestData:      []byte(`{"sku":"A-1"}`),
		Source:        domain.NodeSourceUserInput,
		Status:        domain.NodeStatusDraft,
		SortOrder:     1,
		Revision:      1,
		CreatedBy:     testActor,
		UpdatedBy:     testActor,
		CreatedAt:     testTime,
		UpdatedAt:     testTime,
	}
}

// newCaseStep builds a fully populated user-input step.
func newCaseStep(id, nodeID int64, seq int) domain.CaseStep {
	return domain.CaseStep{
		ID:        id,
		NodeID:    nodeID,
		Seq:       seq,
		Action:    fmt.Sprintf("action %d", seq),
		Expected:  "expected",
		Data:      "",
		Source:    domain.StepSourceUserInput,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
}

// newCaseAssertion builds an assertion optionally linked to a step.
func newCaseAssertion(id, nodeID int64, stepID *int64, kind string) domain.CaseAssertion {
	return domain.CaseAssertion{
		ID:         id,
		NodeID:     nodeID,
		StepID:     stepID,
		Kind:       kind,
		Target:     "/orders",
		Expression: "response.status == 201",
		Expected:   "created",
		Source:     domain.NodeSourceAIGenerated,
		CreatedAt:  testTime,
		UpdatedAt:  testTime,
	}
}

// newCaseSource builds a case source row.
func newCaseSource(id, versionID, nodeID int64, refKind string, refID *int64, refKey string) domain.CaseSource {
	return domain.CaseSource{
		ID:         id,
		VersionID:  versionID,
		NodeID:     nodeID,
		RefKind:    refKind,
		RefID:      refID,
		RefKey:     refKey,
		Source:     domain.NodeSourceUserInput,
		Confidence: domain.ConfidenceHigh,
		CreatedAt:  testTime,
		UpdatedAt:  testTime,
	}
}

// newReview builds a review round.
func newReview(id, versionID int64, status string, createdAt time.Time) domain.CaseReview {
	return domain.CaseReview{
		ID:         id,
		VersionID:  versionID,
		Status:     status,
		Comment:    "",
		ReviewerID: testActor,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
}

// newFinding builds one review finding.
func newFinding(id, reviewID int64, nodeID *int64, severity, status string) domain.CaseReviewFinding {
	return domain.CaseReviewFinding{
		ID:        id,
		ReviewID:  reviewID,
		NodeID:    nodeID,
		Category:  domain.FindingCoverage,
		Severity:  severity,
		Detail:    fmt.Sprintf("finding %d", id),
		Status:    status,
		Source:    domain.FindingSourceAI,
		CreatedBy: testActor,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
}

// newRun builds a design-run row.
func newRun(id, versionID int64, kind, status string, createdAt time.Time) domain.CaseDesignRun {
	return domain.CaseDesignRun{
		ID:            id,
		VersionID:     versionID,
		Kind:          kind,
		Status:        status,
		Model:         "fake-model",
		PromptVersion: "v1",
		Input:         []byte(`{"mode":"append"}`),
		CreatedBy:     testActor,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
}

// newExport builds an export metadata row.
func newExport(id, versionID int64, format string, baselineID *int64, createdAt time.Time) domain.CaseExport {
	return domain.CaseExport{
		ID:         id,
		VersionID:  versionID,
		Format:     format,
		FilePath:   fmt.Sprintf("versions/%d/draft/%s-%d.md", versionID, format, id),
		FileSHA256: fmt.Sprintf("%064x", id),
		SizeBytes:  128,
		BaselineID: baselineID,
		NodeCount:  2,
		CreatedBy:  testActor,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
}

// newBaseline builds one frozen baseline.
func newBaseline(id, versionID, reviewID int64) domain.CaseBaseline {
	commit := "0123456789abcdef0123456789abcdef01234567"
	return domain.CaseBaseline{
		ID:                 id,
		VersionID:          versionID,
		ReviewID:           reviewID,
		NodeCount:          2,
		CaseCount:          1,
		GraphHash:          fmt.Sprintf("%064x", id),
		CodeBaselineCommit: &commit,
		FrozenBy:           testActor,
		FrozenAt:           testTime,
	}
}

// requireDomainError asserts that err is a *domain.Error with kind and reason.
func requireDomainError(t *testing.T, err error, kind, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s/%s, got nil error", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %T %v is not a *domain.Error", err, err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("error kind=%q reason=%q, want kind=%q reason=%q (%v)",
			domainErr.Kind, domainErr.Reason, kind, reason, err)
	}
}

// requireNoError fails the test when err is non-nil.
func requireNoError(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// assertJSONEqual compares two JSON documents semantically, because JSONB
// normalizes key order and whitespace.
func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

// assertCount asserts a raw row count of a table filtered by column = value.
func assertCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, column string, value any, want int) {
	t.Helper()
	query := fmt.Sprintf("SELECT count(*)::int FROM %s WHERE %s = $1", table, column)
	var got int
	if err := pool.QueryRow(ctx, query, value).Scan(&got); err != nil {
		t.Fatalf("count %s where %s: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("%s count where %s = %v: got %d, want %d", table, column, value, got, want)
	}
}

// mustCreateNode inserts n through repo and fails the test on error.
func mustCreateNode(t *testing.T, repo application.NodeRepository, n domain.CaseNode) domain.CaseNode {
	t.Helper()
	created, err := repo.Create(context.Background(), n)
	requireNoError(t, err, fmt.Sprintf("create node %d", n.ID))
	return created
}

// TestNodeRepositoryOCC covers node create/get/list/update/delete and the
// revision compare-and-swap.
func TestNodeRepositoryOCC(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewNodeRepository(pool)

	node := newCaseNode(1001, testVersion, nil, domain.NodeKindCase, "checkout")
	created := mustCreateNode(t, repo, node)
	if created.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", created.Revision)
	}
	if !created.CreatedAt.Equal(testTime) || !created.UpdatedAt.Equal(testTime) {
		t.Fatalf("created timestamps = %v/%v, want %v", created.CreatedAt, created.UpdatedAt, testTime)
	}
	assertJSONEqual(t, created.TestData, `{"sku":"A-1"}`)

	got, err := repo.Get(ctx, testVersion, node.ID)
	requireNoError(t, err, "get node")
	if got.Title != "checkout" || got.Status != domain.NodeStatusDraft || got.Revision != 1 {
		t.Fatalf("stored node = %+v", got)
	}

	// A stale version scope is a not-found, not a conflict.
	_, err = repo.Get(ctx, testVersion+1, node.ID)
	requireDomainError(t, err, domain.KindNotFound, "node_not_found")

	updated := created
	updated.Title = "checkout v2"
	updated.UpdatedAt = testTime.Add(time.Minute)
	current, err := repo.Update(ctx, updated, created.Revision)
	requireNoError(t, err, "update node")
	if current.Revision != 2 {
		t.Fatalf("revision after update = %d, want 2", current.Revision)
	}
	if current.Title != "checkout v2" || !current.UpdatedAt.Equal(testTime.Add(time.Minute)) {
		t.Fatalf("updated node = %+v", current)
	}

	// The same expected revision must now be stale.
	stale := current
	stale.Title = "stale write"
	_, err = repo.Update(ctx, stale, created.Revision)
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")

	// A stale delete is a revision conflict as well.
	err = repo.Delete(ctx, testVersion, node.ID, created.Revision)
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")

	requireNoError(t, repo.Delete(ctx, testVersion, node.ID, current.Revision), "delete node")
	_, err = repo.Get(ctx, testVersion, node.ID)
	requireDomainError(t, err, domain.KindNotFound, "node_not_found")
}

// TestNodeRepositoryBulkStatusOrderAndCount covers the review-freeze bulk status
// rewrite, the approve-compensation restore, the sort-order rewrite and the
// version counters.
func TestNodeRepositoryBulkStatusOrderAndCount(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewNodeRepository(pool)

	scenario := mustCreateNode(t, repo, newCaseNode(1101, testVersion, nil, domain.NodeKindScenario, "scenario"))
	caseA := mustCreateNode(t, repo, newCaseNode(1102, testVersion, &scenario.ID, domain.NodeKindCase, "case a"))
	caseB := mustCreateNode(t, repo, newCaseNode(1103, testVersion, nil, domain.NodeKindCase, "case b"))
	// Give the nodes distinct sort orders: case a, scenario, case b.
	requireNoError(t, repo.SetSortOrders(ctx, testVersion, []int64{caseA.ID, scenario.ID, caseB.ID}), "set sort orders")

	nodes, err := repo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list nodes")
	if len(nodes) != 3 {
		t.Fatalf("list nodes: got %d, want 3", len(nodes))
	}
	if nodes[0].ID != caseA.ID || nodes[1].ID != scenario.ID || nodes[2].ID != caseB.ID {
		t.Fatalf("sort order = %v, want [%d %d %d]", []int64{nodes[0].ID, nodes[1].ID, nodes[2].ID}, caseA.ID, scenario.ID, caseB.ID)
	}

	nodeCount, caseCount, err := repo.CountByVersion(ctx, testVersion)
	requireNoError(t, err, "count nodes")
	if nodeCount != 3 || caseCount != 2 {
		t.Fatalf("counts = nodes %d cases %d, want 3/2", nodeCount, caseCount)
	}

	affected, err := repo.UpdateStatusByVersion(ctx, testVersion, domain.NodeStatusDraft, domain.NodeStatusReviewed)
	requireNoError(t, err, "update status by version")
	if affected != 3 {
		t.Fatalf("affected rows = %d, want 3", affected)
	}
	for _, id := range []int64{scenario.ID, caseA.ID, caseB.ID} {
		node, err := repo.Get(ctx, testVersion, id)
		requireNoError(t, err, "get node after bulk status")
		if node.Status != domain.NodeStatusReviewed || node.Revision != 2 {
			t.Fatalf("node %d after bulk status = status %q revision %d", id, node.Status, node.Revision)
		}
	}

	// Restore writes status and revision exactly, as the approve compensation
	// path needs.
	restored := []domain.CaseNode{
		{ID: scenario.ID, Status: domain.NodeStatusDraft, Revision: 7},
		{ID: caseA.ID, Status: domain.NodeStatusDraft, Revision: 8},
	}
	requireNoError(t, repo.RestoreStatusByVersion(ctx, testVersion, restored), "restore status")
	for _, want := range restored {
		node, err := repo.Get(ctx, testVersion, want.ID)
		requireNoError(t, err, "get restored node")
		if node.Status != want.Status || node.Revision != want.Revision {
			t.Fatalf("restored node %d = status %q revision %d, want %q/%d", want.ID, node.Status, node.Revision, want.Status, want.Revision)
		}
	}
	untouched, err := repo.Get(ctx, testVersion, caseB.ID)
	requireNoError(t, err, "get untouched node")
	if untouched.Status != domain.NodeStatusReviewed || untouched.Revision != 2 {
		t.Fatalf("untouched node = status %q revision %d", untouched.Status, untouched.Revision)
	}
}

// TestActiveRunUnique covers the per-kind single-flight partial unique index and
// the run status CAS.
func TestActiveRunUnique(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepository(pool)

	run := newRun(1201, 500, domain.RunKindDesign, domain.RunQueued, testTime)
	created, err := repo.Create(ctx, run)
	requireNoError(t, err, "create run")
	if created.Status != domain.RunQueued || !created.CreatedAt.Equal(testTime) {
		t.Fatalf("created run = %+v", created)
	}
	assertJSONEqual(t, created.Input, `{"mode":"append"}`)

	duplicate := newRun(1202, 500, domain.RunKindDesign, domain.RunQueued, testTime)
	_, err = repo.Create(ctx, duplicate)
	requireDomainError(t, err, domain.KindConflict, "run_in_progress")

	// A different kind on the same version is a different single-flight slot.
	review := newRun(1203, 500, domain.RunKindReview, domain.RunQueued, testTime)
	_, err = repo.Create(ctx, review)
	requireNoError(t, err, "create review run")

	// A different version is also independent.
	other := newRun(1204, 501, domain.RunKindDesign, domain.RunQueued, testTime)
	_, err = repo.Create(ctx, other)
	requireNoError(t, err, "create other-version run")

	// queued -> running is a CAS transition and records the patch.
	started := testTime.Add(time.Minute)
	running, err := repo.UpdateStatus(ctx, created.ID, domain.RunQueued, domain.RunRunning, application.RunPatch{
		AgentSessionID: int64Ptr(7001),
		AgentRunID:     int64Ptr(7002),
		StartedAt:      &started,
	})
	requireNoError(t, err, "update run status")
	if running.Status != domain.RunRunning || running.AgentSessionID == nil || *running.AgentSessionID != 7001 {
		t.Fatalf("running run = %+v", running)
	}
	if running.StartedAt == nil || !running.StartedAt.Equal(started) {
		t.Fatalf("started_at = %v, want %v", running.StartedAt, started)
	}

	// Same from-status is stale now.
	_, err = repo.UpdateStatus(ctx, created.ID, domain.RunQueued, domain.RunDone, application.RunPatch{})
	requireDomainError(t, err, domain.KindStateConflict, "run_state_conflict")

	// A missing run is not-found.
	_, err = repo.UpdateStatus(ctx, 999999, domain.RunQueued, domain.RunRunning, application.RunPatch{})
	requireDomainError(t, err, domain.KindNotFound, "run_not_found")

	// LatestByKind sees the newest row of the kind.
	latest, err := repo.LatestByKind(ctx, 500, domain.RunKindDesign)
	requireNoError(t, err, "latest by kind")
	if latest.ID != created.ID {
		t.Fatalf("latest run = %d, want %d", latest.ID, created.ID)
	}
	active, err := repo.ActiveByKind(ctx, 500, domain.RunKindReview)
	requireNoError(t, err, "active by kind")
	if active.ID != review.ID {
		t.Fatalf("active run = %d, want %d", active.ID, review.ID)
	}

	// Finishing the run frees the slot and persists the terminal patch,
	// including the JSONB result/stats documents and finished_at.
	finished := testTime.Add(3 * time.Minute)
	doneRun, err := repo.UpdateStatus(ctx, created.ID, domain.RunRunning, domain.RunDone, application.RunPatch{
		Result:     json.RawMessage(`{"cases":2}`),
		Stats:      json.RawMessage(`{"steps":10}`),
		FinishedAt: &finished,
	})
	requireNoError(t, err, "finish run")
	if doneRun.FinishedAt == nil || !doneRun.FinishedAt.Equal(finished) {
		t.Fatalf("finished_at = %v, want %v", doneRun.FinishedAt, finished)
	}
	assertJSONEqual(t, doneRun.Result, `{"cases":2}`)
	assertJSONEqual(t, doneRun.Stats, `{"steps":10}`)
	reused, err := repo.Create(ctx, newRun(1205, 500, domain.RunKindDesign, domain.RunQueued, testTime.Add(2*time.Minute)))
	requireNoError(t, err, "create run after the slot is free")
	if reused.ID != 1205 {
		t.Fatalf("reused run id = %d", reused.ID)
	}

	_, err = repo.Get(ctx, 500, 999999)
	requireDomainError(t, err, domain.KindNotFound, "run_not_found")

	// The run history is newest first with an opaque keyset cursor.
	list, next, hasMore, err := repo.List(ctx, 500, domain.RunKindDesign, "", 1, "")
	requireNoError(t, err, "list runs")
	if len(list) != 1 || list[0].ID != reused.ID {
		t.Fatalf("first run page ids = %v, want [%d]", runIDs(list), reused.ID)
	}
	if !hasMore || next == "" {
		t.Fatalf("first run page hasMore=%v next=%q", hasMore, next)
	}
	secondRun, _, secondMore, err := repo.List(ctx, 500, domain.RunKindDesign, "", 1, next)
	requireNoError(t, err, "list runs second page")
	if len(secondRun) != 1 || secondRun[0].ID != created.ID || secondMore {
		t.Fatalf("second run page ids = %v hasMore=%v, want [%d]/false", runIDs(secondRun), secondMore, created.ID)
	}
}

// TestListActiveOrderingAndLimit covers the worker poll query ordering and
// bound.
func TestListActiveOrderingAndLimit(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepository(pool)

	// Two runs share created_at so the id tie-breaker is exercised.
	first := newRun(1301, 600, domain.RunKindDesign, domain.RunQueued, testTime)
	second := newRun(1302, 601, domain.RunKindReview, domain.RunRunning, testTime)
	third := newRun(1303, 602, domain.RunKindDesign, domain.RunQueued, testTime.Add(time.Minute))
	done := newRun(1304, 603, domain.RunKindDesign, domain.RunDone, testTime.Add(-time.Hour))
	for _, run := range []domain.CaseDesignRun{third, second, first, done} {
		_, err := repo.Create(ctx, run)
		requireNoError(t, err, "create run")
	}

	page, err := repo.ListActive(ctx, 2)
	requireNoError(t, err, "list active")
	if len(page) != 2 || page[0].ID != first.ID || page[1].ID != second.ID {
		t.Fatalf("first active page ids = %v, want [%d %d]", runIDs(page), first.ID, second.ID)
	}

	all, err := repo.ListActive(ctx, 10)
	requireNoError(t, err, "list all active")
	if len(all) != 3 || all[0].ID != first.ID || all[1].ID != second.ID || all[2].ID != third.ID {
		t.Fatalf("active ids = %v, want [%d %d %d]", runIDs(all), first.ID, second.ID, third.ID)
	}

	if _, err := repo.ListActive(ctx, 0); err == nil {
		t.Fatal("ListActive(0) returned no error")
	}
}

// runIDs projects the ids of a run page.
func runIDs(runs []domain.CaseDesignRun) []int64 {
	ids := make([]int64, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	return ids
}

// TestApprovedReviewUnique covers the single approved review per version, the
// latest/approved lookups, the decision CAS and cursor pagination.
func TestApprovedReviewUnique(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewReviewRepository(pool)

	approved, err := repo.Create(ctx, newReview(1401, testVersion, domain.ReviewApproved, testTime))
	requireNoError(t, err, "create approved review")

	_, err = repo.Create(ctx, newReview(1402, testVersion, domain.ReviewApproved, testTime.Add(time.Minute)))
	requireDomainError(t, err, domain.KindConflict, "baseline_frozen")

	// Draft rounds are unlimited and a different version is independent.
	_, err = repo.Create(ctx, newReview(1403, testVersion, domain.ReviewDraft, testTime.Add(2*time.Minute)))
	requireNoError(t, err, "create draft review")
	_, err = repo.Create(ctx, newReview(1405, testVersion, domain.ReviewDraft, testTime.Add(3*time.Minute)))
	requireNoError(t, err, "create newer draft review")
	_, err = repo.Create(ctx, newReview(1404, testVersion+1, domain.ReviewApproved, testTime))
	requireNoError(t, err, "create other-version approved review")

	latest, err := repo.GetLatest(ctx, testVersion)
	requireNoError(t, err, "get latest review")
	if latest.ID != 1405 {
		t.Fatalf("latest review = %d, want 1405", latest.ID)
	}
	gotApproved, err := repo.GetApproved(ctx, testVersion)
	requireNoError(t, err, "get approved review")
	if gotApproved.ID != approved.ID {
		t.Fatalf("approved review = %d, want %d", gotApproved.ID, approved.ID)
	}
	_, err = repo.GetApproved(ctx, testVersion+2)
	requireDomainError(t, err, domain.KindNotFound, "review_not_found")

	// The draft decision is a one-shot CAS and records the actor. It runs on a
	// version without an approved review: a version may hold only one.
	draft, err := repo.Create(ctx, newReview(1406, testVersion+2, domain.ReviewDraft, testTime))
	requireNoError(t, err, "create decidable draft review")
	decided, err := repo.Decide(ctx, draft.ID, domain.ReviewApproved, "looks good", 42)
	requireNoError(t, err, "decide review")
	if decided.Status != domain.ReviewApproved || decided.Comment != "looks good" {
		t.Fatalf("decided review = %+v", decided)
	}
	if decided.DecidedBy == nil || *decided.DecidedBy != 42 || decided.DecidedAt == nil {
		t.Fatalf("decided review actor/at = %v/%v", decided.DecidedBy, decided.DecidedAt)
	}
	// Deciding an already-approved review is a baseline_frozen conflict: the
	// version is frozen once its review is approved.
	_, err = repo.Decide(ctx, draft.ID, domain.ReviewRejected, "", 42)
	requireDomainError(t, err, domain.KindConflict, "baseline_frozen")

	_, err = repo.Decide(ctx, 999999, domain.ReviewApproved, "", 42)
	requireDomainError(t, err, domain.KindNotFound, "review_not_found")

	// Cursor pagination is newest first.
	page, next, hasMore, err := repo.List(ctx, testVersion, 2, "")
	requireNoError(t, err, "list reviews")
	if len(page) != 2 || page[0].ID != 1405 || page[1].ID != 1403 {
		t.Fatalf("first review page ids = %v, want [1405 1403]", reviewIDs(page))
	}
	if !hasMore || next == "" {
		t.Fatalf("first review page hasMore=%v next=%q, want true/non-empty", hasMore, next)
	}
	secondPage, _, secondMore, err := repo.List(ctx, testVersion, 2, next)
	requireNoError(t, err, "list reviews second page")
	if len(secondPage) != 1 || secondPage[0].ID != approved.ID || secondMore {
		t.Fatalf("second review page ids = %v hasMore=%v, want [%d]/false", reviewIDs(secondPage), secondMore, approved.ID)
	}
}

// reviewIDs projects the ids of a review page.
func reviewIDs(reviews []domain.CaseReview) []int64 {
	ids := make([]int64, len(reviews))
	for i, review := range reviews {
		ids[i] = review.ID
	}
	return ids
}

// TestCascadeDeleteScenario proves deleting a scenario cascades through the
// subtree while a version-level finding only loses its node link.
func TestCascadeDeleteScenario(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	nodeRepo := postgres.NewNodeRepository(pool)
	stepRepo := postgres.NewStepRepository(pool)
	assertionRepo := postgres.NewAssertionRepository(pool)
	sourceRepo := postgres.NewSourceRepository(pool)
	reviewRepo := postgres.NewReviewRepository(pool)
	findingRepo := postgres.NewFindingRepository(pool)

	scenario := mustCreateNode(t, nodeRepo, newCaseNode(1501, testVersion, nil, domain.NodeKindScenario, "scenario"))
	child := mustCreateNode(t, nodeRepo, newCaseNode(1502, testVersion, &scenario.ID, domain.NodeKindCase, "case"))

	step1, err := stepRepo.Create(ctx, newCaseStep(1503, child.ID, 1))
	requireNoError(t, err, "create step")
	step2, err := stepRepo.Create(ctx, newCaseStep(1504, child.ID, 2))
	requireNoError(t, err, "create step")
	_, err = assertionRepo.Create(ctx, newCaseAssertion(1505, child.ID, &step1.ID, domain.AssertionKindUI))
	requireNoError(t, err, "create assertion")
	_, err = assertionRepo.Create(ctx, newCaseAssertion(1506, child.ID, &step2.ID, domain.AssertionKindAPI))
	requireNoError(t, err, "create assertion")
	_, err = sourceRepo.Create(ctx, newCaseSource(1507, testVersion, child.ID, domain.RefRequirement, int64Ptr(801), "req-key"))
	requireNoError(t, err, "create source")

	review, err := reviewRepo.Create(ctx, newReview(1508, testVersion, domain.ReviewDraft, testTime))
	requireNoError(t, err, "create review")
	requireNoError(t, findingRepo.CreateBatch(ctx, []domain.CaseReviewFinding{
		newFinding(1509, review.ID, &child.ID, domain.SeverityMajor, domain.FindingOpen),
	}), "create finding")

	requireNoError(t, nodeRepo.Delete(ctx, testVersion, scenario.ID, scenario.Revision), "delete scenario")

	_, err = nodeRepo.Get(ctx, testVersion, scenario.ID)
	requireDomainError(t, err, domain.KindNotFound, "node_not_found")
	_, err = nodeRepo.Get(ctx, testVersion, child.ID)
	requireDomainError(t, err, domain.KindNotFound, "node_not_found")
	assertCount(t, ctx, pool, "case_step", "node_id", child.ID, 0)
	assertCount(t, ctx, pool, "case_assertion", "node_id", child.ID, 0)
	assertCount(t, ctx, pool, "case_source", "node_id", child.ID, 0)

	// The finding survives with a NULL node link (ON DELETE SET NULL).
	findings, err := findingRepo.List(ctx, testVersion, "", "", nil)
	requireNoError(t, err, "list findings")
	if len(findings) != 1 {
		t.Fatalf("findings after cascade = %d, want 1", len(findings))
	}
	if findings[0].NodeID != nil {
		t.Fatalf("finding node id = %v, want nil", findings[0].NodeID)
	}
}

// TestCaseSourceUniqueRef covers the (node, ref_kind, ref_id, ref_key) unique
// index and the requirement-source counter.
func TestCaseSourceUniqueRef(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	nodeRepo := postgres.NewNodeRepository(pool)
	repo := postgres.NewSourceRepository(pool)

	node := mustCreateNode(t, nodeRepo, newCaseNode(1601, testVersion, nil, domain.NodeKindCase, "case"))
	requirementID := int64(801)

	first, err := repo.Create(ctx, newCaseSource(1602, testVersion, node.ID, domain.RefRequirement, &requirementID, "req-801"))
	requireNoError(t, err, "create source")

	// A duplicate of the (node, ref kind, ref id, ref key) source has its own
	// reason so clients can point at the reused requirement.
	_, err = repo.Create(ctx, newCaseSource(1603, testVersion, node.ID, domain.RefRequirement, &requirementID, "req-801"))
	requireDomainError(t, err, domain.KindConflict, "duplicate_case_source")

	// The ref_key is part of the dedup key.
	_, err = repo.Create(ctx, newCaseSource(1604, testVersion, node.ID, domain.RefRequirement, &requirementID, "req-801-alt"))
	requireNoError(t, err, "create source with another ref key")

	// Another ref kind with the same id is a different source.
	_, err = repo.Create(ctx, newCaseSource(1605, testVersion, node.ID, domain.RefAppModel, &requirementID, "req-801"))
	requireNoError(t, err, "create app model source")

	count, err := repo.CountRequirementSources(ctx, node.ID)
	requireNoError(t, err, "count requirement sources")
	if count != 2 {
		t.Fatalf("requirement sources = %d, want 2", count)
	}

	list, err := repo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list sources")
	if len(list) != 3 || list[0].ID != first.ID {
		t.Fatalf("sources = %v", sourceIDs(list))
	}

	requireNoError(t, repo.Delete(ctx, node.ID, first.ID), "delete source")
	count, err = repo.CountRequirementSources(ctx, node.ID)
	requireNoError(t, err, "count requirement sources after delete")
	if count != 1 {
		t.Fatalf("requirement sources after delete = %d, want 1", count)
	}
	err = repo.Delete(ctx, node.ID, first.ID)
	requireDomainError(t, err, domain.KindNotFound, "source_not_found")
}

// sourceIDs projects the ids of a source list.
func sourceIDs(sources []domain.CaseSource) []int64 {
	ids := make([]int64, len(sources))
	for i, source := range sources {
		ids[i] = source.ID
	}
	return ids
}

// TestExportReuseLookup covers the (version, format, baseline) reuse lookup, the
// frozen-baseline unique index and cursor pagination.
func TestExportReuseLookup(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	baselineRepo := postgres.NewBaselineRepository(pool)
	repo := postgres.NewExportRepository(pool)

	baseline, err := baselineRepo.Create(ctx, newBaseline(1701, testVersion, 1702))
	requireNoError(t, err, "create baseline")
	if baseline.VersionID != testVersion || baseline.FrozenAt.IsZero() {
		t.Fatalf("created baseline = %+v", baseline)
	}

	draft := newExport(1703, testVersion, domain.ExportMarkdown, nil, testTime)
	created, err := repo.Create(ctx, draft)
	requireNoError(t, err, "create draft export")
	if created.BaselineID != nil {
		t.Fatalf("draft export baseline = %v, want nil", created.BaselineID)
	}

	reused, err := repo.FindByBaselineFormat(ctx, testVersion, domain.ExportMarkdown, nil)
	requireNoError(t, err, "find draft export")
	if reused.ID != created.ID {
		t.Fatalf("reused export = %d, want %d", reused.ID, created.ID)
	}
	_, err = repo.FindByBaselineFormat(ctx, testVersion, domain.ExportMermaid, nil)
	requireDomainError(t, err, domain.KindNotFound, "export_not_found")

	frozen := newExport(1704, testVersion, domain.ExportMarkdown, &baseline.ID, testTime.Add(time.Minute))
	_, err = repo.Create(ctx, frozen)
	requireNoError(t, err, "create frozen export")
	frozenFound, err := repo.FindByBaselineFormat(ctx, testVersion, domain.ExportMarkdown, &baseline.ID)
	requireNoError(t, err, "find frozen export")
	if frozenFound.ID != frozen.ID {
		t.Fatalf("frozen export = %d, want %d", frozenFound.ID, frozen.ID)
	}

	// A second export of the same frozen (version, format, baseline) is a
	// frozen-state race: export_conflict, backed by uk_case_export_baseline.
	_, err = repo.Create(ctx, newExport(1705, testVersion, domain.ExportMarkdown, &baseline.ID, testTime.Add(2*time.Minute)))
	requireDomainError(t, err, domain.KindConflict, "export_conflict")

	_, err = repo.Get(ctx, testVersion, 999999)
	requireDomainError(t, err, domain.KindNotFound, "export_not_found")

	// Export history is newest first and format-filtered.
	_, err = repo.Create(ctx, newExport(1706, testVersion, domain.ExportOPML, nil, testTime.Add(3*time.Minute)))
	requireNoError(t, err, "create opml export")
	page, next, hasMore, err := repo.List(ctx, testVersion, domain.ExportMarkdown, 10, "")
	requireNoError(t, err, "list exports")
	if len(page) != 2 {
		t.Fatalf("markdown exports = %d, want 2", len(page))
	}
	if hasMore || next != "" {
		t.Fatalf("list exports hasMore=%v next=%q, want false/empty", hasMore, next)
	}
}

// TestBaselineRepositoryFreezeAndUnique covers the one-baseline-per-version
// invariant, the one-baseline-per-review invariant and the delete rollback.
func TestBaselineRepositoryFreezeAndUnique(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewBaselineRepository(pool)

	baseline, err := repo.Create(ctx, newBaseline(1801, testVersion, 1802))
	requireNoError(t, err, "create baseline")

	_, err = repo.Create(ctx, newBaseline(1803, testVersion, 1804))
	requireDomainError(t, err, domain.KindConflict, "baseline_frozen")

	_, err = repo.Create(ctx, newBaseline(1805, testVersion+1, 1802))
	requireDomainError(t, err, domain.KindConflict, "baseline_frozen")

	got, err := repo.GetByVersion(ctx, testVersion)
	requireNoError(t, err, "get baseline")
	if got.ID != baseline.ID || got.GraphHash != baseline.GraphHash {
		t.Fatalf("baseline = %+v", got)
	}
	if got.CodeBaselineCommit == nil || *got.CodeBaselineCommit != *baseline.CodeBaselineCommit {
		t.Fatalf("baseline commit = %v", got.CodeBaselineCommit)
	}

	requireNoError(t, repo.DeleteByVersion(ctx, testVersion), "delete baseline")
	_, err = repo.GetByVersion(ctx, testVersion)
	requireDomainError(t, err, domain.KindNotFound, "baseline_not_found")
	err = repo.DeleteByVersion(ctx, testVersion)
	requireDomainError(t, err, domain.KindNotFound, "baseline_not_found")
}

// TestReviewFindingFlows covers batched finding creation, filtering, status
// updates and the supersede-dismissal of open AI findings.
func TestReviewFindingFlows(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	reviewRepo := postgres.NewReviewRepository(pool)
	nodeRepo := postgres.NewNodeRepository(pool)
	repo := postgres.NewFindingRepository(pool)

	node := mustCreateNode(t, nodeRepo, newCaseNode(1901, testVersion, nil, domain.NodeKindCase, "case"))
	review, err := reviewRepo.Create(ctx, newReview(1902, testVersion, domain.ReviewDraft, testTime))
	requireNoError(t, err, "create review")

	blocker := newFinding(1903, review.ID, &node.ID, domain.SeverityBlocker, domain.FindingOpen)
	open := newFinding(1904, review.ID, nil, domain.SeverityMinor, domain.FindingOpen)
	fixed := newFinding(1905, review.ID, nil, domain.SeverityMajor, domain.FindingFixed)
	human := newFinding(1906, review.ID, nil, domain.SeverityMajor, domain.FindingOpen)
	human.Source = domain.FindingSourceHuman
	requireNoError(t, repo.CreateBatch(ctx, []domain.CaseReviewFinding{blocker, open, fixed, human}), "create findings batch")

	blockers, err := repo.CountOpenBlockers(ctx, testVersion)
	requireNoError(t, err, "count open blockers")
	if blockers != 1 {
		t.Fatalf("open blockers = %d, want 1", blockers)
	}

	if got := findingIDs(mustListFindings(t, ctx, repo, testVersion, domain.FindingOpen, "")); len(got) != 3 {
		t.Fatalf("open findings = %d, want 3", len(got))
	}
	if got := findingIDs(mustListFindings(t, ctx, repo, testVersion, "", domain.SeverityMajor)); len(got) != 2 {
		t.Fatalf("major findings = %d, want 2", len(got))
	}
	nodeScoped := mustListFindings(t, ctx, repo, testVersion, "", "")
	if got := findingIDs(nodeScoped); len(got) != 4 {
		t.Fatalf("all findings = %d, want 4", len(got))
	}
	byNode, err := repo.List(ctx, testVersion, "", "", &node.ID)
	requireNoError(t, err, "list findings by node")
	if len(byNode) != 1 || byNode[0].ID != blocker.ID {
		t.Fatalf("findings by node = %v", findingIDs(byNode))
	}

	updated, err := repo.UpdateStatus(ctx, open.ID, domain.FindingFixed, 42)
	requireNoError(t, err, "update finding status")
	if updated.Status != domain.FindingFixed {
		t.Fatalf("updated finding status = %q", updated.Status)
	}
	_, err = repo.UpdateStatus(ctx, 999999, domain.FindingFixed, 42)
	requireDomainError(t, err, domain.KindNotFound, "finding_not_found")

	// Superseding a review round dismisses the still-open AI findings only.
	dismissed, err := repo.DismissOpenAI(ctx, testVersion)
	requireNoError(t, err, "dismiss openai findings")
	if dismissed != 1 {
		t.Fatalf("dismissed findings = %d, want 1 (only the open AI blocker after the manual fix)", dismissed)
	}
	openLeft := mustListFindings(t, ctx, repo, testVersion, domain.FindingOpen, "")
	if len(openLeft) != 1 || openLeft[0].ID != human.ID {
		t.Fatalf("open findings after dismissal = %v, want only human finding %d", findingIDs(openLeft), human.ID)
	}
	blockers, err = repo.CountOpenBlockers(ctx, testVersion)
	requireNoError(t, err, "count open blockers after dismissal")
	if blockers != 0 {
		t.Fatalf("open blockers after dismissal = %d, want 0", blockers)
	}
}

// TestFindingRepositoryCreateBatchAtomic proves the pool-bound repository wraps
// the whole finding batch in one transaction: a constraint failure on any
// element persists none of the earlier ones.
func TestFindingRepositoryCreateBatchAtomic(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	reviewRepo := postgres.NewReviewRepository(pool)
	repo := postgres.NewFindingRepository(pool)

	review, err := reviewRepo.Create(ctx, newReview(1951, testVersion, domain.ReviewDraft, testTime))
	requireNoError(t, err, "create review")

	valid := newFinding(1952, review.ID, nil, domain.SeverityMinor, domain.FindingOpen)
	// A non-existent review id violates the review_id foreign key after the
	// first (valid) finding of the batch has already been inserted.
	broken := newFinding(1953, 999999, nil, domain.SeverityMinor, domain.FindingOpen)
	err = repo.CreateBatch(ctx, []domain.CaseReviewFinding{valid, broken})
	if err == nil {
		t.Fatal("CreateBatch with a missing review id returned no error")
	}
	assertCount(t, ctx, pool, "case_review_finding", "id", valid.ID, 0)
	assertCount(t, ctx, pool, "case_review_finding", "id", broken.ID, 0)
}

// mustListFindings lists findings and fails on error.
func mustListFindings(t *testing.T, ctx context.Context, repo application.FindingRepository, versionID int64, status, severity string) []domain.CaseReviewFinding {
	t.Helper()
	findings, err := repo.List(ctx, versionID, status, severity, nil)
	requireNoError(t, err, "list findings")
	return findings
}

// findingIDs projects the ids of a finding list.
func findingIDs(findings []domain.CaseReviewFinding) []int64 {
	ids := make([]int64, len(findings))
	for i, finding := range findings {
		ids[i] = finding.ID
	}
	return ids
}

// TestRunRecoverStale covers the startup recovery of runs abandoned by a dead
// worker.
func TestRunRecoverStale(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepository(pool)

	cutoff := testTime.Add(-10 * time.Minute)
	stale := newRun(2001, 900, domain.RunKindDesign, domain.RunQueued, testTime.Add(-time.Hour))
	fresh := newRun(2002, 901, domain.RunKindReview, domain.RunRunning, testTime)
	terminal := newRun(2003, 902, domain.RunKindDesign, domain.RunDone, testTime.Add(-time.Hour))
	for _, run := range []domain.CaseDesignRun{stale, fresh, terminal} {
		_, err := repo.Create(ctx, run)
		requireNoError(t, err, "create run")
	}

	recovered, err := repo.RecoverStale(ctx, cutoff)
	requireNoError(t, err, "recover stale runs")
	if recovered != 1 {
		t.Fatalf("recovered runs = %d, want 1", recovered)
	}

	recoveredStale, err := repo.Get(ctx, stale.VersionID, stale.ID)
	requireNoError(t, err, "get recovered run")
	if recoveredStale.Status != domain.RunFailed {
		t.Fatalf("recovered run status = %q, want failed", recoveredStale.Status)
	}
	if recoveredStale.FinishedAt == nil {
		t.Fatal("recovered run has no finished_at")
	}
	var recoveredReason string
	if err := pool.QueryRow(ctx,
		`SELECT errors->>'reason' FROM case_design_run WHERE id = $1`, stale.ID).Scan(&recoveredReason); err != nil {
		t.Fatalf("read recovered run reason: %v", err)
	}
	if recoveredReason != "worker_restart" {
		t.Fatalf("recovered run reason = %q, want worker_restart", recoveredReason)
	}
	stillFresh, err := repo.Get(ctx, fresh.VersionID, fresh.ID)
	requireNoError(t, err, "get fresh run")
	if stillFresh.Status != domain.RunRunning || stillFresh.FinishedAt != nil {
		t.Fatalf("fresh run = status %q finishedAt %v", stillFresh.Status, stillFresh.FinishedAt)
	}
	stillTerminal, err := repo.Get(ctx, terminal.VersionID, terminal.ID)
	requireNoError(t, err, "get terminal run")
	if stillTerminal.Status != domain.RunDone {
		t.Fatalf("terminal run status = %q", stillTerminal.Status)
	}

	recovered, err = repo.RecoverStale(ctx, cutoff)
	requireNoError(t, err, "recover stale runs twice")
	if recovered != 0 {
		t.Fatalf("second recovery = %d, want 0", recovered)
	}
}

// TestStepAssertionRepositoryCRUD covers step and assertion persistence,
// counters, per-version listing and the 1..N step sequence rewrite.
func TestStepAssertionRepositoryCRUD(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	nodeRepo := postgres.NewNodeRepository(pool)
	stepRepo := postgres.NewStepRepository(pool)
	assertionRepo := postgres.NewAssertionRepository(pool)

	nodeA := mustCreateNode(t, nodeRepo, newCaseNode(2101, testVersion, nil, domain.NodeKindCase, "case a"))
	nodeB := mustCreateNode(t, nodeRepo, newCaseNode(2102, testVersion, nil, domain.NodeKindCase, "case b"))

	step1, err := stepRepo.Create(ctx, newCaseStep(2103, nodeA.ID, 1))
	requireNoError(t, err, "create step 1")
	step2, err := stepRepo.Create(ctx, newCaseStep(2104, nodeA.ID, 2))
	requireNoError(t, err, "create step 2")
	_, err = stepRepo.Create(ctx, newCaseStep(2105, nodeB.ID, 1))
	requireNoError(t, err, "create step for node b")

	// A step written onto a seq another step of the same node holds is a
	// concurrent child-write mismatch: case_revision_conflict.
	_, err = stepRepo.Create(ctx, newCaseStep(2108, nodeA.ID, 1))
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")

	steps, err := stepRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list steps")
	if len(steps) != 3 || steps[0].ID != step1.ID || steps[1].ID != step2.ID || steps[2].ID != 2105 {
		t.Fatalf("steps = %v", stepIDs(steps))
	}
	count, err := stepRepo.CountByNode(ctx, nodeA.ID)
	requireNoError(t, err, "count steps")
	if count != 2 {
		t.Fatalf("steps of node a = %d, want 2", count)
	}

	step1.Action = "action edited"
	step1.UpdatedAt = testTime.Add(time.Minute)
	requireNoError(t, stepRepo.Update(ctx, step1), "update step")
	steps, err = stepRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list steps after update")
	if steps[0].Action != "action edited" {
		t.Fatalf("updated step action = %q", steps[0].Action)
	}

	// Reordering is a full permutation: 2, 1 becomes step2 -> 1, step1 -> 2.
	requireNoError(t, stepRepo.ReplaceSeq(ctx, nodeA.ID, []int64{step2.ID, step1.ID}), "replace step seq")
	steps, err = stepRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list steps after reorder")
	if steps[0].ID != step2.ID || steps[0].Seq != 1 || steps[1].ID != step1.ID || steps[1].Seq != 2 {
		t.Fatalf("reordered steps = %+v", steps)
	}

	// Moving step 1 onto step 2's seq is the same node-level write collision.
	clashing := step1
	clashing.Seq = 1
	requireDomainError(t, stepRepo.Update(ctx, clashing),
		domain.KindStateConflict, "case_revision_conflict")

	// Steps are deletable by (node, step) and a missing row is a step_not_found.
	requireNoError(t, stepRepo.Delete(ctx, nodeA.ID, step2.ID), "delete step")
	count, err = stepRepo.CountByNode(ctx, nodeA.ID)
	requireNoError(t, err, "count steps after delete")
	if count != 1 {
		t.Fatalf("steps after delete = %d, want 1", count)
	}
	err = stepRepo.Delete(ctx, nodeA.ID, step2.ID)
	requireDomainError(t, err, domain.KindNotFound, "step_not_found")

	assertion, err := assertionRepo.Create(ctx, newCaseAssertion(2106, nodeA.ID, &step1.ID, domain.AssertionKindUI))
	requireNoError(t, err, "create assertion")
	_, err = assertionRepo.Create(ctx, newCaseAssertion(2107, nodeB.ID, nil, domain.AssertionKindText))
	requireNoError(t, err, "create vnode b assertion")

	assertions, err := assertionRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list assertions")
	if len(assertions) != 2 {
		t.Fatalf("assertions = %d, want 2", len(assertions))
	}
	count, err = assertionRepo.CountByNode(ctx, nodeA.ID)
	requireNoError(t, err, "count assertions")
	if count != 1 {
		t.Fatalf("assertions of node a = %d, want 1", count)
	}

	assertion.Expected = "updated"
	assertion.UpdatedAt = testTime.Add(2 * time.Minute)
	requireNoError(t, assertionRepo.Update(ctx, assertion), "update assertion")
	assertions, err = assertionRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list assertions after update")
	if assertions[0].Expected != "updated" {
		t.Fatalf("updated assertion expected = %q", assertions[0].Expected)
	}

	requireNoError(t, assertionRepo.Delete(ctx, nodeA.ID, assertion.ID), "delete assertion")
	err = assertionRepo.Delete(ctx, nodeA.ID, assertion.ID)
	requireDomainError(t, err, domain.KindNotFound, "assertion_not_found")
}

// stepIDs projects the ids of a step list.
func stepIDs(steps []domain.CaseStep) []int64 {
	ids := make([]int64, len(steps))
	for i, step := range steps {
		ids[i] = step.ID
	}
	return ids
}

// TestStepRepositoryReplaceSeqFullOccupancy reorders a case whose 30 seq slots
// are all taken: every target slot is occupied, so the repository must still
// rewrite the sequence without dropping the assertions that reference the
// steps.
func TestStepRepositoryReplaceSeqFullOccupancy(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	nodeRepo := postgres.NewNodeRepository(pool)
	stepRepo := postgres.NewStepRepository(pool)
	assertionRepo := postgres.NewAssertionRepository(pool)

	node := mustCreateNode(t, nodeRepo, newCaseNode(2201, testVersion, nil, domain.NodeKindCase, "case"))
	steps := make([]domain.CaseStep, 0, domain.MaxStepsPerCase)
	for i := 1; i <= domain.MaxStepsPerCase; i++ {
		step, err := stepRepo.Create(ctx, newCaseStep(int64(2201*100+i), node.ID, i))
		requireNoError(t, err, "create step")
		steps = append(steps, step)
	}
	_, err := assertionRepo.Create(ctx, newCaseAssertion(2202, node.ID, &steps[0].ID, domain.AssertionKindUI))
	requireNoError(t, err, "create assertion")

	// Rotate the last step to the front: step 30 -> 1, step 1 -> 2, ...,
	// step 29 -> 30. Every row moves into a currently occupied slot.
	ordered := make([]int64, 0, len(steps))
	ordered = append(ordered, steps[len(steps)-1].ID)
	for _, step := range steps[:len(steps)-1] {
		ordered = append(ordered, step.ID)
	}
	requireNoError(t, stepRepo.ReplaceSeq(ctx, node.ID, ordered), "replace full sequence")

	got, err := stepRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list steps")
	if len(got) != len(ordered) {
		t.Fatalf("steps = %d, want %d", len(got), len(ordered))
	}
	for i, step := range got {
		if step.ID != ordered[i] || step.Seq != i+1 {
			t.Fatalf("step %d = id %d seq %d, want id %d seq %d", i, step.ID, step.Seq, ordered[i], i+1)
		}
	}

	// The assertion still points at the same step after the rebuild.
	assertions, err := assertionRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list assertions")
	if len(assertions) != 1 || assertions[0].StepID == nil || *assertions[0].StepID != steps[0].ID {
		t.Fatalf("assertions after rebuild = %+v", assertions)
	}
}

// TestStepRepositoryReplaceSeqLocksNode covers the node row lock taken at the
// start of ReplaceSeq: sequential reorders of the same case both succeed, and a
// missing node is node_not_found before any step lookup can report step_not_found.
func TestStepRepositoryReplaceSeqLocksNode(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	nodeRepo := postgres.NewNodeRepository(pool)
	stepRepo := postgres.NewStepRepository(pool)

	node := mustCreateNode(t, nodeRepo, newCaseNode(2401, testVersion, nil, domain.NodeKindCase, "case"))
	first, err := stepRepo.Create(ctx, newCaseStep(2402, node.ID, 1))
	requireNoError(t, err, "create step 1")
	second, err := stepRepo.Create(ctx, newCaseStep(2403, node.ID, 2))
	requireNoError(t, err, "create step 2")

	// Two sequential reorders exercise the lock path twice and must both commit.
	requireNoError(t, stepRepo.ReplaceSeq(ctx, node.ID, []int64{second.ID, first.ID}), "first reorder")
	requireNoError(t, stepRepo.ReplaceSeq(ctx, node.ID, []int64{first.ID, second.ID}), "second reorder")

	steps, err := stepRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list steps")
	if len(steps) != 2 || steps[0].ID != first.ID || steps[0].Seq != 1 ||
		steps[1].ID != second.ID || steps[1].Seq != 2 {
		t.Fatalf("steps after reorders = %+v", steps)
	}

	// The row lock resolves the node first: a missing node is node_not_found.
	err = stepRepo.ReplaceSeq(ctx, 999999, []int64{first.ID, second.ID})
	requireDomainError(t, err, domain.KindNotFound, "node_not_found")
}

// TestGraphTxInTxCommitsAndRollsBack proves the graph transaction either writes
// the whole graph or nothing.
func TestGraphTxInTxCommitsAndRollsBack(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	graph := postgres.NewGraphTx(pool)

	var _ application.GraphRepository = graph

	node := newCaseNode(2301, testVersion, nil, domain.NodeKindCase, "case")
	err := graph.InTx(ctx, func(tx application.GraphTx) error {
		created, err := tx.Nodes().Create(ctx, node)
		if err != nil {
			return err
		}
		if _, err := tx.Steps().Create(ctx, newCaseStep(2302, created.ID, 1)); err != nil {
			return err
		}
		if _, err := tx.Assertions().Create(ctx, newCaseAssertion(2303, created.ID, nil, domain.AssertionKindAPI)); err != nil {
			return err
		}
		if _, err := tx.Sources().Create(ctx, newCaseSource(2304, testVersion, created.ID, domain.RefRequirement, int64Ptr(901), "req-901")); err != nil {
			return err
		}
		return nil
	})
	requireNoError(t, err, "graph tx commit")

	nodeRepo := postgres.NewNodeRepository(pool)
	stepRepo := postgres.NewStepRepository(pool)
	got, err := nodeRepo.Get(ctx, testVersion, node.ID)
	requireNoError(t, err, "get committed node")
	if got.ID != node.ID {
		t.Fatalf("committed node = %d", got.ID)
	}
	steps, err := stepRepo.ListByVersion(ctx, testVersion)
	requireNoError(t, err, "list committed steps")
	if len(steps) != 1 {
		t.Fatalf("committed steps = %d, want 1", len(steps))
	}

	// A callback error rolls the whole transaction back, nodes included.
	boom := errors.New("boom")
	rolled := newCaseNode(2305, testVersion, nil, domain.NodeKindCase, "rolled back")
	err = graph.InTx(ctx, func(tx application.GraphTx) error {
		if _, err := tx.Nodes().Create(ctx, rolled); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("graph tx error = %v, want boom", err)
	}
	_, err = nodeRepo.Get(ctx, testVersion, rolled.ID)
	requireDomainError(t, err, domain.KindNotFound, "node_not_found")
}

// int64Ptr returns a pointer to value.
func int64Ptr(value int64) *int64 { return &value }
