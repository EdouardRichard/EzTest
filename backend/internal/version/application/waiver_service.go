package versionapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Waiver event types (v0.2.4-G SC-G11/SC-G13). They disclose the admin waiver
// itself; the terminal close still publishes version.closed (with
// closure_kind/unverified) through the single Close entry.
const (
	waiverEventVersionWaived     = "version.waived"
	waiverEventRequirementWaived = "requirement.waived"
)

// Waiver fact source. The waiver is an explicit admin action, never an agent
// conclusion.
const waiverFactSource = "admin"

// WaiverVersionReader loads the version row so WaiveClose can detect an
// already waived-closed version (idempotent report-leg retry) and reject a
// normally closed one.
type WaiverVersionReader interface {
	Get(ctx context.Context, versionID int64) (version.Version, []version.AccountBinding, error)
}

// WaiverVersionCloser is the single terminal Close entry surface the waiver
// consumes. *VersionCloseService satisfies it: Evaluate is the read-only full
// predicate (used for the execution-data precondition), CloseWaived is the
// waiver variant of the SAME Close CAS — never a second status writer.
type WaiverVersionCloser interface {
	Evaluate(ctx context.Context, versionID int64) (version.ClosePredicate, error)
	CloseWaived(ctx context.Context, versionID int64, reason string) (version.Version, error)
}

// WaiverRequirementReader loads one requirement by ID for the waiver state
// gate. It is the GetRequirement slice of ClosureRequirementReader (the waiver
// never scans a version's requirements).
type WaiverRequirementReader interface {
	GetRequirement(ctx context.Context, requirementID int64) (version.Requirement, error)
}

// RequirementWaiverWriter marks the requirement-level waiver flag (always
// true). Clearing happens ONLY through the reopen paths (requirement reopen and
// the version reopen's waiver undo), never through this port. The implementation
// guards on the clarified status so a concurrent state change rejects the write.
type RequirementWaiverWriter interface {
	MarkWaived(ctx context.Context, requirementID int64, updatedAt time.Time) error
}

// WaivedReportPublication identifies the synchronously regenerated unverified
// report revision produced after a waive close.
type WaivedReportPublication struct {
	ReportID int64
	Revision int32
}

// WaivedReportRegenerator is the delivery-context port invoked synchronously
// after a waive close (R-G8). It ensures the version report exists, allocates
// the next revision and publishes it immediately — breaking the dead chain
// where a completed version is excluded from the reconciler's report ensure
// pass. The requirement-settled leg of the report due gate is overridden by the
// waiver; the execution-data legs still hold.
type WaivedReportRegenerator interface {
	RegenerateWaivedVersionReport(ctx context.Context, versionID int64) (WaivedReportPublication, error)
}

// WaiverFactRecorder records the append-only waiver fact (reason + operator).
// *FactService satisfies it.
type WaiverFactRecorder interface {
	RecordFact(ctx context.Context, fact version.Fact) (FactRecordOutcome, error)
}

// WaiverServiceDeps groups the waiver service's consumer ports. Audit, events
// and the clock are optional (best effort / system default); the rest are
// required and a missing one fails the operation closed.
type WaiverServiceDeps struct {
	Versions     WaiverVersionReader
	Closer       WaiverVersionCloser
	Requirements WaiverRequirementReader
	Waivers      RequirementWaiverWriter
	Reports      WaivedReportRegenerator
	Facts        WaiverFactRecorder
	Audit        auditapp.Recorder
	Events       eventingapp.Publisher
	Clock        clock.Clock
}

// WaiverService owns the two-level admin waiver (GD-06/P23, SC-G11, Q7=A).
// WaiveClose overrides version closure completeness through the single Close
// terminal entry and synchronously regenerates the unverified report;
// WaiveRequirement marks a requirement so the deterministic closure reconciler
// closes it with the completeness checks overridden. Both are admin-only and
// non-silent (waiver fact + event + audit recording the operator).
type WaiverService struct {
	versions     WaiverVersionReader
	closer       WaiverVersionCloser
	requirements WaiverRequirementReader
	waivers      RequirementWaiverWriter
	reports      WaivedReportRegenerator
	facts        WaiverFactRecorder
	audit        auditapp.Recorder
	events       eventingapp.Publisher
	clock        clock.Clock
}

