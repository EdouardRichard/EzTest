package deliveryapp

// v0.22 Task 25 (R-B4-3): publishing a report is the production trigger for
// reporting -> completed. ReportService exposes the narrow callback port and the
// second test wires the real *versionapp.StageService behind it, proving the
// version closes without a manual stage API call.

import (
	"context"
	"sync"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type reportStageNotifier struct {
	mu       sync.Mutex
	versions []int64
	err      error
}

func (n *reportStageNotifier) OnReportPublished(_ context.Context, versionID int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.versions = append(n.versions, versionID)
	return n.err
}

func (n *reportStageNotifier) calls() []int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]int64(nil), n.versions...)
}

type reportStageRepo struct {
	mu      sync.Mutex
	state   version.StageState
	applied []version.StageState
}

func (r *reportStageRepo) GetStage(context.Context, int64) (version.StageState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, nil
}

func (r *reportStageRepo) ApplyStage(_ context.Context, expected version.Stage, next version.StageState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Stage != expected {
		return version.ErrStageConflict
	}
	r.state = next
	r.applied = append(r.applied, next)
	return nil
}

func (r *reportStageRepo) currentStage() version.Stage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Stage
}

func (r *reportStageRepo) appliedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.applied)
}

type reportStageCloser struct {
	mu       sync.Mutex
	versions []int64
}

func (c *reportStageCloser) CloseVersion(_ context.Context, versionID int64, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.versions = append(c.versions, versionID)
	return nil
}

func (c *reportStageCloser) calls() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.versions...)
}

// TestReportServiceNotifiesStageOnPublish pins the production call site: a
// successful publish notifies the stage callback with the version ID, and a
// failed render never does.
func TestReportServiceNotifiesStageOnPublish(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)
	notifier := &reportStageNotifier{}
	fixture.svc.WithReportPublishedNotifier(notifier)

	if _, err := fixture.svc.Publish(context.Background(), report.ID, 1, "叙述"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if calls := notifier.calls(); len(calls) != 1 || calls[0] != 7 {
		t.Fatalf("stage callbacks = %v, want [7]", calls)
	}

	// Re-publishing an already-published revision is idempotent for the report
	// but still re-fires the callback so a lost stage advance can heal; the
	// stage service itself is idempotent.
	if _, err := fixture.svc.Publish(context.Background(), report.ID, 1, "叙述"); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if calls := notifier.calls(); len(calls) != 2 || calls[1] != 7 {
		t.Fatalf("stage callbacks after republish = %v", calls)
	}

	// A failed render never publishes and never notifies.
	failed := newReportFixture(t)
	failedReport, _ := failed.seedWaveReport(7, 12)
	failedNotifier := &reportStageNotifier{}
	failed.svc.WithReportPublishedNotifier(failedNotifier)
	failed.renderer.err = context.Canceled
	if _, err := failed.svc.Publish(context.Background(), failedReport.ID, 1, ""); err == nil {
		t.Fatal("failed render published")
	}
	if calls := failedNotifier.calls(); len(calls) != 0 {
		t.Fatalf("failed publish notified the stage: %v", calls)
	}
}

// TestReportPublishedCallbackCompletesStage wires the real StageService: the
// report.published callback completes a reporting version and closes it.
func TestReportPublishedCallbackCompletesStage(t *testing.T) {
	fixture := newReportFixture(t)
	report, _ := fixture.seedWaveReport(7, 12)

	repo := &reportStageRepo{state: version.StageState{
		VersionID:      7,
		Stage:          version.StageReporting,
		StageEnteredAt: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC),
	}}
	closer := &reportStageCloser{}
	stageService := versionapp.NewStageService(versionapp.StageServiceDeps{
		Repo: repo,
		Gates: versionapp.StageGates{
			ReportPublished:    func(context.Context, int64) (bool, error) { return true, nil },
			OpenBlockingIssues: func(context.Context, int64) (int, error) { return 0, nil },
		},
		Closer: closer,
	})
	fixture.svc.WithReportPublishedNotifier(stageService)

	published, err := fixture.svc.Publish(context.Background(), report.ID, 1, "叙述")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if published.Status != delivery.ReportStatusPublished {
		t.Fatalf("revision status = %q, want published", published.Status)
	}
	if stage := repo.currentStage(); stage != version.StageCompleted {
		t.Fatalf("stage = %q, want completed", stage)
	}
	if calls := closer.calls(); len(calls) != 1 || calls[0] != 7 {
		t.Fatalf("version close calls = %v, want [7]", calls)
	}
	applied := repo.appliedCount()

	// The callback is idempotent: re-publishing cannot complete the version
	// twice.
	if _, err := fixture.svc.Publish(context.Background(), report.ID, 1, "叙述"); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if stage := repo.currentStage(); stage != version.StageCompleted {
		t.Fatalf("stage after republish = %q, want completed", stage)
	}
	if repo.appliedCount() != applied {
		t.Fatalf("duplicate completion wrote stage: applied=%d want=%d", repo.appliedCount(), applied)
	}
	if calls := closer.calls(); len(calls) != 1 {
		t.Fatalf("version closed %d times, want once", len(calls))
	}
}
