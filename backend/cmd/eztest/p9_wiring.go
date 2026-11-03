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
// Task 37 adds the analysis consumer (discovery pre-pass, gap / coverage / case
// augmentation) and binds it to that same runtime: the deterministic analysis
// logs in first, crawls the bound SUT pages and scans the bound repositories for
// API facts, then opens pages only after that Login, serializes its actions and
// closes exactly the pages it opened. The consumer is leader-gated and event-
// triggered with a per-version minimum interval, so the wiring is live in
// production without a background write storm.
//
// The P9 pre-discovery login is also the production login-success observation
// that sediments the approved login_recipe knowledge item (Task 67): the
// knowledge adapter is attached to the analysis after the P7 composition builds
// it (this runtime is constructed before the P7 knowledge adapter, so the
// attachment is a late binding and no knowledge adapter is rebuilt here). The
// case-run success hook stays wired by the execution runner at the assembly
// root, not by this file.

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/knowledgeadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionfactpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// P9 analysis bounds. A refresh is a bounded read plus a bounded set of live
// page probes; the per-version interval coalesces event storms. Discovery adds
// a bounded crawl and a bounded static source scan before the analysis reads
// the facts back.
const (
	p9AnalysisMinInterval     = 10 * time.Minute
	p9AnalysisMaxRequirements = 50
	p9AnalysisMaxProbes       = 3
	p9AnalysisMaxDiscovered   = 20
	// p9AnalysisEventMemory bounds the recently observed change-event identities.
	// The event observer is registered on both the local publish path and the
	// cross-instance listener, so the same persisted event can be delivered
	// twice; remembering its channel#seq prevents a duplicate delivery from
	// triggering a second full refresh.
	p9AnalysisEventMemory = 256
	// A discovery login can hit a transient executor condition (page cap reached
	// by a concurrent execution run, executor still starting). Retry the
	// retry_login outcomes a few times before giving up for this debounce window.
	p9LoginRetryAttempts     = 3
	p9LoginRetryDefaultDelay = 1 * time.Second
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
	// Case-augmentation drafts are persisted through the design service. The
	// composition interface is draft-only (CreateAugmentedDraftCase); a design
	// reader that does not expose it leaves augmentation draft-only, which is a
	// safe degradation rather than a silent confirm path.
	var draftWriter exploreapp.DraftCaseWriter
	if designer, ok := deps.Design.(p9AugmentedDraftDesigner); ok {
		draftWriter = newP9DraftCaseWriter(designer, deps.Versions)
	}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      wiring,
		Executor:     executor,
		Sources:      newP9SourceProvider(deps.Versions),
		Pages:        executor,
		Facts:        facts,
		Recorder:     facts,
		Requirements: &p9RequirementSource{requirements: deps.Requirements, versions: deps.Versions},
		Cases:        &p9CaseSource{graph: deps.Design, versions: deps.Versions},
		Roles:        &p9RoleSource{versions: deps.Versions},
		DraftWriter:  draftWriter,
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
	Runtime exploreapp.Runtime
	// Executor is the raw explore-action executor the runtime page crawl drives.
	// Executor/Sources are optional: without Executor the page crawl is skipped,
	// without Sources the static API scan is skipped, and the analysis keeps its
	// static/probe behavior.
	Executor     exploreapp.Executor
	Sources      exploreapp.SourceProvider
	Pages        exploreapp.PageCloser
	Facts        exploreapp.FactReader
	Recorder     exploreapp.FactRecorder
	Requirements exploreapp.RequirementSource
	Cases        exploreapp.CaseSource
	Roles        exploreapp.RoleSource
	// DraftWriter persists augmented case drafts. It is optional: without it the
	// augmentation stays draft-only (computed but not persisted).
	DraftWriter exploreapp.DraftCaseWriter
	Versions    versionadapter.VersionService
	Now         func() time.Time
}

