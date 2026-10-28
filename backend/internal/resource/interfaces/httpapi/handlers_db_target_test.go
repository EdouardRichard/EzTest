package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// dbTargetHTTPTestIDs mints deterministic ids for the handler tests.
type dbTargetHTTPTestIDs struct {
	next int64
}

// Next returns the next id.
func (g *dbTargetHTTPTestIDs) Next() int64 {
	g.next++
	return 6000 + g.next
}

// dbTargetHTTPTestRepo is an in-memory DBTargetRepository.
type dbTargetHTTPTestRepo struct {
	targets     map[int64]domain.DBTarget
	upserts     int
	deletes     int
	testUpdates []dbTargetHTTPTestUpdate
}

// dbTargetHTTPTestUpdate captures one last_test_* write.
type dbTargetHTTPTestUpdate struct {
	targetID int64
	status   string
	message  string
}

func newDBTargetHTTPTestRepo() *dbTargetHTTPTestRepo {
	return &dbTargetHTTPTestRepo{targets: map[int64]domain.DBTarget{}}
}

func (r *dbTargetHTTPTestRepo) GetDBTarget(_ context.Context, environmentID int64) (*domain.DBTarget, error) {
	target, ok := r.targets[environmentID]
	if !ok {
		return nil, nil
	}
	copied := target
	return &copied, nil
}

func (r *dbTargetHTTPTestRepo) UpsertDBTarget(_ context.Context, target *domain.DBTarget) error {
	r.upserts++
	target.CreatedAt = time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	target.UpdatedAt = target.CreatedAt
	r.targets[target.EnvironmentID] = *target
	return nil
}

func (r *dbTargetHTTPTestRepo) UpdateDBTargetTestResult(_ context.Context, targetID int64, testedAt time.Time, status, message string) error {
	r.testUpdates = append(r.testUpdates, dbTargetHTTPTestUpdate{targetID: targetID, status: status, message: message})
	for environmentID, target := range r.targets {
		if target.ID != targetID {
			continue
		}
		target.LastTestedAt = &testedAt
		target.LastTestStatus = status
		target.LastTestMessage = message
		r.targets[environmentID] = target
	}
	return nil
}

func (r *dbTargetHTTPTestRepo) DeleteDBTarget(_ context.Context, environmentID int64) error {
	r.deletes++
	delete(r.targets, environmentID)
	return nil
}

// dbTargetHTTPTestProber is a scripted DBTargetProber.
type dbTargetHTTPTestProber struct {
	result   domain.DBTargetTestResult
	err      error
	last     domain.DBTarget
	lastSeen bool
}

func (p *dbTargetHTTPTestProber) TestConnection(_ context.Context, target domain.DBTarget) (domain.DBTargetTestResult, error) {
	p.last = target
	p.lastSeen = true
	return p.result, p.err
}

// dbTargetHTTPTestAudit captures the db target audit events.
type dbTargetHTTPTestAudit struct {
	events []application.DBTargetAuditEvent
	err    error
}

func (a *dbTargetHTTPTestAudit) RecordDBTargetAudit(_ context.Context, event application.DBTargetAuditEvent) error {
	a.events = append(a.events, event)
	return a.err
}

// dbTargetHTTPFixture bundles the handler test dependencies.
type dbTargetHTTPFixture struct {
	audit   *dbTargetHTTPTestAudit
	envs    *catalogHTTPTestEnvironmentRepo
	targets *dbTargetHTTPTestRepo
	prober  *dbTargetHTTPTestProber
	ids     *dbTargetHTTPTestIDs
	handler *http.ServeMux
}

