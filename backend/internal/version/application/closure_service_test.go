package versionapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- fake closure repository ----------

type fakeClosureRepo struct {
	requirements *fakeRequirementRepo
	closures     map[int64][]version.RequirementClosure
	closeCalls   int
	reopenCalls  int
	latestErr    error
	closeErr     error
	reopenErr    error
}

func (f *fakeClosureRepo) Latest(_ context.Context, requirementID int64) (version.RequirementClosure, bool, error) {
	if f.latestErr != nil {
		return version.RequirementClosure{}, false, f.latestErr
	}
	closures := f.closures[requirementID]
	if len(closures) == 0 {
		return version.RequirementClosure{}, false, nil
	}
	return closures[len(closures)-1], true, nil
}

func (f *fakeClosureRepo) Close(_ context.Context, closure version.RequirementClosure, requirement version.Requirement) (version.RequirementClosure, error) {
	f.closeCalls++
	if f.closeErr != nil {
		return version.RequirementClosure{}, f.closeErr
	}
	stored, ok := f.requirements.requirements[requirement.ID]
	if !ok {
		return version.RequirementClosure{}, version.ErrRequirementNotFound
	}
	if stored.Status != version.RequirementClarified {
		return version.RequirementClosure{}, version.ErrStateConflict
	}
	closure.CloseRound = len(f.closures[requirement.ID]) + 1
	f.closures[requirement.ID] = append(f.closures[requirement.ID], closure)
	f.requirements.requirements[requirement.ID] = requirement
	return closure, nil
}

func (f *fakeClosureRepo) Reopen(_ context.Context, requirement version.Requirement, reopenedAt time.Time) (version.RequirementClosure, error) {
	f.reopenCalls++
	if f.reopenErr != nil {
		return version.RequirementClosure{}, f.reopenErr
	}
	closures := f.closures[requirement.ID]
	if len(closures) == 0 {
		return version.RequirementClosure{}, version.ErrStateConflict
	}
	latest := closures[len(closures)-1]
	latest.ReopenCount++
	latest.UpdatedAt = reopenedAt
	closures[len(closures)-1] = latest
	f.closures[requirement.ID] = closures
	f.requirements.requirements[requirement.ID] = requirement
	return latest, nil
}

// ---------- fake consumer ports ----------

type fakeClosureCases struct {
	byRequirement map[int64][]version.CaseVersionSnapshot
	err           error
}

func (f *fakeClosureCases) CurrentConfirmedCases(_ context.Context, requirementID int64) ([]version.CaseVersionSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byRequirement[requirementID], nil
}

type fakeClosureRuns struct {
	latest   map[int64]map[int64]version.CaseRunSnapshot
	intents  map[int64]int
	inflight map[int64]int
	err      error
}

func (f *fakeClosureRuns) LatestCaseRuns(_ context.Context, requirementID int64) (map[int64]version.CaseRunSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.latest[requirementID], nil
}

func (f *fakeClosureRuns) CountActiveIntents(_ context.Context, requirementID int64) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.intents[requirementID], nil
}

func (f *fakeClosureRuns) CountInflightCaseRuns(_ context.Context, requirementID int64) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.inflight[requirementID], nil
}

type fakeClosureCoverage struct {
	recorded map[int64]bool
	err      error
}

func (f *fakeClosureCoverage) CoverageRecorded(_ context.Context, requirementID int64) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.recorded[requirementID], nil
}

type fakeClosureIssues struct {
	refs map[int64]map[int64][]int64
	err  error
}

func (f *fakeClosureIssues) CaseRunIssueRefs(_ context.Context, requirementID int64) (map[int64][]int64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.refs[requirementID], nil
}

type fakeClosureWork struct {
	counts map[int64]versionapp.OpenWorkItemCounts
	err    error
}

