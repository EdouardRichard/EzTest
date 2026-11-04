package knowledgeadapter

// Task 67 (P8-T9): a real login success observed by the P9 explore login flow
// must sediment an approved login_recipe knowledge item bound to the version's
// SUT/environment/login page/account, carrying the login_session fact as source
// provenance and never a credential. Repeating the same observation is
// idempotent: the recipe key (SUT + environment + account role + recipe) maps to
// one item, so periodic exploration logins do not clone knowledge.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
)

// newLoginRecipeAdapter wires the real knowledge application service (success
// hook + auto-approval) over the real repository contract, so the test observes
// the stored item, not a mock.
func newLoginRecipeAdapter() (*Adapter, *memoryKnowledgeRepository, *recordingKnowledgeAudit) {
	repo := newMemoryKnowledgeRepository()
	audit := &recordingKnowledgeAudit{}
	service := knowledgeapp.NewService(
		repo, nil, nil, audit, nil, nil,
		fixedKnowledgeClock{}, &sequenceKnowledgeIDs{next: 7000},
	)
	return New(service, nil), repo, audit
}

// loginRecipeFactFixture is the credential-free observation the P9 analysis
// projects from one active explore SessionFact.
func loginRecipeFactFixture() LoginRecipeFact {
	return LoginRecipeFact{
		VersionID:     1001,
		VersionSUTID:  77,
		SystemID:      7001,
		EnvironmentID: 7101,
		SystemKey:     "shop",
		AccountRole:   "admin",
		PageKey:       "/login",
		Recipe: map[string]any{
			"mode":           "page",
			"login_url":      "http://sut.test/login",
			"route":          "/login",
			"username_field": "#username",
			"password_field": "#password",
			"submit":         "button[type=submit]",
			"steps": []any{
				map[string]any{"action": "open", "url": "http://sut.test/login"},
				map[string]any{"action": "fill", "field": "username", "selector": "#username"},
				map[string]any{"action": "fill", "field": "password", "selector": "#password"},
				map[string]any{"action": "click", "selector": "button[type=submit]"},
			},
		},
		SourceFactID: 9001,
	}
}

