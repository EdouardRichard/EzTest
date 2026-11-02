package agentapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// DispatchActor is one of the four allowed work_item creators (D21,
// task-types.md §9.3). Executors are never creators: they only claim items and
// write artifacts.
type DispatchActor string

// The four fixed work_item.created_by values.
const (
	DispatchActorStateMachine DispatchActor = DispatchActor(agent.WorkItemCreatedByStateMachine)
	DispatchActorPlanner      DispatchActor = DispatchActor(agent.WorkItemCreatedByPlanner)
	DispatchActorAssistant    DispatchActor = DispatchActor(agent.WorkItemCreatedByAssistant)
	DispatchActorTrigger      DispatchActor = DispatchActor(agent.WorkItemCreatedByTrigger)
)

// Valid reports whether the actor is one of the four fixed creators.
func (a DispatchActor) Valid() bool { return agent.ValidWorkItemCreatedBy(string(a)) }

// DispatchRequest is one four-way work_item creation request (D21). The actor
// is the creator, never the executor; the version stage owns which kinds may be
// created when.
type DispatchRequest struct {
	VersionID      int64
	Kind           string
	Goal           string
	Target         json.RawMessage
	Inputs         json.RawMessage
	Expected       json.RawMessage
	Budget         json.RawMessage
	DependsOn      []int64
	Actor          DispatchActor
	IdempotencyKey string
}

// VersionStageReader reads the deterministic stage projection the dispatcher
// validates stage-entry kinds against. version.StageRepository satisfies it;
// the dispatcher never writes the stage (only the stage machine does).
type VersionStageReader interface {
	GetStage(ctx context.Context, versionID int64) (version.StageState, error)
}

// VersionReportEnsurer is the Task 5 report-gate entry point (R43).
// *deliveryapp.ReportService satisfies it.
type VersionReportEnsurer interface {
	EnsureVersionReport(ctx context.Context, versionID int64) (delivery.Report, error)
}

// DispatcherDeps wires the four-way dispatcher.
type DispatcherDeps struct {
	// WorkItems is the additive 0.2.1 contract repository (Task 6).
	WorkItems agent.WorkItemContractRepository
	// Stages reads the version stage for kind-level stage validation. A
	// degraded assembly may leave it nil when it never dispatches
	// stage-bound kinds.
	Stages VersionStageReader
	// Reports enforces the §5.3 report gate behind report.version (R43).
	Reports VersionReportEnsurer
}

// Dispatcher is the only writer of the 0.2.1 work_item contract. It enforces
// the D21 creator-kind matrix, the version-stage ownership of stage-entry
// kinds, the idempotency key and depends_on positivity before delegating to
// WorkItemContractRepository.CreateWorkItem, whose unique constraint makes a
// repeated dispatch return the existing row instead of creating a duplicate.
type Dispatcher struct {
	workItems agent.WorkItemContractRepository
	stages    VersionStageReader
	reports   VersionReportEnsurer
}

// NewDispatcher wires the dispatcher. A nil WorkItems keeps the dispatcher
// constructible for degraded assemblies; Dispatch then fails explicitly.
func NewDispatcher(deps DispatcherDeps) *Dispatcher {
	return &Dispatcher{workItems: deps.WorkItems, stages: deps.Stages, reports: deps.Reports}
}

// EnqueueStageJob adapts the dispatcher to the Task 4 narrow StageJobEnqueuer
// port (R35). The state machine calls it after committing a stage transition,
// so the version already reads back at the stage that owns the job.
//
// design.cases and execute.batch become idempotent state_machine work items.
// report.version is not a duplicate work item at this layer: it goes through
// the §5.3 report gate (Task 5's EnsureVersionReport), which creates the report
// and its task only when the version has terminal execution data (R43).
// review.cases is deliberately not an entry job anymore: case_review is
// entered after generation+review, so the stage machine never asks for it.
func (d *Dispatcher) EnqueueStageJob(ctx context.Context, job versionapp.StageJob) error {
	switch job.Kind {
	case versionapp.StageJobDesignCases:
		return d.enqueueStageWorkItem(ctx, job, agent.WorkItemKindDesignCases)
	case versionapp.StageJobExecuteBatch:
		return d.enqueueStageWorkItem(ctx, job, agent.WorkItemKindExecuteBatch)
	case versionapp.StageJobReportVersion:
		return d.HandleReportVersion(ctx, job.VersionID)
	default:
		return platform.Invalid("kind", fmt.Sprintf("unsupported stage job kind %q", job.Kind))
	}
}

var _ versionapp.StageJobEnqueuer = (*Dispatcher)(nil)

