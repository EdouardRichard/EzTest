package versionapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- v0.22 stage machine gates (R-B4-2, B §7.1) ----------
//
// The v0.22 orchestrated stage machine (StageService, see stage_machine.go) owns
// every workflow transition and asks narrow ports for the facts each gate needs.
// The version application layer deliberately does not import the requirement,
// design, execution or delivery application packages: the composition root
// (Task 36/37) adapts those services to the function fields below. Nil entries
// fail closed with a typed gate_not_configured error so a missing wiring can
// never silently skip a gate.

// Blocker is the structured version blocker persisted in version.blocker
// (B §7.2, R-B4-4). Ruling C6 keeps the column TEXT: the value is the JSON
// encoding of this struct, not a separate JSONB column.
type Blocker struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Action string `json:"action"`
	Next   string `json:"next"`
}

// BlockerKindManual is the default kind of a blocker that did not classify
// its cause, and the fallback kind for a legacy plain-text blocker row.
const BlockerKindManual = "manual"

// Normalize validates and canonicalizes one blocker. reason, action and next
// are required (R-B4-2 any -> blocked). The normalized result is what gets
// persisted and published, so a whitespace-only field cannot pass a gate.
func (b Blocker) Normalize() (Blocker, error) {
	kind := strings.TrimSpace(b.Kind)
	if kind == "" {
		kind = BlockerKindManual
	}
	reason := strings.TrimSpace(b.Reason)
	action := strings.TrimSpace(b.Action)
	next := strings.TrimSpace(b.Next)
	if reason == "" {
		return Blocker{}, platform.Invalid("blocker.reason", "must not be empty")
	}
	if action == "" {
		return Blocker{}, platform.Invalid("blocker.action", "must not be empty")
	}
	if next == "" {
		return Blocker{}, platform.Invalid("blocker.next", "must not be empty")
	}
	normalized := Blocker{Kind: kind, Reason: reason, Action: action, Next: next}
	encoded, err := normalized.JSON()
	if err != nil {
		return Blocker{}, err
	}
	if len([]rune(encoded)) > stageBlockerMax {
		return Blocker{}, platform.Invalid("blocker", fmt.Sprintf(
			"must encode to at most %d characters", stageBlockerMax))
	}
	return normalized, nil
}

// JSON encodes the blocker for version.blocker.
func (b Blocker) JSON() (string, error) {
	encoded, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("encode stage blocker: %w", err)
	}
	return string(encoded), nil
}

// ParseBlocker decodes a version.blocker value. A legacy plain-text blocker (a
// pre-v0.22 row) never fails: it becomes a manual blocker whose reason is the
// stored text, so an operator can still see and clear it.
func ParseBlocker(raw string) Blocker {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Blocker{}
	}
	var blocker Blocker
	if err := json.Unmarshal([]byte(trimmed), &blocker); err != nil ||
		strings.TrimSpace(blocker.Reason) == "" {
		return Blocker{
			Kind:   "legacy",
			Reason: trimmed,
			Action: "resolve the blocking condition",
			Next:   "retry the stage request",
		}
	}
	if strings.TrimSpace(blocker.Kind) == "" {
		blocker.Kind = BlockerKindManual
	}
	return blocker
}

// StageGateError is the typed gate rejection returned by StageService. It
// carries the closed gate plus the required reason/action/next so the API and
// the version page can tell the user what to do instead of showing a bare
// conflict. APIError maps it to the shared error envelope (409
// stage_gate_failed) for the future HTTP wiring (Task 36/37).
type StageGateError struct {
	Gate   string
	Reason string
	Action string
	Next   string
	Cause  error
}

// Error implements error.
func (e *StageGateError) Error() string {
	if e == nil {
		return "stage gate failed"
	}
	return fmt.Sprintf("stage gate %s is closed: %s (%s)", e.Gate, e.Reason, e.Action)
}