// p9LoginRecipeWriter sediments one approved login_recipe knowledge item from a
// real login success (Task 67). *knowledgeadapter.Adapter implements it; the P9
// analysis depends on this narrow port so a test can observe the trigger and a
// degraded boot can run without the knowledge context.
type p9LoginRecipeWriter interface {
	SedimentLoginRecipe(ctx context.Context, fact knowledgeadapter.LoginRecipeFact) (knowledge.Item, error)
}

// p9Analysis is the deterministic exploration analysis bundle plus the
// leader-gated, event-driven refresh loop that consumes the explore runtime.
// Failure attribution is not part of this bundle: execution.result_read invokes
// the same explore FailureAttribution in the analyze.failure result (Task 37 fix
// round 1), so there is no unused construction here.
type p9Analysis struct {
	Coverage *exploreapp.Coverage
	Augment  *exploreapp.CaseAugment

	deps     p9AnalysisDeps
	debounce *p9AnalysisDebounce
	nowFn    func() time.Time
	// pending holds at most one hint per version: a burst coalesces into one
	// refresh request instead of filling (or overflowing) a channel. wake is a
	// one-slot signal that the pending set changed.
	pendingMu sync.Mutex
	pending   map[int64]p9AnalysisHint
	// seenEvents remembers the recently observed changed-event identities so the
	// duplicate delivery from the cross-instance listener is dropped instead of
	// triggering a second refresh. Its size is bounded by p9AnalysisEventMemory.
	seenMu    sync.Mutex
	seen      map[string]struct{}
	seenOrder []string
	wake      chan struct{}
	logf      func(format string, args ...any)
	// loginRetryDelay is the pause between retry_login discovery attempts. It
	// is a field so the retry test does not sleep.
	loginRetryDelay time.Duration
	// loginRecipes is the late-attached knowledge sedimentation port (Task 67):
	// the P7 knowledge adapter does not exist when this analysis is constructed.
	// The mutex guards the attach against the running analysis loop.
	recipeMu     sync.RWMutex
	loginRecipes p9LoginRecipeWriter
}

// p9AnalysisHint is one coalesced refresh request. changed marks an event that
// modified the analysis input (a requirement was created/updated/confirmed); at
// is the event observation time, used to decide whether a refresh that started
// later already saw the change.
type p9AnalysisHint struct {
	versionID int64
	changed   bool
	at        time.Time
}

// SetLoginRecipeWriter attaches the knowledge sedimentation port. It is a late
// binding because the P9 runtime is built before the P7 knowledge adapter in the
// assembly root; nil is a valid "knowledge context not wired" state.
func (a *p9Analysis) SetLoginRecipeWriter(writer p9LoginRecipeWriter) {
	if a == nil {
		return
	}
	a.recipeMu.Lock()
	a.loginRecipes = writer
	a.recipeMu.Unlock()
}

func (a *p9Analysis) loginRecipeWriter() p9LoginRecipeWriter {
	if a == nil {
		return nil
	}
	a.recipeMu.RLock()
	defer a.recipeMu.RUnlock()
	return a.loginRecipes
}

// p9AttachLoginRecipeWriter completes the Task 67 wiring once the P7 knowledge
// adapter exists. The P9 runtime is constructed before the P7 runtime (its
// version facts feed the design graph), so the attachment is a late binding
// performed by the P7 composition. A nil runtime is a no-op, keeping unit tests
// and a degraded boot safe.
func p9AttachLoginRecipeWriter(runtime *p9Runtime, writer p9LoginRecipeWriter) {
	if runtime == nil || runtime.Analysis == nil {
		return
	}
	runtime.Analysis.SetLoginRecipeWriter(writer)
}

// newP9Analysis wires the analysis services over the explore runtime. Coverage
// and case augmentation read the same fact store the other explore capabilities
// write; discovery is the pre-pass that records those facts, and case
// augmentation persists its drafts through the design draft writer.
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
			Facts:        deps.Facts,
			Cases:        deps.Cases,
			Writer:       deps.DraftWriter,
			Requirements: deps.Requirements,
		}),
		deps:     deps,
		debounce: newP9AnalysisDebounce(p9AnalysisMinInterval, now),
		nowFn:    now,
		pending:  map[int64]p9AnalysisHint{},
		seen:     map[string]struct{}{},
		wake:     make(chan struct{}, 1),
		logf:     log.Printf,

		loginRetryDelay: p9LoginRetryDefaultDelay,
	}, nil
}

