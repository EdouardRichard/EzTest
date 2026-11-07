package versionapi_test

import (
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// TestVersionAccountsListHandler pins API-18 read: GET
// /api/versions/{id}/accounts returns the version's bound account subset.
func TestVersionAccountsListHandler(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}/accounts", versionapi.NewVersionAccountHandler(svc).List)

	recorder := call(t, mux, http.MethodGet, "/api/versions/7/accounts", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if svc.getCalls != 1 || svc.lastID != 7 {
		t.Fatalf("service call = %d/%d", svc.getCalls, svc.lastID)
	}
	items := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("accounts = %#v", items)
	}
	if account := items[0].(map[string]any); account["id"] != "1601" || account["username"] != "order-admin" {
		t.Fatalf("account = %#v", account)
	}
	if recorder := call(t, mux, http.MethodGet, "/api/versions/not-an-id/accounts", "", actor()); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid path status = %d", recorder.Code)
	}
}

// TestVersionAccountsAddHandler pins API-18 write: POST
// /api/versions/{id}/accounts adds account IDs to the bound subset through the
// existing binding update, requires an authenticated actor and reports typed
// 400/404/409 errors.
func TestVersionAccountsAddHandler(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/versions/{id}/accounts", versionapi.NewVersionAccountHandler(svc).Add)

	recorder := call(t, mux, http.MethodPost, "/api/versions/7/accounts", `{"account_ids":["1602"]}`, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if svc.getCalls != 1 || svc.updCalls != 1 || svc.lastActor != 42 {
		t.Fatalf("service calls = get %d/update %d/actor %d", svc.getCalls, svc.updCalls, svc.lastActor)
	}
	patch := svc.lastPatch
	if patch.AccountIDs == nil || len(*patch.AccountIDs) != 2 ||
		(*patch.AccountIDs)[0] != 1601 || (*patch.AccountIDs)[1] != 1602 {
		t.Fatalf("account_ids patch = %#v, want the current subset plus the added id", patch.AccountIDs)
	}
	if dataList(t, recorder) == nil {
		t.Fatal("add response is not a list")
	}

	anonymous := call(t, mux, http.MethodPost, "/api/versions/7/accounts", `{"account_ids":["1602"]}`, nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}
	for _, body := range []string{`{}`, `{"account_ids":[]}`, `{"account_ids":["abc"]}`, `{"account_ids":["0"]}`} {
		recorder := call(t, mux, http.MethodPost, "/api/versions/7/accounts", body, actor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d (%s)", body, recorder.Code, recorder.Body.String())
		}
	}

	svc.err = platform.NotFound("version")
	missing := call(t, mux, http.MethodPost, "/api/versions/7/accounts", `{"account_ids":["1602"]}`, actor())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing version status = %d", missing.Code)
	}
	svc.err = platform.StateConflict("version binding is read-only after completion")
	readOnly := call(t, mux, http.MethodPost, "/api/versions/7/accounts", `{"account_ids":["1602"]}`, actor())
	if readOnly.Code != http.StatusConflict {
		t.Fatalf("read-only status = %d", readOnly.Code)
	}
}

// TestRegisterVersionAccountRoutes pins the mount helper for Task 37.
func TestRegisterVersionAccountRoutes(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	mux := http.NewServeMux()
	auth := func(next http.Handler) http.Handler { return next }
	versionapi.RegisterVersionAccountRoutes(mux, auth, versionapi.NewVersionAccountHandler(svc))

	if recorder := call(t, mux, http.MethodGet, "/api/versions/7/accounts", "", actor()); recorder.Code != http.StatusOK {
		t.Fatalf("GET registered route = %d", recorder.Code)
	}
	if recorder := call(t, mux, http.MethodPost, "/api/versions/7/accounts", `{"account_ids":["1602"]}`, actor()); recorder.Code != http.StatusOK {
		t.Fatalf("POST registered route = %d (%s)", recorder.Code, recorder.Body.String())
	}
}

func TestRegisterVersionAccountRoutesRejectsNilAuth(t *testing.T) {
	panics(t, "nil auth", func() {
		versionapi.RegisterVersionAccountRoutes(http.NewServeMux(), nil, versionapi.NewVersionAccountHandler(nil))
	})
	panics(t, "nil mux", func() {
		versionapi.RegisterVersionAccountRoutes(nil, func(next http.Handler) http.Handler { return next }, versionapi.NewVersionAccountHandler(nil))
	})
}

func panics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s did not panic", name)
		}
	}()
	fn()
}
