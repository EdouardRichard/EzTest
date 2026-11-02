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

// ---------- fake version close repository ----------

type fakeVersionCloseRepo struct {
	versions     map[int64]version.Version
	closeCalls   int
	recheckCalls int
	reopenCalls  int
	closeErr     error
	reopenErr    error
	lockedHook   func(ctx context.Context, locked version.Version)
}

func newFakeVersionCloseRepo() *fakeVersionCloseRepo {
	return &fakeVersionCloseRepo{versions: map[int64]version.Version{}}
}

func (f *fakeVersionCloseRepo) Get(_ context.Context, id int64) (version.Version, []version.Binding, error) {
	stored, ok := f.versions[id]
	if !ok {
		return version.Version{}, nil, version.ErrNotFound
	}
	return stored, nil, nil
}

func (f *fakeVersionCloseRepo) Close(ctx context.Context, id int64, fields version.VersionCloseFields, recheck func(context.Context, version.Version) error) (version.Version, error) {
	f.closeCalls++
	if f.closeErr != nil {
		return version.Version{}, f.closeErr
	}
	stored, ok := f.versions[id]
	if !ok {
		return version.Version{}, version.ErrNotFound
	}
	if stored.Status != version.StatusRunning {
		return version.Version{}, version.ErrStateConflict
	}
	if f.lockedHook != nil {
		f.lockedHook(ctx, stored)
	}
	f.recheckCalls++
	if recheck != nil {
		if err := recheck(ctx, stored); err != nil {
			return version.Version{}, err
		}
	}
	stored.Status = version.StatusCompleted
	stored.ClosedAt = &fields.ClosedAt
	stored.CloseReason = fields.CloseReason
	stored.CloseRunID = cloneCloseInt64(fields.CloseRunID)
	stored.UpdatedAt = fields.ClosedAt
	f.versions[id] = stored
	return stored, nil
}

func (f *fakeVersionCloseRepo) ReopenIfCompleted(_ context.Context, id int64, reopenedAt time.Time) (version.Version, bool, error) {
	f.reopenCalls++
	if f.reopenErr != nil {
		return version.Version{}, false, f.reopenErr
	}
	stored, ok := f.versions[id]
	if !ok {
		return version.Version{}, false, version.ErrNotFound
	}
	if stored.Status != version.StatusCompleted {
		return stored, false, nil
	}
	stored.Status = version.StatusRunning
	stored.ClosedAt = nil
	stored.CloseReason = ""
	stored.CloseRunID = nil
	stored.UpdatedAt = reopenedAt
	f.versions[id] = stored
	return stored, true, nil
}

func cloneCloseInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// ---------- fake consumer ports ----------

type fakeVersionCloseWorkItems struct {
	counts versionapp.BlockingWorkItemCounts
	err    error
}

func (f *fakeVersionCloseWorkItems) BlockingWorkItems(_ context.Context, _ int64) (versionapp.BlockingWorkItemCounts, error) {
	if f.err != nil {
		return versionapp.BlockingWorkItemCounts{}, f.err
	}
	return f.counts, nil
}

type fakeVersionCloseExecution struct {
	intents  int
	waves    int
	inflight int
	err      error
}

func (f *fakeVersionCloseExecution) CountActiveIntents(_ context.Context, _ int64) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.intents, nil
}

func (f *fakeVersionCloseExecution) CountActiveWaves(_ context.Context, _ int64) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.waves, nil
}

func (f *fakeVersionCloseExecution) CountInflightCaseRuns(_ context.Context, _ int64) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.inflight, nil
}

type fakeVersionCloseHumanRequests struct {
	open int
	err  error
}

func (f *fakeVersionCloseHumanRequests) CountOpenHumanRequests(_ context.Context, _ int64) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.open, nil
}

type fakeVersionCloseRequirements struct {
	items []version.Requirement
	err   error
}

