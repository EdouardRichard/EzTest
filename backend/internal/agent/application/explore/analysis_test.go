package explore_test

// Task 37 (P9-T4) analysis tests: requirement gap dimensions, the
// requirement/page/API/role coverage matrix, draft-only case augmentation with
// dedupe, deterministic failure attribution, and the R93 proof that the
// analysis flows consume the shared P9 explore runtime (Login first,
// page-scoped close, serialized actions).

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

/* -------------------------------- fakes ---------------------------------- */

// fakeFactReader serves current version facts by kind. The analysis services
// only ever read; the tests assert the recorder is the only writer.
type fakeFactReader struct {
	facts map[string][]version.Fact
	err   error
}

func (f *fakeFactReader) Current(_ context.Context, _ int64, kind string) ([]version.Fact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]version.Fact(nil), f.facts[kind]...), nil
}

func newFactReader(facts ...version.Fact) *fakeFactReader {
	reader := &fakeFactReader{facts: map[string][]version.Fact{}}
	for _, fact := range facts {
		reader.facts[fact.Kind] = append(reader.facts[fact.Kind], fact)
	}
	return reader
}

type fakeRequirementSource struct {
	requirements []exploreapp.RequirementSnapshot
	err          error
	calls        int
	versionID    int64
}

func (f *fakeRequirementSource) Requirements(_ context.Context, versionID int64) ([]exploreapp.RequirementSnapshot, error) {
	f.calls++
	f.versionID = versionID
	if f.err != nil {
		return nil, f.err
	}
	return append([]exploreapp.RequirementSnapshot(nil), f.requirements...), nil
}

type fakeCaseSource struct {
	cases     []exploreapp.CaseSnapshot
	err       error
	calls     int
	versionID int64
}

func (f *fakeCaseSource) Cases(_ context.Context, versionID int64) ([]exploreapp.CaseSnapshot, error) {
	f.calls++
	f.versionID = versionID
	if f.err != nil {
		return nil, f.err
	}
	return append([]exploreapp.CaseSnapshot(nil), f.cases...), nil
}

type fakeRoleSource struct {
	roles []string
	err   error
	calls int
}

func (f *fakeRoleSource) Roles(_ context.Context, _ int64) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]string(nil), f.roles...), nil
}

// fakeRuntime is the explore runtime port consumed by gap analysis. It records
// the interaction order and the maximum number of concurrent opens, proving the
// flow logs in before opening any page and serializes its explore actions.
type fakeRuntime struct {
	mu        sync.Mutex
	events    []string
	loginCall int
	openCalls []string
	active    int
	maxActive int
	blocked   bool
	loginErr  error
	openErr   error
	pages     []string
}

func (f *fakeRuntime) Login(_ context.Context, cfg exploreapp.LoginFlowConfig) (exploreapp.SessionFact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginCall++
	f.events = append(f.events, "login")
	if f.loginErr != nil {
		return exploreapp.SessionFact{}, f.loginErr
	}
	fact := exploreapp.SessionFact{
		VersionID:     cfg.VersionID,
		VersionSUTID:  cfg.VersionSUTID,
		EnvironmentID: cfg.EnvironmentID,
		AccountRole:   cfg.Role,
		Status:        exploreapp.SessionActive,
	}
	if f.blocked {
		fact.Status = exploreapp.SessionBlocked
		fact.Reason = "invalid_credentials"
		fact.Action = exploreapp.ActionFixCredentials
	}
	return fact, nil
}

func (f *fakeRuntime) OpenWithSession(_ context.Context, _ exploreapp.LoginFlowConfig, url string) (exploreapp.Page, exploreapp.SessionFact, error) {
	f.mu.Lock()
	f.openCalls = append(f.openCalls, url)
	f.events = append(f.events, "open:"+url)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.openErr != nil {
		return exploreapp.Page{}, exploreapp.SessionFact{}, f.openErr
	}
	pageID := "page_" + urlSuffix(url)
	return exploreapp.Page{ID: pageID, URL: url}, exploreapp.SessionFact{}, nil
}