// p9AnalysisResult is the bounded summary of one refreshed version.
type p9AnalysisResult struct {
	Requirements    int
	Cases           int
	Pages           int
	APIs            int
	Roles           int
	Gaps            int
	Drafts          int
	LoginStatus     string
	DiscoveredPages int
	DiscoveredAPIs  int
}

// currentTime returns the analysis clock, defaulting to the wall clock when a
// test or degraded assembly did not inject one.
func (a *p9Analysis) currentTime() time.Time {
	if a == nil || a.nowFn == nil {
		return time.Now()
	}
	return a.nowFn()
}

// Notify coalesces one regular version refresh request. The pending set keeps at
// most one hint per version and a full pending set coalesces instead of
// dropping: a new event only replaces the queued hint, and the one-slot wake
// signal is never lost while a hint exists.
func (a *p9Analysis) Notify(versionID int64) {
	if a == nil || versionID <= 0 {
		return
	}
	a.notify(p9AnalysisHint{versionID: versionID, at: a.currentTime()})
}

// NotifyEvent routes one published event to the analysis. Requirement events
// changed the analysis input, so they bypass the regular 10-minute per-version
// interval; the refresh coalesces normally (a refresh that starts after the
// change already covers it). Other events keep the regular cadence. eventKey is
// the persistent identity (channel#seq) used to drop the duplicate delivery of
// the same event; an empty key skips deduplication.
func (a *p9Analysis) NotifyEvent(versionID int64, eventType, eventKey string) {
	if a == nil || versionID <= 0 {
		return
	}
	changed := p9AnalysisInputChanged(eventType)
	if changed && a.alreadySeenEvent(eventKey) {
		return
	}
	a.notify(p9AnalysisHint{
		versionID: versionID,
		changed:   changed,
		at:        a.currentTime(),
	})
}

// alreadySeenEvent records one changed-event identity and reports whether it
// was already observed. The ring keeps at most p9AnalysisEventMemory identities,
// so a long-lived process cannot grow the set without bound.
func (a *p9Analysis) alreadySeenEvent(eventKey string) bool {
	if eventKey == "" {
		return false
	}
	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	if a.seen == nil {
		a.seen = map[string]struct{}{}
	}
	if _, ok := a.seen[eventKey]; ok {
		return true
	}
	a.seen[eventKey] = struct{}{}
	a.seenOrder = append(a.seenOrder, eventKey)
	if len(a.seenOrder) > p9AnalysisEventMemory {
		oldest := a.seenOrder[0]
		a.seenOrder = a.seenOrder[1:]
		delete(a.seen, oldest)
	}
	return false
}

// p9AnalysisInputChanged reports whether an event changed the data the analysis
// reads (requirements created/updated/confirmed/rolled back). Such events must
// not be swallowed by the regular refresh interval.
func p9AnalysisInputChanged(eventType string) bool {
	return strings.HasPrefix(strings.TrimSpace(eventType), "requirement.")
}

// notify merges one hint into the per-version pending set. The event time is the
// later of the two (a refresh must not be considered covered by a change that
// happened after it started) and the changed flag is sticky.
func (a *p9Analysis) notify(hint p9AnalysisHint) {
	a.pendingMu.Lock()
	if existing, ok := a.pending[hint.versionID]; ok {
		if existing.at.After(hint.at) {
			hint.at = existing.at
		}
		hint.changed = hint.changed || existing.changed
	}
	a.pending[hint.versionID] = hint
	a.pendingMu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// takePending drains the coalesced hints in deterministic version order.
func (a *p9Analysis) takePending() []p9AnalysisHint {
	if a == nil {
		return nil
	}
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pending) == 0 {
		return nil
	}
	hints := make([]p9AnalysisHint, 0, len(a.pending))
	for _, hint := range a.pending {
		hints = append(hints, hint)
	}
	// The pending set is bounded by the number of versions seen since the last
	// drain, so this allocation is bounded too.
	a.pending = make(map[int64]p9AnalysisHint, len(hints))
	sort.Slice(hints, func(i, j int) bool { return hints[i].versionID < hints[j].versionID })
	return hints
}

