package versionapp_test

// Task 27 (v0.22 B-5/handoff): the clarification -> design handoff pack.
//
// These tests pin the application contract of the six-block handoff artifact
// (spec R-B5-1/R-B5-2): generation from injected read ports, revision
// incrementing with an idempotent no-op for identical content, the
// handoff.updated event, the bounded/redacted content, and the latest/revision
// read used by GET /api/versions/{id}/handoff.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- fakes ----------

// fakeHandoffStore is an in-memory versionapp.HandoffStore. It mirrors the SQL
// adapter's unique (version_id, revision) contract and keeps the stored document
// so the tests can assert the persisted JSON.
type fakeHandoffStore struct {
	rows      []versionapp.HandoffRecord
	nextID    int64
	createErr error
}

func (f *fakeHandoffStore) Latest(_ context.Context, versionID int64, revision int) (versionapp.HandoffRecord, error) {
	var found *versionapp.HandoffRecord
	for index := range f.rows {
		row := f.rows[index]
		if row.Handoff.VersionID != versionID {
			continue
		}
		if revision > 0 && row.Handoff.Revision != revision {
			continue
		}
		if found == nil || row.Handoff.Revision > found.Handoff.Revision {
			candidate := row
			found = &candidate
		}
	}
	if found == nil {
		return versionapp.HandoffRecord{}, versionapp.ErrHandoffNotFound
	}
	return *found, nil
}

func (f *fakeHandoffStore) Create(_ context.Context, record versionapp.HandoffRecord) (versionapp.HandoffRecord, error) {
	if f.createErr != nil {
		return versionapp.HandoffRecord{}, f.createErr
	}
	for _, row := range f.rows {
		if row.Handoff.VersionID == record.Handoff.VersionID && row.Handoff.Revision == record.Handoff.Revision {
			return versionapp.HandoffRecord{}, versionapp.ErrHandoffRevisionConflict
		}
	}
	f.nextID++
	record.ID = f.nextID
	if record.CreatedAt.IsZero() {
		record.CreatedAt = testTime
	}
	record.UpdatedAt = record.CreatedAt
	f.rows = append(f.rows, record)
	return record, nil
}

var _ versionapp.HandoffStore = (*fakeHandoffStore)(nil)

// fakeHandoffReader is the injected six-block read port. A real assembly maps
// the version context's requirement/fact/plan/resource/message read models onto
// it; the test seeds one representative row per block.
type fakeHandoffReader struct {
	versionExists bool
	existsErr     error
	readErr       error

	requirements []versionapp.HandoffRequirement
	qa           []versionapp.HandoffQAItem
	facts        []versionapp.HandoffFact
	plan         versionapp.HandoffPlan
	// scope is the VERSION-OWNED input to the resources block; resources is the
	// injected RESOURCE port fake (H-B1: the catalog is read through the port,
	// not through this reader).
	scope     versionapp.HandoffResourceScope
	resources *fakeHandoffResourceReader
}

func (f *fakeHandoffReader) VersionExists(_ context.Context, _ int64) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.versionExists, nil
}

func (f *fakeHandoffReader) Requirements(_ context.Context, _ int64) ([]versionapp.HandoffRequirement, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.requirements, nil
}

func (f *fakeHandoffReader) QA(_ context.Context, _ int64) ([]versionapp.HandoffQAItem, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.qa, nil
}

func (f *fakeHandoffReader) Facts(_ context.Context, _ int64) ([]versionapp.HandoffFact, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.facts, nil
}

func (f *fakeHandoffReader) Plan(_ context.Context, _ int64) (versionapp.HandoffPlan, error) {
	if f.readErr != nil {
		return versionapp.HandoffPlan{}, f.readErr
	}
	return f.plan, nil
}

func (f *fakeHandoffReader) ResourceScope(_ context.Context, _ int64) (versionapp.HandoffResourceScope, error) {
	if f.readErr != nil {
		return versionapp.HandoffResourceScope{}, f.readErr
	}
	return f.scope, nil
}

var _ versionapp.HandoffReader = (*fakeHandoffReader)(nil)

// fakeHandoffResourceReader is the injected resource port (H-B1). It records the
// environment id and the account ids it was asked for, so a test can prove the
// assembly really goes through the port with the version-owned scope.
type fakeHandoffResourceReader struct {
	bundle   resourceapp.HandoffResourceBundle
	accounts []resourceapp.HandoffAccount
	err      error

	environmentCalls []int64
	accountCalls     [][]int64
}

func (f *fakeHandoffResourceReader) HandoffResources(_ context.Context, environmentID int64) (resourceapp.HandoffResourceBundle, error) {
	f.environmentCalls = append(f.environmentCalls, environmentID)
	if f.err != nil {
		return resourceapp.HandoffResourceBundle{}, f.err
	}
	return f.bundle, nil
}

func (f *fakeHandoffResourceReader) HandoffAccounts(_ context.Context, accountIDs []int64) ([]resourceapp.HandoffAccount, error) {
	f.accountCalls = append(f.accountCalls, accountIDs)
	if f.err != nil {
		return nil, f.err
	}
	return f.accounts, nil
}

