package main

import (
	"context"
	"errors"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// fakeResourceProposalService records the resource application calls the
// discovery adapter makes. It implements the narrow p9ResourceProposalService
// surface (CreateProposedPage/CreateProposedAPI) of *resourceapp.Service.
type fakeResourceProposalService struct {
	pageInput  resourceapp.ProposedPageInput
	apiInput   resourceapp.ProposedAPIInput
	pageCalls  int
	apiCalls   int
	pageStatus string
	apiStatus  string
	pageErr    error
	apiErr     error
}

func (f *fakeResourceProposalService) CreateProposedPage(_ context.Context, input resourceapp.ProposedPageInput) (resourceapp.PageView, error) {
	f.pageCalls++
	f.pageInput = input
	if f.pageErr != nil {
		return resourceapp.PageView{}, f.pageErr
	}
	status := f.pageStatus
	if status == "" {
		status = "proposed"
	}
	return resourceapp.PageView{ID: "1301", Status: status}, nil
}

func (f *fakeResourceProposalService) CreateProposedAPI(_ context.Context, input resourceapp.ProposedAPIInput) (resourceapp.APIView, error) {
	f.apiCalls++
	f.apiInput = input
	if f.apiErr != nil {
		return resourceapp.APIView{}, f.apiErr
	}
	status := f.apiStatus
	if status == "" {
		status = "proposed"
	}
	return resourceapp.APIView{ID: "1401", Status: status}, nil
}

func TestP9ResourceProposalsWriteProposedRows(t *testing.T) {
	service := &fakeResourceProposalService{}
	writer := p9ResourceProposals{resources: service}

	proposed, err := writer.ProposePageResource(context.Background(), exploreapp.PageResourceProposal{
		EnvironmentID: 8,
		Name:          "订单列表",
		URL:           "http://sut.test/orders",
		RouteTemplate: "/orders",
		PageType:      "page",
	})
	if err != nil {
		t.Fatalf("ProposePageResource: %v", err)
	}
	if !proposed {
		t.Fatal("ProposePageResource proposed = false, want true for a proposed row")
	}
	want := resourceapp.ProposedPageInput{
		EnvironmentID: 8,
		Name:          "订单列表",
		URL:           "http://sut.test/orders",
		RouteTemplate: "/orders",
		PageType:      "page",
		Source:        "ai",
	}
	if service.pageInput != want {
		t.Fatalf("page input = %+v, want %+v", service.pageInput, want)
	}

	proposed, err = writer.ProposeAPIResource(context.Background(), exploreapp.APIResourceProposal{
		EnvironmentID: 8,
		Method:        "GET",
		PathTemplate:  "/api/orders",
		Name:          "GET /api/orders",
	})
	if err != nil {
		t.Fatalf("ProposeAPIResource: %v", err)
	}
	if !proposed {
		t.Fatal("ProposeAPIResource proposed = false, want true for a proposed row")
	}
	wantAPI := resourceapp.ProposedAPIInput{
		EnvironmentID: 8,
		Method:        "GET",
		PathTemplate:  "/api/orders",
		Name:          "GET /api/orders",
		Source:        "ai",
	}
	if service.apiInput != wantAPI {
		t.Fatalf("api input = %+v, want %+v", service.apiInput, wantAPI)
	}
}

func TestP9ResourceProposalsSkipApprovedAndSurfaceErrors(t *testing.T) {
	approved := &fakeResourceProposalService{pageStatus: "approved", apiStatus: "approved"}
	writer := p9ResourceProposals{resources: approved}
	proposed, err := writer.ProposePageResource(context.Background(), exploreapp.PageResourceProposal{EnvironmentID: 8})
	if err != nil {
		t.Fatalf("ProposePageResource: %v", err)
	}
	if proposed {
		t.Fatal("an already approved page must not count as a new proposal")
	}
	proposed, err = writer.ProposeAPIResource(context.Background(), exploreapp.APIResourceProposal{EnvironmentID: 8})
	if err != nil {
		t.Fatalf("ProposeAPIResource: %v", err)
	}
	if proposed {
		t.Fatal("an already approved API must not count as a new proposal")
	}

	failing := &fakeResourceProposalService{pageErr: errors.New("db down"), apiErr: errors.New("db down")}
	writer = p9ResourceProposals{resources: failing}
	if _, err := writer.ProposePageResource(context.Background(), exploreapp.PageResourceProposal{EnvironmentID: 8}); err == nil {
		t.Fatal("ProposePageResource error = nil, want the catalog write error surfaced")
	}
	if _, err := writer.ProposeAPIResource(context.Background(), exploreapp.APIResourceProposal{EnvironmentID: 8}); err == nil {
		t.Fatal("ProposeAPIResource error = nil, want the catalog write error surfaced")
	}
}

func TestP9ResourceProposalsWithoutServiceAreNoOps(t *testing.T) {
	writer := p9ResourceProposals{}
	if proposed, err := writer.ProposePageResource(context.Background(), exploreapp.PageResourceProposal{EnvironmentID: 8}); err != nil || proposed {
		t.Fatalf("page proposal without a service = (%v, %v), want (false, nil)", proposed, err)
	}
	if proposed, err := writer.ProposeAPIResource(context.Background(), exploreapp.APIResourceProposal{EnvironmentID: 8}); err != nil || proposed {
		t.Fatalf("api proposal without a service = (%v, %v), want (false, nil)", proposed, err)
	}
}
