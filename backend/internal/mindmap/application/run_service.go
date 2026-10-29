package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Run coordinator constants of spec §6.6/§8.3/§8.4.
const (
	// runWorkerInterval is the fixed poll interval of the in-process worker
	// (spec §8.4: 2s).
	runWorkerInterval = 2 * time.Second
	// runWorkerBatch is the maximum number of active runs processed per tick.
	runWorkerBatch = 5
	// runQueuedStartTimeout fails a run that never left queued (spec §8.4).
	runQueuedStartTimeout = 60 * time.Second
	// runAgentTimeoutGrace is added to the profile HardTimeout (spec §8.4).
	runAgentTimeoutGrace = 60 * time.Second
	// runRecoveryWindow is the startup-recovery cutoff (spec §6.6: 10min).
	runRecoveryWindow = 10 * time.Minute
	// runMaterializeScanLimit bounds the active-run scan Materialize uses to
	// resolve a run id. The public port has no get-by-id lookup, so a run that
	// is no longer active is already terminal and materialization is a no-op.
	runMaterializeScanLimit = 200

	// runMaxInstructionBytes is the run-context budget of spec §8.3.
	runMaxInstructionBytes = 64 * 1024
	// runMaxInstructionRequirements bounds the injected confirmed requirements.
	runMaxInstructionRequirements = 20
	// runRequirementExcerptBytes bounds one requirement's excerpt.
	runRequirementExcerptBytes = 8 * 1024
	// runMaxInstructionAppModels bounds the injected app-model summary.
	runMaxInstructionAppModels = 200
	// runMaxInstructionFacts bounds the injected code-fact summary.
	runMaxInstructionFacts = 100
	// runMaxInstructionRelations bounds the injected requirement relations.
	runMaxInstructionRelations = 200
	// runMaxSummaryRunes bounds an event summary (spec §6.11: ≤500 runes).
	runMaxSummaryRunes = 500
)

// RunDeps are the ports of the asynchronous run coordinator: the run store, the
// graph transaction runner plus its read repositories and the baseline store
// (the frozen-write guard), the version lifecycle, the requirement reader, the
// resource/code-intel gateways, the agent runtime, the event sink, the id
// generator and the clock.
type RunDeps struct {
	Runs       RunRepository
	Graph      GraphRepository
	Baselines  BaselineRepository
	Nodes      NodeRepository
	Steps      StepRepository
	Assertions AssertionRepository
	Sources    SourceRepository
	Versions   VersionLifecycle
	// Batches is the optional P7 execution-batch reader: when wired, the
	// supplement/incremental_design parent-batch preconditions of spec §7.6 are
	// enforced at start; when nil the run starts degraded with the
	// batch_precondition_unavailable warning instead of bypassing the check.
	Batches      SupplementBatchReader
	Requirements RequirementReader
	// Knowledge is the optional approved version-knowledge read: when wired, a
	// design run appends the "版本知识" segment to its instruction and records
	// the injected ids as stats.knowledgeRefs (spec §9.4). Review/data_plan runs
	// never read it.
	Knowledge KnowledgeReader
	Catalog   CatalogGateway
	CodeFacts CodeFactsGateway
	// Impact is the optional P4 impact summary read of the incremental_design
	// run starter. When wired, the starter seeds the run.result impact signal so
	// the plan can consume the P4 result; without it the run starts with
	// impactAvailable=false and the impact_unavailable warning (spec §6.1 step
	// 3b/§7.6).
	Impact  P4ImpactReader
	Anchors CodeAnchorGateway
	Agents  AgentRuntime
	Events  EventPublisher
	IDs     IDGenerator
	Clock   Clock
	// MaxSupplementCases is the per-run cap of newly materialized supplement
	// cases (EZTEST_SUPPLEMENT_MAX_CASES, design spec §10.5/§11.4). Zero keeps
	// the spec default 5.
	MaxSupplementCases int
}

// StartRunCommand is the POST /runs body of spec §7.2 endpoint 17 extended for
// the P7 kinds (spec §7.6): design/review/data_plan plus supplement and
// incremental_design, whose parent batch id is mandatory.
type StartRunCommand struct {
	Kind           string  `json:"kind"`
	Mode           string  `json:"mode,omitempty"`
	ConfirmReplace bool    `json:"confirmReplace,omitempty"`
	NodeIDs        []int64 `json:"nodeIds,omitempty"`
	// BatchID is the parent batch of a supplement/incremental_design run (spec
	// §7.6). It is required for those kinds and ignored otherwise.
	BatchID int64 `json:"batchId,omitempty"`
}

// RunService coordinates the asynchronous design, review and data-plan agent
// runs (spec §6.6/§6.8/§8.4) and materializes their structured submissions. It
// never writes the version table directly: the review transition goes through
// VersionLifecycle.
type RunService struct {
	deps RunDeps

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	wg      sync.WaitGroup
}

// NewRunService wires the run coordinator to its ports.
func NewRunService(deps RunDeps) *RunService {
	if deps.MaxSupplementCases <= 0 {
		deps.MaxSupplementCases = supplementMaxCases
	}
	return &RunService{deps: deps}
}

// maxSupplementCases returns the configured per-run supplement case budget.
func (s *RunService) maxSupplementCases() int {
	if s.deps.MaxSupplementCases > 0 {
		return s.deps.MaxSupplementCases
	}
	return supplementMaxCases
}

// runProfile returns the profile key and prompt version of one coordinated run
// kind. An empty key means the kind is not coordinated here.
func runProfile(kind string) (profileKey, promptVersion string) {
	switch kind {
	case domain.RunKindDesign:
		return domain.ProfileKeyCaseDesign, domain.PromptVersionDesign
	case domain.RunKindReview:
		return domain.ProfileKeyCaseReview, domain.PromptVersionReview
	case domain.RunKindDataPlan:
		return domain.ProfileKeyCaseDataPlan, domain.PromptVersionDataPlan
	case RunKindSupplement, RunKindIncrementalDesign:
		// Both P7 kinds run through the supplement-agent profile (spec §9.1/§9.2).
		return ProfileKeySupplement, PromptVersionSupplement
	default:
		return "", ""
	}
}

