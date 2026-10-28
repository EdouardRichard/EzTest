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
	Runs         RunRepository
	Graph        GraphRepository
	Baselines    BaselineRepository
	Nodes        NodeRepository
	Steps        StepRepository
	Assertions   AssertionRepository
	Sources      SourceRepository
	Versions     VersionLifecycle
	Requirements RequirementReader
	Catalog      CatalogGateway
	CodeFacts    CodeFactsGateway
	Anchors      CodeAnchorGateway
	Agents       AgentRuntime
	Events       EventPublisher
	IDs          IDGenerator
	Clock        Clock
}

// StartRunCommand is the POST /runs body of spec §7.2 endpoint 17 restricted to
// the kinds this service coordinates (design/review/data_plan). Explore runs are
// owned by the exploration service.
type StartRunCommand struct {
	Kind           string  `json:"kind"`
	Mode           string  `json:"mode,omitempty"`
	ConfirmReplace bool    `json:"confirmReplace,omitempty"`
	NodeIDs        []int64 `json:"nodeIds,omitempty"`
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
	return &RunService{deps: deps}
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
	default:
		return "", ""
	}
}

// runHardTimeout is the profile HardTimeout of one kind (spec §8.1).
func runHardTimeout(kind string) time.Duration {
	switch kind {
	case domain.RunKindDesign, domain.RunKindReview:
		return 90 * time.Second
	case domain.RunKindDataPlan, domain.RunKindExplore:
		return 60 * time.Second
	default:
		return 90 * time.Second
	}
}

// runStartAllowed reports whether a run of kind may start at the version status.
// Review runs are read-only, so they stay allowed after the baseline froze the
// version to executing (spec §6.4 rule 3); design/data_plan write the graph and
// need an editable version.
func runStartAllowed(kind, status string) bool {
	switch kind {
	case domain.RunKindReview:
		return status == versionStatusDesigning || status == versionStatusReviewing || status == "executing"
	default:
		return status == versionStatusDesigning || status == versionStatusReviewing
	}
}

// runAllowedStates is the human-readable status list of runStartAllowed.
func runAllowedStates(kind string) string {
	if kind == domain.RunKindReview {
		return "designing, reviewing or executing"
	}
	return "designing or reviewing"
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
			fmt.Sprintf("run kind %q is not design, review or data_plan", cmd.Kind))
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
	instruction, truncated := s.buildInstruction(ctx, version, cmd, requirements, runID)
	input, err := json.Marshal(cmd)
	if err != nil {
		return domain.CaseDesignRun{}, fmt.Errorf("mindmap: encode run input: %w", err)
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
	if truncated {
		run.Stats = []byte(`{"contextTruncated":true}`)
	}
	run, err = s.deps.Runs.Create(ctx, run)
	if err != nil {
		return run, err
	}

	sessionID, err := s.deps.Agents.CreateSession(ctx, profileKey, runSessionTitle(version, cmd.Kind), actor,
		fmt.Sprintf("version:%d", versionID), strconv.FormatInt(versionID, 10))
	if err != nil {
		return run, s.abortStart(ctx, run, err)
	}
	agentRunID, err := s.deps.Agents.StartText(ctx, sessionID, instruction, actor)
	if err != nil {
		// The session exists but no run was started on it; abort it so the
		// failed attempt does not leave an active orphan (best-effort).
		_ = s.deps.Agents.Abort(ctx, sessionID, actor)
		return run, s.abortStart(ctx, run, err)
	}
	startedAt := s.now()
	// Keep the pre-update run: on a CAS failure the adapter returns no row, and
	// abortStart needs the persisted id/status to release the run and the
	// single-flight slot.
	running, updateErr := s.deps.Runs.UpdateStatus(ctx, run.ID, domain.RunQueued, domain.RunRunning, RunPatch{
		AgentSessionID: &sessionID,
		AgentRunID:     &agentRunID,
		StartedAt:      &startedAt,
	})
	if updateErr != nil {
		_ = s.deps.Agents.Abort(ctx, sessionID, actor)
		return run, s.abortStart(ctx, run, updateErr)
	}
	run = running
	s.publishRunEvent(ctx, run, domain.EventCaseRunStarted, "", nil)
	return run, nil
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
			return s.failRun(ctx, run, "agent_start_timeout", nil)
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
		return s.failRun(ctx, run, "agent_timeout", nil)
	}
	if run.AgentSessionID == nil {
		return s.failRun(ctx, run, "agent_start_timeout", nil)
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
		return s.failRun(ctx, run, "agent_"+status, nil)
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
	validationErrors = runCapValidationErrors(validationErrors)
	finished := s.now()
	patch := RunPatch{
		Errors:     runFailureErrors(reason, validationErrors),
		FinishedAt: &finished,
	}
	if len(validationErrors) > 0 {
		patch.Stats = runMergeStats(run.Stats, map[string]any{"validationErrors": validationErrors})
	}
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, domain.RunFailed, patch)
	if err != nil {
		return fmt.Errorf("mindmap: fail run %d (%s): %w", run.ID, reason, err)
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
func (s *RunService) buildInstruction(ctx context.Context, version VersionSnapshot, cmd StartRunCommand, requirements []RequirementSnapshot, runID int64) (string, bool) {
	footer := runSubmitInstruction(cmd.Kind)
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
	write(s.versionInstruction(version, cmd, runID))
	write(runRequirementsInstruction(requirements))
	write(s.relationsInstruction(ctx, version.ID))
	write(s.appModelsInstruction(ctx, version))
	write(s.factsInstruction(ctx, version.ID))
	write(s.graphInstruction(ctx, version.ID, cmd))
	builder.WriteString(footer)
	return builder.String(), truncated
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
func runSubmitInstruction(kind string) string {
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
