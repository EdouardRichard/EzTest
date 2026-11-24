package adapters

import (
	"context"
	"sort"
	"strconv"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
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

// Account resolves a test account by exact role match for one SUT of the
// version's bound environment, preferring an account affined to that SUT
// (AccountView.SUTID == sutID) and falling back to a shared account (SUTID
// empty) when none is affined. An empty role selects the first bound account.
// Several accounts sharing a role within the same tier resolve
// deterministically to the lowest account ID.
//
// v0.2.4-F (SC-F6): the per-version binding row is gone, so sutID identifies a
// SUT of the bound environment and the candidate accounts are the
// version-level subset, filtered by SUT affinity with a shared-account
// fallback.
func (a *CredentialAdapter) Account(ctx context.Context, versionID, sutID int64, role string) (executionapp.AccountCredential, error) {
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return executionapp.AccountCredential{}, err
	}
	if !boundSUT(view.Binding, sutID) {
		return executionapp.AccountCredential{}, platform.NotFound("sut")
	}
	accounts := view.Binding.Accounts
	if len(accounts) == 0 {
		return executionapp.AccountCredential{}, platform.NotFound("test_account")
	}
	role = strings.TrimSpace(role)
	matches := make([]string, 0, 1)
	if role == "" {
		matches = append(matches, accounts[0].ID)
	} else {
		// v0.2.4-F (SC-F6): among role-matching candidates, prefer an account
		// affined to this SUT; fall back to shared (no-affinity) accounts only
		// when none is affined. An account affined to a different SUT is not a
		// candidate for this one, even as a fallback.
		wantedSUTID := strconv.FormatInt(sutID, 10)
		affinity := make([]string, 0, 1)
		shared := make([]string, 0, 1)
		for _, account := range accounts {
			matched := false
			for _, accountRole := range account.Roles {
				if strings.TrimSpace(accountRole) == role {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			switch account.SUTID {
			case wantedSUTID:
				affinity = append(affinity, account.ID)
			case "":
				shared = append(shared, account.ID)
			}
		}
		if len(affinity) > 0 {
			matches = affinity
		} else {
			matches = shared
		}
	}
	if len(matches) == 0 {
		return executionapp.AccountCredential{}, platform.NotFound("test_account")
	}
	// Deterministic account selection (Task 90): several SUT test accounts may
	// share a role (e.g. author) within the same affinity tier. Selecting the
	// lowest account ID keeps repeated case runs on the same account instead of
	// failing mid-execution with a state_conflict; all candidates are bound to
	// the version, so no auth boundary is bypassed.
	sort.SliceStable(matches, func(i, j int) bool {
		left, leftErr := strconv.ParseInt(strings.TrimSpace(matches[i]), 10, 64)
		right, rightErr := strconv.ParseInt(strings.TrimSpace(matches[j]), 10, 64)
		if leftErr != nil || rightErr != nil {
			return matches[i] < matches[j]
		}
		return left < right
	})
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

// Database resolves the target database credential of one SUT of the version's
// bound environment. An explicit database_id must belong to that SUT **and be
// active**: a database an operator disabled is outside the version's effective
// scope, so it is a 404 and its secret is never read. Without an explicit id the
// SUT's first active database is used, and a SUT with no active database has no
// credential at all.
func (a *CredentialAdapter) Database(ctx context.Context, versionID, sutID int64, databaseID *int64) (executionapp.DatabaseCredential, error) {
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return executionapp.DatabaseCredential{}, err
	}
	databases := boundSUTDatabases(view.Binding, sutID)
	if databases == nil {
		return executionapp.DatabaseCredential{}, platform.NotFound("sut")
	}
	if len(databases) == 0 {
		return executionapp.DatabaseCredential{}, platform.NotFound("target_database")
	}
	selected := ""
	if databaseID != nil {
		wanted := strconv.FormatInt(*databaseID, 10)
		for _, database := range databases {
			if database.ID == wanted {
				selected = database.ID
				break
			}
		}
		if selected == "" {
			// Not inside the effective scope: the id belongs to another SUT, or an
			// operator disabled that database. Neither may yield a credential.
			return executionapp.DatabaseCredential{}, platform.NotFound("target_database")
		}
	} else {
		// boundSUTDatabases already returns active rows only, so the first one is
		// the default. The status predicate that used to live here was a second
		// copy of the effective-scope rule and is gone with it.
		selected = databases[0].ID
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
	for _, database := range databases {
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

// boundSUT reports whether the SUT belongs to the environment the version binds.
// Membership is an identity question, not a scope question, so this deliberately
// reads the raw catalog list: a SUT whose pages are all still proposed is bound
// all the same, and its version-level accounts still resolve.
func boundSUT(catalog versionapp.EnvironmentSUTCatalog, sutID int64) bool {
	wanted := strconv.FormatInt(sutID, 10)
	for _, sut := range catalog.Environment.SUTs {
		if sut.ID == wanted {
			return true
		}
	}
	return false
}

// boundSUTDatabases returns the ACTIVE target databases of one SUT of the bound
// environment — the effective scope, never the raw catalog list. A nil result
// means the SUT is not part of the environment; an empty result means the SUT is
// bound but owns no database the version may use.
//
// This is a trust boundary: the rows returned here are the only ones Database()
// will resolve a credential for, so a database an operator disabled must not
// appear on either the explicit database_id path or the default fallback.
func boundSUTDatabases(catalog versionapp.EnvironmentSUTCatalog, sutID int64) []resourceapp.DatabaseView {
	for _, sut := range catalog.EffectiveSUTs() {
		if sut.SUTID != sutID {
			continue
		}
		return sut.Databases
	}
	return nil
}
