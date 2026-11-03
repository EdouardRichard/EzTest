package main

// P9 explore composition root. Task 49 wires the autonomous login/session
// reuse runtime that the exploration capabilities (Task 35 executor actions,
// Task 36/37 discovery/coverage) call into. The process-wide pieces are built
// once here: one HTTP explore-action executor, one bounded version-scoped
// session cache and one version fact service, so every explore caller shares
// the same cache-miss policy (unknown storage_state_ref -> fresh login) and the
// same executor-page cleanup (DELETE /v1/explore?page_id=<id>, scoped to the
// throwaway verification page so concurrent explore pages stay open).
//
// Task 37 adds the analysis consumer (gap / coverage / case augmentation /
// failure attribution) and binds it to that same runtime: the deterministic
// analysis opens pages only after Login, serializes its actions and closes
// exactly the pages it opened. The consumer is leader-gated and event-triggered
// with a per-version minimum interval, so the wiring is live in production
// without a background write storm.
//
// The CaseSuccessHook is intentionally not wired here: the knowledge adapter
// does not implement executionapp.CaseSuccessHook yet, and inventing one is not
// this task's scope. Task 51 owns that adapter + the KnowledgeHook injection;
// P7Deps.Retrieval is Task 51 as well.

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionfactpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// P9 analysis bounds. A refresh is a bounded read plus a bounded set of live
// page probes; the per-version interval coalesces event storms.
const (
	p9AnalysisMinInterval     = 10 * time.Minute
	p9AnalysisMaxRequirements = 50
	p9AnalysisMaxProbes       = 3
	p9AnalysisWakeBuffer      = 64
)

// p9RequirementReader is the version requirement read surface the analysis
// adapters consume (*versionapp.RequirementService satisfies it).
type p9RequirementReader interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
	Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
}

// p9DesignGraphReader is the design graph read surface the coverage adapter
// consumes (*designapp.GraphService satisfies it).
type p9DesignGraphReader interface {
	ReadGraph(ctx context.Context, versionID int64) (designapp.GraphView, error)
}

// p9RuntimeDeps are the already-constructed P0-P7 services the explore
// composition consumes. The account store is the real resource secret service
// (LoginFlow reads passwords only through it, never through a prompt/fact).
type p9RuntimeDeps struct {
	Pool          *pgxpool.Pool
	IDs           platformid.Generator
	Events        eventingapp.Publisher
	Accounts      exploreapp.AccountStore
	Versions      versionadapter.VersionService
	Requirements  p9RequirementReader
	Design        p9DesignGraphReader
	ExecutorURL   string
	ExecutorToken string
	Now           func() time.Time
}

// p9Runtime is the constructed explore runtime. Sessions is exposed so a
// future explore handler/tool can inspect or invalidate the same cache; Explore
// builds per-run LoginFlow instances over the shared collaborators. Facts and
// Pages are the same fact recorder / page-scoped closer the analysis consumer
// uses, and Analysis is the live Task 37 consumer of Explore.
type p9Runtime struct {
	Explore  *versionadapter.ExploreWiring
	Sessions *resourceapp.SessionStore
	Facts    *versionapp.FactService
	Pages    exploreapp.PageCloser
	Analysis *p9Analysis
}

// newP9Runtime builds the explore runtime. Construction fails closed on a
// missing required collaborator instead of wiring a nil dependency that would
// only panic during an acceptance run.
func newP9Runtime(deps p9RuntimeDeps) (*p9Runtime, error) {
	if deps.Pool == nil {
		return nil, errors.New("p9 explore: nil database pool")
	}
	if deps.IDs == nil {
		return nil, errors.New("p9 explore: nil id generator")
	}
	if deps.Accounts == nil {
		return nil, errors.New("p9 explore: nil account store")
	}
	if deps.Versions == nil {
		return nil, errors.New("p9 explore: nil version binding reader")
	}
	if deps.Requirements == nil {
		return nil, errors.New("p9 explore: nil requirement reader")
	}
	if deps.Design == nil {
		return nil, errors.New("p9 explore: nil design graph reader")
	}
	sessions := resourceapp.NewSessionStore(resourceapp.DefaultSessionTTL)
	facts := versionapp.NewFactService(
		versionfactpg.NewFactRepository(deps.Pool, deps.IDs), deps.IDs, deps.Events)
	executor := exploreapp.NewHTTPExecutor(deps.ExecutorURL, deps.ExecutorToken, 0)
	wiring, err := versionadapter.NewExploreWiring(versionadapter.ExploreWiringDeps{
		Executor: executor,
		Accounts: deps.Accounts,
		Sessions: sessions,
		Facts:    facts,
		Versions: deps.Versions,
		Now:      deps.Now,
	})
	if err != nil {
		return nil, err
	}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      wiring,
		Pages:        executor,
		Facts:        facts,
		Recorder:     facts,
		Requirements: &p9RequirementSource{requirements: deps.Requirements, versions: deps.Versions},
		Cases:        &p9CaseSource{graph: deps.Design, versions: deps.Versions},
		Roles:        &p9RoleSource{versions: deps.Versions},
		Versions:     deps.Versions,
		Now:          deps.Now,
	})
	if err != nil {
		return nil, err
	}
	return &p9Runtime{
		Explore:  wiring,
		Sessions: sessions,
		Facts:    facts,
		Pages:    executor,
		Analysis: analysis,
	}, nil
}

