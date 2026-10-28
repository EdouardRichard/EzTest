package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// This file reuses the package's catalog test fakes for the two dependencies
// that are not under test here: the authenticated users (catalog tokens and
// authenticator), the seeded default company and the system repository.

// accessHTTPTestAccountRepo is an in-memory domain.AccountRepository.
type accessHTTPTestAccountRepo struct {
	accounts      map[int64]domain.Account
	nextID        int64
	countAccesses int
	countSessions int
	createCalls   int
	updateCalls   int
	deleteCalls   int
}

func newAccessHTTPTestAccountRepo() *accessHTTPTestAccountRepo {
	return &accessHTTPTestAccountRepo{accounts: map[int64]domain.Account{}, nextID: 1}
}

// seed inserts an active human account owned by the default company.
func (r *accessHTTPTestAccountRepo) seed(key string) domain.Account {
	account := domain.Account{
		ID: r.nextID, CompanyID: 1, Key: key, Label: key, Username: key,
		Kind: domain.AccountKindHuman, Status: domain.StatusActive,
	}
	r.nextID++
	r.accounts[account.ID] = account
	return account
}

func (r *accessHTTPTestAccountRepo) CreateAccount(_ context.Context, a *domain.Account) error {
	if a.Status == "" {
		a.Status = domain.StatusActive
	}
	a.ID = r.nextID
	r.nextID++
	r.createCalls++
	r.accounts[a.ID] = *a
	return nil
}

func (r *accessHTTPTestAccountRepo) GetAccount(_ context.Context, id int64) (*domain.Account, error) {
	account, ok := r.accounts[id]
	if !ok {
		return nil, domain.NewNotFound("account", id)
	}
	return &account, nil
}

