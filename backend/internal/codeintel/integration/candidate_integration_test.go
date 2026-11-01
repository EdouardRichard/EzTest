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

func TestCandidateRepositoryIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	analyses := codeintelpg.NewAnalysisRepository(pool)
	candidates := codeintelpg.NewCandidateRepository(pool, ids)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	analysis, err := analyses.Create(ctx, codeintel.Analysis{
		ID: 9201, VersionID: 1, RepoID: 10, BaseCommitSHA: anchorIntegrationCommit,
		HeadCommitSHA: anchorIntegrationHead, Status: codeintel.AnalysisQueued, AnalysisVersion: 1,
		ChangedFiles: []byte(`[]`), Stats: []byte(`{}`), Unmapped: []byte(`[]`),
		Errors: []byte(`[]`), FactVersions: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("Create analysis: %v", err)
	}

	candidate := codeintel.ImpactCandidate{
		SubjectKind: codeintel.SubjectRequirement, SubjectID: 100, Reason: "anchor hit",
		Confidence: codeintel.ConfidenceHigh, Source: codeintel.CandidateDirect,
		Evidence: []byte(`[{"file":"src/a.ts"}]`), Selected: true,
	}
	if err := candidates.ReplaceForAnalysis(ctx, analysis.ID, []codeintel.ImpactCandidate{candidate, {
		SubjectKind: codeintel.SubjectCaseVersion, SubjectID: 200, Reason: "anchor hit",
		Confidence: codeintel.ConfidenceLow, Source: codeintel.CandidateAI, Evidence: []byte(`[]`),
	}}); err != nil {
		t.Fatalf("ReplaceForAnalysis: %v", err)
	}
	stored, err := candidates.ListByAnalysis(ctx, analysis.ID)
	if err != nil {
		t.Fatalf("ListByAnalysis: %v", err)
	}
	if len(stored) != 2 || stored[0].ID == 0 || stored[1].ID == 0 {
		t.Fatalf("stored = %+v", stored)
	}
	if stored[0].SubjectID != 100 || stored[0].Source != codeintel.CandidateDirect || !stored[0].Selected {
		t.Fatalf("first = %+v", stored[0])
	}
	if stored[1].Selected {
		t.Fatalf("low candidate must not be selected: %+v", stored[1])
	}

	updated, err := candidates.UpdateSelection(ctx, analysis.ID, stored[0].ID, 42, false)
	if err != nil {
		t.Fatalf("UpdateSelection: %v", err)
	}
	if updated.Selected || updated.SelectionUpdatedBy == nil || *updated.SelectionUpdatedBy != 42 || updated.SelectionUpdatedAt == nil {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := candidates.UpdateSelection(ctx, 9999, stored[0].ID, 42, true); !errors.Is(err, codeintel.ErrNotFound) {
		t.Fatalf("foreign update error = %v", err)
	}

	// Replace is idempotent: the old set is removed, not appended.
	if err := candidates.ReplaceForAnalysis(ctx, analysis.ID, []codeintel.ImpactCandidate{candidate}); err != nil {
		t.Fatalf("ReplaceForAnalysis replace: %v", err)
	}
	stored, err = candidates.ListByAnalysis(ctx, analysis.ID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("replaced = %+v err=%v", stored, err)
	}
}

func TestImpactEdgeRepositoryIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	entities := codeintelpg.NewEntityRepository(pool, ids)
	edges := codeintelpg.NewImpactEdgeRepository(pool)
	ctx := context.Background()
	now := time.Now()

	src, err := entities.UpsertFactEntity(ctx, codeintel.Entity{
		ID: ids.Next(), RepoID: 10, CommitSHA: anchorIntegrationHead, Kind: codeintel.EntityClientCall,
		Path: "src/client.ts", Name: "POST /api/refund", Source: "ai", Confidence: codeintel.ConfidenceHigh,
		Evidence: []byte(`[]`), Meta: []byte(`{}`), FactKey: "client:POST /api/refund",
		FactVersion: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("Upsert src: %v", err)
	}
	dst, err := entities.UpsertFactEntity(ctx, codeintel.Entity{
		ID: ids.Next(), RepoID: 10, CommitSHA: anchorIntegrationHead, Kind: codeintel.EntityAPI,
		Path: "server/refund.go", Name: "POST /api/refund", Source: "ai", Confidence: codeintel.ConfidenceHigh,
		Evidence: []byte(`[]`), Meta: []byte(`{}`), FactKey: "api:POST /api/refund",
		FactVersion: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("Upsert dst: %v", err)
	}
	if err := entities.UpsertEdge(ctx, codeintel.Edge{
		ID: ids.Next(), RepoID: 10, CommitSHA: anchorIntegrationHead,
		SrcEntityID: src.ID, DstEntityID: dst.ID, Rel: codeintel.EdgeRouteToAPI,
		Source: "ai", Confidence: codeintel.ConfidenceMedium, Evidence: []byte(`[]`), Meta: []byte(`{}`),
		FactKey: "client:POST /api/refund->api:POST /api/refund", FactVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}
	result, err := edges.ImpactEdges(ctx, 10, anchorIntegrationHead)
	if err != nil {
		t.Fatalf("ImpactEdges: %v", err)
	}
	if len(result) != 1 || result[0].SrcFactKey != "client:POST /api/refund" ||
		result[0].DstFactKey != "api:POST /api/refund" || result[0].Rel != codeintel.EdgeRouteToAPI {
		t.Fatalf("edges = %+v", result)
	}
}
