package knowledgeapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* --------------------------------- helpers -------------------------------- */

func testSuccessFact() knowledgeapp.SuccessFact {
	return knowledgeapp.SuccessFact{
		VersionID:     10,
		SystemKey:     "mozhan",
		SystemID:      ptr[int64](7),
		EnvironmentID: ptr[int64](8),
		PageKey:       "/login",
		AccountRole:   "admin",
		Recipe: map[string]any{
			"steps": []any{
				map[string]any{"action": "open", "target": "/login"},
				map[string]any{"action": "fill", "field": "username", "selector": "#username"},
				map[string]any{"action": "fill", "field": "password", "selector": "#password"},
				map[string]any{"action": "click", "selector": "#submit"},
			},
		},
		EvidenceIDs:  []int64{501, 502},
		SourceFactID: ptr[int64](601),
	}
}

func decodeItemContent(t *testing.T, item knowledge.Item) map[string]any {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal([]byte(item.Content), &content); err != nil {
		t.Fatalf("content %q is not a JSON object: %v", item.Content, err)
	}
	return content
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

func findAudit(calls []auditCall, action string) (auditCall, bool) {
	for _, call := range calls {
		if call.action == action {
			return call, true
		}
	}
	return auditCall{}, false
}

func findEvent(events []eventingapp.Event, eventType string) (eventingapp.Event, bool) {
	for _, event := range events {
		if event.Type == eventType {
			return event, true
		}
	}
	return eventingapp.Event{}, false
}

func assertNoSecret(t *testing.T, label, rendered string) {
	t.Helper()
	for _, secret := range []string{"s3cret", "hunter2", "Bearer abc123"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("%s leaks %q: %s", label, secret, rendered)
		}
	}
}

// assertSecretRejected requires the secret-specific validation failure (not an
// earlier kind-binding failure).
func assertSecretRejected(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted a credential", label)
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" || apiErr.Details["field"] != "fact" {
		t.Fatalf("%s: error = %+v, want 400 invalid_argument field=fact", label, apiErr)
	}
	message := strings.ToLower(apiErr.Message)
	if !strings.Contains(message, "credential") && !strings.Contains(message, "secret") {
		t.Fatalf("%s: message = %q, want a secret-specific rejection", label, apiErr.Message)
	}
}

// draftKindItem seeds a low-risk draft in the repository so AutoApprove can be
// exercised without depending on the success-hook path.
func draftKindItem(id int64, kind knowledgeapp.Kind) knowledge.Item {
	item := draftItem(id, 1)
	item.Scope = knowledge.ScopeSystem
	item.SystemKey = ptr("mozhan")
	item.Tags = []string{"kind:" + string(kind)}
	item.Content = fmt.Sprintf(`{"kind":%q,"scope":"system"}`, kind)
	return item
}

/* ---------------------------------- tests --------------------------------- */