var _ resourceapp.HandoffResourceReader = (*fakeHandoffResourceReader)(nil)

// ---------- fixture ----------

func handoffPlanItem(id int64, phase, title, status string) *versionapp.HandoffPlanItem {
	return &versionapp.HandoffPlanItem{
		ID: id, Phase: phase, Title: title, Status: status,
	}
}

func newHandoffTest(t *testing.T) (
	*versionapp.HandoffService, *fakeHandoffStore, *fakeHandoffReader, *fakePublisher,
) {
	t.Helper()
	confidence := 0.9
	confirmedAt := testTime
	reader := &fakeHandoffReader{
		versionExists: true,
		requirements: []versionapp.HandoffRequirement{
			{
				ID: 101, CurrentVersionID: 201, Revision: 2,
				Title: "退款到账时效",
				UserStory: versionapp.HandoffUserStory{
					AsA: "买家", IWant: "申请退款后在约定时效内收到款项", SoThat: "我能确认退款已完成",
				},
				Rules: "退款后约定时效到账。",
				AcceptanceCriteria: []versionapp.HandoffAcceptanceCriterion{
					{ID: "AC-1", Given: "买家已申请退款", When: "退款成功", Then: "T+1 到账且订单状态为已退款"},
				},
				Priority:         "P1",
				SutDescription:   "订单中心的退款流程",
				ScopeOut:         "不覆盖跨境退款",
				AccountRolesHint: []string{"admin"},
				Status:           string(version.RequirementClarified),
				SourceQuote:      "退款到账时间按 T+1 验收",
				ConfirmedAt:      &confirmedAt,
			},
			{
				ID: 102, CurrentVersionID: 202, Revision: 1,
				Title: "退款失败重试",
				UserStory: versionapp.HandoffUserStory{
					AsA: "买家", IWant: "退款失败后重试", SoThat: "我不必重新发起退款",
				},
				Rules: "退款失败允许重试。",
				AcceptanceCriteria: []versionapp.HandoffAcceptanceCriterion{
					{ID: "AC-1", Given: "退款失败", When: "买家重试", Then: "不产生重复退款"},
				},
				Priority:       "P2",
				SutDescription: "订单中心的退款重试",
				Status:         string(version.RequirementDraft),
				SourceQuote:    "退款失败可重试",
			},
		},
		qa: []versionapp.HandoffQAItem{
			{
				RequestID: 501, MessageID: 9001, ConversationID: 301,
				QuestionKey: "q1", Question: "退款到账时间按 T+1 还是 T+3 验收？",
				Options: []versionapp.HandoffQAOption{
					{ID: "a", Label: "T+1"},
					{ID: "b", Label: "T+3"},
				},
				SourceRefs:      []string{"requirement:101", "attachment:7"},
				Status:          string(execution.HumanAnswered),
				AnswerSelected:  []string{"a"},
				AskedAt:         testTime,
				AnsweredAt:      &confirmedAt,
				RequirementID:   101,
				RequirementRefs: []int64{101},
			},
			{
				RequestID: 502, MessageID: 9002, ConversationID: 301,
				QuestionKey: "q1", Question: "是否需要覆盖退款失败场景？",
				Options: []versionapp.HandoffQAOption{{ID: "yes", Label: "需要"}, {ID: "no", Label: "不需要"}},
				Status:  string(execution.HumanPending),
				AskedAt: testTime.Add(time.Minute),
			},
		},
		facts: []versionapp.HandoffFact{
			{
				ID: 601, Kind: version.FactKindDecision, Key: "refund.sla",
				Summary: "退款 SLA 采用 T+1", Confidence: &confidence,
				Source: "clarification_host", SourceRef: "requirement:101", CreatedAt: testTime,
			},
			{
				ID: 602, Kind: version.FactKindConstraint, Key: "refund.region",
				Summary: "仅中国大陆订单支持退款", Confidence: &confidence,
				Source: "clarification_host", CreatedAt: testTime,
			},
			{
				ID: 603, Kind: version.FactKindGap, Key: "refund.failure",
				Summary: "退款失败场景尚未覆盖", Source: "p9", CreatedAt: testTime,
			},
		},
		plan: versionapp.HandoffPlan{
			Stage:   "designing",
			Current: handoffPlanItem(701, "designing", "输出退款用例设计", "in_progress"),
			Next:    handoffPlanItem(702, "designing", "评审退款用例", "pending"),
		},
		scope: versionapp.HandoffResourceScope{EnvironmentID: 1001, AccountIDs: []int64{1301}},
		resources: &fakeHandoffResourceReader{
			bundle: resourceapp.HandoffResourceBundle{
				Environment: resourceapp.HandoffEnvironment{ID: 1001, Name: "staging", Status: resource.StatusActive},
				SUTs: []resourceapp.HandoffSUT{
					{
						ID: 901, Name: "订单中心", BaseURL: "https://orders.example.test",
						Pages: []resourceapp.HandoffPage{
							{ID: 1101, Name: "退款页", URL: "https://orders.example.test/refund", RouteTemplate: "/refund"},
						},
						APIs: []resourceapp.HandoffAPI{
							{ID: 1201, Name: "创建退款", Method: "POST", Path: "/api/refunds"},
						},
						Databases: []resourceapp.HandoffDatabase{
							{ID: 1401, SUTID: 901, Name: "orders", Kind: "postgres", ReadOnly: true},
						},
					},
				},
				Repositories: []resourceapp.HandoffRepo{
					{ID: 1501, Name: "order-backend", Role: "backend", Branch: "main"},
				},
			},
			accounts: []resourceapp.HandoffAccount{
				{ID: 1301, Username: "qa-refund", Roles: []string{"admin"}},
			},
		},
	}
	store := &fakeHandoffStore{}
	publisher := &fakePublisher{}
	service := versionapp.NewHandoffService(store, reader, reader.resources, &seqIDs{}, publisher)
	return service, store, reader, publisher
}

