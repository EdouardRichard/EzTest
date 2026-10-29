package httpapi

import (
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	versionDetailID = int64(100)
	bigVersionID    = int64(9007199254740993)
)

// TestVersionRoutesRequireAuth proves every P3 route sits behind the global
// identity middleware: an unauthenticated call is 401, and a regular user is
// never rejected with 403 (P3 defines no admin-only route).
func TestVersionRoutesRequireAuth(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/versions"},
		{http.MethodPost, "/api/versions"},
		{http.MethodGet, "/api/versions/100"},
		{http.MethodPatch, "/api/versions/100"},
		{http.MethodPut, "/api/versions/100/bindings"},
		{http.MethodPost, "/api/versions/100/start-clarification"},
		{http.MethodPost, "/api/versions/100/enter-design"},
		{http.MethodGet, "/api/versions/100/requirements"},
		{http.MethodPost, "/api/versions/100/requirements"},
		{http.MethodGet, "/api/versions/100/requirements/200"},
		{http.MethodPatch, "/api/versions/100/requirements/200"},
		{http.MethodDelete, "/api/versions/100/requirements/200"},
		{http.MethodPost, "/api/versions/100/requirements/200/confirm"},
		{http.MethodPost, "/api/versions/100/requirements/200/reopen"},
		{http.MethodGet, "/api/versions/100/requirements/200/attachments"},
		{http.MethodPost, "/api/versions/100/requirements/200/attachments"},
		{http.MethodGet, "/api/versions/100/requirements/200/attachments/300/content"},
		{http.MethodDelete, "/api/versions/100/requirements/200/attachments/300"},
		{http.MethodGet, "/api/versions/100/relations"},
		{http.MethodPost, "/api/versions/100/relations"},
		{http.MethodPatch, "/api/versions/100/relations/400"},
		{http.MethodDelete, "/api/versions/100/relations/400"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			handler, _ := newTestServer(t)
			recorder := doRequest(handler, route.method, route.path, "", "")
			requireError(t, recorder, http.StatusUnauthorized, platform.CodeUnauthenticated, "")
		})
	}

	t.Run("regular user writes are allowed", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost, "/api/versions", testUserToken, `{"name":"Checkout"}`)
		if recorder.Code == http.StatusForbidden {
			t.Fatalf("status=403; P3 defines no forbidden route (body=%s)", recorder.Body.String())
		}
		requireStatus(t, recorder, http.StatusCreated)
	})
}

