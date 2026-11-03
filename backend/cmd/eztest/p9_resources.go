package main

import (
	"context"
	"strings"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// p9ResourceProposalService is the narrow resource-catalog write surface the
// discovery hand-off needs. *resourceapp.Service satisfies it.
type p9ResourceProposalService interface {
	CreateProposedPage(ctx context.Context, input resourceapp.ProposedPageInput) (resourceapp.PageView, error)
	CreateProposedAPI(ctx context.Context, input resourceapp.ProposedAPIInput) (resourceapp.APIView, error)
}

// p9ResourceProposals adapts explore discovery onto the resource catalog: every
// page/API discovery finds becomes a proposed sut_page/sut_api row of the
// version's bound environment, so the version wizard can list and bind it.
// The resource service owns dedupe and the proposed/approved state machine;
// this adapter only translates the neutral discovery proposal.
type p9ResourceProposals struct {
	resources p9ResourceProposalService
}

var _ exploreapp.ResourceProposalWriter = p9ResourceProposals{}

// ProposePageResource writes/refreshes one proposed page. It reports proposed
// only when the stored row is still proposed: an already approved page is
// returned unchanged by the resource service and must not be counted again.
func (w p9ResourceProposals) ProposePageResource(ctx context.Context, page exploreapp.PageResourceProposal) (bool, error) {
	if w.resources == nil {
		return false, nil
	}
	view, err := w.resources.CreateProposedPage(ctx, resourceapp.ProposedPageInput{
		EnvironmentID: page.EnvironmentID,
		Name:          page.Name,
		URL:           page.URL,
		RouteTemplate: page.RouteTemplate,
		PageType:      page.PageType,
		Source:        resource.PageSourceAI,
	})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(view.Status) == resource.PageStatusProposed, nil
}

// ProposeAPIResource writes/refreshes one proposed API with the same
// approved-row rule as ProposePageResource.
func (w p9ResourceProposals) ProposeAPIResource(ctx context.Context, api exploreapp.APIResourceProposal) (bool, error) {
	if w.resources == nil {
		return false, nil
	}
	view, err := w.resources.CreateProposedAPI(ctx, resourceapp.ProposedAPIInput{
		EnvironmentID: api.EnvironmentID,
		Method:        api.Method,
		PathTemplate:  api.PathTemplate,
		Name:          api.Name,
		Source:        resource.APISourceAI,
	})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(view.Status) == resource.APIStatusProposed, nil
}