func handoffSectionKeys(t *testing.T, document versionapp.Handoff) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal handoff: %v", err)
	}
	return object
}

func hasPendingOpenQuestion(items []versionapp.HandoffOpenItem) bool {
	for _, item := range items {
		if item.Kind == "question" && item.ReferenceID == 502 {
			return true
		}
	}
	return false
}

// TestHandoffGeneratedOnDesigning pins R-B5-1/R-B5-2: generation on the
// requirement_review -> designing transition produces the six blocks from the
// clarification state, assigns revision 1, persists the JSON document and
// publishes handoff.updated on version:{id}.
func TestHandoffGeneratedOnDesigning(t *testing.T) {
	service, store, _, publisher := newHandoffTest(t)
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if document.VersionID != 7 || document.Revision != 1 {
		t.Fatalf("handoff = version %d revision %d, want 7/1", document.VersionID, document.Revision)
	}
	if document.GeneratedAt.IsZero() {
		t.Fatal("generated_at is zero")
	}

	// All six blocks are present and non-empty from the seeded read ports.
	sections := handoffSectionKeys(t, document)
	for _, name := range []string{"requirements", "qa", "decisions", "open", "plan", "resources"} {
		if _, ok := sections[name]; !ok {
			t.Fatalf("handoff JSON is missing the %q block: %s", name, mustJSON(t, document))
		}
	}
	if len(document.Requirements) != 2 {
		t.Fatalf("requirements = %d, want 2", len(document.Requirements))
	}
	if len(document.Requirements[0].AcceptanceCriteria) == 0 || document.Requirements[0].SourceQuote == "" ||
		document.Requirements[0].Priority != "P1" {
		t.Fatalf("confirmed requirement is missing acceptance/priority/source_quote: %+v", document.Requirements[0])
	}
	if len(document.QA) != 2 {
		t.Fatalf("qa = %d, want 2", len(document.QA))
	}
	if document.QA[0].AnswerSelected[0] != "a" || document.QA[1].Status != string(execution.HumanPending) {
		t.Fatalf("qa answers/status = %+v", document.QA)
	}
	if len(document.Decisions) != 2 {
		t.Fatalf("decisions = %d, want 2 (decision+constraint)", len(document.Decisions))
	}
	if document.Decisions[0].Confidence == nil || *document.Decisions[0].Confidence != 0.9 {
		t.Fatalf("decision confidence = %+v, want 0.9", document.Decisions[0].Confidence)
	}
	if !hasPendingOpenQuestion(document.Open) {
		t.Fatalf("open block does not include the pending question: %+v", document.Open)
	}
	if document.Plan.Stage != "designing" || document.Plan.Current == nil || document.Plan.Next == nil {
		t.Fatalf("plan block = %+v, want stage/current/next", document.Plan)
	}
	resources := document.Resources
	if resources.Environment.ID == 0 || len(resources.SUTs) == 0 ||
		len(resources.SUTs[0].Pages) == 0 || len(resources.SUTs[0].APIs) == 0 || len(resources.SUTs[0].Databases) == 0 ||
		len(resources.Accounts) == 0 || len(resources.Repositories) == 0 {
		t.Fatalf("resources block is missing a summary: %+v", resources)
	}

	// The JSON document persists every block plus revision/generated_at.
	persisted := string(mustJSON(t, store.rows[0].Handoff))
	for _, key := range []string{`"version_id"`, `"revision"`, `"generated_at"`, `"requirements"`, `"qa"`, `"decisions"`, `"open"`, `"plan"`, `"resources"`} {
		if !strings.Contains(persisted, key) {
			t.Fatalf("persisted handoff JSON missing %s: %s", key, persisted)
		}
	}
	if strings.Contains(persisted, "password") || strings.Contains(persisted, "dsn") {
		t.Fatalf("persisted handoff JSON leaked a secret field: %s", persisted)
	}

	if len(publisher.events) != 1 {
		t.Fatalf("events = %d, want 1 handoff.updated", len(publisher.events))
	}
	event := publisher.events[0]
	if event.Type != "handoff.updated" || event.Channel != "version:7" {
		t.Fatalf("event = %+v, want handoff.updated on version:7", event)
	}
	if event.Payload["revision"] != 1 || event.Payload["version_id"] != "7" {
		t.Fatalf("event payload = %+v", event.Payload)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestHandoffRegenerateIdempotent pins the idempotency contract: generating the
// same content twice keeps one revision, one stored row and one event.
func TestHandoffRegenerateIdempotent(t *testing.T) {
	service, store, _, publisher := newHandoffTest(t)
	first, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate first: %v", err)
	}
	second, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate second: %v", err)
	}
	if first.Revision != 1 || second.Revision != 1 {
		t.Fatalf("revisions = %d/%d, want 1/1", first.Revision, second.Revision)
	}
	if len(store.rows) != 1 {
		t.Fatalf("stored rows = %d, want 1 (no revision for identical content)", len(store.rows))
	}
	if len(publisher.events) != 1 {
		t.Fatalf("events = %d, want 1 (no handoff.updated for unchanged content)", len(publisher.events))
	}
}