func urlSuffix(url string) string {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	return parts[len(parts)-1]
}

func (f *fakeRuntime) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loginCall
}

func (f *fakeRuntime) opens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.openCalls...)
}

func (f *fakeRuntime) sequence() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeRuntime) maxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

// fakePageCloser records the page-scoped close ids. There is deliberately no
// close-all method: the analysis must release exactly the pages it opened.
type fakePageCloser struct {
	mu     sync.Mutex
	closed []string
	err    error
}

func (f *fakePageCloser) ClosePage(_ context.Context, pageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, pageID)
	return f.err
}

func (f *fakePageCloser) closedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

type fakeDraftWriter struct {
	mu       sync.Mutex
	drafts   []exploreapp.CaseDraft
	status   string
	err      error
	version  int64
	returned []exploreapp.CaseDraftResult
}

func (f *fakeDraftWriter) CreateDraftCase(_ context.Context, versionID int64, draft exploreapp.CaseDraft) (exploreapp.CaseDraftResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version = versionID
	f.drafts = append(f.drafts, draft)
	if f.err != nil {
		return exploreapp.CaseDraftResult{}, f.err
	}
	status := f.status
	if status == "" {
		status = "draft"
	}
	result := exploreapp.CaseDraftResult{CaseID: int64(7000 + len(f.drafts)), CaseVersionID: int64(8000 + len(f.drafts)), Status: status}
	f.returned = append(f.returned, result)
	return result, nil
}

func (f *fakeDraftWriter) created() []exploreapp.CaseDraft {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]exploreapp.CaseDraft(nil), f.drafts...)
}

/* ------------------------------ fixtures --------------------------------- */

func pageFact(id int64, pageKey, routeTemplate, url string) version.Fact {
	return version.Fact{
		ID:      id,
		Kind:    exploreapp.KindPage,
		Key:     "page:" + pageKey,
		Payload: map[string]any{"page_key": pageKey, "route_template": routeTemplate, "url": url},
	}
}

func apiFact(id int64, apiKey, state string) version.Fact {
	return version.Fact{
		ID:      id,
		Kind:    exploreapp.KindAPI,
		Key:     "api:" + apiKey,
		Payload: map[string]any{"api_key": apiKey, "state": state, "method": "POST", "path_template": "/orders"},
	}
}

func loginFact(id int64, role string) version.Fact {
	return version.Fact{
		ID:      id,
		Kind:    exploreapp.KindLoginSession,
		Key:     "vs:7:role:" + role,
		Payload: map[string]any{"account_role": role, "route": "/login"},
	}
}

func gapFact(id int64, requirementID int64, requirementTitle string, dimension exploreapp.GapDimension, reason string) version.Fact {
	return version.Fact{
		ID:   id,
		Kind: exploreapp.KindGap,
		Key:  "gap:" + string(dimension) + ":" + strconv.FormatInt(requirementID, 10),
		Payload: map[string]any{
			"dimension":         string(dimension),
			"requirement_id":    requirementID,
			"requirement_title": requirementTitle,
			"reason":            reason,
			"question":          "请补充" + dimension.Label(),
			"target_pages":      []any{},
			"target_apis":       []any{},
			"source":            "requirement",
		},
	}
}

func richRequirement() exploreapp.RequirementSnapshot {
	return exploreapp.RequirementSnapshot{
		ID:                 102,
		VersionID:          1002,
		Title:              "退款",
		Status:             "clarified",
		Description:        "管理员可对已支付订单退款；支付系统异常时提示错误；退款金额上限 10000；使用预置退款账号数据。",
		AcceptanceCriteria: "退款成功后订单状态变为已退款",
		Constraints:        json.RawMessage(`{"权限":"仅管理员","超时":"30秒"}`),
		TargetPages:        []string{"/checkout"},
		TargetAPIs:         []string{"POST /orders"},
	}
}

func thinRequirement() exploreapp.RequirementSnapshot {
	return exploreapp.RequirementSnapshot{
		ID:          101,
		VersionID:   1001,
		Title:       "下单",
		Status:      "clarified",
		Description: "用户下单",
		TargetPages: []string{"/missing"},
	}
}

