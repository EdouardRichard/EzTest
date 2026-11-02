package designapp

import (
	"context"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

func newStaleFixture(t *testing.T) (*StaleService, *fakeRepo, *fakePublisher) {
	t.Helper()
	repo := newFakeRepo()
	repo.scenarios = []design.Scenario{{
		ID: 5001, VersionID: 1001, RequirementID: 3001, RequirementVersionID: 3002, Name: "正常登录",
	}}
	publisher := &fakePublisher{}
	service := NewStaleService(repo, newFixedClock(), nil, publisher)
	return service, repo, publisher
}

func TestMarkRequirementChangedMarksOnlyConfirmedCurrent(t *testing.T) {
	service, repo, publisher := newStaleFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "已确认用例", design.CaseStatusConfirmed)
	seedFakeCase(t, repo, 7002, 5001, "草稿用例", design.CaseStatusDraft)
	seedFakeCase(t, repo, 7003, 5001, "旧用例", design.CaseStatusStale)

	affected, err := service.MarkRequirementChanged(context.Background(), 1001, 3001, "requirement_changed", 2)
	if err != nil {
		t.Fatalf("MarkRequirementChanged: %v", err)
	}
	if len(affected) != 1 {
		t.Fatalf("affected = %v, want only the confirmed current version", affected)
	}
	if repo.cases[7001].current.Status != design.CaseStatusStale {
		t.Fatalf("confirmed case status = %q", repo.cases[7001].current.Status)
	}
	if repo.cases[7001].entity.Status != design.CaseStatusStale {
		t.Fatalf("confirmed entity status = %q", repo.cases[7001].entity.Status)
	}
	if repo.cases[7002].current.Status != design.CaseStatusDraft {
		t.Fatalf("draft case changed: %q", repo.cases[7002].current.Status)
	}
	if repo.cases[7003].current.Status != design.CaseStatusStale {
		t.Fatalf("stale case changed: %q", repo.cases[7003].current.Status)
	}
	staleEvents := publisher.byType("design.case_version_stale")
	if len(staleEvents) != 1 {
		t.Fatalf("stale events = %v", publisher.types())
	}
	if staleEvents[0].Payload["reason"] != "requirement_changed" {
		t.Fatalf("event payload = %v", staleEvents[0].Payload)
	}
}

func TestStaleHookIdempotent(t *testing.T) {
	service, repo, publisher := newStaleFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "已确认用例", design.CaseStatusConfirmed)

	first, err := service.MarkRequirementChanged(context.Background(), 1001, 3001, "requirement_changed", 2)
	if err != nil {
		t.Fatalf("first MarkRequirementChanged: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first affected = %v", first)
	}
	eventCount := len(publisher.events)
	second, err := service.MarkRequirementChanged(context.Background(), 1001, 3001, "requirement_changed", 2)
	if err != nil {
		t.Fatalf("second MarkRequirementChanged: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second affected = %v, want empty", second)
	}
	if len(publisher.events) != eventCount {
		t.Fatalf("idempotent stale mark published %v", publisher.types()[eventCount:])
	}
}

func TestMarkRequirementChangedMissingRequirement(t *testing.T) {
	service, _, _ := newStaleFixture(t)
	if _, err := service.MarkRequirementChanged(context.Background(), 1001, 9999, "requirement_changed", 2); err == nil {
		t.Fatal("expected not_found for missing requirement")
	}
}
