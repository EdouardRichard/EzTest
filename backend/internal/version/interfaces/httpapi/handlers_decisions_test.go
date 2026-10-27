package httpapi

import (
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// seedDecision stores one decision fixture on the fake clarification service.
func seedDecision(deps *testDeps, decision versionapp.Decision) *versionapp.Decision {
	if decision.VersionID == 0 {
		decision.VersionID = versionDetailID
	}
	if decision.Status == "" {
		decision.Status = domain.DecisionProposed
	}
	if decision.Source == "" {
		decision.Source = domain.SourceAI
	}
	if decision.ProposedBy == 0 {
		decision.ProposedBy = testUserID
	}
	if decision.CreatedAt.IsZero() {
		decision.CreatedAt = testNow
	}
	if decision.UpdatedAt.IsZero() {
		decision.UpdatedAt = testNow
	}
	return deps.clarification.seedDecision(decision)
}

// TestDecisionEndpoints covers the decision list/create/patch endpoints, the
// manual-create accepted default and the proposed -> accepted/rejected
// transitions.
func TestDecisionEndpoints(t *testing.T) {
	t.Run("list returns string ids and null links", func(t *testing.T) {
		handler, deps := newTestServer(t)
		requirementID := int64(200)
		questionID := int64(300)
		seedDecision(deps, versionapp.Decision{
			ID: bigClarificationDecisionID, VersionID: versionDetailID,
			RequirementID: &requirementID, QuestionID: &questionID,
			Topic: "SLA", Decision: "99.9%", Status: domain.DecisionProposed,
		})
		seedDecision(deps, versionapp.Decision{
			ID: 2, VersionID: versionDetailID, Topic: "Auth", Decision: "JWT",
			Status: domain.DecisionAccepted,
		})

		recorder := doRequest(handler, http.MethodGet,
			"/api/versions/100/decisions?status=proposed&limit=10", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, nextCursor, hasMore := decodePage(t, recorder)
		if len(items) != 1 || nextCursor != "" || hasMore {
			t.Fatalf("page=%v/%q/%v want the single filtered decision", items, nextCursor, hasMore)
		}
		decision := items[0]
		requireJSONString(t, decision, "id", "9007199254740994")
		requireJSONString(t, decision, "versionId", "100")
		requireJSONString(t, decision, "requirementId", "200")
		requireJSONString(t, decision, "questionId", "300")
		requireStringField(t, decision, "topic", "SLA")
		requireStringField(t, decision, "status", domain.DecisionProposed)
		requireJSONNumber(t, decision, "proposedBy", testUserID)
		requireNullField(t, decision, "decidedBy")
		requireNullField(t, decision, "decidedAt")
		listCall := deps.clarification.lastDecisionList
		if listCall.versionID != versionDetailID || listCall.status != "proposed" ||
			listCall.page.Limit != 10 {
			t.Fatalf("list call=%+v", listCall)
		}
	})

	t.Run("manual create is accepted and user-sourced", func(t *testing.T) {
		handler, deps := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/decisions", testUserToken,
			`{"topic":"SLA","decision":"99.9%","rationale":"contract","requirementId":"200","questionId":"300"}`)
		requireStatus(t, recorder, http.StatusCreated)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "id", "6001")
		requireJSONString(t, data, "versionId", "100")
		requireJSONString(t, data, "requirementId", "200")
		requireJSONString(t, data, "questionId", "300")
		requireStringField(t, data, "status", domain.DecisionAccepted)
		requireStringField(t, data, "source", domain.SourceUser)
		requireJSONNumber(t, data, "proposedBy", testUserID)
		requireJSONNumber(t, data, "decidedBy", testUserID)
		if deps.clarification.lastDecisionSrc != domain.SourceUser {
			t.Fatalf("decision source=%q want user", deps.clarification.lastDecisionSrc)
		}
		input := deps.clarification.lastDecisionInput
		if input.Topic != "SLA" || input.Decision != "99.9%" || input.Rationale != "contract" ||
			input.RequirementID == nil || *input.RequirementID != 200 ||
			input.QuestionID == nil || *input.QuestionID != 300 {
			t.Fatalf("decision input=%+v", input)
		}
	})

	t.Run("accept and reject transitions", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedDecision(deps, versionapp.Decision{
			ID: bigClarificationDecisionID, VersionID: versionDetailID,
			Topic: "SLA", Decision: "99.9%", Status: domain.DecisionProposed,
		})
		path := "/api/versions/100/decisions/9007199254740994"

		recorder := doRequest(handler, http.MethodPatch, path, testUserToken, `{"status":"accepted"}`)
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "status", domain.DecisionAccepted)
		requireJSONNumber(t, data, "decidedBy", testUserID)
		if _, ok := data["decidedAt"].(string); !ok || data["decidedAt"] == "" {
			t.Fatalf("decidedAt=%#v want an RFC3339 timestamp", data["decidedAt"])
		}
		if call := deps.clarification.lastDecide; !call.accept || call.decisionID != bigClarificationDecisionID ||
			call.actor != testUserID || len(call.rationale) != 0 {
			t.Fatalf("decide call=%+v", call)
		}

		seedDecision(deps, versionapp.Decision{
			ID: 6002, VersionID: versionDetailID, Topic: "Auth", Decision: "Sessions",
			Status: domain.DecisionProposed,
		})
		recorder = doRequest(handler, http.MethodPatch,
			"/api/versions/100/decisions/6002", testUserToken,
			`{"status":"rejected","rationale":"too risky"}`)
		requireStatus(t, recorder, http.StatusOK)
		data = decodeDataMap(t, recorder)
		requireStringField(t, data, "status", domain.DecisionRejected)
		requireStringField(t, data, "rationale", "too risky")
		if call := deps.clarification.lastDecide; call.accept ||
			len(call.rationale) != 1 || call.rationale[0] != "too risky" {
			t.Fatalf("decide call=%+v", call)
		}
	})

	t.Run("terminal decisions are a 409", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedDecision(deps, versionapp.Decision{
			ID: 6001, VersionID: versionDetailID, Topic: "SLA", Decision: "x",
			Status: domain.DecisionAccepted,
		})
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/decisions/6001", testUserToken, `{"status":"rejected"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "decision_state_conflict")

		deps.clarification.decideErr = domain.NewStateConflict(
			"decision_state_conflict", "the decision is already terminal")
		recorder = doRequest(handler, http.MethodPatch,
			"/api/versions/100/decisions/6001", testUserToken, `{"status":"rejected"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "decision_state_conflict")
	})

	t.Run("an unknown status is a 422", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodPatch,
			"/api/versions/100/decisions/6001", testUserToken, `{"status":"maybe"}`)
		apiError := requireError(t, recorder, http.StatusUnprocessableEntity,
			platform.CodeInvalidArgument, "invalid_status")
		if apiError.Details["field"] != "status" {
			t.Fatalf("details.field=%v want status", apiError.Details["field"])
		}
	})

	t.Run("service validation failures map to 422", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.clarification.createDecision = domain.NewInvalid("topic", "invalid_topic", "topic is required")
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/decisions", testUserToken, `{}`)
		apiError := requireError(t, recorder, http.StatusUnprocessableEntity,
			platform.CodeInvalidArgument, "invalid_topic")
		if apiError.Details["field"] != "topic" {
			t.Fatalf("details.field=%v want topic", apiError.Details["field"])
		}
	})
}