// TestVersionRoutesCRUDAndActions covers the whole version surface: create,
// list, detail, metadata PATCH, binding replacement and the two state actions
// (including the enter-design soft gate and its flattened warnings).
func TestVersionRoutesCRUDAndActions(t *testing.T) {
	t.Run("create returns a string id and resolved bindings", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedDefaultCatalog(deps)
		deps.versions.nextID = bigVersionID

		recorder := doRequest(handler, http.MethodPost, "/api/versions", testUserToken,
			`{"name":"Checkout","label":"v2.0","description":"order flow","systemId":1,"environmentId":2}`)
		requireStatus(t, recorder, http.StatusCreated)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "id", "9007199254740993")
		requireStringField(t, data, "name", "Checkout")
		requireStringField(t, data, "label", "v2.0")
		requireStringField(t, data, "description", "order flow")
		requireStringField(t, data, "status", "draft")
		requireJSONNumber(t, data, "systemId", 1)
		requireJSONNumber(t, data, "environmentId", 2)
		requireJSONNumber(t, data, "revision", 1)
		requireJSONNumber(t, data, "createdBy", testUserID)
		requireJSONNumber(t, data, "updatedBy", testUserID)
		requireJSONNumber(t, data, "requirementCount", 0)
		requireJSONNumber(t, data, "clarifiedCount", 0)
		requireJSONNumber(t, data, "openQuestionCount", 0)
		requireNullField(t, data, "codeRepositoryId")
		bindings := requireMapField(t, data, "bindings")
		system := requireMapField(t, bindings, "system")
		requireJSONNumber(t, system, "id", 1)
		requireStringField(t, system, "name", "Shop")
		requireBoolField(t, system, "available", true)
		environment := requireMapField(t, bindings, "environment")
		requireStringField(t, environment, "name", "staging")
		if bindings["codeRepository"] != nil {
			t.Fatalf("bindings.codeRepository=%v want null", bindings["codeRepository"])
		}

		if deps.versions.lastActor != testUserID {
			t.Fatalf("create actor=%d want %d", deps.versions.lastActor, testUserID)
		}
		command := deps.versions.lastCreate
		if command.Name != "Checkout" || command.Label != "v2.0" || command.Description != "order flow" {
			t.Fatalf("create command=%+v", command)
		}
		if command.SystemID == nil || *command.SystemID != 1 {
			t.Fatalf("create command systemId=%v want 1", command.SystemID)
		}
		if command.EnvironmentID == nil || *command.EnvironmentID != 2 {
			t.Fatalf("create command environmentId=%v want 2", command.EnvironmentID)
		}
	})

	t.Run("list returns the cursor envelope and the resolved counters", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedDefaultCatalog(deps)
		deps.versions.listVersions = []versionapp.Version{
			{ID: 101, Name: "Newer", Status: domain.VersionDraft, Revision: 1, CreatedAt: testNow, UpdatedAt: testNow},
			{ID: 100, Name: "Older", Status: domain.VersionClarifying, Revision: 2, CreatedAt: testNow, UpdatedAt: testNow},
		}
		deps.versionCounts.counts[101] = domain.VersionCounts{RequirementCount: 2, ClarifiedCount: 1, OpenQuestionCount: 0}
		cursor := platform.EncodeCursor(500)

		recorder := doRequest(handler, http.MethodGet, "/api/versions?status=draft&limit=1&cursor="+cursor, testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, next, hasMore := decodePage(t, recorder)
		if len(items) != 1 {
			t.Fatalf("items=%d want 1 (body=%s)", len(items), recorder.Body.String())
		}
		if !hasMore {
			t.Fatal("has_more=false want true")
		}
		if next == "" {
			t.Fatal("next_cursor is empty while has_more is true")
		}
		decoded, err := platform.DecodeCursor(next)
		if err != nil || decoded != 101 {
			t.Fatalf("next_cursor decodes to %d/%v want 101", decoded, err)
		}
		requireJSONString(t, items[0], "id", "101")
		requireJSONNumber(t, items[0], "requirementCount", 2)
		requireJSONNumber(t, items[0], "clarifiedCount", 1)
		requireJSONNumber(t, items[0], "openQuestionCount", 0)

		filter := deps.versions.lastFilter
		if filter.Status != "draft" {
			t.Fatalf("status filter=%q want draft", filter.Status)
		}
		if filter.Page.Limit != 1 || filter.Page.Cursor != cursor {
			t.Fatalf("page=%+v want limit 1 and the request cursor", filter.Page)
		}
	})

	t.Run("detail includes counts and resolved bindings", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedDefaultCatalog(deps)
		commit := "0123456789abcdef0123456789abcdef01234567"
		deps.versions.seed(versionapp.Version{
			ID:                 versionDetailID,
			Name:               "Checkout",
			Label:              "v2.0",
			Status:             domain.VersionClarifying,
			SystemID:           int64Ptr(1),
			EnvironmentID:      int64Ptr(2),
			CodeRepositoryID:   int64Ptr(3),
			CodeBranch:         stringPtr("main"),
			CodeBaselineCommit: stringPtr(commit),
			Revision:           2,
			CreatedBy:          testUserID,
			UpdatedBy:          testUserID,
		})
		deps.versionCounts.counts[versionDetailID] = domain.VersionCounts{
			RequirementCount: 3, ClarifiedCount: 1, OpenQuestionCount: 2,
		}

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "id", "100")
		requireStringField(t, data, "status", "clarifying")
		requireJSONNumber(t, data, "requirementCount", 3)
		requireJSONNumber(t, data, "clarifiedCount", 1)
		requireJSONNumber(t, data, "openQuestionCount", 2)
		requireStringField(t, data, "codeBranch", "main")
		requireStringField(t, data, "codeBaselineCommit", commit)
		bindings := requireMapField(t, data, "bindings")
		repository := requireMapField(t, bindings, "codeRepository")
		requireStringField(t, repository, "name", "order-web")
		requireJSONNumber(t, repository, "id", 3)

		recorder = doRequest(handler, http.MethodGet, "/api/versions/999", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")
	})

	t.Run("a deleted P2 resource is rendered unavailable instead of failing", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{
			ID: versionDetailID, Name: "Checkout", Status: domain.VersionDraft,
			SystemID: int64Ptr(7), Revision: 1,
		})

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		bindings := requireMapField(t, decodeDataMap(t, recorder), "bindings")
		system := requireMapField(t, bindings, "system")
		requireJSONNumber(t, system, "id", 7)
		requireStringField(t, system, "name", "")
		requireBoolField(t, system, "available", false)
	})

	t.Run("metadata patch keeps omitted fields and enforces the revision", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{
			ID: versionDetailID, Name: "Old", Label: "v1", Description: "kept",
			Status: domain.VersionDraft, Revision: 2,
		})

		recorder := doRequest(handler, http.MethodPatch, "/api/versions/100", testUserToken, `{"revision":1,"name":"Stale"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "revision_conflict")

		recorder = doRequest(handler, http.MethodPatch, "/api/versions/100", testUserToken, `{"revision":2,"name":"New"}`)
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "name", "New")
		requireStringField(t, data, "label", "v1")
		requireStringField(t, data, "description", "kept")
		requireStringField(t, data, "status", "draft")
		requireJSONNumber(t, data, "revision", 3)
		call := deps.versions.lastMetadata
		if call.versionID != versionDetailID || call.revision != 2 || call.name != "New" ||
			call.label != "v1" || call.description != "kept" || call.actor != testUserID {
			t.Fatalf("metadata call=%+v", call)
		}

		recorder = doRequest(handler, http.MethodPatch, "/api/versions/100", testUserToken, `{"revision":3,"status":"designing"}`)
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")
	})

	t.Run("bindings replace the whole set and respect the status gate", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedDefaultCatalog(deps)
		deps.versions.seed(versionapp.Version{
			ID: versionDetailID, Name: "Checkout", Status: domain.VersionDraft, Revision: 2,
		})
		commit := "0123456789abcdef0123456789abcdef01234567"

		recorder := doRequest(handler, http.MethodPut, "/api/versions/100/bindings", testUserToken,
			`{"revision":2,"systemId":1,"environmentId":2,"codeRepositoryId":3,"codeBranch":"main","codeBaselineCommit":"`+commit+`"}`)
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireJSONNumber(t, data, "systemId", 1)
		requireJSONNumber(t, data, "environmentId", 2)
		requireJSONNumber(t, data, "codeRepositoryId", 3)
		requireJSONNumber(t, data, "revision", 3)
		call := deps.versions.lastBindings
		if call.versionID != versionDetailID || call.revision != 2 || call.actor != testUserID {
			t.Fatalf("bindings call=%+v", call)
		}
		if call.bindings.SystemID == nil || *call.bindings.SystemID != 1 ||
			call.bindings.EnvironmentID == nil || *call.bindings.EnvironmentID != 2 ||
			call.bindings.CodeRepositoryID == nil || *call.bindings.CodeRepositoryID != 3 {
			t.Fatalf("bindings command=%+v", call.bindings)
		}
		if call.bindings.CodeBranch == nil || *call.bindings.CodeBranch != "main" {
			t.Fatalf("bindings branch=%v", call.bindings.CodeBranch)
		}

		// An omitted or null field clears the binding.
		recorder = doRequest(handler, http.MethodPut, "/api/versions/100/bindings", testUserToken, `{"revision":3}`)
		requireStatus(t, recorder, http.StatusOK)
		data = decodeDataMap(t, recorder)
		requireNullField(t, data, "systemId")
		requireNullField(t, data, "environmentId")
		requireNullField(t, data, "codeRepositoryId")
		bindings := requireMapField(t, data, "bindings")
		if bindings["system"] != nil || bindings["environment"] != nil || bindings["codeRepository"] != nil {
			t.Fatalf("cleared bindings=%v", bindings)
		}

		deps.versions.seed(versionapp.Version{
			ID: 102, Name: "Designing", Status: domain.VersionDesigning, Revision: 1,
		})
		recorder = doRequest(handler, http.MethodPut, "/api/versions/102/bindings", testUserToken, `{"revision":1}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "version_state_conflict")
	})

	t.Run("start-clarification maps requirements_required and moves draft to clarifying", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout", Status: domain.VersionDraft, Revision: 1})
		deps.versions.startErr = domain.NewInvalid("", "requirements_required",
			"start-clarification requires at least one requirement")

		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/start-clarification", testUserToken, "")
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "requirements_required")

		deps.versions.startErr = nil
		recorder = doRequest(handler, http.MethodPost, "/api/versions/100/start-clarification", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		requireStringField(t, decodeDataMap(t, recorder), "status", "clarifying")
		if deps.versions.lastActor != testUserID {
			t.Fatalf("action actor=%d want %d", deps.versions.lastActor, testUserID)
		}
	})

	t.Run("enter-design requires acknowledgement and returns flattened warnings", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{
			ID: 103, Name: "Checkout", Status: domain.VersionClarifying, Revision: 1,
		})
		warnings := versionapp.Warnings{
			UnclarifiedRequirementIDs: []int64{1001, 1002, 1003},
			OpenQuestionCount:         2,
		}
		deps.versions.enterWarnings = warnings
		deps.versions.enterErr = &versionapp.UnclarifiedRequirementsError{
			Cause:    domain.NewStateConflict("unclarified_requirements", "unclarified requirements must be acknowledged"),
			Warnings: warnings,
		}

		recorder := doRequest(handler, http.MethodPost, "/api/versions/103/enter-design", testUserToken, "")
		apiError := requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "unclarified_requirements")
		ids, ok := apiError.Details["unclarifiedRequirementIds"].([]any)
		if !ok || len(ids) != 3 {
			t.Fatalf("unclarifiedRequirementIds=%#v want 3 string ids", apiError.Details["unclarifiedRequirementIds"])
		}
		for i, want := range []string{"1001", "1002", "1003"} {
			got, ok := ids[i].(string)
			if !ok || got != want {
				t.Fatalf("unclarifiedRequirementIds[%d]=%#v want %q", i, ids[i], want)
			}
		}
		count, ok := apiError.Details["openQuestionCount"].(float64)
		if !ok || count != 2 {
			t.Fatalf("openQuestionCount=%#v want 2", apiError.Details["openQuestionCount"])
		}
		if message, _ := apiError.Details["message"].(string); message == "" {
			t.Fatal("details.message must be human-readable")
		}
		if deps.versions.lastEnterAck {
			t.Fatal("the first enter-design call must not acknowledge")
		}

		deps.versions.enterErr = nil
		recorder = doRequest(handler, http.MethodPost, "/api/versions/103/enter-design", testUserToken,
			`{"acknowledgeUnclarified":true}`)
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		version := requireMapField(t, data, "version")
		requireJSONString(t, version, "id", "103")
		requireStringField(t, version, "status", "designing")
		flattened := requireArrayField(t, data, "warnings")
		if len(flattened) != 1 {
			t.Fatalf("warnings=%v want one flattened string", flattened)
		}
		requireStringField(t, map[string]any{"warning": flattened[0]}, "warning", "3 条需求未澄清 / 2 个待澄清问题")
		if !deps.versions.lastEnterAck {
			t.Fatal("the acknowledged retry must forward acknowledgeUnclarified=true")
		}

		deps.versions.seed(versionapp.Version{
			ID: 104, Name: "Clean", Status: domain.VersionClarifying, Revision: 1,
		})
		deps.versions.enterWarnings = versionapp.Warnings{}
		recorder = doRequest(handler, http.MethodPost, "/api/versions/104/enter-design", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data = decodeDataMap(t, recorder)
		if warnings, ok := data["warnings"].([]any); !ok || len(warnings) != 0 {
			t.Fatalf("warnings=%#v want an empty array", data["warnings"])
		}
	})

	t.Run("only the open questions warning is flattened on its own", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: 105, Name: "Checkout", Status: domain.VersionClarifying, Revision: 1})
		deps.versions.enterWarnings = versionapp.Warnings{OpenQuestionCount: 2}

		recorder := doRequest(handler, http.MethodPost, "/api/versions/105/enter-design", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		warnings := requireArrayField(t, decodeDataMap(t, recorder), "warnings")
		if len(warnings) != 1 || warnings[0] != "2 个待澄清问题" {
			t.Fatalf("warnings=%v want [2 个待澄清问题]", warnings)
		}
	})
}

