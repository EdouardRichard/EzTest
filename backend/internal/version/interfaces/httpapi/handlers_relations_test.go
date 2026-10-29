package httpapi

import (
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	relationRequirementFrom = int64(9007199254740995)
	relationRequirementTo   = int64(9007199254740996)
	relationRelID           = int64(400)
)

// seedRelation stores one relation fixture on the fake service.
func seedRelation(deps *testDeps, relation versionapp.Relation) {
	if relation.VersionID == 0 {
		relation.VersionID = versionDetailID
	}
	if relation.RelationType == "" {
		relation.RelationType = domain.RelationDependsOn
	}
	if relation.Source == "" {
		relation.Source = domain.SourceUser
	}
	if relation.CreatedBy == 0 {
		relation.CreatedBy = testUserID
	}
	deps.relations.seed(relation)
}

// TestRelationRoutesCRUDWithActor covers the relation endpoints, including the
// true PATCH semantics (*string fields) and the authenticated actor pass-through
// that lead ruling 4 requires.
func TestRelationRoutesCRUDWithActor(t *testing.T) {
	t.Run("list returns every relation with string ids", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRelation(deps, versionapp.Relation{
			ID: relationRelID, FromRequirementID: relationRequirementFrom,
			ToRequirementID: relationRequirementTo, Description: "blocks checkout",
		})

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/relations", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		relations := decodeArrayData(t, recorder)
		if len(relations) != 1 {
			t.Fatalf("relations=%d want 1", len(relations))
		}
		relation := relations[0].(map[string]any)
		requireJSONString(t, relation, "id", "400")
		requireJSONString(t, relation, "versionId", "100")
		requireJSONString(t, relation, "fromRequirementId", "9007199254740995")
		requireJSONString(t, relation, "toRequirementId", "9007199254740996")
		requireStringField(t, relation, "relationType", "depends_on")
		requireStringField(t, relation, "description", "blocks checkout")
		requireStringField(t, relation, "source", "user")
		requireJSONNumber(t, relation, "createdBy", testUserID)
	})

	t.Run("create parses string id fields and passes the actor", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.relations.nextID = relationRelID

		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/relations", testUserToken,
			`{"fromRequirementId":"9007199254740995","toRequirementId":"9007199254740996","relationType":"depends_on","description":"blocks"}`)
		requireStatus(t, recorder, http.StatusCreated)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "id", "400")
		requireJSONString(t, data, "fromRequirementId", "9007199254740995")
		requireJSONString(t, data, "toRequirementId", "9007199254740996")
		requireStringField(t, data, "relationType", "depends_on")
		requireStringField(t, data, "source", "user")
		requireJSONNumber(t, data, "createdBy", testUserID)

		command := deps.relations.lastCreate
		if command.FromRequirementID != relationRequirementFrom ||
			command.ToRequirementID != relationRequirementTo ||
			command.RelationType != domain.RelationDependsOn || command.Description != "blocks" {
			t.Fatalf("create command=%+v", command)
		}
		if command.Source != domain.SourceUser {
			t.Fatalf("create source=%q want user", command.Source)
		}
		if deps.relations.lastActor != testUserID {
			t.Fatalf("create actor=%d want %d", deps.relations.lastActor, testUserID)
		}
	})

	t.Run("patch keeps omitted fields nil and forwards explicit empty values", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRelation(deps, versionapp.Relation{
			ID: relationRelID, FromRequirementID: relationRequirementFrom,
			ToRequirementID: relationRequirementTo, Description: "old",
		})

		recorder := doRequest(handler, http.MethodPatch, "/api/versions/100/relations/400", testUserToken,
			`{"description":"updated"}`)
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "description", "updated")
		requireStringField(t, data, "relationType", "depends_on")
		call := deps.relations.lastUpdate
		if call.versionID != versionDetailID || call.relationID != relationRelID || call.actor != testUserID {
			t.Fatalf("update call=%+v", call)
		}
		if call.relationType != nil {
			t.Fatalf("relationType=%v want nil (an omitted field stays nil)", call.relationType)
		}
		if call.description == nil || *call.description != "updated" {
			t.Fatalf("description=%v want updated", call.description)
		}

		recorder = doRequest(handler, http.MethodPatch, "/api/versions/100/relations/400", testUserToken,
			`{"relationType":""}`)
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_relation_type")
		call = deps.relations.lastUpdate
		if call.relationType == nil || *call.relationType != "" {
			t.Fatalf("explicit empty relationType must reach the service as non-nil, got %v", call.relationType)
		}
		if call.description != nil {
			t.Fatalf("description=%v want nil", call.description)
		}

		recorder = doRequest(handler, http.MethodPatch, "/api/versions/100/relations/400", testUserToken, `{"revision":1}`)
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")
	})

	t.Run("delete passes the authenticated actor", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRelation(deps, versionapp.Relation{ID: relationRelID, FromRequirementID: relationRequirementFrom, ToRequirementID: relationRequirementTo})

		recorder := doRequest(handler, http.MethodDelete, "/api/versions/100/relations/400", testUserToken, "")
		requireStatus(t, recorder, http.StatusNoContent)
		call := deps.relations.lastDelete
		if call.versionID != versionDetailID || call.relationID != relationRelID || call.actor != testUserID {
			t.Fatalf("delete call=%+v", call)
		}

		recorder = doRequest(handler, http.MethodDelete, "/api/versions/100/relations/400", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")
	})

	t.Run("error reasons are preserved", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.relations.createErr = domain.NewConflict("duplicate_relation", "an equivalent relation already exists")
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/relations", testUserToken,
			`{"fromRequirementId":"1","toRequirementId":"2","relationType":"overlaps"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "duplicate_relation")

		deps.relations.createErr = domain.NewInvalid("", "relation_requirement_mismatch",
			"both requirements of a relation must belong to its version")
		recorder = doRequest(handler, http.MethodPost, "/api/versions/100/relations", testUserToken,
			`{"fromRequirementId":"1","toRequirementId":"2","relationType":"overlaps"}`)
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "relation_requirement_mismatch")

		recorder = doRequest(handler, http.MethodPatch, "/api/versions/100/relations/not-a-number", testUserToken, `{}`)
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
	})

	t.Run("an empty relation list is an empty array", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/relations", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		if relations := decodeArrayData(t, recorder); len(relations) != 0 {
			t.Fatalf("relations=%v want []", relations)
		}
	})
}
