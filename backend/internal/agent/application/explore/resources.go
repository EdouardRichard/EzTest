package explore

// This file owns the discovery → resource-catalog hand-off. Page/API discovery
// has always recorded `page`/`api` version facts, but a fact is not a bindable
// resource: the version create wizard reads sut_page/sut_api rows from the
// resource catalog. Without this hand-off an environment that only ever ran
// discovery stayed at "该环境暂无页面 / 暂无 API" and a version could not bind a
// page, leaving AI design and execution without page targets (product-owner
// trial, task 76).
//
// The writer port is application-only and narrow: the resource context owns
// dedupe, status (proposed) and confirmation. Discovery never approves a
// resource and never binds one itself.

import "context"

// maxResourceProposals bounds how many discovered rows one run writes into the
// resource catalog. The crawl itself is bounded (20 pages) and the static scan
// can yield hundreds of endpoints, so the catalog write is independently
// capped to keep one discovery refresh inexpensive. Existing rows are refreshed
// idempotently by the resource writer, so the cap only truncates new proposals.
const maxResourceProposals = 50

// PageResourceProposal is one discovered page proposed as a resource row of the
// version's bound environment. It carries no credential and no element
// inventory: page facts keep the locators, the catalog keeps the navigable page.
type PageResourceProposal struct {
	EnvironmentID int64
	Name          string
	URL           string
	RouteTemplate string
	PageType      string
}

// APIResourceProposal is one statically discovered API proposed as a resource
// row of the version's bound environment.
type APIResourceProposal struct {
	EnvironmentID int64
	Method        string
	PathTemplate  string
	Name          string
}

// ResourceProposalWriter persists discovered pages/APIs into the resource
// catalog as proposed rows. The composition root adapts the resource
// application service onto this port so the exploration context never imports
// it. Implementations must be idempotent: re-proposing the same page URL or
// method/path refreshes the existing proposed row. implemented=true means the
// stored row is proposed (newly inserted or refreshed); an already
// approved/rejected row is never downgraded and reports implemented=false.
type ResourceProposalWriter interface {
	ProposePageResource(ctx context.Context, page PageResourceProposal) (implemented bool, err error)
	ProposeAPIResource(ctx context.Context, api APIResourceProposal) (implemented bool, err error)
}

// proposePageResources writes the crawled pages to the catalog, bounded by
// maxResourceProposals. A write failure never discards the discovery result or
// its facts: the first error is reported and the remaining pages are still
// attempted, because one bad row must not hide the rest of the discovery. A
// canceled context stops immediately and is returned to the caller.
func proposePageResources(ctx context.Context, writer ResourceProposalWriter, environmentID int64, pages []DiscoveredPage) (proposed, failures int, firstError string, ctxErr error) {
	if writer == nil || environmentID <= 0 {
		return 0, 0, "", nil
	}
	limit := len(pages)
	if limit > maxResourceProposals {
		limit = maxResourceProposals
	}
	for index := 0; index < limit; index++ {
		if err := ctx.Err(); err != nil {
			return proposed, failures, firstError, err
		}
		page := pages[index]
		created, err := writer.ProposePageResource(ctx, PageResourceProposal{
			EnvironmentID: environmentID,
			Name:          firstNonEmptyString(page.Title, page.RouteTemplate),
			URL:           page.URL,
			RouteTemplate: page.RouteTemplate,
			PageType:      "page",
		})
		if err != nil {
			failures++
			if firstError == "" {
				firstError = err.Error()
			}
			continue
		}
		if created {
			proposed++
		}
	}
	return proposed, failures, firstError, nil
}

// proposeAPIResources mirrors proposePageResources for the static API scan.
func proposeAPIResources(ctx context.Context, writer ResourceProposalWriter, environmentID int64, apis []APICandidate) (proposed, failures int, firstError string, ctxErr error) {
	if writer == nil || environmentID <= 0 {
		return 0, 0, "", nil
	}
	limit := len(apis)
	if limit > maxResourceProposals {
		limit = maxResourceProposals
	}
	for index := 0; index < limit; index++ {
		if err := ctx.Err(); err != nil {
			return proposed, failures, firstError, err
		}
		candidate := apis[index]
		name := candidate.APIKey
		if name == "" {
			name = candidate.Method + " " + candidate.PathTemplate
		}
		created, err := writer.ProposeAPIResource(ctx, APIResourceProposal{
			EnvironmentID: environmentID,
			Method:        candidate.Method,
			PathTemplate:  candidate.PathTemplate,
			Name:          name,
		})
		if err != nil {
			failures++
			if firstError == "" {
				firstError = err.Error()
			}
			continue
		}
		if created {
			proposed++
		}
	}
	return proposed, failures, firstError, nil
}
