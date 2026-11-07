package versionapp_test

// v0.22 Task 25 (R-B4-1..R-B4-6): StageService is the orchestrated stage
// machine. These tests pin the whole closure path (created -> ... -> completed
// through the wave-terminal and report-published production callbacks), every
// gate rejection, the C-2 rule that pending questions never block stage
// advancement, blocked round-trips with a structured JSON blocker and minimal
// pause/resume. They are written before the implementation (TDD RED first).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// advancingClock returns a strictly later instant on every read, so a test can
// prove each accepted transition refreshed stage_entered_at.
type advancingClock struct{ at time.Time }

func (c *advancingClock) Now() time.Time {
	c.at = c.at.Add(time.Second)
	return c.at
}

// stageGateState is the programmable stand-in for every v0.22 gate port. The
// zero value is "all gates closed"; newStageGateState opens them and individual
// tests close exactly the gate under test.
type stageGateState struct {
	environmentBound      bool
	requirementCount      int
	requirementsConfirmed bool
	openGaps              int
	confirmedCaseDrafts   int
	coverageComputed      bool
	confirmedCases        int
	executorOnline        bool
	accountsValid         bool
	wavesTerminal         bool
	reportPublished       bool
	openBlockingIssues    int
	blockerCleared        bool

	checked []string
}

func newStageGateState() *stageGateState {
	return &stageGateState{
		environmentBound:      true,
		requirementCount:      2,
		requirementsConfirmed: true,
		confirmedCaseDrafts:   3,
		coverageComputed:      true,
		confirmedCases:        3,
		executorOnline:        true,
		accountsValid:         true,
		wavesTerminal:         true,
		reportPublished:       true,
		blockerCleared:        true,
	}
}

func (g *stageGateState) mark(name string) { g.checked = append(g.checked, name) }

func (g *stageGateState) gates() versionapp.StageGates {
	return versionapp.StageGates{
		EnvironmentBound: func(context.Context, int64) (bool, error) {
			g.mark("environment")
			return g.environmentBound, nil
		},
		RequirementCount: func(context.Context, int64) (int, error) {
			g.mark("requirement_count")
			return g.requirementCount, nil
		},
		RequirementsConfirmed: func(context.Context, int64) (bool, error) {
			g.mark("requirements_confirmed")
			return g.requirementsConfirmed, nil
		},
		OpenGaps: func(context.Context, int64) (int, error) {
			g.mark("open_gaps")
			return g.openGaps, nil
		},
		ConfirmedCaseDrafts: func(context.Context, int64) (int, error) {
			g.mark("confirmed_case_drafts")
			return g.confirmedCaseDrafts, nil
		},
		CoverageComputed: func(context.Context, int64) (bool, error) {
			g.mark("coverage_computed")
			return g.coverageComputed, nil
		},
		ConfirmedCases: func(context.Context, int64) (int, error) {
			g.mark("confirmed_cases")
			return g.confirmedCases, nil
		},
		ExecutorOnline: func(context.Context) (bool, error) {
			g.mark("executor_online")
			return g.executorOnline, nil
		},
		AccountsValid: func(context.Context, int64) (bool, error) {
			g.mark("accounts_valid")
			return g.accountsValid, nil
		},
		WavesTerminal: func(context.Context, int64) (bool, error) {
			g.mark("waves_terminal")
			return g.wavesTerminal, nil
		},
		ReportPublished: func(context.Context, int64) (bool, error) {
			g.mark("report_published")
			return g.reportPublished, nil
		},
		OpenBlockingIssues: func(context.Context, int64) (int, error) {
			g.mark("open_blocking_issues")
			return g.openBlockingIssues, nil
		},
		BlockerCleared: func(_ context.Context, _ int64, _ versionapp.Blocker) (bool, error) {
			g.mark("blocker_cleared")
			return g.blockerCleared, nil
		},
	}
}

type fakeStageWaveLookup struct {
	waves map[int64]versionapp.StageWave
}

func (f *fakeStageWaveLookup) GetWave(_ context.Context, waveID int64) (versionapp.StageWave, error) {
	wave, ok := f.waves[waveID]
	if !ok {
		return versionapp.StageWave{}, version.ErrNotFound
	}
	return wave, nil
}

type fakeStageWaveControl struct {
	paused       []int64
	resumed      []int64
	pauseReasons []string
	resumeCalls  int
	err          error
}

func (f *fakeStageWaveControl) PauseVersionWaves(_ context.Context, versionID int64, reason string, _ int64) error {
	if f.err != nil {
		return f.err
	}
	f.paused = append(f.paused, versionID)
	f.pauseReasons = append(f.pauseReasons, reason)
	return nil
}

func (f *fakeStageWaveControl) ResumeVersionWaves(_ context.Context, versionID int64) error {
	f.resumeCalls++
	if f.err != nil {
		return f.err
	}
	f.resumed = append(f.resumed, versionID)
	return nil
}

type fakeStageReports struct {
	versionIDs []int64
	err        error
}

func (f *fakeStageReports) EnsureVersionReport(_ context.Context, versionID int64) error {
	f.versionIDs = append(f.versionIDs, versionID)
	return f.err
}

type fakeStageCloser struct {
	versionIDs []int64
	reasons    []string
	err        error
	// alreadyClosed makes the fake model the production "ensure closed"
	// contract: the first close succeeds and every later close reports the
	// typed already-closed signal instead of a generic state conflict.
	alreadyClosed map[int64]bool
}

func (f *fakeStageCloser) CloseVersion(_ context.Context, versionID int64, reason string) error {
	f.versionIDs = append(f.versionIDs, versionID)
	f.reasons = append(f.reasons, reason)
	if f.err != nil {
		return f.err
	}
	if f.alreadyClosed != nil {
		if f.alreadyClosed[versionID] {
			return fmt.Errorf("version %d is already completed: %w", versionID, versionapp.ErrVersionAlreadyClosed)
		}
		f.alreadyClosed[versionID] = true
	}
	return nil
}

