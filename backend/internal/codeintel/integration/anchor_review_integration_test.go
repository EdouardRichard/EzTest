//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

const (
	anchorIntegrationCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	anchorIntegrationHead   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestAnchorRepositoryIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := codeintelpg.NewAnchorRepository(pool)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	start, end := 2, 3
	anchor := codeintel.Anchor{
		ID: 9001, VersionID: 1, RepoID: 10, SubjectKind: codeintel.AnchorSubjectRequirement,
		SubjectID: 100, FilePath: "src/orders.ts", StartLine: &start, EndLine: &end,
		CommitSHA: anchorIntegrationCommit, ExcerptSHA: "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111",
		Status: string(codeintel.AnchorValid), Confidence: "high", CreatedByKind: "user",
		LastValidatedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	created, err := repo.Create(ctx, anchor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != 9001 || created.SubjectKind != codeintel.AnchorSubjectRequirement || created.Status != "valid" {
		t.Fatalf("created = %+v", created)
	}

	duplicate := anchor
	duplicate.ID = 9002
	if _, err := repo.Create(ctx, duplicate); !errors.Is(err, codeintel.ErrConflict) {
		t.Fatalf("duplicate error = %v, want ErrConflict", err)
	}

	other := anchor
	other.ID = 9003
	other.SubjectKind = codeintel.AnchorSubjectCaseVersion
	other.SubjectID = 200
	other.StartLine = nil
	other.EndLine = nil
	other.FilePath = "src/cases.ts"
	if _, err := repo.Create(ctx, other); err != nil {
		t.Fatalf("Create other: %v", err)
	}

	list, err := repo.List(ctx, 1, "requirement", 0)
	if err != nil || len(list) != 1 || list[0].ID != 9001 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	list, err = repo.List(ctx, 1, "", 200)
	if err != nil || len(list) != 1 || list[0].ID != 9003 {
		t.Fatalf("list by subject = %+v err=%v", list, err)
	}
	list, err = repo.List(ctx, 2, "", 0)
	if err != nil || len(list) != 0 {
		t.Fatalf("foreign version list = %+v err=%v", list, err)
	}

	validatedAt := now.Add(time.Hour)
	updated, err := repo.UpdateStatus(ctx, 9001, "stale", validatedAt)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if updated.Status != "stale" || updated.LastValidatedAt == nil || !updated.LastValidatedAt.Equal(validatedAt) {
		t.Fatalf("updated = %+v", updated)
	}

	if err := repo.Delete(ctx, 9001); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, 9001); !errors.Is(err, codeintel.ErrNotFound) {
		t.Fatalf("get deleted = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, 9001); !errors.Is(err, codeintel.ErrNotFound) {
		t.Fatalf("delete deleted = %v, want ErrNotFound", err)
	}
}

func TestReviewRepositoryIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	facts := codeintelpg.NewFactRepository(pool, ids)
	reviews := codeintelpg.NewReviewRepository(pool)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	fact, _, err := facts.InsertVersion(ctx, codeintel.Fact{
		RepoID: 10, CommitSHA: anchorIntegrationCommit, FactKey: "route:/orders", Version: 1,
		Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "/orders",
		Body: []byte(`{}`), Evidence: []byte(`[]`), Source: "ai", Confidence: codeintel.ConfidenceHigh,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("InsertVersion: %v", err)
	}
	first, err := reviews.Create(ctx, codeintel.FactReview{
		ID: 9101, FactID: fact.ID, FactVersion: fact.Version, RepoID: 10,
		CommitSHA: anchorIntegrationCommit, Verdict: "correct", Reason: "ok",
		ReviewerID: 42, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := reviews.Create(ctx, codeintel.FactReview{
		ID: 9102, FactID: fact.ID, FactVersion: fact.Version, RepoID: 10,
		CommitSHA: anchorIntegrationCommit, Verdict: "incorrect", Reason: "moved",
		ReviewerID: 43, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	list, err := reviews.List(ctx, fact.ID)
	if err != nil || len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	verdicts, err := reviews.LatestVerdicts(ctx, 10, anchorIntegrationCommit, []string{"route:/orders", "route:/missing"})
	if err != nil {
		t.Fatalf("LatestVerdicts: %v", err)
	}
	if verdicts["route:/orders"] != "incorrect" || len(verdicts) != 1 {
		t.Fatalf("verdicts = %+v", verdicts)
	}
	verdicts, err = reviews.LatestVerdicts(ctx, 10, anchorIntegrationHead, []string{"route:/orders"})
	if err != nil || len(verdicts) != 0 {
		t.Fatalf("foreign commit verdicts = %+v err=%v", verdicts, err)
	}
}