// runHardTimeout is the profile HardTimeout of one kind (spec §8.1/§9.1).
func runHardTimeout(kind string) time.Duration {
	switch kind {
	case domain.RunKindDesign, domain.RunKindReview:
		return 90 * time.Second
	case domain.RunKindDataPlan, domain.RunKindExplore:
		return 60 * time.Second
	case RunKindSupplement, RunKindIncrementalDesign:
		return 120 * time.Second
	default:
		return 90 * time.Second
	}
}

// runStartAllowed reports whether a run of kind may start at the version status.
// Review runs are read-only, so they stay allowed after the baseline froze the
// version to executing (spec §6.4 rule 3); design/data_plan write the graph and
// need an editable version. The P7 supplement run runs after a terminal baseline
// batch (executing); the incremental_design run runs while the version continues
// (continuing; the continue_phase=designing check is separate).
func runStartAllowed(kind, status string) bool {
	switch kind {
	case domain.RunKindReview:
		return status == versionStatusDesigning || status == versionStatusReviewing || status == "executing"
	case RunKindSupplement:
		return status == versionStatusExecuting
	case RunKindIncrementalDesign:
		return status == versionStatusContinuing
	default:
		return status == versionStatusDesigning || status == versionStatusReviewing
	}
}

// runAllowedStates is the human-readable status list of runStartAllowed.
func runAllowedStates(kind string) string {
	switch kind {
	case domain.RunKindReview:
		return "designing, reviewing or executing"
	case RunKindSupplement:
		return "executing"
	case RunKindIncrementalDesign:
		return "continuing (designing phase)"
	default:
		return "designing or reviewing"
	}
}

// isSupplementRunKind reports whether kind is one of the P7 supplement kinds.
func isSupplementRunKind(kind string) bool {
	return kind == RunKindSupplement || kind == RunKindIncrementalDesign
}

// runNormalizeCommand applies the kind-specific defaults of the P7 runs: the
// post_batch / incremental_design mode recorded in the run input and instruction
// (spec §6.4.1).
func runNormalizeCommand(cmd StartRunCommand) StartRunCommand {
	if strings.TrimSpace(cmd.Mode) != "" {
		return cmd
	}
	switch cmd.Kind {
	case RunKindSupplement:
		cmd.Mode = supplementRunModePost
	case RunKindIncrementalDesign:
		cmd.Mode = supplementRunModeDesign
	}
	return cmd
}

// incrementalDesignSeedResult is the starter-owned run.result document of an
// incremental_design run (spec §6.1 step 3b/§7.6). ImpactAvailable is always
// written; UnmappedChanges stays absent when P4 supplied no result so the plan
// can tell "no P4" from "P4 ran and found nothing" (the empty array case).
type incrementalDesignSeedResult struct {
	ImpactAvailable bool                        `json:"impactAvailable"`
	UnmappedChanges *[]supplementUnmappedChange `json:"unmappedChanges,omitempty"`
	Degraded        bool                        `json:"degraded,omitempty"`
	Warnings        []IncrementalPlanWarning    `json:"warnings,omitempty"`
}

// incrementalDesignResultSeed builds the P4 impact preseed of an
// incremental_design run start. It is the only writer of these keys before the
// agent run; the T14 supplement tools carry them through every result rewrite and
// the materializer keeps them in the terminal run result. An available P4 result
// is seeded with its changes and diagnostics (an unmapped_truncated warning
// keeps impactAvailable=true); every unavailable result keeps the adapter's
// diagnostics and degraded flag and adds the impact_unavailable warning, without
// ever fabricating an unmapped change list (spec §6.1 step 3a/3b, §7.6).
func (s *RunService) incrementalDesignResultSeed(ctx context.Context, versionID int64) json.RawMessage {
	if s.deps.Impact == nil {
		return incrementalDesignUnavailableSeed(nil, false)
	}
	summary, err := s.deps.Impact.LatestImpactSummary(ctx, versionID)
	if err != nil {
		slog.WarnContext(ctx, "mindmap: read the latest P4 impact summary failed; starting the incremental design run without impact",
			"version_id", versionID, "error", err)
		return incrementalDesignUnavailableSeed(nil, false)
	}
	// A degraded analysis still has a result but spec §6.1 step 3a records
	// impact_unavailable for it; never present it as a usable impact result.
	// The adapter's degraded diagnostic travels with the unavailable seed so a
	// done+degraded analysis stays distinguishable from no analysis at all.
	if !summary.Available || summary.Degraded {
		return incrementalDesignUnavailableSeed(summary.Warnings, summary.Degraded)
	}
	changes := make([]supplementUnmappedChange, 0, len(summary.UnmappedChanges))
	for _, change := range summary.UnmappedChanges {
		changes = append(changes, supplementUnmappedChange{Path: change.Path, Summary: change.Summary})
	}
	return encodeIncrementalDesignSeed(incrementalDesignSeedResult{
		ImpactAvailable: true,
		UnmappedChanges: &changes,
		Degraded:        summary.Degraded,
		Warnings:        summary.Warnings,
	})
}

// incrementalDesignUnavailableSeed is the seed of a run whose P4 impact result is
// missing, degraded or unreadable. The adapter diagnostics (if any) are carried
// unchanged, including the degraded flag; the plan's impact_unavailable warning
// is appended exactly once so the plan surfaces the same signal before and after
// materialization.
func incrementalDesignUnavailableSeed(warnings []IncrementalPlanWarning, degraded bool) json.RawMessage {
	merged := make([]IncrementalPlanWarning, 0, len(warnings)+1)
	merged = append(merged, warnings...)
	if !planWarningsContainCode(merged, planWarningImpactUnavailable) {
		merged = append(merged, IncrementalPlanWarning{
			Code:    planWarningImpactUnavailable,
			Message: "no P4 impact result is available for this version",
		})
	}
	return encodeIncrementalDesignSeed(incrementalDesignSeedResult{
		ImpactAvailable: false,
		Degraded:        degraded,
		Warnings:        merged,
	})
}

// encodeIncrementalDesignSeed serializes the seed document. Its fields are only
// strings, bools and slices, so a marshal failure cannot happen in practice; the
// fallback keeps the run startable instead of failing on an impossible error.
func encodeIncrementalDesignSeed(document incrementalDesignSeedResult) json.RawMessage {
	encoded, err := json.Marshal(document)
	if err != nil {
		return []byte(`{"impactAvailable":false}`)
	}
	return encoded
}

