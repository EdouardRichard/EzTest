package versionapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- waiver fakes ----------
//
// Discipline ⑨ (19号 §4): the fakes record every call (counters / call slices).
// Tests that must prove a leg did NOT run assert the counters stay zero and/or
// inject a loud error into the port so an unexpected call surfaces as a wrong
// error rather than a silently supplied zero value.

type fakeWaiverVersions struct {
	versions map[int64]version.Version
}

func (f *fakeWaiverVersions) Get(_ context.Context, id int64) (version.Version, []version.AccountBinding, error) {
	stored, ok := f.versions[id]
	if !ok {
		return version.Version{}, nil, version.ErrNotFound
	}
	return stored, nil, nil
}

type fakeWaiverCloser struct {
	predicate       version.ClosePredicate
	evaluateErr     error
	closeErr        error
	closeCalls      int
	closedVersion   version.Version
	regenerateOrder *[]string
}

func (f *fakeWaiverCloser) Evaluate(_ context.Context, _ int64) (version.ClosePredicate, error) {
	if f.evaluateErr != nil {
		return version.ClosePredicate{}, f.evaluateErr
	}
	return f.predicate, nil
}

func (f *fakeWaiverCloser) CloseWaived(_ context.Context, versionID int64, reason string) (version.Version, error) {
	f.closeCalls++
	if f.regenerateOrder != nil {
		*f.regenerateOrder = append(*f.regenerateOrder, "close")
	}
	if f.closeErr != nil {
		return version.Version{}, f.closeErr
	}
	closed := f.closedVersion
	if closed.ID == 0 {
		closed = version.Version{
			ID:          versionID,
			Status:      version.StatusCompleted,
			ClosureKind: version.ClosureKindWaived,
			Unverified:  true,
			CloseReason: reason,
		}
	}
	return closed, nil
}

type fakeWaiverReports struct {
	calls           int
	publication     versionapp.WaivedReportPublication
	err             error
	regenerateOrder *[]string
}

func (f *fakeWaiverReports) RegenerateWaivedVersionReport(_ context.Context, _ int64) (versionapp.WaivedReportPublication, error) {
	f.calls++
	if f.regenerateOrder != nil {
		*f.regenerateOrder = append(*f.regenerateOrder, "regenerate")
	}
	if f.err != nil {
		return versionapp.WaivedReportPublication{}, f.err
	}
	return f.publication, nil
}

type fakeWaiverFacts struct {
	facts   []version.Fact
	outcome versionapp.FactRecordOutcome
	err     error
}

func (f *fakeWaiverFacts) RecordFact(_ context.Context, fact version.Fact) (versionapp.FactRecordOutcome, error) {
	if f.err != nil {
		return versionapp.FactRecordOutcome{}, f.err
	}
	f.facts = append(f.facts, fact)
	outcome := f.outcome
	outcome.Fact = fact
	return outcome, nil
}

type fakeWaiverRequirementReader struct {
	requirement version.Requirement
	err         error
}

func (f *fakeWaiverRequirementReader) GetRequirement(_ context.Context, _ int64) (version.Requirement, error) {
	if f.err != nil {
		return version.Requirement{}, f.err
	}
	return f.requirement, nil
}

type fakeWaiverWriter struct {
	calls int
	err   error
}

func (f *fakeWaiverWriter) MarkWaived(_ context.Context, _ int64, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.calls++
	return nil
}

// ---------- environment ----------

type waiverEnv struct {
	svc          *versionapp.WaiverService
	versions     *fakeWaiverVersions
	closer       *fakeWaiverCloser
	reports      *fakeWaiverReports
	facts        *fakeWaiverFacts
	requirements *fakeWaiverRequirementReader
	writer       *fakeWaiverWriter
	audit        *fakeAudit
	events       *fakePublisher
	order        []string
}

