package chainapp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
)

// Clock is the injectable time source of the chain service.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// ChainService serves the read-only exploration-chain view. It assembles the
// provider nodes, applies the requested filter/budget in the domain builder,
// and stamps the version and replay metadata owned by the service.
type ChainService struct {
	version   VersionChainProvider
	design    DesignChainProvider
	execution ExecutionChainProvider
	delivery  DeliveryChainProvider
	resource  ResourceChainProvider
	cursor    CursorReader
	events    EventTimeReader
	clock     Clock
	budget    chain.Budget
}

// NewChainService wires the five chain providers, the snapshot-cursor reader,
// the event-time reader, the clock and the configured chain budget. A nil clock
// falls back to the system clock. The event-time reader is only needed for
// event_from/event_to replay; an assembly that never serves a range may pass
// nil. The optional budget is the chain.max_nodes / chain.max_edges /
// chain.max_requirements tuple from settings (spec §4.5), clamped through
// domain.ClampBudget; omitting it keeps the spec defaults. At most one budget
// is read; extra values are ignored.
func NewChainService(
	version VersionChainProvider,
	design DesignChainProvider,
	execution ExecutionChainProvider,
	delivery DeliveryChainProvider,
	resource ResourceChainProvider,
	cursor CursorReader,
	events EventTimeReader,
	clock Clock,
	budgets ...chain.Budget,
) *ChainService {
	if clock == nil {
		clock = systemClock{}
	}
	configured := chain.Budget{}
	if len(budgets) > 0 {
		configured = budgets[0]
	}
	return &ChainService{
		version:   version,
		design:    design,
		execution: execution,
		delivery:  delivery,
		resource:  resource,
		cursor:    cursor,
		events:    events,
		clock:     clock,
		budget:    chain.ClampBudget(configured),
	}
}

// Read returns the exploration chain of one version (spec §8.2–§8.4). The
// filter is normalized once; event_from/event_to are mapped to inclusive
// created_at boundaries and provider nodes outside that window are dropped
// before BuildChain, so the induced edges only connect surviving nodes. Nodes
// without a timestamp are kept, and replay is reported as approximate because
// P4 never promises an arbitrary as-of snapshot. A zero event bound is unset; a
// negative or unresolvable seq is rejected.
func (s *ChainService) Read(ctx context.Context, versionID int64, f chain.ChainFilter) (chain.ChainView, error) {
	if versionID <= 0 {
		return chain.ChainView{}, fmt.Errorf("chain: version_id must be positive: %d", versionID)
	}
	if err := validateEventBounds(f); err != nil {
		return chain.ChainView{}, err
	}
	f = f.Normalize()
	eventLower, eventUpper, err := s.eventTimeBounds(ctx, versionID, f)
	if err != nil {
		return chain.ChainView{}, err
	}
	budget := chain.ClampBudget(s.budget)

	// Ask for one requirement root beyond the configured budget. A provider that
	// honors the limit then returns MaxRequirements+1 roots when the dataset is
	// larger than the budget, and BuildChain truncates to MaxRequirements with
	// Truncated=true / reason=requirement_budget instead of silently serving the
	// full page. Providers that cap the page lower than requested (the real
	// version provider pages through platform/pagination.MaxLimit=100) silently
	// hide the extra root: the view then reports no truncation and Counts
	// describe only the returned page. That residual silent provider cap is
	// accepted in P4 because the VersionChainProvider port exposes no "has more"
	// signal; fixing it needs a provider change, not a chain one. A provider that
	// ignores the limit and returns more than MaxRequirements+1 is still
	// truncated deterministically by BuildChain.
	requirements, err := s.version.Requirements(ctx, versionID, budget.MaxRequirements+1)
	if err != nil {
		return chain.ChainView{}, providerError("load requirements", err)
	}
	versionSUTs, err := s.version.VersionSUTs(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load version SUTs", err)
	}
	scenarios, err := s.design.Scenarios(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load scenarios", err)
	}
	cases, err := s.design.Cases(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load cases", err)
	}
	waves, err := s.execution.Waves(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load waves", err)
	}
	caseRuns, err := s.execution.CaseRuns(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load case runs", err)
	}
	if missing := missingCaseVersionIDs(cases, caseRuns); len(missing) > 0 {
		historical, err := s.design.CaseVersionsByIDs(ctx, versionID, missing)
		if err != nil {
			return chain.ChainView{}, providerError("load historical case versions", err)
		}
		cases = append(cases, historical...)
	}
	issues, err := s.delivery.Issues(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load issues", err)
	}
	reports, err := s.delivery.Reports(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load reports", err)
	}
	closures, err := s.delivery.Closures(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load closures", err)
	}
	sutNames, err := s.resource.SUTNames(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load SUT names", err)
	}
	pageNames, err := s.resource.PageNames(ctx, versionID)
	if err != nil {
		return chain.ChainView{}, providerError("load page names", err)
	}
	cursor, err := s.cursor.LatestCursor(ctx, versionChannel(versionID))
	if err != nil {
		return chain.ChainView{}, providerError("load snapshot cursor", err)
	}

	source := chain.SourceGraph{
		Requirements: requirements,
		Scenarios:    scenarios,
		Cases:        cases,
		Waves:        waves,
		CaseRuns:     caseRuns,
		Issues:       issues,
		Reports:      reports,
		Closures:     closures,
		SUTs:         mergeSUTNames(versionSUTs, sutNames),
		Pages:        nonNilNames(pageNames),
	}
	view := chain.BuildChain(filterSourceGraphByEventRange(source, eventLower, eventUpper), f, budget, cursor, s.clock.Now())
	view.VersionID = strconv.FormatInt(versionID, 10)
	return view, nil
}

