package main

// v0.22 W0 stage/runtime composition (Task 36).
//
// This file is the composition-root adapter layer for the v0.22 stage service:
// it projects the version stage's narrow StageGates/StageWave*/StageReports/
// StageVersionCloser/StageHandoffGenerator ports onto the requirement, design,
// execution, delivery and version use cases, and exposes the production
// wave-terminal/report-published callbacks the scheduler, pause service and
// report service invoke.
//
// Wiring rule (R-B4-1): the StageService is the only v0.22 stage writer for the
// production callers assembled here. The legacy HTTP handler surface still
// receives the existing *StageMachine because server.Deps.Stage is concretely
// typed (server.go is Task 37-owned); that is the one remaining legacy writer
// documented in the Task 36 report.

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

/* ----------------------------- callback relay ---------------------------- */

// stageCallbackSink is the narrow callback surface of *versionapp.StageService
// used by the relay. An interface (not the concrete service) keeps the relay
// unit-testable before the service is constructed.
type stageCallbackSink interface {
	OnWaveTerminal(ctx context.Context, waveID int64) error
	OnReportPublished(ctx context.Context, versionID int64) error
	Reconcile(ctx context.Context, versionID int64) error
}

// stageCallbackRelay forwards the execution/report callbacks to the stage
// service once it has been assembled. The scheduler, pause service and report
// service are constructed before the stage service (the stage service reads the
// report service), so they receive the relay and the composition root binds the
// real sink right after construction.
type stageCallbackRelay struct {
	mu   sync.RWMutex
	sink stageCallbackSink
}

func newStageCallbackRelay() *stageCallbackRelay { return &stageCallbackRelay{} }

// set binds the concrete stage service. It is safe to call once during wiring.
func (r *stageCallbackRelay) set(sink stageCallbackSink) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sink = sink
}

func (r *stageCallbackRelay) current() stageCallbackSink {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sink
}

// OnWaveTerminal implements executionapp.WaveTerminalStageNotifier.
func (r *stageCallbackRelay) OnWaveTerminal(ctx context.Context, waveID int64) error {
	sink := r.current()
	if sink == nil {
		return nil
	}
	return sink.OnWaveTerminal(ctx, waveID)
}

// OnReportPublished implements deliveryapp.ReportPublishedStageNotifier.
func (r *stageCallbackRelay) OnReportPublished(ctx context.Context, versionID int64) error {
	sink := r.current()
	if sink == nil {
		return nil
	}
	return sink.OnReportPublished(ctx, versionID)
}

// Reconcile implements executionapp.StageReconciler.
func (r *stageCallbackRelay) Reconcile(ctx context.Context, versionID int64) error {
	sink := r.current()
	if sink == nil {
		return nil
	}
	return sink.Reconcile(ctx, versionID)
}

/* ------------------------------- wave ports ------------------------------- */

// stageWaveRepository is the execution wave read surface the stage ports need.
// *executionpg.WaveRepository (through executionapp.WaveRepository) satisfies it.
type stageWaveRepository interface {
	Get(ctx context.Context, id int64) (execution.RunWave, error)
	List(ctx context.Context, versionID int64, limit int, beforeID *int64) ([]execution.RunWave, bool, error)
}

// stageWaveReader adapts the execution wave repository to the stage terminal
// callback projection (R-B4-3).
type stageWaveReader struct {
	waves stageWaveRepository
}

func (r stageWaveReader) GetWave(ctx context.Context, waveID int64) (versionapp.StageWave, error) {
	if r.waves == nil {
		return versionapp.StageWave{}, errors.New("stage wave reader: wave repository is not configured")
	}
	wave, err := r.waves.Get(ctx, waveID)
	if err != nil {
		return versionapp.StageWave{}, err
	}
	return versionapp.StageWave{
		ID:        wave.ID,
		VersionID: wave.VersionID,
		Status:    string(wave.Status),
		Terminal:  wave.Status.Terminal(),
	}, nil
}

