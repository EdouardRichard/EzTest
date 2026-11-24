package resourceapp

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// newAIRegistrationService wires the service over the in-memory fake repo with
// the shared deterministic clock/ids (testNow / seqIDs).
func newAIRegistrationService(repo *fakeRepo) *Service {
	return New(repo, &seqIDs{next: 5000}, fixedClock{at: testNow}, nil, nil, nil)
}

func aiRegistrationSUT() resource.SUT {
	return resource.SUT{
		ID: 9001, EnvironmentID: 7001, Name: "订单系统",
		BaseURL: "https://orders.example.com",
		Source:  resource.SUTSourceAdmin, Status: resource.SUTStatusApproved,
	}
}

func requireConflictCode(t *testing.T, err error, code string) {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a typed platform error", err)
	}
	if apiErr.Status != 409 || apiErr.Code != code {
		t.Fatalf("error = %+v, want 409 %s", apiErr, code)
	}
}

// proposeAndRejectPage creates a proposed page through the real proposal path and
// rejects it through the real admin reject path, so UpdatedAt is the ACTUAL
// rejection timestamp (testNow) the freshness rule measures against.
func proposeAndRejectPage(t *testing.T, svc *Service, url string) PageView {
	t.Helper()
	proposed, err := svc.CreateProposedPage(context.Background(), ProposedPageInput{
		SUTID: 9001, Name: "订单页", URL: url,
	})
	if err != nil {
		t.Fatalf("propose page: %v", err)
	}
	id, err := strconv.ParseInt(proposed.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse proposed page id: %v", err)
	}
	rejected, err := svc.RejectPage(context.Background(), id, 1)
	if err != nil {
		t.Fatalf("reject page: %v", err)
	}
	if rejected.Status != resource.PageStatusRejected {
		t.Fatalf("page status = %q, want rejected", rejected.Status)
	}
	return rejected
}

// TestRegisterAIPageIdempotentApprovedHit: a retry (cross-context fact-write
// failure replay) resolves the SAME approved row without creating a duplicate,
// and reports the row's actual source (an admin-created page stays admin).
func TestRegisterAIPageIdempotentApprovedHit(t *testing.T) {
	repo := &fakeRepo{suts: []resource.SUT{aiRegistrationSUT()}}
	svc := newAIRegistrationService(repo)
	ctx := context.Background()
	existing, err := svc.CreatePage(ctx, 9001, PageInput{Name: "订单页", URL: "https://orders.example.com/orders"}, 1)
	if err != nil {
		t.Fatalf("create admin page: %v", err)
	}
	createsBefore := repo.createPageCalls

	view, err := svc.RegisterAIPage(ctx, 9001, PageInput{Name: "订单页", URL: "https://orders.example.com/orders"}, 3001, testNow)
	if err != nil {
		t.Fatalf("RegisterAIPage: %v", err)
	}
	if view.ID != existing.ID {
		t.Fatalf("idempotent hit returned id %s, want the existing %s", view.ID, existing.ID)
	}
	if view.Source != resource.PageSourceAdmin || view.Status != resource.PageStatusApproved {
		t.Fatalf("hit reports %s/%s, want the ACTUAL row admin/approved", view.Source, view.Status)
	}
	if repo.createPageCalls != createsBefore || len(repo.pages) != 1 {
		t.Fatalf("idempotent hit created a duplicate: creates=%d rows=%d", repo.createPageCalls, len(repo.pages))
	}
}