// TestHandoffRegenerateIncrementsRevision pins the refresh path: changed
// clarification state writes the next revision and publishes a second event.
func TestHandoffRegenerateIncrementsRevision(t *testing.T) {
	service, store, reader, publisher := newHandoffTest(t)
	if _, err := service.Generate(context.Background(), 7); err != nil {
		t.Fatalf("Generate first: %v", err)
	}

	reader.requirements[0].Title = "退款到账时效（已修订）"
	second, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate changed: %v", err)
	}
	if second.Revision != 2 {
		t.Fatalf("revision = %d, want 2", second.Revision)
	}
	if len(store.rows) != 2 {
		t.Fatalf("stored rows = %d, want 2", len(store.rows))
	}
	if len(publisher.events) != 2 {
		t.Fatalf("events = %d, want 2", len(publisher.events))
	}

	latest, err := service.Latest(context.Background(), 7, 0)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Revision != 2 || latest.Requirements[0].Title != "退款到账时效（已修订）" {
		t.Fatalf("latest = revision %d title %q", latest.Revision, latest.Requirements[0].Title)
	}
	firstRevision, err := service.Latest(context.Background(), 7, 1)
	if err != nil {
		t.Fatalf("Latest revision 1: %v", err)
	}
	if firstRevision.Revision != 1 || firstRevision.Requirements[0].Title != "退款到账时效" {
		t.Fatalf("revision 1 = %+v", firstRevision)
	}
}

// TestHandoffLatestMissing pins the 404 contract of GET /handoff when no
// revision has been generated for the version.
func TestHandoffLatestMissing(t *testing.T) {
	service, _, _, _ := newHandoffTest(t)
	_, err := service.Latest(context.Background(), 7, 0)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("Latest on empty store = %v, want a 404 platform error", err)
	}
}

// TestHandoffGenerateRejectsMissingVersion pins that generation never creates an
// orphan handoff for an unknown version.
func TestHandoffGenerateRejectsMissingVersion(t *testing.T) {
	service, store, reader, _ := newHandoffTest(t)
	reader.versionExists = false
	_, err := service.Generate(context.Background(), 7)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("Generate unknown version = %v, want 404", err)
	}
	if len(store.rows) != 0 {
		t.Fatalf("stored rows = %d, want 0", len(store.rows))
	}
}