// runEncodeStartInput persists the start command of spec §7.6. The parent batch
// id is written as a decimal JSON string (D29) so every new reader sees the P7
// boundary convention; runStoredInput still accepts the legacy number shape.
func runEncodeStartInput(cmd StartRunCommand) ([]byte, error) {
	input := struct {
		Kind           string  `json:"kind"`
		Mode           string  `json:"mode,omitempty"`
		ConfirmReplace bool    `json:"confirmReplace,omitempty"`
		NodeIDs        []int64 `json:"nodeIds,omitempty"`
		BatchID        string  `json:"batchId,omitempty"`
	}{
		Kind:           cmd.Kind,
		Mode:           cmd.Mode,
		ConfirmReplace: cmd.ConfirmReplace,
		NodeIDs:        cmd.NodeIDs,
	}
	if cmd.BatchID > 0 {
		input.BatchID = strconv.FormatInt(cmd.BatchID, 10)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("mindmap: encode run input: %w", err)
	}
	return encoded, nil
}

// validateSupplementParentBatch enforces the parent-batch preconditions of spec
// §5.3/§7.6 for the P7 run kinds when the optional execution-batch reader is
// wired: the parent batch must belong to the version and be its latest terminal
// batch. A nil reader is not a silent bypass: the run starts with the
// batch_precondition_unavailable warning recorded in its stats.
func (s *RunService) validateSupplementParentBatch(ctx context.Context, versionID int64, cmd StartRunCommand) (*IncrementalPlanWarning, error) {
	if !isSupplementRunKind(cmd.Kind) {
		return nil, nil
	}
	if s.deps.Batches == nil {
		return &IncrementalPlanWarning{
			Code:    planWarningBatchUnavailable,
			Message: "the execution batch reader is not configured; the parent batch preconditions were not verified",
		}, nil
	}
	batch, err := s.deps.Batches.GetBatch(ctx, cmd.BatchID)
	if errors.Is(err, ErrSupplementBatchNotFound) {
		return nil, domain.NewInvalid("batchId", "invalid_batch_id",
			fmt.Sprintf("parent batch %d does not exist", cmd.BatchID))
	}
	if err != nil {
		return nil, fmt.Errorf("mindmap: read parent batch %d: %w", cmd.BatchID, err)
	}
	if batch.VersionID != versionID {
		return nil, domain.NewInvalid("batchId", "invalid_batch_id",
			fmt.Sprintf("parent batch %d belongs to version %d, not %d", batch.ID, batch.VersionID, versionID))
	}
	if !supplementBatchTerminal(batch.Status) {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("parent batch %d is %s; a %s run needs a terminal parent batch", batch.ID, batch.Status, cmd.Kind))
	}
	latest, err := s.deps.Batches.LatestTerminalBatch(ctx, versionID)
	if errors.Is(err, ErrSupplementBatchNotFound) {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d has no terminal batch; a %s run needs one", versionID, cmd.Kind))
	}
	if err != nil {
		return nil, fmt.Errorf("mindmap: read the latest terminal batch of version %d: %w", versionID, err)
	}
	if latest.ID != batch.ID {
		return nil, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("parent batch %d is not the latest terminal batch %d of version %d", batch.ID, latest.ID, versionID))
	}
	return nil, nil
}

// Start validates the version state, the confirmed requirements and the
// per-kind single-flight slot, then persists a queued run, creates the agent
// session, starts the instruction and moves the run to running (spec §6.6). A
// failure after the row exists marks the run failed instead of leaking it as
// active and returns the failure.
func (s *RunService) Start(ctx context.Context, versionID, actor int64, cmd StartRunCommand) (domain.CaseDesignRun, error) {
	profileKey, promptVersion := runProfile(cmd.Kind)
	if profileKey == "" {
		return domain.CaseDesignRun{}, domain.NewInvalid("kind", "invalid_run_kind",
			fmt.Sprintf("run kind %q is not design, review, data_plan, supplement or incremental_design", cmd.Kind))
	}
	if isSupplementRunKind(cmd.Kind) && cmd.BatchID <= 0 {
		return domain.CaseDesignRun{}, domain.NewInvalid("batchId", "invalid_batch_id",
			fmt.Sprintf("a %s run requires the parent batch id", cmd.Kind))
	}
	if s.deps.Versions == nil || s.deps.Runs == nil || s.deps.Agents == nil {
		return domain.CaseDesignRun{}, errors.New("mindmap: run coordinator ports are not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return domain.CaseDesignRun{}, err
	}
	if !runStartAllowed(cmd.Kind, version.Status) {
		return domain.CaseDesignRun{}, domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d is %s; mindmap runs of kind %s need %s", versionID, version.Status,
				cmd.Kind, runAllowedStates(cmd.Kind)))
	}
	if cmd.Kind == RunKindIncrementalDesign {
		if err := s.requireDesigningContinuePhase(ctx, versionID); err != nil {
			return domain.CaseDesignRun{}, err
		}
	}
	batchWarning, err := s.validateSupplementParentBatch(ctx, versionID, cmd)
	if err != nil {
		return domain.CaseDesignRun{}, err
	}
	cmd = runNormalizeCommand(cmd)
	requirements, err := s.confirmedRequirements(ctx, versionID)
	if err != nil {
		return domain.CaseDesignRun{}, err
	}
	if len(requirements) == 0 {
		return domain.CaseDesignRun{}, domain.NewInvalid("requirements", "unconfirmed_requirements",
			fmt.Sprintf("version %d has no clarified requirements to design against", versionID))
	}
	if _, err := s.deps.Runs.ActiveByKind(ctx, versionID, cmd.Kind); err == nil {
		return domain.CaseDesignRun{}, runInProgressError(versionID, cmd.Kind)
	} else if !runIsNotFound(err) {
		return domain.CaseDesignRun{}, err
	}

	runID, err := s.nextID()
	if err != nil {
		return domain.CaseDesignRun{}, err
	}
	built := s.buildInstruction(ctx, version, cmd, requirements, runID)
	instruction := built.Text
	input, err := runEncodeStartInput(cmd)
	if err != nil {
		return domain.CaseDesignRun{}, err
	}
	now := s.now()
	run := domain.CaseDesignRun{
		ID:            runID,
		VersionID:     versionID,
		Kind:          cmd.Kind,
		Status:        domain.RunQueued,
		PromptVersion: promptVersion,
		Input:         input,
		CreatedBy:     actor,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if built.Truncated {
		run.Stats = []byte(`{"contextTruncated":true}`)
	}
	warnings := make([]IncrementalPlanWarning, 0, 2)
	if batchWarning != nil {
		warnings = append(warnings, *batchWarning)
	}
	if built.KnowledgeTruncated {
		warnings = append(warnings, IncrementalPlanWarning{
			Code:    planWarningKnowledgeTruncated,
			Message: "the approved version knowledge exceeded the injection budget",
		})
	}
	if len(warnings) > 0 {
		run.Stats = runMergeStats(run.Stats, map[string]any{"warnings": warnings})
	}
	if built.KnowledgeTruncated {
		run.Stats = runMergeStats(run.Stats, map[string]any{"knowledgeTruncated": true})
	}
	if len(built.KnowledgeRefs) > 0 {
		run.Stats = runMergeStats(run.Stats, map[string]any{
			"knowledgeRefs": knowledgeRefIDs(built.KnowledgeRefs),
		})
	}
	if cmd.Kind == RunKindIncrementalDesign {
		// Seed the P4 impact signal before the agent run: it is the only writer
		// of these keys, and T14's tools plus the materializer carry them into
		// the plan (spec §6.1 step 3b/§7.6). The supplement kind is not seeded.
		run.Result = s.incrementalDesignResultSeed(ctx, versionID)
	}
	if isSupplementRunKind(cmd.Kind) {
		// The migration column is the queryable parent-batch link of the P7
		// supplement chain (T14/T16 read it back by batch id).
		batchID := cmd.BatchID
		run.BatchID = &batchID
	}
	run, err = s.deps.Runs.Create(ctx, run)
	if err != nil {
		return run, err
	}

	sessionID, err := s.deps.Agents.CreateSession(ctx, profileKey, runSessionTitle(version, cmd.Kind), actor,
		fmt.Sprintf("version:%d", versionID), strconv.FormatInt(versionID, 10))
	if err != nil {
		return s.abortOrDegradeStart(ctx, run, err)
	}
	agentRunID, err := s.deps.Agents.StartText(ctx, sessionID, instruction, actor)
	if err != nil {
		// The session exists but no run was started on it; abort it so the
		// failed attempt does not leave an active orphan (best-effort).
		_ = s.deps.Agents.Abort(ctx, sessionID, actor)
		return s.abortOrDegradeStart(ctx, run, err)
	}
	startedAt := s.now()
	// Keep the pre-update run: on a CAS failure the adapter returns no row, and
	// the compensation needs the persisted id/status to release the run and the
	// single-flight slot.
	running, updateErr := s.deps.Runs.UpdateStatus(ctx, run.ID, domain.RunQueued, domain.RunRunning, RunPatch{
		AgentSessionID: &sessionID,
		AgentRunID:     &agentRunID,
		StartedAt:      &startedAt,
	})
	if updateErr != nil {
		_ = s.deps.Agents.Abort(ctx, sessionID, actor)
		return s.abortOrDegradeStart(ctx, run, updateErr)
	}
	run = running
	s.publishRunEvent(ctx, run, domain.EventCaseRunStarted, "", nil)
	return run, nil
}

