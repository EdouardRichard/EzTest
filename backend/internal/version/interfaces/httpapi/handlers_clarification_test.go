package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	bigClarificationSessionID  = int64(9007199254740992)
	bigClarificationQuestionID = int64(9007199254740993)
	bigClarificationDecisionID = int64(9007199254740994)
	clarificationMessageRunID  = int64(7001)
)

// TestClarificationOverview covers GET /api/versions/{id}/clarification: the
// active session summary, the per-status counters and the empty-session shape.
func TestClarificationOverview(t *testing.T) {
	t.Run("active session and counters", func(t *testing.T) {
		handler, deps := newTestServer(t)
		summaryUpdatedAt := testNow.Add(-1)
		deps.clarification.seedSession(versionapp.ClarificationSession{
			ID:               bigClarificationSessionID,
			VersionID:        versionDetailID,
			Status:           domain.SessionActive,
			AgentSessionID:   int64Ptr(clarificationMessageRunID),
			ContextSummary:   "context so far",
			SummaryUpdatedAt: &summaryUpdatedAt,
			StartedAt:        testNow,
		})
		deps.clarification.counts = versionapp.ClarificationCounts{
			OpenQuestions:      1,
			AnsweredQuestions:  2,
			DismissedQuestions: 3,
			ProposedDecisions:  4,
			AcceptedDecisions:  5,
			RejectedDecisions:  6,
		}

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/clarification", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		session := requireMapField(t, data, "session")
		requireJSONString(t, session, "id", "9007199254740992")
		requireStringField(t, session, "status", domain.SessionActive)
		requireJSONNumber(t, session, "agentSessionId", clarificationMessageRunID)
		requireStringField(t, session, "contextSummary", "context so far")
		if _, ok := session["startedAt"].(string); !ok || session["startedAt"] == "" {
			t.Fatalf("startedAt=%#v want an RFC3339 timestamp", session["startedAt"])
		}
		questions := requireMapField(t, data, "questions")
		requireJSONNumber(t, questions, "open", 1)
		requireJSONNumber(t, questions, "answered", 2)
		requireJSONNumber(t, questions, "dismissed", 3)
		decisions := requireMapField(t, data, "decisions")
		requireJSONNumber(t, decisions, "proposed", 4)
		requireJSONNumber(t, decisions, "accepted", 5)
		requireJSONNumber(t, decisions, "rejected", 6)

		if !containsCall(deps.clarification.calls, "CurrentSession") ||
			!containsCall(deps.clarification.calls, "Counts") {
			t.Fatalf("calls=%v want CurrentSession and Counts", deps.clarification.calls)
		}
	})

	t.Run("no session renders null and zero counters", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/clarification", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireNullField(t, data, "session")
		requireJSONNumber(t, requireMapField(t, data, "questions"), "open", 0)
		requireJSONNumber(t, requireMapField(t, data, "decisions"), "proposed", 0)
	})

	t.Run("a missing version is a 404", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.clarification.currentErr = domain.NewNotFound("version", versionDetailID)
		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/clarification", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")
	})
}