// TestHandoffOpenKeepsAllPendingQuestionsOfBatch pins the fix-round contract:
// one human_request batch may carry 1-5 questions (B §6.1), so every pending (or
// expired) question must survive as its own open item. The old identity
// (kind + "human_request:<request id>") collapsed a batch to one item.
func TestHandoffOpenKeepsAllPendingQuestionsOfBatch(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	reader.qa = []versionapp.HandoffQAItem{
		{
			RequestID: 601, MessageID: 9601, ConversationID: 301,
			QuestionKey: "q1", Question: "第一个问题", Status: string(execution.HumanPending),
			AskedAt: testTime,
		},
		{
			RequestID: 601, MessageID: 9601, ConversationID: 301,
			QuestionKey: "q2", Question: "第二个问题", Status: string(execution.HumanPending),
			AskedAt: testTime.Add(time.Minute),
		},
		{
			RequestID: 601, MessageID: 9601, ConversationID: 301,
			QuestionKey: "q3", Question: "第三个问题", Status: string(execution.HumanExpired),
			AskedAt: testTime.Add(2 * time.Minute),
		},
		{
			RequestID: 602, MessageID: 9602, ConversationID: 301,
			QuestionKey: "q1", Question: "已回答的问题", Status: string(execution.HumanAnswered),
			AskedAt: testTime,
		},
	}
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	questions := make([]versionapp.HandoffOpenItem, 0, 3)
	for _, item := range document.Open {
		if item.Kind == "question" {
			questions = append(questions, item)
		}
	}
	if len(questions) != 3 {
		t.Fatalf("open questions = %d (%+v), want all 3 questions of request 601", len(questions), questions)
	}
	expectedKeys := []string{"q1", "q2", "q3"}
	expectedSources := []string{"human_request:601:q1", "human_request:601:q2", "human_request:601:q3"}
	for index, question := range questions {
		if question.ReferenceID != 601 {
			t.Fatalf("open question %d reference = %d, want 601", index, question.ReferenceID)
		}
		if question.Source != expectedSources[index] {
			t.Fatalf("open question %d source = %q, want %q", index, question.Source, expectedSources[index])
		}
		raw, err := json.Marshal(question)
		if err != nil {
			t.Fatalf("marshal open question: %v", err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatalf("unmarshal open question: %v", err)
		}
		if object["question_key"] != expectedKeys[index] {
			t.Fatalf("open question %d JSON = %s, want question_key %q", index, raw, expectedKeys[index])
		}
	}
	if questions[0].Status != string(execution.HumanPending) || questions[2].Status != string(execution.HumanExpired) {
		t.Fatalf("open question statuses = %q/%q, want pending/expired", questions[0].Status, questions[2].Status)
	}
}

// TestHandoffRequirementsExcludeClosed pins B §8.1: the requirements block holds
// confirmed and draft requirements only; closed requirement rows are historical
// and must not enter the resolution pack.
func TestHandoffRequirementsExcludeClosed(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	reader.requirements = append(reader.requirements, versionapp.HandoffRequirement{
		ID: 103, CurrentVersionID: 203, Revision: 1,
		Title: "已关闭需求", Status: string(version.RequirementClosed),
	})
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(document.Requirements) != 2 {
		t.Fatalf("requirements = %d (%+v), want only the draft+clarified pair", len(document.Requirements), document.Requirements)
	}
	for _, item := range document.Requirements {
		if !versionapp.HandoffRequirementStatusIncluded(item.Status) {
			t.Fatalf("requirements block contains status %q: %+v", item.Status, item)
		}
	}
	for _, item := range document.Open {
		if item.Kind == "requirement" && item.ReferenceID == 103 {
			t.Fatalf("closed requirement leaked into open block: %+v", item)
		}
	}
}

// TestHandoffCapsKeepNewestRequirementsAndFacts pins the cap direction: when a
// block exceeds its bound the newest rows are kept, not the oldest (an old
// requirement/fact must not crowd out the current clarification state).
func TestHandoffCapsKeepNewestRequirementsAndFacts(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	// Isolate the requirement/fact caps: no pending questions compete for the
	// open-block budget.
	reader.qa = nil
	reader.requirements = make([]versionapp.HandoffRequirement, 0, 250)
	for index := 1; index <= 250; index++ {
		reader.requirements = append(reader.requirements, versionapp.HandoffRequirement{
			ID: int64(index), CurrentVersionID: int64(1000 + index), Revision: 1,
			Title: "需求", Status: string(version.RequirementClarified),
		})
	}
	reader.facts = make([]versionapp.HandoffFact, 0, 300)
	for index := 1; index <= 150; index++ {
		reader.facts = append(reader.facts, versionapp.HandoffFact{
			ID: int64(index), Kind: version.FactKindDecision, Key: "decision", Summary: "决策", CreatedAt: testTime,
		})
	}
	for index := 1; index <= 150; index++ {
		reader.facts = append(reader.facts, versionapp.HandoffFact{
			ID: int64(1000 + index), Kind: version.FactKindGap, Key: "gap", Summary: "缺口", CreatedAt: testTime,
		})
	}
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(document.Requirements) != versionapp.HandoffMaxRequirements {
		t.Fatalf("requirements = %d, want the cap %d", len(document.Requirements), versionapp.HandoffMaxRequirements)
	}
	if document.Requirements[0].ID != 51 || document.Requirements[len(document.Requirements)-1].ID != 250 {
		t.Fatalf("requirements keep IDs %d..%d, want the newest 51..250",
			document.Requirements[0].ID, document.Requirements[len(document.Requirements)-1].ID)
	}
	if len(document.Decisions) != versionapp.HandoffMaxDecisions {
		t.Fatalf("decisions = %d, want the cap %d", len(document.Decisions), versionapp.HandoffMaxDecisions)
	}
	if document.Decisions[0].FactID != 51 || document.Decisions[len(document.Decisions)-1].FactID != 150 {
		t.Fatalf("decisions keep IDs %d..%d, want the newest 51..150",
			document.Decisions[0].FactID, document.Decisions[len(document.Decisions)-1].FactID)
	}
	openGaps := make([]versionapp.HandoffOpenItem, 0, 100)
	for _, item := range document.Open {
		if item.Kind == version.FactKindGap {
			openGaps = append(openGaps, item)
		}
	}
	if len(openGaps) != versionapp.HandoffMaxOpen {
		t.Fatalf("open gaps = %d, want the cap %d", len(openGaps), versionapp.HandoffMaxOpen)
	}
	if openGaps[0].ReferenceID != 1051 || openGaps[len(openGaps)-1].ReferenceID != 1150 {
		t.Fatalf("open gaps keep IDs %d..%d, want the newest 1051..1150",
			openGaps[0].ReferenceID, openGaps[len(openGaps)-1].ReferenceID)
	}
}

// TestHandoffRequirementV1Projection pins R-B1: the requirements block is a
// faithful V1 structural projection of the requirement contract (user_story +
// structured acceptance_criteria + sut_description, plus the optional
// rules/scope_out/account_roles_hint), and the resources block is an environment
// summary with no version_sut binding id and no credentials.
func TestHandoffRequirementV1Projection(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := reader.requirements[0]
	got := document.Requirements[0]
	if got.UserStory != want.UserStory {
		t.Fatalf("user_story = %+v, want the projected %+v", got.UserStory, want.UserStory)
	}
	if got.SutDescription != want.SutDescription || got.Rules != want.Rules || got.ScopeOut != want.ScopeOut {
		t.Fatalf("sut_description/rules/scope_out = %+v, want %+v", got, want)
	}
	if len(got.AcceptanceCriteria) != len(want.AcceptanceCriteria) {
		t.Fatalf("acceptance_criteria = %+v, want %+v", got.AcceptanceCriteria, want.AcceptanceCriteria)
	}
	for index, criterion := range got.AcceptanceCriteria {
		if criterion != want.AcceptanceCriteria[index] {
			t.Fatalf("acceptance_criteria[%d] = %+v, want %+v", index, criterion, want.AcceptanceCriteria[index])
		}
	}
	if len(got.AccountRolesHint) != 1 || got.AccountRolesHint[0] != "admin" {
		t.Fatalf("account_roles_hint = %+v, want [admin]", got.AccountRolesHint)
	}

	// The persisted document carries the V1 JSON keys and no version_sut binding
	// id, no flat description, no password and no DSN.
	raw := string(mustJSON(t, document))
	for _, key := range []string{
		`"user_story"`, `"as_a"`, `"i_want"`, `"so_that"`, `"acceptance_criteria"`,
		`"given"`, `"when"`, `"then"`, `"sut_description"`, `"scope_out"`,
		`"account_roles_hint"`, `"environment"`,
	} {
		if !strings.Contains(raw, key) {
			t.Fatalf("handoff JSON missing V1 key %s: %s", key, raw)
		}
	}
	for _, forbidden := range []string{`"version_sut_id"`, `"description"`, "password", "dsn"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("handoff JSON carries forbidden %q: %s", forbidden, raw)
		}
	}
	// The resources block is headed by the environment and nests assets under SUTs.
	if document.Resources.Environment.ID != 1001 || document.Resources.Environment.Name != "staging" {
		t.Fatalf("environment summary = %+v", document.Resources.Environment)
	}
	if len(document.Resources.SUTs) != 1 || document.Resources.SUTs[0].SUTID != 901 {
		t.Fatalf("suts = %+v", document.Resources.SUTs)
	}
}