// abortOrDegradeStart compensates a failed agent start. The P7 supplement kinds
// become degraded instead of failed so an unavailable LLM never blocks the
// autonomous chain (spec §9.7); every legacy kind keeps the ai_unavailable
// failure.
func (s *RunService) abortOrDegradeStart(ctx context.Context, run domain.CaseDesignRun, cause error) (domain.CaseDesignRun, error) {
	if !isSupplementRunKind(run.Kind) {
		return run, s.abortStart(ctx, run, cause)
	}
	degraded, err := s.degradeRunStart(ctx, run)
	if err != nil {
		return run, err
	}
	return degraded, nil
}

// degradeRunStart moves a queued supplement run to degraded after the agent
// runtime could not be started (spec §6.4.5/§9.7).
func (s *RunService) degradeRunStart(ctx context.Context, run domain.CaseDesignRun) (domain.CaseDesignRun, error) {
	finished := s.now()
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, domain.RunDegraded, RunPatch{
		Errors:     runFailureErrors("ai_unavailable", nil),
		FinishedAt: &finished,
	})
	if err != nil {
		return run, fmt.Errorf("mindmap: start run %d failed and could not be degraded: %w", run.ID, err)
	}
	s.publishRunEvent(ctx, updated, domain.EventCaseRunFinished, "ai_unavailable", nil)
	return updated, nil
}

// requireDesigningContinuePhase enforces the continue_phase=designing half of the
// incremental_design gate (spec §7.6) when the version adapter exposes the phase
// through the optional P7 ContinuePhaseReader.
func (s *RunService) requireDesigningContinuePhase(ctx context.Context, versionID int64) error {
	reader, ok := s.deps.Versions.(ContinuePhaseReader)
	if !ok {
		return nil
	}
	phase, err := reader.ContinuePhase(ctx, versionID)
	if err != nil {
		return fmt.Errorf("mindmap: read continue phase of version %d: %w", versionID, err)
	}
	if phase != continuePhaseDesigning {
		return domain.NewStateConflict("version_state_conflict",
			fmt.Sprintf("version %d continue phase is %q; incremental_design needs designing", versionID, phase))
	}
	return nil
}

// abortStart compensates a failed agent-session start: the queued run becomes
// failed(ai_unavailable) so the single-flight slot is released, and the agent
// runtime failure is returned to the caller (spec §8.5).
func (s *RunService) abortStart(ctx context.Context, run domain.CaseDesignRun, cause error) error {
	finished := s.now()
	_, updateErr := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, domain.RunFailed, RunPatch{
		Errors:     runFailureErrors("ai_unavailable", nil),
		FinishedAt: &finished,
	})
	if updateErr != nil {
		return fmt.Errorf("mindmap: start run %d failed (%v) and could not be failed: %w", run.ID, cause, updateErr)
	}
	return domain.NewConflict("ai_unavailable",
		fmt.Sprintf("mindmap: the agent runtime could not start run %d: %v", run.ID, cause))
}

// confirmedRequirements loads the clarified requirements of a version. The
// requirement reader is mandatory for a run: its absence is a wiring error.
func (s *RunService) confirmedRequirements(ctx context.Context, versionID int64) ([]RequirementSnapshot, error) {
	if s.deps.Requirements == nil {
		return nil, errors.New("mindmap: requirement reader is not configured")
	}
	return s.deps.Requirements.ListConfirmed(ctx, versionID)
}

