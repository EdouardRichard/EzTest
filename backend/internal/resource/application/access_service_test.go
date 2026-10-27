package application

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fakeAccessCompanyRepo is an in-memory domain.CompanyRepository.
type fakeAccessCompanyRepo struct {
	company      *domain.Company
	err          error
	requestedKey string
	calls        int
}

// GetCompanyByKey returns the configured company.
func (r *fakeAccessCompanyRepo) GetCompanyByKey(_ context.Context, key string) (*domain.Company, error) {
	r.calls++
	r.requestedKey = key
	if r.err != nil {
		return nil, r.err
	}
	if r.company == nil {
		return nil, domain.NewNotFound("company", 0)
	}
	company := *r.company
	return &company, nil
}

// fakeAccessAccountRepo is an in-memory domain.AccountRepository.
type fakeAccessAccountRepo struct {
	accounts         map[int64]domain.Account
	nextID           int64
	countAccesses    int
	countSessions    int
	countAccessErr   error
	countSessionsErr error
	createCalls      int
	updateCalls      int
	deleteCalls      int
}

func newFakeAccessAccountRepo() *fakeAccessAccountRepo {
	return &fakeAccessAccountRepo{accounts: map[int64]domain.Account{}, nextID: 1}
}

func (r *fakeAccessAccountRepo) CreateAccount(_ context.Context, a *domain.Account) error {
	if a.Status == "" {
		a.Status = domain.StatusActive
	}
	a.ID = r.nextID
	r.nextID++
	r.createCalls++
	r.accounts[a.ID] = *a
	return nil
}

func (r *fakeAccessAccountRepo) GetAccount(_ context.Context, id int64) (*domain.Account, error) {
	account, ok := r.accounts[id]
	if !ok {
		return nil, domain.NewNotFound("account", id)
	}
	return &account, nil
}

func (r *fakeAccessAccountRepo) ListAccounts(_ context.Context, f domain.AccountFilter) ([]domain.Account, bool, error) {
	ids := make([]int64, 0, len(r.accounts))
	for id := range r.accounts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	matches := make([]domain.Account, 0, len(ids))
	for _, id := range ids {
		account := r.accounts[id]
		if account.ID <= f.Page.AfterID {
			continue
		}
		if f.Status != "" && account.Status != f.Status {
			continue
		}
		if f.Kind != "" && account.Kind != f.Kind {
			continue
		}
		matches = append(matches, account)
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

func (r *fakeAccessAccountRepo) UpdateAccount(_ context.Context, a *domain.Account) error {
	if _, ok := r.accounts[a.ID]; !ok {
		return domain.NewNotFound("account", a.ID)
	}
	r.updateCalls++
	r.accounts[a.ID] = *a
	return nil
}

func (r *fakeAccessAccountRepo) DeleteAccount(_ context.Context, id int64) error {
	if _, ok := r.accounts[id]; !ok {
		return domain.NewNotFound("account", id)
	}
	r.deleteCalls++
	delete(r.accounts, id)
	return nil
}

func (r *fakeAccessAccountRepo) CountAccesses(_ context.Context, _ int64) (int, error) {
	if r.countAccessErr != nil {
		return 0, r.countAccessErr
	}
	return r.countAccesses, nil
}

func (r *fakeAccessAccountRepo) CountSessionsByAccount(_ context.Context, _ int64) (int, error) {
	if r.countSessionsErr != nil {
		return 0, r.countSessionsErr
	}
	return r.countSessions, nil
}

// fakeAccessAccessRepo is an in-memory domain.AccessRepository.
type fakeAccessAccessRepo struct {
	accesses      map[int64]domain.Access
	nextID        int64
	countSessions int
	countErr      error
	createCalls   int
	updateCalls   int
	deleteCalls   int
}

func newFakeAccessAccessRepo() *fakeAccessAccessRepo {
	return &fakeAccessAccessRepo{accesses: map[int64]domain.Access{}, nextID: 1}
}

func (r *fakeAccessAccessRepo) CreateAccess(_ context.Context, a *domain.Access) error {
	a.ID = r.nextID
	r.nextID++
	r.createCalls++
	r.accesses[a.ID] = *a
	return nil
}

func (r *fakeAccessAccessRepo) GetAccess(_ context.Context, id int64) (*domain.Access, error) {
	access, ok := r.accesses[id]
	if !ok {
		return nil, domain.NewNotFound("access", id)
	}
	return &access, nil
}

func (r *fakeAccessAccessRepo) ListAccesses(_ context.Context, accountID int64) ([]domain.Access, error) {
	ids := make([]int64, 0, len(r.accesses))
	for id := range r.accesses {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]domain.Access, 0, len(ids))
	for _, id := range ids {
		if access := r.accesses[id]; access.AccountID == accountID {
			out = append(out, access)
		}
	}
	return out, nil
}

func (r *fakeAccessAccessRepo) UpdateAccess(_ context.Context, a *domain.Access) error {
	if _, ok := r.accesses[a.ID]; !ok {
		return domain.NewNotFound("access", a.ID)
	}
	r.updateCalls++
	r.accesses[a.ID] = *a
	return nil
}

func (r *fakeAccessAccessRepo) DeleteAccess(_ context.Context, id int64) error {
	if _, ok := r.accesses[id]; !ok {
		return domain.NewNotFound("access", id)
	}
	r.deleteCalls++
	delete(r.accesses, id)
	return nil
}

func (r *fakeAccessAccessRepo) CountSessionsByAccess(_ context.Context, _ int64) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.countSessions, nil
}