// TestClarificationSessionStartAndMessage covers the 201 session start, the
// narrow started body, the 200 complete body, message sending (202) and the
// documented 409/503 failures.
func TestClarificationSessionStartAndMessage(t *testing.T) {
	t.Run("start returns 201 with the narrow body", func(t *testing.T) {
		handler, deps := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/sessions", testUserToken, "")
		requireStatus(t, recorder, http.StatusCreated)
		data := decodeDataMap(t, recorder)
		if len(data) != 4 {
			t.Fatalf("started session keys=%v want exactly id/status/agentSessionId/startedAt", data)
		}
		requireJSONString(t, data, "id", "6001")
		requireStringField(t, data, "status", domain.SessionActive)
		requireJSONNumber(t, data, "agentSessionId", int64(7001))
		if _, ok := data["startedAt"].(string); !ok || data["startedAt"] == "" {
			t.Fatalf("startedAt=%#v want an RFC3339 timestamp", data["startedAt"])
		}
		requireMissingField(t, data, "endedAt")
		requireMissingField(t, data, "contextSummary")
		if deps.clarification.lastActor != testUserID || deps.clarification.lastVersion != versionDetailID {
			t.Fatalf("start call actor/version=%d/%d", deps.clarification.lastActor, deps.clarification.lastVersion)
		}
	})

	t.Run("starting a second session is a 409", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.clarification.sessionErr = domain.NewConflict("clarification_session_active",
			"the version already has an active clarification session")
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/sessions", testUserToken, "")
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "clarification_session_active")
	})

	t.Run("start on a wrong version status is a 409", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.clarification.sessionErr = domain.NewStateConflict("version_state_conflict",
			"clarification sessions require clarifying or designing")
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/sessions", testUserToken, "")
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "version_state_conflict")
	})

	t.Run("complete returns the full session dto", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.clarification.seedSession(versionapp.ClarificationSession{
			ID: bigClarificationSessionID, VersionID: versionDetailID,
		})
		recorder := doRequest(handler, http.MethodPost,
			"/api/versions/100/clarification/sessions/9007199254740992/complete", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "id", "9007199254740992")
		requireStringField(t, data, "status", domain.SessionCompleted)
		requireJSONNumber(t, data, "agentSessionId", clarificationMessageRunID)
		requireStringField(t, data, "contextSummary", "")
		if _, ok := data["endedAt"].(string); !ok || data["endedAt"] == "" {
			t.Fatalf("endedAt=%#v want an RFC3339 timestamp", data["endedAt"])
		}
		requireNullField(t, data, "summaryUpdatedAt")
		if deps.clarification.lastSessionID != bigClarificationSessionID ||
			deps.clarification.lastActor != testUserID {
			t.Fatalf("complete call session/actor=%d/%d",
				deps.clarification.lastSessionID, deps.clarification.lastActor)
		}
	})

	t.Run("complete failures keep the documented reasons", func(t *testing.T) {
		cases := []struct {
			name   string
			err    error
			reason string
		}{
			{"not active", domain.NewStateConflict("clarification_session_not_active", "not active"),
				"clarification_session_not_active"},
			{"agent busy", domain.NewStateConflict("agent_busy", "a run is active"), "agent_busy"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				handler, deps := newTestServer(t)
				deps.clarification.completeErr = testCase.err
				recorder := doRequest(handler, http.MethodPost,
					"/api/versions/100/clarification/sessions/6001/complete", testUserToken, "")
				requireError(t, recorder, http.StatusConflict, platform.CodeConflict, testCase.reason)
			})
		}
	})

	t.Run("send message returns 202 with numeric run ids", func(t *testing.T) {
		handler, deps := newTestServer(t)
		session := deps.clarification.seedSession(versionapp.ClarificationSession{
			ID: bigClarificationSessionID, VersionID: versionDetailID,
			AgentSessionID: int64Ptr(clarificationMessageRunID),
		})
		deps.clarification.result = versionapp.StartRunResult{
			SessionID:      session.ID,
			AgentSessionID: clarificationMessageRunID,
			RunID:          9001,
			Status:         "running",
		}
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/messages", testUserToken,
			`{"message":"what is the SLA?"}`)
		requireStatus(t, recorder, http.StatusAccepted)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "sessionId", "9007199254740992")
		requireJSONNumber(t, data, "agentSessionId", clarificationMessageRunID)
		requireJSONNumber(t, data, "runId", 9001)
		requireStringField(t, data, "status", "running")
		if deps.clarification.lastMessage != "what is the SLA?" ||
			deps.clarification.lastActor != testUserID {
			t.Fatalf("send call message/actor=%q/%d",
				deps.clarification.lastMessage, deps.clarification.lastActor)
		}
	})

	t.Run("send failures keep the documented reasons", func(t *testing.T) {
		cases := []struct {
			name   string
			err    error
			reason string
		}{
			{"not active", domain.NewStateConflict("clarification_session_not_active", "not active"),
				"clarification_session_not_active"},
			{"agent busy", domain.NewStateConflict("agent_busy", "a run is active"), "agent_busy"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				handler, deps := newTestServer(t)
				deps.clarification.messageErr = testCase.err
				recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/messages",
					testUserToken, `{"message":"hi"}`)
				requireError(t, recorder, http.StatusConflict, platform.CodeConflict, testCase.reason)
			})
		}
	})

	t.Run("invalid message json is a 422", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/messages",
			testUserToken, `{"message":`)
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")
	})
}