func newWaiverEnv(t *testing.T) *waiverEnv {
	t.Helper()
	env := &waiverEnv{
		versions: &fakeWaiverVersions{versions: map[int64]version.Version{
			701: {ID: 701, Status: version.StatusRunning, ClosureKind: version.ClosureKindNormal},
		}},
		closer: &fakeWaiverCloser{predicate: version.ClosePredicate{
			Running: true, NoBlockingWorkItems: true, NoActiveIntents: true,
			NoOpenHumanRequests: true, NoActiveWaves: true, NoInflightCaseRuns: true,
			HasExecutionData: true,
		}},
		reports:      &fakeWaiverReports{publication: versionapp.WaivedReportPublication{ReportID: 9101, Revision: 2}},
		facts:        &fakeWaiverFacts{},
		requirements: &fakeWaiverRequirementReader{},
		writer:       &fakeWaiverWriter{},
		audit:        &fakeAudit{},
		events:       &fakePublisher{},
	}
	env.closer.regenerateOrder = &env.order
	env.reports.regenerateOrder = &env.order
	env.svc = versionapp.NewWaiverService(versionapp.WaiverServiceDeps{
		Versions:     env.versions,
		Closer:       env.closer,
		Requirements: env.requirements,
		Waivers:      env.writer,
		Reports:      env.reports,
		Facts:        env.facts,
		Audit:        env.audit,
		Events:       env.events,
		Clock:        fixedClock{at: testTime},
	})
	return env
}

func waiverAdminContext() context.Context {
	return platform.WithActor(requestContext(), platform.Actor{ID: 77, Role: "admin"})
}

// ---------- WaiveClose ----------

// TestWaiveCloseAdminOnly pins the Q7=A functional gate: a non-admin actor (or
// no authenticated actor, or an actor that does not match the recorded
// operator) is rejected with 403 BEFORE any close/report side effect.
func TestWaiveCloseAdminOnly(t *testing.T) {
	cases := []struct {
		name    string
		ctx     context.Context
		actorID int64
	}{
		{name: "no authenticated actor", ctx: requestContext(), actorID: 77},
		{name: "user role", ctx: platform.WithActor(requestContext(), platform.Actor{ID: 77, Role: "user"}), actorID: 77},
		{name: "actor mismatch", ctx: platform.WithActor(requestContext(), platform.Actor{ID: 88, Role: "admin"}), actorID: 77},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newWaiverEnv(t)
			// The close/report legs must never run for a rejected actor.
			env.closer.regenerateOrder = nil
			env.reports.regenerateOrder = nil
			env.closer.closeErr = errors.New("close must not be called for a non-admin")
			env.reports.err = errors.New("regenerate must not be called for a non-admin")

			_, err := env.svc.WaiveClose(tc.ctx, 701, "ship it", tc.actorID)
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) || apiErr.Status != 403 {
				t.Fatalf("WaiveClose error = %v, want 403", err)
			}
			if env.closer.closeCalls != 0 || env.reports.calls != 0 {
				t.Fatalf("side effects ran for a non-admin: close=%d regenerate=%d", env.closer.closeCalls, env.reports.calls)
			}
		})
	}
}

// TestWaiveCloseRoutesThroughCloseThenRegenerates pins the R-G8 sequence: the
// single Close terminal entry runs FIRST, then the synchronous unverified
// report regeneration; the waiver fact/event/audit carry reason and operator.
func TestWaiveCloseRoutesThroughCloseThenRegenerates(t *testing.T) {
	env := newWaiverEnv(t)
	result, err := env.svc.WaiveClose(waiverAdminContext(), 701, "remaining ACs descoped by owner", 77)
	if err != nil {
		t.Fatalf("WaiveClose: %v", err)
	}
	if env.closer.closeCalls != 1 || env.reports.calls != 1 {
		t.Fatalf("close/regenerate calls = %d/%d, want 1/1", env.closer.closeCalls, env.reports.calls)
	}
	if len(env.order) != 2 || env.order[0] != "close" || env.order[1] != "regenerate" {
		t.Fatalf("call order = %v, want [close regenerate]", env.order)
	}
	if result.Version.Status != version.StatusCompleted || result.Version.ClosureKind != version.ClosureKindWaived || !result.Version.Unverified {
		t.Fatalf("result version = %+v, want completed waived unverified", result.Version)
	}
	if result.ReportID != 9101 || result.Revision != 2 {
		t.Fatalf("result report = %d rev %d, want 9101 rev 2", result.ReportID, result.Revision)
	}

	if len(env.facts.facts) != 1 {
		t.Fatalf("waiver facts = %d, want 1", len(env.facts.facts))
	}
	fact := env.facts.facts[0]
	if fact.Kind != version.FactKindWaiver || fact.VersionID != 701 {
		t.Fatalf("fact = %+v, want waiver kind on version 701", fact)
	}
	if fact.Payload["reason"] != "remaining ACs descoped by owner" || fact.Payload["actor_id"] != "77" {
		t.Fatalf("fact payload = %#v, want reason + operator", fact.Payload)
	}
	if fact.Payload["closure_kind"] != version.ClosureKindWaived || fact.Payload["unverified"] != true {
		t.Fatalf("fact payload = %#v, want closure_kind waived + unverified", fact.Payload)
	}

	var waiverEvents []string
	for _, event := range env.events.events {
		if event.Type == "version.waived" {
			waiverEvents = append(waiverEvents, event.Channel)
			if event.Payload["reason"] != "remaining ACs descoped by owner" || event.Payload["actor_id"] != "77" {
				t.Fatalf("waiver event payload = %#v", event.Payload)
			}
		}
	}
	if len(waiverEvents) != 2 || waiverEvents[0] != "version:701" || waiverEvents[1] != "system" {
		t.Fatalf("version.waived channels = %v, want [version:701 system]", waiverEvents)
	}

	var audited bool
	for _, entry := range env.audit.events {
		if entry.Action == "version.waived" {
			audited = true
			if entry.ActorID == nil || *entry.ActorID != 77 {
				t.Fatalf("waiver audit actor = %v, want 77", entry.ActorID)
			}
			if entry.TargetKind != "version" || entry.TargetID != "701" {
				t.Fatalf("waiver audit target = %s/%s", entry.TargetKind, entry.TargetID)
			}
		}
	}
	if !audited {
		t.Fatalf("audit events = %#v, want version.waived", env.audit.events)
	}
}