// TestIDStringsInJSON pins the ID serialization of spec §7.1: every P3-owned id
// is a decimal JSON string, while P2 resource ids and user ids stay numbers.
func TestIDStringsInJSON(t *testing.T) {
	const (
		requirementID = int64(9007199254740995)
		relationID    = int64(9007199254740997)
		attachmentID  = int64(9007199254740999)
	)
	handler, deps := newTestServer(t)
	seedDefaultCatalog(deps)
	deps.versions.seed(versionapp.Version{
		ID: bigVersionID, Name: "Checkout", Status: domain.VersionDraft,
		SystemID: int64Ptr(1), EnvironmentID: int64Ptr(2), Revision: 1,
		CreatedBy: testUserID, UpdatedBy: testUserID,
	})
	deps.versions.seed(versionapp.Version{
		ID: 100, Name: "Other", Status: domain.VersionDraft, Revision: 1,
		CreatedBy: testUserID, UpdatedBy: testUserID,
	})
	deps.requirements.seed(versionapp.Requirement{
		ID: requirementID, VersionID: bigVersionID, Title: "Login",
		Priority: domain.PriorityMust, Status: domain.RequirementDraft,
		Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: testUserID, UpdatedBy: testUserID,
	})
	deps.relations.seed(versionapp.Relation{
		ID: relationID, VersionID: bigVersionID,
		FromRequirementID: requirementID, ToRequirementID: requirementID + 2,
		RelationType: domain.RelationDependsOn, Source: domain.SourceUser,
		CreatedBy: testUserID,
	})
	deps.attachments.seed(versionapp.Attachment{
		ID: attachmentID, VersionID: bigVersionID, RequirementID: requirementID,
		Kind: domain.AttachmentText, Filename: "note.txt", MIMEType: "text/plain",
		SizeBytes: 5, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StoragePath: "100/300_note.txt", CreatedBy: testUserID,
	}, []byte("hello"))

	// Version: P3 id string, P2 ids and user ids numbers.
	recorder := doRequest(handler, http.MethodGet, "/api/versions/9007199254740993", testUserToken, "")
	requireStatus(t, recorder, http.StatusOK)
	version := decodeDataMap(t, recorder)
	requireJSONString(t, version, "id", "9007199254740993")
	requireJSONNumber(t, version, "systemId", 1)
	requireJSONNumber(t, version, "createdBy", testUserID)
	bindings := requireMapField(t, version, "bindings")
	system := requireMapField(t, bindings, "system")
	requireJSONNumber(t, system, "id", 1)

	// Requirement: id/versionId strings, user ids numbers.
	recorder = doRequest(handler, http.MethodGet, "/api/versions/9007199254740993/requirements", testUserToken, "")
	requireStatus(t, recorder, http.StatusOK)
	requirements, _, _ := decodePage(t, recorder)
	if len(requirements) != 1 {
		t.Fatalf("requirements=%d want 1", len(requirements))
	}
	requireJSONString(t, requirements[0], "id", "9007199254740995")
	requireJSONString(t, requirements[0], "versionId", "9007199254740993")
	requireJSONNumber(t, requirements[0], "createdBy", testUserID)
	requireJSONNumber(t, requirements[0], "updatedBy", testUserID)
	requireNullField(t, requirements[0], "confirmedBy")

	// Detail exposes attachment metadata with a string id and no storage path.
	recorder = doRequest(handler, http.MethodGet, "/api/versions/9007199254740993/requirements/9007199254740995", testUserToken, "")
	requireStatus(t, recorder, http.StatusOK)
	detail := decodeDataMap(t, recorder)
	requireJSONString(t, detail, "id", "9007199254740995")
	attachments := requireArrayField(t, detail, "attachments")
	if len(attachments) != 1 {
		t.Fatalf("attachments=%d want 1", len(attachments))
	}
	attachment := attachments[0].(map[string]any)
	requireJSONString(t, attachment, "id", "9007199254740999")
	requireMissingField(t, attachment, "storagePath")

	// Relation: all four ids/endpoints strings, creator number.
	recorder = doRequest(handler, http.MethodGet, "/api/versions/9007199254740993/relations", testUserToken, "")
	requireStatus(t, recorder, http.StatusOK)
	relations := decodeArrayData(t, recorder)
	if len(relations) != 1 {
		t.Fatalf("relations=%d want 1", len(relations))
	}
	relation := relations[0].(map[string]any)
	requireJSONString(t, relation, "id", "9007199254740997")
	requireJSONString(t, relation, "versionId", "9007199254740993")
	requireJSONString(t, relation, "fromRequirementId", "9007199254740995")
	requireJSONString(t, relation, "toRequirementId", "9007199254740997")
	requireJSONNumber(t, relation, "createdBy", testUserID)
}