// TestRegisterAIPageProposedApprovedOnProof: a proposed page (no human judgment
// yet) is approved on proof, in place.
func TestRegisterAIPageProposedApprovedOnProof(t *testing.T) {
	repo := &fakeRepo{suts: []resource.SUT{aiRegistrationSUT()}}
	svc := newAIRegistrationService(repo)
	ctx := context.Background()
	proposed, err := svc.CreateProposedPage(ctx, ProposedPageInput{SUTID: 9001, Name: "订单页", URL: "https://orders.example.com/orders"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	view, err := svc.RegisterAIPage(ctx, 9001, PageInput{Name: "订单页", URL: "https://orders.example.com/orders"}, 3001, testNow)
	if err != nil {
		t.Fatalf("RegisterAIPage: %v", err)
	}
	if view.ID != proposed.ID || view.Status != resource.PageStatusApproved || view.Source != resource.PageSourceAI {
		t.Fatalf("proposed page must be approved in place as ai: %+v", view)
	}
	if len(repo.pages) != 1 {
		t.Fatalf("rows = %d, want 1 (in-place approval)", len(repo.pages))
	}
}

// TestRegisterAIPageRejectedOldEvidenceNotRevived pins the conservative revival
// rule: replayed evidence OLDER than (or as old as) the rejection never overturns
// the human reject — the registration fails closed and the row stays rejected.
// A zero provenAt (no timestamps) is treated as old evidence.
func TestRegisterAIPageRejectedOldEvidenceNotRevived(t *testing.T) {
	repo := &fakeRepo{suts: []resource.SUT{aiRegistrationSUT()}}
	svc := newAIRegistrationService(repo)
	ctx := context.Background()
	rejected := proposeAndRejectPage(t, svc, "https://orders.example.com/orders")

	for _, provenAt := range []time.Time{testNow.Add(-time.Hour), testNow, {}} {
		_, err := svc.RegisterAIPage(ctx, 9001, PageInput{Name: "订单页", URL: "https://orders.example.com/orders"}, 3001, provenAt)
		requireConflictCode(t, err, "rejected_requires_fresh_proof")
	}
	if repo.pages[0].Status != resource.PageStatusRejected || repo.pages[0].ID != rejectedID(t, rejected) {
		t.Fatalf("old evidence revived the rejected page: %+v", repo.pages[0])
	}
}

// TestRegisterAIPageRejectedFreshProofRevives: evidence created AFTER the
// rejection is fresh proof and auto-approves the rejected page in place.
func TestRegisterAIPageRejectedFreshProofRevives(t *testing.T) {
	repo := &fakeRepo{suts: []resource.SUT{aiRegistrationSUT()}}
	svc := newAIRegistrationService(repo)
	ctx := context.Background()
	rejected := proposeAndRejectPage(t, svc, "https://orders.example.com/orders")

	view, err := svc.RegisterAIPage(ctx, 9001, PageInput{Name: "订单页", URL: "https://orders.example.com/orders"}, 3001, testNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("fresh proof must revive the rejected page: %v", err)
	}
	if view.ID != rejected.ID || view.Status != resource.PageStatusApproved || view.Source != resource.PageSourceAI {
		t.Fatalf("fresh proof must approve in place as ai: %+v", view)
	}
	if len(repo.pages) != 1 {
		t.Fatalf("rows = %d, want 1 (in-place revival)", len(repo.pages))
	}
}

func rejectedID(t *testing.T, view PageView) int64 {
	t.Helper()
	id, err := strconv.ParseInt(view.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse id: %v", err)
	}
	return id
}

// TestRegisterAIAPIRejectedFreshProofRule pins both sides of the revival rule on
// the API path: old evidence keeps the reject; fresh evidence approves in place.
func TestRegisterAIAPIRejectedFreshProofRule(t *testing.T) {
	repo := &fakeRepo{suts: []resource.SUT{aiRegistrationSUT()}}
	svc := newAIRegistrationService(repo)
	ctx := context.Background()
	proposed, err := svc.CreateProposedAPI(ctx, ProposedAPIInput{SUTID: 9001, Method: "POST", PathTemplate: "/api/orders", Name: "创建订单"})
	if err != nil {
		t.Fatalf("propose api: %v", err)
	}
	apiID, err := strconv.ParseInt(proposed.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse api id: %v", err)
	}
	if _, err := svc.RejectAPI(ctx, apiID, 1); err != nil {
		t.Fatalf("reject api: %v", err)
	}

	input := APIInput{Method: "POST", PathTemplate: "/api/orders", Name: "创建订单"}
	_, err = svc.RegisterAIAPI(ctx, 9001, input, 3001, testNow.Add(-time.Hour))
	requireConflictCode(t, err, "rejected_requires_fresh_proof")
	if repo.apis[0].Status != resource.APIStatusRejected {
		t.Fatalf("old evidence revived the rejected api: %+v", repo.apis[0])
	}

	view, err := svc.RegisterAIAPI(ctx, 9001, input, 3001, testNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("fresh proof must revive the rejected api: %v", err)
	}
	if view.ID != proposed.ID || view.Status != resource.APIStatusApproved || view.Source != resource.APISourceAI {
		t.Fatalf("fresh proof must approve in place as ai: %+v", view)
	}
	if len(repo.apis) != 1 {
		t.Fatalf("rows = %d, want 1 (in-place revival)", len(repo.apis))
	}
}

// TestRegisterAIPageNewCreate: an unregistered proven page is created as
// ai/approved with the derived route template.
func TestRegisterAIPageNewCreate(t *testing.T) {
	repo := &fakeRepo{suts: []resource.SUT{aiRegistrationSUT()}}
	svc := newAIRegistrationService(repo)
	view, err := svc.RegisterAIPage(context.Background(), 9001, PageInput{Name: "订单页", URL: "https://orders.example.com/orders"}, 3001, testNow)
	if err != nil {
		t.Fatalf("RegisterAIPage: %v", err)
	}
	if view.Source != resource.PageSourceAI || view.Status != resource.PageStatusApproved || view.RouteTemplate == "" {
		t.Fatalf("new proven page = %+v, want ai/approved with a derived route", view)
	}
}

// TestRegisterAIDatabaseIdentityConflict pins I-1: a same-name database with a
// different DSN (or username) is NOT the asset the proof touched — the
// registration is a 409 identity conflict, the stored row is untouched, and no
// second row is created. The candidate's description is never silently dropped.
func TestRegisterAIDatabaseIdentityConflict(t *testing.T) {
	dsnA := "postgres://db-a.example.com:5432/orders"
	dsnB := "postgres://db-b.example.com:5432/orders"
	cases := []struct {
		name     string
		input    DatabaseInput
		wantCode string
	}{
		{"different dsn", DatabaseInput{Name: "order_db", DSN: dsnB, Username: "reader"}, "database_identity_conflict"},
		{"different username", DatabaseInput{Name: "order_db", DSN: dsnA, Username: "writer"}, "database_identity_conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{
				suts: []resource.SUT{aiRegistrationSUT()},
				databases: []resource.TargetDatabase{{
					ID: 1501, SUTID: 9001, Name: "order_db", Kind: "postgres",
					DSN: dsnA, Username: "reader", Status: resource.StatusActive,
				}},
			}
			svc := newAIRegistrationService(repo)
			_, err := svc.RegisterAIDatabase(context.Background(), 9001, tc.input, 3001)
			requireConflictCode(t, err, tc.wantCode)
			if len(repo.databases) != 1 || repo.databases[0].DSN != dsnA || repo.databases[0].Username != "reader" {
				t.Fatalf("identity conflict must leave the stored row untouched: %+v", repo.databases)
			}
		})
	}
}