// stageWavePauser is the narrow execution pause surface. *executionapp.
// PauseService satisfies it.
type stageWavePauser interface {
	PauseWave(ctx context.Context, versionID, waveID, actorID int64, mode, reason string) (execution.RunWave, error)
	ResumeWave(ctx context.Context, versionID, waveID, actorID int64) (execution.RunWave, error)
}

// stageWaveControl adapts the execution pause service to the version stage's
// PauseVersionWaves/ResumeVersionWaves port (R-B4-5 minimal paused support).
type stageWaveControl struct {
	waves stageWaveRepository
	pause stageWavePauser
}

func (c stageWaveControl) PauseVersionWaves(ctx context.Context, versionID int64, reason string, actorID int64) error {
	if c.waves == nil || c.pause == nil {
		return errors.New("stage wave control: execution pause service is not configured")
	}
	waves, _, err := c.waves.List(ctx, versionID, 100, nil)
	if err != nil {
		return err
	}
	var failures []error
	for _, wave := range waves {
		if wave.Status != execution.WaveRunning {
			continue
		}
		if _, err := c.pause.PauseWave(ctx, versionID, wave.ID, actorID, execution.PauseModeGraceful, reason); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (c stageWaveControl) ResumeVersionWaves(ctx context.Context, versionID int64) error {
	if c.waves == nil || c.pause == nil {
		return errors.New("stage wave control: execution pause service is not configured")
	}
	waves, _, err := c.waves.List(ctx, versionID, 100, nil)
	if err != nil {
		return err
	}
	var failures []error
	for _, wave := range waves {
		if wave.Status != execution.WavePaused {
			continue
		}
		if _, err := c.pause.ResumeWave(ctx, versionID, wave.ID, 0); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

/* ------------------------------ report/closer ----------------------------- */

// stageReportEnsurer is the delivery report surface behind the report gate.
// *deliveryapp.ReportService satisfies it.
type stageReportEnsurer interface {
	EnsureVersionReport(ctx context.Context, versionID int64) (delivery.Report, error)
}

type stageReports struct {
	reports stageReportEnsurer
}

func (r stageReports) EnsureVersionReport(ctx context.Context, versionID int64) error {
	if r.reports == nil {
		return errors.New("stage reports: report service is not configured")
	}
	_, err := r.reports.EnsureVersionReport(ctx, versionID)
	return err
}

// stageVersionCloseService is the version close surface behind the closer port.
// *versionapp.VersionCloseService satisfies it.
type stageVersionCloseService interface {
	Close(ctx context.Context, versionID, runID int64, reason string) (versiondomain.Version, error)
}

// stageVersionReadModel reads the version status to translate the closer's
// state conflict into the typed already-closed sentinel (Task 25 R-B4-3).
type stageVersionReadModel interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// stageVersionCloser adapts the version close service to the stage's "ensure
// closed" contract: an already-completed row is success, everything else keeps
// its gate failure.
type stageVersionCloser struct {
	closer   stageVersionCloseService
	versions stageVersionReadModel
}

func (c stageVersionCloser) CloseVersion(ctx context.Context, versionID int64, reason string) error {
	if c.closer == nil {
		return errors.New("stage closer: version close service is not configured")
	}
	if _, err := c.closer.Close(ctx, versionID, 0, reason); err != nil {
		if c.versions != nil {
			if view, readErr := c.versions.Get(ctx, versionID); readErr == nil && view.Status == versiondomain.StatusCompleted {
				return versionapp.ErrVersionAlreadyClosed
			}
		}
		return err
	}
	return nil
}

/* --------------------------------- gates --------------------------------- */

// v022StageGateDeps are the services the production stage gates read. They are
// the existing use cases: the gate code only projects their results into the
// version stage's function ports.
type v022StageGateDeps struct {
	Versions     stageVersionReadModel
	Requirements *versionapp.RequirementService
	Design       *designapp.GraphService
	Planner      *designapp.Planner
	Executors    *executionapp.Registry
	Waves        stageWaveRepository
	Reports      stageReportEnsurer
	Issues       *deliveryapp.IssueService
	Questions    *executionapp.QuestionService
	Handoff      *versionapp.HandoffService
	Closer       *versionapp.VersionCloseService
}

// newV022StageGates builds every R-B4-2 gate from the existing use cases. A nil
// dependency leaves its gate unconfigured (the stage service fails closed with a
// typed gate_not_configured instead of silently skipping the check).
func newV022StageGates(deps v022StageGateDeps) versionapp.StageGates {
	gates := versionapp.StageGates{}

	if deps.Versions != nil {
		gates.EnvironmentBound = func(ctx context.Context, versionID int64) (bool, error) {
			view, err := deps.Versions.Get(ctx, versionID)
			if err != nil {
				return false, err
			}
			for _, binding := range view.Bindings {
				if strings.TrimSpace(binding.Environment.ID) != "" {
					return true, nil
				}
			}
			return false, nil
		}
		gates.RequirementCount = func(ctx context.Context, versionID int64) (int, error) {
			view, err := deps.Versions.Get(ctx, versionID)
			if err != nil {
				return 0, err
			}
			count := view.RequirementCounts.Total - view.RequirementCounts.Obsolete
			if count < 0 {
				count = 0
			}
			return count, nil
		}
		gates.RequirementsConfirmed = func(ctx context.Context, versionID int64) (bool, error) {
			view, err := deps.Versions.Get(ctx, versionID)
			if err != nil {
				return false, err
			}
			counts := view.RequirementCounts
			verified := counts.Clarified + counts.Closed
			return counts.Draft == 0 && verified > 0, nil
		}
		gates.AccountsValid = func(ctx context.Context, versionID int64) (bool, error) {
			view, err := deps.Versions.Get(ctx, versionID)
			if err != nil {
				return false, err
			}
			if len(view.Bindings) == 0 {
				return false, nil
			}
			for _, binding := range view.Bindings {
				if strings.TrimSpace(binding.Environment.ID) == "" {
					return false, nil
				}
				// A binding that lists target databases needs at least one
				// account to reach them; a UI-only binding is valid without an
				// account.
				if len(binding.Databases) > 0 && len(binding.Accounts) == 0 {
					return false, nil
				}
			}
			return true, nil
		}
	}

	if deps.Requirements != nil {
		gates.OpenGaps = func(ctx context.Context, versionID int64) (int, error) {
			open := 0
			if deps.Questions != nil {
				pending, _, err := deps.Questions.List(ctx, versionID, string(execution.HumanPending), 100, nil)
				if err != nil {
					return 0, err
				}
				open += len(pending)
			}
			if deps.Handoff != nil {
				latest, err := deps.Handoff.Latest(ctx, versionID, 0)
				if err != nil && !errors.Is(err, versionapp.ErrHandoffNotFound) {
					return 0, err
				}
				if err == nil {
					open += len(latest.Open)
				}
			}
			return open, nil
		}
	}

	if deps.Planner != nil {
		gates.ConfirmedCaseDrafts = func(ctx context.Context, versionID int64) (int, error) {
			counts, err := deps.Planner.CaseCounts(ctx, versionID)
			if err != nil {
				return 0, err
			}
			return counts.Draft + counts.Confirmed, nil
		}
		gates.ConfirmedCases = func(ctx context.Context, versionID int64) (int, error) {
			counts, err := deps.Planner.CaseCounts(ctx, versionID)
			if err != nil {
				return 0, err
			}
			return counts.Confirmed, nil
		}
	}

	if deps.Design != nil {
		gates.CoverageComputed = func(ctx context.Context, versionID int64) (bool, error) {
			graph, err := deps.Design.ReadGraph(ctx, versionID)
			if err != nil {
				return false, err
			}
			considered := 0
			for _, requirement := range graph.Requirements {
				if !strings.EqualFold(strings.TrimSpace(requirement.Status), "clarified") {
					continue
				}
				considered++
				covered := false
				for _, scenario := range requirement.Scenarios {
					for _, testCase := range scenario.Cases {
						switch strings.ToLower(strings.TrimSpace(testCase.Status)) {
						case "draft", "confirmed":
							covered = true
						}
					}
				}
				if !covered {
					return false, nil
				}
			}
			return considered > 0, nil
		}
	}

	if deps.Executors != nil {
		gates.ExecutorOnline = func(ctx context.Context) (bool, error) {
			instances, err := deps.Executors.List(ctx)
			if err != nil {
				return false, err
			}
			for _, instance := range instances {
				if instance.Status.Available() {
					return true, nil
				}
			}
			return false, nil
		}
	}

	if deps.Waves != nil {
		gates.WavesTerminal = func(ctx context.Context, versionID int64) (bool, error) {
			waves, _, err := deps.Waves.List(ctx, versionID, 100, nil)
			if err != nil {
				return false, err
			}
			if len(waves) == 0 {
				return false, nil
			}
			for _, wave := range waves {
				if !wave.Status.Terminal() {
					return false, nil
				}
			}
			return true, nil
		}
	}

	if deps.Closer != nil {
		gates.ReportPublished = func(ctx context.Context, versionID int64) (bool, error) {
			predicate, err := deps.Closer.Evaluate(ctx, versionID)
			if err != nil {
				return false, err
			}
			return predicate.VersionReportPublished, nil
		}
	}

	if deps.Issues != nil {
		gates.OpenBlockingIssues = func(ctx context.Context, versionID int64) (int, error) {
			issues, err := deps.Issues.List(ctx, versionID, deliveryapp.IssueFilter{Status: string(delivery.IssueStatusConfirmed), Limit: 100})
			if err != nil {
				return 0, err
			}
			return len(issues), nil
		}
	}

	if deps.Waves != nil || deps.Executors != nil {
		gates.BlockerCleared = func(ctx context.Context, versionID int64, blocker versionapp.Blocker) (bool, error) {
			switch blocker.Kind {
			case "executor_offline":
				if gates.ExecutorOnline == nil {
					return false, nil
				}
				return gates.ExecutorOnline(ctx)
			case "account", "target_database":
				if gates.AccountsValid == nil {
					return false, nil
				}
				return gates.AccountsValid(ctx, versionID)
			case "wave":
				if gates.WavesTerminal == nil {
					return false, nil
				}
				return gates.WavesTerminal(ctx, versionID)
			default:
				// manual/legacy blockers require an explicit clearance: no open
				// pending question, gap or confirmed blocking issue remains.
				if gates.OpenGaps != nil {
					open, err := gates.OpenGaps(ctx, versionID)
					if err != nil || open > 0 {
						return false, err
					}
				}
				if gates.OpenBlockingIssues != nil {
					open, err := gates.OpenBlockingIssues(ctx, versionID)
					if err != nil || open > 0 {
						return false, err
					}
				}
				return true, nil
			}
		}
	}

	return gates
}

/* ------------------------- legacy writer redirects ------------------------ */

// stageAdvancer routes a legacy automatic forward edge through the v0.22
// StageService (R-B4-1). It satisfies the clarificationStarter,
// RequirementStageConverger and DesignConverger narrow interfaces without
// keeping a *StageMachine reference in the production wiring.
type stageAdvancer struct {
	service *versionapp.StageService
}

// StartClarification applies created -> clarifying through the stage service.
func (a stageAdvancer) StartClarification(ctx context.Context, versionID int64) error {
	if a.service == nil {
		return errors.New("stage advancer: stage service is not configured")
	}
	return a.service.Request(ctx, versionID, versiondomain.StageClarifying)
}

// ConvergeClarification applies clarifying -> requirement_review.
func (a stageAdvancer) ConvergeClarification(ctx context.Context, versionID int64) error {
	if a.service == nil {
		return errors.New("stage advancer: stage service is not configured")
	}
	return a.service.Request(ctx, versionID, versiondomain.StageRequirementReview)
}

// ConvergeDesign applies designing -> case_review.
func (a stageAdvancer) ConvergeDesign(ctx context.Context, versionID int64) error {
	if a.service == nil {
		return errors.New("stage advancer: stage service is not configured")
	}
	return a.service.Request(ctx, versionID, versiondomain.StageCaseReview)
}

/* --------------------------- periodic reconciler ------------------------- */

// stageReconcileVersions is the version list surface of the periodic stage
// reconciler. *versionapp.Service satisfies it.
type stageReconcileVersions interface {
	List(ctx context.Context, limit int, beforeID *int64) ([]versionapp.VersionSummary, bool, error)
}

// stageReconciler is the retry hook of the stage service (Task 25 §7.2).
type stageReconciler interface {
	Reconcile(ctx context.Context, versionID int64) error
}

// stageStateReader reads the persisted stage to select executing/reporting
// versions for the periodic reconcile.
type stageStateReader interface {
	GetStage(ctx context.Context, versionID int64) (versiondomain.StageState, error)
}

// runStageReconciler re-drives the production terminal callbacks for the
// versions whose best-effort scheduler/report callback was lost (process
// restart, transient report failure). It is leader-gated and idempotent.
func runStageReconciler(
	ctx context.Context,
	gate interface{ IsLeader() bool },
	versions stageReconcileVersions,
	stages stageStateReader,
	service stageReconciler,
	interval time.Duration,
) {
	if versions == nil || stages == nil || service == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	reconcile := func() {
		if gate != nil && !gate.IsLeader() {
			return
		}
		summaries, _, err := versions.List(ctx, 100, nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("eztest: stage reconciler: list versions: %v", err)
			}
			return
		}
		for _, summary := range summaries {
			state, err := stages.GetStage(ctx, summary.ID)
			if err != nil {
				if !errors.Is(err, versiondomain.ErrNotFound) && !errors.Is(err, context.Canceled) {
					log.Printf("eztest: stage reconciler: stage version=%d: %v", summary.ID, err)
				}
				continue
			}
			switch state.Stage {
			case versiondomain.StageExecuting, versiondomain.StageReporting:
			default:
				continue
			}
			if err := service.Reconcile(ctx, summary.ID); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("eztest: stage reconciler: reconcile version=%d: %v", summary.ID, err)
			}
		}
	}
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

/* ------------------------------ idempotence ------------------------------ */

// stageCompletionReason is the reason recorded by the closer on the
// reporting -> completed edge.
const stageCompletionReason = "stage_completed"

/* ------------------------ attachment extraction worker ------------------- */

// pendingAttachmentExtractor is the deterministic attachment pipeline surface
// (Task 19/20): pending rows are extracted by the server, never by enqueuing a
// legacy attachment.ingest agent work item (removed by v0.22 D-B1/R-W1-5).
type pendingAttachmentExtractor interface {
	ListPendingForIngest(ctx context.Context, limit int) ([]versionapp.PendingAttachment, error)
	Extract(ctx context.Context, attachmentID int64) (versionapp.ExtractionResult, error)
}

// runAttachmentExtractionWorker drives the deterministic attachment pipeline for
// pending rows. It is leader-gated, idempotent and bounded; parse failures are
// persisted on the attachment row and never abort the loop.
func runAttachmentExtractionWorker(
	ctx context.Context,
	gate interface{ IsLeader() bool },
	service pendingAttachmentExtractor,
	interval time.Duration,
) {
	if service == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	tick := func() {
		if gate != nil && !gate.IsLeader() {
			return
		}
		pending, err := service.ListPendingForIngest(ctx, 10)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("eztest: attachment extraction worker: list pending: %v", err)
			}
			return
		}
		for _, attachment := range pending {
			if _, err := service.Extract(ctx, attachment.ID); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("eztest: attachment extraction worker: attachment=%d: %v", attachment.ID, err)
			}
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