func TestLoginSuccessSedimentsRecipe(t *testing.T) {
	ctx := context.Background()
	adapter, repo, audit := newLoginRecipeAdapter()
	fact := loginRecipeFactFixture()

	item, err := adapter.SedimentLoginRecipe(ctx, fact)
	if err != nil {
		t.Fatalf("SedimentLoginRecipe: %v", err)
	}
	if item.Status != knowledge.StatusApproved {
		t.Fatalf("item status = %q, want approved", item.Status)
	}
	if kind, ok := knowledgeapp.KindOf(item); !ok || kind != knowledgeapp.KindLoginRecipe {
		t.Fatalf("item kind = %q ok=%v, want login_recipe (tags=%v)", kind, ok, item.Tags)
	}
	// Bound to the SUT, environment, login page and account role of the version
	// binding the login ran against.
	if item.Scope != knowledge.ScopeSystem || item.SystemKey == nil || *item.SystemKey != "shop" {
		t.Fatalf("item scope = %q system key = %v, want system/shop", item.Scope, item.SystemKey)
	}
	bindings := item.Bindings
	if bindings.SystemID == nil || *bindings.SystemID != 7001 {
		t.Fatalf("system binding = %v, want 7001", bindings.SystemID)
	}
	if bindings.EnvironmentID == nil || *bindings.EnvironmentID != 7101 {
		t.Fatalf("environment binding = %v, want 7101", bindings.EnvironmentID)
	}
	if bindings.PageKey == nil || *bindings.PageKey != "/login" {
		t.Fatalf("page binding = %v, want /login", bindings.PageKey)
	}
	if bindings.AccountRole == nil || *bindings.AccountRole != "admin" {
		t.Fatalf("account role binding = %v, want admin", bindings.AccountRole)
	}
	if bindings.APIKey != nil {
		t.Fatalf("unexpected api binding: %v", *bindings.APIKey)
	}
	if item.SourceVersionID == nil || *item.SourceVersionID != fact.VersionID {
		t.Fatalf("source version = %v, want %d", item.SourceVersionID, fact.VersionID)
	}

	// The structured content carries the login_session fact as source evidence
	// and repeats the credential-free recipe (selectors, not values).
	var payload struct {
		Kind         string         `json:"kind"`
		Recipe       map[string]any `json:"recipe"`
		SourceFactID string         `json:"source_fact_id"`
		Bindings     struct {
			SystemID      int64  `json:"system_id"`
			EnvironmentID int64  `json:"environment_id"`
			PageKey       string `json:"page_key"`
			AccountRole   string `json:"account_role"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal([]byte(item.Content), &payload); err != nil {
		t.Fatalf("content is not JSON: %v (%s)", err, item.Content)
	}
	if payload.Kind != string(knowledgeapp.KindLoginRecipe) {
		t.Fatalf("content kind = %q, want login_recipe", payload.Kind)
	}
	if payload.SourceFactID != "9001" {
		t.Fatalf("content source_fact_id = %q, want 9001", payload.SourceFactID)
	}
	if payload.Recipe["login_url"] != "http://sut.test/login" ||
		payload.Recipe["username_field"] != "#username" ||
		payload.Recipe["password_field"] != "#password" {
		t.Fatalf("content recipe = %#v", payload.Recipe)
	}
	// A recipe never persists a credential value: the password selector is the
	// knowledge, the account secret stays in the resource store.
	if _, ok := payload.Recipe["password"]; ok {
		t.Fatalf("content recipe carries a password field: %#v", payload.Recipe)
	}
	for _, secret := range []string{"hunter2", "s3cret", "Bearer eyJ"} {
		if strings.Contains(item.Content, secret) {
			t.Fatalf("content leaked a credential %q: %s", secret, item.Content)
		}
	}

	// Idempotent: the same SUT/account/recipe key never sediments a second item
	// when the analysis logs in again on its next refresh.
	again, err := adapter.SedimentLoginRecipe(ctx, fact)
	if err != nil {
		t.Fatalf("SedimentLoginRecipe (repeat): %v", err)
	}
	if again.ID != item.ID {
		t.Fatalf("repeat sedimented a new item %d, want the existing %d", again.ID, item.ID)
	}
	if stored := repo.allItems(); len(stored) != 1 {
		t.Fatalf("stored items = %d after a repeated login, want 1", len(stored))
	}
	if inserted := repo.insertedItems(); len(inserted) != 1 {
		t.Fatalf("inserted items = %d after a repeated login, want 1", len(inserted))
	}
	// The service's low-risk auto-approval wrote its audit entry once.
	if approved := audit.byAction(knowledgeapp.ActionAutoApproved); len(approved) != 1 {
		t.Fatalf("auto-approved audit rows = %d, want 1", len(approved))
	}

	// A different login recipe (route/URL changed) is a different key and
	// sediments its own item instead of overwriting or cloning silently.
	changed := loginRecipeFactFixture()
	changed.Recipe["login_url"] = "http://sut.test/auth/signin"
	changed.Recipe["route"] = "/auth/signin"
	changed.PageKey = "/auth/signin"
	changedItem, err := adapter.SedimentLoginRecipe(ctx, changed)
	if err != nil {
		t.Fatalf("SedimentLoginRecipe (changed recipe): %v", err)
	}
	if changedItem.ID == item.ID {
		t.Fatalf("a changed recipe reused item %d instead of sedimenting its own", item.ID)
	}
	if stored := repo.allItems(); len(stored) != 2 {
		t.Fatalf("stored items = %d, want 2 after a changed recipe", len(stored))
	}

	// A credential smuggled into the recipe is rejected before any item is
	// written; the hook stays best effort and returns the error.
	secretFact := loginRecipeFactFixture()
	secretFact.Recipe["password"] = "hunter2"
	before := len(repo.insertedItems())
	if _, err := adapter.SedimentLoginRecipe(ctx, secretFact); err == nil {
		t.Fatal("SedimentLoginRecipe accepted a recipe carrying a credential")
	}
	if after := len(repo.insertedItems()); after != before {
		t.Fatalf("a rejected secret recipe inserted %d item(s)", after-before)
	}

	// The SUT binding is required: without it the recipe would be a global item
	// that can inject into unrelated systems, so the hook fails closed.
	unbound := loginRecipeFactFixture()
	unbound.SystemID = 0
	if _, err := adapter.SedimentLoginRecipe(ctx, unbound); err == nil {
		t.Fatal("SedimentLoginRecipe accepted a fact without a SUT binding")
	}
}
