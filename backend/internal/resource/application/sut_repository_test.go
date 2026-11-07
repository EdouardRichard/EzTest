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
// than per environment, and the global list is served from any existing SUT.
func TestGlobalAccountPool(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	firstSUT := createTestSUT(t, h, "Orders")
	secondSUT := createTestSUT(t, h, "Billing")

	primary, err := h.svc.CreateAccount(ctx, testID(t, firstSUT.ID), AccountInput{
		Username: "Order_Admin", Password: "s3cret", Roles: []string{"admin"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if primary.Username != "Order_Admin" || !primary.PasswordSet {
		t.Fatalf("account = %#v", primary)
	}

	if _, err := h.svc.CreateAccount(ctx, testID(t, secondSUT.ID), AccountInput{
		Username: "order_admin", Password: "other",
	}, 42); err == nil {
		t.Fatal("duplicate global username accepted across SUTs")
	} else {
		assertAPIError(t, err, 409, "conflict")
	}

	second, err := h.svc.CreateAccount(ctx, testID(t, secondSUT.ID), AccountInput{
		Username: "billing_admin", Password: "pw", Roles: []string{"billing"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount second: %v", err)
	}

	all, err := h.svc.ListAllAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAllAccounts: %v", err)
	}
	if len(all) != 2 || all[0].ID != primary.ID || all[1].ID != second.ID {
		t.Fatalf("global accounts = %#v", all)
	}
	// The legacy SUT-scoped read returns the same global pool; the environment
	// filter no longer exists.
	scoped, err := h.svc.ListAccounts(ctx, testID(t, secondSUT.ID))
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(scoped) != 2 {
		t.Fatalf("SUT-scoped listing = %#v, want the global pool", scoped)
	}
}
