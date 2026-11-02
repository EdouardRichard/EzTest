package adapters

import (
	"context"
	"strconv"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// AccountSecretReader is the resource application secret surface for accounts.
type AccountSecretReader interface {
	GetAccountSecret(ctx context.Context, accountID int64) (username, password string, err error)
}

// DatabaseSecretReader is the resource application secret surface for databases.
type DatabaseSecretReader interface {
	GetDatabaseSecret(ctx context.Context, databaseID int64) (dsn, username, password string, err error)
}

// VersionBindingService is the version read surface used to resolve a version
// SUT binding (*versionapp.Service satisfies it).
type VersionBindingService interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// CredentialAdapter resolves version-SUT-bound credentials through the version
// binding + resource secret readers. Secrets stay in memory only.
type CredentialAdapter struct {
	versions  VersionBindingService
	accounts  AccountSecretReader
	databases DatabaseSecretReader
}

// NewCredentialAdapter wires the credential adapter.
func NewCredentialAdapter(versions VersionBindingService, accounts AccountSecretReader, databases DatabaseSecretReader) *CredentialAdapter {
	return &CredentialAdapter{versions: versions, accounts: accounts, databases: databases}
}

var _ executionapp.CredentialReader = (*CredentialAdapter)(nil)

// Account resolves a test account by exact role match within the version SUT.
// An empty role selects the first bound account; multiple matches are a
// configuration conflict.
func (a *CredentialAdapter) Account(ctx context.Context, versionID, versionSUTID int64, role string) (executionapp.AccountCredential, error) {
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return executionapp.AccountCredential{}, err
	}
	var binding *versionapp.BindingView
	for i := range view.Bindings {
		if view.Bindings[i].ID == versionSUTID {
			binding = &view.Bindings[i]
			break
		}
	}
	if binding == nil {
		return executionapp.AccountCredential{}, platform.NotFound("version_sut")
	}
	if len(binding.Accounts) == 0 {
		return executionapp.AccountCredential{}, platform.NotFound("test_account")
	}
	role = strings.TrimSpace(role)
	matches := make([]string, 0, 1)
	for _, account := range binding.Accounts {
		if role == "" {
			matches = append(matches, account.ID)
			break
		}
		for _, accountRole := range account.Roles {
			if strings.TrimSpace(accountRole) == role {
				matches = append(matches, account.ID)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return executionapp.AccountCredential{}, platform.NotFound("test_account")
	case 1:
	default:
		return executionapp.AccountCredential{}, platform.StateConflict("multiple test accounts share the requested role")
	}
	accountID, err := strconv.ParseInt(strings.TrimSpace(matches[0]), 10, 64)
	if err != nil || accountID <= 0 {
		return executionapp.AccountCredential{}, platform.NewError(500, "internal", "test account has an invalid ID")
	}
	if a.accounts == nil {
		return executionapp.AccountCredential{}, platform.NewError(500, "internal", "account secret reader is not configured")
	}
	username, password, err := a.accounts.GetAccountSecret(ctx, accountID)
	if err != nil {
		return executionapp.AccountCredential{}, err
	}
	return executionapp.AccountCredential{AccountID: accountID, Username: username, Password: password}, nil
}

// Database resolves the version-SUT target database credential. An explicit
// database_id must belong to the binding; otherwise the first bound database is
// used.
func (a *CredentialAdapter) Database(ctx context.Context, versionID, versionSUTID int64, databaseID *int64) (executionapp.DatabaseCredential, error) {
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return executionapp.DatabaseCredential{}, err
	}
	var binding *versionapp.BindingView
	for i := range view.Bindings {
		if view.Bindings[i].ID == versionSUTID {
			binding = &view.Bindings[i]
			break
		}
	}
	if binding == nil {
		return executionapp.DatabaseCredential{}, platform.NotFound("version_sut")
	}
	if len(binding.Databases) == 0 {
		return executionapp.DatabaseCredential{}, platform.NotFound("target_database")
	}
	selected := ""
	if databaseID != nil {
		wanted := strconv.FormatInt(*databaseID, 10)
		for _, database := range binding.Databases {
			if database.ID == wanted {
				selected = database.ID
				break
			}
		}
		if selected == "" {
			return executionapp.DatabaseCredential{}, platform.NotFound("target_database")
		}
	} else {
		for _, database := range binding.Databases {
			if database.Status == "" || database.Status == "active" {
				selected = database.ID
				break
			}
		}
		if selected == "" {
			selected = binding.Databases[0].ID
		}
	}
	databaseIDValue, err := strconv.ParseInt(strings.TrimSpace(selected), 10, 64)
	if err != nil || databaseIDValue <= 0 {
		return executionapp.DatabaseCredential{}, platform.NewError(500, "internal", "target database has an invalid ID")
	}
	if a.databases == nil {
		return executionapp.DatabaseCredential{}, platform.NewError(500, "internal", "database secret reader is not configured")
	}
	dsn, username, password, err := a.databases.GetDatabaseSecret(ctx, databaseIDValue)
	if err != nil {
		return executionapp.DatabaseCredential{}, err
	}
	readOnly := true
	for _, database := range binding.Databases {
		if database.ID == selected {
			readOnly = database.ReadOnly
			break
		}
	}
	return executionapp.DatabaseCredential{
		DatabaseID: databaseIDValue,
		DSN:        dsn,
		Username:   username,
		Password:   password,
		ReadOnly:   readOnly,
	}, nil
}
