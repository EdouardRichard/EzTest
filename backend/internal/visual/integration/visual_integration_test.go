//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
	visualpg "github.com/EdouardRichard/EzTest/backend/internal/visual/infrastructure/postgres"
)

const testFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestVisualRepositoriesPartialUniqueAndOverview(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := visualpg.NewRepository(pool)
	ctx := context.Background()
	now := time.Now().UTC()

	assertion := visual.Assertion{
		ID: 1, VersionID: 10, CaseVersionID: 20, CaseAssertionID: 30,
		StepSeq: 1, AssertionSeq: 1, VersionSUTID: 40, Selector: "",
		Region: json.RawMessage(`{}`), Threshold: 0.005,
		ViewportWidth: 100, ViewportHeight: 100, DeviceScaleFactor: 1,
		WaitPolicy: json.RawMessage(`{}`), ConfigRevision: 1,
		Status: visual.AssertionActive, CreatedAt: now, UpdatedAt: now,
	}
	created, err := repo.Assertions.Create(ctx, assertion)
	if err != nil {
		t.Fatalf("create assertion: %v", err)
	}
	if created.ID != 1 {
		t.Fatalf("created id = %d", created.ID)
	}

	first := visual.Baseline{
		ID: 2, VisualAssertionID: 1, CaseVersionID: 20, VersionSUTID: 40,
		EvidenceID: 50, SHA256: testFingerprint, SizeBytes: 10, Width: 100, Height: 100,
		ViewportWidth: 100, ViewportHeight: 100, DeviceScaleFactor: 1,
		ConfigRevision: 1, EnvironmentFingerprint: testFingerprint,
		Status: visual.BaselineApproved, ApprovedBy: 7, ApprovedAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := repo.Baselines.InsertApproved(ctx, first, now); err != nil {
		t.Fatalf("insert first baseline: %v", err)
	}
	second := first
	second.ID = 3
	second.EvidenceID = 51
	if _, err := repo.Baselines.InsertApproved(ctx, second, now.Add(time.Minute)); err != nil {
		t.Fatalf("insert second baseline: %v", err)
	}
	active, ok, err := repo.Baselines.Active(ctx, 1)
	if err != nil || !ok || active.ID != 3 {
		t.Fatalf("active = %+v ok=%v err=%v", active, ok, err)
	}
	history, err := repo.Baselines.List(ctx, 1)
	if err != nil || len(history) != 2 {
		t.Fatalf("history = %d err=%v", len(history), err)
	}
	if history[1].Status != visual.BaselineSuperseded {
		t.Fatalf("first baseline status = %s", history[1].Status)
	}

	// A configuration update supersedes the approved baseline in the same
	// transaction.
	updated := created
	updated.Selector = "#chart"
	updated.ConfigRevision = 2
	updated.UpdatedAt = now.Add(2 * time.Minute)
	if _, err := repo.Assertions.UpdateWithRevision(ctx, updated, updated.UpdatedAt); err != nil {
		t.Fatalf("update assertion: %v", err)
	}
	if _, ok, err := repo.Baselines.Active(ctx, 1); err != nil || ok {
		t.Fatalf("baseline must be superseded after config update: ok=%v err=%v", ok, err)
	}

	diff := visual.Diff{
		ID: 4, VisualAssertionID: 1, CaseRunID: 60, AssertionResultID: 70,
		ActualEvidenceID: 50, DiffRatio: 0.002, Threshold: 0.005,
		Classification: visual.ClassificationPassed, Reason: visual.ReasonVisualMatch,
		EnvironmentMatch: true, Width: 100, Height: 100,
		MismatchBoxes: json.RawMessage(`[]`), CreatedAt: now, UpdatedAt: now,
	}
	if _, createdNew, err := repo.Diffs.Insert(ctx, diff); err != nil || !createdNew {
		t.Fatalf("insert diff created=%v err=%v", createdNew, err)
	}
	replay := diff
	replay.ID = 5
	stored, createdNew, err := repo.Diffs.Insert(ctx, replay)
	if err != nil || createdNew || stored.ID != diff.ID {
		t.Fatalf("diff replay stored=%+v created=%v err=%v", stored, createdNew, err)
	}

	overview, err := repo.Diffs.Overview(ctx, nil)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.AssertionTotal != 1 || overview.BaselineCovered != 0 {
		t.Fatalf("overview counts = %+v", overview)
	}
	if overview.DiffCounts[visual.ClassificationPassed] != 1 {
		t.Fatalf("overview classifications = %+v", overview.DiffCounts)
	}

	var _ visualapp.DiffRepository = repo.Diffs
}
