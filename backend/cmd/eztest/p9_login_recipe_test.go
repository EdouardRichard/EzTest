package main

// Task 67 (P8-T9): the P9 explore analysis logs in once before discovery. That
// real login success is the production trigger that sediments the approved
// login_recipe knowledge item, with the SUT/environment/login page/account and
// the recorded login_session fact as provenance. Sedimentation is best effort:
// a failing (or panicking) writer must never fail the login, abort discovery or
// change the analysis result, and a blocked login must not sediment anything.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/knowledgeadapter"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// recordingLoginRecipeWriter captures the login-success observations the
// analysis hands to the knowledge adapter. The real adapter's item creation is
// covered by TestLoginSuccessSedimentsRecipe in the knowledgeadapter package;
// this double proves the production trigger and its best-effort contract.
type recordingLoginRecipeWriter struct {
	mu    sync.Mutex
	facts []knowledgeadapter.LoginRecipeFact
	err   error
	panic bool
}

func (w *recordingLoginRecipeWriter) SedimentLoginRecipe(_ context.Context, fact knowledgeadapter.LoginRecipeFact) (knowledge.Item, error) {
	w.mu.Lock()
	w.facts = append(w.facts, fact)
	w.mu.Unlock()
	if w.panic {
		panic("login recipe sedimentation exploded")
	}
	if w.err != nil {
		return knowledge.Item{}, w.err
	}
	return knowledge.Item{ID: 1, Status: knowledge.StatusApproved}, nil
}

func (w *recordingLoginRecipeWriter) recorded() []knowledgeadapter.LoginRecipeFact {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]knowledgeadapter.LoginRecipeFact(nil), w.facts...)
}

// p9LoginRecipeVersionView is the version binding the login runs against: SUT
// 7001 (family shop), environment 8, a bound login page and the admin account.
func p9LoginRecipeVersionView() versionapp.VersionView {
	family := "shop"
	return versionapp.VersionView{
		ID: 1001,
		Bindings: []versionapp.BindingView{{
			ID:          77,
			SUT:         resourceapp.SUTView{ID: "7001", Name: "Shop", Family: &family},
			Environment: resourceapp.EnvironmentView{ID: "8", Name: "staging"},
			Pages: []resourceapp.PageView{
				{ID: "501", URL: "http://sut.test/login", RouteTemplate: "/login"},
				{ID: "502", URL: "http://sut.test/checkout", RouteTemplate: "/checkout"},
			},
			Accounts: []resourceapp.AccountView{{ID: "9", Roles: []string{"admin"}}},
		}},
	}
}

// p9ActiveLoginFact is one active explore login success with its credential-free
// recipe, as the Task 64 login flow returns it.
func p9ActiveLoginFact() exploreapp.SessionFact {
	return exploreapp.SessionFact{
		VersionID:     1001,
		VersionSUTID:  77,
		EnvironmentID: 8,
		AccountRole:   "admin",
		Status:        exploreapp.SessionActive,
		LoginURL:      "http://sut.test/login",
		Route:         "/login",
		FactID:        9001,
		Recipe: map[string]any{
			"mode":           "page",
			"login_url":      "http://sut.test/login",
			"route":          "/login",
			"username_field": "#username",
			"password_field": "#password",
			"submit":         "button[type=submit]",
			"steps": []any{
				map[string]any{"action": "open", "url": "http://sut.test/login"},
				map[string]any{"action": "fill", "field": "password", "selector": "#password"},
			},
		},
	}
}

// newP9LoginRecipeAnalysis builds the analysis over the existing P9 fakes. The
// login-recipe writer is attached through the same late setter the production
// composition uses (the P9 runtime is built before the P7 knowledge adapter).
func newP9LoginRecipeAnalysis(t *testing.T, runtime exploreapp.Runtime, writer p9LoginRecipeWriter) *p9Analysis {
	t.Helper()
	facts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{
		exploreapp.KindPage: {p9PageFact(1, "http://sut.test/checkout")},
		exploreapp.KindGap:  {p9GapFact(2)},
	}}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:  runtime,
		Pages:    &fakeP9Closer{},
		Facts:    facts,
		Recorder: facts,
		Requirements: &fakeP9Requirements{snapshots: []exploreapp.RequirementSnapshot{{
			ID: 101, VersionID: 10010, Title: "下单", Status: "clarified",
			Description: "用户下单", TargetPages: []string{"/checkout"},
		}}},
		Cases: &fakeP9Cases{cases: []exploreapp.CaseSnapshot{{
			CaseID: 301, CaseVersionID: 3001, RequirementID: 101, Title: "下单成功", Status: "confirmed",
			PageKeys: []string{"/checkout"},
		}}},
		Roles:    &fakeP9Roles{roles: []string{"admin"}},
		Versions: &fakeP9Versions{view: p9LoginRecipeVersionView()},
	})
	if err != nil {
		t.Fatalf("newP9Analysis: %v", err)
	}
	analysis.SetLoginRecipeWriter(writer)
	return analysis
}