func (f *fakeClosureWork) OpenWorkItems(_ context.Context, requirementID int64) (versionapp.OpenWorkItemCounts, error) {
	if f.err != nil {
		return versionapp.OpenWorkItemCounts{}, f.err
	}
	return f.counts[requirementID], nil
}

type versionReopenCall struct {
	versionID int64
	trigger   string
	reason    string
}

type fakeVersionReopener struct {
	calls []versionReopenCall
	err   error
}

func (f *fakeVersionReopener) ReopenIfCompleted(_ context.Context, versionID int64, trigger, reason string) error {
	f.calls = append(f.calls, versionReopenCall{versionID: versionID, trigger: trigger, reason: reason})
	return f.err
}

type closureTaskCall struct {
	requirementID        int64
	requirementVersionID int64
	closeRound           int32
}

type fakeClosureTasks struct {
	calls []closureTaskCall
	err   error
}

func (f *fakeClosureTasks) EnsureRequirementClose(_ context.Context, requirementID, requirementVersionID int64, closeRound int32) error {
	f.calls = append(f.calls, closureTaskCall{
		requirementID:        requirementID,
		requirementVersionID: requirementVersionID,
		closeRound:           closeRound,
	})
	return f.err
}

// ---------- closure test environment ----------

type closureEnv struct {
	svc          *versionapp.ClosureService
	requirements *fakeRequirementRepo
	closures     *fakeClosureRepo
	cases        *fakeClosureCases
	runs         *fakeClosureRuns
	coverage     *fakeClosureCoverage
	issues       *fakeClosureIssues
	work         *fakeClosureWork
	versions     *fakeVersionReopener
	tasks        *fakeClosureTasks
	audit        *fakeAudit
	events       *fakePublisher
}

func newClosureEnv(t *testing.T) *closureEnv {
	t.Helper()
	requirements := newFakeRequirementRepo()
	closures := &fakeClosureRepo{requirements: requirements, closures: map[int64][]version.RequirementClosure{}}
	cases := &fakeClosureCases{byRequirement: map[int64][]version.CaseVersionSnapshot{}}
	runs := &fakeClosureRuns{
		latest:   map[int64]map[int64]version.CaseRunSnapshot{},
		intents:  map[int64]int{},
		inflight: map[int64]int{},
	}
	coverage := &fakeClosureCoverage{recorded: map[int64]bool{}}
	issues := &fakeClosureIssues{refs: map[int64]map[int64][]int64{}}
	work := &fakeClosureWork{counts: map[int64]versionapp.OpenWorkItemCounts{}}
	versions := &fakeVersionReopener{}
	tasks := &fakeClosureTasks{}
	audit := &fakeAudit{}
	events := &fakePublisher{}
	svc := versionapp.NewClosureService(versionapp.ClosureServiceDeps{
		Closures:     closures,
		Requirements: requirements,
		Cases:        cases,
		Runs:         runs,
		Coverage:     coverage,
		Issues:       issues,
		WorkItems:    work,
		Versions:     versions,
		Tasks:        tasks,
		Audit:        audit,
		Events:       events,
		IDs:          &seqIDs{},
		Clock:        fixedClock{at: testTime},
	})
	return &closureEnv{
		svc: svc, requirements: requirements, closures: closures, cases: cases,
		runs: runs, coverage: coverage, issues: issues, work: work,
		versions: versions, tasks: tasks, audit: audit, events: events,
	}
}

func seedClosureRequirement(env *closureEnv, requirementID, versionID, currentVersionID int64, status version.RequirementStatus) version.Requirement {
	current := currentVersionID
	requirement := version.Requirement{
		ID:               requirementID,
		VersionID:        versionID,
		CurrentVersionID: &current,
		Status:           status,
		CreatedAt:        testTime,
		UpdatedAt:        testTime,
	}
	env.requirements.seedRequirement(requirement, version.RequirementVersion{
		ID: current, RequirementID: requirementID, Revision: 1, Title: "requirement",
		Priority: "P1", CreatedAt: testTime,
	})
	return requirement
}