// NewWaiverService wires the waiver service. The clock defaults to the system
// clock; audit and events may be nil in unit tests.
func NewWaiverService(deps WaiverServiceDeps) *WaiverService {
	clk := deps.Clock
	if clk == nil {
		clk = clock.System{}
	}
	return &WaiverService{
		versions:     deps.Versions,
		closer:       deps.Closer,
		requirements: deps.Requirements,
		waivers:      deps.Waivers,
		reports:      deps.Reports,
		facts:        deps.Facts,
		audit:        deps.Audit,
		events:       deps.Events,
		clock:        clk,
	}
}

// WaiveCloseResult is the outcome of a version waive close: the closed version
// (closure_kind=waived, unverified) and the synchronously published unverified
// report revision.
type WaiveCloseResult struct {
	Version  version.Version
	ReportID int64
	Revision int32
}

// WaiveClose closes a running version by admin waiver (R-G6/R-G8). The six
// execution-settled checks still hold (an unsettled version is a 409); the
// waiver overrides RequirementsClosed and report currency. The close goes
// through the single Close terminal entry (CloseWaived), then the unverified
// report is regenerated and published synchronously. A repeated call on an
// already waived-closed version retries ONLY the report leg (the close is
// immutable), healing a failed post-close regeneration.
func (s *WaiverService) WaiveClose(ctx context.Context, versionID int64, reason string, actorID int64) (WaiveCloseResult, error) {
	if versionID <= 0 {
		return WaiveCloseResult{}, platform.Invalid("id", "must be a positive decimal ID")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return WaiveCloseResult{}, platform.Invalid("reason", "must not be empty")
	}
	if len([]rune(reason)) > version.VersionCloseReasonMax {
		return WaiveCloseResult{}, platform.Invalid("reason", fmt.Sprintf(
			"must be at most %d characters", version.VersionCloseReasonMax))
	}
	if err := requireWaiverAdmin(ctx, actorID); err != nil {
		return WaiveCloseResult{}, err
	}
	if err := s.requireWaiveCloseDeps(); err != nil {
		return WaiveCloseResult{}, err
	}

	stored, _, err := s.versions.Get(ctx, versionID)
	if err != nil {
		return WaiveCloseResult{}, mapVersionCloseRepositoryError(err)
	}
	if stored.Status == version.StatusCompleted {
		if stored.ClosureKind == version.ClosureKindWaived {
			// Idempotent report-leg retry: the close already committed; heal a
			// failed post-close regeneration without re-closing or re-recording.
			publication, err := s.regenerateUnverifiedReport(ctx, versionID)
			if err != nil {
				return WaiveCloseResult{}, err
			}
			return WaiveCloseResult{Version: stored, ReportID: publication.ReportID, Revision: publication.Revision}, nil
		}
		return WaiveCloseResult{}, platform.StateConflict("version is not running")
	}

	// Fail-closed precondition: the mandatory post-close unverified report needs
	// execution data. Reject BEFORE the terminal write so a waive can never leave
	// a completed version without a report (R-G8 reachability). A never-executed
	// version has no report/closure path under the NORMAL close either (check 8
	// folds HasExecutionData in), so the waive is consistent with the full
	// predicate, not an extra restriction; the never-executed close gap is a
	// registered G residual, not a waiver-specific hole.
	predicate, err := s.closer.Evaluate(ctx, versionID)
	if err != nil {
		return WaiveCloseResult{}, err
	}
	if !predicate.HasExecutionData {
		apiErr := platform.StateConflict("waive close requires execution data to regenerate the unverified report")
		apiErr.Details = map[string]any{"reason": "no_execution_data"}
		return WaiveCloseResult{}, apiErr
	}

	closed, err := s.closer.CloseWaived(ctx, versionID, reason)
	if err != nil {
		var predicateFailed *version.ErrVersionClosePredicateFailed
		if errors.As(err, &predicateFailed) {
			apiErr := platform.StateConflict("version close predicate failed")
			apiErr.Details = map[string]any{
				"reason": "close_predicate_failed",
				"failed": predicateFailed.Failed,
			}
			return WaiveCloseResult{}, apiErr
		}
		return WaiveCloseResult{}, err
	}

	s.recordVersionWaiver(ctx, closed, reason, actorID)

	publication, err := s.regenerateUnverifiedReport(ctx, versionID)
	if err != nil {
		return WaiveCloseResult{}, err
	}
	return WaiveCloseResult{Version: closed, ReportID: publication.ReportID, Revision: publication.Revision}, nil
}