// Run consumes refresh hints until the process context ends. It is leader-gated
// so only one replica performs the probe/write. Regular hints are coalesced to
// one refresh per version per interval; an input-changing requirement event
// bypasses that interval, so a version created before its requirements existed
// is refreshed again when the requirements are actually created/curated and
// confirmed.
func (a *p9Analysis) Run(ctx context.Context, shouldRun func() bool) {
	if a == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
			for _, hint := range a.takePending() {
				if shouldRun != nil && !shouldRun() {
					// Only the leader probes/writes. The next event (or the next
					// leader's event stream) retries; a non-leader must not build
					// up an unbounded backlog.
					continue
				}
				// runHint logs its own outcome; the loop keeps consuming hints.
				_, _ = a.runHint(ctx, hint)
			}
		}
	}
}

// logLine writes one analysis diagnostic, defaulting to the process logger for
// a bare struct literal (tests and degraded assemblies).
func (a *p9Analysis) logLine(format string, args ...any) {
	if a == nil {
		return
	}
	logf := a.logf
	if logf == nil {
		logf = log.Printf
	}
	logf(format, args...)
}

// runHint applies the debounce policy and performs one refresh. It reports
// whether a refresh actually ran; the bool is what the trigger regression test
// asserts (no refresh was dropped, no storm ran twice for the same input).
func (a *p9Analysis) runHint(ctx context.Context, hint p9AnalysisHint) (bool, error) {
	if a == nil || hint.versionID <= 0 {
		return false, nil
	}
	startedAt := a.currentTime()
	if hint.changed {
		if !a.debounce.dueChanged(hint.versionID, hint.at) {
			return false, nil
		}
	} else if !a.debounce.due(hint.versionID) {
		return false, nil
	}
	result, err := a.Refresh(ctx, hint.versionID)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			a.logLine("eztest: P9 explore analysis version=%d failed: %v", hint.versionID, err)
		}
		return true, err
	}
	// Only a successful refresh marks the change covered: a failed pass did not
	// observe the requirements, so a later hint must retry.
	a.debounce.completedAt(hint.versionID, startedAt)
	a.logLine("eztest: P9 explore analysis version=%d requirements=%d cases=%d pages=%d apis=%d roles=%d gaps=%d drafts=%d login=%s",
		hint.versionID, result.Requirements, result.Cases, result.Pages, result.APIs,
		result.Roles, result.Gaps, result.Drafts, result.LoginStatus)
	return true, nil
}