// configurePassingClosure makes requirementID closable: one confirmed case with
// a latest passed run, recorded coverage and only its own close task open.
func configurePassingClosure(env *closureEnv, requirementID int64) {
	env.cases.byRequirement[requirementID] = []version.CaseVersionSnapshot{{
		CaseVersionID: 1101, CaseID: 1100, RequirementID: requirementID,
		RequirementVersionID: 1002, Title: "Checkout applies a coupon",
	}}
	env.runs.latest[requirementID] = map[int64]version.CaseRunSnapshot{
		1101: {CaseRunID: 1201, CaseVersionID: 1101, Status: version.CaseRunPassed},
	}
	env.coverage.recorded[requirementID] = true
	env.work.counts[requirementID] = versionapp.OpenWorkItemCounts{Total: 1, RequirementClose: 1}
}

func closureEventTypes(events []eventingapp.Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func TestClosureServiceCloseWritesClosureClosedStateAndEvent(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)
	runID := int64(9001)

	closure, err := env.svc.Close(requestContext(), requirement.ID, &runID)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closure.ID <= 0 || closure.RequirementID != requirement.ID || closure.RequirementVersionID != 1002 {
		t.Fatalf("closure identity = %#v", closure)
	}
	if closure.CloseRound != 1 {
		t.Fatalf("close round = %d, want 1", closure.CloseRound)
	}
	if !closure.CheckResult.Passed() || closure.CheckResult.Waived {
		t.Fatalf("check result = %#v", closure.CheckResult)
	}
	if closure.ClosedByRunID == nil || *closure.ClosedByRunID != runID {
		t.Fatalf("closed by run = %v", closure.ClosedByRunID)
	}
	if closure.ClosedAt != testTime || !closure.CreatedAt.Equal(testTime) {
		t.Fatalf("closure timestamps = %v/%v", closure.ClosedAt, closure.CreatedAt)
	}
	if len(closure.EvidenceRefs) != 1 || closure.EvidenceRefs[0].Kind != version.ClosureEvidenceCaseRun ||
		closure.EvidenceRefs[0].ID != "1201" {
		t.Fatalf("evidence refs = %#v", closure.EvidenceRefs)
	}
	if env.closures.closeCalls != 1 {
		t.Fatalf("close calls = %d", env.closures.closeCalls)
	}

	stored := env.requirements.requirements[requirement.ID]
	if stored.Status != version.RequirementClosed {
		t.Fatalf("requirement status = %q", stored.Status)
	}
	if stored.ClosedAt == nil || !stored.ClosedAt.Equal(testTime) {
		t.Fatalf("requirement closed_at = %v", stored.ClosedAt)
	}
	if stored.ClosedByRunID == nil || *stored.ClosedByRunID != runID {
		t.Fatalf("requirement closed_by_run_id = %v", stored.ClosedByRunID)
	}

	if got := closureEventTypes(env.events.events); len(got) != 1 || got[0] != "requirement.closed" {
		t.Fatalf("events = %#v", got)
	}
	event := env.events.events[0]
	if event.Channel != "version:701" {
		t.Fatalf("event channel = %q", event.Channel)
	}
	if event.Scope["version_id"] != "701" || event.Scope["requirement_id"] != "1001" {
		t.Fatalf("event scope = %#v", event.Scope)
	}
	if event.Payload["closure_id"] != idString(closure.ID) || event.Payload["close_round"] != 1 ||
		event.Payload["requirement_version_id"] != "1002" || event.Payload["closed_by_run_id"] != "9001" {
		t.Fatalf("event payload = %#v", event.Payload)
	}
	if _, leaked := event.Payload["check_result"]; leaked {
		t.Fatalf("event payload leaked check_result: %#v", event.Payload)
	}
	if !containsAll(auditActions(env.audit.events), "requirement.closed") {
		t.Fatalf("audit actions = %#v", auditActions(env.audit.events))
	}
}

