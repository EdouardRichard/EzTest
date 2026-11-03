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
	view := versionapp.VersionView{
		ID: 10,
		Bindings: []versionapp.BindingView{{
			ID: 7,
			Accounts: []resourceapp.AccountView{
				{ID: "82", Roles: []string{"author"}},
				{ID: "81", Roles: []string{"author"}},
			},
		}},
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
		ID: 10,
		Bindings: []versionapp.BindingView{{
			ID:       7,
			Accounts: []resourceapp.AccountView{{ID: "81", Roles: []string{"author"}}},
		}},
	}
	adapter := NewCredentialAdapter(credentialFakeVersions{view: view}, &credentialFakeSecrets{}, nil)

	_, err := adapter.Account(context.Background(), 10, 7, "reviewer")
	if err == nil {
		t.Fatal("Account with an unbound role must fail, not silently borrow another account")
	}
}
