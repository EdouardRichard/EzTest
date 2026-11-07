package knowledgeapp_test

// Task 33 lifecycle tests: knowledge top-K injection into the version briefing,
// success/failure verified-staleness marking, page/route/account invalidation,
// read-time source-stale consistency between Inject and Retrieve, and the HTTP
// secret gate. The tests share the fake repositories declared in service_test.go
// and retrieval_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	knowledgeapi "github.com/EdouardRichard/EzTest/backend/internal/knowledge/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

/* ----------------------------- lifecycle fakes ---------------------------- */

type statusWrite struct {
	item knowledge.Item
	from knowledge.Status
}

// lifecycleRepo keeps a mutable item map and records every status CAS so the
// lifecycle tests can assert both the written row and the transition source.
type lifecycleRepo struct {
	*fakeRepo
	statusWrites []statusWrite
	failStatus   map[int64]bool
}

func newLifecycleRepo() *lifecycleRepo {
	return &lifecycleRepo{
		fakeRepo:   &fakeRepo{items: map[int64]knowledge.Item{}, revisions: map[int64][]knowledge.Revision{}},
		failStatus: map[int64]bool{},
	}
}

func (r *lifecycleRepo) UpdateStatus(_ context.Context, item knowledge.Item, from knowledge.Status) error {
	r.statusWrites = append(r.statusWrites, statusWrite{item: item, from: from})
	if r.failStatus[item.ID] {
		return knowledge.ErrStateConflict
	}
	r.fakeRepo.statusItem = item
	r.fakeRepo.statusFrom = from
	r.items[item.ID] = item
	return nil
}

func newLifecycleService(repo knowledge.Repository) *knowledgeapp.Service {
	return knowledgeapp.NewService(repo, nil, nil, nil, nil, nil,
		fixedClock{now: testNow}, &seqIDs{next: 2000})
}

func approvedBoundItem(id int64, bindings knowledge.Bindings) knowledge.Item {
	item := approvedItem(id, knowledge.ScopeGlobal, "")
	item.Bindings = bindings
	return item
}

/* ------------------------------- injection -------------------------------- */

// briefingReader is the in-memory read port of the version briefing.
type briefingReader struct {
	source agentcontext.Source
}

func (r briefingReader) Load(_ context.Context, _ int64) (agentcontext.Source, error) {
	return r.source, nil
}

// TestKnowledgeInjectedIntoBriefing pins the injection policy of spec §10.1:
// only approved, non-source-stale knowledge reaches the briefing, ranked by
// confidence and capped at the top-K, with title/snippet/confidence rendered.
// Stale knowledge is not injected but stays readable through knowledge.read.
func TestKnowledgeInjectedIntoBriefing(t *testing.T) {
	t.Run("approved items render title snippet and confidence top-K", func(t *testing.T) {
		reader := briefingReader{source: agentcontext.Source{
			Version: agentcontext.VersionSnapshot{Goal: "验证登录", Stage: "executing"},
			Knowledge: []agentcontext.KnowledgeSnapshot{
				{ID: 1, Title: "登录配方", Summary: "打开 /login 后提交表单", Confidence: ptr(0.9), Status: "approved"},
				{ID: 2, Title: "验证码处理", Summary: "先取验证码再登录", Confidence: ptr(0.95), Status: "approved"},
				{ID: 3, Title: "会话复用", Summary: "复用 storageState", Confidence: ptr(0.8), Status: "approved"},
				{ID: 4, Title: "退出登录", Summary: "点击右上角退出", Confidence: ptr(0.7), Status: "approved"},
				{ID: 5, Title: "密码错误", Summary: "提示密码错误", Confidence: ptr(0.6), Status: "approved"},
				{ID: 6, Title: "低置信知识", Summary: "不应被渲染", Confidence: ptr(0.1), Status: "approved"},
				{ID: 7, Title: "可疑知识", Summary: "不应被渲染", Status: "suspect"},
				{ID: 8, Title: "失效知识", Summary: "不应被渲染", Status: "stale"},
				{ID: 9, Title: "源失效知识", Summary: "不应被渲染", Status: "approved", SourceStale: true},
			},
		}}
		briefing, err := agentcontext.NewBriefingBuilder(reader).BuildBriefing(context.Background(), 7)
		if err != nil {
			t.Fatalf("BuildBriefing: %v", err)
		}
		rendered := briefing.Render()
		for _, want := range []string{"登录配方", "打开 /login 后提交表单", "0.90", "验证码处理", "0.95"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("briefing is missing %q:\n%s", want, rendered)
			}
		}
		for _, banned := range []string{"低置信知识", "可疑知识", "失效知识", "源失效知识"} {
			if strings.Contains(rendered, banned) {
				t.Fatalf("briefing rendered non-injected knowledge %q:\n%s", banned, rendered)
			}
		}
	})

	t.Run("stale knowledge stays readable through knowledge.read", func(t *testing.T) {
		repo := newLifecycleRepo()
		stale := approvedItem(5001, knowledge.ScopeGlobal, "")
		stale.Status = knowledge.StatusStale
		repo.items[5001] = stale
		service := newLifecycleService(repo)
		read, err := service.Read(context.Background(), 5001)
		if err != nil {
			t.Fatalf("knowledge.read must keep a stale item readable: %v", err)
		}
		if read.ID != 5001 || read.Status != knowledge.StatusStale {
			t.Fatalf("read item = %+v", read)
		}
	})
}