// TestHandoffResourcesAggregateBudget pins the C4 ruling: HandoffMaxResources is
// ONE shared budget across every SUT and kind (not a per-SUT cap), consumed in
// catalog order, and truncation is recorded explicitly so the Design Host knows
// the set is partial.
func TestHandoffResourcesAggregateBudget(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	// Three SUTs with 120 pages each = 360 pages, over the single 200 budget.
	reader.resources.bundle = resourceapp.HandoffResourceBundle{
		Environment: resourceapp.HandoffEnvironment{ID: 1, Name: "env", Status: resource.StatusActive},
		SUTs: []resourceapp.HandoffSUT{
			{ID: 1, Name: "a"},
			{ID: 2, Name: "b"},
			{ID: 3, Name: "c"},
		},
	}
	for s := range reader.resources.bundle.SUTs {
		for p := 0; p < 120; p++ {
			reader.resources.bundle.SUTs[s].Pages = append(reader.resources.bundle.SUTs[s].Pages, resourceapp.HandoffPage{
				ID: int64(1000*(s+1) + p), Name: "p",
			})
		}
	}
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	total := 0
	for _, sut := range document.Resources.SUTs {
		total += len(sut.Pages) + len(sut.APIs) + len(sut.Databases)
	}
	if total != versionapp.HandoffMaxResources {
		t.Fatalf("total assets = %d, want the shared budget %d (not a per-SUT cap)", total, versionapp.HandoffMaxResources)
	}
	if !document.Resources.Truncated {
		t.Fatal("Truncated = false, want true (the budget dropped rows)")
	}
	// Minor 2: the Host reads the stored JSONB verbatim, so the observable signal
	// is the JSON key, not the Go field. Pin "truncated":true in the marshalled
	// document (and that it cannot be erased on read).
	if raw := string(mustJSON(t, document)); !strings.Contains(raw, `"truncated":true`) {
		t.Fatalf("handoff JSON missing \"truncated\":true (the Host's partial-set signal): %s", raw)
	}
	// Catalog order: SUT 1 keeps all 120, SUT 2 keeps the next 80, SUT 3 keeps 0.
	if len(document.Resources.SUTs[0].Pages) != 120 ||
		len(document.Resources.SUTs[1].Pages) != 80 ||
		len(document.Resources.SUTs[2].Pages) != 0 {
		t.Fatalf("per-SUT pages = %d/%d/%d, want 120/80/0 in catalog order",
			len(document.Resources.SUTs[0].Pages), len(document.Resources.SUTs[1].Pages), len(document.Resources.SUTs[2].Pages))
	}
	// SUT 2 keeps its 80 lowest-id pages (deterministic catalog order).
	for i, page := range document.Resources.SUTs[1].Pages {
		if page.ID != int64(2000+i) {
			t.Fatalf("SUT 2 page[%d] id = %d, want the lowest-id page %d", i, page.ID, 2000+i)
		}
	}

	// The budget is shared across KINDS too: one SUT with 150 pages + 100 APIs
	// keeps 150 pages + 50 APIs = 200, and records truncation.
	reader.resources.bundle = resourceapp.HandoffResourceBundle{
		Environment: resourceapp.HandoffEnvironment{ID: 1, Name: "env", Status: resource.StatusActive},
		SUTs:        []resourceapp.HandoffSUT{{ID: 1, Name: "a"}},
	}
	for p := 0; p < 150; p++ {
		reader.resources.bundle.SUTs[0].Pages = append(reader.resources.bundle.SUTs[0].Pages, resourceapp.HandoffPage{ID: int64(p + 1), Name: "p"})
	}
	for a := 0; a < 100; a++ {
		reader.resources.bundle.SUTs[0].APIs = append(reader.resources.bundle.SUTs[0].APIs, resourceapp.HandoffAPI{ID: int64(a + 1), Name: "a", Method: "GET", Path: "/x"})
	}
	mixed, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate mixed: %v", err)
	}
	if len(mixed.Resources.SUTs[0].Pages) != 150 || len(mixed.Resources.SUTs[0].APIs) != 50 {
		t.Fatalf("mixed kinds = %d pages / %d apis, want 150/50 (pages first, then APIs)",
			len(mixed.Resources.SUTs[0].Pages), len(mixed.Resources.SUTs[0].APIs))
	}
	if !mixed.Resources.Truncated {
		t.Fatal("mixed Truncated = false, want true")
	}

	// Under the budget: nothing dropped, Truncated stays false.
	reader.resources.bundle.SUTs[0].Pages = reader.resources.bundle.SUTs[0].Pages[:50]
	reader.resources.bundle.SUTs[0].APIs = nil
	under, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate under: %v", err)
	}
	if under.Resources.Truncated {
		t.Fatal("Truncated = true under the budget, want false")
	}
}

