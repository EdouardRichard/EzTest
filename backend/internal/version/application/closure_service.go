package versionapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Closure event types and triggers (events.md §13).
const (
	closureEventRequirementClosed   = "requirement.closed"
	closureEventRequirementReopened = "requirement.reopened"
	closureReopenTrigger            = "requirement_reopened"
)

// ClosureServiceDeps groups the closure service's consumer ports. The readers
// are implemented by the design/execution/delivery/version contexts and wired
// by the assembly root; audit and events are optional (best effort).
type ClosureServiceDeps struct {
	Closures     version.RequirementClosureRepository
	Requirements ClosureRequirementReader
	Cases        ClosureCaseReader
	Runs         ClosureRunReader
	Coverage     ClosureCoverageReader
	Issues       ClosureIssueRefReader
	WorkItems    ClosureWorkItemReader
	Versions     VersionReopener
	Tasks        ClosureTaskEnsurer
	Audit        auditapp.Recorder
	Events       eventingapp.Publisher
	IDs          platformid.Generator
	Clock        clock.Clock
}

// ClosureService recomputes the requirement closure predicate on the server,
// writes append-only closure rounds and owns requirement close/reopen state.
type ClosureService struct {
	closures     version.RequirementClosureRepository
	requirements ClosureRequirementReader
	cases        ClosureCaseReader
	runs         ClosureRunReader
	coverage     ClosureCoverageReader
	issues       ClosureIssueRefReader
	work         ClosureWorkItemReader
	versions     VersionReopener
	tasks        ClosureTaskEnsurer
	audit        auditapp.Recorder
	events       eventingapp.Publisher
	ids          platformid.Generator
	clock        clock.Clock
}

// NewClosureService wires the closure service. audit, events and versions may
// be nil in unit tests; the clock defaults to the system clock.
func NewClosureService(deps ClosureServiceDeps) *ClosureService {
	clk := deps.Clock
	if clk == nil {
		clk = clock.System{}
	}
	return &ClosureService{
		closures:     deps.Closures,
		requirements: deps.Requirements,
		cases:        deps.Cases,
		runs:         deps.Runs,
		coverage:     deps.Coverage,
		issues:       deps.Issues,
		work:         deps.WorkItems,
		versions:     deps.Versions,
		tasks:        deps.Tasks,
		audit:        deps.Audit,
		events:       deps.Events,
		ids:          deps.IDs,
		clock:        clk,
	}
}