type fakeStageHandoff struct {
	versionIDs []int64
	err        error
}

func (f *fakeStageHandoff) Generate(_ context.Context, versionID int64) (versionapp.Handoff, error) {
	f.versionIDs = append(f.versionIDs, versionID)
	return versionapp.Handoff{}, f.err
}

type stageServiceEnv struct {
	svc         *versionapp.StageService
	repo        *fakeStageRepo
	jobs        *fakeStageJobs
	audit       *fakeAudit
	publisher   *fakePublisher
	gates       *stageGateState
	waves       *fakeStageWaveLookup
	waveControl *fakeStageWaveControl
	reports     *fakeStageReports
	closer      *fakeStageCloser
	handoff     *fakeStageHandoff
}

const stageServiceVersionID = int64(101)

func newStageServiceEnv(t *testing.T, initial version.Stage) *stageServiceEnv {
	t.Helper()
	repo := newFakeStageRepo(version.StageState{Stage: initial, StageEnteredAt: testTime})
	jobs := &fakeStageJobs{}
	audit := &fakeAudit{}
	publisher := &fakePublisher{}
	gates := newStageGateState()
	waves := &fakeStageWaveLookup{waves: map[int64]versionapp.StageWave{
		71: {ID: 71, VersionID: stageServiceVersionID, Status: "done", Terminal: true},
	}}
	waveControl := &fakeStageWaveControl{}
	reports := &fakeStageReports{}
	closer := &fakeStageCloser{}
	handoff := &fakeStageHandoff{}
	svc := versionapp.NewStageService(versionapp.StageServiceDeps{
		Repo:        repo,
		Jobs:        jobs,
		Audit:       audit,
		Events:      publisher,
		Clock:       &advancingClock{at: testTime},
		Gates:       gates.gates(),
		Waves:       waves,
		WaveControl: waveControl,
		Reports:     reports,
		Closer:      closer,
		Handoff:     handoff,
	})
	return &stageServiceEnv{
		svc: svc, repo: repo, jobs: jobs, audit: audit, publisher: publisher,
		gates: gates, waves: waves, waveControl: waveControl, reports: reports,
		closer: closer, handoff: handoff,
	}
}

func (e *stageServiceEnv) requireStage(t *testing.T, want version.Stage) {
	t.Helper()
	view, err := e.svc.Stage(requestContext(), stageServiceVersionID)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if view.Stage != want {
		t.Fatalf("stage = %q, want %q", view.Stage, want)
	}
}

func stageEventsOfType(publisher *fakePublisher, eventType string) []map[string]any {
	return stageEventsOfTypeOnChannel(publisher, eventType, "")
}

func stageEventsOfTypeOnChannel(publisher *fakePublisher, eventType, channel string) []map[string]any {
	payloads := make([]map[string]any, 0)
	for _, event := range publisher.events {
		if event.Type != eventType || event.Payload == nil {
			continue
		}
		if channel != "" && event.Channel != channel {
			continue
		}
		payloads = append(payloads, event.Payload)
	}
	return payloads
}

// TestStageHappyPathToCompleted is the R-B4-3 closure: every forward edge with
// its gate, then the production wave-terminal callback enters reporting and the
// report-published callback completes and closes the version. No manual API call
// is used for the two terminal edges.
func TestStageHappyPathToCompleted(t *testing.T) {
	env := newStageServiceEnv(t, version.StageCreated)
	ctx := stageUserContext(42)

	for _, target := range []version.Stage{
		version.StageClarifying,
		version.StageRequirementReview,
		version.StageDesigning,
		version.StageCaseReview,
		version.StageExecuting,
	} {
		if err := env.svc.Request(ctx, stageServiceVersionID, target); err != nil {
			t.Fatalf("Request(%s): %v", target, err)
		}
		env.requireStage(t, target)
	}
	if len(env.jobs.jobs) < 2 {
		t.Fatalf("stage-entry jobs = %#v, want design.cases and execute.batch", env.jobs.jobs)
	}

	if err := env.svc.OnWaveTerminal(ctx, 71); err != nil {
		t.Fatalf("OnWaveTerminal: %v", err)
	}
	env.requireStage(t, version.StageReporting)

	// Pin the stage-entry job kinds: the executing and reporting edges must keep
	// enqueuing execute.batch and report.version exactly (spec §5.4, D21).
	wantedJobs := []string{
		versionapp.StageJobDesignCases,
		versionapp.StageJobExecuteBatch,
		versionapp.StageJobReportVersion,
	}
	if len(env.jobs.jobs) != len(wantedJobs) {
		t.Fatalf("stage-entry jobs = %#v, want kinds %v", env.jobs.jobs, wantedJobs)
	}
	for index, kind := range wantedJobs {
		if env.jobs.jobs[index].Kind != kind {
			t.Fatalf("stage-entry job[%d] = %q, want %q (jobs=%#v)",
				index, env.jobs.jobs[index].Kind, kind, env.jobs.jobs)
		}
	}

	if err := env.svc.OnReportPublished(ctx, stageServiceVersionID); err != nil {
		t.Fatalf("OnReportPublished: %v", err)
	}
	env.requireStage(t, version.StageCompleted)

	// The stage entered timestamp is refreshed on every accepted transition.
	if !env.repo.state.StageEnteredAt.After(testTime) {
		t.Fatalf("stage_entered_at = %v, want later than %v", env.repo.state.StageEnteredAt, testTime)
	}
	if len(env.repo.applied) != 7 {
		t.Fatalf("applied transitions = %d, want 7", len(env.repo.applied))
	}
	for index := 1; index < len(env.repo.applied); index++ {
		if !env.repo.applied[index].StageEnteredAt.After(env.repo.applied[index-1].StageEnteredAt) {
			t.Fatalf("stage_entered_at not refreshed at transition %d: %#v", index, env.repo.applied)
		}
	}

	// The design handoff hook, the version-report ensurer and the version closer
	// each ran exactly once through their production edges.
	if len(env.handoff.versionIDs) != 1 || env.handoff.versionIDs[0] != stageServiceVersionID {
		t.Fatalf("handoff calls = %#v, want one for %d", env.handoff.versionIDs, stageServiceVersionID)
	}
	if len(env.reports.versionIDs) != 1 || env.reports.versionIDs[0] != stageServiceVersionID {
		t.Fatalf("report ensure calls = %#v, want one for %d", env.reports.versionIDs, stageServiceVersionID)
	}
	if len(env.closer.versionIDs) != 1 || env.closer.versionIDs[0] != stageServiceVersionID {
		t.Fatalf("version close calls = %#v, want one for %d", env.closer.versionIDs, stageServiceVersionID)
	}

	// Every transition published the v0.22 event with from/to/reason/actor, and
	// the legacy 0.21 event name still exists for its current consumers.
	dotEvents := stageEventsOfTypeOnChannel(env.publisher, "version.stage.changed", "version:101")
	if len(dotEvents) != 7 {
		t.Fatalf("version.stage.changed events = %d, want 7: %#v", len(dotEvents), dotEvents)
	}
	legacyEvents := stageEventsOfTypeOnChannel(env.publisher, "version.stage_changed", "version:101")
	if len(legacyEvents) != 7 {
		t.Fatalf("version.stage_changed events = %d, want 7", len(legacyEvents))
	}
	last := dotEvents[len(dotEvents)-1]
	if last["from"] != "reporting" || last["to"] != "completed" || last["reason"] != "report_published" {
		t.Fatalf("completion event payload = %#v", last)
	}
	if actor, _ := last["actor"].(string); actor == "" {
		t.Fatalf("completion event has no actor: %#v", last)
	}
}