/* ------------------------------- verification ----------------------------- */

// TestSuccessBumpsVerified pins the success half of the reuse lifecycle: a
// successful reuse stamps last_verified_at and keeps/re-approves the item.
func TestSuccessBumpsVerified(t *testing.T) {
	t.Run("approved reuse bumps last_verified_at", func(t *testing.T) {
		repo := newLifecycleRepo()
		previous := testNow.Add(-24 * time.Hour)
		item := approvedItem(42, knowledge.ScopeGlobal, "")
		item.LastVerifiedAt = &previous
		repo.items[42] = item
		service := newLifecycleService(repo)

		verified, err := service.MarkVerified(context.Background(), 42)
		if err != nil {
			t.Fatalf("MarkVerified: %v", err)
		}
		if verified.Status != knowledge.StatusApproved {
			t.Fatalf("status = %s, want approved", verified.Status)
		}
		if verified.LastVerifiedAt == nil || !verified.LastVerifiedAt.Equal(testNow) {
			t.Fatalf("last_verified_at = %v, want %v", verified.LastVerifiedAt, testNow)
		}
		if len(repo.statusWrites) != 1 {
			t.Fatalf("status writes = %d, want 1", len(repo.statusWrites))
		}
		write := repo.statusWrites[0]
		if write.from != knowledge.StatusApproved || write.item.ID != 42 {
			t.Fatalf("status write = %+v", write)
		}
		stored := repo.items[42]
		if stored.LastVerifiedAt == nil || !stored.LastVerifiedAt.Equal(testNow) {
			t.Fatalf("persisted last_verified_at = %v", stored.LastVerifiedAt)
		}
	})

	t.Run("suspect reuse re-verifies to approved", func(t *testing.T) {
		repo := newLifecycleRepo()
		reason := "previous login failed"
		item := approvedItem(43, knowledge.ScopeGlobal, "")
		item.Status = knowledge.StatusSuspect
		item.ReviewReason = &reason
		repo.items[43] = item
		service := newLifecycleService(repo)

		verified, err := service.MarkVerified(context.Background(), 43)
		if err != nil {
			t.Fatalf("MarkVerified: %v", err)
		}
		if verified.Status != knowledge.StatusApproved || verified.ReviewReason != nil {
			t.Fatalf("verified item = %+v", verified)
		}
		if verified.LastVerifiedAt == nil || !verified.LastVerifiedAt.Equal(testNow) {
			t.Fatalf("last_verified_at = %v", verified.LastVerifiedAt)
		}
		if len(repo.statusWrites) != 1 || repo.statusWrites[0].from != knowledge.StatusSuspect {
			t.Fatalf("status writes = %+v", repo.statusWrites)
		}
	})

	t.Run("draft cannot be verified", func(t *testing.T) {
		repo := newLifecycleRepo()
		repo.items[44] = draftItem(44, 7)
		service := newLifecycleService(repo)
		_, err := service.MarkVerified(context.Background(), 44)
		assertAPIError(t, err, 409, "state_conflict", "invalid_transition")
		if len(repo.statusWrites) != 0 {
			t.Fatalf("draft verification must not write: %+v", repo.statusWrites)
		}
	})
}