func TestClosureServiceClosePredicateFailureKeepsState(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	env.cases.byRequirement[requirement.ID] = []version.CaseVersionSnapshot{{
		CaseVersionID: 1101, RequirementVersionID: 1002,
	}}
	env.runs.latest[requirement.ID] = map[int64]version.CaseRunSnapshot{
		1101: {CaseRunID: 1201, CaseVersionID: 1101, Status: version.CaseRunFailed, FailureClass: version.FailureClassApplication},
	}
	env.coverage.recorded[requirement.ID] = false
	env.work.counts[requirement.ID] = versionapp.OpenWorkItemCounts{Total: 2, RequirementClose: 1}

	_, err := env.svc.Close(requestContext(), requirement.ID, nil)
	var predicateErr *version.ErrClosePredicateFailed
	if !errors.As(err, &predicateErr) {
		t.Fatalf("Close error = %v, want ErrClosePredicateFailed", err)
	}
	wantFailed := map[string]int{
		version.ClosureCheckFailedCaseHasIssue: 1,
		version.ClosureCheckNoOpenWork:         1,
		version.ClosureCheckCoverageRecorded:   1,
	}
	for name, count := range wantFailed {
		if predicateErr.Failed[name] != count {
			t.Fatalf("failed[%q] = %d, want %d (%#v)", name, predicateErr.Failed[name], count, predicateErr.Failed)
		}
	}
	if env.closures.closeCalls != 0 {
		t.Fatalf("close calls = %d, want 0", env.closures.closeCalls)
	}
	if stored := env.requirements.requirements[requirement.ID]; stored.Status != version.RequirementClarified {
		t.Fatalf("requirement status = %q", stored.Status)
	}
	if len(env.events.events) != 0 || len(env.audit.events) != 0 {
		t.Fatalf("failure wrote side effects: %#v/%#v", env.events.events, env.audit.events)
	}

	// A missing issue ref is part of the failed detail, not a 5xx.
	if !strings.Contains(predicateErr.Error(), version.ClosureCheckFailedCaseHasIssue) {
		t.Fatalf("error message = %q", predicateErr.Error())
	}
}

func TestClosureServiceCloseIsIdempotentForClosedRequirement(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClosed)
	existing := version.RequirementClosure{
		ID: 8001, RequirementID: requirement.ID, RequirementVersionID: 1002,
		CloseRound: 1, CheckResult: version.ClosureCheckResult{Waived: false},
		ClosedAt: testTime, CreatedAt: testTime, UpdatedAt: testTime,
	}
	env.closures.closures[requirement.ID] = []version.RequirementClosure{existing}

	closure, err := env.svc.Close(requestContext(), requirement.ID, nil)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closure.ID != existing.ID || closure.CloseRound != existing.CloseRound {
		t.Fatalf("closure = %#v, want existing", closure)
	}
	if env.closures.closeCalls != 0 || len(env.events.events) != 0 || len(env.audit.events) != 0 {
		t.Fatalf("idempotent close wrote side effects")
	}
}