// runSessionTitle builds the agent-session title shown in the P1 session list.
func runSessionTitle(version VersionSnapshot, kind string) string {
	return fmt.Sprintf("%s · %s", version.Name, kind)
}

// runInProgressError is the stable 409 of spec §6.6/§7.4.
func runInProgressError(versionID int64, kind string) error {
	return domain.NewConflict("run_in_progress",
		fmt.Sprintf("a %s run of version %d is already queued or running", kind, versionID))
}

// runIsNotFound reports whether err is a mindmap not-found domain error.
func runIsNotFound(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound
}

// ProcessOnce performs one worker tick: it reads at most five active runs and
// advances each from its agent session state (spec §8.4). A handled terminal
// transition is not an error; infrastructure failures are returned after every
// run had its chance.
func (s *RunService) ProcessOnce(ctx context.Context) error {
	if s.deps.Runs == nil {
		return errors.New("mindmap: run repository is not configured")
	}
	runs, err := s.deps.Runs.ListActive(ctx, runWorkerBatch)
	if err != nil {
		return fmt.Errorf("mindmap: list active runs: %w", err)
	}
	var firstErr error
	for _, run := range runs {
		if err := s.processRun(ctx, run); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// processRun advances one active run. Explore runs are owned by the exploration
// service and are skipped.
func (s *RunService) processRun(ctx context.Context, run domain.CaseDesignRun) error {
	if run.Kind == domain.RunKindExplore {
		return nil
	}
	switch run.Status {
	case domain.RunQueued:
		if !run.CreatedAt.IsZero() && s.now().Sub(run.CreatedAt) > runQueuedStartTimeout {
			return s.failOrDegrade(ctx, run, "agent_start_timeout")
		}
		return nil
	case domain.RunRunning:
	default:
		return nil
	}

	timeoutBase := run.UpdatedAt
	if run.StartedAt != nil {
		timeoutBase = *run.StartedAt
	}
	if !timeoutBase.IsZero() && s.now().Sub(timeoutBase) > runHardTimeout(run.Kind)+runAgentTimeoutGrace {
		if run.AgentSessionID != nil && s.deps.Agents != nil {
			_ = s.deps.Agents.Abort(ctx, *run.AgentSessionID, run.CreatedBy)
		}
		return s.failOrDegrade(ctx, run, "agent_timeout")
	}
	if run.AgentSessionID == nil {
		return s.failOrDegrade(ctx, run, "agent_start_timeout")
	}
	if s.deps.Agents == nil {
		return errors.New("mindmap: agent runtime is not configured")
	}
	session, err := s.deps.Agents.GetSession(ctx, *run.AgentSessionID)
	if err != nil {
		return fmt.Errorf("mindmap: get agent session %d: %w", *run.AgentSessionID, err)
	}
	status := session.Status
	if session.LastRun != nil && session.LastRun.Status != "" {
		status = session.LastRun.Status
	}
	switch status {
	case "done", "completed", "succeeded":
		return s.materializeRun(ctx, run)
	case "failed", "aborted", "interrupted":
		return s.failOrDegrade(ctx, run, "agent_"+status)
	default:
		return nil
	}
}

// Worker starts the in-process 2s poll loop of spec §8.4. Starting twice is a
// no-op; Stop terminates the loop.
func (s *RunService) Worker(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		ticker := time.NewTicker(runWorkerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				if err := s.ProcessOnce(workerCtx); err != nil {
					slog.WarnContext(workerCtx, "mindmap run worker tick failed", "error", err)
				}
			}
		}
	}()
}

// Stop terminates the worker loop and waits for its goroutine. It is safe to
// call without a running worker and more than once.
func (s *RunService) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.running = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// RecoverStale fails the runs left queued/running by a previous process
// (spec §6.6). The repository owns the worker_restart reason document.
func (s *RunService) RecoverStale(ctx context.Context) (int64, error) {
	if s.deps.Runs == nil {
		return 0, errors.New("mindmap: run repository is not configured")
	}
	return s.deps.Runs.RecoverStale(ctx, s.now().Add(-runRecoveryWindow))
}

// failRun moves one active run to failed with the stable reason document
// (spec §8.4: errors.reason + optional errors.validationErrors ≤20×500 runes)
// and publishes the run-finished event. The event is published only when the
// transition won the status CAS.
func (s *RunService) failRun(ctx context.Context, run domain.CaseDesignRun, reason string, validationErrors []string) error {
	return s.finishRun(ctx, run, domain.RunFailed, reason, validationErrors)
}

// failOrDegrade fails a legacy run and degrades a P7 supplement run, so an
// unavailable/timeout LLM never blocks the autonomous chain (spec §9.7).
func (s *RunService) failOrDegrade(ctx context.Context, run domain.CaseDesignRun, reason string) error {
	if isSupplementRunKind(run.Kind) {
		return s.finishRun(ctx, run, domain.RunDegraded, reason, nil)
	}
	return s.finishRun(ctx, run, domain.RunFailed, reason, nil)
}

// finishRun applies one terminal status to an active run under the status CAS.
func (s *RunService) finishRun(ctx context.Context, run domain.CaseDesignRun, status, reason string, validationErrors []string) error {
	validationErrors = runCapValidationErrors(validationErrors)
	finished := s.now()
	patch := RunPatch{
		Errors:     runFailureErrors(reason, validationErrors),
		FinishedAt: &finished,
	}
	if len(validationErrors) > 0 {
		patch.Stats = runMergeStats(run.Stats, map[string]any{"validationErrors": validationErrors})
	}
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, status, patch)
	if err != nil {
		return fmt.Errorf("mindmap: finish run %d as %s (%s): %w", run.ID, status, reason, err)
	}
	s.publishRunEvent(ctx, updated, domain.EventCaseRunFinished, reason, nil)
	return nil
}

// runFailureErrors builds the run errors document. No LLM or mindmap body text
// is ever recorded (D30); validation messages are short field-level summaries.
func runFailureErrors(reason string, validationErrors []string) json.RawMessage {
	document := map[string]any{"reason": reason}
	if len(validationErrors) > 0 {
		document["validationErrors"] = validationErrors
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return []byte(`{"reason":"internal_error"}`)
	}
	return encoded
}