func (f *fakeVersionCloseRequirements) ListByVersion(_ context.Context, _ int64) ([]version.Requirement, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]version.Requirement{}, f.items...), nil
}

type fakeVersionReportStatus struct {
	report version.ReportStatus
	err    error
	calls  int
}

func (f *fakeVersionReportStatus) VersionReportStatus(_ context.Context, _ int64) (version.ReportStatus, error) {
	f.calls++
	if f.err != nil {
		return version.ReportStatus{}, f.err
	}
	return f.report, nil
}

type reportStaleCall struct {
	versionID int64
	reason    string
}

type fakeVersionReportStaler struct {
	calls []reportStaleCall
	err   error
}

func (f *fakeVersionReportStaler) MarkVersionReportStale(_ context.Context, versionID int64, reason string) error {
	f.calls = append(f.calls, reportStaleCall{versionID: versionID, reason: reason})
	return f.err
}

type versionCloseTaskCall struct {
	versionID            int64
	reportRevisionID     int64
	predicateFingerprint string
}

type fakeVersionCloseTasks struct {
	calls []versionCloseTaskCall
	err   error
}

func (f *fakeVersionCloseTasks) EnsureVersionClose(_ context.Context, versionID, reportRevisionID int64, predicateFingerprint string) error {
	f.calls = append(f.calls, versionCloseTaskCall{
		versionID:            versionID,
		reportRevisionID:     reportRevisionID,
		predicateFingerprint: predicateFingerprint,
	})
	return f.err
}

// ---------- environment ----------

type versionCloseEnv struct {
	svc          *versionapp.VersionCloseService
	versions     *fakeVersionCloseRepo
	work         *fakeVersionCloseWorkItems
	execution    *fakeVersionCloseExecution
	human        *fakeVersionCloseHumanRequests
	requirements *fakeVersionCloseRequirements
	reports      *fakeVersionReportStatus
	staler       *fakeVersionReportStaler
	tasks        *fakeVersionCloseTasks
	audit        *fakeAudit
	events       *fakePublisher
}

func newVersionCloseEnv(t *testing.T) *versionCloseEnv {
	t.Helper()
	versions := newFakeVersionCloseRepo()
	work := &fakeVersionCloseWorkItems{}
	execution := &fakeVersionCloseExecution{}
	human := &fakeVersionCloseHumanRequests{}
	requirements := &fakeVersionCloseRequirements{}
	reports := &fakeVersionReportStatus{report: publishedVersionReportStatus()}
	staler := &fakeVersionReportStaler{}
	tasks := &fakeVersionCloseTasks{}
	audit := &fakeAudit{}
	events := &fakePublisher{}
	svc := versionapp.NewVersionCloseService(versionapp.VersionCloseServiceDeps{
		Versions:      versions,
		WorkItems:     work,
		Execution:     execution,
		HumanRequests: human,
		Requirements:  requirements,
		Reports:       reports,
		ReportStaler:  staler,
		Tasks:         tasks,
		Audit:         audit,
		Events:        events,
		Clock:         fixedClock{at: testTime},
	})
	return &versionCloseEnv{
		svc: svc, versions: versions, work: work, execution: execution,
		human: human, requirements: requirements, reports: reports,
		staler: staler, tasks: tasks, audit: audit, events: events,
	}
}

func publishedVersionReportStatus() version.ReportStatus {
	fingerprint := strings.Repeat("a", 64)
	return version.ReportStatus{
		Status:             version.ReportStatusPublished,
		Fingerprint:        fingerprint,
		RevisionID:         9001,
		CurrentFingerprint: fingerprint,
	}
}

