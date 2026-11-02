package resourceapp

import (
	"context"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

func seedProposalEnvironment(h *harness) {
	h.repo.suts = append(h.repo.suts, resource.SUT{ID: 1001, Name: "Order Center", Intro: "orders", CreatedBy: 42, CreatedAt: testNow, UpdatedAt: testNow})
	h.repo.environments = append(h.repo.environments, resource.Environment{
		ID: 1002, SUTID: 1001, Name: "staging", BaseURL: "https://staging.example.test",
		IsDefault: true, Status: resource.StatusActive, CreatedAt: testNow, UpdatedAt: testNow,
	})
}

func proposedPageInput() ProposedPageInput {
	return ProposedPageInput{
		EnvironmentID: 1002,
		Name:          "Orders list",
		URL:           "https://staging.example.test/orders",
		RouteTemplate: "/orders",
		Source:        resource.PageSourceAI,
	}
}

func TestCreateProposedPage(t *testing.T) {
	h := newHarness()
	seedProposalEnvironment(h)

	view, err := h.svc.CreateProposedPage(context.Background(), proposedPageInput())
	if err != nil {
		t.Fatalf("CreateProposedPage: %v", err)
	}
	if view.Status != resource.PageStatusProposed || view.Source != resource.PageSourceAI || view.RouteTemplate != "/orders" {
		t.Fatalf("page = %+v", view)
	}
	if view.SUTID != "1001" || view.EnvironmentID != "1002" || view.PageType != "page" {
		t.Fatalf("page parents = %+v", view)
	}
	if len(h.repo.pages) != 1 || h.repo.pages[0].Status != resource.PageStatusProposed {
		t.Fatalf("stored pages = %+v", h.repo.pages)
	}
	if len(h.audit.entries) != 1 || h.audit.entries[0].Action != "resource.page.proposed" {
		t.Fatalf("audit = %+v", h.audit.entries)
	}

	// A repeat refresh updates the proposed row instead of inserting a new one.
	updates := proposedPageInput()
	updates.Name = "Orders list (updated)"
	refreshed, err := h.svc.CreateProposedPage(context.Background(), updates)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed.ID != view.ID || refreshed.Name != "Orders list (updated)" {
		t.Fatalf("refreshed = %+v", refreshed)
	}
	if len(h.repo.pages) != 1 {
		t.Fatalf("duplicate proposed page: %+v", h.repo.pages)
	}

	// An approved page is never downgraded to proposed.
	h.repo.pages[0].Status = resource.PageStatusApproved
	approved, err := h.svc.CreateProposedPage(context.Background(), updates)
	if err != nil {
		t.Fatalf("approved skip: %v", err)
	}
	if approved.Status != resource.PageStatusApproved || h.repo.pages[0].Status != resource.PageStatusApproved {
		t.Fatalf("approved page changed: %+v", h.repo.pages[0])
	}

	// A rejected page is not automatically re-proposed (P5 backlog).
	h.repo.pages[0].Status = resource.PageStatusRejected
	rejected, err := h.svc.CreateProposedPage(context.Background(), updates)
	if err != nil {
		t.Fatalf("rejected skip: %v", err)
	}
	if rejected.Status != resource.PageStatusRejected {
		t.Fatalf("rejected page re-proposed: %+v", rejected)
	}
}

func TestCreateProposedPageValidates(t *testing.T) {
	h := newHarness()
	seedProposalEnvironment(h)

	missing := proposedPageInput()
	missing.EnvironmentID = 9999
	_, err := h.svc.CreateProposedPage(context.Background(), missing)
	assertAPIError(t, err, 404, "not_found")

	badURL := proposedPageInput()
	badURL.URL = "not-a-url"
	_, err = h.svc.CreateProposedPage(context.Background(), badURL)
	assertAPIError(t, err, 400, "invalid_argument")

	badSource := proposedPageInput()
	badSource.Source = resource.PageSourceAdmin
	_, err = h.svc.CreateProposedPage(context.Background(), badSource)
	assertAPIError(t, err, 400, "invalid_argument")

	// The route template is derived from the URL when omitted.
	derived := proposedPageInput()
	derived.RouteTemplate = ""
	if view, err := h.svc.CreateProposedPage(context.Background(), derived); err != nil || view.RouteTemplate != "/orders" {
		t.Fatalf("derived route = %+v err=%v", view, err)
	}
}

func TestConfirmAndRejectProposedPage(t *testing.T) {
	h := newHarness()
	seedProposalEnvironment(h)
	created, err := h.svc.CreateProposedPage(context.Background(), proposedPageInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := int64(1001) // seqIDs assigned 1001 to the first page
	if created.ID != "1001" {
		t.Fatalf("page id = %q", created.ID)
	}

	approved, err := h.svc.ConfirmPage(context.Background(), id, 42)
	if err != nil {
		t.Fatalf("ConfirmPage: %v", err)
	}
	if approved.Status != resource.PageStatusApproved {
		t.Fatalf("approved = %+v", approved)
	}
	if _, err := h.svc.ConfirmPage(context.Background(), id, 42); err == nil {
		t.Fatal("repeated confirm must conflict")
	} else {
		assertAPIError(t, err, 409, "conflict")
	}

	second := proposedPageInput()
	second.RouteTemplate = "/orders/new"
	second.URL = "https://staging.example.test/orders/new"
	pending, err := h.svc.CreateProposedPage(context.Background(), second)
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	pendingID := int64(1002)
	if pending.ID != "1002" {
		t.Fatalf("second id = %q", pending.ID)
	}
	rejected, err := h.svc.RejectPage(context.Background(), pendingID, 42)
	if err != nil {
		t.Fatalf("RejectPage: %v", err)
	}
	if rejected.Status != resource.PageStatusRejected {
		t.Fatalf("rejected = %+v", rejected)
	}
	if _, err := h.svc.RejectPage(context.Background(), pendingID, 42); err == nil {
		t.Fatal("repeated reject must conflict")
	}
	if _, err := h.svc.ConfirmPage(context.Background(), 9999, 42); err == nil {
		t.Fatal("missing page must fail")
	} else {
		assertAPIError(t, err, 404, "not_found")
	}

	actions := make([]string, 0, len(h.audit.entries))
	for _, entry := range h.audit.entries {
		actions = append(actions, entry.Action)
	}
	for _, want := range []string{"resource.page.proposed", "resource.page.confirmed", "resource.page.rejected"} {
		found := false
		for _, action := range actions {
			if action == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing audit %q in %v", want, actions)
		}
	}
}

func TestCreateProposedAPIAndLifecycle(t *testing.T) {
	h := newHarness()
	seedProposalEnvironment(h)
	view, err := h.svc.CreateProposedAPI(context.Background(), ProposedAPIInput{
		EnvironmentID: 1002,
		Method:        "POST",
		PathTemplate:  "/api/refund",
		Name:          "Refund",
		Source:        resource.APISourceCode,
	})
	if err != nil {
		t.Fatalf("CreateProposedAPI: %v", err)
	}
	if view.Status != resource.APIStatusProposed || view.Source != resource.APISourceCode || view.Method != "POST" {
		t.Fatalf("api = %+v", view)
	}
	if view.SUTID != "1001" || view.EnvironmentID != "1002" {
		t.Fatalf("api parents = %+v", view)
	}

	approved, err := h.svc.ConfirmAPI(context.Background(), 1001, 42)
	if err != nil {
		t.Fatalf("ConfirmAPI: %v", err)
	}
	if approved.Status != resource.APIStatusApproved {
		t.Fatalf("approved = %+v", approved)
	}
	if _, err := h.svc.ConfirmAPI(context.Background(), 1001, 42); err == nil {
		t.Fatal("repeated confirm must conflict")
	}

	second, err := h.svc.CreateProposedAPI(context.Background(), ProposedAPIInput{
		EnvironmentID: 1002, Method: "GET", PathTemplate: "/api/orders", Name: "Orders",
		Source: resource.APISourceAI,
	})
	if err != nil {
		t.Fatalf("CreateProposedAPI second: %v", err)
	}
	if second.Status != resource.APIStatusProposed {
		t.Fatalf("second = %+v", second)
	}
	// Refresh keeps an existing proposed API on its row and fields.
	refreshed, err := h.svc.CreateProposedAPI(context.Background(), ProposedAPIInput{
		EnvironmentID: 1002, Method: "GET", PathTemplate: "/api/orders", Name: "Orders v2",
		Source: resource.APISourceAI,
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed.ID != second.ID || refreshed.Name != "Orders v2" || refreshed.Status != resource.APIStatusProposed {
		t.Fatalf("refresh = %+v", refreshed)
	}
	rejected, err := h.svc.RejectAPI(context.Background(), 1002, 42)
	if err != nil {
		t.Fatalf("RejectAPI: %v", err)
	}
	if rejected.Status != resource.APIStatusRejected {
		t.Fatalf("rejected = %+v", rejected)
	}
	// A rejected API is not re-proposed.
	skipped, err := h.svc.CreateProposedAPI(context.Background(), ProposedAPIInput{
		EnvironmentID: 1002, Method: "GET", PathTemplate: "/api/orders", Name: "Orders v3",
		Source: resource.APISourceAI,
	})
	if err != nil {
		t.Fatalf("rejected skip: %v", err)
	}
	if skipped.Status != resource.APIStatusRejected || skipped.Name != "Orders v2" {
		t.Fatalf("rejected row changed: %+v", skipped)
	}

	invalid := ProposedAPIInput{EnvironmentID: 1002, Method: "FETCH", PathTemplate: "/x", Name: "x"}
	if _, err := h.svc.CreateProposedAPI(context.Background(), invalid); err == nil {
		t.Fatal("invalid method must fail")
	} else {
		assertAPIError(t, err, 400, "invalid_argument")
	}
}

func TestProposedRouteTemplateBounds(t *testing.T) {
	h := newHarness()
	seedProposalEnvironment(h)
	long := proposedPageInput()
	long.Name = strings.Repeat("x", 201)
	_, err := h.svc.CreateProposedPage(context.Background(), long)
	assertAPIError(t, err, 400, "invalid_argument")
}
