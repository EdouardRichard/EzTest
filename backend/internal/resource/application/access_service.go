package application

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Access limits mirror the account_system_access CHECK constraints (spec
// §5.2): concurrency_limit BETWEEN 1 AND 100 and a nullable
// session_ttl_seconds between 60 and 604800.
const (
	minAccessConcurrencyLimit  = 1
	maxAccessConcurrencyLimit  = 100
	minAccessSessionTTLSeconds = 60
	maxAccessSessionTTLSeconds = 604800
)

// AccessService implements the account and account/system access use cases. It
// fixes the company to the seeded default one; requests never carry a company
// id, and credentials are write-only below the HTTP layer (spec §7.4).
type AccessService struct {
	companies domain.CompanyRepository
	accounts  domain.AccountRepository
	accesses  domain.AccessRepository
	systems   domain.SystemRepository
	authFlows domain.AuthFlowRepository
}

// CreateAccountInput is the create-account request.
type CreateAccountInput struct {
	Key      string
	Label    string
	Username string
	Kind     string
	Notes    string
}

// UpdateAccountInput is the partial update-account request. A nil field is left
// unchanged; key and company are immutable after creation.
type UpdateAccountInput struct {
	Label    *string
	Username *string
	Kind     *string
	Notes    *string
	Status   *string
}

// CreateAccessInput is the create-access request. LoginFlowID optionally binds
// the access to an enabled login recipe of the same system; nil leaves it
// unbound.
type CreateAccessInput struct {
	SystemID          int64
	AuthType          string
	Credential        string
	UsernameOverride  string
	LoginURL          string
	LoginFlowID       *int64
	PermissionNote    string
	Enabled           bool
	ConcurrencyLimit  int
	SessionTTLSeconds *int
}

// UpdateAccessInput is the partial update-access request. A nil pointer leaves
// the field unchanged, matching spec §7.4: Credential == nil keeps the stored
// credential, *Credential == "" clears it, any other value replaces it.
// ClearSessionTTL sets session_ttl_seconds to NULL and wins over
// SessionTTLSeconds; with both unset the stored TTL is kept. LoginFlowID binds
// a recipe, ClearLoginFlow unbinds it (and wins over LoginFlowID); with both
// unset the stored binding is kept.
type UpdateAccessInput struct {
	AuthType          *string
	UsernameOverride  *string
	LoginURL          *string
	PermissionNote    *string
	Credential        *string
	LoginFlowID       *int64
	ClearLoginFlow    bool
	Enabled           *bool
	ConcurrencyLimit  *int
	SessionTTLSeconds *int
	ClearSessionTTL   bool
}

// NewAccessService wires the account and access use cases to their
// repositories. The optional auth flow repository validates login_flow_id
// bindings (spec §5.9); when it is omitted a binding is rejected as
// auth_flow_invalid instead of being stored unverified.
func NewAccessService(companies domain.CompanyRepository, accounts domain.AccountRepository, accesses domain.AccessRepository, systems domain.SystemRepository, authFlows ...domain.AuthFlowRepository) *AccessService {
	service := &AccessService{
		companies: companies,
		accounts:  accounts,
		accesses:  accesses,
		systems:   systems,
	}
	for _, flows := range authFlows {
		if flows != nil {
			service.authFlows = flows
			break
		}
	}
	return service
}

// validateAccountKind enforces the account.kind CHECK constraint.
func validateAccountKind(kind string) error {
	if kind != domain.AccountKindHuman && kind != domain.AccountKindService {
		return domain.NewInvalid("kind", "invalid_kind", "kind must be human or service")
	}
	return nil
}

// validateAccountStatus enforces the account.status CHECK constraint, which
// shares the active/disabled enum of the other soft-deletable resources.
func validateAccountStatus(status string) error {
	if status != domain.StatusActive && status != domain.StatusDisabled {
		return domain.NewInvalid("status", "invalid_status", "status must be active or disabled")
	}
	return nil
}

// validateAuthType enforces the account_system_access.auth_type CHECK
// constraint.
func validateAuthType(authType string) error {
	switch authType {
	case domain.AuthTypePassword, domain.AuthTypeSSO, domain.AuthTypeToken,
		domain.AuthTypeAPIKey, domain.AuthTypeBasic, domain.AuthTypeManual:
		return nil
	default:
		return domain.NewInvalid("authType", "invalid_auth_type",
			"authType must be password, sso, token, apikey, basic or manual")
	}
}

// validateAccessConcurrency enforces concurrency_limit BETWEEN 1 AND 100.
func validateAccessConcurrency(limit int) error {
	if limit < minAccessConcurrencyLimit || limit > maxAccessConcurrencyLimit {
		return domain.NewInvalid("concurrencyLimit", "invalid_concurrency",
			"concurrencyLimit must be between 1 and 100")
	}
	return nil
}

// validateAccessSessionTTL enforces the nullable session_ttl_seconds CHECK. A
// nil TTL means "inherit the System default" and is always valid.
func validateAccessSessionTTL(ttl *int) error {
	if ttl == nil {
		return nil
	}
	if *ttl < minAccessSessionTTLSeconds || *ttl > maxAccessSessionTTLSeconds {
		return domain.NewInvalid("sessionTtlSeconds", "invalid_session_ttl",
			"sessionTtlSeconds must be between 60 and 604800")
	}
	return nil
}

// cloneAccessSessionTTL copies the optional TTL so a stored access never
// aliases the caller's request value.
func cloneAccessSessionTTL(ttl *int) *int {
	if ttl == nil {
		return nil
	}
	cloned := *ttl
	return &cloned
}

// cloneInt64 copies the optional login flow id so a stored access never aliases
// the caller's request value.
func cloneInt64(id *int64) *int64 {
	if id == nil {
		return nil
	}
	cloned := *id
	return &cloned
}

// invalidAccessLoginFlow returns the shared 422 auth_flow_invalid error for a
// login_flow_id that cannot be bound (spec §5.9).
func invalidAccessLoginFlow(message string) *domain.Error {
	return domain.NewInvalid("login_flow_id", "auth_flow_invalid", message)
}

// validateAccessLoginFlow enforces the auto-login eligibility of spec §5.9: the
// referenced recipe must exist, be enabled and belong to the access's system.
// A missing validator is a wiring error, so binding is rejected (fail closed)
// rather than stored unverified.
func (s *AccessService) validateAccessLoginFlow(ctx context.Context, flowID, systemID int64) error {
	if s.authFlows == nil {
		return invalidAccessLoginFlow("login_flow_id cannot be validated: auth flow repository is not configured")
	}
	flow, err := s.authFlows.GetAuthFlow(ctx, flowID)
	if err != nil {
		if isNotFound(err) {
			return invalidAccessLoginFlow("login_flow_id does not reference an existing login flow")
		}
		return err
	}
	if !flow.Enabled {
		return invalidAccessLoginFlow("login_flow_id must reference an enabled login flow")
	}
	if flow.SystemID != systemID {
		return invalidAccessLoginFlow("login_flow_id must reference a login flow of the same system")
	}
	return nil
}

// GetAccount returns one account by id.
func (s *AccessService) GetAccount(ctx context.Context, id int64) (*domain.Account, error) {
	return s.accounts.GetAccount(ctx, id)
}

// ListAccounts returns one page of accounts and whether more rows exist.
func (s *AccessService) ListAccounts(ctx context.Context, f domain.AccountFilter) ([]domain.Account, bool, error) {
	return s.accounts.ListAccounts(ctx, f)
}

// CreateAccount validates and stores an account under the seeded default
// company. New accounts always start active.
func (s *AccessService) CreateAccount(ctx context.Context, in CreateAccountInput) (*domain.Account, error) {
	if err := domain.ValidateResourceKey(in.Key); err != nil {
		return nil, err
	}
	if err := validateAccountKind(in.Kind); err != nil {
		return nil, err
	}

	company, err := s.companies.GetCompanyByKey(ctx, DefaultCompanyKey)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrDefaultCompanyMissing
		}
		return nil, err
	}

	account := &domain.Account{
		CompanyID: company.ID,
		Key:       in.Key,
		Label:     in.Label,
		Username:  in.Username,
		Kind:      in.Kind,
		Status:    domain.StatusActive,
		Notes:     in.Notes,
	}
	if err := s.accounts.CreateAccount(ctx, account); err != nil {
		return nil, err
	}
	return account, nil
}

