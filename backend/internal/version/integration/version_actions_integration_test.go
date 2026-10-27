//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// versionIntegrationCommit is a valid Git commit id used by the fake reader.
const versionIntegrationCommit = "0123456789abcdef0123456789abcdef01234567"

// integrationCatalog is a ResourceCatalog with one active system (7),
// environment (8, owned by 7) and code repository (9, fixedBranch=main).
type integrationCatalog struct{}

// GetSystem returns the fixture system.
func (integrationCatalog) GetSystem(_ context.Context, id int64) (application.ResourceRef, error) {
	if id != 7 {
		return application.ResourceRef{}, &application.ResourceError{Reason: "system_not_found"}
	}
	return application.ResourceRef{ID: 7, Name: "Order system", Status: "active", Available: true}, nil
}

// GetEnvironment returns the fixture environment.
func (integrationCatalog) GetEnvironment(_ context.Context, id int64) (application.ResourceRef, error) {
	if id != 8 {
		return application.ResourceRef{}, &application.ResourceError{Reason: "environment_not_found"}
	}
	systemID := int64(7)
	return application.ResourceRef{
		ID: 8, Name: "staging", Status: "active", SystemID: &systemID, Available: true,
	}, nil
}

// GetCodeRepository returns the fixture repository.
func (integrationCatalog) GetCodeRepository(_ context.Context, id int64) (application.CodeRepoRef, error) {
	if id != 9 {
		return application.CodeRepoRef{}, &application.ResourceError{Reason: "code_repository_not_found"}
	}
	return application.CodeRepoRef{
		ID: 9, Name: "order-web", Status: "active", LocalPath: "/srv/order-web",
		FixedBranch: "main", Available: true,
	}, nil
}

// integrationGit is a GitReader returning a fixed commit.
type integrationGit struct{ calls int }

// ResolveCommit returns the fixture commit.
func (g *integrationGit) ResolveCommit(_ context.Context, repoPath, branch string) (string, error) {
	g.calls++
	if repoPath != "/srv/order-web" || branch != "main" {
		return "", &application.GitError{Reason: "branch_not_found", Message: "unexpected branch"}
	}
	return versionIntegrationCommit, nil
}

// Tree is unused by VersionService.
func (g *integrationGit) Tree(context.Context, string, string, []string, []string, int) ([]application.CodeEntry, error) {
	return nil, nil
}

// Read is unused by VersionService.
func (g *integrationGit) Read(context.Context, string, string, string, int) (string, error) {
	return "", nil
}

// integrationEvents records published version events.
type integrationEvents struct{ events []application.VersionEvent }

// Publish records e.
func (e *integrationEvents) Publish(_ context.Context, event application.VersionEvent) error {
	e.events = append(e.events, event)
	return nil
}

// integrationIDs is a deterministic Snowflake id sequence.
type integrationIDs struct{ next int64 }

// Next returns the next id.
func (i *integrationIDs) Next() int64 {
	id := i.next
	i.next++
	return id
}

// assertIntegrationReason asserts err is a *domain.Error with kind and reason.
func assertIntegrationReason(t *testing.T, err error, kind domain.ErrorKind, reason string) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want *domain.Error kind=%s reason=%s, got nil", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *domain.Error, got %T (%v)", err, err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("error kind/reason = %s/%s, want %s/%s (%v)",
			domainErr.Kind, domainErr.Reason, kind, reason, err)
	}
	return domainErr
}