// TestFailureMarksSuspect pins the failure half of the reuse lifecycle: a failed
// reuse marks the approved item suspect with the failure reason while preserving
// its historical verification timestamp.
func TestFailureMarksSuspect(t *testing.T) {
	t.Run("failed reuse marks approved item suspect", func(t *testing.T) {
		repo := newLifecycleRepo()
		previous := testNow.Add(-48 * time.Hour)
		item := approvedItem(45, knowledge.ScopeGlobal, "")
		item.LastVerifiedAt = &previous
		repo.items[45] = item
		service := newLifecycleService(repo)

		suspect, err := service.MarkSuspect(context.Background(), 45, " login callback failed ")
		if err != nil {
			t.Fatalf("MarkSuspect: %v", err)
		}
		if suspect.Status != knowledge.StatusSuspect {
			t.Fatalf("status = %s, want suspect", suspect.Status)
		}
		if suspect.ReviewReason == nil || *suspect.ReviewReason != "login callback failed" {
			t.Fatalf("review reason = %v", suspect.ReviewReason)
		}
		if suspect.LastVerifiedAt == nil || !suspect.LastVerifiedAt.Equal(previous) {
			t.Fatalf("last_verified_at = %v, want %v", suspect.LastVerifiedAt, previous)
		}
		if len(repo.statusWrites) != 1 || repo.statusWrites[0].from != knowledge.StatusApproved {
			t.Fatalf("status writes = %+v", repo.statusWrites)
		}
	})

	t.Run("draft cannot be marked suspect", func(t *testing.T) {
		repo := newLifecycleRepo()
		repo.items[46] = draftItem(46, 7)
		service := newLifecycleService(repo)
		_, err := service.MarkSuspect(context.Background(), 46, "failed")
		assertAPIError(t, err, 409, "state_conflict", "invalid_transition")
		if len(repo.statusWrites) != 0 {
			t.Fatalf("draft failure marking must not write: %+v", repo.statusWrites)
		}
	})
}

/* ------------------------------ source staleness -------------------------- */

// TestRetrieveSkipsSourceStale pins the R87 consistency between the two read
// paths: an item whose source version no longer exists is filtered out by both
// Inject and Retrieve (never injected or returned by knowledge.search). The
// explicit read (knowledge.read) keeps returning it with the source_stale
// marker so the cause is diagnosable.
func TestRetrieveSkipsSourceStale(t *testing.T) {
	staleSource := approvedItem(201, knowledge.ScopeGlobal, "")
	staleSource.SourceVersionID = ptr[int64](501)
	noSource := approvedItem(202, knowledge.ScopeGlobal, "")
	liveSource := approvedItem(203, knowledge.ScopeGlobal, "")
	liveSource.SourceVersionID = ptr[int64](502)

	repo := newRetrievalRepo(nil, []knowledge.Item{staleSource, noSource, liveSource}).
		track(staleSource, noSource, liveSource)
	sources := &fakeSources{missing: map[int64]bool{501: true}}
	service := knowledgeapp.NewService(repo, nil, nil, nil, nil, sources, fixedClock{now: testNow}, &seqIDs{next: 3000})

	page, err := service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 1})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if containsID(page, 201) {
		t.Fatalf("knowledge.search returned a source-stale item: %+v", page.Items)
	}
	if !containsID(page, 202) || !containsID(page, 203) {
		t.Fatalf("knowledge.search dropped live items: %+v", page.Items)
	}

	injected, err := service.Inject(context.Background(), knowledgeapp.InjectInput{VersionID: 1})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	injectedIDs := make(map[int64]bool, len(injected))
	for _, item := range injected {
		injectedIDs[item.ID] = true
	}
	if injectedIDs[201] {
		t.Fatal("Inject admitted a source-stale item")
	}
	for _, id := range []int64{202, 203} {
		if !injectedIDs[id] {
			t.Fatalf("Inject dropped live item %d: %+v", id, injected)
		}
	}

	// A source-read failure is not proof of staleness: both paths keep the item
	// (fail-open, matching the existing Get/list marker policy).
	sources.err = context.DeadlineExceeded
	page, err = service.Retrieve(context.Background(), knowledgeapp.Query{VersionID: 1})
	if err != nil {
		t.Fatalf("Retrieve with a failing source reader: %v", err)
	}
	if !containsID(page, 201) {
		t.Fatalf("a failing source reader must not hide an item: %+v", page.Items)
	}
}

/* ------------------------------- invalidation ----------------------------- */