// fakeAccessSystemRepo is an in-memory domain.SystemRepository.
type fakeAccessSystemRepo struct {
	systems map[int64]domain.System
	nextID  int64
}

func newFakeAccessSystemRepo() *fakeAccessSystemRepo {
	return &fakeAccessSystemRepo{systems: map[int64]domain.System{}, nextID: 1}
}

func (r *fakeAccessSystemRepo) CreateSystem(_ context.Context, s *domain.System) error {
	s.ID = r.nextID
	r.nextID++
	r.systems[s.ID] = *s
	return nil
}

func (r *fakeAccessSystemRepo) GetSystem(_ context.Context, id int64) (*domain.System, error) {
	system, ok := r.systems[id]
	if !ok {
		return nil, domain.NewNotFound("system", id)
	}
	return &system, nil
}

func (r *fakeAccessSystemRepo) ListSystems(_ context.Context, f domain.SystemFilter) ([]domain.System, bool, error) {
	ids := make([]int64, 0, len(r.systems))
	for id := range r.systems {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]domain.System, 0, len(ids))
	for _, id := range ids {
		if system := r.systems[id]; system.ID > f.Page.AfterID {
			out = append(out, system)
		}
	}
	return out, false, nil
}

func (r *fakeAccessSystemRepo) UpdateSystem(_ context.Context, s *domain.System) error {
	if _, ok := r.systems[s.ID]; !ok {
		return domain.NewNotFound("system", s.ID)
	}
	r.systems[s.ID] = *s
	return nil
}

func (r *fakeAccessSystemRepo) DeleteSystem(_ context.Context, id int64) error {
	if _, ok := r.systems[id]; !ok {
		return domain.NewNotFound("system", id)
	}
	delete(r.systems, id)
	return nil
}