// TestVersionActionsLifecycle drives the real repository and service through
// the P3 version pipeline: create with resource/git-snapshot bindings, the
// start-clarification requirement gate, the enter-design soft gate, metadata
// CAS under designing, blocked binding changes under designing, and the
// published event order.
func TestVersionActionsLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repository := postgres.NewVersionRepository(pool)
	events := &integrationEvents{}
	git := &integrationGit{}
	ids := &integrationIDs{next: 7001}
	service := application.NewVersionService(
		repository, integrationCatalog{}, git, events,
		application.WithIDGenerator(ids),
	)

	// Create with full bindings: the fixture branch resolved to a commit.
	systemID, environmentID, repositoryID := int64(7), int64(8), int64(9)
	created, err := service.Create(ctx, application.CreateVersionCommand{
		Name: "Order v2", Label: "v2.0",
		SystemID: &systemID, EnvironmentID: &environmentID, CodeRepositoryID: &repositoryID,
	}, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != 7001 || created.Status != domain.VersionDraft || created.Revision != 1 {
		t.Fatalf("created = %+v", created)
	}
	if created.CodeBranch == nil || *created.CodeBranch != "main" ||
		created.CodeBaselineCommit == nil || *created.CodeBaselineCommit != versionIntegrationCommit {
		t.Fatalf("code snapshot = %v/%v", created.CodeBranch, created.CodeBaselineCommit)
	}
	if git.calls != 1 {
		t.Fatalf("git resolutions = %d, want 1", git.calls)
	}
	stored, err := repository.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get created: %v", err)
	}
	if stored.Name != "Order v2" || stored.SystemID == nil || *stored.SystemID != 7 {
		t.Fatalf("stored = %+v", stored)
	}

	// No requirement yet: start-clarification is the requirements_required 422.
	_, err = service.StartClarification(ctx, created.ID, 42)
	assertIntegrationReason(t, err, domain.KindInvalid, "requirements_required")
	afterReject, err := repository.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after rejected start: %v", err)
	}
	if afterReject.Status != domain.VersionDraft || afterReject.Revision != 1 {
		t.Fatalf("rejected start changed the row: %+v", afterReject)
	}

	// One draft requirement unlocks clarifying.
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirements (id, version_id, title, status, created_by, updated_by)
		VALUES (7101, $1, 'Checkout flow', 'draft', 7, 7)`, created.ID); err != nil {
		t.Fatalf("insert requirement: %v", err)
	}
	clarifying, err := service.StartClarification(ctx, created.ID, 42)
	if err != nil {
		t.Fatalf("StartClarification: %v", err)
	}
	if clarifying.Status != domain.VersionClarifying || clarifying.Revision != 2 {
		t.Fatalf("clarifying = %+v", clarifying)
	}

	// One version-level open question plus the draft requirement: the soft gate
	// rejects unacknowledged enter-design and reports both warnings.
	if _, err := pool.Exec(ctx, `
		INSERT INTO clarification_questions (id, version_id, requirement_id, question, status, created_by)
		VALUES (7201, $1, NULL, 'Which locales?', 'open', 7)`, created.ID); err != nil {
		t.Fatalf("insert question: %v", err)
	}
	_, warnings, err := service.EnterDesign(ctx, created.ID, false, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "unclarified_requirements")
	var gateErr *application.UnclarifiedRequirementsError
	if !errors.As(err, &gateErr) {
		t.Fatalf("soft gate error = %T, want *application.UnclarifiedRequirementsError", err)
	}
	if len(warnings.UnclarifiedRequirementIDs) != 1 || warnings.UnclarifiedRequirementIDs[0] != 7101 ||
		warnings.OpenQuestionCount != 1 {
		t.Fatalf("warnings = %+v", warnings)
	}
	afterGate, err := repository.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after soft gate: %v", err)
	}
	if afterGate.Status != domain.VersionClarifying || afterGate.Revision != 2 {
		t.Fatalf("rejected enter-design changed the row: %+v", afterGate)
	}

	// Acknowledging the warnings moves to designing and returns them.
	designing, warnings, err := service.EnterDesign(ctx, created.ID, true, 42)
	if err != nil {
		t.Fatalf("EnterDesign ack: %v", err)
	}
	if designing.Status != domain.VersionDesigning || designing.Revision != 3 {
		t.Fatalf("designing = %+v", designing)
	}
	if len(warnings.UnclarifiedRequirementIDs) != 1 || warnings.OpenQuestionCount != 1 {
		t.Fatalf("ack warnings = %+v", warnings)
	}

	// Metadata is editable in designing and keeps the status; a stale revision
	// is a revision_conflict.
	renamed, err := service.UpdateMetadata(ctx, created.ID, 3, "Order v2.1", "v2.1", "renamed", 42)
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if renamed.Name != "Order v2.1" || renamed.Status != domain.VersionDesigning || renamed.Revision != 4 {
		t.Fatalf("renamed = %+v", renamed)
	}
	_, err = service.UpdateMetadata(ctx, created.ID, 3, "Stale", "", "", 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "revision_conflict")

	// Bindings are frozen once designing (INV-13).
	_, err = service.UpdateBindings(ctx, created.ID, 4, domain.VersionBindings{}, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "version_state_conflict")
	afterBindings, err := repository.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after blocked bindings: %v", err)
	}
	if afterBindings.SystemID == nil || *afterBindings.SystemID != 7 || afterBindings.Revision != 4 {
		t.Fatalf("blocked bindings changed the row: %+v", afterBindings)
	}

	// List reflects the status filter.
	designingPage, more, err := service.List(ctx, domain.VersionFilter{
		Status: domain.VersionDesigning,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatalf("List designing: %v", err)
	}
	if len(designingPage) != 1 || designingPage[0].ID != created.ID || more {
		t.Fatalf("designing page = %+v more=%v", designingPage, more)
	}

	// Event order: created, clarifying, designing, metadata, metadata conflict
	// publishes nothing.
	wantTypes := []string{
		"version.created",
		"version.status_changed",
		"version.status_changed",
		"version.updated",
	}
	if len(events.events) != len(wantTypes) {
		t.Fatalf("events = %d (%v), want %v", len(events.events), eventTypes(events.events), wantTypes)
	}
	for i, want := range wantTypes {
		if events.events[i].Type != want {
			t.Fatalf("event %d = %q, want %q (all: %v)", i, events.events[i].Type, want, eventTypes(events.events))
		}
		if events.events[i].VersionID != created.ID || events.events[i].ActorID != 42 {
			t.Fatalf("event %d = %+v", i, events.events[i])
		}
	}
}

// TestVersionStatusIsNotChangedByRequirementOps proves INV-2/INV-4 against the
// real schema: confirming, reopening and deleting requirements (the T9 service
// primitives, executed here as raw SQL) never touch Version.status or revision.
func TestVersionStatusIsNotChangedByRequirementOps(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repository := postgres.NewVersionRepository(pool)
	ids := &integrationIDs{next: 8001}
	service := application.NewVersionService(
		repository, integrationCatalog{}, &integrationGit{}, &integrationEvents{},
		application.WithIDGenerator(ids),
	)

	for _, status := range []string{
		domain.VersionDraft, domain.VersionClarifying, domain.VersionDesigning,
	} {
		version, err := service.Create(ctx, application.CreateVersionCommand{Name: "Status " + status}, 42)
		if err != nil {
			t.Fatalf("Create %s: %v", status, err)
		}
		if status != domain.VersionDraft {
			if _, err := pool.Exec(ctx, `UPDATE versions SET status = $2 WHERE id = $1`, version.ID, status); err != nil {
				t.Fatalf("seed status %s: %v", status, err)
			}
		}
		requirementID := version.ID + 100
		if _, err := pool.Exec(ctx, `
			INSERT INTO requirements (id, version_id, title, status, created_by, updated_by)
			VALUES ($1, $2, 'Requirement', 'draft', 7, 7)`, requirementID, version.ID); err != nil {
			t.Fatalf("insert requirement: %v", err)
		}

		operations := []struct {
			name string
			sql  string
			args []any
		}{
			{"confirm", `UPDATE requirements SET status = 'clarified', confirmed_at = now(), confirmed_by = 7 WHERE id = $1`, []any{requirementID}},
			{"reopen", `UPDATE requirements SET status = 'draft', confirmed_at = NULL, confirmed_by = NULL WHERE id = $1`, []any{requirementID}},
			{"update", `UPDATE requirements SET title = 'Updated', updated_by = 7 WHERE id = $1`, []any{requirementID}},
			{"delete", `DELETE FROM requirements WHERE id = $1`, []any{requirementID}},
		}
		for _, operation := range operations {
			if _, err := pool.Exec(ctx, operation.sql, operation.args...); err != nil {
				t.Fatalf("%s requirement: %v", operation.name, err)
			}
			after, err := repository.Get(ctx, version.ID)
			if err != nil {
				t.Fatalf("Get after %s: %v", operation.name, err)
			}
			if after.Status != status || after.Revision != 1 {
				t.Fatalf("%s changed version %d: status=%s revision=%d, want %s/1",
					operation.name, version.ID, after.Status, after.Revision, status)
			}
		}
	}
}

// eventTypes projects the event type list for readable failures.
func eventTypes(events []application.VersionEvent) []string {
	types := make([]string, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}