func seedVersionCloseVersion(env *versionCloseEnv, id int64, status string) version.Version {
	stored := version.Version{
		ID:        id,
		Title:     "Orders Q4",
		Status:    status,
		CreatedBy: 42,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
	env.versions.versions[id] = stored
	return stored
}

// configurePassingVersionClose makes the seeded version closable: every
// requirement terminal, no execution/human blockers and a current published
// version report.
func configurePassingVersionClose(env *versionCloseEnv, versionID int64) {
	seedVersionCloseVersion(env, versionID, version.StatusRunning)
	env.requirements.items = []version.Requirement{
		{ID: 1001, VersionID: versionID, Status: version.RequirementClosed},
		{ID: 1002, VersionID: versionID, Status: version.RequirementObsolete},
	}
	env.reports.report = publishedVersionReportStatus()
}

func versionCloseEventTypes(events []eventingapp.Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func versionCloseEventChannels(events []eventingapp.Event) []string {
	channels := make([]string, 0, len(events))
	for _, event := range events {
		channels = append(channels, event.Channel)
	}
	return channels
}

// ---------- tests ----------

func TestVersionCloseServiceCloseWritesProjectionEventAndAudit(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)
	runID := int64(9001)
	ctx := platform.WithActor(requestContext(), platform.Actor{ID: 77, Role: "user"})

	closed, err := env.svc.Close(ctx, 701, runID, "all delivery work is done")
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closed.ID != 701 || closed.Status != version.StatusCompleted {
		t.Fatalf("closed version = %#v", closed)
	}
	if closed.ClosedAt == nil || !closed.ClosedAt.Equal(testTime) {
		t.Fatalf("closed_at = %v", closed.ClosedAt)
	}
	if closed.CloseReason != "all delivery work is done" {
		t.Fatalf("close_reason = %q", closed.CloseReason)
	}
	if closed.CloseRunID == nil || *closed.CloseRunID != runID {
		t.Fatalf("close_run_id = %v", closed.CloseRunID)
	}
	if env.versions.closeCalls != 1 || env.versions.recheckCalls != 1 {
		t.Fatalf("close/recheck calls = %d/%d", env.versions.closeCalls, env.versions.recheckCalls)
	}

	if got := versionCloseEventTypes(env.events.events); len(got) != 2 ||
		got[0] != "version.closed" || got[1] != "version.closed" {
		t.Fatalf("events = %#v", got)
	}
	if got := versionCloseEventChannels(env.events.events); got[0] != "version:701" || got[1] != "system" {
		t.Fatalf("event channels = %#v", got)
	}
	event := env.events.events[0]
	if event.Scope["version_id"] != "701" {
		t.Fatalf("event scope = %#v", event.Scope)
	}
	if event.Payload["version_id"] != "701" || event.Payload["report_revision_id"] != "9001" ||
		event.Payload["close_reason"] != "all delivery work is done" ||
		event.Payload["closed_at"] != testTime.Format(time.RFC3339) ||
		event.Payload["close_run_id"] != "9001" {
		t.Fatalf("event payload = %#v", event.Payload)
	}

	if len(env.audit.events) != 1 {
		t.Fatalf("audit events = %#v", env.audit.events)
	}
	audit := env.audit.events[0]
	if audit.Action != "version.closed" || audit.TargetKind != "version" || audit.TargetID != "701" {
		t.Fatalf("audit = %#v", audit)
	}
	if audit.ActorID == nil || *audit.ActorID != 77 {
		t.Fatalf("audit actor = %v", audit.ActorID)
	}
	if audit.Metadata["close_run_id"] != "9001" || audit.Metadata["report_revision_id"] != "9001" {
		t.Fatalf("audit metadata = %#v", audit.Metadata)
	}
}

func TestVersionCloseServiceClosePredicateFailureKeepsState(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)
	env.execution.waves = 1
	env.human.open = 1
	env.requirements.items = append(env.requirements.items,
		version.Requirement{ID: 1003, VersionID: 701, Status: version.RequirementClarified})

	_, err := env.svc.Close(requestContext(), 701, 0, "try")
	var predicateErr *version.ErrVersionClosePredicateFailed
	if !errors.As(err, &predicateErr) {
		t.Fatalf("Close error = %v, want ErrVersionClosePredicateFailed", err)
	}
	wantFailed := map[string]int{
		version.VersionCloseCheckNoActiveWaves:       1,
		version.VersionCloseCheckNoOpenHumanRequests: 1,
		version.VersionCloseCheckRequirementsClosed:  1,
	}
	for name, count := range wantFailed {
		if predicateErr.Failed[name] != count {
			t.Fatalf("failed[%q] = %d, want %d (%#v)", name, predicateErr.Failed[name], count, predicateErr.Failed)
		}
	}
	if stored := env.versions.versions[701]; stored.Status != version.StatusRunning || stored.ClosedAt != nil {
		t.Fatalf("version changed on predicate failure: %#v", stored)
	}
	if len(env.events.events) != 0 || len(env.audit.events) != 0 {
		t.Fatalf("failure wrote side effects: %#v/%#v", env.events.events, env.audit.events)
	}
	if !strings.Contains(predicateErr.Error(), version.VersionCloseCheckNoActiveWaves) {
		t.Fatalf("error message = %q", predicateErr.Error())
	}
}