func (r *fakeAccessSystemRepo) CountEnvironments(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

func (r *fakeAccessSystemRepo) CountAccesses(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// accessTestEnv bundles the fake repositories behind one AccessService.
type accessTestEnv struct {
	companies *fakeAccessCompanyRepo
	accounts  *fakeAccessAccountRepo
	accesses  *fakeAccessAccessRepo
	systems   *fakeAccessSystemRepo
	service   *AccessService
}

// newAccessTestEnv builds an AccessService wired to fresh fakes and the seeded
// default company.
func newAccessTestEnv(t *testing.T) *accessTestEnv {
	t.Helper()
	env := &accessTestEnv{
		companies: &fakeAccessCompanyRepo{company: &domain.Company{
			ID: 1, Key: DefaultCompanyKey, Name: "Default", Status: domain.StatusActive,
		}},
		accounts: newFakeAccessAccountRepo(),
		accesses: newFakeAccessAccessRepo(),
		systems:  newFakeAccessSystemRepo(),
	}
	env.service = NewAccessService(env.companies, env.accounts, env.accesses, env.systems)
	return env
}

// seedAccount inserts an account directly into the fake repository.
func (env *accessTestEnv) seedAccount(id int64, key string) domain.Account {
	account := domain.Account{
		ID: id, CompanyID: 1, Key: key, Label: key, Username: key,
		Kind: domain.AccountKindHuman, Status: domain.StatusActive, Notes: "",
	}
	if id >= env.accounts.nextID {
		env.accounts.nextID = id + 1
	}
	env.accounts.accounts[id] = account
	return account
}

// seedSystem inserts a system directly into the fake repository.
func (env *accessTestEnv) seedSystem(id int64, key string) domain.System {
	system := domain.System{
		ID: id, CompanyID: 1, Key: key, Name: key, Kind: domain.SystemKindWeb,
		BaseURL: "http://localhost:13335", DefaultSessionTTLSeconds: 3600, Status: domain.StatusActive,
	}
	if id >= env.systems.nextID {
		env.systems.nextID = id + 1
	}
	env.systems.systems[id] = system
	return system
}

// seedAccess inserts an access directly into the fake repository.
func (env *accessTestEnv) seedAccess(id, accountID, systemID int64, credential string) domain.Access {
	access := domain.Access{
		ID: id, AccountID: accountID, SystemID: systemID,
		AuthType: domain.AuthTypePassword, Credential: credential,
		Enabled: true, ConcurrencyLimit: 1,
	}
	if id >= env.accesses.nextID {
		env.accesses.nextID = id + 1
	}
	env.accesses.accesses[id] = access
	return access
}

// assertAccessServiceError asserts err is a domain error of kind/reason.
func assertAccessServiceError(t *testing.T, err error, kind domain.ErrorKind, reason string) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want a %s/%s error, got nil", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("err=%v (%T), want *domain.Error", err, err)
	}
	if domainErr.Kind != kind {
		t.Fatalf("kind=%q want %q (err=%v)", domainErr.Kind, kind, err)
	}
	if reason != "" && domainErr.Reason != reason {
		t.Fatalf("reason=%q want %q (err=%v)", domainErr.Reason, reason, err)
	}
	return domainErr
}

func TestAccessServiceCreateAccountUsesDefaultCompany(t *testing.T) {
	env := newAccessTestEnv(t)
	env.companies.company.ID = 42

	created, err := env.service.CreateAccount(context.Background(), CreateAccountInput{
		Key: "fixture-user", Label: "Fixture user", Username: "fixture",
		Kind: domain.AccountKindHuman, Notes: "seeded fixture",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if env.companies.requestedKey != DefaultCompanyKey {
		t.Fatalf("company key=%q want %q", env.companies.requestedKey, DefaultCompanyKey)
	}
	if created.CompanyID != 42 {
		t.Fatalf("company id=%d want 42", created.CompanyID)
	}
	if created.Status != domain.StatusActive {
		t.Fatalf("status=%q want active", created.Status)
	}
	if created.ID == 0 {
		t.Fatal("created account has no id")
	}
	if created.Key != "fixture-user" || created.Label != "Fixture user" ||
		created.Username != "fixture" || created.Kind != domain.AccountKindHuman ||
		created.Notes != "seeded fixture" {
		t.Fatalf("created account=%+v", created)
	}
	stored, ok := env.accounts.accounts[created.ID]
	if !ok || stored.Key != "fixture-user" || stored.CompanyID != 42 {
		t.Fatalf("stored account=%+v ok=%v", stored, ok)
	}
}

func TestAccessServiceCreateAccountRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CreateAccountInput)
		reason string
		field  string
	}{
		{"uppercase key", func(in *CreateAccountInput) { in.Key = "Fixture-User" }, "invalid_key", "key"},
		{"short key", func(in *CreateAccountInput) { in.Key = "a" }, "invalid_key", "key"},
		{"unknown kind", func(in *CreateAccountInput) { in.Kind = "robot" }, "invalid_kind", "kind"},
		{"empty kind", func(in *CreateAccountInput) { in.Kind = "" }, "invalid_kind", "kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAccessTestEnv(t)
			in := CreateAccountInput{Key: "fixture-user", Label: "Fixture user", Kind: domain.AccountKindHuman}
			tc.mutate(&in)
			_, err := env.service.CreateAccount(context.Background(), in)
			domainErr := assertAccessServiceError(t, err, domain.KindInvalid, tc.reason)
			if domainErr.Field != tc.field {
				t.Fatalf("field=%q want %q", domainErr.Field, tc.field)
			}
			if env.accounts.createCalls != 0 {
				t.Fatal("invalid create reached the repository")
			}
		})
	}
}