// TestLoginSuccessCreatesDraftAndAutoApproves is the hook -> approved
// integration: the success pipeline persists a draft and then auto-approves it
// (low-risk kind) with the dedicated audit action.
func TestLoginSuccessCreatesDraftAndAutoApproves(t *testing.T) {
	f := newFixture(t)
	item, err := f.svc.OnLoginSuccess(context.Background(), testSuccessFact())
	if err != nil {
		t.Fatalf("OnLoginSuccess: %v", err)
	}

	if item.Status != knowledge.StatusApproved {
		t.Fatalf("status = %q, want approved (hook drafts are auto-approved)", item.Status)
	}
	if item.Scope != knowledge.ScopeSystem || item.SystemKey == nil || *item.SystemKey != "mozhan" {
		t.Fatalf("scope = %q system_key = %v, want system/mozhan", item.Scope, item.SystemKey)
	}
	if item.Bindings.SystemID == nil || *item.Bindings.SystemID != 7 ||
		item.Bindings.EnvironmentID == nil || *item.Bindings.EnvironmentID != 8 ||
		item.Bindings.PageKey == nil || *item.Bindings.PageKey != "/login" ||
		item.Bindings.AccountRole == nil || *item.Bindings.AccountRole != "admin" {
		t.Fatalf("bindings = %+v, want system/environment/page/account from the fact", item.Bindings)
	}
	if item.Bindings.APIKey != nil {
		t.Fatalf("api_key binding = %v, want unset for a login recipe", *item.Bindings.APIKey)
	}
	if !hasTag(item.Tags, "kind:login_recipe") {
		t.Fatalf("tags = %v, want kind:login_recipe", item.Tags)
	}
	if item.SourceVersionID == nil || *item.SourceVersionID != 10 {
		t.Fatalf("source_version_id = %v, want 10", item.SourceVersionID)
	}

	content := decodeItemContent(t, item)
	if content["kind"] != "login_recipe" {
		t.Fatalf("content kind = %v, want login_recipe", content["kind"])
	}
	if content["scope"] != "system" {
		t.Fatalf("content scope = %v, want system", content["scope"])
	}
	recipe, ok := content["recipe"].(map[string]any)
	if !ok || len(recipe["steps"].([]any)) != 4 {
		t.Fatalf("content recipe = %v, want the structured recipe", content["recipe"])
	}
	if content["source_version_id"] != "10" || content["source_fact_id"] != "601" {
		t.Fatalf("content provenance = %v", content)
	}

	// The hook persists a draft revision through the normal Create path...
	if f.repo.insertCalls != 1 {
		t.Fatalf("insert calls = %d, want 1", f.repo.insertCalls)
	}
	if f.repo.insertedItem.Status != knowledge.StatusDraft {
		t.Fatalf("persisted insert status = %q, want draft", f.repo.insertedItem.Status)
	}
	if f.repo.insertedRevision.KnowledgeID != item.ID || f.repo.insertedRevision.Revision != 1 {
		t.Fatalf("revision = %+v", f.repo.insertedRevision)
	}
	// ...and then promotes it through the lifecycle transition.
	if f.repo.statusItem.ID != item.ID || f.repo.statusItem.Status != knowledge.StatusApproved ||
		f.repo.statusFrom != knowledge.StatusDraft {
		t.Fatalf("auto approval write = %+v from %q, want approved from draft",
			f.repo.statusItem, f.repo.statusFrom)
	}
	if _, ok := findAudit(f.audit.calls, knowledgeapp.ActionCreate); !ok {
		t.Fatalf("audit = %v, want knowledge.create", f.audit.calls)
	}
	if _, ok := findAudit(f.audit.calls, knowledgeapp.ActionAutoApproved); !ok {
		t.Fatalf("audit = %v, want %q", f.audit.calls, knowledgeapp.ActionAutoApproved)
	}
	if _, ok := findEvent(f.events.events, "knowledge.proposed"); !ok {
		t.Fatalf("events = %v, want knowledge.proposed", f.events.events)
	}
	if _, ok := findEvent(f.events.events, knowledgeapp.EventAutoApproved); !ok {
		t.Fatalf("events = %v, want %q", f.events.events, knowledgeapp.EventAutoApproved)
	}
	assertNoSecret(t, "hook audit/events", fmt.Sprint(f.audit.calls)+fmt.Sprint(f.events.events))
}

// TestLoginSuccessAutoApprovalIsBestEffort proves the success pipeline does not
// fail when the automatic promotion does: the draft is persisted and returned,
// and only the review gate is left to promote it later.
func TestLoginSuccessAutoApprovalIsBestEffort(t *testing.T) {
	f := newFixture(t)
	f.repo.statusErr = errors.New("knowledge store down")
	item, err := f.svc.OnLoginSuccess(context.Background(), testSuccessFact())
	if err != nil {
		t.Fatalf("OnLoginSuccess failed on an auto-approval error: %v", err)
	}
	if item.Status != knowledge.StatusDraft {
		t.Fatalf("status = %q, want the persisted draft", item.Status)
	}
	if f.repo.insertCalls != 1 || f.repo.insertedItem.Status != knowledge.StatusDraft {
		t.Fatalf("insert calls = %d item = %+v, want the draft persisted", f.repo.insertCalls, f.repo.insertedItem)
	}
	if _, ok := findAudit(f.audit.calls, knowledgeapp.ActionAutoApproved); ok {
		t.Fatalf("failed auto approval wrote %q audit", knowledgeapp.ActionAutoApproved)
	}
	if _, ok := findEvent(f.events.events, knowledgeapp.EventAutoApproved); ok {
		t.Fatalf("failed auto approval published %q", knowledgeapp.EventAutoApproved)
	}
}