/* ------------------------ gap analysis dimensions ------------------------ */

func TestGapAnalysisDimensions(t *testing.T) {
	reader := newFactReader(
		pageFact(1, "http://sut.test/checkout", "/checkout", "http://sut.test/checkout"),
		apiFact(2, "POST /orders", "declared_used"),
	)
	facts := &fakeFacts{}
	analysis := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{}, exploreapp.GapAnalysisDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{thinRequirement(), richRequirement()}},
		Facts:        facts,
		FactReader:   reader,
	})

	result, err := analysis.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("GapAnalysis.Run: %v", err)
	}
	if result.Requirements != 2 {
		t.Fatalf("requirements = %d, want 2", result.Requirements)
	}

	wantDimensions := []exploreapp.GapDimension{
		exploreapp.GapDimensionAcceptance,
		exploreapp.GapDimensionBoundary,
		exploreapp.GapDimensionPermission,
		exploreapp.GapDimensionException,
		exploreapp.GapDimensionData,
		exploreapp.GapDimensionDependency,
	}
	if len(result.Gaps) != len(wantDimensions) {
		t.Fatalf("gaps = %d (%+v), want %d", len(result.Gaps), result.Gaps, len(wantDimensions))
	}
	for index, dimension := range wantDimensions {
		gap := result.Gaps[index]
		if gap.Dimension != dimension {
			t.Fatalf("gaps[%d].dimension = %q, want %q", index, gap.Dimension, dimension)
		}
		if gap.RequirementID != 101 {
			t.Fatalf("gaps[%d].requirement = %d, want 101 (rich requirement must have no gap)", index, gap.RequirementID)
		}
		if strings.TrimSpace(gap.Reason) == "" || strings.TrimSpace(gap.Question) == "" {
			t.Fatalf("gaps[%d] must carry a reason and a clarification question: %+v", index, gap)
		}
		if gap.FactID == 0 {
			t.Fatalf("gaps[%d] was not recorded as a fact", index)
		}
	}
	if len(result.FactIDs) != len(result.Gaps) {
		t.Fatalf("fact ids = %d, want %d", len(result.FactIDs), len(result.Gaps))
	}
	recorded := facts.recorded()
	if len(recorded) != len(result.Gaps) {
		t.Fatalf("recorded facts = %d, want %d", len(recorded), len(result.Gaps))
	}
	for _, fact := range recorded {
		if fact.Kind != exploreapp.KindGap {
			t.Fatalf("fact kind = %q, want %q", fact.Kind, exploreapp.KindGap)
		}
		payload := fact.Payload
		if payload["requirement_id"] != int64(101) {
			t.Fatalf("fact requirement id = %#v", payload["requirement_id"])
		}
		text := strings.ToLower(strings.Join([]string{
			payload["reason"].(string),
			payload["question"].(string),
		}, " "))
		if strings.Contains(text, "password") || strings.Contains(text, "secret") {
			t.Fatalf("gap payload leaked a credential: %q", text)
		}
	}

	// Bound: the configured requirement cap stops the scan deterministically.
	bounded := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{MaxRequirements: 1}, exploreapp.GapAnalysisDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{thinRequirement(), richRequirement()}},
		Facts:        &fakeFacts{},
		FactReader:   reader,
	})
	truncated, err := bounded.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("bounded GapAnalysis.Run: %v", err)
	}
	if !truncated.Truncated {
		t.Fatal("requirement cap must report truncation")
	}
	for _, gap := range truncated.Gaps {
		if gap.RequirementID != 101 {
			t.Fatalf("bounded run analyzed requirement %d", gap.RequirementID)
		}
	}
	if len(truncated.Gaps) != len(wantDimensions) {
		t.Fatalf("bounded gaps = %d, want %d", len(truncated.Gaps), len(wantDimensions))
	}

	// Determinism: the same inputs produce the same gap keys in the same order.
	first := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{}, exploreapp.GapAnalysisDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{thinRequirement()}},
		Facts:        &fakeFacts{},
		FactReader:   reader,
	})
	second := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{}, exploreapp.GapAnalysisDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{thinRequirement()}},
		Facts:        &fakeFacts{},
		FactReader:   reader,
	})
	firstResult, err := first.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("first deterministic run: %v", err)
	}
	secondResult, err := second.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("second deterministic run: %v", err)
	}
	if len(firstResult.Gaps) != len(secondResult.Gaps) {
		t.Fatalf("deterministic gap count differs: %d vs %d", len(firstResult.Gaps), len(secondResult.Gaps))
	}
	for index := range firstResult.Gaps {
		if firstResult.Gaps[index].Dimension != secondResult.Gaps[index].Dimension ||
			firstResult.Gaps[index].Reason != secondResult.Gaps[index].Reason {
			t.Fatalf("gap %d is not deterministic: %+v vs %+v", index, firstResult.Gaps[index], secondResult.Gaps[index])
		}
	}
}