/* ------------------------------ analysis consumer ------------------------- */

// p9AnalysisDeps are the narrow ports of the Task 37 analysis consumer. Runtime
// is the *same* process-wide explore wiring built above (never a rebuilt one),
// and Pages is the same HTTP executor's page-scoped closer.
type p9AnalysisDeps struct {
	Runtime      exploreapp.Runtime
	Pages        exploreapp.PageCloser
	Facts        exploreapp.FactReader
	Recorder     exploreapp.FactRecorder
	Requirements exploreapp.RequirementSource
	Cases        exploreapp.CaseSource
	Roles        exploreapp.RoleSource
	Versions     versionadapter.VersionService
	Now          func() time.Time
}

// p9Analysis is the deterministic exploration analysis bundle plus the
// leader-gated, event-driven refresh loop that consumes the explore runtime.
type p9Analysis struct {
	Coverage *exploreapp.Coverage
	Augment  *exploreapp.CaseAugment
	Failure  *exploreapp.FailureAttribution

	deps     p9AnalysisDeps
	debounce *p9AnalysisDebounce
	wake     chan int64
	logf     func(format string, args ...any)
}

// newP9Analysis wires the analysis services over the explore runtime. Coverage
// and case augmentation read the same fact store the other explore capabilities
// write; failure attribution is deterministic and dependency-free.
func newP9Analysis(deps p9AnalysisDeps) (*p9Analysis, error) {
	if deps.Runtime == nil {
		return nil, errors.New("p9 explore analysis: nil explore runtime")
	}
	if deps.Facts == nil {
		return nil, errors.New("p9 explore analysis: nil fact reader")
	}
	if deps.Recorder == nil {
		return nil, errors.New("p9 explore analysis: nil fact recorder")
	}
	if deps.Requirements == nil {
		return nil, errors.New("p9 explore analysis: nil requirement source")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &p9Analysis{
		Coverage: exploreapp.NewCoverage(exploreapp.CoverageConfig{}, exploreapp.CoverageDeps{
			Requirements: deps.Requirements,
			Cases:        deps.Cases,
			Facts:        deps.Facts,
			Roles:        deps.Roles,
		}),
		Augment: exploreapp.NewCaseAugment(exploreapp.CaseAugmentConfig{}, exploreapp.CaseAugmentDeps{
			Facts: deps.Facts,
			Cases: deps.Cases,
		}),
		Failure:  exploreapp.NewFailureAttribution(),
		deps:     deps,
		debounce: newP9AnalysisDebounce(p9AnalysisMinInterval, now),
		wake:     make(chan int64, p9AnalysisWakeBuffer),
		logf:     log.Printf,
	}, nil
}

// p9AnalysisResult is the bounded summary of one refreshed version.
type p9AnalysisResult struct {
	Requirements int
	Cases        int
	Pages        int
	APIs         int
	Roles        int
	Gaps         int
	Drafts       int
	LoginStatus  string
}

// Notify coalesces one version refresh request. A full buffer drops the hint
// instead of blocking the event publish path; the next event retries.
func (a *p9Analysis) Notify(versionID int64) {
	if a == nil || versionID <= 0 {
		return
	}
	select {
	case a.wake <- versionID:
	default:
	}
}

// Run consumes refresh hints until the process context ends. It is leader-gated
// so only one replica performs the probe/write, and the per-version debounce
// keeps an event storm to one refresh per interval.
func (a *p9Analysis) Run(ctx context.Context, shouldRun func() bool) {
	if a == nil {
		return
	}
	logf := a.logf
	if logf == nil {
		logf = log.Printf
	}
	for {
		select {
		case <-ctx.Done():
			return
		case versionID := <-a.wake:
			if shouldRun != nil && !shouldRun() {
				continue
			}
			if !a.debounce.due(versionID) {
				continue
			}
			result, err := a.Refresh(ctx, versionID)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					logf("eztest: P9 explore analysis version=%d failed: %v", versionID, err)
				}
				continue
			}
			logf("eztest: P9 explore analysis version=%d requirements=%d cases=%d pages=%d apis=%d roles=%d gaps=%d drafts=%d login=%s",
				versionID, result.Requirements, result.Cases, result.Pages, result.APIs,
				result.Roles, result.Gaps, result.Drafts, result.LoginStatus)
		}
	}
}