func TestPageLocatedCreatesDraftAndAutoApproves(t *testing.T) {
	f := newFixture(t)
	item, err := f.svc.OnPageLocated(context.Background(), knowledgeapp.SuccessFact{
		VersionID:   10,
		SystemKey:   "mozhan",
		PageKey:     "/posts/{id}",
		AccountRole: "author",
		Recipe:      map[string]any{"selector": "#post-title"},
	})
	if err != nil {
		t.Fatalf("OnPageLocated: %v", err)
	}
	if item.Status != knowledge.StatusApproved {
		t.Fatalf("status = %q, want approved", item.Status)
	}
	if f.repo.insertedItem.Status != knowledge.StatusDraft || f.repo.statusItem.Status != knowledge.StatusApproved {
		t.Fatalf("insert status = %q final status = %q, want draft then approved",
			f.repo.insertedItem.Status, f.repo.statusItem.Status)
	}
	if item.Bindings.PageKey == nil || *item.Bindings.PageKey != "/posts/{id}" {
		t.Fatalf("bindings = %+v, want page_key=/posts/{id}", item.Bindings)
	}
	if item.Bindings.APIKey != nil {
		t.Fatalf("bindings = %+v, want no api binding", item.Bindings)
	}
	if item.Bindings.AccountRole == nil || *item.Bindings.AccountRole != "author" {
		t.Fatalf("bindings = %+v, want account_role=author", item.Bindings)
	}
	if !hasTag(item.Tags, "kind:page_locator") {
		t.Fatalf("tags = %v, want kind:page_locator", item.Tags)
	}
	if content := decodeItemContent(t, item); content["kind"] != "page_locator" {
		t.Fatalf("content kind = %v", content["kind"])
	}
}

func TestAPIAuthSuccessCreatesDraftAndAutoApproves(t *testing.T) {
	f := newFixture(t)
	item, err := f.svc.OnAPIAuthSuccess(context.Background(), knowledgeapp.SuccessFact{
		VersionID:   10,
		SystemKey:   "mozhan",
		APIKey:      "POST /api/v1/login",
		AccountRole: "admin",
		Recipe:      map[string]any{"header": "Authorization", "scheme": "bearer"},
	})
	if err != nil {
		t.Fatalf("OnAPIAuthSuccess: %v", err)
	}
	if item.Status != knowledge.StatusApproved {
		t.Fatalf("status = %q, want approved", item.Status)
	}
	if f.repo.insertedItem.Status != knowledge.StatusDraft || f.repo.statusItem.Status != knowledge.StatusApproved {
		t.Fatalf("insert status = %q final status = %q, want draft then approved",
			f.repo.insertedItem.Status, f.repo.statusItem.Status)
	}
	if item.Bindings.APIKey == nil || *item.Bindings.APIKey != "POST /api/v1/login" {
		t.Fatalf("bindings = %+v, want api_key=POST /api/v1/login", item.Bindings)
	}
	if !hasTag(item.Tags, "kind:api_auth") {
		t.Fatalf("tags = %v, want kind:api_auth", item.Tags)
	}
	if content := decodeItemContent(t, item); content["kind"] != "api_auth" {
		t.Fatalf("content kind = %v", content["kind"])
	}
}