// TestRouteChangeMarksStale pins spec §10.1 "页面/路由/账号变更 → stale": a
// changed page route or account role marks every approved item verified against
// it stale (and only those), preserving the item body and audit metadata.
func TestRouteChangeMarksStale(t *testing.T) {
	repo := newLifecycleRepo()
	repo.listItems = []knowledge.Item{
		approvedBoundItem(101, knowledge.Bindings{PageKey: ptr("/login")}),
		approvedBoundItem(102, knowledge.Bindings{AccountRole: ptr("admin")}),
		approvedBoundItem(103, knowledge.Bindings{PageKey: ptr("/checkout")}),
		approvedBoundItem(104, knowledge.Bindings{}),
		func() knowledge.Item {
			item := approvedBoundItem(105, knowledge.Bindings{PageKey: ptr("/login")})
			item.Status = knowledge.StatusDraft
			return item
		}(),
		func() knowledge.Item {
			item := approvedBoundItem(106, knowledge.Bindings{PageKey: ptr("/login")})
			item.Status = knowledge.StatusStale
			return item
		}(),
		// 107 is the same route on another SUT: the change names system 1, so an
		// item explicitly bound to system 2 must not be invalidated by a
		// same-named route. 108 is the same route on the changed system.
		approvedBoundItem(107, knowledge.Bindings{SystemID: ptr[int64](2), PageKey: ptr("/login")}),
		approvedBoundItem(108, knowledge.Bindings{SystemID: ptr[int64](1), PageKey: ptr("/login")}),
	}
	repo.failStatus[102] = true // one concurrent writer must not abort the pass
	service := newLifecycleService(repo)

	result, err := service.MarkBindingsStale(context.Background(), knowledgeapp.BindingChange{
		SystemIDs:    []int64{1},
		PageKeys:     []string{" /login "},
		AccountRoles: []string{"admin"},
		Reason:       " route template changed ",
	})
	if err != nil {
		t.Fatalf("MarkBindingsStale: %v", err)
	}
	if result.Matched != 3 || result.Marked != 2 || result.Failed != 1 {
		t.Fatalf("result = %+v, want 3 matched / 2 marked / 1 failed", result)
	}
	if len(repo.statusWrites) != 3 {
		t.Fatalf("status writes = %d, want 3 (101, 102, 108)", len(repo.statusWrites))
	}
	wrote := make(map[int64]bool, len(repo.statusWrites))
	for _, write := range repo.statusWrites {
		wrote[write.item.ID] = true
	}
	if wrote[107] {
		t.Fatalf("item bound to another system was invalidated: %+v", repo.statusWrites)
	}
	if !wrote[108] {
		t.Fatalf("item bound to the changed system's route was not invalidated: %+v", repo.statusWrites)
	}
	for _, write := range repo.statusWrites {
		if write.from != knowledge.StatusApproved || write.item.Status != knowledge.StatusStale {
			t.Fatalf("status write = %+v", write)
		}
		if write.item.ReviewReason == nil || *write.item.ReviewReason != "route template changed" {
			t.Fatalf("reason = %v", write.item.ReviewReason)
		}
	}
	stored := repo.items[101]
	if stored.Status != knowledge.StatusStale {
		t.Fatalf("stored item = %+v, want stale", stored)
	}
	if repo.listFilter.Status != knowledge.StatusApproved {
		t.Fatalf("candidate list status filter = %q", repo.listFilter.Status)
	}
	for _, write := range repo.statusWrites {
		if write.item.ID == 103 || write.item.ID == 104 || write.item.ID == 105 || write.item.ID == 106 {
			t.Fatalf("unrelated item %d was marked stale: %+v", write.item.ID, write)
		}
	}

	t.Run("empty change is rejected", func(t *testing.T) {
		empty := newLifecycleRepo()
		_, err := newLifecycleService(empty).MarkBindingsStale(context.Background(),
			knowledgeapp.BindingChange{Reason: "no keys"})
		if apiErr := platform.AsError(err); apiErr.Status != 400 || apiErr.Details["field"] != "change" {
			t.Fatalf("error = %+v, want invalid change", apiErr)
		}
	})

	// The version side of the same policy: the write path that owns the page and
	// account bindings exposes the current route/role keys, so a change can be
	// diffed against a captured snapshot and fed to MarkBindingsStale.
	t.Run("version exposes the page route and account role keys", func(t *testing.T) {
		bindings := []version.Binding{{
			ID:            9001,
			VersionID:     42,
			SUTID:         1001,
			EnvironmentID: 1101,
			ScopeExclude:  version.NewScopeExclude(nil, nil, nil),
			Accounts:      []version.AccountBinding{{AccountID: 5001}},
		}}
		catalog := versionKeysCatalog{
			pages: map[int64]resourceapp.PageView{
				1301: {ID: "1301", RouteTemplate: " /login "},
				1302: {ID: "1302", RouteTemplate: ""},
			},
			accounts: map[int64]resourceapp.AccountView{
				5001: {ID: "5001", Username: "admin", Roles: []string{" admin "}, Status: "active"},
			},
			environments: map[int64]resourceapp.EnvironmentView{
				1101: {ID: "1101", SUTID: "1001", Pages: []resourceapp.PageView{
					{ID: "1301", RouteTemplate: " /login ", Status: "approved"},
					{ID: "1302", RouteTemplate: "", Status: "approved"},
				}},
			},
		}
		service := versionapp.New(versionKeysRepo{bindings: bindings}, catalog, nil, nil, nil, nil, nil)
		keys, err := service.KnowledgeBindingKeys(context.Background(), 42)
		if err != nil {
			t.Fatalf("KnowledgeBindingKeys: %v", err)
		}
		if keys.VersionID != 42 || len(keys.SystemIDs) != 1 || keys.SystemIDs[0] != 1001 ||
			len(keys.EnvironmentIDs) != 1 || keys.EnvironmentIDs[0] != 1101 {
			t.Fatalf("keys = %+v", keys)
		}
		if len(keys.PageKeys) != 1 || keys.PageKeys[0] != "/login" {
			t.Fatalf("page keys = %v", keys.PageKeys)
		}
		if len(keys.AccountRoles) != 1 || keys.AccountRoles[0] != "admin" {
			t.Fatalf("account roles = %v", keys.AccountRoles)
		}
	})
}