func TestClosureServiceReopenResetsStateAndStartsNewRound(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)
	if _, err := env.svc.Close(requestContext(), requirement.ID, nil); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	if err := env.svc.Reopen(requestContext(), requirement.ID, "customer renegotiated"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	stored := env.requirements.requirements[requirement.ID]
	if stored.Status != version.RequirementDraft {
		t.Fatalf("status = %q, want draft", stored.Status)
	}
	if stored.ClosedAt != nil || stored.ClosedByRunID != nil {
		t.Fatalf("closed fields = %v/%v", stored.ClosedAt, stored.ClosedByRunID)
	}
	closures := env.closures.closures[requirement.ID]
	if len(closures) != 1 || closures[0].ReopenCount != 1 {
		t.Fatalf("closures = %#v", closures)
	}
	if got := closureEventTypes(env.events.events); !containsAll(got, "requirement.closed", "requirement.reopened") {
		t.Fatalf("events = %#v", got)
	}
	reopened := env.events.events[len(env.events.events)-1]
	if reopened.Type != "requirement.reopened" || reopened.Payload["from_status"] != "closed" ||
		reopened.Payload["to_status"] != "draft" || reopened.Payload["reason"] != "customer renegotiated" {
		t.Fatalf("reopened event = %#v", reopened)
	}
	if len(env.versions.calls) != 1 {
		t.Fatalf("version reopen calls = %#v", env.versions.calls)
	}
	if call := env.versions.calls[0]; call.versionID != 701 || call.trigger != "requirement_reopened" ||
		call.reason != "customer renegotiated" {
		t.Fatalf("version reopen call = %#v", call)
	}

	// Reopening a non-closed requirement is a state conflict.
	if err := env.svc.Reopen(requestContext(), requirement.ID, "again"); err == nil {
		t.Fatal("reopen of a draft requirement succeeded")
	} else if apiError(t, err).Status != 409 {
		t.Fatalf("reopen conflict status = %d", apiError(t, err).Status)
	}

	// The next closure after re-clarification uses close_round+1.
	env.requirements.requirements[requirement.ID] = withRequirementStatus(stored, version.RequirementClarified)
	closure, err := env.svc.Close(requestContext(), requirement.ID, nil)
	if err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if closure.CloseRound != 2 {
		t.Fatalf("second close round = %d, want 2", closure.CloseRound)
	}
	if len(env.closures.closures[requirement.ID]) != 2 {
		t.Fatalf("closure rounds = %#v", env.closures.closures[requirement.ID])
	}
}

func TestClosureServiceReopenAuditsAuthenticatedActor(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)
	if _, err := env.svc.Close(requestContext(), requirement.ID, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	env.audit.events = nil

	ctx := platform.WithActor(requestContext(), platform.Actor{ID: 88, Role: "user"})
	if err := env.svc.Reopen(ctx, requirement.ID, "manual reopen"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if len(env.audit.events) != 1 || env.audit.events[0].Action != "requirement.reopened" {
		t.Fatalf("audit events = %#v", env.audit.events)
	}
	if env.audit.events[0].ActorID == nil || *env.audit.events[0].ActorID != 88 {
		t.Fatalf("audit actor = %v", env.audit.events[0].ActorID)
	}
}

func withRequirementStatus(requirement version.Requirement, status version.RequirementStatus) version.Requirement {
	requirement.Status = status
	requirement.UpdatedAt = testTime.Add(time.Minute)
	return requirement
}

func TestClosureServiceReopenBestEffortVersionProjection(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)
	if _, err := env.svc.Close(requestContext(), requirement.ID, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	env.versions.err = errors.New("version store down")

	if err := env.svc.Reopen(requestContext(), requirement.ID, "retry later"); err != nil {
		t.Fatalf("Reopen should not fail on a version projection error: %v", err)
	}
	if env.requirements.requirements[requirement.ID].Status != version.RequirementDraft {
		t.Fatalf("requirement was not reopened")
	}
}

func TestClosureServiceEnsureClosuresEnsuresOnlyEligibleRequirements(t *testing.T) {
	env := newClosureEnv(t)
	eligible := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	blocked := seedClosureRequirement(env, 1002, 701, 1003, version.RequirementClarified)
	closed := seedClosureRequirement(env, 1003, 701, 1004, version.RequirementClosed)
	configurePassingClosure(env, eligible.ID)
	env.cases.byRequirement[blocked.ID] = []version.CaseVersionSnapshot{{CaseVersionID: 2101, RequirementVersionID: 1003}}
	env.runs.latest[blocked.ID] = map[int64]version.CaseRunSnapshot{
		2101: {CaseRunID: 2201, CaseVersionID: 2101, Status: version.CaseRunPassed},
	}
	env.coverage.recorded[blocked.ID] = true
	env.work.counts[blocked.ID] = versionapp.OpenWorkItemCounts{Total: 1, RequirementClose: 0}
	env.closures.closures[closed.ID] = []version.RequirementClosure{{ID: 3001, RequirementID: closed.ID, CloseRound: 1}}

	if err := env.svc.EnsureClosures(requestContext(), 701); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if len(env.tasks.calls) != 1 {
		t.Fatalf("task ensures = %#v, want one eligible call", env.tasks.calls)
	}
	want := closureTaskCall{requirementID: eligible.ID, requirementVersionID: 1002, closeRound: 1}
	if call := env.tasks.calls[0]; call != want {
		t.Fatalf("task ensure = %#v, want %#v", call, want)
	}
}

func TestClosureServiceEnsureClosuresUsesCurrentRequirementVersion(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1005, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)

	if err := env.svc.EnsureClosures(requestContext(), 701); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if len(env.tasks.calls) != 1 || env.tasks.calls[0].requirementVersionID != 1005 {
		t.Fatalf("task ensures = %#v, want requirement version 1005", env.tasks.calls)
	}
}