func TestVersionClosePredicateExcludesOwnTask(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)
	env.work.counts = versionapp.BlockingWorkItemCounts{Total: 1, VersionClose: 1}

	if _, err := env.svc.Close(requestContext(), 701, 0, "own task only"); err != nil {
		t.Fatalf("Close with only the version.close task open: %v", err)
	}
	if env.versions.versions[701].Status != version.StatusCompleted {
		t.Fatalf("version = %#v", env.versions.versions[701])
	}

	// A non-close task still blocks.
	env2 := newVersionCloseEnv(t)
	configurePassingVersionClose(env2, 702)
	env2.work.counts = versionapp.BlockingWorkItemCounts{Total: 2, VersionClose: 1}
	_, err := env2.svc.Close(requestContext(), 702, 0, "blocked")
	var predicateErr *version.ErrVersionClosePredicateFailed
	if !errors.As(err, &predicateErr) || predicateErr.Failed[version.VersionCloseCheckNoBlockingWorkItems] != 1 {
		t.Fatalf("Close error = %v, want no_blocking_work_items=1", err)
	}
}

func TestVersionCloseServiceCloseStateConflict(t *testing.T) {
	env := newVersionCloseEnv(t)
	seedVersionCloseVersion(env, 701, version.StatusCompleted)

	_, err := env.svc.Close(requestContext(), 701, 0, "already done")
	if err == nil {
		t.Fatal("Close of a completed version succeeded")
	}
	if apiErr := apiError(t, err); apiErr.Status != 409 {
		t.Fatalf("conflict status = %d", apiErr.Status)
	}
	if len(env.events.events) != 0 || len(env.audit.events) != 0 {
		t.Fatal("conflict wrote side effects")
	}

	// A missing version maps to 404.
	if _, err := env.svc.Close(requestContext(), 9999, 0, "missing"); err == nil {
		t.Fatal("Close of a missing version succeeded")
	} else if apiErr := apiError(t, err); apiErr.Status != 404 {
		t.Fatalf("missing version status = %d", apiErr.Status)
	}
}

func TestVersionCloseServiceReportMustBeCurrent(t *testing.T) {
	cases := []struct {
		name   string
		report version.ReportStatus
	}{
		{
			name: "stale report",
			report: version.ReportStatus{
				Status: version.ReportStatusStale, Fingerprint: strings.Repeat("a", 64), RevisionID: 9001,
			},
		},
		{
			name: "fingerprint mismatch",
			report: version.ReportStatus{
				Status: version.ReportStatusPublished, Fingerprint: strings.Repeat("a", 64),
				RevisionID: 9001, CurrentFingerprint: strings.Repeat("b", 64),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newVersionCloseEnv(t)
			configurePassingVersionClose(env, 701)
			env.reports.report = tc.report
			_, err := env.svc.Close(requestContext(), 701, 0, "try")
			var predicateErr *version.ErrVersionClosePredicateFailed
			if !errors.As(err, &predicateErr) {
				t.Fatalf("Close error = %v, want predicate failure", err)
			}
			if predicateErr.Failed[version.VersionCloseCheckVersionReportPublished] != 1 {
				t.Fatalf("failed = %#v", predicateErr.Failed)
			}
			if env.versions.versions[701].Status != version.StatusRunning {
				t.Fatal("version completed with a non-current report")
			}
		})
	}
}

