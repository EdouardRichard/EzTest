package version_test

import (
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// v0.2.4-B: the derived binding chain (version_sut, version_sut_resource,
// version_repository_override, scope_exclude) is gone. A version binds an
// environment directly on its own row and persists only the account subset, so
// the domain invariants left to pin are the mandatory environment binding, the
// account selection rules and the binding stage gate.

func TestVersionValidateRequiresAnEnvironmentBinding(t *testing.T) {
	valid := version.Version{
		Title: "Q4 orders", Status: version.StatusInitial, EnvironmentID: 1101,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	unbound := valid
	unbound.EnvironmentID = 0
	err := unbound.Validate()
	if err == nil {
		t.Fatal("Validate accepted a version without an environment binding")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("err = %v, want a 400 platform error", err)
	}
	if field, _ := apiErr.Details["field"].(string); field != "environment_id" {
		t.Fatalf("field = %q, want environment_id", field)
	}

	negative := valid
	negative.EnvironmentID = -1
	if err := negative.Validate(); err == nil {
		t.Fatal("Validate accepted a negative environment_id")
	}

	blank := valid
	blank.Title = "   "
	if err := blank.Validate(); err == nil {
		t.Fatal("Validate accepted a blank title")
	}
}

func TestValidateAccountsAcceptsAUniquePositiveSelection(t *testing.T) {
	if err := version.ValidateAccounts(nil); err != nil {
		t.Fatalf("an empty selection must be valid: %v", err)
	}
	if err := version.ValidateAccounts([]version.AccountBinding{{AccountID: 7}, {AccountID: 9}}); err != nil {
		t.Fatalf("ValidateAccounts = %v, want nil", err)
	}
}

func TestValidateAccountsRejectsInvalidSelections(t *testing.T) {
	cases := map[string][]version.AccountBinding{
		"zero":      {{AccountID: 0}},
		"negative":  {{AccountID: -1}},
		"duplicate": {{AccountID: 7}, {AccountID: 7}},
	}
	for name, accounts := range cases {
		t.Run(name, func(t *testing.T) {
			err := version.ValidateAccounts(accounts)
			if err == nil {
				t.Fatal("ValidateAccounts accepted an invalid selection")
			}
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) || apiErr.Status != 400 {
				t.Fatalf("err = %v, want a 400 platform error", err)
			}
			if field, _ := apiErr.Details["field"].(string); field != "accounts" {
				t.Fatalf("field = %q, want accounts", field)
			}
		})
	}
}

func TestBindingEditableStageGate(t *testing.T) {
	editable := []version.Stage{
		version.StageCreated,
		version.StageClarifying,
		version.StageRequirementReview,
		version.StageDesigning,
		version.StageCaseReview,
	}
	for _, stage := range editable {
		if !version.BindingEditable(stage) {
			t.Fatalf("stage %q must allow binding edits", stage)
		}
	}
	readOnly := []version.Stage{
		version.StageExecuting,
		version.StageReporting,
		version.StageCompleted,
	}
	for _, stage := range readOnly {
		if version.BindingEditable(stage) {
			t.Fatalf("stage %q must be read-only", stage)
		}
	}
}