// RequestJob is the controlled assistant bypass (D21 route 3): the assistant
// may only request the two case-authoring kinds, never execute.batch,
// report.version or explore.*. The request is delegated to Dispatch so it
// shares the role matrix, stage validation and idempotency key with the state
// machine path. A repeated job.start with the same payload returns the existing
// row.
func (d *Dispatcher) RequestJob(ctx context.Context, versionID int64, kind string, payload json.RawMessage) (agent.WorkItem, error) {
	if versionID <= 0 {
		return agent.WorkItem{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	kind = strings.TrimSpace(kind)
	if !agent.ValidWorkItemKind(kind) {
		return agent.WorkItem{}, platform.Invalid("kind", "must be one of the eleven 0.2.1 work item kinds")
	}
	if !actorMayCreate(DispatchActorAssistant, kind) {
		return agent.WorkItem{}, forbiddenKind(DispatchActorAssistant, kind)
	}
	inputs := payload
	if len(bytes.TrimSpace(inputs)) == 0 {
		inputs = nil
	}
	return d.Dispatch(ctx, DispatchRequest{
		VersionID:      versionID,
		Kind:           kind,
		Inputs:         inputs,
		Actor:          DispatchActorAssistant,
		IdempotencyKey: stageJobKey(versionID, kind, inputs),
	})
}

// HandleReportVersion runs the §5.3 report gate for a version entering
// reporting. ErrVersionReportNotDue is a no-op: no retry and no alert. The
// reconciler retries later when the version actually has terminal execution
// data; any other error propagates to the caller.
func (d *Dispatcher) HandleReportVersion(ctx context.Context, versionID int64) error {
	if versionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if d.reports == nil {
		// Degraded assembly: the report gate is not wired, so there is nothing
		// to ensure. The stage write already succeeded and remains the truth.
		return nil
	}
	if _, err := d.reports.EnsureVersionReport(ctx, versionID); err != nil {
		if errors.Is(err, deliveryapp.ErrVersionReportNotDue) {
			return nil
		}
		return fmt.Errorf("ensure version %d report: %w", versionID, err)
	}
	return nil
}

// Dispatch validates and creates one work item. A duplicate
// (version_id, kind, idempotency_key) returns the existing row.
func (d *Dispatcher) Dispatch(ctx context.Context, req DispatchRequest) (agent.WorkItem, error) {
	req.Kind = strings.TrimSpace(req.Kind)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if err := d.validateRequest(ctx, req); err != nil {
		return agent.WorkItem{}, err
	}
	if d.workItems == nil {
		return agent.WorkItem{}, errors.New("dispatcher: work item repository is not configured")
	}
	item, err := d.workItems.CreateWorkItem(ctx, agent.CreateWorkItemParams{
		VersionID:      req.VersionID,
		Kind:           req.Kind,
		Goal:           req.Goal,
		Target:         req.Target,
		Inputs:         req.Inputs,
		Expected:       req.Expected,
		Budget:         req.Budget,
		DependsOn:      req.DependsOn,
		CreatedBy:      string(req.Actor),
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return agent.WorkItem{}, fmt.Errorf("dispatch %s: %w", req.Kind, err)
	}
	return item, nil
}

// ---------- internals ----------

func (d *Dispatcher) validateRequest(ctx context.Context, req DispatchRequest) error {
	if req.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if !req.Actor.Valid() {
		return platform.Invalid("actor", "must be one of state_machine, planner, assistant, trigger")
	}
	if !agent.ValidWorkItemKind(req.Kind) {
		return platform.Invalid("kind", "must be one of the eleven 0.2.1 work item kinds")
	}
	if !actorMayCreate(req.Actor, req.Kind) {
		return forbiddenKind(req.Actor, req.Kind)
	}
	if req.IdempotencyKey == "" {
		return platform.Invalid("idempotency_key", "must not be empty for an idempotent dispatch")
	}
	if len(req.IdempotencyKey) > 200 {
		return platform.Invalid("idempotency_key", "must be at most 200 characters")
	}
	for _, dependency := range req.DependsOn {
		if dependency <= 0 {
			return platform.Invalid("depends_on", "must contain only positive decimal work item IDs")
		}
	}
	return d.validateStage(ctx, req)
}

// validateStage enforces the version stage that owns a stage-bound kind. Kinds
// whose creation is not owned by one stage (explore.*, analyze.failure,
// knowledge.propose) skip the read.
func (d *Dispatcher) validateStage(ctx context.Context, req DispatchRequest) error {
	want, ok := requiredStage(req.Kind)
	if !ok {
		return nil
	}
	if d.stages == nil {
		return errors.New("dispatcher: version stage reader is not configured")
	}
	state, err := d.stages.GetStage(ctx, req.VersionID)
	if err != nil {
		if errors.Is(err, version.ErrNotFound) {
			return platform.NotFound("version")
		}
		return fmt.Errorf("read version %d stage: %w", req.VersionID, err)
	}
	if state.Stage != want {
		return platform.StateConflict(fmt.Sprintf(
			"work item kind %s requires version stage %s, but the version is at %s",
			req.Kind, want, state.Stage,
		))
	}
	return nil
}

// actorMayCreate is the D21 four-way creator-kind matrix (task-types.md §9.3).
func actorMayCreate(actor DispatchActor, kind string) bool {
	switch actor {
	case DispatchActorStateMachine:
		switch kind {
		case agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases,
			agent.WorkItemKindExecuteBatch, agent.WorkItemKindReportVersion:
			return true
		}
	case DispatchActorPlanner:
		return isExploreKind(kind)
	case DispatchActorAssistant:
		switch kind {
		case agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases:
			return true
		}
	case DispatchActorTrigger:
		switch kind {
		case agent.WorkItemKindReportVersion, agent.WorkItemKindAnalyzeFailure,
			agent.WorkItemKindKnowledgePropose:
			return true
		}
	}
	return false
}

func isExploreKind(kind string) bool {
	switch kind {
	case agent.WorkItemKindExploreLogin, agent.WorkItemKindExplorePage,
		agent.WorkItemKindExploreAPI, agent.WorkItemKindExploreCoverage,
		agent.WorkItemKindExploreGap:
		return true
	default:
		return false
	}
}

// requiredStage maps the stage-entry kinds to the version stage that owns them
// (spec §5.3/§5.4). review.cases is generated while designing; case_review has
// no entry job.
func requiredStage(kind string) (version.Stage, bool) {
	switch kind {
	case agent.WorkItemKindDesignCases, agent.WorkItemKindReviewCases:
		return version.StageDesigning, true
	case agent.WorkItemKindExecuteBatch:
		return version.StageExecuting, true
	case agent.WorkItemKindReportVersion:
		return version.StageReporting, true
	default:
		return "", false
	}
}

func forbiddenKind(actor DispatchActor, kind string) error {
	return platform.NewError(http.StatusForbidden, "forbidden", fmt.Sprintf(
		"actor %s may not create work item kind %s", actor, kind,
	))
}

func (d *Dispatcher) enqueueStageWorkItem(ctx context.Context, job versionapp.StageJob, kind string) error {
	inputs, err := stageJobInputs(job, kind)
	if err != nil {
		return err
	}
	goal := "execute the confirmed cases"
	if kind == agent.WorkItemKindDesignCases {
		goal = "generate case drafts for the confirmed requirements"
	}
	_, err = d.Dispatch(ctx, DispatchRequest{
		VersionID:      job.VersionID,
		Kind:           kind,
		Goal:           goal,
		Inputs:         inputs,
		Actor:          DispatchActorStateMachine,
		IdempotencyKey: stageJobKey(job.VersionID, kind, inputs),
	})
	return err
}

// stageJobInputs renders the stage job's narrowing payload. An empty selection
// means "all items of that kind" (Task 4 expands it at the gate), so it renders
// as {} and shares one idempotency key across stage re-entries.
func stageJobInputs(job versionapp.StageJob, kind string) (json.RawMessage, error) {
	var value any
	switch kind {
	case agent.WorkItemKindDesignCases:
		value = struct {
			RequirementIDs []int64 `json:"requirement_ids,omitempty"`
		}{RequirementIDs: job.RequirementIDs}
	case agent.WorkItemKindExecuteBatch:
		value = struct {
			CaseIDs []int64 `json:"case_ids,omitempty"`
		}{CaseIDs: job.CaseIDs}
	default:
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode %s stage job inputs: %w", kind, err)
	}
	return raw, nil
}

// stageJobKey is the contract's version:kind:target_hash idempotency key
// (task-types.md §9.4). The stage machine and the controlled assistant bypass
// derive the same key for the same target, so the two paths can never create
// two rows for the same job.
func stageJobKey(versionID int64, kind string, inputs json.RawMessage) string {
	return fmt.Sprintf("%d:%s:%s", versionID, kind, targetHash(inputs))
}

// targetHash hashes the canonical JSON input object. Canonicalization makes the
// stage machine struct encoding and an assistant payload with the same fields
// (different key order/whitespace) share an idempotency key.
func targetHash(inputs json.RawMessage) string {
	canonical := []byte(`{}`)
	if trimmed := bytes.TrimSpace(inputs); len(trimmed) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err == nil && object != nil {
			if encoded, err := json.Marshal(object); err == nil {
				canonical = encoded
			}
		} else {
			canonical = trimmed
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])[:16]
}
