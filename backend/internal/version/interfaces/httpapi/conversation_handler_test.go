package versionapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

type stubConversationService struct {
	conversation versionapp.ConversationView
	list         []versionapp.ConversationView
	hasMore      bool
	messages     []versionapp.MessageView
	messagesMore bool
	message      versionapp.MessageView
	err          error

	lastVersionID int64
	lastCID       int64
	lastKind      string
	lastTitle     string
	lastLimit     int
	lastBefore    *int64
	lastAfter     int64
	lastClientID  string
	lastParts     []version.MessagePart
	lastActor     int64
	calls         []string
}

func (s *stubConversationService) Create(_ context.Context, versionID int64, kind, title string, actorID int64) (versionapp.ConversationView, error) {
	s.calls = append(s.calls, "create")
	s.lastVersionID = versionID
	s.lastKind = kind
	s.lastTitle = title
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.ConversationView{}, s.err
	}
	return s.conversation, nil
}

func (s *stubConversationService) List(_ context.Context, versionID int64, kind string, limit int, beforeID *int64) ([]versionapp.ConversationView, bool, error) {
	s.calls = append(s.calls, "list")
	s.lastVersionID = versionID
	s.lastKind = kind
	s.lastLimit = limit
	s.lastBefore = beforeID
	if s.err != nil {
		return nil, false, s.err
	}
	return s.list, s.hasMore, nil
}

func (s *stubConversationService) Get(_ context.Context, versionID, conversationID int64) (versionapp.ConversationView, error) {
	s.calls = append(s.calls, "get")
	s.lastVersionID = versionID
	s.lastCID = conversationID
	if s.err != nil {
		return versionapp.ConversationView{}, s.err
	}
	return s.conversation, nil
}

func (s *stubConversationService) Messages(_ context.Context, versionID, conversationID int64, after int64, limit int) ([]versionapp.MessageView, bool, error) {
	s.calls = append(s.calls, "messages")
	s.lastVersionID = versionID
	s.lastCID = conversationID
	s.lastAfter = after
	s.lastLimit = limit
	if s.err != nil {
		return nil, false, s.err
	}
	return s.messages, s.messagesMore, nil
}

func (s *stubConversationService) PostUserMessage(_ context.Context, versionID, conversationID int64, parts []version.MessagePart, clientMessageID string, actorID int64) (versionapp.MessageView, error) {
	s.calls = append(s.calls, "post")
	s.lastVersionID = versionID
	s.lastCID = conversationID
	s.lastParts = parts
	s.lastClientID = clientMessageID
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.MessageView{}, s.err
	}
	return s.message, nil
}

var _ versionapi.ConversationService = (*stubConversationService)(nil)

func newConversationServer(t *testing.T) (http.Handler, *stubConversationService) {
	t.Helper()
	stub := &stubConversationService{}
	handler := versionapi.NewConversationHandler(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}/conversations", handler.List)
	mux.HandleFunc("POST /api/versions/{id}/conversations", handler.Create)
	mux.HandleFunc("GET /api/versions/{id}/conversations/{cid}", handler.Get)
	mux.HandleFunc("GET /api/versions/{id}/conversations/{cid}/messages", handler.Messages)
	mux.HandleFunc("POST /api/versions/{id}/conversations/{cid}/messages", handler.PostMessage)
	return mux, stub
}

func sampleConversationView() versionapp.ConversationView {
	summary := "Latest thread summary"
	lastMessageAt := testTime
	createdBy := int64(42)
	return versionapp.ConversationView{
		ID: 8001, VersionID: 101, Kind: "clarification", Title: "需求澄清 2026-10-31 12:00",
		Status: "active", MessageCount: 3, LastMessageAt: &lastMessageAt,
		CreatedBy: &createdBy, CreatedAt: testTime, UpdatedAt: testTime, Summary: &summary,
	}
}

func sampleMessageView() versionapp.MessageView {
	return versionapp.MessageView{
		ID: 8101, ConversationID: 8001, Role: "assistant",
		Parts: []version.MessagePart{
			{Type: "text", Text: "I drafted this."},
			{Type: "requirement_card", RequirementID: 9001, Title: "Coupons", Status: "draft", Revision: 2},
			{Type: "rollback_proposal", RequirementID: 9001, Reason: "scope changed"},
			{Type: "attachment", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "spec.md", MIME: "text/markdown", Size: 12},
		},
		Refs:      []version.MessageRef{{Kind: "requirement", ID: "9001"}},
		CreatedAt: testTime,
	}
}