// pagedLifecycleRepo is lifecycleRepo with a repository-accurate List: id DESC,
// status filter, exclusive BeforeID cursor and a hard limit. It lets the
// invalidation paging contract be exercised without PostgreSQL.
type pagedLifecycleRepo struct {
	*lifecycleRepo
	listCalls []knowledge.Filter
}

func newPagedLifecycleRepo() *pagedLifecycleRepo {
	return &pagedLifecycleRepo{lifecycleRepo: newLifecycleRepo()}
}

func (r *pagedLifecycleRepo) List(_ context.Context, filter knowledge.Filter) ([]knowledge.Item, error) {
	r.listCalls = append(r.listCalls, filter)
	items := make([]knowledge.Item, 0, len(r.items))
	for _, item := range r.items {
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.BeforeID > 0 && item.ID >= filter.BeforeID {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID > items[right].ID })
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, r.listErr
}

// TestKnowledgeBindingInvalidationPagination pins that MarkBindingsStale walks
// every approved page instead of only the newest one: an affected row older than
// the first MaxListLimit window is still invalidated, while a matching row whose
// CAS loses a race is reported as failed. A run whose matched rows all failed is
// therefore distinguishable from a run with no matches.
func TestKnowledgeBindingInvalidationPagination(t *testing.T) {
	repo := newPagedLifecycleRepo()
	for id := int64(1); id <= 130; id++ {
		pageKey := "/login"
		if id > 120 {
			pageKey = "/checkout"
		}
		repo.items[id] = approvedBoundItem(id, knowledge.Bindings{PageKey: ptr(pageKey)})
	}
	repo.failStatus[5] = true // an older affected row loses the CAS race
	service := newLifecycleService(repo)

	result, err := service.MarkBindingsStale(context.Background(), knowledgeapp.BindingChange{
		PageKeys: []string{"/login"},
	})
	if err != nil {
		t.Fatalf("MarkBindingsStale: %v", err)
	}
	if result.Scanned != 130 || result.Matched != 120 || result.Marked != 119 || result.Failed != 1 {
		t.Fatalf("result = %+v, want scanned=130 matched=120 marked=119 failed=1", result)
	}

	// 120 rows match; 119 move to stale and the failed CAS stays approved.
	if stored := repo.items[4]; stored.Status != knowledge.StatusStale {
		t.Fatalf("older bound item 4 = %+v, want stale", stored)
	}
	if stored := repo.items[5]; stored.Status != knowledge.StatusApproved {
		t.Fatalf("failed-CAS item 5 = %+v, want approved (best effort)", stored)
	}
	if stored := repo.items[120]; stored.Status != knowledge.StatusStale {
		t.Fatalf("last bound item 120 = %+v, want stale", stored)
	}
	if stored := repo.items[121]; stored.Status != knowledge.StatusApproved {
		t.Fatalf("unbound item 121 = %+v, want approved", stored)
	}
	if len(repo.statusWrites) != 120 {
		t.Fatalf("status writes = %d, want 120 matched rows", len(repo.statusWrites))
	}

	// The scan paginates by the id-desc cursor: page one is the newest 100 ids,
	// the last id of that page is the exclusive cursor of page two.
	if len(repo.listCalls) != 2 {
		t.Fatalf("list calls = %d, want 2 pages", len(repo.listCalls))
	}
	if first := repo.listCalls[0]; first.Status != knowledge.StatusApproved ||
		first.BeforeID != 0 || first.Limit != knowledgeapp.MaxListLimit {
		t.Fatalf("first page filter = %+v", first)
	}
	if second := repo.listCalls[1]; second.BeforeID != 31 {
		t.Fatalf("second page cursor = %d, want 31", second.BeforeID)
	}

	// An all-CAS-failure pass is distinguishable from a no-match pass: both move
	// zero rows, but only the failure case reports matched/failed counts.
	t.Run("an all-CAS-failure pass reports the failure count", func(t *testing.T) {
		failed := newPagedLifecycleRepo()
		failed.items[501] = approvedBoundItem(501, knowledge.Bindings{PageKey: ptr("/login")})
		failed.failStatus[501] = true
		failedService := newLifecycleService(failed)

		allFailed, err := failedService.MarkBindingsStale(context.Background(),
			knowledgeapp.BindingChange{PageKeys: []string{"/login"}})
		if err != nil {
			t.Fatalf("MarkBindingsStale: %v", err)
		}
		if allFailed.Marked != 0 || allFailed.Matched != 1 || allFailed.Failed != 1 ||
			allFailed.Scanned != 1 {
			t.Fatalf("all-failed result = %+v", allFailed)
		}

		noMatch, err := failedService.MarkBindingsStale(context.Background(),
			knowledgeapp.BindingChange{PageKeys: []string{"/other"}})
		if err != nil {
			t.Fatalf("MarkBindingsStale (no match): %v", err)
		}
		if noMatch.Marked != 0 || noMatch.Matched != 0 || noMatch.Failed != 0 {
			t.Fatalf("no-match result = %+v, want zero matched/failed", noMatch)
		}
	})
}

