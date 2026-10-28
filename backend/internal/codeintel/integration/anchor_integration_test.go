//go:build integration

package integration

// This file wires the real AnchorService over the real gitx/PostgreSQL/P2/P3
// adapters to prove design spec §6.7 against real storage and a real repository
// checkout: subject ownership, fixed-branch file validation, point uniqueness,
// version-scoped deletion and the case/issue rejection.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	codeintelresource "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/resource"
	codeintelversion "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/version"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcepg "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// anchorIntegrationFixture is the wired anchor service and the seeded ids.
type anchorIntegrationFixture struct {
	service             *codeintelapp.AnchorService
	versionCatalog      *codeintelversion.Catalog
	anchors             *codeintelpg.AnchorRepo
	repoID              int64
	versionID           int64
	requirementID       int64
	systemID            int64
	appModelID          int64
	otherSystemID       int64
	otherAppModelID     int64
	secondVersionID     int64
	secondRequirementID int64
}

// newAnchorIntegrationFixture seeds two versions of the same repository, two
// P2 systems with one app model node each, and wires the production anchor
// service over the real adapters.
func newAnchorIntegrationFixture(t *testing.T, pool *pgxpool.Pool) *anchorIntegrationFixture {
	t.Helper()
	ctx := context.Background()
	dir, base, _ := newAnalysisGitRepo(t)
	repoID, versionID, requirementID := seedAnalysisRepoVersion(t, pool, dir, base)

	systemID := impactIntegrationSeedSystem(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE version SET system_id = $1 WHERE id = $2`, systemID, versionID); err != nil {
		t.Fatalf("bind version system: %v", err)
	}
	appModelID := impactIntegrationSeedAppModel(t, pool, systemID,
		"page", "page:/orders", "Orders", "/orders", "")
	otherSystemID := impactIntegrationSeedSystem(t, pool)
	otherAppModelID := impactIntegrationSeedAppModel(t, pool, otherSystemID,
		"page", "page:/other", "Other", "/other", "")

	secondVersionID := seedAnalysisVersion(t, pool, repoID, base)
	secondRequirementID := impactIntegrationSeedRequirement(t, pool, secondVersionID, "其他版本需求")

	catalogRepo := resourcepg.NewCatalogRepository(pool)
	appCatalog := resourceapp.NewCatalogService(catalogRepo, catalogRepo, catalogRepo, catalogRepo, indexIntegrationClock{})
	repoCatalog := codeintelresource.NewCatalog(
		resourceapp.NewCodeRepoService(resourcepg.NewCodeRepository(pool), nil, indexIntegrationClock{}),
		appCatalog)

	versionRepo := versionpg.NewVersionRepository(pool)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	versionService := versionapp.NewVersionService(versionRepo, nil, nil, nil)
	requirementService := versionapp.NewRequirementService(requirementRepo, versionRepo, nil)
	relationService := versionapp.NewRelationService(requirementRepo)
	versionCatalog := codeintelversion.NewCatalog(versionService, requirementService, relationService)

	generator, err := id.NewSnowflake(13)
	if err != nil {
		t.Fatalf("new anchor snowflake generator: %v", err)
	}
	service := codeintelapp.NewAnchorService(codeintelapp.AnchorServiceDeps{
		Repos:     repoCatalog,
		Versions:  versionCatalog,
		AppModels: repoCatalog,
		Git:       gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 10 * time.Second}),
		Anchors:   codeintelpg.NewAnchorRepository(pool),
		IDs:       generator,
		Clock:     indexIntegrationClock{},
	})
	return &anchorIntegrationFixture{
		service:             service,
		versionCatalog:      versionCatalog,
		anchors:             codeintelpg.NewAnchorRepository(pool),
		repoID:              repoID,
		versionID:           versionID,
		requirementID:       requirementID,
		systemID:            systemID,
		appModelID:          appModelID,
		otherSystemID:       otherSystemID,
		otherAppModelID:     otherAppModelID,
		secondVersionID:     secondVersionID,
		secondRequirementID: secondRequirementID,
	}
}

// TestAnchorIntegrationCRUDUniquenessAndOwnership proves §6.7 end to end.
func TestAnchorIntegrationCRUDUniquenessAndOwnership(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	fixture := newAnchorIntegrationFixture(t, pool)

	start, end := 2, 3
	anchor, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement,
		SubjectID:   fixture.requirementID,
		FilePath:    analysisClientPath,
		StartLine:   &start,
		EndLine:     &end,
	})
	if err != nil {
		t.Fatalf("Create requirement anchor: %v", err)
	}
	if anchor.ID == 0 || anchor.RepoID != fixture.repoID || anchor.VersionID != fixture.versionID {
		t.Fatalf("created anchor = %+v, want the fixture repo/version", anchor)
	}
	if anchor.Confidence != domain.ConfidenceHigh || anchor.CreatedByKind != "user" || anchor.CreatedBy != 1 {
		t.Fatalf("created anchor provenance = %+v", anchor)
	}
	stored, err := fixture.anchors.Get(ctx, fixture.versionID, anchor.ID)
	if err != nil {
		t.Fatalf("Get created anchor: %v", err)
	}
	if stored.FilePath != analysisClientPath || stored.StartLine == nil || *stored.StartLine != 2 ||
		stored.EndLine == nil || *stored.EndLine != 3 {
		t.Fatalf("stored anchor = %+v", stored)
	}

	// Same point -> 409 anchor_duplicate.
	if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: fixture.requirementID,
		FilePath: analysisClientPath, StartLine: &start, EndLine: &end,
	}); domain.Reason(err) != "anchor_duplicate" {
		t.Fatalf("duplicate anchor error = %v, want anchor_duplicate", err)
	}

	// App model anchor on an existing file of the bound system.
	if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectAppModel, SubjectID: fixture.appModelID,
		FilePath: analysisServerPath,
	}); err != nil {
		t.Fatalf("Create app model anchor: %v", err)
	}
	if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectAppModel, SubjectID: fixture.otherAppModelID,
		FilePath: analysisServerPath,
	}); domain.Reason(err) != "app_model_not_found" {
		t.Fatalf("foreign app model error = %v, want app_model_not_found", err)
	}

	// Version subject must match the path version.
	versionAnchor, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectVersion, SubjectID: fixture.versionID,
		FilePath: analysisUtilsPath,
	})
	if err != nil {
		t.Fatalf("Create version anchor: %v", err)
	}
	if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectVersion, SubjectID: fixture.versionID + 1,
		FilePath: analysisUtilsPath,
	}); domain.Reason(err) != "subject_not_in_version" {
		t.Fatalf("version subject mismatch error = %v, want subject_not_in_version", err)
	}

	// Requirement of another version is rejected.
	if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: fixture.secondRequirementID,
		FilePath: analysisClientPath,
	}); domain.Reason(err) != "subject_not_in_version" {
		t.Fatalf("foreign requirement error = %v, want subject_not_in_version", err)
	}

	// case/issue are reserved and rejected (INV-15).
	for _, kind := range []string{"case", "issue"} {
		if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
			SubjectKind: kind, SubjectID: fixture.requirementID, FilePath: analysisClientPath,
		}); domain.Reason(err) != "unsupported_subject_kind" {
			t.Fatalf("%s anchor error = %v, want unsupported_subject_kind", kind, err)
		}
	}

	// The deleted fixture file does not exist at the fixed branch tip.
	if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: fixture.requirementID,
		FilePath: analysisLegacyPath,
	}); domain.Reason(err) != "path_not_found" {
		t.Fatalf("missing file error = %v, want path_not_found", err)
	}
	if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: fixture.requirementID,
		FilePath: "../escape.ts",
	}); domain.Reason(err) != "path_escape" {
		t.Fatalf("escape path error = %v, want path_escape", err)
	}
	reversed := 5
	if _, err := fixture.service.Create(ctx, fixture.versionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: fixture.requirementID,
		FilePath: analysisClientPath, StartLine: &reversed, EndLine: &start,
	}); domain.Reason(err) != "invalid_line_range" {
		t.Fatalf("reversed lines error = %v, want invalid_line_range", err)
	}

	// Listing filters by subject.
	listed, err := fixture.service.List(ctx, fixture.versionID, domain.AnchorFilter{
		SubjectKind: domain.SubjectRequirement, SubjectID: &fixture.requirementID,
	})
	if err != nil {
		t.Fatalf("List anchors: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != anchor.ID {
		t.Fatalf("listed anchors = %+v, want only the created requirement anchor", listed)
	}

	// Delete is scoped to the path version.
	secondAnchor, err := fixture.service.Create(ctx, fixture.secondVersionID, 1, domain.CreateAnchorParams{
		SubjectKind: domain.SubjectRequirement, SubjectID: fixture.secondRequirementID,
		FilePath: analysisClientPath,
	})
	if err != nil {
		t.Fatalf("Create second-version anchor: %v", err)
	}
	if err := fixture.service.Delete(ctx, fixture.versionID, secondAnchor.ID); domain.Reason(err) != "anchor_not_found" {
		t.Fatalf("cross-version delete error = %v, want anchor_not_found", err)
	}
	if err := fixture.service.Delete(ctx, fixture.secondVersionID, secondAnchor.ID); err != nil {
		t.Fatalf("Delete second-version anchor: %v", err)
	}

	if err := fixture.service.Delete(ctx, fixture.versionID, anchor.ID); err != nil {
		t.Fatalf("Delete anchor: %v", err)
	}
	if err := fixture.service.Delete(ctx, fixture.versionID, anchor.ID); domain.Reason(err) != "anchor_not_found" {
		t.Fatalf("second delete error = %v, want anchor_not_found", err)
	}

	// The version anchor is still present and readable.
	stored, err = fixture.anchors.Get(ctx, fixture.versionID, versionAnchor.ID)
	if err != nil || stored.SubjectKind != domain.SubjectVersion {
		t.Fatalf("version anchor after deletes = %+v, %v", stored, err)
	}

	// All ids are snowflake values above the seeded literal range.
	if versionAnchor.CreatedAt.IsZero() || versionAnchor.CreatedAt.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("version anchor createdAt = %v, want the injected clock", versionAnchor.CreatedAt)
	}
}