// Refresh runs one deterministic analysis pass for a version: the static gap
// dimensions and, when the version binds a SUT/role, the authenticated live page
// probes through the shared explore runtime. It builds a per-version gap
// analysis because the login binding (version SUT / role / entry page) is
// version-scoped, then records the gap facts and reads coverage/draft gaps back.
func (a *p9Analysis) Refresh(ctx context.Context, versionID int64) (p9AnalysisResult, error) {
	result := p9AnalysisResult{}
	if a == nil || a.deps.Requirements == nil {
		return result, errors.New("p9 explore analysis is not configured")
	}
	if versionID <= 0 {
		return result, errors.New("p9 explore analysis requires a positive version id")
	}

	login := a.loginBinding(ctx, versionID)
	gapDeps := exploreapp.GapAnalysisDeps{
		Requirements: a.deps.Requirements,
		Facts:        a.deps.Recorder,
		FactReader:   a.deps.Facts,
	}
	// The runtime is only used when a login binding exists: probing without a
	// resolvable account would fail every action. Static gaps still run.
	if login.role != "" && login.versionSUTID > 0 {
		gapDeps.Runtime = a.deps.Runtime
		gapDeps.Pages = a.deps.Pages
	}
	gaps := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{
		VersionSUTID:    login.versionSUTID,
		EnvironmentID:   login.environmentID,
		Role:            login.role,
		LoginURL:        login.loginURL,
		MaxRequirements: p9AnalysisMaxRequirements,
		MaxProbes:       p9AnalysisMaxProbes,
	}, gapDeps)
	gapResult, err := gaps.Run(ctx, versionID)
	if err != nil {
		return result, err
	}
	result.Gaps = len(gapResult.Gaps)
	result.LoginStatus = gapResult.LoginStatus

	matrix, err := a.Coverage.Build(ctx, versionID)
	if err != nil {
		return result, err
	}
	result.Requirements = matrix.Totals.Requirements
	result.Cases = matrix.Totals.Cases
	result.Pages = matrix.Totals.Pages
	result.APIs = matrix.Totals.APIs
	result.Roles = matrix.Totals.Roles

	augmented, err := a.Augment.Run(ctx, versionID)
	if err != nil {
		return result, err
	}
	result.Drafts = len(augmented.Drafts)
	return result, nil
}

// p9LoginBinding is the version-scoped login/probe binding resolved from the
// version read model. An empty binding (no version SUT or role) degrades to the
// static analysis.
type p9LoginBinding struct {
	versionSUTID  int64
	environmentID int64
	role          string
	loginURL      string
}

func (a *p9Analysis) loginBinding(ctx context.Context, versionID int64) p9LoginBinding {
	if a.deps.Versions == nil {
		return p9LoginBinding{}
	}
	view, err := a.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return p9LoginBinding{}
	}
	for _, binding := range view.Bindings {
		if binding.ID <= 0 {
			continue
		}
		role := p9FirstAccountRole(binding.Accounts)
		return p9LoginBinding{
			versionSUTID:  binding.ID,
			environmentID: parseCaseBindingID(binding.Environment.ID),
			role:          role,
			loginURL:      p9LoginPageURL(binding.Pages),
		}
	}
	return p9LoginBinding{}
}

// p9LoginPageURL prefers a bound page whose URL/route looks like a login page
// (so a fresh login can find a form), falling back to the first bound page for
// the reuse-verification open.
func p9LoginPageURL(pages []resourceapp.PageView) string {
	fallback := ""
	for _, page := range pages {
		url := p9FirstNonEmpty(page.URL, page.RouteTemplate)
		if url == "" {
			continue
		}
		if fallback == "" {
			fallback = url
		}
		lowered := strings.ToLower(page.URL + " " + page.RouteTemplate)
		if strings.Contains(lowered, "login") || strings.Contains(lowered, "signin") || strings.Contains(lowered, "sign-in") {
			return url
		}
	}
	return fallback
}