func TestAccessServiceCreateAccountMissingCompany(t *testing.T) {
	env := newAccessTestEnv(t)
	env.companies.company = nil

	_, err := env.service.CreateAccount(context.Background(), CreateAccountInput{
		Key: "fixture-user", Label: "Fixture user", Kind: domain.AccountKindHuman,
	})
	if !errors.Is(err, ErrDefaultCompanyMissing) {
		t.Fatalf("err=%v want ErrDefaultCompanyMissing", err)
	}
	if env.accounts.createCalls != 0 {
		t.Fatal("account was created without the default company")
	}
}

func TestAccessServiceUpdateAccount(t *testing.T) {
	env := newAccessTestEnv(t)
	seeded := env.seedAccount(7, "fixture-user")

	label := "Fixture account"
	username := "fixture"
	updated, err := env.service.UpdateAccount(context.Background(), seeded.ID, UpdateAccountInput{
		Label: &label, Username: &username,
	})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if updated.Label != label || updated.Username != username {
		t.Fatalf("updated account=%+v", updated)
	}
	if updated.Key != seeded.Key || updated.Kind != seeded.Kind || updated.Status != seeded.Status {
		t.Fatalf("partial update changed immutable fields: %+v", updated)
	}
	if env.accounts.updateCalls != 1 {
		t.Fatalf("update calls=%d want 1", env.accounts.updateCalls)
	}

	kind := domain.AccountKindService
	status := domain.StatusDisabled
	notes := "service robot"
	updated, err = env.service.UpdateAccount(context.Background(), seeded.ID, UpdateAccountInput{
		Kind: &kind, Status: &status, Notes: &notes,
	})
	if err != nil {
		t.Fatalf("UpdateAccount full: %v", err)
	}
	if updated.Kind != kind || updated.Status != status || updated.Notes != notes {
		t.Fatalf("updated account=%+v", updated)
	}
	if updated.Label != label || updated.Username != username {
		t.Fatalf("full update lost earlier fields: %+v", updated)
	}
	stored := env.accounts.accounts[seeded.ID]
	if stored.Key != "fixture-user" || stored.Kind != kind || stored.Status != status {
		t.Fatalf("stored account=%+v", stored)
	}
}

func TestAccessServiceUpdateAccountValidations(t *testing.T) {
	cases := []struct {
		name   string
		in     UpdateAccountInput
		reason string
		field  string
	}{
		{"unknown kind", UpdateAccountInput{Kind: accessStringPtr("robot")}, "invalid_kind", "kind"},
		{"unknown status", UpdateAccountInput{Status: accessStringPtr("pending")}, "invalid_status", "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAccessTestEnv(t)
			seeded := env.seedAccount(7, "fixture-user")
			_, err := env.service.UpdateAccount(context.Background(), seeded.ID, tc.in)
			domainErr := assertAccessServiceError(t, err, domain.KindInvalid, tc.reason)
			if domainErr.Field != tc.field {
				t.Fatalf("field=%q want %q", domainErr.Field, tc.field)
			}
			if env.accounts.updateCalls != 0 {
				t.Fatal("invalid update reached the repository")
			}
		})
	}

	env := newAccessTestEnv(t)
	_, err := env.service.UpdateAccount(context.Background(), 404, UpdateAccountInput{})
	assertAccessServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestAccessServiceDeleteAccountRejectsDependencies(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(7, "fixture-user")

	env.accounts.countAccesses = 1
	err := env.service.DeleteAccount(context.Background(), account.ID)
	assertAccessServiceError(t, err, domain.KindDependency, "has_dependencies")
	if env.accounts.deleteCalls != 0 {
		t.Fatal("account was deleted despite accesses")
	}

	env.accounts.countAccesses = 0
	env.accounts.countSessions = 2
	err = env.service.DeleteAccount(context.Background(), account.ID)
	assertAccessServiceError(t, err, domain.KindDependency, "has_dependencies")
	if env.accounts.deleteCalls != 0 {
		t.Fatal("account was deleted despite sessions")
	}

	env.accounts.countSessions = 0
	if err := env.service.DeleteAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if env.accounts.deleteCalls != 1 {
		t.Fatalf("delete calls=%d want 1", env.accounts.deleteCalls)
	}
	if _, ok := env.accounts.accounts[account.ID]; ok {
		t.Fatal("account was not removed")
	}
}