/* ---------------------------- coverage matrix ---------------------------- */

func TestCoverageMatrix(t *testing.T) {
	reader := newFactReader(
		pageFact(11, "http://sut.test/checkout", "/checkout", "http://sut.test/checkout"),
		pageFact(12, "http://sut.test/uncovered", "/uncovered", "http://sut.test/uncovered"),
		apiFact(13, "POST /orders", "declared_used"),
		apiFact(14, "POST /refunds", "declared_unused"),
		loginFact(15, "admin"),
	)
	cases := &fakeCaseSource{cases: []exploreapp.CaseSnapshot{
		{CaseID: 301, CaseVersionID: 3001, RequirementID: 201, Title: "下单成功", Status: "confirmed", PageKeys: []string{"/checkout"}, APIKeys: []string{"POST /orders"}, AccountRole: "admin"},
		{CaseID: 302, CaseVersionID: 3002, RequirementID: 201, Title: "下单草稿", Status: "draft"},
		{CaseID: 303, CaseVersionID: 3003, RequirementID: 203, Title: "已失效", Status: "stale"},
	}}
	coverage := exploreapp.NewCoverage(exploreapp.CoverageConfig{}, exploreapp.CoverageDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{
			{ID: 201, VersionID: 2001, Title: "下单", Status: "clarified", TargetPages: []string{"/checkout"}, TargetAPIs: []string{"POST /orders"}},
			{ID: 202, VersionID: 2002, Title: "退款", Status: "clarified", TargetPages: []string{"/refunds"}},
		}},
		Cases: cases,
		Facts: reader,
		Roles: &fakeRoleSource{roles: []string{"admin", "guest"}},
	})

	matrix, err := coverage.Build(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Coverage.Build: %v", err)
	}
	if len(matrix.Requirements) != 2 {
		t.Fatalf("requirements = %d, want 2", len(matrix.Requirements))
	}
	if !matrix.Requirements[0].Covered || matrix.Requirements[0].RequirementID != 201 {
		t.Fatalf("requirement 201 not covered: %+v", matrix.Requirements[0])
	}
	if len(matrix.Requirements[0].CaseIDs) != 2 {
		t.Fatalf("requirement 201 cases = %+v, want the draft and confirmed case", matrix.Requirements[0].CaseIDs)
	}
	if matrix.Requirements[1].Covered || matrix.Requirements[1].RequirementID != 202 {
		t.Fatalf("requirement 202 must be uncovered: %+v", matrix.Requirements[1])
	}
	if len(matrix.Requirements[1].MissingTargets) == 0 {
		t.Fatalf("requirement 202 must report its uncovered target: %+v", matrix.Requirements[1])
	}

	if len(matrix.Pages) != 2 {
		t.Fatalf("pages = %d, want 2: %+v", len(matrix.Pages), matrix.Pages)
	}
	pages := map[string]exploreapp.PageCoverage{}
	for _, page := range matrix.Pages {
		pages[page.RouteTemplate] = page
	}
	if !pages["/checkout"].Covered || len(pages["/checkout"].CaseIDs) != 1 {
		t.Fatalf("page /checkout not covered: %+v", pages["/checkout"])
	}
	if pages["/uncovered"].Covered {
		t.Fatalf("page /uncovered must stay uncovered: %+v", pages["/uncovered"])
	}

	if len(matrix.APIs) != 2 {
		t.Fatalf("apis = %d, want 2: %+v", len(matrix.APIs), matrix.APIs)
	}
	apis := map[string]exploreapp.APICoverage{}
	for _, api := range matrix.APIs {
		apis[api.APIKey] = api
	}
	if !apis["POST /orders"].Covered || apis["POST /refunds"].Covered {
		t.Fatalf("api coverage = %+v", matrix.APIs)
	}
	if apis["POST /refunds"].State != "declared_unused" {
		t.Fatalf("api state = %q", apis["POST /refunds"].State)
	}

	if len(matrix.Roles) != 2 {
		t.Fatalf("roles = %d, want 2: %+v", len(matrix.Roles), matrix.Roles)
	}
	roles := map[string]exploreapp.RoleCoverage{}
	for _, role := range matrix.Roles {
		roles[role.Role] = role
	}
	if !roles["admin"].Covered || !roles["admin"].LoginVerified {
		t.Fatalf("role admin not covered: %+v", roles["admin"])
	}
	if roles["guest"].Covered || roles["guest"].LoginVerified {
		t.Fatalf("role guest must stay uncovered: %+v", roles["guest"])
	}
	if matrix.Totals.RequirementsCovered != 1 || matrix.Totals.RequirementsUncovered != 1 {
		t.Fatalf("totals = %+v", matrix.Totals)
	}
	if matrix.Totals.Cases != 3 {
		t.Fatalf("case total = %d, want 3 (stale cases are counted but never cover)", matrix.Totals.Cases)
	}

	// The stale case must not appear in any requirement/case coverage list.
	for _, requirement := range matrix.Requirements {
		for _, caseID := range requirement.CaseIDs {
			if caseID == 303 {
				t.Fatalf("stale case 303 counted as coverage: %+v", requirement)
			}
		}
	}
	if cases.calls != 1 || cases.versionID != 1001 {
		t.Fatalf("case source calls = %d version = %d", cases.calls, cases.versionID)
	}
}