// runMergeStats merges update into an existing stats document.
func runMergeStats(raw []byte, update map[string]any) json.RawMessage {
	document := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &document)
	}
	for key, value := range update {
		document[key] = value
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return raw
	}
	return encoded
}

// runCapValidationErrors applies the ≤20×500-rune bound of spec §8.4.
func runCapValidationErrors(messages []string) []string {
	if len(messages) == 0 {
		return nil
	}
	if len(messages) > 20 {
		messages = messages[:20]
	}
	capped := make([]string, 0, len(messages))
	for _, message := range messages {
		capped = append(capped, truncateRunes(message, 500))
	}
	return capped
}

// publishRunEvent emits one case.run_started/case.run_finished summary. A
// publish failure is logged and never returned (spec §6.11).
func (s *RunService) publishRunEvent(ctx context.Context, run domain.CaseDesignRun, eventType, reason string, extra map[string]any) {
	detail := map[string]any{
		"runId":  run.ID,
		"kind":   run.Kind,
		"status": run.Status,
	}
	if reason != "" {
		detail["reason"] = reason
	}
	for key, value := range extra {
		detail[key] = value
	}
	summary := fmt.Sprintf("%s run %s", run.Kind, run.Status)
	if reason != "" {
		summary = fmt.Sprintf("%s run %s: %s", run.Kind, run.Status, reason)
	}
	s.publishEvent(ctx, Event{
		VersionID: run.VersionID,
		Type:      eventType,
		Summary:   truncateRunes(summary, runMaxSummaryRunes),
		ActorID:   run.CreatedBy,
		Source:    domain.NodeSourceAIGenerated,
		Detail:    detail,
	})
}

// publishEvent sends one event after the commit. Failures are logged only.
func (s *RunService) publishEvent(ctx context.Context, event Event) {
	if s.deps.Events == nil {
		return
	}
	if err := s.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "mindmap event publish failed", "type", event.Type, "version_id", event.VersionID, "error", err)
	}
}

// nextID mints one application-generated Snowflake id (D29).
func (s *RunService) nextID() (int64, error) {
	if s.deps.IDs == nil {
		return 0, errors.New("mindmap: id generator is not configured")
	}
	id := s.deps.IDs.Next()
	if id <= 0 {
		return 0, errors.New("mindmap: id generator returned a non-positive id")
	}
	return id, nil
}

// now returns the injected clock time (falling back to the wall clock).
func (s *RunService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}

// buildInstruction assembles the run context of spec §8.3. Sections are added
// in priority order under a fixed footer reservation; the first section that
// does not fit is truncated (or dropped once nothing fits) and the caller
// records contextTruncated.
// runBuiltInstruction is one assembled run instruction plus the provenance the
// run row records: the context-truncation flag and the approved knowledge ids
// that were injected (spec §9.4).
type runBuiltInstruction struct {
	Text               string
	Truncated          bool
	KnowledgeRefs      []int64
	KnowledgeTruncated bool
}

// planWarningKnowledgeTruncated is the stable run warning of an injection budget
// that cut the approved version knowledge.
const planWarningKnowledgeTruncated = "knowledge_truncated"

// knowledgeRefIDs renders the injected knowledge ids as decimal strings: event
// and run detail ids cross the JSON boundary as strings (D29).
func knowledgeRefIDs(ids []int64) []string {
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, strconv.FormatInt(id, 10))
	}
	return refs
}

func (s *RunService) buildInstruction(ctx context.Context, version VersionSnapshot, cmd StartRunCommand, requirements []RequirementSnapshot, runID int64) runBuiltInstruction {
	footer := runSubmitInstruction(cmd.Kind, s.maxSupplementCases())
	budget := runMaxInstructionBytes - len(footer)
	var builder strings.Builder
	truncated := false
	write := func(body string) {
		if body == "" || truncated {
			return
		}
		if builder.Len()+len(body) <= budget {
			builder.WriteString(body)
			return
		}
		if remaining := budget - builder.Len(); remaining > 0 {
			builder.WriteString(runTruncateBytes(body, remaining))
		}
		truncated = true
	}
	built := runBuiltInstruction{}
	write(s.versionInstruction(version, cmd, runID))
	if cmd.Kind == domain.RunKindDesign {
		// The approved version knowledge is a design-run input only (spec §9.4).
		// The read is best-effort: an unavailable knowledge table never blocks a
		// design run, it only omits the segment and the knowledgeRefs record.
		if s.deps.Knowledge != nil {
			section, err := s.deps.Knowledge.ApprovedKnowledgeSection(ctx, version.ID)
			if err != nil {
				slog.WarnContext(ctx, "mindmap: approved knowledge read failed; starting the design run without it",
					"version_id", version.ID, "error", err)
			} else if strings.TrimSpace(section.Text) != "" {
				write(section.Text + "\n")
				built.KnowledgeRefs = section.Refs
				built.KnowledgeTruncated = section.Truncated
			}
		}
	}
	write(runRequirementsInstruction(requirements))
	write(s.relationsInstruction(ctx, version.ID))
	write(s.appModelsInstruction(ctx, version))
	write(s.factsInstruction(ctx, version.ID))
	write(s.graphInstruction(ctx, version.ID, cmd))
	builder.WriteString(footer)
	built.Text = builder.String()
	built.Truncated = truncated
	return built
}

// versionInstruction renders the version section (spec §8.3 point 1).
func (s *RunService) versionInstruction(version VersionSnapshot, cmd StartRunCommand, runID int64) string {
	var builder strings.Builder
	builder.WriteString("## 版本\n")
	fmt.Fprintf(&builder, "versionId=%d\nrunId=%d\nversionName=%q\nstatus=%s\nrunKind=%s\nmode=%s\n",
		version.ID, runID, version.Name, version.Status, cmd.Kind, runMode(cmd.Mode))
	if version.SystemID != nil {
		fmt.Fprintf(&builder, "systemId=%d\n", *version.SystemID)
	}
	if version.EnvironmentID != nil {
		fmt.Fprintf(&builder, "environmentId=%d\n", *version.EnvironmentID)
	}
	if version.CodeRepositoryID != nil {
		fmt.Fprintf(&builder, "codeRepositoryId=%d\n", *version.CodeRepositoryID)
	}
	if version.CodeBaselineCommit != nil {
		fmt.Fprintf(&builder, "codeBaselineCommit=%s\n", *version.CodeBaselineCommit)
	}
	return builder.String()
}