// newDBTargetHTTPFixture mounts the db target routes behind the real auth
// middleware and the admin guard, with one environment (id 7) available.
func newDBTargetHTTPFixture(t *testing.T) *dbTargetHTTPFixture {
	t.Helper()
	envs := newCatalogHTTPTestEnvironmentRepo()
	envs.environments[7] = domain.Environment{
		ID: 7, SystemID: 1, Key: "staging", Name: "Staging", Kind: domain.EnvironmentKindStaging,
	}
	fixture := &dbTargetHTTPFixture{
		audit:   &dbTargetHTTPTestAudit{},
		envs:    envs,
		targets: newDBTargetHTTPTestRepo(),
		prober:  &dbTargetHTTPTestProber{result: domain.DBTargetTestResult{OK: true}},
		ids:     &dbTargetHTTPTestIDs{},
	}
	service := application.NewDBTargetService(fixture.targets, fixture.envs, fixture.prober, fixture.ids,
		catalogHTTPTestClock{now: time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)})
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return identityhttp.AuthMiddleware(catalogHTTPTestAuthenticator{})(identityhttp.RequireAdmin(next))
	}
	NewDBTargetHandler(service, WithDBTargetAuditSink(fixture.audit)).RegisterRoutes(mux, guard)
	fixture.handler = mux
	return fixture
}

// dbTargetDatabasePath is the collection path of environment 7's target.
const dbTargetDatabasePath = "/api/resources/environments/7/db-target"

// putDBTargetBody is a valid upsert body.
const putDBTargetBody = `{"name":"orders","host":"127.0.0.1","port":5432,"database":"eztest_target",` +
	`"username":"eztest_ro","password":"s3cret","sslmode":"disable","connectTimeoutMs":2000,` +
	`"statementTimeoutMs":4000,"maxRows":50,"enabled":true}`

// dbTargetJSON decodes the {"data": ...} envelope into a generic map.
func dbTargetJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return envelope.Data
}

// assertNoPassword proves neither the password field nor the secret value is in
// a response body.
func assertNoPassword(t *testing.T, body []byte) {
	t.Helper()
	if strings.Contains(string(body), "password") || strings.Contains(string(body), "s3cret") {
		t.Fatalf("response leaks the db target password: %s", body)
	}
}

func TestDBTargetRoutesRequireAdmin(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	routes := []struct{ method, path string }{
		{http.MethodGet, dbTargetDatabasePath},
		{http.MethodPut, dbTargetDatabasePath},
		{http.MethodDelete, dbTargetDatabasePath},
		{http.MethodPost, dbTargetDatabasePath + "/test"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rr := catalogRequest(fixture.handler, route.method, route.path, catalogUserToken, "{}")
			if rr.Code != http.StatusForbidden {
				t.Fatalf("regular user = %d body=%s, want 403", rr.Code, rr.Body)
			}
			anonymous := catalogRequest(fixture.handler, route.method, route.path, "", "{}")
			if anonymous.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous = %d body=%s, want 401", anonymous.Code, anonymous.Body)
			}
		})
	}
}