func TestAccessServiceDeleteAccountPropagatesCountErrors(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(7, "fixture-user")
	sentinel := errors.New("count failed")
	env.accounts.countAccessErr = sentinel

	if err := env.service.DeleteAccount(context.Background(), account.ID); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v want the count failure", err)
	}
	if env.accounts.deleteCalls != 0 {
		t.Fatal("account was deleted after a failed count")
	}
}

func TestAccessServiceListAndGetAccounts(t *testing.T) {
	env := newAccessTestEnv(t)
	env.seedAccount(1, "first-user")
	disabled := env.seedAccount(2, "second-user")
	disabled.Status = domain.StatusDisabled
	env.accounts.accounts[2] = disabled
	env.seedAccount(3, "third-user")

	accounts, more, err := env.service.ListAccounts(context.Background(), domain.AccountFilter{
		Page: domain.Page{Limit: 2},
	})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 2 || !more {
		t.Fatalf("accounts=%d more=%v", len(accounts), more)
	}

	disabledAccounts, _, err := env.service.ListAccounts(context.Background(), domain.AccountFilter{
		Status: domain.StatusDisabled, Page: domain.Page{Limit: 20},
	})
	if err != nil {
		t.Fatalf("ListAccounts filtered: %v", err)
	}
	if len(disabledAccounts) != 1 || disabledAccounts[0].ID != 2 {
		t.Fatalf("disabled accounts=%v", disabledAccounts)
	}

	found, err := env.service.GetAccount(context.Background(), 3)
	if err != nil || found.ID != 3 {
		t.Fatalf("GetAccount: account=%+v err=%v", found, err)
	}
	_, err = env.service.GetAccount(context.Background(), 404)
	assertAccessServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestAccessServiceCreateAccess(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(3, "fixture-user")
	system := env.seedSystem(5, "fixture-web")
	ttl := 1800

	created, err := env.service.CreateAccess(context.Background(), account.ID, CreateAccessInput{
		SystemID:          system.ID,
		AuthType:          domain.AuthTypePassword,
		Credential:        "s3cret",
		UsernameOverride:  "fixture",
		LoginURL:          "https://fixture.test/login",
		Enabled:           true,
		ConcurrencyLimit:  4,
		SessionTTLSeconds: &ttl,
		PermissionNote:    "read-only",
	})
	if err != nil {
		t.Fatalf("CreateAccess: %v", err)
	}
	if created.ID == 0 || created.AccountID != account.ID || created.SystemID != system.ID {
		t.Fatalf("created access=%+v", created)
	}
	if created.AuthType != domain.AuthTypePassword || created.Credential != "s3cret" ||
		created.UsernameOverride != "fixture" || created.LoginURL != "https://fixture.test/login" ||
		!created.Enabled || created.ConcurrencyLimit != 4 || created.PermissionNote != "read-only" {
		t.Fatalf("created access=%+v", created)
	}
	if created.SessionTTLSeconds == nil || *created.SessionTTLSeconds != 1800 {
		t.Fatalf("session ttl=%v want 1800", created.SessionTTLSeconds)
	}
	stored, ok := env.accesses.accesses[created.ID]
	if !ok || stored.Credential != "s3cret" || stored.AccountID != account.ID {
		t.Fatalf("stored access=%+v ok=%v", stored, ok)
	}

	secondSystem := env.seedSystem(6, "fixture-api")
	manual, err := env.service.CreateAccess(context.Background(), account.ID, CreateAccessInput{
		SystemID:         secondSystem.ID,
		AuthType:         domain.AuthTypeManual,
		Enabled:          false,
		ConcurrencyLimit: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccess manual: %v", err)
	}
	if manual.SessionTTLSeconds != nil {
		t.Fatalf("session ttl=%v want nil (inherit the system default)", *manual.SessionTTLSeconds)
	}
	if manual.Enabled {
		t.Fatal("disabled access was stored as enabled")
	}
	if env.accesses.accesses[manual.ID].Enabled {
		t.Fatal("stored access was stored as enabled")
	}
}

func TestAccessServiceCreateAccessValidations(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*CreateAccessInput)
		accountID int64
		kind      domain.ErrorKind
		reason    string
		field     string
	}{
		{"missing system", func(in *CreateAccessInput) { in.SystemID = 999 }, 0, domain.KindNotFound, "not_found", ""},
		{"missing account", nil, 999, domain.KindNotFound, "not_found", ""},
		{"unknown auth type", func(in *CreateAccessInput) { in.AuthType = "oauth" }, 0, domain.KindInvalid, "invalid_auth_type", "authType"},
		{"concurrency zero", func(in *CreateAccessInput) { in.ConcurrencyLimit = 0 }, 0, domain.KindInvalid, "invalid_concurrency", "concurrencyLimit"},
		{"concurrency over max", func(in *CreateAccessInput) { in.ConcurrencyLimit = 101 }, 0, domain.KindInvalid, "invalid_concurrency", "concurrencyLimit"},
		{"ttl too small", func(in *CreateAccessInput) { in.SessionTTLSeconds = accessIntPtr(59) }, 0, domain.KindInvalid, "invalid_session_ttl", "sessionTtlSeconds"},
		{"ttl too large", func(in *CreateAccessInput) { in.SessionTTLSeconds = accessIntPtr(604801) }, 0, domain.KindInvalid, "invalid_session_ttl", "sessionTtlSeconds"},
		{"ttl zero", func(in *CreateAccessInput) { in.SessionTTLSeconds = accessIntPtr(0) }, 0, domain.KindInvalid, "invalid_session_ttl", "sessionTtlSeconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAccessTestEnv(t)
			account := env.seedAccount(3, "fixture-user")
			system := env.seedSystem(5, "fixture-web")
			in := CreateAccessInput{
				SystemID: system.ID, AuthType: domain.AuthTypePassword,
				Credential: "s3cret", Enabled: true, ConcurrencyLimit: 1,
			}
			accountID := account.ID
			if tc.accountID != 0 {
				accountID = tc.accountID
			}
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			_, err := env.service.CreateAccess(context.Background(), accountID, in)
			domainErr := assertAccessServiceError(t, err, tc.kind, tc.reason)
			if tc.field != "" && domainErr.Field != tc.field {
				t.Fatalf("field=%q want %q", domainErr.Field, tc.field)
			}
			if env.accesses.createCalls != 0 {
				t.Fatal("invalid create reached the repository")
			}
		})
	}
}