// TestKnowledgeBindingInvalidationReasonValidation pins that the stored
// invalidation reason is validated with the same rule as UpdateStatus: an
// overlong reason is a caller error (400 field=reason) and writes nothing.
func TestKnowledgeBindingInvalidationReasonValidation(t *testing.T) {
	repo := newLifecycleRepo()
	repo.listItems = []knowledge.Item{
		approvedBoundItem(109, knowledge.Bindings{PageKey: ptr("/login")}),
	}
	_, err := newLifecycleService(repo).MarkBindingsStale(context.Background(),
		knowledgeapp.BindingChange{
			PageKeys: []string{"/login"},
			Reason:   strings.Repeat("x", knowledge.MaxReasonRunes+1),
		})
	if err == nil {
		t.Fatal("an overlong reason must be rejected")
	}
	if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != 400 ||
		apiErr.Details["field"] != "reason" {
		t.Fatalf("error = %+v, want 400 field=reason", apiErr)
	}
	if len(repo.statusWrites) != 0 {
		t.Fatalf("an invalid reason wrote %d statuses", len(repo.statusWrites))
	}
}

// versionKeysRepo/repoCatalog are minimal read fakes for KnowledgeBindingKeys:
// embedding the port interfaces keeps the unused methods out of the test.
type versionKeysRepo struct {
	version.Repository
	bindings []version.Binding
}

func (r versionKeysRepo) Get(_ context.Context, id int64) (version.Version, []version.Binding, error) {
	return version.Version{ID: id}, r.bindings, nil
}

type versionKeysCatalog struct {
	resourceapp.CatalogReader
	pages        map[int64]resourceapp.PageView
	accounts     map[int64]resourceapp.AccountView
	environments map[int64]resourceapp.EnvironmentView
}