// UpdateAccount applies the provided fields to an existing account. Key and
// company are immutable.
func (s *AccessService) UpdateAccount(ctx context.Context, id int64, in UpdateAccountInput) (*domain.Account, error) {
	account, err := s.accounts.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Label != nil {
		account.Label = *in.Label
	}
	if in.Username != nil {
		account.Username = *in.Username
	}
	if in.Kind != nil {
		if err := validateAccountKind(*in.Kind); err != nil {
			return nil, err
		}
		account.Kind = *in.Kind
	}
	if in.Notes != nil {
		account.Notes = *in.Notes
	}
	if in.Status != nil {
		if err := validateAccountStatus(*in.Status); err != nil {
			return nil, err
		}
		account.Status = *in.Status
	}
	if err := s.accounts.UpdateAccount(ctx, account); err != nil {
		return nil, err
	}
	return account, nil
}

// DeleteAccount deletes an account unless it still has accesses or sessions
// (spec §6.8).
func (s *AccessService) DeleteAccount(ctx context.Context, id int64) error {
	accesses, err := s.accounts.CountAccesses(ctx, id)
	if err != nil {
		return err
	}
	if accesses > 0 {
		return domain.NewDependency("has_dependencies", "account still has accesses")
	}
	sessions, err := s.accounts.CountSessionsByAccount(ctx, id)
	if err != nil {
		return err
	}
	if sessions > 0 {
		return domain.NewDependency("has_dependencies", "account still has sessions")
	}
	return s.accounts.DeleteAccount(ctx, id)
}

