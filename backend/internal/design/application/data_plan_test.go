package designapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestSubmitDataPlanCreatesDraftRevision(t *testing.T) {
	service, repo, publisher, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusDraft)
	base := repo.cases[7001].current.ID

	preconditions := "已准备测试账号"
	result, err := service.SubmitDataPlan(context.Background(), 1001, base, 77, DataPlanInput{
		Preconditions: &preconditions,
		TestData:      json.RawMessage(`{"username":"admin","password":"secret"}`),
	})
	if err != nil {
		t.Fatalf("SubmitDataPlan: %v", err)
	}
	if result.CaseID != 7001 || result.Revision != 2 || result.CaseVersionID == base {
		t.Fatalf("result = %+v", result)
	}
	current := repo.cases[7001].current
	if current.ID != result.CaseVersionID || current.Revision != 2 {
		t.Fatalf("current = %+v", current)
	}
	if current.Preconditions != preconditions {
		t.Fatalf("preconditions = %q", current.Preconditions)
	}
	if len(current.Steps) != 1 {
		t.Fatalf("steps copied = %d, want 1", len(current.Steps))
	}
	if string(current.TestData) != `{"password":"secret","username":"admin"}` &&
		string(current.TestData) != `{"username":"admin","password":"secret"}` {
		t.Fatalf("test data = %s", current.TestData)
	}
	graphDelta := publisher.byType("design.graph_delta")
	if len(graphDelta) != 1 || graphDelta[0].Payload["op"] != "data_plan" {
		t.Fatalf("graph delta = %+v", graphDelta)
	}
	if len(publisher.byType("design.case_version_created")) != 1 {
		t.Fatalf("events = %v", publisher.types())
	}
}

func TestSubmitDataPlanRejectsConfirmed(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusConfirmed)
	base := repo.cases[7001].current.ID

	_, err := service.SubmitDataPlan(context.Background(), 1001, base, 77, DataPlanInput{})
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("err = %v, want state_conflict", err)
	}
}

func TestSubmitDataPlanRejectsNonCurrentVersion(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusDraft)
	_, err := service.SubmitDataPlan(context.Background(), 1001, 999999, 77, DataPlanInput{})
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestSubmitDataPlanValidatesPatch(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusDraft)
	base := repo.cases[7001].current.ID
	tooLong := string(make([]byte, 20001))
	for _, input := range []DataPlanInput{
		{Preconditions: &tooLong},
		{TestData: json.RawMessage(`[]`)},
	} {
		_, err := service.SubmitDataPlan(context.Background(), 1001, base, 77, input)
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
			t.Fatalf("input err = %v, want invalid_argument", err)
		}
	}
}