func TestClosureServiceEnsureClosuresUsesNextCloseRound(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)
	env.closures.closures[requirement.ID] = []version.RequirementClosure{{
		ID: 3001, RequirementID: requirement.ID, RequirementVersionID: 1002, CloseRound: 1,
	}}

	if err := env.svc.EnsureClosures(requestContext(), 701); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if len(env.tasks.calls) != 1 || env.tasks.calls[0].closeRound != 2 {
		t.Fatalf("task ensures = %#v, want close round 2", env.tasks.calls)
	}
}

func TestClosureServiceEnsureClosuresReportsClosureLookupErrors(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)
	wantErr := errors.New("closure store down")
	env.closures.latestErr = wantErr

	err := env.svc.EnsureClosures(requestContext(), 701)
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnsureClosures error = %v, want %v", err, wantErr)
	}
	if len(env.tasks.calls) != 0 {
		t.Fatalf("task ensures = %#v, want none", env.tasks.calls)
	}
}

func TestClosureServiceEnsureClosuresReportsTaskErrors(t *testing.T) {
	env := newClosureEnv(t)
	eligible := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, eligible.ID)
	wantErr := errors.New("task board down")
	env.tasks.err = wantErr

	err := env.svc.EnsureClosures(requestContext(), 701)
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnsureClosures error = %v, want %v", err, wantErr)
	}
}

func TestClosureServiceValidatesInputs(t *testing.T) {
	env := newClosureEnv(t)
	if _, err := env.svc.Close(requestContext(), 0, nil); err == nil {
		t.Fatal("Close accepted requirement ID 0")
	} else if field := fieldName(t, err); field != "requirement_id" {
		t.Fatalf("field = %q", field)
	}
	if err := env.svc.Reopen(requestContext(), 0, "reason"); err == nil {
		t.Fatal("Reopen accepted requirement ID 0")
	} else if field := fieldName(t, err); field != "requirement_id" {
		t.Fatalf("field = %q", field)
	}
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClosed)
	if err := env.svc.Reopen(requestContext(), requirement.ID, "   "); err == nil {
		t.Fatal("Reopen accepted a blank reason")
	} else if field := fieldName(t, err); field != "reason" {
		t.Fatalf("field = %q", field)
	}
	if err := env.svc.EnsureClosures(requestContext(), 0); err == nil {
		t.Fatal("EnsureClosures accepted version ID 0")
	}
	if _, err := env.svc.Close(requestContext(), 9999, nil); err == nil {
		t.Fatal("Close accepted a missing requirement")
	} else {
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 404 {
			t.Fatalf("missing requirement error = %v", err)
		}
	}
}