// TestClarificationMessageHistory covers GET clarification/messages: numeric
// before_id paging, the system-role filter, the decimal next_cursor and numeric
// message/run ids.
func TestClarificationMessageHistory(t *testing.T) {
	t.Run("filters system messages and returns a decimal cursor", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.clarification.seedSession(versionapp.ClarificationSession{
			ID: 6001, VersionID: versionDetailID, AgentSessionID: int64Ptr(clarificationMessageRunID),
		})
		toolCalls := json.RawMessage(`[{"id":"call-1","name":"version.context_get","arguments":"{}"}]`)
		deps.messages.messages = []versionapp.AgentMessage{
			{ID: 10, RunID: 9001, Role: "user",
				Parts: []versionapp.AgentPart{{Type: "text", Text: "hi"}}, CreatedAt: testNow},
			{ID: 11, RunID: 9001, Role: "system",
				Parts: []versionapp.AgentPart{{Type: "text", Text: "system prompt"}}, CreatedAt: testNow},
			{ID: 12, RunID: 9001, Role: "assistant",
				Parts:     []versionapp.AgentPart{{Type: "text", Text: "hello"}},
				ToolCalls: toolCalls, ToolName: "version.context_get", CreatedAt: testNow},
		}
		deps.messages.hasMore = true

		recorder := doRequest(handler, http.MethodGet,
			"/api/versions/100/clarification/messages?before_id=20&limit=5", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, nextCursor, hasMore := decodePage(t, recorder)
		if len(items) != 2 {
			t.Fatalf("items=%d want the 2 non-system messages", len(items))
		}
		requireJSONNumber(t, items[0], "id", 10)
		requireStringField(t, items[0], "role", "user")
		requireJSONNumber(t, items[0], "runId", 9001)
		parts := requireArrayField(t, items[0], "parts")
		if len(parts) != 1 || parts[0].(map[string]any)["text"] != "hi" {
			t.Fatalf("parts=%v", parts)
		}
		requireJSONNumber(t, items[1], "id", 12)
		requireStringField(t, items[1], "role", "assistant")
		calls := requireArrayField(t, items[1], "toolCalls")
		if len(calls) != 1 {
			t.Fatalf("toolCalls=%v want 1 call", calls)
		}
		if nextCursor != "10" {
			t.Fatalf("next_cursor=%q want the decimal oldest P1 message id %q", nextCursor, "10")
		}
		if !hasMore {
			t.Fatal("has_more=false want the P1 has-more flag")
		}
		if deps.messages.lastAgentSessionID != clarificationMessageRunID ||
			deps.messages.lastBeforeID != 20 || deps.messages.lastLimit != 5 {
			t.Fatalf("messages call agentSession/before/limit=%d/%d/%d",
				deps.messages.lastAgentSessionID, deps.messages.lastBeforeID, deps.messages.lastLimit)
		}
	})

	t.Run("no active session returns an empty page without reading messages", func(t *testing.T) {
		handler, deps := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/clarification/messages", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, nextCursor, hasMore := decodePage(t, recorder)
		if len(items) != 0 || nextCursor != "" || hasMore {
			t.Fatalf("page=%v/%q/%v want empty", items, nextCursor, hasMore)
		}
		if deps.messages.calls != 0 {
			t.Fatalf("message reader calls=%d want 0 without an active session", deps.messages.calls)
		}
	})

	t.Run("query validation", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet,
			"/api/versions/100/clarification/messages?before_id=abc", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_before_id")

		recorder = doRequest(handler, http.MethodGet,
			"/api/versions/100/clarification/messages?limit=0", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_limit")
	})

	t.Run("limit defaults to 20", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.clarification.seedSession(versionapp.ClarificationSession{
			ID: 6001, VersionID: versionDetailID, AgentSessionID: int64Ptr(clarificationMessageRunID),
		})
		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/clarification/messages", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		if deps.messages.lastLimit != 20 || deps.messages.lastBeforeID != 0 {
			t.Fatalf("messages defaults before/limit=%d/%d want 0/20",
				deps.messages.lastBeforeID, deps.messages.lastLimit)
		}
	})
}