func TestSuccessHookRequiresKindBinding(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.OnLoginSuccess(context.Background(), knowledgeapp.SuccessFact{VersionID: 10}); err == nil {
		t.Fatal("OnLoginSuccess accepted a fact without an account role")
	} else if apiErr := platform.AsError(err); apiErr.Status != 400 {
		t.Fatalf("error = %+v, want 400", apiErr)
	}
	if f.repo.insertCalls != 0 || len(f.audit.calls) != 0 || len(f.events.events) != 0 {
		t.Fatalf("rejected fact wrote side effects: insert=%d audit=%v events=%v",
			f.repo.insertCalls, f.audit.calls, f.events.events)
	}
}

func TestAutoApproveLowRisk(t *testing.T) {
	for _, kind := range []knowledgeapp.Kind{
		knowledgeapp.KindLoginRecipe, knowledgeapp.KindPageLocator, knowledgeapp.KindAPIAuth,
	} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFixture(t)
			item := draftKindItem(42, kind)
			f.repo.items[42] = item
			if !f.svc.AutoApprove(context.Background(), item) {
				t.Fatalf("AutoApprove(%s) = false, want true", kind)
			}
			if f.repo.statusItem.ID != item.ID || f.repo.statusItem.Status != knowledge.StatusApproved ||
				f.repo.statusFrom != knowledge.StatusDraft {
				t.Fatalf("persisted status = %+v from %q, want approved from draft",
					f.repo.statusItem, f.repo.statusFrom)
			}
			audit, ok := findAudit(f.audit.calls, knowledgeapp.ActionAutoApproved)
			if !ok {
				t.Fatalf("audit = %v, want %q", f.audit.calls, knowledgeapp.ActionAutoApproved)
			}
			if audit.targetID != item.ID {
				t.Fatalf("audit target = %d, want %d", audit.targetID, item.ID)
			}
			if audit.metadata["kind"] != string(kind) || audit.metadata["scope"] != string(knowledge.ScopeSystem) {
				t.Fatalf("audit metadata = %#v", audit.metadata)
			}
			if _, ok := audit.metadata["knowledge_id"]; !ok {
				t.Fatalf("audit metadata = %#v, want knowledge_id", audit.metadata)
			}
			for _, forbidden := range []string{"title", "content", "recipe", "bindings"} {
				if _, ok := audit.metadata[forbidden]; ok {
					t.Fatalf("audit metadata carries %q: %#v", forbidden, audit.metadata)
				}
			}
			event, ok := findEvent(f.events.events, knowledgeapp.EventAutoApproved)
			if !ok {
				t.Fatalf("events = %v, want %q", f.events.events, knowledgeapp.EventAutoApproved)
			}
			if event.Channel != "system" {
				t.Fatalf("event channel = %q, want system", event.Channel)
			}
			if event.Payload["kind"] != string(kind) || event.Payload["scope"] != string(knowledge.ScopeSystem) {
				t.Fatalf("event payload = %#v", event.Payload)
			}
			if _, ok := event.Payload["knowledge_id"]; !ok {
				t.Fatalf("event payload = %#v, want knowledge_id", event.Payload)
			}
			assertNoSecret(t, "auto approve audit/event", fmt.Sprint(f.audit.calls)+fmt.Sprint(f.events.events))
		})
	}

	t.Run("refuses unknown kind", func(t *testing.T) {
		f := newFixture(t)
		item := draftItem(42, 1)
		item.Tags = []string{"kind:case_pattern"}
		f.repo.items[42] = item
		if f.svc.AutoApprove(context.Background(), item) {
			t.Fatal("AutoApprove approved an unknown knowledge kind")
		}
		if len(f.audit.calls) != 0 || len(f.events.events) != 0 {
			t.Fatalf("refusal wrote audit/events: %v %v", f.audit.calls, f.events.events)
		}
	})

	t.Run("refuses item without kind", func(t *testing.T) {
		f := newFixture(t)
		item := draftItem(42, 1)
		f.repo.items[42] = item
		if f.svc.AutoApprove(context.Background(), item) {
			t.Fatal("AutoApprove approved an item without a knowledge kind tag")
		}
	})

	t.Run("refuses non draft item", func(t *testing.T) {
		f := newFixture(t)
		item := approvedItem(42, knowledge.ScopeGlobal, "")
		item.Tags = []string{"kind:login_recipe"}
		f.repo.items[42] = item
		if f.svc.AutoApprove(context.Background(), item) {
			t.Fatal("AutoApprove re-approved an already approved item")
		}
	})
}