func TestCreateConversationReturnsDetailShape(t *testing.T) {
	handler, stub := newConversationServer(t)
	stub.conversation = sampleConversationView()

	recorder := call(t, handler, http.MethodPost, "/api/versions/101/conversations", `{"kind":"clarification","title":"Coupon rules"}`, actor())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastKind != "clarification" || stub.lastTitle != "Coupon rules" || stub.lastActor != 42 {
		t.Fatalf("args = %#v", stub)
	}
	data := dataBody(t, recorder)
	if data["id"] != "8001" || data["title"] != "需求澄清 2026-10-31 12:00" || data["kind"] != "clarification" || data["status"] != "active" {
		t.Fatalf("data = %#v", data)
	}
	if data["message_count"] != float64(3) || data["last_message_at"] != "2026-10-31T12:00:00Z" || data["summary"] != "Latest thread summary" {
		t.Fatalf("data = %#v", data)
	}
	if data["created_at"] != "2026-10-31T12:00:00Z" || data["updated_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("times = %#v", data)
	}
	for _, forbidden := range []string{"version_id", "created_by"} {
		if _, ok := data[forbidden]; ok {
			t.Fatalf("summary exposed %q: %#v", forbidden, data)
		}
	}

	stub.conversation.Summary = nil
	stub.conversation.LastMessageAt = nil
	empty := call(t, handler, http.MethodPost, "/api/versions/101/conversations", `{"kind":"clarification"}`, actor())
	if empty.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", empty.Code, empty.Body.String())
	}
	emptyData := dataBody(t, empty)
	if emptyData["summary"] != nil || emptyData["last_message_at"] != nil {
		t.Fatalf("empty data = %#v", emptyData)
	}
}

func TestConversationListAndGetParseQuery(t *testing.T) {
	handler, stub := newConversationServer(t)
	stub.list = []versionapp.ConversationView{sampleConversationView()}
	stub.hasMore = true

	recorder := call(t, handler, http.MethodGet, "/api/versions/101/conversations?kind=clarification&limit=5&before=77", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastKind != "clarification" || stub.lastLimit != 5 {
		t.Fatalf("args = %#v", stub)
	}
	if stub.lastBefore == nil || *stub.lastBefore != 77 {
		t.Fatalf("before = %#v", stub.lastBefore)
	}
	items := dataList(t, recorder)
	item := items[0].(map[string]any)
	if item["id"] != "8001" || item["message_count"] != float64(3) {
		t.Fatalf("item = %#v", item)
	}
	if _, ok := item["summary"]; ok {
		t.Fatalf("list summary item must not expose summary: %#v", item)
	}
	if payload := decodeBody(t, recorder); payload["next_cursor"] != "8001" || payload["has_more"] != true {
		t.Fatalf("envelope = %#v", payload)
	}

	stub.lastLimit = 0
	stub.lastBefore = nil
	empty := call(t, handler, http.MethodGet, "/api/versions/101/conversations", "", actor())
	if empty.Code != http.StatusOK {
		t.Fatalf("status = %d", empty.Code)
	}
	if stub.lastLimit != 20 {
		t.Fatalf("default limit = %d", stub.lastLimit)
	}

	stub.conversation = sampleConversationView()
	detail := call(t, handler, http.MethodGet, "/api/versions/101/conversations/8001", "", actor())
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", detail.Code, detail.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastCID != 8001 {
		t.Fatalf("args = %#v", stub)
	}
	detailData := dataBody(t, detail)
	if detailData["id"] != "8001" || detailData["summary"] != "Latest thread summary" || detailData["updated_at"] == "" {
		t.Fatalf("detail = %#v", detailData)
	}
}

