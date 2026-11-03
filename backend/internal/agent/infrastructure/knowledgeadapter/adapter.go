// Package knowledgeadapter adapts the P7 knowledge bounded context to the
// agent runtime, the execution success hook and the tool ports. It is the only
// bridge between agent/** and the knowledge context: the agent application and
// tools packages never import a knowledge application service.
//
// Wiring is owned by cmd/eztest:
//
//	knowledgeAdapter := knowledgeadapter.New(knowledgeService, versionSystemReader)
//	knowledgeAdapter.SetVersionBindings(versionCaseBindings)
//	executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{KnowledgeHook: knowledgeAdapter, ...})
//	agentapp.NewRunner(agentapp.RunnerDeps{Knowledge: knowledgeAdapter, KnowledgeUsage: knowledgeAdapter, ...})
//	tools.Deps{P7: tools.P7Deps{Knowledge: knowledgeAdapter, Retrieval: knowledgeAdapter}}
//
// versionSystemReader resolves the SUT families bound to a version. The
// version/resource read models do not expose sut.family yet, so the assembly
// root implements VersionSystemReader over the version bindings (a nil reader
// only disables system-scoped knowledge; global knowledge still injects).
//
// VersionBindingReader resolves the page/API/account binding keys of one
// version for the success hook (page/API IDs -> page_key/api_key and the
// owning system/environment). It is set once by the composition root; a nil
// reader leaves the success hook failing closed instead of writing an
// unbound draft.
package knowledgeadapter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// KnowledgeInjectionService is the prompt-injection and usage surface.
type KnowledgeInjectionService interface {
	Inject(ctx context.Context, input knowledgeapp.InjectInput) ([]knowledgeapp.Injected, error)
	RecordUsage(ctx context.Context, agentRunID, workItemID int64, items []knowledgeapp.Injected) error
	Create(ctx context.Context, actor platform.Actor, input knowledgeapp.CreateInput) (knowledge.Item, error)
}

// KnowledgeSuccessHookService is the success-sedimentation surface (spec §10.1)
// plus the low-risk auto-approval gate (spec §10.2).
type KnowledgeSuccessHookService interface {
	OnLoginSuccess(ctx context.Context, fact knowledgeapp.SuccessFact) (knowledge.Item, error)
	OnPageLocated(ctx context.Context, fact knowledgeapp.SuccessFact) (knowledge.Item, error)
	OnAPIAuthSuccess(ctx context.Context, fact knowledgeapp.SuccessFact) (knowledge.Item, error)
	AutoApprove(ctx context.Context, item knowledge.Item) bool
}

// KnowledgeRetrievalService is the structured retrieval surface (spec §10.2).
type KnowledgeRetrievalService interface {
	Retrieve(ctx context.Context, query knowledgeapp.Query) (knowledgeapp.RetrievalPage, error)
	Read(ctx context.Context, id int64) (knowledge.Item, error)
}

// KnowledgeService is the knowledge application surface the adapter consumes.
// *knowledgeapp.Service satisfies it.
type KnowledgeService interface {
	KnowledgeInjectionService
	KnowledgeSuccessHookService
	KnowledgeRetrievalService
}

// VersionSystemReader resolves the SUT families bound to one version. The
// knowledge system scope matches scope=system items against those family keys
// (spec §4.3 / contracts/agent-profiles-tools.md §10.3). The version/resource
// read models do not expose sut.family yet, so cmd/eztest implements this port
// over the version bindings (Task 19).
type VersionSystemReader interface {
	VersionSystemKeys(ctx context.Context, versionID int64) ([]string, error)
}

// PageBinding is one page of a version SUT projected to its stable binding key.
type PageBinding struct {
	ID  int64
	Key string
}

// APIBinding is one API of a version SUT projected to its stable binding key
// ("METHOD path_template").
type APIBinding struct {
	ID  int64
	Key string
}