// Refresh runs one deterministic analysis pass for a version: first the
// authenticated discovery pre-pass (runtime page crawl + static API scan), then
// the static gap dimensions and the live page probes over the facts discovery
// just recorded. It builds a per-version gap analysis because the login binding
// (version SUT / role / entry page) is version-scoped, then records the gap
// facts and reads coverage/draft gaps back.
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
	runtime := a.deps.Runtime
	// The runtime is only used when a login binding exists: probing without a
	// resolvable account would fail every action. Static gaps still run.
	if login.role != "" && login.versionSUTID > 0 {
		// Discovery must run with an authenticated session: log in once here,
		// record page/API facts, then hand the same session to the gap probes so
		// they do not log in a second time. The successful login is also the
		// real observation that sediments the approved login_recipe knowledge
		// item; sedimentation is best effort and can never fail the login.
		if fact, ok := a.loginForDiscovery(ctx, versionID, login); ok {
			if fact.Status == exploreapp.SessionActive {
				a.sedimentLoginRecipe(ctx, versionID, login, fact)
				a.discover(ctx, versionID, login, fact, &result)
				runtime = p9SessionRuntime{runtime: runtime, fact: fact}
			}
		}
		gapDeps.Runtime = runtime
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

	// Case augmentation is the last pass and its drafts are a best-effort
	// enhancement: a single ineligible requirement or a writer contract error
	// must never discard the gaps/coverage already computed (or abort the
	// refresh loop). Per-draft writer failures are isolated inside CaseAugment
	// and reported here; a hard error leaves the partial result in place.
	augmented, augmentErr := a.Augment.Run(ctx, versionID)
	if a.deps.DraftWriter != nil {
		// Report the drafts that actually reached the design store, not the
		// candidates that were only computed. This stays accurate when a hard
		// augmentation error interrupts the run after some drafts were written.
		result.Drafts = len(augmented.Created)
	} else if augmentErr == nil {
		result.Drafts = len(augmented.Drafts)
	}
	logf := a.logf
	if logf == nil {
		logf = log.Printf
	}
	if augmentErr != nil {
		logf("eztest: P9 explore analysis version=%d case augmentation failed: %v", versionID, augmentErr)
	} else if len(augmented.Failed) > 0 {
		logf("eztest: P9 explore analysis version=%d case augmentation partial: created=%d failed=%d skipped=%d",
			versionID, len(augmented.Created), len(augmented.Failed), augmented.Skipped)
	}
	return result, nil
}

// p9SessionRuntime re-surfaces an already-established session to the gap
// probes. Login does not touch the underlying runtime again: the analysis logged
// in once before discovery and the shared session cache owns the credentials.
type p9SessionRuntime struct {
	runtime exploreapp.Runtime
	fact    exploreapp.SessionFact
}

func (r p9SessionRuntime) Login(context.Context, exploreapp.LoginFlowConfig) (exploreapp.SessionFact, error) {
	return r.fact, nil
}

func (r p9SessionRuntime) OpenWithSession(ctx context.Context, cfg exploreapp.LoginFlowConfig, url string) (exploreapp.Page, exploreapp.SessionFact, error) {
	if r.runtime == nil {
		return exploreapp.Page{}, exploreapp.SessionFact{}, errors.New("p9 explore analysis: session runtime is not configured")
	}
	return r.runtime.OpenWithSession(ctx, cfg, url)
}

// loginForDiscovery logs in once for the discovery pre-pass, retrying the
// transient retry_login outcomes a bounded number of times. A blocked or failed
// login is reported in the result and skips discovery, but never fails the
// static analysis. ok is false when there is no runtime to log in with.
func (a *p9Analysis) loginForDiscovery(ctx context.Context, versionID int64, binding p9LoginBinding) (exploreapp.SessionFact, bool) {
	if a.deps.Runtime == nil {
		return exploreapp.SessionFact{}, false
	}
	logf := a.logf
	if logf == nil {
		logf = log.Printf
	}
	delay := a.loginRetryDelay
	if delay < 0 {
		delay = 0
	}
	var fact exploreapp.SessionFact
	for attempt := 1; attempt <= p9LoginRetryAttempts; attempt++ {
		var err error
		fact, err = a.deps.Runtime.Login(ctx, exploreapp.LoginFlowConfig{
			VersionID:     versionID,
			VersionSUTID:  binding.versionSUTID,
			EnvironmentID: binding.environmentID,
			Role:          binding.role,
			LoginURL:      binding.loginURL,
		})
		if err != nil {
			logf("eztest: P9 explore analysis version=%d login for discovery failed: %v", versionID, err)
			return exploreapp.SessionFact{}, false
		}
		if fact.Status == exploreapp.SessionActive || fact.Action != exploreapp.ActionRetryLogin {
			break
		}
		if attempt < p9LoginRetryAttempts {
			logf("eztest: P9 explore analysis version=%d login for discovery transient (attempt %d/%d): reason=%s action=%s",
				versionID, attempt, p9LoginRetryAttempts, fact.Reason, fact.Action)
			if delay > 0 {
				select {
				case <-ctx.Done():
					return fact, true
				case <-time.After(delay):
				}
			}
		}
	}
	if fact.Status != exploreapp.SessionActive {
		logf("eztest: P9 explore analysis version=%d login for discovery blocked: reason=%s action=%s",
			versionID, fact.Reason, fact.Action)
	}
	return fact, true
}