// TestClarificationAnalyzeAndRecon covers the 202 one-shot runs, the text-only
// analyze path and the bounded recon parts built from the version attachments.
func TestClarificationAnalyzeAndRecon(t *testing.T) {
	t.Run("analyze starts a text run with numeric ids", func(t *testing.T) {
		handler, deps := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/analyze", testUserToken, "")
		requireStatus(t, recorder, http.StatusAccepted)
		data := decodeDataMap(t, recorder)
		if len(data) != 2 {
			t.Fatalf("analyze keys=%v want exactly agentSessionId/runId", data)
		}
		requireJSONNumber(t, data, "agentSessionId", 7001)
		requireJSONNumber(t, data, "runId", 9001)
		if !containsCall(deps.clarification.calls, "StartAnalysis") {
			t.Fatalf("calls=%v want StartAnalysis", deps.clarification.calls)
		}
		if containsCall(deps.clarification.calls, "StartReconWithParts") {
			t.Fatalf("calls=%v analyze must not start recon", deps.clarification.calls)
		}
		if deps.clarification.lastVersion != versionDetailID || deps.clarification.lastActor != testUserID {
			t.Fatalf("analyze version/actor=%d/%d",
				deps.clarification.lastVersion, deps.clarification.lastActor)
		}
	})

	t.Run("recon injects bounded text and at most two image refs", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.requirements.listResult = []versionapp.Requirement{
			{ID: 200, VersionID: versionDetailID, Title: "Login"},
			{ID: 201, VersionID: versionDetailID, Title: "Payment"},
		}
		// Requirement 200: one text attachment, one injectable image and one
		// oversized image that must be skipped.
		seedTextAttachment(deps, versionapp.Attachment{
			ID: 300, VersionID: versionDetailID, RequirementID: 200, Filename: "note.txt",
			MIMEType: "text/plain", SizeBytes: 5,
		}, []byte("alpha"))
		seedTextAttachment(deps, versionapp.Attachment{
			ID: 301, VersionID: versionDetailID, RequirementID: 200, Kind: domain.AttachmentImage,
			Filename: "shot.png", MIMEType: "image/png", SizeBytes: 1 << 20,
		}, nil)
		seedTextAttachment(deps, versionapp.Attachment{
			ID: 302, VersionID: versionDetailID, RequirementID: 200, Kind: domain.AttachmentImage,
			Filename: "huge.png", MIMEType: "image/png", SizeBytes: domain.MaxAgentImageBytes + 1,
		}, nil)
		// Requirement 201: one text attachment and two injectable images; only
		// the first of those two fits the two-image run budget.
		seedTextAttachment(deps, versionapp.Attachment{
			ID: 303, VersionID: versionDetailID, RequirementID: 201, Filename: "spec.md",
			MIMEType: "text/markdown", SizeBytes: 4,
		}, []byte("beta"))
		seedTextAttachment(deps, versionapp.Attachment{
			ID: 304, VersionID: versionDetailID, RequirementID: 201, Kind: domain.AttachmentImage,
			Filename: "flow.jpg", MIMEType: "image/jpeg", SizeBytes: 1 << 20,
		}, nil)
		seedTextAttachment(deps, versionapp.Attachment{
			ID: 305, VersionID: versionDetailID, RequirementID: 201, Kind: domain.AttachmentImage,
			Filename: "extra.png", MIMEType: "image/png", SizeBytes: 1 << 20,
		}, nil)

		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/recon", testUserToken, "")
		requireStatus(t, recorder, http.StatusAccepted)
		data := decodeDataMap(t, recorder)
		requireJSONNumber(t, data, "agentSessionId", 7001)
		requireJSONNumber(t, data, "runId", 9001)
		if !containsCall(deps.clarification.calls, "StartReconWithParts") {
			t.Fatalf("calls=%v want StartReconWithParts", deps.clarification.calls)
		}

		parts := deps.clarification.lastReconParts
		if len(parts) != 4 {
			t.Fatalf("recon parts=%+v want 2 text + 2 image parts", parts)
		}
		// The instruction part is prepended by StartReconWithParts itself (T10);
		// the HTTP layer only supplies the attachment parts.
		if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "alpha") ||
			!strings.Contains(parts[0].Text, "note.txt") {
			t.Fatalf("parts[0]=%+v want the note.txt text summary", parts[0])
		}
		if parts[1].Type != "image" || parts[1].ImageRef != "attachment:301" {
			t.Fatalf("parts[1]=%+v want the attachment:301 image ref", parts[1])
		}
		if parts[2].Type != "text" || !strings.Contains(parts[2].Text, "beta") ||
			!strings.Contains(parts[2].Text, "spec.md") {
			t.Fatalf("parts[2]=%+v want the spec.md text summary", parts[2])
		}
		if parts[3].Type != "image" || parts[3].ImageRef != "attachment:304" {
			t.Fatalf("parts[3]=%+v want the attachment:304 image ref", parts[3])
		}
		if len(deps.attachments.readTextCalls) != 2 {
			t.Fatalf("ReadText calls=%+v want exactly the 2 text attachments",
				deps.attachments.readTextCalls)
		}
		for _, call := range deps.attachments.readTextCalls {
			if call.versionID != versionDetailID || call.maxBytes <= 0 ||
				call.maxBytes > domain.MaxInjectedTextBytesPerFile {
				t.Fatalf("ReadText call=%+v want a version-scoped 64 KiB-bounded read", call)
			}
		}
	})

	t.Run("recon without attachments passes nil parts", func(t *testing.T) {
		handler, deps := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/recon", testUserToken, "")
		requireStatus(t, recorder, http.StatusAccepted)
		if deps.clarification.lastReconParts != nil {
			t.Fatalf("recon parts=%+v want nil without attachments", deps.clarification.lastReconParts)
		}
	})

	t.Run("recon caps the injected text at 200 KiB per run", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.requirements.listResult = []versionapp.Requirement{
			{ID: 200, VersionID: versionDetailID, Title: "Login"},
		}
		chunk := strings.Repeat("a", 60<<10)
		for i := int64(0); i < 5; i++ {
			seedTextAttachment(deps, versionapp.Attachment{
				ID: 300 + i, VersionID: versionDetailID, RequirementID: 200,
				Filename: "file.txt", MIMEType: "text/plain", SizeBytes: int64(len(chunk)),
			}, []byte(chunk))
		}

		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/clarification/recon", testUserToken, "")
		requireStatus(t, recorder, http.StatusAccepted)
		parts := deps.clarification.lastReconParts
		if len(parts) != 4 {
			t.Fatalf("recon text parts=%d want 4 (the fifth exceeds the 200 KiB run cap)", len(parts))
		}
		if len(deps.attachments.readTextCalls) != 4 {
			t.Fatalf("ReadText calls=%d want 4", len(deps.attachments.readTextCalls))
		}
		if got := deps.attachments.readTextCalls[3].maxBytes; got != 20<<10 {
			t.Fatalf("last ReadText maxBytes=%d want the remaining 20 KiB budget", got)
		}
		injected := 0
		for _, part := range parts {
			if part.Type != "text" {
				t.Fatalf("part=%+v want text", part)
			}
			if index := strings.IndexByte(part.Text, '\n'); index >= 0 {
				injected += len(part.Text) - index - 1
			}
		}
		if injected != domain.MaxInjectedTextBytesPerRun {
			t.Fatalf("injected text bytes=%d want %d", injected, domain.MaxInjectedTextBytesPerRun)
		}
	})

	t.Run("agent and llm startup failures are retryable ai_unavailable", func(t *testing.T) {
		cases := []struct {
			name string
			set  func(*fakeClarificationService, error)
			path string
			body string
		}{
			{"start llm not configured", func(f *fakeClarificationService, err error) { f.sessionErr = err },
				"/api/versions/100/clarification/sessions", ""},
			{"send llm not configured", func(f *fakeClarificationService, err error) { f.messageErr = err },
				"/api/versions/100/clarification/messages", `{"message":"hi"}`},
			{"analyze tools unsupported", func(f *fakeClarificationService, err error) { f.analysisErr = err },
				"/api/versions/100/clarification/analyze", ""},
			{"recon capacity", func(f *fakeClarificationService, err error) { f.reconErr = err },
				"/api/versions/100/clarification/recon", ""},
		}
		errs := map[string]error{
			"start llm not configured":  agentdomain.ErrLLMNotConfigured,
			"send llm not configured":   agentdomain.ErrLLMNotConfigured,
			"analyze tools unsupported": agentdomain.ErrToolsUnsupported,
			"recon capacity":            agentdomain.ErrCapacity,
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				handler, deps := newTestServer(t)
				testCase.set(deps.clarification, errs[testCase.name])
				recorder := doRequest(handler, http.MethodPost, testCase.path, testUserToken, testCase.body)
				apiError := requireError(t, recorder, http.StatusServiceUnavailable,
					platform.CodeUnavailable, "ai_unavailable")
				if !apiError.Retryable {
					t.Fatalf("retryable=false want true for %v", errs[testCase.name])
				}
			})
		}
	})
}

