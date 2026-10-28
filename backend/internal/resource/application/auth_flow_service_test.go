package application

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeAuthFlowRepo is an in-memory domain.AuthFlowRepository. Like the postgres
// implementation it mints the row id on create.
type fakeAuthFlowRepo struct {
	flows       map[int64]domain.AuthFlow
	nextID      int64
	createCalls int
	updateCalls int
	deleteCalls int
}

func newFakeAuthFlowRepo() *fakeAuthFlowRepo {
	return &fakeAuthFlowRepo{flows: map[int64]domain.AuthFlow{}, nextID: 1}
}

func (r *fakeAuthFlowRepo) seed(flow domain.AuthFlow) domain.AuthFlow {
	if flow.ID == 0 {
		flow.ID = r.nextID
	}
	if flow.ID >= r.nextID {
		r.nextID = flow.ID + 1
	}
	r.flows[flow.ID] = flow
	return flow
}

func (r *fakeAuthFlowRepo) CreateAuthFlow(_ context.Context, f *domain.AuthFlow) error {
	f.ID = r.nextID
	r.nextID++
	r.createCalls++
	r.flows[f.ID] = *f
	return nil
}

func (r *fakeAuthFlowRepo) GetAuthFlow(_ context.Context, id int64) (*domain.AuthFlow, error) {
	flow, ok := r.flows[id]
	if !ok {
		return nil, domain.NewNotFound("auth flow", id)
	}
	return &flow, nil
}

func (r *fakeAuthFlowRepo) ListAuthFlows(_ context.Context, f domain.AuthFlowFilter) ([]domain.AuthFlow, bool, error) {
	ids := make([]int64, 0, len(r.flows))
	for id := range r.flows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	matches := make([]domain.AuthFlow, 0, len(ids))
	for _, id := range ids {
		flow := r.flows[id]
		if flow.ID <= f.Page.AfterID {
			continue
		}
		if f.SystemID != 0 && flow.SystemID != f.SystemID {
			continue
		}
		matches = append(matches, flow)
	}
	limit := f.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

func (r *fakeAuthFlowRepo) UpdateAuthFlow(_ context.Context, f *domain.AuthFlow) error {
	if _, ok := r.flows[f.ID]; !ok {
		return domain.NewNotFound("auth flow", f.ID)
	}
	r.updateCalls++
	r.flows[f.ID] = *f
	return nil
}

func (r *fakeAuthFlowRepo) DeleteAuthFlow(_ context.Context, id int64) error {
	if _, ok := r.flows[id]; !ok {
		return domain.NewNotFound("auth flow", id)
	}
	r.deleteCalls++
	delete(r.flows, id)
	return nil
}

// authFlowTestEnv bundles the fakes behind one AuthFlowService.
type authFlowTestEnv struct {
	flows   *fakeAuthFlowRepo
	systems *fakeAccessSystemRepo
	service *AuthFlowService
}

// newAuthFlowTestEnv seeds system 1 and wires the service.
func newAuthFlowTestEnv(t *testing.T) *authFlowTestEnv {
	t.Helper()
	flows := newFakeAuthFlowRepo()
	systems := newFakeAccessSystemRepo()
	if err := systems.CreateSystem(context.Background(), &domain.System{Key: "fixture", Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	return &authFlowTestEnv{
		flows:   flows,
		systems: systems,
		service: NewAuthFlowService(flows, systems),
	}
}

// createInput returns a valid create request for system 1.
func createAuthFlowInput() CreateAuthFlowInput {
	return CreateAuthFlowInput{
		SystemID: 1,
		Name:     "fixture login",
		Definition: domain.FormDefinition{
			LoginURL:         "https://app.test/login",
			UsernameSelector: "#username",
			PasswordSelector: "#password",
			SubmitSelector:   "button[type=submit]",
			Success:          domain.SuccessRule{URLContains: "/home"},
			TimeoutMS:        20000,
		},
		Enabled: true,
	}
}

func TestAuthFlowServiceCreateStoresValidFlow(t *testing.T) {
	env := newAuthFlowTestEnv(t)

	flow, err := env.service.Create(context.Background(), createAuthFlowInput())
	if err != nil {
		t.Fatal(err)
	}
	if flow.ID == 0 || flow.Kind != domain.AuthFlowKindForm || !flow.Enabled {
		t.Fatalf("created flow=%+v", flow)
	}
	if flow.SystemID != 1 || flow.Name != "fixture login" {
		t.Fatalf("created flow=%+v", flow)
	}
	if env.flows.createCalls != 1 {
		t.Fatalf("create calls=%d", env.flows.createCalls)
	}
	stored := env.flows.flows[flow.ID]
	if stored.Definition.LoginURL != "https://app.test/login" {
		t.Fatalf("stored definition=%+v", stored.Definition)
	}
}

func TestAuthFlowServiceCreateValidatesDefinition(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.FormDefinition)
		field  string
	}{
		{"login url", func(d *domain.FormDefinition) { d.LoginURL = "ftp://app.test" }, "definition.login_url"},
		{"username selector", func(d *domain.FormDefinition) { d.UsernameSelector = "" }, "definition.username_selector"},
		{"success", func(d *domain.FormDefinition) { d.Success = domain.SuccessRule{} }, "definition.success"},
		{"timeout", func(d *domain.FormDefinition) { d.TimeoutMS = 1000 }, "definition.timeout_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAuthFlowTestEnv(t)
			in := createAuthFlowInput()
			tc.mutate(&in.Definition)
			_, err := env.service.Create(context.Background(), in)
			assertAuthFlowInvalidError(t, err, tc.field)
			if env.flows.createCalls != 0 {
				t.Fatal("an invalid definition reached the repository")
			}
		})
	}
}