/* --------------------------- debounce ---------------------------- */

// p9AnalysisDebounce coalesces refresh hints per version. A version is due when
// it was never refreshed or the minimum interval elapsed.
type p9AnalysisDebounce struct {
	minInterval time.Duration
	now         func() time.Time
	seen        map[int64]time.Time
}

func newP9AnalysisDebounce(minInterval time.Duration, now func() time.Time) *p9AnalysisDebounce {
	if minInterval <= 0 {
		minInterval = p9AnalysisMinInterval
	}
	if now == nil {
		now = time.Now
	}
	return &p9AnalysisDebounce{minInterval: minInterval, now: now, seen: map[int64]time.Time{}}
}

// due reports whether the version may refresh now, and records the attempt so
// concurrent hints within the interval are dropped.
func (d *p9AnalysisDebounce) due(versionID int64) bool {
	if d == nil || versionID <= 0 {
		return false
	}
	now := d.now()
	if last, ok := d.seen[versionID]; ok && now.Sub(last) < d.minInterval {
		return false
	}
	d.seen[versionID] = now
	return true
}

/* ------------------------- design fact records --------------------------- */

// p9FactCurrentReader is the read surface of the version fact service the
// design case-author projection consumes.
type p9FactCurrentReader interface {
	Current(ctx context.Context, versionID int64, kind string) ([]versiondomain.Fact, error)
}

// p9FactRecords adapts the version fact service to the design context's neutral
// discovery-fact record, so the case author reads exploration facts through the
// design graph without design importing the version context.
type p9FactRecords struct {
	facts p9FactCurrentReader
}

// Current implements designapp.DiscoveryFactsReader.
func (r p9FactRecords) Current(ctx context.Context, versionID int64, kind string) ([]designapp.DiscoveryFactRecord, error) {
	if r.facts == nil {
		return nil, errors.New("p9 explore: fact service is not configured")
	}
	facts, err := r.facts.Current(ctx, versionID, kind)
	if err != nil {
		return nil, err
	}
	records := make([]designapp.DiscoveryFactRecord, 0, len(facts))
	for _, fact := range facts {
		records = append(records, designapp.DiscoveryFactRecord{
			ID:         fact.ID,
			Kind:       fact.Kind,
			Key:        fact.Key,
			Payload:    fact.Payload,
			Confidence: fact.Confidence,
		})
	}
	return records, nil
}

/* --------------------------- version adapters ---------------------------- */

// p9BindingKeySet maps resource IDs to the knowledge keys the exploration facts
// use (page route template / `METHOD /path`).
type p9BindingKeySet struct {
	pages map[string]string
	apis  map[string]string
	roles []string
}

// p9BindingKeys reads the version bindings and projects them onto resource-id
// to knowledge-key maps. A read failure degrades to an empty key set (the
// evidence is best effort; the requirement/design reads are the authority).
func p9BindingKeys(ctx context.Context, versions versionadapter.VersionService, versionID int64) p9BindingKeySet {
	keys := p9BindingKeySet{pages: map[string]string{}, apis: map[string]string{}}
	if versions == nil {
		return keys
	}
	view, err := versions.Get(ctx, versionID)
	if err != nil {
		return keys
	}
	roles := map[string]bool{}
	for _, binding := range view.Bindings {
		for _, page := range binding.Pages {
			key := strings.TrimSpace(page.RouteTemplate)
			if key == "" {
				key = strings.TrimSpace(page.URL)
			}
			if len(key) > 0 {
				keys.pages[strings.TrimSpace(page.ID)] = key
			}
		}
		for _, api := range binding.APIs {
			if key := caseBindingAPIKey(api.Method, api.PathTemplate); key != "" {
				keys.apis[strings.TrimSpace(api.ID)] = key
			}
		}
		for _, account := range binding.Accounts {
			for _, role := range account.Roles {
				if trimmed := strings.TrimSpace(role); trimmed != "" {
					roles[trimmed] = true
				}
			}
		}
	}
	keys.roles = make([]string, 0, len(roles))
	for role := range roles {
		keys.roles = append(keys.roles, role)
	}
	sort.Strings(keys.roles)
	return keys
}

// p9RequirementSource projects the version requirements (with their current
// revision text and knowledge-key targets) onto the exploration analysis input.
type p9RequirementSource struct {
	requirements p9RequirementReader
	versions     versionadapter.VersionService
}