// Close recomputes the eight closure checks server-side and, when they all
// pass, inserts the next closure round, marks the requirement closed and
// publishes requirement.closed. Repeated Close on an already closed requirement
// is idempotent and returns the newest closure. A rejected proposal returns
// *version.ErrClosePredicateFailed and changes nothing.
func (s *ClosureService) Close(ctx context.Context, requirementID int64, runID *int64) (version.RequirementClosure, error) {
	if requirementID <= 0 {
		return version.RequirementClosure{}, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	if err := s.requireEvaluationDeps(); err != nil {
		return version.RequirementClosure{}, err
	}
	requirement, err := s.requirements.GetRequirement(ctx, requirementID)
	if err != nil {
		return version.RequirementClosure{}, mapRequirementRepositoryError(err)
	}

	if requirement.Status == version.RequirementClosed {
		latest, found, err := s.closures.Latest(ctx, requirementID)
		if err != nil {
			return version.RequirementClosure{}, mapClosureRepositoryError(err)
		}
		if found {
			return latest, nil
		}
		return version.RequirementClosure{}, platform.StateConflict("requirement is already closed")
	}

	checks, input, evidence, err := s.evaluate(ctx, requirement)
	if err != nil {
		return version.RequirementClosure{}, err
	}
	if !checks.Passed() {
		return version.RequirementClosure{}, &version.ErrClosePredicateFailed{
			Failed: version.ClosureFailureCounts(input),
		}
	}

	now := s.clock.Now()
	closedBy := cloneInt64(runID)
	currentVersionID := int64(0)
	if requirement.CurrentVersionID != nil {
		currentVersionID = *requirement.CurrentVersionID
	}
	closure := version.RequirementClosure{
		ID:                   s.ids.Next(),
		RequirementID:        requirement.ID,
		RequirementVersionID: currentVersionID,
		CheckResult:          version.ClosureCheckResult{ClosureChecks: checks},
		EvidenceRefs:         evidence,
		ClosedByRunID:        closedBy,
		ClosedAt:             now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	requirement.Status = version.RequirementClosed
	requirement.ClosedAt = &now
	requirement.ClosedByRunID = closedBy
	requirement.UpdatedAt = now

	stored, err := s.closures.Close(ctx, closure, requirement)
	if err != nil {
		return version.RequirementClosure{}, mapClosureRepositoryError(err)
	}

	requirementIDString := platformid.Format(requirement.ID)
	payload := map[string]any{
		"requirement_id":         requirementIDString,
		"requirement_version_id": platformid.Format(stored.RequirementVersionID),
		"closure_id":             platformid.Format(stored.ID),
		"close_round":            stored.CloseRound,
	}
	if stored.ClosedByRunID != nil {
		payload["closed_by_run_id"] = platformid.Format(*stored.ClosedByRunID)
	}
	s.publishEvent(ctx, requirement.VersionID, closureEventRequirementClosed, payload, map[string]any{
		"requirement_id": requirementIDString,
	})
	s.recordAudit(ctx, "requirement.closed", requirement.ID, map[string]any{
		"requirement_version_id": payload["requirement_version_id"],
		"closure_id":             payload["closure_id"],
		"close_round":            stored.CloseRound,
	})
	return stored, nil
}

// Reopen moves a closed requirement back to draft, increments the newest
// closure's reopen_count, publishes requirement.reopened and best-effort
// reopens a completed version. The requirement's current version is unchanged.
func (s *ClosureService) Reopen(ctx context.Context, requirementID int64, reason string) error {
	if requirementID <= 0 {
		return platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return platform.Invalid("reason", "must not be empty")
	}
	if len([]rune(reason)) > version.RequirementReasonMax {
		return platform.Invalid("reason", fmt.Sprintf("must be at most %d characters", version.RequirementReasonMax))
	}
	if s.requirements == nil || s.closures == nil {
		return errors.New("closure service is missing its requirement repository")
	}
	requirement, err := s.requirements.GetRequirement(ctx, requirementID)
	if err != nil {
		return mapRequirementRepositoryError(err)
	}
	if requirement.Status != version.RequirementClosed ||
		!version.CanTransitionRequirement(requirement.Status, version.RequirementDraft) {
		return platform.StateConflict("only a closed requirement can be reopened")
	}

	now := s.clock.Now()
	requirement.Status = version.RequirementDraft
	requirement.ClosedAt = nil
	requirement.ClosedByRunID = nil
	requirement.UpdatedAt = now
	closure, err := s.closures.Reopen(ctx, requirement, now)
	if err != nil {
		return mapClosureRepositoryError(err)
	}

	requirementIDString := platformid.Format(requirement.ID)
	s.publishEvent(ctx, requirement.VersionID, closureEventRequirementReopened, map[string]any{
		"requirement_id": requirementIDString,
		"from_status":    string(version.RequirementClosed),
		"to_status":      string(version.RequirementDraft),
		"reason":         reason,
	}, map[string]any{"requirement_id": requirementIDString})
	metadata := map[string]any{
		"from_status":  string(version.RequirementClosed),
		"to_status":    string(version.RequirementDraft),
		"reason":       reason,
		"closure_id":   platformid.Format(closure.ID),
		"close_round":  closure.CloseRound,
		"reopen_count": closure.ReopenCount,
	}
	s.recordAudit(ctx, closureEventRequirementReopened, requirement.ID, metadata)

	if s.versions != nil {
		// The closure write already committed; a failed projection must not
		// roll the requirement back. The reconciler retries the version reopen.
		_ = s.versions.ReopenIfCompleted(ctx, requirement.VersionID, closureReopenTrigger, reason)
	}
	return nil
}

// EnsureClosures is the reconciler hook: it scans the clarified requirements
// of one version, recomputes the predicate and idempotently ensures the
// requirement.close task for every requirement whose checks already pass. The
// task targets the requirement's current version and next close round, so its
// dedupe key is revision- and round-aware (req-close:{requirement_id}:
// {requirement_version_id}:{close_round}). The closure task itself is excluded
// from the open-work count.
func (s *ClosureService) EnsureClosures(ctx context.Context, versionID int64) error {
	if versionID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	if s.requirements == nil || s.closures == nil || s.tasks == nil {
		return errors.New("closure service is missing its requirement repository, closure repository or task ensurer")
	}
	var beforeID *int64
	var failures []error
	for {
		items, err := s.requirements.List(ctx, versionID, version.RequirementClarified, ReconcilerLimitMax, beforeID)
		if err != nil {
			return mapRequirementRepositoryError(err)
		}
		for _, item := range items {
			checks, _, _, err := s.evaluate(ctx, item.Requirement)
			if err != nil {
				failures = append(failures, fmt.Errorf("evaluate requirement %d: %w", item.Requirement.ID, err))
				continue
			}
			if !checks.Passed() {
				continue
			}
			closeRound, err := s.nextCloseRound(ctx, item.Requirement.ID)
			if err != nil {
				failures = append(failures, fmt.Errorf("load close round for %d: %w", item.Requirement.ID, err))
				continue
			}
			if err := s.tasks.EnsureRequirementClose(ctx, item.Requirement.ID, item.Current.ID, closeRound); err != nil {
				failures = append(failures, fmt.Errorf("ensure requirement.close for %d: %w", item.Requirement.ID, err))
			}
		}
		if len(items) < ReconcilerLimitMax {
			break
		}
		lastID := items[len(items)-1].Requirement.ID
		beforeID = &lastID
	}
	return errors.Join(failures...)
}

// nextCloseRound returns the round the next closure of one requirement will be
// assigned: the newest recorded round plus one, or 1 when it was never closed.
func (s *ClosureService) nextCloseRound(ctx context.Context, requirementID int64) (int32, error) {
	latest, found, err := s.closures.Latest(ctx, requirementID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 1, nil
	}
	return int32(latest.CloseRound + 1), nil
}

// evaluate loads the ports and recomputes the checks plus the persisted
// evidence refs. The requirement.close task is subtracted from the open work
// count here (self-task exclusion).
func (s *ClosureService) evaluate(ctx context.Context, requirement version.Requirement) (version.ClosureChecks, version.ClosureInput, []version.ClosureEvidenceRef, error) {
	if err := s.requireEvaluationDeps(); err != nil {
		return version.ClosureChecks{}, version.ClosureInput{}, nil, err
	}
	caseVersions, err := s.cases.CurrentConfirmedCases(ctx, requirement.ID)
	if err != nil {
		return version.ClosureChecks{}, version.ClosureInput{}, nil, fmt.Errorf("closure requirement %d: load case versions: %w", requirement.ID, err)
	}
	latestRuns, err := s.runs.LatestCaseRuns(ctx, requirement.ID)
	if err != nil {
		return version.ClosureChecks{}, version.ClosureInput{}, nil, fmt.Errorf("closure requirement %d: load case runs: %w", requirement.ID, err)
	}
	issueRefsByRun, err := s.issues.CaseRunIssueRefs(ctx, requirement.ID)
	if err != nil {
		return version.ClosureChecks{}, version.ClosureInput{}, nil, fmt.Errorf("closure requirement %d: load issue refs: %w", requirement.ID, err)
	}
	workCounts, err := s.work.OpenWorkItems(ctx, requirement.ID)
	if err != nil {
		return version.ClosureChecks{}, version.ClosureInput{}, nil, fmt.Errorf("closure requirement %d: load work items: %w", requirement.ID, err)
	}
	activeIntents, err := s.runs.CountActiveIntents(ctx, requirement.ID)
	if err != nil {
		return version.ClosureChecks{}, version.ClosureInput{}, nil, fmt.Errorf("closure requirement %d: count intents: %w", requirement.ID, err)
	}
	inflightRuns, err := s.runs.CountInflightCaseRuns(ctx, requirement.ID)
	if err != nil {
		return version.ClosureChecks{}, version.ClosureInput{}, nil, fmt.Errorf("closure requirement %d: count case runs: %w", requirement.ID, err)
	}
	coverageRecorded, err := s.coverage.CoverageRecorded(ctx, requirement.ID)
	if err != nil {
		return version.ClosureChecks{}, version.ClosureInput{}, nil, fmt.Errorf("closure requirement %d: load coverage: %w", requirement.ID, err)
	}

	issueRefs := make(map[int64]bool, len(issueRefsByRun))
	for runID := range issueRefsByRun {
		issueRefs[runID] = true
	}
	openWorkItems := workCounts.Total - workCounts.RequirementClose
	if openWorkItems < 0 {
		openWorkItems = 0
	}
	input := version.ClosureInput{
		Requirement:      requirement,
		CaseVersions:     caseVersions,
		LatestRuns:       latestRuns,
		IssueRefs:        issueRefs,
		OpenWorkItems:    openWorkItems,
		ActiveIntents:    activeIntents,
		InflightCaseRuns: inflightRuns,
		CoverageComplete: coverageRecorded,
	}
	checks := version.EvaluateRequirementClosure(input)
	return checks, input, closureEvidence(caseVersions, latestRuns, issueRefsByRun), nil
}

func (s *ClosureService) requireEvaluationDeps() error {
	switch {
	case s.requirements == nil:
		return errors.New("closure service is missing its requirement repository")
	case s.closures == nil:
		return errors.New("closure service is missing its closure repository")
	case s.cases == nil:
		return errors.New("closure service is missing its case reader")
	case s.runs == nil:
		return errors.New("closure service is missing its execution reader")
	case s.coverage == nil:
		return errors.New("closure service is missing its coverage reader")
	case s.issues == nil:
		return errors.New("closure service is missing its issue reader")
	case s.work == nil:
		return errors.New("closure service is missing its work item reader")
	case s.ids == nil:
		return errors.New("closure service is missing its ID generator")
	default:
		return nil
	}
}

// closureEvidence collects the latest case_run refs and the issue refs of the
// failed application runs. The result is sorted so the persisted JSON is
// deterministic.
func closureEvidence(caseVersions []version.CaseVersionSnapshot, latestRuns map[int64]version.CaseRunSnapshot, issueRefs map[int64][]int64) []version.ClosureEvidenceRef {
	refs := make([]version.ClosureEvidenceRef, 0, len(caseVersions))
	seen := make(map[string]struct{}, len(caseVersions))
	add := func(kind string, id int64) {
		key := kind + ":" + platformid.Format(id)
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		refs = append(refs, version.ClosureEvidenceRef{Kind: kind, ID: platformid.Format(id)})
	}
	for _, caseVersion := range caseVersions {
		run, ok := latestRuns[caseVersion.CaseVersionID]
		if !ok {
			continue
		}
		add(version.ClosureEvidenceCaseRun, run.CaseRunID)
		for _, issueID := range issueRefs[run.CaseRunID] {
			add(version.ClosureEvidenceIssue, issueID)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		return refs[i].ID < refs[j].ID
	})
	return refs
}

func (s *ClosureService) publishEvent(ctx context.Context, versionID int64, eventType string, payload, scope map[string]any) {
	if s.events == nil {
		return
	}
	mergedScope := map[string]any{"version_id": platformid.Format(versionID)}
	for key, value := range scope {
		mergedScope[key] = value
	}
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      eventType,
		Scope:     mergedScope,
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

func (s *ClosureService) recordAudit(ctx context.Context, action string, requirementID int64, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	// The manual API runs with an authenticated actor in the context; agent-run
	// closures/reopens carry no HTTP actor and leave it nil.
	var actorID *int64
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		id := actor.ID
		actorID = &id
	}
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actorID,
		Action:     action,
		TargetKind: "requirement",
		TargetID:   platformid.Format(requirementID),
		RequestID:  requestid.From(ctx),
		Metadata:   metadata,
	})
}

func mapClosureRepositoryError(err error) error {
	switch {
	case errors.Is(err, version.ErrRequirementNotFound):
		return platform.NotFound("requirement")
	case errors.Is(err, version.ErrStateConflict):
		return platform.StateConflict("requirement is not in a valid closure state")
	default:
		return err
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