func TestAccessServiceCreateAccessBounds(t *testing.T) {
	for _, concurrency := range []int{1, 100} {
		for _, ttl := range []int{60, 604800} {
			env := newAccessTestEnv(t)
			account := env.seedAccount(3, "fixture-user")
			system := env.seedSystem(5, "fixture-web")
			created, err := env.service.CreateAccess(context.Background(), account.ID, CreateAccessInput{
				SystemID:          system.ID,
				AuthType:          domain.AuthTypeToken,
				Credential:        "token-value",
				Enabled:           true,
				ConcurrencyLimit:  concurrency,
				SessionTTLSeconds: &ttl,
			})
			if err != nil {
				t.Fatalf("concurrency=%d ttl=%d: %v", concurrency, ttl, err)
			}
			if created.ConcurrencyLimit != concurrency || created.SessionTTLSeconds == nil ||
				*created.SessionTTLSeconds != ttl {
				t.Fatalf("created access=%+v", created)
			}
		}
	}
}

func TestAccessServiceUpdateAccessCredentialSemantics(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(3, "fixture-user")
	system := env.seedSystem(5, "fixture-web")
	access := env.seedAccess(11, account.ID, system.ID, "old-secret")

	updated, err := env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{})
	if err != nil {
		t.Fatalf("UpdateAccess omitted credential: %v", err)
	}
	if updated.Credential != "old-secret" {
		t.Fatalf("credential=%q want it unchanged", updated.Credential)
	}
	if env.accesses.accesses[access.ID].Credential != "old-secret" {
		t.Fatal("omitted credential changed the stored value")
	}

	empty := ""
	updated, err = env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{Credential: &empty})
	if err != nil {
		t.Fatalf("UpdateAccess clear credential: %v", err)
	}
	if updated.Credential != "" {
		t.Fatalf("credential=%q want cleared", updated.Credential)
	}
	if env.accesses.accesses[access.ID].Credential != "" {
		t.Fatal("empty credential did not clear the stored value")
	}

	replacement := "new-secret"
	updated, err = env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{Credential: &replacement})
	if err != nil {
		t.Fatalf("UpdateAccess replace credential: %v", err)
	}
	if updated.Credential != replacement || env.accesses.accesses[access.ID].Credential != replacement {
		t.Fatalf("credential=%q want %q", updated.Credential, replacement)
	}
	if updated.AccountID != account.ID || updated.SystemID != system.ID {
		t.Fatalf("credential update changed the access links: %+v", updated)
	}
}