func TestDBTargetHandlerGetMissing(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	rr := catalogRequest(fixture.handler, http.MethodGet, dbTargetDatabasePath, catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(envelope.Data) != "null" {
		t.Fatalf("data=%s want null", envelope.Data)
	}
}

func TestDBTargetHandlerUpsertCreate(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	rr := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, putDBTargetBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	data := dbTargetJSON(t, rr.Body.Bytes())
	if _, ok := data["id"].(string); !ok || data["id"] == "" {
		t.Fatalf("id=%v want a decimal string", data["id"])
	}
	if _, ok := data["environmentId"].(string); !ok || data["environmentId"] != "7" {
		t.Fatalf("environmentId=%v want the string \"7\"", data["environmentId"])
	}
	if data["name"] != "orders" || data["host"] != "127.0.0.1" || data["port"] != float64(5432) ||
		data["database"] != "eztest_target" || data["username"] != "eztest_ro" ||
		data["sslmode"] != "disable" || data["maxRows"] != float64(50) || data["enabled"] != true {
		t.Fatalf("data=%v", data)
	}
	if data["credentialSet"] != true {
		t.Fatalf("credentialSet=%v want true", data["credentialSet"])
	}
	if data["lastTestStatus"] != "" || data["lastTestedAt"] != nil || data["lastTestMessage"] != "" {
		t.Fatalf("data=%v want an untested target", data)
	}
	if _, ok := data["createdAt"].(string); !ok {
		t.Fatalf("createdAt=%v want RFC3339", data["createdAt"])
	}
	assertNoPassword(t, rr.Body.Bytes())
	if fixture.targets.upserts != 1 {
		t.Fatalf("upserts=%d want 1", fixture.targets.upserts)
	}
}

func TestDBTargetHandlerUpsertValidation(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		code   string
		reason string
		field  string
	}{
		{"first password required",
			`{"host":"127.0.0.1","port":5432,"database":"db","username":"ro"}`,
			http.StatusUnprocessableEntity, platform.CodeInvalidArgument, domain.ReasonDBTargetInvalid, "password"},
		{"missing host",
			`{"port":5432,"database":"db","username":"ro","password":"s3cret"}`,
			http.StatusUnprocessableEntity, platform.CodeInvalidArgument, domain.ReasonDBTargetInvalid, "host"},
		{"invalid port",
			`{"host":"127.0.0.1","port":70000,"database":"db","username":"ro","password":"s3cret"}`,
			http.StatusUnprocessableEntity, platform.CodeInvalidArgument, domain.ReasonDBTargetInvalid, "port"},
		{"invalid max rows",
			`{"host":"127.0.0.1","port":5432,"database":"db","username":"ro","password":"s3cret","maxRows":2000}`,
			http.StatusUnprocessableEntity, platform.CodeInvalidArgument, domain.ReasonDBTargetInvalid, "maxRows"},
		{"invalid ssl mode",
			`{"host":"127.0.0.1","port":5432,"database":"db","username":"ro","password":"s3cret","sslmode":"allow"}`,
			http.StatusUnprocessableEntity, platform.CodeInvalidArgument, domain.ReasonDBTargetInvalid, "sslmode"},
		{"unknown field",
			`{"host":"127.0.0.1","port":5432,"database":"db","username":"ro","password":"s3cret","bogus":1}`,
			http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json", ""},
		{"missing environment",
			`{"host":"127.0.0.1","port":5432,"database":"db","username":"ro","password":"s3cret"}`,
			http.StatusUnprocessableEntity, platform.CodeInvalidArgument, domain.ReasonDBTargetInvalid, "environmentId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDBTargetHTTPFixture(t)
			path := dbTargetDatabasePath
			if tc.name == "missing environment" {
				path = "/api/resources/environments/99/db-target"
			}
			rr := catalogRequest(fixture.handler, http.MethodPut, path, catalogAdminToken, tc.body)
			apiErr := assertCatalogErrorEnvelope(t, rr, tc.status, tc.code, tc.reason)
			if tc.field != "" && apiErr.Details["field"] != tc.field {
				t.Fatalf("field=%v want %q", apiErr.Details["field"], tc.field)
			}
			if fixture.targets.upserts != 0 {
				t.Fatal("invalid request reached the repository")
			}
		})
	}
}

func TestDBTargetHandlerUpsertUpdateKeepsPassword(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	first := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, putDBTargetBody)
	if first.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", first.Code, first.Body)
	}
	created := dbTargetJSON(t, first.Body.Bytes())
	updateBody := `{"name":"orders","host":"db2.internal","port":5433,"database":"orders_ro","username":"ro2"}`
	rr := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, updateBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rr.Code, rr.Body)
	}
	data := dbTargetJSON(t, rr.Body.Bytes())
	if data["host"] != "db2.internal" || data["port"] != float64(5433) || data["database"] != "orders_ro" {
		t.Fatalf("data=%v want the updated connection fields", data)
	}
	if data["id"] != created["id"] {
		t.Fatalf("id=%v want the stored id %v", data["id"], created["id"])
	}
	if data["credentialSet"] != true {
		t.Fatal("an omitted password must keep credentialSet true")
	}
	if data["sslmode"] != "disable" || data["maxRows"] != float64(50) {
		t.Fatalf("data=%v want omitted options preserved", data)
	}
	assertNoPassword(t, rr.Body.Bytes())
}