// conflictOnceHandoffStore scripts one revision race: the first Create reports
// ErrHandoffRevisionConflict after a concurrent generator stored revision 1, so
// the service must refresh the latest revision and retry as revision 2.
type conflictOnceHandoffStore struct {
	fakeHandoffStore
	conflicts int
	injected  bool
}

func (f *conflictOnceHandoffStore) Create(ctx context.Context, record versionapp.HandoffRecord) (versionapp.HandoffRecord, error) {
	if f.conflicts > 0 {
		f.conflicts--
		if !f.injected {
			f.injected = true
			concurrent := record.Handoff
			concurrent.Revision = 1
			// Copy the requirement slice before mutating it: record.Handoff and
			// the service's document share the backing array.
			concurrent.Requirements = append([]versionapp.HandoffRequirement(nil), record.Handoff.Requirements...)
			concurrent.Requirements[0].Title = "并发生成的内容"
			f.rows = append(f.rows, versionapp.HandoffRecord{ID: 9001, Handoff: concurrent})
		}
		return versionapp.HandoffRecord{}, versionapp.ErrHandoffRevisionConflict
	}
	return f.fakeHandoffStore.Create(ctx, record)
}

// TestHandoffRetriesRevisionConflict pins the concurrency contract: a lost
// revision race is re-read and retried against the refreshed latest revision,
// and only the successful write publishes handoff.updated.
func TestHandoffRetriesRevisionConflict(t *testing.T) {
	reader := &fakeHandoffReader{
		versionExists: true,
		requirements: []versionapp.HandoffRequirement{{
			ID: 101, CurrentVersionID: 201, Revision: 1, Title: "并发目标", Status: string(version.RequirementClarified),
		}},
	}
	store := &conflictOnceHandoffStore{conflicts: 1}
	publisher := &fakePublisher{}
	service := versionapp.NewHandoffService(store, reader, &fakeHandoffResourceReader{}, &seqIDs{}, publisher)
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if document.Revision != 2 {
		t.Fatalf("revision = %d, want 2 after the conflict retry", document.Revision)
	}
	if len(store.rows) != 2 {
		t.Fatalf("stored rows = %d, want the concurrent revision 1 plus the retried revision 2", len(store.rows))
	}
	if len(publisher.events) != 1 {
		t.Fatalf("events = %d, want only the successful revision", len(publisher.events))
	}
	if publisher.events[0].Payload["revision"] != 2 {
		t.Fatalf("event payload = %+v, want revision 2", publisher.events[0].Payload)
	}
}

