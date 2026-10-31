package designapp

import (
	"context"
	"errors"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

type fakeCaseConfirmedReopener struct {
	calls []struct {
		versionID int64
		trigger   string
		reason    string
	}
	err error
}

func (f *fakeCaseConfirmedReopener) ReopenIfCompleted(_ context.Context, versionID int64, trigger, reason string) error {
	f.calls = append(f.calls, struct {
		versionID int64
		trigger   string
		reason    string
	}{versionID: versionID, trigger: trigger, reason: reason})
	return f.err
}

func TestConfirmReopensCompletedVersion(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	reopener := &fakeCaseConfirmedReopener{}
	service.SetVersionReopener(reopener)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusDraft)

	if _, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(reopener.calls) != 1 {
		t.Fatalf("reopen calls = %+v, want one", reopener.calls)
	}
	if reopener.calls[0].versionID != 1001 || reopener.calls[0].trigger != "case_confirmed" {
		t.Fatalf("reopen call = %+v, want version 1001 trigger case_confirmed", reopener.calls[0])
	}
}

func TestConfirmReopenProjectionIsBestEffort(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	service.SetVersionReopener(&fakeCaseConfirmedReopener{err: errors.New("reopen unavailable")})
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusDraft)

	result, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	if err != nil {
		t.Fatalf("Confirm must not fail on a reopen projection error: %v", err)
	}
	if len(result.Confirmed) != 1 {
		t.Fatalf("confirmed = %+v", result.Confirmed)
	}
}

func TestConfirmWithoutReopenerDoesNotPanic(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusDraft)
	if _, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
}

func TestIdempotentConfirmDoesNotReopenAgain(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	reopener := &fakeCaseConfirmedReopener{}
	service.SetVersionReopener(reopener)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusDraft)
	input := ConfirmInput{Scope: design.ConfirmScopeCase, TargetID: 7001}
	if _, err := service.Confirm(context.Background(), 1001, input, 42); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	reopener.calls = nil
	if _, err := service.Confirm(context.Background(), 1001, input, 42); err != nil {
		t.Fatalf("second Confirm: %v", err)
	}
	if len(reopener.calls) != 0 {
		t.Fatalf("idempotent confirmation reopened again: %+v", reopener.calls)
	}
}
