//go:build integration

package integration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionpostgres "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpostgres "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// TestP7VersionContinuingIntegration drives the real version/requirement
// services over PostgreSQL: a requirement created on a completed version moves
// it to continuing (clarifying) exactly once even under concurrency, confirming
// the requirement moves the phase to designing, and the continuing finalize
// gates (incremental batch, settled autonomy, no draft) decide completion.
func TestP7VersionContinuingIntegration(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	ids := p7xNewIDs(920000)
	versionRepo := versionpostgres.NewVersionRepository(pool)
	versionSvc := versionapp.NewVersionService(versionRepo, nil, nil, nil,
		versionapp.WithFinalizeBatchReader(versionRepo))
	requirementRepo := versionpostgres.NewRequirementRepository(pool)
	requirementSvc := versionapp.NewRequirementService(requirementRepo, versionRepo, nil,
		versionapp.WithRequirementIDGenerator(ids))
	batches := executionpostgres.NewBatchRepository(pool)
	autonomy := executionpostgres.NewAutonomyStore(pool)

	// 1. completed -> requirement -> continuing.
	version := p7xCreateVersion(t, versionRepo, 5201, versiondomain.VersionCompleted, "")
	first, err := requirementSvc.Create(ctx, version.ID, versionapp.CreateRequirementCommand{
		Title: "Coupons", Description: "support coupon codes at checkout",
	}, 7)
	if err != nil {
		t.Fatalf("create requirement on completed version: %v", err)
	}
	stored, err := versionRepo.Get(ctx, version.ID)
	if err != nil {
		t.Fatalf("get continued version: %v", err)
	}
	if stored.Status != versiondomain.VersionContinuing ||
		stored.ContinuePhase != versiondomain.ContinuePhaseClarifying ||
		stored.ContinuedAt == nil || stored.Revision != 2 {
		t.Fatalf("continued version = %+v", stored)
	}
	if first.VersionID != version.ID || first.Status != versiondomain.RequirementDraft {
		t.Fatalf("created requirement = %+v", first)
	}

	// 2. Concurrent creates on another completed version transition it once and
	// persist both requirements.
	concurrent := p7xCreateVersion(t, versionRepo, 5202, versiondomain.VersionCompleted, "")
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i, title := range []string{"Wishlist", "Gift cards"} {
		wg.Add(1)
		go func(index int, title string) {
			defer wg.Done()
			_, err := requirementSvc.Create(ctx, concurrent.ID, versionapp.CreateRequirementCommand{
				Title: title,
			}, int64(10+index))
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(i, title)
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("concurrent requirement creates: %v", errs)
	}
	concurrentStored, err := versionRepo.Get(ctx, concurrent.ID)
	if err != nil {
		t.Fatalf("get concurrent version: %v", err)
	}
	if concurrentStored.Status != versiondomain.VersionContinuing ||
		concurrentStored.ContinuePhase != versiondomain.ContinuePhaseClarifying ||
		concurrentStored.Revision != 2 {
		t.Fatalf("concurrent version = %+v, want continuing/clarifying revision 2 (one transition)", concurrentStored)
	}
	concurrentRequirements, _, err := requirementSvc.List(ctx, concurrent.ID, "", versiondomain.Page{Limit: 10})
	if err != nil {
		t.Fatalf("list concurrent requirements: %v", err)
	}
	if len(concurrentRequirements) != 2 {
		t.Fatalf("concurrent requirements = %d, want 2", len(concurrentRequirements))
	}

	// 3. Confirming the draft moves the phase to designing.
	if _, err := requirementSvc.Confirm(ctx, version.ID, first.ID, 7); err != nil {
		t.Fatalf("confirm requirement: %v", err)
	}
	stored, err = versionRepo.Get(ctx, version.ID)
	if err != nil {
		t.Fatalf("get designing version: %v", err)
	}
	if stored.ContinuePhase != versiondomain.ContinuePhaseDesigning {
		t.Fatalf("continue phase = %q, want designing", stored.ContinuePhase)
	}

	// 4. A planned incremental batch is an active batch: finalize must wait.
	incremental := executiondomain.RunBatch{
		ID: 52011, VersionID: version.ID, BatchNo: 1, Status: executiondomain.BatchStatusPlanned,
		Kind: executiondomain.BatchKindIncremental, BaselineHash: p7xValidHash('a'),
		GraphHash: p7xValidHash('a'), CreatedBy: 7, CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
	}
	p7xSeedRunBatch(t, batches, incremental, nil)
	if _, err := versionRepo.SetContinuePhase(ctx, version.ID, versiondomain.ContinuePhaseExecuting, 7); err != nil {
		t.Fatalf("set executing phase: %v", err)
	}
	if finalized, err := versionSvc.TryFinalize(ctx, version.ID); err != nil || finalized {
		t.Fatalf("finalize with an active batch = %v/%v, want false/nil", finalized, err)
	}

	// 5. A draft requirement still blocks the continuing finalize.
	p7xMarkBatchDone(t, batches, incremental.ID, executiondomain.BatchStatusPlanned)
	if won, err := autonomy.MarkAutonomyDone(ctx, incremental.ID); err != nil || !won {
		t.Fatalf("mark autonomy done = %v/%v", won, err)
	}
	draft, err := requirementSvc.Create(ctx, version.ID, versionapp.CreateRequirementCommand{
		Title: "Address book",
	}, 7)
	if err != nil {
		t.Fatalf("create draft requirement while executing: %v", err)
	}
	if finalized, err := versionSvc.TryFinalize(ctx, version.ID); err != nil || finalized {
		t.Fatalf("finalize with a draft = %v/%v, want false/nil", finalized, err)
	}

	// 6. Confirming the draft clears the last gate: the continuing version
	// completes and its phase is cleared.
	if _, err := requirementSvc.Confirm(ctx, version.ID, draft.ID, 7); err != nil {
		t.Fatalf("confirm draft requirement: %v", err)
	}
	finalized, err := versionSvc.TryFinalize(ctx, version.ID)
	if err != nil || !finalized {
		t.Fatalf("finalize settled continuing version = %v/%v, want true/nil", finalized, err)
	}
	completed, err := versionRepo.Get(ctx, version.ID)
	if err != nil {
		t.Fatalf("get completed version: %v", err)
	}
	if completed.Status != versiondomain.VersionCompleted || completed.ContinuePhase != "" {
		t.Fatalf("completed version = %+v, want completed with no phase", completed)
	}
	// Idempotent: a second finalize is a quiet false.
	if again, err := versionSvc.TryFinalize(ctx, version.ID); err != nil || again {
		t.Fatalf("second finalize = %v/%v, want false/nil", again, err)
	}

	// 7. CompleteContinuing is idempotent on an already completed version.
	if done, err := versionSvc.CompleteContinuing(ctx, version.ID, 7); err != nil ||
		done.Status != versiondomain.VersionCompleted {
		t.Fatalf("complete continuing = %+v/%v", done, err)
	}
}

// TestP7VersionFinalizeIntegration covers the executing finalize gates and the
// autonomy_done condition against PostgreSQL: a terminal batch with
// autonomy_done=false blocks the version, a paused version never finalizes and
// the finalize CAS completes the version once.
func TestP7VersionFinalizeIntegration(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	versionRepo := versionpostgres.NewVersionRepository(pool)
	versionSvc := versionapp.NewVersionService(versionRepo, nil, nil, nil,
		versionapp.WithFinalizeBatchReader(versionRepo))
	batches := executionpostgres.NewBatchRepository(pool)
	autonomy := executionpostgres.NewAutonomyStore(pool)

	// Executing version with one unsettled terminal batch.
	version := p7xCreateVersion(t, versionRepo, 5211, versiondomain.VersionExecuting, "")
	terminal := executiondomain.RunBatch{
		ID: 52111, VersionID: version.ID, BatchNo: 1, Status: executiondomain.BatchStatusDone,
		Kind: executiondomain.BatchKindBaseline, BaselineHash: p7xValidHash('a'),
		GraphHash: p7xValidHash('a'), CreatedBy: 1, CreatedAt: p7xTestTime, UpdatedAt: p7xTestTime,
		FinishedAt: p7xTimePtr(p7xTestTime.Add(5 * time.Minute)),
	}
	p7xSeedRunBatch(t, batches, terminal, nil)
	if finalized, err := versionSvc.TryFinalize(ctx, version.ID); err != nil || finalized {
		t.Fatalf("finalize with autonomy_done=false = %v/%v, want false/nil", finalized, err)
	}

	// Pausing does not unblock the unsettled batch either; it short-circuits.
	if _, err := versionRepo.SetPaused(ctx, version.ID, true, versiondomain.PauseModeGraceful, 1); err != nil {
		t.Fatalf("pause version: %v", err)
	}
	if finalized, err := versionSvc.TryFinalize(ctx, version.ID); err != nil || finalized {
		t.Fatalf("finalize paused version = %v/%v, want false/nil", finalized, err)
	}
	if _, err := versionRepo.SetPaused(ctx, version.ID, false, versiondomain.PauseModeGraceful, 1); err != nil {
		t.Fatalf("resume version: %v", err)
	}

	// Settling the autonomy chain lets the finalize CAS complete the version.
	if won, err := autonomy.MarkAutonomyDone(ctx, terminal.ID); err != nil || !won {
		t.Fatalf("mark autonomy done = %v/%v", won, err)
	}
	if finalized, err := versionSvc.TryFinalize(ctx, version.ID); err != nil || !finalized {
		t.Fatalf("finalize settled version = %v/%v, want true/nil", finalized, err)
	}
	completed, err := versionRepo.Get(ctx, version.ID)
	if err != nil {
		t.Fatalf("get finalized version: %v", err)
	}
	if completed.Status != versiondomain.VersionCompleted {
		t.Fatalf("finalized status = %q, want completed", completed.Status)
	}

	// A fresh executing version without any batch never finalizes.
	empty := p7xCreateVersion(t, versionRepo, 5212, versiondomain.VersionExecuting, "")
	if finalized, err := versionSvc.TryFinalize(ctx, empty.ID); err != nil || finalized {
		t.Fatalf("finalize batch-less version = %v/%v, want false/nil", finalized, err)
	}
}

// TestP7VersionRequirementArchivedRejected covers the cross-context requirement
// gate: an archived version refuses a new requirement without writing one, and
// the error is the stable version_not_continuable state conflict.
func TestP7VersionRequirementArchivedRejected(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	versionRepo := versionpostgres.NewVersionRepository(pool)
	requirementRepo := versionpostgres.NewRequirementRepository(pool)
	requirementSvc := versionapp.NewRequirementService(requirementRepo, versionRepo, nil,
		versionapp.WithRequirementIDGenerator(p7xNewIDs(925000)))

	archived := p7xCreateVersion(t, versionRepo, 5221, versiondomain.VersionDraft, "")
	if _, err := versionRepo.UpdateStatus(ctx, archived.ID, versiondomain.VersionDraft, versiondomain.VersionArchived, 1); err != nil {
		t.Fatalf("mark version archived: %v", err)
	}
	if _, err := requirementSvc.Create(ctx, archived.ID, versionapp.CreateRequirementCommand{
		Title: "Should not exist",
	}, 7); err == nil {
		t.Fatal("archived version accepted a requirement")
	} else {
		var domainErr *versiondomain.Error
		if !errors.As(err, &domainErr) || domainErr.Reason != "version_not_continuable" {
			t.Fatalf("archived create error = %v, want version_not_continuable", err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM requirement WHERE version_id = $1`, archived.ID).Scan(&count); err != nil {
		t.Fatalf("count requirements: %v", err)
	}
	if count != 0 {
		t.Fatalf("requirements written for archived version = %d, want 0", count)
	}
}

// p7xTimePtr returns a pointer to value.
func p7xTimePtr(value time.Time) *time.Time { return &value }