// WaiveRequirement marks a clarified requirement as admin-waived so the
// deterministic closure reconciler closes it with the completeness checks
// overridden (the execution-settled checks still gate the actual close). The
// waiver is recorded as an append-only fact + event + audit with the operator.
// Re-waiving an already waived requirement is an idempotent no-op.
func (s *WaiverService) WaiveRequirement(ctx context.Context, requirementID int64, reason string, actorID int64) error {
	if requirementID <= 0 {
		return platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return platform.Invalid("reason", "must not be empty")
	}
	if len([]rune(reason)) > version.RequirementReasonMax {
		return platform.Invalid("reason", fmt.Sprintf(
			"must be at most %d characters", version.RequirementReasonMax))
	}
	if err := requireWaiverAdmin(ctx, actorID); err != nil {
		return err
	}
	if s.requirements == nil || s.waivers == nil || s.facts == nil {
		return errors.New("waiver service is missing its requirement reader, waiver writer or fact recorder")
	}

	requirement, err := s.requirements.GetRequirement(ctx, requirementID)
	if err != nil {
		return mapRequirementRepositoryError(err)
	}
	switch {
	case requirement.Status == version.RequirementClarified && requirement.Waived:
		return nil // idempotent
	case requirement.Status == version.RequirementClosed && requirement.Waived:
		return nil // idempotent
	case requirement.Status != version.RequirementClarified:
		return platform.StateConflict("only a clarified requirement can be waived")
	}

	// One clock read for the whole waiver action: the flag's waived_at and the
	// fact's waived_at discriminator must be the SAME instant (H-G4 T6: two
	// independent clock.Now() calls could straddle a tick and disagree).
	now := s.clock.Now()
	if err := s.waivers.MarkWaived(ctx, requirementID, now); err != nil {
		return mapRequirementRepositoryError(err)
	}

	// The waiver transition succeeded: the event + audit ALWAYS fire (GD-06
	// accountability). Fact-log idempotency must never suppress the audit of a
	// real admin action — a repeat waiver with an identical reason is a NEW
	// action, not a duplicate announcement. waived_at discriminates the fact so
	// each waiver action appends its own historical row.
	waivedAt := now.UTC().Format(closeTimeLayout)
	fact := version.Fact{
		VersionID: requirement.VersionID,
		Kind:      version.FactKindWaiver,
		Key:       version.RequirementFactKeyPrefix + platformid.Format(requirementID),
		Source:    waiverFactSource,
		Payload: map[string]any{
			"scope":          "requirement",
			"version_id":     platformid.Format(requirement.VersionID),
			"requirement_id": platformid.Format(requirementID),
			"reason":         reason,
			"actor_id":       platformid.Format(actorID),
			"waived_at":      waivedAt,
		},
	}
	if _, err := s.facts.RecordFact(ctx, fact); err != nil {
		// The waived flag committed; a fact-store failure must not roll it back,
		// but it is never silent (七.6). The event/audit below still disclose the
		// waiver and operator.
		slog.WarnContext(ctx, "requirement waiver fact record failed",
			"requirement_id", requirementID, "kind", version.FactKindWaiver, "error", err)
	}
	s.publishWaiverEvent(ctx, requirement.VersionID, waiverEventRequirementWaived, map[string]any{
		"version_id":     platformid.Format(requirement.VersionID),
		"requirement_id": platformid.Format(requirementID),
		"reason":         reason,
		"actor_id":       platformid.Format(actorID),
		"waived_at":      waivedAt,
	})
	s.recordWaiverAudit(ctx, waiverEventRequirementWaived, "requirement", requirementID, actorID, map[string]any{
		"version_id":     platformid.Format(requirement.VersionID),
		"requirement_id": platformid.Format(requirementID),
		"reason":         reason,
		"waived_at":      waivedAt,
	})
	return nil
}

// regenerateUnverifiedReport invokes the synchronous post-close report path and
// surfaces a failure as a typed, disclosed error (七.6): the close already
// committed, so the failure is never silent and a repeated WaiveClose retries
// this leg.
func (s *WaiverService) regenerateUnverifiedReport(ctx context.Context, versionID int64) (WaivedReportPublication, error) {
	publication, err := s.reports.RegenerateWaivedVersionReport(ctx, versionID)
	if err != nil {
		apiErr := platform.NewError(500, "waive_report_regeneration_failed",
			"the version was waived closed but the unverified report could not be regenerated; retry the waive to heal the report")
		apiErr.Details = map[string]any{"version_id": platformid.Format(versionID), "cause": err.Error()}
		return WaivedReportPublication{}, apiErr
	}
	return publication, nil
}