func TestVersionCloseServiceEvaluate(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)

	predicate, err := env.svc.Evaluate(requestContext(), 701)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !predicate.Passed() {
		t.Fatalf("predicate = %+v, want all true", predicate)
	}

	if _, err := env.svc.Evaluate(requestContext(), 0); err == nil {
		t.Fatal("Evaluate accepted version ID 0")
	} else if field := fieldName(t, err); field != "id" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.Evaluate(requestContext(), 9999); err == nil {
		t.Fatal("Evaluate accepted a missing version")
	} else if apiErr := apiError(t, err); apiErr.Status != 404 {
		t.Fatalf("missing version status = %d", apiErr.Status)
	}

	wantErr := errors.New("work board down")
	env.work.err = wantErr
	if _, err := env.svc.Evaluate(requestContext(), 701); !errors.Is(err, wantErr) {
		t.Fatalf("Evaluate error = %v, want %v", err, wantErr)
	}
}

func TestVersionCloseServiceEnsureVersionClose(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)

	if err := env.svc.EnsureVersionClose(requestContext(), 701); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	if len(env.tasks.calls) != 1 {
		t.Fatalf("task ensures = %#v", env.tasks.calls)
	}
	call := env.tasks.calls[0]
	if call.versionID != 701 || call.reportRevisionID != 9001 {
		t.Fatalf("task call = %#v", call)
	}
	if len(call.predicateFingerprint) != 64 || strings.Trim(call.predicateFingerprint, "0123456789abcdef") != "" {
		t.Fatalf("predicate fingerprint = %q", call.predicateFingerprint)
	}

	// A changed predicate input naturally produces a different dedupe key.
	env.requirements.items[0].Status = version.RequirementClarified
	env.tasks.calls = nil
	if err := env.svc.EnsureVersionClose(requestContext(), 701); err != nil {
		t.Fatalf("EnsureVersionClose after state change: %v", err)
	}
	if len(env.tasks.calls) != 0 {
		t.Fatalf("blocked predicate still ensured a task: %#v", env.tasks.calls)
	}
	env.requirements.items[0].Status = version.RequirementClosed
	env.execution.intents = 1
	env.tasks.calls = nil
	if err := env.svc.EnsureVersionClose(requestContext(), 701); err != nil {
		t.Fatalf("EnsureVersionClose with intents: %v", err)
	}
	if len(env.tasks.calls) != 0 {
		t.Fatalf("predicate with active intent still ensured a task: %#v", env.tasks.calls)
	}

	env.execution.intents = 0
	env.tasks.calls = nil
	env.reports.report.RevisionID = 9002
	if err := env.svc.EnsureVersionClose(requestContext(), 701); err != nil {
		t.Fatalf("EnsureVersionClose passing: %v", err)
	}
	if len(env.tasks.calls) != 1 || env.tasks.calls[0].reportRevisionID != 9002 ||
		env.tasks.calls[0].predicateFingerprint == call.predicateFingerprint {
		t.Fatalf("new report revision did not produce a new ensure key: %#v", env.tasks.calls)
	}

	wantErr := errors.New("task board down")
	env.tasks.err = wantErr
	if err := env.svc.EnsureVersionClose(requestContext(), 701); !errors.Is(err, wantErr) {
		t.Fatalf("EnsureVersionClose error = %v, want %v", err, wantErr)
	}
	if err := env.svc.EnsureVersionClose(requestContext(), 0); err == nil {
		t.Fatal("EnsureVersionClose accepted version ID 0")
	}
}

