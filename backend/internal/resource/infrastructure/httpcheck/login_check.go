// Package httpcheck implements the environment's read-only login_check over
// HTTP (spec §6.3). It issues one GET request with the stored session state and
// reports the observed result. Transport failures are reported in the result as
// observed=error with reason env_unreachable; they never become returned errors,
// so a failed check cannot block saving a Session (spec §7.1).
package httpcheck

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

const (
	// defaultTimeout bounds one login_check request.
	defaultTimeout = 10 * time.Second
	// maxBodyBytes caps the response body read for body_contains.
	maxBodyBytes = 1 << 20
	// reasonNotConfigured distinguishes a skipped check from a failed one.
	reasonNotConfigured = "login_check_not_configured"
	// reasonUnreachable is the stable failure_reason for network/timeout.
	reasonUnreachable = "env_unreachable"
)

// Checker performs login_check requests. Create it with NewChecker; it is safe
// for concurrent use once constructed.
type Checker struct {
	client *http.Client
}

// NewChecker returns a Checker with the 10s timeout and the default redirect
// policy (at most 10 hops) required by spec §6.3.
func NewChecker() *Checker {
	return &Checker{client: &http.Client{Timeout: defaultTimeout}}
}

// Check performs env's configured login_check with state and reports the
// observed outcome. A nil or empty LoginCheck is not configured and returns a
// degraded result without making a request. HTTP statuses outside ExpectStatus
// (default [200]) or a missing body_contains marker yield observed=denied;
// network, timeout and read failures yield observed=error with reason
// env_unreachable. The error return is reserved for callers that cannot
// interpret a result at all; this adapter reports every runtime failure in the
// result.
func (c *Checker) Check(ctx context.Context, env domain.Environment, state domain.StorageState) (domain.LoginCheckResult, error) {
	loginCheck := env.LoginCheck
	if loginCheck == nil || loginCheck.URL == "" {
		return domain.LoginCheckResult{
			Degraded: true,
			Reason:   reasonNotConfigured,
		}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginCheckURL(env.BaseURL, loginCheck.URL), nil)
	if err != nil {
		return unreachableResult(), nil
	}
	applyState(req, state)

	resp, err := c.client.Do(req)
	if err != nil {
		return unreachableResult(), nil
	}
	defer func() { _ = resp.Body.Close() }()

	bodyMatches := true
	if loginCheck.BodyContains != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if err != nil {
			return unreachableResult(), nil
		}
		bodyMatches = bytes.Contains(body, []byte(loginCheck.BodyContains))
	}

	if bodyMatches && statusMatches(loginCheck.ExpectStatus, resp.StatusCode) {
		return domain.LoginCheckResult{
			Observed: domain.ObservedAllowed,
			Status:   resp.StatusCode,
		}, nil
	}
	return domain.LoginCheckResult{
		Observed: domain.ObservedDenied,
		Status:   resp.StatusCode,
	}, nil
}

// loginCheckURL returns raw when it is already an absolute http(s) URL and
// otherwise joins it with baseURL, avoiding a doubled slash at the seam.
func loginCheckURL(baseURL, raw string) string {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	switch {
	case strings.HasSuffix(baseURL, "/") && strings.HasPrefix(raw, "/"):
		return baseURL + strings.TrimPrefix(raw, "/")
	case !strings.HasSuffix(baseURL, "/") && !strings.HasPrefix(raw, "/"):
		return baseURL + "/" + raw
	default:
		return baseURL + raw
	}
}

// applyState adds the request credentials: cookies for a storage_state and the
// stored headers for a token. Header names/values are already validated by the
// domain parser; Host and Content-Length are skipped defensively because the
// transport owns them.
func applyState(req *http.Request, state domain.StorageState) {
	if cookie := cookieHeader(state.Cookies); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	for name, value := range state.Headers {
		switch strings.ToLower(name) {
		case "", "host", "content-length":
			continue
		}
		req.Header.Set(name, value)
	}
}

// cookieHeader builds the Cookie header value from name=value pairs only
// (spec §6.3).
func cookieHeader(cookies []domain.Cookie) string {
	var builder strings.Builder
	for _, cookie := range cookies {
		if cookie.Name == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("; ")
		}
		builder.WriteString(cookie.Name)
		builder.WriteByte('=')
		builder.WriteString(cookie.Value)
	}
	return builder.String()
}

// statusMatches reports whether status is in expected; an empty list defaults
// to [200] per spec §6.3.
func statusMatches(expected []int, status int) bool {
	if len(expected) == 0 {
		return status == http.StatusOK
	}
	for _, want := range expected {
		if want == status {
			return true
		}
	}
	return false
}

// unreachableResult is the observed-error result for transport failures.
func unreachableResult() domain.LoginCheckResult {
	return domain.LoginCheckResult{
		Observed: domain.ObservedError,
		Reason:   reasonUnreachable,
	}
}