// TestClarificationRoutesRequireAuth proves every T15 route sits behind the
// login middleware and none of them needs an admin role.
func TestClarificationRoutesRequireAuth(t *testing.T) {
	handler, _ := newTestServer(t)
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/versions/100/clarification", ""},
		{http.MethodPost, "/api/versions/100/clarification/sessions", ""},
		{http.MethodPost, "/api/versions/100/clarification/sessions/6001/complete", ""},
		{http.MethodGet, "/api/versions/100/clarification/messages", ""},
		{http.MethodPost, "/api/versions/100/clarification/messages", `{"message":"hi"}`},
		{http.MethodPost, "/api/versions/100/clarification/analyze", ""},
		{http.MethodPost, "/api/versions/100/clarification/recon", ""},
		{http.MethodGet, "/api/versions/100/clarification/questions", ""},
		{http.MethodPost, "/api/versions/100/clarification/questions", `{"question":"q"}`},
		{http.MethodPatch, "/api/versions/100/clarification/questions/6001", `{"status":"dismissed"}`},
		{http.MethodGet, "/api/versions/100/decisions", ""},
		{http.MethodPost, "/api/versions/100/decisions", `{"topic":"t","decision":"d"}`},
		{http.MethodPatch, "/api/versions/100/decisions/6001", `{"status":"accepted"}`},
		{http.MethodGet, "/api/versions/100/events", ""},
		{http.MethodGet, "/api/versions/100/stream", ""},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			recorder := doRequest(handler, route.method, route.path, "", route.body)
			requireError(t, recorder, http.StatusUnauthorized, platform.CodeUnauthenticated, "")
		})
	}
}

// TestHandlersNilSafe proves the v2 extension stays nil-safe: an empty
// dependency set mounts nothing, a partial set only mounts the backed routes and
// a nil mux is ignored instead of panicking.
func TestHandlersNilSafe(t *testing.T) {
	handlers := NewHandlers(HandlersDeps{})
	if handlers.Clarification != nil || handlers.Questions != nil || handlers.Decisions != nil ||
		handlers.Events != nil || handlers.Stream != nil {
		t.Fatalf("empty deps built v2 groups: %+v", handlers)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, *handlers)
	RegisterRoutes(nil, *handlers)

	partial := NewHandlers(HandlersDeps{Clarification: newFakeClarificationService()})
	if partial.Clarification == nil || partial.Questions == nil || partial.Decisions == nil {
		t.Fatal("the clarification group must mount without the P1 agent wiring")
	}
	if partial.Events != nil || partial.Stream != nil {
		t.Fatal("the event and stream groups need their readers and must stay unmounted")
	}
	mux = http.NewServeMux()
	RegisterRoutes(mux, *partial)
	_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, "/api/versions/100/clarification", nil))
	if pattern == "" {
		t.Fatal("the clarification overview route is not mounted")
	}
}