func TestDBTargetHandlerGetAndDelete(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	if rr := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, putDBTargetBody); rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body)
	}

	rr := catalogRequest(fixture.handler, http.MethodGet, dbTargetDatabasePath, catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rr.Code, rr.Body)
	}
	data := dbTargetJSON(t, rr.Body.Bytes())
	if data["credentialSet"] != true || data["host"] != "127.0.0.1" {
		t.Fatalf("data=%v", data)
	}
	assertNoPassword(t, rr.Body.Bytes())

	rr = catalogRequest(fixture.handler, http.MethodDelete, dbTargetDatabasePath, catalogAdminToken, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rr.Code, rr.Body)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("delete body=%q want empty", rr.Body.String())
	}

	rr = catalogRequest(fixture.handler, http.MethodGet, dbTargetDatabasePath, catalogAdminToken, "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"data":null`) {
		t.Fatalf("get after delete status=%d body=%s", rr.Code, rr.Body)
	}
	if fixture.targets.deletes != 1 {
		t.Fatalf("deletes=%d want 1", fixture.targets.deletes)
	}

	rr = catalogRequest(fixture.handler, http.MethodDelete, dbTargetDatabasePath, catalogAdminToken, "")
	assertCatalogErrorEnvelope(t, rr, http.StatusConflict, platform.CodeConflict, domain.ReasonDBTargetMissing)
}

func TestDBTargetHandlerTestConnectionSuccess(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	if rr := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, putDBTargetBody); rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body)
	}
	fixture.prober.result = domain.DBTargetTestResult{
		OK: true, LatencyMS: 12, ServerVersion: "PostgreSQL 17.0", CurrentUser: "eztest_ro",
		TransactionReadOnly: true, Warnings: []string{"role_has_write_privileges"},
	}
	rr := catalogRequest(fixture.handler, http.MethodPost, dbTargetDatabasePath+"/test", catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	data := dbTargetJSON(t, rr.Body.Bytes())
	if data["ok"] != true || data["latencyMs"] != float64(12) || data["serverVersion"] != "PostgreSQL 17.0" ||
		data["currentUser"] != "eztest_ro" || data["transactionReadOnly"] != true {
		t.Fatalf("data=%v", data)
	}
	warnings, ok := data["warnings"].([]any)
	if !ok || len(warnings) != 1 || warnings[0] != "role_has_write_privileges" {
		t.Fatalf("warnings=%v", data["warnings"])
	}
	if _, ok := data["error"]; ok {
		t.Fatalf("data=%v must not carry an error on success", data)
	}
	if fixture.prober.last.Password != "s3cret" || fixture.prober.last.Host != "127.0.0.1" {
		t.Fatalf("prober target=%+v want the stored target", fixture.prober.last)
	}
	if len(fixture.targets.testUpdates) != 1 || fixture.targets.testUpdates[0].status != domain.DBTargetTestStatusOK {
		t.Fatalf("test updates=%+v want one ok update", fixture.targets.testUpdates)
	}
}

func TestDBTargetHandlerTestConnectionFailure(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	if rr := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, putDBTargetBody); rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body)
	}
	fixture.prober.result = domain.DBTargetTestResult{
		OK: false, LatencyMS: 5,
		Error: &domain.DBTargetTestError{Reason: domain.ReasonDBConnectionFailed, Message: "connection refused"},
	}
	rr := catalogRequest(fixture.handler, http.MethodPost, dbTargetDatabasePath+"/test", catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 with ok:false", rr.Code, rr.Body)
	}
	data := dbTargetJSON(t, rr.Body.Bytes())
	if data["ok"] != false {
		t.Fatalf("data=%v want ok:false", data)
	}
	errorData, ok := data["error"].(map[string]any)
	if !ok || errorData["reason"] != domain.ReasonDBConnectionFailed || errorData["message"] != "connection refused" {
		t.Fatalf("error=%v", data["error"])
	}
	assertNoPassword(t, rr.Body.Bytes())
	if len(fixture.targets.testUpdates) != 1 || fixture.targets.testUpdates[0].status != domain.DBTargetTestStatusFailed {
		t.Fatalf("test updates=%+v want one failed update", fixture.targets.testUpdates)
	}
}

