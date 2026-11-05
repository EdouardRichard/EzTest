package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// fakeCheckExecutorAuthChecker records the endpoint the handler resolved from
// the repository and returns a canned auth status.
type fakeCheckExecutorAuthChecker struct {
	status   executionapp.ExecutorAuthStatus
	detail   string
	endpoint string
	calls    int
}

func (f *fakeCheckExecutorAuthChecker) Check(_ context.Context, endpoint string) (executionapp.ExecutorAuthStatus, string) {
	f.calls++
	f.endpoint = endpoint
	return f.status, f.detail
}

// newCheckExecutorMux mounts the execution routes with pass-through auth/admin
// guards: the guard itself is covered elsewhere, and these tests pin the
// handler's repository/checker mapping.
func newCheckExecutorMux(t *testing.T, repo *internalRepo, checker executionapp.ExecutorAuthChecker) *http.ServeMux {
	t.Helper()
	handler := New(Deps{Executors: repo, AuthChecker: checker})
	mux := http.NewServeMux()
	passthrough := func(next http.Handler) http.Handler { return next }
	handler.Register(mux, passthrough, passthrough)
	return mux
}

func sendCheckExecutor(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
	return recorder
}

// TestCheckExecutorReturnsAuthStatus covers the three D8 outcomes through the
// admin route: ok, unauthenticated (token rejected/missing) and unreachable.
func TestCheckExecutorReturnsAuthStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status executionapp.ExecutorAuthStatus
		detail string
	}{
		{name: "ok", status: executionapp.ExecutorAuthOK, detail: ""},
		{name: "unauthenticated", status: executionapp.ExecutorAuthUnauthenticated, detail: "executor returned HTTP 401"},
		{name: "unreachable", status: executionapp.ExecutorAuthUnreachable, detail: "connection refused"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			repo := newInternalRepo()
			repo.rows[7] = execution.ExecutorInstance{ID: 7, Endpoint: "http://127.0.0.1:13336"}
			checker := &fakeCheckExecutorAuthChecker{status: tc.status, detail: tc.detail}

			recorder := sendCheckExecutor(t, newCheckExecutorMux(t, repo, checker), "/api/admin/executors/7/check")
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
			}
			var envelope struct {
				Data struct {
					ExecutorID string `json:"executor_id"`
					Endpoint   string `json:"endpoint"`
					AuthStatus string `json:"auth_status"`
					Detail     string `json:"detail"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode %q: %v", recorder.Body.String(), err)
			}
			if envelope.Data.ExecutorID != "7" {
				t.Fatalf("executor_id = %q, want decimal 7", envelope.Data.ExecutorID)
			}
			if envelope.Data.Endpoint != "http://127.0.0.1:13336" {
				t.Fatalf("endpoint = %q", envelope.Data.Endpoint)
			}
			if envelope.Data.AuthStatus != string(tc.status) {
				t.Fatalf("auth_status = %q, want %q", envelope.Data.AuthStatus, tc.status)
			}
			if envelope.Data.Detail != tc.detail {
				t.Fatalf("detail = %q, want %q", envelope.Data.Detail, tc.detail)
			}
			if checker.calls != 1 || checker.endpoint != "http://127.0.0.1:13336" {
				t.Fatalf("checker calls = %d endpoint = %q, want one call with the stored endpoint", checker.calls, checker.endpoint)
			}
		})
	}
}

func TestCheckExecutorUnknownExecutorIs404(t *testing.T) {
	recorder := sendCheckExecutor(t, newCheckExecutorMux(t, newInternalRepo(), &fakeCheckExecutorAuthChecker{}), "/api/admin/executors/99/check")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	assertErrorCode(t, recorder, "not_found")
}

func TestCheckExecutorWithoutCheckerIs503(t *testing.T) {
	repo := newInternalRepo()
	repo.rows[7] = execution.ExecutorInstance{ID: 7, Endpoint: "http://127.0.0.1:13336"}
	recorder := sendCheckExecutor(t, newCheckExecutorMux(t, repo, nil), "/api/admin/executors/7/check")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	assertErrorCode(t, recorder, "unavailable")
}

func TestCheckExecutorRejectsInvalidID(t *testing.T) {
	recorder := sendCheckExecutor(t, newCheckExecutorMux(t, newInternalRepo(), &fakeCheckExecutorAuthChecker{}), "/api/admin/executors/not-a-number/check")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	assertErrorCode(t, recorder, "invalid_argument")
}