// sedimentLoginRecipe hands one real login success to the knowledge adapter so
// it sediments the approved login_recipe bound to the version SUT/environment/
// login page/account role. The recorded login_session fact is the source
// provenance and the recipe is credential-free by construction (the login flow
// never puts a credential in SessionFact). The call is best effort: a nil
// writer, a sedimentation error or even a panic is logged and the login result,
// discovery and analysis outcome stay untouched.
func (a *p9Analysis) sedimentLoginRecipe(ctx context.Context, versionID int64, binding p9LoginBinding, session exploreapp.SessionFact) {
	if a == nil || session.Status != exploreapp.SessionActive || len(session.Recipe) == 0 {
		return
	}
	writer := a.loginRecipeWriter()
	if writer == nil {
		return
	}
	logf := a.logf
	if logf == nil {
		logf = log.Printf
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logf("eztest: P9 explore analysis version=%d login recipe sedimentation panicked: %v", versionID, recovered)
		}
	}()
	fact := knowledgeadapter.LoginRecipeFact{
		VersionID:     versionID,
		VersionSUTID:  p9FirstPositive(session.VersionSUTID, binding.versionSUTID),
		SystemID:      binding.systemID,
		EnvironmentID: p9FirstPositive(session.EnvironmentID, binding.environmentID),
		SystemKey:     binding.systemKey,
		AccountRole:   p9FirstNonEmpty(session.AccountRole, binding.role),
		PageKey:       binding.loginPageKey,
		Recipe:        session.Recipe,
		SourceFactID:  session.FactID,
	}
	if _, err := writer.SedimentLoginRecipe(ctx, fact); err != nil {
		logf("eztest: P9 explore analysis version=%d login recipe sedimentation failed: %v", versionID, err)
	}
}

// discover runs the bounded discovery pre-pass and records page/API facts. The
// crawl is DOM-based (no browser network capture), uses the session captured by
// Login, and closes exactly the page it created through the shared page-scoped
// closer. Every failure is best effort: the static analysis still runs, and the
// next refresh retries the discovery.
func (a *p9Analysis) discover(ctx context.Context, versionID int64, binding p9LoginBinding, session exploreapp.SessionFact, result *p9AnalysisResult) {
	if a.deps.Executor != nil {
		if startURL, seedURLs, ok := p9DiscoverySeeds(binding); ok {
			discovery := exploreapp.NewPageDiscovery(exploreapp.PageDiscoveryConfig{
				VersionSUTID:    binding.versionSUTID,
				EnvironmentID:   binding.environmentID,
				StartURL:        startURL,
				SeedURLs:        seedURLs,
				StorageStateRef: session.StorageStateRef,
				MaxPages:        p9AnalysisMaxDiscovered,
			}, exploreapp.PageDiscoveryDeps{
				Executor:   a.deps.Executor,
				Facts:      a.deps.Recorder,
				PageCloser: a.deps.Pages,
			})
			pageResult, err := discovery.Run(ctx, versionID)
			if err != nil {
				a.logf("eztest: P9 explore analysis version=%d page discovery failed: %v", versionID, err)
			} else {
				result.DiscoveredPages = len(pageResult.Pages)
				// A result page id only appears when discovery had no PageCloser;
				// release it here so a degraded assembly cannot leak the page.
				if pageID := strings.TrimSpace(pageResult.PageID); pageID != "" && a.deps.Pages != nil {
					_ = a.deps.Pages.ClosePage(context.WithoutCancel(ctx), pageID)
				}
			}
		}
	}
	if a.deps.Sources != nil {
		discovery := exploreapp.NewAPIDiscovery(exploreapp.APIDiscoveryConfig{
			VersionSUTID:  binding.versionSUTID,
			EnvironmentID: binding.environmentID,
		}, exploreapp.APIDiscoveryDeps{
			Sources: a.deps.Sources,
			Facts:   a.deps.Recorder,
		})
		apiResult, err := discovery.Run(ctx, versionID)
		if err != nil {
			a.logf("eztest: P9 explore analysis version=%d api discovery failed: %v", versionID, err)
			return
		}
		result.DiscoveredAPIs = len(apiResult.APIs)
	}
}

