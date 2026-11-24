package versionapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// fakeConfirmationSettings is the settings-center stand-in for the
// confirmation-gate wiring tests. Values are stored in the persisted JSON
// encoding (the same shape settingsapp.Service reads), so Int/Raw decode
// exactly like the production adapter. The seeded values mirror the frozen
// catalog defaults (pinned by settings/application/catalog_test.go).
type fakeConfirmationSettings struct {
	mu     sync.Mutex
	values map[string]json.RawMessage
}

func newFakeConfirmationSettings() *fakeConfirmationSettings {
	return &fakeConfirmationSettings{values: map[string]json.RawMessage{
		versionapp.SettingPlaceholderTerms:    json.RawMessage(`["TBD","TODO","待定","待补充","xxx","占位","placeholder","暂无","略"]`),
		versionapp.SettingMinDescriptionChars: json.RawMessage(`20`),
	}}
}

// set changes one setting value, mirroring settingsapp.Service.Update's
// encode-then-store path so a later confirm reads the new value.
func (f *fakeConfirmationSettings) set(key string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = raw
}

func (f *fakeConfirmationSettings) raw(key string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.values[key]
	if !ok {
		return nil, errors.New("setting not found")
	}
	return value, nil
}

func (f *fakeConfirmationSettings) Raw(_ context.Context, key string) (json.RawMessage, error) {
	return f.raw(key)
}

func (f *fakeConfirmationSettings) Int(_ context.Context, key string) (int64, error) {
	raw, err := f.raw(key)
	if err != nil {
		return 0, err
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

// confirmationAPIField asserts the error is a 400 platform field error and
// returns its field path. It mirrors requirementAPIField
// (version/domain/requirement_test.go:50), which lives in the domain test
// package and cannot be imported here.
func confirmationAPIField(t *testing.T, err error) string {
	t.Helper()
	apiErr, ok := err.(*platform.Error)
	if !ok {
		t.Fatalf("error %v is not a platform error", err)
	}
	if apiErr.Status != 400 {
		t.Fatalf("status = %d, want 400", apiErr.Status)
	}
	field, _ := apiErr.Details["field"].(string)
	return field
}

// TestConfirmReadsSettings pins the T11 wiring: Confirm reads
// validation.min_description_chars at confirm time, and a settings change on
// the SAME service instance takes effect on the next confirmation (no startup
// caching).
func TestConfirmReadsSettings(t *testing.T) {
	env := newRequirementEnv(t)
	env.settings.set(versionapp.SettingMinDescriptionChars, 50)
	draft := validRequirementDraft(env)
	draft.SutDescription = "短" // passes ValidateDraft, below the configured floor
	created, err := env.svc.Create(requestContext(), env.versionID, draft, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = env.svc.Confirm(requestContext(), env.versionID, created.ID, 42)
	if err == nil {
		t.Fatal("short sut_description confirmed despite min_description_chars=50")
	}
	if field := confirmationAPIField(t, err); field != "sut_description" {
		t.Fatalf("field = %q, want sut_description", field)
	}

	// Lowering the floor must let the SAME revision confirm: the gate reads
	// the setting per call, never a value cached at construction.
	env.settings.set(versionapp.SettingMinDescriptionChars, 1)
	confirmed, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 42)
	if err != nil {
		t.Fatalf("setting change must take effect: %v", err)
	}
	if !confirmed.Confirmed {
		t.Fatalf("confirmed = false, want true after lowering the floor")
	}
}

// TestConfirmReadsPlaceholderTermsSetting routes a placeholder through an
// acceptance-criterion part (given="TBD") and asserts the 400 status and the
// acceptance_criteria[i].given field path end to end through the settings
// wiring, then proves the blacklist itself is settings-driven (removing TBD
// lets the same revision confirm).
func TestConfirmReadsPlaceholderTermsSetting(t *testing.T) {
	env := newRequirementEnv(t)
	draft := validRequirementDraft(env)
	draft.AcceptanceCriteria[0].Given = "TBD"
	created, err := env.svc.Create(requestContext(), env.versionID, draft, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = env.svc.Confirm(requestContext(), env.versionID, created.ID, 42)
	if err == nil {
		t.Fatal("placeholder acceptance-criterion given confirmed")
	}
	if field := confirmationAPIField(t, err); field != "acceptance_criteria[0].given" {
		t.Fatalf("field = %q, want acceptance_criteria[0].given", field)
	}

	env.settings.set(versionapp.SettingPlaceholderTerms, []string{"later"})
	if _, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 42); err != nil {
		t.Fatalf("terms change must take effect (TBD no longer blacklisted): %v", err)
	}
}

// TestBatchConfirmReadsSettings pins the same wiring on the batch call site:
// one settings snapshot validates the whole batch, and a later batch reads the
// changed value.
func TestBatchConfirmReadsSettings(t *testing.T) {
	env := newRequirementEnv(t)
	env.settings.set(versionapp.SettingMinDescriptionChars, 50)
	createShort := func(title string) versionapp.RequirementDetail {
		t.Helper()
		draft := validRequirementDraft(env)
		draft.Title = title
		draft.SutDescription = "短"
		created, err := env.svc.Create(requestContext(), env.versionID, draft, 42)
		if err != nil {
			t.Fatalf("Create(%q): %v", title, err)
		}
		return created
	}
	first := createShort("checkout applies coupons")
	second := createShort("checkout rejects expired coupons")

	_, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{first.ID, second.ID}, 42)
	if err == nil {
		t.Fatal("batch confirmed short sut_description despite min_description_chars=50")
	}
	if field := confirmationAPIField(t, err); field != "sut_description" {
		t.Fatalf("field = %q, want sut_description", field)
	}
	if apiErr := platform.AsError(err); apiErr.Details["requirement_id"] == nil {
		t.Fatalf("batch field error must name the failing requirement: %#v", apiErr.Details)
	}

	env.settings.set(versionapp.SettingMinDescriptionChars, 1)
	result, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{first.ID, second.ID}, 42)
	if err != nil {
		t.Fatalf("setting change must take effect on the batch path: %v", err)
	}
	if len(result.Confirmed) != 2 || len(result.Skipped) != 0 {
		t.Fatalf("confirmed/skipped = %v/%v, want both confirmed", result.Confirmed, result.Skipped)
	}
}