// TestErrorReasons consolidates the spec §7.1 status/reason mapping at the HTTP
// boundary: 409 state/uniqueness conflicts, 422 semantic validation, 404 missing
// rows and an internal failure that never leaks its message.
func TestErrorReasons(t *testing.T) {
	t.Run("409 version_state_conflict", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.startErr = domain.NewStateConflict("version_state_conflict",
			"start-clarification requires draft")
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/start-clarification", testUserToken, "")
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "version_state_conflict")
	})

	t.Run("409 revision_conflict", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.seed(versionapp.Version{ID: versionDetailID, Name: "Checkout", Status: domain.VersionDraft, Revision: 2})
		recorder := doRequest(handler, http.MethodPatch, "/api/versions/100", testUserToken, `{"revision":1}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "revision_conflict")
	})

	t.Run("409 requirement_state_conflict", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login", Status: domain.RequirementClarified})
		recorder := doRequest(handler, http.MethodPatch, "/api/versions/100/requirements/200", testUserToken,
			`{"revision":1,"title":"New"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "requirement_state_conflict")
	})

	t.Run("409 duplicate_attachment", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.attachments.uploadErr = domain.NewConflict("duplicate_attachment", "same sha256")
		recorder := doMultipartRequest(t, handler, "/api/versions/100/requirements/200/attachments", testUserToken,
			"note.txt", "text/plain", []byte("content"))
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "duplicate_attachment")
	})

	t.Run("422 invalid field reason", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.requirements.createErr = domain.NewInvalid("title", "invalid_title", "title is required")
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/requirements", testUserToken, `{"title":""}`)
		apiError := requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_title")
		if apiError.Details["field"] != "title" {
			t.Fatalf("details.field=%v want title", apiError.Details["field"])
		}
	})

	t.Run("422 malformed JSON body", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/requirements", testUserToken, `{"title":`)
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")
	})

	t.Run("404 not_found", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet, "/api/versions/999", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")
	})

	t.Run("500 internal does not leak the cause", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.startErr = errBoom
		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/start-clarification", testUserToken, "")
		apiError := requireError(t, recorder, http.StatusInternalServerError, platform.CodeInternal, "")
		if apiError.Message != "internal server error" {
			t.Fatalf("message=%q leaks the internal cause", apiError.Message)
		}
	})
}

