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

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
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
	resources    versionapp.HandoffResources
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

func (f *fakeHandoffReader) Resources(_ context.Context, _ int64) (versionapp.HandoffResources, error) {
	if f.readErr != nil {
		return versionapp.HandoffResources{}, f.readErr
	}
	return f.resources, nil
}

var _ versionapp.HandoffReader = (*fakeHandoffReader)(nil)

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
				Title:              "退款到账时效",
				Description:        "用户申请退款后，款项应在约定时效内到账。",
				AcceptanceCriteria: "退款成功后 T+1 到账；到账后订单状态为已退款。",
				Priority:           "P1",
				Status:             "clarified",
				SourceQuote:        "退款到账时间按 T+1 验收",
				ConfirmedAt:        &confirmedAt,
			},
			{
				ID: 102, CurrentVersionID: 202, Revision: 1,
				Title:              "退款失败重试",
				Description:        "退款失败时允许用户重试。",
				AcceptanceCriteria: "重试不产生重复退款。",
				Priority:           "P2",
				Status:             "draft",
				SourceQuote:        "退款失败可重试",
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
				Status:          "answered",
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
				Status:  "pending",
				AskedAt: testTime.Add(time.Minute),
			},
		},
		facts: []versionapp.HandoffFact{
			{
				ID: 601, Kind: "decision", Key: "refund.sla",
				Summary: "退款 SLA 采用 T+1", Confidence: &confidence,
				Source: "clarification_host", SourceRef: "requirement:101", CreatedAt: testTime,
			},
			{
				ID: 602, Kind: "constraint", Key: "refund.region",
				Summary: "仅中国大陆订单支持退款", Confidence: &confidence,
				Source: "clarification_host", CreatedAt: testTime,
			},
			{
				ID: 603, Kind: "gap", Key: "refund.failure",
				Summary: "退款失败场景尚未覆盖", Source: "explorer", CreatedAt: testTime,
			},
		},
		plan: versionapp.HandoffPlan{
			Stage:   "designing",
			Current: handoffPlanItem(701, "designing", "输出退款用例设计", "in_progress"),
			Next:    handoffPlanItem(702, "designing", "评审退款用例", "pending"),
		},
		resources: versionapp.HandoffResources{
			SUTs: []versionapp.HandoffSUT{
				{VersionSUTID: 801, SUTID: 901, Name: "订单中心", EnvironmentID: 1001, Environment: "staging", BaseURL: "https://orders.example.test"},
			},
			Pages: []versionapp.HandoffPage{
				{ID: 1101, VersionSUTID: 801, Name: "退款页", URL: "https://orders.example.test/refund", RouteTemplate: "/refund"},
			},
			APIs: []versionapp.HandoffAPI{
				{ID: 1201, VersionSUTID: 801, Name: "创建退款", Method: "POST", Path: "/api/refunds"},
			},
			Accounts: []versionapp.HandoffAccount{
				{ID: 1301, Username: "qa-refund", Roles: []string{"admin"}, Role: "primary"},
			},
			Databases: []versionapp.HandoffDatabase{
				{ID: 1401, Name: "orders", Kind: "postgres", ReadOnly: true},
			},
			Repositories: []versionapp.HandoffRepository{
				{ID: 1501, Name: "order-backend", Role: "backend", Branch: "main", Source: "sut"},
			},
		},
	}
	store := &fakeHandoffStore{}
	publisher := &fakePublisher{}
	service := versionapp.NewHandoffService(store, reader, &seqIDs{}, publisher)
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
	if document.Requirements[0].AcceptanceCriteria == "" || document.Requirements[0].SourceQuote == "" ||
		document.Requirements[0].Priority != "P1" {
		t.Fatalf("confirmed requirement is missing acceptance/priority/source_quote: %+v", document.Requirements[0])
	}
	if len(document.QA) != 2 {
		t.Fatalf("qa = %d, want 2", len(document.QA))
	}
	if document.QA[0].AnswerSelected[0] != "a" || document.QA[1].Status != "pending" {
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
	if len(resources.SUTs) == 0 || len(resources.Pages) == 0 || len(resources.APIs) == 0 ||
		len(resources.Accounts) == 0 || len(resources.Databases) == 0 || len(resources.Repositories) == 0 {
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

// TestHandoffBoundsOversizedContent pins the text/JSON safety contract: each
// free-text field is bounded and each block is capped before persistence.
func TestHandoffBoundsOversizedContent(t *testing.T) {
	service, _, reader, _ := newHandoffTest(t)
	reader.requirements[0].Title = strings.Repeat("标", 5000)
	reader.requirements[0].Description = strings.Repeat("述", 50000)
	reader.requirements[0].AcceptanceCriteria = strings.Repeat("验", 50000)
	reader.requirements[0].SourceQuote = strings.Repeat("源", 5000)
	reader.qa[0].Question = strings.Repeat("问", 9000)
	reader.qa[0].AnswerText = strings.Repeat("答", 9000)
	reader.facts[0].Summary = strings.Repeat("决", 9000)
	for index := 0; index < 300; index++ {
		reader.resources.Pages = append(reader.resources.Pages, versionapp.HandoffPage{
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
	if runes := len([]rune(document.Requirements[0].Description)); runes > versionapp.HandoffDescriptionMax {
		t.Fatalf("requirement description runes = %d", runes)
	}
	if runes := len([]rune(document.Requirements[0].AcceptanceCriteria)); runes > versionapp.HandoffAcceptanceMax {
		t.Fatalf("requirement acceptance runes = %d", runes)
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
	if len(document.Resources.Pages) > versionapp.HandoffMaxResources {
		t.Fatalf("pages = %d, want <= %d", len(document.Resources.Pages), versionapp.HandoffMaxResources)
	}
	if runes := len([]rune(document.Resources.Pages[0].Name)); runes > versionapp.HandoffResourceNameMax {
		t.Fatalf("page name runes = %d", runes)
	}
	if runes := len([]rune(document.Resources.Pages[0].URL)); runes > versionapp.HandoffResourceURLMax {
		t.Fatalf("page URL runes = %d", runes)
	}
}