// TestConfirmUnwiredSettingsFailsClosed pins the no-silent-degradation rule
// (constitution VII.6): without a settings reader the confirmation gate
// refuses explicitly instead of silently confirming without the increment.
func TestConfirmUnwiredSettingsFailsClosed(t *testing.T) {
	env := newRequirementEnv(t)
	env.svc.SetConfirmationSettings(nil)
	created := createRequirement(t, env, "checkout applies coupons")

	_, err := env.svc.Confirm(requestContext(), env.versionID, created.ID, 42)
	if err == nil {
		t.Fatal("unwired settings reader silently confirmed without the gate")
	}
	if apiErr := platform.AsError(err); apiErr.Status != 503 {
		t.Fatalf("status = %d, want 503 (fail closed)", apiErr.Status)
	}
	if _, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{created.ID}, 42); err == nil {
		t.Fatal("unwired settings reader silently batch-confirmed without the gate")
	}
}

// TestBatchConfirmAllClarifiedNeedsNoSettings pins H-F10b #23: the batch path is
// now symmetric with Confirm. A batch whose every requirement is already
// clarified is a no-op replay (all skipped) and must not depend on the settings
// reader at all — the single path returns its idempotent replay before reading
// settings, and the batch used to 503 on a batch that had nothing to validate.
// The discriminating control is the mixed batch: one draft still forces the read
// and still fails closed.
func TestBatchConfirmAllClarifiedNeedsNoSettings(t *testing.T) {
	env := newRequirementEnv(t)
	first := createRequirement(t, env, "checkout applies coupons")
	second := createRequirement(t, env, "checkout rejects expired coupons")
	if _, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{first.ID, second.ID}, 42); err != nil {
		t.Fatalf("initial batch confirm: %v", err)
	}

	env.svc.SetConfirmationSettings(nil)
	result, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{first.ID, second.ID}, 42)
	if err != nil {
		t.Fatalf("an all-clarified batch must not read settings: %v", err)
	}
	if len(result.Confirmed) != 0 || len(result.Skipped) != 2 {
		t.Fatalf("confirmed/skipped = %v/%v, want both skipped", result.Confirmed, result.Skipped)
	}

	// Control: a batch that still contains a draft MUST fail closed.
	third := createRequirement(t, env, "checkout refunds a cancelled order")
	if _, err := env.svc.BatchConfirm(requestContext(), env.versionID, []int64{first.ID, third.ID}, 42); err == nil {
		t.Fatal("a batch with a draft silently confirmed without the settings gate")
	}
}
