package domain

import "time"

// Auth types, mirrored from the account_system_access.auth_type CHECK
// constraint.
const (
	AuthTypePassword = "password"
	AuthTypeSSO      = "sso"
	AuthTypeToken    = "token"
	AuthTypeAPIKey   = "apikey"
	AuthTypeBasic    = "basic"
	AuthTypeManual   = "manual"
)

// Access links an Account to a System and holds its login configuration.
// Credential is stored in plaintext by decision D28; it is never returned by
// the HTTP API (handlers expose credentialSet). SessionTTLSeconds is nil to
// inherit the System default. LoginFlowID is the optional auto-login recipe
// (auth_flow.login_flow_id); nil means no recipe is bound.
type Access struct {
	ID                int64
	AccountID         int64
	SystemID          int64
	AuthType          string
	Credential        string
	UsernameOverride  string
	LoginURL          string
	LoginFlowID       *int64
	Enabled           bool
	ConcurrencyLimit  int
	SessionTTLSeconds *int
	PermissionNote    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