func (r *accessHTTPTestAccountRepo) ListAccounts(_ context.Context, f domain.AccountFilter) ([]domain.Account, bool, error) {
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
		limit = platform.DefaultLimit
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

func (r *accessHTTPTestAccountRepo) UpdateAccount(_ context.Context, a *domain.Account) error {
	if _, ok := r.accounts[a.ID]; !ok {
		return domain.NewNotFound("account", a.ID)
	}
	r.updateCalls++
	r.accounts[a.ID] = *a
	return nil
}

func (r *accessHTTPTestAccountRepo) DeleteAccount(_ context.Context, id int64) error {
	if _, ok := r.accounts[id]; !ok {
		return domain.NewNotFound("account", id)
	}
	r.deleteCalls++
	delete(r.accounts, id)
	return nil
}

func (r *accessHTTPTestAccountRepo) CountAccesses(_ context.Context, _ int64) (int, error) {
	return r.countAccesses, nil
}

func (r *accessHTTPTestAccountRepo) CountSessionsByAccount(_ context.Context, _ int64) (int, error) {
	return r.countSessions, nil
}

// accessHTTPTestAccessRepo is an in-memory domain.AccessRepository. It keeps
// the plaintext credential like the real repository does; the HTTP tests assert
// that it never leaves the process.
type accessHTTPTestAccessRepo struct {
	accesses      map[int64]domain.Access
	nextID        int64
	countSessions int
	createCalls   int
	updateCalls   int
	deleteCalls   int
}

func newAccessHTTPTestAccessRepo() *accessHTTPTestAccessRepo {
	return &accessHTTPTestAccessRepo{accesses: map[int64]domain.Access{}, nextID: 1}
}

// seed inserts an access, filling the DDL defaults for omitted fields.
func (r *accessHTTPTestAccessRepo) seed(access domain.Access) domain.Access {
	if access.ID == 0 {
		access.ID = r.nextID
	}
	if access.ID >= r.nextID {
		r.nextID = access.ID + 1
	}
	if access.AuthType == "" {
		access.AuthType = domain.AuthTypePassword
	}
	if access.ConcurrencyLimit == 0 {
		access.ConcurrencyLimit = 1
	}
	r.accesses[access.ID] = access
	return access
}

func (r *accessHTTPTestAccessRepo) CreateAccess(_ context.Context, a *domain.Access) error {
	a.ID = r.nextID
	r.nextID++
	r.createCalls++
	r.accesses[a.ID] = *a
	return nil
}

func (r *accessHTTPTestAccessRepo) GetAccess(_ context.Context, id int64) (*domain.Access, error) {
	access, ok := r.accesses[id]
	if !ok {
		return nil, domain.NewNotFound("access", id)
	}
	return &access, nil
}

func (r *accessHTTPTestAccessRepo) ListAccesses(_ context.Context, accountID int64) ([]domain.Access, error) {
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

func (r *accessHTTPTestAccessRepo) UpdateAccess(_ context.Context, a *domain.Access) error {
	if _, ok := r.accesses[a.ID]; !ok {
		return domain.NewNotFound("access", a.ID)
	}
	r.updateCalls++
	r.accesses[a.ID] = *a
	return nil
}

func (r *accessHTTPTestAccessRepo) DeleteAccess(_ context.Context, id int64) error {
	if _, ok := r.accesses[id]; !ok {
		return domain.NewNotFound("access", id)
	}
	r.deleteCalls++
	delete(r.accesses, id)
	return nil
}

func (r *accessHTTPTestAccessRepo) CountSessionsByAccess(_ context.Context, _ int64) (int, error) {
	return r.countSessions, nil
}

// accessHTTPTestRepos bundles the fakes behind the handler test server.
type accessHTTPTestRepos struct {
	companies catalogHTTPTestCompanyRepo
	accounts  *accessHTTPTestAccountRepo
	accesses  *accessHTTPTestAccessRepo
	systems   *catalogHTTPTestSystemRepo
	flows     *authFlowHTTPTestRepo
}

// newAccessHandlerTestServer mounts the account/access routes behind the real
// auth middleware and RequireAdmin.
func newAccessHandlerTestServer(t *testing.T) (*http.ServeMux, *accessHTTPTestRepos) {
	t.Helper()
	repos := &accessHTTPTestRepos{
		accounts: newAccessHTTPTestAccountRepo(),
		accesses: newAccessHTTPTestAccessRepo(),
		systems:  newCatalogHTTPTestSystemRepo(),
		flows:    newAuthFlowHTTPTestRepo(),
	}
	service := application.NewAccessService(repos.companies, repos.accounts, repos.accesses, repos.systems, repos.flows)
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return identityhttp.AuthMiddleware(catalogHTTPTestAuthenticator{})(identityhttp.RequireAdmin(next))
	}
	NewAccessHandler(service).RegisterRoutes(mux, guard)
	return mux, repos
}

// accessRequest performs one request against the account/access test mux.
func accessRequest(mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// decodeAccessData decodes the {"data": ...} success envelope.
func decodeAccessData[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return envelope.Data
}

// decodeAccessPage decodes the cursor page envelope.
func decodeAccessPage(t *testing.T, rr *httptest.ResponseRecorder) ([]map[string]any, string, bool) {
	t.Helper()
	var envelope struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode page %q: %v", rr.Body.String(), err)
	}
	return envelope.Data, envelope.NextCursor, envelope.HasMore
}

// assertAccessErrorEnvelope asserts the status, code and details.reason.
func assertAccessErrorEnvelope(t *testing.T, rr *httptest.ResponseRecorder, status int, code, reason string) platform.APIError {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status=%d want %d (body=%s)", rr.Code, status, rr.Body.String())
	}
	apiErr := decodeResourceAPIError(t, rr)
	if apiErr.Code != code {
		t.Fatalf("code=%q want %q", apiErr.Code, code)
	}
	if reason != "" {
		if got := apiErr.Details["reason"]; got != reason {
			t.Fatalf("details.reason=%v want %q", got, reason)
		}
	}
	return apiErr
}

// assertNoCredentialLeak fails when the body carries the credential field or a
// known credential value.
func assertNoCredentialLeak(t *testing.T, rr *httptest.ResponseRecorder, secrets ...string) {
	t.Helper()
	body := rr.Body.String()
	if strings.Contains(body, `"credential"`) {
		t.Fatalf("response leaks a credential field: %s", body)
	}
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaks the credential value %q: %s", secret, body)
		}
	}
}