// TestStageGateRejections pins every R-B4-2 gate: the rejection is a typed gate
// error with reason/action/next, and nothing is written.
func TestStageGateRejections(t *testing.T) {
	testCases := []struct {
		name       string
		from, to   version.Stage
		closeGate  func(*stageGateState)
		wantReason string
	}{
		{
			name: "environment not bound", from: version.StageCreated, to: version.StageClarifying,
			closeGate:  func(g *stageGateState) { g.environmentBound = false },
			wantReason: "environment_not_bound",
		},
		{
			name: "missing requirement", from: version.StageClarifying, to: version.StageRequirementReview,
			closeGate:  func(g *stageGateState) { g.requirementCount = 0 },
			wantReason: "missing_requirement",
		},
		{
			name: "requirements unconfirmed", from: version.StageRequirementReview, to: version.StageDesigning,
			closeGate:  func(g *stageGateState) { g.requirementsConfirmed = false },
			wantReason: "requirements_unconfirmed",
		},
		{
			name: "open gap", from: version.StageRequirementReview, to: version.StageDesigning,
			closeGate:  func(g *stageGateState) { g.openGaps = 1 },
			wantReason: "open_gap",
		},
		{
			name: "no confirmed case draft", from: version.StageDesigning, to: version.StageCaseReview,
			closeGate:  func(g *stageGateState) { g.confirmedCaseDrafts = 0 },
			wantReason: "no_confirmed_case_draft",
		},
		{
			name: "coverage not computed", from: version.StageDesigning, to: version.StageCaseReview,
			closeGate:  func(g *stageGateState) { g.coverageComputed = false },
			wantReason: "coverage_not_computed",
		},
		{
			name: "cases unconfirmed", from: version.StageCaseReview, to: version.StageExecuting,
			closeGate:  func(g *stageGateState) { g.confirmedCases = 0 },
			wantReason: "cases_unconfirmed",
		},
		{
			name: "executor offline", from: version.StageCaseReview, to: version.StageExecuting,
			closeGate:  func(g *stageGateState) { g.executorOnline = false },
			wantReason: "executor_offline",
		},
		{
			name: "account invalid", from: version.StageCaseReview, to: version.StageExecuting,
			closeGate:  func(g *stageGateState) { g.accountsValid = false },
			wantReason: "account_invalid",
		},
		{
			name: "waves still active", from: version.StageExecuting, to: version.StageReporting,
			closeGate:  func(g *stageGateState) { g.wavesTerminal = false },
			wantReason: "waves_active",
		},
		{
			name: "report not published", from: version.StageReporting, to: version.StageCompleted,
			closeGate:  func(g *stageGateState) { g.reportPublished = false },
			wantReason: "report_not_published",
		},
		{
			name: "open blocking issue", from: version.StageReporting, to: version.StageCompleted,
			closeGate:  func(g *stageGateState) { g.openBlockingIssues = 1 },
			wantReason: "open_blocking_issue",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newStageServiceEnv(t, testCase.from)
			testCase.closeGate(env.gates)

			err := env.svc.Request(stageUserContext(42), stageServiceVersionID, testCase.to)
			var gateErr *versionapp.StageGateError
			if !errors.As(err, &gateErr) {
				t.Fatalf("error = %v, want *StageGateError", err)
			}
			if gateErr.Reason != testCase.wantReason {
				t.Fatalf("gate reason = %q, want %q (gate=%q)", gateErr.Reason, testCase.wantReason, gateErr.Gate)
			}
			if gateErr.Action == "" || gateErr.Next == "" {
				t.Fatalf("gate error lacks action/next: %+v", gateErr)
			}
			if len(env.repo.applied) != 0 {
				t.Fatalf("rejected gate wrote stage: %#v", env.repo.applied)
			}
			if len(env.publisher.events) != 0 {
				t.Fatalf("rejected gate published events: %#v", env.publisher.events)
			}
		})
	}
}

// TestClarifyingPendingQuestionDoesNotBlock pins C-2: a pending question is not
// part of the stage gate surface at all, so clarifying -> requirement_review
// advances with pending questions waiting on the user.
func TestClarifyingPendingQuestionDoesNotBlock(t *testing.T) {
	env := newStageServiceEnv(t, version.StageClarifying)
	// Simulate the question state: only the requirement count gate exists and it
	// is satisfied; no gate method can express "pending question".
	env.gates.requirementCount = 1
	env.gates.openGaps = 3 // open questions do not block either; gaps are accepted-owned

	if err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageRequirementReview); err != nil {
		t.Fatalf("Request with pending questions: %v", err)
	}
	env.requireStage(t, version.StageRequirementReview)
}

