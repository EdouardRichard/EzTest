package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// LoginBrowser runs one configured form login and captures the resulting
// browser session state (spec §5.9). It depends on domain types only, so the
// implementation lives outside the resource context (the executor adapter) and
// no execution package leaks into this layer.
//
// The password in LoginRequest is plaintext by decision D28. Implementations
// must pass it to the browser as a secret value and must never write it to
// logs, events, evidence or returned error messages.
type LoginBrowser interface {
	Login(ctx context.Context, req LoginRequest) (LoginResult, error)
}

// LoginRequest is one auto-login attempt. Flow is the stored form recipe;
// LoginURL is the effective login page (the access override when set, otherwise
// the recipe URL). Username is the access override or the account username.
type LoginRequest struct {
	EnvironmentID int64
	SystemID      int64
	AccountID     int64
	AccessID      int64
	Flow          domain.FormDefinition
	LoginURL      string
	Username      string
	Password      string
	Timeout       time.Duration
}

// LoginResult is the outcome of one attempt. Outcome is one of
// domain.LoginOutcomeValid/MFA/Captcha/Invalid/Unreachable. On valid,
// StorageState is the canonical browser storage_state document (cookies and
// origins, each origin's sessionStorage included) and SessionStorage may repeat
// the per-origin sessionStorage map for consumers that inject it directly.
// ExpiresAt optionally carries an expiry the adapter observed (for example from
// a cookie); the service still caps it by the state's earliest cookie expiry.
type LoginResult struct {
	Outcome        string
	StorageState   json.RawMessage
	SessionStorage json.RawMessage
	ExpiresAt      *time.Time
}