// TestWaiveCloseUnsettledReturns409 pins that a predicate rejection from the
// Close entry surfaces as a 409 with the failed check map and no report call.
func TestWaiveCloseUnsettledReturns409(t *testing.T) {
	env := newWaiverEnv(t)
	env.closer.closeErr = &version.ErrVersionClosePredicateFailed{Failed: map[string]int{
		version.VersionCloseCheckNoActiveWaves: 1,
	}}
	env.reports.err = errors.New("regenerate must not run after a rejected close")

	_, err := env.svc.WaiveClose(waiverAdminContext(), 701, "ship it", 77)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("WaiveClose error = %v, want 409", err)
	}
	failed, ok := apiErr.Details["failed"].(map[string]int)
	if !ok || failed[version.VersionCloseCheckNoActiveWaves] != 1 {
		t.Fatalf("409 details = %#v, want failed map with no_active_waves", apiErr.Details)
	}
	if env.reports.calls != 0 {
		t.Fatalf("regenerate ran after a rejected close")
	}
	if len(env.facts.facts) != 0 {
		t.Fatalf("waiver fact recorded after a rejected close")
	}
}

// TestWaiveCloseNoExecutionDataReturns409 pins the fail-closed precondition:
// without execution data the mandatory unverified report cannot be generated,
// so the waive is rejected BEFORE the close (no completed version without a
// report).
func TestWaiveCloseNoExecutionDataReturns409(t *testing.T) {
	env := newWaiverEnv(t)
	env.closer.predicate.HasExecutionData = false
	env.closer.closeErr = errors.New("close must not run without execution data")

	_, err := env.svc.WaiveClose(waiverAdminContext(), 701, "ship it", 77)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("WaiveClose error = %v, want 409", err)
	}
	if apiErr.Details["reason"] != "no_execution_data" {
		t.Fatalf("409 details = %#v, want reason no_execution_data", apiErr.Details)
	}
	if env.closer.closeCalls != 0 {
		t.Fatalf("close ran without execution data")
	}
}