func (c versionKeysCatalog) GetEnvironment(_ context.Context, id int64) (resourceapp.EnvironmentView, error) {
	environment, ok := c.environments[id]
	if !ok {
		return resourceapp.EnvironmentView{}, platform.NotFound("environment")
	}
	return environment, nil
}

func (c versionKeysCatalog) GetPage(_ context.Context, id int64) (resourceapp.PageView, error) {
	page, ok := c.pages[id]
	if !ok {
		return resourceapp.PageView{}, platform.NotFound("page")
	}
	return page, nil
}

func (c versionKeysCatalog) GetAccount(_ context.Context, id int64) (resourceapp.AccountView, error) {
	account, ok := c.accounts[id]
	if !ok {
		return resourceapp.AccountView{}, platform.NotFound("account")
	}
	return account, nil
}

/* ------------------------------- HTTP secrets ----------------------------- */

func assertSecretRejection(t *testing.T, recorder *httptest.ResponseRecorder, body map[string]any, secret string) {
	t.Helper()
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%v, want 400", recorder.Code, body)
	}
	envelope := errorEnvelope(t, body)
	if envelope["code"] != "invalid_argument" {
		t.Fatalf("error code = %v, want invalid_argument", envelope["code"])
	}
	message, _ := envelope["message"].(string)
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "secret") && !strings.Contains(lower, "credential") {
		t.Fatalf("error message %q is not secret-specific", message)
	}
	details, _ := envelope["details"].(map[string]any)
	if details["field"] != "secret" {
		t.Fatalf("error details = %v, want field=secret", details)
	}
	// The rejection must never echo the credential back: the response is a
	// generic field-level error, not a validation dump of the submitted body.
	if secret != "" && strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("rejection echoed the credential %q: %s", secret, recorder.Body.String())
	}
}