// TestBlockedRoundTrip pins R-B4-4: blocked requires the structured
// reason/action/next, stores JSON in version.blocker, records from/to/reason and
// can only be unblocked after the clearance gate passes.
func TestBlockedRoundTrip(t *testing.T) {
	env := newStageServiceEnv(t, version.StageDesigning)
	blocker := versionapp.Blocker{
		Kind:   "executor",
		Reason: "executor offline",
		Action: "start the executor",
		Next:   "retry the wave",
	}
	if err := env.svc.Block(stageUserContext(42), stageServiceVersionID, blocker); err != nil {
		t.Fatalf("Block: %v", err)
	}
	env.requireStage(t, version.StageBlocked)
	if env.repo.state.SuspendedFrom == nil || *env.repo.state.SuspendedFrom != version.StageDesigning {
		t.Fatalf("suspended_from = %#v, want designing", env.repo.state.SuspendedFrom)
	}
	var stored versionapp.Blocker
	if err := json.Unmarshal([]byte(env.repo.state.Blocker), &stored); err != nil {
		t.Fatalf("version.blocker is not JSON: %q (%v)", env.repo.state.Blocker, err)
	}
	if stored != blocker {
		t.Fatalf("stored blocker = %+v, want %+v", stored, blocker)
	}
	blockedEvents := stageEventsOfTypeOnChannel(env.publisher, "version.stage.changed", "version:101")
	if len(blockedEvents) != 1 || blockedEvents[0]["to"] != "blocked" ||
		blockedEvents[0]["from"] != "designing" || blockedEvents[0]["reason"] != "blocked" {
		t.Fatalf("block event = %#v", blockedEvents)
	}
	// The event payload carries the same structured JSON that is persisted in
	// version.blocker, so consumers can render kind/reason/action/next.
	payloadBlocker, ok := blockedEvents[0]["blocker"].(string)
	if !ok {
		t.Fatalf("block event has no blocker payload: %#v", blockedEvents[0])
	}
	if payloadBlocker != env.repo.state.Blocker {
		t.Fatalf("event blocker = %q, want persisted %q", payloadBlocker, env.repo.state.Blocker)
	}
	var eventBlocker versionapp.Blocker
	if err := json.Unmarshal([]byte(payloadBlocker), &eventBlocker); err != nil {
		t.Fatalf("event blocker is not JSON: %q (%v)", payloadBlocker, err)
	}
	if eventBlocker != blocker {
		t.Fatalf("event blocker = %+v, want %+v", eventBlocker, blocker)
	}

	// A blocker that is not cleared cannot be resumed.
	env.gates.blockerCleared = false
	err := env.svc.Unblock(stageUserContext(42), stageServiceVersionID)
	var gateErr *versionapp.StageGateError
	if !errors.As(err, &gateErr) || gateErr.Reason != "blocker_not_cleared" {
		t.Fatalf("Unblock not cleared error = %v, want blocker_not_cleared gate", err)
	}
	env.requireStage(t, version.StageBlocked)

	env.gates.blockerCleared = true
	if err := env.svc.Unblock(stageUserContext(42), stageServiceVersionID); err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	env.requireStage(t, version.StageDesigning)
	if env.repo.state.Blocker != "" || env.repo.state.SuspendedFrom != nil {
		t.Fatalf("unblock did not clear blocker/suspended_from: %+v", env.repo.state)
	}
	events := stageEventsOfTypeOnChannel(env.publisher, "version.stage.changed", "version:101")
	last := events[len(events)-1]
	if last["from"] != "blocked" || last["to"] != "designing" || last["reason"] != "unblocked" {
		t.Fatalf("unblock event = %#v", last)
	}
}

// TestBlockedRequiresReason is the brief's mandatory case: an empty blocker is
// rejected before any stage write.
func TestBlockedRequiresReason(t *testing.T) {
	testCases := []struct {
		name    string
		blocker versionapp.Blocker
	}{
		{name: "empty", blocker: versionapp.Blocker{}},
		{name: "missing reason", blocker: versionapp.Blocker{Kind: "executor", Action: "start", Next: "retry"}},
		{name: "missing action", blocker: versionapp.Blocker{Kind: "executor", Reason: "offline", Next: "retry"}},
		{name: "missing next", blocker: versionapp.Blocker{Kind: "executor", Reason: "offline", Action: "start"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newStageServiceEnv(t, version.StageExecuting)
			if err := env.svc.Block(stageUserContext(42), stageServiceVersionID, testCase.blocker); err == nil {
				t.Fatal("invalid blocker accepted")
			}
			if len(env.repo.applied) != 0 {
				t.Fatalf("invalid blocker wrote stage: %#v", env.repo.applied)
			}
		})
	}
}