func TestAccessServiceUpdateAccessSessionTTL(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(3, "fixture-user")
	system := env.seedSystem(5, "fixture-web")
	initial := 1800
	access := authAccessWithTTL(env, 11, account.ID, system.ID, &initial)

	updated, err := env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{})
	if err != nil {
		t.Fatalf("UpdateAccess omitted ttl: %v", err)
	}
	if updated.SessionTTLSeconds == nil || *updated.SessionTTLSeconds != 1800 {
		t.Fatalf("session ttl=%v want 1800", updated.SessionTTLSeconds)
	}

	next := 3600
	updated, err = env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{SessionTTLSeconds: &next})
	if err != nil {
		t.Fatalf("UpdateAccess set ttl: %v", err)
	}
	if updated.SessionTTLSeconds == nil || *updated.SessionTTLSeconds != 3600 {
		t.Fatalf("session ttl=%v want 3600", updated.SessionTTLSeconds)
	}

	updated, err = env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{ClearSessionTTL: true})
	if err != nil {
		t.Fatalf("UpdateAccess clear ttl: %v", err)
	}
	if updated.SessionTTLSeconds != nil {
		t.Fatalf("session ttl=%v want nil", *updated.SessionTTLSeconds)
	}
	if env.accesses.accesses[access.ID].SessionTTLSeconds != nil {
		t.Fatal("clear did not persist a NULL ttl")
	}

	// Clear wins when the request carries both a value and the clear flag.
	updated, err = env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{
		SessionTTLSeconds: &next, ClearSessionTTL: true,
	})
	if err != nil {
		t.Fatalf("UpdateAccess clear ttl with value: %v", err)
	}
	if updated.SessionTTLSeconds != nil {
		t.Fatalf("session ttl=%v want nil", *updated.SessionTTLSeconds)
	}
}

func TestAccessServiceUpdateAccessSessionTTLValidation(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(3, "fixture-user")
	system := env.seedSystem(5, "fixture-web")
	initial := 1800
	access := authAccessWithTTL(env, 11, account.ID, system.ID, &initial)

	for _, ttl := range []int{0, 59, 604801} {
		_, err := env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{
			SessionTTLSeconds: &ttl,
		})
		domainErr := assertAccessServiceError(t, err, domain.KindInvalid, "invalid_session_ttl")
		if domainErr.Field != "sessionTtlSeconds" {
			t.Fatalf("field=%q want sessionTtlSeconds", domainErr.Field)
		}
		if env.accesses.updateCalls != 0 {
			t.Fatal("invalid ttl reached the repository")
		}
	}
}

func TestAccessServiceUpdateAccessFields(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(3, "fixture-user")
	system := env.seedSystem(5, "fixture-web")
	ttl := 1800
	access := env.seedAccess(11, account.ID, system.ID, "old-secret")
	access.SessionTTLSeconds = &ttl
	env.accesses.accesses[access.ID] = access

	authType := domain.AuthTypeToken
	username := "bob"
	loginURL := "https://fixture.test/login"
	permission := "read-only"
	enabled := false
	concurrency := 25
	updated, err := env.service.UpdateAccess(context.Background(), access.ID, UpdateAccessInput{
		AuthType: &authType, UsernameOverride: &username, LoginURL: &loginURL,
		PermissionNote: &permission, Enabled: &enabled, ConcurrencyLimit: &concurrency,
	})
	if err != nil {
		t.Fatalf("UpdateAccess: %v", err)
	}
	if updated.AuthType != authType || updated.UsernameOverride != username || updated.LoginURL != loginURL ||
		updated.PermissionNote != permission || updated.Enabled != enabled || updated.ConcurrencyLimit != concurrency {
		t.Fatalf("updated access=%+v", updated)
	}
	if updated.Credential != "old-secret" {
		t.Fatalf("credential=%q want it unchanged", updated.Credential)
	}
	if updated.SessionTTLSeconds == nil || *updated.SessionTTLSeconds != 1800 {
		t.Fatalf("session ttl=%v want 1800", updated.SessionTTLSeconds)
	}
	if updated.AccountID != account.ID || updated.SystemID != system.ID {
		t.Fatalf("update changed the immutable links: %+v", updated)
	}
}