func TestAccessHandlerCreateAccount(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)

	rr := accessRequest(mux, http.MethodPost, "/api/resources/accounts", catalogAdminToken,
		`{"key":"fixture-user","label":"Fixture user","username":"fixture","kind":"human","notes":"seeded"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeAccessData[map[string]any](t, rr)
	if data["key"] != "fixture-user" || data["label"] != "Fixture user" ||
		data["username"] != "fixture" || data["kind"] != domain.AccountKindHuman ||
		data["status"] != domain.StatusActive || data["notes"] != "seeded" {
		t.Fatalf("data=%v", data)
	}
	if _, ok := data["id"].(float64); !ok {
		t.Fatalf("id=%v want a number", data["id"])
	}
	if _, ok := data["companyId"]; ok {
		t.Fatal("DTO leaks companyId")
	}
	if len(repos.accounts.accounts) != 1 {
		t.Fatalf("stored %d accounts", len(repos.accounts.accounts))
	}
	for _, account := range repos.accounts.accounts {
		if account.CompanyID != 1 {
			t.Fatalf("company id=%d want the default company", account.CompanyID)
		}
	}

	// The create body never accepts a company id (spec §7.4).
	rr = accessRequest(mux, http.MethodPost, "/api/resources/accounts", catalogAdminToken,
		`{"key":"other-user","label":"Other","kind":"human","companyId":9}`)
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")

	rr = accessRequest(mux, http.MethodPost, "/api/resources/accounts", catalogAdminToken,
		`{"key":"BAD","label":"Bad","kind":"human"}`)
	apiErr := assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_key")
	if apiErr.Details["field"] != "key" {
		t.Fatalf("details.field=%v want key", apiErr.Details["field"])
	}

	rr = accessRequest(mux, http.MethodPost, "/api/resources/accounts", catalogAdminToken,
		`{"key":"robot-user","label":"Robot","kind":"robot"}`)
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_kind")
}

func TestAccessHandlerAccountCRUD(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	seeded := repos.accounts.seed("fixture-user")

	rr := accessRequest(mux, http.MethodGet, "/api/resources/accounts/1", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeAccessData[map[string]any](t, rr)
	if data["id"] != float64(seeded.ID) || data["key"] != "fixture-user" || data["status"] != domain.StatusActive {
		t.Fatalf("data=%v", data)
	}

	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accounts/1", catalogAdminToken,
		`{"label":"Fixture v2","status":"disabled"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched := decodeAccessData[map[string]any](t, rr)
	if patched["label"] != "Fixture v2" || patched["status"] != domain.StatusDisabled ||
		patched["key"] != "fixture-user" || patched["kind"] != domain.AccountKindHuman {
		t.Fatalf("patched=%v", patched)
	}
	if repos.accounts.accounts[seeded.ID].Key != "fixture-user" {
		t.Fatal("update changed the immutable key")
	}

	rr = accessRequest(mux, http.MethodGet, "/api/resources/accounts/999", catalogUserToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")

	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accounts/999", catalogAdminToken, `{"label":"x"}`)
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")

	rr = accessRequest(mux, http.MethodDelete, "/api/resources/accounts/1", catalogAdminToken, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204 (body=%s)", rr.Code, rr.Body.String())
	}
	if _, ok := repos.accounts.accounts[seeded.ID]; ok {
		t.Fatal("account was not deleted")
	}
	rr = accessRequest(mux, http.MethodGet, "/api/resources/accounts/1", catalogUserToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")
}

func TestAccessHandlerListAccountsPagination(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	repos.accounts.seed("one")
	second := repos.accounts.seed("two")
	repos.accounts.seed("three")

	rr := accessRequest(mux, http.MethodGet, "/api/resources/accounts?limit=2", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, next, hasMore := decodeAccessPage(t, rr)
	if len(items) != 2 || !hasMore {
		t.Fatalf("items=%d hasMore=%v", len(items), hasMore)
	}
	if next != platform.EncodeCursor(second.ID) {
		t.Fatalf("next_cursor=%q want %q", next, platform.EncodeCursor(second.ID))
	}

	rr = accessRequest(mux, http.MethodGet, "/api/resources/accounts?limit=2&cursor="+next, catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, _, hasMore = decodeAccessPage(t, rr)
	if len(items) != 1 || hasMore {
		t.Fatalf("page 2 items=%d hasMore=%v", len(items), hasMore)
	}

	rr = accessRequest(mux, http.MethodGet, "/api/resources/accounts?limit=101", catalogUserToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_limit")

	rr = accessRequest(mux, http.MethodGet, "/api/resources/accounts?cursor=not-a-cursor!", catalogUserToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_cursor")
}

func TestAccessHandlerCreateAccess(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	account := repos.accounts.seed("fixture-user")
	system := repos.systems.seed("fixture-web")

	rr := accessRequest(mux, http.MethodPost, "/api/resources/accounts/1/accesses", catalogAdminToken,
		`{"systemId":1,"authType":"password","credential":"s3cret","usernameOverride":"fixture",`+
			`"loginUrl":"https://fixture.test/login","enabled":true,"concurrencyLimit":4,`+
			`"sessionTtlSeconds":1800,"permissionNote":"read-only"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeAccessData[map[string]any](t, rr)
	if data["accountId"] != float64(account.ID) || data["systemId"] != float64(system.ID) {
		t.Fatalf("data=%v", data)
	}
	if data["authType"] != domain.AuthTypePassword || data["credentialSet"] != true ||
		data["usernameOverride"] != "fixture" || data["loginUrl"] != "https://fixture.test/login" ||
		data["enabled"] != true || data["concurrencyLimit"] != float64(4) ||
		data["sessionTtlSeconds"] != float64(1800) || data["permissionNote"] != "read-only" {
		t.Fatalf("data=%v", data)
	}
	if _, ok := data["credential"]; ok {
		t.Fatal("access DTO leaks credential")
	}
	assertNoCredentialLeak(t, rr, "s3cret")
	for _, access := range repos.accesses.accesses {
		if access.Credential != "s3cret" {
			t.Fatalf("stored credential=%q", access.Credential)
		}
	}

	// The DDL default for an omitted enabled flag is true; an omitted
	// credential yields credentialSet=false.
	secondSystem := repos.systems.seed("fixture-api")
	rr = accessRequest(mux, http.MethodPost, "/api/resources/accounts/1/accesses", catalogAdminToken,
		`{"systemId":2,"authType":"manual","concurrencyLimit":1}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	manual := decodeAccessData[map[string]any](t, rr)
	if manual["systemId"] != float64(secondSystem.ID) || manual["credentialSet"] != false ||
		manual["enabled"] != true || manual["sessionTtlSeconds"] != nil {
		t.Fatalf("data=%v", manual)
	}
	assertNoCredentialLeak(t, rr)

	createCalls := repos.accesses.createCalls
	cases := []struct {
		name   string
		body   string
		status int
		code   string
		reason string
	}{
		{"missing system", `{"systemId":999,"authType":"manual","concurrencyLimit":1}`, http.StatusNotFound, platform.CodeNotFound, "not_found"},
		{"unknown auth type", `{"systemId":1,"authType":"oauth","concurrencyLimit":1}`, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_auth_type"},
		{"concurrency too high", `{"systemId":1,"authType":"manual","concurrencyLimit":101}`, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_concurrency"},
		{"ttl too small", `{"systemId":1,"authType":"manual","concurrencyLimit":1,"sessionTtlSeconds":30}`, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_session_ttl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := accessRequest(mux, http.MethodPost, "/api/resources/accounts/1/accesses", catalogAdminToken, tc.body)
			assertAccessErrorEnvelope(t, rr, tc.status, tc.code, tc.reason)
		})
	}
	if repos.accesses.createCalls != createCalls {
		t.Fatalf("invalid creates reached the repository: calls=%d want %d",
			repos.accesses.createCalls, createCalls)
	}

	rr = accessRequest(mux, http.MethodPost, "/api/resources/accounts/999/accesses", catalogAdminToken,
		`{"systemId":1,"authType":"manual","concurrencyLimit":1}`)
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")
}

func TestAccessHandlerCredentialNeverExposed(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	account := repos.accounts.seed("fixture-user")
	system := repos.systems.seed("fixture-web")
	access := repos.accesses.seed(domain.Access{
		AccountID: account.ID, SystemID: system.ID, AuthType: domain.AuthTypePassword,
		Credential: "old-secret", Enabled: true, ConcurrencyLimit: 1,
	})

	rr := accessRequest(mux, http.MethodGet, "/api/resources/accesses/1", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeAccessData[map[string]any](t, rr)
	if data["credentialSet"] != true {
		t.Fatalf("credentialSet=%v want true", data["credentialSet"])
	}
	if _, ok := data["credential"]; ok {
		t.Fatal("detail DTO leaks credential")
	}
	assertNoCredentialLeak(t, rr, "old-secret")

	rr = accessRequest(mux, http.MethodGet, "/api/resources/accounts/1/accesses", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items := decodeAccessData[[]map[string]any](t, rr)
	if len(items) != 1 || items[0]["credentialSet"] != true {
		t.Fatalf("items=%v", items)
	}
	if _, ok := items[0]["credential"]; ok {
		t.Fatal("list DTO leaks credential")
	}
	assertNoCredentialLeak(t, rr, "old-secret")

	// An omitted credential keeps the stored one.
	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"usernameOverride":"bob"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched := decodeAccessData[map[string]any](t, rr)
	if patched["usernameOverride"] != "bob" || patched["credentialSet"] != true {
		t.Fatalf("patched=%v", patched)
	}
	assertNoCredentialLeak(t, rr, "old-secret")
	if repos.accesses.accesses[access.ID].Credential != "old-secret" {
		t.Fatal("omitted credential changed the stored value")
	}

	// A non-empty credential replaces it.
	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"credential":"new-secret"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	replaced := decodeAccessData[map[string]any](t, rr)
	if replaced["credentialSet"] != true {
		t.Fatalf("credentialSet=%v want true", replaced["credentialSet"])
	}
	assertNoCredentialLeak(t, rr, "new-secret")
	if repos.accesses.accesses[access.ID].Credential != "new-secret" {
		t.Fatal("non-empty credential did not replace the stored value")
	}

	// An empty string clears it.
	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"credential":""}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	cleared := decodeAccessData[map[string]any](t, rr)
	if cleared["credentialSet"] != false {
		t.Fatalf("credentialSet=%v want false", cleared["credentialSet"])
	}
	assertNoCredentialLeak(t, rr)
	if repos.accesses.accesses[access.ID].Credential != "" {
		t.Fatal("empty credential did not clear the stored value")
	}

	rr = accessRequest(mux, http.MethodGet, "/api/resources/accesses/999", catalogUserToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")
}

func TestAccessHandlerUpdateAccessSessionTTL(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	account := repos.accounts.seed("fixture-user")
	system := repos.systems.seed("fixture-web")
	initial := 1800
	access := repos.accesses.seed(domain.Access{
		AccountID: account.ID, SystemID: system.ID, AuthType: domain.AuthTypePassword,
		Enabled: true, ConcurrencyLimit: 1, SessionTTLSeconds: &initial,
	})

	rr := accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"usernameOverride":"bob"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched := decodeAccessData[map[string]any](t, rr)
	if patched["sessionTtlSeconds"] != float64(1800) {
		t.Fatalf("sessionTtlSeconds=%v want 1800 (omitted means unchanged)", patched["sessionTtlSeconds"])
	}

	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"sessionTtlSeconds":3600}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched = decodeAccessData[map[string]any](t, rr)
	if patched["sessionTtlSeconds"] != float64(3600) {
		t.Fatalf("sessionTtlSeconds=%v want 3600", patched["sessionTtlSeconds"])
	}

	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"sessionTtlSeconds":null}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched = decodeAccessData[map[string]any](t, rr)
	if patched["sessionTtlSeconds"] != nil {
		t.Fatalf("sessionTtlSeconds=%v want null", patched["sessionTtlSeconds"])
	}
	if repos.accesses.accesses[access.ID].SessionTTLSeconds != nil {
		t.Fatal("explicit null did not clear the stored TTL")
	}

	updateCalls := repos.accesses.updateCalls
	for _, body := range []string{
		`{"sessionTtlSeconds":30}`,
		`{"sessionTtlSeconds":0}`,
		`{"sessionTtlSeconds":"soon"}`,
	} {
		rr := accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken, body)
		assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_session_ttl")
	}
	if repos.accesses.updateCalls != updateCalls {
		t.Fatal("invalid TTLs reached the repository")
	}
}

func TestAccessHandlerUpdateAccessAcceptsSharedFormPayload(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	account := repos.accounts.seed("fixture-user")
	system := repos.systems.seed("fixture-web")
	access := repos.accesses.seed(domain.Access{
		AccountID: account.ID, SystemID: system.ID, AuthType: domain.AuthTypePassword,
		Enabled: true, ConcurrencyLimit: 1,
	})

	// The shared AccessForm always submits every writable field, including the
	// immutable systemId (the same form is reused for create and edit).
	rr := accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"systemId":1,"authType":"token","usernameOverride":"","loginUrl":"","enabled":true,`+
			`"concurrencyLimit":4,"sessionTtlSeconds":null,"permissionNote":""}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched := decodeAccessData[map[string]any](t, rr)
	if patched["systemId"] != float64(system.ID) || patched["accountId"] != float64(account.ID) {
		t.Fatalf("patched=%v want the immutable links preserved", patched)
	}
	if patched["authType"] != domain.AuthTypeToken || patched["concurrencyLimit"] != float64(4) ||
		patched["sessionTtlSeconds"] != nil {
		t.Fatalf("patched=%v", patched)
	}
	stored := repos.accesses.accesses[access.ID]
	if stored.SystemID != system.ID || stored.AccountID != account.ID {
		t.Fatalf("stored access=%+v want the immutable links preserved", stored)
	}
}

func TestAccessHandlerWriteRoutesRequireAdmin(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	account := repos.accounts.seed("fixture-user")
	system := repos.systems.seed("fixture-web")
	repos.accesses.seed(domain.Access{
		AccountID: account.ID, SystemID: system.ID, AuthType: domain.AuthTypePassword,
		Enabled: true, ConcurrencyLimit: 1,
	})

	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/resources/accounts", `{"key":"new-user","label":"New","kind":"human"}`},
		{http.MethodPatch, "/api/resources/accounts/1", `{"label":"x"}`},
		{http.MethodDelete, "/api/resources/accounts/1", ""},
		{http.MethodPost, "/api/resources/accounts/1/accesses", `{"systemId":1,"authType":"manual","concurrencyLimit":1}`},
		{http.MethodPatch, "/api/resources/accesses/1", `{"enabled":false}`},
		{http.MethodDelete, "/api/resources/accesses/1", ""},
	}
	for _, write := range writes {
		t.Run(write.method+" "+write.path, func(t *testing.T) {
			rr := accessRequest(mux, write.method, write.path, catalogUserToken, write.body)
			assertAccessErrorEnvelope(t, rr, http.StatusForbidden, platform.CodeForbidden, "")
		})
		rr := accessRequest(mux, write.method, write.path, "", write.body)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without token: status=%d want 401", write.method, write.path, rr.Code)
		}
	}
	if repos.accounts.createCalls != 0 || repos.accounts.updateCalls != 0 || repos.accounts.deleteCalls != 0 {
		t.Fatalf("forbidden account writes reached the service: create=%d update=%d delete=%d",
			repos.accounts.createCalls, repos.accounts.updateCalls, repos.accounts.deleteCalls)
	}
	if repos.accesses.createCalls != 0 || repos.accesses.updateCalls != 0 || repos.accesses.deleteCalls != 0 {
		t.Fatalf("forbidden access writes reached the service: create=%d update=%d delete=%d",
			repos.accesses.createCalls, repos.accesses.updateCalls, repos.accesses.deleteCalls)
	}

	// Reads stay available to regular users.
	reads := []string{
		"/api/resources/accounts",
		"/api/resources/accounts/1",
		"/api/resources/accounts/1/accesses",
		"/api/resources/accesses/1",
	}
	for _, path := range reads {
		rr := accessRequest(mux, http.MethodGet, path, catalogUserToken, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: status=%d want 200 (body=%s)", path, rr.Code, rr.Body.String())
		}
		assertNoCredentialLeak(t, rr)
	}
}

func TestAccessHandlerDeleteDependencies(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	account := repos.accounts.seed("fixture-user")
	system := repos.systems.seed("fixture-web")
	access := repos.accesses.seed(domain.Access{
		AccountID: account.ID, SystemID: system.ID, AuthType: domain.AuthTypePassword,
		Enabled: true, ConcurrencyLimit: 1,
	})

	repos.accounts.countAccesses = 1
	rr := accessRequest(mux, http.MethodDelete, "/api/resources/accounts/1", catalogAdminToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusConflict, platform.CodeConflict, "has_dependencies")
	if repos.accounts.deleteCalls != 0 {
		t.Fatal("an account with accesses was deleted")
	}

	repos.accounts.countAccesses = 0
	repos.accounts.countSessions = 1
	rr = accessRequest(mux, http.MethodDelete, "/api/resources/accounts/1", catalogAdminToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusConflict, platform.CodeConflict, "has_dependencies")
	if repos.accounts.deleteCalls != 0 {
		t.Fatal("an account with sessions was deleted")
	}

	repos.accounts.countSessions = 0
	repos.accesses.countSessions = 1
	rr = accessRequest(mux, http.MethodDelete, "/api/resources/accesses/1", catalogAdminToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusConflict, platform.CodeConflict, "has_dependencies")
	if repos.accesses.deleteCalls != 0 {
		t.Fatal("an access with sessions was deleted")
	}

	repos.accesses.countSessions = 0
	rr = accessRequest(mux, http.MethodDelete, "/api/resources/accesses/1", catalogAdminToken, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204 (body=%s)", rr.Code, rr.Body.String())
	}
	if _, ok := repos.accesses.accesses[access.ID]; ok {
		t.Fatal("access was not deleted")
	}

	rr = accessRequest(mux, http.MethodDelete, "/api/resources/accounts/1", catalogAdminToken, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestAccessHandlerRejectsOversizedBody(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)

	body := `{"key":"fixture-user","label":"Fixture user","kind":"human","notes":"` +
		strings.Repeat("a", int(64<<10)) + `"}`
	rr := accessRequest(mux, http.MethodPost, "/api/resources/accounts", catalogAdminToken, body)
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "body_too_large")
	if repos.accounts.createCalls != 0 {
		t.Fatalf("oversized body reached the service: create calls=%d", repos.accounts.createCalls)
	}

	rr = accessRequest(mux, http.MethodPost, "/api/resources/accounts", catalogAdminToken, `{"key":`)
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")
}

// TestAccessHandlerLoginFlowBinding covers exposing login_flow_id on reads and
// bind/keep/unbind on writes (spec §5.9, P6 auto-login).
func TestAccessHandlerLoginFlowBinding(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	repos.accounts.seed("fixture-user")
	system := repos.systems.seed("fixture-web")
	flow := repos.flows.seed(domain.AuthFlow{SystemID: system.ID, Name: "fixture login", Enabled: true})
	flowID := strconv.FormatInt(flow.ID, 10)

	createBody := `{"systemId":1,"authType":"password","credential":"s3cret","enabled":true,` +
		`"concurrencyLimit":1,"login_flow_id":"` + flowID + `"}`
	rr := accessRequest(mux, http.MethodPost, "/api/resources/accounts/1/accesses", catalogAdminToken, createBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	created := decodeAccessData[map[string]any](t, rr)
	if created["login_flow_id"] != flowID {
		t.Fatalf("login_flow_id=%v want %q", created["login_flow_id"], flowID)
	}
	if access := repos.accesses.accesses[1]; access.LoginFlowID == nil || *access.LoginFlowID != flow.ID {
		t.Fatalf("stored login flow=%v want %d", access.LoginFlowID, flow.ID)
	}
	assertNoCredentialLeak(t, rr, "s3cret")

	// GET detail exposes the decimal string.
	rr = accessRequest(mux, http.MethodGet, "/api/resources/accesses/1", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if detail := decodeAccessData[map[string]any](t, rr); detail["login_flow_id"] != flowID {
		t.Fatalf("detail login_flow_id=%v want %q", detail["login_flow_id"], flowID)
	}

	// GET list exposes it too.
	rr = accessRequest(mux, http.MethodGet, "/api/resources/accounts/1/accesses", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items := decodeAccessData[[]map[string]any](t, rr)
	if len(items) != 1 || items[0]["login_flow_id"] != flowID {
		t.Fatalf("list items=%v want login_flow_id %q", items, flowID)
	}

	// An omitted login_flow_id keeps the stored binding.
	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"permissionNote":"read-only"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched := decodeAccessData[map[string]any](t, rr)
	if patched["login_flow_id"] != flowID || patched["permissionNote"] != "read-only" {
		t.Fatalf("patched=%v want the binding kept", patched)
	}

	// An explicit null unbinds it and reads back as null.
	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"login_flow_id":null}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	cleared := decodeAccessData[map[string]any](t, rr)
	if value, ok := cleared["login_flow_id"]; !ok || value != nil {
		t.Fatalf("login_flow_id=%v present=%v want an explicit null", value, ok)
	}
	if repos.accesses.accesses[1].LoginFlowID != nil {
		t.Fatal("explicit null did not clear the stored login_flow_id")
	}

	// Rebinding works after an unbind.
	rr = accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"login_flow_id":"`+flowID+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if rebounded := decodeAccessData[map[string]any](t, rr); rebounded["login_flow_id"] != flowID {
		t.Fatalf("login_flow_id=%v want %q", rebounded["login_flow_id"], flowID)
	}
}

// TestAccessHandlerRejectsInvalidLoginFlow maps every invalid binding to 422
// auth_flow_invalid without writing (spec §5.9).
func TestAccessHandlerRejectsInvalidLoginFlow(t *testing.T) {
	mux, repos := newAccessHandlerTestServer(t)
	repos.accounts.seed("fixture-user")
	system := repos.systems.seed("fixture-web")
	otherSystem := repos.systems.seed("fixture-api")
	repos.accesses.seed(domain.Access{
		AccountID: 1, SystemID: system.ID, AuthType: domain.AuthTypePassword,
		Enabled: true, ConcurrencyLimit: 1,
	})
	disabledFlow := repos.flows.seed(domain.AuthFlow{SystemID: system.ID, Name: "disabled", Enabled: false})
	otherSystemFlow := repos.flows.seed(domain.AuthFlow{SystemID: otherSystem.ID, Name: "other", Enabled: true})
	enabledFlow := repos.flows.seed(domain.AuthFlow{SystemID: system.ID, Name: "enabled", Enabled: true})

	invalidValues := []string{
		`"abc"`,
		`""`,
		`"0"`,
		`7`,
		`"999"`,
		`"` + strconv.FormatInt(disabledFlow.ID, 10) + `"`,
		`"` + strconv.FormatInt(otherSystemFlow.ID, 10) + `"`,
	}
	for _, value := range invalidValues {
		createBody := `{"systemId":1,"authType":"password","credential":"s3cret","enabled":true,` +
			`"concurrencyLimit":1,"login_flow_id":` + value + `}`
		rr := accessRequest(mux, http.MethodPost, "/api/resources/accounts/1/accesses", catalogAdminToken, createBody)
		apiErr := assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "auth_flow_invalid")
		if apiErr.Details["field"] != "login_flow_id" {
			t.Fatalf("details.field=%v want login_flow_id", apiErr.Details["field"])
		}
		if repos.accesses.createCalls != 0 {
			t.Fatal("an invalid login flow reached the repository")
		}
	}

	// Invalid bindings on update are rejected before the repository write too.
	updateCalls := repos.accesses.updateCalls
	for _, value := range invalidValues {
		rr := accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
			`{"login_flow_id":`+value+`}`)
		apiErr := assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "auth_flow_invalid")
		if apiErr.Details["field"] != "login_flow_id" {
			t.Fatalf("details.field=%v want login_flow_id", apiErr.Details["field"])
		}
	}
	if repos.accesses.updateCalls != updateCalls {
		t.Fatal("an invalid login flow reached the repository")
	}

	// A valid same-system enabled flow is accepted on update.
	rr := accessRequest(mux, http.MethodPatch, "/api/resources/accesses/1", catalogAdminToken,
		`{"login_flow_id":"`+strconv.FormatInt(enabledFlow.ID, 10)+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if got := repos.accesses.accesses[1].LoginFlowID; got == nil || *got != enabledFlow.ID {
		t.Fatalf("stored login flow=%v want %d", got, enabledFlow.ID)
	}
}