// TestPauseResumeMinimal pins R-B4-5: pausing asks the wave controller for a
// graceful pause and records the previous stage; resuming restores it and asks
// the controller to continue.
func TestPauseResumeMinimal(t *testing.T) {
	env := newStageServiceEnv(t, version.StageExecuting)
	if err := env.svc.Pause(stageUserContext(42), stageServiceVersionID, "user paused"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	env.requireStage(t, version.StagePaused)
	if len(env.waveControl.paused) != 1 || env.waveControl.paused[0] != stageServiceVersionID {
		t.Fatalf("paused waves = %#v", env.waveControl.paused)
	}
	if env.waveControl.pauseReasons[0] != "user paused" {
		t.Fatalf("pause reason = %q", env.waveControl.pauseReasons[0])
	}
	if env.repo.state.SuspendedFrom == nil || *env.repo.state.SuspendedFrom != version.StageExecuting {
		t.Fatalf("suspended_from = %#v, want executing", env.repo.state.SuspendedFrom)
	}

	if err := env.svc.Resume(stageUserContext(42), stageServiceVersionID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	env.requireStage(t, version.StageExecuting)
	if len(env.waveControl.resumed) != 1 || env.waveControl.resumed[0] != stageServiceVersionID {
		t.Fatalf("resumed waves = %#v", env.waveControl.resumed)
	}
}

// TestStageCallbacksAreIdempotent pins R-B4-3's duplicate-delivery safety: the
// second production callback changes nothing and publishes no extra event.
func TestStageCallbacksAreIdempotent(t *testing.T) {
	env := newStageServiceEnv(t, version.StageExecuting)
	ctx := requestContext()
	if err := env.svc.OnWaveTerminal(ctx, 71); err != nil {
		t.Fatalf("first OnWaveTerminal: %v", err)
	}
	env.requireStage(t, version.StageReporting)
	appliedAfterWave := len(env.repo.applied)
	eventsAfterWave := len(env.publisher.events)

	if err := env.svc.OnWaveTerminal(ctx, 71); err != nil {
		t.Fatalf("second OnWaveTerminal: %v", err)
	}
	if len(env.repo.applied) != appliedAfterWave || len(env.publisher.events) != eventsAfterWave {
		t.Fatalf("duplicate wave callback wrote stage/events: applied=%d events=%d",
			len(env.repo.applied), len(env.publisher.events))
	}

	if err := env.svc.OnReportPublished(ctx, stageServiceVersionID); err != nil {
		t.Fatalf("first OnReportPublished: %v", err)
	}
	env.requireStage(t, version.StageCompleted)
	appliedAfterReport := len(env.repo.applied)
	eventsAfterReport := len(env.publisher.events)
	if err := env.svc.OnReportPublished(ctx, stageServiceVersionID); err != nil {
		t.Fatalf("second OnReportPublished: %v", err)
	}
	if len(env.repo.applied) != appliedAfterReport || len(env.publisher.events) != eventsAfterReport {
		t.Fatalf("duplicate report callback wrote stage/events: applied=%d events=%d",
			len(env.repo.applied), len(env.publisher.events))
	}
	if len(env.closer.versionIDs) != 1 {
		t.Fatalf("version closed %d times, want once", len(env.closer.versionIDs))
	}
}

// TestStageInvalidTransitionIsTyped pins the typed invalid-transition error with
// details and the fail-closed behavior before any write.
func TestStageInvalidTransitionIsTyped(t *testing.T) {
	env := newStageServiceEnv(t, version.StageCreated)
	err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageExecuting)
	var transitionErr *versionapp.StageTransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatalf("error = %v, want *StageTransitionError", err)
	}
	if transitionErr.From != version.StageCreated || transitionErr.To != version.StageExecuting {
		t.Fatalf("transition error = %+v", transitionErr)
	}
	if transitionErr.Action == "" {
		t.Fatalf("transition error lacks an action: %+v", transitionErr)
	}
	if len(env.repo.applied) != 0 || len(env.publisher.events) != 0 {
		t.Fatalf("invalid transition wrote state/events")
	}

	// Suspension targets are never reachable through Request: Block/Pause own
	// those edges.
	if err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageBlocked); err == nil {
		t.Fatal("Request(blocked) must be rejected")
	}

	// Idempotent same-stage requests are a no-op.
	for _, stage := range []version.Stage{version.StageCreated} {
		if err := env.svc.Request(stageUserContext(42), stageServiceVersionID, stage); err != nil {
			t.Fatalf("same-stage Request(%s): %v", stage, err)
		}
	}
	if len(env.repo.applied) != 0 {
		t.Fatalf("same-stage request wrote stage: %#v", env.repo.applied)
	}
}

// TestStageGateErrorMapsToAPIError keeps the future HTTP wiring trivial: every
// gate rejection maps to a 409 with stable code and details.
func TestStageGateErrorMapsToAPIError(t *testing.T) {
	env := newStageServiceEnv(t, version.StageClarifying)
	env.gates.requirementCount = 0
	err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageRequirementReview)
	var gateErr *versionapp.StageGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("error = %v, want *StageGateError", err)
	}
	apiErr := gateErr.APIError()
	if apiErr.Status != 409 || apiErr.Code != "stage_gate_failed" {
		t.Fatalf("api error = %d/%s", apiErr.Status, apiErr.Code)
	}
	for _, key := range []string{"gate", "reason", "action", "next"} {
		if apiErr.Details[key] == nil || apiErr.Details[key] == "" {
			t.Fatalf("api error details[%q] missing: %#v", key, apiErr.Details)
		}
	}
}

// TestWaveTerminalCallbackNoOpsOutsideExecuting documents the production
// callback contract: a late/early wave callback for a version that is not in
// executing is harmless.
func TestWaveTerminalCallbackNoOpsOutsideExecuting(t *testing.T) {
	env := newStageServiceEnv(t, version.StageDesigning)
	if err := env.svc.OnWaveTerminal(requestContext(), 71); err != nil {
		t.Fatalf("OnWaveTerminal outside executing: %v", err)
	}
	if len(env.repo.applied) != 0 {
		t.Fatalf("wave callback moved a designing version: %#v", env.repo.applied)
	}

	// A non-terminal wave is a typed gate rejection when the version executes.
	env = newStageServiceEnv(t, version.StageExecuting)
	env.waves.waves[72] = versionapp.StageWave{ID: 72, VersionID: stageServiceVersionID, Status: "running"}
	err := env.svc.OnWaveTerminal(requestContext(), 72)
	var gateErr *versionapp.StageGateError
	if !errors.As(err, &gateErr) || gateErr.Reason != "wave_not_terminal" {
		t.Fatalf("non-terminal wave error = %v", err)
	}
	if len(env.repo.applied) != 0 {
		t.Fatalf("non-terminal wave callback wrote stage: %#v", env.repo.applied)
	}
}

