package httpapi

import (
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// seedQuestion stores one question fixture on the fake clarification service.
func seedQuestion(deps *testDeps, question versionapp.Question) *versionapp.Question {
	if question.VersionID == 0 {
		question.VersionID = versionDetailID
	}
	if question.Status == "" {
		question.Status = domain.QuestionOpen
	}
	if question.Source == "" {
		question.Source = domain.SourceUser
	}
	if question.AnsweredVia == "" {
		question.AnsweredVia = domain.AnsweredViaUI
	}
	if question.CreatedAt.IsZero() {
		question.CreatedAt = testNow
	}
	if question.UpdatedAt.IsZero() {
		question.UpdatedAt = testNow
	}
	return deps.clarification.seedQuestion(question)
}

// TestQuestionEndpoints covers the question list/create/patch endpoints and the
// full open -> answered -> open -> dismissed -> open transition set.
func TestQuestionEndpoints(t *testing.T) {
	t.Run("list filters and returns string ids", func(t *testing.T) {
		handler, deps := newTestServer(t)
		requirementID := int64(200)
		sessionID := int64(6001)
		seedQuestion(deps, versionapp.Question{
			ID: bigClarificationQuestionID, VersionID: versionDetailID,
			RequirementID: &requirementID, SessionID: &sessionID,
			Question: "What is the SLA?", Status: domain.QuestionOpen,
			CreatedBy: testUserID,
		})
		seedQuestion(deps, versionapp.Question{
			ID: 2, VersionID: versionDetailID, Question: "Version-level?",
			Status: domain.QuestionAnswered, Answer: "yes",
			CreatedBy: testUserID,
		})

		recorder := doRequest(handler, http.MethodGet,
			"/api/versions/100/clarification/questions?status=open&requirementId=200&limit=10",
			testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, nextCursor, hasMore := decodePage(t, recorder)
		if len(items) != 1 || nextCursor != "" || hasMore {
			t.Fatalf("page=%v/%q/%v want the single filtered question", items, nextCursor, hasMore)
		}
		question := items[0]
		requireJSONString(t, question, "id", "9007199254740993")
		requireJSONString(t, question, "versionId", "100")
		requireJSONString(t, question, "requirementId", "200")
		requireJSONString(t, question, "sessionId", "6001")
		requireStringField(t, question, "status", domain.QuestionOpen)
		requireJSONNumber(t, question, "createdBy", testUserID)
		requireNullField(t, question, "answeredBy")
		requireNullField(t, question, "answeredAt")
		requireStringField(t, question, "answeredVia", domain.AnsweredViaUI)
		listCall := deps.clarification.lastQuestionList
		if listCall.versionID != versionDetailID || listCall.status != "open" ||
			listCall.requirementID == nil || *listCall.requirementID != 200 || listCall.page.Limit != 10 {
			t.Fatalf("list call=%+v", listCall)
		}
	})

	t.Run("create is open, user-sourced and 201", func(t *testing.T) {
		handler, deps := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost,
			"/api/versions/100/clarification/questions", testUserToken,
			`{"question":"Which currencies?","requirementId":"200"}`)
		requireStatus(t, recorder, http.StatusCreated)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "id", "6001")
		requireJSONString(t, data, "versionId", "100")
		requireJSONString(t, data, "requirementId", "200")
		requireStringField(t, data, "question", "Which currencies?")
		requireStringField(t, data, "status", domain.QuestionOpen)
		requireStringField(t, data, "source", domain.SourceUser)
		if deps.clarification.lastQuestionSrc != domain.SourceUser {
			t.Fatalf("question source=%q want user", deps.clarification.lastQuestionSrc)
		}
		if deps.clarification.lastActor != testUserID {
			t.Fatalf("actor=%d want %d", deps.clarification.lastActor, testUserID)
		}
		input := deps.clarification.lastQuestionInput
		if input.Question != "Which currencies?" || input.RequirementID == nil || *input.RequirementID != 200 {
			t.Fatalf("question input=%+v", input)
		}
	})

	t.Run("a numeric requirement id is rejected", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost,
			"/api/versions/100/clarification/questions", testUserToken,
			`{"question":"Which currencies?","requirementId":200}`)
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")
	})

	t.Run("answer then reopen then dismiss then reopen", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedQuestion(deps, versionapp.Question{
			ID: bigClarificationQuestionID, VersionID: versionDetailID,
			Question: "What is the SLA?", CreatedBy: testUserID,
		})
		path := "/api/versions/100/clarification/questions/9007199254740993"

		recorder := doRequest(handler, http.MethodPatch, path, testUserToken,
			`{"status":"answered","answer":"99.9%"}`)
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "status", domain.QuestionAnswered)
		requireStringField(t, data, "answer", "99.9%")
		requireStringField(t, data, "answeredVia", domain.AnsweredViaUI)
		requireJSONNumber(t, data, "answeredBy", testUserID)
		if _, ok := data["answeredAt"].(string); !ok || data["answeredAt"] == "" {
			t.Fatalf("answeredAt=%#v want an RFC3339 timestamp", data["answeredAt"])
		}
		if deps.clarification.lastQuestionID != bigClarificationQuestionID ||
			deps.clarification.lastAnswer != "99.9%" {
			t.Fatalf("answer call id/answer=%d/%q",
				deps.clarification.lastQuestionID, deps.clarification.lastAnswer)
		}

		recorder = doRequest(handler, http.MethodPatch, path, testUserToken, `{"status":"open"}`)
		requireStatus(t, recorder, http.StatusOK)
		data = decodeDataMap(t, recorder)
		requireStringField(t, data, "status", domain.QuestionOpen)
		requireStringField(t, data, "answer", "")
		requireNullField(t, data, "answeredBy")
		requireNullField(t, data, "answeredAt")

		recorder = doRequest(handler, http.MethodPatch, path, testUserToken,
			`{"status":"dismissed"}`)
		requireStatus(t, recorder, http.StatusOK)
		requireStringField(t, decodeDataMap(t, recorder), "status", domain.QuestionDismissed)

		recorder = doRequest(handler, http.MethodPatch, path, testUserToken, `{"status":"open"}`)
		requireStatus(t, recorder, http.StatusOK)
		requireStringField(t, decodeDataMap(t, recorder), "status", domain.QuestionOpen)

		if !containsCall(deps.clarification.calls, "AnswerQuestion") ||
			!containsCall(deps.clarification.calls, "DismissQuestion") ||
			!containsCall(deps.clarification.calls, "ReopenQuestion") {
			t.Fatalf("calls=%v want answer/dismiss/reopen", deps.clarification.calls)
		}
	})

	t.Run("editing the text keeps the state", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedQuestion(deps, versionapp.Question{
			ID: 6001, VersionID: versionDetailID, Question: "Old question?", CreatedBy: testUserID,
		})
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/clarification/questions/6001", testUserToken,
			`{"question":"New question?"}`)
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "question", "New question?")
		requireStringField(t, data, "status", domain.QuestionOpen)
		input := deps.clarification.lastQuestionInput
		if input.QuestionID == nil || *input.QuestionID != 6001 || input.Question != "New question?" {
			t.Fatalf("upsert input=%+v", input)
		}
	})

	t.Run("answering requires an answer", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedQuestion(deps, versionapp.Question{
			ID: 6001, VersionID: versionDetailID, Question: "What?", CreatedBy: testUserID,
		})
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/clarification/questions/6001", testUserToken,
			`{"status":"answered"}`)
		apiError := requireError(t, recorder, http.StatusUnprocessableEntity,
			platform.CodeInvalidArgument, "invalid_answer")
		if apiError.Details["field"] != "answer" {
			t.Fatalf("details.field=%v want answer", apiError.Details["field"])
		}
	})

	t.Run("illegal transitions are a 409", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedQuestion(deps, versionapp.Question{
			ID: 6001, VersionID: versionDetailID, Question: "What?",
			Status: domain.QuestionDismissed, CreatedBy: testUserID,
		})
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/clarification/questions/6001", testUserToken,
			`{"status":"answered","answer":"late"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict,
			"clarification_question_state_conflict")

		deps.clarification.answerErr = domain.NewStateConflict(
			"clarification_question_state_conflict", "the question is not open")
		recorder = doRequest(handler, http.MethodPatch,
			"/api/versions/100/clarification/questions/6001", testUserToken,
			`{"status":"answered","answer":"late"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict,
			"clarification_question_state_conflict")
	})

	t.Run("an illegal transition does not write the combined text edit", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedQuestion(deps, versionapp.Question{
			ID: 6001, VersionID: versionDetailID, Question: "original",
			Status: domain.QuestionDismissed, CreatedBy: testUserID,
		})
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/clarification/questions/6001", testUserToken,
			`{"status":"answered","answer":"late","question":"rewritten"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict,
			"clarification_question_state_conflict")
		if containsCall(deps.clarification.calls, "UpsertQuestion") {
			t.Fatalf("calls=%v want no text write before an illegal transition",
				deps.clarification.calls)
		}
	})

	t.Run("an unknown status is a 422", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/clarification/questions/6001", testUserToken,
			`{"status":"bogus"}`)
		apiError := requireError(t, recorder, http.StatusUnprocessableEntity,
			platform.CodeInvalidArgument, "invalid_status")
		if apiError.Details["field"] != "status" {
			t.Fatalf("details.field=%v want status", apiError.Details["field"])
		}
	})

	t.Run("an empty patch is a 422", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/clarification/questions/6001", testUserToken, `{}`)
		requireError(t, recorder, http.StatusUnprocessableEntity,
			platform.CodeInvalidArgument, "invalid_request")
	})

	t.Run("path ids are validated", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/clarification/questions/not-an-id", testUserToken,
			`{"status":"open"}`)
		requireError(t, recorder, http.StatusUnprocessableEntity,
			platform.CodeInvalidArgument, "invalid_id")
	})

	t.Run("list rejects a malformed requirement filter", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet,
			"/api/versions/100/clarification/questions?requirementId=abc", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest,
			platform.CodeInvalidArgument, "invalid_id")
	})
}