// TestWaiveCloseRegenerateFailureIsDisclosedAndRetryable pins 七.6: a failed
// post-close regeneration is a typed, disclosed error (never silent), and a
// repeated WaiveClose on the already waived-closed version retries ONLY the
// report leg (the close is immutable and not re-run).
func TestWaiveCloseRegenerateFailureIsDisclosedAndRetryable(t *testing.T) {
	env := newWaiverEnv(t)
	env.reports.err = errors.New("render exploded")

	_, err := env.svc.WaiveClose(waiverAdminContext(), 701, "ship it", 77)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Code != "waive_report_regeneration_failed" {
		t.Fatalf("WaiveClose error = %v, want typed 500 waive_report_regeneration_failed", err)
	}
	if env.closer.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1 (the close committed before the failure)", env.closer.closeCalls)
	}

	// The version is now completed+waived (the close committed). Retry heals the
	// report leg without re-closing.
	env.versions.versions[701] = version.Version{
		ID: 701, Status: version.StatusCompleted, ClosureKind: version.ClosureKindWaived,
		Unverified: true, CloseReason: "ship it",
	}
	env.reports.err = nil
	result, err := env.svc.WaiveClose(waiverAdminContext(), 701, "ship it", 77)
	if err != nil {
		t.Fatalf("retry WaiveClose: %v", err)
	}
	if env.closer.closeCalls != 1 {
		t.Fatalf("close calls after retry = %d, want still 1 (idempotent report-only retry)", env.closer.closeCalls)
	}
	if env.reports.calls != 2 {
		t.Fatalf("regenerate calls = %d, want 2 (the retry re-ran the report leg)", env.reports.calls)
	}
	if result.ReportID != 9101 || result.Revision != 2 {
		t.Fatalf("retry result report = %d rev %d", result.ReportID, result.Revision)
	}
}

// TestWaiveCloseRejectsCompletedNormalVersion pins that a normally closed
// version cannot be waived retroactively (reopen first).
func TestWaiveCloseRejectsCompletedNormalVersion(t *testing.T) {
	env := newWaiverEnv(t)
	env.versions.versions[701] = version.Version{ID: 701, Status: version.StatusCompleted, ClosureKind: version.ClosureKindNormal}
	env.closer.closeErr = errors.New("close must not run for a completed version")

	_, err := env.svc.WaiveClose(waiverAdminContext(), 701, "ship it", 77)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("WaiveClose error = %v, want 409", err)
	}
	if env.closer.closeCalls != 0 {
		t.Fatalf("close ran for a completed version")
	}
}

func TestWaiveCloseValidatesReason(t *testing.T) {
	env := newWaiverEnv(t)
	for _, reason := range []string{"", "   ", strings.Repeat("x", version.VersionCloseReasonMax+1)} {
		_, err := env.svc.WaiveClose(waiverAdminContext(), 701, reason, 77)
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 400 {
			t.Fatalf("WaiveClose(reason=%q) error = %v, want 400", reason, err)
		}
	}
}

// ---------- WaiveRequirement ----------

func TestWaiveRequirementAdminOnly(t *testing.T) {
	env := newWaiverEnv(t)
	ctx := platform.WithActor(requestContext(), platform.Actor{ID: 77, Role: "user"})
	err := env.svc.WaiveRequirement(ctx, 1001, "descoped", 77)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("WaiveRequirement error = %v, want 403", err)
	}
	if env.writer.calls != 0 {
		t.Fatalf("waiver write ran for a non-admin")
	}
}

func TestWaiveRequirementMarksWaivedWithFactEventAudit(t *testing.T) {
	env := newWaiverEnv(t)
	current := int64(1002)
	env.requirements.requirement = version.Requirement{
		ID: 1001, VersionID: 701, Status: version.RequirementClarified, CurrentVersionID: &current,
	}

	if err := env.svc.WaiveRequirement(waiverAdminContext(), 1001, "AC-2 descoped by owner", 77); err != nil {
		t.Fatalf("WaiveRequirement: %v", err)
	}
	if env.writer.calls != 1 {
		t.Fatalf("MarkWaived calls = %d, want 1", env.writer.calls)
	}
	if len(env.facts.facts) != 1 {
		t.Fatalf("waiver facts = %d, want 1", len(env.facts.facts))
	}
	fact := env.facts.facts[0]
	if fact.Kind != version.FactKindWaiver || fact.VersionID != 701 || fact.Key != "requirement:1001" {
		t.Fatalf("fact = %+v, want requirement-scoped waiver on version 701", fact)
	}
	if fact.Payload["requirement_id"] != "1001" || fact.Payload["reason"] != "AC-2 descoped by owner" || fact.Payload["actor_id"] != "77" {
		t.Fatalf("fact payload = %#v", fact.Payload)
	}
	var found bool
	for _, event := range env.events.events {
		if event.Type == "requirement.waived" {
			found = true
			if event.Channel != "version:701" || event.Payload["requirement_id"] != "1001" || event.Payload["actor_id"] != "77" {
				t.Fatalf("requirement.waived event = %#v", event)
			}
		}
	}
	if !found {
		t.Fatalf("events = %#v, want requirement.waived", env.events.events)
	}
	var audited bool
	for _, entry := range env.audit.events {
		if entry.Action == "requirement.waived" && entry.TargetKind == "requirement" && entry.TargetID == "1001" {
			audited = true
			if entry.ActorID == nil || *entry.ActorID != 77 {
				t.Fatalf("audit actor = %v", entry.ActorID)
			}
		}
	}
	if !audited {
		t.Fatalf("audit = %#v, want requirement.waived", env.audit.events)
	}
}