/* --------------------------- case augmentation --------------------------- */

func TestCaseAugmentDedup(t *testing.T) {
	reader := newFactReader(
		gapFact(21, 401, "下单", exploreapp.GapDimensionAcceptance, "缺少验收标准"),
		gapFact(22, 401, "下单", exploreapp.GapDimensionException, "缺少异常处理"),
		gapFact(23, 402, "退款", exploreapp.GapDimensionAcceptance, "缺少验收标准"),
	)
	existingTitle := exploreapp.DraftCaseTitle("下单", exploreapp.GapDimensionAcceptance)
	cases := &fakeCaseSource{cases: []exploreapp.CaseSnapshot{
		{CaseID: 501, CaseVersionID: 5001, RequirementID: 401, Title: existingTitle, Status: "draft"},
	}}
	writer := &fakeDraftWriter{}
	augment := exploreapp.NewCaseAugment(exploreapp.CaseAugmentConfig{}, exploreapp.CaseAugmentDeps{
		Facts:  reader,
		Cases:  cases,
		Writer: writer,
	})

	result, err := augment.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("CaseAugment.Run: %v", err)
	}
	if result.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", result.Duplicates)
	}
	if len(result.Drafts) != 2 {
		t.Fatalf("drafts = %d, want 2: %+v", len(result.Drafts), result.Drafts)
	}
	for _, draft := range result.Drafts {
		if draft.DedupeKey == "" {
			t.Fatalf("draft has no dedupe key: %+v", draft)
		}
		if len(draft.SourceFactIDs) != 1 {
			t.Fatalf("draft must reference exactly one gap fact: %+v", draft)
		}
		if len(draft.Steps) == 0 || len(draft.Assertions) == 0 {
			t.Fatalf("draft is not executable: %+v", draft)
		}
		if len(draft.SourceFactKeys) != 1 {
			t.Fatalf("draft source fact keys = %+v", draft.SourceFactKeys)
		}
		if draft.Title == existingTitle {
			t.Fatalf("duplicate draft was produced: %q", draft.Title)
		}
	}
	created := writer.created()
	if len(created) != 2 {
		t.Fatalf("writer calls = %d, want 2", len(created))
	}
	if len(result.Created) != 2 || result.Created[0].Status != "draft" {
		t.Fatalf("created = %+v", result.Created)
	}
	for _, item := range result.Created {
		if item.Status != "draft" {
			t.Fatalf("augmentation wrote a non-draft case: %+v", item)
		}
	}

	// A writer that claims a confirmed result is rejected: augmentation may only
	// produce draft cases.
	confirmedWriter := &fakeDraftWriter{status: "confirmed"}
	rejecting := exploreapp.NewCaseAugment(exploreapp.CaseAugmentConfig{}, exploreapp.CaseAugmentDeps{
		Facts:  reader,
		Cases:  &fakeCaseSource{},
		Writer: confirmedWriter,
	})
	if _, err := rejecting.Run(context.Background(), 1001); err == nil {
		t.Fatal("CaseAugment must reject a non-draft writer result")
	} else if !strings.Contains(err.Error(), "draft") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Without a writer the drafts are returned for a caller that persists them.
	draftOnly := exploreapp.NewCaseAugment(exploreapp.CaseAugmentConfig{}, exploreapp.CaseAugmentDeps{
		Facts: reader,
		Cases: &fakeCaseSource{},
	})
	drafts, err := draftOnly.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("draft-only CaseAugment.Run: %v", err)
	}
	if len(drafts.Drafts) != 3 || len(drafts.Created) != 0 {
		t.Fatalf("draft-only result = %+v", drafts)
	}

	// Bound: the configured cap stops draft creation deterministically.
	bounded := exploreapp.NewCaseAugment(exploreapp.CaseAugmentConfig{MaxDrafts: 1}, exploreapp.CaseAugmentDeps{
		Facts: reader,
		Cases: &fakeCaseSource{},
	})
	limited, err := bounded.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("bounded CaseAugment.Run: %v", err)
	}
	if len(limited.Drafts) != 1 || !limited.Truncated {
		t.Fatalf("bounded result = %+v", limited)
	}
}

