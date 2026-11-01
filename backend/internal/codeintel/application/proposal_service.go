package codeintelapp

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ProposalTarget is the resource destination of one version SUT: the SUT,
// environment and base URL its proposed pages/APIs belong to (spec §8.1).
type ProposalTarget struct {
	VersionSUTID  int64
	SUTID         int64
	EnvironmentID int64
	BaseURL       string
}

// ProposalTargetReader resolves the resource target of a version SUT binding.
// The version/resource adapters implement it; codeintel never joins those
// contexts directly.
type ProposalTargetReader interface {
	ProposalTarget(ctx context.Context, versionSUTID int64) (ProposalTarget, error)
}

// ProposalPageInput is one proposed page write.
type ProposalPageInput struct {
	EnvironmentID int64
	SUTID         int64
	Name          string
	URL           string
	RouteTemplate string
	PageType      string
	AuthRole      string
	Module        string
	Source        string
}

// ProposalAPIInput is one proposed API write.
type ProposalAPIInput struct {
	EnvironmentID int64
	SUTID         int64
	Method        string
	PathTemplate  string
	Name          string
	AuthRole      string
	SchemaRef     string
	Source        string
}

// ProposalItem is one proposed resource row returned by the resource port.
type ProposalItem struct {
	Kind          string
	ID            int64
	Name          string
	URL           string
	RouteTemplate string
	Method        string
	PathTemplate  string
	Source        string
	Status        string
}

// ProposalResourcePort writes and lists proposed resources. ProposePage and
// ProposeAPI must dedupe against the resource unique indexes: created=true only
// when a new proposed row was inserted; an approved/rejected row is returned
// unchanged with created=false, and an existing proposed row is refreshed with
// created=false.
type ProposalResourcePort interface {
	ProposePage(ctx context.Context, in ProposalPageInput) (ProposalItem, bool, error)
	ProposeAPI(ctx context.Context, in ProposalAPIInput) (ProposalItem, bool, error)
	ListProposals(ctx context.Context, environmentIDs []int64) ([]ProposalItem, error)
}

// ProposalResult reports the generation counts (spec §4.2 #24).
type ProposalResult struct {
	PagesCreated int
	PagesSkipped int
	APIsCreated  int
	APISkipped   int
}

// ProposalView is one proposed resource of a version (spec §4.2 #25).
type ProposalView struct {
	Kind          string
	ID            int64
	Name          string
	URL           string
	RouteTemplate string
	Method        string
	PathTemplate  string
	Source        string
	Status        string
}

// ProposalDeps is the page/API proposal wiring.
type ProposalDeps struct {
	Analyses  AnalysisRepository
	Facts     FactRepository
	Versions  VersionReader
	Targets   ProposalTargetReader
	Resources ProposalResourcePort
	Execution ExecutionReader
	Events    EventPublisher
	Audit     AuditRecorder
	Settings  SettingsReader
	Clock     Clock
}

// ProposalService generates proposed pages/APIs from code facts plus captured
// network evidence (spec §8.1). Generation is synchronous and bounded.
type ProposalService struct {
	analyses  AnalysisRepository
	facts     FactRepository
	versions  VersionReader
	targets   ProposalTargetReader
	resources ProposalResourcePort
	execution ExecutionReader
	events    EventPublisher
	audit     AuditRecorder
	settings  SettingsReader
	clock     Clock
}

// NewProposalService wires the proposal service.
func NewProposalService(deps ProposalDeps) *ProposalService {
	return &ProposalService{
		analyses:  deps.Analyses,
		facts:     deps.Facts,
		versions:  deps.Versions,
		targets:   deps.Targets,
		resources: deps.Resources,
		execution: deps.Execution,
		events:    deps.Events,
		audit:     deps.Audit,
		settings:  deps.Settings,
		clock:     clockOrSystem(deps.Clock),
	}
}