func TestVersionCloseServiceReopenIfCompletedClearsAndEmits(t *testing.T) {
	env := newVersionCloseEnv(t)
	closedAt := testTime.Truncate(time.Second)
	runID := int64(9001)
	env.versions.versions[701] = version.Version{
		ID: 701, Title: "Orders Q4", Status: version.StatusCompleted,
		ClosedAt: &closedAt, CloseReason: "done", CloseRunID: &runID,
		CreatedBy: 42, CreatedAt: testTime, UpdatedAt: testTime,
	}
	ctx := platform.WithActor(requestContext(), platform.Actor{ID: 88, Role: "user"})

	if err := env.svc.ReopenIfCompleted(ctx, 701, "manual", "new scope agreed"); err != nil {
		t.Fatalf("ReopenIfCompleted: %v", err)
	}
	stored := env.versions.versions[701]
	if stored.Status != version.StatusRunning || stored.ClosedAt != nil || stored.CloseReason != "" || stored.CloseRunID != nil {
		t.Fatalf("reopened version = %#v", stored)
	}
	if !stored.UpdatedAt.Equal(testTime) {
		t.Fatalf("reopened updated_at = %v", stored.UpdatedAt)
	}
	if len(env.staler.calls) != 1 || env.staler.calls[0].versionID != 701 || env.staler.calls[0].reason != "new scope agreed" {
		t.Fatalf("report stale calls = %#v", env.staler.calls)
	}
	if got := versionCloseEventTypes(env.events.events); len(got) != 2 || got[0] != "version.reopened" || got[1] != "version.reopened" {
		t.Fatalf("events = %#v", got)
	}
	if got := versionCloseEventChannels(env.events.events); got[0] != "version:701" || got[1] != "system" {
		t.Fatalf("event channels = %#v", got)
	}
	event := env.events.events[0]
	if event.Payload["version_id"] != "701" || event.Payload["trigger"] != "manual" ||
		event.Payload["reason"] != "new scope agreed" {
		t.Fatalf("event payload = %#v", event.Payload)
	}
	if len(env.audit.events) != 1 || env.audit.events[0].Action != "version.reopened" {
		t.Fatalf("audit = %#v", env.audit.events)
	}
	if env.audit.events[0].ActorID == nil || *env.audit.events[0].ActorID != 88 {
		t.Fatalf("audit actor = %v", env.audit.events[0].ActorID)
	}

	// Reopening a running version is a no-op: no events, no stale hook, no audit.
	env.events.events = nil
	env.staler.calls = nil
	env.audit.events = nil
	if err := env.svc.ReopenIfCompleted(requestContext(), 701, "execution_intent", ""); err != nil {
		t.Fatalf("noop reopen: %v", err)
	}
	if len(env.events.events) != 0 || len(env.staler.calls) != 0 || len(env.audit.events) != 0 {
		t.Fatalf("noop reopen wrote side effects: %#v/%#v/%#v", env.events.events, env.staler.calls, env.audit.events)
	}

	// The report stale projection is best effort and never rolls the reopen back.
	env.versions.versions[701] = version.Version{ID: 701, Status: version.StatusCompleted}
	env.staler.err = errors.New("report store down")
	if err := env.svc.ReopenIfCompleted(requestContext(), 701, "case_confirmed", ""); err != nil {
		t.Fatalf("ReopenIfCompleted with a failing stale hook: %v", err)
	}
	if env.versions.versions[701].Status != version.StatusRunning {
		t.Fatal("reopen was rolled back by the stale hook")
	}

	// A missing version maps to 404.
	if err := env.svc.ReopenIfCompleted(requestContext(), 9999, "manual", "missing"); err == nil {
		t.Fatal("reopen of a missing version succeeded")
	} else if apiErr := apiError(t, err); apiErr.Status != 404 {
		t.Fatalf("missing version status = %d", apiErr.Status)
	}
}