// VersionBinding is one SUT binding of a version projected for the knowledge
// success hook: identifiers and binding keys only, never credentials.
type VersionBinding struct {
	SystemID      int64
	EnvironmentID int64
	// SystemKey is the SUT family (empty leaves the item global).
	SystemKey    string
	Pages        []PageBinding
	APIs         []APIBinding
	AccountRoles []string
}

// VersionBindingReader resolves the page/API/account back-references of one
// version. The knowledge context never reads the version tables itself, so the
// composition root implements this port over the version read model.
type VersionBindingReader interface {
	VersionBindings(ctx context.Context, versionID int64) ([]VersionBinding, error)
}

// Adapter implements the agent knowledge ports, the knowledge.propose writer,
// the structured retrieval port and the case-run success hook on top of the
// knowledge application service.
type Adapter struct {
	knowledge KnowledgeService
	systems   VersionSystemReader
	bindings  VersionBindingReader
}

// New wires the knowledge adapter. systems may be nil: system-scoped knowledge
// is then skipped while global knowledge still injects. The success hook needs
// SetVersionBindings before it can sediment anything.
func New(knowledge KnowledgeService, systems VersionSystemReader) *Adapter {
	return &Adapter{knowledge: knowledge, systems: systems}
}

// SetVersionBindings wires the success-hook binding resolver. It is a
// wiring-time setter because the P7 runtime is constructed once with the
// knowledge service and completed at the assembly root, where the version read
// model is already available.
func (a *Adapter) SetVersionBindings(reader VersionBindingReader) {
	a.bindings = reader
}

var (
	_ agentapp.KnowledgeReader        = (*Adapter)(nil)
	_ agentapp.KnowledgeUsageRecorder = (*Adapter)(nil)
	_ tools.KnowledgeProposePort      = (*Adapter)(nil)
	_ tools.KnowledgeRetrievalPort    = (*Adapter)(nil)
	_ executionapp.CaseSuccessHook    = (*Adapter)(nil)
	_ KnowledgeService                = (*knowledgeapp.Service)(nil)
)

/* ------------------------------ injection -------------------------------- */