// Unwrap exposes the underlying gate failure.
func (e *StageGateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// APIError maps the gate rejection onto the platform error envelope.
func (e *StageGateError) APIError() *platform.Error {
	if e == nil {
		return platform.StateConflict("stage gate is closed")
	}
	return &platform.Error{
		Status:  http.StatusConflict,
		Code:    "stage_gate_failed",
		Message: e.Error(),
		Details: map[string]any{
			"gate":   e.Gate,
			"reason": e.Reason,
			"action": e.Action,
			"next":   e.Next,
		},
	}
}

// StageTransitionError is the typed invalid-transition rejection: the
// deterministic graph does not allow from -> to, or the caller used the wrong
// entry point (Request for a suspension stage, Unblock for a non-blocked
// version). It carries the same actionable detail as a gate rejection.
type StageTransitionError struct {
	From   version.Stage
	To     version.Stage
	Action string
	Cause  error
}

// Error implements error.
func (e *StageTransitionError) Error() string {
	if e == nil {
		return "invalid stage transition"
	}
	return fmt.Sprintf("invalid stage transition %s -> %s: %s", e.From, e.To, e.Action)
}

// Unwrap exposes the underlying cause.
func (e *StageTransitionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// APIError maps the invalid transition onto the platform error envelope.
func (e *StageTransitionError) APIError() *platform.Error {
	if e == nil {
		return platform.StateConflict("invalid stage transition")
	}
	return &platform.Error{
		Status:  http.StatusConflict,
		Code:    "invalid_stage_transition",
		Message: e.Error(),
		Details: map[string]any{
			"from":   string(e.From),
			"to":     string(e.To),
			"action": e.Action,
		},
	}
}

func newStageGateError(gate, reason, action, next string, cause error) *StageGateError {
	return &StageGateError{Gate: gate, Reason: reason, Action: action, Next: next, Cause: cause}
}

func newStageGateNotConfigured(gate string) *StageGateError {
	return newStageGateError(gate, "gate_not_configured",
		"wire this stage gate in the composition root",
		"stage_request", nil)
}

func newStageTransitionError(from, to version.Stage, action string) *StageTransitionError {
	if action == "" {
		action = fmt.Sprintf("reach %s through the documented workflow before requesting %s", from, to)
	}
	return &StageTransitionError{From: from, To: to, Action: action}
}

// StageGates is the v0.22 gate port set. Each function answers one §7.1 gate;
// nil fails closed (see newStageGateNotConfigured). The composition root
// (Task 36/37) adapts the requirement/design/execution/delivery/version close
// services here.
type StageGates struct {
	EnvironmentBound      func(ctx context.Context, versionID int64) (bool, error)
	RequirementCount      func(ctx context.Context, versionID int64) (int, error)
	RequirementsConfirmed func(ctx context.Context, versionID int64) (bool, error)
	OpenGaps              func(ctx context.Context, versionID int64) (int, error)
	ConfirmedCaseDrafts   func(ctx context.Context, versionID int64) (int, error)
	CoverageComputed      func(ctx context.Context, versionID int64) (bool, error)
	ConfirmedCases        func(ctx context.Context, versionID int64) (int, error)
	ExecutorOnline        func(ctx context.Context) (bool, error)
	AccountsValid         func(ctx context.Context, versionID int64) (bool, error)
	WavesTerminal         func(ctx context.Context, versionID int64) (bool, error)
	ReportPublished       func(ctx context.Context, versionID int64) (bool, error)
	OpenBlockingIssues    func(ctx context.Context, versionID int64) (int, error)
	BlockerCleared        func(ctx context.Context, versionID int64, blocker Blocker) (bool, error)
}

// StageWave is the terminal-callback projection of one execution wave. The
// stage service never imports the execution context; the composition root
// adapts its wave repository.
type StageWave struct {
	ID        int64
	VersionID int64
	Status    string
	Terminal  bool
}

// StageWaveReader resolves the production wave-terminal callback
// (OnWaveTerminal(waveID)) into its version and terminal state.
type StageWaveReader interface {
	GetWave(ctx context.Context, waveID int64) (StageWave, error)
}

// StageWaveController is the minimal pause/resume port (R-B4-5): it asks the
// execution context to gracefully pause the version's running waves and to
// continue them on resume. v0.22 does not do complex rescheduling.
type StageWaveController interface {
	PauseVersionWaves(ctx context.Context, versionID int64, reason string, actorID int64) error
	ResumeVersionWaves(ctx context.Context, versionID int64) error
}

// StageReports ensures the version report exists before executing -> reporting
// (R-B4-2 "报告已生成"). The composition root adapts ReportService.
type StageReports interface {
	EnsureVersionReport(ctx context.Context, versionID int64) error
}

// StageVersionCloser closes the version on reporting -> completed (R-B4-3
// "version.closed 有真实路径"). The composition root adapts VersionCloseService.
type StageVersionCloser interface {
	CloseVersion(ctx context.Context, versionID int64, reason string) error
}

// StageHandoffGenerator is the Task 27 handoff hook: Generate is called
// best-effort when requirement_review -> designing advances.
type StageHandoffGenerator interface {
	Generate(ctx context.Context, versionID int64) (Handoff, error)
}

// requirementGateConfirmer adapts RequirementService.BatchConfirm to the stage
// machine's narrow RequirementGateConfirmer port. The actor is taken from the
// request context: the HTTP layer has already authorized the user-only gate, so
// the service records the same actor in its confirmation/audit rows.
type requirementGateConfirmer struct {
	svc *RequirementService
}

// NewRequirementGateConfirmer wires the requirement batch-confirm use case into
// the requirement hard gate.
func NewRequirementGateConfirmer(svc *RequirementService) RequirementGateConfirmer {
	return &requirementGateConfirmer{svc: svc}
}

// BatchConfirm confirms the selected requirements before the gate advances. An
// empty requirementIDs selection means "confirm all current draft requirements"
// (contract StageConfirmRequirementsRequest), so it is expanded to the version's
// draft requirements first; a version with no draft requirement is a typed 409
// no_requirements_to_confirm rather than an empty batch or a stage advance.
// Service validation errors (invalid_argument, not_found, state_conflict)
// propagate untouched, so a failed confirmation never reaches the stage write.
func (c *requirementGateConfirmer) BatchConfirm(ctx context.Context, versionID int64, requirementIDs []int64) error {
	if c == nil || c.svc == nil {
		return fmt.Errorf("requirement gate confirmer is not configured")
	}
	ids := requirementIDs
	if len(ids) == 0 {
		expanded, err := c.draftRequirementIDs(ctx, versionID)
		if err != nil {
			return err
		}
		if len(expanded) == 0 {
			return platform.NewError(http.StatusConflict, "no_requirements_to_confirm",
				"the version has no draft requirement to confirm")
		}
		ids = expanded
	}
	var actorID int64
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID = actor.ID
	}
	_, err := c.svc.BatchConfirm(ctx, versionID, ids, actorID)
	return err
}

// draftRequirementIDs pages through the version's current draft requirements
// (ID-descending) so an empty gate selection can be expanded to the full list.
func (c *requirementGateConfirmer) draftRequirementIDs(ctx context.Context, versionID int64) ([]int64, error) {
	ids := make([]int64, 0)
	var beforeID *int64
	for {
		views, hasMore, err := c.svc.List(ctx, versionID, string(version.RequirementDraft), pagination.MaxLimit, beforeID)
		if err != nil {
			return nil, err
		}
		for _, view := range views {
			ids = append(ids, view.ID)
		}
		if !hasMore || len(views) == 0 {
			return ids, nil
		}
		lastID := views[len(views)-1].ID
		beforeID = &lastID
	}
}

var _ RequirementGateConfirmer = (*requirementGateConfirmer)(nil)
