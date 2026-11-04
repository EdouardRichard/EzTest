package explore_test

import (
	"context"
	"errors"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
)

/* ---------------------- discovered resource proposals --------------------- */

// fakeResourceProposals records the pages/APIs discovery hands to the resource
// catalog. It is the narrow port a caller uses to turn discovery evidence into
// proposed sut_page/sut_api rows a version can bind.
type fakeResourceProposals struct {
	pages     []exploreapp.PageResourceProposal
	apis      []exploreapp.APIResourceProposal
	pageErr   error
	apiErr    error
	pageCalls int
	apiCalls  int
}

func (f *fakeResourceProposals) ProposePageResource(_ context.Context, page exploreapp.PageResourceProposal) (bool, error) {
	f.pageCalls++
	f.pages = append(f.pages, page)
	if f.pageErr != nil {
		return false, f.pageErr
	}
	return true, nil
}

func (f *fakeResourceProposals) ProposeAPIResource(_ context.Context, api exploreapp.APIResourceProposal) (bool, error) {
	f.apiCalls++
	f.apis = append(f.apis, api)
	if f.apiErr != nil {
		return false, f.apiErr
	}
	return true, nil
}

var _ exploreapp.ResourceProposalWriter = (*fakeResourceProposals)(nil)

func TestPageDiscoveryProposesResources(t *testing.T) {
	exec := newPageDiscoveryExecutor()
	facts := &fakeFacts{}
	resources := &fakeResourceProposals{}
	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:       7,
			EnvironmentID:      8,
			StartURL:           pageDiscoveryStartURL,
			MaxPages:           10,
			MaxDepth:           3,
			MaxLinksPerPage:    20,
			MaxElementsPerPage: 20,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: facts, Resources: resources},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}

	// Every crawled page must become a bindable proposed resource: this is the
	// missing link between discovery facts and the version binding wizard.
	if len(resources.pages) != len(result.Pages) || len(resources.pages) != 4 {
		t.Fatalf("proposed pages = %d (want %d), discovered = %d", len(resources.pages), len(result.Pages), len(result.Pages))
	}
	if result.ProposedResources != len(result.Pages) {
		t.Fatalf("result.ProposedResources = %d, want %d", result.ProposedResources, len(result.Pages))
	}
	if result.ResourceErrors != 0 {
		t.Fatalf("result.ResourceErrors = %d, want 0", result.ResourceErrors)
	}
	for index, proposal := range resources.pages {
		if proposal.EnvironmentID != 8 {
			t.Fatalf("page[%d] environment = %d, want 8", index, proposal.EnvironmentID)
		}
		if proposal.URL == "" || proposal.RouteTemplate == "" || proposal.Name == "" {
			t.Fatalf("page[%d] proposal is incomplete: %+v", index, proposal)
		}
		if proposal.PageType != "page" {
			t.Fatalf("page[%d] page type = %q, want page", index, proposal.PageType)
		}
	}
	if resources.pages[0].RouteTemplate != "/app#/home" || resources.pages[0].URL != "http://sut.test/app#/home" {
		t.Fatalf("root proposal = %+v, want the normalized hash-route page", resources.pages[0])
	}
}

func TestPageDiscoveryResourceProposalIsBestEffort(t *testing.T) {
	exec := newPageDiscoveryExecutor()
	facts := &fakeFacts{}
	resources := &fakeResourceProposals{pageErr: errors.New("catalog unavailable")}
	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{
			VersionSUTID:  7,
			EnvironmentID: 8,
			StartURL:      pageDiscoveryStartURL,
			MaxPages:      10,
		},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: facts, Resources: resources},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("page discovery must not fail on a resource write error: %v", err)
	}
	if len(result.Pages) != 4 {
		t.Fatalf("discovered pages = %d, want the crawl result to survive: %+v", len(result.Pages), result.Pages)
	}
	if len(facts.recorded()) != 4 {
		t.Fatalf("page facts = %d, want 4", len(facts.recorded()))
	}
	if result.ProposedResources != 0 || result.ResourceErrors != 4 {
		t.Fatalf("proposed=%d errors=%d, want 0/4", result.ProposedResources, result.ResourceErrors)
	}
	if result.ResourceError == "" {
		t.Fatal("result.ResourceError is empty, want the first write error")
	}
}

func TestPageDiscoveryWithoutEnvironmentSkipsResources(t *testing.T) {
	exec := newPageDiscoveryExecutor()
	facts := &fakeFacts{}
	resources := &fakeResourceProposals{}
	discovery := exploreapp.NewPageDiscovery(
		exploreapp.PageDiscoveryConfig{VersionSUTID: 7, StartURL: pageDiscoveryStartURL, MaxPages: 10},
		exploreapp.PageDiscoveryDeps{Executor: exec, Facts: facts, Resources: resources},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("PageDiscovery.Run: %v", err)
	}
	if resources.pageCalls != 0 {
		t.Fatalf("page resource calls = %d, want 0 without an environment", resources.pageCalls)
	}
	if result.ProposedResources != 0 {
		t.Fatalf("result.ProposedResources = %d, want 0", result.ProposedResources)
	}
}

func TestAPIDiscoveryProposesResources(t *testing.T) {
	sources := discoverySourceProvider(t, ginRouteSources())
	facts := &fakeFacts{}
	resources := &fakeResourceProposals{}
	discovery := exploreapp.NewAPIDiscovery(
		exploreapp.APIDiscoveryConfig{EnvironmentID: 8, MaxAPIs: 100},
		exploreapp.APIDiscoveryDeps{Sources: sources, Facts: facts, Resources: resources},
	)
	result, err := discovery.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("APIDiscovery.Run: %v", err)
	}
	if len(resources.apis) != len(result.APIs) || len(resources.apis) == 0 {
		t.Fatalf("proposed APIs = %d, discovered = %d", len(resources.apis), len(result.APIs))
	}
	if result.ProposedResources != len(result.APIs) || result.ResourceErrors != 0 {
		t.Fatalf("proposed=%d errors=%d, want %d/0", result.ProposedResources, result.ResourceErrors, len(result.APIs))
	}
	for index, proposal := range resources.apis {
		if proposal.EnvironmentID != 8 {
			t.Fatalf("api[%d] environment = %d, want 8", index, proposal.EnvironmentID)
		}
		if proposal.Method == "" || proposal.PathTemplate == "" || proposal.Name == "" {
			t.Fatalf("api[%d] proposal is incomplete: %+v", index, proposal)
		}
	}
	// The proposals mirror the discovered facts, so the wizard can show the same
	// method/path pairs the exploration chain reported.
	for index, candidate := range result.APIs {
		proposal := resources.apis[index]
		if proposal.Method != candidate.Method || proposal.PathTemplate != candidate.PathTemplate {
			t.Fatalf("api[%d] proposal = %s %s, want %s %s",
				index, proposal.Method, proposal.PathTemplate, candidate.Method, candidate.PathTemplate)
		}
	}
}
