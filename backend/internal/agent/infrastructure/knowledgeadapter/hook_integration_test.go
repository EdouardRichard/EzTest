package knowledgeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
)

// This file carries the end-to-end success-hook test plus the shared test
// doubles used by the retrieval adapter test. The knowledge repository is a
// real implementation of knowledge.Repository (the same contract the postgres
// adapter implements: approved-only injection, JSONB-containment binding
// filters, CAS status writes), so the test exercises the real application
// service and success hooks without a database (Task 51 focused test rule).

/* ----------------------------- test doubles ------------------------------ */

// fakeKnowledgeService (adapter_test.go) was written for the injection-only
// surface; the adapter now consumes the complete knowledge service, so the
// success-hook/retrieval methods are declared here as failing stubs. The
// injection/proposal tests never call them.
func (f *fakeKnowledgeService) OnLoginSuccess(context.Context, knowledgeapp.SuccessFact) (knowledge.Item, error) {
	return knowledge.Item{}, errors.New("success hooks are not configured in this fake")
}

func (f *fakeKnowledgeService) OnPageLocated(context.Context, knowledgeapp.SuccessFact) (knowledge.Item, error) {
	return knowledge.Item{}, errors.New("success hooks are not configured in this fake")
}

func (f *fakeKnowledgeService) OnAPIAuthSuccess(context.Context, knowledgeapp.SuccessFact) (knowledge.Item, error) {
	return knowledge.Item{}, errors.New("success hooks are not configured in this fake")
}

func (f *fakeKnowledgeService) AutoApprove(context.Context, knowledge.Item) bool { return false }

func (f *fakeKnowledgeService) Retrieve(context.Context, knowledgeapp.Query) (knowledgeapp.RetrievalPage, error) {
	return knowledgeapp.RetrievalPage{}, errors.New("retrieval is not configured in this fake")
}

func (f *fakeKnowledgeService) Read(context.Context, int64) (knowledge.Item, error) {
	return knowledge.Item{}, errors.New("retrieval is not configured in this fake")
}

// memoryKnowledgeRepository is an in-memory knowledge.Repository.
type memoryKnowledgeRepository struct {
	mu        sync.Mutex
	items     map[int64]knowledge.Item
	revisions map[int64][]knowledge.Revision
	inserted  []knowledge.Item
	updates   int
	statusCAS int
}

func newMemoryKnowledgeRepository() *memoryKnowledgeRepository {
	return &memoryKnowledgeRepository{
		items:     make(map[int64]knowledge.Item),
		revisions: make(map[int64][]knowledge.Revision),
	}
}

func (r *memoryKnowledgeRepository) Insert(_ context.Context, item knowledge.Item, revision knowledge.Revision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[item.ID]; exists {
		return knowledge.ErrStateConflict
	}
	r.items[item.ID] = cloneKnowledgeItem(item)
	r.revisions[item.ID] = append(r.revisions[item.ID], revision)
	r.inserted = append(r.inserted, cloneKnowledgeItem(item))
	return nil
}

func (r *memoryKnowledgeRepository) Get(_ context.Context, id int64) (knowledge.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok {
		return knowledge.Item{}, knowledge.ErrNotFound
	}
	return cloneKnowledgeItem(item), nil
}