/* --------------------------- failure attribution ------------------------- */

func TestFailureAttribution(t *testing.T) {
	attribution := exploreapp.NewFailureAttribution()

	application, err := attribution.Classify(context.Background(), 1001, exploreapp.FailureSignal{
		CaseRunID:    9001,
		CaseTitle:    "提交订单",
		FailureClass: "failed",
		Category:     "ui",
		Reason:       "expected button text 提交订单 got 提交",
		EvidenceIDs:  []int64{3001, 3002},
		Attempts:     1,
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("application Classify: %v", err)
	}
	if application.Classification != exploreapp.FailureClassificationApplication ||
		application.Action != exploreapp.FailureActionHuman || application.Retryable {
		t.Fatalf("application attribution = %+v", application)
	}
	if application.Problem == nil {
		t.Fatal("application failure must produce a problem report draft")
	}
	if application.Problem.Title == "" || application.Problem.Summary == "" ||
		len(application.Problem.DedupeKey) != 64 || len(application.Problem.EvidenceIDs) != 2 {
		t.Fatalf("problem draft = %+v", application.Problem)
	}
	again, err := attribution.Classify(context.Background(), 1001, exploreapp.FailureSignal{
		CaseRunID:    9001,
		CaseTitle:    "提交订单",
		FailureClass: "failed",
		Category:     "ui",
		Reason:       "expected button text 提交订单 got 提交",
		EvidenceIDs:  []int64{3001, 3002},
		Attempts:     1,
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("application Classify again: %v", err)
	}
	if again.Problem == nil || again.Problem.DedupeKey != application.Problem.DedupeKey {
		t.Fatalf("dedupe key is not deterministic: %+v vs %+v", application.Problem, again.Problem)
	}

	environment, err := attribution.Classify(context.Background(), 1001, exploreapp.FailureSignal{
		CaseRunID:    9002,
		FailureClass: "blocked",
		Reason:       "net::ERR_CONNECTION_TIMED_OUT while opening http://sut.test",
		Attempts:     1,
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("environment Classify: %v", err)
	}
	if environment.Classification != exploreapp.FailureClassificationEnvironment ||
		environment.Action != exploreapp.FailureActionRetry || !environment.Retryable {
		t.Fatalf("environment attribution = %+v", environment)
	}
	if environment.Problem != nil {
		t.Fatalf("environment failure must not create a problem draft: %+v", environment.Problem)
	}

	exhausted, err := attribution.Classify(context.Background(), 1001, exploreapp.FailureSignal{
		CaseRunID:    9003,
		FailureClass: "blocked",
		Reason:       "net::ERR_CONNECTION_TIMED_OUT",
		Attempts:     3,
		MaxAttempts:  3,
	})
	if err != nil {
		t.Fatalf("exhausted Classify: %v", err)
	}
	if exhausted.Action != exploreapp.FailureActionHuman || exhausted.Retryable {
		t.Fatalf("exhausted environment attribution = %+v", exhausted)
	}

	automation, err := attribution.Classify(context.Background(), 1001, exploreapp.FailureSignal{
		CaseRunID:    9004,
		FailureClass: "failed",
		Reason:       "target_not_found: selector #submit",
	})
	if err != nil {
		t.Fatalf("automation Classify: %v", err)
	}
	if automation.Classification != exploreapp.FailureClassificationAutomation ||
		automation.Action != exploreapp.FailureActionHuman || automation.Retryable {
		t.Fatalf("automation attribution = %+v", automation)
	}
	if automation.Problem != nil {
		t.Fatalf("automation failure must not create a problem draft: %+v", automation.Problem)
	}

	data, err := attribution.Classify(context.Background(), 1001, exploreapp.FailureSignal{
		CaseRunID:    9005,
		FailureClass: "blocked",
		Reason:       "account_secret_unavailable: no bound test account",
	})
	if err != nil {
		t.Fatalf("data Classify: %v", err)
	}
	if data.Classification != exploreapp.FailureClassificationData || data.Action != exploreapp.FailureActionHuman {
		t.Fatalf("data attribution = %+v", data)
	}

	if _, err := attribution.Classify(context.Background(), 0, exploreapp.FailureSignal{}); err == nil {
		t.Fatal("Classify must reject a non-positive version id")
	}
}

/* ------------------------ runtime consumption (R93) ---------------------- */

func TestExploreRuntimeConsumed(t *testing.T) {
	reader := newFactReader(
		pageFact(31, "http://sut.test/a", "/a", "http://sut.test/a"),
		pageFact(32, "http://sut.test/b", "/b", "http://sut.test/b"),
	)
	facts := &fakeFacts{}
	runtime := &fakeRuntime{}
	closer := &fakePageCloser{}
	analysis := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{
		VersionSUTID:    7,
		EnvironmentID:   8,
		Role:            "admin",
		LoginURL:        "http://sut.test/login",
		MaxProbes:       2,
		MaxRequirements: 10,
	}, exploreapp.GapAnalysisDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{thinRequirement()}},
		Facts:        facts,
		FactReader:   reader,
		Runtime:      runtime,
		Pages:        closer,
	})

	result, err := analysis.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("GapAnalysis.Run: %v", err)
	}
	if runtime.loginCount() != 1 {
		t.Fatalf("runtime login calls = %d, want exactly 1", runtime.loginCount())
	}
	sequence := runtime.sequence()
	if len(sequence) == 0 || sequence[0] != "login" {
		t.Fatalf("login must run before opening pages, got %v", sequence)
	}
	opens := runtime.opens()
	if len(opens) != 2 {
		t.Fatalf("runtime opens = %v, want 2 probed pages", opens)
	}
	if runtime.maxConcurrent() != 1 {
		t.Fatalf("runtime opened %d pages concurrently, want serialized actions", runtime.maxConcurrent())
	}
	closed := closer.closedIDs()
	if len(closed) != 2 || closed[0] != "page_a" || closed[1] != "page_b" {
		t.Fatalf("page-scoped close ids = %v, want [page_a page_b]", closed)
	}
	if len(result.UnclosedPageIDs) != 0 {
		t.Fatalf("unclosed pages = %v", result.UnclosedPageIDs)
	}
	if result.Probes != 2 || result.LoginStatus != exploreapp.SessionActive {
		t.Fatalf("result = %+v", result)
	}
	for _, fact := range facts.recorded() {
		if fact.Kind != exploreapp.KindGap {
			t.Fatalf("unexpected fact kind %q", fact.Kind)
		}
	}

	// A blocked login stops the flow before any page is opened: every explore
	// action needs an authenticated session.
	blockedRuntime := &fakeRuntime{blocked: true}
	blockedFacts := &fakeFacts{}
	blockedCloser := &fakePageCloser{}
	blocked := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{
		VersionSUTID: 7, Role: "admin", LoginURL: "http://sut.test/login", MaxProbes: 2,
	}, exploreapp.GapAnalysisDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{thinRequirement()}},
		Facts:        blockedFacts,
		FactReader:   reader,
		Runtime:      blockedRuntime,
		Pages:        blockedCloser,
	})
	blockedResult, err := blocked.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("blocked GapAnalysis.Run: %v", err)
	}
	if blockedResult.LoginStatus != exploreapp.SessionBlocked || blockedResult.LoginReason != "invalid_credentials" {
		t.Fatalf("blocked login result = %+v", blockedResult)
	}
	if len(blockedRuntime.opens()) != 0 {
		t.Fatalf("blocked login must not open pages: %v", blockedRuntime.opens())
	}
	if len(blockedCloser.closedIDs()) != 0 {
		t.Fatalf("blocked login must not close pages: %v", blockedCloser.closedIDs())
	}

	// An unreachable probed page becomes a dependency gap, and a page that could
	// not be opened is never closed.
	failingRuntime := &fakeRuntime{openErr: errors.New("executor action failed: page_not_found")}
	failingFacts := &fakeFacts{}
	failingCloser := &fakePageCloser{}
	failing := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{
		VersionSUTID: 7, Role: "admin", LoginURL: "http://sut.test/login", MaxProbes: 2,
	}, exploreapp.GapAnalysisDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{thinRequirement()}},
		Facts:        failingFacts,
		FactReader:   reader,
		Runtime:      failingRuntime,
		Pages:        failingCloser,
	})
	failingResult, err := failing.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("failing GapAnalysis.Run: %v", err)
	}
	dependencyGaps := 0
	for _, gap := range failingResult.Gaps {
		if gap.Dimension == exploreapp.GapDimensionDependency && gap.RequirementID == 0 && strings.Contains(gap.Reason, "page_not_found") {
			dependencyGaps++
		}
	}
	if dependencyGaps != 2 {
		t.Fatalf("dependency gaps = %d, want 2: %+v", dependencyGaps, failingResult.Gaps)
	}
	if len(failingCloser.closedIDs()) != 0 {
		t.Fatalf("failed opens must not be closed: %v", failingCloser.closedIDs())
	}

	// Without a runtime the static analysis still runs (no panic, no page).
	static := exploreapp.NewGapAnalysis(exploreapp.GapAnalysisConfig{}, exploreapp.GapAnalysisDeps{
		Requirements: &fakeRequirementSource{requirements: []exploreapp.RequirementSnapshot{thinRequirement()}},
		Facts:        &fakeFacts{},
		FactReader:   reader,
	})
	staticResult, err := static.Run(context.Background(), 1001)
	if err != nil {
		t.Fatalf("static GapAnalysis.Run: %v", err)
	}
	if staticResult.Probes != 0 || staticResult.LoginStatus != "" {
		t.Fatalf("static result = %+v", staticResult)
	}
}