// TestCreateApproveRejectSecrets pins the R91 write gate end to end over HTTP:
// create/update/approve reject a secret-bearing item with a secret-specific 400
// and write nothing; a clean create still round-trips through HTTP approve.
func TestCreateApproveRejectSecrets(t *testing.T) {
	t.Run("create rejects a secret-bearing body without writing", func(t *testing.T) {
		_, repo, mux := newHTTPHarness(t)
		for _, testCase := range []struct {
			name    string
			payload string
			secret  string
		}{
			{"assignment", `{"title":"登录配方","content":"password=hunter2"}`, "hunter2"},
			{"jwt", `{"title":"登录配方","content":"token: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln"}`,
				"eyJhbGciOiJIUzI1NiJ9"},
			{"tag", `{"title":"登录配方","content":"打开 /login","tags":["Bearer abc123"]}`, "abc123"},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				recorder, body := httpCall(t, mux, http.MethodPost, "/api/knowledge/items", testCase.payload)
				assertSecretRejection(t, recorder, body, testCase.secret)
			})
		}
		if repo.insertCalls != 0 {
			t.Fatalf("secret-bearing creates wrote %d items", repo.insertCalls)
		}
	})

	t.Run("create then approve end to end", func(t *testing.T) {
		_, repo, mux := newHTTPHarness(t)
		recorder, body := httpCall(t, mux, http.MethodPost, "/api/knowledge/items",
			`{"title":"登录配方","content":"打开 /login，使用 {{username}} 登录","tags":["kind:login_recipe"]}`)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create status = %d body=%v", recorder.Code, body)
		}
		created, _ := dataEnvelope(t, body).(map[string]any)
		if created["status"] != "draft" {
			t.Fatalf("created status = %v, want draft", created["status"])
		}
		id, _ := created["id"].(string)
		if id == "" {
			t.Fatalf("created item id missing: %v", created)
		}

		recorder, body = httpCall(t, mux, http.MethodPost, "/api/knowledge/items/"+id+"/approve", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("approve status = %d body=%v", recorder.Code, body)
		}
		approved, _ := dataEnvelope(t, body).(map[string]any)
		if approved["status"] != "approved" {
			t.Fatalf("approved status = %v, want approved", approved["status"])
		}
		if approved["reviewed_by"] != "88" {
			t.Fatalf("reviewed_by = %v, want 88", approved["reviewed_by"])
		}
		if repo.updateCalls != 0 {
			t.Fatalf("create+approve must not rewrite content: update calls = %d", repo.updateCalls)
		}
	})

	t.Run("update rejects a secret and keeps the stored body", func(t *testing.T) {
		_, repo, mux := newHTTPHarness(t)
		recorder, body := httpCall(t, mux, http.MethodPost, "/api/knowledge/items",
			`{"title":"登录配方","content":"打开 /login"}`)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create status = %d body=%v", recorder.Code, body)
		}
		created, _ := dataEnvelope(t, body).(map[string]any)
		id, _ := created["id"].(string)

		recorder, body = httpCall(t, mux, http.MethodPatch, "/api/knowledge/items/"+id,
			`{"content":"password=hunter2"}`)
		assertSecretRejection(t, recorder, body, "hunter2")
		if repo.updateCalls != 0 {
			t.Fatalf("a rejected update wrote the item: %d", repo.updateCalls)
		}

		recorder, body = httpCall(t, mux, http.MethodPatch, "/api/knowledge/items/"+id,
			`{"content":"打开 /login，提交表单"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("clean update status = %d body=%v", recorder.Code, body)
		}
	})

	t.Run("approve rejects a secret-bearing stored draft", func(t *testing.T) {
		_, repo, mux := newHTTPHarness(t)
		secret := draftItem(9101, 7)
		secret.Content = `{"login":{"password":"hunter2"}}`
		repo.items[9101] = secret

		recorder, body := httpCall(t, mux, http.MethodPost, "/api/knowledge/items/9101/approve", "")
		assertSecretRejection(t, recorder, body, "hunter2")
		if stored := repo.items[9101]; stored.Status != knowledge.StatusDraft {
			t.Fatalf("secret-bearing draft was approved: %+v", stored)
		}
	})
}

// TestListHidesSecrets pins the R91 read gate: neither the item list nor the
// structured search returns an item whose stored title/tags/content carries a
// credential, even when the row was written before the gate existed.
func TestListHidesSecrets(t *testing.T) {
	_, repo, mux := newHTTPHarness(t)
	clean := approvedItem(3001, knowledge.ScopeGlobal, "")
	clean.Title = "登录配方"
	clean.Content = "打开 /login"
	repo.listItems = []knowledge.Item{
		clean,
		func() knowledge.Item {
			item := approvedItem(3002, knowledge.ScopeGlobal, "")
			item.Title = "登录配方（旧）"
			item.Content = "password=hunter2"
			return item
		}(),
		func() knowledge.Item {
			item := approvedItem(3003, knowledge.ScopeGlobal, "")
			item.Title = "Bearer abc123"
			item.Content = "打开 /login"
			return item
		}(),
	}

	for name, target := range map[string]string{
		"list":   "/api/knowledge/items",
		"search": "/api/knowledge/search",
	} {
		t.Run(name+" hides secret-bearing items", func(t *testing.T) {
			recorder, body := httpCall(t, mux, http.MethodGet, target, "")
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body=%v", recorder.Code, body)
			}
			items, ok := dataEnvelope(t, body).([]any)
			if !ok {
				t.Fatalf("data = %v, want an item array", body["data"])
			}
			if len(items) != 1 {
				t.Fatalf("returned %d items, want only the clean one: %v", len(items), items)
			}
			item, _ := items[0].(map[string]any)
			if item["id"] != "3001" {
				t.Fatalf("returned item = %v, want 3001", item)
			}
			if strings.Contains(recorder.Body.String(), "hunter2") ||
				strings.Contains(recorder.Body.String(), "abc123") {
				t.Fatalf("response leaked a credential: %s", recorder.Body.String())
			}
		})
	}
}

/* --------------------------------- helpers -------------------------------- */

func newHTTPHarness(t *testing.T) (*knowledgeapp.Service, *lifecycleRepo, *http.ServeMux) {
	t.Helper()
	repo := newLifecycleRepo()
	service := newLifecycleService(repo)
	handler := knowledgeapi.New(service)
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 7, Role: "user"})))
		})
	}
	admin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 88, Role: "admin"})))
		})
	}
	handler.Register(mux, authenticated, admin)
	handler.RegisterSearch(mux, authenticated)
	return service, repo, mux
}

func httpCall(t *testing.T, mux *http.ServeMux, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Body.Len() == 0 {
		return recorder, map[string]any{}
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, recorder.Body.String())
	}
	return recorder, decoded
}

func errorEnvelope(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	envelope, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body = %v, want an error envelope", body)
	}
	return envelope
}

func dataEnvelope(t *testing.T, body map[string]any) any {
	t.Helper()
	data, ok := body["data"]
	if !ok {
		t.Fatalf("body = %v, want a data envelope", body)
	}
	return data
}