// recordVersionWaiver records the version-level waiver fact + event + audit
// after a REAL waive-close transition. The event + audit ALWAYS fire (GD-06
// accountability): fact-log idempotency must never suppress the audit of a real
// admin action. waived_at discriminates the fact so each waiver action appends
// its own historical row; FactService still dedupes an identical re-record.
func (s *WaiverService) recordVersionWaiver(ctx context.Context, closed version.Version, reason string, actorID int64) {
	if s.facts == nil {
		return
	}
	waivedAt := s.clock.Now().UTC().Format(closeTimeLayout)
	fact := version.Fact{
		VersionID: closed.ID,
		Kind:      version.FactKindWaiver,
		Key:       "version",
		Source:    waiverFactSource,
		Payload: map[string]any{
			"scope":        "version",
			"version_id":   platformid.Format(closed.ID),
			"reason":       reason,
			"actor_id":     platformid.Format(actorID),
			"closure_kind": closed.ClosureKind,
			"unverified":   closed.Unverified,
			"waived_at":    waivedAt,
		},
	}
	if _, err := s.facts.RecordFact(ctx, fact); err != nil {
		// The close committed; a fact-store failure must not roll it back, but it
		// is never silent (七.6): log it loudly. The waiver is still disclosed by
		// version.closed (closure_kind/unverified + operator) and close_reason
		// carries the reason, so the durable truth survives even if this
		// report-read-model convenience fact is lost.
		slog.WarnContext(ctx, "waiver fact record failed",
			"version_id", closed.ID, "kind", version.FactKindWaiver, "error", err)
	}
	s.publishWaiverEvent(ctx, closed.ID, waiverEventVersionWaived, map[string]any{
		"version_id":   platformid.Format(closed.ID),
		"reason":       reason,
		"actor_id":     platformid.Format(actorID),
		"closure_kind": closed.ClosureKind,
		"unverified":   closed.Unverified,
		"waived_at":    waivedAt,
	})
	s.recordWaiverAudit(ctx, waiverEventVersionWaived, "version", closed.ID, actorID, map[string]any{
		"version_id":   platformid.Format(closed.ID),
		"reason":       reason,
		"closure_kind": closed.ClosureKind,
		"unverified":   closed.Unverified,
		"waived_at":    waivedAt,
	})
}

// requireWaiverAdmin is the Q7=A functional gate: the authenticated actor must
// be the admin AND match the recorded operator. A missing/mismatched/non-admin
// actor is a 403 before any side effect. This is a functional distinction, not
// permission engineering (GD-01 / Q08=C).
func requireWaiverAdmin(ctx context.Context, actorID int64) error {
	actor, ok := platform.ActorFrom(ctx)
	if !ok || actor.ID <= 0 || actor.ID != actorID || !actor.IsAdmin() {
		return platform.Forbidden()
	}
	return nil
}

func (s *WaiverService) requireWaiveCloseDeps() error {
	switch {
	case s.versions == nil:
		return errors.New("waiver service is missing its version reader")
	case s.closer == nil:
		return errors.New("waiver service is missing its version closer")
	case s.reports == nil:
		return errors.New("waiver service is missing its report regenerator")
	case s.facts == nil:
		return errors.New("waiver service is missing its fact recorder")
	default:
		return nil
	}
}

// publishWaiverEvent publishes a waiver event to the version channel; the
// version-level waiver also goes to system (the version-state convention).
// Delivery is best effort — the fact and the close projection are the truth.
func (s *WaiverService) publishWaiverEvent(ctx context.Context, versionID int64, eventType string, payload map[string]any) {
	if s.events == nil {
		return
	}
	versionIDString := platformid.Format(versionID)
	channels := []string{"version:" + versionIDString}
	if eventType == waiverEventVersionWaived {
		channels = append(channels, "system")
	}
	for _, channel := range channels {
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   channel,
			Type:      eventType,
			Scope:     map[string]any{"version_id": versionIDString},
			Payload:   payload,
			RequestID: requestid.From(ctx),
		})
	}
}

// recordWaiverAudit records the best-effort audit entry with the operator.
func (s *WaiverService) recordWaiverAudit(ctx context.Context, action, targetKind string, targetID, actorID int64, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	var actor *int64
	if actorID > 0 {
		value := actorID
		actor = &value
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actor,
		Action:     action,
		TargetKind: targetKind,
		TargetID:   platformid.Format(targetID),
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}

var (
	_ WaiverVersionCloser = (*VersionCloseService)(nil)
	_ WaiverFactRecorder  = (*FactService)(nil)
)