func TestAccessServiceUpdateAccessValidations(t *testing.T) {
	cases := []struct {
		name   string
		in     UpdateAccessInput
		reason string
		field  string
	}{
		{"unknown auth type", UpdateAccessInput{AuthType: accessStringPtr("oauth")}, "invalid_auth_type", "authType"},
		{"concurrency zero", UpdateAccessInput{ConcurrencyLimit: accessIntPtr(0)}, "invalid_concurrency", "concurrencyLimit"},
		{"concurrency over max", UpdateAccessInput{ConcurrencyLimit: accessIntPtr(101)}, "invalid_concurrency", "concurrencyLimit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAccessTestEnv(t)
			account := env.seedAccount(3, "fixture-user")
			system := env.seedSystem(5, "fixture-web")
			access := env.seedAccess(11, account.ID, system.ID, "")
			_, err := env.service.UpdateAccess(context.Background(), access.ID, tc.in)
			domainErr := assertAccessServiceError(t, err, domain.KindInvalid, tc.reason)
			if domainErr.Field != tc.field {
				t.Fatalf("field=%q want %q", domainErr.Field, tc.field)
			}
			if env.accesses.updateCalls != 0 {
				t.Fatal("invalid update reached the repository")
			}
		})
	}

	env := newAccessTestEnv(t)
	_, err := env.service.UpdateAccess(context.Background(), 404, UpdateAccessInput{})
	assertAccessServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestAccessServiceDeleteAccessRejectsDependencies(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(3, "fixture-user")
	system := env.seedSystem(5, "fixture-web")
	access := env.seedAccess(11, account.ID, system.ID, "")

	env.accesses.countSessions = 2
	err := env.service.DeleteAccess(context.Background(), access.ID)
	assertAccessServiceError(t, err, domain.KindDependency, "has_dependencies")
	if env.accesses.deleteCalls != 0 {
		t.Fatal("access was deleted despite sessions")
	}

	env.accesses.countSessions = 0
	if err := env.service.DeleteAccess(context.Background(), access.ID); err != nil {
		t.Fatalf("DeleteAccess: %v", err)
	}
	if env.accesses.deleteCalls != 1 {
		t.Fatalf("delete calls=%d want 1", env.accesses.deleteCalls)
	}
	if _, ok := env.accesses.accesses[access.ID]; ok {
		t.Fatal("access was not removed")
	}

	err = env.service.DeleteAccess(context.Background(), 404)
	assertAccessServiceError(t, err, domain.KindNotFound, "not_found")
}

func TestAccessServiceListAndGetAccesses(t *testing.T) {
	env := newAccessTestEnv(t)
	account := env.seedAccount(3, "fixture-user")
	otherAccount := env.seedAccount(4, "other-user")
	firstSystem := env.seedSystem(5, "fixture-web")
	secondSystem := env.seedSystem(6, "fixture-api")
	env.seedAccess(11, account.ID, firstSystem.ID, "")
	env.seedAccess(12, account.ID, secondSystem.ID, "")
	env.seedAccess(13, otherAccount.ID, firstSystem.ID, "")

	accesses, err := env.service.ListAccesses(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("ListAccesses: %v", err)
	}
	if len(accesses) != 2 || accesses[0].ID != 11 || accesses[1].ID != 12 {
		t.Fatalf("accesses=%+v", accesses)
	}

	found, err := env.service.GetAccess(context.Background(), 12)
	if err != nil || found.ID != 12 || found.AccountID != account.ID {
		t.Fatalf("GetAccess: access=%+v err=%v", found, err)
	}
	_, err = env.service.GetAccess(context.Background(), 404)
	assertAccessServiceError(t, err, domain.KindNotFound, "not_found")
}

// accessStringPtr returns a pointer to value; it keeps table tests compact.
func accessStringPtr(value string) *string { return &value }

// accessIntPtr returns a pointer to value; it keeps table tests compact.
func accessIntPtr(value int) *int { return &value }

// authAccessWithTTL seeds an access with an explicit session TTL.
func authAccessWithTTL(env *accessTestEnv, id, accountID, systemID int64, ttl *int) domain.Access {
	access := env.seedAccess(id, accountID, systemID, "")
	access.SessionTTLSeconds = ttl
	env.accesses.accesses[id] = access
	return access
}