// runRequirementsInstruction renders the confirmed-requirement section
// (spec §8.3 point 2): id/title/priority/acceptance/attachment digest, at most
// 20 requirements and ≤8 KiB of detail per requirement.
func runRequirementsInstruction(requirements []RequirementSnapshot) string {
	if len(requirements) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("\n## 已确认需求\n")
	for index, requirement := range requirements {
		if index >= runMaxInstructionRequirements {
			break
		}
		fmt.Fprintf(&builder, "- id=%d title=%q priority=%s status=%s\n",
			requirement.ID, requirement.Title, requirement.Priority, requirement.Status)
		var detail strings.Builder
		if value := strings.TrimSpace(requirement.Description); value != "" {
			detail.WriteString("description: " + value)
		}
		if value := strings.TrimSpace(requirement.AcceptanceCriteria); value != "" {
			if detail.Len() > 0 {
				detail.WriteString("\n")
			}
			detail.WriteString("acceptance: " + value)
		}
		if value := strings.TrimSpace(requirement.AttachmentSummary); value != "" {
			if detail.Len() > 0 {
				detail.WriteString("\n")
			}
			detail.WriteString("attachments: " + value)
		}
		if detail.Len() > 0 {
			fmt.Fprintf(&builder, "  %s\n", runTruncateBytes(detail.String(), runRequirementExcerptBytes))
		}
	}
	return builder.String()
}

// relationsInstruction renders the requirement-relation section (spec §8.3
// point 3).
func (s *RunService) relationsInstruction(ctx context.Context, versionID int64) string {
	if s.deps.Requirements == nil {
		return ""
	}
	relations, err := s.deps.Requirements.ListRelations(ctx, versionID)
	if err != nil {
		slog.WarnContext(ctx, "mindmap: list run relations failed", "version_id", versionID, "error", err)
		return ""
	}
	if len(relations) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("\n## 需求关系\n")
	for index, relation := range relations {
		if index >= runMaxInstructionRelations {
			break
		}
		fmt.Fprintf(&builder, "- id=%d from=%d to=%d type=%s description=%q\n",
			relation.ID, relation.FromRequirementID, relation.ToRequirementID, relation.RelationType, relation.Description)
	}
	return builder.String()
}

// appModelsInstruction renders the app-model section (spec §8.3 point 4), at
// most 200 keys.
func (s *RunService) appModelsInstruction(ctx context.Context, version VersionSnapshot) string {
	if version.SystemID == nil || s.deps.Catalog == nil {
		return ""
	}
	models, err := s.deps.Catalog.ListAppModels(ctx, *version.SystemID)
	if err != nil {
		slog.WarnContext(ctx, "mindmap: list run app models failed", "system_id", *version.SystemID, "error", err)
		return ""
	}
	if len(models) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("\n## 应用模型\n")
	for index, model := range models {
		if index >= runMaxInstructionAppModels {
			break
		}
		fmt.Fprintf(&builder, "- key=%s kind=%s name=%q path=%q method=%s\n",
			model.Key, model.Kind, model.Name, model.Path, model.Method)
	}
	return builder.String()
}

// factsInstruction renders the P4 code-fact section (spec §8.3 point 5), at most
// 100 facts.
func (s *RunService) factsInstruction(ctx context.Context, versionID int64) string {
	if s.deps.CodeFacts == nil {
		return ""
	}
	facts, err := s.deps.CodeFacts.ListFacts(ctx, versionID, runMaxInstructionFacts)
	if err != nil {
		slog.WarnContext(ctx, "mindmap: list run code facts failed", "version_id", versionID, "error", err)
		return ""
	}
	if len(facts) == 0 {
		// No P4 analysis facts yet: fall back to the T0 git tree at the version
		// baseline commit so the agent still sees real file paths and can propose
		// code anchors (spec §6.7 anchors, §8.5 degraded mode).
		files, fileErr := s.deps.CodeFacts.ListBaselineFiles(ctx, versionID, runMaxInstructionFacts)
		if fileErr != nil {
			slog.WarnContext(ctx, "mindmap: list baseline files failed", "version_id", versionID, "error", fileErr)
			return ""
		}
		if len(files) == 0 {
			return ""
		}
		var fallback strings.Builder
		fallback.WriteString("\n## 代码文件（T0 基线，未运行 P4 分析）\n")
		fallback.WriteString("以下路径来自版本代码基线 commit；为每条用例至少给出 1 条 anchors（file 必须取自该列表，并带 startLine/endLine）。\n")
		for index, path := range files {
			if index >= runMaxInstructionFacts {
				break
			}
			fmt.Fprintf(&fallback, "- path=%q\n", path)
		}
		return fallback.String()
	}
	var builder strings.Builder
	builder.WriteString("\n## 代码事实\n")
	for index, fact := range facts {
		if index >= runMaxInstructionFacts {
			break
		}
		fmt.Fprintf(&builder, "- kind=%s key=%s path=%q name=%q detail=%q\n",
			fact.Kind, fact.Key, fact.Path, fact.Name, fact.Detail)
	}
	return builder.String()
}

// graphInstruction renders the current-mindmap summary (spec §8.3 point 6). A
// data-plan run with an explicit node id list only receives those nodes,
// including the nodeRevision the agent must echo back.
func (s *RunService) graphInstruction(ctx context.Context, versionID int64, cmd StartRunCommand) string {
	if s.deps.Nodes == nil {
		return ""
	}
	nodes, err := s.deps.Nodes.ListByVersion(ctx, versionID)
	if err != nil {
		slog.WarnContext(ctx, "mindmap: list run graph failed", "version_id", versionID, "error", err)
		return ""
	}
	stepsByNode, assertionsByNode, requirementsByNode := s.graphInstructionIndexes(ctx, versionID)

	if cmd.Kind == domain.RunKindDataPlan && len(cmd.NodeIDs) > 0 {
		return runTargetNodesInstruction(nodes, cmd.NodeIDs, stepsByNode, assertionsByNode, requirementsByNode)
	}
	if len(nodes) == 0 {
		return "\n## 现有脑图\n（空）\n"
	}
	var builder strings.Builder
	builder.WriteString("\n## 现有脑图\n")
	for index, node := range nodes {
		if index >= maxGraphNodes {
			break
		}
		builder.WriteString(runGraphNodeLine(node, len(stepsByNode[node.ID]), len(assertionsByNode[node.ID]), requirementsByNode[node.ID]))
	}
	return builder.String()
}