// RetrieveForVersion injects the approved knowledge visible to a version. The
// caller may pass systemKeys explicitly; nil/empty lets the adapter derive the
// version's SUT families. The knowledge service owns the approved/source_stale
// filters, the ts_rank ordering and the settings-driven item/token budget.
func (a *Adapter) RetrieveForVersion(ctx context.Context, versionID int64, systemKeys []string, query string) ([]agentapp.KnowledgeRef, int, error) {
	if versionID <= 0 {
		return nil, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if a.knowledge == nil {
		return nil, 0, errors.New("knowledgeadapter: knowledge service is not configured")
	}
	keys := systemKeys
	if len(keys) == 0 {
		derived, err := a.versionSystemKeys(ctx, versionID)
		if err != nil {
			return nil, 0, err
		}
		keys = derived
	}
	injected, err := a.knowledge.Inject(ctx, knowledgeapp.InjectInput{
		VersionID:  versionID,
		SystemKeys: keys,
		Query:      query,
	})
	if err != nil {
		return nil, 0, err
	}
	refs := make([]agentapp.KnowledgeRef, 0, len(injected))
	total := 0
	for _, item := range injected {
		refs = append(refs, agentapp.KnowledgeRef{
			ID:      item.ID,
			Title:   item.Title,
			Snippet: item.Snippet,
			Scope:   string(item.Scope),
			Tokens:  item.Tokens,
		})
		total += item.Tokens
	}
	return refs, total, nil
}

// RecordInjection appends one knowledge_usage row per injected item. The runner
// treats the returned error as log-only (contracts §10.3).
func (a *Adapter) RecordInjection(ctx context.Context, agentRunID, workItemID int64, refs []agentapp.KnowledgeRef) error {
	if len(refs) == 0 {
		return nil
	}
	if a.knowledge == nil {
		return errors.New("knowledgeadapter: knowledge service is not configured")
	}
	items := make([]knowledgeapp.Injected, 0, len(refs))
	for _, ref := range refs {
		items = append(items, knowledgeapp.Injected{
			ID:      ref.ID,
			Title:   ref.Title,
			Snippet: ref.Snippet,
			Scope:   knowledge.Scope(ref.Scope),
			Tokens:  ref.Tokens,
		})
	}
	return a.knowledge.RecordUsage(ctx, agentRunID, workItemID, items)
}

// ProposeKnowledge creates one AI knowledge draft. The service enforces
// status=draft, source_version_id=promoted_from_version_id=current version and
// created_by=run actor; the adapter strips any admin role from the run actor so
// an AI proposal never auto-approves even when knowledge.review_required=false
// (contracts/agent-profiles-tools.md §10.2).
func (a *Adapter) ProposeKnowledge(ctx context.Context, versionID, agentRunID int64, in tools.KnowledgeProposal) (tools.KnowledgeProposalResult, error) {
	if versionID <= 0 {
		return tools.KnowledgeProposalResult{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if a.knowledge == nil {
		return tools.KnowledgeProposalResult{}, errors.New("knowledgeadapter: knowledge service is not configured")
	}
	actor, _ := platform.ActorFrom(ctx)
	actor = platform.Actor{ID: actor.ID}

	sourceVersionID := versionID
	runID := agentRunID
	item, err := a.knowledge.Create(ctx, actor, knowledgeapp.CreateInput{
		Title:                 in.Title,
		Content:               in.Content,
		Scope:                 knowledge.Scope(in.Scope),
		SystemKey:             in.SystemKey,
		Tags:                  in.Tags,
		SourceVersionID:       &sourceVersionID,
		PromotedFromVersionID: &sourceVersionID,
		AgentRunID:            &runID,
	})
	if err != nil {
		return tools.KnowledgeProposalResult{}, err
	}
	return tools.KnowledgeProposalResult{
		ID:       item.ID,
		Status:   string(item.Status),
		Scope:    string(item.Scope),
		Revision: item.Revision,
	}, nil
}

/* ------------------------------ retrieval -------------------------------- */

// SearchKnowledge runs one structured knowledge.search. The version's SUT
// families are resolved here (the knowledge context never reads the version
// tables) and become the retrieval visibility scope; the knowledge service owns
// approved-only filtering, binding containment/fallback, trigram ranking, secret
// hiding and the page bounds.
func (a *Adapter) SearchKnowledge(ctx context.Context, filters tools.KnowledgeSearchFilters) (tools.KnowledgeSearchResult, error) {
	if filters.VersionID <= 0 {
		return tools.KnowledgeSearchResult{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if a.knowledge == nil {
		return tools.KnowledgeSearchResult{}, errors.New("knowledgeadapter: knowledge service is not configured")
	}
	systemKeys, err := a.versionSystemKeys(ctx, filters.VersionID)
	if err != nil {
		return tools.KnowledgeSearchResult{}, err
	}
	page, err := a.knowledge.Retrieve(ctx, knowledgeapp.Query{
		VersionID:     filters.VersionID,
		SystemKeys:    systemKeys,
		SystemID:      filters.SystemID,
		EnvironmentID: filters.EnvironmentID,
		PageKey:       filters.PageKey,
		APIKey:        filters.APIKey,
		AccountRole:   filters.AccountRole,
		Text:          filters.Text,
		Page:          filters.Page,
		Limit:         filters.Limit,
	})
	if err != nil {
		return tools.KnowledgeSearchResult{}, err
	}
	hits := make([]tools.KnowledgeHit, 0, len(page.Items))
	for _, item := range page.Items {
		hits = append(hits, tools.KnowledgeHit{
			ID:      item.ID,
			Title:   item.Title,
			Kind:    item.Kind,
			Score:   item.Score,
			Snippet: item.Snippet,
		})
	}
	return tools.KnowledgeSearchResult{
		Hits:    hits,
		Page:    page.Page,
		Limit:   page.Limit,
		HasMore: page.HasMore,
	}, nil
}

// ReadKnowledge fetches one item by ID for knowledge.read. The version's SUT
// families are resolved first and an item outside them (another system family,
// or any item of a version whose own family differs) is denied as not_found:
// IDs are opaque and must not become a cross-system/cross-version read oracle.
// The knowledge service already hides non-readable lifecycle states and
// secret-bearing items; the adapter re-checks scope visibility only.
func (a *Adapter) ReadKnowledge(ctx context.Context, scope tools.Scope, id int64) (tools.KnowledgeDetail, error) {
	if scope.VersionID <= 0 {
		return tools.KnowledgeDetail{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if id <= 0 {
		return tools.KnowledgeDetail{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	if a.knowledge == nil {
		return tools.KnowledgeDetail{}, errors.New("knowledgeadapter: knowledge service is not configured")
	}
	systemKeys, err := a.versionSystemKeys(ctx, scope.VersionID)
	if err != nil {
		return tools.KnowledgeDetail{}, err
	}
	item, err := a.knowledge.Read(ctx, id)
	if err != nil {
		return tools.KnowledgeDetail{}, err
	}
	if !itemVisibleToSystems(item, systemKeys) {
		return tools.KnowledgeDetail{}, platform.NotFound("knowledge_item")
	}
	return knowledgeDetail(item), nil
}

// versionSystemKeys resolves the SUT families visible to a version. A missing
// reader means "no system families": only global knowledge is visible.
func (a *Adapter) versionSystemKeys(ctx context.Context, versionID int64) ([]string, error) {
	if a.systems == nil {
		return nil, nil
	}
	keys, err := a.systems.VersionSystemKeys(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("knowledgeadapter: resolve version SUT families: %w", err)
	}
	return keys, nil
}

// itemVisibleToSystems is the read-path visibility check: global knowledge is
// cross-version by design, system knowledge must match a SUT family bound to
// the calling version.
func itemVisibleToSystems(item knowledge.Item, systemKeys []string) bool {
	switch item.Scope {
	case knowledge.ScopeGlobal:
		return true
	case knowledge.ScopeSystem:
		if item.SystemKey == nil {
			return false
		}
		key := strings.TrimSpace(*item.SystemKey)
		if key == "" {
			return false
		}
		for _, visible := range systemKeys {
			if strings.TrimSpace(visible) == key {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// knowledgeDetail maps a stored item onto the tool read model. The read-time
// SourceStale marker stays at the service boundary: tools.KnowledgeDetail has
// no field for it in the frozen 0.2.1 tool contract.
func knowledgeDetail(item knowledge.Item) tools.KnowledgeDetail {
	kind, _ := knowledgeapp.KindOf(item)
	return tools.KnowledgeDetail{
		ID:             item.ID,
		Title:          item.Title,
		Kind:           string(kind),
		Content:        item.Content,
		Scope:          string(item.Scope),
		Status:         string(item.Status),
		Tags:           append([]string(nil), item.Tags...),
		Revision:       item.Revision,
		LastVerifiedAt: item.LastVerifiedAt,
		Bindings: tools.KnowledgeBindings{
			SystemID:      item.Bindings.SystemID,
			EnvironmentID: item.Bindings.EnvironmentID,
			PageKey:       item.Bindings.PageKey,
			APIKey:        item.Bindings.APIKey,
			AccountRole:   item.Bindings.AccountRole,
		},
	}
}

/* ---------------------------- success hook ------------------------------- */

// OnCaseRunPassed sediments the low-risk knowledge observed by one passed case
// run (spec §10.1): page IDs become page_locator drafts, API IDs become
// api_auth drafts and account roles become login_recipe drafts, each bound to
// the version's system/environment/page/api keys. The knowledge service creates
// the drafts (and auto-approves low-risk kinds with the knowledge.auto_approved
// audit); the adapter calls AutoApprove again so the documented pipeline stays
// explicit, and a no-op once the hook already approved the item.
//
// The hook is best effort at the execution boundary: every fact is attempted,
// unbound IDs and per-fact failures are collected and returned as one joined
// error. The case runner logs that error and never blocks the case run.
func (a *Adapter) OnCaseRunPassed(ctx context.Context, fact executionapp.CaseSuccessFact) error {
	if fact.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if a.knowledge == nil {
		return errors.New("knowledgeadapter: knowledge service is not configured")
	}
	if a.bindings == nil {
		return errors.New("knowledgeadapter: version binding reader is not configured")
	}
	bindings, err := a.bindings.VersionBindings(ctx, fact.VersionID)
	if err != nil {
		return fmt.Errorf("knowledgeadapter: resolve version bindings: %w", err)
	}
	index := newSuccessBindingIndex(bindings)
	recipe := successRecipe(fact)

	var errs []error
	for _, pageID := range uniqueIDs(fact.PageIDs) {
		binding, key, ok := index.page(pageID)
		if !ok {
			errs = append(errs, fmt.Errorf("knowledgeadapter: page %d is not bound to version %d", pageID, fact.VersionID))
			continue
		}
		pageFact := successFactForBinding(binding, fact.VersionID, recipe)
		pageFact.PageKey = key
		errs = a.runSuccessHook(ctx, a.knowledge.OnPageLocated, pageFact, errs)
	}
	for _, apiID := range uniqueIDs(fact.APIIDs) {
		binding, key, ok := index.api(apiID)
		if !ok {
			errs = append(errs, fmt.Errorf("knowledgeadapter: api %d is not bound to version %d", apiID, fact.VersionID))
			continue
		}
		apiFact := successFactForBinding(binding, fact.VersionID, recipe)
		apiFact.APIKey = key
		errs = a.runSuccessHook(ctx, a.knowledge.OnAPIAuthSuccess, apiFact, errs)
	}
	for _, role := range uniqueTexts(fact.AccountRoles) {
		loginFact, ok := index.loginFact(role, fact.VersionID, recipe)
		if !ok {
			errs = append(errs, fmt.Errorf("knowledgeadapter: account role %q is not bound to version %d", role, fact.VersionID))
			continue
		}
		errs = a.runSuccessHook(ctx, a.knowledge.OnLoginSuccess, loginFact, errs)
	}
	return errors.Join(errs...)
}

// runSuccessHook executes one success hook and its explicit auto-approval. A
// hook error is collected, never fatal to the remaining facts.
func (a *Adapter) runSuccessHook(
	ctx context.Context,
	hook func(context.Context, knowledgeapp.SuccessFact) (knowledge.Item, error),
	fact knowledgeapp.SuccessFact,
	errs []error,
) []error {
	item, err := hook(ctx, fact)
	if err != nil {
		return append(errs, err)
	}
	// The service's success hooks already auto-approve low-risk kinds; the
	// explicit call keeps the adapter's documented pipeline (成功钩子 + 自动批准)
	// and is a no-op for an already-approved item.
	a.knowledge.AutoApprove(ctx, item)
	return errs
}

// successBindingIndex is the ID -> binding-key index of one version.
type successBindingIndex struct {
	pages map[int64]successPageBinding
	apis  map[int64]successAPIBinding
	roles map[string][]VersionBinding
}

type successPageBinding struct {
	binding VersionBinding
	key     string
}

type successAPIBinding struct {
	binding VersionBinding
	key     string
}

func newSuccessBindingIndex(bindings []VersionBinding) successBindingIndex {
	index := successBindingIndex{
		pages: make(map[int64]successPageBinding),
		apis:  make(map[int64]successAPIBinding),
		roles: make(map[string][]VersionBinding),
	}
	for _, binding := range bindings {
		for _, page := range binding.Pages {
			key := strings.TrimSpace(page.Key)
			if page.ID <= 0 || key == "" {
				continue
			}
			if _, duplicate := index.pages[page.ID]; duplicate {
				// A page ID cannot belong to two SUTs; keep the first
				// deterministic version-binding order.
				continue
			}
			index.pages[page.ID] = successPageBinding{binding: binding, key: key}
		}
		for _, api := range binding.APIs {
			key := strings.TrimSpace(api.Key)
			if api.ID <= 0 || key == "" {
				continue
			}
			if _, duplicate := index.apis[api.ID]; duplicate {
				continue
			}
			index.apis[api.ID] = successAPIBinding{binding: binding, key: key}
		}
		for _, role := range binding.AccountRoles {
			role = strings.TrimSpace(role)
			if role == "" {
				continue
			}
			index.roles[role] = appendUniqueBinding(index.roles[role], binding)
		}
	}
	return index
}

func (i successBindingIndex) page(id int64) (VersionBinding, string, bool) {
	binding, ok := i.pages[id]
	if !ok {
		return VersionBinding{}, "", false
	}
	return binding.binding, binding.key, true
}

func (i successBindingIndex) api(id int64) (VersionBinding, string, bool) {
	binding, ok := i.apis[id]
	if !ok {
		return VersionBinding{}, "", false
	}
	return binding.binding, binding.key, true
}

// loginFact builds the login_recipe fact of one account role. A role owned by
// exactly one version SUT is bound to that system/environment; a role present
// on several SUTs is ambiguous, so the recipe stays global and carries only the
// account_role binding rather than guessing an attribution. A role that no
// version SUT exposes is unmapped.
func (i successBindingIndex) loginFact(role string, versionID int64, recipe map[string]any) (knowledgeapp.SuccessFact, bool) {
	bindings, ok := i.roles[role]
	if !ok || len(bindings) == 0 {
		return knowledgeapp.SuccessFact{}, false
	}
	fact := knowledgeapp.SuccessFact{
		VersionID:   versionID,
		AccountRole: role,
		Recipe:      recipe,
	}
	if len(bindings) == 1 {
		fact = successFactForBinding(bindings[0], versionID, recipe)
		fact.AccountRole = role
	}
	return fact, true
}

// successFactForBinding copies the system/environment binding of one version
// SUT into a success fact. IDs are optional (a resource view with an invalid ID
// degrades to an unbound dimension instead of a zero ID).
func successFactForBinding(binding VersionBinding, versionID int64, recipe map[string]any) knowledgeapp.SuccessFact {
	fact := knowledgeapp.SuccessFact{
		VersionID: versionID,
		SystemKey: strings.TrimSpace(binding.SystemKey),
		Recipe:    recipe,
	}
	if binding.SystemID > 0 {
		systemID := binding.SystemID
		fact.SystemID = &systemID
	}
	if binding.EnvironmentID > 0 {
		environmentID := binding.EnvironmentID
		fact.EnvironmentID = &environmentID
	}
	return fact
}

// successRecipe carries the case-run provenance as structured, non-secret
// identifiers. The case title is deliberately not copied: it is free text that
// may contain content the knowledge writer must never persist.
func successRecipe(fact executionapp.CaseSuccessFact) map[string]any {
	recipe := map[string]any{"source": "case_run"}
	if fact.CaseRunID > 0 {
		recipe["case_run_id"] = strconv.FormatInt(fact.CaseRunID, 10)
	}
	if fact.CaseVersionID > 0 {
		recipe["case_version_id"] = strconv.FormatInt(fact.CaseVersionID, 10)
	}
	if fact.EvidenceCount > 0 {
		recipe["evidence_count"] = fact.EvidenceCount
	}
	return recipe
}

func appendUniqueBinding(bindings []VersionBinding, binding VersionBinding) []VersionBinding {
	for _, existing := range bindings {
		if existing.SystemID == binding.SystemID &&
			existing.EnvironmentID == binding.EnvironmentID &&
			strings.TrimSpace(existing.SystemKey) == strings.TrimSpace(binding.SystemKey) {
			return bindings
		}
	}
	return append(bindings, binding)
}

func uniqueIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func uniqueTexts(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