// TestHandoffFailureDoesNotBlockDesigning pins the best-effort handoff hook:
// Task 27's Generate failure is logged/retried by its own reconciler and never
// blocks the stage transition.
func TestHandoffFailureDoesNotBlockDesigning(t *testing.T) {
	env := newStageServiceEnv(t, version.StageRequirementReview)
	env.handoff.err = errors.New("handoff store unavailable")
	if err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageDesigning); err != nil {
		t.Fatalf("Request with failing handoff hook: %v", err)
	}
	env.requireStage(t, version.StageDesigning)
}

// TestStageReportsFailureBlocksReporting pins the report-generation half of the
// executing -> reporting gate: without an ensured version report the callback
// rejects with a typed gate error.
func TestStageReportsFailureBlocksReporting(t *testing.T) {
	env := newStageServiceEnv(t, version.StageExecuting)
	env.reports.err = errors.New("report generation gate is closed")
	err := env.svc.OnWaveTerminal(requestContext(), 71)
	var gateErr *versionapp.StageGateError
	if !errors.As(err, &gateErr) || gateErr.Reason != "report_not_generated" {
		t.Fatalf("report gate error = %v", err)
	}
	if len(env.repo.applied) != 0 {
		t.Fatalf("report gate failure wrote stage: %#v", env.repo.applied)
	}
}

// TestStageGateErrorsExposePlatformError protects the errors.As contract used by
// the future handler wiring: an unconfigured gate also fails closed as typed.
func TestUnconfiguredGateFailsClosed(t *testing.T) {
	repo := newFakeStageRepo(version.StageState{Stage: version.StageClarifying, StageEnteredAt: testTime})
	svc := versionapp.NewStageService(versionapp.StageServiceDeps{Repo: repo})
	err := svc.Request(stageUserContext(42), stageServiceVersionID, version.StageRequirementReview)
	var gateErr *versionapp.StageGateError
	if !errors.As(err, &gateErr) || gateErr.Reason != "gate_not_configured" {
		t.Fatalf("unconfigured gate error = %v", err)
	}
	if len(repo.applied) != 0 {
		t.Fatalf("unconfigured gate wrote stage: %#v", repo.applied)
	}
}

// TestParseBlockerLegacyTolerance keeps a pre-v0.22 plain-text blocker
// unmarshalable: ParseBlocker never fails, it falls back to a manual blocker so
// an operator can still clear a legacy row.
func TestParseBlockerLegacyTolerance(t *testing.T) {
	legacy := versionapp.ParseBlocker("database unreachable")
	if legacy.Reason != "database unreachable" || legacy.Action == "" || legacy.Next == "" {
		t.Fatalf("legacy blocker = %+v", legacy)
	}
	structured := versionapp.Blocker{Kind: "database", Reason: "db down", Action: "restore db", Next: "retry"}
	raw, err := json.Marshal(structured)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := versionapp.ParseBlocker(string(raw)); got != structured {
		t.Fatalf("parsed blocker = %+v, want %+v", got, structured)
	}
}

// TestStageEvidenceChain emits the W0 task-25 acceptance evidence: the complete
// stage event chain (from/to/reason/actor) and the blocked/unblocked round trip.
// Run it with -v and capture the output into
// docs/version-V0.22/acceptance/evidence/w0/task-25.txt.
func TestStageEvidenceChain(t *testing.T) {
	env := newStageServiceEnv(t, version.StageCreated)
	ctx := stageUserContext(7)
	for _, target := range []version.Stage{
		version.StageClarifying,
		version.StageRequirementReview,
		version.StageDesigning,
		version.StageCaseReview,
		version.StageExecuting,
	} {
		if err := env.svc.Request(ctx, stageServiceVersionID, target); err != nil {
			t.Fatalf("Request(%s): %v", target, err)
		}
	}
	if err := env.svc.OnWaveTerminal(ctx, 71); err != nil {
		t.Fatalf("OnWaveTerminal: %v", err)
	}
	if err := env.svc.OnReportPublished(ctx, stageServiceVersionID); err != nil {
		t.Fatalf("OnReportPublished: %v", err)
	}
	t.Logf("stage chain final stage: %s", env.repo.state.Stage)
	for _, payload := range stageEventsOfTypeOnChannel(env.publisher, "version.stage.changed", "version:101") {
		encoded, _ := json.Marshal(payload)
		t.Logf("stage event: %s", encoded)
	}

	blocked := newStageServiceEnv(t, version.StageDesigning)
	blocker := versionapp.Blocker{Kind: "executor", Reason: "executor offline",
		Action: "start the executor", Next: "retry the wave"}
	if err := blocked.svc.Block(ctx, stageServiceVersionID, blocker); err != nil {
		t.Fatalf("Block: %v", err)
	}
	stored, _ := json.Marshal(versionapp.ParseBlocker(blocked.repo.state.Blocker))
	t.Logf("blocked: stage=%s suspended_from=%v blocker=%s", blocked.repo.state.Stage,
		stageOrNil(blocked.repo.state.SuspendedFrom), stored)
	blocked.gates.blockerCleared = false
	if err := blocked.svc.Unblock(ctx, stageServiceVersionID); err != nil {
		t.Logf("unblock rejected while blocker remains: %v", err)
	}
	blocked.gates.blockerCleared = true
	if err := blocked.svc.Unblock(ctx, stageServiceVersionID); err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	t.Logf("unblocked: stage=%s suspended_from=%v blocker=%q", blocked.repo.state.Stage,
		stageOrNil(blocked.repo.state.SuspendedFrom), blocked.repo.state.Blocker)
	for _, payload := range stageEventsOfTypeOnChannel(blocked.publisher, "version.stage.changed", "version:101") {
		encoded, _ := json.Marshal(payload)
		t.Logf("blocked event: %s", encoded)
	}
}

func stageOrNil(stage *version.Stage) string {
	if stage == nil {
		return "<nil>"
	}
	return string(*stage)
}

