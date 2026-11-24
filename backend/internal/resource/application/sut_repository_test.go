package resourceapp

import (
	"context"
	"strconv"
	"testing"
)

func testID(t *testing.T, raw string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("parse ID %q: %v", raw, err)
	}
	return value
}

// TestSUTRepositoryManagement pins v0.22 R-C2/API-17: a SUT associates two or
// more global code repositories with a role, the list is deterministic by
// repository ID, the natural key (sut_id, repo_id) is unique and unknown
// repositories are a typed 404.
func TestSUTRepositoryManagement(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	sut := createTestSUT(t, h, "Orders")
	first, err := h.svc.CreateRepository(ctx, RepositoryInput{
		Name: "orders-backend", Path: "https://git.example.com/backend.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository first: %v", err)
	}
	second, err := h.svc.CreateRepository(ctx, RepositoryInput{
		Name: "orders-frontend", Path: "https://git.example.com/frontend.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository second: %v", err)
	}

	list, err := h.svc.ListSUTRepositories(ctx, testID(t, sut.ID))
	if err != nil {
		t.Fatalf("ListSUTRepositories empty: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("initial repositories = %#v", list)
	}

	// Added out of ID order; the read is ordered by repository ID.
	frontend, err := h.svc.AddSUTRepository(ctx, testID(t, sut.ID), SUTRepositoryInput{
		RepositoryID: testID(t, second.ID), Role: "  frontend  ",
	}, 42)
	if err != nil {
		t.Fatalf("AddSUTRepository frontend: %v", err)
	}
	if frontend.Role != "frontend" || !frontend.Enabled || frontend.Repository.ID != second.ID {
		t.Fatalf("frontend association = %#v", frontend)
	}
	if _, err := h.svc.AddSUTRepository(ctx, testID(t, sut.ID), SUTRepositoryInput{
		RepositoryID: testID(t, first.ID), Role: "backend",
	}, 42); err != nil {
		t.Fatalf("AddSUTRepository backend: %v", err)
	}

	list, err = h.svc.ListSUTRepositories(ctx, testID(t, sut.ID))
	if err != nil {
		t.Fatalf("ListSUTRepositories: %v", err)
	}
	if len(list) != 2 || list[0].Repository.ID != first.ID || list[1].Repository.ID != second.ID {
		t.Fatalf("repositories = %#v, want deterministic repo-id order", list)
	}
	if list[0].Role != "backend" || list[1].Role != "frontend" {
		t.Fatalf("roles = %q/%q", list[0].Role, list[1].Role)
	}

	if _, err := h.svc.AddSUTRepository(ctx, testID(t, sut.ID), SUTRepositoryInput{
		RepositoryID: testID(t, first.ID), Role: "duplicate",
	}, 42); err == nil {
		t.Fatal("duplicate SUT repository accepted")
	} else {
		assertAPIError(t, err, 409, "conflict")
	}

	if _, err := h.svc.AddSUTRepository(ctx, testID(t, sut.ID), SUTRepositoryInput{
		RepositoryID: 999999,
	}, 42); err == nil {
		t.Fatal("unknown repository accepted")
	} else {
		assertAPIError(t, err, 404, "not_found")
	}

	if _, err := h.svc.ListSUTRepositories(ctx, 999999); err == nil {
		t.Fatal("missing SUT listing accepted")
	} else {
		assertAPIError(t, err, 404, "not_found")
	}
}

// TestGlobalAccountPool pins DB-15/R-C1-2: test accounts are global, so the
// uniqueness key is the case-insensitive username across the whole pool rather
// than per SUT, and the optional v0.2.4-B sut_id affinity never scopes the read.
func TestGlobalAccountPool(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	firstSUT := createTestSUT(t, h, "Orders")
	secondSUT := createTestSUT(t, h, "Billing")

	firstID := testID(t, firstSUT.ID)
	primary, err := h.svc.CreateAccount(ctx, AccountInput{
		SUTID: &firstID, Username: "Order_Admin", Password: "s3cret", Roles: []string{"admin"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if primary.Username != "Order_Admin" || !primary.PasswordSet {
		t.Fatalf("account = %#v", primary)
	}
	if primary.SUTID != firstSUT.ID {
		t.Fatalf("affinity = %q, want %q", primary.SUTID, firstSUT.ID)
	}

	if _, err := h.svc.CreateAccount(ctx, AccountInput{
		Username: "order_admin", Password: "other",
	}, 42); err == nil {
		t.Fatal("duplicate global username accepted across SUTs")
	} else {
		assertAPIError(t, err, 409, "conflict")
	}

	// An account without affinity is the default: the pool stays global.
	second, err := h.svc.CreateAccount(ctx, AccountInput{
		Username: "billing_admin", Password: "pw", Roles: []string{"billing"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount second: %v", err)
	}
	if second.SUTID != "" {
		t.Fatalf("affinity = %q, want empty", second.SUTID)
	}

	all, err := h.svc.ListAllAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAllAccounts: %v", err)
	}
	if len(all) != 2 || all[0].ID != primary.ID || all[1].ID != second.ID {
		t.Fatalf("global accounts = %#v", all)
	}
	// ListAccounts serves the same global pool; the affinity is a property of
	// each row, not a filter.
	scoped, err := h.svc.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(scoped) != 2 {
		t.Fatalf("listing = %#v, want the global pool", scoped)
	}

	// The affinity can be moved to another SUT and an unknown SUT is a 404.
	secondID := testID(t, secondSUT.ID)
	moved, err := h.svc.UpdateAccount(ctx, testID(t, second.ID), AccountPatch{SUTID: &secondID}, 42)
	if err != nil {
		t.Fatalf("UpdateAccount affinity: %v", err)
	}
	if moved.SUTID != secondSUT.ID {
		t.Fatalf("moved affinity = %q", moved.SUTID)
	}
	missing := int64(999999)
	if _, err := h.svc.UpdateAccount(ctx, testID(t, second.ID), AccountPatch{SUTID: &missing}, 42); err == nil {
		t.Fatal("an unknown affinity SUT was accepted")
	} else {
		assertAPIError(t, err, 404, "not_found")
	}
}

// TestEnvironmentRepositoryPool pins the v0.2.4-B environment code pool (GD-03):
// the union of the repositories of every SUT in the environment, de-duplicated by
// repository ID and ordered by repository ID, each row carrying its resolved
// code baseline.
func TestEnvironmentRepositoryPool(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	first := createTestSUT(t, h, "Orders")
	second := createTestSUT(t, h, "Billing")
	other := createTestEnvironment(t, h, "staging")
	outsider, err := h.svc.CreateSUT(ctx, testID(t, other.ID), SUTInput{
		Name: "Outside", BaseURL: "https://outside.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT in the second environment: %v", err)
	}

	backend, err := h.svc.CreateRepository(ctx, RepositoryInput{
		Name: "backend", Path: "https://git.example.com/backend.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository backend: %v", err)
	}
	frontend, err := h.svc.CreateRepository(ctx, RepositoryInput{
		Name: "frontend", Path: "https://git.example.com/frontend.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository frontend: %v", err)
	}
	shared, err := h.svc.CreateRepository(ctx, RepositoryInput{
		Name: "shared", Path: "https://git.example.com/shared.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository shared: %v", err)
	}

	for _, association := range []struct {
		sut  SUTView
		repo CodeRepositoryView
		role string
	}{
		{second, frontend, "frontend"},
		{first, backend, "backend"},
		{first, shared, ""},
		{second, shared, ""},
		{outsider, backend, "outside"},
	} {
		if _, err := h.svc.AddSUTRepository(ctx, testID(t, association.sut.ID), SUTRepositoryInput{
			RepositoryID: testID(t, association.repo.ID), Role: association.role,
		}, 42); err != nil {
			t.Fatalf("AddSUTRepository(%s): %v", association.repo.Name, err)
		}
	}

	pool, err := h.svc.ListEnvironmentRepositories(ctx, testEnvironmentID)
	if err != nil {
		t.Fatalf("ListEnvironmentRepositories: %v", err)
	}
	// backend < frontend < shared by repository ID; the shared repository appears
	// once even though two SUTs of the environment associate it, and the SUT of
	// the other environment never contributes.
	if len(pool) != 3 {
		t.Fatalf("pool = %#v", pool)
	}
	if pool[0].Repository.ID != backend.ID || pool[1].Repository.ID != frontend.ID || pool[2].Repository.ID != shared.ID {
		t.Fatalf("pool order = %#v", pool)
	}
	if pool[0].SUTID != first.ID || pool[1].SUTID != second.ID {
		t.Fatalf("pool ownership = %#v", pool)
	}
	for _, row := range pool {
		if row.CodeBaselineCommit != "" {
			t.Fatalf("a fresh association must have no baseline: %#v", row)
		}
	}

	otherPool, err := h.svc.ListEnvironmentRepositories(ctx, testID(t, other.ID))
	if err != nil {
		t.Fatalf("ListEnvironmentRepositories second environment: %v", err)
	}
	if len(otherPool) != 1 || otherPool[0].Repository.ID != backend.ID {
		t.Fatalf("second environment pool = %#v", otherPool)
	}
	if _, err := h.svc.ListEnvironmentRepositories(ctx, 999999); err == nil {
		t.Fatal("a missing environment must be a 404")
	} else {
		assertAPIError(t, err, 404, "not_found")
	}
}

// TestSetSUTRepositoryBaselineCommit pins the write path the version binding
// resolution and the codeintel index backfill share.
func TestSetSUTRepositoryBaselineCommit(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	sut := createTestSUT(t, h, "Orders")
	repository, err := h.svc.CreateRepository(ctx, RepositoryInput{
		Name: "backend", Path: "https://git.example.com/backend.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if _, err := h.svc.AddSUTRepository(ctx, testID(t, sut.ID), SUTRepositoryInput{
		RepositoryID: testID(t, repository.ID),
	}, 42); err != nil {
		t.Fatalf("AddSUTRepository: %v", err)
	}

	const commit = "0123456789abcdef0123456789abcdef01234567"
	if err := h.svc.SetSUTRepositoryBaselineCommit(ctx, testID(t, sut.ID), testID(t, repository.ID), commit); err != nil {
		t.Fatalf("SetSUTRepositoryBaselineCommit: %v", err)
	}
	list, err := h.svc.ListSUTRepositories(ctx, testID(t, sut.ID))
	if err != nil {
		t.Fatalf("ListSUTRepositories: %v", err)
	}
	if len(list) != 1 || list[0].CodeBaselineCommit != commit {
		t.Fatalf("association = %#v", list)
	}
	if len(h.repo.baselineWrites) != 1 {
		t.Fatalf("baseline writes = %#v", h.repo.baselineWrites)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.sut_repository.baseline_set" {
		t.Fatalf("event = %q", got)
	}

	// An empty commit clears the baseline back to unresolved.
	if err := h.svc.SetSUTRepositoryBaselineCommit(ctx, testID(t, sut.ID), testID(t, repository.ID), ""); err != nil {
		t.Fatalf("clear baseline: %v", err)
	}
	list, err = h.svc.ListSUTRepositories(ctx, testID(t, sut.ID))
	if err != nil {
		t.Fatalf("ListSUTRepositories after clear: %v", err)
	}
	if list[0].CodeBaselineCommit != "" {
		t.Fatalf("cleared baseline = %q", list[0].CodeBaselineCommit)
	}

	// A malformed SHA is rejected before it reaches the database CHECK.
	if err := h.svc.SetSUTRepositoryBaselineCommit(ctx, testID(t, sut.ID), testID(t, repository.ID), "nope"); err == nil {
		t.Fatal("a malformed commit SHA was accepted")
	} else {
		assertAPIError(t, err, 400, "invalid_argument")
	}
	// A missing association is a 404.
	if err := h.svc.SetSUTRepositoryBaselineCommit(ctx, testID(t, sut.ID), 999999, commit); err == nil {
		t.Fatal("an unknown association was accepted")
	} else {
		assertAPIError(t, err, 404, "not_found")
	}
	if err := h.svc.SetSUTRepositoryBaselineCommit(ctx, 0, testID(t, repository.ID), commit); err == nil {
		t.Fatal("an invalid sut_id was accepted")
	}
}