// TestPaginationEnvelope pins limit 1-100 / default 20 / opaque cursor handling
// for both paged endpoints and the 400 mapping of malformed parameters.
func TestPaginationEnvelope(t *testing.T) {
	t.Run("versions page", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.versions.listVersions = []versionapp.Version{
			{ID: 101, Name: "Newer", Status: domain.VersionDraft, Revision: 1, CreatedAt: testNow, UpdatedAt: testNow},
			{ID: 100, Name: "Older", Status: domain.VersionDraft, Revision: 1, CreatedAt: testNow, UpdatedAt: testNow},
		}

		recorder := doRequest(handler, http.MethodGet, "/api/versions?limit=1", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, next, hasMore := decodePage(t, recorder)
		if len(items) != 1 || !hasMore {
			t.Fatalf("items=%d hasMore=%v want 1/true", len(items), hasMore)
		}
		decoded, err := platform.DecodeCursor(next)
		if err != nil || decoded != 101 {
			t.Fatalf("next_cursor=%q decodes %d/%v want 101", next, decoded, err)
		}

		for _, raw := range []string{"0", "101", "abc"} {
			recorder = doRequest(handler, http.MethodGet, "/api/versions?limit="+raw, testUserToken, "")
			requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_limit")
		}
		recorder = doRequest(handler, http.MethodGet, "/api/versions?cursor=not-a-cursor", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_cursor")

		recorder = doRequest(handler, http.MethodGet, "/api/versions", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		if deps.versions.lastFilter.Page.Limit != platform.DefaultLimit {
			t.Fatalf("default limit=%d want %d", deps.versions.lastFilter.Page.Limit, platform.DefaultLimit)
		}
	})

	t.Run("requirements page", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.requirements.listResult = []versionapp.Requirement{
			{ID: 201, VersionID: 100, Title: "One", Priority: domain.PriorityShould, Status: domain.RequirementDraft, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser, Revision: 1},
			{ID: 200, VersionID: 100, Title: "Two", Priority: domain.PriorityShould, Status: domain.RequirementDraft, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser, Revision: 1},
		}

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements?limit=1", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, next, hasMore := decodePage(t, recorder)
		if len(items) != 1 || !hasMore {
			t.Fatalf("items=%d hasMore=%v want 1/true", len(items), hasMore)
		}
		decoded, err := platform.DecodeCursor(next)
		if err != nil || decoded != 201 {
			t.Fatalf("next_cursor=%q decodes %d/%v want 201", next, decoded, err)
		}

		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/requirements?limit=0", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_limit")
		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/requirements?cursor=garbage", testUserToken, "")
		requireError(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_cursor")

		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/requirements", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		if deps.requirements.lastListPage.Limit != platform.DefaultLimit {
			t.Fatalf("default limit=%d want %d", deps.requirements.lastListPage.Limit, platform.DefaultLimit)
		}
		if deps.requirements.lastListID != 100 {
			t.Fatalf("list version=%d want 100", deps.requirements.lastListID)
		}
	})

	t.Run("an empty page is an empty array", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet, "/api/versions", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, _, _ := decodePage(t, recorder)
		if len(items) != 0 {
			t.Fatalf("items=%v want []", items)
		}
	})
}