func TestAuthFlowServiceCreateRejectsBadKindAndName(t *testing.T) {
	env := newAuthFlowTestEnv(t)

	in := createAuthFlowInput()
	in.Kind = "api"
	_, err := env.service.Create(context.Background(), in)
	assertAuthFlowInvalidError(t, err, "kind")

	in = createAuthFlowInput()
	in.Name = "   "
	_, err = env.service.Create(context.Background(), in)
	assertAuthFlowInvalidError(t, err, "name")
}

func TestAuthFlowServiceCreateRejectsUnknownSystem(t *testing.T) {
	env := newAuthFlowTestEnv(t)
	in := createAuthFlowInput()
	in.SystemID = 99

	_, err := env.service.Create(context.Background(), in)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindNotFound {
		t.Fatalf("error=%v want not_found", err)
	}
	if env.flows.createCalls != 0 {
		t.Fatal("a flow with an unknown system reached the repository")
	}
}

func TestAuthFlowServiceCRUD(t *testing.T) {
	env := newAuthFlowTestEnv(t)
	ctx := context.Background()

	first, err := env.service.Create(ctx, createAuthFlowInput())
	if err != nil {
		t.Fatal(err)
	}
	secondInput := createAuthFlowInput()
	secondInput.Name = "second login"
	secondInput.Enabled = false
	second, err := env.service.Create(ctx, secondInput)
	if err != nil {
		t.Fatal(err)
	}

	got, err := env.service.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != first.ID || got.Name != first.Name {
		t.Fatalf("got=%+v want %+v", got, first)
	}

	page, more, err := env.service.List(ctx, domain.AuthFlowFilter{Page: domain.Page{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("page=%+v more=%v want only the first flow and more", page, more)
	}
	rest, more, err := env.service.List(ctx, domain.AuthFlowFilter{Page: domain.Page{Limit: 10, AfterID: first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if more || len(rest) != 1 || rest[0].ID != second.ID {
		t.Fatalf("rest=%+v more=%v", rest, more)
	}

	enabled := false
	newDefinition := first.Definition
	newDefinition.TimeoutMS = 30000
	updated, err := env.service.Update(ctx, first.ID, UpdateAuthFlowInput{
		Enabled:    &enabled,
		Definition: &newDefinition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.Definition.TimeoutMS != 30000 || updated.Name != first.Name {
		t.Fatalf("updated=%+v", updated)
	}
	if env.flows.updateCalls != 1 {
		t.Fatalf("update calls=%d", env.flows.updateCalls)
	}

	if err := env.service.Delete(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if env.flows.deleteCalls != 1 {
		t.Fatalf("delete calls=%d", env.flows.deleteCalls)
	}
	if _, err := env.service.Get(ctx, second.ID); err == nil {
		t.Fatal("a deleted flow is still readable")
	}
	if err := env.service.Delete(ctx, second.ID); err == nil {
		t.Fatal("deleting a missing flow must fail")
	}
}

func TestAuthFlowServiceUpdateValidatesAndKeepsUnchangedFields(t *testing.T) {
	env := newAuthFlowTestEnv(t)
	ctx := context.Background()
	created, err := env.service.Create(ctx, createAuthFlowInput())
	if err != nil {
		t.Fatal(err)
	}

	bad := created.Definition
	bad.Success = domain.SuccessRule{URLContains: "/home", Selector: "#home"}
	_, err = env.service.Update(ctx, created.ID, UpdateAuthFlowInput{Definition: &bad})
	assertAuthFlowInvalidError(t, err, "definition.success")
	if env.flows.updateCalls != 0 {
		t.Fatal("an invalid update reached the repository")
	}

	name := "renamed login"
	updated, err := env.service.Update(ctx, created.ID, UpdateAuthFlowInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || !updated.Enabled || updated.Definition.TimeoutMS != created.Definition.TimeoutMS {
		t.Fatalf("partial update changed unrelated fields: %+v", updated)
	}
}

// assertAuthFlowInvalidError asserts the 422 auth_flow_invalid shape.
func assertAuthFlowInvalidError(t *testing.T, err error, wantField string) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error=%v want a *domain.Error", err)
	}
	if domainErr.Kind != domain.KindInvalid || domainErr.Reason != "auth_flow_invalid" {
		t.Fatalf("error kind/reason=%q/%q want invalid/auth_flow_invalid", domainErr.Kind, domainErr.Reason)
	}
	if domainErr.Field != wantField {
		t.Fatalf("error field=%q want %q", domainErr.Field, wantField)
	}
}