// ---------------------------------------------------------------------------
// Fix round 1 (review findings)
// ---------------------------------------------------------------------------

// TestOnReportPublishedSucceedsWhenVersionAlreadyClosed pins the closer contract
// from review fix 1: CloseVersion means "ensure the version is closed", so an
// already-completed version is gate success. Before the fix the production
// VersionCloseService returned a generic 409 for a completed row, stranding the
// stage in reporting forever.
func TestOnReportPublishedSucceedsWhenVersionAlreadyClosed(t *testing.T) {
	env := newStageServiceEnv(t, version.StageReporting)
	// Wrapped on purpose: implementations may wrap the sentinel with context.
	env.closer.err = fmt.Errorf("version 101 already completed: %w", versionapp.ErrVersionAlreadyClosed)

	if err := env.svc.OnReportPublished(requestContext(), stageServiceVersionID); err != nil {
		t.Fatalf("OnReportPublished with an already-closed version: %v", err)
	}
	env.requireStage(t, version.StageCompleted)
	if len(env.closer.versionIDs) != 1 || env.closer.versionIDs[0] != stageServiceVersionID {
		t.Fatalf("closer calls = %#v, want one for %d", env.closer.versionIDs, stageServiceVersionID)
	}
}

// TestOnReportPublishedRetriesAfterCloseWonButStageCASLost covers the second
// half of fix 1: the version close commits, then the stage compare-and-swap loses
// a race. The first callback reports the conflict; a retry must see the
// already-closed version as success and finish the stage write.
func TestOnReportPublishedRetriesAfterCloseWonButStageCASLost(t *testing.T) {
	env := newStageServiceEnv(t, version.StageReporting)
	env.closer.alreadyClosed = map[int64]bool{}

	env.repo.applyErr = version.ErrStageConflict
	if err := env.svc.OnReportPublished(requestContext(), stageServiceVersionID); err == nil {
		t.Fatal("stage CAS loss was not surfaced")
	}
	env.requireStage(t, version.StageReporting)
	if len(env.closer.versionIDs) != 1 {
		t.Fatalf("close calls after CAS loss = %#v, want one", env.closer.versionIDs)
	}

	// The version is closed now; the retry must treat that as success.
	env.repo.applyErr = nil
	if err := env.svc.OnReportPublished(requestContext(), stageServiceVersionID); err != nil {
		t.Fatalf("retry after a successful close: %v", err)
	}
	env.requireStage(t, version.StageCompleted)
	if len(env.closer.versionIDs) != 2 {
		t.Fatalf("close calls after retry = %#v, want two", env.closer.versionIDs)
	}
}

// TestStageReconcileRetriesTransientReportFailure covers review fix 2: a
// transient EnsureVersionReport failure after the wave already finalized must be
// retryable without a manual stage API call. Reconcile is the production
// reconciler hook; the first pass fails the report gate, the second advances.
func TestStageReconcileRetriesTransientReportFailure(t *testing.T) {
	env := newStageServiceEnv(t, version.StageExecuting)
	env.reports.err = errors.New("report store unavailable")

	err := env.svc.Reconcile(requestContext(), stageServiceVersionID)
	var gateErr *versionapp.StageGateError
	if !errors.As(err, &gateErr) || gateErr.Reason != "report_not_generated" {
		t.Fatalf("first Reconcile error = %v, want report_not_generated gate", err)
	}
	env.requireStage(t, version.StageExecuting)
	if len(env.repo.applied) != 0 {
		t.Fatalf("failed reconcile wrote stage: %#v", env.repo.applied)
	}

	env.reports.err = nil
	if err := env.svc.Reconcile(requestContext(), stageServiceVersionID); err != nil {
		t.Fatalf("retry Reconcile: %v", err)
	}
	env.requireStage(t, version.StageReporting)
	if len(env.reports.versionIDs) != 2 {
		t.Fatalf("report ensure calls = %#v, want two", env.reports.versionIDs)
	}
}

// TestStageReconcileNoOpsWhenNotReady documents the reconciler contract: a
// version with an active wave, or one outside executing/reporting, is a no-op.
func TestStageReconcileNoOpsWhenNotReady(t *testing.T) {
	active := newStageServiceEnv(t, version.StageExecuting)
	active.gates.wavesTerminal = false
	if err := active.svc.Reconcile(requestContext(), stageServiceVersionID); err != nil {
		t.Fatalf("Reconcile with active waves: %v", err)
	}
	active.requireStage(t, version.StageExecuting)
	if len(active.reports.versionIDs) != 0 {
		t.Fatalf("active-wave reconcile ensured a report: %#v", active.reports.versionIDs)
	}

	designing := newStageServiceEnv(t, version.StageDesigning)
	if err := designing.svc.Reconcile(requestContext(), stageServiceVersionID); err != nil {
		t.Fatalf("Reconcile outside executing: %v", err)
	}
	designing.requireStage(t, version.StageDesigning)
	if len(designing.repo.applied) != 0 {
		t.Fatalf("reconcile moved a designing version: %#v", designing.repo.applied)
	}
}

// TestStageReconcileCompletesReportingVersion heals the publish-raced-the-edge
// case: the report is published but the publishing callback landed while the
// version was still executing (a no-op), so no later publish event will arrive.
func TestStageReconcileCompletesReportingVersion(t *testing.T) {
	env := newStageServiceEnv(t, version.StageReporting)
	env.gates.reportPublished = true
	if err := env.svc.Reconcile(requestContext(), stageServiceVersionID); err != nil {
		t.Fatalf("Reconcile reporting version: %v", err)
	}
	env.requireStage(t, version.StageCompleted)

	unpublished := newStageServiceEnv(t, version.StageReporting)
	unpublished.gates.reportPublished = false
	if err := unpublished.svc.Reconcile(requestContext(), stageServiceVersionID); err != nil {
		t.Fatalf("Reconcile unpublished reporting version: %v", err)
	}
	unpublished.requireStage(t, version.StageReporting)
	if len(unpublished.repo.applied) != 0 {
		t.Fatalf("unpublished version advanced: %#v", unpublished.repo.applied)
	}
}