func TestNoSecretInKnowledge(t *testing.T) {
	assertNoWrites := func(t *testing.T, f *fixture) {
		t.Helper()
		if f.repo.insertCalls != 0 {
			t.Fatalf("secret fact was persisted (insert calls = %d)", f.repo.insertCalls)
		}
		if len(f.audit.calls) != 0 || len(f.events.events) != 0 {
			t.Fatalf("secret fact wrote audit/events: %v %v", f.audit.calls, f.events.events)
		}
	}
	assertNoAutoApproval := func(t *testing.T, f *fixture) {
		t.Helper()
		if f.repo.statusItem.ID != 0 {
			t.Fatalf("refused auto approval wrote the status transition: %+v", f.repo.statusItem)
		}
		if _, ok := findAudit(f.audit.calls, knowledgeapp.ActionAutoApproved); ok {
			t.Fatal("refused auto approval wrote knowledge.auto_approved audit")
		}
		if _, ok := findEvent(f.events.events, knowledgeapp.EventAutoApproved); ok {
			t.Fatal("refused auto approval published knowledge.auto_approved")
		}
	}

	t.Run("rejects password field", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.Recipe = map[string]any{"password": "s3cret"}
		_, err := f.svc.OnLoginSuccess(context.Background(), fact)
		assertSecretRejected(t, "password recipe field", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects nested authorization header value", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		// The kind binding must be valid so the failure is the secret scan, not a
		// missing api_key (a vacuous rejection).
		fact.APIKey = "POST /api/v1/login"
		fact.Recipe = map[string]any{"headers": map[string]any{"Authorization": "Bearer abc123"}}
		_, err := f.svc.OnAPIAuthSuccess(context.Background(), fact)
		assertSecretRejected(t, "nested authorization header", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects raw bearer token under a generic recipe key", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.Recipe = map[string]any{"note": "Bearer abc123"}
		_, err := f.svc.OnLoginSuccess(context.Background(), fact)
		assertSecretRejected(t, "raw bearer token", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects raw jwt under a generic recipe key", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.Recipe = map[string]any{
			"note": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		}
		_, err := f.svc.OnLoginSuccess(context.Background(), fact)
		assertSecretRejected(t, "raw jwt", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects secret in page key", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.PageKey = "/login?password=hunter2"
		_, err := f.svc.OnLoginSuccess(context.Background(), fact)
		assertSecretRejected(t, "page key binding", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects raw bearer token in api key", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.APIKey = "Bearer abc123"
		_, err := f.svc.OnAPIAuthSuccess(context.Background(), fact)
		assertSecretRejected(t, "api key binding", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects secret in account role", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.AccountRole = "token=hunter2"
		_, err := f.svc.OnLoginSuccess(context.Background(), fact)
		assertSecretRejected(t, "account role binding", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects secret in system key", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.SystemKey = "password=s3cret"
		_, err := f.svc.OnLoginSuccess(context.Background(), fact)
		assertSecretRejected(t, "system key", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects raw bearer token in title", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.Title = "登录配方 Bearer abc123"
		_, err := f.svc.OnLoginSuccess(context.Background(), fact)
		assertSecretRejected(t, "title", err)
		assertNoWrites(t, f)
	})

	t.Run("rejects credential-looking text value", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.Recipe = map[string]any{"note": "password=hunter2"}
		_, err := f.svc.OnLoginSuccess(context.Background(), fact)
		assertSecretRejected(t, "embedded password assignment", err)
	})

	t.Run("allows credential placeholders", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.APIKey = "POST /api/v1/login"
		fact.Recipe = map[string]any{"headers": map[string]any{"Authorization": "Bearer {{access_token}}"}}
		item, err := f.svc.OnAPIAuthSuccess(context.Background(), fact)
		if err != nil {
			t.Fatalf("OnAPIAuthSuccess rejected a template placeholder: %v", err)
		}
		if item.Status != knowledge.StatusApproved {
			t.Fatalf("placeholder item status = %q, want approved", item.Status)
		}
		assertNoSecret(t, "placeholder hook", fmt.Sprint(f.audit.calls)+fmt.Sprint(f.events.events))
	})

	t.Run("auto approval refuses item with secret content", func(t *testing.T) {
		f := newFixture(t)
		item := draftItem(42, 1)
		item.Tags = []string{"kind:login_recipe"}
		item.Content = `{"kind":"login_recipe","recipe":{"password":"s3cret"}}`
		f.repo.items[42] = item
		if f.svc.AutoApprove(context.Background(), item) {
			t.Fatal("AutoApprove approved an item containing a secret")
		}
		assertNoAutoApproval(t, f)
	})

	t.Run("auto approval refuses secret in stored bindings", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*knowledge.Item)
		}{
			{"page_key", func(item *knowledge.Item) { item.Bindings.PageKey = ptr("Bearer abc123") }},
			{"api_key", func(item *knowledge.Item) { item.Bindings.APIKey = ptr("password=hunter2") }},
			{"account_role", func(item *knowledge.Item) { item.Bindings.AccountRole = ptr("token=s3cret") }},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				f := newFixture(t)
				item := draftKindItem(42, knowledgeapp.KindPageLocator)
				testCase.mutate(&item)
				f.repo.items[42] = item
				if f.svc.AutoApprove(context.Background(), item) {
					t.Fatalf("AutoApprove approved a credential in the stored %s binding", testCase.name)
				}
				assertNoAutoApproval(t, f)
			})
		}
	})

	t.Run("auto approval refuses secret in content bindings", func(t *testing.T) {
		f := newFixture(t)
		item := draftKindItem(42, knowledgeapp.KindPageLocator)
		item.Content = `{"kind":"page_locator","bindings":{"page_key":"Bearer abc123"}}`
		f.repo.items[42] = item
		if f.svc.AutoApprove(context.Background(), item) {
			t.Fatal("AutoApprove approved a credential duplicated in the stored content bindings")
		}
		assertNoAutoApproval(t, f)
	})

	t.Run("auto approval refuses raw bearer token in stored title", func(t *testing.T) {
		f := newFixture(t)
		item := draftKindItem(42, knowledgeapp.KindLoginRecipe)
		item.Title = "Bearer abc123"
		f.repo.items[42] = item
		if f.svc.AutoApprove(context.Background(), item) {
			t.Fatal("AutoApprove approved a raw bearer token in the stored title")
		}
		assertNoAutoApproval(t, f)
	})

	t.Run("auto approval refuses secret in stored system key", func(t *testing.T) {
		f := newFixture(t)
		item := draftKindItem(42, knowledgeapp.KindLoginRecipe)
		item.SystemKey = ptr("token=hunter2")
		f.repo.items[42] = item
		if f.svc.AutoApprove(context.Background(), item) {
			t.Fatal("AutoApprove approved an item with a credential in its system key")
		}
		assertNoAutoApproval(t, f)
	})

	t.Run("auto approval refuses a draft edited after create", func(t *testing.T) {
		f := newFixture(t)
		// The caller still holds the clean handle it read after create...
		created := draftKindItem(42, knowledgeapp.KindLoginRecipe)
		f.repo.items[42] = created
		// ...but the stored row was edited and now carries a credential.
		edited := created
		edited.Content = `{"kind":"login_recipe","recipe":{"password":"s3cret"}}`
		f.repo.items[42] = edited
		if f.svc.AutoApprove(context.Background(), created) {
			t.Fatal("AutoApprove approved a stale caller handle; the stored draft now contains a secret")
		}
		assertNoAutoApproval(t, f)
	})
}