// p9DiscoverySeeds builds the crawl seeds from the bound pages. Only absolute
// http(s) URLs can seed the crawl; route templates are knowledge keys, not
// navigable locations. The login page stays first when no application page is
// bound so a fresh deployment still discovers something.
func p9DiscoverySeeds(binding p9LoginBinding) (string, []string, bool) {
	urls := make([]string, 0, len(binding.pageURLs)+1)
	seen := make(map[string]bool, len(binding.pageURLs)+1)
	appendURL := func(raw string) {
		trimmed := strings.TrimSpace(raw)
		lowered := strings.ToLower(trimmed)
		if trimmed == "" || (!strings.HasPrefix(lowered, "http://") && !strings.HasPrefix(lowered, "https://")) || seen[trimmed] {
			return
		}
		seen[trimmed] = true
		urls = append(urls, trimmed)
	}
	for _, pageURL := range binding.pageURLs {
		appendURL(pageURL)
	}
	appendURL(binding.loginURL)
	if len(urls) == 0 {
		return "", nil, false
	}
	return urls[0], urls[1:], true
}

// p9LoginBinding is the version-scoped login/probe binding resolved from the
// version read model. An empty binding (no version SUT or role) degrades to the
// static analysis.
type p9LoginBinding struct {
	versionSUTID  int64
	environmentID int64
	role          string
	loginURL      string
	// systemID/systemKey are the bound SUT identity the login recipe knowledge
	// item is scoped and bound to.
	systemID  int64
	systemKey string
	// loginPageKey is the route template of the bound login page (the knowledge
	// page key). It is empty when the login URL falls back to the environment
	// base URL, which is not a page resource.
	loginPageKey string
	// environmentBaseURL is the bound environment base URL. It is the login and
	// crawl seed fallback when the version binds no page resources.
	environmentBaseURL string
	// pageURLs are the absolute bound page URLs the discovery crawl can seed.
	pageURLs []string
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
		environmentBaseURL := strings.TrimSpace(binding.Environment.BaseURL)
		loginURL, loginPageKey := p9LoginPage(binding.Pages)
		// A version may bind no page resources (the S1 acceptance binds only the
		// SUT/environment/accounts/repository). The environment base URL is the
		// only navigable location then: it seeds the fresh login (which can open
		// a modal login trigger) and the page crawl.
		if loginURL == "" {
			loginURL = environmentBaseURL
			loginPageKey = ""
		}
		systemKey := ""
		if binding.SUT.Family != nil {
			systemKey = strings.TrimSpace(*binding.SUT.Family)
		}
		return p9LoginBinding{
			versionSUTID:       binding.ID,
			systemID:           parseCaseBindingID(binding.SUT.ID),
			systemKey:          systemKey,
			environmentID:      parseCaseBindingID(binding.Environment.ID),
			role:               role,
			loginURL:           loginURL,
			loginPageKey:       loginPageKey,
			environmentBaseURL: environmentBaseURL,
			pageURLs:           p9PageURLs(binding.Pages),
		}
	}
	return p9LoginBinding{}
}

// p9PageURLs lists the navigable bound pages in binding order.
func p9PageURLs(pages []resourceapp.PageView) []string {
	urls := make([]string, 0, len(pages))
	for _, page := range pages {
		if url := strings.TrimSpace(page.URL); url != "" {
			urls = append(urls, url)
		}
	}
	return urls
}

