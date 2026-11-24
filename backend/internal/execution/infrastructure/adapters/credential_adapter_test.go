package adapters

import (
	"context"
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type credentialFakeVersions struct {
	view versionapp.VersionView
}

func (f credentialFakeVersions) Get(context.Context, int64) (versionapp.VersionView, error) {
	return f.view, nil
}

type credentialFakeSecrets struct {
	ids []int64
}

func (f *credentialFakeSecrets) GetAccountSecret(_ context.Context, accountID int64) (string, string, error) {
	f.ids = append(f.ids, accountID)
	return "user", "pass", nil
}

func TestCredentialAdapterAccountRoleDeterministic(t *testing.T) {
	// v0.2.4-B: accounts are a version-level subset and the SUT argument only
	// gates that the SUT belongs to the bound environment.
	view := versionapp.VersionView{
		ID: 10, EnvironmentID: 3,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 10, EnvironmentID: 3,
			Environment: resourceapp.EnvironmentView{
				ID: "3", Name: "env",
				SUTs: []resourceapp.SUTView{{ID: "7", EnvironmentID: "3", Name: "sut"}},
			},
			Accounts: []resourceapp.AccountView{
				{ID: "82", Roles: []string{"author"}},
				{ID: "81", Roles: []string{"author"}},
			},
		},
	}
	secrets := &credentialFakeSecrets{}
	adapter := NewCredentialAdapter(credentialFakeVersions{view: view}, secrets, nil)

	credential, err := adapter.Account(context.Background(), 10, 7, "author")
	if err != nil {
		t.Fatalf("Account: %v (two accounts sharing a role must resolve deterministically)", err)
	}
	if credential.AccountID != 81 {
		t.Fatalf("account ID = %d, want the lowest bound account 81", credential.AccountID)
	}
	if credential.Username != "user" || credential.Password != "pass" {
		t.Fatalf("credential = %+v, want the bound secret", credential)
	}
	if len(secrets.ids) != 1 || secrets.ids[0] != 81 {
		t.Fatalf("secret lookups = %v, want exactly [81]", secrets.ids)
	}
}

func TestCredentialAdapterAccountRoleMissing(t *testing.T) {
	view := versionapp.VersionView{
		ID: 10, EnvironmentID: 3,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 10, EnvironmentID: 3,
			Environment: resourceapp.EnvironmentView{
				ID: "3", Name: "env",
				SUTs: []resourceapp.SUTView{{ID: "7", EnvironmentID: "3", Name: "sut"}},
			},
			Accounts: []resourceapp.AccountView{{ID: "81", Roles: []string{"author"}}},
		},
	}
	adapter := NewCredentialAdapter(credentialFakeVersions{view: view}, &credentialFakeSecrets{}, nil)

	_, err := adapter.Account(context.Background(), 10, 7, "reviewer")
	if err == nil {
		t.Fatal("Account with an unbound role must fail, not silently borrow another account")
	}
}

func TestCredentialAffinityPrefersSUTMatch(t *testing.T) {
	// v0.2.4-F (SC-F6): an account affined to the resolved SUT wins over a
	// shared account even when the shared account has the lower ID. This is
	// the case that proves affinity beats the old min-ID-only selection, not
	// just a tie-break within an already-filtered pool.
	view := versionapp.VersionView{
		ID: 10, EnvironmentID: 3,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 10, EnvironmentID: 3,
			Environment: resourceapp.EnvironmentView{
				ID: "3", Name: "env",
				SUTs: []resourceapp.SUTView{{ID: "7", EnvironmentID: "3", Name: "sut"}},
			},
			Accounts: []resourceapp.AccountView{
				{ID: "81", Roles: []string{"author"}},             // shared, lower ID
				{ID: "82", SUTID: "7", Roles: []string{"author"}}, // affined to SUT 7, higher ID
			},
		},
	}
	secrets := &credentialFakeSecrets{}
	adapter := NewCredentialAdapter(credentialFakeVersions{view: view}, secrets, nil)

	credential, err := adapter.Account(context.Background(), 10, 7, "author")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if credential.AccountID != 82 {
		t.Fatalf("account ID = %d, want the SUT-affined account 82 even though shared account 81 has the lower ID", credential.AccountID)
	}
	if len(secrets.ids) != 1 || secrets.ids[0] != 82 {
		t.Fatalf("secret lookups = %v, want exactly [82]", secrets.ids)
	}
}

func TestCredentialSharedFallback(t *testing.T) {
	// v0.2.4-F (SC-F6): with no account affined to SUT 7, resolution falls back
	// to the shared (SUTID-empty) account. An account affined to a DIFFERENT
	// SUT is never a candidate for this one, even when it has the lower ID;
	// an account matching the SUT but not the role is excluded by the role
	// filter first.
	view := versionapp.VersionView{
		ID: 10, EnvironmentID: 3,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 10, EnvironmentID: 3,
			Environment: resourceapp.EnvironmentView{
				ID: "3", Name: "env",
				SUTs: []resourceapp.SUTView{{ID: "7", EnvironmentID: "3", Name: "sut"}},
			},
			Accounts: []resourceapp.AccountView{
				{ID: "80", SUTID: "7", Roles: []string{"reviewer"}}, // affinity match, wrong role
				{ID: "81", SUTID: "99", Roles: []string{"author"}},  // correct role, affined to another SUT, lower ID
				{ID: "82", Roles: []string{"author"}},               // shared, correct role, higher ID
			},
		},
	}
	secrets := &credentialFakeSecrets{}
	adapter := NewCredentialAdapter(credentialFakeVersions{view: view}, secrets, nil)

	credential, err := adapter.Account(context.Background(), 10, 7, "author")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if credential.AccountID != 82 {
		t.Fatalf("account ID = %d, want the shared account 82 (no affinity match for SUT 7; account 81 belongs to SUT 99)", credential.AccountID)
	}
	if len(secrets.ids) != 1 || secrets.ids[0] != 82 {
		t.Fatalf("secret lookups = %v, want exactly [82]", secrets.ids)
	}
}

func TestCredentialAffinityTieBreakMinID(t *testing.T) {
	// v0.2.4-F (SC-F6): the deterministic min-ID tie-break applies within the
	// affinity tier too, not just the shared tier — a lower-ID shared account
	// must not win over the affinity tier, and within that tier the lowest ID
	// wins.
	view := versionapp.VersionView{
		ID: 10, EnvironmentID: 3,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 10, EnvironmentID: 3,
			Environment: resourceapp.EnvironmentView{
				ID: "3", Name: "env",
				SUTs: []resourceapp.SUTView{{ID: "7", EnvironmentID: "3", Name: "sut"}},
			},
			Accounts: []resourceapp.AccountView{
				{ID: "81", Roles: []string{"author"}},             // shared, lowest overall ID
				{ID: "83", SUTID: "7", Roles: []string{"author"}}, // affined, higher ID
				{ID: "82", SUTID: "7", Roles: []string{"author"}}, // affined, lower ID within tier
			},
		},
	}
	secrets := &credentialFakeSecrets{}
	adapter := NewCredentialAdapter(credentialFakeVersions{view: view}, secrets, nil)

	credential, err := adapter.Account(context.Background(), 10, 7, "author")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if credential.AccountID != 82 {
		t.Fatalf("account ID = %d, want the lowest-ID account within the affinity tier (82), not shared 81 or affined 83", credential.AccountID)
	}
	if len(secrets.ids) != 1 || secrets.ids[0] != 82 {
		t.Fatalf("secret lookups = %v, want exactly [82]", secrets.ids)
	}
}