func TestVersionCloseServiceValidatesInputs(t *testing.T) {
	env := newVersionCloseEnv(t)
	if _, err := env.svc.Close(requestContext(), 0, 0, "reason"); err == nil {
		t.Fatal("Close accepted version ID 0")
	} else if field := fieldName(t, err); field != "id" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.Close(requestContext(), 701, 0, "   "); err == nil {
		t.Fatal("Close accepted a blank reason")
	} else if field := fieldName(t, err); field != "reason" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.Close(requestContext(), 701, 0, strings.Repeat("x", version.VersionCloseReasonMax+1)); err == nil {
		t.Fatal("Close accepted an overlong reason")
	} else if field := fieldName(t, err); field != "reason" {
		t.Fatalf("field = %q", field)
	}
	if err := env.svc.ReopenIfCompleted(requestContext(), 701, "manual", "   "); err == nil {
		t.Fatal("manual reopen accepted a blank reason")
	} else if field := fieldName(t, err); field != "reason" {
		t.Fatalf("field = %q", field)
	}
	if err := env.svc.ReopenIfCompleted(requestContext(), 701, "other", "reason"); err == nil {
		t.Fatal("reopen accepted an unknown trigger")
	} else if field := fieldName(t, err); field != "trigger" {
		t.Fatalf("field = %q", field)
	}
	if err := env.svc.ReopenIfCompleted(requestContext(), 0, "manual", "reason"); err == nil {
		t.Fatal("reopen accepted version ID 0")
	} else if field := fieldName(t, err); field != "id" {
		t.Fatalf("field = %q", field)
	}
}

func TestVersionCloseConcurrentNewIntent(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)

	locked := make(chan struct{})
	intentCreated := make(chan struct{})
	env.versions.lockedHook = func(_ context.Context, _ version.Version) {
		close(locked)
		<-intentCreated
	}

	closeErr := make(chan error, 1)
	go func() {
		_, err := env.svc.Close(requestContext(), 701, 0, "close race")
		closeErr <- err
	}()

	<-locked
	// A new intent commits while the close transaction holds the version row
	// lock. The in-transaction re-evaluation must see it and reject the close.
	env.execution.intents = 1
	close(intentCreated)

	err := <-closeErr
	var predicateErr *version.ErrVersionClosePredicateFailed
	if !errors.As(err, &predicateErr) {
		t.Fatalf("concurrent Close error = %v, want predicate failure", err)
	}
	if predicateErr.Failed[version.VersionCloseCheckNoActiveIntents] != 1 {
		t.Fatalf("failed = %#v", predicateErr.Failed)
	}
	if env.versions.versions[701].Status != version.StatusRunning || env.execution.intents != 1 {
		t.Fatalf("final state = %#v/intents=%d, want running with the intent", env.versions.versions[701], env.execution.intents)
	}
	if len(env.events.events) != 0 {
		t.Fatalf("losing close published events: %#v", env.events.events)
	}

	// The opposite ordering: close commits first, then the intent path reopens
	// the completed version, so completed never coexists with an active intent.
	env = newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)
	if _, err := env.svc.Close(requestContext(), 701, 0, "close first"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	env.execution.intents = 1
	if err := env.svc.ReopenIfCompleted(requestContext(), 701, "execution_intent", ""); err != nil {
		t.Fatalf("intent reopen: %v", err)
	}
	if env.versions.versions[701].Status != version.StatusRunning || env.versions.versions[701].ClosedAt != nil {
		t.Fatalf("final state = %#v, want running", env.versions.versions[701])
	}
	if got := versionCloseEventTypes(env.events.events); !containsAll(got, "version.closed", "version.reopened") {
		t.Fatalf("events = %#v", got)
	}
}