// Generate proposes pages/APIs for the version (optionally one repository). It
// is idempotent: existing approved/proposed/rejected rows are skipped or
// refreshed by the resource port and reported in the skipped counts.
func (s *ProposalService) Generate(ctx context.Context, versionID, repoID int64) (ProposalResult, error) {
	bindings, err := s.versions.VersionSUTBindings(ctx, versionID)
	if err != nil {
		return ProposalResult{}, err
	}
	selected := make([]VersionSUTBinding, 0, len(bindings))
	seenSUT := map[int64]bool{}
	for _, binding := range bindings {
		if repoID > 0 && binding.RepoID != repoID {
			continue
		}
		if binding.VersionSUTID <= 0 || binding.RepoID <= 0 || seenSUT[binding.VersionSUTID] {
			continue
		}
		seenSUT[binding.VersionSUTID] = true
		selected = append(selected, binding)
	}
	if len(selected) == 0 {
		return ProposalResult{}, unprocessable("repo_not_bound", "repository is not bound to this version")
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].VersionSUTID < selected[j].VersionSUTID })

	network := make([]NetworkRequest, 0)
	if s.execution != nil {
		if requests, netErr := s.execution.NetworkEvidence(ctx, versionID); netErr == nil {
			network = requests
		}
	}

	result := ProposalResult{}
	createdPages := make([]int64, 0)
	createdAPIs := make([]int64, 0)
	seenPages := map[string]bool{}
	seenAPIs := map[string]bool{}
	analyzed := false
	for _, binding := range selected {
		target, targetErr := s.targets.ProposalTarget(ctx, binding.VersionSUTID)
		if targetErr != nil {
			return ProposalResult{}, mapDomainError(targetErr, "proposal target")
		}
		analysis, latestErr := s.analyses.Latest(ctx, versionID, binding.RepoID)
		if latestErr != nil {
			if errors.Is(latestErr, codeintel.ErrNotFound) {
				continue
			}
			return ProposalResult{}, mapDomainError(latestErr, "code analysis")
		}
		if analysis.Status != codeintel.AnalysisDone {
			continue
		}
		analyzed = true
		facts, factErr := s.facts.LatestByCommit(ctx, binding.RepoID, analysis.HeadCommitSHA,
			[]codeintel.EntityKind{codeintel.EntityPage, codeintel.EntityRoute, codeintel.EntityAPI, codeintel.EntityClientCall})
		if factErr != nil {
			return ProposalResult{}, mapDomainError(factErr, "code fact")
		}
		sort.Slice(facts, func(i, j int) bool { return facts[i].FactKey < facts[j].FactKey })
		for _, fact := range facts {
			switch fact.Kind {
			case codeintel.EntityPage, codeintel.EntityRoute:
				route, ok := impactFactRoute(fact)
				if !ok {
					continue
				}
				key := platformid.Format(target.EnvironmentID) + "|" + route
				if seenPages[key] {
					continue
				}
				seenPages[key] = true
				item, created, proposeErr := s.resources.ProposePage(ctx, ProposalPageInput{
					EnvironmentID: target.EnvironmentID,
					SUTID:         target.SUTID,
					Name:          proposePageName(fact, route),
					URL:           joinBaseURL(target.BaseURL, route),
					RouteTemplate: route,
					PageType:      "page",
					Source:        "ai",
				})
				if proposeErr != nil {
					return ProposalResult{}, proposeErr
				}
				if created {
					result.PagesCreated++
					createdPages = append(createdPages, item.ID)
				} else {
					result.PagesSkipped++
				}
			case codeintel.EntityAPI, codeintel.EntityClientCall:
				method, path, ok := impactFactAPI(fact)
				if !ok {
					continue
				}
				key := platformid.Format(target.EnvironmentID) + "|" + apiRouteKey(method, path)
				if seenAPIs[key] {
					continue
				}
				seenAPIs[key] = true
				item, created, proposeErr := s.resources.ProposeAPI(ctx, ProposalAPIInput{
					EnvironmentID: target.EnvironmentID,
					SUTID:         target.SUTID,
					Method:        method,
					PathTemplate:  path,
					Name:          proposeAPIName(fact, method, path),
					Source:        "ai",
				})
				if proposeErr != nil {
					return ProposalResult{}, proposeErr
				}
				if created {
					result.APIsCreated++
					createdAPIs = append(createdAPIs, item.ID)
				} else {
					result.APISkipped++
				}
			}
		}
		for _, request := range network {
			if request.VersionSUTID != nil && *request.VersionSUTID != binding.VersionSUTID {
				continue
			}
			method, path, ok := networkRoute(target.BaseURL, request)
			if !ok {
				continue
			}
			key := platformid.Format(target.EnvironmentID) + "|" + apiRouteKey(method, path)
			if seenAPIs[key] {
				continue
			}
			seenAPIs[key] = true
			item, created, proposeErr := s.resources.ProposeAPI(ctx, ProposalAPIInput{
				EnvironmentID: target.EnvironmentID,
				SUTID:         target.SUTID,
				Method:        method,
				PathTemplate:  path,
				Name:          method + " " + path,
				Source:        "code",
			})
			if proposeErr != nil {
				return ProposalResult{}, proposeErr
			}
			if created {
				result.APIsCreated++
				createdAPIs = append(createdAPIs, item.ID)
			} else {
				result.APISkipped++
			}
		}
	}
	if !analyzed {
		return ProposalResult{}, unprocessable("analysis_not_found", "no done code analysis is available for the bound repositories")
	}

	publishEvent(ctx, s.events, "code.page_api_proposed", versionID, map[string]any{
		"pages_created": result.PagesCreated,
		"pages_skipped": result.PagesSkipped,
		"apis_created":  result.APIsCreated,
		"apis_skipped":  result.APISkipped,
		"page_ids":      proposalIDs(createdPages),
		"api_ids":       proposalIDs(createdAPIs),
	})
	recordAudit(ctx, s.audit, "code.page_api_proposed", "version", platformid.Format(versionID), map[string]any{
		"pages_created": result.PagesCreated,
		"pages_skipped": result.PagesSkipped,
		"apis_created":  result.APIsCreated,
		"apis_skipped":  result.APISkipped,
	})
	return result, nil
}