// TestRequestRejectsBackAndReopenEdges pins the review fix that Request only
// advances the deterministic forward workflow. Gate rejection back edges and
// the completed-reopen edge need a dedicated, audited entry point (Task 36/37).
func TestRequestRejectsBackAndReopenEdges(t *testing.T) {
	testCases := []struct {
		name     string
		from, to version.Stage
	}{
		{name: "requirement gate reject", from: version.StageRequirementReview, to: version.StageClarifying},
		{name: "case gate reject", from: version.StageCaseReview, to: version.StageDesigning},
		{name: "partial execution back edge", from: version.StageExecuting, to: version.StageCaseReview},
		{name: "completed reopen", from: version.StageCompleted, to: version.StageClarifying},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newStageServiceEnv(t, testCase.from)
			err := env.svc.Request(stageUserContext(42), stageServiceVersionID, testCase.to)
			var transitionErr *versionapp.StageTransitionError
			if !errors.As(err, &transitionErr) {
				t.Fatalf("Request(%s -> %s) error = %v, want *StageTransitionError",
					testCase.from, testCase.to, err)
			}
			if transitionErr.From != testCase.from || transitionErr.To != testCase.to {
				t.Fatalf("transition error = %+v", transitionErr)
			}
			if len(env.repo.applied) != 0 || len(env.publisher.events) != 0 {
				t.Fatalf("back/reopen edge wrote state: applied=%#v events=%d",
					env.repo.applied, len(env.publisher.events))
			}
		})
	}
}

// TestPauseRejectsInvalidReasonBeforePausingWaves pins the review fix that the
// pause reason is validated before the execution side effect: an over-long
// reason must not pause the waves and then fail the stage write.
func TestPauseRejectsInvalidReasonBeforePausingWaves(t *testing.T) {
	env := newStageServiceEnv(t, version.StageExecuting)
	err := env.svc.Pause(stageUserContext(42), stageServiceVersionID, strings.Repeat("x", 4001))
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("Pause error = %v, want a 400 invalid_argument", err)
	}
	if len(env.waveControl.paused) != 0 || env.waveControl.resumeCalls != 0 {
		t.Fatalf("invalid pause touched the waves: paused=%#v resumed=%#v",
			env.waveControl.paused, env.waveControl.resumeCalls)
	}
	if len(env.repo.applied) != 0 {
		t.Fatalf("invalid pause wrote the stage: %#v", env.repo.applied)
	}
	env.requireStage(t, version.StageExecuting)
}

// TestResumeIsBestEffortWhenWaveResumeFails pins the resume ordering fix: the
// stage resume is the truth. A wave-resume failure is reported to logs and
// retried by the execution reconciler; it must not make the caller believe the
// stage is still paused.
func TestResumeIsBestEffortWhenWaveResumeFails(t *testing.T) {
	env := newStageServiceEnv(t, version.StageExecuting)
	if err := env.svc.Pause(stageUserContext(42), stageServiceVersionID, "maintenance"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	env.waveControl.err = errors.New("execution pause service unavailable")

	if err := env.svc.Resume(stageUserContext(42), stageServiceVersionID); err != nil {
		t.Fatalf("Resume with failing wave continuation: %v", err)
	}
	env.requireStage(t, version.StageExecuting)
	if env.waveControl.resumeCalls != 1 {
		t.Fatalf("wave resume attempts = %d, want 1", env.waveControl.resumeCalls)
	}
	if len(env.repo.applied) != 2 {
		t.Fatalf("stage writes = %d, want pause + resume", len(env.repo.applied))
	}
}

// TestStageTransitionErrorMapsToAPIError keeps the Task 37 HTTP wiring trivial:
// invalid transitions map to 409 invalid_stage_transition with from/to/action.
func TestStageTransitionErrorMapsToAPIError(t *testing.T) {
	err := &versionapp.StageTransitionError{
		From:   version.StageRequirementReview,
		To:     version.StageClarifying,
		Action: "use the gate reject entry point",
	}
	apiErr := err.APIError()
	if apiErr.Status != 409 || apiErr.Code != "invalid_stage_transition" {
		t.Fatalf("api error = %d/%s, want 409/invalid_stage_transition", apiErr.Status, apiErr.Code)
	}
	if apiErr.Details["from"] != "requirement_review" || apiErr.Details["to"] != "clarifying" {
		t.Fatalf("api details = %#v", apiErr.Details)
	}
	if apiErr.Details["action"] != "use the gate reject entry point" {
		t.Fatalf("api details action = %#v", apiErr.Details["action"])
	}
}

// TestStageServiceUsesAtomicCoreCAS pins R-B4-1 at the write boundary: every
// StageService edge must go through the same source-stage compare-and-swap as
// the legacy machine, and a lost CAS is a typed 409 with no event/job leak.
func TestStageServiceUsesAtomicCoreCAS(t *testing.T) {
	env := newStageServiceEnv(t, version.StageCreated)
	if err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageClarifying); err != nil {
		t.Fatalf("Request(clarifying): %v", err)
	}
	if len(env.repo.expectedStages) != 1 || env.repo.expectedStages[0] != version.StageCreated {
		t.Fatalf("CAS source stages = %#v, want [created]", env.repo.expectedStages)
	}
	eventsAfterFirst := len(env.publisher.events)
	jobsAfterFirst := len(env.jobs.jobs)

	env.repo.applyErr = version.ErrStageConflict
	err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageRequirementReview)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("CAS conflict error = %v, want a 409 platform error", err)
	}
	env.requireStage(t, version.StageClarifying)
	if len(env.publisher.events) != eventsAfterFirst || len(env.jobs.jobs) != jobsAfterFirst {
		t.Fatalf("lost CAS leaked side effects: events=%d->%d jobs=%d->%d",
			eventsAfterFirst, len(env.publisher.events), jobsAfterFirst, len(env.jobs.jobs))
	}
}
