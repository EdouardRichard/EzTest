//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// authFlowTestDefinition returns a complete form recipe.
func authFlowTestDefinition() domain.FormDefinition {
	return domain.FormDefinition{
		LoginURL:         "https://app.test/login",
		UsernameSelector: "#username",
		PasswordSelector: "#password",
		SubmitSelector:   "button[type=submit]",
		Success:          domain.SuccessRule{URLContains: "/home"},
		Failure: domain.FailureSelectors{
			MFASelector:     "#mfa",
			CaptchaSelector: "#captcha",
			InvalidSelector: ".login-error",
		},
		TimeoutMS: 20000,
	}
}

// TestAuthFlowRepositoryCRUD walks one recipe through create, read, list,
// update and delete, and proves the access.login_flow_id foreign key clears on
// recipe deletion.
func TestAuthFlowRepositoryCRUD(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	company := seedCatalogCompany(t, ctx, pool)
	sysOne := accessSeedSystem(t, ctx, pool, company.ID, "flow-one")
	sysTwo := accessSeedSystem(t, ctx, pool, company.ID, "flow-two")

	generator, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("new snowflake generator: %v", err)
	}
	flows := postgres.NewAuthFlowRepository(pool, generator)

	flow := &domain.AuthFlow{
		SystemID:   sysOne,
		Name:       "fixture login",
		Kind:       domain.AuthFlowKindForm,
		Definition: authFlowTestDefinition(),
		Enabled:    true,
	}
	if err := flows.CreateAuthFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
	if flow.ID <= 0 {
		t.Fatalf("created flow has id=%d, want a positive snowflake", flow.ID)
	}
	if flow.CreatedAt.IsZero() || flow.UpdatedAt.IsZero() {
		t.Fatalf("created timestamps=%v/%v", flow.CreatedAt, flow.UpdatedAt)
	}

	loaded, err := flows.GetAuthFlow(ctx, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SystemID != flow.SystemID || loaded.Name != flow.Name ||
		loaded.Kind != flow.Kind || loaded.Enabled != flow.Enabled ||
		!reflect.DeepEqual(loaded.Definition, flow.Definition) {
		t.Fatalf("loaded=%+v\nwant %+v", *loaded, *flow)
	}

	duplicate := &domain.AuthFlow{
		SystemID: sysOne, Name: flow.Name, Kind: domain.AuthFlowKindForm,
		Definition: authFlowTestDefinition(), Enabled: true,
	}
	err = flows.CreateAuthFlow(ctx, duplicate)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindConflict || domainErr.Reason != "duplicate_key" {
		t.Fatalf("duplicate error=%v want a duplicate_key conflict", err)
	}

	second := &domain.AuthFlow{
		SystemID: sysTwo, Name: "second login", Kind: domain.AuthFlowKindForm,
		Definition: authFlowTestDefinition(), Enabled: false,
	}
	if err := flows.CreateAuthFlow(ctx, second); err != nil {
		t.Fatal(err)
	}

	all, more, err := flows.ListAuthFlows(ctx, domain.AuthFlowFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if more || len(all) != 2 || all[0].ID != flow.ID || all[1].ID != second.ID {
		t.Fatalf("all=%+v more=%v want both flows in id order", all, more)
	}
	oneSystem, more, err := flows.ListAuthFlows(ctx, domain.AuthFlowFilter{
		SystemID: sysOne, Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if more || len(oneSystem) != 1 || oneSystem[0].ID != flow.ID {
		t.Fatalf("system page=%+v more=%v", oneSystem, more)
	}
	firstPage, more, err := flows.ListAuthFlows(ctx, domain.AuthFlowFilter{Page: domain.Page{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(firstPage) != 1 || firstPage[0].ID != flow.ID {
		t.Fatalf("first page=%+v more=%v", firstPage, more)
	}
	rest, more, err := flows.ListAuthFlows(ctx, domain.AuthFlowFilter{
		Page: domain.Page{Limit: 1, AfterID: firstPage[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if more || len(rest) != 1 || rest[0].ID != second.ID {
		t.Fatalf("rest=%+v more=%v", rest, more)
	}

	updatedDefinition := flow.Definition
	updatedDefinition.TimeoutMS = 45000
	flow.Name = "renamed login"
	flow.Definition = updatedDefinition
	flow.Enabled = false
	if err := flows.UpdateAuthFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
	reloaded, err := flows.GetAuthFlow(ctx, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Name != "renamed login" || reloaded.Enabled ||
		reloaded.Definition.TimeoutMS != 45000 || reloaded.UpdatedAt.Before(loaded.UpdatedAt) {
		t.Fatalf("reloaded=%+v want the update applied and updated_at refreshed", *reloaded)
	}

	// Seed an access that selects this recipe, then prove ON DELETE SET NULL.
	var accountID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO account (company_id, key, label, kind) VALUES ($1, 'flow-user', 'Flow user', 'human') RETURNING id`,
		company.ID).Scan(&accountID); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	var accessID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO account_system_access (account_id, system_id, auth_type, concurrency_limit)
		 VALUES ($1, $2, 'password', 1) RETURNING id`, accountID, sysOne).Scan(&accessID); err != nil {
		t.Fatalf("seed access: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE account_system_access SET login_flow_id = $1 WHERE id = $2`, flow.ID, accessID); err != nil {
		t.Fatalf("link access to auth flow: %v", err)
	}
	if err := flows.DeleteAuthFlow(ctx, flow.ID); err != nil {
		t.Fatal(err)
	}
	var loginFlowID *int64
	if err := pool.QueryRow(ctx,
		`SELECT login_flow_id FROM account_system_access WHERE id = $1`, accessID).Scan(&loginFlowID); err != nil {
		t.Fatalf("read login_flow_id: %v", err)
	}
	if loginFlowID != nil {
		t.Fatalf("login_flow_id=%d, want NULL after deleting the auth flow", *loginFlowID)
	}

	if _, err := flows.GetAuthFlow(ctx, flow.ID); err == nil {
		t.Fatal("a deleted auth flow is still readable")
	}
	if err := flows.DeleteAuthFlow(ctx, flow.ID); err == nil {
		t.Fatal("deleting a missing auth flow must fail")
	}
}