func (r *memoryKnowledgeRepository) List(_ context.Context, filter knowledge.Filter) ([]knowledge.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 {
		limit = knowledgeapp.DefaultListLimit
	}
	items := make([]knowledge.Item, 0, len(r.items))
	for _, item := range r.items {
		if filter.Scope != "" && item.Scope != filter.Scope {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.SystemKey != "" && (item.SystemKey == nil || *item.SystemKey != filter.SystemKey) {
			continue
		}
		if !bindingFilterMatches(item.Bindings, filter.Bindings) {
			continue
		}
		if filter.BeforeID > 0 && item.ID >= filter.BeforeID {
			continue
		}
		items = append(items, cloneKnowledgeItem(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (r *memoryKnowledgeRepository) Update(_ context.Context, item knowledge.Item, revision knowledge.Revision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.items[item.ID]
	if !ok {
		return knowledge.ErrNotFound
	}
	if stored.Revision != item.Revision-1 {
		return knowledge.ErrStateConflict
	}
	r.items[item.ID] = cloneKnowledgeItem(item)
	r.revisions[item.ID] = append(r.revisions[item.ID], revision)
	r.updates++
	return nil
}

func (r *memoryKnowledgeRepository) UpdateStatus(_ context.Context, item knowledge.Item, from knowledge.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.items[item.ID]
	if !ok {
		return knowledge.ErrNotFound
	}
	if stored.Status != from {
		return knowledge.ErrStateConflict
	}
	r.items[item.ID] = cloneKnowledgeItem(item)
	r.statusCAS++
	return nil
}

func (r *memoryKnowledgeRepository) ListRevisions(_ context.Context, knowledgeID int64) ([]knowledge.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]knowledge.Revision(nil), r.revisions[knowledgeID]...), nil
}

// ListForInjection mirrors the postgres adapter: approved rows only, visible
// global/system scope, JSONB-containment binding filter, id-desc order.
func (r *memoryKnowledgeRepository) ListForInjection(_ context.Context, query knowledge.InjectionQuery) ([]knowledge.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	keys := make(map[string]struct{}, len(query.SystemKeys))
	for _, key := range query.SystemKeys {
		keys[strings.TrimSpace(key)] = struct{}{}
	}
	items := make([]knowledge.Item, 0, len(r.items))
	for _, item := range r.items {
		if item.Status != knowledge.StatusApproved {
			continue
		}
		switch item.Scope {
		case knowledge.ScopeGlobal:
		case knowledge.ScopeSystem:
			if item.SystemKey == nil {
				continue
			}
			if _, visible := keys[strings.TrimSpace(*item.SystemKey)]; !visible {
				continue
			}
		default:
			continue
		}
		if !bindingFilterMatches(item.Bindings, query.Bindings) {
			continue
		}
		items = append(items, cloneKnowledgeItem(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// allItems returns every stored item ordered by id.
func (r *memoryKnowledgeRepository) allItems() []knowledge.Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]knowledge.Item, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, cloneKnowledgeItem(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (r *memoryKnowledgeRepository) insertedItems() []knowledge.Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]knowledge.Item(nil), r.inserted...)
}

// bindingFilterMatches implements the knowledge binder's JSONB-containment
// semantics: every dimension set on the filter must be present and equal.
func bindingFilterMatches(stored, filter knowledge.Bindings) bool {
	if filter.SystemID != nil && (stored.SystemID == nil || *stored.SystemID != *filter.SystemID) {
		return false
	}
	if filter.EnvironmentID != nil && (stored.EnvironmentID == nil || *stored.EnvironmentID != *filter.EnvironmentID) {
		return false
	}
	if filter.PageKey != nil && (stored.PageKey == nil || *stored.PageKey != *filter.PageKey) {
		return false
	}
	if filter.APIKey != nil && (stored.APIKey == nil || *stored.APIKey != *filter.APIKey) {
		return false
	}
	if filter.AccountRole != nil && (stored.AccountRole == nil || *stored.AccountRole != *filter.AccountRole) {
		return false
	}
	return true
}

func cloneKnowledgeItem(item knowledge.Item) knowledge.Item {
	item.Tags = append([]string(nil), item.Tags...)
	if item.SystemKey != nil {
		value := *item.SystemKey
		item.SystemKey = &value
	}
	return item
}

// recordingKnowledgeAudit captures the knowledge.create / knowledge.auto_approved
// audit rows written by the real application service.
type recordingKnowledgeAudit struct {
	mu      sync.Mutex
	records []knowledgeAuditRecord
}

type knowledgeAuditRecord struct {
	Action   string
	TargetID int64
	Metadata map[string]any
}

func (a *recordingKnowledgeAudit) Record(_ context.Context, action string, targetID int64, metadata map[string]any) error {
	copied := make(map[string]any, len(metadata))
	for key, value := range metadata {
		copied[key] = value
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, knowledgeAuditRecord{Action: action, TargetID: targetID, Metadata: copied})
	return nil
}

func (a *recordingKnowledgeAudit) byAction(action string) []knowledgeAuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	records := make([]knowledgeAuditRecord, 0)
	for _, record := range a.records {
		if record.Action == action {
			records = append(records, record)
		}
	}
	return records
}

// fixedKnowledgeClock is the deterministic time source of the knowledge service.
type fixedKnowledgeClock struct {
	now time.Time
}

func (c fixedKnowledgeClock) Now() time.Time {
	if c.now.IsZero() {
		return time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	}
	return c.now
}

// sequenceKnowledgeIDs hands out increasing decimal IDs.
type sequenceKnowledgeIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *sequenceKnowledgeIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

// fakeVersionBindingReader implements VersionBindingReader.
type fakeVersionBindingReader struct {
	calls     int
	versionID int64
	bindings  []VersionBinding
	err       error
}

func (f *fakeVersionBindingReader) VersionBindings(_ context.Context, versionID int64) ([]VersionBinding, error) {
	f.calls++
	f.versionID = versionID
	if f.err != nil {
		return nil, f.err
	}
	return append([]VersionBinding(nil), f.bindings...), nil
}

// insertKnowledgeItem seeds one item straight through the repository contract.
func insertKnowledgeItem(t *testing.T, repo *memoryKnowledgeRepository, item knowledge.Item) knowledge.Item {
	t.Helper()
	if item.Revision == 0 {
		item.Revision = 1
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if item.Status == "" {
		item.Status = knowledge.StatusApproved
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
		item.UpdatedAt = item.CreatedAt
	}
	revision := knowledge.Revision{
		ID:          item.ID,
		KnowledgeID: item.ID,
		Revision:    item.Revision,
		Title:       item.Title,
		Content:     item.Content,
		Tags:        item.Tags,
		CreatedAt:   item.CreatedAt,
	}
	if err := repo.Insert(context.Background(), item, revision); err != nil {
		t.Fatalf("seed knowledge item %d: %v", item.ID, err)
	}
	return item
}

/* ------------------------- success hook integration ----------------------- */

// TestCaseSuccessHookEndToEnd drives a passed-case observation through the
// adapter, the real knowledge application service (success hooks +
// auto-approval), the real repository contract, and the audit recorder.
func TestCaseSuccessHookEndToEnd(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryKnowledgeRepository()
	audit := &recordingKnowledgeAudit{}
	service := knowledgeapp.NewService(
		repo, nil, nil, audit, nil, nil,
		fixedKnowledgeClock{}, &sequenceKnowledgeIDs{next: 7000},
	)
	systems := &fakeVersionSystemReader{keys: []string{"shop"}}
	versions := &fakeVersionBindingReader{bindings: []VersionBinding{{
		SystemID:      7001,
		EnvironmentID: 7101,
		SystemKey:     "shop",
		Pages:         []PageBinding{{ID: 501, Key: "/login"}},
		APIs:          []APIBinding{{ID: 601, Key: "POST /api/login"}},
		AccountRoles:  []string{"admin"},
	}}}
	adapter := New(service, systems)
	adapter.SetVersionBindings(versions)

	fact := caseSuccessFactFixture()
	if err := adapter.OnCaseRunPassed(ctx, fact); err != nil {
		t.Fatalf("OnCaseRunPassed: %v", err)
	}
	if versions.calls != 1 || versions.versionID != fact.VersionID {
		t.Fatalf("binding reader calls = %d version = %d", versions.calls, versions.versionID)
	}

	drafts := repo.insertedItems()
	if len(drafts) != 3 {
		t.Fatalf("drafts = %d, want 3 (page/api/login)", len(drafts))
	}
	for _, draft := range drafts {
		if draft.Status != knowledge.StatusDraft {
			t.Fatalf("draft %d status = %q, want draft", draft.ID, draft.Status)
		}
	}

	items := repo.allItems()
	if len(items) != 3 {
		t.Fatalf("stored items = %d, want 3", len(items))
	}
	byKind := make(map[knowledgeapp.Kind]knowledge.Item, len(items))
	for _, item := range items {
		if item.Status != knowledge.StatusApproved {
			t.Fatalf("item %d status = %q, want approved", item.ID, item.Status)
		}
		kind, ok := knowledgeapp.KindOf(item)
		if !ok {
			t.Fatalf("item %d has no low-risk kind: tags=%v content=%s", item.ID, item.Tags, item.Content)
		}
		byKind[kind] = item
	}

	page, ok := byKind[knowledgeapp.KindPageLocator]
	if !ok {
		t.Fatal("page_locator item missing")
	}
	if page.Scope != knowledge.ScopeSystem || page.SystemKey == nil || *page.SystemKey != "shop" {
		t.Fatalf("page locator scope = %q system key = %v, want system/shop", page.Scope, page.SystemKey)
	}
	pageBindings := page.Bindings
	if pageBindings.PageKey == nil || *pageBindings.PageKey != "/login" ||
		pageBindings.SystemID == nil || *pageBindings.SystemID != 7001 ||
		pageBindings.EnvironmentID == nil || *pageBindings.EnvironmentID != 7101 {
		t.Fatalf("page locator bindings = %#v, want page_key=/login system=7001 env=7101", pageBindings)
	}
	if pageBindings.APIKey != nil || pageBindings.AccountRole != nil {
		t.Fatalf("page locator unexpected bindings: %#v", pageBindings)
	}

	api, ok := byKind[knowledgeapp.KindAPIAuth]
	if !ok {
		t.Fatal("api_auth item missing")
	}
	apiBindings := api.Bindings
	if apiBindings.APIKey == nil || *apiBindings.APIKey != "POST /api/login" ||
		apiBindings.SystemID == nil || *apiBindings.SystemID != 7001 ||
		apiBindings.EnvironmentID == nil || *apiBindings.EnvironmentID != 7101 {
		t.Fatalf("api auth bindings = %#v, want api_key=POST /api/login system=7001 env=7101", apiBindings)
	}

	login, ok := byKind[knowledgeapp.KindLoginRecipe]
	if !ok {
		t.Fatal("login_recipe item missing")
	}
	if login.Scope != knowledge.ScopeSystem || login.SystemKey == nil || *login.SystemKey != "shop" {
		t.Fatalf("login recipe scope = %q system key = %v, want system/shop", login.Scope, login.SystemKey)
	}
	loginBindings := login.Bindings
	if loginBindings.AccountRole == nil || *loginBindings.AccountRole != "admin" {
		t.Fatalf("login recipe bindings = %#v, want account_role=admin", loginBindings)
	}
	if loginBindings.SystemID == nil || *loginBindings.SystemID != 7001 ||
		loginBindings.EnvironmentID == nil || *loginBindings.EnvironmentID != 7101 {
		t.Fatalf("login recipe system/environment = %#v, want the version binding", loginBindings)
	}

	// The structured recipe carries the passed case provenance but no secret.
	for _, item := range items {
		var payload struct {
			Kind    string         `json:"kind"`
			Recipe  map[string]any `json:"recipe"`
			Scope   string         `json:"scope"`
			Version string         `json:"source_version_id"`
		}
		if err := json.Unmarshal([]byte(item.Content), &payload); err != nil {
			t.Fatalf("item %d content is not JSON: %v (%s)", item.ID, err, item.Content)
		}
		if payload.Recipe == nil {
			t.Fatalf("item %d has no recipe: %s", item.ID, item.Content)
		}
		if payload.Recipe["case_run_id"] != "9001" || payload.Recipe["case_version_id"] != "801" {
			t.Fatalf("item %d recipe = %#v, want the case run provenance", item.ID, payload.Recipe)
		}
		if payload.Recipe["source"] != "case_run" || payload.Version != "10" {
			t.Fatalf("item %d provenance = %#v/%q", item.ID, payload.Recipe, payload.Version)
		}
	}

	// Auto-approval is audited for every low-risk item, with no body content.
	creates := audit.byAction(knowledgeapp.ActionCreate)
	if len(creates) != 3 {
		t.Fatalf("knowledge.create audits = %d, want 3", len(creates))
	}
	approvals := audit.byAction(knowledgeapp.ActionAutoApproved)
	if len(approvals) != 3 {
		t.Fatalf("knowledge.auto_approved audits = %d, want 3: %#v", len(approvals), approvals)
	}
	approvedIDs := make(map[string]struct{}, len(approvals))
	for _, record := range approvals {
		id, _ := record.Metadata["knowledge_id"].(string)
		if id == "" {
			t.Fatalf("approval metadata has no knowledge_id: %#v", record.Metadata)
		}
		if _, duplicate := approvedIDs[id]; duplicate {
			t.Fatalf("knowledge %s auto-approved twice", id)
		}
		approvedIDs[id] = struct{}{}
		if kind, _ := record.Metadata["kind"].(string); kind == "" {
			t.Fatalf("approval metadata has no kind: %#v", record.Metadata)
		}
		if _, leaks := record.Metadata["content"]; leaks {
			t.Fatalf("approval metadata leaks the body: %#v", record.Metadata)
		}
	}

	// No credential value may reach the persisted knowledge.
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal item %d: %v", item.ID, err)
		}
		lower := strings.ToLower(string(raw))
		for _, marker := range []string{"password", "hunter2", "bearer ", "authorization", "secret"} {
			if strings.Contains(lower, marker) {
				t.Fatalf("item %d leaks secret marker %q: %s", item.ID, marker, raw)
			}
		}
	}
}

// TestCaseSuccessHookUnknownBindingContinues proves the hook is best effort:
// one unbound page/API ID is reported but the remaining facts still sediment.
func TestCaseSuccessHookUnknownBindingContinues(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryKnowledgeRepository()
	audit := &recordingKnowledgeAudit{}
	service := knowledgeapp.NewService(
		repo, nil, nil, audit, nil, nil,
		fixedKnowledgeClock{}, &sequenceKnowledgeIDs{next: 7100},
	)
	adapter := New(service, &fakeVersionSystemReader{keys: []string{"shop"}})
	adapter.SetVersionBindings(&fakeVersionBindingReader{bindings: []VersionBinding{{
		SystemID:      7001,
		EnvironmentID: 7101,
		SystemKey:     "shop",
		Pages:         []PageBinding{{ID: 501, Key: "/login"}},
		APIs:          []APIBinding{{ID: 601, Key: "POST /api/login"}},
		AccountRoles:  []string{"admin"},
	}}})

	fact := caseSuccessFactFixture()
	fact.PageIDs = []int64{999, 501}
	fact.APIIDs = []int64{998}
	if err := adapter.OnCaseRunPassed(ctx, fact); err == nil {
		t.Fatal("unbound page/API IDs must be reported to the best-effort caller")
	}

	byKind := make(map[knowledgeapp.Kind]knowledge.Item)
	for _, item := range repo.allItems() {
		kind, ok := knowledgeapp.KindOf(item)
		if !ok {
			t.Fatalf("item %d has no kind", item.ID)
		}
		byKind[kind] = item
	}
	if _, ok := byKind[knowledgeapp.KindPageLocator]; !ok {
		t.Fatal("the bound page 501 must still sediment a page_locator")
	}
	if _, ok := byKind[knowledgeapp.KindAPIAuth]; ok {
		t.Fatal("API 998 is not bound to the version and must not sediment")
	}
	if _, ok := byKind[knowledgeapp.KindLoginRecipe]; !ok {
		t.Fatal("the login role must still sediment a login_recipe")
	}
}

// TestCaseSuccessHookAmbiguousRoleStaysGlobal pins the attribution policy: a
// role present on more than one version SUT is not guessed, so the login
// recipe stays global and bound to the account role only.
func TestCaseSuccessHookAmbiguousRoleStaysGlobal(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryKnowledgeRepository()
	service := knowledgeapp.NewService(
		repo, nil, nil, nil, nil, nil,
		fixedKnowledgeClock{}, &sequenceKnowledgeIDs{next: 7200},
	)
	adapter := New(service, nil)
	adapter.SetVersionBindings(&fakeVersionBindingReader{bindings: []VersionBinding{
		{SystemID: 7001, EnvironmentID: 7101, SystemKey: "shop", AccountRoles: []string{"admin"}},
		{SystemID: 7002, EnvironmentID: 7102, SystemKey: "cms", AccountRoles: []string{"admin"}},
	}})

	fact := caseSuccessFactFixture()
	fact.PageIDs = nil
	fact.APIIDs = nil
	if err := adapter.OnCaseRunPassed(ctx, fact); err != nil {
		t.Fatalf("OnCaseRunPassed: %v", err)
	}
	items := repo.allItems()
	if len(items) != 1 {
		t.Fatalf("items = %d, want one login recipe", len(items))
	}
	item := items[0]
	if item.Scope != knowledge.ScopeGlobal || item.SystemKey != nil {
		t.Fatalf("ambiguous role login scope = %q key = %v, want global", item.Scope, item.SystemKey)
	}
	if item.Bindings.AccountRole == nil || *item.Bindings.AccountRole != "admin" {
		t.Fatalf("login bindings = %#v, want account_role=admin", item.Bindings)
	}
	if item.Bindings.SystemID != nil || item.Bindings.EnvironmentID != nil {
		t.Fatalf("ambiguous login bindings guess a system: %#v", item.Bindings)
	}
}

// TestCaseSuccessHookUnconfiguredFailsClosed proves a missing wiring is an
// internal error, never a silent no-op or a panic.
func TestCaseSuccessHookUnconfiguredFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryKnowledgeRepository()
	service := knowledgeapp.NewService(
		repo, nil, nil, nil, nil, nil,
		fixedKnowledgeClock{}, &sequenceKnowledgeIDs{next: 7300},
	)
	fact := caseSuccessFactFixture()

	// Missing version binding resolver.
	if err := New(service, nil).OnCaseRunPassed(ctx, fact); err == nil {
		t.Fatal("a hook without a version binding resolver must fail closed")
	}
	// Missing knowledge service.
	adapter := New(nil, nil)
	adapter.SetVersionBindings(&fakeVersionBindingReader{})
	if err := adapter.OnCaseRunPassed(ctx, fact); err == nil {
		t.Fatal("a hook without the knowledge service must fail closed")
	}
	// Invalid version.
	adapter = New(service, nil)
	adapter.SetVersionBindings(&fakeVersionBindingReader{})
	bad := fact
	bad.VersionID = 0
	if err := adapter.OnCaseRunPassed(ctx, bad); err == nil {
		t.Fatal("a hook without a version must fail closed")
	}
	if len(repo.allItems()) != 0 {
		t.Fatalf("no item may be written by a failing hook: %d", len(repo.allItems()))
	}
}

func caseSuccessFactFixture() executionapp.CaseSuccessFact {
	return executionapp.CaseSuccessFact{
		VersionID:     10,
		CaseRunID:     9001,
		CaseVersionID: 801,
		Title:         "登录并下单",
		AccountRoles:  []string{"admin"},
		PageIDs:       []int64{501},
		APIIDs:        []int64{601},
		EvidenceCount: 3,
	}
}
