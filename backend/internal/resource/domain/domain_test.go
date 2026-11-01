package resource

import (
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func strPtr(value string) *string { return &value }

func assertInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a validation error for %q", field)
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a *platform.Error", err)
	}
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("error = %d/%s, want 400/invalid_argument", apiErr.Status, apiErr.Code)
	}
	if got := apiErr.Details["field"]; got != field {
		t.Fatalf("field = %v, want %q", got, field)
	}
}

func TestSUTValidate(t *testing.T) {
	cases := []struct {
		name    string
		sut     SUT
		wantErr bool
		field   string
	}{
		{name: "valid", sut: SUT{Name: "Order Center", Intro: "orders"}},
		{name: "intro at limit", sut: SUT{Name: "S", Intro: strings.Repeat("x", 2000)}},
		{name: "family set", sut: SUT{Name: "S", Family: strPtr("payments")}},
		{name: "family at limit", sut: SUT{Name: "S", Family: strPtr(strings.Repeat("a", 64))}},
		{name: "blank name", sut: SUT{Name: "   "}, wantErr: true, field: "name"},
		{name: "name too long", sut: SUT{Name: strings.Repeat("x", 101)}, wantErr: true, field: "name"},
		{name: "intro too long", sut: SUT{Name: "S", Intro: strings.Repeat("x", 2001)}, wantErr: true, field: "intro"},
		{name: "empty family", sut: SUT{Name: "S", Family: strPtr("")}, wantErr: true, field: "family"},
		{name: "blank family", sut: SUT{Name: "S", Family: strPtr("   ")}, wantErr: true, field: "family"},
		{name: "uppercase family", sut: SUT{Name: "S", Family: strPtr("Payments")}, wantErr: true, field: "family"},
		{name: "family too long", sut: SUT{Name: "S", Family: strPtr(strings.Repeat("a", 65))}, wantErr: true, field: "family"},
		{name: "family with whitespace", sut: SUT{Name: "S", Family: strPtr("order center")}, wantErr: true, field: "family"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.sut.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}

func TestNormalizeFamily(t *testing.T) {
	cases := []struct {
		name  string
		in    *string
		want  *string
		field string
	}{
		{name: "unset", in: nil},
		{name: "trims", in: strPtr("  payments  "), want: strPtr("payments")},
		{name: "allows slug characters", in: strPtr("order-center_v2"), want: strPtr("order-center_v2")},
		{name: "empty", in: strPtr(""), field: "family"},
		{name: "blank", in: strPtr("   "), field: "family"},
		{name: "uppercase", in: strPtr("Payments"), field: "family"},
		{name: "too long", in: strPtr(strings.Repeat("a", 65)), field: "family"},
		{name: "inner whitespace", in: strPtr("order center"), field: "family"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeFamily(tc.in)
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
				return
			}
			if err != nil {
				t.Fatalf("NormalizeFamily() error = %v", err)
			}
			if tc.want == nil {
				if got != nil {
					t.Fatalf("NormalizeFamily() = %q, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("NormalizeFamily() = %v, want %q", got, *tc.want)
			}
		})
	}
}

func TestEnvironmentValidate(t *testing.T) {
	cases := []struct {
		name    string
		env     Environment
		wantErr bool
		field   string
	}{
		{name: "valid https", env: Environment{Name: "prod", BaseURL: "https://app.example.com", Status: StatusActive}},
		{name: "valid http", env: Environment{Name: "dev", BaseURL: "http://localhost:8080", Status: StatusDisabled}},
		{name: "empty name", env: Environment{Name: "", BaseURL: "https://app.example.com", Status: StatusActive}, wantErr: true, field: "name"},
		{name: "base url without scheme", env: Environment{Name: "prod", BaseURL: "app.example.com", Status: StatusActive}, wantErr: true, field: "base_url"},
		{name: "ftp base url", env: Environment{Name: "prod", BaseURL: "ftp://app.example.com", Status: StatusActive}, wantErr: true, field: "base_url"},
		{name: "unknown status", env: Environment{Name: "prod", BaseURL: "https://app.example.com", Status: "archived"}, wantErr: true, field: "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.env.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}

func TestPageValidate(t *testing.T) {
	valid := Page{
		Name: "Order list", URL: "https://app.example.com/orders",
		RouteTemplate: "/orders", PageType: "page", Status: PageStatusApproved, Source: PageSourceAdmin,
	}
	cases := []struct {
		name    string
		mutate  func(*Page)
		wantErr bool
		field   string
	}{
		{name: "valid", mutate: func(*Page) {}},
		{name: "empty name", mutate: func(p *Page) { p.Name = "" }, wantErr: true, field: "name"},
		{name: "invalid url", mutate: func(p *Page) { p.URL = "/orders" }, wantErr: true, field: "url"},
		{name: "route without slash", mutate: func(p *Page) { p.RouteTemplate = "orders" }, wantErr: true, field: "route_template"},
		{name: "empty route", mutate: func(p *Page) { p.RouteTemplate = "" }, wantErr: true, field: "route_template"},
		{name: "unknown status", mutate: func(p *Page) { p.Status = "draft" }, wantErr: true, field: "status"},
		{name: "unknown source", mutate: func(p *Page) { p.Source = "bogus" }, wantErr: true, field: "source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := valid
			tc.mutate(&page)
			err := page.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}

func TestDeriveRouteTemplate(t *testing.T) {
	cases := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "https://app.example.com/orders/list?state=open", want: "/orders/list"},
		{raw: "https://app.example.com", want: "/"},
		{raw: "https://app.example.com/", want: "/"},
		{raw: "https://app.example.com/orders/", want: "/orders/"},
		{raw: "not a url", wantErr: true},
		{raw: "ftp://app.example.com/orders", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := DeriveRouteTemplate(tc.raw)
			if tc.wantErr != (err != nil) {
				t.Fatalf("DeriveRouteTemplate(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("DeriveRouteTemplate(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestAPIValidate(t *testing.T) {
	valid := API{
		Name: "List orders", Method: "GET", PathTemplate: "/api/orders",
		Status: APIStatusApproved, Source: APISourceManual,
	}
	cases := []struct {
		name    string
		mutate  func(*API)
		wantErr bool
		field   string
	}{
		{name: "valid", mutate: func(*API) {}},
		{name: "empty name", mutate: func(a *API) { a.Name = "" }, wantErr: true, field: "name"},
		{name: "lowercase method", mutate: func(a *API) { a.Method = "get" }, wantErr: true, field: "method"},
		{name: "unsupported method", mutate: func(a *API) { a.Method = "TRACE" }, wantErr: true, field: "method"},
		{name: "path without slash", mutate: func(a *API) { a.PathTemplate = "api/orders" }, wantErr: true, field: "path_template"},
		{name: "empty path", mutate: func(a *API) { a.PathTemplate = "" }, wantErr: true, field: "path_template"},
		{name: "unknown status", mutate: func(a *API) { a.Status = "draft" }, wantErr: true, field: "status"},
		{name: "unknown source", mutate: func(a *API) { a.Source = "bogus" }, wantErr: true, field: "source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := valid
			tc.mutate(&api)
			err := api.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}

func TestTargetDatabaseValidate(t *testing.T) {
	valid := TargetDatabase{
		Name: "orders db", DSN: "postgres://db.example.com:5432/orders",
		Username: "reader", Kind: "postgres", Status: StatusActive,
	}
	cases := []struct {
		name    string
		mutate  func(*TargetDatabase)
		wantErr bool
		field   string
	}{
		{name: "valid", mutate: func(*TargetDatabase) {}},
		{name: "postgresql scheme", mutate: func(d *TargetDatabase) { d.DSN = "postgresql://db.example.com:5432/orders" }},
		{name: "empty name", mutate: func(d *TargetDatabase) { d.Name = "" }, wantErr: true, field: "name"},
		{name: "empty dsn", mutate: func(d *TargetDatabase) { d.DSN = "" }, wantErr: true, field: "dsn"},
		{name: "wrong dsn scheme", mutate: func(d *TargetDatabase) { d.DSN = "mysql://db.example.com/orders" }, wantErr: true, field: "dsn"},
		{name: "empty username", mutate: func(d *TargetDatabase) { d.Username = "" }, wantErr: true, field: "username"},
		{name: "unknown kind", mutate: func(d *TargetDatabase) { d.Kind = "mysql" }, wantErr: true, field: "kind"},
		{name: "unknown status", mutate: func(d *TargetDatabase) { d.Status = "gone" }, wantErr: true, field: "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := valid
			tc.mutate(&db)
			err := db.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}

func TestTestAccountValidate(t *testing.T) {
	valid := TestAccount{
		Username: "order_admin", Password: "s3cret", Roles: []string{"order_admin"},
		Notes: "seed account", Status: StatusActive,
	}
	cases := []struct {
		name    string
		mutate  func(*TestAccount)
		wantErr bool
		field   string
	}{
		{name: "valid", mutate: func(*TestAccount) {}},
		{name: "empty username", mutate: func(a *TestAccount) { a.Username = "" }, wantErr: true, field: "username"},
		{name: "empty password on create", mutate: func(a *TestAccount) { a.Password = "" }, wantErr: true, field: "password"},
		{name: "empty role", mutate: func(a *TestAccount) { a.Roles = []string{"admin", " "} }, wantErr: true, field: "roles"},
		{name: "unknown status", mutate: func(a *TestAccount) { a.Status = "locked" }, wantErr: true, field: "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := valid
			tc.mutate(&account)
			err := account.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}

func TestTestAccountValidateUpdateAllowsKeptPassword(t *testing.T) {
	account := TestAccount{Username: "order_admin", Status: StatusActive}
	if err := account.ValidateUpdate(); err != nil {
		t.Fatalf("ValidateUpdate() with kept password: %v", err)
	}
	if err := account.Validate(); err == nil {
		t.Fatal("Validate() accepted a missing password on create")
	}
	account.Username = ""
	assertInvalidField(t, account.ValidateUpdate(), "username")
}

func TestCodeRepositoryValidate(t *testing.T) {
	valid := CodeRepository{
		Name: "orders-service", Path: "https://git.example.com/orders.git",
		Include: []string{"src/**"}, Exclude: []string{"vendor/**"}, Status: StatusActive,
	}
	cases := []struct {
		name    string
		mutate  func(*CodeRepository)
		wantErr bool
		field   string
	}{
		{name: "valid", mutate: func(*CodeRepository) {}},
		{name: "empty name", mutate: func(r *CodeRepository) { r.Name = "" }, wantErr: true, field: "name"},
		{name: "empty path", mutate: func(r *CodeRepository) { r.Path = "" }, wantErr: true, field: "path"},
		{name: "empty include item", mutate: func(r *CodeRepository) { r.Include = []string{"src/**", ""} }, wantErr: true, field: "include"},
		{name: "unknown status", mutate: func(r *CodeRepository) { r.Status = "deleted" }, wantErr: true, field: "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := valid
			tc.mutate(&repo)
			err := repo.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertInvalidField(t, err, tc.field)
			}
		})
	}
}
