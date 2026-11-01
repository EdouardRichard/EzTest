//go:build integration

package deliverypg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newAnalysisRepoForTest(t *testing.T) (*AnalysisRepository, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	return NewAnalysisRepository(pool), pool
}

func analysisRepoInput(t *testing.T, issueID string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"issue_id":   issueID,
		"version_id": "100",
		"refs":       []map[string]any{{"kind": "case_run", "ref_id": "3001"}},
	})
	if err != nil {
		t.Fatalf("marshal input_ref: %v", err)
	}
	return raw
}

// analysisRepoInputHasIssue decodes the analysis input reference and reports
// whether it names issueID (the LatestByIssue projection key).
func analysisRepoInputHasIssue(raw json.RawMessage, issueID int64) bool {
	var input struct {
		IssueID string `json:"issue_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return false
	}
	return input.IssueID == fmt.Sprint(issueID)
}

func analysisRepoQueuedRun(t *testing.T, issueID string) delivery.AnalysisRun {
	t.Helper()
	return delivery.AnalysisRun{
		VersionID: 100,
		Kind:      "triage",
		Status:    delivery.AnalysisStatusQueued,
		DedupeKey: "triage:" + issueID,
		InputRef:  analysisRepoInput(t, issueID),
	}
}

func TestAnalysisRepoCreateQueuedActiveDedupe(t *testing.T) {
	repo, _ := newAnalysisRepoForTest(t)
	ctx := context.Background()

	first, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9001"))
	if err != nil {
		t.Fatalf("first CreateQueued: %v", err)
	}
	if first.ID <= 0 || first.Status != delivery.AnalysisStatusQueued {
		t.Fatalf("first run = %+v", first)
	}
	if first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Fatal("repository must assign analysis timestamps")
	}
	if !analysisRepoInputHasIssue(first.InputRef, 9001) {
		t.Fatalf("input_ref = %s", first.InputRef)
	}

	// A repeat while the first run is active returns the same row and creates no
	// second active run (partial unique key).
	second, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9001"))
	if err != nil {
		t.Fatalf("second CreateQueued: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("active dedupe returned a new row: %d then %d", first.ID, second.ID)
	}
	var activeCount int
	if err := repo.pool.QueryRow(ctx, `
		SELECT count(*) FROM delivery_analysis_run
		WHERE version_id = 100 AND kind = 'triage' AND dedupe_key = 'triage:9001'
		  AND status IN ('queued', 'running')
	`).Scan(&activeCount); err != nil {
		t.Fatalf("count active runs: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active runs = %d, want 1", activeCount)
	}

	// Once the first run is done it no longer participates in the partial unique
	// key, so a new active run is allocated.
	if err := repo.Complete(ctx, first.ID, delivery.AnalysisStatusDone, json.RawMessage(`{"classification":"flaky"}`), ""); err != nil {
		t.Fatalf("Complete first run: %v", err)
	}
	third, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9001"))
	if err != nil {
		t.Fatalf("third CreateQueued: %v", err)
	}
	if third.ID == first.ID {
		t.Fatalf("finished run was reused as active: %d", third.ID)
	}
}

func TestAnalysisRepoAgentRunIsOneToOne(t *testing.T) {
	repo, _ := newAnalysisRepoForTest(t)
	ctx := context.Background()

	first, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9001"))
	if err != nil {
		t.Fatalf("CreateQueued first: %v", err)
	}
	second, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9002"))
	if err != nil {
		t.Fatalf("CreateQueued second: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("different issues shared an analysis run: %d", first.ID)
	}
	if err := repo.MarkRunning(ctx, first.ID, 7001); err != nil {
		t.Fatalf("MarkRunning first: %v", err)
	}
	// The same binding is idempotent.
	if err := repo.MarkRunning(ctx, first.ID, 7001); err != nil {
		t.Fatalf("idempotent MarkRunning: %v", err)
	}
	// A second analysis run cannot claim the same agent_run (unique partial
	// index on agent_run_id).
	if err := repo.MarkRunning(ctx, second.ID, 7001); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("duplicate agent_run MarkRunning error = %v, want delivery.ErrStateConflict", err)
	}
	var agentRunID *int64
	if err := repo.pool.QueryRow(ctx, `SELECT agent_run_id FROM delivery_analysis_run WHERE id = $1`, second.ID).Scan(&agentRunID); err != nil {
		t.Fatalf("load second agent_run_id: %v", err)
	}
	if agentRunID != nil {
		t.Fatalf("rejected analysis run kept agent_run_id=%d", *agentRunID)
	}
}

func TestAnalysisRepoStatusTransitionsAndOutput(t *testing.T) {
	repo, _ := newAnalysisRepoForTest(t)
	ctx := context.Background()

	run, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9001"))
	if err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	if err := repo.Complete(ctx, run.ID, delivery.AnalysisStatusDone, json.RawMessage(`{"classification":"duplicate"}`), ""); err != nil {
		t.Fatalf("Complete queued run: %v", err)
	}
	stored, found, err := repo.LatestByIssue(ctx, 9001)
	if err != nil || !found {
		t.Fatalf("LatestByIssue: %v (found=%v)", err, found)
	}
	if stored.ID != run.ID || stored.Status != delivery.AnalysisStatusDone {
		t.Fatalf("stored run = %+v", stored)
	}
	if string(stored.Output) != `{"classification": "duplicate"}` && string(stored.Output) != `{"classification":"duplicate"}` {
		t.Fatalf("stored output = %s", stored.Output)
	}

	// A terminal run cannot start running again.
	if err := repo.MarkRunning(ctx, run.ID, 7001); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("MarkRunning(done) error = %v, want delivery.ErrStateConflict", err)
	}
	// Completing a terminal run again is a state conflict, not a silent rewrite.
	if err := repo.Complete(ctx, run.ID, delivery.AnalysisStatusFailed, json.RawMessage(`{}`), "boom"); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("second Complete error = %v, want delivery.ErrStateConflict", err)
	}

	degraded, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9002"))
	if err != nil {
		t.Fatalf("CreateQueued degraded: %v", err)
	}
	if err := repo.MarkRunning(ctx, degraded.ID, 7002); err != nil {
		t.Fatalf("MarkRunning degraded: %v", err)
	}
	if err := repo.Complete(ctx, degraded.ID, delivery.AnalysisStatusDegraded, json.RawMessage(`{}`), "partial triage"); err != nil {
		t.Fatalf("Complete degraded: %v", err)
	}
	storedDegraded, found, err := repo.LatestByIssue(ctx, 9002)
	if err != nil || !found {
		t.Fatalf("LatestByIssue degraded: %v (found=%v)", err, found)
	}
	if storedDegraded.Status != delivery.AnalysisStatusDegraded || storedDegraded.Error != "partial triage" {
		t.Fatalf("degraded run = %+v", storedDegraded)
	}
}

func TestAnalysisRepoRejectsInvalidInput(t *testing.T) {
	repo, _ := newAnalysisRepoForTest(t)
	ctx := context.Background()

	invalidOutput := analysisRepoQueuedRun(t, "9001")
	invalidOutput.InputRef = json.RawMessage(`{`)
	if _, err := repo.CreateQueued(ctx, invalidOutput); !errors.Is(err, delivery.ErrInvalidArgument) {
		t.Fatalf("CreateQueued(malformed input) error = %v, want delivery.ErrInvalidArgument", err)
	}
	invalidStatus := analysisRepoQueuedRun(t, "9002")
	invalidStatus.Status = delivery.AnalysisStatusDone
	if _, err := repo.CreateQueued(ctx, invalidStatus); !errors.Is(err, delivery.ErrInvalidArgument) {
		t.Fatalf("CreateQueued(done status) error = %v, want delivery.ErrInvalidArgument", err)
	}

	run, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9003"))
	if err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}
	if err := repo.Complete(ctx, run.ID, delivery.AnalysisStatusRunning, json.RawMessage(`{}`), ""); !errors.Is(err, delivery.ErrInvalidArgument) {
		t.Fatalf("Complete(running) error = %v, want delivery.ErrInvalidArgument", err)
	}
	if err := repo.Complete(ctx, run.ID, delivery.AnalysisStatusDone, json.RawMessage(`[1]`), ""); !errors.Is(err, delivery.ErrInvalidArgument) {
		t.Fatalf("Complete(array output) error = %v, want delivery.ErrInvalidArgument", err)
	}
	if err := repo.MarkRunning(ctx, 999999, 7009); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("MarkRunning(unknown) error = %v, want not found", err)
	}
}

func TestAnalysisRepoLatestByIssue(t *testing.T) {
	repo, _ := newAnalysisRepoForTest(t)
	ctx := context.Background()

	first, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9001"))
	if err != nil {
		t.Fatalf("CreateQueued first: %v", err)
	}
	other, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9002"))
	if err != nil {
		t.Fatalf("CreateQueued other: %v", err)
	}
	// The API detail projection takes the latest done/degraded run, never an
	// in-flight queued/running run (spec §5.3).
	if latest, found, err := repo.LatestByIssue(ctx, 9001); err != nil || found {
		t.Fatalf("LatestByIssue(in-flight) = %+v, found=%v, err=%v, want false/nil", latest, found, err)
	}
	if _, found, err := repo.LatestByIssue(ctx, 9999); err != nil || found {
		t.Fatalf("LatestByIssue(unknown) = found=%v err=%v, want false/nil", found, err)
	}

	if err := repo.Complete(ctx, first.ID, delivery.AnalysisStatusDone, json.RawMessage(`{}`), ""); err != nil {
		t.Fatalf("Complete first: %v", err)
	}
	latest, found, err := repo.LatestByIssue(ctx, 9001)
	if err != nil || !found || latest.ID != first.ID {
		t.Fatalf("LatestByIssue after done = %+v, %v, %v (first=%d, other=%d)", latest, found, err, first.ID, other.ID)
	}

	// A newer in-flight run is not projected until it finishes.
	newer, err := repo.CreateQueued(ctx, analysisRepoQueuedRun(t, "9001"))
	if err != nil {
		t.Fatalf("CreateQueued newer: %v", err)
	}
	latest, found, err = repo.LatestByIssue(ctx, 9001)
	if err != nil || !found || latest.ID != first.ID {
		t.Fatalf("LatestByIssue with newer in-flight run = %+v, %v, %v, want first %d", latest, found, err, first.ID)
	}
	if err := repo.Complete(ctx, newer.ID, delivery.AnalysisStatusDegraded, json.RawMessage(`{}`), "partial"); err != nil {
		t.Fatalf("Complete newer: %v", err)
	}
	latest, found, err = repo.LatestByIssue(ctx, 9001)
	if err != nil || !found || latest.ID != newer.ID || latest.Status != delivery.AnalysisStatusDegraded {
		t.Fatalf("LatestByIssue after newer done = %+v, %v, %v, want %d", latest, found, err, newer.ID)
	}
}