func providerError(operation string, err error) error {
	return fmt.Errorf("chain: %s: %w", operation, err)
}

func versionChannel(versionID int64) string {
	return "version:" + strconv.FormatInt(versionID, 10)
}

// missingCaseVersionIDs lists the case versions referenced by case runs but not
// returned by the current-case reader. The design provider's historical reader
// resolves them so superseded rows keep their titles in the chain.
func missingCaseVersionIDs(cases, caseRuns []chain.Node) []int64 {
	known := make(map[int64]bool, len(cases))
	for _, node := range cases {
		if id, ok := nodeRefID(node); ok {
			known[id] = true
		}
	}
	missing := map[int64]bool{}
	for _, node := range caseRuns {
		if node.CaseVersionID == nil || *node.CaseVersionID <= 0 {
			continue
		}
		if !known[*node.CaseVersionID] {
			missing[*node.CaseVersionID] = true
		}
	}
	ids := make([]int64, 0, len(missing))
	for id := range missing {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// mergeSUTNames builds the version-SUT-ID label map. VersionSUT nodes provide
// the binding labels, resource SUT names refine them (they win on conflict).
func mergeSUTNames(versionSUTs []chain.Node, names map[int64]string) map[int64]string {
	out := make(map[int64]string, len(versionSUTs)+len(names))
	for _, node := range versionSUTs {
		id, ok := nodeRefID(node)
		if !ok || node.Label == "" {
			continue
		}
		out[id] = node.Label
	}
	for id, name := range names {
		if id > 0 && name != "" {
			out[id] = name
		}
	}
	return out
}

func nodeRefID(node chain.Node) (int64, bool) {
	if id, err := strconv.ParseInt(strings.TrimSpace(node.RefID), 10, 64); err == nil && id > 0 {
		return id, true
	}
	if index := strings.LastIndex(node.ID, ":"); index >= 0 {
		if id, err := strconv.ParseInt(strings.TrimSpace(node.ID[index+1:]), 10, 64); err == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}

func nonNilNames(names map[int64]string) map[int64]string {
	if names == nil {
		return map[int64]string{}
	}
	return names
}