func (s *p9RequirementSource) Requirements(ctx context.Context, versionID int64) ([]exploreapp.RequirementSnapshot, error) {
	if s == nil || s.requirements == nil {
		return nil, errors.New("p9 explore: requirement reader is not configured")
	}
	views, _, err := s.requirements.List(ctx, versionID, "", p9AnalysisMaxRequirements, nil)
	if err != nil {
		return nil, err
	}
	keys := p9BindingKeys(ctx, s.versions, versionID)
	snapshots := make([]exploreapp.RequirementSnapshot, 0, len(views))
	for _, view := range views {
		status := strings.ToLower(strings.TrimSpace(view.Status))
		if status == "obsolete" || status == "closed" {
			continue
		}
		detail, err := s.requirements.Get(ctx, versionID, view.ID)
		if err != nil {
			return nil, err
		}
		current := detail.CurrentVersion
		snapshots = append(snapshots, exploreapp.RequirementSnapshot{
			ID:                 view.ID,
			VersionID:          current.ID,
			Title:              p9FirstNonEmpty(view.Title, current.Title),
			Description:        current.Description,
			AcceptanceCriteria: current.AcceptanceCriteria,
			Constraints:        current.Constraints,
			Status:             view.Status,
			TargetPages:        p9MappedKeys(current.TargetPages, keys.pages),
			TargetAPIs:         p9MappedKeys(current.TargetAPIs, keys.apis),
		})
	}
	return snapshots, nil
}

// p9CaseSource projects the design graph cases onto the coverage matrix input.
type p9CaseSource struct {
	graph    p9DesignGraphReader
	versions versionadapter.VersionService
}

func (s *p9CaseSource) Cases(ctx context.Context, versionID int64) ([]exploreapp.CaseSnapshot, error) {
	if s == nil || s.graph == nil {
		return nil, errors.New("p9 explore: design graph reader is not configured")
	}
	graph, err := s.graph.ReadGraph(ctx, versionID)
	if err != nil {
		return nil, err
	}
	keys := p9BindingKeys(ctx, s.versions, versionID)
	cases := make([]exploreapp.CaseSnapshot, 0)
	for _, requirement := range graph.Requirements {
		for _, scenario := range requirement.Scenarios {
			for _, card := range scenario.Cases {
				status := strings.ToLower(strings.TrimSpace(card.Status))
				if status != "draft" && status != "confirmed" {
					continue
				}
				cases = append(cases, exploreapp.CaseSnapshot{
					CaseID:        card.CaseID,
					CaseVersionID: card.CurrentVersionID,
					RequirementID: requirement.RequirementID,
					Title:         card.Title,
					Status:        card.Status,
					PageKeys:      p9MappedInt64Keys(card.PageIDs, keys.pages),
					APIKeys:       p9MappedInt64Keys(card.APIIDs, keys.apis),
				})
			}
		}
	}
	return cases, nil
}

// p9RoleSource exposes the version-bound account roles to the role matrix.
type p9RoleSource struct {
	versions versionadapter.VersionService
}

func (s *p9RoleSource) Roles(ctx context.Context, versionID int64) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	return p9BindingKeys(ctx, s.versions, versionID).roles, nil
}

/* ------------------------------- helpers -------------------------------- */

// p9MappedKeys resolves resource IDs to their knowledge keys; an unmapped target
// is dropped (an unbound page/API is not verifiable for this version SUT).
func p9MappedKeys(ids []string, mapping map[string]string) []string {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		if key, ok := mapping[strings.TrimSpace(id)]; ok && key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func p9MappedInt64Keys(ids []int64, mapping map[string]string) []string {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		if key, ok := mapping[strconv.FormatInt(id, 10)]; ok && key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func p9FirstAccountRole(accounts []resourceapp.AccountView) string {
	for _, account := range accounts {
		for _, role := range account.Roles {
			if trimmed := strings.TrimSpace(role); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// p9ExploreRuntime is the process-wide explore assembly. The Task 37 analysis
// consumer built in newP9Runtime holds its Explore wiring, so the runtime is
// live in production (event-triggered, leader-gated) instead of dead wiring.
var p9ExploreRuntime *p9Runtime

var _ exploreapp.Runtime = (*versionadapter.ExploreWiring)(nil)
var _ designapp.DiscoveryFactsReader = p9FactRecords{}
var _ exploreapp.RequirementSource = (*p9RequirementSource)(nil)
var _ exploreapp.CaseSource = (*p9CaseSource)(nil)
var _ exploreapp.RoleSource = (*p9RoleSource)(nil)

// p9FirstNonEmpty returns the first non-blank trimmed value.
func p9FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