// TestRegisterAIDatabaseDisabledConflict: a disabled same-identity database is an
// explicit 409 — registration never silently treats it as a successful
// registration nor reactivates it.
func TestRegisterAIDatabaseDisabledConflict(t *testing.T) {
	dsn := "postgres://db-a.example.com:5432/orders"
	repo := &fakeRepo{
		suts: []resource.SUT{aiRegistrationSUT()},
		databases: []resource.TargetDatabase{{
			ID: 1501, SUTID: 9001, Name: "order_db", Kind: "postgres",
			DSN: dsn, Username: "reader", Status: resource.StatusDisabled,
		}},
	}
	svc := newAIRegistrationService(repo)
	_, err := svc.RegisterAIDatabase(context.Background(), 9001, DatabaseInput{Name: "order_db", DSN: dsn, Username: "reader"}, 3001)
	requireConflictCode(t, err, "database_disabled")
	if repo.databases[0].Status != resource.StatusDisabled {
		t.Fatalf("registration reactivated a disabled database: %+v", repo.databases[0])
	}
}

// TestRegisterAIDatabaseIdempotentSameIdentity: a same-name, same-DSN/username
// active row is returned unchanged (retry no-dup), including the case-insensitive
// name key of the schema; a genuinely new database is created active.
func TestRegisterAIDatabaseIdempotentSameIdentity(t *testing.T) {
	dsn := "postgres://db-a.example.com:5432/orders"
	repo := &fakeRepo{
		suts: []resource.SUT{aiRegistrationSUT()},
		databases: []resource.TargetDatabase{{
			ID: 1501, SUTID: 9001, Name: "order_db", Kind: "postgres",
			DSN: dsn, Username: "reader", Status: resource.StatusActive,
		}},
	}
	svc := newAIRegistrationService(repo)
	ctx := context.Background()

	view, err := svc.RegisterAIDatabase(ctx, 9001, DatabaseInput{Name: "Order_DB", DSN: dsn, Username: "reader"}, 3001)
	if err != nil {
		t.Fatalf("same-identity hit: %v", err)
	}
	if view.ID != "1501" || view.Status != resource.StatusActive || len(repo.databases) != 1 {
		t.Fatalf("same-identity hit must return the existing active row: %+v rows=%d", view, len(repo.databases))
	}

	created, err := svc.RegisterAIDatabase(ctx, 9001, DatabaseInput{Name: "stock_db", DSN: "postgres://db-a.example.com:5432/stock", Username: "reader"}, 3001)
	if err != nil {
		t.Fatalf("new database: %v", err)
	}
	if created.Status != resource.StatusActive || len(repo.databases) != 2 {
		t.Fatalf("new proven database must be created active: %+v rows=%d", created, len(repo.databases))
	}
}
