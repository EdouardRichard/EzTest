package httpcheck_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/httpcheck"
)

const (
	loginCheckNotConfigured = "login_check_not_configured"
	envUnreachable          = "env_unreachable"
)

func environment(baseURL string, loginCheck *domain.LoginCheck) domain.Environment {
	return domain.Environment{BaseURL: baseURL, LoginCheck: loginCheck}
}

func TestCheckPassesOnStatusAndBodyWithCookies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/account" {
			t.Errorf("path = %q, want /account", r.URL.Path)
		}
		if got, want := r.Header.Get("Cookie"), "sid=abc; theme=dark"; got != want {
			t.Errorf("Cookie = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte("Fixture Account"))
	}))
	defer srv.Close()

	env := environment(srv.URL+"/", &domain.LoginCheck{
		URL:          "/account",
		ExpectStatus: []int{http.StatusOK},
		BodyContains: "Fixture Account",
	})
	state := domain.StorageState{
		Kind: domain.StorageStateKindStorageState,
		Cookies: []domain.Cookie{
			{Name: "sid", Value: "abc"},
			{Name: "theme", Value: "dark"},
		},
	}

	got, err := httpcheck.NewChecker().Check(context.Background(), env, state)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if got.Observed != domain.ObservedAllowed {
		t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedAllowed)
	}
	if got.Status != http.StatusOK {
		t.Fatalf("Status = %d, want %d", got.Status, http.StatusOK)
	}
	if got.Degraded || got.Reason != "" {
		t.Fatalf("Degraded = %v, Reason = %q, want false and empty", got.Degraded, got.Reason)
	}
}

func TestCheckNotConfiguredIsDegraded(t *testing.T) {
	requested := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requested <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	tests := []struct {
		name       string
		loginCheck *domain.LoginCheck
	}{
		{name: "nil login check", loginCheck: nil},
		{name: "empty url", loginCheck: &domain.LoginCheck{ExpectStatus: []int{http.StatusOK}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, tc.loginCheck), domain.StorageState{})
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			if !got.Degraded {
				t.Fatalf("Degraded = false, want true")
			}
			if got.Reason != loginCheckNotConfigured {
				t.Fatalf("Reason = %q, want %q", got.Reason, loginCheckNotConfigured)
			}
			if got.Observed != "" || got.Status != 0 {
				t.Fatalf("Observed = %q, Status = %d, want empty and 0", got.Observed, got.Status)
			}
		})
	}

	select {
	case <-requested:
		t.Fatal("unconfigured login_check performed an HTTP request")
	default:
	}
}

func TestCheckMatchesExpectedStatus(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		expect   []int
		observed string
	}{
		{
			name:     "configured status matches",
			status:   http.StatusUnauthorized,
			expect:   []int{http.StatusOK, http.StatusUnauthorized},
			observed: domain.ObservedAllowed,
		},
		{
			name:     "status not in list",
			status:   http.StatusUnauthorized,
			expect:   []int{http.StatusOK},
			observed: domain.ObservedDenied,
		},
		{
			name:     "default expects exactly 200",
			status:   http.StatusCreated,
			expect:   nil,
			observed: domain.ObservedDenied,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, &domain.LoginCheck{
				URL:          "/account",
				ExpectStatus: tc.expect,
			}), domain.StorageState{})
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			if got.Observed != tc.observed {
				t.Fatalf("Observed = %q, want %q", got.Observed, tc.observed)
			}
			if got.Status != tc.status {
				t.Fatalf("Status = %d, want %d", got.Status, tc.status)
			}
			if got.Degraded || got.Reason != "" {
				t.Fatalf("Degraded = %v, Reason = %q, want false and empty", got.Degraded, got.Reason)
			}
		})
	}
}

func TestCheckDeniedOnBodyMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Sign in"))
	}))
	defer srv.Close()

	got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, &domain.LoginCheck{
		URL:          "/account",
		ExpectStatus: []int{http.StatusOK},
		BodyContains: "Fixture Account",
	}), domain.StorageState{})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if got.Observed != domain.ObservedDenied {
		t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedDenied)
	}
	if got.Status != http.StatusOK {
		t.Fatalf("Status = %d, want %d", got.Status, http.StatusOK)
	}
}

func TestCheckSendsTokenHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer fixture-token"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("X-Client"), "fixture"; got != want {
			t.Errorf("X-Client = %q, want %q", got, want)
		}
		if got := r.Header.Get("Cookie"); got != "" {
			t.Errorf("Cookie = %q, want no cookie header", got)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	state := domain.StorageState{
		Kind: domain.StorageStateKindToken,
		Headers: map[string]string{
			"Authorization": "Bearer fixture-token",
			"X-Client":      "fixture",
		},
	}
	got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, &domain.LoginCheck{
		URL:          "/api/profile",
		ExpectStatus: []int{http.StatusOK},
	}), state)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if got.Observed != domain.ObservedAllowed {
		t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedAllowed)
	}
}

func TestCheckUsesAbsoluteURLAsIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account" {
			t.Errorf("path = %q, want /account", r.URL.Path)
		}
		_, _ = w.Write([]byte("Fixture Account"))
	}))
	defer srv.Close()

	// The base URL points at a closed port: an absolute login_check URL must
	// not be joined with it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	baseURL := "http://" + ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	got, err := httpcheck.NewChecker().Check(context.Background(), environment(baseURL, &domain.LoginCheck{
		URL:          srv.URL + "/account",
		ExpectStatus: []int{http.StatusOK},
		BodyContains: "Fixture Account",
	}), domain.StorageState{})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if got.Observed != domain.ObservedAllowed {
		t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedAllowed)
	}
}

func TestCheckJoinsRelativeURLWithoutLeadingSlash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account" {
			t.Errorf("path = %q, want /account", r.URL.Path)
		}
		_, _ = w.Write([]byte("Fixture Account"))
	}))
	defer srv.Close()

	got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, &domain.LoginCheck{
		URL:          "account",
		ExpectStatus: []int{http.StatusOK},
		BodyContains: "Fixture Account",
	}), domain.StorageState{})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if got.Observed != domain.ObservedAllowed {
		t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedAllowed)
	}
}

func TestCheckFollowsRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/account", http.StatusFound)
	})
	mux.HandleFunc("/account", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Fixture Account"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, &domain.LoginCheck{
		URL:          "/login",
		ExpectStatus: []int{http.StatusOK},
		BodyContains: "Fixture Account",
	}), domain.StorageState{})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if got.Observed != domain.ObservedAllowed {
		t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedAllowed)
	}
	if got.Status != http.StatusOK {
		t.Fatalf("Status = %d, want %d", got.Status, http.StatusOK)
	}
}

func TestCheckReportsUnreachableAsObservedError(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		baseURL := "http://" + ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatalf("close listener: %v", err)
		}

		got, err := httpcheck.NewChecker().Check(context.Background(), environment(baseURL, &domain.LoginCheck{
			URL: "/account",
		}), domain.StorageState{})
		if err != nil {
			t.Fatalf("Check() error = %v, want nil (transport failures are observed, not errors)", err)
		}
		if got.Observed != domain.ObservedError {
			t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedError)
		}
		if got.Reason != envUnreachable {
			t.Fatalf("Reason = %q, want %q", got.Reason, envUnreachable)
		}
		if got.Status != 0 {
			t.Fatalf("Status = %d, want 0", got.Status)
		}
		if got.Degraded {
			t.Fatalf("Degraded = true, want false")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
		}))
		defer srv.Close()
		defer close(release)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		got, err := httpcheck.NewChecker().Check(ctx, environment(srv.URL, &domain.LoginCheck{
			URL: "/account",
		}), domain.StorageState{})
		if err != nil {
			t.Fatalf("Check() error = %v, want nil (timeouts are observed, not errors)", err)
		}
		if got.Observed != domain.ObservedError {
			t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedError)
		}
		if got.Reason != envUnreachable {
			t.Fatalf("Reason = %q, want %q", got.Reason, envUnreachable)
		}
	})

	t.Run("truncated response body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("short"))
		}))
		defer srv.Close()

		got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, &domain.LoginCheck{
			URL:          "/account",
			BodyContains: "short",
		}), domain.StorageState{})
		if err != nil {
			t.Fatalf("Check() error = %v, want nil (read failures are observed, not errors)", err)
		}
		if got.Observed != domain.ObservedError {
			t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedError)
		}
		if got.Reason != envUnreachable {
			t.Fatalf("Reason = %q, want %q", got.Reason, envUnreachable)
		}
	})
}

func TestCheckReadsAtMostOneMiB(t *testing.T) {
	const bodyCap = 1 << 20
	const marker = "MARKER-AT-BODY-CAP"

	t.Run("marker ending inside the cap is found", func(t *testing.T) {
		body := strings.Repeat("a", bodyCap-len(marker)) + marker
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()

		got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, &domain.LoginCheck{
			URL:          "/account",
			ExpectStatus: []int{http.StatusOK},
			BodyContains: marker,
		}), domain.StorageState{})
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if got.Observed != domain.ObservedAllowed {
			t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedAllowed)
		}
	})

	t.Run("marker beyond the cap is not read", func(t *testing.T) {
		body := strings.Repeat("a", bodyCap) + marker
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()

		got, err := httpcheck.NewChecker().Check(context.Background(), environment(srv.URL, &domain.LoginCheck{
			URL:          "/account",
			ExpectStatus: []int{http.StatusOK},
			BodyContains: marker,
		}), domain.StorageState{})
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if got.Observed != domain.ObservedDenied {
			t.Fatalf("Observed = %q, want %q", got.Observed, domain.ObservedDenied)
		}
	})
}