// runTargetNodesInstruction renders the explicit data-plan target list.
func runTargetNodesInstruction(nodes []domain.CaseNode, targetIDs []int64, stepsByNode map[int64][]domain.CaseStep, assertionsByNode map[int64][]domain.CaseAssertion, requirementsByNode map[int64][]int64) string {
	byID := make(map[int64]domain.CaseNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	var builder strings.Builder
	builder.WriteString("\n## 目标节点（数据规划）\n")
	for _, nodeID := range targetIDs {
		node, ok := byID[nodeID]
		if !ok {
			fmt.Fprintf(&builder, "- nodeId=%d（不存在）\n", nodeID)
			continue
		}
		builder.WriteString(runGraphNodeLine(node, len(stepsByNode[nodeID]), len(assertionsByNode[nodeID]), requirementsByNode[nodeID]))
	}
	return builder.String()
}

// runGraphNodeLine renders one summary line in the shape of case.graph_read.
func runGraphNodeLine(node domain.CaseNode, steps, assertions int, requirementIDs []int64) string {
	return fmt.Sprintf("- nodeId=%d kind=%s title=%q priority=%s status=%s revision=%d steps=%d assertions=%d requirements=%v source=%s\n",
		node.ID, node.Kind, node.Title, node.Priority, node.Status, node.Revision, steps, assertions, requirementIDs, node.Source)
}

// graphInstructionIndexes loads the optional child collections used by the
// graph summary. Missing repositories yield empty indexes.
func (s *RunService) graphInstructionIndexes(ctx context.Context, versionID int64) (map[int64][]domain.CaseStep, map[int64][]domain.CaseAssertion, map[int64][]int64) {
	stepsByNode := make(map[int64][]domain.CaseStep)
	assertionsByNode := make(map[int64][]domain.CaseAssertion)
	requirementsByNode := make(map[int64][]int64)
	if s.deps.Steps != nil {
		if steps, err := s.deps.Steps.ListByVersion(ctx, versionID); err == nil {
			for _, step := range steps {
				stepsByNode[step.NodeID] = append(stepsByNode[step.NodeID], step)
			}
		}
	}
	if s.deps.Assertions != nil {
		if assertions, err := s.deps.Assertions.ListByVersion(ctx, versionID); err == nil {
			for _, assertion := range assertions {
				assertionsByNode[assertion.NodeID] = append(assertionsByNode[assertion.NodeID], assertion)
			}
		}
	}
	if s.deps.Sources != nil {
		if sources, err := s.deps.Sources.ListByVersion(ctx, versionID); err == nil {
			for _, source := range sources {
				if source.RefKind == domain.RefRequirement && source.RefID != nil {
					requirementsByNode[source.NodeID] = append(requirementsByNode[source.NodeID], *source.RefID)
				}
			}
		}
	}
	return stepsByNode, assertionsByNode, requirementsByNode
}

// runSubmitInstruction is the mandatory task footer of spec §8.3 point 7. It is
// always the last block of the instruction and names the submission tool.
func runSubmitInstruction(kind string, maxSupplementCases int) string {
	if kind == RunKindSupplement || kind == RunKindIncrementalDesign {
		return supplementSubmitInstruction(kind, maxSupplementCases)
	}
	tool, role := "case.graph_propose", "结构化用例提案"
	switch kind {
	case domain.RunKindReview:
		tool, role = "case.review_submit", "评审 findings"
	case domain.RunKindDataPlan:
		tool, role = "case.data_plan_submit", "前置条件与测试数据 items"
	}
	var builder strings.Builder
	builder.WriteString("\n## 任务与提交要求\n")
	fmt.Fprintf(&builder, "任务：按角色边界完成%s，先读取版本上下文、已确认需求与现有脑图；需要代码事实时使用只读代码工具，不要猜测。\n", role)
	builder.WriteString("禁止编造：需求 ID 必须来自上文已确认需求；文件路径必须来自代码事实或仓库真实路径；不得编造需求、文件、接口或页面。\n")
	builder.WriteString("失败处理：如果信息不足或工具不可用，明确说明缺少什么（需求、文件、接口或数据），不要猜测，也不要提交不完整的提案。\n")
	fmt.Fprintf(&builder, "最后确认：必须调用 %s 完成提交（*_submit 工具），不要只输出文字。\n", tool)
	return builder.String()
}

// supplementSubmitInstruction is the task footer of the two P7 supplement kinds
// (spec §9.2). The agent submits through the supplement.* tools instead of a
// single *_submit tool; a run without findings may finish immediately.
func supplementSubmitInstruction(kind string, maxSupplementCases int) string {
	var builder strings.Builder
	builder.WriteString("\n## 任务与提交要求\n")
	if kind == RunKindIncrementalDesign {
		builder.WriteString("任务：为继续中的版本做增量设计。先读取已确认需求、现有脑图、最近结果摘要与 P4 影响摘要，再给出新增用例与受影响的旧用例。\n")
	} else {
		builder.WriteString("任务：为已完成基线批次的版本做一次后补全。先读取已确认需求、现有脑图与覆盖矩阵，找出缺失或薄弱的用例。\n")
	}
	fmt.Fprintf(&builder, "提交：新增用例必须调用 supplement.case_propose（单次运行最多 %d 条；每条至少 1 个步骤、1 个断言、至少 1 个已确认需求来源）。\n", maxSupplementCases)
	builder.WriteString("增量设计：调用 supplement.affected_cases_propose 给出受影响的旧用例（最多 20 条；高/中置信默认标记，低置信仅展示）。\n")
	builder.WriteString("需求缺口：调用 supplement.escalate（最多 3 条），不得自行编造需求。无发现时直接结束，不要提交空提案。\n")
	builder.WriteString("约束：补全不改基线；只提议，不直接写库。\n")
	return builder.String()
}

// runMode normalizes the optional design mode for the version section.
func runMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "append"
	}
	return mode
}

// runTruncateBytes cuts value to at most max bytes on a UTF-8 boundary.
func runTruncateBytes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && cut < len(value) && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
