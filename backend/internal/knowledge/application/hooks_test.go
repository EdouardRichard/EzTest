package knowledgeapp_test

import (
	"context"
	"encoding/json"
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

/* ---------------------------------- tests --------------------------------- */

func TestLoginSuccessCreatesDraft(t *testing.T) {
	f := newFixture(t)
	item, err := f.svc.OnLoginSuccess(context.Background(), testSuccessFact())
	if err != nil {
		t.Fatalf("OnLoginSuccess: %v", err)
	}

	if item.Status != knowledge.StatusDraft {
		t.Fatalf("status = %q, want draft (hooks create drafts; AutoApprove approves)", item.Status)
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

	// The hook persists a draft revision through the normal Create path.
	if f.repo.insertCalls != 1 {
		t.Fatalf("insert calls = %d, want 1", f.repo.insertCalls)
	}
	if f.repo.insertedItem.Status != knowledge.StatusDraft {
		t.Fatalf("persisted status = %q, want draft", f.repo.insertedItem.Status)
	}
	if f.repo.insertedRevision.KnowledgeID != item.ID || f.repo.insertedRevision.Revision != 1 {
		t.Fatalf("revision = %+v", f.repo.insertedRevision)
	}
	if _, ok := findAudit(f.audit.calls, knowledgeapp.ActionCreate); !ok {
		t.Fatalf("audit = %v, want knowledge.create", f.audit.calls)
	}
	if _, ok := findEvent(f.events.events, "knowledge.proposed"); !ok {
		t.Fatalf("events = %v, want knowledge.proposed", f.events.events)
	}
	assertNoSecret(t, "hook audit/events", fmt.Sprint(f.audit.calls)+fmt.Sprint(f.events.events))
}

func TestPageLocatedCreatesDraft(t *testing.T) {
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
	if item.Status != knowledge.StatusDraft {
		t.Fatalf("status = %q, want draft", item.Status)
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

func TestAPIAuthSuccessCreatesDraft(t *testing.T) {
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
	if item.Status != knowledge.StatusDraft {
		t.Fatalf("status = %q, want draft", item.Status)
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
	cases := []struct {
		name string
		kind knowledgeapp.Kind
		fact knowledgeapp.SuccessFact
		run  func(*fixture, knowledgeapp.SuccessFact) (knowledge.Item, error)
	}{
		{
			name: "login_recipe",
			kind: knowledgeapp.KindLoginRecipe,
			run: func(f *fixture, fact knowledgeapp.SuccessFact) (knowledge.Item, error) {
				return f.svc.OnLoginSuccess(context.Background(), fact)
			},
			fact: testSuccessFact(),
		},
		{
			name: "page_locator",
			kind: knowledgeapp.KindPageLocator,
			run: func(f *fixture, fact knowledgeapp.SuccessFact) (knowledge.Item, error) {
				return f.svc.OnPageLocated(context.Background(), fact)
			},
			fact: knowledgeapp.SuccessFact{VersionID: 10, SystemKey: "mozhan", PageKey: "/login"},
		},
		{
			name: "api_auth",
			kind: knowledgeapp.KindAPIAuth,
			fact: knowledgeapp.SuccessFact{VersionID: 10, SystemKey: "mozhan", APIKey: "POST /api/v1/login", AccountRole: "admin"},
			run: func(f *fixture, fact knowledgeapp.SuccessFact) (knowledge.Item, error) {
				return f.svc.OnAPIAuthSuccess(context.Background(), fact)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			item, err := testCase.run(f, testCase.fact)
			if err != nil {
				t.Fatalf("hook: %v", err)
			}
			if !f.svc.AutoApprove(context.Background(), item) {
				t.Fatalf("AutoApprove(%s) = false, want true", testCase.name)
			}
			if f.repo.statusItem.Status != knowledge.StatusApproved || f.repo.statusFrom != knowledge.StatusDraft {
				t.Fatalf("persisted status = %q from %q, want approved from draft", f.repo.statusItem.Status, f.repo.statusFrom)
			}
			audit, ok := findAudit(f.audit.calls, knowledgeapp.ActionAutoApproved)
			if !ok {
				t.Fatalf("audit = %v, want %q", f.audit.calls, knowledgeapp.ActionAutoApproved)
			}
			if audit.targetID != item.ID {
				t.Fatalf("audit target = %d, want %d", audit.targetID, item.ID)
			}
			if audit.metadata["kind"] != string(testCase.kind) || audit.metadata["scope"] != string(knowledge.ScopeSystem) {
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
			if event.Payload["kind"] != string(testCase.kind) || event.Payload["scope"] != string(knowledge.ScopeSystem) {
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
	t.Run("rejects password field", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.Recipe = map[string]any{"password": "s3cret"}
		if _, err := f.svc.OnLoginSuccess(context.Background(), fact); err == nil {
			t.Fatal("OnLoginSuccess accepted a password field")
		} else if apiErr := platform.AsError(err); apiErr.Status != 400 {
			t.Fatalf("error = %+v, want 400", apiErr)
		}
		if f.repo.insertCalls != 0 {
			t.Fatalf("secret fact was persisted (insert calls = %d)", f.repo.insertCalls)
		}
		if len(f.audit.calls) != 0 || len(f.events.events) != 0 {
			t.Fatalf("secret fact wrote audit/events: %v %v", f.audit.calls, f.events.events)
		}
	})

	t.Run("rejects nested authorization header value", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.Recipe = map[string]any{"headers": map[string]any{"Authorization": "Bearer abc123"}}
		if _, err := f.svc.OnAPIAuthSuccess(context.Background(), fact); err == nil {
			t.Fatal("OnAPIAuthSuccess accepted an authorization header value")
		}
		if f.repo.insertCalls != 0 {
			t.Fatalf("secret fact was persisted (insert calls = %d)", f.repo.insertCalls)
		}
	})

	t.Run("rejects credential-looking text value", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.Recipe = map[string]any{"note": "password=hunter2"}
		if _, err := f.svc.OnLoginSuccess(context.Background(), fact); err == nil {
			t.Fatal("OnLoginSuccess accepted an embedded password assignment")
		}
	})

	t.Run("allows credential placeholders", func(t *testing.T) {
		f := newFixture(t)
		fact := testSuccessFact()
		fact.APIKey = "POST /api/v1/login"
		fact.Recipe = map[string]any{"headers": map[string]any{"Authorization": "Bearer {{access_token}}"}}
		if _, err := f.svc.OnAPIAuthSuccess(context.Background(), fact); err != nil {
			t.Fatalf("OnAPIAuthSuccess rejected a template placeholder: %v", err)
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
		if _, ok := findAudit(f.audit.calls, knowledgeapp.ActionAutoApproved); ok {
			t.Fatal("refused auto approval wrote knowledge.auto_approved audit")
		}
	})
}