// List returns the proposed resources of the version's bound environments.
func (s *ProposalService) List(ctx context.Context, versionID int64) ([]ProposalView, error) {
	bindings, err := s.versions.VersionSUTBindings(ctx, versionID)
	if err != nil {
		return nil, err
	}
	versionSUTIDs := make([]int64, 0, len(bindings))
	seenSUT := map[int64]bool{}
	for _, binding := range bindings {
		if binding.VersionSUTID <= 0 || seenSUT[binding.VersionSUTID] {
			continue
		}
		seenSUT[binding.VersionSUTID] = true
		versionSUTIDs = append(versionSUTIDs, binding.VersionSUTID)
	}
	sort.Slice(versionSUTIDs, func(i, j int) bool { return versionSUTIDs[i] < versionSUTIDs[j] })
	environmentIDs := make([]int64, 0, len(versionSUTIDs))
	seenEnv := map[int64]bool{}
	for _, versionSUTID := range versionSUTIDs {
		target, targetErr := s.targets.ProposalTarget(ctx, versionSUTID)
		if targetErr != nil {
			return nil, mapDomainError(targetErr, "proposal target")
		}
		if target.EnvironmentID <= 0 || seenEnv[target.EnvironmentID] {
			continue
		}
		seenEnv[target.EnvironmentID] = true
		environmentIDs = append(environmentIDs, target.EnvironmentID)
	}
	sort.Slice(environmentIDs, func(i, j int) bool { return environmentIDs[i] < environmentIDs[j] })
	if len(environmentIDs) == 0 {
		return []ProposalView{}, nil
	}
	items, err := s.resources.ListProposals(ctx, environmentIDs)
	if err != nil {
		return nil, err
	}
	views := make([]ProposalView, 0, len(items))
	for _, item := range items {
		views = append(views, ProposalView(item))
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Kind != views[j].Kind {
			return views[i].Kind < views[j].Kind
		}
		return views[i].ID < views[j].ID
	})
	return views, nil
}

// --- helpers ----------------------------------------------------------------------

func proposePageName(fact codeintel.Fact, route string) string {
	name := strings.TrimSpace(fact.Name)
	if name == "" || strings.Contains(name, "\n") {
		name = route
	}
	return truncateString(name, 200)
}

func proposeAPIName(fact codeintel.Fact, method, path string) string {
	name := strings.TrimSpace(fact.Name)
	if name == "" || strings.Contains(name, "\n") {
		name = method + " " + path
	}
	return truncateString(name, 200)
}

func joinBaseURL(baseURL, route string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	return base + route
}

// networkRoute accepts a captured request only when its host matches the
// version SUT environment base URL (spec §8.1).
func networkRoute(baseURL string, request NetworkRequest) (string, string, bool) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Host == "" {
		return "", "", false
	}
	captured, err := url.Parse(strings.TrimSpace(request.URL))
	if err != nil {
		return "", "", false
	}
	if captured.Host == "" {
		// The executor may persist a browser-relative request URL; resolve it
		// against the version SUT base URL before host matching.
		captured = base.ResolveReference(captured)
	}
	if !strings.EqualFold(captured.Host, base.Host) {
		return "", "", false
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	path := codeintel.NormalizeAPIPath(captured.Path)
	if method == "" || path == "" {
		return "", "", false
	}
	return method, path, true
}

func proposalIDs(ids []int64) []string {
	limit := len(ids)
	if limit > 20 {
		limit = 20
	}
	result := make([]string, 0, limit)
	for _, id := range ids[:limit] {
		result = append(result, platformid.Format(id))
	}
	return result
}