// GetAccess returns one access by id.
func (s *AccessService) GetAccess(ctx context.Context, id int64) (*domain.Access, error) {
	return s.accesses.GetAccess(ctx, id)
}

// ListAccesses returns every access of an account ordered by id.
func (s *AccessService) ListAccesses(ctx context.Context, accountID int64) ([]domain.Access, error) {
	return s.accesses.ListAccesses(ctx, accountID)
}

// CreateAccess validates and stores an access of an existing account to an
// existing system.
func (s *AccessService) CreateAccess(ctx context.Context, accountID int64, in CreateAccessInput) (*domain.Access, error) {
	if err := validateAuthType(in.AuthType); err != nil {
		return nil, err
	}
	if err := validateAccessConcurrency(in.ConcurrencyLimit); err != nil {
		return nil, err
	}
	if err := validateAccessSessionTTL(in.SessionTTLSeconds); err != nil {
		return nil, err
	}
	if _, err := s.accounts.GetAccount(ctx, accountID); err != nil {
		return nil, err
	}
	if _, err := s.systems.GetSystem(ctx, in.SystemID); err != nil {
		return nil, err
	}
	if in.LoginFlowID != nil {
		if err := s.validateAccessLoginFlow(ctx, *in.LoginFlowID, in.SystemID); err != nil {
			return nil, err
		}
	}

	access := &domain.Access{
		AccountID:         accountID,
		SystemID:          in.SystemID,
		AuthType:          in.AuthType,
		Credential:        in.Credential,
		UsernameOverride:  in.UsernameOverride,
		LoginURL:          in.LoginURL,
		LoginFlowID:       cloneInt64(in.LoginFlowID),
		Enabled:           in.Enabled,
		ConcurrencyLimit:  in.ConcurrencyLimit,
		SessionTTLSeconds: cloneAccessSessionTTL(in.SessionTTLSeconds),
		PermissionNote:    in.PermissionNote,
	}
	if err := s.accesses.CreateAccess(ctx, access); err != nil {
		return nil, err
	}
	return access, nil
}

// UpdateAccess applies a partial update to an existing access. The credential
// follows the write-only semantics documented on UpdateAccessInput; account and
// system are immutable.
func (s *AccessService) UpdateAccess(ctx context.Context, id int64, in UpdateAccessInput) (*domain.Access, error) {
	access, err := s.accesses.GetAccess(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.AuthType != nil {
		if err := validateAuthType(*in.AuthType); err != nil {
			return nil, err
		}
		access.AuthType = *in.AuthType
	}
	if in.Credential != nil {
		// nil keeps the stored credential, "" clears it and any other value
		// replaces it (spec §7.4).
		access.Credential = *in.Credential
	}
	if in.UsernameOverride != nil {
		access.UsernameOverride = *in.UsernameOverride
	}
	if in.LoginURL != nil {
		access.LoginURL = *in.LoginURL
	}
	if in.PermissionNote != nil {
		access.PermissionNote = *in.PermissionNote
	}
	if in.Enabled != nil {
		access.Enabled = *in.Enabled
	}
	if in.ConcurrencyLimit != nil {
		if err := validateAccessConcurrency(*in.ConcurrencyLimit); err != nil {
			return nil, err
		}
		access.ConcurrencyLimit = *in.ConcurrencyLimit
	}
	switch {
	case in.ClearLoginFlow:
		access.LoginFlowID = nil
	case in.LoginFlowID != nil:
		if err := s.validateAccessLoginFlow(ctx, *in.LoginFlowID, access.SystemID); err != nil {
			return nil, err
		}
		access.LoginFlowID = cloneInt64(in.LoginFlowID)
	}
	switch {
	case in.ClearSessionTTL:
		access.SessionTTLSeconds = nil
	case in.SessionTTLSeconds != nil:
		if err := validateAccessSessionTTL(in.SessionTTLSeconds); err != nil {
			return nil, err
		}
		access.SessionTTLSeconds = cloneAccessSessionTTL(in.SessionTTLSeconds)
	}
	if err := s.accesses.UpdateAccess(ctx, access); err != nil {
		return nil, err
	}
	return access, nil
}

// DeleteAccess deletes an access unless it still has sessions (spec §6.8).
func (s *AccessService) DeleteAccess(ctx context.Context, id int64) error {
	sessions, err := s.accesses.CountSessionsByAccess(ctx, id)
	if err != nil {
		return err
	}
	if sessions > 0 {
		return domain.NewDependency("has_dependencies", "access still has sessions")
	}
	return s.accesses.DeleteAccess(ctx, id)
}