func TestLoginSuccessSedimentsRecipeFromAnalysis(t *testing.T) {
	ctx := context.Background()

	t.Run("real login success sediments the recipe", func(t *testing.T) {
		loginFact := p9ActiveLoginFact()
		runtime := &fakeP9Runtime{loginQueue: []exploreapp.SessionFact{loginFact}}
		writer := &recordingLoginRecipeWriter{}
		analysis := newP9LoginRecipeAnalysis(t, runtime, writer)

		result, err := analysis.Refresh(ctx, 1001)
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if result.LoginStatus != exploreapp.SessionActive {
			t.Fatalf("login status = %q, want active", result.LoginStatus)
		}
		recorded := writer.recorded()
		if len(recorded) != 1 {
			t.Fatalf("sedimented %d login recipes, want 1", len(recorded))
		}
		fact := recorded[0]
		if fact.VersionID != 1001 || fact.VersionSUTID != 77 {
			t.Fatalf("version binding = %d/%d, want 1001/77", fact.VersionID, fact.VersionSUTID)
		}
		// The SUT/environment/page/account come from the version binding the
		// login ran against, so the knowledge item is bound to the real surface.
		if fact.SystemID != 7001 || fact.EnvironmentID != 8 || fact.SystemKey != "shop" {
			t.Fatalf("SUT binding = system %d env %d key %q, want 7001/8/shop", fact.SystemID, fact.EnvironmentID, fact.SystemKey)
		}
		if fact.PageKey != "/login" || fact.AccountRole != "admin" {
			t.Fatalf("page/account binding = %q/%q, want /login/admin", fact.PageKey, fact.AccountRole)
		}
		// The login_session fact recorded by the flow is the source evidence.
		if fact.SourceFactID != loginFact.FactID {
			t.Fatalf("source fact = %d, want the login_session fact %d", fact.SourceFactID, loginFact.FactID)
		}
		if fact.Recipe["login_url"] != "http://sut.test/login" || fact.Recipe["password_field"] != "#password" {
			t.Fatalf("recipe = %#v, want the credential-free login recipe", fact.Recipe)
		}
		// No credential enters the knowledge path: the recipe carries selectors,
		// never a password value or a captured storage state.
		if _, ok := fact.Recipe["password"]; ok {
			t.Fatalf("recipe carries a password value: %#v", fact.Recipe)
		}
		for _, secret := range []string{"storage_state", "hunter2", "s3cret"} {
			if strings.Contains(strings.ToLower(fact.Recipe["login_url"].(string)), secret) {
				t.Fatalf("recipe carried a secret %q", secret)
			}
		}
	})

	t.Run("a blocked login sediments nothing", func(t *testing.T) {
		writer := &recordingLoginRecipeWriter{}
		analysis := newP9LoginRecipeAnalysis(t, &fakeP9Runtime{blocked: true}, writer)
		result, err := analysis.Refresh(ctx, 1001)
		if err != nil {
			t.Fatalf("blocked Refresh: %v", err)
		}
		if result.LoginStatus != exploreapp.SessionBlocked {
			t.Fatalf("blocked login status = %q", result.LoginStatus)
		}
		if recorded := writer.recorded(); len(recorded) != 0 {
			t.Fatalf("a blocked login sedimented %d recipes", len(recorded))
		}
	})

	t.Run("an active login without a recipe is skipped", func(t *testing.T) {
		fact := p9ActiveLoginFact()
		fact.Recipe = nil
		writer := &recordingLoginRecipeWriter{}
		analysis := newP9LoginRecipeAnalysis(t, &fakeP9Runtime{loginQueue: []exploreapp.SessionFact{fact}}, writer)
		if _, err := analysis.Refresh(ctx, 1001); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if recorded := writer.recorded(); len(recorded) != 0 {
			t.Fatalf("a recipe-less login sedimented %d recipes", len(recorded))
		}
	})

	// Best effort: sedimentation failure (or a panic) is logged, never fatal to
	// the login, the discovery pre-pass or the analysis result.
	for _, tc := range []struct {
		name  string
		build func() *recordingLoginRecipeWriter
	}{
		{name: "writer error", build: func() *recordingLoginRecipeWriter {
			return &recordingLoginRecipeWriter{err: errors.New("knowledge store unavailable")}
		}},
		{name: "writer panic", build: func() *recordingLoginRecipeWriter {
			return &recordingLoginRecipeWriter{panic: true}
		}},
	} {
		t.Run(tc.name+" never fails the login", func(t *testing.T) {
			runtime := &fakeP9Runtime{loginQueue: []exploreapp.SessionFact{p9ActiveLoginFact()}}
			writer := tc.build()
			analysis := newP9LoginRecipeAnalysis(t, runtime, writer)
			result, err := analysis.Refresh(ctx, 1001)
			if err != nil {
				t.Fatalf("Refresh failed on a best-effort sedimentation error: %v", err)
			}
			if result.LoginStatus != exploreapp.SessionActive {
				t.Fatalf("login status = %q, want active despite the sediment failure", result.LoginStatus)
			}
			// Discovery still ran with the session the login produced.
			if len(runtime.opens) == 0 {
				t.Fatal("discovery did not run after a sediment failure")
			}
			if len(writer.recorded()) != 1 {
				t.Fatalf("sediment attempts = %d, want 1", len(writer.recorded()))
			}
		})
	}
}