func TestDBTargetHandlerTestConnectionOverride(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	if rr := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, putDBTargetBody); rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body)
	}
	rr := catalogRequest(fixture.handler, http.MethodPost, dbTargetDatabasePath+"/test", catalogAdminToken,
		`{"name":"orders","host":"override.internal","port":6000,"database":"other","username":"other_ro","password":"override-secret"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	if fixture.prober.last.Host != "override.internal" || fixture.prober.last.Password != "override-secret" {
		t.Fatalf("prober target=%+v want the form override", fixture.prober.last)
	}
	if len(fixture.targets.testUpdates) != 0 {
		t.Fatal("an unsaved override must not rewrite the stored last_test_* fields")
	}
	stored := fixture.targets.targets[7]
	if stored.Host != "127.0.0.1" {
		t.Fatalf("stored host=%q want the saved target", stored.Host)
	}
}

func TestDBTargetHandlerTestConnectionMissingTarget(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	rr := catalogRequest(fixture.handler, http.MethodPost, dbTargetDatabasePath+"/test", catalogAdminToken, "")
	assertCatalogErrorEnvelope(t, rr, http.StatusConflict, platform.CodeConflict, domain.ReasonDBTargetMissing)
	if fixture.prober.lastSeen {
		t.Fatal("a missing target reached the prober")
	}
}

func TestDBTargetHandlerAuditsWithoutSecrets(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	if rr := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, putDBTargetBody); rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body)
	}
	if rr := catalogRequest(fixture.handler, http.MethodPost, dbTargetDatabasePath+"/test", catalogAdminToken, ""); rr.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", rr.Code, rr.Body)
	}
	if rr := catalogRequest(fixture.handler, http.MethodDelete, dbTargetDatabasePath, catalogAdminToken, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rr.Code, rr.Body)
	}

	actions := make([]string, len(fixture.audit.events))
	for i, event := range fixture.audit.events {
		actions[i] = event.Action
		if event.TargetType != application.DBTargetAuditTargetType || event.TargetID == "" || event.ActorID == 0 {
			t.Fatalf("event=%+v want a target id and the admin actor", event)
		}
		if encoded, err := json.Marshal(event); err != nil || strings.Contains(string(encoded), "s3cret") {
			t.Fatalf("audit event leaks the password: %s (%v)", encoded, err)
		}
	}
	want := []string{
		application.ActionDBTargetUpserted,
		application.ActionDBTargetTested,
		application.ActionDBTargetDeleted,
	}
	if len(actions) != len(want) {
		t.Fatalf("actions=%v want %v", actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("actions=%v want %v", actions, want)
		}
	}
	for _, event := range fixture.audit.events {
		if event.Metadata["environment_id"] != int64(7) && event.Metadata["environment_id"] != "7" {
			t.Fatalf("event metadata=%v want the environment id", event.Metadata)
		}
		if _, ok := event.Metadata["password"]; ok {
			t.Fatal("audit metadata must not carry the password key")
		}
	}
}

func TestDBTargetHandlerAuditFailureDoesNotFailRequest(t *testing.T) {
	fixture := newDBTargetHTTPFixture(t)
	fixture.audit.err = errors.New("audit store is down")
	rr := catalogRequest(fixture.handler, http.MethodPut, dbTargetDatabasePath, catalogAdminToken, putDBTargetBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want the audit failure to be best effort", rr.Code, rr.Body)
	}
}