// p9LoginPage prefers a bound page whose URL/route looks like a login page (so
// a fresh login can find a form), falling back to the first bound page for the
// reuse-verification open. It returns the page URL and the page's knowledge key
// (route template), which is empty for a page without one.
func p9LoginPage(pages []resourceapp.PageView) (string, string) {
	fallbackURL, fallbackKey := "", ""
	for _, page := range pages {
		url := p9FirstNonEmpty(page.URL, page.RouteTemplate)
		if url == "" {
			continue
		}
		key := strings.TrimSpace(page.RouteTemplate)
		if fallbackURL == "" {
			fallbackURL, fallbackKey = url, key
		}
		lowered := strings.ToLower(page.URL + " " + page.RouteTemplate)
		if strings.Contains(lowered, "login") || strings.Contains(lowered, "signin") || strings.Contains(lowered, "sign-in") {
			return url, key
		}
	}
	return fallbackURL, fallbackKey
}

// p9LoginPageURL is the URL-only projection of p9LoginPage.
func p9LoginPageURL(pages []resourceapp.PageView) string {
	url, _ := p9LoginPage(pages)
	return url
}

/* --------------------------- debounce ---------------------------- */

// p9AnalysisDebounce coalesces refresh hints per version. Regular hints keep a
// minimum interval per version. Input-changing hints bypass that interval but are
// covered by a successful refresh that started after the change: a burst of
// requirement events therefore causes at most one extra pass, while a
// confirmation after the last pass always triggers a new one.
type p9AnalysisDebounce struct {
	minInterval time.Duration
	now         func() time.Time
	// attempted is the last refresh start per version (regular rate limit).
	attempted map[int64]time.Time
	// completed is the start time of the last successful refresh. A changed hint
	// whose event time is at or before it was already observed by that pass.
	completed map[int64]time.Time
}

func newP9AnalysisDebounce(minInterval time.Duration, now func() time.Time) *p9AnalysisDebounce {
	if minInterval <= 0 {
		minInterval = p9AnalysisMinInterval
	}
	if now == nil {
		now = time.Now
	}
	return &p9AnalysisDebounce{
		minInterval: minInterval,
		now:         now,
		attempted:   map[int64]time.Time{},
		completed:   map[int64]time.Time{},
	}
}

// due reports whether the version may refresh now for a regular hint, and
// records the attempt so concurrent hints within the interval are dropped.
func (d *p9AnalysisDebounce) due(versionID int64) bool {
	if d == nil || versionID <= 0 {
		return false
	}
	now := d.now()
	if last, ok := d.attempted[versionID]; ok && now.Sub(last) < d.minInterval {
		return false
	}
	d.attempted[versionID] = now
	return true
}

// dueChanged reports whether a changed-input hint needs a refresh. A hint is
// covered when a successful refresh started at or after the event; otherwise it
// bypasses the regular interval and refreshes now.
func (d *p9AnalysisDebounce) dueChanged(versionID int64, at time.Time) bool {
	if d == nil || versionID <= 0 {
		return false
	}
	now := d.now()
	if at.IsZero() {
		at = now
	}
	if completed, ok := d.completed[versionID]; ok && !at.After(completed) {
		return false
	}
	d.attempted[versionID] = now
	return true
}

// completedAt records the start time of a successful refresh so later changed
// hints that were already observed by it coalesce instead of re-running.
func (d *p9AnalysisDebounce) completedAt(versionID int64, startedAt time.Time) {
	if d == nil || versionID <= 0 || startedAt.IsZero() {
		return
	}
	if last, ok := d.completed[versionID]; !ok || startedAt.After(last) {
		d.completed[versionID] = startedAt
	}
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
var _ p9LoginRecipeWriter = (*knowledgeadapter.Adapter)(nil)

// p9FirstNonEmpty returns the first non-blank trimmed value.
func p9FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// p9FirstPositive returns the first positive value, or zero when none is set.
func p9FirstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