func TestConversationMessagesParseCursorAndReturnParts(t *testing.T) {
	handler, stub := newConversationServer(t)
	stub.messages = []versionapp.MessageView{sampleMessageView()}
	stub.messagesMore = true

	recorder := call(t, handler, http.MethodGet, "/api/versions/101/conversations/8001/messages?after=123&limit=50", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastCID != 8001 || stub.lastAfter != 123 || stub.lastLimit != 50 {
		t.Fatalf("args = %#v", stub)
	}
	items := dataList(t, recorder)
	message := items[0].(map[string]any)
	if message["id"] != "8101" || message["conversation_id"] != "8001" || message["role"] != "assistant" {
		t.Fatalf("message = %#v", message)
	}
	for _, forbidden := range []string{"token_usage", "actor_id", "agent_run_id", "reply_to_message_id"} {
		if _, ok := message[forbidden]; ok {
			t.Fatalf("message exposed %q: %#v", forbidden, message)
		}
	}
	parts := message["parts"].([]any)
	card := parts[1].(map[string]any)
	if card["requirement_id"] != "9001" || card["revision"] != float64(2) {
		t.Fatalf("card = %#v", card)
	}
	attachment := parts[3].(map[string]any)
	if attachment["sha256"] == "" || attachment["size"] != float64(12) {
		t.Fatalf("attachment = %#v", attachment)
	}
	if _, ok := attachment["attachment_id"]; ok {
		t.Fatalf("attachment part must not expose attachment_id: %#v", attachment)
	}
	refs := message["refs"].([]any)
	if refs[0].(map[string]any)["kind"] != "requirement" {
		t.Fatalf("refs = %#v", refs)
	}
	if payload := decodeBody(t, recorder); payload["next_cursor"] != "8101" || payload["has_more"] != true {
		t.Fatalf("envelope = %#v", payload)
	}

	stub.lastAfter = -1
	stub.lastLimit = 0
	defaulted := call(t, handler, http.MethodGet, "/api/versions/101/conversations/8001/messages", "", actor())
	if defaulted.Code != http.StatusOK {
		t.Fatalf("status = %d", defaulted.Code)
	}
	if stub.lastAfter != 0 || stub.lastLimit != 100 {
		t.Fatalf("defaults = %d/%d", stub.lastAfter, stub.lastLimit)
	}
}

func TestPostConversationMessageParsesPartsAndReturns201(t *testing.T) {
	handler, stub := newConversationServer(t)
	stub.message = versionapp.MessageView{
		ID: 8102, ConversationID: 8001, Role: "user",
		Parts: []version.MessagePart{
			{Type: "text", Text: "Please check"},
			{Type: "attachment", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "spec.md", MIME: "text/markdown", Size: 42},
		},
		Refs:      []version.MessageRef{},
		CreatedAt: testTime,
	}

	body := `{"client_message_id":"client-1","parts":[{"type":"text","text":"Please check"},{"type":"attachment","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"spec.md","mime":"text/markdown","size":42}]}`
	recorder := call(t, handler, http.MethodPost, "/api/versions/101/conversations/8001/messages", body, actor())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastVersionID != 101 || stub.lastCID != 8001 || stub.lastClientID != "client-1" || stub.lastActor != 42 {
		t.Fatalf("args = %#v", stub)
	}
	if len(stub.lastParts) != 2 || stub.lastParts[0].Text != "Please check" || stub.lastParts[1].Size != 42 {
		t.Fatalf("parts = %#v", stub.lastParts)
	}
	data := dataBody(t, recorder)
	if data["id"] != "8102" || data["role"] != "user" || data["created_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("data = %#v", data)
	}
	if len(data["parts"].([]any)) != 2 {
		t.Fatalf("parts = %#v", data["parts"])
	}

	// An empty optional client_message_id is forwarded as empty.
	noClient := call(t, handler, http.MethodPost, "/api/versions/101/conversations/8001/messages", `{"parts":[{"type":"text","text":"hi"}]}`, actor())
	if noClient.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", noClient.Code, noClient.Body.String())
	}
	if stub.lastClientID != "" {
		t.Fatalf("client id = %q", stub.lastClientID)
	}
}

func TestConversationHandlerValidationAndErrors(t *testing.T) {
	handler, stub := newConversationServer(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		actor  *platform.Actor
		want   int
		field  string
	}{
		{"bad version id", http.MethodGet, "/api/versions/abc/conversations", "", actor(), 400, "id"},
		{"bad cid", http.MethodGet, "/api/versions/101/conversations/xyz", "", actor(), 400, "cid"},
		{"bad before", http.MethodGet, "/api/versions/101/conversations?before=abc", "", actor(), 400, "before"},
		{"bad after", http.MethodGet, "/api/versions/101/conversations/8001/messages?after=abc", "", actor(), 400, "after"},
		{"malformed create", http.MethodPost, "/api/versions/101/conversations", `{"kind":`, actor(), 400, "body"},
		{"malformed message", http.MethodPost, "/api/versions/101/conversations/8001/messages", `{`, actor(), 400, "body"},
		{"bad part type", http.MethodPost, "/api/versions/101/conversations/8001/messages", `{"parts":[{"type":"video"}]}`, actor(), 400, "parts[0]"},
		{"bad part json", http.MethodPost, "/api/versions/101/conversations/8001/messages", `{"parts":[{"type":"text","text":5}]}`, actor(), 400, "parts[0]"},
		{"missing actor create", http.MethodPost, "/api/versions/101/conversations", `{"kind":"clarification"}`, nil, 401, ""},
		{"missing actor post", http.MethodPost, "/api/versions/101/conversations/8001/messages", `{"parts":[{"type":"text","text":"x"}]}`, nil, 401, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := call(t, handler, tc.method, tc.path, tc.body, tc.actor)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.want, recorder.Body.String())
			}
			if tc.field != "" {
				if field := errorBody(t, recorder)["details"].(map[string]any)["field"]; field != tc.field {
					t.Fatalf("field = %#v, want %q", field, tc.field)
				}
			}
		})
	}

	stub.err = platform.NotFound("conversation")
	if recorder := call(t, handler, http.MethodGet, "/api/versions/101/conversations/8001", "", actor()); recorder.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d", recorder.Code)
	}
}

var _ = json.RawMessage{}