// TestHandoffBoundsOversizedContent pins the text/JSON safety contract: each
// free-text field is bounded and each block is capped before persistence.
func TestHandoffBoundsOversizedContent(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	reader.requirements[0].Title = strings.Repeat("标", 5000)
	reader.requirements[0].UserStory.AsA = strings.Repeat("角", 5000)
	reader.requirements[0].UserStory.IWant = strings.Repeat("望", 5000)
	reader.requirements[0].UserStory.SoThat = strings.Repeat("因", 5000)
	reader.requirements[0].Rules = strings.Repeat("规", 50000)
	reader.requirements[0].SutDescription = strings.Repeat("述", 50000)
	reader.requirements[0].ScopeOut = strings.Repeat("外", 50000)
	reader.requirements[0].AcceptanceCriteria = []versionapp.HandoffAcceptanceCriterion{
		{ID: "AC-1", Given: strings.Repeat("前", 50000), When: strings.Repeat("作", 50000), Then: strings.Repeat("验", 50000)},
	}
	reader.requirements[0].SourceQuote = strings.Repeat("源", 5000)
	reader.qa[0].Question = strings.Repeat("问", 9000)
	reader.qa[0].AnswerText = strings.Repeat("答", 9000)
	reader.facts[0].Summary = strings.Repeat("决", 9000)
	for index := 0; index < 300; index++ {
		reader.resources.bundle.SUTs[0].Pages = append(reader.resources.bundle.SUTs[0].Pages, resourceapp.HandoffPage{
			ID: int64(2000 + index), Name: strings.Repeat("页", 2000),
			URL: "https://example.test/" + strings.Repeat("u", 2000),
		})
	}
	document, err := service.Generate(context.Background(), 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if runes := len([]rune(document.Requirements[0].Title)); runes > versionapp.HandoffTitleMax {
		t.Fatalf("requirement title runes = %d, want <= %d", runes, versionapp.HandoffTitleMax)
	}
	for _, part := range []string{
		document.Requirements[0].UserStory.AsA,
		document.Requirements[0].UserStory.IWant,
		document.Requirements[0].UserStory.SoThat,
	} {
		if runes := len([]rune(part)); runes > versionapp.HandoffUserStoryPartMax {
			t.Fatalf("user story part runes = %d, want <= %d", runes, versionapp.HandoffUserStoryPartMax)
		}
	}
	if runes := len([]rune(document.Requirements[0].Rules)); runes > versionapp.HandoffRulesMax {
		t.Fatalf("requirement rules runes = %d", runes)
	}
	if runes := len([]rune(document.Requirements[0].SutDescription)); runes > versionapp.HandoffSutDescriptionMax {
		t.Fatalf("requirement sut_description runes = %d", runes)
	}
	if runes := len([]rune(document.Requirements[0].ScopeOut)); runes > versionapp.HandoffScopeOutMax {
		t.Fatalf("requirement scope_out runes = %d", runes)
	}
	for _, criterion := range document.Requirements[0].AcceptanceCriteria {
		for _, part := range []string{criterion.Given, criterion.When, criterion.Then} {
			if runes := len([]rune(part)); runes > versionapp.HandoffACPartMax {
				t.Fatalf("acceptance criterion part runes = %d", runes)
			}
		}
	}
	if runes := len([]rune(document.Requirements[0].SourceQuote)); runes > versionapp.HandoffSourceQuoteMax {
		t.Fatalf("requirement source quote runes = %d", runes)
	}
	if runes := len([]rune(document.QA[0].Question)); runes > versionapp.HandoffQuestionMax {
		t.Fatalf("question runes = %d", runes)
	}
	if runes := len([]rune(document.QA[0].AnswerText)); runes > versionapp.HandoffAnswerMax {
		t.Fatalf("answer runes = %d", runes)
	}
	if runes := len([]rune(document.Decisions[0].Summary)); runes > versionapp.HandoffFactSummaryMax {
		t.Fatalf("fact summary runes = %d", runes)
	}
	pages := document.Resources.SUTs[0].Pages
	if len(pages) != versionapp.HandoffMaxResources {
		t.Fatalf("pages = %d, want the shared budget %d", len(pages), versionapp.HandoffMaxResources)
	}
	if !document.Resources.Truncated {
		t.Fatal("Truncated = false, want true (300 pages over the 200 budget)")
	}
	if runes := len([]rune(pages[0].Name)); runes > versionapp.HandoffResourceNameMax {
		t.Fatalf("page name runes = %d", runes)
	}
	if runes := len([]rune(pages[0].URL)); runes > versionapp.HandoffResourceURLMax {
		t.Fatalf("page URL runes = %d", runes)
	}
}