// TestWaiveRequirementRepeatEmitsEventAndAuditDespiteFactDedupe pins GD-06
// accountability: a repeat waiver whose fact dedupes (AlreadyRecorded) is still
// a REAL admin transition, so the event + audit must fire — fact-log idempotency
// only suppresses duplicate FACT announcements, never the audit.
func TestWaiveRequirementRepeatEmitsEventAndAuditDespiteFactDedupe(t *testing.T) {
	env := newWaiverEnv(t)
	current := int64(1002)
	env.requirements.requirement = version.Requirement{
		ID: 1001, VersionID: 701, Status: version.RequirementClarified, CurrentVersionID: &current,
	}
	env.facts.outcome = versionapp.FactRecordOutcome{AlreadyRecorded: true}

	if err := env.svc.WaiveRequirement(waiverAdminContext(), 1001, "same reason", 77); err != nil {
		t.Fatalf("WaiveRequirement: %v", err)
	}
	if env.writer.calls != 1 {
		t.Fatalf("MarkWaived calls = %d, want 1 (the transition happened)", env.writer.calls)
	}
	var events, audits int
	for _, event := range env.events.events {
		if event.Type == "requirement.waived" {
			events++
		}
	}
	for _, entry := range env.audit.events {
		if entry.Action == "requirement.waived" {
			audits++
		}
	}
	if events != 1 || audits != 1 {
		t.Fatalf("repeat waiver events/audits = %d/%d, want 1/1 (fact dedupe must not suppress the audit)", events, audits)
	}
}

// TestWaiveCloseRepeatEmitsEventAndAuditDespiteFactDedupe is the version-level
// analogue: a waive-close whose fact dedupes still emits version.waived + audit.
func TestWaiveCloseRepeatEmitsEventAndAuditDespiteFactDedupe(t *testing.T) {
	env := newWaiverEnv(t)
	env.facts.outcome = versionapp.FactRecordOutcome{AlreadyRecorded: true}

	if _, err := env.svc.WaiveClose(waiverAdminContext(), 701, "same reason", 77); err != nil {
		t.Fatalf("WaiveClose: %v", err)
	}
	var events, audits int
	for _, event := range env.events.events {
		if event.Type == "version.waived" {
			events++
		}
	}
	for _, entry := range env.audit.events {
		if entry.Action == "version.waived" {
			audits++
		}
	}
	if events == 0 || audits == 0 {
		t.Fatalf("waive-close events/audits = %d/%d, want >0 (fact dedupe must not suppress the audit)", events, audits)
	}
}

func TestWaiveRequirementStateGate(t *testing.T) {
	cases := []struct {
		name       string
		status     version.RequirementStatus
		waived     bool
		wantStatus int // 0 = success
	}{
		{name: "draft cannot be waived", status: version.RequirementDraft, wantStatus: 409},
		{name: "obsolete cannot be waived", status: version.RequirementObsolete, wantStatus: 409},
		{name: "closed without waiver cannot be waived", status: version.RequirementClosed, wantStatus: 409},
		{name: "already waived closed is idempotent", status: version.RequirementClosed, waived: true, wantStatus: 0},
		{name: "already waived clarified is idempotent", status: version.RequirementClarified, waived: true, wantStatus: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newWaiverEnv(t)
			env.requirements.requirement = version.Requirement{
				ID: 1001, VersionID: 701, Status: tc.status, Waived: tc.waived,
			}
			err := env.svc.WaiveRequirement(waiverAdminContext(), 1001, "descoped", 77)
			if tc.wantStatus == 0 {
				if err != nil {
					t.Fatalf("WaiveRequirement: %v", err)
				}
				return
			}
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) || apiErr.Status != tc.wantStatus {
				t.Fatalf("WaiveRequirement error = %v, want %d", err, tc.wantStatus)
			}
			if env.writer.calls != 0 {
				t.Fatalf("waiver write ran for a rejected state")
			}
		})
	}
}
