package versionapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// stubMessagePartsService is the narrow application surface the parts handler
// consumes. It records the scope so the test can prove the handler forwards
// {id}/{cid}/{mid} unchanged (the service owns the version-ownership check).
type stubMessagePartsService struct {
	parts []versionapp.MessagePartView
	err   error

	calls         int
	lastVersionID int64
	lastCID       int64
	lastMessageID int64
}

func (s *stubMessagePartsService) MessageParts(_ context.Context, versionID, conversationID, messageID int64) ([]versionapp.MessagePartView, error) {
	s.calls++
	s.lastVersionID = versionID
	s.lastCID = conversationID
	s.lastMessageID = messageID
	if s.err != nil {
		return nil, s.err
	}
	return s.parts, nil
}

var _ versionapi.MessagePartsService = (*stubMessagePartsService)(nil)

func newMessagePartsServer(svc versionapi.MessagePartsService) http.Handler {
	mux := http.NewServeMux()
	versionapi.RegisterMessageParts(mux, func(next http.Handler) http.Handler { return next }, versionapi.NewMessagePartsHandler(svc))
	return mux
}

// sampleMessageParts returns rows deliberately out of seq order to prove the
// read API sorts, not just forwards.
func sampleMessageParts() []versionapp.MessagePartView {
	return []versionapp.MessagePartView{
		{
			Seq:       3,
			Type:      "job_card",
			Payload:   json.RawMessage(`{"job_id":"9001","status":"running","current":12,"total":27}`),
			CreatedAt: testTime,
		},
		{
			Seq:       1,
			Type:      "text",
			Payload:   json.RawMessage(`{"text":"hello"}`),
			CreatedAt: testTime,
		},
		{
			Seq:       2,
			Type:      "thinking",
			Payload:   json.RawMessage(`{"text":"checking the contract","duration_ms":1200}`),
			CreatedAt: testTime,
		},
	}
}

func TestMessagePartsOrdered(t *testing.T) {
	t.Run("orders by seq and maps the MessagePartView wire shape", func(t *testing.T) {
		stub := &stubMessagePartsService{parts: sampleMessageParts()}
		handler := newMessagePartsServer(stub)

		recorder := call(t, handler, http.MethodGet,
			"/api/versions/101/conversations/8001/messages/8101/parts", "", actor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if stub.lastVersionID != 101 || stub.lastCID != 8001 || stub.lastMessageID != 8101 {
			t.Fatalf("scope = %d/%d/%d", stub.lastVersionID, stub.lastCID, stub.lastMessageID)
		}

		payload := decodeBody(t, recorder)
		for _, absent := range []string{"next_cursor", "has_more"} {
			if _, ok := payload[absent]; ok {
				t.Fatalf("parts response must be the bare {data} envelope, got %q: %s", absent, recorder.Body.String())
			}
		}

		items := dataList(t, recorder)
		if len(items) != 3 {
			t.Fatalf("items = %d", len(items))
		}
		wantSeqs := []float64{1, 2, 3}
		wantTypes := []string{"text", "thinking", "job_card"}
		for index, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("item %d = %#v", index, raw)
			}
			if item["seq"] != wantSeqs[index] || item["part_type"] != wantTypes[index] {
				t.Fatalf("item %d = %#v, want seq=%v part_type=%q", index, item, wantSeqs[index], wantTypes[index])
			}
			if item["created_at"] != "2026-10-31T12:00:00Z" {
				t.Fatalf("item %d created_at = %#v", index, item["created_at"])
			}
			if _, ok := item["payload"].(map[string]any); !ok {
				t.Fatalf("item %d payload is not an object: %#v", index, item["payload"])
			}
		}

		textPayload := items[0].(map[string]any)["payload"].(map[string]any)
		if textPayload["text"] != "hello" {
			t.Fatalf("text payload = %#v", textPayload)
		}
		jobPayload := items[2].(map[string]any)["payload"].(map[string]any)
		if jobPayload["job_id"] != "9001" || jobPayload["total"] != float64(27) {
			t.Fatalf("job payload = %#v", jobPayload)
		}
	})

	t.Run("cross-version access is 404", func(t *testing.T) {
		stub := &stubMessagePartsService{err: platform.NotFound("message")}
		handler := newMessagePartsServer(stub)

		recorder := call(t, handler, http.MethodGet,
			"/api/versions/999/conversations/8001/messages/8101/parts", "", actor())
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if stub.lastVersionID != 999 || stub.lastCID != 8001 || stub.lastMessageID != 8101 {
			t.Fatalf("scope = %d/%d/%d", stub.lastVersionID, stub.lastCID, stub.lastMessageID)
		}
		if body := errorBody(t, recorder); body["code"] != "not_found" {
			t.Fatalf("error = %#v", body)
		}
	})

	t.Run("a message without parts returns an empty array", func(t *testing.T) {
		handler := newMessagePartsServer(&stubMessagePartsService{})
		recorder := call(t, handler, http.MethodGet,
			"/api/versions/101/conversations/8001/messages/8101/parts", "", actor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if items := dataList(t, recorder); len(items) != 0 {
			t.Fatalf("items = %#v", items)
		}
		if !strings.Contains(recorder.Body.String(), `"data":[]`) {
			t.Fatalf("empty parts must encode as []: %s", recorder.Body.String())
		}
	})
}

func TestMessagePartsValidation(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		field string
	}{
		{"bad version id", "/api/versions/abc/conversations/8001/messages/8101/parts", "id"},
		{"bad conversation id", "/api/versions/101/conversations/xyz/messages/8101/parts", "cid"},
		{"bad message id", "/api/versions/101/conversations/8001/messages/xyz/parts", "mid"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stub := &stubMessagePartsService{}
			handler := newMessagePartsServer(stub)
			recorder := call(t, handler, http.MethodGet, testCase.path, "", actor())
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if field := errorBody(t, recorder)["details"].(map[string]any)["field"]; field != testCase.field {
				t.Fatalf("field = %#v, want %q", field, testCase.field)
			}
			if stub.calls != 0 {
				t.Fatalf("invalid path reached the service %d times", stub.calls)
			}
		})
	}
}

func TestMessagePartTypesMatchContract(t *testing.T) {
	want := []string{
		"text", "thinking", "tool_call", "job_card",
		"attachment", "requirement_card", "case_card", "error",
	}
	if got := versionapi.MessagePartTypes(); !slices.Equal(got, want) {
		t.Fatalf("part types = %v, want %v", got, want)
	}
}
